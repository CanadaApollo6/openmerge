package gitops

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestBuildBaseProofPreservesFailureClasses(t *testing.T) {
	for _, tc := range []struct {
		name, command, diagnostic string
		code                      int
		contextError              error
	}{
		{name: "missing object exit 128", command: "merge-base", code: 128},
		{name: "exit 1 with diagnostic", command: "merge-base", diagnostic: "fatal: broken ancestry", code: 1},
		{name: "failed ls-remote", command: "ls-remote", diagnostic: "transport unavailable", code: 128},
		{name: "cancelled during ancestry", command: "merge-base", contextError: context.Canceled},
		{name: "deadline", contextError: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			live := f.heads[1]
			f.push("main", live, false)
			opts := validationOptions(t, forge.PushBuild, []forge.PushRefIntent{{Ref: "refs/heads/proof", BuildBaseSHA: f.base}}, []authority.Rule{})
			s, unlock, err := f.g.open(t.Context(), testRepo)
			must(t, err)
			defer unlock()
			must(t, s.commits(t.Context(), f.base, live))
			realGit := f.g.git
			wrapper := filepath.Join(t.TempDir(), "git")
			writeExecutable(t, wrapper, "for arg do\nif [ \"$arg\" = "+shellQuote(tc.command)+" ]; then\nprintf '%s' "+shellQuote(tc.diagnostic)+" >&2\nexit "+strconv.Itoa(tc.code)+"\nfi\ndone\nexec "+shellQuote(f.g.git)+" \"$@\"\n")
			f.g.git = wrapper
			ctx := t.Context()
			if tc.contextError == context.Canceled {
				// A FIFO handshake places cancellation inside the ancestry command,
				// after both object acquisition and the live lease read succeeded.
				ready, gate := filepath.Join(t.TempDir(), "ready"), filepath.Join(t.TempDir(), "gate")
				must(t, syscall.Mkfifo(ready, 0600))
				must(t, syscall.Mkfifo(gate, 0600))
				writeExecutable(t, wrapper, "for arg do\nif [ \"$arg\" = merge-base ]; then\nprintf ready > "+shellQuote(ready)+"\nread line < "+shellQuote(gate)+"\nfi\ndone\nexec "+shellQuote(realGit)+" \"$@\"\n")
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- s.proveBuildBase(ctx, opts.Context) }()
				reader, readErr := os.Open(ready)
				must(t, readErr)
				_, readErr = io.ReadAll(reader)
				must(t, readErr)
				must(t, reader.Close())
				cancel()
				err = <-done
			} else {
				if tc.contextError == context.DeadlineExceeded {
					var cancel context.CancelFunc
					ctx, cancel = context.WithDeadline(ctx, time.Unix(1, 0))
					defer cancel()
				}
				err = s.proveBuildBase(ctx, opts.Context)
			}
			var moved *forge.BaseMovedError
			if err == nil || errors.As(err, &moved) {
				t.Fatalf("failure misclassified as base movement: %v", err)
			}
			if tc.contextError != nil {
				if !errors.Is(err, tc.contextError) {
					t.Fatalf("lost context class: %v", err)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != tc.code {
					t.Fatalf("lost exit class: %v", err)
				}
			}
		})
	}
}

func TestAuthorityCompletionDedicatedCacheAvoidsSessionLock(t *testing.T) {
	f := newFixture(t)
	completion := f.client(t, 0)
	session, unlock, err := f.g.open(t.Context(), testRepo)
	must(t, err)
	defer unlock()
	// The guard runs while this mutex is held. Reusing this cache would
	// wait on its own lock until the guard context expires.
	value, ok := cacheLocks.Load(session.dir)
	if !ok {
		t.Fatal("push session cache lock absent")
	}
	lock := value.(*sync.Mutex)
	if lock.TryLock() {
		lock.Unlock()
		t.Fatal("push session did not hold its cache lock")
	}
	live, err := completion.ReadPreparedRef(t.Context(), testRepo, "main")
	must(t, err)
	if live != f.base {
		t.Fatalf("completion read=%s", live)
	}
}
