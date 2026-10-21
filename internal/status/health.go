package status

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// Probe limits bound health work independently of journal size.
const (
	// MaxHealthQueues caps partitions inspected by one health request.
	MaxHealthQueues = 256
	// MaxHealthBytes caps the complete serialized health response.
	MaxHealthBytes = 256 << 10
	// HealthTimeout includes DB pool acquisition and the read-only probe.
	HealthTimeout = 2 * time.Second
)

// HealthSettings are process alerting thresholds, never queue policy.
type HealthSettings struct {
	OverdueGrace       time.Duration
	MaxSuccessAge      time.Duration
	MaxCycleDuration   time.Duration
	ProgressStallLimit time.Duration
}

// Valid checks the supported process threshold ranges.
func (s HealthSettings) Valid() bool {
	return s.OverdueGrace >= time.Second && s.OverdueGrace <= time.Minute && s.MaxSuccessAge >= time.Minute && s.MaxSuccessAge <= time.Hour && s.MaxCycleDuration >= 2*time.Minute && s.MaxCycleDuration <= 6*time.Hour && s.ProgressStallLimit >= time.Second && s.ProgressStallLimit <= 30*time.Minute
}

// DefaultHealthSettings returns the alerting defaults from ADR 0008.
func DefaultHealthSettings() HealthSettings {
	return HealthSettings{OverdueGrace: 30 * time.Second, MaxSuccessAge: 15 * time.Minute, MaxCycleDuration: 3 * time.Hour, ProgressStallLimit: 4 * time.Minute}
}

// Health is the bounded alerting response on the status API listener.
type Health struct {
	Version   int           `json:"version"`
	Healthy   bool          `json:"healthy"`
	CheckedAt time.Time     `json:"checked_at"`
	Queues    []HealthQueue `json:"queues"`
}

// HealthQueue contains process telemetry and durable display state. It is not an
// audit assertion, and cannot be used to justify a restart or a landing.
type HealthQueue struct {
	Installation         string           `json:"installation"`
	Repository           string           `json:"repo"`
	Base                 string           `json:"base"`
	Lane                 string           `json:"lane"`
	Lifecycle            string           `json:"lifecycle"`
	Readiness            Readiness        `json:"readiness"`
	Watched              bool             `json:"watched"`
	StartedAt            time.Time        `json:"started_at"`
	LastSuccess          *time.Time       `json:"last_success_at"`
	LastSuccessClockSkew bool             `json:"last_success_clock_skew,omitempty"`
	LastSuccessAge       *float64         `json:"last_success_age_seconds"`
	InCycle              bool             `json:"in_cycle"`
	CycleStartedAt       *time.Time       `json:"cycle_started_at"`
	LastProgressAt       *time.Time       `json:"last_progress_at"`
	NextDeadline         *time.Time       `json:"next_deadline"`
	Failures             int              `json:"consecutive_failures"`
	Overdue              bool             `json:"overdue"`
	Degraded             bool             `json:"degraded"`
	Reasons              []string         `json:"reasons"`
	Authority            *Authority       `json:"authority,omitempty"`
	OutsideWarnings      *OutsideWarnings `json:"outside_warnings,omitempty"`
}

// EvaluateHealth only renders the schedule published by the actual worker loop.
func EvaluateHealth(q HealthQueue, settings HealthSettings, now time.Time) HealthQueue {
	q.Reasons = append([]string{}, q.Reasons...)
	q.LastSuccessAge = age(q.LastSuccess, now)
	q.LastSuccessClockSkew = q.LastSuccess != nil && q.LastSuccess.After(now)
	if q.Authority != nil {
		a := *q.Authority
		a.RefreshAges(now)
		q.Authority = &a
	}
	q.Overdue = false
	if q.Watched {
		if q.InCycle {
			q.Overdue = q.CycleStartedAt == nil || q.LastProgressAt == nil ||
				now.After(q.LastProgressAt.Add(settings.ProgressStallLimit)) ||
				now.After(q.CycleStartedAt.Add(settings.MaxCycleDuration))
		} else {
			q.Overdue = q.NextDeadline != nil && now.After(q.NextDeadline.Add(settings.OverdueGrace))
		}
	}
	since := q.StartedAt
	if q.LastSuccess != nil {
		since = *q.LastSuccess
	}
	if q.InCycle && q.LastProgressAt != nil && q.LastProgressAt.After(since) {
		since = *q.LastProgressAt
	}
	q.Degraded = q.Watched && !since.IsZero() && now.Sub(since) > settings.MaxSuccessAge
	if q.LastSuccess == nil {
		q.Reasons = append(q.Reasons, "not-yet-reconciled")
	}
	if !q.Readiness.Ready {
		q.Reasons = append(q.Reasons, q.Readiness.Reason)
	}
	if q.Overdue {
		q.Reasons = append(q.Reasons, "reconciliation-overdue")
	}
	if q.Degraded {
		q.Reasons = append(q.Reasons, "reconciliation-degraded")
	}
	return q
}

