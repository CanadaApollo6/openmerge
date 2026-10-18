# 0007. Make the change the unit of work, with pull requests as one optional view

- **Status:** Accepted
- **Date:** 2026-10-16

## Context

OpenMerge's queue has always been about changes more than pull requests. To decide whether to land something it needs a head commit, who may land it, what was tested, and whether policy allows it. On GitHub, the pull request is how those facts arrive.

A pull request is a proposal to merge some commits into a branch, with a discussion attached. Git has never required one: anyone can merge locally and push the result to the base, and branch protection is the only thing that stops them. OpenMerge itself lands this way. It composes commits onto the base in its own git cache, has CI test the result, and pushes the base in one atomic push leased on the fence ([ADR 0004](0004-forge-driver-interface.md)). What it adds over a person doing the same by hand is the evidence: the pushed commit is the tested one, landings are serialized, and the log records who allowed it.

With coding agents, two costs of the pull request model grow:

- **Every change gets the same review.** A typo fix and an authentication rewrite wait for the same human. Anthropic's source-control team reports that its reviewers, who still review every change request, now "examine the intent of the change as opposed to reading every line of code", backed by approval rules that tell a comment-only change from a sensitive one ([Redesigning source control at Anthropic](https://www.anthropic.com/engineering/redesigning-source-control)). That supports reviewing intent instead of lines; it is not an argument for no review.
- **Every reader gets the same page.** A reviewer, an operator and an agent fixing a conflict need different information, and the pull request page serves none of them well at volume.

Opening a pull request is cheap for an agent. Waiting for a human and reading a page built for line-by-line review are not. The win this ADR targets is therefore specific: low-risk changes from trusted authors land on policy without waiting for a person, human attention goes to intent where it matters, and agents can submit work without the ceremony of a branch and a pull request. It does not change CI cost or queue throughput; those are Phase 2 and [ADR 0005](0005-verification-modes.md).

## Decision

The decision has three stages. Each is usable on its own, and each later stage depends on the one before it.

| Stage | Adds | Needs before it ships |
|---|---|---|
| A | Change identity, classification, policy approvals, approval receipts and briefs, all on pull requests | This ADR; organization members read for team owners |
| B | Direct submission without a pull request (`when_review_needed`) | A follow-up ADR on user authentication and authorization |
| C | Queues with no pull requests at all (`never`), and optional model-written prose in briefs | Notification and request-changes channels outside pull requests; a model-provider decision |

### Configuration

Everything new is opt-in and lives in the default-branch configuration ([ADR 0006](0006-multi-repository-serving.md)). Key names are fixed here; the full schema lands with the implementation. This is a fragment of a proposed file, not one current releases load:

```text
version: 1
classification:
  docs: ["docs/**", "**/*.md"]
  generated: ["gen/**"]            # in addition to .gitattributes linguist-generated
  sensitive: ["infra/**", "auth/**"]
groups:
  platform: ["acme/platform"]      # approver pools; users are bare logins, teams org/team
agents:
  - identity: "acme-coder[bot]"
    accountable: ["alice", "bob"]
queues:
  - name: main
    merge_when: ["check-success = ci"]
    changes:
      pull_requests: required      # Stage B adds when_review_needed; Stage C adds never
    approvals:
      default: any_writer          # required; never none
      rules:
        - when: { labels: [docs] }
          require: none
        - when: { touches: ["infra/**"], authors: [agent] }
          require: { group: platform, count: 2 }
```

**Matching.** Within one rule, every listed facet must match. `labels` matches when the change carries every listed classification label; forge labels are never classification labels, so nobody can label their way into a rule. `paths` matches when every changed file matches one of its globs; `touches` matches when any changed file does. `authors` matches the kind (`human` or `agent`) of the forge account that authored the pull request, or of the submitter for a direct change; an account listed in `agents` is an `agent` whatever its account type. Globs use OpenMerge's whole-path dialect; `CODEOWNERS` keeps GitHub's.

**Opt-in.** A queue without an `approvals` section behaves exactly as today: none of the requirements below apply to it. A queue with one must set `approvals.default`, and the loader rejects `none` as the default.

**Identities.** Configuration names users by login and teams as `org/team`. Parsing stays free of forge calls; `serve` resolves them to stable forge IDs when it assembles the effective configuration, and a login or team it cannot resolve is a configuration problem that pauses the queue, as in ADR 0006. The resolved IDs are part of the policy digest, so a login that changes hands invalidates batches in flight rather than carrying their approvals over to its new holder. Records store both; decisions compare IDs. Conditions keep comparing logins, as they do today.

### Changes and their identity (Stage A)

**A change is a durable record owned by one queue.** OpenMerge assigns its ID; the repository and base never change after creation. A change has a sequence of revisions. Each revision pins one head commit and carries its own trust decision, classification, approvals, tests and outcome. Nothing from an earlier revision carries over to a new one.

**Each live change has exactly one source.** For a pull request, the source is the pull request, and every existing pull request maps deterministically to a change ID derived from its queue and PR number, so existing queues need no migration. For a direct submission (Stage B), the source is its submission ref. A pull request opened later for a submitted change attaches to that change as an alias rather than creating a second one. Updates to a change must name the revision they replace; a stale update is rejected.

**A re-push follows today's rule.** A new head leaves the queue and its work is discarded, as `HeadChanged` does today; the only exception is conflict grace. As planned in [ADR 0005](0005-verification-modes.md), a conflicting change may hold its position for one `conflict_grace` deadline set at the first conflict and never renewed across revisions, with a bounded number of retries, while entries behind it keep landing.

**Maintainer-approved clarification, 2026-10-18 (Q3, Stage A acceptance).**
Conflict grace is a separate later design and is explicitly excluded from Stage A
acceptance. Stage A preserves today's conflict behavior: a composition conflict
removes the affected work via `TestRefBuildFailed`; it offers no grace deadline,
retry budget or structured conflict-file report. The grace exception described
above is not delivered by Stage A.

**A landing settles before an update applies.** Once a landing attempt starts for revision H1, that attempt is settled through the fence exactly as today, whatever happens to the change meanwhile. A later revision H2 is never reported as landed by H1's attempt. This keeps the settlement guarantees of [ADR 0004](0004-forge-driver-interface.md) and ADR 0006.

**Storage stays additive.** Every deployed event and action encoding, and every golden fixture, stays byte-for-byte unchanged ([ADR 0002](0002-storage-and-state-model.md)). Batches made only of pull requests keep their existing entry, batch and action types; new facts about them (classification, receipts, decision snapshots) arrive as new event types. A batch that contains any direct submission uses new change-keyed types for every member, keeping pull request numbers only as aliases for reporting. Both forms share one planner and one fence. No pull request number is ever invented. Records written before this ADR have no accountability information and show it as "not recorded". Rolling back across these additions follows ADR 0002: once new event or action types are written, an older binary needs a compatible database restore, not just a config change.

### Classification (Stage A)

**Maintainer-approved clarification, 2026-10-18 (Q2, pinned base).** A revision's
first observed queue-base tip is its diff basis, separate from speculative build
bases. Ordinary forward predecessor landings do not reclassify it or invalidate
its receipts. A base rewrite (non-fast-forward or rewind) re-diffs affected
not-yet-landing revisions against the newly observed base and invalidates their
receipts, even if the head is unchanged. Previously shared commits can re-enter
the diff, so an old docs-only classification cannot survive on head equality.
Started attempts keep their frozen snapshots and settle first. Governing policy,
owner and attribute changes, and classifier-version changes refresh evidence as
specified below.

**Maintainer-approved clarification, 2026-10-18 (Q7, classification inputs).**
The classification-inputs sentence below uses semantic policy, not config blob
identity, for invalidation. The governing config blob ID is provenance only.
Formatting edits and edits confined to other queues' sections do not invalidate
classification, receipts, or batches. Semantic policy changes and governing
`CODEOWNERS` and `.gitattributes` content changes do invalidate them, as does a
classifier-version change. The policy digest includes those inputs and resolved
identities; legacy queues' digests stay untouched. A provenance-only refresh
cannot change a brief digest. This narrows "a change to any of these inputs" in
the following paragraph without removing the recorded object IDs.

**Classification records observations, not guarantees.** OpenMerge diffs each revision's head against its merge base with the pinned base tip, once per revision, and the landing gate reuses that classification rather than diffing again. It records the diff endpoints and merge base, the classifier version (which fixes diff and rename semantics), and the object IDs of the governing configuration, `CODEOWNERS` and `.gitattributes` files. All of these are read at the governing default-branch commit, never from the head. For release queues this differs from GitHub, which reads `CODEOWNERS` from the target branch; OpenMerge follows ADR 0006 so a release branch cannot weaken its own owners. A change to any of these inputs (including the classifier version) invalidates classifications and batches not yet landing, even when the head and base are unchanged; an attempt that has started keeps its frozen snapshot. The labels are:

- `docs`: every changed file matches `classification.docs`;
- `generated`: every changed file is marked `linguist-generated` or matches `classification.generated`. This records where a file sits, not that a generator produced it;
- `owned:<owner>`: changed files covered by a `CODEOWNERS` entry for that owner;
- `policy`: the change adds, deletes, modifies or renames (on either side) any accepted OpenMerge config path, any `CODEOWNERS` location the forge reads, any `.gitattributes` file, any CI workflow definition, or any `classification.sensitive` path;
- `unknown`: anything that cannot be observed safely.

A change can carry several labels. **Anything uncertain is `unknown`:** binary files, mode or symlink changes, submodule updates, renames that cannot be paired, diffs over the forge's file limit, or any read failure.

**Comment-only changes are not classified in this ADR.** Comments change behaviour too often to promise otherwise: Go build constraints and `//go:generate`, compiler pragmas, doctests, Python docstrings, shebangs and type annotations in comments. A later decision can add comment-only detection for specific languages, with listed exclusions, when there is a tested classifier for each.

### Policy approvals (Stage A)

**Policy adds requirements; it never removes them.** `enter_when` and `merge_when` still apply to every change. Every rule that matches a change adds its requirement, and all of them must be met, so a rule saying "no approval needed" cannot cancel another that requires one. Each requirement is checked separately and all must be met: two approvals from `platform` and one receipt from `platform` need two eligible approvers, at least one with a receipt. One approver counts toward every requirement they are eligible for. `approvals.default` applies to every change that no rule definitely matches.

**An unknown match never lowers a requirement.** A facet's match can be true, false or unknown, for example when the inventory is incomplete or the change is labelled `unknown`. A rule whose match is unknown is enforced as if it matched, and a rule like `require: none` only helps when it definitely matches. An incomplete inventory cannot show which policy files were touched, so it makes the policy owner requirement below unresolvable: the change cannot land while its inventory is incomplete, and its brief says to split it.

**Path ownership comes only from `CODEOWNERS`.** `groups` are approver pools that rules name; they own no paths. OpenMerge uses GitHub's `CODEOWNERS` file-selection precedence and last-matching-entry semantics at the governing commit. A `policy` change needs, for each policy file it touches, at least one eligible approval from that file's effective owners; one reviewer may satisfy several files. This requirement is always added, even when a `require: none` rule also matches (a `docs/CODEOWNERS` file is both). If an owner requirement cannot be resolved (no entry covers the path, an entry names only an email address, or a team cannot be read), the change cannot land and its brief says why.

