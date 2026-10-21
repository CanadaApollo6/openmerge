package store

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func verificationFixture(t *testing.T) (QueueKey, queue.WriteRestrictionVerified, queue.PolicyAuthorityApplied) {
	t.Helper()
	read := func(path string) queue.Event {
		raw, err := os.ReadFile("../queue/testdata/wire/v1/" + path + ".json")
		must(t, err)
		e, err := queue.DecodeEvent(raw)
		must(t, err)
		return e
	}
	v := read("event_write_restriction_verified").(queue.WriteRestrictionVerified)
	v.VerifiedAt = time.Now().UTC().Add(-time.Minute)
	v.Basis = 0
	pin := read("variants/policy_authority_applied_activation").(queue.PolicyAuthorityApplied)
	pin.OpenMerge.Activation = nil
	return QueueKey{Installation: "173082682", Repository: v.Binding.Repository, BaseBranch: "main"}, v, pin
}

func TestVerificationAtomicRetryDailyAndRestart(t *testing.T) {
	s := newStore(t)
	k, v, _ := verificationFixture(t)
	// Wire-identical retries must survive JSON dropping monotonic clock data
	// and normalizing the time location. Both are valid event representations.
	v.VerifiedAt = time.Now()
	q, err := s.EnsureQueue(t.Context(), k)
	must(t, err)
	records := []VerificationRecord{{q, v}}
	first, err := s.CommitWriteRestrictionVerification(t.Context(), records)
	must(t, err)
	duplicate, err := s.CommitWriteRestrictionVerification(t.Context(), records)
	must(t, err)
	if first[0].EventID != duplicate[0].EventID {
		t.Fatal("retry appended twice")
	}
	reused := v
	reused.VerifiedAt = reused.VerifiedAt.Add(time.Second)
	if _, err := s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, reused}}); err == nil {
		t.Fatal("nonce reused across reads")
	}
	daily := v
	daily.Nonce = "next-day"
	daily.VerifiedAt = daily.VerifiedAt.Add(24 * time.Hour)
	second, err := s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, daily}})
	must(t, err)
	if second[0].EventID == first[0].EventID || second[0].Event.VerifiedAt.Equal(first[0].Event.VerifiedAt) {
		t.Fatal("daily evidence deduped")
	}
	stored, err := s.ProposedVerification(t.Context(), q, first[0].EventID)
	must(t, err)
	if !stored.VerifiedAt.Equal(v.VerifiedAt) {
		t.Fatal("source event changed")
	}
	k.BaseBranch = "other"
	other, err := s.EnsureQueue(t.Context(), k)
	must(t, err)
	if _, err := s.ProposedVerification(t.Context(), other, first[0].EventID); err == nil {
		t.Fatal("adopted another queue's event")
	}
	bad := v
	bad.Nonce = "atomic"
	bad.Binding.QueueKey = "wrong"
	_, before, err := s.Load(t.Context(), q)
	must(t, err)
	good := v
	good.Nonce = "atomic"
	if _, err := s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, good}, {other, bad}}); err == nil {
		t.Fatal("invalid target accepted")
	}
	_, after, err := s.Load(t.Context(), q)
	must(t, err)
	if before != after {
		t.Fatal("partially appended multi-queue verification")
	}
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if state.AuthorityPin() != nil || len(state.RulesProblems()) != 0 || state.WriteRestrictionStatus().State != "unverified" {
		t.Fatal("proposed verification activated authority")
	}
	// A new Store pool replays the journal independently of the first pool.
	restarted := openTestStore(t, s.pool.Config().ConnString())
	state2, watermark, err := restarted.Load(t.Context(), q)
	must(t, err)
	raw, err := queue.EncodeSnapshot(state)
	must(t, err)
	raw2, err := queue.EncodeSnapshot(state2)
	must(t, err)
	if !bytes.Equal(raw, raw2) || watermark != after {
		t.Fatal("restart projection differs")
	}
}

