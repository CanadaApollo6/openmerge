package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// DatabaseIdentity reads the same server-side receipt binding as activation,
// without migrations or writes. It requires an existing, nonempty journal.
func DatabaseIdentity(ctx context.Context, raw string) (string, error) {
	s, err := OpenAdmin(ctx, raw, DefaultJournalReadTimeout)
	if err != nil {
		return "", errors.New("open receipt identity connection failed")
	}
	defer s.Close()
	return s.DatabaseIdentity(ctx)
}

// DatabaseIdentity binds to a cluster, database, resolved schema and journal
// genesis, not a DSN. A physical clone retaining all these values is identical.
// Call only for explicit operator verification/authority activation, not startup.
func (s *Store) DatabaseIdentity(ctx context.Context) (string, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", errors.New("begin receipt database identity read failed")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var cluster, database, databaseOID string
	if err := tx.QueryRow(ctx, `SELECT system_identifier::text FROM pg_catalog.pg_control_system()`).Scan(&cluster); err != nil {
		return "", errors.New("receipt database identity requires EXECUTE on pg_catalog.pg_control_system(); grant it explicitly to the operator/activation role")
	}
	if err := tx.QueryRow(ctx, `SELECT datname, oid::text FROM pg_catalog.pg_database WHERE datname=current_database()`).Scan(&database, &databaseOID); err != nil {
		return "", errors.New("read receipt database catalog identity failed")
	}
	// Reject shadowed copies as well as split schemas. All persistent store tables
	// must resolve uniquely in the effective path and belong to the same schema.
	var schema, schemaOID string
	for _, table := range []string{"events", "queues", "actions", "canonical_queues", "installation_apps", "observations", "webhook_deliveries", "schema_migrations"} {
		var name, oid string
		var count int
		err := tx.QueryRow(ctx, `SELECT min(n.nspname), min(n.oid::text), count(*)
   FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
   WHERE c.relname=$1 AND n.nspname=ANY(current_schemas(true))`, table).Scan(&name, &oid, &count)
		if err != nil || count != 1 || (schema != "" && schemaOID != oid) {
			return "", errors.New("receipt database identity requires unambiguous OpenMerge tables in one effective schema")
		}
		schema, schemaOID = name, oid
	}
	var genesisID, recordedAt, payload string
	query := `SELECT id::text, to_char(recorded_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US'), payload::text FROM ` + pgx.Identifier{schema, "events"}.Sanitize() + ` ORDER BY id LIMIT 1`
	if err := tx.QueryRow(ctx, query).Scan(&genesisID, &recordedAt, &payload); err != nil {
		return "", errors.New("receipt database identity requires a readable nonempty journal genesis")
	}
	// JSON framing prevents delimiter collisions; only the digest leaves this read.
	encoded, err := json.Marshal([]string{cluster, database, databaseOID, schema, schemaOID, genesisID, recordedAt, payload})
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", errors.New("commit receipt database identity read failed")
	}
	return fmt.Sprintf("openmerge-db-v2:sha256:%x", sha256.Sum256(encoded)), nil
}
