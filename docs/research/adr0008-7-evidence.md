# ADR 0008 PR 7: author queue instructions

Trial class: **HOLD**. This PR may merge, but must not deploy before period B
drains. It builds on main at `975b1b7762f58c128b1703f11fd5c4b6ff552c55` and
has no dependency on PR 6 (#115). PR 9 remains the only runtime activation.
Round-1 implementation revision: `c291ef8706604a14bb30932f94e048881aa3ab16`.
Validation used exactly this implementation content; the following evidence-only
commit does not change the tested source.

The existing OpenMerge check includes this paragraph after queue state/CI ledger
and before any Stage A reviewer brief:

> This repository merges through OpenMerge. To request a merge, post a comment whose entire body is `@openmerge queue` (no additional text).

`status.QueueInstructions` reads the projected durable authority pin. No pin, a
GitHub pin, or a rollback to GitHub yields the empty string. Neither proposed
verification nor observation visibility activates the paragraph. There is no
verification claim or acknowledgement instruction in it. Its static length and
separate paragraph preserve the existing name escaping and per-line bounds.

`serve.ledgerSummary` composes it once. Existing worker call sites use that suffix
for periodic, readiness, and durable `ReportStatus` reporting, including terminal
outcomes. Ready, unqueued PRs gain a periodic check only after a mode pin exists;
ordinary GitHub queues retain their previous absence of a check. Rollback pins
refresh the pre-admission check without instructions. Admitted and terminal
entries are excluded from that invitation path. Terminal checks on still-open PRs
now refresh only the instruction paragraph from the cached successful reporting
action; status, reason, exact head, ledger text and reviewer brief stay unchanged.
Closed PRs are skipped by this refresh; existing closed-PR reporting paths are unchanged. Existing exact-head arguments, external check identity, durable cache,
retry behavior, titles, annotations, comment effects, and wire payloads are
unchanged. Approval brief rendering remains independent.

## Local acceptance

Results, UTC timestamps, commands, and numeric exits are recorded in the
[artifact directory](adr0008-7-artifacts/). The compressed JSON receipts retain
both complete baseline and candidate CLI/HTTP bytes as base64; decode them with
`gzip -dc` and a JSON/base64 reader. The artifact manifest records SHA-256 hashes
of receipts, logs, and the implementation/test source used for validation. All Go invocations,
including the Go subprocesses in comparison scripts and `make check`, run under
`flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB`.
DB tests use isolated schemas on `localhost:54329` only.
The rendering seed is the existing `event_policy_authority_applied.json` wire
fixture: governing commit `governing-commit`, effective digest `a` repeated 64
times, and coverage digest `b` repeated 64 times. The service tests substitute
their isolated fixture config digest. The driver test uses a projected seed,
not a persisted journal, so there is no production source event ID or sandbox
config commit to claim; those remain part of the deferred activation capture.

- Four golden check suffixes: waiting, testing, blocked, and landed.
- Long hostile queue names and intent titles; static instructions survive the
  GitHub driver's long-line bound.
- Before admission: legacy absence, authority invitation, unchanged-cycle dedupe,
  rollback refresh, and preservation of a landed result while the forge still
  lists the PR as open.
- Both ordinary and approval queues: retry after failed write, unchanged-cycle
  dedupe, readiness paths, no reporting comments, and complete cached status
  equality before activation versus after rollback.
- A fresh local bare Git remote supplies distinct real heads. The loopback
  GitHub driver fixture verifies head attachment, repeated updates, preservation
  of a foreign App's check, rollback, and no comment creation. The verbose log
  records the local PR ref SHA. No live GitHub call occurs in these tests.
- `check-legacy-reporting.py`: six deterministic fixtures and existing golden;
  exact check bytes and preexisting JSON field bytes/order (only the existing
  change/landing exceptions allowed). The candidate probe includes the new
  instruction formatter.
- The CLI/HTTP probes are the comparison probes used for #115, copied without
  changing their comparison rules (receipt names identify PR 7). Six complete
  CLI outputs and 56 complete HTTP responses are compared without stripping
  fields, headers, status lines, or whitespace.

Round-1 validation completed **2026-10-21 00:09:10 UTC**; every recorded command
exited **0**. `make check` passed formatting, lint (0 issues), the complete
race-enabled short test suite with the test DB, and the build. The built local
binary identifies revision `c291ef8`; its hash and the exact tested source hashes
are retained in `adr0008-7-artifacts/manifest.json`.

## Deferred acceptance

Actual authority-mode sandbox check output is **not executed here**. As specified
by PR 7, retain that output after PR 9 activates authority. Local HTTP fixtures
prove driver transport and check identity, not enforcement by real GitHub.
No deployment, live DB/status access, systemd operation, or live OpenMerge ref
operation was performed.

No specification deviations. The shared ledger suffix is the integration point
for the existing worker reporting paths, avoiding duplicate instruction logic.

## Round 2: rollback refresh

- Item 1: durable `ReportStatus` writes now record their exact successful output
  in the existing status observation cache. Periodic reporting refreshes only the
  instruction paragraph on open dequeued or merged checks, using the same
  exact-head status dedupe. It preserves terminal state, dequeue reason, summary
  details and reviewer brief even after repeated PR observations reset the
  projected pre-admission ledger entry. Tests cover activation and rollback with
  three observe/report cycles per transition, both dequeued and landed PRs,
  and exactly one update per transition. Closed versions of both terminal cases
  receive zero updates. No checks are deleted, and terminal PRs without a cached
  successful terminal report do not gain a new check through this path.
- Item 2: the refresh loop returns when `ledgerSummary` leaves `ledgerCache` nil,
  matching the Stage A guard.
- Item 3 decision: retain pre-admission checks after rollback, including creation
  for newly observed unqueued heads. A durable GitHub pin therefore keeps the
  existing “Not queued; waiting for a queue request” check without the paragraph
  saying this repository merges through OpenMerge. The queue is optional again;
  never-pinned GitHub repositories still receive no pre-admission check. The
  regression verifies both existing-check rollback dedupe and a new unqueued
  head after rollback. This retains the original PR 7 choice explicitly.

Round-2 validation results and source hashes are recorded in the round-2 artifact
manifest alongside the original round-1 receipts.
