package store

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DatabaseIdentity returns the credential-free receipt identity for a configured
// DSN, without connecting. Setup and runtime use the same identity format.
func DatabaseIdentity(raw string) (string, error) {
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		return "", errors.New("invalid operator database URL")
	}
	return connectionIdentity(cfg.ConnConfig), nil
}

// DatabaseIdentity binds receipts to this store's configured connection, including
// its schema. A request cannot substitute its own claimed database identity.
// Reading pool configuration performs no database I/O.
func (s *Store) DatabaseIdentity() string {
	return connectionIdentity(s.pool.Config().ConnConfig)
}

func connectionIdentity(c *pgx.ConnConfig) string {
	return fmt.Sprintf("%s:%d/%s?search_path=%s", c.Host, c.Port, c.Database, c.RuntimeParams["search_path"])
}
