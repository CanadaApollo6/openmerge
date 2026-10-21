package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

func TestDatabaseIdentitySequenceGapAndIndexedGenesis(t *testing.T) {
	raw := databaseURL(t)
	s := openTestStore(t, raw)
	q := ensureQueue(t, s, "")
	// A rolled-back first insert consumes id 1 without creating genesis.
	tx, err := s.pool.Begin(t.Context())
	must(t, err)
	_, err = tx.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) VALUES ($1,'pause_requested','{}')`, q)
	must(t, err)
	must(t, tx.Rollback(t.Context()))
	appendEvents(t, s, q, queue.PauseRequested{Reason: "operator", Detail: "genesis at 2"})
	var first int64
	must(t, s.pool.QueryRow(t.Context(), `SELECT min(id) FROM events`).Scan(&first))
	if first != 2 {
		t.Fatalf("genesis id=%d, want 2", first)
	}
	want := identityOf(t, s)
	for _, last := range []int{10, 100, 10000} {
		_, err := s.pool.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) SELECT queue_id,type,payload FROM events CROSS JOIN generate_series((SELECT max(id)::int+1 FROM events),$1) WHERE id=2`, last)
		must(t, err)
		if got := identityOf(t, s); got != want {
			t.Errorf("append through %d changed identity", last)
		}
	}
	_, err = s.pool.Exec(t.Context(), `ANALYZE events`)
	must(t, err)
	u, err := url.Parse(raw)
	must(t, err)
	var planJSON []byte
	must(t, s.pool.QueryRow(t.Context(), `EXPLAIN (FORMAT JSON) `+databaseGenesisQuery(u.Query().Get("search_path"))).Scan(&planJSON))
	var plans []struct{ Plan map[string]any }
	must(t, json.Unmarshal(planJSON, &plans))
	indexed := false
	var inspect func(map[string]any)
	inspect = func(node map[string]any) {
		kind, _ := node["Node Type"].(string)
		if strings.Contains(kind, "Sort") || kind == "Seq Scan" {
			t.Fatalf("genesis scans/sorts journal: %s", planJSON)
		}
		if kind == "Index Scan" && node["Index Name"] == "events_pkey" {
			indexed = true
		}
		children, _ := node["Plans"].([]any)
		for _, child := range children {
			inspect(child.(map[string]any))
		}
	}
	inspect(plans[0].Plan)
	if !indexed {
		t.Fatalf("genesis did not use primary-key index: %s", planJSON)
	}
	t.Logf("genesis plan: %s", planJSON)
}

func TestReceiptIdentityDiagnostics(t *testing.T) {
	const secret = "postgres://operator:secret-password@private-host/database"
	for _, tc := range []struct {
		name  string
		err   error
		want  string
		grant bool
	}{
		{"privilege", &pgconn.PgError{Code: "42501", Message: secret}, "SQLSTATE 42501", true},
		{"unavailable function", &pgconn.PgError{Code: "42883", Message: secret}, "SQLSTATE 42883", false},
		{"canceled", fmt.Errorf("%s: %w", secret, context.Canceled), "context canceled", false},
		{"deadline", context.DeadlineExceeded, "deadline exceeded", false},
		{"network", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New(secret)}, "network failure", false},
		{"unknown", errors.New(secret), "connection or protocol failure", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := clusterIdentityError(tc.err).Error()
			if !strings.Contains(got, tc.want) || strings.Contains(got, "requires EXECUTE") != tc.grant || strings.Contains(got, "secret-password") || strings.Contains(got, "private-host") {
				t.Fatal(got)
			}
		})
	}
	_, err := DatabaseIdentity(t.Context(), "postgres://operator:secret-password@private-host:bad-port/database")
	if err == nil || !strings.Contains(err.Error(), "invalid connection configuration") || strings.Contains(err.Error(), "secret-password") || strings.Contains(err.Error(), "private-host") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = DatabaseIdentity(ctx, databaseURL(t))
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatal(err)
	}
}
