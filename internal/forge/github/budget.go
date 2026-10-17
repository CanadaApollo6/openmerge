package github

import (
	"context"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// Budget is a conservative snapshot after local reservations, not a promise
// that GitHub has capacity (other processes may use the installation token).
type Budget struct {
	Known             bool      `json:"known"`
	Limit             int       `json:"limit"`
	Remaining         int       `json:"remaining"`
	SettlementReserve int       `json:"settlement_reserve"`
	ResetAt           time.Time `json:"reset_at"`
}

// Budget returns the installation's current capacity without an API request.
func (d *Driver) Budget() Budget {
	t := d.client.Client().Transport.(*transport)
	t.mu.Lock()
	defer t.mu.Unlock()
	return Budget{Known: t.budgetKnown, Limit: t.limit, Remaining: t.remaining, SettlementReserve: t.settlementReserve(), ResetAt: t.reset}
}

func (t *transport) settlementReserve() int { return max(1, (t.limit+9)/10) }

// reservation pins local accounting to the window it consumed.
type reservation struct {
	window  time.Time
	class   forge.RequestClass
	charged bool
}

// reserve protects settlement and maintenance shares under the shared lock.
func (t *transport) reserve(ctx context.Context) (reservation, error) {
	ticket := reservation{class: forge.ClassOfRequest(ctx)}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if now.Before(t.until) {
		return ticket, &RateLimitError{RetryAt: t.until, Secondary: t.secondary}
	}
	if t.budgetKnown && now.Before(t.reset) {
		class := ticket.class
		floor := 0
		if class != forge.SettlementRequest {
			floor = t.settlementReserve()
			share := max(1, t.limit/100)
			for _, protected := range []forge.RequestClass{forge.DiscoveryRequest, forge.RulesRequest} {
				if class != protected {
					floor += max(0, share-t.spent[protected])
				}
			}
			// Idle polling yields before active testing/landing; reporting yields
			// before polling. These floors do not consume the protected shares.
			if class == forge.IdleRequest || class == forge.ReportingRequest {
				floor += t.limit / 10
			}
			if class == forge.ReportingRequest {
				floor += t.limit / 20
			}
		}
		if t.remaining <= floor {
			return ticket, &RateLimitError{RetryAt: t.reset}
		}
		ticket.window, ticket.charged = t.reset, true
		t.remaining--
		t.spent[class]++
	}
	t.inflight++
	return ticket, nil
}
