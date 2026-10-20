package github

import (
	"context"
	"fmt"
	"io"
)

// DiagnoseAuthorityPermissions is setup-only; runtime grants stay unchanged.
func (d *Driver) DiagnoseAuthorityPermissions(ctx context.Context, out io.Writer) error {
	// Runtime intentionally caches only its existing grant set. Read a fresh,
	// unrestricted installation token grant for this setup-only recommendation;
	// never widen the runtime cache or change its permission requirements.
	token, _, err := d.app.client.Apps.CreateInstallationToken(ctx, d.installation, nil)
	if err != nil {
		return safeError(err)
	}
	d.app.sanitizer.AddSecrets(token.GetToken())
	grant := token.GetPermissions().GetAdministration()
	adopted := grant == "read" || grant == "write" || grant == "admin"
	if _, err := fmt.Fprintln(out, authorityPermissionGuidance); err != nil {
		return err
	}
	if !adopted {
		_, err = fmt.Fprintln(out, "RECOMMEND: installed App missing Administration: read; update App permissions and accept the grant in installation settings.")
	} else {
		_, err = fmt.Fprintln(out, "Installed App has Administration: read or greater.")
	}
	return err
}
