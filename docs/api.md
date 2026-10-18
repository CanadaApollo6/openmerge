# Read-only status API and CLI (v1)

Enable the API on `openmerge serve` with `--api-listen :8081` or
`OPENMERGE_API_LISTEN=:8081`. It is **off by default**. A port-only address binds
`127.0.0.1`; explicit `127.0.0.1:8081` and `[::1]:8081` also work. Binding
`0.0.0.0:8081` or `[::]:8081` exposes other interfaces and requires a bearer token.
Without a token, every resolved address of a hostname must be loopback; startup
rejects unresolved or mixed/public results and pins the loopback address. Use HTTPS
ingress when crossing a trusted local boundary; the binary serves HTTP.
The API and webhook listeners are separate and shut down with the worker.

Authentication is optional only for loopback binds. Set `OPENMERGE_API_TOKEN` **or**
`--api-token-file /path/to/token` (`OPENMERGE_API_TOKEN_FILE`) before startup.
The file must be readable and nonempty; surrounding whitespace is trimmed.
Tokens use visible ASCII without spaces. Conflicting token sources fail startup.
Clients send `Authorization: Bearer TOKEN`. Token changes require restarting
serve. Authentication covers unknown paths and unsupported methods as well.
Never use an App private key, GitHub token, or webhook secret as the API token.

## Endpoints

All responses are JSON with `version: 1`, `Content-Type: application/json`, and
`Cache-Control: no-store`. Only **GET** is accepted; there are no write endpoints.
HEAD and other methods return 405 with `Allow: GET`. Request bodies are limited
to 4096 bytes, including chunked bodies; oversized bodies return 413. Bodies
within the limit are ignored.

| Path | Response |
| --- | --- |
| `/v1/discovery` | `{"version":1,"discovery":DISCOVERY}`; visible/removed repositories, sweeps, installation budgets |
| `/v1/queues` | `{"version":1,"queues":[QUEUE,...]}`; empty list is `[]` |
| `/v1/queues/{owner}/{repo}` | List every served queue for this repository |
| `/v1/queues/{owner}/{repo}/{base}` | `{"version":1,"queue":QUEUE}` |
| `/v1/queues/{owner}/{repo}/{base}/prs/{n}` | `{"version":1,"queue":QUEUE,"pr":ENTRY,"why":WHY}` |
| `/v1/queues/{owner}/{repo}/{base}/changes/{id}/brief?audience=reviewer` | Public recorded brief v1 DTO; also operator/agent |

URL-encode each path component, including slashes in base branch names:
`/v1/queues/acme/project/release%2Fv1`. PR numbers are positive integers.
PR detail accepts `?events=N`: default 20, range 1–100. Brief detail requires
`?audience=reviewer|operator|agent`. Other query parameters
are rejected. A queue is identified internally by installation/repo/base/lane;
lists include all four fields. A detail path matching multiple served partitions
returns 409 rather than choosing one. Discovery serves every installation of one
App; default lanes remain empty. Repository path matching is case-insensitive.

`/v1/queues` also includes an additive `discovery` object. It contains
`last_successful_sweep` (null before success), `repositories` and `installations`.
Every repository has `installation`, `repo`, `state`, optional `detail`, and
`last_successful_sweep`. States are `served`, `not configured`, `invalid config`,
`removed`, `suspended`, `dormant base`, `validating`, and `unavailable`. A new or
restarted queue stays `validating` until validation succeeds. A transient config
read sets `unavailable` with a retry detail; it does not create a durable block.
Failed listings retain the previous
state and successful timestamp. Installations expose `installation`, `suspended`,
`last_successful_sweep`, and `budget` (`known`, `limit`, `remaining`,
`settlement_reserve`, `reset_at`). Budget reads are local snapshots after atomic
reservations; `known: false` means capacity has not been observed. Discovery
reads never call GitHub. `openmerge queue status --discovery [--json]` selects
this view; ordinary queue lists include it too.

Webhook visibility is additive to discovery responses:

- Installations include `webhooks_configured` (boolean), `webhook_healthy`
  (boolean), and `last_verified_delivery` (RFC3339 time or null).
- Repositories include `last_verified_delivery` (RFC3339 time or null) and
  `last_delivery_age_seconds` (nonnegative number of seconds or null).

