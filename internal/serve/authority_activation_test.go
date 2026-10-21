package serve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func activationReceipt(t *testing.T, db *store.Store) authority.EmissionReceipt {
	t.Helper()
	// Test binaries without VCS metadata use the same explicit fallback as
	// release tooling. Never replace a revision already embedded by Go.
	if authority.RunningBinaryRevision() == "" {
		previous := authority.BuildRevision
		authority.BuildRevision = "activation-test-revision"
		t.Cleanup(func() { authority.BuildRevision = previous })
	}
	path := filepath.Join(t.TempDir(), "backup")
	must(t, os.WriteFile(path, []byte("offline test backup"), 0600))
	return authority.EmissionReceipt{DatabaseIdentity: db.DatabaseIdentity(), BinaryRevision: authority.RunningBinaryRevision(), DrainedWorkers: []string{"test-worker"}, DrainedAt: time.Now().Add(-time.Minute), RestoreTestedAt: time.Now().Add(-time.Second), BackupPath: path, BackupSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("offline test backup"))), CompatibleReadersOnly: true}
}

func TestAuthorityActivationReceiptBeforeIO(t *testing.T) {
	for name, want := range map[string]string{
		"no-opt-in":                    "internal authority activation opt-in required",
		"zero-age":                     "maximum age must be positive",
		"negative-age":                 "maximum age must be positive",
		"stale-drain":                  "stale emission receipt",
		"stale-restore":                "stale emission receipt",
		"wrong-binary":                 "BinaryRevision does not match",
		"caller-db-differs-from-store": "database-bound drain inventory",
		"missing-backup":               "first-emission backup unavailable",
		"read-error":                   "quota exhausted",
	} {
		t.Run(name, func(t *testing.T) {
			s, w, _, _ := fixture(t, repositoryConfig)
			w.AuthorityRequired = true
			r := activationReceipt(t, s.store)
			request := authorityActivation{Receipt: r, ReceiptMaxAge: time.Hour}
			switch name {
			case "no-opt-in":
				w.AuthorityRequired = false
			case "zero-age":
				request.ReceiptMaxAge = 0
			case "negative-age":
				request.ReceiptMaxAge = -time.Second
			case "stale-drain":
				request.Receipt.DrainedAt = time.Now().Add(-2 * time.Hour)
			case "stale-restore":
				request.Receipt.DrainedAt = time.Now().Add(-3 * time.Hour)
				request.Receipt.RestoreTestedAt = time.Now().Add(-2 * time.Hour)
			case "wrong-binary":
				request.Receipt.BinaryRevision = "another-binary"
			case "caller-db-differs-from-store":
				request.Receipt.DatabaseIdentity = "another-db"
				// A caller can supply a self-consistent receipt for another DB.
				// That attestation must not choose the runtime validation identity.
				must(t, request.Receipt.ValidateForBinary(request.Receipt.DatabaseIdentity, time.Now(), request.ReceiptMaxAge, authority.RunningBinaryRevision()))
				if request.Receipt.DatabaseIdentity == s.store.DatabaseIdentity() {
					t.Fatal("fixture did not use a different database")
				}
			case "missing-backup":
				request.Receipt.BackupPath += ".missing"
			}
			if name != "read-error" {
				s.store.Close() // receipt failures must precede database I/O
			}
			called := false
			sentinel := errors.New("quota exhausted")
			request.Read = func(context.Context) (authority.Observation, error) {
				called = true
				return authority.Observation{}, sentinel
			}
			receipt, err := s.activateAuthority(t.Context(), w, request)
			if err == nil || receipt != (store.AuthorityAdoptionReceipt{}) || called != (name == "read-error") {
				t.Fatalf("receipt=%+v err=%v read=%t", receipt, err, called)
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("receipt validation error=%v, want %q", err, want)
			}
			if name == "read-error" && !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
		})
	}
}

