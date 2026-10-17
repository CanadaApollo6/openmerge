# 0008. Make OpenMerge authoritative for queue policy behind forge write restrictions

- **Status:** Proposed
- **Date:** 2026-10-17

## Context

A team running OpenMerge today keeps two policy systems: GitHub's branch protection and rulesets, and `.openmerge.yml`. They overlap and fight.

- OpenMerge lands with one atomic push as its App. For that push to succeed, the App must bypass GitHub's pull request and review rules ([ADR 0004](0004-forge-driver-interface.md)), and required status checks block it, so teams already express CI requirements in `merge_when`. For changes that go through the queue, GitHub's review rules are therefore advisory.
- [ADR 0007](0007-changes-not-prs.md) Stage B refuses direct submission on any base where GitHub would require a pull request, so a team that wants it must move review requirements into OpenMerge.
- Agents work through OpenMerge's CLI, API and MCP. Asking them to also reason about GitHub settings that the App bypasses anyway adds a second source of truth.
- What GitHub enforces well, and OpenMerge cannot, is who may write a ref. If ordinary writers can push to the base, every OpenMerge policy is a suggestion.

This ADR lets a repository make OpenMerge the authority for what lands, with GitHub restricting who can write. It does not make OpenMerge the authority for everything: some content rules stay on GitHub, some GitHub requirements have no OpenMerge equivalent yet, and administrators remain trusted.

## Decision

### Authority is a per-repository choice

**`policy_authority` in the default-branch `.openmerge.yml` selects who owns queue policy:**

- `github` (the default): today's behavior. GitHub's protection is the team's policy; OpenMerge checks that it doesn't block its landing push.
- `openmerge`: OpenMerge's configuration is the policy for every queue in the repository, and GitHub keeps the write restrictions and content rules below.

The setting follows [ADR 0006](0006-multi-repository-serving.md): it is read from the governing default-branch commit, is part of the configuration digest, and older binaries reject it. **In `openmerge` mode, the default branch must itself be served by a queue,** because it holds the policy. A repository that serves only release branches cannot use this mode.

`openmerge` mode also requires `break_glass` in the same file: exactly one actor, either a team (`org/team`) or the repository-admin role. It is part of the configuration digest and is resolved to a stable forge ID, so the runtime compares observed bypass lists against declared policy rather than against what it is auditing.

**Prerequisites.** `openmerge` mode depends on ADR 0007 Stage A: approval policy, owner requirements, records of pushes made outside the queue, and decision snapshots. A repository that requires reviews today cannot express that requirement equivalently without Stage A. The configuration loader rejects `policy_authority: openmerge` until those features exist.

### What GitHub keeps

**Two write rulesets, managed by `openmerge setup rulesets --authority openmerge`:**

- `openmerge-base`: targets the default branch and every queue's `base` selector, written as ruleset ref patterns that mirror the selectors (globs included), so a newly created matching branch is covered before its queue activates. It restricts creation, update, deletion and non-fast-forward pushes. Its bypass list is the App and the declared break-glass actor.
- `openmerge-refs`: the existing App-only `openmerge-fence` ruleset, extended to `openmerge/green/` and `openmerge/verify/` with the same recursive patterns the fence uses today (`openmerge/fence/*` and `openmerge/fence/**/*`, and likewise for the others). Only the App may bypass it. Break-glass never reaches fence, green or verify refs, so it can repair a base without rewriting the queue's evidence.

Queue refs (`openmerge/queue/**`) and, in Stage B, submission refs (`openmerge/submit/**`) are not in either write ruleset, as today. Setup preserves other rulesets and never edits inherited ones.

**Content rules stay on GitHub only where every operation can satisfy them.** Rules about commit content rather than about who may write (linear history, signatures, file path, file size and commit message restrictions) can remain in a ruleset without an App bypass, so GitHub applies them to the App's pushes too. This needs new validation: today any active ruleset on the base without an App bypass is a blocker. The runtime and setup checks gain rule-aware validation by merge method and by affected ref, covering queue construction, the atomic base-and-fence landing, verify pushes and green ref updates:

- Required signatures are incompatible, because OpenMerge's generated commits, markers and fence commits are unsigned.
- Linear history is incompatible with the `merge` method, and with fast-forward when contributor history contains merges.
- Commit message patterns must accept OpenMerge's generated messages and trailers, including generated reverts.
- Push rules on paths and file sizes apply to every push, including queue builds, and are compatible only when contributors' commits already satisfy them.

Unknown rule types and combinations are unsupported and block the queue, as today.

**Rules shaped around pull requests move to OpenMerge.** Required reviews, code owner review and required status checks belong in `.openmerge.yml`: approval policy, owner requirements and `merge_when`. Last-push approval, conversation resolution and required deployments have no OpenMerge equivalent yet. A team that relies on them either keeps them, where GitHub still enforces them on the App's push (see below), or accepts the gap explicitly during migration.

### Enforcement and drift

**The write restriction is its own rules source.** Like `rulesets`, `classic_protection` and `permissions` today, `write_restriction` has its own block and revision under the source model from #30 and #35, so its clears never touch fence or other findings. Each observation is one of:

| Observation | Effect |
|---|---|
| **Verified:** the base and refs rulesets are visible, target every required ref, and their bypass lists are exactly the declared actors | Admit work |
| **Violated:** a required ref is visibly unprotected, or a visible bypass list includes an undeclared actor | Block the affected queues until a complete passing observation |
| **Unverified:** the rulesets are present, but their bypass lists are hidden from the installation | Admit work, and report "authority unverified" with its age in status, health and the queue's checks |
| **Read failure or unavailable source** | Keep the source's previous state; a queue with no previous state starts as unverified |

GitHub returns bypass lists only to callers with write access to the ruleset, so the App usually sees the unverified state. Operators verify bypass ownership with `setup rulesets --authority openmerge`, which uses a repository-admin token as `setup rulesets` does today; inherited organization rulesets may need an organization owner. Granting the App Administration read lets it read classic protection, which today returns 403. Setup recommends that grant in `openmerge` mode and names the privilege it adds. Refresh and acknowledgement never clear a violation, and settlement always runs.

**Every applicable protection is still validated.** An App bypass on `openmerge-base` does not bypass other rulesets, inherited organization rulesets or classic protection: GitHub applies them together. The runtime keeps validating all of them. A rule shaped around pull requests is only a warning when it cannot affect the App's push, because the App is on its own bypass list. A rule that would block the landing push remains a blocker. ADR 0007 Stage B still refuses direct submission while any rule that needs a pull request is active, bypassed or not.

**The trust boundary is stated, not implied.** The write rulesets constrain ordinary writers under the observed rules. Repository rules administrators, organization owners, whoever holds the App's private key, and the declared break-glass actor remain trusted. A push by any of them is detected after the fact, not prevented. Detection does not depend on catching a ruleset change: every base commit that OpenMerge did not land is recorded as landed outside the queue (ADR 0007), even if the protection was disabled and restored between observations. OpenMerge reports what it observed and when; it does not claim continuous exclusivity.

**Outside pushes are flagged until acknowledged.** In `openmerge` mode, an outside-queue landing is shown as a warning in status, health and the API for its base. Landings continue meanwhile. An operator acknowledges it with `openmerge admin acknowledge --queue <key> --event <id> --sha <sha>`, which runs against the database with operator credentials, because it is not a pull request action. The command records the operator-supplied name, the target event and SHA, and the time, atomically and idempotently. Acknowledgement records that someone with operator access saw the landing. It does not record who pushed, does not approve the change, does not clear rules or suspect blocks, and does not move the green ref.

### Migrating protection into OpenMerge

**`openmerge setup import-protection --repo owner/name` reports how existing protection would translate.** It reads rulesets, inherited rulesets and classic protection with a repository-admin token, and is useful in `github` mode as an audit. It never edits the repository. For each requirement it prints a proposed `.openmerge.yml` change and a classification: **equivalent**, **stricter**, **weaker** or **unsupported**.

