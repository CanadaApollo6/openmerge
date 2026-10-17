# 0008. Let GitHub enforce who can write, and OpenMerge enforce everything else

- **Status:** Proposed
- **Date:** 2026-10-17

## Context

A team running OpenMerge today keeps two policy systems: GitHub's branch protection and rulesets, and `.openmerge.yml`. They overlap and fight.

- OpenMerge lands with one atomic push as its App. For that push to succeed, the App must bypass GitHub's pull request and review rules ([ADR 0004](0004-forge-driver-interface.md)), and required status checks block it, so we already tell teams to express CI requirements in `merge_when` instead. GitHub's review rules are therefore already advisory for every change that goes through the queue.
- [ADR 0007](0007-changes-not-prs.md) Stage B refuses direct submission on any base where GitHub would require a pull request, so a team that wants it must move review requirements into OpenMerge and remove them from GitHub.
- Agents work through OpenMerge's CLI, API and MCP. Asking them to also reason about GitHub's protection settings, which the App bypasses anyway, adds a second source of truth that is wrong half the time.
- The setting that GitHub *does* enforce reliably, and that OpenMerge cannot, is who may write a ref. If people can push to the base directly, every OpenMerge policy is a suggestion.

This ADR makes the split explicit: GitHub enforces who can write protected refs; OpenMerge enforces everything else about what lands.

## Decision

### Authority is a per-repository choice

**`policy_authority` in the default-branch `.openmerge.yml` selects who owns policy:**

- `github` (the default): today's behavior. GitHub's branch protection and rulesets are the team's policy; OpenMerge checks that they don't block its landing push and coexists with them.
- `openmerge`: OpenMerge's configuration is the policy for every queue in the repository. GitHub keeps only the write restriction below.

The setting follows [ADR 0006](0006-multi-repository-serving.md): it is read from the governing default-branch commit, is part of the configuration digest, and older binaries reject it.

### What GitHub keeps

**One write ruleset, managed by OpenMerge setup.** In `openmerge` mode, `openmerge setup rulesets --authority openmerge` creates or repairs a ruleset named `openmerge-write` that targets every base branch the repository's queues serve, plus `openmerge/fence/*`, `openmerge/green/*` and `openmerge/verify/*`. It restricts creation, update, deletion and non-fast-forward pushes. Its bypass list has exactly two kinds of entry: the OpenMerge App (`always`), and a break-glass actor chosen by the operator, such as the repository admin role or a named team. Like today's `openmerge-fence` ruleset, setup preserves other rulesets and never edits inherited ones.

**Content rules may stay on GitHub if they are compatible.** Rules about commit content rather than about who may write (required linear history, required signatures, file path, file size and commit message restrictions) can remain in a separate ruleset *without* an App bypass, so GitHub applies them to the App's push too. OpenMerge's runtime rules check already validates which of these its landing can satisfy. An incompatible content rule (for example, required signatures while OpenMerge creates unsigned commits) is a configuration problem that blocks the queue, as today.

**Rules shaped around pull requests move to OpenMerge.** Required pull request reviews, code owner review, last-push approval, conversation resolution and required status checks are not enforced on the App's push in either mode. In `openmerge` mode they belong in `.openmerge.yml` (approvals, conditions and `merge_when`). Required deployments have no OpenMerge equivalent yet and are reported as untranslatable.

**This mode needs an organization repository.** GitHub can reject an `Integration` bypass actor on personal-account repositories. Where it does, `openmerge` mode cannot be enforced, setup reports it, and the repository stays on `github`.

### Enforcement and drift

**OpenMerge refuses to claim authority it does not have.** In `openmerge` mode, the runtime rules check, which already reads rulesets and classic protection every ten minutes, also verifies the write restriction:

- If any base the repository serves is writable by anyone other than the App and the declared break-glass actor, that queue is blocked with a `repository-rules` problem naming the gap. A team that declared OpenMerge authoritative is told plainly that it is not, instead of OpenMerge reporting policy it cannot enforce.
- If the bypass list contains other actors, the queue is blocked the same way.
- If the write restriction cannot be read, the queue keeps its previous state for that source, as with every other rules source today. Bypass lists hidden from the installation are reported as unverified, and `setup rulesets` with an admin token remains the audit.
- Pull-request-shaped GitHub rules that are still present are reported as warnings, not blocks: they cannot affect the App's push, but they confuse people and should be removed.

**Break-glass pushes are visible.** A push to the base that OpenMerge did not make is recorded as landed outside the queue, with no OpenMerge verification before landing ([ADR 0007](0007-changes-not-prs.md)). In `openmerge` mode, such a push also raises a warning in status and in the queue's next PR checks until an operator acknowledges it with `@openmerge acknowledge <sha>` (writer-only, recorded in the log).

