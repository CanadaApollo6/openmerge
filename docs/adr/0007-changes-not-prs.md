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

**A landing settles before an update applies.** Once a landing attempt starts for revision H1, that attempt is settled through the fence exactly as today, whatever happens to the change meanwhile. A later revision H2 is never reported as landed by H1's attempt. This keeps the settlement guarantees of [ADR 0004](0004-forge-driver-interface.md) and ADR 0006.

**Storage stays additive.** Every deployed event and action encoding, and every golden fixture, stays byte-for-byte unchanged ([ADR 0002](0002-storage-and-state-model.md)). Batches made only of pull requests keep their existing entry, batch and action types; new facts about them (classification, receipts, decision snapshots) arrive as new event types. A batch that contains any direct submission uses new change-keyed types for every member, keeping pull request numbers only as aliases for reporting. Both forms share one planner and one fence. No pull request number is ever invented. Records written before this ADR have no accountability information and show it as "not recorded". Rolling back across these additions follows ADR 0002: once new event or action types are written, an older binary needs a compatible database restore, not just a config change.

### Classification (Stage A)

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

### Briefs (Stage A, prose in Stage C)

**A brief is a template over recorded facts, with an optional labelled summary.** For each audience:

- *Reviewer:* the pinned head and base; the stated intent, shown as the submitter's claim, not a fact; the inventory of changed files and hunks from the recorded diff, labelled incomplete when the diff is truncated or unreadable; the labels and why each applies; the policy requirements and who can meet them; and links to test evidence.
- *Operator:* where the change is in the queue, what blocks it, and what clears each block.
- *Agent:* structured next steps (fix this check, rebase onto this commit, wait for this approval) as stable JSON.

Briefs are available through the CLI and API, and through MCP when it ships. When a pull request exists, its OpenMerge check shows a summary of the reviewer brief, its digest and a link to the whole brief. A brief's digest covers the change and revision identifiers, the head, the diff endpoints and merge base, the inventory and whether it is complete, the labels and their reasons, the intent digest, the policy digest and the requirements. It excludes queue position, approval progress, timestamps, test links and generated prose. The reviewer supplies that digest to give a receipt; a native review records none, and the brief says so.

**Generated prose is never evidence.** In Stage C, an operator may configure a model to add a summary to briefs. It appears in a delimited section labelled as generated, is never read by policy or the planner, and is off by default; choosing a provider means choosing where private diffs are sent. Because the factual inventory does not depend on it, text planted in a diff or an intent to steer the summary ("this is a trivial change") cannot hide what the change touches. This extends principle 1, "never lie about what was tested", to review: OpenMerge never presents a summary as if someone reviewed the code.

### Implementation

Stage A lands in the PRs below. This section plans the accepted decision; it
does not enable Stages B or C. **Pushes outside the queue is already in flight
in a separate implementation.** It has no work items in this sequence. ADR 0008
must wait for both that work and Stage A before enabling policy authority.

#### Boundaries and compatibility

The current integration points are `internal/queue/{types,apply,plan,wire,snapshot}.go`,
`internal/config`, `internal/serve/{reconcile,policy,worker,command}.go`,
`internal/executor`, `internal/store`, `internal/forge`, `internal/gitops`,
`internal/status`, and `cmd/openmerge`. Keep classification and approval evaluation as pure functions
over recorded inputs. `serve` performs reads and identity resolution, the queue
fold owns durable state, and the executor performs effects through the forge
interfaces. Neither the planner nor the condition evaluator performs I/O.

PR numbers below identify implementation steps, not GitHub issue numbers. Each
step targets about 1–2.5k changed lines, including tests and fixtures, with a
3k review ceiling. Split a step at its stated boundary if it exceeds that
ceiling; do not combine independent steps to reduce the PR count. The estimates
are planning budgets, not measurements.