func TestAuthorityActivationReportsDurableOutcome(t *testing.T) {
	for _, adopted := range []bool{true, false} {
		t.Run(fmt.Sprint(adopted), func(t *testing.T) {
			s, w, _, _ := fixture(t, repositoryConfig)
			w.AuthorityRequired = true
			read := func(path string) queue.Event {
				raw, err := os.ReadFile("../queue/testdata/wire/v1/" + path + ".json")
				must(t, err)
				e, err := queue.DecodeEvent(raw)
				must(t, err)
				return e
			}
			draft := read("event_write_restriction_verified").(queue.WriteRestrictionVerified)
			pin := read("variants/policy_authority_applied_activation").(queue.PolicyAuthorityApplied)
			pin.OpenMerge.Activation = nil
			draft.Binding.InstallationID = 7
			draft.Binding.Repository = w.Key.Repository
			draft.Binding.QueueKey = "7/" + w.Key.Repository + "/main"
			draft.Binding.DeclaredActor.Team = "acme/emergency"
			selectors := []authority.BaseSelector{{DefaultBranch: true}, {Pattern: "release/**"}}
			digest, err := authority.CoverageDigest("main", selectors)
			must(t, err)
			draft.Binding.CoverageDigest = digest
			pin.CoverageDigest = digest
			pin.OpenMerge.Binding = draft.Binding
			draft.Basis = 0
			draft.VerifiedAt = time.Now().Add(-time.Minute)
			in := forge.WriteRestrictionInput{Repo: forge.RepoRef{Path: w.Key.Repository}, Binding: draft.Binding, BaseSelectors: selectors, ExpectedRefs: []string{"refs/heads/main"}, AppID: "5383059"}
			draft.Evidence, err = forge.InterpretWriteRestriction(in, draft.Evidence)
			must(t, err)
			draft.ProtectionFingerprint, err = draft.Evidence.Fingerprint()
			must(t, err)
			fresh := draft.Evidence
			if !adopted {
				draft.Evidence.Findings = []authority.Finding{{Element: "invented", Kind: "missing_rule", ProtectionID: "repository_ruleset/base-10"}}
			}
			verification, err := s.store.CommitWriteRestrictionVerification(t.Context(), []store.VerificationRecord{{QueueID: w.id, Event: draft}})
			must(t, err)
			_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}}, "")
			must(t, err)
			var log bytes.Buffer
			s.options.Logger = slog.New(slog.NewTextHandler(&log, nil))
			r := activationReceipt(t, s.store)
			request := authorityActivation{Receipt: r, ReceiptMaxAge: time.Hour, Pin: pin, DraftID: verification[0].EventID, Input: in, Read: func(context.Context) (authority.Observation, error) { return fresh, nil }}
			receipt, err := s.activateAuthority(t.Context(), w, request)
			must(t, err)
			want := store.AuthorityPinnedUnverified
			if adopted {
				want = store.AuthorityDraftAdopted
			}
			if receipt.Outcome != want || receipt.EventID == 0 || receipt.QueueID != w.id || !strings.Contains(log.String(), receipt.String()) {
				t.Fatalf("receipt=%+v log=%s", receipt, log.String())
			}
			if adopted != (receipt.ProposedEventID == verification[0].EventID) {
				t.Fatal(receipt)
			}
			retry, err := s.activateAuthority(t.Context(), w, request)
			must(t, err)
			if retry != receipt {
				t.Fatal("retry changed outcome", retry, receipt)
			}
		})
	}
}

func TestAuthorityActivationReceiptExpiresDuringSeeingRead(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, _, _ := fixture(t, repositoryConfig)
		w.AuthorityRequired = true
		r := activationReceipt(t, s.store)
		_, before, err := s.store.Load(t.Context(), w.id)
		must(t, err)
		request := authorityActivation{Receipt: r, ReceiptMaxAge: time.Hour, Read: func(context.Context) (authority.Observation, error) {
			time.Sleep(2 * time.Hour)
			return authority.Observation{}, nil
		}}
		receipt, err := s.activateAuthority(t.Context(), w, request)
		if err == nil || !strings.Contains(err.Error(), "stale emission receipt") || receipt != (store.AuthorityAdoptionReceipt{}) {
			t.Fatal("expired receipt reached emission", receipt, err)
		}
		_, after, err := s.store.Load(t.Context(), w.id)
		must(t, err)
		if before != after {
			t.Fatal("expired seeing read emitted events")
		}
	})
}
