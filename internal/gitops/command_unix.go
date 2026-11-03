//go:build unix && !linux

package gitops

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type managedCommand = exec.Cmd

func newGitCommand(ctx context.Context, name string, args ...string) *managedCommand {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// x/sys has no Waitid with WNOWAIT on these platforms. Cancel-only
		// cleanup misses descendants after early leader exit, and may signal a
		// reused PGID after Cmd.Wait reaps the leader. Linux owns both steps.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return cmd
}