| PR | Scope | Depends on | Parallel work |
|---|---|---|---|
| 1 | Change identity and revision projection | None | Independent of 2, 3, and 5 |
| 2 | Pinned diff and governing-file reads | None | Independent of 1, 3, and 5 |
| 3 | Forge identities, review stances, and membership | None | Independent of 1, 2, and 5 |
| 4 | Classification and owner evaluation | 2 | Parallel with 6 once its prerequisites land |
| 5 | Approval schema design and pure policy evaluation | None | Independent of 1, 2, and 3 |
| 6 | Durable stances and accountability | 1, 3, 5 | Parallel with 4 |
| 7 | Briefs, digest, and receipt validation | 1, 4, 5, 6 | Parallel with 8 and 9 |
| 8 | Waiting projection and batch selection | 1, 5, 6 | Parallel with 7, 9, and 10 |
| 9 | Atomic landing decision storage | 1, 5, 6 | Parallel with 7, 8, and 10 |
| 10 | Build-time commit trailers | 1, 4 | Parallel with 6–9 |
| 11 | Runtime activation and landing policy gate | 1–10 | Integration point |
| 12 | Complete CLI, API, check reporting, and exit scenarios | 11 | Stage A acceptance |

PRs 1–10 provide tested components without accepting unenforced approval
configuration. The public loader and published schema continue to reject the
new approval keys until PR 11 connects every enforcement path. Proposed schema
fixtures in PR 5 are separate from the published schema. There is no production
flag that permits partial enforcement. PR 7 can expose read-only briefs over
recorded evidence, including explicit missing evidence for legacy records.

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
fixtures in `internal/queue/testdata/wire/v1`. Existing event/action encodings,
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

Until PR 11, no existing queue starts writing Stage A types. From PR 11 onward,
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
  No new actions. Runtime emission waits for PR 11.
- Planner and executor: preserve `HeadChanged` dequeue and work invalidation.
  A landing on H1 settles for H1 even if H2 is observed meanwhile. Do not add
  direct-submission refs or alter trust. Reserve the conflict-grace integration
  point described under Open questions rather than claiming it exists today.
- Tests and exit: deterministic replay across restart and repeated enqueue,
  H1/H2/H1, stale updates, PR closure, multi-base isolation, and a head change
  during an unsettled landing. Existing wire bytes and legacy plans are equal.

#### PR 2: Read pinned diff inputs through the forge boundary

- Scope: implement the observation inputs in **Classification (Stage A)** and
  the file/hunk inventory in **Briefs (Stage A, prose in Stage C)**. This PR is
  independent of identity and approval policy.
- Config, events, actions, and landing gate: no changes. Add read capabilities
  and fake-forge contract cases only; no new runtime records or decisions.
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

#### PR 3: Observe forge users, teams, and native stances

- Scope: implement the driver prerequisites in **Configuration / Identities**,
  **Policy approvals (Stage A) / Approvals are head-bound**, and
  **Accountability**, plus the Stage A permission in **Consequences**.
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
- Setup: request **organization members read** (`members: read`) for team
  resolution, and inspect the installed grant, not just the App manifest.
  Document that existing Apps and installations must accept the addition.
  Missing permission does not stop unrelated personal/user-only queues.
  Named configuration identities that cannot resolve will pause affected
  queues in PR 11; an unreadable effective owner requirement blocks its change.
- Planner, executor, and gate: no behavior change yet. Keep these reads under
  ADR 0006's installation budgets, preserving settlement's reserved capacity.
- Tests and exit: fake contracts and GitHub fixtures cover renamed/reassigned
  logins, stable IDs, nested/inherited team membership as the forge exposes it,
  inaccessible teams, missing grants, pagination, duplicate/out-of-order
  reviews, and dismissed reviews. No user token or new forge write is needed.

#### PR 4: Classify revisions and compute effective owners

- Scope: implement **Classification (Stage A)** and **Policy approvals / Path
  ownership comes only from CODEOWNERS** as pure evaluation over PR 2 inputs.
- Config and loader: no public keys yet. Accept typed classification settings
  in component tests; PR 11 publishes `classification` with PR 5's schema.
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
  owner diagnostics. Resolve effective owners per policy path, including both
  sides of renames. A group owns no paths. An uncovered path, email-only owner,
  or unreadable team cannot satisfy that path's mandatory owner requirement.
- Mark `policy` for all four accepted config paths from `config.Find`, every
  forge-recognized `CODEOWNERS` location, all `.gitattributes` files, CI workflow
  definitions, and configured sensitive paths. Forged PR labels never affect
  classification. Binary files, modes, symlinks, submodules, unpaired renames,
  overflow, and failed reads add `unknown`. Retain known facts alongside it.
  Incomplete inventory makes policy ownership unresolvable even if no observed
  path is sensitive. Do not add comment-only detection.
