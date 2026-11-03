// Package procgroup supervises subprocess lifetimes without application policy.
// Linux kills the process group before reaping its leader, on exit or cancellation.
// Other Unix systems use cancellation-only group cleanup; non-Unix systems cancel
// only the direct process. Detached descendants are outside these guarantees.
package procgroup

import (
	"context"
	"time"
)

// CommandContext creates a command with a one-second pipe-drain backstop.
// Callers configure its exec.Cmd fields, then use Start/Wait, Run, Output, or
// CombinedOutput. They must not reap the process or bypass these methods.
func CommandContext(ctx context.Context, name string, args ...string) *Cmd {
	cmd := newCommand(ctx, name, args...)
	cmd.WaitDelay = time.Second
	return cmd
}
