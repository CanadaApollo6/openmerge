package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestRecoverLandedDuplicateMemberDecision(t *testing.T) {
	for _, kind := range []string{"singleton-identical", "singleton-altered", "multi-identical", "multi-altered"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			var q QueueID
			var c landingCase
			var decisions []queue.LandingDecisionRecorded
			if strings.HasPrefix(kind, "multi") {
				var claim ClaimedAction
				var fresh []queue.ApprovalEligibilityObserved
				q, claim, c, fresh, decisions = twoMemberLanding(t, s)
				must(t, s.startLandingWithSide(t.Context(), claim, c.Config, c.Policy, fresh, decisions, c.Start, s.authorizeStart, twoMemberSide(t, c, fresh)))
			} else {
				var claim ClaimedAction
				var d queue.LandingDecisionRecorded
				q, claim, c, d = decisionLand(t, s)
				decisions = []queue.LandingDecisionRecorded{d}
				must(t, startWithSide(t.Context(), s, claim, c, d, landingSide(t, c)))
			}
			var duplicate queue.LandingDecisionRecorded
			raw, err := json.Marshal(decisions[len(decisions)-1])
			must(t, err)
			must(t, json.Unmarshal(raw, &duplicate))
			if strings.HasSuffix(kind, "altered") {
				duplicate.Approval.ClassificationDigest = strings.Repeat("0", 64)
			}
			duplicateID := appendEvents(t, s, q, duplicate)
			landedID := appendEvents(t, s, q, queue.Landed{BatchID: c.Start.BatchID, NewBaseSHA: decisions[0].LandedSHA, NewFence: "next"})
			l := recoveredCompanion(t, s, q)
			member := len(l.Members) - 1
			if member == 0 {
				l.DecisionID = uint64(duplicateID)
			}
			for i := range l.Members[member].Layers {
				l.Members[member].Layers[i].References = []uint64{uint64(duplicateID)}
			}
			l.Members[member].RecordDigest = pj.MemberDigest(l.Members[member])
			_, err = s.RecoverLanded(t.Context(), q, landedID, recoveryRequest(l))
			if strings.HasSuffix(kind, "identical") {
				must(t, err)
			} else if err == nil {
				t.Fatal("accepted non-authoritative member decision with correct SHAs")
			}
			t.Logf("recovery result: %v", err)
		})
	}
}

// Exercise the already-complete branch with Astra's same secondary-decision
// substitution. A settled projection must not turn altered evidence into trust.
func TestRecoverLandedValidatesCompletedSecondaryDecision(t *testing.T) {
	s := newStore(t)
	q, claim, c, fresh, decisions := twoMemberLanding(t, s)
	must(t, s.startLandingWithSide(t.Context(), claim, c.Config, c.Policy, fresh, decisions, c.Start, s.authorizeStart, twoMemberSide(t, c, fresh)))
	d := decisions[1]
	d.Approval.ClassificationDigest = strings.Repeat("0", 64)
	duplicateID := appendEvents(t, s, q, d)
	landedID := appendEvents(t, s, q, queue.Landed{BatchID: c.Start.BatchID, NewBaseSHA: decisions[0].LandedSHA, NewFence: "next"})
	l := recoveredCompanion(t, s, q)
	for i := range l.Members[1].Layers {
		l.Members[1].Layers[i].References = []uint64{uint64(duplicateID)}
	}
	l.Members[1].RecordDigest = pj.MemberDigest(l.Members[1])
	request := recoveryRequest(l)
	request.DedupeKey = "completed-by-another-writer"
	_, err := s.AppendSide(t.Context(), q, request)
	must(t, err)
	_, err = s.RecoverLanded(t.Context(), q, landedID, recoveryRequest(l))
	if err == nil || !strings.Contains(err.Error(), "decision binding mismatch") {
		t.Fatal("completed secondary decision not validated", err)
	}
}

