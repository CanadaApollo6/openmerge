package gitops

import (
	"bytes"
	"context"
	"errors"
	"os/exec"

	"golang.org/x/sys/unix"
)

// managedCommand owns group cleanup before Cmd.Wait can reap the group leader.
// Keep the embedded Cmd private to this package; callers must use these methods.
type managedCommand struct {
	*exec.Cmd
	ctx       context.Context
	groupDone chan struct{}
	groupErr  error
	killGroup func(int) error
}

func newGitCommand(ctx context.Context, name string, args ...string) *managedCommand {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &unix.SysProcAttr{Setpgid: true}
	// The supervisor owns cancellation. os/exec may still enforce WaitDelay,
	// but must never send a numeric group signal after reaping the leader.
	cmd.Cancel = nil
	return &managedCommand{Cmd: cmd, ctx: ctx, killGroup: killProcessGroup}
}

func (c *managedCommand) Start() error {
	if err := c.Cmd.Start(); err != nil {
		return err
	}
	c.groupDone = make(chan struct{})
	go c.supervise()
	return nil
}

func (c *managedCommand) supervise() {
	defer close(c.groupDone)
	pid := c.Process.Pid
	exited := make(chan error, 1)
	go func() {
		var err error
		var info unix.Siginfo
		for {
			// WNOWAIT leaves the leader unreaped, pinning its PID and PGID.
			err = unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
			if !errors.Is(err, unix.EINTR) {
				break
			}
		}
		exited <- err
	}()
	select {
	case err := <-exited:
		if err != nil {
			// An unexpected external reaper would invalidate group ownership.
			// Never signal a numeric PGID when waitid could not retain it.
			c.groupErr = err
			return
		}
		c.groupErr = c.killGroup(pid)
	case <-c.ctx.Done():
		// Cmd.Wait cannot run yet, even if the leader has already exited.
		c.groupErr = c.killGroup(pid)
		c.groupErr = errors.Join(c.groupErr, <-exited)
	}
}

func killProcessGroup(pid int) error {
	for {
		err := unix.Kill(-pid, unix.SIGKILL)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.ESRCH) {
			return nil
		}
		return err
	}
}

func (c *managedCommand) Wait() error {
	if c.groupDone != nil {
		<-c.groupDone
	}
	err := c.Cmd.Wait()
	if c.groupErr != nil {
		return errors.Join(c.groupErr, err)
	}
	return err
}

func (c *managedCommand) Run() error {
	if err := c.Start(); err != nil {
		return err
	}
	return c.Wait()
}

// Output and CombinedOutput must use the supervised Run rather than the
// embedded Cmd's convenience methods, which call its own Start and Wait.
func (c *managedCommand) Output() ([]byte, error) {
	if c.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	var out, diagnostic bytes.Buffer
	c.Stdout = &out
	captureStderr := c.Stderr == nil
	if captureStderr {
		c.Stderr = &diagnostic
	}
	err := c.Run()
	var status *exec.ExitError
	if captureStderr && errors.As(err, &status) {
		status.Stderr = diagnostic.Bytes()
	}
	return out.Bytes(), err
}

func (c *managedCommand) CombinedOutput() ([]byte, error) {
	if c.Stdout != nil || c.Stderr != nil {
		return nil, errors.New("exec: output already set")
	}
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	return out.Bytes(), err
}