- **Reviews** map only to ADR 0007 approval policy, with eligible, distinct reviewers and blocking requests for changes. Plain `#approved-reviews-by` conditions are not equivalent, because they don't filter for eligibility. Head-bound approvals are stricter than GitHub with stale-review dismissal off. They are approximately equivalent with it on, and the report says where GitHub's merge-base handling differs. Last-push approval is unsupported: excluding the submitter does not exclude a later pusher.
- **Code owner review** maps to owner requirements over `CODEOWNERS` at the governing commit. The report compares the effective `CODEOWNERS` for every served base with the default branch's and lists every difference, since ADR 0007 deliberately reads only the default branch's.
- **Required status checks** map to `merge_when` `check-success` terms only where the name identifies one producer. A check pinned to a specific App, a name shared by a commit status and a check run, and GitHub's acceptance of neutral or skipped results are reported as weaker or unsupported. For each check, the report says whether any workflow producing it runs on pushes to `openmerge/queue/**`; a check that runs only on pull requests is unsupported until CI changes. GitHub's strict up-to-date requirement maps to strict verification ([ADR 0005](0005-verification-modes.md)); a post-merge queue is reported as a relaxation.
- **Conversation resolution and required deployments** are unsupported.
- **Inherited bypass actors and other writers of each base** are reported as blockers to the switch. That covers release jobs, version-bump CI and backport bots. Each must route through the queue, become the declared break-glass actor, or stop the switch.
- **OpenMerge's own configuration paths** must be covered by `CODEOWNERS`. In `openmerge` mode a policy change needs owner approval (ADR 0007's `policy` label); without that, any queue-eligible writer could propose weaker policy, a separation-of-duties regression from GitHub settings that need an administrator.

The command exits nonzero while anything is weaker, unsupported or blocking, unless the operator passes `--accept` with the list of items they accept. Accepted items are written into the proposed configuration as comments.

**The switch is a coordinated transition, not an atomic one.** In order:

1. **Preflight:** `import-protection` reports clean or explicitly accepted, and setup confirms GitHub accepts the App and break-glass bypass on the repository.
2. **Install the write rulesets** while the old protection stays in place, and verify them with the admin token.
3. **Land the configuration** with `policy_authority: openmerge` through the queue, under the old protection.
4. **Wait** until every affected queue reports the new configuration digest, settles any attempt started under the old one, and shows no violation.
5. **Remove** only the reviewed, redundant pull-request rules.

Serialize other policy edits during the switch. Rollback reverses the order: restore the GitHub rules first, keeping the App on their bypass lists so the queue can still land, then land `policy_authority: github`. If the write rulesets cannot be installed at step 2, nothing has changed and the repository stays on `github`. The procedure is verified on an organization sandbox repository before it ships.

### People and agents

**The merge button stops working for ordinary writers.** With `openmerge-base` in place, GitHub rejects a merge through its interface by anyone not on the bypass list. Where OpenMerge reports a check on a pull request, the check states that the repository merges through OpenMerge, with the `@openmerge queue` command and the change's queue state. `import-protection` prints a short snippet for the repository's contributing guide. Docs tell teams to disable GitHub auto-merge, which would otherwise fail silently. Members of the break-glass actor can still merge through GitHub, and those merges are recorded as outside-queue landings.

**CI stays where it is.** OpenMerge does not run CI. It decides which checks must pass and keeps its existing rules for which runs count.

### Evidence and operations

**`openmerge audit export` writes what the log holds, and says what that proves.** It runs against the database with operator credentials, for one repository and a time range, and writes JSON Lines with a manifest. The manifest records the repository identity, filter, source watermarks, record count, terminal digest and serialization version. Each record references its source event and carries the SHA-256 of the previous record.

The chain detects edits within an export. It detects edits to the log only where an earlier export's terminal digest was recorded outside OpenMerge, for example signed and stored with the organization's audit records. It does not make the database tamper-proof. It does not prove that a person rather than their token acted, or that protection held between observations.

