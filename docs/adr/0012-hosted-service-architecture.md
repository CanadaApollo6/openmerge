# 0012. Hosted service architecture

- **Status:** Proposed (Inert; documentation only)
- **Date:** 2026-10-27

## Context

OpenMerge becomes a product with hosted and self-hosted editions. The owner keeps the merge engine
in Go and chooses Cloudflare for the hosted control plane. Hosting must preserve the engine's safety
and audit contracts rather than reimplement them in a second runtime.

[ADR 0004](0004-forge-driver-interface.md) makes the engine responsible for real Git construction
and fenced landing through forge-neutral interfaces. [ADR 0006](0006-multi-repository-serving.md)
makes webhooks scheduling hints, with fresh forge reads and polling as the authority. [ADR
0007](0007-changes-not-prs.md) binds review evidence to changes and revisions. [ADR
0008](0008-openmerge-policy-authority.md) adds forge write restrictions, owner-approved policy,
database-bound activation receipts, and audit exports. [ADR 0009](0009-speculative-batching.md)
keeps one ordered landing state machine. [ADR 0010](0010-agent-provenance.md) and [ADR
0011](0011-cross-repository-dependent-changes.md) extend its evidence, not its set of authorities.
Hosted operation must retain those contracts.

The current [status API](../api.md), implemented in `internal/status`, exposes GET-only `/v1`
endpoints. Its bearer token permits reads over the configured engine scope, not user impersonation
or queue mutation. The [operating guide](../operating.md) also imposes whole-database restore
boundaries that a dashboard deployment cannot waive.

This ADR proposes the product boundary and hosting choices. It adds no runtime, schema, migration,
credentials, infrastructure, App grants, or active service. **Implementation starts only after ADR
0009 period B drains**, expected in mid-November 2026. A calendar date does not establish the drain.
Design and this docs-only PR may proceed now. Acceptance still needs the runtime clarification below
and the implementation gates in the rollout plan.

## Decision

### Keep one merge engine and put product services around it

Ship the same Go engine in both editions. It remains the single authority on what lands and owns:

- Git operations through the real `git` binary, including merge and batch
  construction, `rev-list`, `cat-file`, and trailer parsing under `prlimit`
  resource caps. Workers do not execute repository code or build test commits.
- The Postgres event journal, strict decoders, deterministic projections,
  advisory locks, action recovery, fences, and evidence retention.
- ADR 0008 policy authority, activation/completion checks, and audit export;
  ADR 0010 provenance; and ADR 0011 dependency validation and decisions.
- Fresh forge identity, permissions, policy, trust, CI, and protection reads
  needed to authorize a durable landing start and its final push guard.

The control plane owns accounts, organizations, subscriptions, onboarding, hosted resource
provisioning requests, dashboard views, notifications, and optional AI assistance. It cannot write
engine tables, construct commits, record approval evidence, satisfy dependencies, or tell a fence
which outcome to record. The forge driver boundary remains in Go for every supported forge.

The intended hosted paths are:

```text
Forge webhook -> Worker ingress -> Queues -> delivery Worker
                     |                         |
                     v                         v
              R2 delivery spool       private engine webhook listener
                                               |
Browser -> Worker dashboard/API -> private GET /v1 API
            |       |                          |
            |       +-> D1 accounts/catalog    v
            +-> installation DO         Go engine cell -> forge API/git
            +-> optional Workflows             |
            +-> AI Gateway -> model            v
                                      dedicated installation Postgres
                                               |
                                     read-only audit exporter
                                               |
                                R2 bundles -> independent signed anchors

Regional credential broker -> installation-scoped token -> Go engine cell
```

The diagram describes trust boundaries, not implemented network routes. Delivery and status use
different listeners and credentials. Credential issuance and audit custody are separate from the
public Worker and its deployment role.

### Choose a small set of Cloudflare services

Use Workers for HTTPS ingress, product APIs, BetterAuth, and dashboard assets. Use Queues for
asynchronous webhook delivery and optional assistance jobs. Use one Durable Object per installation
for control-plane coordination. Use D1 for durable product records and R2 for immutable audit
bundles and a separate short-lived webhook spool. Defer Workflows until onboarding needs multi-step
retries. Do not introduce KV in the alpha.

