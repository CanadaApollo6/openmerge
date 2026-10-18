package github

import (
	"context"
	"fmt"
)

// ValidateInstallationPermissions inspects the actual installation token grant,
// not the App manifest. Landing permissions block; other deficits are warnings.
// Cached grants expire with their token, so accepting an updated installation
// is detected after renewal (or immediately after restarting serve).
func (d *Driver) ValidateInstallationPermissions(ctx context.Context) (problems, warnings []string, err error) {
	if _, err := d.app.token(ctx, d.installation); err != nil {
		return nil, nil, err
	}
	d.app.mu.Lock()
	entry := d.app.tokens[d.installation]
	d.app.mu.Unlock()
	entry.mu.Lock()
	defer entry.mu.Unlock()
	for _, permission := range []struct {
		name, access string
		landing      bool
	}{
		{"contents", "write", true}, {"workflows", "write", true},
		{"pull_requests", "write", false}, {"checks", "write", false}, {"actions", "write", false},
		{"statuses", "read", false}, {"issues", "read", false}, {"metadata", "read", false},
		{"members", "read", false},
	} {
		granted := entry.permissions[permission.name]
		if granted == "write" || granted == "admin" || permission.access == "read" && granted == "read" {
			continue
		}
		if granted == "" {
			granted = "none"
		}
		message := fmt.Sprintf("installed App missing permission %s: %s (granted: %s); update App permissions and accept them in installation settings", permission.name, permission.access, granted)
		if permission.landing {
			problems = append(problems, message)
		} else {
			warnings = append(warnings, message)
		}
	}
	return problems, warnings, nil
}
