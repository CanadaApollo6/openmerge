package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// Astra's two-member reproduction uses the authorized single-member fixture,
// adding a second exact revision and head before the batch is built.
func twoMemberLanding(t *testing.T, s *Store) (QueueID, ClaimedAction, landingCase, []queue.ApprovalEligibilityObserved, []queue.LandingDecisionRecorded) {
	t.Helper()
	raw, err := os.ReadFile("../queue/testdata/landing-start.json")
	must(t, err)
	var c landingCase
	must(t, json.Unmarshal(raw, &c))
	id, err := (QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "release"}).ChangeID(13)
	must(t, err)
	rewrite := func(v []byte) []byte {
		var compact bytes.Buffer
		must(t, json.Compact(&compact, v))
		v = compact.Bytes()
		v = bytes.ReplaceAll(v, []byte(c.Fresh.Basis.ChangeID), []byte(id))
		v = bytes.ReplaceAll(v, []byte(c.Fresh.Basis.Head), []byte(strings.Repeat("9", 40)))
		return bytes.ReplaceAll(v, []byte(`"pr":12`), []byte(`"pr":13`))
	}
	var events []json.RawMessage
	for _, raw := range c.Events {
		e, err := queue.DecodeEvent(raw)
		must(t, err)
		if start, ok := e.(queue.BatchStarted); ok {
			start.PRs = append(start.PRs, queue.BatchPR{PR: 13, HeadSHA: strings.Repeat("9", 40)})
			_, raw, err = queue.EncodeEvent(start)
			must(t, err)
		}
		events = append(events, raw)
		switch e.(type) {
		case queue.ChangeRevisionObserved, queue.ChangeClassified, queue.ChangeAccountabilityObserved, queue.ChangeIntentObserved, queue.ReviewStanceObserved, queue.PRObserved, queue.EnqueueRequested, queue.ApprovalReceiptRecorded:
			events = append(events, rewrite(raw))
		}
	}
	c.Events = events
	c.Land.PRs = append(c.Land.PRs, queue.BatchPR{PR: 13, HeadSHA: strings.Repeat("9", 40)})
	raw, err = json.Marshal(c)
	must(t, err)
	q, claim, c, d := decisionLandInput(t, s, raw)
	raw, err = json.Marshal(c.Fresh)
	must(t, err)
	var second queue.ApprovalEligibilityObserved
	must(t, json.Unmarshal(rewrite(raw), &second))
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	d2, err := state.PrepareLandingDecision(c.Policy, second, c.Start, c.Land)
	must(t, err)
	return q, claim, c, []queue.ApprovalEligibilityObserved{c.Fresh, second}, []queue.LandingDecisionRecorded{d, d2}
}

func twoMemberSide(t *testing.T, c landingCase, fresh []queue.ApprovalEligibilityObserved) func([]uint64) (SideAppend, error) {
	t.Helper()
	second := c
	second.Fresh = fresh[1]
	return func(decisions []uint64) (SideAppend, error) {
		one, err := landingSide(t, c)(decisions[:1])
		if err != nil {
			return SideAppend{}, err
		}
		two, err := landingSide(t, second)(decisions[1:])
		if err != nil {
			return SideAppend{}, err
		}
		return SideAppend{Count: 3, DedupeKey: "two-member-tested", Build: func(ids []uint64) ([]journalreader.Event, error) {
			a, err := one.Build([]uint64{ids[0], ids[2]})
			if err != nil {
				return nil, err
			}
			b, err := two.Build([]uint64{ids[1], ids[2]})
			if err != nil {
				return nil, err
			}
			l := a[1].Provenance.(pj.Landing)
			l.Members[0].Mapping.Build.Result = strings.Repeat("7", 40) // valid intermediate composition
			l.Members[0].RecordDigest = pj.MemberDigest(l.Members[0])
			l.Members = append(l.Members, b[1].Provenance.(pj.Landing).Members[0])
			return []journalreader.Event{a[0], b[0], {Provenance: l}}, nil
		}}, nil
	}
}

func recoveredCompanion(t *testing.T, s *Store, q QueueID) pj.Landing {
	t.Helper()
	var raw []byte
	must(t, s.pool.QueryRow(t.Context(), `SELECT payload FROM events WHERE queue_id=$1 AND type='landing_provenance_recorded' ORDER BY id DESC LIMIT 1`, q).Scan(&raw))
	e, err := pj.DecodeEvent(raw)
	must(t, err)
	l := e.(pj.Landing)
	l.Stage = "landed"
	for i := range l.Members {
		m := &l.Members[i]
		m.Mapping.Build.Stage = "landed"
		m.Mapping.Build.ID = "landed-composition"
		m.RecordDigest = pj.MemberDigest(*m)
	}
	return l
}
func recoveryRequest(l pj.Landing) SideAppend {
	return SideAppend{Count: 1, Build: func([]uint64) ([]journalreader.Event, error) { return []journalreader.Event{{Provenance: l}}, nil }}
}

