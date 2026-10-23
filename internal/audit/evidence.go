package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/jackc/pgx/v5"
)

// EvidenceOwner pins the decision and its immutable member/attempt identity.
type EvidenceOwner struct {
	QueueID   int64  `json:"queue_id"`
	EventID   int64  `json:"event_id"`
	BatchID   string `json:"batch_id"`
	AttemptID string `json:"attempt_id"`
	ChangeID  string `json:"change_id"`
	Revision  uint64 `json:"revision"`
	Head      string `json:"head"`
	TestedSHA string `json:"tested_sha"`
}

// Reference records an actual retained source or an explicit historical absence.
// Source is nil for absence: an expected identity is never a fabricated event ID.
type Reference struct {
	Kind         string        `json:"kind"`
	Relation     string        `json:"relation"`
	Status       string        `json:"status"`
	Owner        EvidenceOwner `json:"owner"`
	ExpectedType string        `json:"expected_type"`
	Identity     string        `json:"identity"`
	Generation   uint64        `json:"generation"`
	RunID        uint64        `json:"run_id"`
	Attempt      uint64        `json:"attempt"`
	Source       *Source       `json:"source"`
}

var evidenceKind = map[string]string{"landing_decision_recorded": "settlement", "change_revision_observed": "classification", "batch_started": "check", "test_ref_built": "check", "approval_eligibility_observed": "eligibility", "change_classified": "classification", "change_accountability_observed": "accountability", "approval_receipt_recorded": "receipt", "review_stance_observed": "receipt", "policy_authority_applied": "authority", "landing_checks_recorded": "check", "checks_observed": "check", "checks_completed": "check", "check_rerun_requested": "check", "check_rerun_dispatch_progress": "check", "check_rerun_dispatched": "check", "checks_failure_confirmed": "check", "landing_started": "settlement", "landed": "settlement", "landing_abandoned": "settlement"}

func evidenceKinds(typ string) []string {
	if k := evidenceKind[typ]; k != "" {
		return []string{k}
	}
	return []string{}
}

// References contain only JSON-safe scalar fields; marshaling cannot fail.
// References use canonical JSON byte order as a total comparator. All fields,
// including complete owner/source identities, participate; equal bytes dedup.
func normalizeReferences(refs []Reference) []Reference {
	sort.Slice(refs, func(i, j int) bool {
		a := referenceBytes(refs[i])
		b := referenceBytes(refs[j])
		return bytes.Compare(a, b) < 0
	})
	out := refs[:0]
	var previous []byte
	for _, r := range refs {
		b := referenceBytes(r)
		if !bytes.Equal(b, previous) {
			out = append(out, r)
			previous = b
		}
	}
	return out
}

type evidenceResolver struct {
	ctx       context.Context
	tx        pgx.Tx
	event     sourceEvent
	owner     EvidenceOwner
	refs      []Reference
	sources   []sourceEvent
	maxID     int64
	authority bool
}

