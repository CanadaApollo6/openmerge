package github

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// ValidateInstallationPermissions inspects the actual installation token grant,
// not the App manifest. Landing permissions block; other deficits are warnings.
// Cached grants expire with their token, so accepting an updated installation
// is detected after renewal (or immediately after restarting serve).
func (d *Driver) ValidateInstallationPermissions(ctx context.Context) (problems, warnings []string, err error) {
	return d.validatePermissions(ctx, []installationPermission{
		{"contents", "write", true}, {"workflows", "write", true},
		{"pull_requests", "write", false}, {"checks", "write", false}, {"actions", "write", false},
		{"statuses", "read", false}, {"issues", "read", false}, {"metadata", "read", false},
	})
}

// ValidateTeamPolicyPermissions checks optional team grants for the explicit
// openmerge setup permissions diagnostic via DiagnoseInstallationPermissions.
// Runtime team policy resolution uses ResolveTeam and TeamMembers: definite
// unavailability creates a durable ConfigSource block; transient failures retry.
func (d *Driver) ValidateTeamPolicyPermissions(ctx context.Context) (problems, warnings []string, err error) {
	return d.validatePermissions(ctx, []installationPermission{{"members", "read", false}})
}

// DiagnoseInstallationPermissions prints setup diagnostics for the installed
// token grant, including optional team policy prerequisites. It changes no App
// settings and does not block setup on optional permissions.
func (d *Driver) DiagnoseInstallationPermissions(ctx context.Context, out io.Writer) error {
	problems, warnings, err := d.ValidateInstallationPermissions(ctx)
	if err != nil {
		return err
	}
	_, teamWarnings, err := d.ValidateTeamPolicyPermissions(ctx)
	if err != nil {
		return err
	}
	for _, problem := range problems {
		if _, err := fmt.Fprintln(out, "ERROR:", problem); err != nil {
			return err
		}
	}
	for _, warning := range append(warnings, teamWarnings...) {
		if _, err := fmt.Fprintln(out, "WARN:", warning); err != nil {
			return err
		}
	}
	if len(problems) != 0 {
		return errors.New("installed App is missing landing permissions")
	}
	return nil
}

type installationPermission struct {
	name, access string
	landing      bool
}

func (d *Driver) validatePermissions(ctx context.Context, required []installationPermission) (problems, warnings []string, err error) {
	if _, err := d.app.token(ctx, d.installation); err != nil {
		return nil, nil, err
	}
	d.app.mu.Lock()
	entry := d.app.tokens[d.installation]
	d.app.mu.Unlock()
	entry.mu.Lock()
	defer entry.mu.Unlock()
	for _, permission := range required {
		granted := entry.permissions[permission.name]
		if granted == "write" || granted == "admin" || permission.access == "read" && granted == "read" {
			continue
		}
		if granted == "" {
			granted = "none"
		}
		message := fmt.Sprintf("installed App missing permission %s: %s (granted: %s); update App permissions and accept them in installation settings", permission.name, permission.access, granted)
		if permission.name == "members" {
			message = fmt.Sprintf("installed App missing permission members: read (granted: %s); needed only for organization team owners or groups in approval policy; see the operating guide", granted)
		}
		if permission.landing {
			problems = append(problems, message)
		} else {
			warnings = append(warnings, message)
		}
	}
	return problems, warnings, nil
}
