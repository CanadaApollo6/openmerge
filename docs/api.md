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
| `/v1/queues` | `{"version":1,"queues":[QUEUE,...]}`; empty list is `[]` |
| `/v1/queues/{owner}/{repo}` | List every served queue for this repository |
| `/v1/queues/{owner}/{repo}/{base}` | `{"version":1,"queue":QUEUE}` |
| `/v1/queues/{owner}/{repo}/{base}/prs/{n}` | `{"version":1,"queue":QUEUE,"pr":ENTRY,"why":WHY}` |

URL-encode each path component, including slashes in base branch names:
`/v1/queues/acme/project/release%2Fv1`. PR numbers are positive integers.
PR detail accepts `?events=N`: default 20, range 1–100. Other query parameters
are rejected. A queue is identified internally by installation/repo/base/lane;
lists include all four fields. A detail path matching multiple served partitions
returns 409 rather than choosing one. Phase 1 serve uses one installation and
the default (empty) lane.

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
