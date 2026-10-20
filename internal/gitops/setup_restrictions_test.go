package gitops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// This is a local transport test, not evidence of GitHub enforcement. The hook
// models the installed write restriction on one side of an atomic transaction.
// Objects come from the actual queue/fence builders and hit real receive-pack.
func TestSetupWriteRestrictionsRejectGeneratedAtomicPush(t *testing.T) {
	for _, rejected := range []string{"base", "fence", "stale lease"} {
		t.Run(rejected, func(t *testing.T) {
			f := newFixture(t)
			initial, err := f.g.EnsureFence(t.Context(), testRepo, testFence, forge.PushOptions{})
			must(t, err)
			tested := f.build(forge.Squash, f.prs[:1], "ruleset-setup")
			coverage := authority.CompileProtectedRefCoverage()
			ok, err := coverage.ContainsRef("refs/heads/"+testFence, "refs/heads/main")
			must(t, err)
			if !ok {
				t.Fatal("fence outside generated setup coverage")
			}
			s, unlock, err := f.g.open(t.Context(), testRepo)
			must(t, err)
			defer unlock()
			next, err := s.fence(t.Context(), initial, "land/setup", tested)
			must(t, err)
			marker := filepath.Join(t.TempDir(), "rejection")
			ref := "refs/heads/main"
			if rejected == "fence" {
				ref = "refs/heads/" + testFence
			}
			hook := "#!/bin/sh\nref=$1\nold=$2\nnew=$3\nif [ \"$ref\" = " + shellQuote(ref) + " ]; then\n printf '%s %s %s\\n' \"$old\" \"$new\" \"$ref\" > " + shellQuote(marker) + "\n echo 'managed write restriction rejected generated object' >&2\n exit 1\nfi\nexit 0\n"
			must(t, os.MkdirAll(filepath.Join(f.remote, "hooks"), 0700))
			must(t, os.WriteFile(filepath.Join(f.remote, "hooks", "update"), []byte(hook), 0700))
			lease := f.base
			if rejected == "stale lease" {
				lease = f.heads[2]
			}
			err = s.pushLease(t.Context(), "--force-with-lease=refs/heads/main:"+lease, "--force-with-lease=refs/heads/"+testFence+":"+initial, "--", s.remote, tested+":refs/heads/main", next+":refs/heads/"+testFence)
			t.Logf("scenario=%s before_base=%s before_fence=%s generated_test=%s generated_fence=%s rejection=%v", rejected, f.base, initial, tested, next, err)
			if err == nil {
				t.Fatal("rejected atomic push succeeded")
			}
			if gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/main") != f.base || gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/"+testFence) != initial {
				t.Fatal("atomic rejection partially moved remote refs")
			}
			if rejected == "stale lease" {
				if err != forge.ErrLeaseRejected {
					t.Fatal("stale lease lost classification", err)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("stale lease sent objects to hook")
				}
			} else {
				data, err := os.ReadFile(marker)
				must(t, err)
				expected := tested
				if rejected == "fence" {
					expected = next
				}
				if !strings.Contains(string(data), expected+" "+ref) {
					t.Fatal("hook did not see actual generated OID", string(data))
				}
			}
			if rejected == "fence" {
				// A per-ref update hook permits the other ref in a non-atomic control.
				// This demonstrates that the prior all-or-nothing result needs --atomic.
				_, _, controlErr := s.run(t.Context(), "", "push", "--porcelain", "--force-with-lease=refs/heads/main:"+f.base, "--force-with-lease=refs/heads/"+testFence+":"+initial, "--", s.remote, tested+":refs/heads/main", next+":refs/heads/"+testFence)
				if controlErr == nil || gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/main") != tested || gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/"+testFence) != initial {
					t.Fatal("non-atomic control did not independently reject fence")
				}
				gitCommand(t, f.remote, "", nil, "update-ref", "refs/heads/main", f.base, tested)
			}
			// Removing the local rejecting policy lets the SAME generated objects and
			// exact leases land together, proving failure did not consume either lease.
			must(t, os.Remove(filepath.Join(f.remote, "hooks", "update")))
			must(t, s.pushLease(t.Context(), "--force-with-lease=refs/heads/main:"+f.base, "--force-with-lease=refs/heads/"+testFence+":"+initial, "--", s.remote, tested+":refs/heads/main", next+":refs/heads/"+testFence))
			t.Logf("scenario=%s retry_base=%s retry_fence=%s", rejected, tested, next)
			if gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/main") != tested || gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/"+testFence) != next {
				t.Fatal("retry did not atomically land objects")
			}
		})
	}
}
