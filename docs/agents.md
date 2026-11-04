# Read OpenMerge from an agent

Connect to the read-only status API with its URL and bearer token. You do not
need Postgres credentials or the GitHub App private key. Ask the operator for a
served API URL and a token. See [API v1](api.md) for response contracts and
[the operating guide](operating.md) for server setup and recovery.

## Connect an MCP client

Build or install `openmerge`. Add one stdio server entry to your MCP client.
Replace the binary path, API URL, and example token with your configuration.
Keep the token in the client's secret configuration when it supports one.

```json
{
  "mcpServers": {
    "openmerge": {
      "command": "/absolute/path/openmerge",
      "args": ["mcp", "--api", "https://openmerge.example.net"],
      "env": {
        "OPENMERGE_API_TOKEN": "YOUR_READ_ONLY_TOKEN"
      }
    }
  }
}
```

Alternatively, set `OPENMERGE_API_TOKEN_FILE` or pass
`--api-token-file /absolute/path/token`. The file contains the token with an
optional trailing newline. Set only one token source. MCP requires a token,
including on loopback. `OPENMERGE_API_URL` sets the default API origin, which is
`http://127.0.0.1:8081`. The URL must have no credentials, path, query, or fragment.

Initialize the client and list tools. Start with `queues`, then select the
repository and base. Each registry read becomes a tool with the same name,
description, and parameter types. `live` is excluded because it is an
unauthenticated process probe. Supply raw path values such as `release/v1`.
Do not URL-encode them yourself. For example, call `pr` with:

```json
{"owner":"acme","repo":"service","base":"release/v1","n":42,"events":5}
```

Successful text content contains the API body unchanged, including whitespace.
Structured content contains the same JSON. Tool errors have `isError: true`.
API errors include the sanitized error code, message, and HTTP status. HTTP
401, 403, 404, and 409 remain distinguishable. A computing report returns a
normal result with its JSON and a separate `Retry-After` text item. Tool errors,
including HTTP 429 and 503, also carry this item when the header is valid.
The interval is an integer from 0 to 86400 seconds. Other values are ignored.
Wait for that interval, then repeat the call.

Each read has a 20-second client timeout. Redirects are refused. MCP rejects
bodies over 256 KiB without truncation. The error names a smaller parameter
value when the read has one and the request has not already selected it.
`detail=summary` reduces a full report. `limit=1` bounds a decisions page, but
one record can still exceed the limit. `events=1` reduces PR event history but
leaves embedded queue entries unchanged. If the body stays over the limit at the
smallest parameter values, no narrower read exists for that request.
`openmerge queue status --json` returns the full queue body, up to the CLI's
32 MiB limit. Use the API directly for other full read bodies. Stdout contains
only MCP protocol. Startup diagnostics go to stderr.

## Answer common questions

Use the following reads on this branch. Treat missing historical evidence as
unknown. Do not infer that a successful read means a PR is ready to land.

| Question | Read and next step |
| --- | --- |
| Why is my PR waiting? | Call `pr` with the repository, base, and PR number. Inspect `waiting_reason`, queue blocks, `why`, and the CI ledger. Use its change ID with `brief`, audience `agent`, for recorded next steps. |
| Which commit landed PR N? | Call `pr`. Read `landing.sha` and `landing.outcome`. Use `change` and `decisions` for recorded evidence. An outside-queue landing does not establish OpenMerge verification. |
| What is slowing our CI? | Inspect the PR's CI run ledger and queue readiness with `pr` and `queue`. Call `savings` for a bounded RFC3339 `since` and optional `until` window to measure observed runs and minutes. These reads do not replace a workflow bottleneck report. |
| What changed since I last looked? | Call `queue_events` with your saved cursor as `after`, and save `next_cursor` only after you consume the page. Drain pages while `has_more` is true, then wait at least 60 seconds. Keep one cursor per queue. For one change, page `decisions` with the last event ID as `after`. A queue watermark is not a cursor for either read. |
| What does this server serve? | Call `reads`, `queues`, `repository_queues`, or `discovery`. Use `health` for runtime readiness. |

CI doctor, Git cache, and queue events appear in `GET /v1` once their API slices
are deployed. Consult that registry for their parameters and descriptions.
New registry rows become MCP tools when you install a binary containing them.
A newer client can list a read that an older API does not yet serve. HTTP 404
then means you must check the deployed API's registry.

## Use CLI JSON reads

Queue and change reads use the same API and token settings. Pass `--json` for
machine-readable output and keep errors on stderr. Queue and change commands
preserve successful response bytes. Savings prints the savings payload without
the API envelope and requires an explicit `--api`.

```bash
openmerge queue status --json
openmerge queue status --repo acme/service --base main --pr 42 --json
openmerge change show CHANGE_ID --repo acme/service --base main --json
openmerge change brief CHANGE_ID --repo acme/service --audience agent --json
openmerge change decisions CHANGE_ID --repo acme/service --after 0 --limit 10 --json
openmerge report savings --api https://openmerge.example.net \
  --repository acme/service --base main --since 2026-11-01T00:00:00Z --json
```

Get `CHANGE_ID` from PR detail. Keep its revision when you need to discuss a
specific head. Queue status and API savings reads have a 32 MiB limit.
Change views use the smaller bound defined in the API contract. These exit codes describe the
read operation, not whether the change landed.

| Exit | Meaning |
| --- | --- |
| 0 | Successful read or help. Blocked and dequeued PRs can still return 0. |
| 1 | Transport, server, protocol, size, or output failure. |
| 2 | Invalid input, URL, or local token configuration. Also API `invalid-request` or `request-too-large`. |
| 3 | Queue, PR, change, or endpoint not found. |
| 4 | Authentication or authorization failure, HTTP 401 or 403. |
| 5 | Ambiguous queue, HTTP 409 with `ambiguous-queue`. Select a uniquely served partition. |

`openmerge mcp` uses exits 0, 1, and 2 for its process. Individual tool failures
travel in the protocol and do not exit the server.

## Act through the forge

Post commands as an authorized forge user on the PR. On GitHub, use the existing
forge credential with `gh pr comment`. For example:

```bash
gh pr comment 42 --repo acme/service --body '@openmerge queue'
```

Use `@openmerge dequeue` to cancel queue admission. Use `@openmerge refresh` to
retry after fixing an operator-clearable block. It cannot clear a known rules
block. When a recorded brief requests an intent receipt, an eligible reviewer
posts `@openmerge approve <brief-digest>` for that exact brief. Native reviews
remain subject to the configured approval policy. See [the operating
guide](operating.md) for command permissions and recovery conditions.

MCP has no write tools. The read-only bearer token cannot queue, dequeue,
approve, change policy, or submit a change. It cannot bypass a human approval,
forge protection, or missing evidence. Direct submission and a write API require
the separate authentication and authorization design in
[ADR 0007](adr/0007-changes-not-prs.md). Bulk audit export remains an operator
command. [ADR 0017](adr/0017-agent-first-reads.md) defines the registry and rollout.
