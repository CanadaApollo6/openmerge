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