// lookup reads one bounded identity result. The caller supplies fixed SQL only;
// values always remain query parameters. A duplicate immutable identity fails.
func (r *evidenceResolver) lookup(kind, typ, identity, predicate string, required bool, args ...any) (*sourceEvent, error) {
	values := []any{r.event.source.QueueID, typ}
	values = append(values, args...)
	bound := r.maxID
	if bound == 0 {
		bound = int64(^uint64(0) >> 1)
	}
	values = append(values, bound)
	prefix := fmt.Sprintf(`WITH retained_events AS NOT MATERIALIZED (SELECT * FROM events WHERE queue_id=$1 AND id<=$%d) `, len(values))
	predicate = strings.ReplaceAll(predicate, "FROM events", "FROM retained_events")
	rows, err := r.tx.Query(r.ctx, prefix+`SELECT id,type,recorded_at,CASE WHEN octet_length(payload::text)<=4194304 THEN payload::text ELSE NULL END FROM retained_events WHERE queue_id=$1 AND type=$2 AND `+predicate+` LIMIT 2`, values...)
	if err != nil {
		return nil, errSource
	}
	var found []sourceEvent
	for rows.Next() {
		e := sourceEvent{source: Source{QueueID: r.event.source.QueueID, Queue: r.event.source.Queue}}
		var at time.Time
		if rows.Scan(&e.source.ID, &e.source.Type, &at, &e.payload) != nil {
			rows.Close()
			return nil, errSource
		}
		e.source.RecordedAt = stamp(at)
		if len(e.payload) == 0 || len(e.payload) > MaxBytes {
			rows.Close()
			return nil, errSource
		}
		found = append(found, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(found) > 1 || required && len(found) != 1 {
		return nil, errSource
	}
	ref := Reference{Kind: kind, Relation: "relied_on", Status: "not_recorded", Owner: r.owner, ExpectedType: typ, Identity: identity}
	if len(found) == 1 {
		ref.Status = "recorded"
		ref.Source = &found[0].source
		r.sources = append(r.sources, found[0])
	}
	r.refs = append(r.refs, ref)
	if len(found) == 0 {
		return nil, nil
	}
	return &found[0], nil
}
func basisPredicate(start int) string {
	return fmt.Sprintf(`payload->'data'->'basis'->>'change_id'=$%d AND payload->'data'->'basis'->>'revision'=$%d AND payload->'data'->'basis'->>'head'=$%d`, start, start+1, start+2)
}
func basisArgs(b queue.ApprovalBasis) []any {
	return []any{string(b.ChangeID), fmt.Sprint(b.Revision), b.Head}
}
func decodeEvidence(e *sourceEvent) (queue.Event, error) {
	if e == nil {
		return nil, nil
	}
	v, err := queue.DecodeEvent(e.payload)
	if err != nil {
		return nil, errSource
	}
	typ, _, err := queue.EncodeEvent(v)
	if err != nil || typ != e.source.Type {
		return nil, errSource
	}
	return v, nil
}

func resolveEvidence(ctx context.Context, tx pgx.Tx, event sourceEvent) ([]Reference, []sourceEvent, error) {
	var maxID int64
	if tx.QueryRow(ctx, `SELECT COALESCE(max(id),0) FROM events WHERE queue_id=$1`, event.source.QueueID).Scan(&maxID) != nil {
		return nil, nil, errSource
	}
	return resolveEvidenceAt(ctx, tx, event, maxID)
}
func resolveEvidenceAt(ctx context.Context, tx pgx.Tx, event sourceEvent, maxID int64) ([]Reference, []sourceEvent, error) {
	r := &evidenceResolver{ctx: ctx, tx: tx, event: event, maxID: maxID, owner: EvidenceOwner{QueueID: event.source.QueueID, EventID: event.source.ID}, refs: []Reference{}, sources: []sourceEvent{}}
	switch event.source.Type {
	case "landing_decision_recorded":
		v, err := decodeEvidence(&event)
		if err != nil {
			return nil, nil, errSource
		}
		d, ok := v.(queue.LandingDecisionRecorded)
		if !ok {
			return nil, nil, errSource
		}
		r.owner.BatchID = d.BatchID
		r.owner.AttemptID = d.AttemptID
		r.owner.TestedSHA = d.TestedSHA
		// Reuse the existing retained eligibility semantics in this exact snapshot.
		retained, err := store.LandingDecisionEvidenceTx(ctx, tx, store.QueueID(event.source.QueueID), d.BatchID, d.AttemptID, &maxID)
		if err != nil {
			return nil, nil, errSource
		}
		matched := false
		for _, item := range retained {
			if item.DecisionEventID == event.source.ID {
				matched = reflect.DeepEqual(item.Decision, d)
			}
		}
		if !matched {
			return nil, nil, errSource
		}
		// The latest pin before this decision governs requiredness. Future
		// activation and historical openmerge pins before rollback cannot govern it.
		pin, err := r.lookup("authority", "policy_authority_applied", "governing", `id=(SELECT max(id) FROM events WHERE queue_id=$1 AND type=$2 AND id<$3)`, false, event.source.ID)
		if err != nil {
			return nil, nil, err
		}
		if pin != nil {
			v, err := decodeEvidence(pin)
			if err != nil {
				return nil, nil, err
			}
			r.authority = v.(queue.PolicyAuthorityApplied).Authority == "openmerge"
		}
		if !r.authority {
			// A github pin records rollback, not applicable openmerge evidence.
			r.refs[len(r.refs)-1].Status = "not_recorded"
			r.refs[len(r.refs)-1].Source = nil
			r.sources = r.sources[:0]
		}
		if a := d.Approval; a != nil {
			b := a.Basis
			r.owner.ChangeID = string(b.ChangeID)
			r.owner.Revision = uint64(b.Revision)
			r.owner.Head = b.Head
			rev, err := r.lookup("classification", "change_revision_observed", evidenceIdentity(string(b.ChangeID), fmt.Sprint(b.Revision)), `payload->'data'->>'change_id'=$3 AND payload->'data'->>'revision'=$4 AND payload->'data'->>'head_sha'=$5 AND id<$6`, true, string(b.ChangeID), fmt.Sprint(b.Revision), b.Head, event.source.ID)
			if err != nil {
				return nil, nil, err
			}
			rv, err := decodeEvidence(rev)
			if err != nil {
				return nil, nil, err
			}
			revision := rv.(queue.ChangeRevisionObserved)
			batch, err := r.lookup("check", "batch_started", d.BatchID, `payload->'data'->>'batch_id'=$3 AND id<$4`, true, d.BatchID, event.source.ID)
			if err != nil {
				return nil, nil, err
			}
			bv, err := decodeEvidence(batch)
			if err != nil {
				return nil, nil, err
			}
			matchedMember := false
			for _, member := range bv.(queue.BatchStarted).PRs {
				if member.PR == revision.PR && member.HeadSHA == b.Head {
					matchedMember = true
				}
			}
			if !matchedMember {
				return nil, nil, errSource
			}

			args := basisArgs(b)
			args = append(args, a.EligibilityReadID, event.source.ID)
			if _, err = r.lookup("eligibility", "approval_eligibility_observed", a.EligibilityReadID, basisPredicate(3)+` AND payload->'data'->'basis'->>'read_id'=$6 AND id<$7`, true, args...); err != nil {
				return nil, nil, err
			}
			id := cp.ReceiptIdentity(a.Identity)
			class, err := r.lookup("classification", "change_classified", evidenceIdentity(id.ChangeID, fmt.Sprint(id.Revision), fmt.Sprint(id.Generation)), `payload->'data'->'evidence'->'Classification'->'Inputs'->>'ChangeID'=$3 AND payload->'data'->'evidence'->'Classification'->'Inputs'->>'Revision'=$4 AND payload->'data'->'evidence'->'Classification'->'Inputs'->>'Generation'=$5 AND id<$6`, true, id.ChangeID, fmt.Sprint(id.Revision), fmt.Sprint(id.Generation), event.source.ID)
			if err != nil {
				return nil, nil, err
			}
			cv, err := decodeEvidence(class)
			if err != nil {
				return nil, nil, err
			}
			c := cv.(queue.ChangeClassified)
			ci := c.Evidence.Classification.Inputs
			if ci.Head != id.Head || ci.Base != id.Base || ci.MergeBase != id.MergeBase || ci.PolicyDigest != id.PolicyDigest || classificationEvidenceDigest(c) != a.ClassificationDigest {
				return nil, nil, errSource
			}
			accArgs := basisArgs(b)
			accArgs = append(accArgs, event.source.ID)
			acc, err := r.lookup("accountability", "change_accountability_observed", evidenceIdentity(string(b.ChangeID), fmt.Sprint(b.Revision)), basisPredicate(3)+` AND id=(SELECT max(id) FROM events WHERE queue_id=$1 AND type=$2 AND `+basisPredicate(3)+` AND id<$6)`, true, accArgs...)
			if err != nil {
				return nil, nil, err
			}
			av, err := decodeEvidence(acc)
			if err != nil {
				return nil, nil, err
			}
			ac := av.(queue.ChangeAccountabilityObserved)
			if ac.PolicyDigest != id.PolicyDigest || ac.Actor != cp.Identity(a.Actor) || ac.Author != cp.Identity(a.Author) || ac.SubmitterID != a.SubmitterID || !accountableHumansEqual(ac.Humans, a) || ac.Recorded != a.AccountabilityRecorded {
				return nil, nil, errSource
			}
			if r.authority {
				value, err := decodeEvidence(pin)
				if err != nil || value.(queue.PolicyAuthorityApplied).GoverningCommit != ci.GoverningCommit {
					return nil, nil, errSource
				}
			}
			for _, src := range a.Sources {
				if src.Receipt == nil {
					args := basisArgs(src.Basis)
					args = append(args, src.Basis.ReadID, src.Source.ID, event.source.ID)
					stance, err := r.lookup("receipt", "review_stance_observed", evidenceIdentity(string(src.Source.Channel), src.Source.ID), basisPredicate(3)+` AND payload->'data'->'basis'->>'read_id'=$6 AND payload->'data'->'stance'->'Source'->>'ID'=$7 AND id<$8`, true, args...)
					if err != nil {
						return nil, nil, err
					}
					sv, err := decodeEvidence(stance)
					if err != nil {
						return nil, nil, err
					}
					s := sv.(queue.ReviewStanceObserved)
					if s.Reviewer != cp.Identity(src.Reviewer) || s.Stance.Source != cp.StanceSource(src.Source) || s.Basis != src.Basis || s.SourceKind != src.SourceKind || s.SourceTime != forge.NativeTime(src.SourceTime) || s.Observation != forge.NativeObservation(src.Observation) || s.OriginalSubmission != forge.NativeTime(src.OriginalSubmission) || s.Stance.Kind != cp.Approve || s.Stance.Head != b.Head || s.Stance.Revision != uint64(b.Revision) {
						return nil, nil, errSource
					}
				}

				if src.Receipt != nil {
					args := basisArgs(src.Basis)
					args = append(args, src.Receipt.Basis.ReadID, src.Source.ID, event.source.ID)
					receipt, err := r.lookup("receipt", "approval_receipt_recorded", src.Source.ID, basisPredicate(3)+` AND payload->'data'->'basis'->>'read_id'=$6 AND payload->'data'->>'comment_id'=$7 AND id<$8`, true, args...)
					if err != nil {
						return nil, nil, err
					}
					rv, err := decodeEvidence(receipt)
					if err != nil {
						return nil, nil, err
					}
					rec := rv.(queue.ApprovalReceiptRecorded)
					_, receiptRaw, receiptErr := queue.EncodeEvent(rec)
					if receiptErr != nil {
						return nil, nil, errSource
					}
					var wire struct {
						Data json.RawMessage `json:"data"`
					}
					if json.Unmarshal(receiptRaw, &wire) != nil {
						return nil, nil, errSource
					}
					expectedReceipt, _ := encode(src.Receipt)
					actualReceipt, _ := Canonical(wire.Data)
					if !rec.Accepted || rec.Supplied != id || !bytes.Equal(expectedReceipt, actualReceipt) {
						return nil, nil, errSource
					}
				}
			}
		}
		if d.Remedy != nil {
			r.owner.ChangeID = string(d.Remedy.ChangeID)
			r.owner.Revision = uint64(d.Remedy.Revision)
			r.owner.Head = d.Remedy.Head
			revisionEvent, revisionErr := r.lookup("classification", "change_revision_observed", evidenceIdentity(r.owner.ChangeID, fmt.Sprint(r.owner.Revision)), `payload->'data'->>'change_id'=$3 AND payload->'data'->>'revision'=$4 AND payload->'data'->>'head_sha'=$5 AND id<$6`, true, r.owner.ChangeID, fmt.Sprint(r.owner.Revision), r.owner.Head, event.source.ID)
			if revisionErr != nil {
				return nil, nil, revisionErr
			}
			revisionValue, revisionErr := decodeEvidence(revisionEvent)
			if revisionErr != nil {
				return nil, nil, revisionErr
			}
			revision := revisionValue.(queue.ChangeRevisionObserved)
			derivedID, revisionErr := queue.PRChangeID(event.source.Queue.Installation, event.source.Queue.Repository, event.source.Queue.Base, event.source.Queue.Lane, revision.PR)
			if revisionErr != nil || string(derivedID) != r.owner.ChangeID {
				return nil, nil, errSource
			}
		}
		if err := r.attemptRelations(d); err != nil {
			return nil, nil, err
		}
	case "landing_checks_recorded":
		v, err := decodeEvidence(&event)
		if err != nil {
			return nil, nil, errSource
		}
		c := v.(queue.LandingChecksRecorded)
		r.owner.BatchID = c.BatchID
		r.owner.AttemptID = c.AttemptID
		r.owner.TestedSHA = c.TestedSHA
		if err := r.checkBasis(c); err != nil {
			return nil, nil, err
		}
		if err := r.checkHistory(c); err != nil {
			return nil, nil, err
		}
	case "landing_started":
		v, err := decodeEvidence(&event)
		if err != nil {
			return nil, nil, errSource
		}
		s := v.(queue.LandingStarted)
		r.owner.BatchID = s.BatchID
		r.owner.AttemptID = s.AttemptID
		if err := r.startRelations(s); err != nil {
			return nil, nil, err
		}
		if err := r.settlement(s.BatchID, s.AttemptID); err != nil {
			return nil, nil, err
		}
	case "landed", "landing_abandoned":
		v, err := decodeEvidence(&event)
		if err != nil {
			return nil, nil, err
		}
		var batch string
		switch s := v.(type) {
		case queue.Landed:
			batch = s.BatchID
		case queue.LandingAbandoned:
			batch = s.BatchID
		}
		r.owner.BatchID = batch
		start, err := r.lookup("settlement", "landing_started", batch, `payload->'data'->>'batch_id'=$3 AND id=(SELECT max(id) FROM events WHERE queue_id=$1 AND type=$2 AND payload->'data'->>'batch_id'=$3 AND id<$4)`, false, batch, event.source.ID)
		if err != nil {
			return nil, nil, err
		}
		if start != nil {
			sv, err := decodeEvidence(start)
			if err != nil {
				return nil, nil, err
			}
			r.owner.AttemptID = sv.(queue.LandingStarted).AttemptID
			r.refs[len(r.refs)-1].Owner = r.owner
		}

	}
	for i := range r.refs {
		r.refs[i].Owner = r.owner
	}
	return normalizeReferences(r.refs), r.sources, nil
}

func (r *evidenceResolver) attemptRelations(d queue.LandingDecisionRecorded) error {
	batch, attempt, tested := d.BatchID, d.AttemptID, d.TestedSHA
	e, err := r.lookup("check", "landing_checks_recorded", evidenceIdentity(batch, attempt), `payload->'data'->>'batch_id'=$3 AND payload->'data'->>'attempt_id'=$4 AND $5::boolean`, r.authority, batch, attempt, r.authority)
	if err != nil {
		return err
	}
	v, err := decodeEvidence(e)
	if err != nil {
		return err
	}
	if v != nil {
		c := v.(queue.LandingChecksRecorded)
		if c.TestedSHA != tested || c.LandedSHA != d.LandedSHA || d.Approval != nil && c.PolicyDigest != d.Approval.Identity.PolicyDigest {
			return errSource
		}
		if r.owner.ChangeID != "" {
			found := false
			for _, m := range c.Members {
				memberID, identityErr := queue.PRChangeID(r.event.source.Queue.Installation, r.event.source.Queue.Repository, r.event.source.Queue.Base, r.event.source.Queue.Lane, m.PR)
				if identityErr == nil && string(memberID) == r.owner.ChangeID && m.HeadSHA == r.owner.Head {
					found = true
				}
			}
			if !found {
				return errSource
			}
		}
	}
	if _, err = r.lookup("settlement", "landing_started", evidenceIdentity(batch, attempt), `payload->'data'->>'batch_id'=$3 AND payload->'data'->>'attempt_id'=$4`, true, batch, attempt); err != nil {
		return err
	}
	return r.settlement(batch, attempt)
}
func (r *evidenceResolver) settlement(batch, attempt string) error {
	var start, next int64
	bound := r.maxID
	if bound == 0 {
		bound = int64(^uint64(0) >> 1)
	}
	if r.tx.QueryRow(r.ctx, `SELECT COALESCE(max(id),0) FROM events WHERE queue_id=$1 AND type='landing_started' AND payload->'data'->>'batch_id'=$2 AND payload->'data'->>'attempt_id'=$3 AND id<=$4`, r.event.source.QueueID, batch, attempt, bound).Scan(&start) != nil || start == 0 {
		return errSource
	}
	if r.tx.QueryRow(r.ctx, `SELECT COALESCE(min(id),0) FROM events WHERE queue_id=$1 AND type='landing_started' AND payload->'data'->>'batch_id'=$2 AND id>$4 AND id<=$3`, r.event.source.QueueID, batch, bound, start).Scan(&next) != nil {
		return errSource
	}

	for _, typ := range []string{"landed", "landing_abandoned"} {
		if _, err := r.lookup("settlement", typ, batch, `payload->'data'->>'batch_id'=$3 AND id>$4 AND ($5::bigint=0 OR id<$5)`, false, batch, start, next); err != nil {
			return err
		}
	}
	return nil
}

func (r *evidenceResolver) checkHistory(c queue.LandingChecksRecorded) error {
	for _, check := range c.Checks {
		identity := evidenceIdentity(check.Source, check.Name)
		// A single latest observation is selected at the immutable decision boundary,
		// never from the current forge or from the latest projection.
		obs, err := r.lookup("check", "checks_observed", identity, checkObservationPredicate, false, c.BatchID, c.TestedSHA, r.event.source.ID, check.Name, check.Source)
		if err != nil {
			return err
		}
		if obs != nil {
			v, err := decodeEvidence(obs)
			if err != nil {
				return err
			}
			o := v.(queue.ChecksObserved)
			newest := newestCheckRun(o.Runs, check.Name, check.Source)
			if newest == nil {
				return errSource
			}
			if check.RunID != nil && *check.RunID != newest.ID || check.Attempt != nil && max(*check.Attempt, 1) != max(newest.Attempt, 1) || check.WorkflowRunID != nil && *check.WorkflowRunID != newest.WorkflowRunID || string(check.Result) != newest.State {
				return errSource
			}
			ref := &r.refs[len(r.refs)-1]
			ref.RunID = newest.ID
			ref.Attempt = max(newest.Attempt, 1)
		}
		// Retry generation and dispatch outcome are retained independently of the
		// observation. Missing historical retry instrumentation remains explicit.
		var workflow uint64
		if check.WorkflowRunID != nil {
			workflow = *check.WorkflowRunID
		}
		var generation uint64
		for _, typ := range []string{"check_rerun_requested", "check_rerun_dispatch_progress", "check_rerun_dispatched"} {
			match := ` AND EXISTS (SELECT 1 FROM jsonb_array_elements(payload->'data'->'runs') run WHERE run->>'name'=$6 AND run->>'source'=$7 AND ($8='0' OR run->>'workflow_run_id'=$8))`
			args := []any{c.BatchID, c.TestedSHA, r.event.source.ID, check.Name, check.Source, fmt.Sprint(workflow)}
			if typ != "check_rerun_requested" {
				// Dispatch belongs to the selected request generation, rather
				// than another check's unrelated retry in this batch.
				match = ` AND payload->'data'->>'generation'=$6`
				args = []any{c.BatchID, c.TestedSHA, r.event.source.ID, fmt.Sprint(generation)}
				if typ == "check_rerun_dispatch_progress" {
					match += ` AND ($7='0' OR payload->'data'->'target'->>'workflow_run_id'=$7)`
					args = append(args, fmt.Sprint(workflow))
				}
			}
			predicate := `payload->'data'->>'batch_id'=$3 AND payload->'data'->>'tested_sha'=$4` + match
			e, err := r.lookup("check", typ, identity, predicate+` AND id=(SELECT max(id) FROM events WHERE queue_id=$1 AND type=$2 AND `+predicate+` AND id<$5)`, false, args...)
			if err != nil {
				return err
			}
			if e != nil {
				v, err := decodeEvidence(e)
				if err != nil {
					return err
				}
				var g uint64
				switch x := v.(type) {
				case queue.CheckRerunRequested:
					g = x.Generation
				case queue.CheckRerunDispatchProgress:
					g = x.Generation
					if x.Target.Accepted && check.WorkflowRunID != nil && x.Target.WorkflowRunID == *check.WorkflowRunID && check.Attempt != nil && *check.Attempt <= x.Target.Attempt {
						return errSource
					}
				case queue.CheckRerunDispatched:
					g = x.Generation
				}
				if typ == "check_rerun_requested" {
					generation = g
				}
				if !sameGeneration(g, generation) {
					return errSource
				}
				r.refs[len(r.refs)-1].Generation = g
			}
		}

		// The first later event ID wins, even when run/attempt ties. It cannot
		// replace the governing observation. Explicitly identify the retry boundary, which cannot
		// replace the governing observation even when its result changes.
		later, err := r.lookup("check", "checks_observed", evidenceIdentity(check.Source, check.Name, "later"), `payload->'data'->>'batch_id'=$3 AND payload->'data'->>'tested_sha'=$4 AND id=(SELECT min(id) FROM events WHERE queue_id=$1 AND type=$2 AND payload->'data'->>'batch_id'=$3 AND payload->'data'->>'tested_sha'=$4 AND id>$5 AND EXISTS (SELECT 1 FROM jsonb_array_elements(payload->'data'->'runs') run WHERE run->>'name'=$6 AND run->>'source'=$7))`, false, c.BatchID, c.TestedSHA, r.event.source.ID, check.Name, check.Source)
		if err != nil {
			return err
		}
		if later != nil {
			r.refs[len(r.refs)-1].Relation = "excluded_later"
		}
	}
	return nil
}

func validReferences(refs []Reference) bool {
	if refs == nil {
		return false
	}
	for i, r := range refs {
		if r.Owner.QueueID <= 0 || r.Owner.EventID <= 0 || r.ExpectedType == "" || r.Identity == "" || r.Relation != "relied_on" && r.Relation != "excluded_later" {
			return false
		}
		kinds := evidenceKinds(r.ExpectedType)
		if len(kinds) != 1 || kinds[0] != r.Kind {
			return false
		}
		switch r.Status {
		case "recorded":
			if r.Source == nil || r.Source.ID <= 0 || r.Source.QueueID != r.Owner.QueueID || r.Source.Type != r.ExpectedType {
				return false
			}
		case "not_recorded":
			if r.Source != nil || r.Relation != "relied_on" {
				return false
			}
		default:
			return false
		}
		if i > 0 {
			a := referenceBytes(refs[i-1])
			b := referenceBytes(r)
			if bytes.Compare(a, b) >= 0 {
				return false
			}
		}
	}
	return true
}

// Match the decision digest's diagnostic-free classification identity. Clone
// before stripping diagnostics so retained payloads remain untouched.
func classificationEvidenceDigest(e queue.ChangeClassified) string {
	raw, _ := json.Marshal(e.Evidence.Classification)
	var c cp.Classification
	_ = json.Unmarshal(raw, &c)
	c.Reasons = nil
	for i := range c.Files {
		c.Files[i].Reasons = nil
	}
	for i := range c.CodeOwners {
		c.CodeOwners[i].Eligible.Reason = ""
	}
	for i := range c.PolicyOwners {
		c.PolicyOwners[i].Eligible.Reason = ""
	}
	raw, _ = json.Marshal(c)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func accountableHumansEqual(humans []cp.Identity, a *queue.LandingApprovalDecision) bool {
	if len(humans) != len(a.AccountableHumans) {
		return false
	}
	for i, h := range humans {
		if h != cp.Identity(a.AccountableHumans[i]) {
			return false
		}
	}
	return true
}

func (r *evidenceResolver) startRelations(start queue.LandingStarted) error {
	items, err := store.LandingDecisionEvidenceTx(r.ctx, r.tx, store.QueueID(r.event.source.QueueID), start.BatchID, start.AttemptID, &r.maxID)
	if err != nil {
		return errSource
	}
	if len(items) == 0 {
		_, err = r.lookup("settlement", "landing_decision_recorded", evidenceIdentity(start.BatchID, start.AttemptID), `payload->'data'->>'batch_id'=$3 AND payload->'data'->>'attempt_id'=$4`, false, start.BatchID, start.AttemptID)
		return err
	}
	for _, item := range items {
		if _, err := r.lookup("settlement", "landing_decision_recorded", fmt.Sprint(item.DecisionEventID), `id=$3`, true, item.DecisionEventID); err != nil {
			return err
		}
	}
	return nil
}

func (r *evidenceResolver) checkBasis(c queue.LandingChecksRecorded) error {
	batch, err := r.lookup("check", "batch_started", c.BatchID, `payload->'data'->>'batch_id'=$3 AND id<$4`, true, c.BatchID, r.event.source.ID)
	if err != nil {
		return err
	}
	v, err := decodeEvidence(batch)
	if err != nil {
		return err
	}
	members := v.(queue.BatchStarted).PRs
	if len(members) != len(c.Members) {
		return errSource
	}
	for _, member := range c.Members {
		found := false
		for _, batchMember := range members {
			if batchMember.PR == member.PR && batchMember.HeadSHA == member.HeadSHA {
				found = true
			}
		}
		if !found {
			return errSource
		}
	}
	built, err := r.lookup("check", "test_ref_built", evidenceIdentity(c.BatchID, c.TestedSHA), `payload->'data'->>'batch_id'=$3 AND payload->'data'->>'tested_sha'=$4 AND id=(SELECT max(id) FROM events WHERE queue_id=$1 AND type=$2 AND payload->'data'->>'batch_id'=$3 AND id<$5)`, true, c.BatchID, c.TestedSHA, r.event.source.ID)
	if err != nil {
		return err
	}
	v, err = decodeEvidence(built)
	if err != nil {
		return err
	}
	b := v.(queue.TestRefBuilt)
	if b.Method != "" && b.Method != c.Method {
		return errSource
	}
	return nil
}

// JSON string tuples preserve component boundaries even for slashes and suffixes.
func evidenceIdentity(parts ...string) string {
	raw, _ := json.Marshal(parts) // A string tuple is always JSON encodable.
	return string(raw)
}

func referenceBytes(ref Reference) []byte {
	raw, err := encode(ref)
	if err != nil {
		panic("audit: invalid internal reference encoding")
	}
	return raw
}

// Run identity precedes normalized attempt; event ID breaks equal tuples in SQL.
func newestCheckRun(runs []queue.CheckRun, name, source string) *queue.CheckRun {
	var newest *queue.CheckRun
	for _, run := range runs {
		if run.Name != name || run.Source != source {
			continue
		}
		if newest == nil || run.ID > newest.ID || run.ID == newest.ID && max(run.Attempt, 1) > max(newest.Attempt, 1) {
			candidate := run
			newest = &candidate
		}
	}
	return newest
}

func sameGeneration(actual, expected uint64) bool { return actual == expected }

const checkObservationPredicate = `payload->'data'->>'batch_id'=$3 AND payload->'data'->>'tested_sha'=$4 AND id=(SELECT e.id FROM events e WHERE e.queue_id=$1 AND e.type=$2 AND e.payload->'data'->>'batch_id'=$3 AND e.payload->'data'->>'tested_sha'=$4 AND e.id<$5 AND EXISTS (SELECT 1 FROM jsonb_array_elements(e.payload->'data'->'runs') run WHERE run->>'name'=$6 AND run->>'source'=$7) ORDER BY (SELECT ARRAY[(run->>'id')::numeric,greatest((run->>'attempt')::numeric,1)] FROM jsonb_array_elements(e.payload->'data'->'runs') run WHERE run->>'name'=$6 AND run->>'source'=$7 ORDER BY (run->>'id')::numeric DESC,greatest((run->>'attempt')::numeric,1) DESC LIMIT 1) DESC,e.id DESC LIMIT 1)`