func TestStartCompanionRequiresExactBatchCoverage(t *testing.T) {
	for _, kind := range []string{"missing", "revision", "head", "duplicate", "terminal-result"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			q, claim, c, fresh, decisions := twoMemberLanding(t, s)
			build := twoMemberSide(t, c, fresh)
			before := eventCount(t, s, q)
			err := s.startLandingWithSide(t.Context(), claim, c.Config, c.Policy, fresh, decisions, c.Start, s.authorizeStart, func(ids []uint64) (SideAppend, error) {
				request, err := build(ids)
				if err != nil {
					return request, err
				}
				inner := request.Build
				request.Build = func(ids []uint64) ([]journalreader.Event, error) {
					events, err := inner(ids)
					if err != nil {
						return nil, err
					}
					l := events[2].Provenance.(pj.Landing)
					switch kind {
					case "missing":
						l.Members = l.Members[:1]
					case "revision":
						l.Members[1].Request.Revision = "2"
					case "head":
						l.Members[1].Request.Head = strings.Repeat("8", 40)
					case "duplicate":
						l.Members[1] = l.Members[0]
					case "terminal-result":
						l.Members[1].Mapping.Build.Result = strings.Repeat("8", 40)
					}
					events[2].Provenance = l
					return events, nil
				}
				return request, nil
			})
			if err == nil {
				t.Fatal("accepted", kind)
			}
			assertNoLandingSideEffects(t, s, q, claim, before)
		})
	}
}

func TestRecoverCompanionRequiresExactBatchCoverage(t *testing.T) {
	for _, kind := range []string{"complete", "missing", "revision", "head", "terminal-result", "incomplete-historical-tested"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			q, claim, c, fresh, decisions := twoMemberLanding(t, s)
			if kind == "incomplete-historical-tested" {
				// Simulate the old seam's accepted incomplete companion, so recovery is
				// independently checked against the real batch rather than tested evidence.
				must(t, s.startLandingWithSide(t.Context(), claim, c.Config, c.Policy, fresh, decisions, c.Start, s.authorizeStart, nil))
				var id uint64
				must(t, s.pool.QueryRow(t.Context(), `SELECT id FROM events WHERE queue_id=$1 AND type='landing_decision_recorded' ORDER BY id LIMIT 1`, q).Scan(&id))
				request, err := landingSide(t, c)([]uint64{id})
				must(t, err)
				_, err = s.AppendSide(t.Context(), q, request)
				must(t, err)
			} else {
				must(t, s.startLandingWithSide(t.Context(), claim, c.Config, c.Policy, fresh, decisions, c.Start, s.authorizeStart, twoMemberSide(t, c, fresh)))
			}
			landedID := appendEvents(t, s, q, queue.Landed{BatchID: c.Start.BatchID, NewBaseSHA: decisions[0].LandedSHA, NewFence: "next"})
			l := recoveredCompanion(t, s, q)
			switch kind {
			case "missing":
				l.Members = l.Members[:1]
			case "revision":
				l.Members[1].Request.Revision = "2"
			case "head":
				l.Members[1].Request.Head = strings.Repeat("8", 40)
			case "terminal-result":
				l.Members[1].Mapping.Build.Result = strings.Repeat("8", 40)
			}
			before := eventCount(t, s, q)
			_, err := s.RecoverLanded(t.Context(), q, landedID, recoveryRequest(l))
			if kind == "complete" {
				must(t, err)
			} else if err == nil || eventCount(t, s, q) != before {
				t.Fatal("accepted invalid recovery", err)
			}
		})
	}
}

func TestRecoverySurvivesTerminalQueueEvents(t *testing.T) {
	for _, kind := range []string{"close", "dequeue", "head"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			q, claim, c, d := decisionLand(t, s)
			if kind == "close" {
				seedSideFences(t, s, q)
			}
			must(t, startWithSide(t.Context(), s, claim, c, d, landingSide(t, c)))
			landedID := appendEvents(t, s, q, queue.Landed{BatchID: c.Start.BatchID, NewBaseSHA: d.LandedSHA, NewFence: "next"})
			var event queue.Event
			switch kind {
			case "close":
				event = queue.PRClosed{PR: c.Land.PRs[0].PR}
			case "dequeue":
				event = queue.Dequeued{PR: c.Land.PRs[0].PR}
			case "head":
				event = queue.PRObserved{PR: c.Land.PRs[0].PR, HeadSHA: strings.Repeat("8", 40)}
			}
			appendEvents(t, s, q, event)
			auditPass(t, s, q) // persist/reload the terminal suffix before recovery
			restarted := openTestStore(t, s.pool.Config().ConnString())
			request := recoveryRequest(recoveredCompanion(t, s, q))
			id, err := restarted.RecoverLanded(t.Context(), q, landedID, request)
			must(t, err)
			got, err := restarted.RecoverLanded(t.Context(), q, landedID, request)
			must(t, err)
			if got != id {
				t.Fatal("retry appended again")
			}
			reader, _, err := replayJournal(t.Context(), s.pool, q, queue.State{BaseBranch: "release"}, 0, true)
			must(t, err)
			if len(reader.Provenance.Records()) != 0 {
				t.Fatal("completed evidence not retired")
			}
		})
	}
}

func TestRecoveryReportsAlreadyCompleteUnderAnotherKey(t *testing.T) {
	s := newStore(t)
	q, claim, c, d := decisionLand(t, s)
	must(t, startWithSide(t.Context(), s, claim, c, d, landingSide(t, c)))
	landedID := appendEvents(t, s, q, queue.Landed{BatchID: c.Start.BatchID, NewBaseSHA: d.LandedSHA, NewFence: "next"})
	request := recoveryRequest(recoveredCompanion(t, s, q))
	request.DedupeKey = "original-writer-key"
	id, err := s.AppendSide(t.Context(), q, request)
	must(t, err)
	_, err = s.RecoverLanded(t.Context(), q, landedID, request)
	var complete LandedAlreadyCompleteError
	if !errors.As(err, &complete) || complete.CompanionID != uint64(id) {
		t.Fatal("missing typed completion", err)
	}
}
