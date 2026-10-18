# 0008. Make OpenMerge authoritative for queue policy behind forge write restrictions

- **Status:** Accepted
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

**Prerequisites.** `openmerge` mode depends on ADR 0007 Stage A: approval policy, owner requirements, records of pushes made outside the queue, and decision snapshots. A repository that requires reviews today cannot express that requirement equivalently without Stage A. The configuration loader rejects `policy_authority: openmerge` until those features exist. This ADR amends ADR 0007 in one respect: in `openmerge` mode, owner approval for policy-class changes is mandatory even where a queue has no `approvals` section, and `--accept` during import cannot waive it. Policy-class changes include edits to OpenMerge's configuration files and to `CODEOWNERS` itself. The loader also rejects `openmerge` mode while `CODEOWNERS` at the governing commit does not cover OpenMerge's configuration paths and `CODEOWNERS`, so coverage cannot be dropped later by an ordinary change.

### What GitHub keeps

**Two write rulesets, managed by `openmerge setup rulesets --authority openmerge`:**

- `openmerge-base`: targets the default branch and every queue's `base` selector, translated from OpenMerge's glob dialect into ruleset ref patterns (not copied verbatim) and keeping discovery's exclusion of `openmerge/` refs, so a newly created matching branch is covered before its queue activates. It restricts creation, update, deletion and non-fast-forward pushes. Its bypass list is the App and the declared break-glass actor.
- `openmerge-refs`: the existing App-only `openmerge-fence` ruleset, extended to `openmerge/queue/`, `openmerge/green/` and `openmerge/verify/` with the same recursive patterns the fence uses today (`openmerge/fence/*` and `openmerge/fence/**/*`, and likewise for the others). Only the App may bypass it, which also keeps queue refs App-only as ADR 0007 assumes, so no one else can push a commit to a test ref. Break-glass never reaches these refs, so it can repair a base without rewriting the queue's evidence.

In Stage B, submission refs (`openmerge/submit/**`) stay writable by writers, as ADR 0007 requires. Setup preserves other rulesets and never edits inherited ones.

Because `openmerge-base` restricts creation, creating a branch that matches a base selector (cutting `release/1.3`, say) becomes an App or break-glass action. The contributing-guide snippet from `import-protection` says so.

**Content rules stay on GitHub only where every operation can satisfy them.** Rules about commit content rather than about who may write (linear history, signatures, file path, file size and commit message restrictions) can remain in a ruleset without an App bypass, so GitHub applies them to the App's pushes too. This needs new validation: today any active ruleset on the base without an App bypass is a blocker. The runtime and setup checks gain rule-aware validation by merge method and by affected ref, covering queue construction, the atomic base-and-fence landing, verify pushes and green ref updates:

- Required signatures are unsupported on any ref that receives OpenMerge's unsigned generated commits, markers or fence commits. A signature rule confined to the base could in principle hold for fast-forward landing of signed contributor history; until validation can prove that per merge method, it is unsupported.
- Linear history is incompatible with the `merge` method, and with fast-forward when contributor history contains merges.
- Commit message patterns must accept OpenMerge's generated messages and trailers, including generated reverts.
- Push rules on paths and file sizes apply to every push, including queue builds, and are compatible only when contributors' commits already satisfy them.

Unknown rule types and combinations are unsupported and block the queue, as today.

**Rules shaped around pull requests move to OpenMerge.** Required reviews, code owner review and required status checks belong in `.openmerge.yml`: approval policy, owner requirements and `merge_when`. Last-push approval, conversation resolution and required deployments have no OpenMerge equivalent yet. Keeping them on GitHub does not help: with the App on their bypass list they never gate the queue's landing, and without it they block the landing push. They would gate only break-glass merges. A team that relies on them accepts the gap explicitly during migration or stays on `github`.

### Enforcement and drift

**The write restriction is its own rules source.** Like `rulesets`, `classic_protection` and `permissions` today, `write_restriction` has its own block and revision under the source model from #30 and #35, so its clears never touch fence or other findings. Each observation is one of:

| Observation | Effect |
|---|---|
| **Verified:** the base and refs rulesets are visible, target every required ref, and their bypass lists are exactly the declared actors | Admit work |
| **Violated:** a required ref is visibly unprotected, or a visible bypass list includes an undeclared actor | Block the affected queues until an observation that can see the violated element shows it resolved |
| **Unverified:** the rulesets are present, but their bypass lists are hidden from the installation | Does not block on its own, and is reported as "authority unverified" in status, health and the API (not on pull request checks, where authors cannot act on it). A violation already recorded stays until an observation that can see the violated element shows it resolved: a repaired structure clears once the runtime sees it, while an unauthorized bypass actor seen earlier clears only on a visible bypass list, from the runtime or a recorded `--verify`; a list that becomes hidden clears nothing |
| **Read failure or unavailable source** | Keep the source's previous state; a queue with no previous state starts as unverified |

