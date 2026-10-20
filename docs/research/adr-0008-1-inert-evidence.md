# ADR 0008 PR 1: inert contract evidence

## Round 2 revision and schema audit

Rebased onto `e9b24e84a7c6a2ec93be501f4730cefdbf99c0fa` (including #92 shared
forge validators). Production implementation and verification helper:
`ea2ce6c79818c4f04eeec44ab849a658cb57a22a`. The final evidence commit changes
only this note. All public type names remain unchanged.

Observed bypass inventory now includes `user`, `organization_admin`, `deploy_key`,
`enterprise_owner`, and `enterprise_role`, alongside the original app/team/role
kinds. `exempt` is retained alongside `always` and `pull_request`. Class-wide actors
can omit `ID`; unknown forge types retain their exact name with `Kind: unknown`
and `RawKind`. The unauthorized-inventory fixture round-trips all nine normalized
kinds with corresponding `unexpected_bypass` findings, including ID-less and
unknown actors. The strict declared team/repository-admin union is unchanged.

The audit used GitHub's [repository ruleset schema](https://docs.github.com/en/rest/repos/rules#get-a-repository-ruleset)
and [organization ruleset schema](https://docs.github.com/en/rest/orgs/rules#get-an-organization-repository-ruleset),
plus the [Enterprise Cloud actor schema](https://docs.github.com/en/enterprise-cloud%40latest/rest/orgs/rules).
`Protection.Target` now retains branch/tag/push/repository distinctions. A missing
unseen target can be stored, but cannot receive a complete-seeing fingerprint.
The frozen inventory includes enforcement, target, ref-name include/exclude,
all other conditions, rules with every parameter, and bypass kind/identity/mode.
`Parameter.Name` specifies relative JSON-pointer paths with escaped segments and
array indices; ordered scalar values preserve nested object associations without
raw API bodies. Organization variants exercise repository-name include/exclude
and protected, repository IDs, and repository-property include/exclude names,
values and sources. Nested status-check context/integration-ID parameters are
covered too. Sensitivity tests cover each leaf, all target variants, rule kinds,
and unknown actor types. Ruleset names, links, node IDs and timestamps are display
metadata rather than enforcement inputs; an adapter may retain a reliable revision
in the existing `Revision` field.

Every evidence collection, including nested rules, selectors, conditions, rule
parameters, parameter values, bypass lists and coverage protection IDs, is bounded
at 1,000 entries. Boundary tests accept 1,000 and reject 1,001. `cloneState` now
copies the full authority projection; mutation tests cover its pin, activation,
observation, proposed verification and applied verification. The optional forge
input wraps every validation failure with `ErrInvalidInput`, reuses `BranchRef`
for the default branch and `ValidateRefName` for unique full expected branch refs.

The trial remains **Inert**: public authority config remains rejected, no runtime
reader/emitter or SQL migration was added, and the shared-validator changes affect
only the dormant input contract. Every Go/build/make invocation was serialized
with the shared heavy-work lock, `GOFLAGS=-p=2`, and 4 GiB (6 GiB for replay).
Only localhost:54329 was used for DB tests. No live DB, service, status API, or
remote `openmerge/*` ref was accessed; no real GitHub API call was added or made
by code or tests.

### Round 2 executed evidence

Completed 2026-10-20 at 15:01:26 UTC, Go 1.27.2. The focused authority/wire/digest/
forge/snapshot suite and full wrapped `make check` both exit **0**. The full check
includes zero lint issues, the short race suite, localhost:54329 integrations and
the production build. Candidate loader/schema and existing digest golden vectors
pass unchanged. Exact manual command logs:
`/home/riels/.cache/adr0008-1-round2-focused.log` and
`/home/riels/.cache/adr0008-1-round2-check.log`.

The wrapper exits **0**, verdict **PASS**, with these results against e9b24e8:

- **94/94** existing wire fixtures, including **85** registry fixtures, are byte
  identical. Sorted repository-relative path/NUL/file-byte SHA-256 remains
  `328d1264cf4242cf381c1f596f0393064395a31b5e5994cb07ef291ac9e474c1`.
  There are **8** additive authority fixtures (**102** total): three registry,
  activation, unauthorized inventory, and three inherited-condition variants.
  Registry golden/strictness, activation golden and all four observation/verification
  variant round-trips pass. Required-field, duplicate and case-folded key tests pass.
- Snapshot gate and snapshot-tail tests pass. The untouched e9b24e8 State/decoder
  preserves the populated v6 cursor bytes and rejects v7. The legacy snapshot hash
  remains `94a9dea394ab17bb0921e76c413d40c2a3a8dfa73733e53728460de01d59d096`;
  the updated authority v7 hash is
  `c5d7a3d5e56f25bbf3175741f24df40e54dbf3501882d137311172f7ffdb01cd`.
- **90/90** approval/legacy contract checkpoints at depths 1 and 4 match;
  action counts, snapshot bytes and plan bytes all have delta **0**. Both streams
  hash to `88e727ebe7ea5b570f352c5c661c2cd1642c1b5120f3d3ed086993f4dd00362a`.
- Offline replay gates **2/4 PASS**, tool self-tests **PASS**, and synthetic
  compatibility **7/7**. All **2,159/2,159** action, structural-state, snapshot-hash
  and deadline-boundary checkpoints match across the unchanged 2,158-event export,
  including **8,676** boundary probes. Candidate repeats match. Authority remains
  nil at every checkpoint; non-allowlisted differences are empty.
- Both unchanged `make test-sim-exits` runs exit **0**. All **146** normalized
  result blocks match, with SHA-256
  `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.

Commands, numeric exits and input hashes are retained in
`/home/riels/.cache/adr0008-1/round2.json`; detailed replay and per-step logs are
under `runs/round2/` there. Hashes for reports, streams, logs, exports, snapshots
and the production binary are in `round2-artifact-manifest.json`. All paths belong
to this round; round-1 evidence below remains historical. An initial wrapper
preflight exited **1** before any Go command because the supplied baseline SHA
was mistyped. The corrected run uses the git-resolved full SHA above; the rejected
preflight is retained in `round2-preflight-failure.json`.

Reproduce from clean checkouts and the retained offline export:

```sh
python3 scripts/adr-0008-1-verify.py \
  --baseline /home/riels/.cache/adr0008-1-round2-main \
  --baseline-commit e9b24e84a7c6a2ec93be501f4730cefdbf99c0fa \
  --simulator --out /home/riels/.cache/adr0008-1/round2-repeat.json
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB \
  OPENMERGE_TEST_DATABASE_URL='postgres://postgres:openmerge@localhost:54329/postgres?sslmode=disable' \
  make check
```

## Round 1 evidence (historical)

Validated on 2026-10-20, completed at 14:28:40 UTC, with Go 1.27.2.
Baseline: `e7000f8cc1df54372f5220617361f21aaca58b0a`.
Implementation: `92f695f40a5b64a710ad21c7e0f64fdd82002f9f`.
Final replay helper: `222f511bd8cf2301ac399c2897ba122427a57618`.
The following evidence commit adds this note and a focused sibling-isolation test;
it changes no production code or wire fixture.

The maintainer assigned **Inert** before these measurements. Public `config.Load`
and `schema/openmerge.v1.json` still reject both authority keys. There is no SQL
migration, runtime emitter, reader implementation, production observation call,
App grant, or activation. No live database, status endpoint, systemd unit, or
remote `openmerge/*` ref was accessed. No new GitHub HTTP call exists. The replay
uses an already exported local journal copy; it does not refresh that export.
This evidence does not authorize deploying held Stage A prerequisites.

### Contracts and boundaries

`internal/authority` defines candidate modes, exclusive declared/resolved
break-glass actors, owner semantics, coverage/effective digests, typed visibility,
protection inventories, findings, fingerprints, and proposed activation bindings.
Team examples use `Smart-Data-Ohio/break-glass`. The repository-admin variant
retains an opaque adapter-resolved role identity; its observation identity is
`repository_role`, that exact role ID, and `always`. No numeric role is guessed.
Real GitHub role resolution and enforcement remain PR 11 acceptance work.

The three additive wire-v1 events are `policy_authority_applied`,
`write_restriction_observed`, and `write_restriction_verified`. Each has exactly
one registry fixture. A separate activation variant freezes the proposed event
reference, policy binding, complete protection fingerprint, and fresh seeing read.
PR 4 must authenticate that reference against the actual proposed journal event.
The pure projection retains evidence; source blocking/clearing belongs to PR 4.
Pin changes discard candidate draft projections. The journal retains history.

Unknown, missing, null, duplicate and case-folded data keys are rejected.
Visibility survives decoding separately for inventories, targets, rules, bypass
lists and coverage. Absence, hidden data and read failure remain distinct.
Coverage rejects phantom/duplicate protection IDs. Findings require corresponding
visible evidence. The forge reader and pure interpreter contracts are optional
and have no implementation or production caller.

Authority without `approvals` adds only mandatory policy-owner semantics and
immutable evidence requirements. It invents no ordinary reviewer floor and does
not synthesize `approvals.default: none`. Legacy approval/evaluator/gate sources
are unchanged. PR 9 owns enforcement and public configuration activation.

### Wire and digest stability

All **94** pre-existing wire fixtures, including **85** registry fixtures, match
main byte-for-byte. SHA-256 over sorted relative path, NUL and file bytes:
`328d1264cf4242cf381c1f596f0393064395a31b5e5994cb07ef291ac9e474c1`.
The registry golden, complete-variant registry, required-field mutation and
lowercase-key checks pass with the three new fixtures.

The tracked configuration blob remains
`25086900c4db80476dcdf5251839f5cf3054da8d`.
The offline journal's latest `config_applied` is event **1287**, governing commit
`6fa7d082a9888f86fdec9f4f80473070c7fb4be6`. Its effective digest remains
`e58c893b8b3af7685dc07a086824b3bc1fc044456083d193f9566f8387cb58fc`.
Omitted authority and explicit candidate `github` return the existing digest bytes
unchanged. Candidate loader/schema vectors cover both actors and invalid modes,
false roles, users, bots, unknown keys, multiple actors and duplicate keys.
The future YAML fixture uses `.yml.candidate` so the existing public-YAML fixture
check continues rejecting authority without any exception or schema relaxation.

Golden authority vectors pin canonical selectors, actor IDs, merge method and
mandatory owner semantics. Reordering/duplicating selectors preserves identity;
changing coverage, actor, default branch or method changes it. Membership is not
part of identity. Team and repository-admin vectors are in
`internal/authority/digest_test.go`.

### Snapshot and downgrade gates

The v7 decoder retains v2–v6 decoding, including the existing historical upgrade
tests. `EncodeSnapshot` still selects the exact v6 shape when authority evidence
is absent, and `EncodeSnapshotV6` refuses authority evidence. No legacy cursor is
eagerly upgraded. A new event, proposed verification, or retained rollback pin
requires v7; returning to github mode does not make an old binary compatible.
First emission/persistence remains subject to the operator drain/backup gate.

Tests prove immutable application, stale/wrong-policy rejection, every
snapshot-tail split, mode-specific rollback, proposed/adopted scope binding,
actor variants and activation mismatch rejection. The store test
`TestLegacyOutsidePushCursorRemainsV6` writes, reloads and advances a persisted
outside-push cursor through the real test database path.

The round-1 offline probe compiles e7000f8's **unchanged State and decoder sources**, reads
a populated newly encoded v6 snapshot containing outside-push evidence inside the
full cursor wrapper, and preserves its bytes. It rejects a fixture-backed v7
snapshot. Both probe exits are **0**. The main decoder also reads every original
journal event during differential replay. General compaction/pruning is disabled.

This proof is not binary rollback compatibility with the live revision
`3f77a88`. That binary only decodes snapshot versions 2–3 and already rejects
the version-6 outside-push cursors written by main; this mismatch predates PR 1.
Binary-only rollback to `3f77a88` is out of scope. Rolling back to that revision
requires restoring the pre-deploy database backup, not reusing the newer DB.

The contract differential compares approval-enabled and no-approvals legacy
journals at depths 1 and 4: **90/90** serialized snapshot/action checkpoints match.
The per-case counts are 13 approval and 32 legacy-block checkpoints at each depth;
all action counts, plan bytes and snapshot bytes have delta **0**.
The focused sibling test additionally keeps an authority queue beside both legacy
forms at depths 1 and 4, applies valid and invalid dormant authority inputs, and
proves the siblings' snapshots/actions remain unchanged. Its race run exits **0**.

### Offline replay and simulator results

Inputs are `/home/riels/.cache/om-pr72-fix1-input/{events,queues}.jsonl`, the
previously retained 2026-10-18 export. It has **2,158 events**, IDs **1–2158**, and
one queue. Recorded-time bounds are `2026-10-16T13:56:49.699138-04:00` through
`2026-10-18T01:08:55.427607-04:00`.

The committed wrapper reuses `scripts/check-live-journal-replay.py` unchanged,
with local objects, network/module/toolchain downloads disabled and strict state
comparison enabled. Only the structural probe adds a matching nil
`*authorityProjection` to the baseline test overlay. It proves the candidate
field is nil at **all 2,159 checkpoints**; it adds no comparator allowance.
The independent old-decoder probe does not use this State instrumentation.

Final wrapper exit **0**; replay gates 2 and 4 and tool self-tests **PASS**.
All **2,159/2,159** actions, structural states, snapshot hashes and deadline-boundary
checkpoints match, including **8,676** boundary probes. Candidate repeat bytes
also match. There are no non-allowlisted differences, extra `ConfigApplied`, or
authority emissions on unchanged policy. No remote SHA was newly observed here;
this is historical-journal evidence, not current deployment or sandbox acceptance.

Both wrapped `make test-sim-exits` runs exit **0** on unchanged seeds/test sources.
All **146** normalized result blocks match. Normalization sorts complete `t.Log`
blocks and excludes Go harness timing/interleaving, as in Stage A 2b. Normalized
SHA-256: `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.

### Commands, artifacts and validation

Baseline checkout: `/home/riels/.cache/adr0008-1-main`.
Artifacts and exact probe commands/exits: `/home/riels/.cache/adr0008-1/final-r3.json`.
Detailed replay: `runs/final-r3/replay.json` below that cache directory.
The full artifact manifest is `artifact-manifest.json` there. Selected SHA-256s:

| Artifact | SHA-256 |
| --- | --- |
| Exported events | `9c8f78f3e967c7cb030948cfc9871cc67a45da06f80b12f27d1c55d09ba8accf` |
| Exported queues | `418f025a0e3c8792321b6d71d297f17fff1adf48cb616c8f4fcb774163696886` |
| Baseline binary, e7000f8 | `37322d4838074f1f07ecbd15b1125ac213da77abf25e58d8c2e0bf99a01fbbfd` |
| Candidate binary, 92f695f | `89a276a20ed0d826e55d4b5f246a75bf155c463146487cc700dc7c855f34de5b` |
| Full-check production binary, 222f511 | `9f08555820d30e56eec0e1cd3ed724228a854feaed5d42e3165a826d5cf2afb2` |
| Final wrapper report | `625a647cc634ef77f0e7ed402581faaf167d0b25740a017f9463b879d58983ed` |
| Detailed replay report | `04b37bfda41ce2e99832319d48d5d90a2aeecec63a45963f26ddf44aa47fa671` |
| Each replay stream | `1fe079295539183b7008e70b940c7f71d45bd4aa4e443c8c1e581fa10d31051a` |
| Contract differential, both trees | `88e727ebe7ea5b570f352c5c661c2cd1642c1b5120f3d3ed086993f4dd00362a` |
| Populated legacy v6 snapshot | `94a9dea394ab17bb0921e76c413d40c2a3a8dfa73733e53728460de01d59d096` |
| Authority v7 snapshot | `f8648e583a40e9f686f1d44f90e960ef5bcb02e16c6470643c95b55e3fad5f13` |

Reproduce from clean checkouts with the retained offline exports:

```sh
python3 scripts/adr-0008-1-verify.py --out ~/.cache/adr0008-1/repeated.json
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB \
  OPENMERGE_TEST_DATABASE_URL='postgres://postgres:openmerge@localhost:54329/postgres?sslmode=disable' \
  make check
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB make test-sim-exits
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB \
  make -C /home/riels/.cache/adr0008-1-main test-sim-exits
```

The wrapper locks every Go invocation and uses `GOMEMLIMIT=6GiB` for replay.
Standalone binary builds use the same wrapper with 4 GiB and
`go build -ldflags '-X main.version=<revision>' -o <artifact> ./cmd/openmerge`.

Final `make check` exits **0**: formatting, zero lint issues, complete short race
suite, shared-Postgres integrations and production build. Log:
`/home/riels/.cache/adr0008-1-check-final.log`.
Final focused authority/sibling race suite exits **0**:
`/home/riels/.cache/adr0008-1-final-contracts.log`.
Only localhost:54329 was used for database tests. No database was initialized.

Earlier construction runs exited **1** for not-yet-written registry/candidate
helpers, an accidentally public future-YAML fixture, and a fingerprint mutation
that needed its coverage ID updated after stricter validation. The first
`make check` exited **2** on lint comments/builtin-name shadowing. All were fixed;
the successful full check covers the final production implementation. Two queued
proof attempts were intentionally cancelled before Go execution while strengthening
the baseline decoder probe; their child exits are **-15**, wrapper exits **1**.
The final r3 probe is the executed passing evidence above.

There is no scope deviation. Real-git policy-file coverage and actual GitHub role,
permission, protection and switch acceptance remain their planned PR 9/11 work.
