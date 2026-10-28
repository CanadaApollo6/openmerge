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
serve. Authentication covers unknown paths and unsupported methods as well,
except the exact `/v1/live` process probe.
Never use an App private key, GitHub token, or webhook secret as the API token.

## Endpoints

Except `/v1/live`, responses are JSON with `version: 1`, `Content-Type: application/json`, and
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

After approval configuration has loaded, a busy worker returns committed journal
evidence with
`readiness: {"ready": false, "reason": "policy_busy"}` and the last locally
loaded policy settings. This is a transient reporting limitation: retry the read
for current readiness. It does not pause the queue or change landing decisions.
Discovery retains its last inventory snapshot while that worker is busy. These
reads do not wait for gate, landing, or cache maintenance work. Legacy queues
retain their existing reporting behavior during the trial. Workers whose
configuration has not yet loaded also retain the existing startup behavior.

ENTRY status values are `queued`, `building`, `testing`, `landing`,
`dequeue-pending`, `paused`, `merged`, `dequeued`, `closed`, `blocked`, and
`not-queued`, and `waiting-entry`. `reason` is the pause or pending/terminal dequeue reason where
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
A held queue request has `status: waiting-entry`, `reason: entry-conditions`,
`position: 0`, and a `waiting_reason` with code `entry-conditions` and text
`Waiting for queue entry conditions: <conditions>`. Its `detail` names the
unknown conditions. Held requests appear in PR detail, not the admitted queue list.
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
the forge, or refreshes eligibility. Busy cache, worker or registry locks return
transient `policy_busy` with `retry` advice, without waiting past the request
context. Legacy configuration is loaded at the pinned commit, resolved for the
queue base branch with `config.ResolveBranch`, and checked against
`ConfigApplied.Digest` using `config.Queue.Digest(deleteHeadBranch)`. An explicit
`delete_head_branch` is respected; when omitted, either deletion setting may
match because deletion does not affect approval requirements. A match exposes
`approvals_required: false`, known-empty obligations `[]`, a brief digest and the
`approvals_not_required` marker (“approvals not required by the recorded policy”).
A digest mismatch, including older planner JSON composition, withholds requirements
and digest with `policy_digest_mismatch`. Load/resolve failures report
`policy_unavailable`. Resolution uses the worker’s known repository default
branch; an unknown default branch also withholds policy. To restore missing local
objects, an operator must fetch or import from backup the pinned `ConfigApplied`
commit and its tree/blob objects into the repository Git cache, including the
default-branch configuration commit for release queues. The reader never fetches.

Approval-bearing configs, including normal `version: 1` configs, always withhold
requirements and digest with
`policy_unavailable` and `implement_11b_policy_composition`: recorded-policy digest
composition is defined in **11b**. 11b must define, reconstruct and verify the
full effective queue plus approval-policy composition (`WithPolicyDigest`) against
the recorded pin before these requirements become available. Restoring local
objects cannot complete that missing implementation. Policy reconstruction alone
does not trigger `not_recorded` / `record_evidence` advice. A later config cannot
substitute for the historical pin. Recorded progress is informational and never
a fresh landing authorization.

For approval queues, obligation progress uses the latest journaled eligibility
observation in database commit order (`events.id`), including approval-wait
input updates. The observation must match the current revision, head,
classification and policy. A missing observation reports `eligibility_pending`
("eligibility pending gate read"); an outdated binding reports
`eligibility_stale`. Both use `wait_for_gate_read`. An observed unresolved read
still reports `unknown_eligibility`. Brief reads use only the journal and local
immutable Git objects and never refresh eligibility from the forge.

An unmet, resolved receipt obligation reports `receipt_required`, with
`next_step.action: post_receipt` and `next_step.command: "@openmerge approve <digest>"`.
The command remains present in agent output. Approval progress, eligibility,
display codes and next steps are excluded from the canonical brief digest, so
changing approval progress does not invalidate a receipt.

