# 0014. Review cards and risk-aware batching

- **Status:** Proposed
- **Date:** 2026-10-31

## Context

OpenMerge aims to reduce CI minutes and elapsed CI time, batch and merge PRs
intelligently, and make large, rapid, often agent-written changes reviewable.
Startup founders are lining up as design partners. This proposal connects review
cost to the batching design in [ADR 0009](0009-speculative-batching.md),
[provenance](0010-agent-provenance.md), [dependencies and privacy](0011-cross-repository-dependent-changes.md),
[hosted architecture](0012-hosted-service-architecture.md), and
[CI economics](0013-ci-cost-executors.md).

Owner decisions dated 2026-10-31 make opt-in AI review cards the flagship AI
feature, require human approval by default for one-way doors with an easy
repository opt-out, and isolate one-way changes from batching and speculation.
This supersedes only ADR 0012's CI-failure-summaries-first priority; its other
boundaries remain. The dated note there preserves the historical decision.

Credit: the card structure follows Matt Pocock's MIT-licensed
[`pr` skill](https://github.com/mattpocock/skills/tree/main/skills/engineering/pr).
Its Summary visual menu credits Dex Horthy's Apache-2.0
[`show-me` skill](https://github.com/humanlayer/skills/blob/main/plugins/show-me/skills/show-me/SKILL.md).
Retain both attributions and applicable license notices when shipping the skill.

This PR is **Inert, docs only**. It activates no feature. Implementation waits
for the recorded ADR 0009 period-B drain and all applicable
[operating gates](../operating.md); the expected date alone is insufficient.

## Decision

### Separate facts from narrative

The Go engine computes versioned, deterministic **FACTS**, with reason codes,
source identities, completeness flags and a digest binding base/head/tested SHA,
composition, policy and analyzer versions. Optional AI **NARRATIVE** supplies
Summary visuals and prose. Gates and batching use only facts and governing
policy, never narrative or author claims alone. Neither an AI nor a PR author
may talk a one-way change into two-way. Facts are observations, not proof that
an arbitrary program is reversible; unsupported analysis is unknown, hence
one-way. Reports expose that limitation rather than claiming semantic certainty.

A card has **Summary** (smallest useful visual: pseudocode, call tree, component
tree, shallow file tree, Mermaid or shape-matched diff sketch), **Evidence**
(before/after), and **Merge Danger**: **Door** (one-way/two-way) and **Blast
Radius** (one word, followed by ramifications). Deterministic cards exist without
AI; generated Summary prose is labelled advisory and linked to source facts.

### Fact inventory and conservative evaluation

All repository bytes come from pinned objects through git's own readers:
`rev-parse`, `ls-tree -z`, `cat-file`, `diff --raw -z` and `--numstat -z`, with
bounded execution/output and exact object validation. Reuse established adapters;
never hand-parse Git objects, tree entries, patches or rename syntax (the
#170–#172 lessons). Disable external diff/textconv and repository code execution.
Language/config parsers consume blobs returned by Git, not a working tree.
Missing objects, limits, unsupported syntax or inconsistent identities produce
explicit unknowns; they cannot produce an empty successful inventory.

| Signal | Definition and source | Git reader and failure rule |
|---|---|---|
| Migrations/schema | Any added, modified, renamed or deleted migration/schema path in built-in and repo globs; inspect both old and new paths | Git inventory/blobs; match means one-way; missing inventory unknown |
| Destructive operations | Versioned language-specific AST rules for drop/truncate/delete-data and irreversible resource operations; list supported dialects in analyzer metadata | Git blobs, language parsers rather than Git syntax parsing; hit one-way, unsupported relevant language or parse failure unknown |
| Workflow/CI/infra/secrets | Any change to built-in workflow, CI, infrastructure or secret-management paths, plus repo additions; never read secret values into a card | Git inventory; hit one-way, incomplete scan unknown |
| Deleted public APIs | Compare exported declarations/signatures in base/head using supported language API extractors, including removal through rename | Git blobs plus language parsers; deletion or incompatible signature one-way; unavailable API surface unknown |
| Data formats | Compare supported serialization/schema declarations for incompatible wire/storage changes, including removed fields and changed encodings | Git blobs plus format parsers; incompatible change one-way; unsupported affected format unknown |
| Dependency majors | Compare declared/resolved package identities and major versions in supported manifests/lockfiles, treating pre-1.0 incompatible minor changes conservatively | Git blobs plus ecosystem parsers; major/incompatible bump one-way; unresolved ranges, missing lock or parse failure unknown |
| Repo door globs | Governing base policy adds paths requiring one-way treatment; old and new names both match | Git reads governing config and inventory; invalid config blocks evaluation, never relaxes built-ins |
| Packages touched | Distinct package IDs owning changed old/new paths in supported workspace manifests | Git inventory/blobs plus static manifest parser; absent/ambiguous ownership unknown |
| Reverse-import fan-out | Number of distinct transitive reverse dependents of touched packages in base/head union graph, excluding touched packages | Git blobs plus static import parser, no execution; incomplete graph unknown |
| CODEOWNERS areas | Distinct effective ownership rule areas matched by old/new paths under governing Stage A semantics | Git blobs and existing CODEOWNERS parser; unresolved ownership or malformed rules unknown |
| Cross-repo edges | Count affected incoming/outgoing dependency edges and authorized scopes from ADR 0011's verified projection | Not Git alone: independently bound dependency observations; unavailable or stale graph unknown |
| Files/lines | Count distinct changed paths and added plus deleted lines; renames count once, binary line counts are unknown | Git raw/numstat readers; binary/overflow/truncation unknown for size tier |
| CI evidence | Accepted producer/run/attempt/check generation on exact queue tested SHA/ref, compared with equivalent base checks | Forge driver/journal, not Git; absent or incomparable checks say unknown, never passing |
| Test reports | Named test outcomes and failures from bounded supported JUnit/CTRF reports tied to those run identities | CI artifacts, not Git; missing reports labelled unavailable; no invented before/after |
| Provenance | ADR 0010 identities, declarations, attestations and requirements bound to change/revision/composition | Git's trailer reader for commit declarations plus verified forge/journal sources; absent/unverified stays unknown and required provenance blocks |

Any door hit or unknown yields one-way. Two-way requires complete supported
analysis and no hits. Missing optional test reports do not alone change the door,
but cannot satisfy required evidence. Newly passing/failing checks require the
same producer/check identity on base and tested SHA; report missing, pending and
incomparable separately. A batch result is composition evidence, not proof that
each member individually fixed a test.

Blast radius uses `none`, `small`, `broad`, `unknown`. `none` requires a complete
inventory containing only documentation, no executable/config/schema effects,
no dependency edges and no affected code ownership areas. `small` requires at
most 1 package, 5 reverse dependents, 1 ownership area, 0 cross-repo edges,
10 files and 300 changed lines. Exceeding any limit means broad; missing any
required input means unknown. Docs classification cannot override a door hit.
These are initial policy defaults, to validate with partners, not measured safety.

### Rendering and privacy

Publish a per-PR check summary, with an optional single updated comment; a batch
card rolls up members, cumulative composition and worst door/radius; a stack
card shows ordered layers, dependencies and the riskiest layer. Include stale
markers and exact fact digests. Status API and CLI expose the same structured
facts, claims, mismatches and advisory narrative through existing read-only
interfaces. Rendering never fetches extra private repository data.

For unauthorized audiences use ADR 0011's explicit public-field allowlist,
including generic waits rather than private names, counts, links or inferred
existence. Apply projection before rendering or prompting. Metamorphic tests
vary private-source existence, closure, supersession and landing while requiring
identical unauthorized API, CLI, PR, batch and stack output. Do not disclose a
private edge through aggregate counts, risk explanations or narrative caching.

### Policy and batching

Proposed repository keys below are a future schema, not currently supported
configuration. Load only owner-approved governing base policy; a PR cannot
relax its own classification. Invalid keys/values fail configuration validation.

```yaml
review_cards:
  door_globs: []                 # additive to versioned built-ins
  require_one_way_approval: true # easy per-repo opt-out: false
  risk_batching: false           # explicit rollout opt-in
  batching_tiers:
    small: {max_size: 8, max_depth: 4}
    broad: {max_size: 1, max_depth: 1}
```

With risk batching enabled, two-way none/small changes share aggressive batches
and deeper speculation, bounded by ADR 0009's adaptive window, existing queue
ceilings and budgets; tiers never raise queue limits. Broad/unknown radius uses
the conservative tier. One-way always has size 1 and no speculative predecessor
or successor: drain earlier work, test on settled base, settle its landing, then
build successors. A stack containing a one-way layer stops at that barrier.
Never run later speculative CI on top of it. Reclassification invalidates affected
unstarted compositions/suffixes; durable landing attempts still settle through
the existing fence. Revalidate facts/policy at durable start and final guard.

One-way adds one eligible human approval through ADR 0007 Stage A's existing
head-bound, identity-checked evaluator, including its author/submitter/accountable
human exclusions and revocations. Missing Stage A support blocks activation of
this gate. Existing approval, receipt, CODEOWNERS and provenance requirements
remain conjunctive. Opting out removes only this added approval, never the
one-way solo barrier, other approvals, provenance or CI requirements. Record and
audit the governing opt-out; no AI, author body or unapproved head policy can
set it. Default risk batching is off until rollout acceptance.

### Agent loop

Ship the attributed template as an OpenMerge authoring skill. Agents supply
Summary, before/after Evidence and Merge Danger, then read engine facts and
correct discrepancies. Parse PR bodies with goldmark into an AST and a bounded,
fail-closed grammar: exactly one top-level Summary, Evidence and Merge Danger
section; exactly one Door field with enum `one-way` or `two-way`, and one Blast
Radius field with a single supported token followed by optional ramifications.
Reject duplicates, contradictory fields, HTML-hidden values, nested spoofed
headings and fields inside code/quotes as claims. Missing/malformed claims are
unverified, never inferred as two-way. Pin body version/digest and PR revision.
Flag mismatches visibly, especially claimed two-way versus computed one-way;
claims may request stricter treatment but can never loosen computed policy.
Editing a body cannot satisfy approval or provenance or rewrite recorded facts.

### Optional AI narrative

Per-organization opt-in and a required monthly spend cap control hosted calls
through Cloudflare AI Gateway under ADR 0012. Review cards replace CI-failure
summaries as the first flagship AI slice. Narrative remains advisory, labelled
AI-generated with model/prompt version, input digest and fact links. AI outages,
cap exhaustion or opt-out leave deterministic cards and queue authority intact.

PR content, diffs, comments and artifacts are untrusted input: isolate them as
data, supply no tools/credentials, prohibit instruction execution and validate
bounded output. Sanitize Markdown/links and prohibit active HTML. A prompt cannot
change facts, approve, enqueue or merge. Use audience-scoped inputs and caches;
do not send secret-bearing blobs. Require no provider-side retention and disable
Gateway prompt/response logging; if these cannot be established, send nothing.
Retain generated cards only as explicitly approved artifacts under ADR 0012's
retention policy, separately from immutable audit evidence; record minimal usage
metadata and invalidate display on access revocation. Before dispatch reserve
worst-case token cost including bounded retries; reconcile actual cost, dedupe by
input digest, and stop before the cap. Publish cost per generated/used card;
no fixed savings or current token-price claim is made here.

### Pre-register the design-partner rollout

Before enrollment, freeze metric definitions, repo cohorts, baseline windows,
analyzer versions and stop rules. Recommend two baseline weeks and four pilot
weeks, reporting per repo and by door/radius, PR size and author type. Compare
within-repo matched workloads, recording concurrent CI/config changes and low
sample sizes; do not attribute correlation as causal savings.

Primary metrics: total runner minutes (including retries, bisections, discarded
speculation and cancellation lag) per landed PR, and active human review minutes
from consented review-session measurement. Also report CI elapsed p50/p95,
ready-to-land p50/p95, batch size/depth, wasted minutes, review rounds, card
usage/corrections, unknown and mismatch rates, approval waits, post-merge
reverts/incidents and AI dollars per landed PR. Distinguish elapsed wall time
from billed executor minutes using ADR 0013 accounting. Include rejected PR
costs in period totals rather than hiding them from the denominator.

Hypothesis: low-risk cohorts amortize CI across deeper batches while concise
visuals/evidence reduce review effort. Pre-register pilot targets of 15% fewer
runner minutes per landing and 20% less median active review time, with no
increase in severe incidents or p95 queue latency above 10%. These are targets,
not results. Any bypass/privacy leak stops rollout immediately; investigate
latency or incident regressions before expansion. Preserve ADR 0009's sealed
period-B measurements; collect no competing experiment before its drain.

### Staged slices and deploy classes

| Slice | Deliverable and acceptance evidence | Deploy class |
|---|---|---|
| 1 | Pure fact engine, read-only status; real-git pathological names, rename/binary/limits, unsupported analysis and digest/replay fixtures; no new journal writers | Inert only while read-only and without Hold dependencies |
| 2 | PR, batch and stack cards; idempotent updates, stale output and audience metamorphic tests; forge writes require sandbox acceptance and explicit rollout | Hold |
| 3 | Risk tiers and Stage A approval composition; simulator barrier tests, reclassification races, restart, fenced landing and compatible-reader/backup/restore rehearsal | Hold |
| 4 | Attributed agent skill and goldmark verification; adversarial grammar, body-edit and mismatch tests; no authority from claims | Inert for standalone skill/parser; Hold when runtime-wired |
| 5 | Opt-in narrative; injection/privacy fixtures, retention qualification, budget races and outage fallback | Hold |

Every implementation PR declares its actual deploy class; importing Hold work
inherits Hold. New persisted fact/decision evidence requires reader-first rollout,
strict codecs and operating-guide rollback boundaries before emission. No slice
waives prior drain, activation, approval or provenance requirements.

### Open questions and recommended answers

1. **How broad should initial analyzers be?** Start with Go and explicitly listed
   manifest/schema formats; unsupported relevant content stays unknown/one-way.
   Expand using false-negative fixtures before widening two-way eligibility.
2. **Who may opt out of human approval?** Repository policy owners through the
   existing governing-policy approval path; expose one boolean and an audit note.
   Keep solo/no-speculation mandatory even after opt-out.
3. **Which tier thresholds should partners get?** Start with the conservative
   limits above, preserve lower queue ceilings, and tune only after the registered
   pilot. Owners may tighten limits; loosening needs governing-policy approval.
4. **Should an author mismatch block by itself?** Initially warn; computed facts,
   required evidence and approvals remain authoritative. Track mismatch rate
   before adding any mandatory author-template requirement.
5. **Which AI model and artifact retention window?** Qualify one provider/model
   through the Gateway with no provider retention; default to no retained raw
   prompts/responses and explicitly opt in to generated-card storage. Set an
   organization-specific artifact window before enabling that storage.
6. **How should unknown classification be improved?** Display precise authorized
   reason codes and add deterministic analyzer support; never offer an AI or
   author override to turn unknown into two-way.

## Consequences

Review becomes a consistent, source-linked interface for humans and agents.
Low-risk workloads can use larger batches without allowing narrative to become
merge authority. One-way changes consume more CI and approval time; unknowns
will initially be common. We owe analyzer maintenance, policy/schema design,
privacy fixtures, skill license notices and measured partner evidence. This
proposal changes no live service and promises no measured savings yet.

## Alternatives considered

- **AI-only risk scoring:** vulnerable to author instructions and nondeterminism;
  cannot authorize landing or speculation.
- **Trust the author's Merge Danger:** useful intent but not independent evidence.
- **Uniform deep batching:** spends speculative work on irreversible changes and
  weakens the owner's required isolation rule.
- **Universal solo human review:** predictable but loses CI amortization and makes
  rapid agent-authored work harder to review.
- **CI-failure summaries first:** still useful later, but the owner prioritizes
  review cards as the flagship opt-in AI experience.
