package audit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

func agreementPin(t *testing.T) queue.PolicyAuthorityApplied {
	t.Helper()
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
	must(t, err)
	v, err := queue.DecodeEvent(raw)
	must(t, err)
	return v.(queue.PolicyAuthorityApplied)
}

// Compare every prefix with the complete queue fold, including unrelated events.
// The audit projection must preserve the accepted pin even after rejection;
// its extra rejected flag prevents that preserved pin from hiding bad history.
func TestGoverningPinPrefixAgreement(t *testing.T) {
	pin := agreementPin(t)
	github := pin
	github.Authority, github.OpenMerge = "github", nil
	invalid := github
	invalid.GoverningCommit = ""
	mismatch := github
	mismatch.EffectiveDigest = strings.Repeat("f", 64)
	config := queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}
	for name, suffix := range map[string][]queue.Event{
		"rollback":           {github},
		"validate-rejected":  {invalid, github},
		"digest-ignored":     {mismatch, github},
		"config-transitions": {queue.ConfigApplied{Commit: "next", Digest: mismatch.EffectiveDigest}, mismatch, config, pin},
		"same-digest-rebind": {queue.ConfigApplied{Commit: "rebind", Digest: pin.EffectiveDigest}, pin},
	} {
		t.Run(name, func(t *testing.T) {
			events := append([]queue.Event{pin, config, pin, queue.PauseRequested{Reason: "operator", Detail: "operator pause"}, config, pin}, suffix...)
			// The first pin is ignored, and must remain a fail-closed condition even
			// after a later valid pin. Also exercise clean prefixes independently.
			for _, start := range []int{0, 1} {
				f := database(t)
				f.queue(t, 1, "")
				var full queue.State
				var audit pinFold
				rejected := false
				var acceptedID int64
				for i, event := range events[start:] {
					typ, raw, err := queue.EncodeEvent(event)
					if err != nil {
						// Deliberately retain a Validate-rejected pin without changing the codec.
						if _, ok := event.(queue.PolicyAuthorityApplied); !ok {
							t.Fatal(err)
						}
						typ = "policy_authority_applied"
						raw, err = json.Marshal(map[string]any{"version": 1, "type": typ, "data": event})
						must(t, err)
					}
					var id int64
					must(t, f.admin.QueryRow(t.Context(), `INSERT INTO events(queue_id,type,payload,recorded_at) VALUES(1,$1,$2,'2026-10-22T12:00:00Z') RETURNING id`, typ, raw).Scan(&id))
					if p, ok := event.(queue.PolicyAuthorityApplied); ok {
						if p.Validate() != nil || p.EffectiveDigest != full.ConfigDigest {
							rejected = true
						} else {
							acceptedID = id
						}
					}
					full = queue.Apply(full, event)
					audit.apply(id, event)
					if !reflect.DeepEqual(audit.state.AuthorityPin(), full.AuthorityPin()) || audit.rejected != rejected || audit.id != acceptedID {
						t.Fatalf("start %d prefix %d: audit pin disagrees with queue fold", start, i+1)
					}
					must(t, f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
						got, err := governingPin(ctx, tx, 1, id+1)
						if rejected {
							if err != ErrPinRejected {
								t.Fatalf("prefix %d accepted rejected/ignored pin: %d %v", i+1, got, err)
							}
						} else if err != nil || got != acceptedID {
							t.Fatalf("prefix %d: got %d %v want %d", i+1, got, err, acceptedID)
						}
						return nil
					}))
				}
			}
		})
	}
}

func TestGoverningPinRejectedHistoryFailsExport(t *testing.T) {
	for _, mode := range []string{"invalid", "digest-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			fx := round3ApprovalFixture(t)
			pin := agreementPin(t)
			pin.Authority, pin.OpenMerge = "github", nil
			if mode == "invalid" {
				pin.GoverningCommit = ""
			} else {
				pin.EffectiveDigest = strings.Repeat("f", 64)
			}
			raw, err := json.Marshal(map[string]any{"version": 1, "type": "policy_authority_applied", "data": pin})
			must(t, err)
			_, err = fx.f.admin.Exec(t.Context(), `UPDATE events SET payload=$2 WHERE id=$1`, fx.pinID, raw)
			must(t, err)
			_, err = fx.f.reader.Export(t.Context(), Request{Installation: "7", Repository: "Owner/Repo"}, filepath.Join(t.TempDir(), "export"))
			if err != ErrPinRejected {
				t.Fatalf("bad pin hid requiredness: %v", err)
			}
		})
	}
}