**Maintainer-approved clarification, 2026-10-18 (code-owner requirement).** Rules
may also require code-owner review for every owned file, not only policy files,
so that GitHub's "require review from Code Owners" can be expressed. The
requirement `{code_owners: true}` expands to one obligation per covered changed
file: that file's effective owners are the pool, with count 1. One eligible
reviewer may satisfy several files, but owners are never unioned across files.
Each covered side of a rename has its own obligation; a deletion uses the old
path's owners. Both are resolved against `CODEOWNERS` at the governing commit,
never a head edit. The same ownership source, precedence and exclusions as the
policy-owner requirement apply: author, submitter and accountable humans never
count. `receipt: true` propagates to every per-file obligation; `count`, `group`,
`any_writer` and `human` may not combine with `code_owners`. Only the object form
with `code_owners: true` is valid; `code_owners: false` and a scalar form are
rejected.

An uncovered file adds no code-owner obligation. A last-matching entry with no
owners intentionally clears ownership and also adds none, as in
[GitHub's ownerless-path example](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners#example-of-a-codeowners-file).
This is distinct from an entry whose owners cannot be resolved: email owners,
unreadable teams or unknown team membership make that file's obligation
unresolved and blocking. A mixed entry (a resolvable user plus an email or
unreadable team) also fails closed, even if the known user approves. This is
intentionally stricter than GitHub's alternative-owner approval semantics:
GitHub can resolve account emails and accepts any eligible owner on a line;
OpenMerge cannot establish the full pool when an owner or membership is unknown.
The requirement is vacuously met only with a complete inventory and no covered
owned file; an incomplete inventory blocks. Here and below, a policy path with
no owners, including an explicitly ownerless entry, still blocks under the
independent mandatory policy-owner requirement.

**Maintainer decision within this clarification:** `{code_owners: true}` is
for rules only and is rejected as `approvals.default`. The default must be a
real approval floor: a code-owner default would be vacuously met for uncovered
files, allowing `default: none` by the back door. PR 5 tests that rejection.
Like every requirement this form only adds; the mandatory policy-owner
requirement still applies independently.

**The policy digest is part of the configuration digest.** It covers approval rules, the default, groups, `CODEOWNERS` and `.gitattributes` content at the governing commit, classification settings, the classifier version, agent mappings and the resolved forge IDs. It is a component of the queue's existing configuration digest, so a change to any of it invalidates batches in flight with no second mechanism.

**What intent means.** For a pull request, the intent is its title and body, with line endings normalized and surrounding whitespace trimmed. For a direct submission, it is the stated intent. The intent digest covers that text. Editing the intent is not a new revision and does not discard tests.

**Approvals are head-bound; receipts also bind intent.** Each user's latest stance, from either channel, is the one that counts: an approval, a request for changes, or a withdrawal (`@openmerge unapprove`, or a dismissed or changes-requested native review). Stances keep their source identifiers and the source's own ordering (review IDs and submission times, comment IDs). Within a channel, source order decides; across channels, the order OpenMerge recorded them in the log, with one exception: a withdrawal or request for changes is only cleared by an approval shown to be newer by the source's own times. Observing an older action again, or seeing it for the first time late, never supersedes a newer stance. Each approver counts once, by user ID, and only toward requirements they are eligible for; a reviewer is eligible when their approval would count toward some requirement that applies to the change. The author, submitter and accountable human of a revision never count toward its approvals; Stage A has no self-approval exceptions. An outstanding request for changes from an eligible reviewer blocks. Write permission and group membership are re-read inside the landing gate. When a queue has an `approvals` section, `approved-reviews-by` in conditions uses this same approval set and exclusions, so a condition and a policy rule count the same people. Conditions see native approvals even where a `receipt: true` rule does not count them.

- *A receipt* is created by `@openmerge approve <brief-digest>`, a writer-only comment command authenticated like the existing commands, or in Stage B by `openmerge change approve <id> --brief <brief-digest>`. The brief digest the reviewer supplies identifies what they approved: the change, the revision, the head, the intent digest and the policy digest. A receipt counts, when given and again at the landing gate, only while its digest equals the current brief digest; OpenMerge rejects a mismatched digest and never substitutes current values. Editing the intent invalidates receipts given against the old intent.
- *A native GitHub review* on the exact head counts as an approval of that head. OpenMerge records the review's commit and submission time; its intent and brief bindings are "not recorded", and it is never bound to the intent current when it was observed. A rule can set `receipt: true` to require receipts, so that review of intent is proven rather than assumed.

**Approvals gate landing, not testing.** A revision may be tested before its approvals are met. Receipts and native review observations are events in the log, so the planner can see approval state. Inside the existing landing-start gate, where the executor already re-reads state before `LandingStarted`, OpenMerge evaluates policy for every member of the batch and records an immutable decision snapshot per member: the revision, its classification and inputs, the policy digest, the approvals relied on with their receipts or observation records, the intent digest, and the protection observation for direct changes (below). All member snapshots and `LandingStarted` are recorded atomically; from then on the attempt settles through the fence like any other.

If any member's snapshot cannot be satisfied, nothing lands. The gate records which changes failed and why: waiting for approval, an approver no longer eligible, or an owner requirement that cannot be resolved. They keep their queue positions but are skipped by batch formation, much as conflicting changes are under conflict grace, and the other members are batched and tested again without them. While a waiting change's approval inputs are unchanged, no composition including it is rebuilt or retested. It becomes eligible for a new batch on a new receipt or review observation, a permission or membership change seen at the gate, an applied configuration change (`ConfigApplied`), an intent edit, or a new revision. Speculative batches that depend on a rejected composition are discarded, and its test results never authorize a different composition. Testing before approval is speculative: it spends CI on changes that may never be approved, a cost queues accept when they want review and CI to run in parallel.

**Generated remedies are a separate source.** The reverts that ADR 0005 generates for a confirmed suspect are not submitted by anyone and are exempt from approval policy, as they are from admission testing. Their decision snapshot records the suspect, the evidence and the queue's `on_red` policy instead of approvals.

**Commits carry what is known at build time.** `OpenMerge-Change` and `OpenMerge-Classification` trailers go in each commit's single trailer block: on squash and merge commits, and on the last replayed commit of each change for rebase. Only the top commit also carries the final `OpenMerge-Batch` line that ADR 0004 requires. Fast-forward lands the original heads untouched, so the batch marker carries one `OpenMerge-Change` trailer per change. Approvals stay in the log, because they usually arrive while CI runs. In post-merge queues, the decision snapshot names both the tested commit T and the landed commit L, as ADR 0005 requires; L is never described as tested.

### Accountability (Stage A for pull requests, Stage B for submissions)

**Three identities are kept apart:** the *actor* that authenticated (a person, a bot account or an App), the *accountable human* who answers for the change, and each *reviewer*. All are stable forge user IDs. An author's kind (human or agent) comes from the forge account behind the actor, never from git author strings.

- A person submitting for themselves is their own accountable human.
- An agent's accountable humans come only from `agents` in the default-branch configuration. A submission cannot name its own accountable human, so a write token cannot assign a change to a colleague.
- For a pull request, the `agents` mapping is applied first: an author listed there is an agent and its mapped people are accountable, whatever its account type. Otherwise the accountable human is the author if that is a person. A bot-authored PR with no mapping keeps working exactly as today, with its accountable human "not recorded"; a rule can require a human approval for that case.
- Accountability neither grants nor requires authorization for pull request authors. A fork contributor is the accountable human for their change without write access, and fork trust still requires a writer's approval of the exact head under ADR 0004.

This model cannot prove that a person, rather than an agent holding their token, took an action. The docs will say so. Agents should run under their own identities (a bot account or an App), which also keeps their actions out of their owner's approvals.

### Enforcement without a pull request (Stage B)

**OpenMerge already lands by bypassing pull request rules.** Its atomic push needs the App on the bypass list of rulesets and classic protection that cover the base. GitHub evaluates required reviews, code owner review, last-push approval and conversation resolution only on a pull request, and never on a bypassed push. Even with pull requests, OpenMerge's push is not GitHub-enforced: OpenMerge reads reviews through its trust gate and conditions, and reproduces native rules such as `CODEOWNERS` review or last-push approval only where its own policy declares them.

**A direct change never lands while GitHub would require a pull request.** Before accepting a direct submission, and again inside the landing-start gate, OpenMerge freshly reads the rulesets and classic protection that apply to the base. Any active rule that depends on a pull request blocks a direct landing: a `pull_request` ruleset rule of any kind, including one requiring zero approvals; classic required reviews; code owner review; last-push approval; classic required conversation resolution. Protection that is incomplete or unreadable blocks it too. The observation goes into the decision snapshot. Once a landing has started, it settles through the fence as always.

A team that wants direct submission moves its review requirements into OpenMerge policy and removes them from GitHub, and in doing so accepts that OpenMerge, not GitHub, enforces review on that base. This has a cost: merges made through the GitHub interface are no longer required by GitHub to receive review, and OpenMerge only records them afterwards (see Pushes outside the queue). OpenMerge enforces its queue, not every path to the base. Teams that need review evidence on every change keep `required`, which keeps every change on a pull request; any review requirement they want OpenMerge to enforce still has to be configured.

### Direct submission (Stage B)

**Per queue, `changes.pull_requests` chooses how much a queue relies on pull requests:**

- `required` (the default): only pull requests become changes, exactly as today.
- `when_review_needed`: direct submissions are accepted. If approval policy or the queue's own conditions need a human review, OpenMerge opens a pull request for the change. Combinations it cannot honor are rejected with a diagnostic.
- `never` (Stage C): direct submissions only.

The setting is part of the queue's configuration digest. Older binaries reject the new keys.

**Submitting a change pushes a commit, not a branch.** `openmerge change submit` asks the server for a change ID, keyed by a client idempotency key, then pushes the local `HEAD` to `openmerge/submit/<id>` and records the pinned SHA with a stated intent. The submission ref is only where OpenMerge fetches the commit from: moving it later does nothing until an authorized update names the revision it replaces. Submission refs need write access, unlike the App-only queue and fence refs. In this stage, direct submission covers commits pushed to the repository itself; fork heads stay pull-request only.

**An opened pull request is an alias, not a second source.** It is opened as the App, from the submission ref, titled from the intent. It attaches atomically to its change and never replaces the change's submitter or accountable human. Its reviews count as approvals; every other condition attribute stays sourced from the submission. A push to its branch is not a revision update. Closing it withdraws the change's pending or admitted work and invalidates batches not yet landing; reopening it, or an authorized update, makes the change eligible again. A landing already started still settles. Merging it through the GitHub interface is a push outside the queue.

**Conditions see what a direct change actually has.** For a direct change: `base` is the queue's base; `head` is the submission ref; `files` comes from the recorded diff; `conflict` comes from composition; `approved-reviews-by` is the approval set; check attributes come from CI as today; `draft`, `label`, `title` and `author` are unknown, and unknown never passes, including under negation. New string attributes `submitter` and `accountable` (a list) exist for every change.

**Commit metadata follows ADR 0004**, with the submitter in place of the PR author and the intent's first line in place of the title; other authors become coauthors and sign-off trailers are kept. Checks that only run on pull requests, such as many DCO workflows, must be adapted to run on direct submissions; until they are, those checks are missing, and missing checks never pass. An App-opened alias does not make a change eligible for a bot exemption. Pull requests and direct changes can share a batch; each keeps its own decision snapshot.

**Authentication and authorization are a separate decision.** A follow-up ADR, required before Stage B ships, decides how OpenMerge knows who is calling and what they may do. It must meet these constraints: identity is verified with the forge on every write; OpenMerge stores no user tokens; the check goes through the forge driver so other forges can implement it; setup stays within the five-minute self-host target. It must also define who may submit, update and withdraw which changes; what happens on revocation; how unmapped agents are treated; and, for an agent with several accountable humans, that at least one must keep write permission. A mapped human's permission never stands in for the actor's own.

### Pushes outside the queue (Stage A)

**OpenMerge records the base moves it observes but did not make.** Unsettled OpenMerge attempts are settled first, so a landing of its own is never mistaken for an outside push. Each remaining observed advance is recorded as landed outside the queue, with no OpenMerge verification before landing. A move that is not a fast-forward is recorded as a rewrite of the base. Commits between two observations are listed from history when it can be read, and are shown as unknown when it cannot; provenance is never inferred from commit authorship. Verification that happens later is recorded separately, so the record stays true afterwards: once post-merge verification exists (ADR 0005, Phase 2b), outside commits enter its verification frontier like any other landed commit.

**Implementation note, 2026-10-17 — journal-only outside-push audit.** A separate
audit loop classifies durable `BaseMoved` transitions only after every overlapping
attempt has settled in the shared journal. It uses the recorded landed SHA,
including the verified contributor head for fast-forward. Git supplies ancestry
and commit-list enrichment only; missing history is unknown. The audit has its
own cache and credentials, takes no worker mutex or execution slot, and never
adds work to reconciliation, configuration, planning or settlement. Its durable
cursor resumes an incremental scan after restart; each `BaseMoved` event ID is
an insert-if-absent deduplication key. Away-and-back observations remain distinct.

Main has a pre-existing multi-server stale-read ambiguity: after `Landed(L)`, a
configuration read begun before that landing can append `BaseMoved(B)`, where B
is the attempt's pre-landing build base. A real rollback produces identical
journal evidence. Only this exact `L → B` shape sets
`possible_stale_observation: true`: it records an observed possible rewrite,
which **may reflect a stale base read rather than a real rollback**, rather than
a confident outside rewrite. A subsequent `B → L` is suppressed only if that
settled attempt has not already suppressed a transition and no other move has
invalidated its allowance.
Each attempt can suppress at most one exact build-base-to-landed-SHA move.
An outside `L → C` invalidates the allowance, so `C → L` is recorded.
For `L → B → L → B → L`, the second rewind is flagged and the second return
to L is recorded, not suppressed. If observations continue to C instead, the
flagged rewind remains and subsequent transitions are classified normally. Other rewrites do not get
this flag. Reconciliation remains unchanged; fixing its stale-read behavior is
material and deferred until after the ADR 0009 trial. ADR 0008's later warnings
must treat flagged records as lower-confidence evidence and require verification
before acknowledgement. This PR adds no acknowledgement command.


Classification uses only durable journal settlement, never live fence evidence.
If a rewrite removes a landed commit before settlement, the audit waits for
`Landed` or `LandingAbandoned`; it does not settle or clear the safety block.
An attempt that never settles can defer audits indefinitely. Pending observations
remain durable and can grow without a bound while waiting; this release does not
emit speculative overflow records. History-provider or Git failures produce
permanently unknown evidence for that observation, without enrichment retry.
The read-only history constructor makes zero installation-authenticated REST or
GraphQL calls: token minting uses the App JWT and app-scoped authentication,
not the installation REST quota, and ancestry/history use only Git smart HTTP
in a separate cache. The status/snapshot tail is bounded and is not an
acknowledgement ledger. ADR 0008 warnings and acknowledgements must read the
durable journal and its event IDs, not this tail.

### Briefs (Stage A, prose in Stage C)

**A brief is a template over recorded facts, with an optional labelled summary.** For each audience:

- *Reviewer:* the pinned head and base; the stated intent, shown as the submitter's claim, not a fact; the inventory of changed files and hunks from the recorded diff, labelled incomplete when the diff is truncated or unreadable; the labels and why each applies; the policy requirements and who can meet them; and links to test evidence.
- *Operator:* where the change is in the queue, what blocks it, and what clears each block.
- *Agent:* structured next steps (fix this check, rebase onto this commit, wait for this approval) as stable JSON.

Briefs are available through the CLI and API, and through MCP when it ships. When a pull request exists, its OpenMerge check shows a summary of the reviewer brief, its digest and a link to the whole brief. A brief's digest covers the change and revision identifiers, the head, the diff endpoints and merge base, the inventory and whether it is complete, the labels and their reasons, the intent digest, the policy digest and the requirements. It excludes queue position, approval progress, timestamps, test links and generated prose. The reviewer supplies that digest to give a receipt; a native review records none, and the brief says so.

**Generated prose is never evidence.** In Stage C, an operator may configure a model to add a summary to briefs. It appears in a delimited section labelled as generated, is never read by policy or the planner, and is off by default; choosing a provider means choosing where private diffs are sent. Because the factual inventory does not depend on it, text planted in a diff or an intent to steer the summary ("this is a trivial change") cannot hide what the change touches. This extends principle 1, "never lie about what was tested", to review: OpenMerge never presents a summary as if someone reviewed the code.

### Implementation

Stage A lands in the PRs below. This section plans the accepted decision; it
does not enable Stages B or C. **Pushes outside the queue is separately tracked
in [PR #53](https://github.com/CanadaApollo6/openmerge/pull/53), branch
`adr-0007/outside-pushes`.** It has no implementation work items in this
sequence, but Stage A acceptance requires it. ADR 0008 waits for complete Stage A
before enabling policy authority.

#### Boundaries and compatibility

The four dated maintainer-approved clarifications (Q2, Q7, the Q3
acceptance exclusion, and the code-owner requirement above) are the only changes
to accepted Decision text.
The resolved design choices in this implementation section settle the previous
open questions before their owning PRs freeze fixtures.

The current integration points are `internal/queue/{types,apply,plan,wire,snapshot}.go`,
`internal/config`, `internal/serve/{reconcile,policy,worker,command}.go`,
`internal/executor`, `internal/store`, `internal/forge`, `internal/gitops`,
`internal/status`, and `cmd/openmerge`. Keep classification and approval evaluation as pure functions
over recorded inputs. `serve` performs reads and identity resolution, the queue
fold owns durable state, and the executor performs effects through the forge
interfaces. Neither the planner nor the condition evaluator performs I/O.
Put the pure evaluator in `internal/changepolicy`, outside `internal/config`.
Config already imports queue; making queue import an evaluator in config would
create an import cycle. `changepolicy` imports neither config nor queue.

PR numbers below identify implementation steps, not GitHub issue numbers. Each
step targets about 1–2.5k changed lines, including tests and fixtures, with a
3k review ceiling. Split a step at its stated boundary if it exceeds that
ceiling; do not combine independent steps to reduce the PR count. The estimates
are planning budgets, not measurements.

| PR | Scope | Depends on | Trial deployment class |
|---|---|---|---|
| 1 | Change identity and revision projection | None | Inert |
| 2a | Pinned git inventory and diff capability | None | Inert |
| 2b | Governing-file reads and bounded hunk evidence | 2a | Inert |
| 3a | Stable identities, identity-bound grants and membership | None | Inert |
| 3b | Native review acquisition and dismissal times | 3a | Inert |
| 4a | Classification and replayable evidence | 1, 2b, 5 | Inert |
| 4b | CODEOWNERS and attributes parsing | 4a | Inert |
| 5 | Candidate schema, shared input types and pure evaluator | None | Inert |
| 6 | Durable stances, attribution and accountability | 1, 3b, 4b, 5 | Inert |
| 7a | Brief evidence, digests and receipt validation | 1, 4b, 5, 6 | Inert |
| 7b | Brief rendering and read-only views | 7a | Inert |
| 8 | Approval waits, CI outcome contract and batch selection | 1, 4b, 5, 6 | Hold |
| 9 | Atomic landing decision storage | 1, 4b, 5, 6, 7a | Hold |
| 10 | Build-time commit trailers | 1, 4b | Inert |
| 11a | Ingestion, commands and shadow gate; loader rejects keys | 1–10, including 7b | Hold |
| 11b | Publish schema and enable the complete gate | 11a | Hold |
| 12a | CLI, API and check reporting | 7b, 9, 11a | Inert |
| 12b | Acceptance evidence and operating guide | 10, 11b, 12a, outside-pushes #53 | Inert |

Lettered steps are separate PRs, not optional subcommits. This is an **18-PR
sequence in 12 numbered workstreams**. The initial four-way parallel set is
**1, 2a, 3a, and 5**. After prerequisites, 2b and 3b proceed independently;
7a, 8 and 10 can run together after 6, and 9 can join once 7a lands. The graph
explicitly includes 1 → 4, 4 → 6, and 4 + 7a → 9. A dependency on 4 means
both 4a and 4b unless the table names a particular split.

PR 5 owns the shared `Classification`, `Requirement`, receipt-identity and
stance-source value contracts in separate `internal/changepolicy` type files.
PR 4 owns classification derivation; PR 7a owns receipt binding and wire records;
PR 3b owns forge stance observations and PR 6 their durable revision attribution.
PR 1 owns queue change/revision IDs. PRs 2 and 3 expose independent forge input
DTOs, converted to the shared contracts by their consumers. No later PR invents
another decision-input type with the same meaning.

Expect collisions in forge interfaces/fakes, queue wire and snapshot registries,
and serve wiring. Use separate capability, type and test files for parallel
work, distinct fixture names, and small coordinated registry edits after the
owning types land. One author owns each registry edit at a time; rebase the
consumer before adding its cases. Do not split shared registries by copying them.

PRs 1–10 and 11a provide tested components without accepting unenforced approval
configuration. The public loader and schema reject the new keys until 11b
publishes them together and enables every enforcement path. PR 5's candidate
schema fixtures stay separate. There is no production partial-enforcement flag.
PR 7b can expose read-only briefs over recorded evidence, with explicit missing
evidence for legacy records. Deployment is a separate gate from merging; see
[Trial deployment](#trial-deployment).

The runtime opt-in for Stage A evidence is a queue's `approvals` section.
`changes.pull_requests: required` alone keeps the existing PR-only behavior.
Queues without `approvals` retain their existing conditions, policy digest,
build messages, and event/action protocol. Change IDs may be derived for those
queues without writing new records. Repository-level classification, groups,
and agent settings feed queues that opt into approvals. ADR 0008's later
mandatory owner gate in authority mode is a separate activation path, including
for queues without `approvals`.

All new persisted types listed below use additive wire-v1 envelopes with
explicit structs, strict decoding, required-field and value validation, and
fixtures in `internal/queue/testdata/wire/v1`. Respect `fixtureVariantNames` in
`wire_test.go`: exactly one registry fixture per concrete event/action type.
Keep extra state, invalid-input and decision-variant vectors outside the
`event_*.json` and `action_*.json` registry glob, in a separate testdata directory
or table tests. Existing event/action encodings,
keys, and golden fixtures stay **byte-identical**, including `ConfigApplied`
with and without its existing optional window bounds. PR-only batches retain
`Entry`, `BatchPR`, `BatchStarted`, `ChainedBatchStarted`, `BuildTestRef`,
`BuildChainedTestRef`, `Land`, `LandingStarted`, and the existing resolve actions.
A side projection keyed by change and revision holds Stage A evidence. No
change-keyed batch actions or invented PR numbers are needed in this stage.

Each PR that adds a type tests encode/decode, missing and unknown fields,
invalid values, historical replay, and immutable `Apply`. Each projection
addition also extends the snapshot codec and proves snapshot-plus-tail and
full-journal replay produce the same state and next actions. The current
version-2 snapshot codec is not connected to store persistence. Introduce a
new snapshot version for Stage A state while retaining version-2 decoding;
do not turn on compaction or change retention in this work. Keep bounded live
facts in snapshots and historical decision evidence in the journal.

Until PR 11b, no existing queue starts writing Stage A types. From PR 11b onward,
**every existing queue that adds `approvals`**, at depth 1 or a speculative
depth, writes the new observation types. Receipt, wait, and decision records
appear when those operations occur. Removing `approvals` does not erase them.
Drain old workers before activation. An older binary that cannot decode the
new types needs a compatible database restore from before their first use;
removing the config keys alone is insufficient for rollback.

#### PR 1: Derive change IDs and project revisions

- Scope: implement **Changes and their identity (Stage A)**, paragraphs
  "A change is a durable record", "Each live change has exactly one source",
  "A re-push follows today's rule", and "Storage stays additive", for PRs only.
  Add queue-owned change and revision value types and a replayable side
  projection without replacing the PR batch protocol.
- Config and loader: no keys or schema changes.
- Identity: define a versioned, domain-separated SHA-256 encoding of the
  existing canonical queue key and PR number. Length-prefix fields; use the
  stored repository spelling and installation/base/lane identity from ADR 0006,
  never YAML queue names or process-local database IDs. Pin vectors for case
  alias reuse, distinct installations, bases, lanes, and PRs. Derive legacy
  change IDs on read without a backfill or log rewrite.
- Revisions: use a durable per-change observation sequence, not the head hash
  alone. H1, H2, then H1 again are three revisions. Unchanged observations and
  re-enqueueing an unchanged head do not invent revisions. New revision facts
  carry the expected predecessor revision; reject stale updates atomically.
  Old journal observations establish the sequence on replay, without inventing
  accountability, classification, or approvals for old records.
- Events: add `change_revision_observed` with queue-local PR alias, change ID,
  revision, predecessor, head, and observation basis. Add its golden fixture.
  No new actions. Runtime emission waits for PR 11b.
- Planner and executor: preserve `HeadChanged` dequeue and work invalidation.
  A landing on H1 settles for H1 even if H2 is observed meanwhile. Do not add
  direct-submission refs or alter trust. Reserve the conflict-grace integration
  point described under Resolved questions rather than claiming it exists today.
- Tests and exit: deterministic replay across restart and repeated enqueue,
  H1/H2/H1, stale updates, PR closure, multi-base isolation, and a head change
  during an unsettled landing. Existing wire bytes and legacy plans are equal.

#### PRs 2a–2b: Read pinned diff inputs through the forge boundary

- Scope: implement the observation inputs in **Classification (Stage A)** and
  the file/hunk inventory in **Briefs (Stage A, prose in Stage C)**. This PR is
  independent of identity and approval policy.
- Split: 2a owns pinned git inventory and the narrow diff capability; 2b adds
  governing-file reads and bounded hunk evidence over it. Each has its own
  contract tests. Config, events, actions, and landing gate have no changes.
  Add read capabilities and fake-forge contract cases only; no new runtime records or decisions.
- Forge: introduce a narrow revision-diff capability with explicit base and
  head object IDs. Return merge base, both endpoints, old/new paths, object
  modes, binary/submodule flags, rename evidence, hunks, completeness, and
  typed failure reasons. The existing `PullRequest.Files` list is insufficient:
  it lacks endpoints, old paths, modes, and hunks, and is capped at 1,000 paths.
  Never mistake a partial list for a complete diff.
- Reuse `Driver.ReadFile` at an immutable governing commit. Add object-ID and
  explicit-absence evidence for config, the selected `CODEOWNERS`, and every
  governing `.gitattributes` file used by changed paths. Use a git-backed
  reader where an API cannot provide the pinned comparison or modes; keep
  transport inside the forge implementation. Bound files, bytes, and hunks,
  and return incomplete evidence at limits instead of truncating silently.
- Tests and exit: fake contract and GitHub HTTP/local-git tests for merge-base
  comparisons, renames, deletes, binary and mode changes, symlinks, submodules,
  missing objects, pagination limits, read errors, and a default branch that
  moves between reads. Every successful result identifies immutable inputs.

#### PRs 3a–3b: Observe forge users, teams, and native stances

- Scope: implement the driver prerequisites in **Configuration / Identities**,
  **Policy approvals (Stage A) / Approvals are head-bound**, and
  **Accountability**, plus the Stage A permission in **Consequences**.
- Split: 3a owns stable identity, write authorization and membership; 3b owns
  review acquisition, dismissal events and source-time fallback. No callers of
  the new capabilities activate yet.
- Config and wire: no public keys or new event/action types. Keep the existing
  `Approval` view and its legacy semantics intact. Add a richer review reader
  rather than deriving new evidence from `Approval{User,SHA,CanWrite}`.
- Forge: provide stable user IDs with login and account kind, login-to-ID and
  team-to-ID resolution, paginated team membership by user ID, and complete
  native stance observations. Reviews retain source review ID, commit ID,
  submission time, state, and dismissal evidence; comments retain author ID,
  comment ID, and creation time. Include requests for changes and dismissals
  that the current driver reduces away. Comment-only reviews are not approvals
  or withdrawals. Conditional revalidation is acceptable; a cached grant with
  no forge round trip cannot authorize landing.
- Identity-bound authorization (3a): today's `CanWrite(login)` in
  `internal/forge/github/driver.go` returns a bool and drops the response user ID.
  Add a grant observation that verifies the returned stable user ID against the
  review or comment author ID, not just its current login. Missing or mismatched
  IDs fail closed. A fresh response for a reassigned login cannot lend its
  permission to the old ID. PR 11a's receipt ingestion and final gate must use
  this capability, including after a rename between reads.
- Dismissals (3b): if the reviews API lacks dismissal time, read timeline
  dismissal events with their IDs and timestamps. If no authoritative time is
  available, record an explicit observation-time fallback and its uncertainty,
  never silently reuse review submission time. Pin the recovery protocol under
  Q4 so the dismissal cannot become permanently unclearable.
- Setup: request **organization members read** (`members: read`) for team
  resolution, and inspect the installed grant, not just the App manifest.
  Assign adoption instructions to `docs/operating.md`: existing Apps and
  installations must accept the addition. Do not change the live App's
  permissions during the dogfood pair, including this `members: read` addition.
  Missing permission does not stop unrelated personal/user-only queues.
  Named configuration identities that cannot resolve will pause affected
  queues at PR 11b activation; an unreadable effective owner requirement blocks its change.
- Planner, executor, and gate: no behavior change yet. Keep these reads under
  ADR 0006's installation budgets, preserving settlement's reserved capacity.
- Tests and exit: fake contracts and GitHub fixtures cover renamed/reassigned
  logins, stable IDs, nested/inherited team membership as the forge exposes it,
  inaccessible teams, missing grants, pagination, duplicate/out-of-order
  reviews, and dismissed reviews. Name the rename-or-reassignment-between-reads
  test: a review/comment by ID A followed by a permission response for ID B (or
  no ID) cannot authorize A. Test timeline dismissal time and missing-time
  fallback/recovery. No user token or new forge write is needed.

#### PRs 4a–4b: Classify revisions and compute effective owners

- Scope: implement **Classification (Stage A)** and **Policy approvals / Path
  ownership comes only from CODEOWNERS** over PR 2 inputs and PR 1 revisions.
  Split 4a's classification/event projection from 4b's CODEOWNERS and attribute
  parsers. PR 4a consumes PR 5's shared types; 4b completes generated/owner
  derivation before any consumer treats it as production-ready.
- Config and loader: no public keys yet. Accept typed classification settings
  in component tests; PR 11b publishes `classification` with PR 5's schema.
- Classification: pin a classifier version that fixes diff options, rename
  pairing, and inventory limits. Record base/head/merge-base, governing commit,
  governing file object IDs or absence, inventory completeness, labels, and
  reasons. Read policy files at the default-branch commit for release queues
  too. `docs` and `generated` require every file to qualify. Read generated
  attributes with directory precedence and unset/override semantics from the
  governing tree, without consulting head attributes or host git attributes.
- Ownership: implement GitHub's `.github/CODEOWNERS`, root `CODEOWNERS`, then
  `docs/CODEOWNERS` file-selection precedence and last matching entry, with its
  own pattern parser, not `condition.CompileGlob`. Retain invalid or unsupported
  owner diagnostics. Compute `owned:<owner>` labels for **every changed file**;
  only the mandatory approval requirement is restricted to policy files.
  Canonical labels are `owned:user:alice`, `owned:team:acme/platform` and
  `owned:email:ops@example.com`. Remove CODEOWNERS' leading `@` for users/teams
  and lowercase their names; preserve an email's local part and lowercase its
  domain. Email labels do not make email owners resolvable to approvers.
  Resolve effective owners per policy path, including both
  sides of renames. A group owns no paths. An uncovered path, email-only owner,
  or unreadable team cannot satisfy that path's mandatory owner requirement.
  The 4b→5 contract uses `CodeOwnersKnown` plus per-file `CodeOwners` entries
  parallel to `PolicyOwners`, not in place of them: reusing `PolicyOwners` would
  make every owned file mandatory without a rule. Record path and rename side,
  last-matching entry, resolved owner IDs and resolution status for every changed
  file, distinguishing uncovered, explicitly ownerless and unresolved entries.
  `CodeOwnersKnown` is false for incomplete inventories or unknown owners or team
  membership; retain per-file evidence and reasons even then. Deletions use the
  old path and renames record each side separately against the governing
  CODEOWNERS. Ownerless entries clear ownership; a mixed entry containing any
  unresolvable owner remains unresolved even alongside a resolvable owner.
  PR 5 expands these facts into obligations only when a code-owner rule applies.
- Mark `policy` for all four accepted config paths from `config.Find`, every
  forge-recognized `CODEOWNERS` location, all `.gitattributes` files, CI workflow
  definitions, and configured sensitive paths. Forged PR labels never affect
  classification. Binary files, modes, symlinks, submodules, unpaired renames,
  overflow, and failed reads add `unknown`. Retain known facts alongside it.
  Incomplete inventory makes policy ownership unresolvable even if no observed
  path is sensitive. Do not add comment-only detection.
- Events: add `change_classified`, including input fingerprint, classifier
  version, inventory, explanations and durable classification generation.
  Advance the generation on a base rewrite even when the diff is equal, so
  old receipts cannot be replayed. Use one registry golden fixture; keep
  complete/incomplete variants outside the registry glob. No new actions.
  Runtime emission waits for PR 11b.
- Planner and executor: expose invalidation inputs for `ConfigApplied`; never
  re-diff in the landing gate. Pin the first observed queue-base tip. Ordinary
  forward predecessor landings preserve classification and receipts. A base
  rewrite re-diffs affected revisions and invalidates receipts with unchanged
  heads; governing semantic policy, owners/attributes and classifier-version
  changes also refresh evidence. A provenance-only config blob edit does not.
  Invalidate affected not-yet-landing compositions through the existing batch
  protocol, including trailers whose classification changed; never mutate a
  tested commit in place.
- Tests and exit: a classification matrix covers every uncertainty, attribute
  override, owner precedence, path dialect, deleted/renamed policy path, and
  a PR trying to weaken its own config or owners. Repeated inputs yield equal
  classifications and hashes. Name **docs-codeowners-owner-required** for
  `docs/CODEOWNERS` always adding an owner requirement, and
  **classifier-version-only-refresh** for a refresh with otherwise equal inputs.
  Test an unchanged head after a rewind that reintroduces shared non-docs
  commits. Snapshots preserve the current evidence. PR 4b owns evidence cases
  **code-owners-governing-precedence**, **code-owners-ownerless-evidence**,
  **code-owners-mixed-entry-evidence**, **code-owners-unknown-team-evidence**,
  **code-owners-delete-rename-evidence**, and **code-owners-head-edits-ignored**:
  head CODEOWNERS edits cannot change governing ownership. Keep evaluation and
  approval satisfaction tests in PR 5.

Pin this facet derivation table in PRs 4 and 5. "Safe" means the relevant
inventory or attribute fact is complete and readable. An `unknown` label is not
an empty set of facts; a facet may use a known positive hit but cannot infer an
absence from uncertainty.

| Facet | Complete, safe inputs | Incomplete inventory or unknown relevant inputs |
|---|---|---|
| `docs` / `generated` | True iff every file qualifies; false on a known failure | Unknown; observed qualifying files never prove every-file coverage |
| `paths` (every-file) | True iff every file matches; otherwise false | Unknown |
| `touches` (any-file) | True on a hit; otherwise false | True on an observed hit; otherwise unknown |
| `policy` / `owned:` facts | Derive known path facts and owners | Keep observed positive facts; absence is unknown; policy owner and applicable `code_owners` requirements are unresolvable with incomplete inventory or unknown owners |
| `labels` | All listed classification labels must be true; forge labels are excluded | Three-valued conjunction of recorded labels; missing labels are unknown |
| `authors` | Match the supplied effective forge account kind (`human` or `agent`), after agent mapping | Unknown account kind remains unknown, independently of inventory |

Name the negative test **truncated-all-docs**: every observed file is docs, but
the inventory is truncated. It lands nowhere, its owner requirement is
unresolvable, and its brief tells the author to split it. Also test a complete
path list with an unreadable attribute: it cannot turn `generated` into false or
make an unknown rule relax policy.

#### PR 5: Define approval config and evaluate requirements

- Scope: implement **Configuration / Matching, Opt-in, Identities** and
  **Policy approvals / Policy adds requirements, An unknown match never lowers
  a requirement, Path ownership, The policy digest** as pure policy code.
  This PR is independent and can be developed beside PRs 1–3. It owns the
  shared decision-input value contracts and pure evaluator in
  `internal/changepolicy`; config parsing maps into those types.
- Proposed schema: repository keys `classification.{docs,generated,sensitive}`
  are lists of whole-path globs; `groups` maps pool names to lists of bare user
  logins or `org/team`; `agents` lists `{identity, accountable}` records with a
  user login and nonempty human-login list. Queue keys are
  `changes.pull_requests` (only `required`, default `required` in Stage A) and
  `approvals.{default,rules}`. Rules require `when` and `require`; `when` accepts
  only `labels`, `paths`, `touches`, and `authors`. `authors` accepts `human` and
  `agent`. Known classification labels and `owned:<owner>` are distinct from
  forge labels. Preserve the existing condition grammar and glob dialect.
- Validate unknown/duplicate keys, scalar/list types, empty names and match
  lists, malformed globs and identities, duplicate agent mappings, nonexistent
  group references, and positive integer counts. Reject `default: none` and
  Stage B/C values. Settle Q1 **before PR 5's fixtures**, not before PR 11.
  The union is `none`, `any_writer`, `{any_writer: true, count: 1}`,
  `{group: platform, count: 2}`, `{human: true, count: 1}`, and
  `{code_owners: true}` (see the code-owner clarification). For `code_owners`,
  reject combinations with `count`, `group`, `any_writer` or `human`, reject
  `code_owners: false`, a scalar form, and use as `approvals.default`;
  only `receipt: true` may combine with it. `count` defaults
  to 1 on every other object and is always a positive integer. `human: true` can
  combine with `group` or `any_writer: true` as a kind filter. Group and
  `any_writer` are mutually exclusive selectors; a standalone human form
  selects human writers. All non-`none` forms require fresh identity-bound
  write permission. Writer permission never implies human kind.
  `receipt: true` is valid on every requirement object, including
  `{any_writer: true, receipt: true}` and human/group combinations. It is not
  valid on `none` or as a selector by itself; scalar `any_writer` cannot carry
  it. Reject `human: false`, selector-free objects and scalar/object mixtures.
  Human means human after configured agent mapping and forge account kind.
  Parsing performs no forge calls. Validate that accountable IDs identify humans
  after resolution; do not demand that a PR
  author or its accountable humans have write permission.
- Do not make `config.Load` accept these keys yet. Keep a candidate schema and
  parser/evaluator agreement fixtures until PR 11b installs the public loader,
  key registry, generated schema command, and `schema/openmerge.v1.json`
  together. Existing accepted configs and digests remain identical meanwhile.
- Evaluation: AND the facets within each rule, accumulate every true or
  unknown rule's requirements separately, and apply the default when no rule
  definitely matches. `none` helps only on a definite match and cancels nothing.
  One ID can satisfy several eligible requirements, once per requirement.
  Always add per-file policy owners for opted-in queues, independently of
  code-owner rules. A `code_owners` requirement expands to one obligation per
  covered changed file (each covered rename side separately), whose pool is
  that file's effective owners, count 1, with `receipt` propagated. It is never
  a cross-file union. A deletion uses the old path's governing owners; an
  owned-to-uncovered rename retains the old side's obligation. Uncovered and
  explicitly ownerless files add none. It is vacuously met only when the
  inventory is complete and no file is covered by owners; incomplete inventory,
  unknown owners or unknown membership are unresolved (blocking), including
  mixed entries with another resolvable owner. Use `CodeOwnersKnown` and the
  per-file `CodeOwners` evidence from 4b, not `PolicyOwners`. Apply exclusions
  and receipt checks separately to each pool. Return explainable
  unmet/unresolvable requirements rather than a single approval count.
  Diagnose `approvals` coexisting with review terms in either condition field,
  recommending explicit pools and moving approval terms into `approvals`.
  A known empty eligible set is an empty list, so normal condition negation
  applies; an unresolved set is an unknown attribute, never an empty list.
  Under `approvals`, a positive review condition with no applicable pool stays
  unmet. Do not silently invent a reviewer pool or change condition grammar.
- Digest: canonicalize settings, defaults, groups, agents, resolved user/team
  IDs, governing owners and attributes content, and classifier version into
  one policy digest. Fold it into the existing effective queue config digest.
  Use the existing batch invalidation mechanism with the semantic digest from Q7:
  governing config blob ID is provenance only; formatting and other queues'
  sections do not invalidate. Include CODEOWNERS and attributes **content**.
  `Queue.Digest` serializes `queue.Config`, so new zero-valued fields must use
  omission that preserves the existing bytes, or stay outside that serialization.
  Pin old digest vectors; adding a zero-valued field without omission changes
  every legacy digest. Membership and current write grants are fresh eligibility
  observations, not static policy.
- Events/actions: none; reuse unchanged `ConfigApplied` at activation. Planner
  and executor consume the same pure evaluator; no gate changes yet.
- Tests and exit: truth tables for true/false/unknown facets and overlapping
  rules, default fallback, receipt counts, per-file owner pools, canonicalization,
  identity reassignment, unchanged legacy digests, and invalid config cases.
  Name **two-platform-one-receipt** (two distinct approvers, at least one with a
  receipt), **bot-identity** (`"acme-coder[bot]"` parses), and **mixed-repository**:
  an opted-in queue causes no observations, digest change or trailers in its
  non-opted-in sibling. Cover known-empty versus unresolved eligibility under
  negation and diagnostic output for review terms alongside `approvals`.
  PR 5 owns these named evaluator/parser cases over 4b's evidence contract:

  - **code-owners-every-owned-file**: a non-owner cannot satisfy an owned
    non-policy file.
  - **code-owners-disjoint-owners**: A's owner cannot satisfy B; a shared owner
    in both per-file pools can satisfy both.
  - **code-owners-owned-deletion**, **code-owners-rename-disjoint-owners**, and
    **code-owners-owned-to-uncovered-rename**: old-path owners still count;
    each covered rename side requires its own approval, never a union.
  - **code-owners-uncovered-file**, **code-owners-ownerless-entry**, and
    **code-owners-incomplete-inventory**: distinguish no obligation from an
    unresolved obligation.
  - **code-owners-mixed-entry** and **code-owners-unknown-team-membership**:
    another resolvable owner's approval cannot clear unknown owners/membership.
  - **code-owners-excluded-sole-owner**: an author, submitter or accountable
    human who is the sole owner cannot satisfy the obligation.
  - **code-owners-per-file-receipts**: `{code_owners: true, receipt: true}`
    requires a valid receipt from each file's eligible pool; a shared owner's
    valid receipt may satisfy several files.
  - **code-owners-policy-independent**: absent or met code-owner rules do not
    remove mandatory policy owners; an ownerless policy path still blocks.
  - **code-owners-invalid-combinations**: reject `count`, `group`, `any_writer`,
    `human`, `code_owners: false`, and scalar forms; accept only the true object
    with optional `receipt: true`.
  - **code-owners-default-rejected**: reject the form as `approvals.default`.
  - **code-owners-independent-count**: count-as-default plus independent
    `paths: ["**"]` count and `touches: ["**"]` owner rules preserves N approvals
    for owned, uncovered and complete empty diffs; only owned paths add owners.

#### PR 6: Fold stances and record accountability

- Scope: implement **Policy approvals / Approvals are head-bound; receipts
  also bind intent** except receipt command handling, and **Accountability**.
- Config and loader: no new keys beyond PR 5's proposal; no public activation.
- Events: add `change_accountability_observed`, `change_intent_observed`,
  `review_stance_observed`, and `approval_eligibility_observed`. Include stable
  IDs and display logins, change/revision/head, source IDs and source times,
  read basis, and known/unknown eligibility with reasons as appropriate.
  Intent records pin normalized title and body and their digest. Add one
  registry fixture per concrete type; keep person, mapped agent,
  unmapped bot, approval, changes-requested, dismissal, withdrawal and unknown
  eligibility vectors outside that glob. No new actions.
- Keep actor, PR author, accountable humans, and reviewers separate. Apply
  configured agent mappings before forge account kind; unmapped bots retain
  "not recorded" accountability. Never infer a human from git author/email.
  A fork author's accountability does not grant test-ref trust.
- Fold per-user-ID latest stances using source ordering within a channel and
  log order across channels. Retain the source-time barrier for withdrawals
  and requests for changes: an approval clears either only when its source
  proves it is newer. Repeated or late older observations cannot resurrect
  approval. A native approval binds its reviewed head; intent/brief are
  explicitly "not recorded", never filled from observation-time intent.
- Count eligible distinct IDs; exclude the author, submitter where present,
  and every accountable human. Outstanding requests for changes from eligible
  reviewers block. Eligibility means eligibility for an applicable requirement,
  not simply appearing in a review list. Share this derived set with condition
  snapshots under `approvals`; retain login comparisons for conditions and
  native approvals in that set even for receipt-only requirements.
- Planner and executor: facts change the per-change approval-input fingerprint
  without resetting CI timers or classifying a revision again. Runtime and
  landing re-reads wait for PR 11a wiring and 11b activation. Revision changes
  clear current trust/review facts; intent changes clear receipt eligibility without discarding tests.
- Persist source-to-revision attribution for native reviews, not only a reviewed
  SHA. An approval observed on revision 1/H1 cannot authorize revision 3/H1
  after H2. On first discovery after H1→H2→H1, require source time/order and
  recorded revision boundaries to prove attribution; ambiguous late discoveries
  are historical evidence only and do not authorize the current revision.
  Retain attribution across restart and snapshot replay; SHA equality is never
  sufficient. Recovery is a fresh review or receipt bound to the current revision.
- `unapprove` binds to the current revision at authenticated processing and
  records that revision. Follow Q4's conservative withdrawal barrier and
  missing-time recovery. A late positive receipt cannot be rebound this way.
- Tests and exit: permutations across channels, equal/missing source times,
  dismissals, duplicate polling, removed team members, revoked writers, self
  and accountable approvals, ID dedupe across aliases, and snapshot replay.
  Pin native-after-receipt and receipt-after-native-then-intent-edit: the latest
  native stance has no intent binding, and an intent edit invalidates the later
  receipt without reviving the older native approval. Late older observations
  cannot supersede in either direction. Test previously observed and
  first-observed-late native reviews through H1→H2→H1, restart and snapshot
  replay, and Q4's missing-time recovery.

#### PRs 7a–7b: Build factual briefs and validate receipt bindings

- Scope: implement **Briefs (Stage A, prose in Stage C)**, **Policy approvals /
  What intent means**, and the receipt digest portion of **Approvals are
  head-bound; receipts also bind intent**.
- Split: 7a owns recorded evidence, canonical digests, receipt identity and
  validation; 7b owns renderers, read-only endpoint and CLI views. PR 9 consumes
  7a without waiting for rendering. Config and loader have no additions.
  No model settings or generated prose.
- Brief: provide one recorded-fact model with reviewer, operator, and agent
  renderers. Include pinned inputs, the author's claimed intent, complete or
  explicitly incomplete file/hunk inventory, label reasons, requirements and
  eligible pools, and test links. PR 7a records per-file owner sets and expanded
  obligations with per-file unmet/unresolved reasons and stable agent reason
  codes, including unknown inventory, unknown owners and excluded owners.
  Operator output explains each blocker and what clears it. Agent output uses
  stable reason codes and structured next
  steps rather than prose parsing. Missing historical facts say "not recorded".
  Persist bounded hunk evidence with classification: at most 1,000 files,
  2 MiB of UTF-8 hunk text and 200 lines per file, in deterministic path order.
  Record omitted/truncated ranges and immutable object IDs. These limits become
  part of the classifier version. Missing hunk text never masquerades as a
  complete brief; inventory completeness is recorded separately. Renderers read
  this persisted evidence and never recompute from mutable refs or forge reads.
- **Maintainer-approved clarification, 2026-10-18 (Q7a, retained hunk budget).**
  The v8 classifier additionally retains at most 4,096 hunks across a diff and
  4 MiB of JSON-escaped hunk text (excluding string quotes), alongside the
  2 MiB raw UTF-8 and 200-lines-per-file caps. `hunk_count_limit` is v8-only;
  historical v7 records retain their original omission semantics.
- Digest: define a versioned canonical serialization and SHA-256 vectors over
  change/revision/head, diff endpoints/merge-base, inventory and completeness,
  labels/reasons, intent digest, policy digest, and requirements. Sort unordered
  sets, preserve meaningful order, and specify absent versus empty values.
  Exclude queue position, approval progress, timestamps, links to tests, and
  rendered text. Normalize intent line endings and surrounding whitespace as
  the decision requires. A renderer change alone cannot invalidate a receipt.
- Events: add `approval_receipt_recorded` with the supplied digest and explicit
  change/revision/head/intent/policy bindings, actor ID/login, source comment ID
  and creation time. Use `review_stance_observed` for `unapprove` withdrawal.
  Add one registry fixture and keep rejected-binding vectors outside its glob.
  No new actions or actual commands yet. Validate the supplied digest against current evidence
  at acceptance and again at landing; never replace it with a current digest.
- Expose a read-only reviewer brief endpoint and CLI view over recorded facts
  so PR 11b's receipt command is usable as soon as activated. Use the scoped
  route and command shapes specified in PR 12, with authenticated reads under
  the current status listener. No write bearer-token authentication is added.
- Planner and executor: no testing gate. Native reviews retain durable
  revision/head attribution and visibly lack intent/brief bindings. Tests prove intent edits invalidate receipts
  but leave tests valid; changing each included digest field changes the hash,
  and changing each excluded field does not. Provenance-only config blob or
  governing commit changes are excluded; semantic policy/content changes remain
  included. Rendered and JSON fixtures agree. A base rewrite always withdraws
  old receipt validity through a durable evidence generation, even when a re-diff
  happens to yield identical paths; include that generation in the brief digest.

#### PR 8: Skip approval waits without rebuilding them

- Scope: implement **Policy approvals / Approvals gate landing, not testing**,
  the waiting and batching behavior, and ADR 0009 **Interaction with other
  decisions / Approvals**. Public config still stays disabled until PR 11b.
- Config and loader: no additions. No new action type is needed.
- Events: add `approval_gate_rejected` with batch/composition, per-member
  change/revision, approval-input fingerprint, and stable waiting reasons;
  add `approval_inputs_changed` to wake a wait only on a changed fingerprint
  with a current observation basis. Add one registry fixture per concrete type;
  keep multi-member failure, stale wake and unknown eligibility vectors outside
  the registry glob. Existing
  stance, intent, revision, `change_classified`, and `ConfigApplied` events also
  invalidate the appropriate wait basis.
- Preserve `Entry` positions, admission IDs, and enqueue times. Initially allow
  testing without approvals. After a rejected gate, skip unchanged waiting
  revisions in **all** selection paths: fresh batches, retained retry groups,
  bisection halves, fast-forward prefixes, and speculative admission. Filter
  or retire empty pending groups so an all-waiting half cannot become a barrier.
  Compute `max_wait` from eligible candidates, not a skipped entry's age.
- Rejecting composition B discards B and its speculative suffix through
  `invalidateFrom` and existing `InvalidateBatches`/`BatchesInvalidated` effects.
  Earlier batches remain valid. Rebatch non-waiting members in their original
  order with fresh batch IDs and checks on the new composition. Preserve any
  real unresolved bisection barrier, but never diagnose an approval wait as a
  CI failure, halve W, or add it to the defect estimator. Late build/check
  results cannot revive discarded work. Use the existing cancellation ledger
  and cleanup paths; unsupported cancellation remains wasted CI. Cancellation
  is speculative-only today: depth-1 approval queues get no cancellation.
  Broadening cancellation is an explicit later design decision.
- Wake on a new stance/receipt observation, changed observed permissions or
  membership, `change_classified`, `ConfigApplied`, intent edit, or new revision.
  An ordinary poll,
  duplicate observation, elapsed time, or `refresh` alone does not rebuild an
  unchanged waiting composition. A permission-only change must still be
  discoverable while no batch is active: schedule read-only gate-input probes
  through reconciliation, without constructing test refs. Record changes only.
- Executor integration waits for PR 11a; the reducer and pure planner tests
  inject outcomes now. Exit requires zero new builds over repeated unchanged
  polls, progress behind a waiting first entry, precise suffix invalidation at
  depth 4, safe cancellation races, and full replay/snapshot equivalence.
  Name the no-`approvals` differential gate **legacy-plan-action-depths-1-4**:
  old/new plans and next actions are identical at both depths.
- Own the CI-versus-approval outcome contract here, while dormant, rather than
  leaving it to activation. `serve/policy.go:mergeResult` currently calls a
  definitely-false review condition terminal failure. Separate CI evidence from
  approval waiting while evaluating the full condition expression. Pin tests:
  CI passes + missing approval waits with no retry, timeout, bisection or
  dequeue; genuinely failed CI + missing approval performs real bounded retries;
  nested `all`/`any`/negation is preserved at landing; unknown eligibility never
  becomes an empty list. Keep CI timers, producer checks and same-commit retry
  limits. 11a only wires this already-tested contract into its shadow gate.

#### PR 9: Store all decision snapshots with landing start

- Scope: implement **Policy approvals / Approvals gate landing, not testing**,
  the atomic immutable snapshot, and **Changes and their identity / A landing
  settles before an update applies**. Keep the fence protocol unchanged.
- Config and loader: no additions. No new action type. `Land` stays byte-identical.
- Events: add `landing_decision_recorded`, one per member, keyed by batch and
  random attempt ID. Record change/revision/head, classification and its input
  evidence, policy digest, intent digest, requirements, eligible identities and
  the approval/receipt source records actually relied on, accountability, and
  tested/landed commit identities. PR-only evidence explicitly marks direct
  protection observations not applicable. Include native reviews' absent brief
  and intent bindings. Record expanded per-file owner obligations, including
  rename sides and receipt requirements, and which approval/receipt satisfied
  each. A shared approval is referenced by each obligation it satisfies.
  Add a full golden fixture and legacy absence tests.
- Store/executor: `Store.Emit` currently commits one event per call. Introduce
  a claim-bound, queue-locked landing-start operation that authorizes the
  existing action against the latest projection, validates all member decisions
  against their input basis, and appends the complete decision set **and the
  unchanged `LandingStarted` in one transaction**. Validate the **whole
  operation before inserting any decision**, under that same queue lock,
  including the last start authorization, claim and all existing `CanStart`
  checks. The current store `append` inserts sequentially and can return nil
  from its transaction on a stale start, then return `ErrStale` after committing
  earlier inserts. Merely passing several events to it is insufficient. Reject
  stale starts without any inserts or transactional side effects; authorization
  failure must roll back. Reject missing, duplicate, stale, extra, or mismatched
  members. Separate pre-start callback emissions
  cannot provide this guarantee. A single-event legacy start remains available
  only when no approval policy applies.
- Forge reads happen before that transaction. If a receipt, head, intent,
  permission observation, policy change, check reopen, or claim loss commits
  before the lock-protected start, re-plan and reject stale evidence. Once
  committed, no later update changes the snapshots or stops fence settlement.
  Do not hold a database transaction open across a forge request or git push.
  Remote changes not yet observed have the same read-to-push boundary as CI.
- Generated remedies remain an explicit decision variant for ADR 0005, naming
  suspect, evidence, and `on_red` instead of approvals. Do not implement remedy
  generation or post-merge mode here. Preserve distinct tested T and landed L
  for fast-forward today and for post-merge when it arrives; never claim L was
  tested when only T was tested. ADR 0008 later adds per-check producer/results
  through additive evidence, without changing this deployed event payload.
- Tests and exit: real Postgres races and fault injection prove all-or-nothing
  snapshots/start, lost leases, stale preflight, config/head/intent/check races,
  retry after rollback, crash after commit/before push, crash after push, and
  settlement under pause or removal. Name **last-start-stale**: make only the
  last start authorization stale; zero decision or start events commit and no
  push happens. Name **legacy-start-only**: without `approvals`, a start writes
  exactly the unchanged `LandingStarted` and nothing else. Pin the
  **generated-remedy-decision** variant. No snapshot may authorize H2 from H1.

#### PR 10: Add change and classification trailers before CI

- Scope: implement **Policy approvals / Commits carry what is known at build
  time**, preserving ADR 0004 attribution and exact tested commits.
- Config and loader: no additions. Events/actions: none. Build actions retain
  their existing wire bytes; the executor loads matching recorded classification
  under the pinned batch config and supplies internal build metadata. The
  runtime path activates with PR 11b, so legacy builds remain unchanged.
- Add `OpenMerge-Change` and `OpenMerge-Classification` to each squash/merge
  commit's single trailer block and to the last replayed commit of each change
  for rebase. Define a deterministic label ordering and change/revision value
  spelling with fixtures. Pin `OpenMerge-Change: <change-id>@<revision>` and
  `OpenMerge-Classification: <change-id>@<revision> <labels>` for every method.
  Labels are sorted lexically and comma-separated; the empty set is exactly
  `OpenMerge-Classification: <change-id>@<revision> none` (`none` is not a label).
  Use PR 4's canonical `owned:` values. Keep contributor sign-offs, coauthors,
  author dates,
  and replay provenance. Remove conflicting reserved input trailers before
  writing authoritative generated ones. Only the top commit has the final
  `OpenMerge-Batch` line accepted by `forge.ParseBatchMessage`.
- Fast-forward leaves original history and signatures untouched. Put one change
  trailer per member on the tested marker, immediately followed by that
  member's classification trailer in the same spelling. Keep member order
  deterministic and the sole batch trailer last. Never rewrite a tested commit to add approvals
  or a new intent. Metadata changes that affect build inputs require new work,
  not edits at landing. Gate and fence behavior stay unchanged.
- Tests and exit: real local git plus fake-forge contract tests for every method,
  several changes per batch, multiline bodies, adversarial reserved trailers,
  retained DCO/coauthors, and a receipt arriving while CI runs. Verify marker
  parent/tree and tested/landed identities as in the existing fast-forward path.
  Name **legacy-commit-id-per-method**: old and new binaries produce identical
  commit IDs for no-`approvals` squash, merge, rebase and fast-forward builds,
  not just identical message substrings.

#### PR 11a: Wire ingestion, commands, and a shadow gate

- Scope: integrate all Stage A components behind the opt-in, with the public
  loader **still rejecting** the new keys. Use candidate config only in isolated
  tests. This PR cannot enable policy in a deployed queue. No Stage B/C settings,
  `policy_authority`, `break_glass`, model or conflict-grace settings are accepted.
- `serve` resolves users/teams to stable IDs at the governing commit. Unresolved
  names pause affected queues through the existing config source and
  basis-checked recovery, never fall back to last valid policy. Revalidate
  mappings even at an unchanged commit so login reassignment changes the
  semantic digest. Governing owners/attributes content and classifier-version
  changes refresh evidence via `ConfigApplied`; formatting and unrelated queue
  edits do not. A base rewrite refreshes classifications and invalidates receipts.
- Ingest revision, intent, classification, accountability, native stance and
  eligibility records through deduplicated reconciler observations. Webhooks
  wake polling; they are not authority. Wire the whole-comment commands
  `@openmerge approve <brief-digest>` and `@openmerge unapprove`, with inclusive
  cursor, durable dedupe and App-self-comment exclusion. Use PR 3a's
  identity-bound authorization against the **comment author ID**, never the old
  bool-only `CanWrite(login)`. Atomically record command dedupe and receipt
  acceptance. Reject mismatched digests with supplied/current values explained;
  never rebind them. Edits to old comments create no new commands. `unapprove`
  binds the current revision at authenticated processing as Q4 specifies.
- Preserve exact-head fork trust even for `none`. Under `approvals`, supply
  `approved-reviews-by` from the shared eligible stance set in admission,
  reconciliation, retries and final preflight; otherwise retain today's native
  condition behavior. A review term in `enter_when` still prevents early testing.
  Emit PR 5's config diagnostic for review terms alongside `approvals`.
- Wire PR 8's already-tested CI-versus-approval outcome contract. The shadow
  gate evaluates full nested conditions and additive requirements against fresh
  stances, intent, membership and grants for every member. Grants must verify
  the review/comment **author ID** using PR 3a. Reuse pinned classification and
  source-to-revision attribution; revalidate supplied receipt digests. Shadow
  evaluation is test-only, never authority for a live legacy landing.
- In opt-in integration tests, unsatisfied gates record PR 8's rejection and
  skip unchanged waits. Successful gates call PR 9's fully validated atomic
  start once. Settlement remains first under pause, config errors or installation
  loss. Minimal waiting reason, reviewer brief link and digest are present in
  the existing check before activation.
- No new event/action types beyond the component PRs. Tests cover opt-in and
  legacy queues at depths 1 and 4, multiple bases/installations, identity swaps
  between review and grant reads, revoked permissions, changed owners, stale
  receipts, dropped webhooks, unreadable teams, and config changed mid-landing.
  Prove every opted-in gate uses identity-bound authorization and no unapproved
  composition lands. The loader/schema agreement still rejects approval keys.
- This wiring is inert for legacy queues but inherits PR 11's **Hold** deployment
  class. It stays off the live server until period B drains.

#### PR 11b: Publish the schema and enable the complete landing gate

- Make this a small final activation PR after 11a's integration gates pass.
  Publish PR 5's candidate schema, loader, key registry, generated schema command
  and `schema/openmerge.v1.json` together. Accept only
  `changes.pull_requests: required`; preserve docs-fence/schema agreement tests.
  Proposed examples remain in `text` fences until the loader accepts them.
- Enable the complete 11a gate only for queues with `approvals`; no production
  switch permits partial enforcement. This is the first production writer of
  Stage A evidence and its database-restore rollback boundary. Activation needs
  drained old workers and a pre-use backup, as `docs/operating.md` specifies.
- Tests and exit: schema/loader round trips and integrated no-bypass scenarios,
  plus **mixed-repository** acceptance with one opted-in queue and a legacy
  sibling: no new observations, digest changes or trailers in the sibling.
  Legacy `ConfigApplied` bytes and condition semantics remain identical.
- Merge behind the opt-in. Deploy only after period B drains; never add
  `approvals` to the dogfood queue until the trial pair closes.

#### PRs 12a–12b: Complete CLI/API reporting and Stage A acceptance

- Split: 12a owns CLI/API/check reporting; 12b owns the acceptance evidence
  and final operating guide. 12a can use 11a's isolated test wiring without
  publishing the schema; 12b requires 11b and outside-pushes #53.
- Scope: complete **Briefs**, **Consequences / Stage A exit**, and visibility of
  **Accountability** and **Policy approvals**. No config, event, action, or
  landing-gate changes. Public DTOs remain separate from queue wire structs.
- Extend existing queue status and PR lookup additively with change/revision,
  accountability, classification reasons, exact waiting gate and requirements,
  stance provenance, receipt validity, and landed commit/decision links. Old
  records report "not recorded". Do not infer decisions from commit trailers.
- Finish scoped reads at
  `GET /v1/queues/{owner}/{repo}/{base}/changes/{id}`,
  `.../changes/{id}/brief?audience=reviewer|operator|agent`, and
  `.../changes/{id}/decisions` with bounded pagination and revision selection.
  Resolve IDs only within the selected queue; preserve ambiguous-installation
  diagnostics and escape branch names as existing routes do. Decisions include
  attempt and source event references, including abandoned attempts distinctly
  from confirmed landings. Reads never fetch mutable forge facts.
- Finish `openmerge change show <id>` and
  `openmerge change brief <id> --audience reviewer|operator|agent`, with queue
  selectors, `--json`, and the existing status command's stable error/exit-code
  conventions. Keep `queue status --pr N` as a lookup alias. For Stage A, agents
  open PRs and queue them with the existing authenticated forge-comment commands;
  the CLI reads briefs and outcomes. `gh pr comment` provides the existing
  forge-authenticated command path in the CLI-only exit scenario.
  `change submit`, direct writes, and
  `change approve` remain Stage B after its authentication/authorization ADR.
  The read-only status bearer token must not become a user write credential.
- Update the one existing OpenMerge check, without a new comment channel, with
  the reviewer summary, brief digest, full brief link, incomplete-inventory
  warning, accountability where known, native-versus-receipt evidence, and
  actionable waiting reasons. Retain the bounded CI ledger and mode's honest
  tested/landed wording. Escape untrusted title/body/path text and bound summaries
  without omitting safety-relevant warnings. The API retains full evidence.
- MCP is later Phase 2/3 work: tools call these same versioned read projections
  and eventual authenticated command handlers. Do not add an MCP server or a
  second approval evaluator to Stage A.
- Tests and exit: CLI/HTTP/check golden responses, invalid and cross-queue IDs,
  auth isolation, pagination, size limits, rendering, and old clients/records.
  The fake-forge scenario drives an agent-opened PR to landed or definitively
  rejected using CLI reads and forge-authenticated commands: low-risk `none`,
  exact-head human review, receipt-required intent review, and read-back of
  every landing decision. PR 12b also covers an owned non-policy file waiting
  for its owner and an uncovered non-policy file that does not wait for owner
  approval, using ADR 0008's two-rule translation (a `paths: ["**"]` count rule
  plus a `touches: ["**"]` code-owner rule) so the count requirement still applies
  to the uncovered file.
  Run an organization sandbox acceptance for team owners and permission
  installation. The sandbox includes a real receipt
  posted with `gh pr comment <number> --body '@openmerge approve <brief-digest>'`,
  then reads back the accepted receipt and immutable landing decision through
  CLI/API. Save `stage-a-sandbox-evidence.md` as the reviewable acceptance
  artifact, with repository/PR, installed grants, posted comment URL/ID, commands,
  brief/receipt digests, revision, decision event IDs and landed/tested SHAs.
  Never require GitHub credentials in CI.
- Docs ownership: 12a updates `docs/api.md` for the routes, DTOs, pagination,
  native-versus-receipt provenance, known-empty versus unknown fields and legacy
  "not recorded" values. 12b completes `docs/operating.md` with drain before
  activation, backup before first `approvals` use, database-restore rollback,
  and `members: read` adoption (initial adoption text belongs to 3a).
  Both the operating guide and accountability API explanation carry the
  promised disclaimer: a token authenticates an account, and cannot prove a
  person rather than an agent holding the token acted. Recommend separate
  bot or App identities so an agent does not consume its human owner's approval.
- Stage A is **not complete until [PR #53](https://github.com/CanadaApollo6/openmerge/pull/53),
  `adr-0007/outside-pushes`, lands**, plus this sequence's acceptance gates.
  Conflict grace is explicitly excluded from Stage A acceptance. Delivered
  conflicts remove work as today, with no grace deadline or retry budget.
  Acceptance must report those limits and must not claim cancellation at depth 1.

#### Resolved questions

These maintainer decisions close Q1–Q7. Their owning PRs pin the choices in
component fixtures before integration; none waits for activation to decide.

1. **Q1, requirement syntax (PR 5, before its fixtures).** Accept `none`, scalar
   `any_writer` and the object union specified in PR 5. Count defaults to 1
   except for the separate 2026-10-18 code-owner maintainer decision:
   `{code_owners: true}` is a rule-only per-file requirement with optional
   `receipt: true`, no `count`, and is forbidden as `approvals.default`.
   `{human: true, count: 1}` is explicit, requires write permission, and combines
   with `group` and `receipt: true`. Writer never means human. Any writer's
   receipt uses `{any_writer: true, receipt: true}`; `receipt` is valid on every
   selector object and never on `none` or on its own.
2. **Q2, pinned base (PRs 4/11).** First observed queue-base tip is the revision's
   diff basis. Forward predecessor landings preserve classification and
   receipts. Rewrites/rewinds re-diff affected revisions and invalidate receipts
   even at unchanged heads. Governing semantic policy, owner/attribute content
   and classifier-version changes refresh evidence. See the dated
   maintainer-approved ADR clarification under Classification.
3. **Q3, conflict grace (PR 1 boundary, PR 12b acceptance).** Grace requires a
   separate later design and is explicitly excluded from Stage A acceptance.
   Current conflicts remove work via `TestRefBuildFailed`; no structured file
   report, grace deadline or bounded grace retries are delivered. Future grace
   must not renew its deadline across revisions or block following changes.
4. **Q4, stance ordering and recovery (PRs 3b/6/7a).** Withdrawals and requests
   for changes win ties and missing/ambiguous source times. An approval clears
   them only when provably newer; native observation order alone is insufficient.
   `unapprove` binds the current revision at authenticated processing, recorded
   explicitly. It cannot turn a late approval into a current one.
   PR 3b sources missing dismissal time from timeline dismissal events first.
   If unavailable, record an observation-time fallback, source, precision and
   uncertainty. Use a conservative upper time bound in the forge's clock
   (for example its response Date plus timestamp precision and clock allowance),
   never the dismissed review's old submission time. Pin the fallback barrier
   at its **first** observation; duplicate polls and re-observation must not
   advance it. Only a distinct newer blocking action can raise the barrier.
   The brief explains how to clear it. Re-observe the withdrawal and accept a fresh
   native review or receipt only with source time strictly after that bound and
   no newer blocking stance. Test this recovery at the same revision, including
   equal timestamps and restart. If no comparable time bound can be established,
   fail closed and direct the author to a new revision and a fresh review/receipt
   attributed to it. Old ambiguous sources remain historical; do not erase the
   withdrawal, relax the barrier on restart, or silently authorize it. This
   supplies recovery without an operator approval override.
5. **Q5, eligibility and conditions (PRs 5/6/8/11a).** No implicit extra pool.
   Emit a config diagnostic when `approvals` coexists with review terms in
   `enter_when` or `merge_when`, recommending explicit pools and policy rules.
   A positive review condition with a known empty eligible pool remains unmet;
   its known empty list retains normal negation semantics. Unresolved membership
   or eligibility is unknown, never an empty list that negation can turn into
   permission. Preserve the condition grammar and full nested expressions.
6. **Q6, fast-forward trailers (PR 10).** Use adjacent change/classification
   pairs on the marker keyed by `<change-id>@<revision>`, in the same spelling
   as other methods. Empty classification is exactly
   `OpenMerge-Classification: <change-id>@<revision> none`. Contributor heads
   remain untouched; the single `OpenMerge-Batch` trailer is last.
7. **Q7, semantic digest (PRs 4/5/7a/11).** Invalidate on semantic policy changes,
   CODEOWNERS/attributes content and classifier version. Record config blob ID
   as provenance only; formatting and other queues' sections do not invalidate.
   Preserve legacy digest bytes, including zero-valued queue config fields.
   See the dated maintainer-approved classification-inputs clarification.

#### Trial deployment

The live queue is in the [ADR 0009 dogfooding trial](../research/phase2-dogfood.md).
A change to batching, landing or check observation restarts **both** periods.
Merging and deploying have different gates: merge all Stage A work behind the
opt-in, with public keys rejected until 11b, but deploy only the inert class
below during the trial.

- Eligible for deployment: PRs **1–7, 10 and 12**, including their lettered
  splits, only if they are behavior-neutral for every queue without `approvals`.
  Each deployment needs the differential evidence below and a dogfood log entry.
- **Hold PRs 8, 9 and 11, and every split of them, off the live server until
  period B drains**, even when their new paths are dormant. Never add `approvals`
  to the dogfood queue until the pair closes.
- Do not change the live App's permissions mid-trial, including 3a's
  `members: read`. Permission adoption and any new opt-in tests use an isolated
  sandbox installation. Keep the live App's current `actions: write` status.
- A permitted dependent PR does not permit its held prerequisites to ship.
  If main already contains held PRs, pin or build a release branch containing
  only deployable commits and their deployable dependencies, or defer the
  upgrade. Reporting and acceptance work in 12 cannot include held 9/11 code
  in a live binary. Separate code dependency from release eligibility.

Each deployable PR (and each split) attaches a reviewable evidence artifact
identifying old/new binary SHAs, exported journal identity/hash, live config,
commands and results. Require all of the following before logging it inert:

1. Zero diff in all legacy wire fixtures.
2. Differential replay of an exported journal: old and new binaries produce
   identical next actions at **every revision**, not only the final state.
3. Identical legacy simulator results with the same seeds and inputs.
4. Unchanged live effective config digest, with no new event types or
   `ConfigApplied` on shadow replay. The replay is offline and writes nothing
   to the live journal.
5. For PR 10, identical commit IDs per merge method (squash, merge, rebase,
   fast-forward), using identical build inputs.

The [dogfood log](../research/phase2-dogfood.md#log) records each upgrade's SHA,
evidence artifact, deployment class and pre-metrics maintainer classification.
If any differential gate fails, the PR is not an inert deployment; defer it
until the trial closes or explicitly restart both periods. Passing these gates
does not waive the hold on 8, 9 or 11.

#### Risks

- Approval policy can weaken accidentally through incomplete inventories,
  mistaken `CODEOWNERS` dialects, `unknown` handling, or mixed condition/policy
  counts. Treat those as negative safety cases, not only matching examples.
- Identity and membership reads add rate pressure and failure modes across
  bases. Cache immutable content and dedupe observations, but revalidate grants
  at the gate and keep settlement's budget. A read failure must never reuse an
  old grant as fresh evidence.
- Polling cannot atomically observe every remote edit with a git push. Record
  exactly which reads authorized the durable start; preserve the fence's
  settlement guarantee instead of claiming continuous review authorization.
- Incorrect wait clearing can either starve a change or repeatedly spend CI.
  Persist fingerprints and observation bases, probe idle waits, and test every
  selection path and speculative suffix, including a late builder response.
- Inventory/hunk and decision records increase journal size and can contain
  private source. Bound observations, authenticate reads, sanitize rendering,
  and leave retention/compaction disabled until audit and ledger preservation
  are designed together. ADR 0008 audit export needs the original evidence.
- Accepting config before enforcement or activating only part of a batch gate
  creates a policy bypass. PR 11b is the only activation point; older workers
  must be drained. An incompatible rollback requires restoring the database.
- A dormant change can still alter the live ADR 0009 trial. Follow
  [Trial deployment](#trial-deployment): hold 8/9/11 and their splits until
  period B drains, retain the no-`approvals` differential artifacts, and log
  every permitted upgrade. A batching/landing/check-observation change restarts
  both periods; never activate approvals or change live App permissions mid-pair.
- PR 11 overlaps the landing gate used by ADR 0005 and policy authority work.
  Keep strict/fast-forward fence forms unchanged, expose explicit decision
  variants, and coordinate additive evidence formats before parallel work lands.

#### CI and simulator impact

This stage does not reduce the checks needed for a tested composition. Approval
reads and classification are control-plane work, not new CI runs. A rejected
batch and its dependent speculative suffix waste their already-started runs;
remaining members need new compositions and tests. At depth W, a rejection at
chain position k can discard up to W − k + 1 admitted batches. Cancellation
reduces runner-minutes only where supported and only on speculative paths
currently; depth-1 approval queues receive none. Broadening it needs a later
explicit decision. While approval inputs stay equal,
waiting changes cause **zero additional builds or tests**. Repeated approval
input changes can cause repeated speculation; the ADR provides no total CI-spend
cap, and this plan does not invent one.

Extend `internal/sim` to drive the real planner with review/receipt arrivals,
intent edits, membership/permission changes, policy updates, finite runners,
and cancellations both supported and unsupported. Add small deterministic
safety scenarios at depths 1 and 4 to `make check`, plus all-waiting queues,
multiple pending bisection groups, and policy changes during an unsettled attempt.
Assert approved snapshots for every opted-in landing, no self-approval, exact
composition evidence, no wait-as-defect accounting, and no unchanged-input
retest churn. Preserve all legacy simulator results when approvals are absent.

Planning allowance is roughly 20–30 focused unit/contract/race scenario groups
across the sequence, plus a small seeded approval workload in `make check`.
Target under 10% extra short-suite runtime, to be measured against the same
checkout/toolchain before activation; this is a budget, not a claimed result.
Keep the existing required `sim-exits` Team/Slow profiles and thresholds intact.
Add an approval-aware paired profile beside them, reporting review delay
separately from CI/queue delay, runs and runner-minutes per landed change,
invalidated compositions, and rebuilds per unchanged approval fingerprint.
Do not apply the old latency target to time deliberately spent waiting for a
reviewer. Full eight-seed, 30-day approval/adaptive combinations belong in the
existing non-required full-simulation workflow until runtime is measured.

## Consequences

- Repositories that change nothing see nothing change: `required` is the default, queues without an `approvals` section keep today's rules, and pull requests keep their current flow.
- Phase 3 is reframed around changes. Stage A ships first and works entirely on pull requests; Stages B and C follow only once their prerequisites exist.
- Phase 3's exit tests, all on the fake forge:
  - *Stage A:* an agent opens a pull request and takes it to landed or definitively rejected through the CLI or MCP. A low-risk change lands on policy alone. A change that needs a human waits until a scripted reviewer approves its exact head. Every landing's decision snapshot can be read back, with brief digests for receipt approvals.
  - *Stage B:* the same, starting from `openmerge change submit` with no pull request, and refusing to land when the base's protection requires a pull request.
- Adopting approval policy means moving approval terms out of `enter_when` and `merge_when` into `approvals`. Adding an `approvals` section also changes what `approved-reviews-by` counts: authors, submitters and accountable humans stop counting; conditions keep expressing everything else. Until a queue does, its condition-based review requirements apply in full, and an approval term in `enter_when` also prevents testing before approval. Stage A's gain is risk-based approval and an audit trail; removing the branch-and-pull-request ceremony is Stage B.
- OpenMerge becomes the enforcer of review policy for queues that use direct submission. The policy engine, owner resolution and approval counting need the same simulator and contract testing as landing.
- The forge driver grows: reading diffs against a base, reading review stances (requests for changes and dismissals, which the driver discards today), reporting status on a bare commit, opening a pull request for a change, resolving team membership for owner rules, and verifying a user's identity. Each gets a fake-forge contract test. Other forges also center on pull or merge requests, so this does not make Phase 5 easier, but it keeps those operations behind the driver.
- Setup grows: Stage A needs organization members read to resolve team owners and groups. In Stage B, `setup rulesets` must allow writers to push `openmerge/submit/**` while keeping queue and fence refs App-only; the App already has pull request write permission for opening aliases.
- Landing without a pull request gives things up: integrations keyed by pull requests, release notes built from them, and `#N` links in history. Briefs and change IDs replace some of that, not all; teams choose per queue.
- Every landed change records its accountable human where one is known, and says "not recorded" where it is not.

## Alternatives considered

- **Keep pull requests as the only unit.** Simplest, and the default stays exactly that. Stage A's policy approvals already work on pull requests; keeping them as the only unit would keep a mandatory pull request as the way every change is submitted and integrated, even for agents.
- **Drop pull requests entirely.** It would remove the discussion, notifications and integrations teams depend on, and make OpenMerge hard to adopt inside an existing GitHub organization.
- **Let a model decide approvals.** Fast, but not auditable or reproducible, and open to text in the change steering the decision. Classification stays deterministic and conservative; a model may only write prose a human reads.
- **One ADR, shipped at once.** The first draft did this. Authentication, PR-less review and model integration each need their own design; staging lets the parts that fit today's trust model ship first.
- **Build a separate review tool.** Anthropic built one (Antique) alongside a new version control system. Briefs stop well short of that: they are views over evidence OpenMerge already holds, shown where review already happens.