D1 holds BetterAuth records, organization-to-installation bindings, billing references, desired
hosting region, and provisioning request IDs. It holds no engine events or landing decisions.
BetterAuth's [Drizzle adapter](https://better-auth.com/docs/adapters/drizzle) supports SQLite; D1
uses [SQLite semantics](https://developers.cloudflare.com/d1/best-practices/query-d1/). This is an
integration choice, not evidence that every plugin works on Workers. Pin versions and qualify
accounts, organizations, token revocation, and SSO against D1 and `workerd` before alpha. A failed
qualification blocks that slice; it does not move auth tables into an engine database.

KV is eventually consistent, with updates potentially invisible elsewhere for [60 seconds or
more](https://developers.cloudflare.com/kv/concepts/how-kv-works/). Later use may cache public
assets or non-sensitive hints. It must never store revocation decisions, installation ownership,
routing authority, or engine state.

### Use Bun and name the Worker exception honestly

Choose **Bun**, rather than scriptc, for TypeScript package management, scripts, unit tests, builds,
and any non-Worker TypeScript server. Bun documents native [TypeScript
support](https://bun.sh/docs/runtime/typescript) and a [test runner](https://bun.sh/docs/test). Pin
the toolchain and lockfile. Run explicit type checking as well as tests; transpilation is not a type
check.

Public research confirms that [Vercel Labs' scriptc](https://github.com/vercel-labs/scriptc) is an
experimental compiler for supported TypeScript and JavaScript to native executables and WebAssembly.
Its README describes a subset of language and Node.js APIs, build-time diagnostics for unsupported
code, and a [`scriptc coverage` compatibility check](https://scriptc.dev/docs/limitations). It is
not a documented replacement runtime for Cloudflare Workers. Do not infer BetterAuth, Worker
bindings, or dashboard framework compatibility from its WebAssembly output. This ADR runs no scriptc
compatibility experiment and makes no performance claim for it.

Cloudflare Workers execute on [`workerd`](https://github.com/cloudflare/workerd), not Bun. Browser
dashboard JavaScript also executes in the user's browser. Using Bun to produce either artifact does
not change those runtimes. Workers currently have a [128 MB isolate memory limit and bounded
CPU](https://developers.cloudflare.com/workers/platform/limits/), which further favors small request
handlers over Git or journal replay there.

Propose this explicit interpretation of the owner's runtime requirement: OpenMerge-controlled
TypeScript tooling and conventional servers use Bun; browser bundles and Cloudflare-managed Worker,
DO, and Workflow execution are named deployment exceptions. Worker integration tests must exercise
`workerd` through Bun-invoked tooling, in addition to Bun unit tests. Qualify the actual CLI
dependencies under Bun; `bunx` alone does not prove their execution runtime. Do not silently add a
Node service or claim that Worker tests run on Bun.

**Owner ratification of these exceptions is required before TypeScript Worker implementation.** If
the requirement literally includes every execution host, this design cannot satisfy it as written.
The alternative is a Bun control-plane origin behind Cloudflare ingress, with the Worker role and
storage adapters redesigned before implementation. scriptc is not a demonstrated escape from this
conflict. Selecting Bun now avoids adding an experimental compiler to the hosting and auth
qualification work.

### Keep Postgres as the only engine system of record

Give each hosted installation one dedicated logical Postgres database with one OpenMerge schema and
one active Go engine cell. A cell serves that installation's repositories and bases. It uses the
existing installation allowlist as a narrowing restriction; successful forge discovery still decides
which repositories exist and may be served.

ADR 0011 dependency resolution stays within this verified installation/journal scope. Hosting
does not add cross-tenant dependency lookup or let a public reference authorize remote evidence.

This adopts the current [dedicated-database authority
model](../operating.md#public-authority-activation-9c-3-hold). Every registered queue counts,
including paused, retired, excluded, and other-repository queues. A second installation must still
stop authority activation and final authority pushes. Do not weaken that guard for hosting. No
schema-per-tenant journal, shared queue process across tenant databases, or row-level tenant column
is introduced here. Shared physical database servers are possible, with separate database roles and
restore units; noisy-neighbor and cluster-admin risks remain. Shared multi-tenant engine databases
require a future ADR for scoped locks, watermarks, receipts, completion, and restores.

Use managed Postgres for production, with **PlanetScale Postgres as the first candidate to
qualify**, not its MySQL product. The provider publishes [Postgres direct and PgBouncer connection
options](https://planetscale.com/blog/scaling-postgres-connections-with-pgbouncer). Use direct
primary connections initially. Do not insert transaction pooling, Hyperdrive, a read replica, or a
serverless SQL adapter into lock-sensitive engine paths without proving their semantics against the
store contract.

The qualification must run actual migrations, concurrent advisory-lock and transaction tests, crash
recovery, full replay, backup/restore, and authority activation on the selected service tier. It
must establish permitted execution of `pg_catalog.pg_control_system()` for the operator/activation
role. **Zero acknowledged-commit loss on failover is a pass/fail requirement:** qualify synchronous
commit to durable HA replicas and promotion semantics that preserve every acknowledged write, and
test primary loss immediately after committed `LandingStarted` and a successful forge push. A local
`synchronous_commit` setting alone does not prove replica durability or safe promotion. PostgreSQL
[documents the loss risk of asynchronous replication and synchronous durability semantics](https://www.postgresql.org/docs/current/warm-standby.html#SYNCHRONOUS-REPLICATION).
Losing that start while retaining pushed refs would misclassify our own landing as an outside push.
Public provider descriptions do not establish identity privileges or these HA guarantees. If either
is unavailable, choose another managed Postgres tier that passes; never omit a receipt check or
silently promote a replica with missing commits. Any RPO above zero requires the restore and ref
reconciliation procedure below, never transparent failover.

[`internal/store/database_identity.go`](../../internal/store/database_identity.go) binds
`openmerge-db-v2` to the cluster `system_identifier`, database and schema names/OIDs, and journal
genesis. It requires all eight persistent tables to resolve unambiguously in that schema and a
nonempty journal. Preserve its fail-closed behavior. Different logical databases remain different
identities even on one cluster. A logical restore changes identity; a physical clone preserving
identity inputs can be indistinguishable. The [receipt
limitations](../operating.md#receipt-database-binding) remain true.

D1 and DO SQLite cannot supply the engine's existing Postgres lock and catalog contracts. Neither is
an engine-store replica or failover destination. R2 exports are evidence archives, not a journal
from which Workers may run a queue. D1 may be the system of record for product accounts without
becoming a second system of record for engine state.

### Ingest webhooks at the edge and push hints to the engine

Hosted webhook ingress terminates at a Worker. It validates the raw-body forge signature, enforces
size/rate bounds, and routes only an already provisioned installation. It never trusts a payload
repository list as access discovery. The webhook secret is separate from the GitHub App private key.
GitHub configures a [webhook secret per App](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app),
so cells for that App share it. A compromised cell can forge wake hints for other installations;
ADR 0006 bounds this to scheduling/load because fresh forge reads still authorize every decision.
Rate-limit hints and consider separate App partitions for large tenants; signatures alone are not
tenant isolation.

Persist the exact raw body and required original event/signature/delivery headers in a private R2
spool, then enqueue a small versioned envelope containing the object digest/key, App identity,
installation, delivery ID, and receipt time. Return success to the forge only after durable storage
and queue publication succeed. A storage success followed by publication failure leaves an orphan to
expire and returns failure; redelivery may safely publish it again. Do not acknowledge first and
rely on `waitUntil` for durability.

Cloudflare Queues has [at-least-once
delivery](https://developers.cloudflare.com/queues/reference/delivery-guarantees/) and a [128 KB
message limit](https://developers.cloudflare.com/queues/platform/limits/). The spool keeps raw
webhook bodies out of that envelope limit. Use a bounded payload limit compatible with the Go
listener; reject oversize input explicitly. Configure a paid queue's retention and dead-letter
handling for the outage budget, within the documented maximum of 14 days. No queue retention period
replaces engine evidence retention.

A delivery Worker retrieves and hashes the object, then pushes the unchanged body and original
headers over authenticated private ingress to the cell's existing webhook listener. The engine
verifies the forge signature again, checks its configured installation scope, persists existing
refresh hints where required, and deduplicates delivery IDs. A consumer acknowledges only the
engine's successful response. A crash between response and queue acknowledgment causes harmless
redelivery, not another landing. Unknown versions, route/body mismatches, or permanently invalid
signatures go to quarantine with an alert.

Delivery uses the same Tunnel and short-lived, cell-verified assertion scheme described below,
with a delivery-only audience/role distinct from status. The transport credential selects a
provisioned cell; it cannot select arbitrary URLs or widen an installation allowlist. An infrastructure-controlled enrollment binds that route to the cell
identity. Product account ownership alone cannot retarget it. Use bounded retries and
per-installation fairness so a broken installation cannot exhaust every consumer. Spool expiry must
exceed the queue retry/dead-letter investigation window; delete acknowledged payloads promptly and
apply a separately approved privacy retention limit.

**Event delivery is queued push, not engine polling of a hosted event log.** The engine still polls
the forge as ADR 0006 requires. Payloads wake or request refresh; fresh reads authorize decisions.
Preserve current listener dedupe, process-local health reset, and repository-specific webhook
health. Edge receipt is not engine delivery health. Delayed replay must not manufacture fresh trust
or current protection evidence. Test backlog replay against scheduling defaults. Lost/expired
deliveries degrade latency and polling load, not merge safety. Self-hosted installations can keep
direct webhooks or polling only.

### Integrate through the existing read-only status contract

The Worker backend calls the engine's private `internal/status` HTTP API over TLS through
[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/),
whose outbound connector avoids a public inbound cell port. Keep delivery and status routes and
credentials separate. Use short-lived signed assertions bound to the cell audience, installation,
service role, request and expiry, verified by the cell; do not store reusable per-cell bearer tokens
in Workers. A reviewed Go authentication adapter and replay/wrong-audience tests are prerequisites
for hosted alpha; today's status bearer API does not already implement this protocol. Self-hosted
bearer authentication remains supported. Expose `/v1/queues`, repository/PR/change detail and briefs,
`/v1/discovery`, and `/v1/health` through an authorization-filtering product backend. Never send
service credentials to a browser or give a Worker a database connection as a shortcut. The dashboard
Worker is a cross-tenant confidentiality boundary: a valid service assertion does not replace its
per-request user/repository authorization, and compromise can expose the tenants it may read.

Preserve versioning, path escaping, ambiguity errors, bounds, sanitized errors, and `Cache-Control:
no-store` from the [API contract](../api.md). Product pages must label an unavailable engine as
unavailable, and any retained display as stale with its observation time. A cached view never
becomes policy evidence. Use `/v1/live` only for process restarts and `/v1/health` for
readiness/alerts.

Do not routinely proxy `/v1/authority-completion` to ordinary dashboard polling. It performs
outbound discovery, exposes repository-wide state, and has an operator serialization/budget
requirement. Keep it on an explicitly authorized operator path and preserve its current
whole-repository result.

There are **no `/v1` mutation endpoints today**. The alpha dashboard links to forge-native
queue/review commands and shows their recorded results. It must not invent `POST /v1/merge`, insert
events, or transform a product API token into an engine operator identity. Any future user-write API
needs a separate reviewed authentication/authorization contract satisfying ADR 0007 Stage B,
including forge verification on every write and no stored user tokens. This ADR does not enable
direct submission or complete that deferred decision. Customer self-service acknowledgement and
block recovery likewise depend on future forge-authorized write APIs (ADR 0007 Stage B or a
separately reviewed equivalent); until then use the owner-consented support procedure below.

### Keep coordination outside the landing state machine

An installation DO serializes onboarding attempts, prevents duplicate product provisioning requests,
coalesces refresh work, and meters product API/AI quotas. D1 retains the desired installation
binding and provisioning request IDs; DO state tracks execution coordination and short-lived
counters. After a DO reset, reconcile actual resources with those IDs before creating anything.

DO rate budgets cover hosted dashboard and assistance traffic. They cannot replace the engine's
installation budget or delay settlement. Engine traffic keeps ADR 0006's priority and
last-10-percent settlement reserve, plus the operating guide's discovery/rules shares. Optional
product forge reads get an explicit subordinate allocation or stop when capacity is unknown. A DO
cannot declare a previously observed CI success current or reserve landing authority.

DO SQLite must never store authoritative queue position, `LandingStarted`, fence outcome, approvals,
policy pins, dependency satisfaction, provenance, or the journal watermark used for activation.
Deleting every DO should affect product availability and quotas, with engine history still
recoverable from Postgres and remote refs. It must never require reconstructing a queue from DOs.

Defer [Workflows](https://developers.cloudflare.com/workflows/) until a proven need for durable
onboarding retries, export-publication tracking, or account lifecycle operations. Its persisted
steps and retries are useful there. Provisioning may request resources from a separately authorized
deployer using idempotent operation IDs. It cannot install arbitrary engine code or read keys.
Provision cell configuration only from reviewed, versioned templates. Customer-supplied values may
only narrow allowed depth, authority mode and installation/repository allowlists; they cannot raise
a depth cap, enable a broader authority mode, or widen scope beyond the reviewed template and
forge-approved policy. A Workflow completion records product progress, not authority activation.

Never put build/test/land progression, CI retry decisions, fence settlement, or policy approval in a
Workflow. Do not create one Workflow per landing. The Go journal/planner/executor remains the only
landing state machine. An export Workflow may request and track a read-only export job; the Go
exporter selects sources and computes every manifest and digest.

### Hold the hosted App key outside public product services

The hosted GitHub App private key belongs in a regional **sign-only HSM/KMS key vault**, used by a
Go credential broker under the engine infrastructure security boundary. This follows GitHub's
[sign-only key-vault guidance](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/managing-private-keys-for-github-apps#storing-private-keys).
The broker creates App JWTs by remote signing and never holds exportable private-key material.
Qualify GitHub-key import, JWT algorithm support and audited sign-only access; restrict initial key
import to custodians and destroy staging copies. Only the broker workload identity may request
signatures. Never put the key in broker memory, D1, KV, DO storage, R2, a dashboard, a build artifact,
or a Worker.

The broker authenticates each engine with a short-lived workload identity and maps it to one
configured App/installation through infrastructure enrollment. It mints only that installation's
short-lived tokens, narrowed to required repositories and permissions. It does not accept
caller-supplied arbitrary installation IDs, issue App JWTs to Workers, or expose a generic signing
API. Provide narrow App identity/discovery operations needed by the current driver; filter discovery
to the enrolled installation. The engine still independently checks scope, grants, and every
decision through `internal/forge`.

GitHub documents [installation-token repository and permission narrowing](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app).
The broker must enforce those bounds rather than trusting the requesting cell. Tokens
[expire after one hour](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app).
During broker/signing outages, cells may use already issued tokens only until their actual expiry
or known revocation, with unchanged scope and fresh forge checks. New work additionally requires a
valid availability renewal; cached credentials never extend that gate. Settlement may use an
unexpired cached token even after the gate closes, then waits for access if none remains.

This requires a reviewed Go credential-provider adapter before hosted alpha. Today the engine reads
a local App private key; a remote broker is proposed, not existing functionality. Keep the local-key
provider as the self-hosted default, with no dependency on the broker or Cloudflare. Do not
distribute a shared public App's full private key to every hosted cell as an alpha shortcut.
Separate customer-owned Apps can use separate broker key partitions.

The broker is a high-trust service: compromise of the shared App key can affect all its
installations. Scoped tokens reduce cell-to-cell exposure but do not remove this root risk. Name a
primary/backup custodian, compromise contact, rotation procedure, and emergency revocation owner
before alpha. Follow ADR 0008's recommended 90-day rotation and immediate rotation after exposure.
For routine rotation, overlap then delete: import a new key, validate remote signing/token issuance,
switch all broker instances, then delete the old key without downtime. GitHub supports
[multiple active keys for rotation](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/managing-private-keys-for-github-apps#about-private-keys-for-github-apps).
Compromise requires prompt revocation rather than waiting for a routine overlap window.
Keep setup/admin credentials and the audit signing identity separate. Ordinary control-plane
deployments have no broker-secret or engine-deployment permission.

Use [Worker secret bindings](https://developers.cloudflare.com/workers/configuration/secrets/) for
webhook, BetterAuth, and downstream service credentials, with separate staging and production
scopes. Runtime code with a binding can use its value; secret storage does not make a compromised
Worker safe. Give delivery Workers no status/export credentials and give dashboard Workers no
webhook or broker credentials. Restrict network paths and rotate credentials after compromise.

### Separate product login from forge authority

Use BetterAuth for accounts and sessions, its [organization
plugin](https://better-auth.com/docs/plugins) for product membership, [SSO
plugin](https://better-auth.com/docs/plugins/sso) for enterprise login, and [API Key
plugin](https://better-auth.com/docs/plugins/api-key) for product API tokens. Those are product
identities and permissions. SSO can establish access to an organization's dashboard; it cannot
establish repository write permission. Qualify the selected SSO methods and any commercial service
terms before sale.

Bind a product organization to a GitHub App installation only after server-side forge confirmation
of installation ownership/control, with a single-use, expiring onboarding challenge. An installation
ID supplied by the browser, matching email/domain, org slug, invitation, or SSO claim is
insufficient. Store stable forge host, App, installation and user IDs, preserving display names
separately. Require explicit authenticated account linking, and do not merge forge identities
through email equality. Other forges use their driver.

Dashboard authorization checks the current product session/token scope, org membership and
installation binding on every request, using authoritative reads for revocation. Private repository
visibility is the intersection with freshly verified forge access for a linked user; unknown access
denies the read. Alpha product tokens are user-bound and scoped read-only. Org-owned service tokens
and SSO-only private-repository viewers stay disabled until a separate explicit data-access policy
is approved. Never equate org membership with visibility of every private repository on an
installation.

A hosted Go authorization adapter performs those forge reads through `internal/forge`; it is
a prerequisite for private views, not an existing user-auth endpoint in `/v1`.

Acquire any user proof transiently for linking/access checks and discard it; retain no forge user
access or refresh token in BetterAuth account records. Using GitHub social login requires
configuring and testing that storage rule, not accepting a framework default. If a private-access
check needs unavailable user proof or cannot be resolved through the installation driver, deny
access and request fresh linking/proof. Do not create an implicit stored-token design.

Writer permissions, stable-ID grant checks, team membership, exact-head trust, review/receipt
exclusions, and policy-owner requirements remain Go forge checks. BetterAuth roles and API tokens
must **never grant merge authority**. Only the engine can evaluate an owner-approved policy from the
governing forge commit. Product admins cannot approve a policy change, acknowledge an outside push,
clear an engine block, or activate authority through a product-role assignment. Retain the existing
separate operator CLI/database paths and audited procedures.

In hosted mode, OpenMerge staff execute those procedures only on a forge-verified repository owner's
or administrator's explicit request. Require an owner action through the forge, or a signed,
single-use expiring request bound to a forge identity whose owner/admin role is freshly verified.
Bind consent to the cell, repository, operation and exact evidence (for outside pushes, queue/event
ID and SHA); product membership, email or a support ticket alone is insufficient. Record the stable
requester identity, role proof, consent, staff executor identity, time, command and resulting receipt
in retained audit evidence linked to the cell journal. Use narrowly scoped, time-limited operator DB
access. Staff follow the [outside-push acknowledgement](../operating.md#acknowledging-outside-pushes)
and applicable block-recovery runbooks, including their backup, drain and verification prerequisites.
Acknowledgement records observation, not approval, and clears no rules/suspect block. Recovery may
clear only the specific source after its documented proof succeeds; neither consent nor staff
judgement permits policy relaxation, event edits or bypass of normal engine gates. Missing proof
leaves the block in place. This interim support process must be staffed and audited before hosted
alpha; it does not add a dashboard mutation path.

### Use one AI gateway for useful, optional assistance

Choose **Cloudflare AI Gateway** for hosted AI calls. It fits the selected control plane and
supplies a common place for provider routing, rate limits, and usage accounting. Its [documented
REST interface](https://developers.cloudflare.com/ai-gateway/usage/rest-api/) supports multiple
model providers. Do not also deploy Vercel AI Gateway. There is no requirement here that justifies a
second gateway, credential set, or billing path. Models remain replaceable behind the chosen
gateway.

Start with opt-in CI failure summaries grounded in links to exact failed runs and bounded redacted
log excerpts. Add flaky-test triage that suggests likely rerun patterns from recorded history,
without deciding whether a failure is confirmed. Reviewer agents may produce advisory comments or
ordinary CI checks, with explicit run/SHA/producer provenance. They get no App landing key and
cannot submit human approval receipts or waive required reviewers.

AI output is labelled generated, with source links, model/prompt version and input digest. It never
supplies policy facts, changes queue ordering, requests unbounded retries, clears a block, or
becomes evidence that an unobserved test passed. Required agent-review checks, if an owner
configures them, are ordinary forge checks subject to the same exact-SHA/provenance rules as other
CI. Generated brief prose remains subject to ADR 0007 Stage C prerequisites; this hosting choice
does not activate that stage or alter ADR 0010 provenance.

Models receive untrusted diff/log text, not instructions with credentials or write tools. Bound
inputs, execution time, output size, retries, and per-org spend. Disable private-content caching and
prompt/response logging by default; [AI Gateway
logging](https://developers.cloudflare.com/ai-gateway/observability/logging/) is enabled by default
and its controls do not control Logpush exports. Verify all logging/export destinations and
model-provider retention before opt-in. Store only approved metadata and explicitly retained
generated artifacts. An AI outage removes assistance; it never blocks settlement or weakens policy.

### Retain audit evidence and publish independent anchors

Keep all original journal evidence under [ADR 0008 10b
retention](../operating.md#retention-decision-and-audit-closure), including transitive ADR 0010/0011
dependencies. Do not apply webhook-spool or AI-log lifecycle rules to audit buckets or source
history. Derived projection compaction never authorizes deleting original events or replacing them
with R2.

A separate regional read-only audit job runs the Go exporter with a SELECT-only role, no migrations,
and no forge/service key. Upload its five-file bundle, including selection and manifest, plus
exact command outputs, binary/config identity and custody metadata to a private R2 audit bucket
under a unique content-addressed prefix. Preserve v1/v2 bytes and required dependency closure. Only publish a
completed, replay-verified bundle; partial multipart uploads are not successful exports.

Follow [external anchoring](../operating.md#external-anchors-and-overlapping-windows): repeat
export, replay against the intended database/isolated restore, compare all five files, and retain
the command's canonical manifest digest. It is not `sha256sum manifest.json`. A custodian signs that
receipt with an independently controlled signing identity and retains signer verification/rotation
history. Keep the receipt, signature, bounds, source watermarks, and storage identity in a
separately administered custody account and an owner-controlled external anchor channel. The
delivery/dashboard service cannot sign or replace anchors.

[R2 bucket locks](https://developers.cloudflare.com/r2/buckets/bucket-locks/) help prevent
overwrite/deletion during the chosen retention period, but their rules can be removed by an
authorized administrator. Use separate custody and access policies; do not call the bucket
tamper-proof or independently anchored merely because it is encrypted. Detect rewritten/rehashed
exports against the prior externally retained signed manifest. No anchor proves completeness before
its commitment or protection between forge observations.

Adopt daily closed-window exports and weekly overlapping/full-coverage checks for hosted alpha,
after choosing a defensible maximum commit lag L. If no bound is established by bounded writers or a
drain barrier, mark the window provisional. Record cadence, retention, custody and recovery owners
before activation. A missed anchor pages the audit owner and preserves the previous commitment; it
never causes history to be regenerated as if nothing failed. The existing personal daily-anchor plan
does not establish independent custody and cannot be presented as the hosted compliance posture.

### Preserve self-hosted parity without Cloudflare dependencies

Self-hosted users retain `openmerge serve`, systemd/containers, Postgres, process-exclusive Git
caches, direct webhooks or forge polling, CLI/status, and the same authority, provenance,
dependency, and export capabilities as they become available. Hosted product flags cannot change
engine policy meaning. The Go engine must not import Cloudflare SDKs or require a hosted account,
license-server call, remote token broker, DO, or AI gateway to land.

Ship the dashboard source and static bundle for self-hosted use, reading the same `/v1` API through
a local authenticated backend. A Bun companion can add BetterAuth using a separately scoped
supported SQL store; it must not add tables to the receipt-bound engine schema. Cloudflare account
management, billing, managed backups, SSO operations and hosted support are service features, not
engine prerequisites. Document packaging and any paid support terms before release without making
self-hosted safety features hosted-only.

AI assistance is optional and off by default. Self-hosted operators may bring one gateway/provider
endpoint, credentials, data policy and spend limits, or omit the assistance component entirely.
Hosted Cloudflare AI Gateway selection does not mandate that vendor in the engine or self-hosted
dashboard. An export can stay local or use operator-selected object storage and anchor custody.

### Fail safely and state the residency limits

Hosted engines enforce an availability gate separate from merge authorization. At startup it is
closed. The cell polls an authenticated control-plane liveness endpoint every 15 seconds for a
nonce-bound renewal, valid for at most 60 seconds measured from the local monotonic request start.
It verifies the enrolled cell/installation and deployment generation. Replayed responses cannot extend the deadline.
This management poll is not the event-delivery path or an alternative queue journal.

Lost contact uses the bounded renewal grace; an authenticated explicit infrastructure suspension
closes the gate immediately on receipt. On expiry or suspension, stop fresh admissions, builds, and
landing starts. Recheck the gate at durable start and immediately before a new landing push. Do not cancel held
requests or pretend queue lifecycle changed. A healthy renewal removes only this availability
restriction; every normal engine gate still applies. This adapter is future **Hold** work with
race/restart tests, not a new `/v1` write endpoint or a DO landing decision.

A total control-plane outage therefore stops new hosted work after bounded detection. It cannot
trigger a landing or corrupt evidence. An attempt already started may have pushed before expiry and
must still settle through the fence; an in-flight push cannot be recalled. Settlement, journal
recovery and its credential budget remain available independently of the control-plane gate. Never
declare abandonment because a Worker, token service or network is down. An unavailable credential
broker can leave settlement waiting for access. This accepts a hosted availability dependency while
self-hosted defaults remain unchanged. It does not promise an instantaneous stop at the first lost
packet.

A webhook/Queue-only outage permits ordinary engine polling while the liveness path is healthy. An
AI, billing-notification or dashboard-asset outage does not close that path. Product auth/catalog
failure denies product reads/onboarding; the liveness handler needs a current enrolled binding and
must refuse renewal if it cannot verify one. R2 audit failure alerts and retries export, preserving
source evidence. Engine DB failure stops mutation; `/v1/live` remains a process probe. No layer
falls back to cached policy or identity evidence to authorize work; unexpired cached broker tokens
are only transport credentials under the rules above.

Start hosted alpha in one explicitly selected engine/DB region. Keep Git caches, broker, exporter
and backups within the declared regional storage policy, with encrypted backups and TLS on every
cross-host path. Region changes are restore and identity operations, not edits to an account
preference.

Cloudflare's global execution is distinct from a DB region. [DO location
hints](https://developers.cloudflare.com/durable-objects/reference/data-location/) are best effort;
jurisdictions constrain the object, not every calling Worker. [D1
jurisdictions](https://developers.cloudflare.com/d1/configuration/data-location/) and [R2
jurisdictions](https://developers.cloudflare.com/r2/reference/data-location/) constrain their
respective storage, while location hints alone do not provide the same guarantee. [Queues
jurisdictions](https://developers.cloudflare.com/queues/configuration/data-location/) constrain queue
storage/processing but not all producing/consuming Workers. Worker request
processing, logs, support access, IdPs and AI providers need separate review. No North-America-only
or EU-only promise follows from placing Postgres there. Do not sell a residency tier until its
entire data path and vendor terms are qualified. Minimize payload retention and exclude secrets, raw
diffs and personal data from routine telemetry.

### Operate and upgrade each layer independently

Deploy the engine cells and credential broker as pinned Go images on regional Linux VMs/containers
supporting the real Git binary and `prlimit`. Start alpha with one active cell per installation,
bounded CPU/memory/disk, and a unique cache directory. Use a regional container/VM host, including
Fly as a candidate, without moving the public control plane off Cloudflare. Qualify the host before
selection. Automatic multi-region engine failover is outside this ADR.

Deploy Worker bundles separately, with pinned compatibility dates, Bun-built assets, reviewed D1/DO
migrations, and staged traffic. Control-plane rollback must preserve product-schema compatibility
and active Workflow versions. It cannot roll back engine evidence or change the engine's desired
image by itself. Use a separate infrastructure deployment identity for cells, broker and routes.

Collect engine JSON logs and infrastructure/Worker metrics with correlated opaque installation,
delivery, batch and attempt IDs. Monitor engine progress, block/settlement age, database
reachability, quota reserve, disk, webhook backlog, DLQ age, auth failures, availability-renewal
age, audit lag and AI spend. Keep telemetry access tenant-scoped; logs are not the journal. Adopt
ADR 0008's health/protection-verification alerts and named on-call/backup ownership. Only process
liveness restarts a cell; a queue or DB problem raises an alert.

Upgrade in cohorts after sandbox validation, canarying the owner's own cells first and then one
installation at a time within each cohort. Abort fleet progression on the first failed eligibility
check, migration, replay or health check; recover that cell before approving another cohort. Notify
tenants in advance of version/boundary changes, expected interruption and recovery contact, and use
their agreed maintenance windows. Freeze intake and settle what can settle. An automated, cell-local
precondition check must prove a drained queue, no active batches/CI, no unsettled attempts, no held
requests and no `pending_conditions` in current projections/snapshots before eligibility; inspect
historical evidence separately for reader compatibility. Stop every writer and incompatible reader,
prevent automatic restarts, and inventory old binaries on all hosts. Retain the prior binary/config
and a verified whole-database pre-emission backup. Use the existing activation receipts and
repository-wide completion; onboarding progress, a DO lock, or a successful deploy is not activation
evidence.

Keep Worker↔cell API contracts compatible across N/N-1 in both directions, including Worker rollback
while cells remain on N; qualify the full adjacent-version matrix before rollout. Unknown or
incompatible versions deny affected requests, never reinterpret fields. This HTTP compatibility policy does not authorize an old Go
reader against new journal evidence. For boundary-crossing releases, expand compatible readers and
API/schema support first, then activate new writers, and contract only after the fleet and rollback
window permit it. Where expansion is impossible, require a coordinated maintenance cutover and the
existing restore boundary, rather than claiming a rolling downgrade.

The [#163 held-request boundary](../operating.md#queue-requests-with-pending-entry-conditions) can
be crossed by the first persisted `pending_conditions` observation or held-request snapshot without
any queue command. Inspect fields and snapshots, not only new event names. Authority `read_error`,
v7 cursors, side events, compact v9 cursors, and future writer slices have their own reader/restore
boundaries. Follow the [writer-slice recovery
limits](../operating.md#cursor-prefix-invariant-and-rollback) and [invalid-record
procedure](../operating.md#invalid-records-and-unresolved-references). The authority downgrade
checker is necessary where applicable, but does not certify compatibility with every other feature.

After a boundary, disabling a feature or reverting a Worker cannot make an old Go binary safe. Keep
a compatible reader, or stop workers, retain post-backup evidence, restore the tested pre-emission
database, rotate the ADR 0011 journal incarnation/token as required, and reconcile actual base/fence
refs and all subsequent effects before resuming. Regenerate identity/receipts after a logical
restore. Never delete events, relax decoding, reset fences, or blindly run a stale restored queue. A
wrong or invalid side event can fail status for the whole requested key set; read-only aggregation
must preserve that failure.

Use encrypted nightly logical backups plus provider PITR where qualified, pre-change backups, and
separately retained signed anchors. Routine HA failover must lose zero acknowledged commits as
qualified above. For disaster recovery only, initial engineering targets are RPO at most 15 minutes
with PITR and RTO at most four hours for one cell; these are unproven targets, not a customer SLA.
Any nonzero-RPO recovery, including PITR, must stop writers, retain surviving evidence, follow the
[documented restore-then-reconcile-refs procedure](../operating.md#cursor-prefix-invariant-and-rollback)
and the steps above, and reconcile actual base/fence refs and subsequent effects before resuming.
It is a declared recovery event, never a silent failover or automatic restart of a stale queue.
Exercise isolated restore/replay monthly and recovery against retained anchors quarterly. Test
identity across provider failover and restores. Logical databases may share a cluster, so prove
per-database restore and account for cluster-wide outages. Back up D1 product records and regional
enrollment separately. Losing the product catalog must deny rebinding, never enroll the wrong cell
by inference.

### Budget for isolated engines before optimizing edge costs

Use this planning workload, not a vendor price quote: 100 installations, 10 repositories each, 20
changes per installation per day, and 20 webhook deliveries per change. Over 30 days that is 60,000
changes and 1.2 million deliveries. At a 25 KB average raw body, the delivery spool receives about
30 GB/month before retries. At a one-day average residence it holds about 1 GB; choose capacity for
the full configured retry window, not that average alone.

At 100 KB of retained journal/audit evidence per change, growth is about 6 GB/month before indexes,
replicas, repeated exports, and backups. This is an assumption to replace with measured source
bytes, not a pruning allowance. A 10 GB Git cache per installation adds 1 TB regional disk. A
continuously provisioned 1-vCPU/1-GiB cell per installation reserves 100 vCPUs/100 GiB, although
actual Git/replay peaks may require larger cells. This is the dedicated-warm baseline, not a
minimum reservation for every installation. Measure before sizing; density options below share
capacity while preserving cell boundaries.

Monthly cost is the sum of cell compute/cache, Postgres databases and backup storage,
broker/observability, Worker requests/CPU, Queue operations/retries, D1 reads/writes/storage, DO
requests/duration/storage, R2 storage/operations, network transfer, and model tokens. Keep idle
cells warm in the first qualification cohort for polling and settlement. A 15-second renewal adds
about 17.28 million requests/month for 100 continuously running cells, before dashboard and delivery
calls; budget that deliberate availability cost rather than hiding it in webhook volume.

For AI, assume 10% of changes request one 8,000-input/1,000-output-token summary: 48 million input
and 6 million output tokens/month. Charge usage at the selected provider's actual rates, including
retries, under per-org caps. [AI Gateway
pricing](https://developers.cloudflare.com/ai-gateway/reference/pricing/) distinguishes gateway
features, logs, inference, and optional billing fees; free core features do not mean free models or
free telemetry.

Scale horizontally by adding installation cells/databases and fair delivery partitions. Scale a busy
cell vertically before proposing engine sharding. DOs coordinate product requests, not extra landing
writers. Capacity limits include Git disk/CPU, DB connections and I/O, forge quotas,
source-retention growth, and customers' CI runners. Hosting does not increase strict-mode throughput
beyond ADR 0009's tested limits. Gather a paid-tier quote and a 30-day alpha bill before pricing or
promising an SLA. An internal allowance of $50-150 per installation-month for cell/DB/storage alone
implies $5,000-15,000 for 100 installations, excluding support, AI and shared services. That is a
sensitivity range for the dedicated-warm baseline, not a claim about PlanetScale or any host's rates.

#### Cell density and a small-installation path

ADR 0008 guards apply per logical database and per process, not per physical machine. Pack many
small cells on one host as separate processes or containers, each with its own OS user, DB role and
credentials, workload identity and cache directory. Deny cross-cell filesystem/credential access,
bound CPU/memory/disk and DB connections, and qualify noisy-neighbor behavior before choosing a
density. Share one Postgres cluster across separate logical databases with separate access and
restore units; never combine journals, schemas or installations in one logical DB/process. DB
identity and credentials remain per cell in every tier, with identical activation, locks, fences,
policy, settlement and audit guarantees. Shared hosts/clusters add correlated outage and privileged
operator risks; dedicated physical isolation can remain a separate option.

An optional hibernated tier may stop an idle cell and wake it on a verified webhook hint, with an
independent slow poll (proposed maximum ten minutes) to wake and reconcile even when hints are lost.
A restricted regional supervisor schedules starts, not engine decisions. A cell-local serialized
sleep check closes intake and must prove no queued entries, active batches/CI, unsettled attempts,
held requests or pending conditions before stopping; unknown state prohibits sleep. Resume from the same DB identity, restore
all process-local gates fail-closed, reset webhook health, obtain a fresh availability renewal and
forge/policy observations, and recover/settle before admitting work. Idle suspension never abandons
an attempt, recreates a DB or bypasses a receipt. Qualify sleep/wake races and polling latency before
offering this tier; the current engine does not implement it.

Per-installation monthly infrastructure cost floors depend on measured density. For an explicit
planning example only, let `n = 20` small installations share a host costing `H = $100/month` and a
qualified zero-loss HA Postgres cluster costing `P = $200/month`; allow `S = $2/cell-month` for
cache, evidence and backups. For hibernation, assume `W = $20/month` for a shared wake/slow-poll
supervisor and separately meter active compute `A`. These are hypothetical cost inputs, not vendor
quotes, proven capacity, customer prices or an SLA; retained evidence growth raises `S`.

| Candidate tier | Planning cost floor per installation-month, before shared product services/support/AI |
|---|---|
| Dedicated warm | Existing $50-150 cell/DB/storage allowance; replace with a qualified HA quote |
| Shared warm | `(H + P) / n + S`, or $17 at the illustrative density |
| Hibernated, optional | `(P + W) / n + S + A`, or $13 idle floor plus active compute; provisioned warm capacity still costs money if the host cannot scale down |

The HA DB, retained evidence and wake service never become free when compute sleeps. Quote each
pool and its minimum capacity, measure idle memory/Git peaks and simultaneous wakes, then replace
these inputs with the 30-day bill. Owner choices on tiers, density, latency and pricing remain open.

### Roll out after period B, with explicit activation gates

Riel St. Amand owns acceptance of the recorded period-B drain: no queued entries, active
batches/chain, or queued/running queue-ref CI, including discarded runs. Follow the [dogfood
plan](../research/phase2-dogfood.md#plan). No implementation starts before that gate, even if a
later slice is classified Inert. Do not change live App grants, CI, default depth, or authority
during the trial.

Inert means no changed live queue behavior, credentials, policy or writers. Hold means no
deployment/activation until prerequisites and owner approval are recorded. These classes do not
waive evidence or earlier ADR release gates. Implement separate small PRs, splitting a slice further
where review requires.

| Slice | Scope and exit evidence | Class |
|---|---|---|
| 0, now | This ADR and index only; no deployment. Resolve runtime exception and named owners before implementation. | Inert |
| 1, after B | Contract/threat-model fixtures, Bun/workerd and BetterAuth/D1 qualification, regional host/Postgres identity/locks/restore qualification, budget. No production writers or resources. | Inert |
| 2 | Read-only dashboard and product auth in isolated staging, stable-ID onboarding proof and negative tenant-access/revocation tests. No engine mutations or production binding. | Inert |
| 3 | Go broker adapter, isolated engine cells, queued webhook transport, private read-only API integration, availability gate and failure/replay tests. Prove retries and outages preserve fences, held requests and evidence. | Hold |
| 4 | Audit R2 publication and independently signed 10b anchors. Prove five-file replay, tamper/omission detection, overlapping ranges and isolated restore with actual custody roles. | Hold |
| 5 | Hosted alpha on the owner's repositories after 3/4, reviewed per-installation enrollment, documented backup/restore and support owners. Require observed isolation, outage drills, upgrade/downgrade evidence and a 30-day bill. | Hold |
| 6 | Optional AI summaries/triage, then advisory reviewer agents, each with privacy/spend opt-in and exact-run provenance. No human-review substitution. | Hold |
| 7 | External paid beta after alpha acceptance, security/residency review, tested recovery targets, self-hosted dashboard packaging, and approved pricing/support terms. | Hold |

Include **Smart-Data-Ohio/smartfire** in the owner alpha. It already runs a separate engine
instance; preserve its database, installation scope, cache, credentials and history. Start by
attaching a read-only view. A later hosted migration is an explicit drained cutover with tested
backup and remote-ref reconciliation, not registration in a shared database. Do not reuse the
organization acceptance sandbox as if it were smartfire's production identity.

Exercise wrong-tenant routing and tokens, revoked access, spoofed signatures,
duplicated/reordered/expired deliveries, DO reset, D1 restore, Worker outage, DB/broker failure,
lease replay/expiry during landing, and R2 custody failure. Require zero new starts after the
availability gate closes, truthful settlement of started attempts, and no policy relaxation under
any failure. Re-run full replay and snapshot-plus-tail checks for activated evidence writers.
Existing ADR 0008 organization authority acceptance remains a separate owner-executed gate; hosted
alpha does not authorize that switch or ADR 0010/0011 activation.

## Consequences

- The hosted product gains managed onboarding and visibility while the Go
  engine, forge-neutral interfaces, and self-hosted deployment remain useful
  independently. The API boundary makes a second queue implementation unnecessary.
- Logical cells/databases preserve the existing receipt, lock, restore and export
  unit. Shared-host density can lower the small-tenant floor; hibernation needs
  lifecycle automation and trades latency for compute cost without relaxing gates.
- A broker reduces private-key distribution but becomes critical trusted
  infrastructure. Cloudflare, DB, infrastructure and signing administrators
  remain explicit trust assumptions; compromised root operators cannot be
  made harmless by a dashboard role model.
- Hosted availability now includes the control-plane renewal path. A deliberate
  pause after failure is preferable to inventing a second landing authority.
  Settlement may remain pending until forge credentials return.
- We owe integration contracts, runtime exception approval, managed-Postgres
  qualification, security/custody ownership, recovery drills and measured cost
  before hosting customers. This Proposed ADR certifies none of those as done.

## Alternatives considered

- **Rewrite the engine in TypeScript on Workers.** Rejected by the owner. It
  would replace real-Git/process limits and Postgres/fence/audit contracts with
  new implementations, while adding runtime constraints without product benefit.
- **Run everything on Fly or conventional containers.** Technically coherent
  and simpler for Bun plus Go, with fewer distributed service boundaries. It
  loses the owner's chosen Cloudflare control plane. Keep containers for the
  engine rather than moving its responsibilities to match the edge platform.
- **Use D1 or DO SQLite as the engine store.** Their control-plane convenience
  does not preserve existing Postgres advisory locks, transactions, catalog
  identity, whole-database activation boundary or restore receipts. Rejected.
- **Share an engine database with tenant columns or schemas.** Cheaper, but
  conflicts with the explicit dedicated-database guard. Deferred to a future
  tenancy ADR, not hidden behind product organization IDs or routing filters.
- **Put the App key in Workers or every engine cell.** Simpler token minting,
  but broadens private-key access and couples public product code to forge
  bypass authority. Choose a scoped broker despite its operational cost.
- **Poll a control-plane queue from the engine.** Outbound-only connectivity is
  attractive, but adds a hosted event-consumer protocol to the engine. Queued
  push reuses the verified webhook listener and preserves forge polling as the
  independent backstop. Availability renewal is a separate management concern.
- **Use Workflows or DOs to run the merge state machine.** Competing retries and
  outcomes would require translating the Go journal/fence contract. Rejected.
- **Use Vercel AI Gateway, or both gateways.** No required capability here
  outweighs the extra vendor boundary. Choose Cloudflare for hosted AI and
  permit an operator-selected gateway only in the separate self-hosted context.
- **Adopt scriptc for all TypeScript.** Publicly confirmed, but experimental
  subset support is not compatibility evidence for this product. Choose Bun;
  revisit a bounded native utility only after an actual qualification.
- **Continue hosted new landings through every control-plane outage.** The Go
  engine could keep polling safely, but the product would have no dependable
  stop during lost control-plane coordination. Choose a bounded availability
  gate while preserving independent settlement and self-hosted behavior.

### Open questions for the owner

1. Ratify the Bun requirement's explicit browser/workerd exceptions, or choose
   a Bun origin and revise the Worker architecture before implementation?
2. Which regional engine host and managed Postgres tier should be qualified
   first, and which alpha region/data-handling policy may customers rely on?
   PlanetScale Postgres is a candidate, not an assertion of identity privileges.
3. Who are the primary/backup App-key, protection, audit-signing, recovery and
   on-call owners? Which independently controlled anchor channel and retention
   period replace the personal trial's shared custody?
4. Accept the 15-second renewal/60-second stop-detection design and its cost?
   Which tested recovery objectives and outage obligations become paid promises?
5. Should external beta use the shared hosted public App or offer customer-owned
   Apps/key partitions first? What level of physical DB/compute isolation is sold?
6. Which SSO methods and product roles ship first? Keep org-owned API tokens and
   SSO-only private-repository viewing disabled, or approve a separate explicit
   data-access policy? Forge merge authority remains outside that choice.
7. Which AI providers, private-data consent, spend caps and log retention are
   acceptable? Are CI summaries sufficient for alpha, with reviewer agents later?
8. What self-hosted packaging, support/licensing terms, and measured per-installation
   budget should gate beta? This ADR does not introduce a licensing restriction.
9. Which dedicated-warm, shared-warm and optional hibernated tiers should ship, at
   what qualified density, idle/wake latency and measured cost floor? What pricing
   and physical-isolation options should beta offer after quotes and the alpha bill?
10. Accept the interim forge-verified owner-consent/staff-executed recovery process,
    with which support owners and response obligations? When should reviewed write
    APIs enable customer self-service acknowledgement and block recovery?
11. What fleet upgrade cadence, canary/cohort sizes, tenant notice period and
    maintenance-window commitments should apply, including boundary cutovers?

Vendor facts above were checked against public primary sources on 2026-10-27. Recheck limits,
compatibility and prices during qualification. Proposed resource sizes, recovery targets, costs and
schedules are engineering assumptions until the corresponding rollout evidence exists.