Display bounds are 32 rows per collection and eight hunks per displayed file.
Each text field has a 2,048-byte JSON-escaped bound. Separate text budgets reserve
64 KiB for metadata, 96 KiB for obligations, 96 KiB for provenance, 64 KiB for
tests, and 192 KiB for hunks (512 KiB total). Machine fields have per-field
bounds without consuming the content budgets. Safety warnings survive row
limits. `display_truncated` directs readers to retained journal evidence;
responses are capped at 2 MiB. The multi-line sanitizer redacts complete display
inputs before any field or section bounds apply, including title/body, paths and
hunks. It preserves hunk line breaks, tabs and indentation; brief text has the
2,048-byte JSON-escaped field cap above, without the diagnostic sanitizer's
1,024-rune cap. `display_redacted` signals credential redaction, independently of
truncation or control-character escaping. Machine fields (digests, identifiers,
SHAs, codes and enums) are excluded from display redaction. Recorded facts and
their canonical digest remain untouched. Text rendering JSON-quotes untrusted
text and escapes HTML/control characters; links accept only HTTP(S) without
userinfo. Obsolete batch/producer attempts remain in test history but never
produce current repair advice.

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

Renaming the repository default branch after an approval policy has been pinned
makes that pinned policy unavailable to the brief read boundary. The response
reports `policy_digest_mismatch`; read the governing policy again before giving
a receipt. Candidate approval policies continue to withhold requirements as
`policy_unavailable` until PR 11b defines the applied-config digest composition.

### Change reporting and immutable decisions (Stage A 12a)

`GET /v1/queues/{owner}/{repo}/{base}/changes/{id}` returns public v1
`{version, pr, brief}`. The PR is the existing PR lookup projection, extended
with `change`: canonical ID, revision, accountability, recorded waiting reason
codes, reviewer brief and decision paths. Queue entries and
`GET .../prs/{number}` (the `queue status --pr N` alias) receive the same additive
`change` metadata. Queue lists use the shared journal projection without
rendering per-entry briefs. The selected PR includes an `evidence` reviewer
brief only when Stage A evidence is recorded; legacy observations alone do
not trigger brief rendering. Change show and the PR alias return the same
Entry DTO. Existing fields retain their legacy meanings. An approval wait
reports separate `waiting_reasons`; operational dequeue and invalidation
gates retain precedence. An otherwise waiting entry reports gate `approvals`;
this does not add a second approval evaluator.

Change show and `.../brief?audience=reviewer|operator|agent` accept
`revision=N` (positive uint64). Omission selects the latest revision. Historical
views stop at the last committed event in that revision; later stances and
accountability cannot be attached to an old head. An unrecorded revision is 404.
The existing audience parameter remains required for brief reads.

`GET .../changes/{id}/decisions?limit=20&after=0&revision=N` returns
`{version, change_id, decisions, next_cursor}`. `limit` is 1–100, `after` is an
exclusive nonnegative journal event ID, and `revision` is optional; omission
selects all recorded revisions. `next_cursor=0` means the end of this page's
snapshot, not that future attempts cannot appear. Repeat nonzero cursors as
`after`. Repeated, unknown, malformed, or out-of-range query parameters are 400.
A known change without decisions returns `decisions: []`. The reader selects
only typed decision identities using migration 006's partial identity index,
then uses `LandingDecisionEvidence` to read the exact eligibility observation
named by the decision. It does not replay the journal or read mutable forge facts.

Each decision includes its journal `event_id`, batch and attempt IDs, revision,
variant, eligibility read ID, immutable brief/intent/policy digests, source
references and expanded requirements with eligible and relied-on IDs. A source
reference's `source_id` identifies the native review or authenticated comment;
`read_id` identifies its recorded acquisition event. `receipt_id` is only present
as evidence for a receipt command. Native reviews have intent and brief bindings
`"not recorded"`. `tested_sha` and `intended_landing_sha` identify recorded
inputs, while `landed_sha` is populated only by a confirmed `landed` event.
`outcome` is `unresolved`, `abandoned`, or `landed`; the settlement's journal
ID is separate. A later attempt cannot confirm an earlier attempt. Commit
trailers are never used to invent a decision or landing.

All IDs resolve only within the selected queue, including installation and lane.
A syntactically valid cross-queue or unknown ID returns 404; malformed IDs are
400. Ambiguous installations/lanes retain 409 `ambiguous-queue`. Escape a branch
containing `/` as one URL path component, such as `release%2Fv1`. These routes
share the existing GET-only bearer authentication, response errors and no-store
headers. A status bearer token grants no command or write authority. Responses
are capped at 2 MiB; an oversized page returns 503 and advises a smaller page.

