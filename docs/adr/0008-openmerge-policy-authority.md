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

#### Implementation plan

This plan covers Parts 2 and 3. Part 1 is implemented. Stage A's
[exit report](../research/stage-a-acceptance.md) and
[organization-sandbox evidence](../research/stage-a-sandbox-evidence.md) unblock
Part 2. They do not authorize a live deployment. This section plans the accepted
Decision without changing it. PR labels below are implementation steps, not
GitHub PR numbers. Proposed flags, events, and files are explicitly future work.

##### Boundaries and current integration points

Keep forge reads in `internal/serve`, effects behind forge capabilities, and
queue decisions as pure folds over durable inputs. Do not put GitHub calls in
`queue.Plan`, the config loader, or the condition evaluator.

- `internal/config/config.go` (`Load`, `Find`), `queues.go` (`ResolveBranch`,
  `Queue.Digest`), and `schema/openmerge.v1.json` own public config. `Find`
  recognizes four config paths. Authority must cover all four, plus the
  effective CODEOWNERS file, rather than only `.openmerge.yml`.
- `internal/serve/reconcile.go` (`configuration`, `loadConfiguration`) reads
  governing default-branch policy. Today `selected.Approvals != nil` alone
  installs `stageAPolicy`. `internal/changepolicy/digest.go` (`PolicyDigest`)
  returns no component without approvals, and
  `internal/executor/landing_decision.go` rejects a nil approvals policy. All
  three need an authority activation path. A check in the YAML loader alone
  cannot enforce mandatory policy-owner approval.
- `internal/serve/rules.go` (`repositoryRules`) observes independent sources.
  `internal/queue/rules_sources.go` owns their revisions and stale-clear
  protection. Add `write_restriction` without letting its clears affect
  `rulesets`, `classic_protection`, `permissions`, or block sources.
- `internal/forge/github/runtime_rules.go` (`ValidateRepositoryRules`) currently
  substitutes an App-only bypass when the list is hidden. Authority observations
  need the original visibility, not that substitution. `rules.go`
  (`ValidateRules`) currently rejects active unbypassed content rules.
  `classic_protection.go` (`ValidateClassicProtection`) remains an independent
  input. `setup_rules.go` (`SetupRules`, `planRules`) currently manages
  `openmerge-fence` and can add App bypasses to other repository rulesets. The
  authority setup path must instead preserve unrelated and inherited policy.
- `internal/forge/forge.go`, `identity.go`, `internal/forge/fake`, and
  `internal/forge/forgetest/contract.go` own shared driver contracts.
  `internal/gitops/{repo,build,metadata,fence,fast_forward,acquisition}.go`
  implement real refs, generated commits, acquisition, and atomic landing.
  Extend these boundaries rather than adding shell pushes in `serve`.
- `internal/store/outside_push.go` (`ReadOutsidePushAudit`,
  `CommitOutsidePushAudit`) already deduplicates durable base moves after
  overlapping attempts settle. `internal/serve/outside_push.go` enriches them
  independently of settlement. `queue.OutsidePushLimit` is only a bounded recent
  projection, currently 20. It cannot be the acknowledgement ledger.
- `internal/store/landing_start.go` (`StartLanding`) atomically records fresh
  eligibility, decisions, and start. `internal/queue/landing_decision.go`
  explicitly reserves separate check evidence. Preserve that transaction and
  `internal/status/decisions.go`'s distinction between tested, intended landing,
  and confirmed landed SHAs.
- `cmd/openmerge/serve.go` constructs the status listener using
  `internal/status/http.go` (`DynamicHandler`, `Handler`). Authentication
  currently precedes routing. A database-independent `/v1/live` needs an early,
  exact route, before authentication or journal reads. Scheduler facts come
  from `internal/serve/{serve,webhooks,rules}.go`, not inferred status timestamps.
- `internal/store/journal_reader.go` (`OpenJournalReader`) and `journal.go`
  (`Journals`) provide read-only, repeatable-read precedent. `Journals` loads
  whole histories for explicit keys. Export needs a bounded streaming reader
  that also discovers retired queues in the selected repository.

Each PR targets a reviewable unit, approximately 1–2.5k changed lines including
tests. Split further above 3k. A split inherits its parent's deployment class.
No consumer invents a second actor, observation, or digest contract.

##### Sequence and dependencies

| PR | Scope | Depends on | Trial class |
| --- | --- | --- | --- |
| 1 | Candidate authority contracts, digest, and wire types | Complete Stage A | Inert |
| 2 | Visibility-preserving authority observations | 1, issue #83 | Inert |
| 3a | Ref compilation, content validation, and pre-push integration | 1, 2 | Hold |
| 3b | Reversible two-ruleset setup | 2, 3a | Inert |
| 4 | Durable write-restriction observations and admin verification | 1, 2, 3a | Inert |
| 5 | Durable acknowledgement and outside-push warnings | 1, 4 | Inert |
| 6 | Authority status/CLI/API, health, liveness, and deadlines | 4, 5 | Hold |
| 7 | Human-facing PR check instructions | 1 | Hold |
| 8 | Atomic per-check landing evidence | 1, 2 | Hold |
| 9 | Publish config and activate complete authority enforcement | 3b, 4, 5, 6, 7, 8 | Hold |
| 10a | Deterministic read-only audit export | 4, 5, 8 | Inert |
| 10b | Audit reproducibility, retention, and anchor acceptance | 10a | Inert |
| 11 | Organization switch, break-glass, drift, and rollback acceptance | 9, 10b | Inert |

This is **13 PRs in 11 workstreams**. After 1, 2 precedes the rule work;
7 and 8 can proceed independently. After 4, acknowledgement and then health
can land while setup finishes. PR 9 is the only runtime activation and public
schema release. PRs 5–8 land as dormant components before it. Export can be
developed against persisted fixtures before activation. Part 2 exits at 11; Part 3 requires 10b plus a real authority export
from 11. Neither is claimed complete by merging components alone. Inert describes
that PR's own behavior; it does not permit deployment of held dependencies.
In particular 3b/4 depend on Hold 3a, 5 on 4, and 10a on Hold 8; their binaries
wait for B to drain. PR 11's sandbox may use held code in isolation.

All PRs follow these storage and acceptance rules:

- New persisted events are additive **wire v1** types, with explicit structs,
  lowercase snake_case keys, strict unknown/missing-field rejection, and value
  validation. Keep every deployed event/action encoding and golden fixture
  byte-identical. Do not extend deployed `config_applied`,
  `landing_decision_recorded`, `approval_eligibility_observed`, or
  `outside_push_recorded` payloads. Each new concrete type gets exactly one
  `event_*.json` registry fixture under `internal/queue/testdata/wire/v1`.
  `fixtureVariantNames` in `wire_test.go` rejects duplicates. Put additional
  visibility, invalid-input, and outcome cases under `variants/` or table tests.
- Each new projection proves immutable `Apply`, historical replay, stale input
  handling, and full replay versus snapshot-plus-tail state and next actions.
  The current snapshot codec is version 6, used by the outside-push cursor but
  not general store compaction. Bump its version when its shape changes, retain
  old decoding, and test the persisted cursor's upgrade. Never enable pruning.
  PR 1 owns the 6 → 7 codec transition and the downgrade gate below; merely
  deploying its decoder must not rewrite legacy cursors to version 7.
- **Migration decision:** no PR in this sequence adds SQL migrations. Remove
  the formerly optional verification, attempt, and export indexes and both
  proposed acknowledgement indexes; the per-PR replacements are specified
  below. Reuse `events_queue_watermark`, `events_queue_dedupe`, and existing
  reporting indexes. There is no added migration lock or startup DDL duration
  at ~5k events (or larger journals); paged queue scans instead cost O(N) in
  that queue's history and export costs O(N) in the selected repository.
  Retain isolated ~5k-event and larger-fixture query plans and timings, without
  claiming a measured latency in this plan. Bound pages/memory and command
  deadlines; cancellation returns failure without a receipt or valid export.
  Any later design that adds a migration is **Hold (migration)**, even if its
  runtime behavior is inert, and requires a plan amendment before deployment.
  `Store.Open` runs all pending migrations in one transaction under the migration
  lock: `CONCURRENTLY` is impossible. That amendment must name each PostgreSQL
  lock, estimate and measure ~5k-event duration and growth, set lock/statement
  timeouts, and prove transactional failure/rollback and retry. Drain every
  worker across hosts after period B before startup with that migration, as for
  Stage A 006/007. No implicit “if needed” indexes remain.