- Events: add `change_classified`, including input fingerprint, classifier
  version, inventory, and explanations, with golden fixtures for complete and
  incomplete observations. No new actions. Runtime emission waits for PR 11.
- Planner and executor: expose invalidation inputs for `ConfigApplied`; never
  re-diff in the landing gate. Respect the pinned-base question below.
- Tests and exit: a classification matrix covers every uncertainty, attribute
  override, owner precedence, path dialect, deleted/renamed policy path, and
  a PR trying to weaken its own config or owners. Repeated inputs yield equal
  classifications and hashes. Snapshots preserve the current evidence.

#### PR 5: Define approval config and evaluate requirements

- Scope: implement **Configuration / Matching, Opt-in, Identities** and
  **Policy approvals / Policy adds requirements, An unknown match never lowers
  a requirement, Path ownership, The policy digest** as pure policy code.
  This PR is independent and can be developed beside PRs 1–3.
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
  Stage B/C values. The accepted example fixes `none`, `any_writer`, and
  `{group, count}` requirements, with optional `receipt: true` on a requirement
  object. Resolve the remaining requirement syntax question below before
  publishing the schema. Parsing performs no forge calls. Validate that
  accountable IDs identify humans after resolution; do not demand that a PR
  author or its accountable humans have write permission.
- Do not make `config.Load` accept these keys yet. Keep a candidate schema and
  parser/evaluator agreement fixtures until PR 11 installs the public loader,
  key registry, generated schema command, and `schema/openmerge.v1.json`
  together. Existing accepted configs and digests remain identical meanwhile.
- Evaluation: AND the facets within each rule, accumulate every true or
  unknown rule's requirements separately, and apply the default when no rule
  definitely matches. `none` helps only on a definite match and cancels nothing.
  One ID can satisfy several eligible requirements, once per requirement.
  Always add per-file policy owners for opted-in queues. Return explainable
  unmet/unresolvable requirements rather than a single approval count.
- Digest: canonicalize settings, defaults, groups, agents, resolved user/team
  IDs, governing owners and attributes content, and classifier version into
  one policy digest. Fold it into the existing effective queue config digest.
  No separate batch invalidation generation. Keep semantic policy independent
  of unrelated queues and commits, subject to the config-blob conflict under
  Open questions. Membership and
  current write grants are fresh eligibility observations, not static policy.
- Events/actions: none; reuse unchanged `ConfigApplied` at activation. Planner
  and executor consume the same pure evaluator; no gate changes yet.
- Tests and exit: truth tables for true/false/unknown facets and overlapping
  rules, default fallback, receipt counts, owner unions, canonicalization,
  identity reassignment, unchanged legacy digests, and invalid config cases.

#### PR 6: Fold stances and record accountability

- Scope: implement **Policy approvals / Approvals are head-bound; receipts
  also bind intent** except receipt command handling, and **Accountability**.
- Config and loader: no new keys beyond PR 5's proposal; no public activation.
- Events: add `change_accountability_observed`, `change_intent_observed`,
  `review_stance_observed`, and `approval_eligibility_observed`. Include stable
  IDs and display logins, change/revision/head, source IDs and source times,
  read basis, and known/unknown eligibility with reasons as appropriate.
  Intent records pin normalized title and body and their digest. Add golden
  fixtures for person, mapped agent, unmapped bot, approval, changes requested,
  dismissal, withdrawal, and unknown eligibility. No new actions.
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
  landing re-reads wait for PR 11. Revision changes clear current trust/review
  facts; intent changes clear receipt eligibility without discarding tests.
- Tests and exit: permutations across channels, equal/missing source times,
  dismissals, duplicate polling, removed team members, revoked writers, self
  and accountable approvals, ID dedupe across aliases, and snapshot replay.
  Pin the ambiguous cases identified under Open questions before activation.

#### PR 7: Build factual briefs and validate receipt bindings

- Scope: implement **Briefs (Stage A, prose in Stage C)**, **Policy approvals /
  What intent means**, and the receipt digest portion of **Approvals are
  head-bound; receipts also bind intent**.
