# 0017. Serve every read to agents first

- **Status:** Accepted
- **Date:** 2026-11-04

## Context

OpenMerge's users increasingly work through coding agents. On 2026-11-04 the
owner set the order of consumers: "This should basically be meant to be used by
a user's agents first and the user in a UI second", and agent access "takes
priority over a dashboard".

The status API already serves much of what an agent needs: queue state, a pull
request's waiting reason and landing commit, its CI ledger, briefs, decisions,
health, and discovery. Three things an agent cannot reach with an API token:

| Information | Where it is today | What an agent needs to hold |
| --- | --- | --- |
| Savings report | `openmerge report savings` | Postgres credentials |
| CI doctor report | `openmerge report ci-doctor` | Postgres credentials and the App private key |
| Git cache lock and maintenance timing | Structured logs, mostly at DEBUG | Shell access to the server |

Giving an agent Postgres credentials or the App key is not acceptable. The
README also lists an MCP server that does not exist. ADR 0007 deferred it and
required that its tools call the same versioned read projections.

A live CI doctor report cannot be served as an ordinary request. Measured on
OpenMerge's own repository on 2026-11-04, for a 14-day window with 72 queue
runs, one report took 109 seconds, spent its whole budget of 500 GitHub API
requests, recorded 236 gaps, and produced 462 KB of JSON. The status API's
request deadline is 15 seconds. A result that size would also fill much of an
agent's context.

## Decision

### Order of consumers

Every read is a versioned JSON response from the status API first. The CLI, MCP,
and any later dashboard are projections of those responses, and none of them
gets information that the API does not serve. An agent needs only an API URL and
the read-only bearer token.

### A registry of reads

`internal/status` holds one table of reads. Each row names the read, its path
template, its parameters, and a description written for an agent. Three
consumers use the table:

- `GET /v1` returns it, so an agent can discover what exists.
- `openmerge mcp` builds one tool per row.
- Tests fail when a row has no working route, when `docs/api.md` omits a row, or
  when an `openmerge report` subcommand has no row.

The tests keep this decision true after it is written.

### New reads

| Read | Path under `/v1/queues/{owner}/{repo}/{base}` | Behavior |
| --- | --- | --- |
| Savings | `/reports/savings?since=&until=` | Computed from the journal in the request |
| CI doctor | `/reports/ci-doctor?window=7d\|14d\|30d&detail=summary\|full` | Served from a snapshot, as described below |
| Queue events | `/events?after=&limit=` | Cursor over the event projection that pull request detail already serves |

Git cache timing is served at `/v1/git-cache`: per-repository counts and bounded
wait, hold, and maintenance histograms since process start, plus garbage
collection outcomes and dropped samples. It is aggregated in memory and is not
stored.

### CI doctor snapshots

A request for a window with no fresh snapshot starts one background computation
and returns `202` with `Retry-After` and a `computing` status. A later request
returns `200` with `generated_at`. These rules keep the report a T0 diagnostic
under ADR 0016, including its rule that the report never changes queue
behavior:

- One computation runs at a time per server. Other requests get `202` with
  their own status, and no request waits on GitHub.
- Only an authorized read starts a computation. No timer does.
- A computation does not start when the installation's remaining GitHub quota
  is below a named reserve. The response then says so, with the reset time.
- Each computation keeps the existing budget of 500 requests. Exhausting it
  produces the existing `actions-budget` gap.
- Snapshots stay in memory for one hour, keyed by queue and window. The windows
  are a fixed set, so memory is bounded. A restart discards them, which keeps
  ADR 0016's rule that CI doctor stores nothing outside the journal.

`detail=summary` is the default. It returns the findings with their suggested
fixes, gap counts by code, sample counts, and thresholds. A test bounds its size
at 32 KiB on a recorded report. Each finding keeps a bounded sample of workflow
run IDs and their total count, which satisfies ADR 0016's rule that a finding
cites its evidence. `detail=full` returns the existing report contract.

### MCP

`openmerge mcp` is a read-only stdio server built on the official Go SDK,
`github.com/modelcontextprotocol/go-sdk`. Each tool sends one GET to the status
API and returns the body unchanged. It uses the same `--api` and token options as
`openmerge queue status`. It needs no database and no App key, and it adds no new
authentication surface.

### Writes stay where they are

Agents still queue, dequeue, and approve through forge-authenticated commands.
ADR 0007 forbids turning the read-only token into a write credential. Write tools
wait for the authentication ADR that ADR 0007 requires.

### What stays out

`openmerge audit export` stays a database-backed operator command. It writes a
bulk evidence bundle to a directory, not a read projection. The landing decisions
it draws on are already served per change.

Logs are not an API. Anything an agent needs from a log gets a read instead.

### Tenancy

Every new read uses the existing route selection, which confines a request to
the server's configured queues and returns 404 otherwise. The bearer token covers
them all. Each read needs a test for a wrong repository, a missing token, and a
wrong token, per ADR 0012.

### Rollout

| Slice | Exit evidence | Deploy class |
| --- | --- | --- |
| This ADR | Docs checks | Inert |
| Registry, `GET /v1`, savings read, CLI `--api` for savings | Registry tests; golden response equal to the CLI's JSON; tenancy tests | Hold |
| `/v1/git-cache` | Aggregation tests; real lock contention appears in the histogram | Hold |
| CI doctor snapshots and CLI `--api` | Fake-forge tests for `202` then `200`, single flight, quota reserve, and expiry; summary size bound; live report on OpenMerge's repository | Hold |
| Queue events read | Cursor and limit tests; same redaction as pull request detail | Hold |
| `openmerge mcp`, `docs/agents.md`, `llms.txt` | Scripted MCP client lists tools and reads a landed change on the fake forge; README claim becomes true | Inert until installed; the server does not run it |
| Dashboard | ADR 0012 tenancy tests; uses only registry reads | Separate approval |

The owner approved the status API slices on 2026-11-04. Each deploy still follows
the journal replay check in `docs/operating.md`.

## Consequences

An agent can answer "why is my change waiting", "what is slowing our CI", and
"is the Git cache contended" with one token and no server access. The API is now
the product surface, so changing a response is a contract change.

Snapshots mean CI doctor data can be up to one hour old, and the first request
after a restart waits for a computation. The response always states its age.

We now owe the contract tests above, and a reduction in GitHub requests per
report so that a 14-day window on a busy repository fits its budget. Completed
runs do not change, so reuse across snapshots is the first thing to try.

## Alternatives considered

- **Answer CI doctor synchronously.** Measured at 109 seconds and 500 GitHub
  requests, this would hold a connection past every deadline, and repeated calls
  could spend the quota the queue needs.
- **Store snapshots in Postgres.** They would survive restarts, but ADR 0016
  stores nothing outside the journal, and a one-hour diagnostic does not justify
  a migration.
- **Give agents database access through the CLI.** This hands out the credentials
  that the API exists to withhold.
- **Write the MCP protocol by hand.** The read-only subset is small, but protocol
  version negotiation would become ours to maintain.
- **Build the dashboard first.** The owner ranked it second, and it would need
  these reads anyway.
