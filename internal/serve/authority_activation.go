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
// candidate config or a journal pin. Production constructors do not enable it.
// Config/pin lifecycle integration remains the next 9b-ii slice: this caller
// requires an already durable matching config, as enforced by the store.
type authorityActivation struct {
	Receipt          authority.EmissionReceipt
	DatabaseIdentity string
	ReceiptMaxAge    time.Duration
	Pin              queue.PolicyAuthorityApplied
	DraftID          int64
	Input            forge.WriteRestrictionInput
	Read             func(context.Context) (authority.Observation, error)
}

// activateAuthority gates the internal adoption writer before any DB/forge
// work. Its receipt distinguishes adoption from pinned-unverified; successful
// persistence alone never claims verification or installs runtime hooks.
func (s *Server) activateAuthority(ctx context.Context, w *worker, request authorityActivation) (store.AuthorityAdoptionReceipt, error) {
	var zero store.AuthorityAdoptionReceipt
	if !w.AuthorityRequired {
		return zero, errors.New("internal authority activation opt-in required")
	}
	validateReceipt := func() error {
		return request.Receipt.ValidateForBinary(request.DatabaseIdentity, time.Now().UTC(), request.ReceiptMaxAge, authority.RunningBinaryRevision())
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