- Config and loader: no additions. No model settings or generated prose.
- Brief: provide one recorded-fact model with reviewer, operator, and agent
  renderers. Include pinned inputs, the author's claimed intent, complete or
  explicitly incomplete file/hunk inventory, label reasons, requirements and
  eligible pools, and test links. Operator output explains each blocker and
  what clears it. Agent output uses stable reason codes and structured next
  steps rather than prose parsing. Missing historical facts say "not recorded".
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
  Add golden fixtures, including rejected-binding tests. No new actions or
  actual commands yet. Validate the supplied digest against current evidence
  at acceptance and again at landing; never replace it with a current digest.
- Expose a read-only reviewer brief endpoint and CLI view over recorded facts
  so PR 11's receipt command is usable as soon as activated. Use the scoped
  route and command shapes specified in PR 12, with authenticated reads under
  the current status listener. No write bearer-token authentication is added.
- Planner and executor: no testing gate. Native reviews remain head-only and
  visibly lack receipt bindings. Tests prove intent edits invalidate receipts
  but leave tests valid; changing each included digest field changes the hash,
  and changing each excluded field does not. Rendered and JSON fixtures agree.

#### PR 8: Skip approval waits without rebuilding them

- Scope: implement **Policy approvals / Approvals gate landing, not testing**,
  the waiting and batching behavior, and ADR 0009 **Interaction with other
  decisions / Approvals**. Public config still stays disabled until PR 11.
- Config and loader: no additions. No new action type is needed.
- Events: add `approval_gate_rejected` with batch/composition, per-member
  change/revision, approval-input fingerprint, and stable waiting reasons;
  add `approval_inputs_changed` to wake a wait only on a changed fingerprint
  with a current observation basis. Golden fixtures cover multi-member failure,
  stale wake, and unknown eligibility. Existing stance, intent, revision, and
  `ConfigApplied` events also invalidate the appropriate wait basis.
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
  and cleanup paths; unsupported cancellation remains wasted CI.
- Wake on a new stance/receipt observation, changed observed permissions or
  membership, `ConfigApplied`, intent edit, or new revision. An ordinary poll,
  duplicate observation, elapsed time, or `refresh` alone does not rebuild an
  unchanged waiting composition. A permission-only change must still be
  discoverable while no batch is active: schedule read-only gate-input probes
  through reconciliation, without constructing test refs. Record changes only.
- Executor integration waits for PR 11; the reducer and pure planner tests
  inject outcomes now. Exit requires zero new builds over repeated unchanged
  polls, progress behind a waiting first entry, precise suffix invalidation at
  depth 4, safe cancellation races, and full replay/snapshot equivalence.

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
  and intent bindings. Add a full golden fixture and legacy absence tests.
- Store/executor: `Store.Emit` currently commits one event per call. Introduce
  a claim-bound, queue-locked landing-start operation that authorizes the
  existing action against the latest projection, validates all member decisions
  against their input basis, and appends the complete decision set **and the
  unchanged `LandingStarted` in one transaction**. Reject missing, duplicate,
  stale, extra, or mismatched members. Separate pre-start callback emissions
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
  settlement under pause or removal. No snapshot may authorize H2 from H1.

#### PR 10: Add change and classification trailers before CI

- Scope: implement **Policy approvals / Commits carry what is known at build
  time**, preserving ADR 0004 attribution and exact tested commits.
- Config and loader: no additions. Events/actions: none. Build actions retain
  their existing wire bytes; the executor loads matching recorded classification
  under the pinned batch config and supplies internal build metadata. The
  runtime path activates with PR 11, so legacy builds remain unchanged.
- Add `OpenMerge-Change` and `OpenMerge-Classification` to each squash/merge
  commit's single trailer block and to the last replayed commit of each change
  for rebase. Define a deterministic label ordering and change/revision value
  spelling with fixtures. Keep contributor sign-offs, coauthors, author dates,
  and replay provenance. Remove conflicting reserved input trailers before
  writing authoritative generated ones. Only the top commit has the final
  `OpenMerge-Batch` line accepted by `forge.ParseBatchMessage`.
- Fast-forward leaves original history and signatures untouched. Put one change
  trailer per member on the tested marker; resolve its classification-marker
  spelling under Open questions. Never rewrite a tested commit to add approvals
  or a new intent. Metadata changes that affect build inputs require new work,
  not edits at landing. Gate and fence behavior stay unchanged.
