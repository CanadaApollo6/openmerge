package store

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func authorityLifecycleConfig(pin queue.PolicyAuthorityApplied) queue.ConfigApplied {
	return queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}
}

func cloneLifecyclePin(t *testing.T, pin queue.PolicyAuthorityApplied) queue.PolicyAuthorityApplied {
	t.Helper()
	_, raw, err := queue.EncodeEvent(pin)
	must(t, err)
	event, err := queue.DecodeEvent(raw)
	must(t, err)
	return event.(queue.PolicyAuthorityApplied)
}

func rawLifecyclePin(t *testing.T) queue.PolicyAuthorityApplied {
	t.Helper()
	raw, err := os.ReadFile("../queue/testdata/wire/v1/variants/policy_authority_applied_activation.json")
	must(t, err)
	event, err := queue.DecodeEvent(raw)
	must(t, err)
	return event.(queue.PolicyAuthorityApplied)
}

func lifecycleEventTypes(t *testing.T, s *Store, q QueueID) []string {
	t.Helper()
	rows, err := s.pool.Query(t.Context(), `SELECT type FROM events WHERE queue_id=$1 ORDER BY id`, q)
	must(t, err)
	defer rows.Close()
	var types []string
	for rows.Next() {
		var eventType string
		must(t, rows.Scan(&eventType))
		types = append(types, eventType)
	}
	must(t, rows.Err())
	return types
}