Enforcement does not depend on observation: GitHub rejects ordinary writers whether or not the App can read the bypass list. Unverified means only that a change to the bypass list would be noticed when the added actor pushes (an outside-queue landing) or when an operator re-verifies. GitHub returns bypass lists only to callers with write access to the ruleset, so unverified is the expected steady state. Operators verify bypass ownership with `setup rulesets --authority openmerge --verify`, which uses a repository-admin token as `setup rulesets` does today (inherited organization rulesets may need an organization owner) and, like `openmerge admin acknowledge`, writes a durable verification event to the database with operator credentials, which the runtime reports and which counts as a seeing observation for clearing a violation. Status shows two ages: when protection was last observed, and when it was last verified. The runbook schedules re-verification. Granting the App Administration read lets it read classic protection, which today returns 403. Setup recommends that grant in `openmerge` mode and names the privilege it adds. Refresh and acknowledgement never clear a violation, and settlement always runs.

**Every applicable protection is still validated.** An App bypass on `openmerge-base` does not bypass other rulesets, inherited organization rulesets or classic protection: GitHub applies them together. The runtime keeps validating all of them. A rule shaped around pull requests is only a warning when it cannot affect the App's push, because the App is on its own bypass list. A rule that would block the landing push remains a blocker. ADR 0007 Stage B still refuses direct submission while any rule that needs a pull request is active, bypassed or not.

**The trust boundary is stated, not implied.** The write rulesets constrain ordinary writers under the observed rules. Repository rules administrators, organization owners, whoever holds the App's private key, and the declared break-glass actor remain trusted. A push by any of them is detected after the fact, not prevented. Detection does not depend on catching a ruleset change: observed base movements that OpenMerge did not make, and the intervening history it can recover, are recorded as landed outside the queue (ADR 0007), even if the protection was disabled and restored between observations. A push followed by restoring the old tip between observations can leave nothing to detect. OpenMerge reports what it observed and when; it does not claim continuous exclusivity.

**Outside pushes are flagged until acknowledged.** In `openmerge` mode, an outside-queue landing is shown as a warning in status, health and the API for its base. Landings continue meanwhile. An operator acknowledges it with `openmerge admin acknowledge --queue <key> --event <id> --sha <sha>`, which runs against the database with operator credentials, because it is not a pull request action. The command records the operator-supplied name, the target event and SHA, and the time, atomically and idempotently. Acknowledgement records that someone with operator access saw the landing. It does not record who pushed, does not approve the change, does not clear rules or suspect blocks, and does not move the green ref.

### Migrating protection into OpenMerge

**`openmerge setup import-protection --repo owner/name` reports how existing protection would translate.** It reads rulesets, inherited rulesets and classic protection with a repository-admin token, and is useful in `github` mode as an audit. It never edits the repository. For each requirement it prints a proposed `.openmerge.yml` change and a classification: **equivalent**, **stricter**, **weaker** or **unsupported**.

- **Reviews** map only to ADR 0007 approval policy, with eligible, distinct reviewers and blocking requests for changes. Plain `#approved-reviews-by` conditions are not equivalent, because they don't filter for eligibility. Head-bound approvals are stricter than GitHub with stale-review dismissal off. With it on, any case where GitHub would invalidate an approval after a merge-base or diff change but OpenMerge would keep it is classified weaker and needs acceptance. Last-push approval is unsupported: excluding the submitter does not exclude a later pusher.
- **Code owner review** maps to owner requirements over `CODEOWNERS` at the governing commit. The report compares the effective `CODEOWNERS` for every served base with the default branch's and lists every difference, since ADR 0007 deliberately reads only the default branch's.
- **Required status checks** map to `merge_when` `check-success` terms only where the name identifies one producer. A check pinned to a specific App and a name shared by a commit status and a check run are reported as weaker or unsupported. OpenMerge treats neutral and skipped results as failures where GitHub accepts them, which is stricter, and the report says so because it can stall changes GitHub would have merged. For each check, the report says whether any workflow producing it runs on pushes to `openmerge/queue/**`; a check that runs only on pull requests is unsupported until CI changes. Workflows are inspected at each base's pinned tree. Missing queue-push evidence, unresolved reusable-workflow callers, path filters and unresolved producer names cannot establish equivalence. GitHub's strict up-to-date requirement maps to strict verification ([ADR 0005](0005-verification-modes.md)); a post-merge queue is reported as a relaxation.
- **Conversation resolution and required deployments** are unsupported.
- **Other writers of each base** are reported as blockers to the switch: release jobs, version-bump CI, backport bots, and any inherited bypass actor that can actually write a served base past `openmerge-base`. The report inventories every inherited bypass entry but blocks only on effective authority gaps, since bypassing one ruleset does not bypass another that still applies. A bot cannot be the break-glass actor (a team or the repository-admin role), so each writer must route through the queue or stop the switch.
- **OpenMerge's own configuration paths** must be covered by `CODEOWNERS`. In `openmerge` mode a policy change needs owner approval (ADR 0007's `policy` label); without that, any queue-eligible writer could propose weaker policy, a separation-of-duties regression from GitHub settings that need an administrator.