- Tests and exit: real local git plus fake-forge contract tests for every method,
  several changes per batch, multiline bodies, adversarial reserved trailers,
  retained DCO/coauthors, and a receipt arriving while CI runs. Verify marker
  parent/tree and tested/landed identities as in the existing fast-forward path.

#### PR 11: Activate configuration, observations, and the landing gate

- Scope: connect the preceding components for all Stage A portions of
  **Configuration**, **Changes and their identity**, **Classification**,
  **Policy approvals**, **Accountability**, and **Briefs**. Resolve the blocking
  Open questions below before this PR. No Stages B/C settings are accepted.
- Config: publish the PR 5 schema and loader together. Stage A only accepts
  `changes.pull_requests: required`. No `policy_authority`, `break_glass`,
  direct-submission, model, or new conflict-grace settings enter this PR.
  Preserve the existing schema/loader docs-fence agreement tests. Keep proposed
  config fragments in `text` fences until the public loader accepts them.
- `serve` resolves declared users/teams to stable IDs while assembling effective
  config at the governing commit. Unresolved names pause affected queues through
  the existing config source and basis-checked recovery, never fall back to the
  last valid approval policy. Revalidate mappings even at an unchanged commit
  so login reuse changes the policy digest. Re-read membership/write grants at
  each landing and in waiting probes. Governing owner/attribute content and
  classifier-version changes flow through the policy component of
  `ConfigApplied`, invalidating all not-yet-landing batches.
- Ingest change/revision, intent, classification, accountability, native stance,
  and eligibility records through deduplicated reconciler observations. Webhooks
  wake polling; they are not authority. Add the whole-comment commands
  `@openmerge approve <brief-digest>` and `@openmerge unapprove` with the current
  writer authorization, inclusive comment cursor, durable receipt dedupe, and
  App-self-comment exclusion. A mismatch is rejected with the supplied and
  current digest explained, never rebound. Receipt acceptance and command
  dedupe commit atomically. Edits to old comments do not create new commands.
- Keep exact-head fork trust independent of policy, even for `require: none`.
  Approval rules never weaken `enter_when` or `merge_when`. Under `approvals`,
  supply `approved-reviews-by` from the shared eligible stance set in admission,
  reconciliation, retries, and final preflight. Without `approvals`, preserve
  today's raw native-review condition behavior. A review term in `enter_when`
  still prevents early testing; document moving such terms into `approvals`.
- Separate CI failure evidence from unmet approval policy in the current
  `CheckPolicy`/`BeforeLand` path. An approval wait is not `ChecksFailed`, a
  failed rerun, a timeout, a bisection trigger, or a terminal dequeue. Evaluate
  the full existing condition expression at landing, including nested logical
  groups; do not strip review terms mechanically. CI observations must retain
  their own timers, producer checks, and same-commit retry decisions.
- After fresh checks and trust, re-read stances, intent, membership, and grants
  for every batch member. Reuse recorded classification, evaluate all additive
  requirements, revalidate receipt digests, then call PR 9's atomic start.
  On an unsatisfied gate record PR 8's rejection and skip waits. On success
  freeze every decision and start once; settlement remains first under every
  pause, config error, or installation loss. Provide a minimal waiting reason,
  reviewer brief link, and digest in the existing check from activation onward.
- No new event/action types beyond PRs 1, 4, 6–9. This is their first production
  writer and the rollback boundary described above. Tests and exit: opt-in and
  legacy end-to-end queues at depths 1 and 4, multiple bases and installations,
  revoked permissions, changed owners, stale receipts, dropped webhooks,
  unreadable teams, and config changed mid-landing. Prove no eligible policy
  bypass and no unapproved composition lands.

#### PR 12: Complete CLI/API reporting and Stage A acceptance

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
  every landing decision. Run an organization sandbox acceptance for team
  owners and permission installation; never require GitHub credentials in CI.

#### Open questions

These are gaps or tensions to resolve explicitly, not changes to accepted
requirements. Blocking items must be settled before PR 11 publishes the schema
or starts writing evidence. Component tests should pin the resolution.