Accountability distinguishes authenticated account, PR author, submitter and
accountable humans. Historical absence is `status: "not recorded"` with null
identities; a known empty list is `[]`, and null means unknown. A token
authenticates an account and cannot prove that a person rather than an agent
holding the token acted. Use separate bot or App identities so an agent does not
consume its human owner's approval.
The [organization sandbox runbook](research/stage-a-sandbox-evidence.md) records
installation grants, the authenticated receipt comment and both CLI/API
read-backs; its real sandbox execution remains pending maintainer resources.

Reviewer evidence includes classification reasons, requirements, native/receipt
stance provenance, and bounded receipt command processing history. A receipt's
historical `accepted` flag does not establish current eligibility. Its `binding`
is `rejected`, `stale`, `current` (matches the reconstructed brief), or
`"not recorded"` when current policy/brief reconstruction is unavailable. It is
not a second approval authorization result. Candidate-policy requirements and
receipt digests remain unavailable until 11b supplies recorded digest composition;
`policy_unavailable` and `"not recorded"` are explicit, never guessed.

CLI equivalents:

```sh
openmerge change show <id> --repo owner/repo --base release/v1 --json
openmerge change show <id> --repo owner/repo --revision 1
openmerge change brief <id> --repo owner/repo --audience reviewer --revision 1
openmerge change decisions <id> --repo owner/repo --base release/v1 --revision 1 --limit 20 --after 0 --json
```

`change show` always prints its decisions URL in human output and accepts
only the reviewer audience. Decisions supports revision selection and exclusive
`--after`/bounded `--limit` pagination.

They share queue status's API/token options and exit conventions: 0 success,
1 transport/unsupported response, 2 invalid invocation/request, 3 not found,
4 authentication/authorization, 5 ambiguous queue. JSON preserves server bytes.
There is no `change submit` or `change approve` command.

The single OpenMerge check adds a bounded reviewer summary only for approval
queues: digest and full-brief path, incomplete-inventory and other safety
warnings, accountability, native-versus-receipt evidence, and actionable waiting
reasons. Existing CI ledger and tested/landed wording remain in place. Legacy
queues append no new check text. Display strings are escaped; complete recorded
evidence remains available through the read projections and retained journal.

No event compaction or persisted snapshot pruning exists today.
`TestDecisionEvidenceRetentionNoPruning` rejects newly introduced destructive
SQL in production `internal/` and `cmd/` Go strings and SQL migrations,
including index drops and updates of event payloads. This is a source tripwire
for straightforward SQL literals, not a SQL parser or retention proof: quoted
identifiers, dynamic SQL and external database clients can bypass it. Any future pruning
implementation must replace that guard with integration tests proving retention
of `landing_decision_recorded` **and** its referenced
`approval_eligibility_observed`, preserving original read IDs after settlement.

Migration 007 adds partial indexes for queue-local legacy PR aliases, decision
pagination, and attempt settlement. It changes no config schema, event, action,
or landing behavior. Alias resolution transfers at most 100,001 integer PR
aliases (100,000 distinct aliases supported); exhaustion fails closed with 503.
Neither alias lookup nor decision settlement requires scanning unrelated event
types. Full decision/eligibility read-back continues through migration 006's
identity indexes. Decision responses include the exact `eligibility_event_id`;
status `landing` is omitted until an applied `Landed` event records a confirmed SHA,
batch, attempt and settlement event ID.

For opted-in queues, the existing GitHub check shares the ledger's journal
snapshot and projection. If recorded approval policy reconstruction fails, the
check is still updated with an explicit `policy_unavailable` warning and the
server logs `check approval policy unavailable`. Requirements and receipt
binding remain unavailable; this warning does not authorize landing. Publishing
honest unavailable evidence is intentional: retaining a stale check would hide
the loss of policy evidence.

## Authority and alerting probes (ADR 0008 PR 6, Hold)

Exact `GET /v1/live` returns only `{"live":true}` with 200, before authentication,
key discovery or DB reads. This response has no version or queue details. An
encoded alias or trailing slash does not bypass authentication. Live and health
reject queries (including an empty `?`) and any request body with 400; methods
other than GET return 405. Existing routes retain their body/query rules.