func TestRecoverLandedAboveHighWaterDrainsPending(t *testing.T) {
	s := newStore(t)
	q, claim, c, d := decisionLand(t, s)
	must(t, startWithSide(t.Context(), s, claim, c, d, landingSide(t, c)))
	landedID := appendEvents(t, s, q, queue.Landed{BatchID: c.Start.BatchID, NewBaseSHA: d.LandedSHA, NewFence: "next"})
	l := recoveredCompanion(t, s, q)
	growth := SideAppend{Count: 192, DedupeKey: "unrelated-active-inventories", Build: func([]uint64) ([]journalreader.Event, error) {
		events := make([]journalreader.Event, 192)
		for i := range events {
			h := sideInventory(t)
			h.Request.ChangeID = fmt.Sprint("unrelated-", i)
			h.Digest = pj.InventoryDigest(h, h.Commits)
			events[i].Provenance = h
		}
		return events, nil
	}}
	// Recovery must not become a capacity bypass for a growing side bundle.
	growing := growth
	growing.Count++
	growing.Build = func(ids []uint64) ([]journalreader.Event, error) {
		events, err := growth.Build(ids)
		return append(events, journalreader.Event{Provenance: l}), err
	}
	if _, err := s.RecoverLanded(t.Context(), q, landedID, growing); err == nil || !strings.Contains(err.Error(), "not bound to historical member") {
		t.Fatal("growing recovery admitted", err)
	}
	_, err := s.AppendSide(t.Context(), q, growth)
	must(t, err)
	s.sideHighWater = 1
	reader, _, err := replayJournal(t.Context(), s.pool, q, queue.State{BaseBranch: "release"}, 0, true)
	must(t, err)
	before := reader.Provenance.Capacity(1)
	if before.ProjectedBytes <= before.HighWaterBytes {
		t.Fatal("fixture below high water", before)
	}
	if err := reader.Preflight(1); err == nil {
		t.Fatal("ordinary append should refuse")
	}
	raw, err := reader.CursorSnapshot()
	must(t, err)
	if !bytes.Contains(raw, []byte(`"Awaiting"`)) {
		t.Fatal("missing pending marker")
	}
	_, err = s.RecoverLanded(t.Context(), q, landedID, recoveryRequest(l))
	must(t, err)
	reader, _, err = replayJournal(t.Context(), s.pool, q, queue.State{BaseBranch: "release"}, 0, true)
	must(t, err)
	after := reader.Provenance.Capacity(1)
	if after.ProjectedBytes >= before.ProjectedBytes || after.ProjectedBytes <= after.HighWaterBytes {
		t.Fatal("recovery must reduce evidence while remaining above high water", before, after)
	}
	raw, err = reader.CursorSnapshot()
	must(t, err)
	if bytes.Contains(raw, []byte(`"Awaiting"`)) {
		t.Fatal("recovery pinned pending marker")
	}
	if _, ok := reader.Provenance.SettledAttempt(c.Start.AttemptID); !ok {
		t.Fatal("recovery did not settle")
	}
	auditPass(t, s, q)
	t.Logf("recovery above %d-byte high water reduced %d -> %d bytes and drained Awaiting", before.HighWaterBytes, before.ProjectedBytes, after.ProjectedBytes)
}

// Walk the complete recorded decision, including nested source, policy/brief,
// obligation and actor evidence. Each table row changes exactly one wire field.
// Valid wire mutations must reach the historical-decision check; malformed
// mutations must fail the strict reader. Restore the isolated test row between
// cases so a malformed duplicate cannot poison the next case's replay.
func TestRecoverLandedDecisionFieldMutations(t *testing.T) {
	for _, variant := range []string{"approvals", "generated_remedy"} {
		t.Run(variant, func(t *testing.T) { testRecoverLandedDecisionFields(t, variant) })
	}
}

