package serve

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// ObserveWriteRestriction is dormant until PR 9 installs the authority hook.
// The initial Load completes before forge I/O; Append owns its own short lock.
// The caller must complete the first-emission drain/backup procedure before
// enabling this hook. A failed read never refreshes or clears durable evidence.
func ObserveWriteRestriction(ctx context.Context, db *store.Store, q store.QueueID, reader forge.WriteRestrictionReader, in forge.WriteRestrictionInput) (int64, error) {
	state, _, err := db.Load(ctx, q)
	if err != nil {
		return 0, err
	}
	pin := state.AuthorityPin()
	if pin == nil || pin.OpenMerge == nil || !reflect.DeepEqual(pin.OpenMerge.Binding, in.Binding) || state.ConfigDigest != in.Binding.EffectiveDigest {
		return 0, errors.New("observer requires the currently applied authority pin")
	}
	basis := state.RulesSourceRevision(queue.WriteRestriction)
	var evidence authority.Observation
	if full, ok := reader.(forge.WriteRestrictionInventoryReader); ok {
		evidence, err = full.ObserveAuthorityInventory(ctx, in)
	} else {
		evidence, err = reader.ObserveWriteRestriction(ctx, in)
	}
	readErr := err
	if readErr != nil {
		evidence.Inventory = authority.ReadFailure
	}
	evidence, err = forge.InterpretWriteRestriction(in, evidence)
	if err != nil {
		return 0, err
	}
	if evidence.Inventory == authority.ReadFailure && len(evidence.Findings) == 0 {
		return 0, errors.Join(readErr, forge.ErrRulesUnavailable)
	}
	id, appendErr := db.Append(ctx, q, []queue.Event{queue.WriteRestrictionObserved{Binding: in.Binding, Basis: basis, ObservedAt: time.Now().UTC(), Evidence: evidence}}, "")
	return id, errors.Join(readErr, appendErr)
}

// authorityObservationReader adapts the private lifecycle acquisition seam to
// the shared interpreter/fold. It cannot be installed by persisted evidence.
type authorityObservationReader func(context.Context) (authority.Observation, error)

func (read authorityObservationReader) ObserveWriteRestriction(ctx context.Context, _ forge.WriteRestrictionInput) (authority.Observation, error) {
	return read(ctx)
}

// observeWorkerAuthority runs once per reconciliation, including the first
// cycle and unadopted pins. Status requests never trigger acquisition. Rollback
// disables new reads without changing warning history or unrelated sources.
func (s *Server) observeWorkerAuthority(ctx context.Context, w *worker) error {
	if !w.AuthorityRequired || w.resolveAuthorityLifecycle == nil || w.configError != nil || w.configCandidate.Authority.Mode != authority.OpenMerge {
		return nil
	}
	request := w.authorityObservation
	if request == nil || request.Read == nil {
		return errors.New("authority observation reader unavailable")
	}
	_, err := ObserveWriteRestriction(ctx, s.store, w.id, authorityObservationReader(request.Read), request.Input)
	return err
}
