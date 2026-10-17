package status

import (
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"slices"
	"time"
)

// Discovery includes visible repositories without queues, retained removals,
// and per-installation request capacity. Failed sweeps retain successful times.
type Discovery struct {
	LastSuccessfulSweep *time.Time     `json:"last_successful_sweep"`
	Repositories        []Repository   `json:"repositories"`
	Installations       []Installation `json:"installations"`
}

// Repository records an authoritative discovery result, independently of queues.
type Repository struct {
	Installation           string     `json:"installation"`
	Name                   string     `json:"repo"`
	State                  string     `json:"state"`
	Detail                 string     `json:"detail,omitempty"`
	LastSuccessfulSweep    *time.Time `json:"last_successful_sweep"`
	LastVerifiedDelivery   *time.Time `json:"last_verified_delivery"`
	LastDeliveryAgeSeconds *float64   `json:"last_delivery_age_seconds"`
}

// Installation exposes capacity after atomic reservations by all its workers.
type Installation struct {
	ID                   string     `json:"installation"`
	Suspended            bool       `json:"suspended"`
	LastSuccessfulSweep  *time.Time `json:"last_successful_sweep"`
	Budget               Budget     `json:"budget"`
	WebhooksConfigured   bool       `json:"webhooks_configured"`
	WebhookHealthy       bool       `json:"webhook_healthy"`
	LastVerifiedDelivery *time.Time `json:"last_verified_delivery"`
}

// Budget reports unknown capacity explicitly until rate headers are observed.
type Budget struct {
	Known             bool      `json:"known"`
	Limit             int       `json:"limit"`
	Remaining         int       `json:"remaining"`
	SettlementReserve int       `json:"settlement_reserve"`
	ResetAt           time.Time `json:"reset_at"`
}

func sanitizeDiscovery(d Discovery, sanitizer *diagnostic.Sanitizer) Discovery {
	d.Repositories = slices.Clone(d.Repositories)
	for i := range d.Repositories {
		d.Repositories[i].Detail = sanitizer.Sanitize(d.Repositories[i].Detail)
	}
	return d
}