These observations are process-local and reset to null/unhealthy after a
restart. An installation is healthy only with a configured listener and a
verified delivery within `webhook_health_window` (default 30 minutes).
Redeliveries refresh the observation, while delivery IDs still deduplicate
refresh work. Invalid signatures never change these fields. Repository ages
remain independent: a repository can have a null or old delivery time while
another repository keeps its installation healthy. `openmerge queue status`
and `--discovery` show health, last verified time, and repository delivery ages;
`--json` preserves these fields. Reads compute ages locally without forge calls.
See [webhook setup and scheduling](operating.md#webhook-setup-and-health).

Example queue response:

```json
{
  "version": 1,
  "queue": {
    "state": "active",
    "installation": "456",
    "repo": "acme/project",
    "base": "main",
    "lane": "",
    "watermark": 17,
    "base_sha": "BASE_SHA",
    "paused": false,
    "pause_reason": "",
    "pause_detail": "",
    "blocks": [],
    "active_batch": {
      "id": "b1",
      "phase": "testing",
      "base_sha": "BASE_SHA",
      "tested_sha": "TESTED_SHA",
      "test_ref": "openmerge/queue/main/b1",
      "method": "squash",
      "prs": [{"pr": 42, "head_sha": "HEAD_SHA"}]
    },
    "entries": [{
      "position": 1,
      "pr": 42,
      "head_sha": "HEAD_SHA",
      "admitted_at": "2026-10-16T12:00:00Z",
      "status": "testing",
      "reason": "",
      "last_dequeue_reason": ""
    }]
  }
}
```

Queues add `outside_pushes` after observing a base move that OpenMerge did not
land. It contains the latest 20 observations, oldest first, independent of the
PR explanation's event limit. Each includes `event_id`, `previous_sha`,
`new_sha`, `at`, `ancestry_known`, `rewrite`, `history_known`, `commits`,
`truncated`, `possible_stale_observation`, and
`verification_before_landing: "none"`. The initial base read
establishes a baseline and is not an outside push. Outstanding landings settle
through the existing fence before the independent journal audit classifies their
observations (possibly indefinitely for an unresolved attempt); an OpenMerge landing followed
by an admin push records only the move from the landed commit to the admin tip.

`possible_stale_observation: true` applies only to a rewind from a settled
landing L to the base B that its attempt was built on. This is lower-confidence
evidence: the observed rewrite may reflect a stale base read rather than a real
rollback. A return from B to L is suppressed only once per settled attempt, provided
no other nontrivial move has consumed or invalidated that attempt's allowance.
Thus L→C→L records both outside moves, and L→B→L→B→L records the second
return to L; both rewinds to B remain flagged. The flag
remains if the next observation is C instead. Verify such records before any
future ADR 0008 acknowledgement; other transition shapes never set this flag.

`rewrite: true` means the previous tip is not an ancestor of the new tip. When
`ancestry_known` is false, `rewrite: false` is not proof of a fast-forward.
`commits` lists commits reachable from the new tip but not the previous tip,
including merged side histories, newest first in topological order. It holds at
most 100 IDs; `truncated: true` means more exist. A rollback can have a known
empty list. Unreadable or unavailable history sets `history_known: false` with
an empty list. Neither the record nor status attributes the push to commit
authors. Later verification must be separate evidence and cannot change the
claim about verification before landing. The durable journal retains every
observation; status and the snapshot projection retain only the bounded tail.
`openmerge queue status` prints these observations, including rewrites, unknown
history and truncation; `--json` preserves the additive API fields. This record
does not add outside-push warnings or acknowledgement commands (ADR 0008).

When bounded failed-check retries are enabled and active, `active_batch` also
contains `check_retry`. It reports the original and current deadlines, request
and dispatch state, generation, failed/observed check identities, and the
`rerun_state` (`requested`, `running`, `passed`, `failed`, or `unsupported`).
The field is omitted for the default retry-disabled policy.
Retry evidence rows in a PR's `why.events` include `runs` and `rerun_state`
when present, so the original flaky identities remain visible after landing.

Queue `state` is `active`, `dormant`, `retired`, or `overlapping`.
An absent lifecycle event means `active`; startup validation and settlement-only
mode are in memory and do not change the persisted state. Lists include newly
discovered bases and retained inactive queues without restarting the API.
Lifecycle blocks prevent admission and new landings; they never suppress
resolution of a recorded landing attempt.
`openmerge queue status --repo owner/repo` lists every base; `--base` selects one.

Queue entries are ordered by admission and positions are 1-based, including the
active batch. `active_batch` is `null` when idle and `entries` is always an array.
`blocks` is always an array of `{ "reason": "...", "detail": "..." }` objects
covering every active cause: generic pause, action poison, repository rules, and
the build retry limit. `paused` is true when any block exists; `pause_reason` and
`pause_detail` describe the first block. Clearing repository rules never clears
the other causes. Diagnostic strings are credential-redacted, stripped of
control/format characters and bounded to 1024 characters plus an ellipsis.
The batch method is empty until construction completes. Empty strings mean no
reason/SHA/method is recorded, not an omitted field. `admitted_at` is the queue
request's recorded admission time, not the time of the HTTP read.

When rules sources have been observed, `rules_sources` lists objects with
`source` (`rulesets`, `classic_protection`, or `permissions`), `problems` (an array),
and `revision` (the source's current observation revision). Cleared sources have
an empty problems array. Each source recovers independently; a read error retains
only that source's previous state. Stored legacy rules events appear as `rulesets`.

Served queue objects also include `readiness: {"ready": true}` or
`readiness: {"ready": false, "reason": "repository rules not yet verified: rulesets read failed"}`.
This process-local admission gate is independent of durable `state` and `blocks`.
The CLI displays `waiting` and its reason while gated, and PR checks show
`validation-pending`. Validation retries on a bounded schedule, including after
landing settlement, without waiting for the discovery interval. A fresh process
must verify rules before admitting work; an already verified worker retains its
prior rules on a transient re-read failure.

ENTRY status values are `queued`, `building`, `testing`, `landing`,
`dequeue-pending`, `paused`, `merged`, `dequeued`, `closed`, `blocked`, and
`not-queued`. `reason` is the pause or pending/terminal dequeue reason where
applicable. During testing, `reason: unbound-check-provenance` and optional
`detail` explain required checks observed only in suites outside the queue push.
`detail` names the check, App source, observed branch (or `null`), and expected
ref; `why.summary` carries the same explanation. A provenance timeout keeps
`checks-timeout` as its dequeue reason and retains that diagnostic in `detail`.
`last_dequeue_reason` retains the most recent **completed** dequeue,
even after readmission; it is empty if none exists. Dequeue values are
`not-eligible`, `untrusted`, `head-changed`, `checks-failed`, `checks-timeout`,
`conflict`, `closed`, and `requested`. A pending dequeue is not a definitive
outcome: an in-flight landing may still merge the PR. Paused entries retain their
actual batch phase in `active_batch` and landing resolution continues while paused.

PR detail includes previously observed, rejected, dequeued and landed PRs.
Outside the queue, `position` is zero (rendered as `-` in the CLI table),
`admitted_at` retains the last successful admission (or `null` if never admitted), and `head_sha` is the latest recorded
head. An admitted entry always pins the admitted head despite later observations.
An unknown PR returns 404 without contacting GitHub.

WHY has `summary` (human-readable, not a parsing contract), `limit`, and `events`.
Events are the last N relevant evidence rows after grouping repeated CI pairs,
in increasing journal ID order:
direct PR observations/commands/outcomes, its batch and tested-SHA events, and
base/pause/resume/refresh/poison/rules events while admitted. Unrelated PR/batch/CI
events and action-delivery acknowledgements are excluded. Each item has `id`,
`type` (event discriminator or the CI pair type described below), `at` (journal recording time, UTC RFC3339), and
`summary` (a short sanitized explanation). These fields are part of API v1;
summary wording is for humans, not a parsing contract. Raw persisted wire
envelopes are not exposed and are outside API v1 stability. Results can contain
fewer than N events.
IDs order evidence; wall-clock timestamps do not authorize landing.
CI observed before `TestRefBuilt` is correlated with the batch that later
consumed it, so retained/reordered CI evidence remains explainable.
An admitted PR also includes current pause, poison and rules-block evidence when
those causes predate admission.

The API replays the same journal as the planner in a read-only repeatable-read
transaction. Current state, `watermark` (last queue event ID, zero if empty), and
explanation share one snapshot. It reads only this serve process's configured
targets; other queues in the same database are not exposed. It makes no forge
calls and reports recorded observations, which can lag GitHub by a poll interval.
Lists currently replay full histories without pagination or compaction; this
matches Phase 1's store, but large deployments will need snapshots/pagination.
Each request has a 15-second database-read deadline.

Errors use `{"version":1,"error":"CODE","message":"DETAIL"}`:

| HTTP | Code | Meaning |
| --- | --- | --- |
| 400 | `invalid-request` | Invalid path number/query |
| 401 | `unauthorized` | Missing/invalid bearer token |
| 404 | `not-found` | Unknown endpoint, unserved queue, or unknown PR |
| 405 | `method-not-allowed` | Unsupported method |
| 409 | `ambiguous-queue` | Multiple partitions match |
| 413 | `request-too-large` | Request body exceeds 4096 bytes |
| 503 | `unavailable` | Database read/decode/deadline failure |

Clients should ignore added fields within v1. Breaking response changes require
a new API version. Queue wire envelope versions are independent of API versions.

## CLI

```sh
openmerge queue status
openmerge queue status --json --api http://127.0.0.1:8081
openmerge queue status --repo acme/project --base main
openmerge queue status --repo acme/project --pr 42 --json
```

The CLI **uses HTTP only**, sharing server scoping, authentication and projection
semantics. It does not accept `--database-url`, need Postgres credentials, or run
migrations. `--api` defaults to `OPENMERGE_API_URL`, then
`http://127.0.0.1:8081`; use an HTTP(S) origin without path/query/credentials.
It uses the same token environment/file options as serve. Redirects are refused.
Timeout is 20 seconds; responses over 32 MiB are rejected.

Omit `--repo` to list all queues. `--repo owner/name` lists every served base
in that repository; add `--base` to select one queue. PR detail defaults to
`--base main`. Explicit `--base` and `--pr` require `--repo`.
Human output is a compact table; PR detail adds its explanation and event IDs,
timestamps, types and summaries. `--json` writes the successful API JSON **byte for byte**,
including its trailing newline; errors go to stderr and produce no stdout JSON.
On non-200 responses, the CLI reads at most 8 KiB of error JSON and prints its
sanitized `error` code and `message`; malformed/oversized errors use a generic
HTTP diagnostic. Error output never includes the raw response body.

| Exit | Meaning |
| --- | --- |
| 0 | Successful read or help; a paused/dequeued/blocked PR is still a successful read |
| 1 | Transport, server, protocol/version/size or output failure |
| 2 | Invalid flags, arguments, URL or local token configuration; API `invalid-request` (400) or `request-too-large` (413) |
| 3 | Queue/PR/endpoint not found (HTTP 404) |
| 4 | Authentication/authorization failure (HTTP 401/403) |
| 5 | Ambiguous queue (HTTP 409, `ambiguous-queue`); select a uniquely served partition |

These exit codes describe the read operation. Automation must inspect JSON
`status`, `paused`, and reasons to decide what to do next.

### Repeated CI observations

The why view collapses consecutive identical `checks_reopened` +
`checks_refreshed` pairs for the same batch and tested SHA into one evidence row.
The row uses `type: "checks_reopened+checks_refreshed"`, the first event's `id`
and `at`, plus optional `count` (number of observations/pairs), `end_id` (last
refresh event), and `until` (last refresh record time). `events=N` limits rows
**after grouping**, retaining useful preceding evidence during long pending CI.
The CLI renders the count and time range on one line. Unpaired events, changed
outcomes, and intervening relevant decisions remain separate rows.

Reopen summaries say `CI still pending` when the fold has no success to revoke.
`CI reopened; previous success revoked` appears only when that event actually
cleared recorded success, including success retained before test-ref creation.
A pending read after `LandingStarted` does not revoke a landing; its summary
explains that landing already started. The underlying journal is unchanged.

## Recorded change briefs (Stage A 7b)

`GET /v1/queues/{owner}/{repo}/{base}/changes/{id}/brief?audience=reviewer|operator|agent`
reads a coherent committed journal snapshot through the existing status listener.
The audience query is required and accepts exactly one value. Escape each path
component independently, including `/` in branch names (`release%2Fv1`). IDs
are resolved only in the selected queue. Unknown IDs, IDs belonging to another
queue, and unserved queues return 404. Multiple matching installations or lanes
return the existing 409 `ambiguous-queue` diagnostic. Read-token authentication,
GET-only behavior, request-body limits, `Cache-Control: no-store`, and error
DTOs match the other status routes. This adds no write authentication.

The response is the public brief v1 DTO, independent of queue wire encodings:
`version`, `audience`, `change_id`, `digest`, `progress`, `claimed_intent`,
`classification`, `obligations`, `stances`, `tests`, `warnings`, `truncated`.
Classification includes revision/generation, pinned head/base/merge base, semantic
policy digest, classifier, labels with reasons, retained file/hunk inventory,
and governing owner evidence. Truth values are the strings `unknown`, `false`, and `true`.
Intent is explicitly the submitter's claim. Stances distinguish native reviews
from receipt evidence, retaining source ordering/time provenance and Q4 recovery
steps. Native intent/brief bindings say `not recorded`; they are never inferred.
Tests retain check name, safe HTTP(S) URL, status, and tested SHA when recorded.

A null section means **not recorded**; an empty array means recorded and empty.
Known-empty stances render as "none recorded" in text.
The historical policy loader uses the ConfigApplied commit and digest recorded
at classification time, reading only immutable objects already in the local Git
cache. It never fetches, hydrates missing objects, resolves mutable refs, queries
the forge, or refreshes eligibility. A busy cache immediately returns unavailable.
Requirements and a brief digest require verification of both the effective config
digest and the classification's semantic policy digest. `policy_digest_mismatch`
means reconstructed semantic inputs disagree. `policy_unavailable` means the
pinned objects or recorded identities are missing, or the effective configuration
cannot be reconstructed. Legacy journals have no semantic approval policy;
the candidate-only schema omits effective legacy settings, so both currently
withhold requirements and the brief digest with `policy_unavailable`. A later
config cannot substitute for the historical pin. Restore pinned local objects
and recorded identity inputs; full config composition belongs to 11b. Recorded
progress is informational and never a fresh landing authorization.

Display bounds are 32 rows per collection and eight hunks per displayed file.
Each text field has a 2,048-byte JSON-escaped bound. Separate text budgets reserve
64 KiB for metadata, 96 KiB for obligations, 96 KiB for provenance, 64 KiB for
tests, and 192 KiB for hunks (512 KiB total). Machine fields have per-field
bounds without consuming the content budgets. Safety warnings survive row
limits. `display_truncated` directs readers to retained journal evidence;
responses are capped at 2 MiB. The handler sanitizer covers every final display
string, including submitter title/body, paths and hunks. `display_redacted`
indicates that shown text differs from the recorded facts covered by the digest;
display sanitization never changes those facts or their digest. The sanitizer
also normalizes whitespace and bounds strings to 1,024 runes. Text rendering
JSON-quotes untrusted text and escapes HTML/control characters; links accept
only HTTP(S) without userinfo.

Agent output uses stable reason codes and structured `next_step` objects without
prose recovery instructions. Actions include `split_change`, `resolve_owners`,
`record_eligibility`, `wait_for_approval`, `new_revision`,
`reobserve_then_fresh_approval` with `source_time_after`, `trust_exact_head`,
`fix_eligibility`, `rebase_onto` with the recorded base branch/commit, and
`fix_check` with name, safe URL and tested commit. Historical superseded failures
remain visible but do not produce current `fix_check` advice. Queue blocks use
`refresh` with their source for operator-clearable blocks (`@openmerge refresh`),
or `wait_for_recovery` for automatic recovery. Policy recovery uses
`restore_recorded_policy`; display warnings use `read_recorded_evidence`.
A source-time bound is strict; without a comparable bound, a new revision and
fresh approval are needed.

### CLI brief view

```sh
openmerge change brief chg1_<64-lowercase-hex> --repo owner/repo --base main --audience reviewer
openmerge change brief chg1_<64-lowercase-hex> --repo owner/repo --audience operator
openmerge change brief chg1_<64-lowercase-hex> --repo owner/repo --audience agent --json
```

`--repo` is required; `--base` defaults to `main`; `--audience` defaults to
`reviewer`. Put the ID before flags. `--api`, `OPENMERGE_API_URL`,
`--api-token-file`, `OPENMERGE_API_TOKEN_FILE`, and `OPENMERGE_API_TOKEN` behave
as for `queue status`. The client refuses redirects and caps responses at 2 MiB.
`--json` emits response bytes unchanged; the agent view also renders as JSON.
Reviewer text leads with intent and evidence; operator text leads with queue
progress and blockers, including what clears them. Exit codes match status:
0 success, 1 transport/unsupported response, 2 usage/invalid request,
3 not found, 4 authentication/authorization, 5 ambiguous queue.
