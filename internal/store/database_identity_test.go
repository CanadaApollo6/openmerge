package store

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

func seededIdentityStore(t *testing.T, raw string) *Store {
	t.Helper()
	s := openTestStore(t, raw)
	appendEvents(t, s, ensureQueue(t, s, ""), queue.PauseRequested{Reason: "operator", Detail: "identity genesis"})
	return s
}

func identityOf(t *testing.T, s *Store) string {
	t.Helper()
	identity, err := s.DatabaseIdentity(t.Context())
	must(t, err)
	if !strings.HasPrefix(identity, "openmerge-db-v2:sha256:") || len(identity) != len("openmerge-db-v2:sha256:")+64 {
		t.Fatal(identity)
	}
	return identity
}

func TestDatabaseIdentityEffectiveSchemaAndReconnect(t *testing.T) {
	rawA, rawB := databaseURL(t), databaseURL(t)
	a, b := seededIdentityStore(t, rawA), seededIdentityStore(t, rawB)
	idA, idB := identityOf(t, a), identityOf(t, b)
	if idA == idB {
		t.Fatal("different effective schemas collided")
	}
	for _, raw := range []string{rawA, rawB} {
		u, err := url.Parse(raw)
		must(t, err)
		params := u.Query()
		schema := params.Get("search_path")
		params.Del("search_path")
		params.Set("options", "-c search_path="+schema)
		u.RawQuery = strings.ReplaceAll(params.Encode(), "+", "%20")
		got, err := DatabaseIdentity(t.Context(), u.String())
		must(t, err)
		want := idA
		if raw == rawB {
			want = idB
		}
		if got != want {
			t.Fatalf("options identity %q != %q", got, want)
		}
	}
	// Reset forces all subsequent reads onto new pool connections.
	a.pool.Reset()
	if got := identityOf(t, a); got != idA {
		t.Fatal("reconnect changed identity")
	}
	appendEvents(t, a, ensureQueue(t, a, ""), queue.PauseRequested{Reason: "operator", Detail: "later event"})
	if got := identityOf(t, a); got != idA {
		t.Fatal("append changed genesis binding")
	}
	// Same catalog identity, unrelated restored/replaced genesis.
	_, err := a.pool.Exec(t.Context(), `UPDATE events SET recorded_at=recorded_at+interval '1 second' WHERE id=(SELECT min(id) FROM events)`)
	must(t, err)
	if got := identityOf(t, a); got == idA {
		t.Fatal("changed genesis collided")
	}
	u, err := url.Parse(rawA)
	must(t, err)
	v, err := url.Parse(rawB)
	must(t, err)
	params := u.Query()
	params.Set("search_path", params.Get("search_path")+","+v.Query().Get("search_path"))
	u.RawQuery = strings.ReplaceAll(params.Encode(), "+", "%20")
	if _, err := DatabaseIdentity(t.Context(), u.String()); err == nil {
		t.Fatal("shadowed schemas accepted")
	}
}