func TestApplyAuthorityConfigRejectsInvalidLifecycleAtomically(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, *Store, QueueID, queue.PolicyAuthorityApplied) (queue.PolicyAuthorityApplied, int64)
	}{
		{
			name: "missing-draft",
			prepare: func(_ *testing.T, _ *Store, _ QueueID, pin queue.PolicyAuthorityApplied) (queue.PolicyAuthorityApplied, int64) {
				return pin, 9223372036854775807
			},
		},
		{
			name: "wrong-queue-binding",
			prepare: func(t *testing.T, _ *Store, _ QueueID, pin queue.PolicyAuthorityApplied) (queue.PolicyAuthorityApplied, int64) {
				pin = cloneLifecyclePin(t, pin)
				pin.OpenMerge.Binding.QueueKey = "different/queue"
				return pin, 0
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newStore(t)
			key, draft, pin := verificationFixture(t)
			in := adoptionInput(t, &draft, &pin)
			q, err := s.EnsureQueue(t.Context(), key)
			must(t, err)
			pin, draftID := test.prepare(t, s, q, pin)
			started := time.Now().UTC()
			if _, err := s.ApplyAuthorityConfig(t.Context(), q, authorityLifecycleConfig(pin), pin, draftID, 0, started, started, in, draft.Evidence); err == nil {
				t.Fatal("invalid lifecycle input committed")
			}
			if n := count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1`, q); n != 0 {
				t.Fatalf("partial lifecycle append left %d events", n)
			}
		})
	}
}

func TestApplyAuthorityConfigRejectsMismatchedOrRawPinsAtomically(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, queue.PolicyAuthorityApplied) (queue.ConfigApplied, queue.PolicyAuthorityApplied)
	}{
		{
			name: "commit-mismatch",
			setup: func(_ *testing.T, pin queue.PolicyAuthorityApplied) (queue.ConfigApplied, queue.PolicyAuthorityApplied) {
				config := authorityLifecycleConfig(pin)
				config.Commit = "other-governing-commit"
				return config, pin
			},
		},
		{
			name: "digest-mismatch",
			setup: func(_ *testing.T, pin queue.PolicyAuthorityApplied) (queue.ConfigApplied, queue.PolicyAuthorityApplied) {
				config := authorityLifecycleConfig(pin)
				config.Digest = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
				return config, pin
			},
		},
		{
			name: "raw-activation-pin",
			setup: func(t *testing.T, _ queue.PolicyAuthorityApplied) (queue.ConfigApplied, queue.PolicyAuthorityApplied) {
				pin := rawLifecyclePin(t)
				return authorityLifecycleConfig(pin), pin
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newStore(t)
			key, _, pin := verificationFixture(t)
			q, err := s.EnsureQueue(t.Context(), key)
			must(t, err)
			config, pin := test.setup(t, pin)
			if _, err := s.ApplyAuthorityConfig(t.Context(), q, config, pin, 0, 0, time.Time{}, time.Time{}, forge.WriteRestrictionInput{}, authority.Observation{}); err == nil {
				t.Fatal("invalid config/pin pair committed")
			}
			if got := lifecycleEventTypes(t, s, q); len(got) != 0 {
				t.Fatalf("invalid config/pin pair left events: %v", got)
			}
		})
	}
}

func TestApplyAuthorityConfigExactConfigAndConcurrentPairs(t *testing.T) {
	t.Run("window-is-part-of-idempotence", func(t *testing.T) {
		s := newStore(t)
		key, draft, pin := verificationFixture(t)
		in := adoptionInput(t, &draft, &pin)
		q, err := s.EnsureQueue(t.Context(), key)
		must(t, err)
		verification, err := s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{QueueID: q, Event: draft}})
		must(t, err)
		started := time.Now().UTC()
		first, err := s.ApplyAuthorityConfig(t.Context(), q, authorityLifecycleConfig(pin), pin, verification[0].EventID, 0, started, time.Now().UTC(), in, draft.Evidence)
		must(t, err)
		windowed := authorityLifecycleConfig(pin)
		windowed.Window = &queue.WindowBounds{MinDepth: 1, MaxDepth: 1}
		state, _, err := s.Load(t.Context(), q)
		must(t, err)
		second, err := s.ApplyAuthorityConfig(t.Context(), q, windowed, pin, verification[0].EventID, state.RulesSourceRevision(queue.WriteRestriction), time.Now().UTC(), time.Now().UTC(), in, draft.Evidence)
		must(t, err)
		if second.EventID <= first.EventID || second.Outcome != AuthorityDraftAdopted {
			t.Fatalf("full config difference was treated as a no-op: first=%#v second=%#v", first, second)
		}
		if got, want := lifecycleEventTypes(t, s, q), []string{"write_restriction_verified", "config_applied", "policy_authority_applied", "config_applied", "policy_authority_applied"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("config change did not append a complete pair: got %v want %v", got, want)
		}
	})

	t.Run("same-request", func(t *testing.T) {
		s := newStore(t)
		key, draft, pin := verificationFixture(t)
		in := adoptionInput(t, &draft, &pin)
		q, err := s.EnsureQueue(t.Context(), key)
		must(t, err)
		verification, err := s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{QueueID: q, Event: draft}})
		must(t, err)
		started := time.Now().UTC()
		type result struct {
			receipt AuthorityAdoptionReceipt
			err     error
		}
		results := make(chan result, 2)
		for range 2 {
			go func() {
				receipt, err := s.ApplyAuthorityConfig(context.Background(), q, authorityLifecycleConfig(pin), pin, verification[0].EventID, 0, started, time.Now().UTC(), in, draft.Evidence)
				results <- result{receipt: receipt, err: err}
			}()
		}
		first, second := <-results, <-results
		must(t, first.err)
		must(t, second.err)
		if first.receipt != second.receipt {
			t.Fatalf("concurrent retry receipts differ: %#v %#v", first.receipt, second.receipt)
		}
		if got, want := lifecycleEventTypes(t, s, q), []string{"write_restriction_verified", "config_applied", "policy_authority_applied"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("concurrent same request did not dedupe pair: got %v want %v", got, want)
		}
	})

	t.Run("different-requests-remain-adjacent", func(t *testing.T) {
		s := newStore(t)
		key, draft, pin := verificationFixture(t)
		in := adoptionInput(t, &draft, &pin)
		q, err := s.EnsureQueue(t.Context(), key)
		must(t, err)
		verification, err := s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{QueueID: q, Event: draft}})
		must(t, err)
		type result struct{ err error }
		results := make(chan result, 2)
		for _, commit := range []string{"concurrent-first", "concurrent-second"} {
			candidate := cloneLifecyclePin(t, pin)
			candidate.GoverningCommit = commit
			go func(pin queue.PolicyAuthorityApplied) {
				_, err := s.ApplyAuthorityConfig(context.Background(), q, authorityLifecycleConfig(pin), pin, verification[0].EventID, 0, time.Now().UTC(), time.Now().UTC(), in, draft.Evidence)
				results <- result{err: err}
			}(candidate)
		}
		must(t, (<-results).err)
		must(t, (<-results).err)
		if got, want := lifecycleEventTypes(t, s, q), []string{"write_restriction_verified", "config_applied", "policy_authority_applied", "config_applied", "policy_authority_applied"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("concurrent lifecycle writes interleaved pairs: got %v want %v", got, want)
		}
	})
}

func TestApplyAuthorityConfigHistoricalBackfillIsAtomic(t *testing.T) {
	s := newStore(t)
	key, _, pin := verificationFixture(t)
	q, err := s.EnsureQueue(t.Context(), key)
	must(t, err)
	old := queue.ConfigApplied{Commit: "legacy-governing-commit", Digest: strings.Repeat("a", 64)}
	appendEvents(t, s, q, old)
	backfill := old
	backfill.Window = &queue.WindowBounds{MinDepth: 1, MaxDepth: 1}
	if _, err := s.ApplyAuthorityConfig(t.Context(), q, authorityLifecycleConfig(pin), pin, 0, 0, time.Time{}, time.Time{}, forge.WriteRestrictionInput{}, authority.Observation{}, backfill); err != nil {
		t.Fatal(err)
	}
	if got, want := lifecycleEventTypes(t, s, q), []string{"config_applied", "config_applied", "config_applied", "policy_authority_applied"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("historical backfill was not followed by one complete pair: got %v want %v", got, want)
	}
	bad := backfill
	bad.Commit = "wrong-legacy-governing-commit"
	next := cloneLifecyclePin(t, pin)
	next.GoverningCommit = "next-governing-commit"
	if _, err := s.ApplyAuthorityConfig(t.Context(), q, authorityLifecycleConfig(next), next, 0, 0, time.Time{}, time.Time{}, forge.WriteRestrictionInput{}, authority.Observation{}, bad); err == nil {
		t.Fatal("mismatched historical backfill committed")
	}
	if got, want := lifecycleEventTypes(t, s, q), []string{"config_applied", "config_applied", "config_applied", "policy_authority_applied"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("invalid historical backfill partially appended: got %v want %v", got, want)
	}
}

func TestApplyAuthorityConfigLifecycleAndRollback(t *testing.T) {
	s := newStore(t)
	key, draft, pin := verificationFixture(t)
	in := adoptionInput(t, &draft, &pin)
	q, err := s.EnsureQueue(t.Context(), key)
	must(t, err)
	verification, err := s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{QueueID: q, Event: draft}})
	must(t, err)

	started := time.Now().UTC()
	adopted, err := s.ApplyAuthorityConfig(t.Context(), q, authorityLifecycleConfig(pin), pin, verification[0].EventID, 0, started, time.Now().UTC(), in, draft.Evidence)
	must(t, err)
	if adopted.Outcome != AuthorityDraftAdopted || adopted.ProposedEventID != verification[0].EventID {
		t.Fatal(adopted)
	}
	// Retrying the same lifecycle event must return before examining an invalid
	// draft and, critically, must keep the persisted interpreted activation.
	retry, err := s.ApplyAuthorityConfig(t.Context(), q, authorityLifecycleConfig(pin), pin, 9223372036854775807, 99, time.Time{}, time.Time{}, in, authority.Observation{})
	must(t, err)
	if retry != adopted {
		t.Fatalf("retry changed durable receipt: got %#v want %#v", retry, adopted)
	}
	if n := count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1`, q); n != 3 {
		t.Fatalf("lifecycle retry appended events: got %d, want 3", n)
	}

	// A fresh store must replay the atomic pair into the same active pin.
	restarted := openTestStore(t, s.pool.Config().ConnString())
	state, watermark, err := restarted.Load(t.Context(), q)
	must(t, err)
	if watermark != adopted.EventID || state.AuthorityPin() == nil || state.AuthorityPin().OpenMerge == nil || state.AuthorityPin().OpenMerge.Activation == nil {
		t.Fatalf("atomic lifecycle did not survive reload: watermark=%d pin=%#v", watermark, state.AuthorityPin())
	}

	rollback := cloneLifecyclePin(t, pin)
	rollback.GoverningCommit = "rollback-governing-commit"
	rollback.Authority = authority.GitHub
	rollback.OpenMerge = nil
	rolledBack, err := s.ApplyAuthorityConfig(t.Context(), q, authorityLifecycleConfig(rollback), rollback, 0, 0, time.Time{}, time.Time{}, forge.WriteRestrictionInput{}, authority.Observation{})
	must(t, err)
	if rolledBack.Outcome != AuthorityPinnedUnverified {
		t.Fatal(rolledBack)
	}
	state, _, err = s.Load(t.Context(), q)
	must(t, err)
	if got := state.AuthorityPin(); got == nil || got.Authority != authority.GitHub || got.OpenMerge != nil {
		t.Fatalf("rollback did not fold github mode: %#v", got)
	}

	// A github-only queue does not acquire a policy event. A github pin is only
	// the durable receipt for the OpenMerge pin immediately being rolled back.
	legacyKey := key
	legacyKey.BaseBranch = "release"
	legacy, err := s.EnsureQueue(t.Context(), legacyKey)
	must(t, err)
	if _, err := s.ApplyAuthorityConfig(t.Context(), legacy, authorityLifecycleConfig(rollback), rollback, 0, 0, time.Time{}, time.Time{}, forge.WriteRestrictionInput{}, authority.Observation{}); err == nil {
		t.Fatal("ordinary github queue acquired an authority pin")
	}
	if n := count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1`, legacy); n != 0 {
		t.Fatalf("ordinary github lifecycle left %d events", n)
	}

	// The proposal pre-dates the rollback pin. Re-enabling may record the new
	// policy atomically, but it must not revive that proposal as a verification.
	reenabled := cloneLifecyclePin(t, pin)
	reenabled.GoverningCommit = "re-enable-governing-commit"
	unverified, err := s.ApplyAuthorityConfig(t.Context(), q, authorityLifecycleConfig(reenabled), reenabled, verification[0].EventID, 0, time.Now().UTC(), time.Now().UTC(), in, draft.Evidence)
	must(t, err)
	if unverified.Outcome != AuthorityPinnedUnverified {
		t.Fatalf("stale proposed verification was adopted after rollback: %#v", unverified)
	}
	state, _, err = s.Load(t.Context(), q)
	must(t, err)
	if got := state.AuthorityPin(); got == nil || got.OpenMerge == nil || got.OpenMerge.Activation != nil {
		t.Fatalf("re-enable retained stale activation: %#v", got)
	}
}

func TestApplyAuthorityConfigChecksBasisAfterQueueLock(t *testing.T) {
	s := newStore(t)
	key, draft, pin := verificationFixture(t)
	in := adoptionInput(t, &draft, &pin)
	q, err := s.EnsureQueue(t.Context(), key)
	must(t, err)
	verification, err := s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{QueueID: q, Event: draft}})
	must(t, err)

	started := time.Now().UTC()
	_, err = s.ApplyAuthorityConfig(t.Context(), q, authorityLifecycleConfig(pin), pin, verification[0].EventID, 0, started, time.Now().UTC(), in, draft.Evidence)
	must(t, err)
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	basis := state.RulesSourceRevision(queue.WriteRestriction)

	// Pause the lifecycle call before its lock. The independently appended
	// observation then advances the source revision. The lifecycle transaction
	// must reload under that lock and decline the otherwise matching draft.
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.beforeQueueLock = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	next := cloneLifecyclePin(t, pin)
	next.GoverningCommit = "stale-basis-governing-commit"
	type result struct {
		receipt AuthorityAdoptionReceipt
		err     error
	}
	resultCh := make(chan result, 1)
	go func() {
		receipt, err := s.ApplyAuthorityConfig(context.Background(), q, authorityLifecycleConfig(next), next, verification[0].EventID, basis, time.Now().UTC(), time.Now().UTC(), in, draft.Evidence)
		resultCh <- result{receipt: receipt, err: err}
	}()
	<-entered
	observation := queue.WriteRestrictionObserved{Binding: pin.OpenMerge.Binding, Basis: basis, ObservedAt: time.Now().UTC(), Evidence: draft.Evidence}
	// The contested call owns s.beforeQueueLock. Use another pool against the
	// same scratch schema for the competing writer so it can acquire the queue
	// lock while the first store remains paused before that lock.
	writer := openTestStore(t, s.pool.Config().ConnString())
	_, err = writer.Append(t.Context(), q, []queue.Event{observation}, "")
	must(t, err)
	close(release)
	gotResult := <-resultCh
	s.beforeQueueLock = nil
	must(t, gotResult.err)
	if gotResult.receipt.Outcome != AuthorityPinnedUnverified {
		t.Fatalf("stale lifecycle basis adopted a draft: %#v", gotResult.receipt)
	}
	state, _, err = s.Load(t.Context(), q)
	must(t, err)
	if got := state.AuthorityPin(); got == nil || got.OpenMerge == nil || got.OpenMerge.Activation != nil {
		t.Fatalf("stale basis retained an activation: %#v", got)
	}
}

// Live branch inventory is not part of the policy binding. Both lifecycle
// writers must invalidate adoption before an unchanged binding can be reused.
func TestAuthorityInputDriftInvalidatesBothWriters(t *testing.T) {
	for _, configWriter := range []bool{true, false} {
		for _, oldDraft := range []bool{true, false} {
			t.Run(fmt.Sprintf("config=%v/draft=%v", configWriter, oldDraft), func(t *testing.T) {
				s := newStore(t)
				key, draft, pin := verificationFixture(t)
				in := adoptionInput(t, &draft, &pin)
				q, err := s.EnsureQueue(t.Context(), key)
				must(t, err)
				verification, err := s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{QueueID: q, Event: draft}})
				must(t, err)
				started := time.Now().UTC()
				first, err := s.ApplyAuthorityConfig(t.Context(), q, authorityLifecycleConfig(pin), pin, verification[0].EventID, 0, started, started, in, draft.Evidence)
				must(t, err)
				if first.Outcome != AuthorityDraftAdopted {
					t.Fatal(first)
				}
				// A newly discovered live branch changes interpretation without changing the
				// coverage digest, policy binding, config or raw fingerprint.
				changed := in
				changed.ExpectedRefs = append(slices.Clone(in.ExpectedRefs), "refs/heads/release/new")
				id := int64(0)
				if oldDraft {
					id = verification[0].EventID
				}
				var receipt AuthorityAdoptionReceipt
				if configWriter {
					receipt, err = s.ApplyAuthorityConfig(t.Context(), q, authorityLifecycleConfig(pin), pin, id, 0, started, started, changed, draft.Evidence)
				} else {
					receipt, err = s.AdoptAuthorityPin(t.Context(), q, pin, id, 0, started, started, changed, draft.Evidence)
				}
				must(t, err)
				state, _, err := s.Load(t.Context(), q)
				must(t, err)
				if receipt.EventID <= first.EventID || receipt.Outcome != AuthorityPinnedUnverified || state.AuthorityPin().OpenMerge.Activation != nil || state.WriteRestrictionStatus().State == "verified" || !state.WriteRestrictionStatus().LastVerifiedAt.IsZero() {
					t.Fatalf("stale adoption survived input drift: %#v %#v", receipt, state.WriteRestrictionStatus())
				}
				// Retrying with the old proposal cannot undo the durable floor.
				retry, err := s.ApplyAuthorityConfig(t.Context(), q, authorityLifecycleConfig(pin), pin, verification[0].EventID, state.RulesSourceRevision(queue.WriteRestriction), started, started, changed, draft.Evidence)
				must(t, err)
				if retry.Outcome != AuthorityPinnedUnverified {
					t.Fatal(retry)
				}
				restarted := openTestStore(t, s.pool.Config().ConnString())
				state, _, err = restarted.Load(t.Context(), q)
				must(t, err)
				if state.WriteRestrictionStatus().State == "verified" {
					t.Fatal("restart revived adoption")
				}
				// Proposed verification is a GitHub-mode preparation operation.
				// Recover through the supported rollback, fresh proposal and
				// re-enable lifecycle rather than reviving the old proposal.
				rollback := cloneLifecyclePin(t, pin)
				rollback.Authority, rollback.OpenMerge = authority.GitHub, nil
				_, err = s.ApplyAuthorityConfig(t.Context(), q, authorityLifecycleConfig(rollback), rollback, 0, 0, time.Time{}, time.Time{}, forge.WriteRestrictionInput{}, authority.Observation{})
				must(t, err)
				freshDraft := draft
				freshDraft.Nonce = "after-live-ref-drift"
				freshDraft.VerifiedAt = time.Now().UTC()
				freshDraft.Evidence = authority.CloneObservation(draft.Evidence)
				freshDraft.Evidence.Coverage = append(freshDraft.Evidence.Coverage, authority.Coverage{Ref: "refs/heads/release/new", Visibility: authority.Visible, ProtectionIDs: []string{"repository_ruleset/base-10"}})
				freshDraft.Evidence, err = forge.InterpretWriteRestriction(changed, freshDraft.Evidence)
				must(t, err)
				freshDraft.ProtectionFingerprint, err = freshDraft.Evidence.Fingerprint()
				must(t, err)
				proposal, err := s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{QueueID: q, Event: freshDraft}})
				must(t, err)
				state, _, err = s.Load(t.Context(), q)
				must(t, err)
				started = time.Now().UTC()
				if configWriter {
					receipt, err = s.ApplyAuthorityConfig(t.Context(), q, authorityLifecycleConfig(pin), pin, proposal[0].EventID, state.RulesSourceRevision(queue.WriteRestriction), started, started, changed, freshDraft.Evidence)
				} else {
					receipt, err = s.AdoptAuthorityPin(t.Context(), q, pin, proposal[0].EventID, state.RulesSourceRevision(queue.WriteRestriction), started, started, changed, freshDraft.Evidence)
				}
				must(t, err)
				state, _, err = s.Load(t.Context(), q)
				must(t, err)
				if receipt.Outcome != AuthorityDraftAdopted || !reflect.DeepEqual(state.AuthorityPin().OpenMerge.VerificationInput.ExpectedRefs, changed.ExpectedRefs) {
					t.Fatal("new proposal did not refreeze current inputs", receipt, state.AuthorityPin())
				}
				if got := state.WriteRestrictionStatus(); got.State != "verified" || got.LastVerifiedAt.IsZero() {
					t.Fatal("fresh adoption did not restore interpreted provenance", got)
				}
				// Replaying the same journal must retain the new input and pass.
				state, _, err = restarted.Load(t.Context(), q)
				must(t, err)
				if state.WriteRestrictionStatus().State != "verified" {
					t.Fatal("replay lost fresh adoption")
				}
				retry, err = s.ApplyAuthorityConfig(t.Context(), q, authorityLifecycleConfig(pin), pin, proposal[0].EventID, 0, time.Time{}, time.Time{}, changed, authority.Observation{})
				must(t, err)
				if retry != receipt {
					t.Fatal("fresh adoption retry changed receipt", retry, receipt)
				}
			})
		}
	}
}
