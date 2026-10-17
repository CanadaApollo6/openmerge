# 0007. Make the change the unit of work, with pull requests as one optional view

- **Status:** Proposed
- **Date:** 2026-10-16

## Context

OpenMerge's queue has always been about changes more than pull requests. To decide whether to land something it needs a head commit, who may land it, what was tested, and whether policy allows it. On GitHub, the pull request is how those facts arrive.

A pull request is a proposal to merge some commits into a branch, with a discussion attached. Git has never required one: anyone can merge locally and push the result to the base, and branch protection is the only thing that stops them. OpenMerge itself lands this way. It composes commits onto the base in its own git cache, has CI test the result, and pushes the base in one atomic push leased on the fence ([ADR 0004](0004-forge-driver-interface.md)). What it adds over a person doing the same by hand is the evidence: the pushed commit is the tested one, landings are serialized, and the log records who allowed it.

With coding agents, two costs of the pull request model grow:

- **Every change gets the same review.** A typo fix and an authentication rewrite wait for the same human. Anthropic reports that its reviewers now "examine the intent of the change as opposed to reading every line of code", backed by approval rules that tell a comment-only change from a sensitive one ([Redesigning source control at Anthropic](https://www.anthropic.com/engineering/redesigning-source-control)).
- **Every reader gets the same page.** A reviewer, an operator and an agent fixing a conflict need different information, and the pull request page serves none of them well at volume.

Opening a pull request is cheap for an agent. Waiting for a human and reading a page built for line-by-line review are not. The win this ADR targets is therefore specific: low-risk changes from trusted authors land on policy without waiting for a person, human attention goes to intent where it matters, and agents can submit work without the ceremony of a branch and a pull request. It does not change CI cost or queue throughput; those are Phase 2 and [ADR 0005](0005-verification-modes.md).

## Decision

The decision has three stages. Each is usable on its own, and each later stage depends on the one before it.

| Stage | Adds | Needs before it ships |
|---|---|---|
| A | Change identity, classification, policy approvals and briefs, all on pull requests | This ADR |
| B | Direct submission without a pull request (`when_review_needed`) | A follow-up ADR on user authentication |
| C | Repositories with no pull requests at all (`never`), and optional model-written prose in briefs | Notification and request-changes channels outside pull requests; a model-provider decision |

### Changes and their identity (Stage A)

**A change is a durable record owned by one queue.** OpenMerge assigns its ID; the repository and base never change after creation. A change has a sequence of revisions. Each revision pins one head commit and carries its own trust decision, classification, approvals, tests and outcome. A re-push creates a new revision; nothing from an earlier revision carries over except the change's position rules (below).

**Each live change has exactly one source.** For a pull request, the source is the pull request, and every existing pull request maps deterministically to a change ID derived from its queue and PR number, so existing queues need no migration. For a direct submission (Stage B), the source is its submission ref. A pull request opened later for a submitted change attaches to that change as an alias rather than creating a second one. Updates to a change must name the revision they replace; a stale update is rejected.

**A landing settles before an update applies.** Once a landing attempt starts for revision H1, that attempt is settled through the fence exactly as today, whatever happens to the change meanwhile. A later revision H2 is never reported as landed by H1's attempt. This keeps the settlement guarantees of [ADR 0004](0004-forge-driver-interface.md) and [ADR 0006](0006-multi-repository-serving.md).

**Conflict grace uses change identity.** As planned in [ADR 0005](0005-verification-modes.md), a conflicting change may hold its position for one `conflict_grace` deadline set at the first conflict and never renewed across revisions, with a bounded number of retries. Entries behind it keep landing.

**Storage stays additive.** Every deployed event and action encoding, and every golden fixture, stays byte-for-byte unchanged ([ADR 0002](0002-storage-and-state-model.md)). Today's batches, entries and actions are keyed by PR number. Changes add a parallel family of change-keyed event and action types rather than reusing PR fields or inventing PR numbers. Pull-request changes keep their existing entry, batch and action types, and the new facts about them (classification, decision snapshots) arrive as new event types, so pull-request-only queues replay as before. Records written before this ADR have no accountability information; they show it as "not recorded", never as a guess.

### Classification (Stage A)

**Classification records observations, not guarantees.** OpenMerge labels each revision from its diff against a pinned base, recorded as (base SHA, head SHA, classifier version). The labels are:

- `docs`: every changed file matches the repository's declared documentation paths;
- `generated`: every changed file is marked generated by `.gitattributes` (`linguist-generated`) or by declared paths. This records where a file sits, not that a generator produced it;
- `owned:<owner>`: changed files covered by an owner rule;
- `policy`: the change touches `.openmerge.yml`, `CODEOWNERS`, `.gitattributes`, CI workflow definitions, or any path the config marks sensitive;
- `unknown`: anything that cannot be observed safely.

A change can carry several labels. **Anything uncertain is `unknown`, and `unknown` gets the strongest requirement:** binary files, mode or symlink changes, submodule updates, renames that cannot be paired, diffs over the forge's file limit, or any read failure. A `policy` label always requires owner approval, so a change can never weaken the rules it is judged by.

**Comment-only changes are not classified in this ADR.** Comments change behaviour too often to promise otherwise: Go build constraints and `//go:generate`, compiler pragmas, doctests, Python docstrings, shebangs and type annotations in comments. A later decision can add comment-only detection for specific languages, with listed exclusions, when there is a tested classifier for each.

### Policy approvals (Stage A)

**Policy adds requirements; it never removes them.** `enter_when` and `merge_when` still apply to every change. On top of them, `.openmerge.yml` rules on the default branch (per [ADR 0006](0006-multi-repository-serving.md)) map labels, paths and author kinds to approval requirements: none, any writer, or specific owners, with a count. When several rules match, every matching requirement must be met, so a rule saying "no approval needed" cannot cancel another rule that requires one. A change no rule matches gets the queue's `default_approval`, which defaults to the existing behaviour. Owner rules come from `.openmerge.yml` or from `CODEOWNERS` at the governing default-branch commit, never from the change itself. For release queues this differs from GitHub, which reads `CODEOWNERS` from the target branch; OpenMerge follows ADR 0006 so a release branch cannot weaken its own owners. Policy rules are part of the queue's configuration digest, so changing them invalidates batches in flight.

**Approvals are bound and counted strictly.** An approval names the revision's exact head and the policy digest it was given under, and a new revision invalidates it. Each approver counts once. The author, submitter and accountable human of a revision (below) never count toward its approvals unless a rule explicitly allows self-approval. A reviewer's write permission, and ownership for owner rules, is re-read when the approval is evaluated. On pull requests, GitHub reviews on the exact head are approvals. In Stage B they can also be given with `openmerge change approve <id> --sha <head>`.

**Approvals are evidence in the log, not in the tested commit.** Approvals usually arrive while CI runs, so they cannot be fixed when the test ref is built without retesting. Before a landing starts, OpenMerge records an immutable decision snapshot in the event log: the revision, its classification, the policy digest, the approvals relied on, and the intent text the approvals were given against. If the snapshot no longer holds (an approval was withdrawn, a permission changed), the landing does not start. Commits carry what is known at build time, where the merge method allows it: `OpenMerge-Change` and `OpenMerge-Classification` trailers on squash and merge commits, and on the batch marker for fast-forward, which lands the original head untouched. In post-merge queues, the decision snapshot names both the tested commit T and the landed commit L, as ADR 0005 requires; L is never described as tested.

### Accountability (Stage A for pull requests, Stage B for submissions)

**Three identities are kept apart:** the *actor* that authenticated (a person, a bot account or an App), the *accountable human* who answers for the change, and each *reviewer*. All are stable forge user IDs, not display names.

- A person submitting for themselves is their own accountable human.
- An agent's accountable human is declared in the default-branch `.openmerge.yml`, as a mapping from the agent's identity to one or more people. A submission cannot name its own accountable human, so a write token cannot assign a change to a colleague.
- For a pull request, the accountable human is the PR's author if they are a person, or the agent mapping for a bot author. A bot-authored PR with no mapping keeps working exactly as today; its accountable human is "not recorded" and policy can require a human approval for that case.
- OpenMerge re-reads the accountable human's write permission before testing and before landing.

This model cannot prove that a person, rather than an agent holding their token, took an action. The ADR says so, and the docs will too. Agents should run under their own identities (a bot account or an App), which also keeps their actions out of their owner's approvals.

### Enforcement without a pull request (Stage B)

**OpenMerge already lands by bypassing pull request rules.** Its atomic push needs the App on the bypass list of rulesets and classic protection that cover the base. A bypassed push is not checked against GitHub's required reviews, CODEOWNERS reviews or last-push approval: GitHub evaluates those only on a pull request. With pull requests, OpenMerge's trust gate and conditions read those reviews. Without one, nothing does unless OpenMerge enforces them itself.

**So direct submission refuses bases where GitHub would require reviews.** Before accepting a direct submission, OpenMerge reads the base's rulesets and classic protection. If they require pull request reviews or code owner review, or cannot be read, the submission is rejected with that reason. A team that wants direct submission moves its review requirements into OpenMerge policy and removes them from GitHub, and in doing so accepts that OpenMerge, not GitHub, enforces review on that base. GitHub's audit log then shows a push by the App; OpenMerge's log holds the decision. Teams whose compliance evidence must be GitHub-enforced reviews stay on `required`.

### Direct submission (Stage B)

**Per queue, `changes.pull_requests` chooses how much a queue relies on pull requests:**

- `required` (the default): only pull requests become changes, exactly as today.
- `when_review_needed`: direct submissions are accepted. If policy needs a human approval, OpenMerge opens a pull request for the change as the App, titled from the intent, so review happens where the team already works. That pull request is an alias of the change, not a second change.
- `never` (Stage C): direct submissions only.

The setting is part of the queue entry in the default-branch config and of its digest. Older binaries reject the new keys, so downgrading past this change means removing them first, as in ADR 0006.

**Submitting a change pushes a commit, not a branch.** `openmerge change submit` pushes the local `HEAD` to `openmerge/submit/<id>` and records the pinned SHA with a stated intent. The submission ref is only where OpenMerge fetches the commit from; moving it later does nothing until an authorized update names the revision it replaces. Submission refs need write access, unlike the App-only queue and fence refs. Submissions are idempotent by a client request key. In this stage, direct submission covers commits pushed to the repository itself; fork heads stay pull-request only, because fork trust today is a writer's review of the exact head.

**Conditions see what a direct change actually has.** `approved-reviews-by` counts approvals bound to the head, from either source. PR-only attributes (`draft`, `label`, `title`, `author`) are unknown for a direct change, and unknown never passes, including under negation. New attributes `submitter` and `accountable` are available for all changes. Squash and merge commits keep the git authorship of the submitted commits; sign-offs and DCO checks are unaffected. Pull requests and direct changes can share a batch; each keeps its own decision snapshot.

**User authentication is a separate decision.** Direct submission and CLI approval need to know which person or agent is calling. A follow-up ADR, required before Stage B ships, decides the mechanism. It must meet these constraints: the identity is verified with the forge on every write; OpenMerge stores no user tokens; the check goes through the forge driver so other forges can implement it; and setup stays within the five-minute self-host target.

### Pushes outside the queue (Stage A)

**OpenMerge records every base move it did not make.** When the base advances by a push that was not an OpenMerge landing, the queue records the new commits as landed outside the queue and not tested by OpenMerge. Status and briefs show them that way. Strict queues already rebuild batches when the base moves; post-merge queues already verify every landed commit. Recording these moves keeps the history honest about what the queue did and did not check.

### Briefs (Stage A, prose in Stage C)

**A brief is a template over recorded facts, with an optional labelled summary.** For each audience:

- *Reviewer:* the pinned head and base; the stated intent, shown as the submitter's claim, not a fact; the full inventory of changed files and hunks from the recorded diff, so nothing can be left out; the labels and why each applies; the policy requirements and who can meet them; and links to test evidence.
- *Operator:* where the change is in the queue, what blocks it, and what clears each block.
- *Agent:* structured next steps (fix this check, rebase onto this commit, wait for this approval) as stable JSON.

Briefs are available through the CLI and API, and through MCP when it ships. When a pull request exists, the reviewer brief also appears in its OpenMerge check. An approval given from a brief records the brief's digest, so an audit can show exactly what the approver saw.

**Generated prose is never evidence.** In Stage C, an operator may configure a model to add a summary to briefs. It appears in a delimited section labelled as generated, is never read by policy or the planner, and is off by default; choosing a provider means choosing where private diffs are sent. Because the factual inventory is complete without it, text planted in a diff or an intent to steer the summary ("this is a trivial change") cannot hide what the change touches. This extends principle 1, "never lie about what was tested", to review: OpenMerge never presents a summary as if someone reviewed the code.

## Consequences

- Repositories that change nothing see nothing change: `required` is the default, and pull requests keep their current flow.
- Phase 3 is reframed around changes. Stage A (change identity, classification, policy approvals, briefs, records of outside pushes) ships first. It delivers intent-level review inside the trust model teams already have. Stage B and Stage C follow only once their prerequisites exist.
- The Phase 3 exit test becomes testable on the fake forge: an unattended agent takes a change from submitted to landed or definitively rejected through the CLI or MCP; low-risk changes land on policy alone; a change that policy says needs a human waits until a scripted reviewer approves the exact head; and every landing's decision snapshot and brief digest can be read back.
- OpenMerge becomes the enforcer of review policy for queues that use direct submission. That is a real responsibility: the policy engine, owner resolution and approval counting need the same simulator and contract testing as landing.
- The forge driver grows: reading diffs against a base, reporting status on a bare commit, opening a pull request for a change, resolving team membership for owner rules, and verifying a user's identity. Each gets a fake-forge contract test. Other forges also center on pull or merge requests, so this does not make Phase 5 easier, but it keeps those operations behind the driver.
- Landing without a pull request gives things up: integrations keyed by pull requests, release notes built from them, and `#N` links in history. Briefs and change IDs replace some of that, not all; teams choose per queue.
- Every landed change records its accountable human where one is known, and says "not recorded" where it is not.

## Alternatives considered

- **Keep pull requests as the only unit.** Simplest, and the default stays exactly that. But agents would keep waiting for full human review of every change, and OpenMerge could not land low-risk changes on policy.
- **Drop pull requests entirely.** It would remove the discussion, notifications and integrations teams depend on, and make OpenMerge hard to adopt inside an existing GitHub organization.
- **Let a model decide approvals.** Fast, but not auditable or reproducible, and open to text in the change steering the decision. Classification stays deterministic and conservative; a model may only write prose a human reads.
- **One ADR, shipped at once.** The first draft did this. Authentication, PR-less review and model integration each need their own design; staging lets the parts that fit today's trust model ship first.
- **Build a separate review tool.** Anthropic built one (Antique) alongside a new version control system. Briefs stop well short of that: they are views over evidence OpenMerge already holds, shown where review already happens.