1. **Requirement syntax and human-only approval (PR 5, blocking).** The example
   fixes `any_writer`, `none`, and `{group, count}` but does not enumerate the
   requirement union, count defaults, or how a rule requires a human for an
   unmapped bot. Recommend accepting those fixed forms, defaulting object count
   to 1, and adding an explicit human-kind requirement form rather than treating
   every writer as human. Specify where `receipt: true` is valid, including a
   receipt from any writer, before freezing schema fixtures.
2. **Classification's pinned base (PRs 4/11, blocking).** "Once per revision"
   pins a base tip, while "a change to any of these inputs" could include that
   tip or merge base. Speculative compositions also use predecessor landing
   commits. Recommend a revision's first observed queue-base tip as its diff
   basis, separate from speculative build bases; ordinary predecessor landings
   do not reclassify the same revision. Governing config/owners/attributes and
   classifier-version changes explicitly refresh its evidence. Confirm whether
   other base movement requires reclassification, and receipt invalidation,
   before implementing that case. Continue invalidating stale test compositions.
3. **Conflict grace is described but not implemented (PR 1 boundary).** Today's
   `TestRefBuildFailed` conflict path removes work and records no structured
   file report, retry budget, or grace deadline. ADR 0005 supplies no concrete
   config schema for those bounds. Recommend a separate bounded conflict-grace
   PR/design before advertising that exception; Stage A identity must neither
   renew a future deadline on revision updates nor block following changes.
   Do not silently introduce grace configuration in this plan's activation PR.
4. **Source times and mutated native reviews (PRs 3/6, blocking).** Reviews have
   review IDs and submission times, but a dismissal can change a review with
   the same ID, and a polling response may lack the dismissal's own time. The
   decision does not define ties or a missing time for cross-channel clearing.
   Recommend conservative withdrawal precedence until a later approval has
   provably newer source time; retain dismissal identity/time when available.
   Also pin how `unapprove`, which has no revision argument, binds if its comment
   is first processed after a push. Recommend current revision at authenticated
   processing, recorded explicitly, without letting a late approval bind there.
5. **Unknown or no applicable reviewer pool (PRs 5/6/11, blocking).** The ADR
   defines eligibility by applicable requirements; a definite `none` rule may
   leave no approval pool while `merge_when` still contains a review condition.
   It also does not say how incomplete membership affects negative conditions.
   Recommend no implicit extra pool: an uncountable review condition stays
   unmet, with a diagnostic recommending an explicit pool. Unknown eligibility
   supplies an unknown condition attribute, never a known empty list that
   negation could turn into permission. Keep the grammar unchanged.
6. **Fast-forward classification trailers (PR 10, blocking for that method).**
   The decision explicitly puts per-change trailers on its marker but does not
   specify where each member's classification goes there. Recommend paired
   change/classification records on the marker, keyed by change ID, keeping
   contributor heads untouched and the sole batch trailer last. Confirm the
   value format before golden commit fixtures become a contract.
7. **Config blob identity versus semantic digest (PRs 4/5/11, blocking).**
   Classification records the governing config object's ID and invalidates on
   input changes. ADR 0002's effective digest ignores formatting and unrelated
   queues. Editing either changes the config blob while leaving semantic policy
   equal, so existing `ConfigApplied` behavior cannot supply both promises.
   Recommend confirming an opt-in Stage A amendment that includes the governing
   config blob in its policy digest, accepting extra invalidation on those edits
   for approval-enabled queues. Alternatively narrow the classification input
   rule to semantic config changes. Keep legacy digests untouched either way;
   do not silently choose an interpretation in implementation.

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
  creates a policy bypass. PR 11 is the only activation point; older workers
  must be drained. An incompatible rollback requires restoring the database.
- PR 11 overlaps the landing gate used by ADR 0005 and policy authority work.
  Keep strict/fast-forward fence forms unchanged, expose explicit decision
  variants, and coordinate additive evidence formats before parallel work lands.

#### CI and simulator impact

This stage does not reduce the checks needed for a tested composition. Approval
reads and classification are control-plane work, not new CI runs. A rejected
batch and its dependent speculative suffix waste their already-started runs;
remaining members need new compositions and tests. At depth W, a rejection at
chain position k can discard up to W − k + 1 admitted batches. Cancellation
reduces runner-minutes only where supported. While approval inputs stay equal,
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