**Maintainer-approved consequential clarification, 2026-10-18 (ADR 0007
code-owner requirement).** Code-owner review maps to a rule requiring
`{code_owners: true}` for every **owned** file
([ADR 0007's Policy approvals](0007-changes-not-prs.md#policy-approvals-stage-a)).
Until that requirement is **activated** (PR 11b publishes the loader), the report
classifies code-owner review as unsupported. Intentionally ownerless entries
add no code-owner obligation. Unresolved owners, including mixed entries and
unknown team membership, fail closed even alongside a resolvable owner; report
this as stricter where GitHub would accept an available owner's approval.
The existing stale-dismissal caveat for reviews applies to code-owner approvals
too: with dismissal on, any case where GitHub invalidates an approval after a
merge-base or diff change but OpenMerge retains it is weaker and needs acceptance;
with dismissal off, head-bound approvals are stricter.

A faithful translation of "N approvals + code-owner review" keeps the count as
`approvals.default` and adds two independently matching rules. For N = 2:

```text
approvals:
  default: {any_writer: true, count: 2}
  rules:
    - when: {paths: ["**"]}
      require: {any_writer: true, count: 2}
    - when: {touches: ["**"]}
      require: {code_owners: true}
```

An empty diff still gets the count, but not because the `paths` rule matches it:
with no changed paths, every-file facets evaluate as unknown rather than true, so
missing evidence can never definitely match an exemption. An unknown rule is
enforced as if it matched, and because no rule definitely matches, the count-bearing
default applies as well. `touches` has any-file semantics and is false for an empty
diff, so there is no owner obligation, as on GitHub. For a nonempty diff both rules match independently, so
owner review cannot displace the imported N approvals; one eligible approval
may contribute to both requirements. The default alone does not preserve the
count once a rule definitely matches. `import-protection` must not propose or
classify a `{code_owners: true}` default alone as equivalent: it is rejected by
the loader and would permit uncovered-only changes without the imported floor.

The command exits nonzero while anything is weaker, unsupported or blocking, unless the operator passes `--accept` with the list of policy relaxations they accept. Installation prerequisites (a served default branch, the bypass GitHub must accept, `CODEOWNERS` coverage, mandatory policy approval, every other writer resolved) cannot be accepted away. Unhandled pull-request parameters are unsupported and named explicitly. Effective authority is computed from readable principal memberships; unresolved overlaps remain blockers. CODEOWNERS owner identity and eligibility remain "to verify at switch". Overlapping review counts use the maximum in a reconciled queue fragment; general code-owner review is reported unsupported and is not drafted until `{code_owners: true}` is activated. Acceptance IDs include reported requirement parameters so changed counts or App bindings invalidate acceptance. Accepted items are written into the proposed configuration as comments, so their durable record is the reviewed pull request that lands it.

**The switch is a coordinated transition, not an atomic one.** In order:

1. **Preflight:** `import-protection` reports clean or explicitly accepted, and setup confirms GitHub accepts the App and break-glass bypass on the repository.
2. **Install the write rulesets** while the old protection stays in place, and verify them with the admin token. Setup takes the break-glass actor from the proposed configuration (or `--break-glass`), and step 4 checks the landed configuration's resolved actor against what was installed.
3. **Land the configuration** with `policy_authority: openmerge` through the queue, under the old protection.
4. **Wait** until every affected queue reports the new configuration digest, settles any attempt started under the old one, and shows no violation.
5. **Remove** only the reviewed, redundant pull-request rules.

Serialize other policy edits during the switch. Rollback reverses the order: restore the GitHub rules first, keeping the App on their bypass lists so the queue can still land, then land `policy_authority: github`. The two rulesets cannot be installed atomically, so step 2 can fail part-way. Setup captures the prior ruleset settings first, reports exactly what it changed, and supports resuming idempotently or restoring the captured settings. Either way the App's landing and settlement path keeps working, and the repository stays on `github` until step 3. The procedure is verified on an organization sandbox repository before it ships.

### People and agents

**The merge button stops working for ordinary writers.** With `openmerge-base` in place, GitHub rejects a merge through its interface by anyone not on the bypass list. Where OpenMerge reports a check on a pull request, the check states that the repository merges through OpenMerge, with the `@openmerge queue` command and the change's queue state. `import-protection` prints a short snippet for the repository's contributing guide. Docs tell teams to disable GitHub auto-merge, which would otherwise fail silently. Members of the break-glass actor can still merge through GitHub, and those merges are recorded as outside-queue landings.

**CI stays where it is.** OpenMerge does not run CI. It decides which checks must pass and keeps its existing rules for which runs count.

### Evidence and operations

**`openmerge audit export` writes what the log holds, and says what that proves.** It runs against the database with operator credentials, for one repository and a time range, and writes JSON Lines with a manifest. The manifest records the repository identity, filter, source watermarks, record count, terminal digest and serialization version. Each record references its source event and carries the SHA-256 of the previous record.

Records are serialized and ordered deterministically, so the same range always exports identically. Without an anchor, the chain only detects internal inconsistency in an export, not a deliberate rewrite followed by recomputation. It detects edits to the log only where an earlier export's manifest and terminal digest were recorded outside OpenMerge, for example signed and stored with the organization's audit records. It does not make the database tamper-proof. It does not prove that a person rather than their token acted, or that protection held between observations.

The export only reads, so the facts it reports must be recorded first. This ADR adds new, durable event types, with golden fixtures, for write-restriction state changes, verifications and acknowledgements. Decision snapshots come from ADR 0007 Stage A; this ADR extends them to record each required check's name, producer and result, which Stage A's snapshot does not yet list. Separate tested and landed commits and later verification come from ADR 0005. History recorded before these events existed is exported as "not recorded". Once a repository has used `openmerge` mode, compaction must not discard decision evidence inside the operator's configured retention window, even after switching back to `github` or retiring a queue. Every deployed wire v1 payload stays byte-for-byte unchanged ([ADR 0002](0002-storage-and-state-model.md)).

**Liveness and readiness are separate.** `GET /v1/live` answers without authentication whether the process itself is responsive, with no queue detail and without depending on the database, so a shared database outage does not restart-loop every process. It is the only probe an orchestrator should use to restart the process. Database reachability is reported by `/v1/health`. `GET /v1/health`, authenticated like the status API, reports for each queue:
- its lifecycle and readiness, with reasons;
- its write-restriction state, with when it was last observed and when it was last verified;
- outstanding outside-queue warnings;
- the time of its last successful reconciliation;
- its next scheduled reconciliation, the effective deadline after any webhook backstop and quota or error backoff.

A queue is **overdue** when it passes that deadline plus a bounded grace, and **degraded** when its last successful reconciliation is older than a bounded limit however its next attempt is scheduled, so a worker that keeps failing on schedule cannot hide behind ever-later backoff deadlines. Dormant and retired queues without obligations are excluded; settlement-only queues are watched until their attempts resolve. `/v1/health` returns non-200 when the database is unreachable or any queue is overdue, degraded or violated. It is for alerting, never for restarts, so one repository's problem cannot take down a process serving others.

**Running OpenMerge as critical infrastructure.** In `openmerge` mode an outage stops merges. The expected posture until high availability (Phase 6) is that merges stop and break-glass is the documented hotfix path. The runbook covers:
- alerts on overdue or degraded queues, block age, time since last verification and outside-queue landings;
- database backups, which are now the compliance record's backups;
- custody and rotation of the App's private key, since whoever holds it can land anything;
- downgrade, since older binaries reject both the setting and the new events.

### Implementation

Each part lands as its prerequisites exist:

1. `setup import-protection`, as a read-only report usable in `github` mode. Implemented as an admin-token, GET-only audit with stable JSON item IDs, repeatable `--base`/`--accept`, local `--out` drafts, inherited/classic protection reads, CODEOWNERS comparisons and workflow-trigger inspection. Approval fragments require the release implementing ADR 0007 Stage A; owner requirements remain annotated pending its full schema. Installation prerequisites cannot be accepted; bypass acceptance remains "to verify at switch". See the [operating guide](../operating.md#auditing-protection-before-a-policy-authority-switch).
2. After ADR 0007 Stage A: `policy_authority` and `break_glass`, the two write rulesets, the `write_restriction` source, rule-aware content validation, the pull request check message, outside-push warnings with `openmerge admin acknowledge`, `/v1/live` and `/v1/health`, and the new durable event types for write-restriction state changes, verifications and acknowledgements, so the later export has records from the first day of `openmerge` mode. Its exit test is the switch procedure on an organization sandbox repository.
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
