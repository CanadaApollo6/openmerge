// Package store persists queue events and action claims in PostgreSQL.
//
// Events are append-only. Load folds the complete log from an immutable queue
// bootstrap (base branch and initial forge fence). Snapshots can later replace
// that prefix, provided replay and historical action reconstruction still see
// exactly the same state at every retained action watermark.
//
// Action JSON includes a stable type discriminator, exported action data and
// the planner's config and time. Some queue action keys include private fields;
// decoding replays the action's event prefix and invokes the pure planner to
// recover those fields without exposing queue internals or using reflection.
//
// Append, Plan and BeginConsequential share a transaction-scoped queue lock.
// Serializing appends makes event IDs also reflect commit order within a queue:
// an earlier uncommitted event cannot appear behind a watermark already planned.
// No lock or connection-local state survives a transaction, including migrations.
//
// Workers claim actions, call BeginConsequential for consequential actions, then
// pass Action and Emit to executor.Execute and call Complete. Expired in-flight
// claims can be recovered; BeginConsequential revalidates them. Emit admits only
// one LandingStarted per batch during a valid in-flight execution window, so a
// paused worker cannot start another landing after a replacement has recorded
// its attempt or a conflicting action has begun. Once LandingStarted is durable,
// replay permits only ResolveLanding, even if the original Land is retried.
package store
