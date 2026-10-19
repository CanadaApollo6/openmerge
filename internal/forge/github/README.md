# GitHub App driver

`New(Config)` loads an App ID and a PKCS#1 or PKCS#8 RSA PEM key (at least
2048 bits). `app.Installation(ctx, id)` discovers the App slug using `GET /app`
and returns a shared, installation-scoped `forge.Driver`. Keep one `App` per
credential set; the caller must select the installation that owns the repository.

For GitHub Enterprise Server, set `APIBaseURL` to `https://HOST/api/v3/`.
`GitBaseURL` defaults to `https://HOST/` (`https://github.com/` for the public
API). Both can be configured independently. HTTPS is required except for
loopback HTTP test servers. API credentials are restricted to the configured
origin and API path, and redirects are disabled.

Pass `driver.Remote` and `github.HeadRef` into `gitops.Config`. The remote URL
contains no credentials; `gitops.Auth` supplies `x-access-token` and the fresh
installation token through gitops' askpass transport. App JWTs use RS256,
`iat = now - 60s`, and `exp = now + 9m` (ten minutes between the claims).
Installation tokens are refreshed at five minutes before expiry. Concurrent
refreshes are coalesced per installation. Credentials and raw API diagnostics
never appear in driver errors or logs.

## API behavior

- Reviews use GitHub's `commit_id`, including an old SHA after a push. Each
  reviewer's newest decisive review ID wins. `CHANGES_REQUESTED` and `DISMISSED`
  revoke previous approvals; `COMMENTED` and `PENDING` do not. Writer permission
  comes from a freshly revalidated collaborator permission API request on every
  current-head fork approval. Stale reviews and same-repository PRs do not cause
  permission reads; their approval `CanWrite` is false. Closed PRs skip reviews
  and permissions entirely. Missing permissions deny access; other API failures
  propagate without granting trust. There is no boolean permission cache or
  permission lock held across network calls.