- **[Issue #83](https://github.com/CanadaApollo6/openmerge/issues/83) is a merge
  prerequisite of PR 2.** Consume its shared `internal/forge/validation.go`
  validators and `forgetest` contract-suite structure. #83 owns the existing
  fake/real parity work; this plan does not reimplement or enumerate that scope.
  PR 2 adds only authority-reader and visibility cases. Later new capabilities
  extend the same shared validators/contracts rather than creating a fork.
- **First emission and downgrade:** drain old workers across hosts before any
  new journal type or persisted version-7 snapshot, including explicit admin
  commands against still-`github` queues. Capture and test a pre-emission DB
  backup and record the binary compatibility boundary. Switching mode is not
  binary rollback: old decoders reject unknown journal types, new source enum
  values on old types, and version-7 snapshots. PR 1 must retain version-6
  encoding for legacy state/cursors and prove old-binary journal and cursor
  decoding after an inert deployment, not just new-binary replay. Once a v7
  cursor or new event has been persisted, use a compatible binary or restore
  the verified pre-emission backup while all workers are stopped. If later
  landings exist, reconcile actual GitHub refs and recover their evidence before
  restarting; never blindly resume a stale backup or delete evidence to downgrade.
  This drain is operator-controlled; there is no automatic binary-capability
  check today. Retain the drained-worker inventory and backup receipt.
- A PR touching git or forge includes an automated real-git path using the
  CLI-backed fixtures in `internal/gitops/contract_test.go` and the worker
  pattern in `internal/serve/stage_a_real_git_test.go`, with a fresh bare remote
  and cache. GitHub HTTP fixtures test transport; they do not prove GitHub
  enforces rules. Each such PR also retains its named organization-sandbox
  result below, or remains unaccepted until the dependent sandbox run supplies
  it. Fake-forge success alone never completes a forge PR.
- Evidence files name commit/binary revisions, config commits and digests,
  fixture seeds, commands, numeric exits, timestamps, source event IDs, and
  observed remote SHAs. Retain sanitized before/after API bodies and an artifact
  hash manifest. Distinguish planned, executed, failed, and unavailable cases.
  Never infer grant snapshots or setup exits from a later successful landing.

##### PR 1: Define candidate authority policy and durable contracts

- **Scope:** introduce pure authority types shared by config, setup, serve, and
  queue, outside GitHub transport code. Choose a discriminated break-glass actor:
  proposed YAML is `break_glass: {team: Smart-Data-Ohio/owners}` or
  `break_glass: {repository_admin: true}`, exactly one. Reject users, bots,
  multiple actors, false role values, and unknown keys. A team resolves to
  organization and team IDs; a repository role uses GitHub's documented role
  identity resolved/validated by the adapter, never a guessed numeric ID.
- **Config/schema:** keep `policy_authority` and `break_glass` rejected by both
  `config.Load` and `schema/openmerge.v1.json` until 9. Candidate fixtures define
  their future agreement. Omitted authority and explicit `github` normalize to
  the existing digest. Reject `break_glass` in `github` mode to avoid dormant
  ambiguity. In `openmerge` mode fold authority, declared stable actor, mandatory
  owner semantics, and the complete canonical base-selector set into each
  effective queue digest. Selector changes affect write coverage even if an
  existing branch's selected queue is unchanged. Do not change legacy approval
  digests or include mutable team membership in policy identity.
- **Wire/events:** own `policy_authority_applied`, `write_restriction_observed`,
  and `write_restriction_verified`, with one registry fixture each. The first
  binds governing commit, effective digest, authority, declared/resolved actor,
  default branch, and selector/coverage digest. Use mode-specific variants so
  `github` does not require a break-glass actor. Observations bind that policy,
  source basis revision, observation time, visibility per element, ruleset IDs,
  observed actors, coverage, and typed findings. Verification adds the supplied
  operator name, verification time, and explicit `proposed` versus `applied`
  scope. Freeze PR 4's policy/protection fingerprint and activation binding in
  these fixtures. Record absence, hidden, and read failure distinctly. No raw
  credentials or arbitrary API bodies enter events.
- **Forge contract:** define an optional typed observation capability and inputs
  for expected coverage, actor identity, and merge method. Separate read results
  from their pure interpretation. Visibility must survive decoding. No network
  implementation or production invocation yet.
- **Migrations/tests/evidence:** no SQL migration. Extend snapshot codecs for
  authority pins and source evidence: version 7 decodes versions 2–6 as well.
  Keep a version-6 encoder with the exact old shape for state without authority
  evidence, including the persisted outside-push cursor; do not eagerly upgrade
  legacy cursors on encode. New authority state uses version 7 only after the
  first-emission drain/backup gate. The old binary rejects v7 even if no new
  journal events exist, so an unconditional 6 → 7 write would make this PR
  ineligible as Inert. Test that refusal and old-binary decoding of untouched
  journals and newly written legacy cursors. Prove strict wire validation,
  fixture equality, actor/digest vectors, and snapshot-tail equivalence. Attach
  candidate loader/schema agreement and offline differential replay. Real-git
  policy-file coverage is exercised by 9; no public config activation here.

##### PR 2: Read actual authority protection after #83

- **Prerequisite/scope:** merge #83 first and consume its shared validators and
  contract-suite structure. Add only the new typed authority reader and its
  visibility cases to the fake and GitHub driver. Reuse installation auth,
  conditional reads, pagination checks, quota classes,
  and stable `IdentityReader.ResolveTeam` identities. Read repository and
  inherited rulesets, details, enforcement, conditions, rule parameters, and
  bypass visibility. Missing bypass JSON is not an empty visible list.
  Preserve successful partial evidence when a later page fails; absence requires
  a completed authoritative inventory. Generic 403/404 cannot prove absence.
  Keep classic protection and installation permission sources independent.
- **Wire/config/migrations:** use 1's types without emitting events. No new wire
  types, public schema changes, SQL migrations, or alteration of legacy
  `ValidateRepositoryRules` behavior. Add a separate authority reader until 9
  selects it. App Administration read is diagnostic/adoption guidance, never
  an automatic grant or a new requirement for `github` queues.
- **Tests:** extend #83's shared contract suite only for the authority-reader
  inputs and visibility semantics, with GitHub HTTP fixtures. Cover hidden
  versus visible-empty lists, partial inventories, inherited visibility,
  actor resolution in authority observations, permission denial, and quota
  responses. Existing boundary/parity coverage remains owned by #83.
- **Real acceptance/evidence:** use real git to enumerate nested bases and the
  protected ref families, then compare observations with actual sandbox rules.
  Retain separate installation-token and admin-token GETs showing what each
  caller can see, plus actual App/installation/grant IDs. If GitHub exposes more
  than expected, report the observed behavior; simulated hidden fixtures remain
  required. Attach the parity matrix and sanitized read transcript.

##### PR 3a: Compile ref coverage and validate content by operation

- **Scope:** share a pure compiler/validator between authority setup and runtime.
  Translate `config.Queue.MatchesBase`'s slash-aware grammar to GitHub patterns.
  Include the default branch and future matching bases, exclude `openmerge/`
  as discovery does, and reject selectors whose exact semantics cannot be
  represented. Never install a silently weaker or broader approximation.
  For 3b's reviewed selector transition, validate containment of required current
  base coverage, allowing the exact union of old/proposed selectors without
  relaxing actor lists, exclusions, or App-only refs. Extra base coverage is
  reported, not treated as a loss of current protection that would prevent the
  config edit from landing. The compiler still translates each selector exactly;
  this is a deliberate setup transition, not a glob approximation.
- **Contract:** evaluate all applicable repository, inherited, push, and classic
  rules for each operation: queue build/update/delete, strict landing's atomic
  base and fence update, fast-forward's contributor head and marker, verify
  pushes, green advances, and generated revert/deploy candidates. Rule parameters
  and combinations matter. Unknown rules block. PR/review/check rules warn only
  when the App actually bypasses that particular policy; bypass elsewhere proves
  nothing. No new CI counting or merge semantics are introduced.
  Define a bounded prepared-push object inventory: exact ref updates/deletions,
  expected old and proposed new SHAs, and every reachable commit introduced to
  each affected ref, with paths, blob sizes, messages, signatures, and parents.
  Acquisition supplies pinned contributor objects; generated objects are read
  directly from the same local session before publication. The fake and gitops
  share its new input/completeness contracts using #83's validators. A remote
  `PushContentReader` after `BuildTestRef` returns cannot enforce this boundary.
- **Pre-push integration (owned here, activated by 9):** refactor gitops to
  prepare → inspect/validate → push within its existing session/cache lock.
  Construct each commit exactly once using the existing builders and pass its
  actual OID to the pure validator; never regenerate a preview message/commit.
  `BuildTestRef` must validate after composing all commits/fast-forward markers
  and before its current push in `build.go`. Route `Land`, `EnsureFence`, and
  `AdvanceFence` through the same boundary after their fence objects exist,
  validating the entire atomic base-and-fence update before either ref moves.
  Cover verify, green, generated reverts/deploy candidates, ref creation, and
  deletion through that boundary too; no authority push path may bypass it.
  Supply an immutable, repository/policy-digest-bound validation context per
  operation from the executor, not mutable global policy on a shared Repo.
  Missing/stale authority context, incomplete inventories, or rejection aborts
  before any `git push`; uncertainty is not a warning. Existing started attempts
  still run settlement/read-back after an error. Keep leases, atomicity,
  credentials, commit bytes, and the no-context legacy path unchanged. Do not
  add fields to deployed action wire payloads to carry this transient context.
- **Content checks:** reject signatures for unsigned generated objects, including
  base-only fast-forward until proof exists. Reject linear-history merge builds
  and fast-forward contributor history containing merges. Validate message
  restrictions against real generated messages, metadata trailers, fences,
  markers, and reverts. Unsupported pattern semantics block rather than using
  Go regex as an assumed GitHub equivalent. Path/size rules require complete
  evidence for every pushed commit; uncertainty cannot pass validation.
- **Wire/config/migrations:** no new events, public config keys, or SQL changes.
  4 records typed findings using 1's events. Candidate selector validation must
  agree with the eventual loader; public activation remains 9. Legacy validation
  remains on its existing path until then. This PR is **Hold** because pre-push
  integration touches shared build/landing paths, even while authority is dormant.
- **Tests/real acceptance/evidence:** property vectors compare matching branches
  at multiple depths, omitted base, `release/*`, `release/**`, exclusions, and
  future branch creation. Build commits with real `gitops` for squash, merge,
  rebase, fast-forward, and available revert/verify/green paths. Retain commit
  graphs/messages and allowed/rejected operation matrices. For every push family,
  reject an actual intermediate commit, marker, or fence and assert a push-spy
  saw no invocation and real bare-remote refs are byte-for-byte unchanged,
  including both base and fence. Also assert accepted objects equal the OIDs
  inspected before push, and compare legacy commit IDs. On GitHub, apply
  compatible linear/message/path/size rules in the sandbox and prove actual
  App pushes succeed; incompatible signature/merge/unknown cases must be
  blocked before push. Unsupported/unavailable rule families stay explicitly
  unaccepted, not silently marked compatible.

##### PR 3b: Install and recover the two write rulesets

- **Scope:** extend `cmd/openmerge/setup.go` and GitHub `SetupRules` with
  `--authority openmerge`, a local proposed `--config` file, and optional
  `--break-glass` with the same actor grammar. This setup parser uses the
  candidate contract before 9 publishes the runtime keys. Preview is read-only.
  `--apply` requires the separate admin token with Administration write.
- **Effects/contract:** create `openmerge-base` with creation, strict update,
  deletion, and non-fast-forward restrictions, bypassed only by the App and
  declared actor in `always` mode. Extend/rename the locally owned
  `openmerge-fence` into `openmerge-refs` while preserving its fence coverage;
  add paired single-level and recursive queue/green/verify patterns. Only the
  App bypasses refs. Do not include future submission refs. Refuse duplicate or
  ambiguous managed rulesets and name collisions. Never edit inherited rulesets
  or add bypasses to unrelated content rules in this authority path.
  Teach legacy `planRules` to recognize `openmerge-refs` as the successor to
  `openmerge-fence` by managed name plus required fence coverage/App-only shape;
  keep its extra ref coverage, do not create a second fence ruleset, and reject
  ambiguous pairs. Test legacy setup preview/apply after the rename and rollback.
- **Setup-only permission guidance:** own authority-aware `setup permissions`
  and ruleset-preview diagnostics here. With `--authority openmerge` or the
  proposed authority config, recommend the App's **Administration: read** grant,
  naming the added ability to read classic branch-protection settings. Explain
  that it neither grants ruleset writes nor guarantees bypass-list visibility.
  Keep this recommendation out of runtime `ValidateInstallationPermissions`;
  unchanged `github` queues gain no requirement, warning, or request. Test missing
  and adopted grants in setup output and unchanged runtime permission traces.
- **Adding a base selector after activation (Decision refinement):** apply the
  Decision's coordinated setup-before-config transition to later selector edits
  too. Preview/apply setup with the reviewed proposed config before landing it. First install the union
  of current and proposed selector coverage, preserving the current actor and
  every existing base/fence obligation, then admin-verify that proposed binding.
  Only then queue the config edit with governing-owner approval. Wait for all
  affected queue digests and settlement before pruning obsolete coverage or
  creating a newly matching branch via App/break-glass. Failed setup leaves the
  old config active; never activate a new selector on an uncovered base. Repeat
  this setup-before-config sequence in 11 on a fresh `release/**` addition.
- **Rollback preflight:** provide a read-only proposed `setup rulesets
  --authority github --rollback-preflight --config github.yml` path using the
  complete prospective base inventory. Run the actual legacy `ValidateRules`
  and `ValidateClassicProtection` with admin-visible full policies, plus the
  installation's runtime permission checks, for every base and protected ref
  family. Include inherited/push/classic policies and renamed fence recognition;
  no relaxed authority interpretation or hidden-list substitution may certify
  compatibility. Report all blockers before landing the rollback config.
  A compatible content rule without an App bypass still fails legacy validation:
  an operator must explicitly approve adding an App bypass to that rule,
  disabling/removing the owned rule, or having its inherited-policy owner act.
  Name the resulting loss of App content enforcement and retain before/after
  evidence. If none is acceptable, stop in authority mode; never silently widen
  a bypass or rely on a failed post-switch queue to discover this gap.
- **Recovery:** add proposed `--backup <file>` and `--restore <file>` options.
  Before the first mutation, exclusively write a versioned, credential-free
  capture of exact prior managed settings, IDs, and planned edits. Read back
  before every mutation and reject observed concurrent edits. On partial failure
  print completed/pending operations; rerunning converges without duplicate
  rulesets. Restore only captured managed changes, with comparison against the
  expected post-apply state, and refuse to overwrite intervening operator edits.
  GitHub PUT has no CAS, so policy edits must be serialized operationally.
- **Wire/config/migrations:** no new queue events or SQL changes. Setup capture
  is a local recovery artifact, not journal verification. Keep public schema and
  runtime loader unchanged. Retain today's `github` setup behavior separately.
- **Tests/real acceptance/evidence:** HTTP fault injection at each write/read-back,
  crash after first ruleset, idempotent retry, restore, unknown retained fields,
  actor rejection, and inherited read-only policy. Real-git tests push actual
  generated objects to a bare remote with rejection hooks to verify leases and
  atomic failure handling. Real GitHub acceptance installs on the sandbox while
  old protection remains, repeats setup with zero changes, and verifies ordinary
  writers cannot create/update/delete/force bases or refs. App pushes work;
  break-glass reaches bases but never refs. Retain backups and actual rejection
  responses. Do not execute this acceptance against the live repository.

##### PR 4: Persist source observations and operator verification

- **Scope:** add `write_restriction` to the source model and implement its
  observer/fold using 1's events. A single append atomically updates visibility,
  typed findings, and that source's block/revision. Do not pair an observation
  with an independently racing generic clear. Initial state is unverified.
  Freeze the wire choice: derive runtime blocks directly from applied-scope
  `write_restriction_observed` / `write_restriction_verified` in the same fold.
  Do not emit `RulesSourceBlocked` or `RulesSourceCleared` with a new source
  enum value on those deployed types. The internal source model can grow;
  deployed fixtures stay unchanged. Old strict decoders would reject either
  that new enum value or these new event types, so this choice does not waive
  the first-emission drain/downgrade gate. Reads happen outside locks; recorded
  basis revisions guard clears.
- **Finding semantics:** index findings by violated element, such as a ruleset's
  target coverage or a bypass actor. A completed structural read may clear a
  repaired structure while a previously visible unauthorized actor remains
  blocked behind a now-hidden list. New definite negatives invalidate earlier
  passes; stale positive reads cannot clear newer failures. A wrong policy
  digest or coverage/actor binding cannot clear current policy findings.
  Read failure preserves prior state, including last-observed/verified ages.
- **Verification:** add `setup rulesets --authority openmerge --verify` using
  operator DB credentials and an admin-token read, never App admin-write
  credentials. It makes no GitHub writes. Validate all target queues and bind
  records to proposed/applied scope and the binding below. Findings under a
  proposed policy, including visible violations, are **evidence-only** on a
  still-`github` queue: they cannot set/clear an admission block, change active
  authority state, or satisfy active verification. Lock queues in canonical
  order and append atomically with an idempotency key. Applied verification
  folds only against the currently pinned authority policy. A visible violation
  is recorded and exits nonzero; failed reads never claim verification.
  The first-emission drain/backup rule **does gate `--verify` itself**, even
  before config lands: its new event is incompatible with old readers. During
  period B only isolated sandbox verification may write these events.
- **Draft-to-activation binding:** record installation/repository/queue keys,
  proposed effective policy digest (including actor, merge method, owner policy,
  default branch, and selector coverage), stable resolved actor IDs, and the
  exact applicable repository/inherited/classic protection inventory. Fingerprint
  sorted IDs, enforcement, targets, conditions, rule parameters, and visible
  bypass actors/modes; include reliable forge revisions when supplied, never
  assume every ruleset has a usable version. Record completeness and the admin
  seeing-read time. At activation a draft is usable only if the landed effective
  digest/identities match and a fresh complete seeing read matches that entire
  protection fingerprint; record its adoption against the actual governing
  commit atomically with the authority pin. A hidden or incomplete read cannot
  prove this binding. Otherwise leave the draft historical, report unverified,
  and run applied `--verify` after config lands; do not revive an old pass or
  import its violations into a different policy. Any changed rule ID/parameter,
  actor, selector, or method requires new verification. Unverified remains
  non-blocking; current visible violations still block after activation.
- **Wire/config/schema:** emit `write_restriction_observed` only from the dormant
  authority observer and `write_restriction_verified` on explicit operator
  invocation. No additional types or public config keys. Verification output
  names the source events and distinguishes observation age from verification
  age. Refresh and acknowledgement never invoke a seeing clear.
- **Migrations/tests/evidence:** no SQL migration or verification index. Fold
  applied evidence from each queue's existing ordered journal; use bounded
  `events_queue_watermark` pages for proposed-history lookups and the existing
  queue/dedupe unique index for command retries. Test proposed violations leave
  `github` admission and next actions unchanged, draft mismatch/hidden binding,
  the first-emission drain procedure, concurrent verification/runtime reads, hidden-after-violation, mixed repaired/unseen
  findings, digest changes, duplicate commands, restart, and source isolation.
  Real-git worker acceptance must settle a started atomic landing even when the
  observer records a violation. On GitHub, disable one managed rule, observe a
  block, repair it, and prove only a suitable visible observation clears it.
  Retain journal IDs, source revisions, read bodies, and before/after states.

##### PR 5: Acknowledge outside pushes without changing their meaning

- **Scope:** add proposed `cmd/openmerge/admin.go` for
  `openmerge admin acknowledge --queue <key> --event <id> --sha <sha>
  --operator <name>`. Use operator DB access, not PR authorization or a status
  API mutation. Check the event exists in that exact queue, is
  `outside_push_recorded`, and its `new_sha` equals the supplied SHA.
- **Wire/store:** add `outside_push_acknowledged` and its one registry fixture.
  Record supplied operator name, source event ID/SHA, and time, atomically and
  idempotently under the queue lock. A retry returns the original receipt, not
  another timestamp. Different concurrent acknowledgements cannot duplicate
  the fact. Use a deterministic `dedupe_key` derived from acknowledgement kind
  and original source event ID, protected by existing `events_queue_dedupe` and
  the queue lock; operator name is not part of that key. Validate before insert.
  **No migration:** remove both the proposed partial unique and warning indexes.
  Stream that queue's events in bounded `events_queue_watermark` pages to derive
  outstanding warnings/receipt lookups; no 20-item cap or new indexed JSON query.
  Preserve atomic cancellation/failure behavior and measure the ~5k-event scan
  plus growth on isolated fixtures before acceptance.
- **Projection/config:** query the journal-backed acknowledgement ledger even
  after the original push falls outside the 20-item recent projection. Show
  outstanding warnings in CLI status, API, and health for authority bases.
  Record warning eligibility from authority active at the observed base move,
  so enabling authority does not relabel all historic github-mode pushes.
  Switching modes or retiring a queue never deletes receipts or audit evidence.
  No YAML/schema or forge capability change. No acknowledgement on PR checks.
- **Tests/real acceptance/evidence:** wrong queue/type/SHA, concurrent retry,
  restart, more than 20 outstanding pushes, and archived queue lookup. Prove
  acknowledgement does not clear rules/suspect blocks, change ancestry, grant
  approvals, or move green. Real-git break-glass forward pushes and rewinds must
  flow through existing `ReadOutsidePushAudit` suppression after settlement,
  retaining `possible_stale_observation`, truncated/unknown history, and
  `verification_before_landing: none`. On GitHub, acknowledge the actual
  break-glass move from 11 and compare source/receipt IDs and remote refs.
  Preserve the journal-only observer's independent cache and credentials.

##### PR 6: Render authority in status, CLI, API, and health

- **Status owner (Stage A 12a equivalent):** extend `internal/status` views,
  status JSON/API, and CLI status rendering with verified/unverified/violated
  authority, exact text **“authority unverified”**, per-element visibility,
  last-observed and last-verified timestamps and ages. Missing evidence renders
  “never”/unknown rather than zero age. Reuse 4's durable projection and 5's
  outstanding-warning ledger in status and health; do not perform forge reads
  or fabricate current verification from a successful status request. Keep
  unchanged `github` JSON/text byte-identical when authority fields are absent.
  Test hidden, never observed/verified, violated, repaired, and stale evidence
  consistently across CLI, status API, and health, including after restart.
- **Scope:** use the existing status API listener. Route exact `GET /v1/live`
  before auth, key discovery, or DB access; return only a process response.
  Keep `/v1/health` under existing bearer-token and listener-address rules.
  Health DB failures return 503; an unauthorized request returns 401 without
  queue details. Method/query/body validation remains explicit.
- **Scheduling:** expose a synchronized read-only snapshot of actual last
  successful reconciliation and next effective deadline from the worker loop.
  Include webhook expiry/backstop, queue wake, rules/validation retry, quota,
  and error backoff. Update success only after a lifecycle-appropriate successful
  reconciliation, never on attempt, reporting-only work, or failure. Restarted
  workers report unknown/not-yet-reconciled until successful. Do not derive
  success from journal activity or calculate a new polling schedule in health.
- **Readiness:** report lifecycle/readiness/reasons, authority visibility and
  both ages, outstanding outside warnings, timestamps, overdue, and degraded.
  Recommend process flags with defaults of 30s overdue grace and 15m maximum
  success age, validated to bounded ranges (1–60s and 1–60m respectively).
  Even repeated on-time failures become degraded. Exclude dormant/retired queues
  without obligations; watch settlement-only queues until attempts resolve.
  Unverified and outside warnings alone return 200. Any violated, overdue, or
  degraded queue returns 503. The endpoint is for alerting, never restarts.
- **Wire/config/forge/migrations:** no new journal types, YAML/schema keys, SQL
  migrations, or forge methods. These timestamps are process telemetry, not
  audit assertions. Preserve existing status JSON when authority fields are
  absent. It is **Hold** because it instruments reconciliation and scheduling
  used by existing queues and changes shared status rendering. Its status
  components are dormant until 9, and 9 cannot activate without all of them;
  passing fake-clock tests does not waive that hold.
- **Tests/real acceptance/evidence:** fake-clock deadline and backoff cases,
  webhook silence, repeated failures, zero/new queues, retirement with a pending
  start, and multi-repo isolation. Block/fail the test DB and prove liveness
  remains 200 while authenticated health becomes 503. Use an isolated real-git
  worker with an unavailable remote and a settled/pending attempt to verify
  actual scheduler values. Retain sandbox HTTP responses during healthy,
  unverified, violation, drift, and outside-warning cases in 11.

##### PR 7: Tell PR authors how to use the queue

- **Scope:** extend `internal/serve/worker.go` (`status`, `report`,
  `reportWaiting`) and `internal/status/check.go` to say the repository merges
  through OpenMerge, show the queue state, and print `@openmerge queue` when
  authority is active. Preserve exact-head reporting, check ownership, escaping,
  dedupe, body bounds, and the existing driver `ReportStatus` contract.
  Do not expose unverified authority or acknowledgement instructions to authors.
- **Wire/config/migrations:** no new types, keys, or SQL. Compose through existing
  reporting actions without changing their wire payloads. Dormant until 9.
- **Tests/real acceptance/evidence:** golden messages for waiting/testing/blocked/
  landed states, long or hostile names, retries, and byte-identical legacy
  messages. Use real-git heads and the GitHub driver to prove the check attaches
  to the intended PR head and updates idempotently; retain actual sandbox check
  output after 9. This is **Hold** even if authority-gated: the new message is
  a check reporting change in shared paths, and trial comparison must not depend
  on calling a reporting change cosmetic.

##### PR 8: Record the exact checks used at landing start

- **Scope:** add separate `landing_checks_recorded` with one registry fixture,
  leaving Stage A's decision payload unchanged. Bind batch/attempt, config and
  policy digest, tested SHA, check name, producer kind/identity, observed result,
  run/status ID and attempt where available, and observation time. Represent
  unavailable producer or attempt evidence explicitly, never as a fabricated ID.
  Retain required condition terms and their observed inputs for nested/negative
  expressions, rather than assuming every term means `check-success`.
- **Atomicity:** use the exact `CheckRuns` read evaluated by
  `internal/serve/stage_a_gate.go` and `policy.go` (`checkSnapshot`), preserving
  source ambiguity and branch provenance checks. Extend the gate/store contract
  so evidence and eligibility/decisions/start commit or roll back together in
  `Store.StartLanding`. Validate claim, tested SHA, batch membership, digest,
  and source revisions under lock. Do not perform a later forge read to fill
  historical evidence. Include generated-remedy starts with their existing
  exemption semantics. Fast-forward records marker/tested SHA separately from
  the contributor head that lands.
- **Config/forge/migrations:** no public schema keys or new check-selection
  semantics. Extend both fake and GitHub contract cases for check identity,
  absence, attempt, and invalid inputs. No SQL migration or attempt index:
  use bounded queue/id pages from `events_queue_watermark` for linked check
  evidence and existing reporting settlement indexes where applicable. Emit
  only on authority paths after 9; historical decisions remain
  readable with checks `not recorded`.
- **Tests/real acceptance/evidence:** transactional failure/concurrency cases,
  stale claims, producer collisions, retries, negative/nested expressions, all
  merge methods, and snapshot-tail replay. Real-git starts with cold caches and
  actual generated commits; real GitHub checks on the queue ref must match the
  recorded tested SHA, producer, and result read back after settlement. Retain
  decision/check/settlement IDs and compare them with actual base history.
  This is **Hold** because it changes landing-start and check-read wiring.

##### PR 9: Publish configuration and enable the complete authority gate

- **Scope/config:** publish repository-root `policy_authority` and `break_glass`
  in `schema/openmerge.v1.json` and `config.Load` together. Test the same valid
  and invalid documents through schema, loader, and CLI config validation.
  Branch-dependent validation requires exactly one queue serving the default
  branch and effective CODEOWNERS coverage for all config paths and CODEOWNERS
  itself at the governing commit. Missing, unresolved, or incomplete governing
  evidence cannot activate authority. Import `--accept` cannot waive these gates.
- **Owner enforcement:** activate classification, briefs, eligibility, and atomic
  decision storage even for authority queues with no `approvals` section.
  Non-policy changes on those queues add no invented reviewer floor, but policy
  changes always require eligible exact-revision owner approval. User `none`
  rules cannot override that obligation. Evaluate proposed CODEOWNERS/config
  edits against governing owners so an author cannot remove the gate that is
  authorizing the edit. Invalid landed config blocks further work.
- **Wire/state:** emit 1's `policy_authority_applied` alongside the unchanged
  `config_applied` only for queues entering, already under, or leaving authority,
  with matching commit/digest and resolved actor. New queues in an authority
  repository also get pins. Never emit it for an ordinary `github`-only
  `config_applied`; unchanged `github` repositories acquire no new event types
  or downgrade boundary. Persist pin changes through the queue fold, not only
  worker memory. Apply 4's draft-binding rules at activation. Activate
  observation, warnings, status/CLI/API/health rendering, check messages, and
  per-check evidence from 4–8 on the first authority cycle. All landing
  start paths, at depths 1 and 4, consult current source findings and the exact
  authority digest under the existing queue lock. An admitted start settles
  regardless of later drift. Do not change batching or fence protocol.
  Switching back to `github` records a mode-specific authority pin, stops new
  authority observations/warnings, and removes only authority-specific admission
  requirements. Preserve their history and every unrelated protection/block.
  Re-enabling authority requires fresh policy-bound observation rather than
  reviving a stale verification. A repository-wide digest change must reach
  every served queue before the operator removes redundant protection.
- **Forge/migrations:** wire the typed readers, compiler, and content validation
  into discovery/reconciliation and setup checks for authority mode only, and
  supply 3a's per-operation immutable context to every generated-object push
  before it runs. A post-build pinned read is evidence, never the push guard.
  Invalidate observations on default-branch, selector, actor, or method changes.
  No new capability or SQL migration beyond prerequisites. Manually drain older
  workers before the first emission, including pre-switch `--verify`, and obey
  PR 1's snapshot/journal downgrade and backup-reconciliation gate. Switching
  back to `github` does not remove new event types or version-7 cursors.
- **Tests/real acceptance/evidence:** mixed repositories and bases, no-approvals
  authority queues, default-branch rename/deletion, ambiguous selectors, invalid
  coverage, owner removal, head changes, quota/errors, and source repair races.
  Real-git acceptance starts with an empty cache, acquires tested commits, and
  lands every supported merge method through `gitops`, with actual base/fence
  read-back and immutable decisions. Reproduce #82's limit boundary and #84's
  tested-commit acquisition case. Rejected intermediate commits/markers/fences
  leave real remote refs unchanged before any push, while mixed `github` queues
  retain legacy commit IDs, permission checks, and event traces. The real
  organization switch in 11 is this PR's final acceptance gate; a fake-only activation is not a release.

##### PR 10a: Stream a deterministic audit export

- **Scope:** add proposed `cmd/openmerge/audit.go` with
  `openmerge audit export --repo owner/name --installation <id>
  --from <RFC3339> --until <RFC3339> --out <directory>`. Use operator credentials
  through a read-only connection like `OpenJournalReader`; never run migrations,
  acquire worker locks, write observations, or call GitHub. Fail ambiguous
  repository identity rather than combining installations silently.
- **Format:** output UTF-8 JSON Lines plus `manifest.json`, serialization version
  1. Select by immutable DB `recorded_at` in `[from, until)`, not event-supplied
  time. Discover all selected repository queue keys, including retired queues,
  in one repeatable-read, read-only snapshot. Order records by canonical queue
  key then event ID, with stable ordering of dependency evidence. Include
  source event ID/type/recorded time, original wire payload as canonical JSON,
  record kind, and references. Preserve all source events in scope, not only a
  hand-picked list of successful decisions. Export linked eligibility,
  classification, accountability, receipt, authority, check, and settlement
  evidence outside the range as labelled dependencies. Unknown historical
  evidence says `not recorded`; missing required retained evidence fails.
- **Integrity:** specify compact deterministic JSON bytes, field/map ordering,
  UTC timestamp precision, numeric handling, escaping, and trailing LF with
  golden vectors. Canonicalize JSONB payloads rather than claiming original
  JSON text survives PostgreSQL. Each record carries `previous_sha256`, starting
  with 64 zeroes. Its digest is SHA-256 of that record's canonical bytes excluding
  the trailing LF. The next record references it. Manifest includes repository/
  installation, filter, per-queue watermarks, dependency boundaries, record
  count, terminal digest, and serialization version. Empty exports have the
  zero terminal digest. Do not include current time or mutable remote URLs.
- **Reproducibility:** emit a replayable source selection in the manifest.
  Repeatable-read prevents an internally mixed export, but event IDs alone are
  not commit-order watermarks across concurrent queues. For exact later replay,
  retain the selected source event IDs and payload hashes, including dependencies,
  in a manifest-referenced selection file with its digest. Re-export with proposed
  `--manifest <manifest.json>` selects those exact sources and verifies hashes.
  A fresh export after a late transaction commits is a new selection for the same time
  filter; never silently call the two snapshots identical. **Decision refinement:**
  this qualifies “the same range always exports identically”: identical bytes
  require the same range **and retained source selection**, including dependency
  hashes and serialization version. A time range alone does not freeze later
  commits. The accepted Decision text above is unchanged; this plan makes the
  concurrency condition explicit and acceptance must test it.
- **Wire/config/contract/migrations:** no new queue event, YAML/schema key, forge
  method, or runtime activation. Extend the read-only store API for streaming;
  no SQL migration or export index. Discover queues with the existing identity
  index, stream each in `(queue_id, id)` pages using `events_queue_watermark`,
  and apply the recorded-time filter during the scan. Resolve dependency IDs
  through existing event keys. Accept O(N) selected-repository scanning with
  bounded memory; retain ~5k-event and growth query plans/timings. Write output
  into a private temporary sibling directory and rename only when complete.
  Refuse overwrite and leave no valid manifest for failed/truncated output.
- **Tests/evidence:** fixed vectors, repeat byte comparison, empty/range boundary,
  retired queues, concurrent late commits, cross-range references, unknown old
  events, missing evidence, and read-only DB role tests. From a real-git fixture
  journal, match exported tested/landed SHAs and outside moves to the bare
  remote's graph. GitHub performs no role in export itself; 11 supplies actual
  forge provenance. Attach selection/manifest/JSONL hashes and memory-bounded
  streaming proof without using the live DB.
- **Deploy eligibility:** this PR is Inert by its own behavior, but depends on
  **Hold PR 8** (and transitively Hold 3a through 4). It cannot ship mid-trial in
  a binary containing those prerequisites. Develop against fixtures, merge
  independently of deployment, and defer the binary until B drains unless a
  separate release proves it contains only deployable code/dependencies. This
  caveat also applies transitively to 10b and 11; Inert is not a dependency waiver.

##### PR 10b: Prove retention and external anchoring

- **Scope:** finish Part 3 acceptance and operating guidance. The initial
  retention policy is **retain all journal evidence**, configurable pruning
  deferred. `internal/store/evidence_retention_test.go` already forbids history
  pruning and payload mutation. Preserve its guarantee for
  `landing_decision_recorded`, `approval_eligibility_observed`, and every linked
  new event, across mode changes, queue retirement, backup/restore, and restart.
  A future retention setting must preserve transitive dependencies for the whole
  configured audit window and must land with compaction integration tests.
- **Wire/config/forge/migrations:** none. No new retention YAML accepted by the
  schema or loader. Snapshot state is not a substitute for original evidence.
  Document compatible-binary and DB-backup requirements and a separate operator
  read role. Retention duration is an organizational choice, not a compliance
  guarantee invented by this command.
- **Tests/real evidence:** export twice using the same selection, restore an
  isolated DB backup and reproduce bytes, and export after switching to github
  and retiring a queue. Edit, reorder, drop, and truncate records to prove chain
  failure. Show that recomputing an altered chain is detectable only against an
  earlier externally retained manifest/terminal digest. Use real-git history
  read-back for the source fixture and repeat against 11's real sandbox journal.
  Retain exact command exits and hashes; document signing and storing the anchor
  outside OpenMerge without claiming the DB is tamper-proof.

##### PR 11: Execute the organization exit test

This PR adds the executed evidence and operating runbook. It changes no code,
config schema, wire types, forge contracts, or SQL migrations. It is Inert as a
code deployment, but requires the held implementation on an isolated sandbox.
Record every step below, including failed commands and cleanup. Unexecuted
acceptance is not a pass. The proposed command flags above must exist before
running this procedure; 11 replaces placeholders with the concrete tested values.

The runbook deliverables must also name actual owners and adopted schedules:

- **Outage duty:** ordinary queue merges stop until the OpenMerge service returns;
  the documented exception is a declared break-glass hotfix, subsequently
  reconciled and acknowledged. Show who may invoke it and how service resumes.
- **App private-key custody/rotation:** name the custodian and backup custodian,
  secret-store location/access policy (never the key), audit access, and the
  compromise contact. Document creating/installing a replacement key, proving
  App identity/landing with it, revoking the old key, and emergency revocation;
  recommend rotation every 90 days and immediately on suspected exposure.
- **Alerts and re-verification:** the repository's named protection operator owns
  Q2's daily admin `--verify` and re-verification after policy/App/actor changes;
  name a backup operator and keep their DB/admin-token access separate from the
  service key. Recommend health polling every minute, page on overdue/degraded
  or violated state, alert on admission-block age over 15m, verification age
  over 24h, and each new outside-queue landing. Test routing to the on-call owner
  without sending production alerts from acceptance. Record any adopted values
  differing from these recommendations and the exact last-success/age evidence.
- **Backups/recovery:** name the DB backup/recovery owner, recommend nightly
  encrypted backups plus a pre-change/pre-emission backup, and a monthly
  isolated restore/export comparison. Record retention, location, access and
  recovery objectives, and how to reconcile remote refs after a restore. Keep
  externally signed export anchors separately. Include PR 1's v6/v7 and unknown
  journal-type downgrade limits; a mode rollback does not make an old binary safe.

Use **Smart-Data-Ohio/openmerge-sandbox** and App
**openmerge-sandbox-smartdata** (recorded App ID `5383059`, installation
`173082682`). Reconfirm these with GETs. The prior sandbox is archived; have the
operator unarchive it before writes and rearchive it after evidence is retained.
The App already has `members: read` and is selected only for this repository.
Retain a fresh permission/install snapshot, not just the Stage A operator report.

1. **Isolate and capture.** Use a separate process, cache, credentials, status
   listener, and database/schema with verified sandbox identity. Save old config,
   governing/served-base SHAs, local/inherited rulesets, classic protection,
   current grant JSON, and existing queue/green/verify/fence tips. Use fresh
   evidence branches and a representative `release/**` queue. Keep old protection
   enabled. Stop unrelated sandbox policy edits. No production units, DB, refs,
   status listener, or App permissions are involved.
2. **Adopt sandbox permissions.** Request and have the organization approve
   **Administration read** for this sandbox App, retaining its existing
   `members: read` and other grants. Read does not grant ruleset mutation or
   guarantee bypass-list visibility. Administration **write** belongs only to
   the separate setup operator token. Run `setup permissions --authority openmerge`
   (3b's setup-only diagnostic) and save exact output/exit and grant GETs. Do not request the live App grant until period B
   drains and an explicit production adoption is scheduled.
3. **Preflight the draft.** Run
   `openmerge setup import-protection --repo Smart-Data-Ohio/openmerge-sandbox
   --base main --base release/acceptance --json`, and repeat with reviewed
   `--accept` item IDs only for intentional policy relaxations. Save both reports
   and draft. Resolve all other base writers. Verify queue-push CI producers,
   exact-head approvals, effective team owners, all config paths, CODEOWNERS,
   and a served default branch. Use the independent owner identity from Stage A
   or re-resolve/regrant and record the replacement. Disable GitHub auto-merge.
4. **Prepare setup evidence.** Write the reviewed future config as
   `authority.yml`, selecting `policy_authority: openmerge` and exactly one
   break-glass team. Set `REPO=Smart-Data-Ohio/openmerge-sandbox` and
   `APP_ID=5383059` only after confirmation. Run
   `openmerge setup rulesets --repo "$REPO" --app-id "$APP_ID"
   --authority openmerge --config authority.yml` as preview. Inspect the exact
   targets and actors. Apply with the same arguments plus
   `--backup rules-before.json --apply`; keep old PR protections intact.
   Repeat preview and require zero changes. With sandbox operator DB credentials,
   run the same authority/config selection plus `--verify --operator <name>`.
   Save the proposed-scope event IDs and complete policy/protection fingerprint.
   This isolated DB may emit new events; production `--verify` must wait for the
   first-emission drain/backup gate. Prove a draft verification with a visible
   negative leaves still-`github` admission unchanged, then repair/reverify.
5. **Exercise setup recovery before switching.** Retain PR 3b's deterministic
   HTTP fixture run: let the first managed PUT/POST succeed, return HTTP 503 on
   the second mutation, then replay with normal responses and prove idempotent
   resume and restore against the captured settings. These HTTP fault-injection
   tests suffice for the mid-apply failure; no forced failure against real
   GitHub is claimed or required. On real GitHub demonstrate zero-change retry,
   `--restore rules-before.json`, and reapply/reverify without removing inherited
   or unrelated policy. Preserve fence coverage throughout. Run legacy setup
   preview/apply with `openmerge-refs` present and require no duplicate fence.
   **Repository-admin actor round:** before the main team switch, preview/apply
   a separate reviewed draft with `break_glass: {repository_admin: true}`,
   record GitHub's actual role ID/representation and bypass acceptance, admin
   verify it, and prove a repository administrator can write the test base but
   cannot write App-only refs. Capture/restore that round, then reapply/reverify
   the team draft for the main switch. Test the App and each actor as separate
   identities. If GitHub rejects either bypass variant, record failure and stop
   that variant's acceptance; never relax protected refs to obtain a pass.
6. **Land policy under old protection.** Submit the config/CODEOWNERS change
   through the sandbox queue, using the normal queue command as a human in the
   later acceptance run. Obtain exact-head eligible owner approval. Wait for
   settlement. Save each queue's governing commit and new effective digest,
   authority pin, source revision, and verify binding. Require every affected
   queue to settle old-policy starts and report no violation before proceeding.
   A stale draft verification or a merely queued config PR does not pass.
   Exercise 4's full matching-fingerprint adoption case and a mismatch/hidden
   case. In the latter run admin `--verify` against the actual landed policy;
   require a new applied event rather than a draft being silently promoted.
   Save status CLI/API and health's “authority unverified” and both ages before
   that seeing read, and verified evidence afterward.
7. **Remove only reviewed redundancy.** Capture a fresh policy snapshot. Remove
   only the specifically reviewed redundant PR/review/check requirements from
   their owned repository policies using the admin operator. Preserve compatible
   content rules and both write rulesets. Inherited conflicts must be resolved
   by their actual owner or the switch stops. Rerun import, admin verification,
   runtime observation, and health. Save exact changed rules/parameters.
   **Selector addition:** while authority is active, prepare another reviewed
   config adding a fresh release selector. Run setup preview/apply to install
   union coverage with a fresh `--backup selector-before.json` and verify it
   before queuing the config. Keep the ordered captures for each accepted setup
   mutation so guarded restore can unwind only those changes. Prove the current
   authority queue remains unblocked and an ordinary writer cannot create a
   future matching base in that interval. Land the edit
   with owner approval, wait for every new digest/settlement, then use the App
   to create the branch and activate its queue. Retain both coverage bindings.
8. **Prove enforcement on real GitHub.** As an ordinary writer, attempt a
   fast-forward base push, matching release-branch creation, deletion, force
   update, and GitHub merge-button/API merge on a fresh test PR. Save rejections
   and unchanged remote tips. Repeat ref creation/update/deletion attempts for
   nested queue/fence/green/verify names as both ordinary writer and break-glass;
   both must fail. Through the App, build/test/land queued changes with real CI
   and confirm atomic base/fence movement. Run supported merge methods on
   dedicated bases, including fast-forward marker/head separation. Record
   per-check and decision/settlement IDs, actual check producer/results, and
   remote SHAs. Never infer protection from a displayed ruleset alone.
9. **Prove policy without explicit approvals.** Use an authority queue with no
   `approvals`. A non-policy change can land on its CI policy. Config or
   CODEOWNERS edits wait for an eligible governing owner, including an edit that
   removes ownership, changes the actor, or attempts to weaken policy. Neither
   an exemption nor import `--accept` permits bypass. Repeat head changes and
   missing-owner/coverage negatives. Save brief and immutable decision reads.
10. **Exercise break-glass.** Stop only the isolated sandbox process. As the
    declared break-glass team member, push an ordinary forward hotfix to a served
    base. Save old/new SHA and remote acceptance. An attempt to write any
    protected OpenMerge ref still fails. Restart the sandbox process; wait for
    prior starts to settle and its existing outside-push observer to record the
    move. Status/API/health show a warning, not a new admission block, and the
    event says no verification before landing. Queue and land another change
    before acknowledging to prove the warning does not stop merges. Run
    `openmerge admin acknowledge --queue <sandbox-key> --event <outside-event>
    --sha <new-sha> --operator <name>` twice. Require the same receipt ID, the
    warning cleared, original evidence unchanged, and no green-ref move caused
    by acknowledgement. Do not claim the supplied name proves who pushed.
11. **Reintroduce conflicting protection.** Add an active repository PR/check
    ruleset on a served base with no App bypass, then repeat with classic
    protection where supported. Save the concrete ID/parameters. Under polling
    without webhooks, wait for the configured rules deadline and require the
    appropriate `rulesets` or `classic_protection` block, health 503, and no new
    landing starts. Repeat with webhook delivery enabled and with delivery
    suppressed long enough for the backstop to fire. Already-started landings
    must settle. Remove only that test conflict, observe that source clear,
    and land again. `write_restriction` verification/refresh/acknowledgement must
    not clear a conflicting protection source.
12. **Drift the write restriction itself.** Add an undeclared bypass actor or
    remove required target coverage using the sandbox admin. Record the exact
    visible response. Require `write_restriction` violated and new starts
    blocked. Follow a visible bypass violation with a hidden-list observation
    in the shared adapter fixture; on GitHub use actual installation visibility
    when available, without claiming it can be forced. Hidden reads, failures,
    refresh, and acknowledgement retain that finding. Repair structure and
    verify its finding clears independently; repair the bypass list and use
    admin `--verify` to clear that finding. Save observed/verified ages. An
    installation that cannot see an unauthorized actor must remain honestly
    unverified until the admin seeing observation records the violation.
13. **Prove probes and recovery.** Save unauthenticated live=200, unauthenticated
    health=401 when token protection is configured, and authenticated health
    for healthy/unverified/warning states. Stop only the sandbox DB or deny its
    test connection: live stays 200 and health becomes 503. Exercise overdue
    and repeated-failure degradation with bounded test scheduling. A sibling
    healthy repo continues reconciling. Restore DB and prove pending settlement.
    **Rollback (Decision refinement):** this expands “restore the GitHub rules
    first … then land `policy_authority: github`” with a mandatory complete
    legacy-compatibility preflight and post-config write-ruleset cleanup. It
    preserves the accepted Decision text; reverse mode order alone is unsafe.
    First ensure an actual compatible **unbypassed content ruleset** is present
    on GitHub (for example linear history on a fast-forward base), and prove an
    authority landing succeeds under it. Restore the saved PR/review/check
    policy with App bypasses while keeping both authority write rulesets active.
    Run 3b's rollback preflight against the entire prospective `github` config,
    all bases/ref families, inherited rulesets, classic protection, and actual
    installation grants. Require it to reject that otherwise compatible content
    rule under legacy `ValidateRules` before the config is queued. The operator
    explicitly approves and adds an App bypass to this retained content ruleset
    for the test, recording the loss of App content enforcement; disabling or
    removing owned rules is a documented alternative, and unresolved inherited
    conflicts stop rollback. Rerun the complete preflight and require success.
    Only then land `policy_authority: github` through the queue. Wait for every
    affected queue's new digest, old-attempt settlement, and healthy legacy
    rules/permission sources; prove a subsequent ordinary queue landing works.
    **After** that config has landed everywhere, remove `openmerge-base` and
    restore `openmerge-refs` to the captured legacy App-only fence settings/name
    with 3b's guarded restore, unwinding the accepted setup captures in reverse
    order (including selector expansion) and preserving intervening unrelated
    policy. Do this for a normal rollback, not only when retiring setup: until removal, ordinary
    GitHub merges remain blocked. Verify legacy setup creates no duplicate and
    an ordinary authorized GitHub merge now succeeds under restored PR policy.
    Preserve outside/verification evidence. This is a mode rollback on the new
    binary; a binary downgrade separately obeys the pre-emission backup and
    remote-ref/evidence reconciliation gate, never deletion of config keys alone.
14. **Export and retain.** Export a time window spanning activation, a reviewed
    landing, break-glass, acknowledgement, both drift cases, and rollback.
    Re-export from its manifest selection and require identical bytes/digests.
    Check source references outside the range and older `not recorded` history.
    Restore an isolated backup and repeat. Store the signed manifest/terminal
    digest outside OpenMerge and retain sanitized source artifacts with hashes.
    Re-read actual GitHub history against exported tested/landed/outside SHAs.
    Clean up only named test resources, retain the evidence, and have the
    operator rearchive the sandbox. Publish the outcome and explicit limitations.

##### Trial deployment

Follow [ADR 0009's trial policy](../research/phase2-dogfood.md) and
[ADR 0007's deployment gates](0007-changes-not-prs.md#trial-deployment).
Stage A is complete on main but its held changes remain held for deployment.
No ADR 0008 PR authorizes deploying those prerequisites during the trial.

- **Inert** means behavior-neutral for queues without `policy_authority:
  openmerge`, including existing queues with Stage A `approvals`. The numbered
  Inert PRs are only candidates for a mid-trial release containing exclusively
  deployable dependencies. Explicit operator setup/verification on a sandbox
  is not a live deployment. No authority keys, new events, new reads, new check
  output, or new permission requirements appear on an unchanged live queue.
- **Hold** applies to 3a, 6, 7, 8, and 9 until **period B drains**. Pre-push
  integration changes shared build/landing paths; activation changes policy;
  health instruments the common scheduling loop and owns status rendering;
  check instructions change reporting; per-check evidence changes landing/check
  observation. Any PR adding a migration is **Hold (migration)** and deploys
  only in the drained window after B, including behaviorally inert migrations.
  These remain Hold even with dormant paths or passing replay.
  Any further split touching batching, landing, or check observation inherits
  Hold. A material deployment restarts both trial periods.
- The **live App permissions stay frozen** for the whole pair, including
  Administration, `members: read`, and the existing `actions: write` status.
  Request Administration read for the live App only after B drains, during a
  separately approved authority adoption. Setup uses an operator's admin-write
  token, never an App Administration-write grant. The isolated sandbox can
  adopt its additional read grant for acceptance without changing the live App.
- For each Inert PR attach old/new binary SHAs, the identity/hash of an already
  exported offline journal, configuration, commands, exits, and a maintainer's
  pre-metrics classification. Require byte-identical legacy wire fixtures,
  identical next actions at **every** replay revision, identical seeded simulator
  results, unchanged effective digest, and no new `ConfigApplied` or authority
  events on unchanged config. Compare real-git generated commit IDs for every
  supported merge method when a shared build/metadata path is touched. Compare
  legacy check output and forge request traces for read/reporting paths.
- Repeat those gates for no-approvals legacy queues and existing approval
  queues, with mixed-mode siblings, at depths 1 and 4. Dormant failures must not
  change their plans. PR 1 additionally proves old-binary decoding of both the
  journal and persisted outside-push cursor after new-binary legacy operation:
  keep writing v6 there, not v7 merely because the new codec exists. After any
  v7 persistence or new event (including pre-switch admin `--verify`), the old
  binary is incompatible even if config says `github`; use the shared downgrade
  gate above. No replay writes to the live journal or calls its API.
  The deploy operator logs revision, evidence, and classification in the dogfood
  log. If main includes held dependencies, use a release containing only eligible
  changes or defer. An Inert acceptance-doc PR cannot make its held binary inert.

##### Open questions and recommended answers

Resolve these before their owning PR freezes fixtures. Recommendations below
are implementation choices, not permission to weaken the accepted Decision.

1. **Does health share the status API listener?** Yes (6). Reuse its configured
   address and token rules. Handle only exact `/v1/live` before authentication
   and all DB/key reads. Health is authenticated, read-only, bounded, and uses
   503 for DB failure or any overdue/degraded/violated watched queue. Outside
   warnings and unverified authority remain visible with 200. Never use health
   for restarts. Keep thresholds as bounded process settings, not queue policy.
2. **How does verification work under polling and webhooks?** Use the same
   authority reader/interpreter in both (2, 4, 9). Poll at the rules interval
   even while idle; webhook notifications invalidate/wake but do not constitute
   verification. The worker's effective deadline must include the webhook
   backstop and quota/error delay. Admin `--verify` records a separate seeing
   observation using operator DB credentials. PR 11 names the repository
   protection operator and backup operator responsible for daily re-verification
   and after policy/App/actor changes, with an alert when older than 24h. PR 3b
   owns the setup-only Administration-read recommendation; runtime grants for
   `github` queues stay unchanged. Do not increase App privilege merely to make
   the status say verified. PR 4's proposed evidence is admission-neutral until
   its explicit activation binding is satisfied; `--verify` itself requires
   the first-emission drain even while the active config remains `github`.
3. **What is visible violation evidence?** A successful response proving a
   required ruleset is disabled/mis-targeted/missing required restrictions,
   a completed inventory proving coverage absent, or a visible actor list
   proving an undeclared actor or missing declared bypass (2–4). Store ruleset/
   ref/actor identity, parameters or normalized evidence, visibility, timestamp,
   policy binding, and source revision. Names alone, hidden fields, generic
   permission errors, incomplete pagination, and a failed push are insufficient
   to prove which authority element failed. Unknown active content rules remain
   blockers in the content/rules source, distinct from visibility uncertainty.
4. **How are break-glass pushes reconciled with Stage A?** Reuse
   `outside_push_recorded` unchanged and its attempt-aware journal derivation
   (5). Break-glass is authorization to bypass GitHub, not an OpenMerge landing.
   Warn until an idempotent acknowledgement targets the original event and SHA.
   Do not infer pusher identity from authorship, claim prior verification, clear
   blocks, or advance green. Unknown/truncated history and stale-observation
   caveats remain visible. A push-and-restore between observations remains a
   stated detection limit.
5. **What is the export format and retention guarantee?** Versioned canonical
   JSONL plus manifest and hashed source-selection file (10a–10b). Include
   provenance, transitive decision evidence, deterministic ordering, a previous
   record digest, and an externally retained terminal anchor. Retain all events
   initially; no compaction is introduced. Yes, this requires retaining both
   `landing_decision_recorded` and `approval_eligibility_observed`, plus their
   dependencies, after reverting authority or retiring queues. A later bounded
   retention policy needs operator configuration and integration proof before
   pruning is accepted. Hash chains do not prove continuous protection, database
   honesty without an anchor, or that a human used their account.
6. **How is authority enforced without approvals?** Make authority a separate
   reason to activate Stage A evidence and the mandatory owner obligation (1,
   9). Preserve the zero-additional-review behavior for non-policy changes.
   Expand evaluator/digest/gate contracts explicitly instead of synthesizing an
   invalid `approvals.default: none` or changing the legacy nil meaning. Require
   the same immutable decision and check evidence on every authority landing.
7. **When is Administration requested and how is break-glass represented?**
   Request sandbox Administration read before 11's observation tests, retaining
   `members: read`; request live read only after B drains. Admin-write stays on
   the operator token. Use 1's exclusive team-or-role shape, stable forge IDs,
   and strict exact bypass membership. Reject unresolved teams/roles at setup
   or activation. Confirm the actual GitHub bypass representation on the org
   sandbox before fixtures freeze; never use installation ID as Integration App
   ID. PR 3b owns setup-only grant diagnostics. Test the repository-admin variant
   in shared contracts and PR 11 step 5's explicit real sandbox setup/verify/
   enforcement/restore round, as well as the team used for the main switch.

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