func TestVerificationConcurrentRuntimeAndAdoption(t *testing.T) {
	s := newStore(t)
	k, v, pin := verificationFixture(t)
	in := adoptionInput(t, &v, &pin)
	q, err := s.EnsureQueue(t.Context(), k)
	must(t, err)
	receipts, err := s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, v}})
	must(t, err)
	appendEvents(t, s, q, queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest})
	started := time.Now().UTC()
	readAt := time.Now().UTC()
	receipt, err := s.AdoptAuthorityPin(t.Context(), q, pin, receipts[0].EventID, 0, started, readAt, in, v.Evidence)
	must(t, err)
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if state.AuthorityPin().OpenMerge.Activation == nil || state.WriteRestrictionStatus().State != "verified" {
		t.Fatal("matching draft not adopted")
	}
	retry, err := s.AdoptAuthorityPin(t.Context(), q, pin, 9223372036854775807, 0, time.Time{}, time.Time{}, forge.WriteRestrictionInput{}, authority.Observation{})
	if err == nil && retry != receipt {
		t.Fatal("retry lost durable receipt", retry, receipt)
	}
	must(t, err)
	_, after, err := s.Load(t.Context(), q)
	must(t, err)
	if after != receipt.EventID {
		t.Fatal("per-operation pin writer appended duplicate")
	}
	basis := state.RulesSourceRevision(queue.WriteRestriction)
	// A runtime failure and operator pass are both based on the same pre-read
	// revision. Arrange both append orders; a definite negative always wins.
	for _, negativeFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(negativeFirst), func(t *testing.T) {
			state, _, err := s.Load(t.Context(), q)
			must(t, err)
			basis = state.RulesSourceRevision(queue.WriteRestriction)
			pass := v
			pass.Scope = "applied"
			pass.Nonce = fmt.Sprint("concurrent/", negativeFirst)
			pass.Basis = basis
			pass.VerifiedAt = time.Now().UTC()
			a := authority.BypassActor{Kind: "user", ID: "rogue", Mode: "always"}
			o := authority.CloneObservation(v.Evidence)
			o.Protections[0].BypassActors = append(o.Protections[0].BypassActors, a)
			o.Findings = []authority.Finding{{Element: "rogue", Kind: "unexpected_bypass", ProtectionID: "repository_ruleset/base-10", Actor: &a}}
			failure := queue.WriteRestrictionObserved{Binding: v.Binding, Basis: basis, ObservedAt: time.Now().UTC(), Evidence: o}
			firstDone := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			errs := make(chan error, 2)
			go func() {
				defer wg.Done()
				if !negativeFirst {
					<-firstDone
				}
				_, err := s.Append(context.Background(), q, []queue.Event{failure}, "")
				errs <- err
				if negativeFirst {
					close(firstDone)
				}
			}()
			go func() {
				defer wg.Done()
				if negativeFirst {
					<-firstDone
				}
				_, err := s.CommitWriteRestrictionVerification(context.Background(), []VerificationRecord{{q, pass}})
				errs <- err
				if !negativeFirst {
					close(firstDone)
				}
			}()
			wg.Wait()
			close(errs)
			for err := range errs {
				must(t, err)
			}
			state, _, err = s.Load(t.Context(), q)
			must(t, err)
			if state.WriteRestrictionStatus().State != "violated" {
				t.Fatal("concurrent stale pass cleared new violation")
			}
			pass.Basis = state.RulesSourceRevision(queue.WriteRestriction)
			pass.Nonce += "/fresh"
			pass.VerifiedAt = time.Now().UTC()
			_, err = s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, pass}})
			must(t, err)
			state, _, err = s.Load(t.Context(), q)
			must(t, err)
			if state.WriteRestrictionStatus().State != "verified" || !state.WriteRestrictionStatus().LastVerifiedAt.Equal(pass.VerifiedAt) {
				t.Fatal("fresh pass did not clear")
			}
		})
	}
}

func TestProposedLookupBoundedPages(t *testing.T) {
	s := newStore(t)
	k, v, _ := verificationFixture(t)
	q, err := s.EnsureQueue(t.Context(), k)
	must(t, err)
	events := make([]queue.Event, 15200)
	for i := range events {
		events[i] = queue.BaseMoved{SHA: fmt.Sprint("base", i)}
	}
	appendEvents(t, s, q, events...)
	receipts, err := s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, v}})
	must(t, err)
	start := time.Now()
	_, err = s.ProposedVerification(t.Context(), q, receipts[0].EventID)
	must(t, err)
	t.Logf("15201-event bounded proposed lookup: %s", time.Since(start))
	rows, err := s.pool.Query(t.Context(), `EXPLAIN (ANALYZE, BUFFERS) SELECT id,payload FROM events WHERE queue_id=$1 AND id>$2 AND id<=$3 ORDER BY id LIMIT 256`, q, 14800, receipts[0].EventID)
	must(t, err)
	defer rows.Close()
	for rows.Next() {
		var line string
		must(t, rows.Scan(&line))
		t.Log(line)
	}
	must(t, rows.Err())
}

