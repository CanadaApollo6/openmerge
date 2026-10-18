package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrations embed.FS

func (s *Store) migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockMigration(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return err
		}
		files, err := fs.Glob(migrations, "migrations/*.sql")
		if err != nil {
			return err
		}
		for _, file := range files {
			var applied bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM schema_migrations WHERE version = $1)`, file).Scan(&applied); err != nil {
				return err
			}
			if applied {
				continue
			}
			sql, err := migrations.ReadFile(file)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return fmt.Errorf("migration %s: %w", file, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, file); err != nil {
				return err
			}
		}
		return nil
	})
}