func TestGoverningPinRollbackReplaySubstitutions(t *testing.T) {
	for _, mode := range []string{"recorded", "absent", "source"} {
		t.Run(mode, func(t *testing.T) {
			fx := round3ApprovalFixture(t)
			pin := fx.events["policy_authority_applied"][0].(queue.PolicyAuthorityApplied)
			pin.Authority, pin.OpenMerge = "github", nil
			rollbackID := evidenceInsert(t, fx.f, pin)
			// Equal pins still have distinct retained event identities.
			pin.GoverningCommit = "later-rollback"
			latest := evidenceInsert(t, fx.f, pin)
			decision := fx.decision
			decision.AttemptID = "rollback"
			decisionID := evidenceInsert(t, fx.f, decision)
			start := fx.events["landing_started"][0].(queue.LandingStarted)
			start.AttemptID = decision.AttemptID
			evidenceInsert(t, fx.f, start)
			dir := filepath.Join(t.TempDir(), "export")
			_, err := fx.f.admin.Exec(t.Context(), `UPDATE events SET recorded_at='2026-10-22T13:00:00Z' WHERE id=$1 OR id=$2`, decisionID, rollbackID)
			must(t, err)
			request := Request{Installation: "7", Repository: "Owner/Repo", From: "2026-10-22T13:00:00Z", Until: "2026-10-22T14:00:00Z"}
			_, err = fx.f.reader.Export(t.Context(), request, dir)
			must(t, err)
			retained := false
			var alternative Source
			for _, selected := range readSelections(t, dir) {
				if selected.Source.ID == rollbackID {
					alternative = selected.Source
				}
				if selected.Source.ID == latest && selected.Role == "dependency" {
					retained = true
				}
			}
			if !retained || alternative.ID != rollbackID {
				t.Fatal("closure omitted out-of-range rollback source")
			}
			_, err = fx.f.reader.Replay(t.Context(), filepath.Join(dir, "manifest.json"), filepath.Join(t.TempDir(), "valid"))
			must(t, err)
			rewriteSelections(t, dir, func(selections []Selection) {
				found := false
				for i := range selections {
					if selections[i].Source.ID != decisionID {
						continue
					}
					for j := range selections[i].References {
						ref := &selections[i].References[j]
						if ref.ExpectedType != "policy_authority_applied" {
							continue
						}
						if ref.Status != "inapplicable" || ref.Source == nil || ref.Source.ID != latest {
							t.Fatal("missing retained rollback", ref)
						}
						found = true
						switch mode {
						case "recorded":
							ref.Status = "recorded"
						case "absent":
							ref.Status, ref.Source = "not_recorded", nil
						case "source":
							ref.Source = &alternative
						}
					}
					selections[i].References, err = normalizeReferences(selections[i].References)
					must(t, err)
				}
				if !found {
					t.Fatal("decision reference missing")
				}
			})
			_, err = fx.f.reader.Replay(t.Context(), filepath.Join(dir, "manifest.json"), filepath.Join(t.TempDir(), "forged"))
			if err != errIntegrity {
				t.Fatalf("rehashed %s substitution accepted: %v", mode, err)
			}
		})
	}
}

