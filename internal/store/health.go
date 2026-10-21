package store

import "context"

// Probe executes a bounded, read-only query independent of queue history size.
// The caller supplies the health request deadline, including pool acquisition.
func (s *Store) Probe(ctx context.Context) error {
	var one int
	return s.pool.QueryRow(ctx, "SELECT 1").Scan(&one)
}
