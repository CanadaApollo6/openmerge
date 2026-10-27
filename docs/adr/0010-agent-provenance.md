# 0010. Agent provenance in queue policy and audit

- **Status:** Proposed
- **Date:** 2026-10-27

## Context

OpenMerge can classify a submitting account as an agent, but that does not
describe the commits it submits. A human can submit agent-assisted commits, and
a bot can submit a person's work. Owners need a way to require more evidence
for declared agent work and retain the declaration behind a landing decision.

Entire's Checkpoints CLI is useful prior art. It links commits to session
metadata stored in Git. OpenMerge can read that link without adopting Entire
as its forge, running its CLI, or collecting transcripts. The same mechanism
must work for a change with no pull request under
[ADR 0007](0007-changes-not-prs.md), on GitHub, and through future
[ADR 0004](0004-forge-driver-interface.md) drivers, including Entire itself.

Git authors, trailers, and checkpoint refs are pusher-controlled declarations.
Neither a convincing agent name nor a reachable metadata object proves who
performed the work. Provenance can add requirements; it cannot establish trust.

### Entire contract checked for this proposal

The sources below are Entire CLI **v0.10.0**, commit
`a47cf0c70ddf75c02c37d6718d3d52b4d33425f5`. These are storage observations,
not a dependency or a promise about later releases.

