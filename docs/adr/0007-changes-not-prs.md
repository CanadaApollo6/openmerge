# 0007. Make the change the unit of work, with pull requests as one optional view

- **Status:** Proposed
- **Date:** 2026-10-16

## Context

OpenMerge's queue has always been about changes, not pull requests. What it needs to decide whether to land something is a head commit, who may land it, what was tested, and whether policy allows it. The pull request is how GitHub hands it those facts.

With coding agents, the pull request is becoming the bottleneck rather than the container:

- **It is shaped for line-by-line human review.** Agents produce more diffs than people can read. Anthropic reports that its reviewers now "examine the intent of the change as opposed to reading every line of code", backed by approval rules that tell a comment-only change from a sensitive one ([Redesigning source control at Anthropic](https://www.anthropic.com/engineering/redesigning-source-control)).
- **It gives every change the same ceremony.** A typo fix and an authentication rewrite get the same flow and the same wait.
- **It is one page for every reader.** A reviewer, an operator and an agent fixing a conflict need different information.
- **It costs something to create.** A branch, a description, labels and reviewers are overhead for an agent's thirtieth small change of the day.

People are asking whether there is something better than pull requests. OpenMerge already holds everything a merge decision needs in its event log ([ADR 0002](0002-storage-and-state-model.md)), and it reaches forges through a narrow driver ([ADR 0004](0004-forge-driver-interface.md)). That makes it a natural place to treat the change as the unit and the pull request as one way to look at it.

## Decision

### The change is the unit

**A change is a durable record in the queue.** It has an ID; a repository and base branch; a head commit, which must be on a branch in the repository or an approved fork head, as today; the submitter's identity; an accountable human; a stated intent (a short description of what the change is for); and its history. A change survives re-pushes and rebases of its head: this is the change identity already planned for Phase 3. Queue entries refer to changes, and every existing decision (trust, conditions, testing, landing) applies to a change exactly as it does to a pull request today.

**Changes arrive two ways.** A pull request becomes a change through the forge driver, as now. A direct submission (`openmerge change submit`, the API, or MCP) creates a change from a pushed branch without opening a pull request. The submitter authenticates as a GitHub user, and OpenMerge checks write permission freshly, as the trust gate already does. An agent submits on behalf of an accountable human: the person who runs or owns that agent, recorded with the change. Changes that no person is accountable for are rejected.

### Pull requests become optional, per repository

**Each repository chooses how much it relies on pull requests.** A new `changes.pull_requests` setting in `.openmerge.yml` takes one of three values:

- `required` (the default): only pull requests become changes, exactly as today.
- `when_review_needed`: direct submissions are accepted. OpenMerge opens a pull request only when policy requires a human review, so the review happens where the team already works.
- `never`: direct submissions only; human approvals happen through OpenMerge.

Branch protection and rulesets still apply to every landing, whichever path a change took.

### Approvals come from policy, recorded as evidence

**Every change is classified deterministically from its diff.** The classes are comment-only, documentation-only, generated (by path patterns the repository declares), owned paths (from CODEOWNERS or declared owners), and everything else. The classification is code, not a model's opinion, and it is recorded with the change.

**Policy decides what approval a class needs.** Repositories declare rules in `.openmerge.yml` that map classes, paths and authors to requirements: no human approval, any writer's approval of intent, or specific owners. A comment-only change from a trusted author can land on policy alone; an agent's change to `infra/**` can require two owners. Approvals are bound to the exact head they approved, and a new push invalidates them, as the trust gate already requires. They can be given in a pull request review or through `openmerge change approve <id> --sha <head>`.

**Approvals and their basis travel with the landed commit.** The landing metadata, fixed when the test ref is built and so part of what was tested, records the change ID, the classification, and who approved it and why (policy rule or person). The event log keeps the full record.

### Views are generated per reader

**OpenMerge produces review briefs on demand.** Each brief is built from the event log and the diff, for its reader:

- *Reviewer:* the stated intent, the classification and why, the files and behaviour that changed, test evidence, and what policy requires.
- *Operator:* where the change is in the queue, what blocks it, and what clears each block.
- *Agent:* structured next steps (fix this check, rebase onto this commit, wait for this approval) as stable JSON.

Briefs are available through the CLI, the API and MCP. When a pull request exists, the reviewer brief also appears in its OpenMerge check.

**Generated prose is never evidence.** A brief may include a model-written summary, clearly labelled as generated. Policy decisions, approvals and the "tested" claim rest only on recorded facts: diffs, classifications, check results and approvals. This extends principle 1 ("never lie about what was tested") to review: OpenMerge never presents a summary as if someone had reviewed the code.

## Consequences

- Teams that live in GitHub see no change by default. Teams with agents can drop per-change pull requests where policy allows, and keep human review exactly where it matters.
- Phase 3 is reframed around changes. Change identity, change classification, policy approvals, the write API and agent-facing explanations become parts of one model instead of separate features. The Phase 3 exit test becomes: an unattended agent takes a change from "submitted" to "landed or definitively rejected" through the CLI or MCP, with a pull request only where policy required a human, and the human approves from the generated brief without leaving it to read logs.
- New event types record changes, submissions, classifications and approvals. They are additive under [ADR 0002](0002-storage-and-state-model.md), and pull-request-only repositories keep replaying as before.
- Accountability becomes explicit: every landed change names a human, even when an agent did the work.
- Direct submission needs authenticated users, through GitHub OAuth or a GitHub token checked for write permission, in addition to the App installation. The setup flow and docs gain that step.
- Forges without pull requests in the GitHub sense, or with different review models, fit the change model more naturally. That helps the Phase 5 drivers.

## Alternatives considered

- **Keep pull requests as the only unit.** Simplest, and the default stays exactly that. But agents would keep paying full pull request ceremony for every change, and OpenMerge could not offer policy-based landing without a review UI.
- **Drop pull requests entirely.** It would cut out the audit trail, the discussion and the integrations teams depend on, and make OpenMerge hard to adopt inside an existing GitHub organization.
- **Let a model decide approvals.** Fast, but not auditable or reproducible, and it would let a summary stand in for a review. Classification stays deterministic. Models may write the prose a human reads, never the decision.
- **Build a separate review tool.** Anthropic built Antique alongside a new version control system. OpenMerge sits on top of existing forges, so the views belong in the queue that already holds the evidence, not in another system to run.
