package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

func isProvenance(typ string) bool {
	return typ == "change_provenance_observed" || typ == "landing_provenance_recorded"
}

func evidenceKindsVersion(typ string, version int) []string {
	if version == 2 && isProvenance(typ) {
		return []string{"provenance"}
	}
	return evidenceKinds(typ)
}

// Choose only after closure: an in-range legacy decision can depend on an
// out-of-range companion. The spool keeps this check independent of range size.
func exportVersion(path string, requested int) (int, error) {
	c, err := openSpool(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = c.f.Close() }()
	version := 1
	for c.ok {
		if isProvenance(c.value.Source.Type) {
			version = 2
		}
		if err = c.next(); err != nil {
			return 0, err
		}
	}
	if requested == 1 && version == 2 {
		return 0, errIntegrity
	}
	if requested == 2 {
		version = 2
	}
	return version, nil
}

func resolveEvidenceVersion(ctx context.Context, tx pgx.Tx, event sourceEvent, maxID int64, version int) ([]Reference, []sourceEvent, error) {
	if version == 1 {
		return resolveEvidenceAt(ctx, tx, event, maxID)
	}
	r := &evidenceResolver{ctx: ctx, tx: tx, event: event, maxID: maxID,
		owner: EvidenceOwner{QueueID: event.source.QueueID, EventID: event.source.ID}, refs: []Reference{}, sources: []sourceEvent{}}
	if isProvenance(event.source.Type) {
		if err := r.provenance(); err != nil {
			return nil, nil, err
		}
	} else {
		refs, deps, err := resolveEvidenceAt(ctx, tx, event, maxID)
		if err != nil {
			return nil, nil, err
		}
		r.refs, r.sources = refs, deps
		if event.source.Type == "landing_decision_recorded" {
			// Invalid stages must not disappear behind the stage-specific selectors.
			invalid, err := r.lookup("provenance", "landing_provenance_recorded", evidenceIdentity("invalid-stage"),
				`payload->'data'->>'decision_id'=$3 AND COALESCE(payload->'data'->>'stage','') NOT IN ('tested','landed')`, false, fmt.Sprint(event.source.ID))
			if err != nil || invalid != nil {
				return nil, nil, errSource
			}
			r.refs = r.refs[:len(r.refs)-1]
			// No synthetic absence edge: legacy-only selections stay frozen v1.
			for _, stage := range []string{"tested", "landed"} {
				_, err := r.lookup("provenance", "landing_provenance_recorded", evidenceIdentity("decision", fmt.Sprint(event.source.ID), stage),
					`payload->'data'->>'decision_id'=$3 AND payload->'data'->>'stage'=$4`, false, fmt.Sprint(event.source.ID), stage)
				if err != nil {
					return nil, nil, err
				}
				if r.refs[len(r.refs)-1].Source == nil {
					r.refs = r.refs[:len(r.refs)-1]
				}
			}
		}
	}
	refs, err := normalizeReferences(r.refs)
	return refs, r.sources, err
}

func decodeProvenance(event sourceEvent) (pj.Event, error) {
	e, err := pj.DecodeEvent(event.payload)
	if err != nil {
		return nil, errSource
	}
	typ, _, err := pj.EncodeEvent(e)
	if err != nil || typ != event.source.Type {
		return nil, errSource
	}
	return e, nil
}

// Validate the bounded retained provenance prefix with slice 3a's pure fold.
// This is export-local, not a live reader migration. Pagination limits buffers;
// the codec's record/byte limits bound fold state and fail closed on excess.
func (r *evidenceResolver) provenancePrefix(through int64) error {
	var state pj.State
	var after int64
	for {
		rows, err := r.tx.Query(r.ctx, `SELECT id,type,recorded_at,CASE WHEN octet_length(payload::text)<=4194304 THEN payload::text ELSE NULL END FROM events WHERE queue_id=$1 AND id>$2 AND id<=$3 AND type IN ('change_provenance_observed','landing_provenance_recorded') ORDER BY id LIMIT 32`, r.event.source.QueueID, after, through)
		if err != nil {
			return errSource
		}
		count := 0
		for rows.Next() {
			var event sourceEvent
			var at time.Time
			if rows.Scan(&event.source.ID, &event.source.Type, &at, &event.payload) != nil {
				rows.Close()
				return errSource
			}
			e, err := decodeProvenance(event)
			if err != nil {
				rows.Close()
				return err
			}
			state, err = pj.Apply(state, uint64(event.source.ID), e)
			if err != nil {
				rows.Close()
				return errSource
			}
			after = event.source.ID
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return errSource
		}
		if count < pageSize {
			return nil
		}
	}
}

