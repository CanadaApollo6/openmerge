package store

import (
	"context"
	"errors"
)

// InstallationApps returns only previously authenticated App ownership. Legacy
// queues remain unbound until a successful App listing identifies them.
func (s *Store) InstallationApps(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT installation, app_id FROM installation_apps`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owners := make(map[string]string)
	for rows.Next() {
		var installation, app string
		if err := rows.Scan(&installation, &app); err != nil {
			return nil, err
		}
		owners[installation] = app
	}
	return owners, rows.Err()
}

// BindInstallation records a complete App listing's ownership without changing
// queue keys, events or fences. A different App cannot claim that installation.
func (s *Store) BindInstallation(ctx context.Context, installation, app string) error {
	result, err := s.pool.Exec(ctx, `INSERT INTO installation_apps(installation,app_id) VALUES ($1,$2) ON CONFLICT (installation) DO UPDATE SET app_id=EXCLUDED.app_id WHERE installation_apps.app_id=EXCLUDED.app_id`, installation, app)
	if err == nil && result.RowsAffected() != 1 {
		return errors.New("installation belongs to another App")
	}
	return err
}
