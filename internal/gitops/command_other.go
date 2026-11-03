//go:build !unix

package gitops

import (
	"context"
	"os/exec"
)

type managedCommand = exec.Cmd

// Other platforms retain CommandContext's direct-process cancellation.
func newGitCommand(ctx context.Context, name string, args ...string) *managedCommand {
	return exec.CommandContext(ctx, name, args...)
}