### Migrating protection into OpenMerge

**`openmerge setup import-protection --repo owner/name` translates existing protection into configuration.** It reads rulesets and classic protection with a repository-admin token, as `setup rulesets` does, and prints:

- a proposed `.openmerge.yml` change: required approval counts become approval requirements ([ADR 0007](0007-changes-not-prs.md) Stage A) or `#approved-reviews-by` conditions when approval policy is not in use; code owner review becomes an owner requirement over `CODEOWNERS`; required status checks become `merge_when` `check-success` terms; dismissal of stale approvals is already implied by head-bound approvals; last-push approval maps to the exclusion of a change's submitter from its own approvals;
- the content rules that can stay on GitHub;
- what it cannot translate, such as required deployments, conversation resolution and bypass lists that exist for reasons OpenMerge cannot see.

It never edits the repository or its rulesets. It exits nonzero while anything is untranslatable, unless the operator passes `--accept-untranslatable` after reading the report.

**The switch has a fixed order, so there is never a gap.** First the configuration with `policy_authority: openmerge` lands on the default branch through the queue, while GitHub's old rules are still in place. OpenMerge then reports any missing write restriction as a block. Then `setup rulesets --authority openmerge --apply` installs `openmerge-write`. Only then does the operator remove the old pull-request-shaped rules. Going back to `github` reverses the order: restore GitHub's rules first, then change the setting.

### People and agents

**The merge button stops working, and OpenMerge says why.** With the write ruleset in place, a person's merge through GitHub's interface is rejected by GitHub. In `openmerge` mode, OpenMerge's check on every pull request states that the repository merges through OpenMerge and shows the command (`@openmerge queue`) and the change's current queue state. Docs tell teams to disable GitHub auto-merge, which would otherwise fail silently.

**CI stays where it is.** OpenMerge does not run CI. It decides which checks must pass, through `merge_when`, and keeps its existing provenance rules for which runs count.

### Evidence and availability

**`openmerge audit export` produces the record auditors need.** For a repository and time range, it writes JSON Lines covering every landing's decision snapshot (what was tested, which checks passed, who or which rule approved, the accountable human), every outside-queue landing and acknowledgement, every configuration digest change with its default-branch commit, and every protection observation. Each record carries the SHA-256 of the previous one, so a gap or an edit is detectable. The export reads the event log; it adds nothing to it.

**OpenMerge must be observable as critical infrastructure.** In `openmerge` mode an OpenMerge outage stops merges, and a stuck queue that reports itself as healthy is worse than a crashed one. The status API gains `GET /v1/health`, which returns, per queue, whether it is ready, why not, and the time of its last successful cycle, with a non-200 status if any served queue has not completed a cycle within three poll intervals. Docs give a monitoring recipe and a break-glass runbook.

### Implementation

Implementation lands in three PRs:

1. `policy_authority`, the `openmerge-write` ruleset in `setup rulesets`, the runtime write-restriction and bypass checks, drift warnings, the PR check message, outside-push warnings with `@openmerge acknowledge`, and `/v1/health`.
2. `setup import-protection`.
3. `audit export`.

## Consequences

- A team in `openmerge` mode has one policy surface, versioned with its code and changed only by default-branch writers through the queue. Agents only ever deal with OpenMerge.
- Repositories that do nothing see nothing change: `github` remains the default.
- OpenMerge becomes critical infrastructure for those teams. Its availability and its honesty about readiness matter as much as its correctness; `/v1/health` and the break-glass path exist for that. High availability stays in Phase 6.
- Compliance evidence moves from GitHub's protection settings to OpenMerge's log. The audit export is the artifact; teams that cannot accept that stay on `github`.
- The same configuration expresses policy on other forges, which helps Phase 5: each forge driver only has to provide a write restriction with an App bypass, which GitLab and Forgejo protected branches can.
- OpenMerge's own repository is on a personal account, where the App bypass may be rejected, so dogfooding `openmerge` mode needs an organization repository.

## Alternatives considered

- **Keep both systems.** It is the default and stays available, but teams keep two sources of truth, one of which the queue bypasses.
- **Make OpenMerge a required status check and keep GitHub's merge button.** It keeps GitHub as the authority, but GitHub's merge does not land exactly what was tested, and serialization moves back to people. A GitHub merge queue adapter remains a Phase 5 option for teams that want GitHub's interface.
- **Drop GitHub protection entirely.** Anyone with write access could push around OpenMerge, so its policies would be suggestions.
- **Have OpenMerge compile its policy into GitHub rulesets.** GitHub's rules cannot express approval by change class, accountable humans or post-merge verification, and they are evaluated on pull requests that the App bypasses anyway.