- [`trailers/trailers.go`](https://github.com/entireio/cli/blob/a47cf0c70ddf75c02c37d6718d3d52b4d33425f5/cmd/entire/cli/trailers/trailers.go)
  and [checkpoint ID linking](https://github.com/entireio/cli/blob/a47cf0c70ddf75c02c37d6718d3d52b4d33425f5/docs/architecture/sessions-and-checkpoints.md#checkpoint-id-linking)
  define `Entire-Checkpoint: <id>`. Legacy IDs are 12 lowercase hex characters;
  newer IDs are 26-character ULIDs.
- [`checkpoint/refs_naming.go`](https://github.com/entireio/cli/blob/a47cf0c70ddf75c02c37d6718d3d52b4d33425f5/cmd/entire/cli/checkpoint/refs_naming.go)
  and [`checkpoint/id/id.go`](https://github.com/entireio/cli/blob/a47cf0c70ddf75c02c37d6718d3d52b4d33425f5/cmd/entire/cli/checkpoint/id/id.go)
  place checkpoints at `refs/entire/checkpoints/<last2>/<id>`. Refs support both
  ID formats, including migrated legacy IDs. ULIDs require canonical uppercase
  Crockford encoding and the ULID overflow check, not merely 26 characters.
- [`checkpoint/persistent.go`](https://github.com/entireio/cli/blob/a47cf0c70ddf75c02c37d6718d3d52b4d33425f5/cmd/entire/cli/checkpoint/persistent.go)
  creates the legacy orphan branch `entire/checkpoints/v1`. Its checkpoint tree
  path is `<first2>/<rest>/`, independent of ref sharding.
- [`settings/checkpoints.go`](https://github.com/entireio/cli/blob/a47cf0c70ddf75c02c37d6718d3d52b4d33425f5/cmd/entire/cli/settings/checkpoints.go)
  reads `checkpoints.primary.type` in `.entire/settings.json`, with `git-refs`
  and `git-branch` backends. The
  [first-run integration test](https://github.com/entireio/cli/blob/a47cf0c70ddf75c02c37d6718d3d52b4d33425f5/cmd/entire/cli/integration_test/enable_backend_default_test.go)
  confirms that new v0.10.0 setups write `git-refs`. An absent settings block
  retains the legacy branch default. "Default since 0.10.0" means new setup,
  not automatic migration of every existing repository.
- [The ref backend's push flow](https://github.com/entireio/cli/blob/a47cf0c70ddf75c02c37d6718d3d52b4d33425f5/docs/architecture/ref-checkpoint-backend.md#pre-push-flow)
  describes pre-push synchronization. The legacy branch also has a pre-push
  path in [`strategy/manual_commit_push.go`](https://github.com/entireio/cli/blob/a47cf0c70ddf75c02c37d6718d3d52b4d33425f5/cmd/entire/cli/strategy/manual_commit_push.go).
  [`git_operations.go`](https://github.com/entireio/cli/blob/a47cf0c70ddf75c02c37d6718d3d52b4d33425f5/cmd/entire/cli/git_operations.go)
  discovers refs with `ls-remote`; for a remote named origin, the diagnostic is
  `git ls-remote origin 'refs/entire/checkpoints/*'`.
  The same push implementation accepts
  `strategy_options.checkpoint_remote: {"provider": "github", "repo": "org/repo"}`
  in `.entire/settings.json` and pushes checkpoint branches or refs to that
  separate repository. Code and checkpoint remotes need not be the same.
- [Security and privacy](https://github.com/entireio/cli/blob/a47cf0c70ddf75c02c37d6718d3d52b4d33425f5/docs/security-and-privacy.md#what-entire-redacts-automatically)
  describes secret redaction before transcript and metadata storage.
  OpenMerge must still treat those objects as potentially sensitive.

Exact metadata JSON field mapping is **to verify in implementation** against
versioned source and fixtures. This ADR does not assume fields for model names,
tokens, decisions, or code-commit bindings. Resolution below means structural
availability, not validation of a session's account of the work.

## Decision

### Record declarations per source commit

Opt-in provenance is a Git reader plus a pure classifier, separate from forge
API drivers. A provenance source has a stable name and format version, extracts
declarations from pinned Git objects, and optionally resolves a declaration to
other Git objects. A shared acquisition layer enforces remote and size limits.
Sources return records and typed outcomes; they cannot approve, execute hooks,
invoke another program, or decide whether a change lands. Initial declaration
sources are Entire trailers, generic trailers, and configured Git identity
classes. The MVP reads only the already-acquired source
commits. It classifies declarations, adds requirements, and records evidence
without any provenance network fetching. An Entire checkpoint resolver follows
as a separate, later opt-in slice. In the MVP, checkpoint resolution is
`not_requested`. The public loader rejects resolver-dependent policy attributes
until that slice is supported and enabled. Declaration and review policy does
not depend on shipping a resolver.

Each revision records its pinned head and queue-base observation, the merge
base, and a complete inventory of source commits in `base..head`, traversing
all parent edges. This includes side-branch commits introduced by a merge.
Record the exact object IDs, not a count inferred from a forge's commit list.
Use the revision's ADR 0007 pinned base for the stable inventory. At build time,
also enumerate any commits newly included by the actual build-base difference.
The evaluated inventory is their union, so a base rewrite cannot hide a commit.
Already-landed ancestors and synthetic queue markers are not contributors.

A record contains the following conceptual fields. This is not a wire schema:

- Repository, queue, change ID, revision, original commit object ID, and source
  inventory generation, with completeness and a bounded reason for failure.
- Source name/version, recognized declaration kind and bounded value, matched
  configured identity rule IDs, and the contributing Git object IDs.
- Declared class and its reasons, independently of checkpoint resolution.
- Each checkpoint's ID, backend, repository/ref, observed ref tip, subtree and
  metadata blob object IDs, observation generation, and resolution outcome.
- A digest of the canonical record and the mapping from original commits to
  commits composed for the tested and, where different, landed result.

Records retain only normalized declarations needed for the decision. They do
not copy full commit messages, prompts, tool calls, transcripts, or metadata
blobs into the journal. Overlong invalid values retain a digest and typed
failure, never a silently truncated valid declaration. Hashes and refs are
evidence of what was read, not a
certificate of its truth. Historical decisions without these records say
`not recorded`, never `human`.

### Recognize Entire, neutral trailers, and identity classes

Parse the final Git trailer block with fixed parser semantics, independent of
the host's Git configuration. Match trailer keys case-insensitively, preserve
distinct values, and deduplicate identical declarations per original commit.
Prose containing an example trailer is not a declaration. A recognized key with
an invalid value remains a declaration with an invalid outcome; it must not
disappear during validation.

An `Entire-Checkpoint` declaration is agent evidence even if resolution fails.
When the later resolver is enabled, probe the exact checkpoint ref for either
valid ID format. A legacy hex ID can fall back to the exact legacy branch and
`<first2>/<rest>/` tree path only if that route is owner-enabled and the ref is
absent. Legacy-branch support is optional. A ULID uses refs only. Unreadable or
malformed refs do not count as absence and cannot trigger a successful fallback.
Pin the chosen tip and record the route; later movement cannot rewrite the
record. An unsupported route has an explicit `unsupported_format` outcome.

Resolution requires a commit, the expected tree or subtree, and a bounded,
regular `metadata.json` blob containing a JSON object. Do not traverse symlinks or
read transcript blobs. Internal metadata field mapping and any additional
format-version validation must be verified before the adapter ships. A
resolved checkpoint proves only that metadata with this structural shape was
available at that Git object ID. It proves no binding to the source commit.

Define a neutral trailer, **`Agent-Session: <source>:<session-id>`**, as an agent
declaration. `source` is a lowercase ASCII identifier matching
`[a-z][a-z0-9-]{0,31}`; `session-id` is 1–128 ASCII letters, digits, dots,
underscores, or hyphens. Multiple sessions are separate trailer lines.
This keeps a useful declaration available without an Entire account or CLI.
It is not a URL, fetch instruction, or universal session storage protocol.
The first implementation does not resolve neutral sessions into checkpoints.

`Co-Authored-By` contributes agent evidence only when its mailbox matches an
owner-configured agent identity. The Git author and committer can match the
same configured identity classes. Match exact mailboxes using documented ASCII
normalization, not display names, substring guesses, or forge account lookups.
Unknown mailboxes remain unclassified. A configured bot match is a declaration
class, not authentication. This narrows ADR 0007's "author kind from forge
account, never git author strings" rule for provenance classification only.
It is acceptable because this classification can only tighten policy.
Submitting-account kind, authority, approver kind, and permissions still come
from the forge identity under ADR 0007. A Git mailbox cannot change them.

Positive configured human identity matches contribute human declarations.
No declaration means `undeclared`, not human. For each commit, `agent` means only
agent evidence, `human` means only human evidence, and `mixed` means both.
`undeclared` means neither. Agent evidence takes no exception for a human author.
The generated OpenMerge committer is excluded from classification of original
work; its identity cannot make every replay agent-authored.

### Declarations only add requirements

Any pusher can omit or forge a trailer, copy somebody else's checkpoint ID, or
push convincing metadata. A checkpoint ref proves only that someone pushed
metadata. Even signed metadata does not prove that an agent produced the code.
There is no verified-human or verified-agent state in this design.

Ordinary trust, CI, authority, owner, and approval gates always apply. In
particular, provenance cannot waive ADR 0004's exact-head fork trust gate,
select `require: none`, reduce approval counts, choose a more permissive queue,
or supply an approver identity. Agent evaluations remain ordinary CI checks
required through `check-success = X`.

Add the attributes below to the existing condition grammar, but permit their
use only in a proposed additive per-queue `provenance.requirements` section.
Each rule has a `when` expression using the existing list, `or:`, `and:`,
`not:`, and atom syntax. Its `require` mapping accepts `conditions`, an expression
in that grammar, and `approvals`, an ADR 0007 approval requirement.
At least one is required; both apply when present. Approval requirements need
the queue's existing `approvals` opt-in and cannot use `none`. A rule cannot
remove any other requirement or suppress `approvals.default`.

Provenance requirements may only add obligations evaluated on their own
inputs. They never change any baseline input. Merely ANDing results from a
shared, expanded reviewer pool would violate this rule.

Evaluate each change in this order, from one recorded observation generation:

1. Evaluate the baseline exactly as if `provenance.requirements` were absent.
   Preserve its original applicable approval requirements, default, owners,
   eligibility set, stances, and all other condition inputs. In particular,
   baseline `enter_when`, `merge_when`, and approval conditions see only the
   original `approved-reviews-by` set. Added provenance pools cannot expand it,
   alter a baseline request-for-changes veto, or suppress the baseline default.
2. Evaluate every provenance `when` against that fixed baseline snapshot plus
   the provenance attributes. Its `approved-reviews-by` is the baseline set,
   including the existing exclusions and pending semantics, before adding any
   provenance pool. All triggers use the same snapshot. A rule cannot make
   itself or another rule applicable by changing reviewer eligibility.
3. Evaluate each applicable `require` separately. Its approval obligation uses
   its own pool and ADR 0007 exclusions and vetoes. For `require.conditions`,
   `approved-reviews-by` includes baseline-eligible reviewers and reviewers
   eligible for this rule's approval obligation only. Without an `approvals`
   entry, it uses the baseline set. No other provenance rule contributes a pool.
   Other ordinary inputs retain their baseline values.
4. Require the independently evaluated baseline AND every provenance obligation
   to pass. Retain each layer's inputs and result as separate decision evidence.

A false `when` adds nothing; a true `when` requires a true `require`.
A pending `when` blocks unless `require` is already true. An unreadable trigger
therefore cannot silently skip an extra human review. Rules are combined with
AND, never first-match selection. Reject provenance
attributes in `enter_when`, ordinary `merge_when`, approval exemptions, and
queue selection, including inside nested OR/NOT groups. This placement refines
the suggested unrestricted conditions to prevent `or: [CI, provenance=human]`
from authorizing a landing through self-asserted metadata.

This is proposed syntax, not configuration current binaries accept. The second
rule requires the later checkpoint-resolver slice:

```yaml
provenance:
  requirements:
    - when:
        - or:
            - provenance = agent
            - provenance = mixed
            - provenance = undeclared
      require:
        approvals: {human: true, count: 2}
    - when: ["#agent-commits > 0"]
      require:
        conditions: [checkpoint-present]
```

The surrounding queue still requires, for example, `check-success = ci` and
its normal ADR 0007 approvals. `#approved-reviews-by >= 1` remains available in
`require.conditions`, using the rule-local eligibility snapshot above. It is
not by itself a human-only pool. Use `approvals: {human: true, count: N}` for
human review. Baseline conditions and every `when` keep baseline eligibility.
Fresh eligibility excludes configured agents, authors, submitters, and
accountable humans as ADR 0007 requires. One reviewer can satisfy both layers.
With a normal requirement of one human, the example raises the minimum to two.
This ADR adds no distinct-reviewer allocation across requirement pools.

Omission can still evade a rule that targets only `agent` or `mixed`. No Git
reader can discover undeclared assistance. Owners needing protection from that
case require human review for all work or also include `undeclared`; even a forged
human identity is possible, so baseline review must cover the threat they mean
to prevent. Neither the UI nor audit may describe this as agent detection.

### Conditions use complete per-change observations

`agent|human|mixed|undeclared` below lists alternative values, not pipe syntax.
Use `or:` for alternatives in a policy. Lists contain unique stable keys so
the existing `#` length operator keeps its meaning.

| Attribute | Value after a complete observation | Truth rule |
|---|---|---|
| `provenance` | String: `agent`, `human`, `mixed`, or `undeclared` | Agent and human declarations anywhere in the inventory give mixed; otherwise any agent gives agent. Human requires every commit to have human evidence and no agent evidence. All remaining inventories, including empty ones, are undeclared. Equality compares exactly. |
| `agent-commits` | List of commits with agent evidence, including mixed commits | `#agent-commits` counts each original commit once, regardless of sessions or sources. |
| `unresolved-checkpoints` | List of unique `(commit, source, declaration)` keys with terminal unresolved outcomes | `#unresolved-checkpoints = 0` is true only after every declared checkpoint has been assessed. Multiple failures on one commit are separate keys. |
| `checkpoint-present` | Boolean over agent-declared commits | True when each has at least one resolved checkpoint and all its checkpoint declarations resolve; false for a terminal failure or an agent commit with no resolvable checkpoint declaration. True vacuously when there are no agent commits. |

The unresolved list counts checkpoint declarations, not bare agent identities
or neutral sessions. Thus zero unresolved checkpoints does not imply
`checkpoint-present`. A coauthor-only agent commit has zero unresolved
checkpoints and fails checkpoint-present. The neutral trailer alone also fails.

The string `undeclared` is separate from ADR 0003's three-valued unknown,
called pending here. A completely read commit with no declarations yields
`provenance = undeclared` as true. Unreadable commit history or an enabled
extraction source yields pending for every affected attribute, including negations and numeric comparisons. No
partial inventory may yield a passing zero count. Failure to resolve a
checkpoint does not erase known agent classification or a complete agent count.

| Source outcome | Resolution-dependent condition result |
|---|---|
| Request still running, network/authentication error, interrupted read, or transient timeout within the remaining acquisition budget | Pending; no reuse of an earlier passing observation as current evidence. |
| Successful exact lookup reports no ref/path, invalid ID, wrong object shape, invalid metadata JSON, or unsupported declared format | Known unresolved; checkpoint-present is false and the unresolved count includes the declaration. Absence expires as described below. |
| Unmapped fork or checkpoint source, or a server without required protocol/filter capability | Terminal `unavailable_source` or `unsupported_capability`, with the source and reason in status and evidence. Resolution-dependent gates fail explicitly. |
| Deterministic size, entry, or work limit, including the acquisition deadline, exceeded | Terminal `limit_exceeded`. Checkpoint-present cannot pass. Counts needing a truncated inventory have no complete value and cannot authorize progress. Status reports `limit_exceeded`, never retryable pending. |
| All required objects read within bounds | Evaluate from the recorded complete values. |

AND is false if any operand is false, otherwise pending if any is pending.
OR is true if any operand is true, otherwise pending if any is pending.
NOT preserves pending. Only true authorizes progress. Retry transient reads
with existing bounded backoff; surface persistent errors for the owner to fix,
without converting elapsed time into a passing result. A terminal limit failure
is not automatically retried against unchanged inputs; it needs an input,
source, or owner-approved limit change.

Known absence is an observation with an expiry, not a permanent failure.
Use a bounded negative-cache lifetime, proposed at 30 seconds. Check expiry on
every evaluation and schedule a wake-up at expiry for a blocked change even if
no new push or review arrives. Re-observe the exact ref and any enabled fallback
within that change's budget. An unexpired absence can block with its recorded
reason; an expired absence cannot answer the new evaluation. While its refresh
runs, resolution-dependent attributes are pending. A late checkpoint can then
satisfy the gate on the same revision without changing the brief or receipts.

### Batches, rebuilds, and change identity

Evaluate requirements per change and revision, never by pooling declarations
or reviews across a batch. Every member must pass. Counts exclude speculative
predecessors assigned to other changes; shared source commits can occur in
multiple changes' inventories but are counted once within each. A fully human
member cannot hide an agent member. Cache immutable Git reads across members
without sharing their policy decisions.

Rebase retains recognized provenance trailers on each replay and records the
original-to-replayed mapping alongside ADR 0004's `OpenMerge-Replayed-From`.
Never trust a contributor-supplied replay trailer to choose an original.
OpenMerge's build result supplies that mapping. Merge preserves original
history. Only provenance-opted-in queues retain distinct recognized trailers
in the single attribution block of squash and generated merge messages. Bound
that block to a proposed 64 KiB of UTF-8, counting existing attribution and
sign-off content as well. If required retention would exceed the bound, fail
that change with terminal `limit_exceeded` before publishing a test ref. Never
silently truncate attribution. Queues without opt-in keep existing messages.
Counts still use originals, not generated commits or copied trailers.
Fast-forward leaves heads untouched and keeps mappings in decision evidence,
not contributor metadata on its marker. The final `OpenMerge-Batch` line and
reserved `OpenMerge-*` sanitization rules remain authoritative.

Rebuilds use the pinned originals. Re-pushes create a new revision and discard
prior provenance eligibility. Base rewrites refresh the source inventory even
when the head is unchanged. Ordinary predecessor landings do not erase the
stable inventory or its obligations. Additional commits introduced by a build
must be classified before publishing that test ref; inventory failure blocks
publication. In post-merge verification, record tested T and landed L separately
and assess any changed composition before landing; never call L tested.

Provenance sources, parser/classifier versions, identity rules, and requirement
expressions belong to the effective policy digest. Read them only from the
governing default-branch policy under ADRs 0007 and
[0008](0008-openmerge-policy-authority.md), including owner approval for edits.
Contributor `.entire/settings.json` may explain a backend choice but cannot
change policy, remote access, limits, or the supported lookup routes.

Keep the canonical evidence-record digest separate from the declaration digest
bound into the opted-in change's brief and receipt identity. The declaration
digest covers only inputs derived from the pinned source inventory under the
governing policy: the declaration inventory, declared classes, and requirement
definitions those declarations can trigger. Include their `when` expressions;
do not drop a potential requirement based on a mutable review or checkpoint
observation. Actual applicability and satisfaction at evaluation are gate
evidence only. Policy and classifier versions remain bound through the
effective policy digest.

Checkpoint resolution outcomes, known absence, ref tips, and metadata object
IDs never enter the brief digest. A checkpoint arriving late or a pusher
rewriting the unprotected legacy checkpoint branch refreshes gate evidence
without voiding anyone's receipt. Mutable approval observations used by `when`
also cannot churn receipts. A changed declaration inventory, class, or bound
requirement definition does invalidate receipts and not-yet-started decisions.
ADR 0007's Q7 phrase "provenance-only" refers to governing-config object identity.
This ADR calls mutable checkpoint updates "checkpoint evidence refreshes" to
keep that meaning separate.

Run declaration extraction before publishing a CI test ref. With the later
resolver enabled, acquisition needed by provenance rules and applicable
checkpoint obligations also gates this publication. This avoids CI spend on
changes already blocked by checkpoint availability or terminal acquisition
limits. Review and CI conditions can still use ADR 0007's speculative-CI
behavior; all added obligations must pass at landing. This ADR adds no baseline
admission requirement. Exclude a member failing the pre-CI provenance gate
before composing and testing the remaining members. Refresh all added
requirements, review eligibility, and required checkpoint resolution again
before the durable landing start. Pin the read generation and every member's
evidence atomically with the decision,
the landing-check evidence, and `LandingStarted`; reject a stale generation at
that transaction boundary. Do not hold a database transaction open for fetches.
Concurrent ref movement after observation does not revoke an already-started
attempt. Its recorded observation settles through the existing fence protocol.
Replay and recovery use retained evidence, never today's checkpoint refs.

### Bound fetch cost before trusting any object

Use the authenticated Git transport already authorized for that repository.
Only owner-approved source repository mappings can expand it. A mapping has
the conceptual shape `(source forge host, stable source repository ID, provenance
source/version) -> (approved checkpoint repository ID, transport credential
binding, allowed backends)`. Exact configuration keys belong to the resolver
slice. Resolve destinations through the owner's configured transport registry;
no entry is a contributor-supplied URL. Record the selected mapping and both
repository identities in evidence.

For same-repository PRs and direct changes, the default checkpoint source is
that repository's authorized Git transport. A fork PR needs an explicit mapping
from its head repository identity to an approved checkpoint repository. It does
not inherit a target-repository lookup or authorize arbitrary fork access.
A requested checkpoint lookup for an unmapped fork returns terminal
`unavailable_source`, explaining that resolution is unsupported for this source. Declarations still classify and can
require review. Checkpoint requirements only make sense for same-repository or
explicitly mapped sources; unsupported changes remain visibly blocked until
the owner supplies a mapping or changes policy.

Entire's `checkpoint_remote` can place metadata in a separate repository even
for a same-repository code change. Supporting that setup requires the same
owner-approved mapping. Contributor settings may describe it but cannot grant
access or select the destination. Never take a remote URL, refspec, credential,
local path, or executable from a trailer or checkpoint settings.

Fetch only exact derived refs, into an isolated local namespace, with explicit
source/destination refspecs. For example, fetch the validated Entire ref to a
private cache ref, or fetch
`+refs/heads/entire/checkpoints/v1:<private-cache-ref>` for legacy storage.
Pin fetched tips to object IDs before reading. Do not install Entire hooks,
check out checkpoint trees, initialize submodules, or follow external assets.
Wildcard `ls-remote` is a diagnostic, not the normal queue acquisition path.
Require Git protocol v2 `ls-refs` with `ref-prefix` filtering for the exact
validated refs, including the legacy branch if enabled. Bound advertisements
while reading them. A server that cannot provide or does not honor this
filtering gets terminal `unsupported_capability`, not an unrestricted discovery
fallback or indefinite pending. Use bounded shallow/filtered object acquisition;
if a required object filter is unsupported, report the same explicit outcome.

Enforce these proposed hard ceilings per change evaluation, with a global
concurrency cap:

- 1,024 source commits, 16 declarations per commit, and 128 distinct checkpoint
  lookups; duplicate IDs reuse acquisition but retain commit-specific evidence.
- 64 MiB transferred, 128 MiB expanded objects, 10,000 visited tree entries,
  and 256 KiB per metadata blob. Limit the complete fetch subprocess, disk use,
  and decompression, not merely the bytes later handed to a JSON parser.
- One 30-second active acquisition deadline per change across all sources and
  retries, with at most two concurrent fetches per process. Cancellation removes
  private temporary data. Time waiting for a scheduler slot consumes no active
  acquisition budget.

The batch budget equals the sum of its members' individual budgets for each
resource. A member cannot borrow another's allocation. Charge each requesting
change the full cost of a shared acquisition, as if it ran alone. Never divide
the cost across members to evade a per-change ceiling. Cache reuse never
transfers unused budget. If a member exceeds its own budget,
stop its acquisition, fail only that member with `limit_exceeded`, and exclude
it. Cancel any composition that already included it, then compose and test the
remaining members. Never wait for that member's retry before progressing the
others. Bound concurrency and schedule fairly across changes and repositories
so one slow member cannot occupy every slot. The sum is an accounting ceiling,
not permission to serialize everyone behind one member's deadline.

A supervising process meters transport bytes, expanded-object bytes, tree
entries, metadata bytes, elapsed active time, and temporary disk use. It kills
the Git child process group when a ceiling is reached and rejects incomplete
output. Enforce byte limits during transport and decompression, tree limits
during traversal, and disk limits with a quota on private temporary storage.
A parser checking a blob after an unbounded fetch does not enforce these limits.

These are proposed safety bounds, not measurements of Entire checkpoint sizes.
Owners must confirm them before implementation. Policy cannot raise process
ceilings. The optional legacy branch can exceed them even with a shallow fetch;
large branches are expected to return terminal `limit_exceeded`. There is no
unbounded fallback. Bound advertisements, repeated missing-ID probes, retries,
cache size, and negative-cache lifetime too. Cache successes by repository and
object ID. Check negative-cache expiry on every evaluation and refresh required
mutable tips at the landing gate. Never retain transcripts as an intentional
cache or expose pack contents through status or audit.

Forged IDs can cause denial of service or deliberately block the forger's own
change. They cannot reduce its requirements. Resource limits and scheduling
fairness must keep that failure from starving other repositories. A resolved
but copied checkpoint has the same limited trust as any other declaration.
No completeness or code-quality claim follows from resolution.

### Add evidence kinds in export v2; preserve journal payloads

The current audit format is **v1, frozen by PR 10a-2b-2, PR #149**, rather than
an unfinished 10a-2 contract. See the
[frozen specification](../research/adr-0008-10a-design.md#v1-frozen-specification),
[fixture rules](../../internal/audit/testdata/README.md), and
[`internal/audit/source.go`](../../internal/audit/source.go). Its evidence-kind
inventory and dependency selectors are fixed. Current replay rejects a manifest
version other than 1; merely adding a recognized kind would change saved bytes.

Introduce **export v2**, with a `provenance` evidence kind and explicit edges
from an opted-in landing decision to its exact provenance generation. Keep
the canonical hashing, selection, watermark, dependency closure, and external
anchor guarantees of ADR 0008. V2 records the actual typed condition inputs,
truth values, declaration and record digests, build mapping, read outcomes, and
policy source. Cross-range provenance dependencies must be retained and
exported; a required missing record fails export, not `not recorded`.
Historical decisions predating activation use `not recorded` without invented
human declarations. Export never consults Git, Entire, or a forge API.

Use new event types, provisionally `change_provenance_observed` for bounded
revision evidence and `landing_provenance_recorded` for per-attempt/member
decisions and their references. Exact wire schemas and chunking for large
inventories belong to an implementation PR with golden vectors. Commit all
required chunks and the decision binding atomically; incomplete evidence
cannot authorize landing. Keep existing `LandingStarted`, decision, check,
observation, and snapshot payloads byte-for-byte unchanged. Do not smuggle new
attributes into existing condition-evidence events: older validators reject
unknown attribute names even if the enclosing JSON shape is unchanged.

Preserve v1 manifest replay and all frozen fixtures exactly. The owner-delegate
has decided the export default: emit byte-identical v1 unless the selected
range or its required dependency closure contains a provenance-bearing decision
or event. Such a range emits v2. An explicit v2 request also emits v2 for a
legacy-only range. A requested v1 representation of a provenance-bearing range
fails instead of dropping dependencies. Installing v2 support alone does not
change legacy queues' exports or ADR 0008's 10b anchor consumers. An old v1
exporter may preserve an unknown source as opaque payload with empty kinds. That is not provenance-aware
verification and cannot certify the new policy. Older binaries reject v2
manifests. They must not silently downgrade or recompute an anchor as v1.

New additive journal events are also **not readable by older strict queue
decoders**. This design avoids breaking old payloads, not the downgrade boundary
after first use of a new type. Install readers and v2 export support everywhere
before enabling writers, drain older workers, and take a verified backup.
Before first new-event emission, an otherwise compatible old binary can be
restored after removing unsupported policy keys. After emission, disabling
provenance does not erase history. Downgrading past support requires a database
restore to the pre-emission boundary and reconciliation of external effects,
or a separately reviewed compatibility reader. Never delete journal evidence
or ignore unknown events as a rollback technique. Snapshot support needs a
new version; unchanged legacy queues retain their existing encoding.

### Non-goals

- Store, render, summarize, or search transcripts and prompts. Fetch transport
  may transfer sensitive blobs; acquisition must minimize and bound that cost.
- Judge code quality, identify undisclosed agent use, or attest human authorship.
- Implement agent review, model evaluation, token accounting, or an Entire SDK.
  Agent review/evals remain ordinary `check-success` CI requirements.
- Change forge write restrictions, fork trust, review authority, or verification
  modes. A new forge supplies the same Git transport and existing driver contract.

### Related ideas

Cross-repository dependent changes are being drafted separately as ADR 0011.
This ADR defines no dependency graph, cross-repository ordering, or atomic
multi-repository landing protocol.

## Consequences

Owners can require human attention and checkpoint availability for declared
agent work with replayable evidence, using any forge. Entire is one optional
source. Absence of its CLI or metadata does not affect queues without opt-in.
Legacy queues retain their configuration digest, events, and landing behavior.

This adds a bounded Git acquisition path, new policy syntax, evidence storage,
and an explicit journal downgrade boundary. A strict checkpoint requirement
can block legitimate work when metadata is missing or too large. Owners must
accept that cost and the limits of self-declaration before activation.

### Rollout and tests

This **Proposed, docs-only PR is Inert**. It activates nothing. The
owner-delegate has decided to ship the no-network declaration, review, evidence,
and audit path first. The Entire network resolver is a later independent opt-in.
Pure unwired packages are **Inert**. Shared evaluation, reporting, acquisition,
and landing wiring are **Hold until ADR 0009 dogfood period B drains**, expected
around mid-November 2026. The calendar estimate is not permission to deploy.
The [dogfood report](../research/phase2-dogfood.md#plan) defines the actual
boundary, including an empty queue/chain and no remaining period CI.

Split implementation along ADR 0008's contract, storage, export, evaluation, and
landing seams. Each row is a separate reviewable PR and can split further.
Reader support precedes writers. Inert describes a slice's own behavior, not
permission to deploy its held dependencies. A migration is Hold (migration).

| Slice | Scope and dependency | Required proof before merging/activation |
|---|---|---|
| 1, Inert | Unwired declaration contracts, fixed trailer parser and identity classifier, candidate additive schema and declaration digest; no runtime calls | Trailer case/folding/duplicates and malformed values; identity spoofing and class conflicts; no-declaration `undeclared`; all three-valued operators; unchanged legacy digest vectors. |
| 2, Inert | Bounded local inventory and original/build mapping package, after 1; no network or queue wiring | Complete all-parent traversal, merge-side commits, base rewrites, source limits, bounded attribution, and terminal truncation diagnostics through real-Git fixtures. |
| 3a, Inert | New event contracts and pure snapshot decoder, after 1–2; no append calls or new snapshot writes | Golden payloads, complete/chunked inventory, generation bindings, legacy snapshot bytes, strict old-decoder refusal, and snapshot-plus-tail replay. |
| 3b, Inert | Read-only audit v2 and dependency selection, after 3a; no runtime writers | Independent canonical vectors, cross-range closure, missing required evidence failure, default byte-identical v1 for legacy-only ranges, automatic v2 for provenance events/decisions, explicit v2, and 10b anchor compatibility. |
| 4a, Hold | Wire no-network evaluation and declaration-bound briefs, after 2 and 3a–3b | Separate baseline, trigger, and rule-local approval snapshots; both eligibility cases below; mutable review/checkpoint outcomes cannot churn receipts; nested policy bypass rejection; per-change pre-CI exclusion and unchanged baseline behavior. |
| 4b, Hold | Atomic journal writers, landing-start gate and tested T/landed L integration, after 4a and reader rollout | Fake-forge plus real-Git merge/squash/rebase/fast-forward, batches, speculative rebuilds, re-push, restarts, base rewrites, concurrent-generation races, and replay without network. Backup/restore downgrade rehearsal. |
| 5, Hold | Owner-approved no-network public policy/schema, diagnostics, operating guidance, and sandbox activation, after 4b | GitHub and fake/future-driver paths with zero provenance network calls, permissions unchanged, declarations and added review obligations in audit v2, and rollback acceptance after B before explicit owner opt-in. |
| 6a, Inert | Unwired bounded Entire resolver, approved source mapping contracts, and optional legacy route, after 5; no scheduler or transport calls from live queues | ULID refs, migrated hex refs, optional legacy branch, exact metadata mapping against pinned Entire source, fork and separate-remote fixtures, protocol v2 ref-prefix/filter refusal, huge advertisements/packs/trees/blobs, supervisor termination, and cancellation. |
| 6b, Hold | Wire resolver opt-in, checkpoint conditions, fair acquisition, negative-cache wake-ups, and gate evidence, after 6a | Same-revision late checkpoint, expiry checked on each evaluation, explicit unmapped/unsupported outcomes, per-member budget exhaustion without stalling honest members, legacy `limit_exceeded`, pre-CI gate and fresh landing-start observation, and unchanged receipts after metadata movement. |

Acceptance must cover both reviewer-eligibility interactions explicitly:

1. The baseline requires one `platform` approval and
   `merge_when: ["#approved-reviews-by >= 2"]`. Alice is only in `platform`, Bob
   only in `security`, and both approve. An agent declaration adds one `security`
   approval. The baseline snapshot still contains only Alice, so its condition
   fails and landing remains blocked even though both approval obligations pass.
   Compare the baseline inputs and result with provenance disabled; they match.
2. With the same original pools and approvals, a provenance rule has
   `when: {not: "approved-reviews-by = bob"}` and adds a `security` approval.
   The trigger sees only Alice and is true. Its own added pool cannot make Bob
   visible to that `when`, turn it false, or create a fixed-point loop. Bob can
   satisfy the rule's approval obligation. Reordering provenance rules, adding
   another rule's pool, or evaluating them again cannot change that trigger
   snapshot. Baseline `#approved-reviews-by >= 2` still fails.

Only true authorizes either layer. Add pending-membership variants to both
cases so an unresolved baseline set cannot become a known empty or expanded set.

Do not deploy held wiring during the comparison pair on the strength of a
disabled feature flag. Keep sandbox data and verification away from the live
journal. Pure unwired packages remain Inert subject to their dependencies.
Activation follows owner review of this ADR and the implementation evidence.

### Owner-delegate decisions recorded

- Ship declarations, classification, additive requirements, evidence, and audit
  v2 with no provenance network fetching first. Add the Entire resolver later
  through a separate opt-in, with legacy-branch resolution optional.
- Default exports remain byte-identical v1 for ranges without provenance-bearing
  decisions or events, including required dependencies. Use v2 when those are
  present or when explicitly requested. Legacy queues and 10b consumers retain
  their existing output.

### Open questions for the owner

1. Accept the additive requirement section and neutral `Agent-Session` format?
   Recommended: yes; the independent baseline snapshot prevents new pools from
   weakening ordinary policy, and declarations need no vendor storage protocol.
2. Which declarations warrant extra review, and which human pool/count applies?
   Recommended: include undeclared work or retain universal baseline human review
   when omission matters. Decide whether an extra reviewer must be distinct.
3. When the later resolver is available, require checkpoints for all agent-declared
   commits, or keep them as optional audit evidence? Recommended: audit and review
   first; neutral trailers and bot-only commits have no checkpoint resolver.
4. Accept the proposed per-change ceilings, 30-second absence expiry, and 64 KiB
   attribution bound? Enable the optional legacy branch despite expected
   `limit_exceeded` on large histories? Recommended: retain hard process bounds
   and confirm numbers with representative sandbox fixtures.
5. Which fork head repositories map to which approved checkpoint repositories,
   credential bindings, and allowed backends? Which separate Entire
   `checkpoint_remote` destinations are approved? Recommended: explicit mappings
   only, with visible `unavailable_source` for every unmapped fork or source.
6. Accept the restore boundary after first new-event emission? Recommended: yes,
   with reader-first rollout, retention, and external anchors covering provenance
   exactly as other ADR 0008 decision dependencies. The export-default decision
   above is settled; it is not an open question.

## Alternatives considered

- **Treat a resolved checkpoint or a human trailer as trusted authorship.**
  Both can be forged or copied; neither may waive CI or review.
- **Use forge APIs, Entire's service, or its CLI to obtain provenance.** Couples
  policy to a provider and adds credentials or execution. Git is sufficient.
- **Use author identity alone.** Misses agent work submitted by humans and
  confuses submission identity with the history behind the code.
- **Allow provenance anywhere in existing expressions.** An OR branch could
  replace required checks with a pusher-controlled declaration. Restrict its
  placement while retaining the existing expression grammar and truth rules.
- **Add optional fields to existing journal events or extend frozen export v1.**
  Breaks strict decoders or reproducible exports. New types and export v2 make
  both compatibility boundaries explicit.
- **Ingest transcripts and infer agent use from their contents.** Adds sensitive
  data, cost, and an unverifiable judgment that the queue does not need.