func TestCheckAttemptNormalizedCrossEventTie(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	observation := func(attempt uint64, state string) queue.ChecksObserved {
		return queue.ChecksObserved{BatchID: "batch", TestedSHA: "tested", Runs: []queue.CheckRun{{ID: 5, Attempt: attempt, Name: "unit", Source: "app:1", State: state}}}
	}
	evidenceInsert(t, f, observation(1, "failure"))
	selected := evidenceInsert(t, f, observation(0, "success"))
	boundary := insert(t, f.admin, 1, "2026-10-22T12:00:01Z")
	id, attempt := uint64(5), uint64(1)
	c := queue.LandingChecksRecorded{BatchID: "batch", TestedSHA: "tested", Checks: []queue.LandingCheck{{Name: "unit", Source: "app:1", RunID: &id, Attempt: &attempt, Result: forge.CheckState("success")}}}
	must(t, f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		r := &evidenceResolver{ctx: ctx, tx: tx, event: sourceEvent{source: Source{QueueID: 1, ID: boundary}}, owner: EvidenceOwner{QueueID: 1, EventID: boundary}, refs: []Reference{}}
		if err := r.checkHistory(c); err != nil {
			return err
		}
		if r.refs[0].Source == nil || r.refs[0].Source.ID != selected || r.refs[0].RunID != 5 || r.refs[0].Attempt != 1 {
			t.Fatal("SQL did not select the later normalized tie", r.refs)
		}
		return nil
	}))
	// Within-event normalization describes the selected legacy zero attempt.
	got := newestCheckRun(observation(0, "success").Runs, "unit", "app:1")
	if got == nil || max(got.Attempt, 1) != 1 {
		t.Fatal("within-event normalization", got)
	}
}

func TestGoverningPinInapplicableReferenceShape(t *testing.T) {
	owner := EvidenceOwner{QueueID: 1, EventID: 10}
	ref := Reference{Kind: "authority", Relation: "relied_on", Status: "inapplicable", Owner: owner, ExpectedType: "policy_authority_applied", Identity: evidenceIdentity("governing"), Source: &Source{QueueID: 1, ID: 3, Type: "policy_authority_applied"}}
	if !validReferences([]Reference{ref}) {
		t.Fatal("valid inapplicable source rejected")
	}
	for _, mode := range []string{"missing-source", "wrong-type", "wrong-relation", "wrong-identity"} {
		bad := ref
		switch mode {
		case "missing-source":
			bad.Source = nil
		case "wrong-type":
			bad.Kind, bad.ExpectedType = "settlement", "landed"
			src := *ref.Source
			src.Type = "landed"
			bad.Source = &src
		case "wrong-relation":
			bad.Relation = "excluded_later"
		case "wrong-identity":
			bad.Identity = evidenceIdentity("other")
		}
		if validReferences([]Reference{bad}) {
			t.Fatal("invalid inapplicable shape accepted", mode)
		}
	}
}

// Proposed regression: governingPin is scoped to its own queue.
func TestGoverningPinQueueScoped(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	f.queue(t, 2, "lane2")
	pin := agreementPin(t)
	ins := func(q int, e queue.Event) int64 {
		typ, raw, err := queue.EncodeEvent(e)
		must(t, err)
		var id int64
		must(t, f.admin.QueryRow(t.Context(), `INSERT INTO events(queue_id,type,payload,recorded_at) VALUES($1,$2,$3,'2026-10-22T12:00:00Z') RETURNING id`, q, typ, raw).Scan(&id))
		return id
	}
	// queue 2: a digest-mismatched (rejected) github pin, and a different valid pin.
	bad := pin
	bad.Authority, bad.OpenMerge, bad.EffectiveDigest = "github", nil, strings.Repeat("e", 64)
	ins(2, bad)
	ins(1, queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest})
	own := ins(1, pin)
	ins(2, queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest})
	ins(2, pin)
	end := ins(1, queue.PauseRequested{Reason: "operator", Detail: "d"})
	must(t, f.reader.snapshot(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		got, err := governingPin(ctx, tx, 1, end)
		if err != nil || got != own {
			t.Fatalf("queue 1 pin: got %d,%v want %d", got, err, own)
		}
		if _, err := governingPin(ctx, tx, 2, end+1); err != ErrPinRejected {
			t.Fatalf("queue 2 rejection lost across cursor switch: %v", err)
		}
		if got, err := governingPin(ctx, tx, 1, end); err != nil || got != own {
			t.Fatalf("queue 1 poisoned by queue 2: %d %v", got, err)
		}
		if got, err := governingPin(ctx, tx, 1, own); err != nil || got != 0 {
			t.Fatalf("backward prefix reused future pin: %d %v", got, err)
		}
		if got, err := governingPin(ctx, tx, 1, end); err != nil || got != own {
			t.Fatalf("forward prefix lost pin: %d %v", got, err)
		}
		return nil
	}))
}