- Check runs use `filter=all`, and all review, check, status, and comment lists
  follow pagination. Native IDs order reruns within one producer. Check runs carry `Source:
  "app:<id>"` and a provider-supplied `Attempt` when a native ID repeats; statuses carry `Source: "status:<lowercase-creator-login>"`.
  Check suites for the SHA are read on every check refresh. A queued, running,
  or unknown suite state makes its run pending regardless of stale success.
  `HeadBranch` preserves the refreshed suite's `head_branch`; missing suites
  have unknown provenance. All methods follow the
  [queue CI provenance contract](../../../docs/operating.md#queue-ci-provenance).
  Missing producer identities and unknown outcomes are pending.
  Only the `OpenMerge` check owned by this App is excluded from CI observations.
- Speculative CI cancellation uses the optional `CheckCanceller` capability and
  `POST .../actions/runs/{id}/cancel`. The queue durably retires a batch and pins
  its recorded identities before dispatch. The driver validates the exact tested
  SHA, test ref, Actions job, workflow and current attempt; it never targets an
  unrecorded run. Accepted POSTs remain requested until a read reports completion.
  Missing `actions: write` degrades to unsupported; quota errors retry. The setup
  manifest already requests this grant for retries. Existing Apps/installations
  must approve it; cancellation never authorizes a landing.
- Same-commit check retries are optional. For GitHub Actions, an explicit
  retry request uses the workflow run ID from a validated Actions check URL and
  `POST .../actions/runs/{id}/rerun-failed-jobs`. Foreign-App check runs and
  commit statuses are unsupported. Each target's intent must commit before POST,
  and acceptance immediately after. Uncertain outcomes are reconciled from the
  workflow's increased `run_attempt`; status alone never proves acceptance.
  Uncertain intents never authorize another POST. A durably recorded definite
  refusal permits re-dispatch only under a fresh fenced intent. Every requested
  rerun has a deadline, using one hour when `check_timeout: 0`.
  Retry-enabled observations join Actions job run_attempt to each check-run ID,
  preserving old jobs' original attempts. Default-off reads use no Actions API.
  Missing `actions: write` is a WARN and leaves ordinary check processing
  available. Existing installations must approve the new permission in App
  installation settings before Actions retries can run. After the App owner
  approves Actions, retries activate on the next installation-token renewal
  (up to about an hour) or immediately on a serve restart; the WARN persists
  until then. Before a retry request, outcomes match retry-off while events/status
  still show retry state. Permission loss during an outstanding rerun or uncertain
  dispatch reconciliation confirms failure immediately with "Actions permission
  lost during rerun; rerun outcome unknown" in dequeue details, PR status and
  `why`; the refused read is neither rerun evidence nor timeout evidence.
- Multiple Apps, multiple status creators, or a check/status pair using a required
  name are ambiguous. The executor keeps that check pending and returns
  `executor.CheckSourceAmbiguityError{Name, Sources}` after recording the refresh.
  No terminal check result is emitted. The server surfaces that diagnostic;
  configure distinct names for independent producers.
- Fast-forward build validation reads the base's classic linear-history setting
  and effective active branch rules (`rules/branches/{base}`, including inherited
  rulesets). Either readable requirement rejects new merge commits before the
  queue ref is pushed, even when the other source cannot be read. Unknown policy
  retains the existing behavior.
- `ListOpenPullRequests` paginates open PR summaries, filtered by target branch
  (or all branches for an empty filter), without loading approvals. `PullRequest`
  includes `Base`, `Draft`, `Author`, numeric `AuthorID`, `Title`, `Body`, and
  `HeadBranch` (GitHub's owner:branch label, falling back to the ref).
  `AuthorEmail` is `<id>+<login>@users.noreply.github.com`. The git transport
  reads this metadata again at test-ref build time, checking the pinned head;
  title/body edits after building never change the commit that lands.
- Labels are read from the PR. Serve requests changed files only when an entry
  or merge condition uses `files`; commit metadata reads omit them. Renames
  include both the previous and current filename. Distinct paths follow all pages
  up to the shared 1,000-path cap; larger PRs expose no partial list and `FilesKnown` is
  false. A declared count above the cap skips file reads. Within the cap,
  count mismatches and changes to head/base or condition/trust metadata reject the
  observation for retry. `ConflictKnown` is false while GitHub's `mergeable` is
  null; otherwise conflict is its inverse. Known empty labels/files differ from
  absent data. Serve journals these inputs with eligibility and retries
  unresolved mergeability on polls even without a changed `updated_at`.
- `ListComments` paginates issue-thread comments, including humans and bots.
  GitHub's `since` filters updated-at; the driver additionally filters created-at
  so an edit of an old command cannot become a new command. The server overlaps
  its timestamp cursor and dedupes by ID. `CanWrite` freshly authorizes commands.
- One App-owned `OpenMerge` check is discovered by head SHA and external ID
  `openmerge/pr/<number>`. queued/testing/landing map to `in_progress`; merged,
  dequeued, blocked, and paused map to completed checks with conclusions
  success, cancelled, failure, and action_required respectively. Output title
  includes state and Reason; output summary contains Summary (or the title if
  the supplied summary is empty).
- Comments carry `<!-- openmerge:key=<key> -->`. Dedupe trusts only comments
  whose user login is `<app-slug>[bot]` and type is `Bot`.
- `MarkLanded` posts a keyed link to the landed commit, then closes the PR.
  GitHub cannot mark externally squashed/rebased commits as a native merge.
  Retrying repairs a crash between the two writes; it never invokes a merge API.
- `DeleteRef` accepts a branch name or a full heads/tags ref. HTTP 404 and 422
  with exactly "Reference does not exist" (optional terminal period) succeed;
  other validation and authorization failures remain errors.
- `ReadFile` uses the contents API at an explicit ref. Missing files yield
  `APIError{StatusCode: 404}`. Directory responses and unsupported encodings
  (including contents over the API's inline size limit) fail closed.

A primary limit, a secondary limit, or `Retry-After` establishes a cooldown.
During it, requests return `RateLimitError{RetryAt, Secondary}` without network
traffic. Repeated secondary limits back off exponentially from one minute,
capped at 64 minutes, while honoring longer server reset/retry deadlines.
`Driver.BackoffUntil()` also exposes the shared cooldown and reserves the last
100 primary requests until reset for the polling server. The caller schedules
another attempt at `RetryAt`; the transport never retries
an uncertain write automatically. HTTP status failures yield `APIError` without
response bodies or request URLs; cancellation and deadlines remain recognizable.

GitHub offers no atomic create-if-absent for issue comments or check runs, and
external IDs are not uniqueness constraints. Local writes are serialized per
installation driver, and retries rediscover persisted effects. Exactly-once
creation across independent processes, delayed in-flight requests, or stale API
reads cannot be guaranteed by GitHub's API. The server must serialize durable
actions and must not overlap retries with unresolved HTTP writes.

## Webhooks and the server boundary

`VerifyAndParse(request, secret)` requires POST, a nonempty secret and delivery
ID, and an HMAC-SHA256 signature verified with `hmac.Equal`. It reads at most
25 MiB plus one byte, rejecting oversized bodies before JSON parsing.
`Event.Payload` is one of `PRChanged`, `ReviewChanged`, `ChecksChanged`,
`BranchPushed`, `BranchChanged`, `PRCommentCreated`, `InstallationChanged`,
or `Ignored`.
Unknown event names and unsupported actions are acknowledged as `Ignored`.
`check_suite` requested/rerequested/completed and `check_run` rerequested/completed
all produce `ChecksChanged{SHA}`; reruns must trigger a complete check/suite refresh.
The JSON files under `testdata/` are synthetic webhook fixtures and independent
REST fixtures (`testdata/rest/`). The HTTP fake serves those schemas rather than
serializing go-github response structs. It supplies 201 creates, JSON errors,
`total_count` for check collections, empty arrays, pagination, ETags, and rate
headers; it verifies App JWTs on every App endpoint and rejects unknown
installation IDs. No credentials or live user data are captured.

The package also provides the manifest callback and ruleset setup behind
`openmerge setup`. HTTP serving, persistence, worker loops, and commands live in
`cmd/openmerge serve` and `internal/serve`. Remaining work includes:

- Installation/repository discovery and reconciliation, token eviction after
  suspension/deletion.
- Installation repository enumeration remains a follow-up. Core PR condition
  observations, permission validation, and runtime classic protection reads are implemented.

## Setup and ruleset validation

Required App repository permissions:

| Permission | Access | Driver use |
|---|---|---|
| Contents | write | Git fetch/push, refs and config |
| Workflows | write | Test-ref builds and base landings that change `.github/workflows/` |
| Pull requests | write | PR/review reads, PR-thread comments and closure |
| Checks | write | CI reads and OpenMerge check reporting |
| Actions | write | Rerun failed GitHub Actions jobs when explicitly requested |
| Commit statuses | read | CI status observations |
| Issues | read | Issue-thread command reads and `issue_comment` webhook subscription |
| Metadata | read | Collaborator permissions, repository/default branch and ruleset reads |

Setup also requests organization **Members: read** (`members: read`) for
team owners or groups in approval policy. Existing Apps and organization
installations need to accept that addition only before using team-based approval
policy; [the operating guide](../../../docs/operating.md#organization-membership-permission-stage-a-prerequisite)
documents adoption and the live trial's permission freeze. `openmerge setup permissions --app-id ID --installation-id ID
--private-key FILE` diagnoses the actual installed grant. Only setup diagnostics
call `DiagnoseInstallationPermissions`, which invokes
`ValidateTeamPolicyPermissions` to inspect this optional grant. This is an
explicit diagnostic, not an automatic runtime check. Runtime team policy
resolution uses `ResolveTeam` and `TeamMembers`; definite unavailability creates
a durable `ConfigSource` block for the affected queue, while transient or
incomplete reads retry without a durable configuration block. The server's
unconditional permission check excludes the optional grant, so unrelated queues
receive no new warning or block.

All comment posting goes through `Driver.Comment`, including `MarkLanded`.
It uses `POST /repos/{owner}/{repo}/issues/{issue_number}/comments` for PR threads;
GitHub accepts [Pull requests: write for this endpoint](https://docs.github.com/en/rest/issues/comments#create-an-issue-comment).
No comment-posting path requires Issues: write.

`CanWrite` does not read organization membership or administration endpoints.
See GitHub's [collaborator permission requirements](https://docs.github.com/en/rest/collaborators/collaborators#get-repository-permissions-for-a-user).
`ValidateInstallationPermissions` compares the installation token response's
granted `permissions` against this table, including on token renewal. Missing
Contents/Workflows write becomes an ERROR and a durable rules block in serve;
other missing grants are WARN diagnostics. An omitted permission is not a grant.

`IdentityReader.WriteGrant` keeps the permission response's user ID and verifies
it against the supplied review/comment author ID, failing closed on missing or
mismatched IDs. HTTP 404 returns an error wrapping `forge.ErrIdentityUnavailable`
(and preserves `APIError`), matching the fake's unconfigured repository/user grant:
it means unknown, never a successful known-empty permission. Runtime policy
resolution can report an unavailable lookup; only an identity-bound successful
response can establish a known denial. `ResolveUser` reports user, bot, App, or
unknown account kind; GitHub App bot users remain bots, rather than being guessed
from login strings.
`ResolveTeam` pins organization and team IDs. `TeamMembers` reads all roles by
those IDs, including [members of child teams](https://docs.github.com/en/rest/teams/members#list-team-members),
and deduplicates by user ID. It rejects failed pages, invalid member identities,
and pagination beyond 100 pages of 100 members rather than returning partial
membership. These optional reads use the shared installation transport, always
revalidate, and cannot consume settlement's reserved budget. Legacy `CanWrite`
and `Approval` behavior is unchanged; runtime team policy resolution uses these
identity reads separately from the explicit installation-grant diagnostic.

`SetupRules` uses a separate admin `GITHUB_TOKEN`, with Administration read for
inspection and write for apply. It reads all pages and detailed rulesets,
including inherited policies; hidden bypass lists fail closed. Base edits send
only the updated bypass list. Fence edits retain existing rules and parameters;
incompatible conditions require manual correction. Writes are never retried
automatically. After a partial failure, rerun inspection before applying again.

`ValidateRules(rulesets, base, defaultBranch, appID)` is pure and returns typed
`RuleProblem`s with stable codes, policy IDs, refs and actionable messages. Supply complete
details including visible bypass lists, the landing base, and the repository's
actual default branch from `GET /repos/{owner}/{repo}`. `~DEFAULT_BRANCH` always
uses that default, even when landing on a release branch. It requires one active
policy covering the entire fence namespace with all four ref restrictions and exactly one
always-allowed App bypass. Every other applicable policy must allow the App.
Pathname-aware patterns cover nested base names; uncertain namespace overlaps
are conservative diagnostics. Classic branch protection is outside this function.
`serve` calls it through `ValidateRepositoryRules` at startup and on a slow
interval, using the installation client. Hidden bypass lists are explicitly
assumed App-only for structural validation and produce WARN logs; visible
problems pause as `repository-rules` until a later passing read.
`ValidateClassicProtection` independently reads the exact base and fence refs,
including required signatures (a separate read if omitted from the full
response). Required reviews without this App's bypass, required statuses,
signatures, locks, incompatible linear history, and push restrictions excluding
the App are problems. App identity uses the App ID, never the installation ID.
Linear history is compatible with squash/rebase and the fence's single-parent
updates. Force pushes are unnecessary for the current leased fast-forward
updates. Only the explicit `Branch not protected` 404 is a pass; generic 403/404
is unreadable. This endpoint needs installation Administration read, which is
not requested by the default manifest. Serve logs WARN and preserves only the
classic-protection source’s existing state when unreadable. Definite problems
from partially readable protection still block. Ruleset and permission sources
can independently block or recover, with their own revisions.
See the
[runtime rules policy](../../../docs/operating.md#runtime-repository-rules),
including GitHub's permission documentation. Do not treat ruleset-only validation
as proof about classic protection or hidden bypass ownership.

## App setup and troubleshooting

Start with [Setting up the GitHub App](../../../README.md#setting-up-the-github-app)
for the manifest flow, generated key, installation, webhooks and rulesets.

Subscribe to **Pull requests** (`pull_request`), **Pull request reviews**
(`pull_request_review`), **Check runs** (`check_run`), **Check suites**
(`check_suite`), **Commit statuses** (`status`), **Pushes** (`push`),
**Branch/tag creation** (`create`), **Branch/tag deletion** (`delete`), and
**Issue comments** (`issue_comment`). The parser also accepts `installation` and
`installation_repositories`, which GitHub sends automatically to Apps. These
eleven event names are the supported set in `webhook.go`. Use the same webhook secret
in App settings and the server. The server durably deduplicates typed-payload
delivery IDs; ignored payloads only wake workers in memory.

Find the installation ID in your personal/organization **Settings → Applications
→ Installed GitHub Apps → Configure** URL: its last component is the numeric
installation ID (`.../settings/installations/12345`). You can also obtain `id`
from the App-authenticated [repository installation endpoint](https://docs.github.com/en/rest/apps/apps#get-a-repository-installation)
or the signed installation webhook's `installation.id`. The App ID and
installation ID are different values.

For HTTP 401, first check that the configured App ID is the numeric App ID and
the RSA private key belongs to that App. Check that the installation ID belongs
to the intended account and still has access to the repository. Synchronize the
host clock with NTP: JWTs are backdated 60 seconds, expire after nine minutes,
and fail with larger skew. After correcting the clock or credentials, recreate
the App to discard old installation tokens. See GitHub's
[JWT requirements](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app).
HTTP 403 can mean missing installed permissions or a rate limit; inspect the
normalized `APIError`/`RateLimitError` and confirm the installation accepted any
new permissions. The driver deliberately omits raw response bodies from errors.
Setup additionally reports bounded, sanitized GitHub `message`, `errors[]`
resource/field/code/message details, and `documentation_url` links to GitHub Docs.
It redacts the admin token and manifest code, omits validation values and unknown
response fields, and strips credentials/query strings/fragments from diagnostic links.
An Integration bypass rejection during `setup rulesets --apply` explains that the
App must be installed on the repository first. If GitHub still rejects it on a
personal-account repository, keep protections enabled and use an organization-owned
repository that accepts the App bypass, or defer OpenMerge; a broader bypass would
break exclusive fence ownership.

## Polling cost and conditional requests

Keep one App/installation driver alive across polls. Every GET still contacts
GitHub, supplying `If-None-Match` when an ETag is cached. A 304 replays the cached
body and pagination headers as a 200 **inside the HTTP transport**, before
go-github can treat 304 as an error. GitHub's
[conditional request guidance](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api#use-conditional-requests)
states that correctly authenticated 304s do not count against the primary rate
limit. They still consume network requests and can encounter secondary limits.
Prefer webhooks with periodic reconciliation, and honor `RateLimitError.RetryAt`.

The concurrent LRU is bounded to 1,024 entries and 16 MiB of stored URL, hashed
identity/header, response-header, and body data per client transport. Keys include
the complete URL/query, a SHA-256 token identity, and request headers. Token
rotation and representation changes cannot reuse another identity's body.
Only a single valid strong or weak entity-tag is stored. A 304 must include one
valid ETag with the same opaque-tag; `If-None-Match` uses weak comparison, ignoring
the optional `W/` prefix. A missing, mismatched, or malformed 304 validator evicts
the entry and returns an error without replay or an automatic retry; the next
read is unconditional. These entries do not qualify for validatorless reuse.
Allowed 304 metadata updates are retained; `no-store` and `Vary: *`, on any field
line, evict and prevent replay.
Eviction, restart, token refresh, missing ETags, and uncacheable/oversized responses
cause normal GETs. Errors never replay a cached grant or CI result; writes are
never cached or retried. Permission entries are evicted by the same LRU.

For a PR with one current-head fork approver and at most one page per endpoint:

- Full PR refresh: **5 GETs** (PR, files, pinned-head/base recheck, reviews,
  permission). Same-repository PRs use **4**; a closed PR uses **1**. Declared
  file counts above the cap skip the files/recheck pair.
- Active tested-SHA check refresh: **3 GETs** (check runs, suites, statuses),
  shareable by all PRs in the same batch. Landing preflight repeats these **3 GETs**
  before recording an attempt.
- Polling-only command discovery: **1 GET** for a page of comments. Each command
  authorization adds a fresh `CanWrite` GET.

Thus one open fork PR plus one active CI SHA uses **8 GETs/cycle**, or **9** with
comment discovery. A warm unchanged cycle still makes 8/9 requests, typically
all validated 304s and **zero primary-limit charges**. The in-cycle PR recheck
can itself return 304 even on the first cycle. Changed responses charge only
the pages returning 200. These are estimates, verified by
`TestPollCycleRequestBudget`, conditional on endpoint ETag support. Add one per
extra page, one per additional relevant approver, and repository-level
`ListOpenPullRequests` pages shared across PRs. Initial App lookup and token
exchange and periodic repository protection/rules reads are separate costs;
tokens renew roughly every 55 minutes. Keep a stable comment
cursor until progress, then overlap it and dedupe IDs; changing the query URL
misses the prior ETag. The driver does not implement the server's polling loop.

## Dependency rationale (ADR 0001 / PR body)

The new direct dependency is `github.com/google/go-github/v93 v93.0.0`, the
current release when implemented (verified with GitHub's releases API). It
carries the REST request/response schema, endpoint encoding, and pagination
metadata for App authentication, reviews, permissions, checks, statuses, PRs,
comments, refs, and repository contents. That is the forge SDK exception
explicitly allowed by ADR 0001, and all SDK imports stay inside this package.
`github.com/google/go-querystring v1.2.0` is an indirect SDK dependency for query
encoding. JWT signing and webhook authentication use only the standard library;
no JWT or authentication framework is added.

Upstream references: [go-github releases](https://github.com/google/go-github/releases),
[App JWT requirements](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app),
[rate-limit handling](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api),
and [check runs](https://docs.github.com/en/rest/checks/runs).

`TestDriverContract` runs `forgetest.RunDriver` against an in-package
`httptest.Server`, with deliberately short pages. Focused tests cover all native
mappings, approval supersession, refresh concurrency, cooldowns, spoofing,
restart recovery, signature mismatches at every digest byte, and webhook fixture
normalization. No live API tests or credentials are needed.

## Build attribution and landed head cleanup

`CommitAuthors` follows the [PR commits API](https://docs.github.com/en/rest/pulls/pulls#list-commits-on-a-pull-request)
and exposes the linked `author.login` / `author.id`, separately from git commit
names/emails. It runs once per PR in a squash/merge test-ref build, never in the
poll loop; rebase preserves original authors and makes no extra call. Measured
by `TestCommitAuthorsPaginationAndRequestCost`: **one additional request** for a
single-page PR, one more per page (100 commits per page in production; the test
uses two-item pages). GitHub limits this endpoint to 250 commits; any unreturned
commits fall back to git email attribution. Supporting larger PRs with complete
linked-user attribution is a follow-up.

See the canonical [landed head cleanup protocol](../../../docs/operating.md#landed-head-cleanup)
for `HeadBranchForDeletion` eligibility reads, deletion leases, configuration,
and upgrade behavior.
