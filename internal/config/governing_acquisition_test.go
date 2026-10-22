package config

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
)

type mismatchedGoverningIdentity struct{ forge.IdentityReader }

func (r mismatchedGoverningIdentity) WriteGrant(ctx context.Context, repo forge.RepoRef, user forge.UserIdentity) (forge.WriteGrant, error) {
	g, err := r.IdentityReader.WriteGrant(ctx, repo, user)
	g.User.ID++
	return g, err
}

type governingQuota struct{}

func (governingQuota) Error() string         { return "ghs_SECRET" }
func (governingQuota) RetryAfter() time.Time { return time.Unix(100, 0) }

func TestGoverningReadErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		cause error
		class string
	}{{context.DeadlineExceeded, "timeout"}, {context.Canceled, "canceled"}, {governingQuota{}, "quota"}, {errors.New("ghs_SECRET"), "unavailable"}} {
		err := &GoverningReadError{Boundary: "owners", Cause: tc.cause}
		if !strings.HasSuffix(err.Error(), ": "+tc.class) || strings.Contains(err.Error(), "ghs_SECRET") || !errors.Is(err, tc.cause) {
			t.Fatal(err)
		}
	}
}

func TestGoverningAcquisitionUsesForgeIdentitiesAndGrants(t *testing.T) {
	for _, kind := range []string{"user", "team", "reassigned-grant", "read-only", "missing-team", "incomplete-members", "email", "candidate-only"} {
		t.Run(kind, func(t *testing.T) {
			f := fake.New()
			repo := forge.RepoRef{Path: "acme/project"}
			f.Init(repo, "main", "openmerge/fence/main")
			raw := "version: 1\npolicy_authority: openmerge\nbreak_glass: {team: acme/emergency}\nqueues: [{name: main, merge_when: ['check-success = ci']}]\n"
			owner := "@alice"
			if kind == "team" || kind == "missing-team" || kind == "incomplete-members" {
				owner = "@acme/owners"
			}
			if kind == "email" {
				owner = "alice@example.test"
			}
			tip, err := f.PushBase(repo, "main", "landed governing", map[string]string{".openmerge.yml": raw, "CODEOWNERS": "* " + owner + "\n"})
			if err != nil {
				t.Fatal(err)
			}
			alice := forge.UserIdentity{ID: 21, Login: "alice", Kind: forge.UserAccount}
			f.SetIdentity(alice)
			f.SetWriteGrant(repo, 21, "write")
			if kind == "read-only" {
				f.SetWriteGrant(repo, 21, "read")
			}
			if kind != "missing-team" {
				f.SetTeam(forge.TeamIdentity{ID: 44, OrganizationID: 55, Organization: "acme", Slug: "owners"}, []forge.UserIdentity{alice})
			}
			if kind == "incomplete-members" {
				f.FailNext("TeamMembers", forge.ErrMembershipIncomplete)
			}
			candidate, _, err := LoadAuthorityCandidate([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if kind == "candidate-only" {
				candidate.Config.Queues[0].Name = "proposed"
			}
			var ids forge.IdentityReader = f
			if kind == "reassigned-grant" {
				ids = mismatchedGoverningIdentity{f}
			}
			err = candidate.AcquireGoverning(t.Context(), repo, "main", tip, f, ids)
			if (err == nil) != (kind == "user" || kind == "team") {
				t.Fatalf("%s acquisition: %v", kind, err)
			}
		})
	}
}

func TestGoverningDiagnosticHasNoFileLine(t *testing.T) {
	if got := (Diagnostic{NoLine: true, Path: "governing policy", Message: "invalid owners"}).Error(); got != "governing policy: invalid owners" {
		t.Fatal(got)
	}
}

func TestLegacyLineZeroDiagnosticsRemainExact(t *testing.T) {
	for _, tc := range []struct {
		diagnostic Diagnostic
		want       string
	}{
		{Diagnostic{Path: "queues", Message: "approval queue has no parsed policy candidate"}, "line 0: queues: approval queue has no parsed policy candidate"},
		{Diagnostic{Path: "approvals", Message: "accountable identity \"human\" must resolve to a human"}, "line 0: approvals: accountable identity \"human\" must resolve to a human"},
		{Diagnostic{Message: "invalid configuration"}, "line 0: invalid configuration"},
	} {
		if got := tc.diagnostic.Error(); got != tc.want {
			t.Fatalf("got %q; want %q", got, tc.want)
		}
	}
}

// Different owners may fail for different reasons in the same acquisition.
type mixedGoverningFailures struct {
	forge.IdentityReader
	calls map[string]int
}

func (r mixedGoverningFailures) ResolveUser(_ context.Context, name string) (forge.UserIdentity, error) {
	r.calls[name]++
	if name == "missing" {
		return forge.UserIdentity{}, forge.ErrIdentityUnavailable
	}
	return forge.UserIdentity{}, context.DeadlineExceeded
}

func TestGoverningMissingAndTransientOwnersRemainMutable(t *testing.T) {
	f := fake.New()
	repo := forge.RepoRef{Path: "acme/project"}
	f.Init(repo, "main", "openmerge/fence/main")
	raw := "version: 1\npolicy_authority: openmerge\nbreak_glass: {team: acme/emergency}\nqueues: [{name: main, merge_when: ['check-success = ci']}]\n"
	tip, err := f.PushBase(repo, "main", "mixed owner failures", map[string]string{".openmerge.yml": raw, "CODEOWNERS": "* @missing @transient\n"})
	if err != nil {
		t.Fatal(err)
	}
	candidate, _, err := LoadAuthorityCandidate([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	ids := mixedGoverningFailures{IdentityReader: f, calls: map[string]int{}}
	err = candidate.AcquireGoverning(t.Context(), repo, "main", tip, f, ids)
	var mutable *GoverningIdentityError
	if !errors.As(err, &mutable) || !errors.Is(err, forge.ErrIdentityUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("mixed owner failure must remain mutable and retain both causes: %v", err)
	}
	if ids.calls["missing"] == 0 || ids.calls["transient"] == 0 {
		t.Fatal("did not read both owners", ids.calls)
	}
	if got := err.Error(); got != "authority governing owners: timeout" {
		t.Fatalf("unexpected sanitized error: %q", got)
	}
}
