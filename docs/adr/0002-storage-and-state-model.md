# 0002. Use Postgres only, with queue state derived from an append-only event log

- **Status:** Accepted
- **Date:** 2026-10-15

## Context

A merge queue is a small amount of state with very high stakes. A wrong decision merges an untested commit or silently drops a PR. Three failure modes from prior art shape this decision:

- **Lost or reordered webhooks.** Forges deliver late, twice, or never. Prow/Tide survives this by reconciling against forge state instead of trusting events alone.
- **Unexplainable decisions.** "Why is my PR stuck?" is the most common support question for any queue. Mergify users report check runs stuck `in_progress` with no recovery path.
- **Operational weight.** Mergify's 2022 engine ran on Redis streams and hashes. Its current self-hosted edition needs both Redis and PostgreSQL. Our roadmap promises one binary plus Postgres.

Concurrency is naturally partitioned. A queue is identified by (forge installation, repository, base branch, lane), and decisions inside one queue must be serial, while different queues are independent.

## Decision

**Postgres (version 15 or later) is the only stateful dependency.** No Redis, no message broker.

**Inputs are events in an append-only log.** Everything that can change a decision is recorded as an event before it is acted on:

- forge webhooks, deduplicated by delivery ID;
- observations from the reconciler, which periodically re-reads forge state and records what it saw;
- results of actions the executor performed, including failures;
- user commands from comments, the CLI, the API, or MCP;
- timer firings (check timeouts, batch wait windows, schedules).

**Queue state is a projection.** Each queue's current state is the fold of its events. Projections are cached in tables for speed but can always be rebuilt from the log, so a bug in state handling is fixed by fixing the fold and replaying.

**The planner is a pure function.** For one queue it takes (config, state, CI results, current time) and returns a list of intended actions: create a test ref, cancel a run, land a SHA, post a check, dequeue with a reason. It performs no I/O and reads the clock only through its input. This is where batching, speculation, bisection, and priorities live, and it is tested with tables and simulations that need no database or forge.

**One worker plans a queue at a time.** A worker takes a transaction-scoped Postgres advisory lock on the queue's key, folds new events, runs the planner, and writes the planned actions in the same transaction. Each action records an idempotency key and the event watermark it was planned from (the last event ID it saw). There is no leader election: any number of `openmerge` processes can run, and the lock serializes planning for each queue. Work items are claimed with `SELECT … FOR UPDATE SKIP LOCKED` and a lease, so a crashed worker's claim expires and another worker picks it up.

**Consequential actions are fenced by a durable in-flight record.** Idempotency stops an action running twice; it does not stop an action that has gone stale. An action is consequential if it changes what can land: pushing a test ref, landing, or dequeuing. To execute one, the executor takes the queue's planning lock and checks for events newer than the action's watermark. If any exist, it re-runs the planner, and an action the new plan no longer contains is cancelled. Otherwise it marks the action `in_flight` with an attempt ID and a deadline, commits, and only then calls the forge. For landing, the executor mints a random ID with at least 128 bits from `crypto/rand` and durably appends `LandingStarted` before the push. A batch/action key can repeat across queue generations and must never identify an attempt. After that record, the planner emits only `ResolveLanding` for the batch; retrying the original `Land` action is forbidden. While an action is in flight, the planner resolves it before planning anything that conflicts with it: it reads forge state to see whether the effect happened (does the fence record this attempt and its tested SHA, does the test ref exist). An attempt whose outcome is still uncertain after its deadline is resolved through the forge-enforced fence in [ADR 0004](0004-forge-driver-interface.md): OpenMerge advances the fence so the attempt can no longer take effect, then reads the outcome. A rejected landing push follows that same fence resolution; a lease refusal alone cannot settle the attempt, because another copy may already have landed or still be paused. Only `Landed` or `LandingAbandoned` settles it. A deadline alone never declares an attempt failed. A conflicting command, such as dequeuing a PR that is being landed, is recorded as an event and acknowledged after the in-flight action resolves, with the true outcome ("already landed" or "dequeued"). The deadline only decides when to stop waiting and fence. Correctness holds even if a lock is lost or a worker disappears, because the fences are the durable record and the forge-side fence ref, not the lock.

**Other actions run without the lock** (comments, labels, status updates). Every action is idempotent under its key, so a crash between "applied" and "recorded" converges on retry. Each outcome is appended to the log as an event.

**Every decision is explainable.** Because each planned action records the event range and planner inputs that produced it, the API can answer "why is this PR in this state" by pointing at concrete events. This backs the dashboard's stuck-PR explainer and the agent-facing failure explanations in Phase 3.

**Retention.** Old events are compacted per queue into a snapshot plus recent history after a configurable window. Compaction must preserve the ability to rebuild current state.

## Consequences

- Self-hosting stays at one binary plus one database. Backups, high availability, and observability reuse standard Postgres tooling.
- Missed webhooks degrade latency, not correctness: the reconciler catches up.
- Debugging becomes replay. A bug report can ship the relevant event range, and a test can reproduce the exact decision.
- The planner can be simulated at scale (thousands of PRs, scripted failure rates) in milliseconds, which is how the roadmap's throughput exit tests are measured.
- We own schema migrations from Phase 1 onward. Migrations ship inside the binary and run on startup, forward-only.
- Store-level tests need a real Postgres. CI provides a Postgres service. Locally, store tests use `OPENMERGE_TEST_DATABASE_URL` and are skipped with a clear message when it is unset. Planner and fake-forge tests never need a database.
- Advisory-lock ownership means a very hot queue is limited to one worker's throughput. Lanes (Phase 4) split such queues.
- Lanes parallelize testing, not landing. Every lane on a base branch lands through the same base-branch lock and compare-and-swap, so disjoint-scope merges keep the exact-SHA guarantee. Phase 4 must design lane landing on top of this rule, not around it.
- OpenMerge uses only transaction-scoped locks, so it works behind transaction-mode connection poolers such as PgBouncer.
- A dequeue that races a landing is acknowledged a moment later than it would be otherwise. That delay is the cost of always reporting the true outcome.
- **Stored events and actions use a versioned wire format** owned by `internal/queue` (`WireVersion` 1). Every field is required, unknown fields are rejected, and golden fixtures pin the format. Strict decoding means an older binary cannot read data a newer one wrote, so upgrades drain old workers before new ones start writing. New fields are added as a new wire version, never silently.
- **An action that cannot be decoded or validated is quarantined** as `poisoned`, never executed, and never blocks other queues. Re-planning the same key replaces it with the planner's fresh payload. After three incidents on one key the queue pauses with reason `action-poisoned`, visible on the PR, until someone resumes it.
- **Pausing stops new work; it never stops settling a landing already in flight.** `ResolveLanding` runs while paused and is exempt from the quarantine limit.

## Alternatives considered

- **Redis streams plus hashes** (Mergify's 2022 design). Fast and proven at their scale, but it adds a second stateful service, durability depends on AOF configuration, and Mergify itself later added PostgreSQL for persistent state.
- **Mutable state rows without an event log.** Simpler to start, but loses replay, makes "why" questions unanswerable after the fact, and turns webhook ordering bugs into silent corruption.
- **SQLite.** Attractive for single-node installs and development. It lost as the primary store because multiple workers need row-level locking and `SKIP LOCKED`. The store interface should not preclude a SQLite backend for single-process development later; that would need its own ADR.
- **An external workflow engine** (Temporal and similar). Durable execution would cover crash recovery, but it is a heavy dependency for self-hosters and hides the queue logic inside workflow code that is harder to simulate than a pure planner.
