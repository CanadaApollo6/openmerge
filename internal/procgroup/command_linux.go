package procgroup

import (
	"bytes"
	"context"
	"errors"
	"os/exec"

	"golang.org/x/sys/unix"
)

// Cmd owns group cleanup before exec.Cmd.Wait can reap the group leader.
// Use its supervised lifecycle methods rather than those on the embedded Cmd.
type Cmd struct {
	*exec.Cmd
	ctx       context.Context
	groupDone chan struct{}
	groupErr  error
	killGroup func(int) error
}

func newCommand(ctx context.Context, name string, args ...string) *Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &unix.SysProcAttr{Setpgid: true}
	// The supervisor owns cancellation. os/exec may still enforce WaitDelay,
	// but must never send a numeric group signal after reaping the leader.
	cmd.Cancel = nil
	return &Cmd{Cmd: cmd, ctx: ctx, killGroup: killProcessGroup}
}

// Start launches the command and its non-reaping group supervisor.
func (c *Cmd) Start() error {
	if err := c.Cmd.Start(); err != nil {
		return err
	}
	c.groupDone = make(chan struct{})
	go c.supervise()
	return nil
}

func (c *Cmd) supervise() {
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

// Wait cleans up the process group before reaping the leader and draining pipes.
func (c *Cmd) Wait() error {
	if c.groupDone != nil {
		<-c.groupDone
	}
	err := c.Cmd.Wait()
	if c.groupErr != nil {
		return errors.Join(c.groupErr, err)
	}
	return err
}

// Run starts the command and waits for supervised cleanup.
func (c *Cmd) Run() error {
	if err := c.Start(); err != nil {
		return err
	}
	return c.Wait()
}

// Output returns stdout after supervised cleanup. It must use the supervised
// Run rather than the embedded Cmd's method, which calls its own Start and Wait.
func (c *Cmd) Output() ([]byte, error) {
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

// CombinedOutput returns merged stdout and stderr after supervised cleanup.
func (c *Cmd) CombinedOutput() ([]byte, error) {
	if c.Stdout != nil || c.Stderr != nil {
		return nil, errors.New("exec: output already set")
	}
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	return out.Bytes(), err
}
