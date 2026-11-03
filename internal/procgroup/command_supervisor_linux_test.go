package procgroup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCommandGroupKillBeforeReap(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(strconv.FormatBool(canceled), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			script := "exit 0"
			if canceled {
				script = "printf ready; read line"
			}
			cmd := CommandContext(ctx, "/bin/sh", "-c", script)
			input, err := cmd.StdinPipe()
			must(t, err)
			defer func() { _ = input.Close() }()
			var observed string
			calls := 0
			cmd.killGroup = func(pid int) error {
				calls++
				// This runs on the owned cleanup path, before Wait may reap.
				if errors.Is(cmd.Process.Signal(unix.Signal(0)), os.ErrProcessDone) {
					observed = "group kill after reaping"
					return errors.New(observed)
				}
				if !canceled {
					state, err := processState(pid)
					if err != nil || state != "Z" {
						observed = fmt.Sprintf("normal-exit leader before kill: state=%s err=%v", state, err)
					}
				}
				if err := killProcessGroup(pid); err != nil {
					return err
				}
				var info unix.Siginfo
				if err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil); err != nil {
					observed = fmt.Sprintf("leader reaped before group cleanup: %v", err)
				}
				state, err := processState(pid)
				if err != nil || state != "Z" {
					observed = fmt.Sprintf("leader after kill: state=%s err=%v", state, err)
				}
				return nil
			}
			must(t, cmd.Start())
			if canceled {
				cancel()
			}
			err = cmd.Wait()
			if calls != 1 {
				t.Fatalf("group kill called %d times", calls)
			}
			if observed != "" {
				t.Fatal(observed)
			}
			if canceled && err == nil || !canceled && err != nil {
				t.Fatalf("canceled=%v, Wait=%v", canceled, err)
			}
			var status unix.WaitStatus
			if _, err := unix.Wait4(cmd.Process.Pid, &status, unix.WNOHANG, nil); !errors.Is(err, unix.ECHILD) {
				t.Fatalf("leader not reaped by Wait: %v", err)
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("second Wait succeeded")
			}
		})
	}
}

func TestCommandStartWaitCancellationKillsDescendant(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := CommandContext(ctx, "/bin/sh", "-c", "sleep 30 &\nprintf '%s\\n' \"$!\"; read line")
	cmd.WaitDelay = time.Second
	input, err := cmd.StdinPipe()
	must(t, err)
	defer func() { _ = input.Close() }()
	output, err := cmd.StdoutPipe()
	must(t, err)
	must(t, cmd.Start())
	reader := bufio.NewReader(output)
	line, err := reader.ReadString('\n')
	must(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	must(t, err)
	t.Cleanup(func() { _ = unix.Kill(pid, unix.SIGKILL) })
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("canceled command succeeded")
	}
	assertChildDead(t, pid)
}

func TestCommandRunIOAndStartFailure(t *testing.T) {
	cmd := CommandContext(t.Context(), "/bin/sh", "-c", "cat; printf diagnostic >&2")
	cmd.Stdin = strings.NewReader("input")
	var out, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	must(t, cmd.Run())
	if out.String() != "input" || diagnostic.String() != "diagnostic" {
		t.Fatalf("stdout=%q stderr=%q", &out, &diagnostic)
	}
	cmd = CommandContext(t.Context(), "/does-not-exist")
	if err := cmd.Run(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing command: %v", err)
	}
}

func TestCommandOutputSupervisesGroup(t *testing.T) {
	for _, combined := range []bool{false, true} {
		t.Run(strconv.FormatBool(combined), func(t *testing.T) {
			cmd := CommandContext(t.Context(), "/bin/sh", "-c", "printf output; printf diagnostic >&2; exit 1")
			calls := 0
			cmd.killGroup = func(pid int) error { calls++; return killProcessGroup(pid) }
			var out []byte
			var err error
			if combined {
				out, err = cmd.CombinedOutput()
			} else {
				out, err = cmd.Output()
			}
			var status *exec.ExitError
			if calls != 1 || !errors.As(err, &status) || status.ExitCode() != 1 {
				t.Fatalf("kill calls=%d err=%v", calls, err)
			}
			if combined && string(out) != "outputdiagnostic" || !combined && (string(out) != "output" || string(status.Stderr) != "diagnostic") {
				t.Fatalf("output=%q stderr=%q", out, status.Stderr)
			}
		})
	}
}

// Round-2's probe used Cancel after a deliberate sleep and observed a reaped
// leader in 100/100 iterations. Exercise cancellation after cleanup deterministically.
func TestReviewCancelCannotRunAfterReap(t *testing.T) {
	for range 100 {
		ctx, cancel := context.WithCancel(t.Context())
		cmd := CommandContext(ctx, "/bin/true")
		calls, afterReap := 0, false
		cmd.killGroup = func(pid int) error {
			calls++
			if errors.Is(cmd.Process.Signal(unix.Signal(0)), os.ErrProcessDone) {
				afterReap = true
				return os.ErrProcessDone
			}
			return killProcessGroup(pid)
		}
		must(t, cmd.Start())
		<-cmd.groupDone
		cancel()
		must(t, cmd.Wait())
		if calls != 1 || afterReap {
			t.Fatalf("group kill calls=%d, after reaping=%v", calls, afterReap)
		}
	}
	t.Log("100/100 group kills preceded reaping, including late cancellation")
}

func processState(pid int) (string, error) {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", err
	}
	_, fields, ok := strings.Cut(string(stat), ") ")
	if !ok {
		return "", errors.New("invalid process stat")
	}
	return strings.Fields(fields)[0], nil
}

func assertChildDead(t *testing.T, pid int) {
	t.Helper()
	// A killed orphan can remain a zombie until init reaps it. Poll the actual
	// process state; elapsed time alone never establishes descendant death.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for {
		state, err := processState(pid)
		if os.IsNotExist(err) || errors.Is(err, unix.ESRCH) || err == nil && state == "Z" {
			return
		}
		must(t, err)
		select {
		case <-ctx.Done():
			t.Fatalf("descendant %d remains alive after command return: %s", pid, state)
		case <-time.After(time.Millisecond):
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