func testRecoverLandedDecisionFields(t *testing.T, variant string) {
	t.Helper()
	s := newStore(t)
	q, claim, c, fresh, decisions := twoMemberLanding(t, s)
	if variant == "approvals" {
		must(t, s.startLandingWithSide(t.Context(), claim, c.Config, c.Policy, fresh, decisions, c.Start, s.authorizeStart, twoMemberSide(t, c, fresh)))
	} else {
		// Seed the generated-remedy journal variant without enabling a writer.
		var ids []uint64
		for i := range decisions {
			d := &decisions[i]
			basis := fresh[i].Basis
			d.Variant, d.Approval = variant, nil
			d.Remedy = &queue.GeneratedRemedyDecision{ChangeID: basis.ChangeID, Revision: basis.Revision, Head: basis.Head, Suspect: basis.Head, Evidence: []string{"immutable-remedy-evidence"}, OnRed: "revert"}
			ids = append(ids, uint64(appendEvents(t, s, q, *d)))
		}
		appendEvents(t, s, q, c.Start)
		request, err := twoMemberSide(t, c, fresh)(ids)
		must(t, err)
		_, err = s.AppendSide(t.Context(), q, request)
		must(t, err)
	}
	duplicateID := appendEvents(t, s, q, decisions[1])
	landedID := appendEvents(t, s, q, queue.Landed{BatchID: c.Start.BatchID, NewBaseSHA: decisions[0].LandedSHA, NewFence: "next"})
	l := recoveredCompanion(t, s, q)
	for i := range l.Members[1].Layers {
		l.Members[1].Layers[i].References = []uint64{uint64(duplicateID)}
	}
	l.Members[1].RecordDigest = pj.MemberDigest(l.Members[1])
	_, original, err := queue.EncodeEvent(decisions[1])
	must(t, err)
	var wire map[string]any
	must(t, json.Unmarshal(original, &wire))
	type mutation struct {
		name string
		path []string
	}
	var table []mutation
	var fields func(any, []string)
	fields = func(v any, path []string) {
		switch v := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				fields(v[k], append(slices.Clone(path), k))
			}
		case []any:
			if len(v) == 0 {
				table = append(table, mutation{strings.Join(path, "/"), path})
			}
			for i, child := range v {
				fields(child, append(slices.Clone(path), strconv.Itoa(i)))
			}
		default:
			table = append(table, mutation{strings.Join(path, "/"), path})
		}
	}
	fields(wire["data"], []string{"data"})
	valid, rejected := 0, 0
	before := eventCount(t, s, q)
	ctx := t.Context()
	for _, row := range table {
		t.Run(row.name, func(t *testing.T) {
			var changed any
			must(t, json.Unmarshal(original, &changed))
			var replace func(any, []string)
			replace = func(node any, path []string) {
				get := func() any {
					if m, ok := node.(map[string]any); ok {
						return m[path[0]]
					}
					i, e := strconv.Atoi(path[0])
					must(t, e)
					return node.([]any)[i]
				}
				if len(path) > 1 {
					replace(get(), path[1:])
					return
				}
				var value any
				switch old := get().(type) {
				case string:
					value = old + "-altered"
					if len(old) == 40 || len(old) == 64 {
						value = strings.Repeat("0", len(old))
						if value == old {
							value = strings.Repeat("1", len(old))
						}
					}
				case float64:
					value = old + 1
				case bool:
					value = !old
				case nil:
					value = map[string]any{}
				case []any:
					value = nil
				default:
					t.Fatalf("unhandled mutation %T", old)
				}
				if m, ok := node.(map[string]any); ok {
					m[path[0]] = value
				} else {
					i, e := strconv.Atoi(path[0])
					must(t, e)
					node.([]any)[i] = value
				}
			}
			replace(changed, row.path)
			raw, e := json.Marshal(changed)
			must(t, e)
			_, decodeErr := journalreader.DecodeEvent(raw)
			_, e = s.pool.Exec(t.Context(), `UPDATE events SET payload=$1::jsonb WHERE queue_id=$2 AND id=$3`, string(raw), q, duplicateID)
			must(t, e)
			t.Cleanup(func() {
				_, e := s.pool.Exec(ctx, `UPDATE events SET payload=$1::jsonb WHERE queue_id=$2 AND id=$3`, string(original), q, duplicateID)
				must(t, e)
			})
			_, e = s.RecoverLanded(t.Context(), q, landedID, recoveryRequest(l))
			if e == nil {
				t.Fatal("altered secondary decision accepted")
			}
			if decodeErr == nil {
				valid++
				if !strings.Contains(e.Error(), "decision binding mismatch") {
					t.Fatal("valid mutation did not reach historical binding check", e)
				}
			} else {
				rejected++
			}
			if eventCount(t, s, q) != before {
				t.Fatal("failed recovery appended evidence")
			}
		})
	}
	_, err = s.RecoverLanded(t.Context(), q, landedID, recoveryRequest(l))
	must(t, err)
	t.Logf("decision mutation table: %d rejected (%d structurally valid, %d strict-wire refusals); identical secondary duplicate accepted", valid+rejected, valid, rejected)
}