// Failing excludes queues without obligations from alerting.
func (q HealthQueue) Failing() bool {
	return q.Watched && (q.Overdue || q.Degraded || q.Authority != nil && q.Authority.State == "violated")
}

// processLive must run before dynamic key discovery as well as authentication.
func processLive(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.EscapedPath() != "/v1/live" {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if !probeRequest(w, r) {
		return true
	}
	writeJSON(w, struct {
		Live bool `json:"live"`
	}{true})
	return true
}

func probeRequest(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		fail(w, 405, "method-not-allowed", "only GET is supported")
		return false
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		fail(w, 400, "invalid-request", "probe accepts no query")
		return false
	}
	// Reject framed bodies without waiting for a slow or unending upload.
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		fail(w, 400, "invalid-request", "probe accepts no body")
		return false
	}
	return true
}

func serveHealth(w http.ResponseWriter, r *http.Request, source Source, sanitizer *diagnostic.Sanitizer) {
	if !probeRequest(w, r) {
		return
	}
	provider, ok := source.(interface {
		Health(context.Context) (Health, error)
	})
	if !ok {
		fail(w, 503, "unavailable", "health snapshot unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), HealthTimeout)
	defer cancel()
	h, err := provider.Health(ctx)
	if err != nil || ctx.Err() != nil || len(h.Queues) > MaxHealthQueues {
		fail(w, 503, "unavailable", "health snapshot unavailable")
		return
	}
	h.Queues = append([]HealthQueue{}, h.Queues...)
	for i := range h.Queues {
		q := &h.Queues[i]
		q.Repository = sanitizer.Sanitize(q.Repository)
		q.Base = sanitizer.Sanitize(q.Base)
		q.Lane = sanitizer.Sanitize(q.Lane)
		q.Readiness.Reason = sanitizer.Sanitize(q.Readiness.Reason)
		q.Reasons = append([]string(nil), q.Reasons...)
		for j := range q.Reasons {
			q.Reasons[j] = sanitizer.Sanitize(q.Reasons[j])
		}
		if q.OutsideWarnings != nil {
			warnings := *q.OutsideWarnings
			const warningIDLimit = store.OutsideWarningIDLimit
			if len(warnings.EventIDs) > warningIDLimit {
				warnings.EventIDs = warnings.EventIDs[:warningIDLimit]
				warnings.Truncated = true
			}
			warnings.EventIDs = append([]int64{}, warnings.EventIDs...)
			q.OutsideWarnings = &warnings
		}
		if q.Authority != nil {
			a := *q.Authority
			const healthElementLimit = 64
			if len(a.Elements) > healthElementLimit {
				a.ElementsOmitted += len(a.Elements) - healthElementLimit
				a.Elements = a.Elements[:healthElementLimit]
			}
			a.Elements = append([]AuthorityElement(nil), a.Elements...)
			for j := range a.Elements {
				a.Elements[j].Element = sanitizer.Sanitize(a.Elements[j].Element)
			}
			q.Authority = &a
		}
	}
	raw, err := json.Marshal(h)
	if err != nil || len(raw)+1 > MaxHealthBytes {
		fail(w, 503, "unavailable", "health exceeds response limit")
		return
	}
	if !h.Healthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_, _ = w.Write(append(raw, '\n'))
}