func TestDailyAppliedVerificationAdvancesTimeWithIdenticalFingerprint(t *testing.T) {
	s := newStore(t)
	k, v, pin := verificationFixture(t)
	q, err := s.EnsureQueue(t.Context(), k)
	must(t, err)
	appendEvents(t, s, q, queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}, pin)
	var previous int64
	for day := 0; day < 2; day++ {
		state, _, err := s.Load(t.Context(), q)
		must(t, err)
		read := v
		read.Scope = "applied"
		read.Basis = state.RulesSourceRevision(queue.WriteRestriction)
		read.Nonce = fmt.Sprint("daily-applied/", day)
		read.VerifiedAt = v.VerifiedAt.Add(time.Duration(day) * 24 * time.Hour)
		receipts, err := s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, read}})
		must(t, err)
		if receipts[0].EventID == previous || receipts[0].Event.ProtectionFingerprint != v.ProtectionFingerprint {
			t.Fatal("daily identical evidence did not get a distinct source event")
		}
		previous = receipts[0].EventID
		state, _, err = s.Load(t.Context(), q)
		must(t, err)
		status := state.WriteRestrictionStatus()
		if status.State != "verified" || !status.LastVerifiedAt.Equal(read.VerifiedAt) || !status.LastObservedAt.Equal(read.VerifiedAt) {
			t.Fatal("daily applied evidence did not advance the active verification time", status)
		}
	}
}

func adoptionInput(t *testing.T, draft *queue.WriteRestrictionVerified, pin *queue.PolicyAuthorityApplied) forge.WriteRestrictionInput {
	t.Helper()
	selectors := []authority.BaseSelector{{DefaultBranch: true}, {Pattern: "release/**"}}
	digest, err := authority.CoverageDigest("main", selectors)
	must(t, err)
	draft.Binding.CoverageDigest = digest
	pin.CoverageDigest = digest
	pin.OpenMerge.Binding = draft.Binding
	in := forge.WriteRestrictionInput{Repo: forge.RepoRef{Path: draft.Binding.Repository}, Binding: draft.Binding, BaseSelectors: selectors, ExpectedRefs: []string{"refs/heads/main"}, AppID: "5383059"}
	draft.Evidence.Findings = []authority.Finding{}
	draft.Evidence, err = forge.InterpretWriteRestriction(in, draft.Evidence)
	must(t, err)
	draft.ProtectionFingerprint, err = draft.Evidence.Fingerprint()
	must(t, err)
	return in
}

func TestAuthorityAdoptionReceiptsAndNoOpRetry(t *testing.T) {
	for _, adopted := range []bool{true, false} {
		t.Run(fmt.Sprint(adopted), func(t *testing.T) {
			s := newStore(t)
			k, draft, pin := verificationFixture(t)
			in := adoptionInput(t, &draft, &pin)
			q, err := s.EnsureQueue(t.Context(), k)
			must(t, err)
			if !adopted {
				// Fingerprints intentionally exclude findings. An invented draft
				// finding must not be silently stripped into a passing adoption.
				draft.Evidence.Findings = []authority.Finding{{Element: "invented", Kind: "missing_rule", ProtectionID: "repository_ruleset/base-10"}}
			}
			verification, err := s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, draft}})
			must(t, err)
			appendEvents(t, s, q, queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest})
			started := time.Now().UTC()
			receipt, err := s.AdoptAuthorityPin(t.Context(), q, pin, verification[0].EventID, 0, started, time.Now().UTC(), in, draft.Evidence)
			must(t, err)
			if receipt.QueueID != q || receipt.EventID <= verification[0].EventID {
				t.Fatal(receipt)
			}
			if adopted {
				if receipt.Outcome != AuthorityDraftAdopted || receipt.ProposedEventID != verification[0].EventID {
					t.Fatal(receipt)
				}
			} else if receipt.Outcome != AuthorityPinnedUnverified || receipt.ProposedEventID != 0 {
				t.Fatal(receipt)
			}
			wantOutput := fmt.Sprintf("authority %s: queue_id=%d pin_event_id=%d proposed_event_id=%d", receipt.Outcome, q, receipt.EventID, receipt.ProposedEventID)
			if receipt.String() != wantOutput {
				t.Fatal(receipt.String())
			}
			// Invalid draft IDs and unusable reads prove retry returns before
			// proposed history lookup, without appending another pin event.
			for range 3 {
				retry, err := s.AdoptAuthorityPin(t.Context(), q, pin, 9223372036854775807, 42, time.Time{}, time.Time{}, forge.WriteRestrictionInput{}, authority.Observation{})
				must(t, err)
				if retry != receipt {
					t.Fatal("retry changed receipt", retry, receipt)
				}
			}
			restarted := openTestStore(t, s.pool.Config().ConnString())
			state, watermark, err := restarted.Load(t.Context(), q)
			must(t, err)
			wantStatus := "unverified"
			if adopted {
				wantStatus = "verified"
			}
			if state.WriteRestrictionStatus().State != wantStatus || watermark != receipt.EventID {
				t.Fatal(state.WriteRestrictionStatus(), watermark)
			}
			retry, err := restarted.AdoptAuthorityPin(t.Context(), q, pin, 9223372036854775807, 0, time.Time{}, time.Time{}, forge.WriteRestrictionInput{}, authority.Observation{})
			must(t, err)
			if retry != receipt {
				t.Fatal("restart lost receipt", retry, receipt)
			}
		})
	}
}