func TestRecoverLandedReferencesReconstructedInventoryInBundle(t *testing.T) {
	for _, scenario := range []string{"valid", "extra-inventory", "extra-dependency", "extra-companion", "wrong-generation", "wrong-pinned-base", "altered-inventory"} {
		t.Run(scenario, func(t *testing.T) {
			s := newStore(t)
			q, claim, c, d := decisionLand(t, s)
			must(t, startWithSide(t.Context(), s, claim, c, d, landingSide(t, c)))
			landedID := appendEvents(t, s, q, queue.Landed{BatchID: c.Start.BatchID, NewBaseSHA: d.LandedSHA, NewFence: "next"})
			l := recoveredCompanion(t, s, q)
			var raw []byte
			must(t, s.pool.QueryRow(t.Context(), `SELECT payload FROM events WHERE queue_id=$1 AND id=$2`, q, l.Members[0].ObservationIDs[0]).Scan(&raw))
			e, err := pj.DecodeEvent(raw)
			must(t, err)
			h := e.(pj.Observation)
			h.PreviousObservationID = l.Members[0].ObservationIDs[0]
			h.Request.Generation = "reconstructed-for-recovery"
			if scenario == "wrong-pinned-base" {
				h.Request.PinnedBase = strings.Repeat("8", 40)
			}
			if scenario == "altered-inventory" {
				h.Limits.SourceCommits--
			}
			h.Digest = pj.InventoryDigest(h, h.Commits)
			count := 2
			if strings.HasPrefix(scenario, "extra-") {
				count++
			}
			_, companionRaw, err := pj.EncodeEvent(l)
			must(t, err)
			var token [32]byte
			token[0] = 1
			binding, err := dependency.NewJournalIdentity("openmerge-db-v2:sha256:"+strings.Repeat("a", 64), token, "github:https://github.com", "7")
			must(t, err)
			request := SideAppend{Count: count, Binding: binding, Build: func(ids []uint64) ([]journalreader.Event, error) {
				event, err := pj.DecodeEvent(companionRaw)
				if err != nil {
					return nil, err
				}
				l := event.(pj.Landing)
				m := &l.Members[0]
				m.Request, m.Mapping.Request = h.Request, h.Request
				m.ObservationIDs = []uint64{ids[0]}
				m.InventoryDigest = h.Digest
				for i := range m.Layers {
					m.Layers[i].References = append(m.Layers[i].References, ids[0])
				}
				m.RecordDigest = pj.MemberDigest(*m)
				events := []journalreader.Event{{Provenance: h}}
				switch scenario {
				case "extra-inventory":
					extra := sideInventory(t)
					extra.Request.ChangeID = "unrelated"
					extra.Digest = pj.InventoryDigest(extra, extra.Commits)
					events = append(events, journalreader.Event{Provenance: extra})
				case "extra-dependency":
					events = append(events, journalreader.Event{Dependency: binding})
				case "extra-companion":
					events = append(events, journalreader.Event{Provenance: l})
				case "wrong-generation":
					m.Request.Generation = "not-the-reconstructed-generation"
					m.Mapping.Request = m.Request
					m.RecordDigest = pj.MemberDigest(*m)
				}
				return append(events, journalreader.Event{Provenance: l}), nil
			}}
			before := eventCount(t, s, q)
			_, err = s.RecoverLanded(t.Context(), q, landedID, request)
			if scenario != "valid" {
				if err == nil || !strings.Contains(err.Error(), "landed recovery") {
					t.Fatal("invalid reconstruction admitted", err)
				}
				if eventCount(t, s, q) != before {
					t.Fatal("invalid reconstruction wrote evidence")
				}
				return
			}
			must(t, err)
			// Canonical retries must validate against the same historical inventory.
			_, err = s.RecoverLanded(t.Context(), q, landedID, request)
			must(t, err)
			reader, _, err := replayJournal(t.Context(), s.pool, q, queue.State{BaseBranch: "release"}, 0, true)
			must(t, err)
			if len(reader.Provenance.Records()) != 0 {
				t.Fatal("reconstructed recovery did not drain evidence")
			}
		})
	}
}