`GET /v1/health` uses the status listener's existing address and bearer-token
rules. Unauthorized requests return 401 without queue details. Authenticated
health is read-only: one `SELECT 1` and immutable process snapshots, with a 2s
request deadline, at most 256 queues and a 256 KiB response cap. Missing DB access,
a busy registry or a limit exceeded returns 503. Health never reads forge data
or replays queue histories. It reports lifecycle, readiness/reasons, whether the
queue is watched, process start, last successful reconciliation time/age, the
worker loop's effective next-start deadline, `in_cycle`, `cycle_started_at`, `last_progress_at`,
consecutive failures, overdue and degraded. Last success is null until a completed
reconciliation. Consecutive failures are informational. Degraded means that time since the later of last success and in-cycle progress
(or process queue start before either exists) exceeds `MaxSuccessAge`. Completed
actions and turns publish progress; journal activity and waiting wakes do not.
A progressing long cycle remains healthy past both 15m and 60m without inventing
a reconciliation success. Failure counts remain informational.
Dormant/retired queues without pending attempts and completed recovery-only
queues are excluded from alerting; unsettled starts remain watched.

The complete health 503 enumeration is:

- The health provider is unavailable, its read-only DB probe fails, or the 2s
  request deadline/cancellation prevents determining health.
- The worker registry is busy (TryRLock fails), more than 256 queues would be
  inspected, serialization fails, or the complete response exceeds 256 KiB.
- A watched queue is authority-violated, overdue, or degraded.

Readiness alone, warning-ledger availability, and failure counts do not add 503
conditions. Method/query/body errors remain 405/400 and unauthorized reads 401. Unverified authority
and outstanding outside-push warnings alone return 200. Health is for alerting;
**never configure it as a restart probe**. The process settings
`--health-overdue-grace` / `OPENMERGE_HEALTH_OVERDUE_GRACE` (default 30s, range
1–60s) and `--health-max-success-age` / `OPENMERGE_HEALTH_MAX_SUCCESS_AGE` (default
15m, range 1–60m), plus `--health-max-cycle-duration` /
`OPENMERGE_HEALTH_MAX_CYCLE_DURATION` (default 3h, range 2m–6h), are independent
of queue policy. While waiting, overdue means `now > next_deadline + grace`;
every immediate wake, including journal changes, sets that deadline to now.
Slot acquisition clears the start deadline and publishes the cycle start.
In-cycle overdue means `now > last_progress_at + progress_stall_limit`, or a
missing cycle start/progress timestamp (fail closed).
`--health-progress-stall-limit` / `OPENMERGE_HEALTH_PROGRESS_STALL_LIMIT` defaults
to 4m, range 1s–30m, with startup validation requiring at least action timeout +
30s Git maintenance + 10s completion write + configured grace. CLI and embedded
servers reject incompatible settings; longer embedded action timeouts require an
explicit compatible stall limit. The default 2m timeout requires 3m10s including
default grace, leaving 50s margin. Slot acquisition seeds progress; each completed
reconciliation source read, observation write, check-report write, action (after
its completion write) and turn advances it. GitHub also publishes after each
successful HTTP response body reaches EOF, so every pagination page and PR
sub-read counts independently. Failed responses, incomplete bodies and hung
requests cannot publish request progress. Read-unit progress likewise requires
success; completed actions and turns still record their completion independently.
A validated wire 304 served from cache counts as a successful round trip once
the cached body reaches EOF, including each revalidated pagination page. A 304
that cannot be served from cache publishes nothing.
The maximum cycle
duration remains an absolute backstop, even with recent progress. This permits a
hot turn's 64 chained actions while detecting a hung action promptly. Cycle end
publishes the next effective start deadline. Slot starvation remains detectable,
and a first cycle with zero delay does not become overdue after the grace period.

Authority queues add `authority` to queue JSON, omitted for legacy GitHub queues.
It contains `state` (`unverified`, `verified`, `violated`), `inventory`, per-element
`elements` visibility, `last_observed_at`, `last_verified_at`, and independent
`last_observed_age_seconds` / `last_verified_age_seconds`. Missing times/ages
are null; CLI text says “never (age unknown)” and “authority unverified”.
Future evidence timestamps render a null age, `clock_skew: true`, and the
corresponding `last_observed_clock_skew` / `last_verified_clock_skew` marker;
CLI age text says `clock_skew`. Health also marks future process success times
with `last_success_clock_skew`. Reading status never establishes verification. Health refreshes ages on its snapshots;
it caps visibility details at 64 elements per queue and exposes `elements_omitted`
when truncated. Ordinary status retains every per-element visibility.