func (r *evidenceResolver) provenance() error {
	e, err := decodeProvenance(r.event)
	if err != nil {
		return err
	}
	through := r.event.source.ID
	switch v := e.(type) {
	case pj.Observation:
		// Retain all chunks, including cross-range siblings, and the previous
		// generation fence. Exact Request equality includes head/base/exclusions.
		request, err := json.Marshal(v.Request)
		if err != nil {
			return errSource
		}
		for i := 0; i < v.Chunks; i++ {
			chunk, err := r.lookup("provenance", "change_provenance_observed", evidenceIdentity("inventory", v.Request.ChangeID, v.Request.Generation, fmt.Sprint(i)),
				`payload->'data'->'request'=$3::jsonb AND payload->'data'->>'index'=$4`, true, string(request), fmt.Sprint(i))
			if err != nil {
				return err
			}
			if chunk.source.ID > through {
				through = chunk.source.ID
			}
		}
		if v.PreviousObservationID != 0 {
			prior, err := r.provenanceID(v.PreviousObservationID, "change_provenance_observed", "previous")
			if err != nil || prior.source.ID >= r.event.source.ID {
				return errSource
			}
		}
	case pj.Landing:
		r.owner.BatchID, r.owner.AttemptID = v.BatchID, v.AttemptID
		decision, err := r.provenanceID(v.DecisionID, "landing_decision_recorded", "decision")
		if err != nil || decision.source.ID >= r.event.source.ID {
			return errSource
		}
		decoded, err := decodeEvidence(decision)
		if err != nil {
			return err
		}
		d := decoded.(queue.LandingDecisionRecorded)
		if d.BatchID != v.BatchID || d.AttemptID != v.AttemptID {
			return errSource
		}
		// The queue decision is for one member. Its identity must occur in
		// the companion rather than merely sharing an attempt/build string.
		change, revision, head := "", uint64(0), ""
		if d.Approval != nil {
			change, revision, head = string(d.Approval.Basis.ChangeID), uint64(d.Approval.Basis.Revision), d.Approval.Basis.Head
		} else if d.Remedy != nil {
			change, revision, head = string(d.Remedy.ChangeID), uint64(d.Remedy.Revision), d.Remedy.Head
		}
		matched := false
		for _, m := range v.Members {
			if m.Request.ChangeID == change && m.Request.Revision == fmt.Sprint(revision) && m.Request.Head == head {
				matched = true
			}
			result := d.TestedSHA
			if v.Stage == "landed" {
				result = d.LandedSHA
			}
			if m.Mapping.Build.Result != result {
				return errSource
			}
			for _, id := range m.ObservationIDs {
				if _, err := r.provenanceID(id, "change_provenance_observed", "inventory"); err != nil {
					return err
				}
			}
			for _, layer := range m.Layers {
				for _, id := range layer.References {
					if id >= uint64(r.event.source.ID) {
						return errSource
					}
					if _, err := r.provenanceID(id, "", "layer"); err != nil {
						return err
					}
				}
			}
		}
		if !matched {
			return errSource
		}
		if v.Stage == "landed" {
			_, err := r.lookup("provenance", "landing_provenance_recorded", evidenceIdentity(v.AttemptID, "tested"),
				`payload->'data'->>'attempt_id'=$3 AND payload->'data'->>'stage'='tested' AND id<$4`, true, v.AttemptID, r.event.source.ID)
			if err != nil {
				return err
			}
		}
	}
	if err := r.provenancePrefix(through); err != nil {
		return err
	}
	for i := range r.refs {
		r.refs[i].Owner = r.owner
	}
	return nil
}

// All external targets are exact IDs in the same authorized queue. ADR 0011
// dependency targets are deliberately unsupported until its scoped audit slice.
func (r *evidenceResolver) provenanceID(id uint64, typ, relation string) (*sourceEvent, error) {
	if id == 0 || id > uint64(^uint64(0)>>1) {
		return nil, errSource
	}
	page := []Selection{{Source: Source{QueueID: r.event.source.QueueID, ID: int64(id)}}}
	var found sourceEvent
	if err := fetchPage(r.ctx, r.tx, page, func(e sourceEvent, _ *Selection) error { found = e; return nil }); err != nil {
		return nil, err
	}
	if found.source.Queue != r.event.source.Queue || r.maxID != 0 && found.source.ID > r.maxID || typ != "" && found.source.Type != typ {
		return nil, errSource
	}
	kinds := evidenceKindsVersion(found.source.Type, 2)
	if len(kinds) != 1 {
		return nil, errSource
	}
	if isProvenance(found.source.Type) {
		if _, err := decodeProvenance(found); err != nil {
			return nil, err
		}
	} else if _, err := decodeEvidence(&found); err != nil {
		return nil, err
	}
	r.refs = append(r.refs, Reference{Kind: kinds[0], Relation: "relied_on", Status: "recorded", Owner: r.owner,
		ExpectedType: found.source.Type, Identity: evidenceIdentity(relation, fmt.Sprint(id)), Source: &found.source})
	r.sources = append(r.sources, found)
	return &found, nil
}
