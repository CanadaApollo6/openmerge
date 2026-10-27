package audit

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	p "github.com/CanadaApollo6/openmerge/internal/provenance"
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
		return 0, ErrProvenanceVersion
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
			for _, stage := range []string{"tested", "landed"} {
				if _, err := r.provenanceLookup(evidenceIdentity("decision", fmt.Sprint(event.source.ID), stage), false); err != nil {
					return nil, nil, err
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

func (r *evidenceResolver) provenance() error {
	e, err := decodeProvenance(r.event)
	if err != nil {
		return err
	}
	switch v := e.(type) {
	case pj.Observation:
		if v.Index == 0 {
			prior, err := r.previousInventory(v.Request, r.event.source.ID)
			if err != nil || prior != v.PreviousObservationID {
				return errSource
			}
		}
		// Retain all chunks, including cross-range siblings, and the previous
		// generation fence. Exact Request equality includes head/base/exclusions.
		for i := 0; i < v.Chunks; i++ {
			if _, err := r.provenanceLookup(inventoryLookupIdentity(v, i), true); err != nil {
				return err
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
			latest, err := r.previousInventory(m.Request, r.event.source.ID)
			if err != nil || latest != m.ObservationIDs[0] {
				return errSource
			}
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
			_, err := r.provenanceLookup(evidenceIdentity(v.AttemptID, "tested"), true)
			if err != nil {
				return err
			}
		}
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

// ErrProvenanceVersion explains why an explicit legacy export cannot be emitted.
var ErrProvenanceVersion = errors.New("audit: provenance requires export version 2")

type provenanceIndexKey struct{}
type provenanceIndex struct{ dir string }

// Index raw immutable journal rows once per queue/snapshot, independently of any
// retained side projection. Result rows and lookup files are individually bounded;
// neither historical payloads nor an in-memory identity map accumulate.
func (p provenanceIndex) queue(ctx context.Context, tx pgx.Tx, source Source, maxID int64) (string, error) {
	dir := filepath.Join(p.dir, fmt.Sprintf("%d-%d", source.QueueID, maxID))
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	if os.Mkdir(dir, 0700) != nil {
		return "", errOutput
	}
	rows, err := tx.Query(ctx, provenanceIndexSQL, source.QueueID, maxID)
	if err != nil {
		return "", errSource
	}
	defer rows.Close()
	for rows.Next() {
		event := sourceEvent{source: Source{QueueID: source.QueueID, Queue: source.Queue}}
		var at time.Time
		if rows.Scan(&event.source.ID, &event.source.Type, &at, &event.payload) != nil {
			return "", errSource
		}
		event.source.RecordedAt = stamp(at)
		// Decode every companion-shaped row before using its identity. Thus
		// 170, 170.0, 1.70e2 and malformed/missing IDs cannot hide evidence.
		e, err := decodeProvenance(event)
		if err != nil {
			return "", err
		}
		var keys []Identity
		switch v := e.(type) {
		case pj.Observation:
			keys = []Identity{inventoryLookupIdentity(v, v.Index)}
			if v.Index == 0 {
				path := filepath.Join(dir, inventoryHistoryKey(v.Request))
				f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
				if err != nil {
					return "", errOutput
				}
				raw := binary.BigEndian.AppendUint64(nil, uint64(event.source.ID))
				_, err = f.Write(raw)
				closeErr := f.Close()
				if err != nil || closeErr != nil {
					return "", errOutput
				}
			}
		case pj.Landing:
			keys = []Identity{evidenceIdentity("decision", fmt.Sprint(v.DecisionID), v.Stage), evidenceIdentity(v.AttemptID, v.Stage)}
		}
		for _, key := range keys {
			path := filepath.Join(dir, digest([]byte(key)))
			if _, err := os.Stat(path); err == nil {
				// A bounded duplicate marker; unrelated lookups still work.
				if os.WriteFile(path, nil, 0600) != nil {
					return "", errOutput
				}
				continue
			}
			if err := spoolFile(path, func(out io.Writer) error {
				return spoolWrite(out, spoolRecord{Source: event.source, Payload: event.payload})
			}); err != nil {
				return "", err
			}
		}
	}
	if rows.Err() != nil {
		return "", errSource
	}
	return dir, nil
}

const provenanceIndexSQL = `SELECT id,type,recorded_at,CASE WHEN octet_length(payload::text)<=1048576 THEN payload::text ELSE NULL END FROM events WHERE queue_id=$1 AND id<=$2 AND type IN ('change_provenance_observed','landing_provenance_recorded') ORDER BY id`

func inventoryLookupIdentity(v pj.Observation, index int) Identity {
	raw, _ := json.Marshal(v.Request)
	return evidenceIdentity("inventory-request", string(raw), fmt.Sprint(index))
}

func (r *evidenceResolver) provenanceLookup(key Identity, required bool) (*sourceEvent, error) {
	index, ok := r.ctx.Value(provenanceIndexKey{}).(provenanceIndex)
	if !ok {
		return nil, errSource
	}
	dir, err := index.queue(r.ctx, r.tx, r.event.source, r.maxID)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, digest([]byte(key)))
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if required {
			return nil, errSource
		}
		return nil, nil
	}
	c, err := openSpool(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.f.Close() }()
	if !c.ok {
		return nil, errSource
	}
	found := c.value.event()
	decoded, err := decodeProvenance(found)
	if err != nil {
		return nil, err
	}
	identity := key
	if v, ok := decoded.(pj.Observation); ok {
		identity = evidenceIdentity("inventory", v.Request.ChangeID, v.Request.Generation, fmt.Sprint(v.Index))
	}
	r.refs = append(r.refs, Reference{Kind: "provenance", Relation: "relied_on", Status: "recorded", Owner: r.owner,
		ExpectedType: found.source.Type, Identity: identity, Source: &found.source})
	r.sources = append(r.sources, found)
	return &found, nil
}

// The closed spool is already in (queue,id) order. Fold each selected provenance
// record exactly once; unrelated history is never folded. Temporary per-queue
// fold state is disk-backed so the projection's 4096-record retention ceiling
// cannot become an export lifetime or selection-size ceiling.
func validateProvenanceClosure(path, dir string) error {
	c, err := openSpool(path)
	if err != nil {
		return err
	}
	defer func() { _ = c.f.Close() }()
	var queueID int64
	var fold *pj.ClosureFold
	for c.ok {
		event := c.value.event()
		if isProvenance(event.source.Type) {
			if fold == nil || queueID != event.source.QueueID {
				queueID = event.source.QueueID
				storeDir := filepath.Join(dir, fmt.Sprintf("fold-%d", queueID))
				if os.Mkdir(storeDir, 0700) != nil {
					return errOutput
				}
				fold = &pj.ClosureFold{Store: provenanceFoldStore{storeDir}}
			}
			e, err := decodeProvenance(event)
			if err != nil {
				return err
			}
			if err := fold.Apply(uint64(event.source.ID), e); err != nil {
				return errSource
			}
		}
		if err := c.next(); err != nil {
			return err
		}
	}
	return nil
}

type provenanceFoldStore struct{ dir string }

func (s provenanceFoldStore) Get(key string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, digest([]byte(key))))
	if os.IsNotExist(err) {
		return nil, nil
	}
	return raw, err
}
func (s provenanceFoldStore) Put(key string, raw []byte) error {
	return os.WriteFile(filepath.Join(s.dir, digest([]byte(key))), raw, 0600)
}

// Preserve raw-journal generation fences even when an intervening observation
// is outside closure or has been pruned from the side projection. Fixed-width
// ID files allow bounded binary searches without re-scanning per-change history.
func inventoryHistoryKey(r p.InventoryRequest) string {
	return "history-" + digest([]byte(evidenceIdentity(r.Repository, r.Queue, r.ChangeID)))
}
func (r *evidenceResolver) previousInventory(request p.InventoryRequest, before int64) (uint64, error) {
	index, ok := r.ctx.Value(provenanceIndexKey{}).(provenanceIndex)
	if !ok {
		return 0, errSource
	}
	dir, err := index.queue(r.ctx, r.tx, r.event.source, r.maxID)
	if err != nil {
		return 0, err
	}
	f, err := os.Open(filepath.Join(dir, inventoryHistoryKey(request)))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, errOutput
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.Size()%8 != 0 {
		return 0, errOutput
	}
	var readErr error
	read := func(i int) uint64 {
		var raw [8]byte
		if _, err := f.ReadAt(raw[:], int64(i)*8); err != nil {
			readErr = err
		}
		return binary.BigEndian.Uint64(raw[:])
	}
	i := sort.Search(int(info.Size()/8), func(i int) bool { return read(i) >= uint64(before) })
	var id uint64
	if i > 0 {
		id = read(i - 1)
	}
	if readErr != nil {
		return 0, errOutput
	}
	return id, nil
}
