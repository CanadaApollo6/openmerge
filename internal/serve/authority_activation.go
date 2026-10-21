package serve

import (
	"context"
	"errors"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// authorityActivation is an internal, explicit request, never derived from a
// candidate config or a journal pin. w.AuthorityRequired is the mechanism;
// production constructors do not enable it.
// Config/pin lifecycle integration remains in 9b-ii-2b: this caller
// requires an already durable matching config, as enforced by the store.
type authorityActivation struct {
	Receipt       authority.EmissionReceipt
	ReceiptMaxAge time.Duration
	Pin           queue.PolicyAuthorityApplied
	DraftID       int64
	Input         forge.WriteRestrictionInput
	Read          func(context.Context) (authority.Observation, error)
}

// activateAuthority requires w.AuthorityRequired before identity I/O. Local
// receipt checks precede read-only identity queries; the database binding precedes
// writes and forge reads. Its adoption receipt does not install runtime hooks.
// Receipt age and database identity are rechecked before calling the adoption
// transaction. It can still expire while that transaction waits for the queue
// lock or commits; this is not a transaction-time expiry guarantee.
func (s *Server) activateAuthority(ctx context.Context, w *worker, request authorityActivation) (store.AuthorityAdoptionReceipt, error) {
	var zero store.AuthorityAdoptionReceipt
	if !w.AuthorityRequired {
		return zero, errors.New("internal authority activation opt-in required")
	}
	// Reject locally invalid attestations before opening the identity transaction.
	if err := request.Receipt.ValidateForBinary(request.Receipt.DatabaseIdentity, time.Now().UTC(), request.ReceiptMaxAge, authority.RunningBinaryRevision()); err != nil {
		return zero, err
	}
	validateReceipt := func() error {
		identity, err := s.store.DatabaseIdentity(ctx)
		if err != nil {
			return err
		}
		return request.Receipt.ValidateForBinary(identity, time.Now().UTC(), request.ReceiptMaxAge, authority.RunningBinaryRevision())
	}
	if err := validateReceipt(); err != nil {
		return zero, err
	}
	if request.Read == nil {
		return zero, errors.New("authority activation seeing reader required")
	}
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return zero, err
	}
	basis := state.RulesSourceRevision(queue.WriteRestriction)
	started := time.Now().UTC()
	fresh, err := request.Read(ctx)
	if err != nil {
		return zero, err
	}
	at := time.Now().UTC()
	// A slow/quota-delayed read must not outlive the first-emission receipt.
	if err := validateReceipt(); err != nil {
		return zero, err
	}
	receipt, err := s.store.AdoptAuthorityPin(ctx, w.id, request.Pin, request.DraftID, basis, started, at, request.Input, fresh)
	if err != nil {
		return zero, err
	}
	s.options.Logger.Info(receipt.String(), "repository", w.Key.Repository, "base", w.Key.BaseBranch)
	return receipt, nil
}