func TestDatabaseIdentityRoleDefaultSchema(t *testing.T) {
	raw := databaseURL(t)
	s := seededIdentityStore(t, raw)
	want := identityOf(t, s)
	u, err := url.Parse(raw)
	must(t, err)
	schema := u.Query().Get("search_path")
	role := schema + "_role"
	ident := pgx.Identifier{role}.Sanitize()
	_, err = s.pool.Exec(t.Context(), `CREATE ROLE `+ident+` LOGIN PASSWORD 'identity-test-only'`)
	must(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := s.pool.Exec(ctx, `DROP OWNED BY `+ident)
		if err != nil {
			t.Error(err)
		}
		_, err = s.pool.Exec(ctx, `DROP ROLE `+ident)
		if err != nil {
			t.Error(err)
		}
	})
	var db string
	must(t, s.pool.QueryRow(t.Context(), `SELECT current_database()`).Scan(&db))
	for _, sql := range []string{
		`GRANT USAGE ON SCHEMA ` + pgx.Identifier{schema}.Sanitize() + ` TO ` + ident,
		`GRANT SELECT ON ALL TABLES IN SCHEMA ` + pgx.Identifier{schema}.Sanitize() + ` TO ` + ident,
		`GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO ` + ident,
		`ALTER ROLE ` + ident + ` IN DATABASE ` + pgx.Identifier{db}.Sanitize() + ` SET search_path TO ` + pgx.Identifier{schema}.Sanitize(),
	} {
		_, err := s.pool.Exec(t.Context(), sql)
		must(t, err)
	}
	u.User = url.UserPassword(role, "identity-test-only")
	params := u.Query()
	params.Del("search_path")
	u.RawQuery = strings.ReplaceAll(params.Encode(), "+", "%20")
	got, err := DatabaseIdentity(t.Context(), u.String())
	must(t, err)
	if got != want {
		t.Fatal("role default did not resolve effective schema")
	}
	otherRaw := databaseURL(t)
	other := seededIdentityStore(t, otherRaw)
	otherURL, err := url.Parse(otherRaw)
	must(t, err)
	otherSchema := pgx.Identifier{otherURL.Query().Get("search_path")}.Sanitize()
	for _, sql := range []string{
		`GRANT USAGE ON SCHEMA ` + otherSchema + ` TO ` + ident,
		`GRANT SELECT ON ALL TABLES IN SCHEMA ` + otherSchema + ` TO ` + ident,
		`ALTER ROLE ` + ident + ` IN DATABASE ` + pgx.Identifier{db}.Sanitize() + ` SET search_path TO ` + otherSchema,
	} {
		_, err := s.pool.Exec(t.Context(), sql)
		must(t, err)
	}
	got, err = DatabaseIdentity(t.Context(), u.String())
	must(t, err)
	if got == want || got != identityOf(t, other) {
		t.Fatal("changed role default schema collided")
	}

}

func TestDatabaseIdentityDatabaseIncarnation(t *testing.T) {
	raw := databaseURL(t)
	original := seededIdentityStore(t, raw)
	u, err := url.Parse(raw)
	must(t, err)
	name := u.Query().Get("search_path") + "_db"
	ident := pgx.Identifier{name}.Sanitize()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := original.pool.Exec(ctx, `DROP DATABASE IF EXISTS `+ident)
		if err != nil {
			t.Error(err)
		}
	})
	u.Path = "/" + name
	params := u.Query()
	params.Set("search_path", "public")
	u.RawQuery = strings.ReplaceAll(params.Encode(), "+", "%20")
	role := name + "_role"
	roleIdent := pgx.Identifier{role}.Sanitize()
	_, err = original.pool.Exec(t.Context(), `CREATE ROLE `+roleIdent+` LOGIN PASSWORD 'identity-test-only'`)
	must(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := original.pool.Exec(ctx, `DROP ROLE `+roleIdent)
		if err != nil {
			t.Error(err)
		}
	})
	var previous string
	for range 2 {
		_, err := original.pool.Exec(t.Context(), `CREATE DATABASE `+ident)
		must(t, err)
		s := seededIdentityStore(t, u.String())
		// A restored journal can preserve genesis exactly. Hold it constant so
		// same-name recreation must be distinguished by the database OID.
		_, err = s.pool.Exec(t.Context(), `UPDATE events SET recorded_at='2000-01-01T00:00:00Z'`)
		must(t, err)
		got := identityOf(t, s)
		if got == identityOf(t, original) || got == previous {
			t.Fatal("database incarnation collided")
		}
		previous = got
		// Function ACL is database-local: alter only this scratch database.
		_, err = s.pool.Exec(t.Context(), `REVOKE EXECUTE ON FUNCTION pg_catalog.pg_control_system() FROM PUBLIC`)
		must(t, err)
		restricted := *u
		restricted.User = url.UserPassword(role, "identity-test-only")
		if _, err := DatabaseIdentity(t.Context(), restricted.String()); err == nil || !strings.Contains(err.Error(), "requires EXECUTE") {
			t.Fatalf("privilege failure=%v", err)
		}
		s.Close()
		_, err = original.pool.Exec(t.Context(), `DROP DATABASE `+ident)
		must(t, err)
	}
}

func TestDatabaseIdentityEmptyJournalFailsClosed(t *testing.T) {
	s := openTestStore(t, databaseURL(t))
	if _, err := s.DatabaseIdentity(t.Context()); err == nil || !strings.Contains(err.Error(), "nonempty journal") {
		t.Fatal(fmt.Sprint(err))
	}
}
