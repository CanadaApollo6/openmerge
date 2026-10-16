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
  comes from the collaborator permission API, cached for one minute. Missing
  permissions deny access; other API failures propagate without granting trust.
- Check runs use `filter=all`, and all review, check, status, and comment lists
  follow pagination. Native check run IDs and native status IDs monotonically
  order reruns within their respective histories. Unknown outcomes are pending.
  Only the `OpenMerge` check owned by this App is excluded from CI observations.
- Check runs and legacy statuses have independent ID sequences. Reusing a name
  across both APIs cannot meet the contract's ordering rule with native IDs.
  `CheckNameCollisionError` fails closed in that case; use distinct check names.
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
The caller schedules another attempt at `RetryAt`; the transport never retries
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
`Event.Payload` is one of `PRChanged`, `ReviewChanged`, `ChecksCompleted`,
`BranchPushed`, `PRCommentCreated`, `InstallationChanged`, or `Ignored`.
Unknown event names and unsupported actions are acknowledged as `Ignored`.
The JSON files under `testdata/` are synthetic, real-shaped webhook fixtures;
no credentials or live user data are captured.

This package does not implement HTTP serving, persistence, worker loops, or
commands. The server PR still owns:

- Durable delivery dedupe, webhook response handling, and event-to-queue routing.
- Installation/repository discovery and reconciliation, token eviction after
  suspension/deletion, and persistent action serialization and retry scheduling.
- Base-branch `.openmerge.yml` loading/validation and configuration reloads.
- Command parsing and permission checks (`CanWrite` is available).
- Constructing gitops repositories, cache ownership, and executor wiring.
- Branch/ruleset protection and App permission validation, including protected
  base bypass and exclusive, nondeletable `openmerge/fence/*` ownership. APIs for
  ruleset inspection, PR labels/files/draft metadata, and installation repository
  enumeration are not included; add narrow reads when their consumers arrive.

Required App repository permissions are contents read/write (Git fetch/push and
config), pull requests read/write (reviews, comments, closure), checks read/write,
commit statuses read, and metadata read (collaborator permissions). Startup
validation of installed permissions and organization rules remains server work.

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