The export only reads, so the facts it reports must be recorded first. This ADR adds new, durable event types, with golden fixtures, for write-restriction state changes and for acknowledgements. Decision snapshots, with their individual check results, come from ADR 0007 Stage A. Separate tested and landed commits and later verification come from ADR 0005. History recorded before these events existed is exported as "not recorded". In `openmerge` mode, compaction must not discard decision evidence inside the operator's configured retention window. Every deployed wire v1 payload stays byte-for-byte unchanged ([ADR 0002](0002-storage-and-state-model.md)).

**Liveness and readiness are separate.** `GET /v1/live` answers without authentication whether the process and its database are reachable, with no queue detail. It is the only probe an orchestrator should use to restart the process. `GET /v1/health`, authenticated like the status API, reports for each queue:
- its lifecycle and readiness, with reasons;
- its write-restriction state and its age;
- outstanding outside-queue warnings;
- the time of its last successful reconciliation;
- its next scheduled reconciliation, the effective deadline after any webhook backstop and quota or error backoff.

A queue is **overdue** when it passes that deadline plus a bounded grace. Dormant and retired queues without obligations are excluded; settlement-only queues are watched until their attempts resolve. `/v1/health` returns non-200 when any queue is overdue or violated. It is for alerting, never for restarts, so one repository's problem cannot take down a process serving others.

**Running OpenMerge as critical infrastructure.** In `openmerge` mode an outage stops merges. The expected posture until high availability (Phase 6) is that merges stop and break-glass is the documented hotfix path. The runbook covers:
- alerts on overdue queues, block age, unverified age and outside-queue landings;
- database backups, which are now the compliance record's backups;
- custody and rotation of the App's private key, since whoever holds it can land anything;
- downgrade, since older binaries reject both the setting and the new events.

### Implementation

Each part lands as its prerequisites exist:

1. `setup import-protection`, as a read-only report usable in `github` mode.
2. After ADR 0007 Stage A: `policy_authority` and `break_glass`, the two write rulesets, the `write_restriction` source, rule-aware content validation, the pull request check message, outside-push warnings with `openmerge admin acknowledge`, `/v1/live` and `/v1/health`. Its exit test is the switch procedure on an organization sandbox repository.
3. After Stage A decision snapshots exist: `audit export`.

## Consequences

- A repository in `openmerge` mode has one policy surface for what lands, versioned with its code and changed only through the queue with owner approval. Agents only deal with OpenMerge. Content rules and break-glass remain GitHub settings, and the docs say so.
- Repositories that do nothing see nothing change: `github` remains the default.
- OpenMerge becomes critical infrastructure for those repositories. It reports authority as verified, unverified or violated rather than claiming more than it can see.
- Compliance evidence moves from GitHub's settings to OpenMerge's log. The audit export is credible to the extent its terminal digests are retained outside OpenMerge; teams that cannot accept that stay on `github`.
- Other forges need an equivalent write restriction that can express App-only refs plus a break-glass actor. That is a per-forge design question for Phase 5, with contract tests, not a guarantee.
- This project's own repository is on a personal account but already runs the App-only fence ruleset, so the base ruleset can probably be tested on it too; implementation confirms that empirically before relying on it.

## Alternatives considered

- **Keep both systems.** It is the default and stays available, but teams keep two sources of truth, one of which the queue bypasses.
- **Make OpenMerge a required status check and keep GitHub's merge button.** GitHub stays the authority, but its merge does not land exactly what was tested, and serialization moves back to people. A GitHub merge queue adapter remains a Phase 5 option.
- **Drop GitHub protection entirely.** Anyone with write access could push around OpenMerge, so its policies would be suggestions.
- **Fail closed on unverified protection.** It would block nearly every installation, since the App usually cannot see bypass lists, and it would repeat the wedge that #35 fixed. Reporting unverified state honestly, and blocking only on what is visibly wrong, keeps the queue running without overclaiming.
- **Have OpenMerge compile its policy into GitHub rulesets.** GitHub's rules cannot express approval by change class, accountable humans or post-merge verification, and they are evaluated on pull requests that the App bypasses anyway.
