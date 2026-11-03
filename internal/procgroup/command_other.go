//go:build !unix

package procgroup

import (
	"context"
	"os/exec"
)

// Cmd uses the platform fallback lifecycle.
type Cmd = exec.Cmd

// Other platforms retain CommandContext's direct-process cancellation.
func newCommand(ctx context.Context, name string, args ...string) *Cmd {
	return exec.CommandContext(ctx, name, args...)
}