This rendering consumes PR 4's durable `WriteRestrictionStatus` projection,
including sticky violations through hidden reads. Visibility comes from the
retained applied evidence. Proposed verification is never presented as applied
verification. `outside_warnings` consumes PR 5's outstanding ledger, with an
exact `outstanding` count, at most 20 `event_ids` in originating base-move order,
and `truncated` when IDs are omitted. Empty successful ledgers omit the field.
Ledger errors affect only that queue's warning section: `error: "ledger_unavailable"`
retains the last complete counts, which may be stale; they never fail a multi-queue
status response or worker registration. Registration logs the error and continues.

The per-queue warning cache advances only after its last event watermark. HTTP/CLI
status reuse the journal's already decoded snapshot, with zero additional warning
queries and no second history scan/decode. Queues that have never had an authority
pin skip the ledger entirely. Worker-owned CI check rendering omits warning I/O;
health reads immutable atomic summaries. Background refresh performs DB paging
against a private index and swaps it in only after success; no DB I/O holds the
cache lock. Foreground rendering never waits for that lock: contention returns
last complete counts with queue-local `ledger_unavailable`. Pin removal clears
the published health warnings; rollback refreshes the historical ledger as status does. The independent outside-push loop pages
only the new suffix (256 rows/page), probing indexed origins for new audits and
sources for new acknowledgements. Registration seeds authority queues once and
skips legacy queues. The index retains policy boundaries and outstanding event/
origin IDs; public samples remain capped at 20. Acknowledgement appears in health
on the next audit pass. Failed reads preserve the last complete warning snapshot
with the explicit error. Health requests never scan journals. Authority-era queues
keep using nonblocking readiness/policy snapshots after rollback, even with zero
outstanding warnings. Acknowledgement removes the warning while keeping
the deployed recent `outside_pushes` history unchanged. Pushes originating in
GitHub mode gain no authority warning; warnings from before a rollback remain
until acknowledged.

When a background warning refresh returns an older complete snapshot after an
interrupted foreground fold advanced its index, that snapshot carries
`error: "ledger_refreshing"`. A later complete fold/refresh clears the marker.

This reporting change may merge but **must not deploy until ADR 0009 period B
has drained**. PR 9 still owns authority activation; PR 11 owns real sandbox HTTP
acceptance for violation, drift and break-glass warning cases.

### ADR 0011 Hold dependency presentation (test activation only)

Dependency configuration remains rejected by the public loader. The internal
activation seam can install an optional `dependency` object on an entry, with the
same `code`, `text`, `details` and `url` contract as `waiting_reason`. Its
`diagnostics` array contains typed `{code, source}` records only when every
recorded audience member has identity-bound source read access and the source
journal binding agrees. Missing source bindings, diagnostics or audience evidence
fail closed. A bearer token and `audience=operator` do not establish source access.
The existing v1 rule allowing additive fields applies; inactive responses retain
their existing bytes. No persisted event is extended.

Unauthorized readers see `code: dependencies` and exactly `Dependency cannot be
verified`, even for a landed source. There are no source details, links or typed
diagnostics. Redacted explanation evidence is withheld before pagination.
Private records cannot change row counts or continuation behavior. Redacted queue views withhold unstarted batch and
CI evidence, including the check prefix, so build progress cannot disclose a
private source landing. When private history prevents exposing original
journal positions, `watermark`, explanation IDs and exposed event references are
`0` (withheld), never synthetic journal IDs. Authorized views retain original
positions. This convention is confined to the new, test-only activated view;
existing deployed v1 queues retain their original reference semantics.

Public checks require a separate complete recorded audience proof; an API reader's
grant never authorizes the check audience. Current dependency evidence is withheld
from explicitly selected historical revisions. Redacted historical reads retain a
generic dependency hold to conceal old unstarted progress. Rendering reads supplied journals only and never calls a
forge. Terminal B outcomes, landing settlement and B's independent gates retain
their existing precedence. Dependency presentation does not grant landing authority.

This is slice **5-1**. Scoped audit export, completed self-acknowledgement evidence,
the opt-in multi-writer warning, wait-age display and additional evidence pagination
remain slice **5-2**. The existing `events=1..100` limit is unchanged.
