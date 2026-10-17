// Package store persists queue events and action claims in PostgreSQL.
//
// Events are append-only. Load folds the complete log from an immutable queue
// bootstrap (base branch and initial forge fence). Snapshots can later replace
// that prefix as long as they preserve the replayed projection.
//
// Events and actions use queue's versioned wire format, including private action
// key inputs. Decoding never invokes the planner or loads an event prefix. Keys
// are stored separately when actions are inserted. Missing/null wire fields and
// semantically invalid actions are quarantined as poisoned and exposed by
// Store.Poisoned. Replanning repairs a poisoned key with fresh inputs, preserving
// its incident count. Three incidents append an action-poisoned BlockSourceBlocked and
// pause the queue visibly on its head PR. ResolveLanding is exempt from this cap:
// it always repairs without pausing. A current-basis BlockSourceCleared for
// action-poisoned clears that source and incident counts; legacy ResumeRequested
// retains its original reset behavior; the next plan retries current work. Done keys remain terminal.
//
// Append, Plan and BeginConsequential share a transaction-scoped queue lock.
// Serializing appends makes event IDs also reflect commit order within a queue:
// an earlier uncommitted event cannot appear behind a watermark already planned.
// No lock or connection-local state survives a transaction, including migrations.
//
// Workers claim actions, call BeginConsequential for consequential actions, then
// pass Action and Emit (with the same planner config) to executor.Execute and call
// Complete. BeginConsequential
// renews the lease; forge calls and result persistence must finish within it.
// Expired in-flight claims can be recovered; BeginConsequential revalidates them.
// Emit replans under the queue lock before BatchStarted, CheckRerunRequested, or LandingStarted and
// requires the same key and complete payload. A stale start appends nothing,
// cancels its action, and returns ErrStale before any forge call. Emit admits only
// one LandingStarted per batch during a valid in-flight execution window, so a
// paused worker cannot start another landing after a replacement has recorded
// its attempt or a conflicting action has begun. Once LandingStarted is durable,
// replay permits only ResolveLanding, even if the original Land is retried.
package store
