package status

import (
	"fmt"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// Authority is a read-only rendering of durable applied evidence. Ages are
// seconds; nil means never observed/verified, not a fresh observation.
type Authority struct {
	State           string             `json:"state"`
	LastObservedAt  *time.Time         `json:"last_observed_at"`
	LastObservedAge *float64           `json:"last_observed_age_seconds"`
	LastVerifiedAt  *time.Time         `json:"last_verified_at"`
	LastVerifiedAge *float64           `json:"last_verified_age_seconds"`
	Inventory       string             `json:"inventory"`
	Elements        []AuthorityElement `json:"elements"`
	ElementsOmitted int                `json:"elements_omitted,omitempty"`
}

// AuthorityElement names one independently visible policy element.
type AuthorityElement struct {
	Element    string `json:"element"`
	Visibility string `json:"visibility"`
}

// OutsideWarnings renders outstanding ledger entries, independently of the
// bounded recent OutsidePushes history. Acknowledgements remove only warnings.
type OutsideWarnings struct {
	Outstanding int     `json:"outstanding"`
	EventIDs    []int64 `json:"event_ids"`
	Truncated   bool    `json:"truncated"`
}

// ProjectOutsideWarnings omits empty ledgers to preserve legacy output. The
// ledger read must have succeeded; callers must discard summaries on error.
func ProjectOutsideWarnings(summary store.OutsideWarningSummary) *OutsideWarnings {
	if summary.Outstanding == 0 {
		return nil
	}
	return &OutsideWarnings{Outstanding: summary.Outstanding, EventIDs: append([]int64{}, summary.EventIDs...), Truncated: summary.Truncated}
}

// ProjectAuthority renders PR 4's durable projection. It neither interprets
// forge responses nor reconciles findings; hidden reads retain the source's
// sticky violations and operator verification remains a distinct timestamp.
func ProjectAuthority(s queue.State, now time.Time) *Authority {
	pin := s.AuthorityPin()
	if pin == nil || pin.Authority != authority.OpenMerge {
		return nil
	}
	durable := s.WriteRestrictionStatus()
	a := &Authority{State: durable.State, Inventory: "unknown", Elements: []AuthorityElement{}}
	if !durable.LastObservedAt.IsZero() {
		a.LastObservedAt = &durable.LastObservedAt
	}
	if !durable.LastVerifiedAt.IsZero() {
		a.LastVerifiedAt = &durable.LastVerifiedAt
	}
	_, evidence, found := s.AuthorityProtection()
	if !found && pin.OpenMerge.Activation != nil {
		evidence = pin.OpenMerge.Activation.Evidence
		found = true
	}
	if found {
		a.Inventory = string(evidence.Inventory)
		for _, p := range evidence.Protections {
			a.Elements = append(a.Elements, AuthorityElement{p.Source + "/" + p.ID + "/targets", string(p.TargetsVisibility)}, AuthorityElement{p.Source + "/" + p.ID + "/rules", string(p.RulesVisibility)}, AuthorityElement{p.Source + "/" + p.ID + "/bypass", string(p.BypassVisibility)})
		}
		for _, c := range evidence.Coverage {
			a.Elements = append(a.Elements, AuthorityElement{c.Ref, string(c.Visibility)})
		}
	}
	a.RefreshAges(now)
	return a
}

func age(at *time.Time, now time.Time) *float64 {
	if at == nil {
		return nil
	}
	value := max(0, now.Sub(*at).Seconds())
	return &value
}

// RefreshAges updates display ages without asserting new evidence.
func (a *Authority) RefreshAges(now time.Time) {
	a.LastObservedAge = age(a.LastObservedAt, now)
	a.LastVerifiedAge = age(a.LastVerifiedAt, now)
}

// AuthoritySummary renders the same evidence fields for CLI status.
func AuthoritySummary(a *Authority) string {
	if a == nil {
		return ""
	}
	stamp := func(at *time.Time, seconds *float64) string {
		if at == nil {
			return "never (age unknown)"
		}
		elapsed := "unknown"
		if seconds != nil {
			elapsed = (time.Duration(*seconds * float64(time.Second))).Round(time.Second).String()
		}
		return at.UTC().Format(time.RFC3339Nano) + " (age " + elapsed + ")"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "authority %s; last observed: %s; last verified: %s; inventory: %s", a.State, stamp(a.LastObservedAt, a.LastObservedAge), stamp(a.LastVerifiedAt, a.LastVerifiedAge), a.Inventory)
	for _, e := range a.Elements {
		fmt.Fprintf(&b, "; %s: %s", e.Element, e.Visibility)
	}
	return b.String()
}
