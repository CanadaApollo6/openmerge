# Stage A PR 11b activation evidence

Base: `origin/main` / `6e2b50b9da5c098ebfd76e0b1b20a5990c2663bb` (#77).
Worktree: `/home/riels/openmerge-sa-11b`; branch: `stage-a/11b-activation`.
Trial class: **Hold**. The dogfood configuration is unchanged. No deployment or
approval opt-in is authorized before the ADR 0009 trial pair closes.

## Implementation and obligations

- Public schema, reflected loader key registry, and `config schema` now expose
  the PR 5 candidate additions together. `changes.pull_requests` only accepts
  `required`. The shared candidate parser validates the projected policy after
  the existing YAML/condition/planner parser. Legacy-only input never enters the
  policy parser. Expanded aliases and legacy conditions retain their parser;
  sibling policy parsing does not reinterpret those conditions.
  `TestPublishedPolicyLoaderRoundTrip`, `TestPublicLoaderAndSchemaAcceptPolicyKeys`,
  `TestFullCandidateSchemaComposition`, `TestSchemaKeysMatchLoader`,
  `TestSchemaRepositoryFixturesAndExamples`, and CLI `TestConfigSchemaPrintsEmbeddedDocument` cover
  publication and agreement. Future unaccepted examples remain `text` fences.
- `loadConfiguration` installs `w.stageA` only when the resolved queue has
  `approvals`. There is no partial-enforcement option. The shared Stage A fixture
  now enters via the production public configuration rather than assigning a
  candidate policy. Existing integrated no-bypass coverage therefore exercises
  production installation: `TestStageAShadowGateDepthsAndIdentityIsolation`,
  `TestStageAFreshNegativeStancesBeforeStart`,
  `TestStageAReceiptGateAllMethods`,
  `TestStageAFreshRevokedGrantBlocksAcceptedReceipt`,
  `TestStageAGateRejectsMalformedGeneratedMemberMetadata`, and
  `TestStageAHTTPIdentityFailuresBlockOrRetry`.
- `TestStageAMixedRepositoryActivation` covers depths 1 and 4 with both queues
  on one fake repository and one test database. An unapproved opted-in landing
  is rejected; the legacy sibling lands. Changing the opted-in policy also leaves the legacy journal watermark unchanged. Its worker has nil Stage A wiring,
  retains the old digest, writes no Stage A observations or policy pins, and
  gains no Stage A commit trailers. `TestLegacyConditionsWithApprovalSibling`
  checks native review/list/nested-condition outcomes and digest equality.
  Legacy SQL, depth/action, commit-ID, and replay differentials supply the
  cross-revision proof. `TestApprovalConfigAppliedPinRoundTrip` retains the
  exact old ConfigApplied wire vector while round-tripping the new optional pin.
- The effective approval digest uses PR 5's existing `WithPolicyDigest`:
  SHA-256 of the canonical `{Version, Queue, Policy}` JSON, with version
  `openmerge-effective-policy-v1`, unchanged legacy queue digest, and static
  semantic policy digest. Without approvals, no component is folded in.
  The policy digest's original identity-field spelling is preserved independently
  of new journal JSON tags; existing semantic digest vectors remain unchanged.
- Approval ConfigApplied records add an optional `policy` pin containing static
  resolved identity IDs, classifier version, and governing default-branch name.
  No state/snapshot field or new event type is added. Production journals retain
  the complete ConfigApplied history. The brief projection selects the pin in
  force at classification, reads config plus the entire governing tree from local
  immutable objects, asserts component and effective digests, and only then
  exposes requirements/digest. It makes no forge, fetch, current membership or
  name-resolution calls. The 11b placeholder is removed. Missing inputs produce
  `policy_unavailable`; mismatches produce `policy_digest_mismatch`.
  `TestApprovalBriefPinnedComposition` covers reconstruction, HTTP success and
  mismatch, altered effective/component digests, altered policy, missing pins,
  and missing trees. `TestStageAProductionBriefReconstruction` checks the actual
  production record through the server's local-reader path with a nil forge
  driver. `TestLocalGoverningNeverHydratesMissingObjects` and
  `TestLocalConfigNeverHydratesMissingObjects` prove local immutable reads do
  not hydrate objects or resolve remote credentials.
- PR 4b gate audit: main already satisfies eight diagnostics/file plus `+N more`
  (`TestSourceDiagnosticsBounded`), owner entry/line/token budgets
  (`TestOwnerBudgets`), and fresh decision-time `ResolveOwnerEligibility`
  (`TestResolveOwnerEligibilityUsesFreshObservation`,
  `TestStageAUnReadableTeamAndDecisionTimeOwners`). Main still compiled patterns
  eagerly. This PR adds a 512-byte governing pattern limit and lazy compilation,
  tested on near-limit CODEOWNERS and attributes files by
  `TestGoverningPatternCapsAndLazyCompile`. The classifier version changes to
  `governing-v2-pattern-budget`; previous versions and golden wire fixtures remain
  decodable. `TestActivationClassifierVersion` and
  `TestClassificationHistoricalRenameAndObjectFormats` cover version retention.
- `docs/operating.md` documents the mandatory drain of **all** old workers and
  outstanding actions/leases, verified pre-use backup, first-evidence restore
  boundary, and rollback procedure. Existing action leases
  (`internal/store/migrations/001_initial.sql`) have no worker binary/capability
  identity; reliable detection of a legacy-only holder is not possible without
  a separate lease protocol migration. No heuristic is presented as detection.
  Old binaries reject the published approval config and cannot decode the new
  policy pin, but this does not replace draining already claimed actions.

## Execution constraints

Go build/test and journal replay commands wait until no foreign `go test`, `*.test`, or journal replay
is running. One heavy gate runs at a time. Environment:

```sh
GOFLAGS=-p=2
GOMEMLIMIT=4GiB # 6GiB for journal replay
GOTMPDIR=/home/riels/.cache/openmerge-11b/gotmp
GOCACHE=/home/riels/.cache/go-build
OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
```

The only terminated process was this task's own waiting helper (exit 143), before
it started `make check`, to queue the new classifier golden generation ahead of
verification. No foreign process was killed. No live OpenMerge database, systemd unit,
production `openmerge/*` ref or live status API was accessed. Tests and simulator
use fake forges/local git only. The existing immutable journal exports are used
offline, never re-exported from the live database. Makefile, CI workflows and
their test flags are unchanged.

Fetch: `git fetch origin refs/heads/main:refs/remotes/origin/main` exited 0 and
confirmed the requested base. Both worktrees were created under `/home/riels`,
not `/tmp`; no `initdb` or database cluster initialization was performed.

## Initial submission attempts and results

All durations below are wall time **after** the host-idle wait. Logs are in
`/home/riels/.cache/openmerge-11b/`; hashes are SHA-256. At the initial submission, production code was unchanged from
`f12cba23a92667b3584a0b85911e000fb52171a6`. The subsequent `fb771ed` commit adds
only separate activation vectors and their test paths; `git diff --exit-code
f12cba2..fb771ed -- . ':!**/*_test.go' ':!**/testdata/**'` exited 0. The final
full check, compatibility, commit-ID, and strict-main replay gates use `fb771ed`.
Earlier successful simulator/SQL/depth/deployed-replay gates retain the exact same
production source and unchanged baseline-pinned legacy fixture corpus. The final
follow-up commit updates only this evidence document.

Initial failures were corrected rather than waived: `focused-1` found missing
wire JSON tags; `all-short-1` caught digest-vector changes from those tags (fixed
by preserving the digest's original field spelling); `focused-3` and
`activation-1` caught incomplete fake-native-review/command fixture setup.
`check-2` found a staticcheck conversion diagnostic; its actual make exit was 2.
`check-3` passed lint and serve but exited 2 on two embedded-classifier golden
mismatches in queue. The activation receipt/decision/fact/rejection vectors were
added separately; existing pre-activation goldens remain unchanged.
The first commit-ID invocation used the wrong argument form (exit 2); the next
corrected invocation refused untracked Python bytecode generated by the shape
inspector (exit 2). Only that generated cache was removed, and bytecode generation
was disabled before the final retry.

Three helper-only attempts are not in the runner JSONL:

- `python /home/riels/.cache/openmerge-11b/run.py focused-2 go test -short ./internal/config ./internal/changepolicy ./internal/status ./internal/serve -run 'TestSchemaKeysMatchLoader|TestPublicLoaderAndSchema|TestLegacyDigestAndMixedRepository|TestR4VersionedApprovalRecovery|TestShadow|TestStageA' -count=1`: **exit 1**
  before launching Go because the process-list header was parsed as a PID.
  The runner was fixed to skip the header, then `focused-2-retry` ran normally.
- The waiting `check-1` runner was terminated by this task: **exit 143**.
  `make check` had not started; the queue was replaced to generate the new
  classifier golden before verification.
- Early standalone `python3 /home/riels/.cache/openmerge-11b/inspect-replay-shape.py
  /home/riels/.cache/openmerge-11b/replay-deployed.json`: **exit 0**. This read-only
  Python assertion ran while foreign testing was active; it launched no Go or
  service access. That early scheduling mistake is retained here. The same
  assertion was repeated through the serialized runner as `replay-shape` below.

| Attempt | Actual exit | Wall seconds | Log SHA-256 |
|---|---:|---:|---|
| `focused-1` | 1 | 6.53 | `6a600fbca382af175e7d2eeb37e9f748098cf740eaaf2a03298586ce434e2ab7` |
| `focused-2-retry` | 0 | 5.953 | `07ce676ced09c66eb6dd409026f94d80c96008c07e67422992666490243d7b1e` |
| `all-short-1` | 1 | 86.988 | `d5230975c6f4c5a6c074ca47e37a93175ba005658493e2bc1cd71763322f42c3` |
| `focused-3` | 1 | 9.952 | `a802d14e3ea48b3e9667aa93d403cd5d398d02a2d0a6862f57d5d5fe8f5dd1cd` |
| `activation-1` | 1 | 2.014 | `a6383072760f337edf9c73e4e227a77481b8cf542d190c66a8ad7bf733f38345` |
| `mixed-debug` | 0 | 1.454 | `889d1d907d436cf9b2ae83d8a69272f42123f9fdff5962400adc493fe675a1b0` |
| `activation-2` | 0 | 5.781 | `d64090e996cc194a29652a0630f281d491339cddd1865bf8a934c9c389c1c3d0` |
| `activation-golden` | 0 | 1.277 | `2be096e6ac10930c9b6ef946f2975c6d1548e00734c3eac77df0f73e380dada9` |
| `check-2` | 2 | 5.762 | `6ff57064c3bc458d7bdd66f55dc4f5af868279aa35b61b6de92cd478915aff39` |
| `sim-exits` | 0 | 38.739 | `31fe793e863c268cf8b8335ee805cc43669ad1cd03cbd22ed916ff1740e3d546` |
| `compatibility` | 0 | 13.378 | `8609790ee807f78ae7fadd31e6503a6a932f9f874af818197be4849fe5a93749` |
| `legacy-sql` | 0 | 4.914 | `fe2faa0a68410d87ceef36bb932bf7203279378467e8af399b607abf15999e31` |
| `legacy-depths` | 0 | 143.095 | `3c25309b33c985c03a3aff2a5c961878e2dd1b080a03ec27dc5894981e7b3453` |
| `commit-ids` | 2 | 0.035 | `0cd8123425f07340d03b5632b1bf0f66cc87f2675036d6b9ad87f9d7216e363d` |
| `replay-main` | 0 | 64.063 | `50fdc8938328f6f24684aa42bc635653b1f614c54667810fa9f0fdd583abbf99` |
| `replay-deployed` | 1 | 47.731 | `6348497927981ad3cc7c0b75fc0a3af8e347c1c52ee6ddf3e6e27fd223515cd4` |
| `commit-ids-retry` | 2 | 0.089 | `f147dc40adb584a4267a575c3e1672f532469b799ea7a05d61297f26c8f0fcdd` |
| `check-3` | 2 | 403.504 | `ffb20a06744659ca5bfb2e757e4b5734cc11fe21743e4c0f360ffe679fd82de4` |
| `replay-shape` | 0 | 6.298 | `c86c474f8c3cecc6cd556ee0c0fb64b25cdc32280b48089554baacaed467deae` |
| `receipt-decision-goldens` | 0 | 1.053 | `7c65596fd418cbf43c9025250245efbb2f2aca18590e281a64dfdbd72242662d` |
| `golden-race` | 0 | 4.055 | `a9980b03fde5e93bae284c07d53e9f45b6e34b9c42f6291650c612ee4e769498` |
| `activation-timings` | 0 | 3.394 | `f262d43b654aeafcf0bb14b2b32f19222d804fbc0d54f06c063496882c61f35a` |
| `check-4` | 0 | 381.004 | `bc408cabbc1a0eeee3265eeca7738a0d445c29e1263327b95223ce0a50731c6f` |
| `compatibility-final` | 0 | 11.098 | `8609790ee807f78ae7fadd31e6503a6a932f9f874af818197be4849fe5a93749` |
| `commit-ids-final` | 0 | 12.812 | `b53f54e8cd0f048a96a1696fc34eb77cf65b33514ba50981b5be218c17c3c22a` |
| `replay-main-final` | 0 | 49.039 | `0b9a7e7581e806bd64af73edf5ba84567fb7ad66b4145c6d5aaa02f3dab042b2` |

Exact runner commands (with the environment above):

`focused-1` — actual exit **1**:

```sh
go test -short ./internal/config ./internal/changepolicy ./internal/status ./internal/serve ./internal/queue -run 'TestSchemaKeysMatchLoader|TestLegacyDigestAndMixedRepository|TestR4VersionedApprovalRecovery|TestShadow|TestStageA' -count=1
```

`focused-2-retry` — actual exit **0**:

```sh
go test -short ./internal/config ./internal/changepolicy ./internal/status ./internal/serve -run 'TestSchemaKeysMatchLoader|TestPublicLoaderAndSchema|TestLegacyDigestAndMixedRepository|TestR4VersionedApprovalRecovery|TestShadow|TestStageA' -count=1
```

`all-short-1` — actual exit **1**:

```sh
go test -short -count=1 -timeout=15m ./...
```

`focused-3` — actual exit **1**:

```sh
go test -short -count=1 ./internal/serve ./internal/changepolicy ./internal/classification ./internal/status ./internal/config -run 'TestStageA|TestGoverningPattern|TestSemanticPolicyDigest|TestCodeOwnersDigestVectors|TestBrief|TestR3PolicyRecovery|TestR4VersionedApprovalRecovery|TestSchema'
```

`activation-1` — actual exit **1**:

```sh
go test -short -count=1 ./internal/serve ./internal/status ./internal/gitops -run 'TestStageAMixedRepositoryActivation|TestApprovalBriefPinnedComposition|TestLocal.*(MissingObjects|Busy)'
```

`mixed-debug` — actual exit **0**:

```sh
go test -short -count=1 -v ./internal/serve -run '^TestStageAMixedRepositoryActivation$'
```

`activation-2` — actual exit **0**:

```sh
go test -short -count=1 ./internal/serve ./internal/status ./internal/gitops ./internal/config ./internal/queue ./internal/classification ./internal/changepolicy -run 'TestStageA|TestApprovalBrief|TestLocal.*(MissingObjects|Busy)|TestPublished|TestApprovalConfig|TestGoverningPattern|TestSemanticPolicyDigest|TestCodeOwnersDigestVectors|TestClassification.*Version|TestBudgetVersion'
```

`activation-golden` — actual exit **0**:

```sh
env UPDATE_7A=1 go test -short -count=1 ./internal/queue -run '^TestClassificationWireAndVariants$'
```

`check-2` — actual exit **2**:

```sh
make check
```

`sim-exits` — actual exit **0**:

```sh
make test-sim-exits
```

`compatibility` — actual exit **0**:

```sh
python3 scripts/check-phase2-compatibility.py /home/riels/openmerge-sa-11b-main
```

`legacy-sql` — actual exit **0**:

```sh
python3 scripts/check-legacy-landing-sql.py /home/riels/openmerge-sa-11b-main
```

`legacy-depths` — actual exit **0**:

```sh
python3 scripts/check-legacy-plan-action-depths-1-4.py /home/riels/openmerge-sa-11b-main
```

`commit-ids` — actual exit **2**:

```sh
python3 scripts/check-legacy-commit-ids.py /home/riels/openmerge-sa-11b-main
```

`replay-main` — actual exit **0**:

```sh
python3 scripts/check-live-journal-replay.py --baseline /home/riels/openmerge-sa-11b-main --candidate /home/riels/openmerge-sa-11b --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --strict-state --out /home/riels/.cache/openmerge-11b/replay-main.json
```

`replay-deployed` — actual exit **1**:

```sh
python3 scripts/check-live-journal-replay.py --baseline /home/riels/.cache/sa9b-r2-deployed --candidate /home/riels/openmerge-sa-11b --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --strict-state --out /home/riels/.cache/openmerge-11b/replay-deployed.json
```

`commit-ids-retry` — actual exit **2**:

```sh
python3 scripts/check-legacy-commit-ids.py --baseline /home/riels/openmerge-sa-11b-main
```

`check-3` — actual exit **2**:

```sh
make check
```

`replay-shape` — actual exit **0**:

```sh
python3 /home/riels/.cache/openmerge-11b/inspect-replay-shape.py /home/riels/.cache/openmerge-11b/replay-deployed.json
```

`receipt-decision-goldens` — actual exit **0**:

```sh
env UPDATE_7A=1 UPDATE_9=1 go test -short -count=1 ./internal/queue -run '^Test(ReceiptWireFactGoldenAndRejectedBindings|LandingDecisionGolden)$'
```

`golden-race` — actual exit **0**:

```sh
go test -race -short -count=1 ./internal/queue -run '^Test(ReceiptWireFactGoldenAndRejectedBindings|LandingDecisionGolden|ClassificationWireAndVariants)$'
```

`activation-timings` — actual exit **0**:

```sh
go test -race -short -count=1 -v ./internal/serve -run '^Test(StageAMixedRepositoryActivation|StageAProductionBriefReconstruction)$'
```

`check-4` — actual exit **0**:

```sh
make check
```

`compatibility-final` — actual exit **0**:

```sh
python3 scripts/check-phase2-compatibility.py /home/riels/openmerge-sa-11b-main
```

`commit-ids-final` — actual exit **0**:

```sh
python3 scripts/check-legacy-commit-ids.py --baseline /home/riels/openmerge-sa-11b-main
```

`replay-main-final` — actual exit **0**:

```sh
python3 scripts/check-live-journal-replay.py --baseline /home/riels/openmerge-sa-11b-main --candidate /home/riels/openmerge-sa-11b --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --strict-state --out /home/riels/.cache/openmerge-11b/replay-main-final.json
```

The final `bash finish-verification.sh` exited **0**. The outer first `bash gates.sh` exited **1**, because its last command was the
strict deployed replay with the known exception. Each child status is retained
individually above; a later successful command is never used to mask a failure.

## Initial submission gate disposition

| Gate | Actual exit | Result |
|---|---:|---|
| `make check` (final) | 0 | fmt, lint, complete race/short suite and binary build |
| `make test-sim-exits` | 0 | Full required simulator exits |
| Compatibility vs main (final) | 0 | 17 exits, 1,055 retry/digest records, 84 existing wire fixtures byte-identical |
| Legacy SQL + negative controls | 0 | Seven exact statements; all five mutants detected; every trace Go test exited 0 |
| Depth 1/4 | 0 | 227,662 short + 5,615,998 full checkpoints + 188 fixture streams; every helper Go test exited 0; no exceptions |
| Four-method commit IDs (final) | 0 | Tested/landed IDs identical; mandatory controls: identical=0, mutant=1, dirty=2 (all expected) |
| Strict journal replay vs main (final) | 0 | All 2,159 real and seven synthetic checkpoints match; actions, state/snapshot hashes and timer boundaries exact |
| Strict replay vs deployed `3f77a88` | 1 | Known reviewed absent-to-nil `classifications` boundary only; Gate 2 FAIL, Gate 4 PASS; mandatory synthetic self-test exit 1 for this same boundary |
| Exact deployed replay shape assertion (serialized) | 0 | All 2,159 real + seven synthetic checkpoints; only absent → strictly nil/zero/empty `queue.classifications` map |

The main replay's mandatory synthetic self-test exited 0. The deployed pair's
synthetic self-test exited 1 for precisely the same `classifications` boundary;
it is not represented as a passing strict comparison. No replay exception
allowlist or harness code changed. The source and baseline-pinned corpus of the
successful SQL, depth and simulator gates are unchanged by the final test-vector
commit; `git diff --exit-code f12cba2..fb771ed -- . ':!**/*_test.go'
':!**/testdata/**'` exited 0.

The two added serve tests ran with `-race -short`: mixed repository depths 1/4
1.80s total (0.90s / 0.91s), production brief reconstruction 0.10s; focused package
2.914s, command wall 3.394s, exit 0. These are local single-run measurements,
not a prediction for CI. Full `make check` runs retained the existing 900s race/short timeout.

Start and final explicit-refspec main fetches both exited 0; main remained
`6e2b50b9da5c098ebfd76e0b1b20a5990c2663bb`. Final legacy fixture/protected-file
`git diff --exit-code` checks and `git diff --check` exited 0. No Makefile,
CI workflow, test flags or dogfood configuration changed.

The exact standalone shape inspector, also rerun through the host-idle runner:

```python
import importlib.util,itertools,json,sys
from pathlib import Path
sys.dont_write_bytecode=True
spec=importlib.util.spec_from_file_location('replay','/home/riels/openmerge-sa-11b/scripts/check-live-journal-replay.py')
module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
report=json.loads(Path(sys.argv[1]).read_text())
counts={}
streams=report['streams']
for label,pair in [('real',(streams['baseline']['path'],streams['candidate']['path']))]:
 count=0
 for left,right in itertools.zip_longest(Path(pair[0]).read_text().splitlines(),Path(pair[1]).read_text().splitlines()):
  assert left and right,'unaligned streams'
  a,b=json.loads(left),json.loads(right)
  assert a['kind']==b['kind']
  if a['kind']!='checkpoint':continue
  count+=1
  for key in ('actions','revision','event_id','queue_id','final','next_wake','deadlines','boundary_probes'):
   assert module.exact(a.get(key),b.get(key)),(label,count,key)
  differences=module.structural_differences(a['structural'],b['structural'])
  assert set(differences)=={'classifications'},(label,count,differences)
  field=differences['classifications']
  assert field['baseline'] is None and field['new_field'] is True,field
  assert field['candidate']=={'type':'queue.classifications','kind':'map','zero':True,'nil':True,'value':{}},field
 counts[label]=count
synthetic=report['synthetic_compatibility']
assert synthetic['actions_identical']=='7/7'
assert synthetic['boundary_checkpoints_identical']=='7/7'
assert synthetic['derived_determinism']=='PASS'
assert len(synthetic['non_allowlisted_fields'])==7
for row in synthetic['non_allowlisted_fields']:
 assert row['fields']=={'classifications':{'baseline':None,'candidate':{'type':'queue.classifications','kind':'map','zero':True,'nil':True,'value':{}},'new_field':True}},row
counts['synthetic']=7
print(json.dumps({'exit':0,'exact_shape':'classifications absent -> strictly nil, zero, empty map only','checkpoints':counts,'gate4':report['gate4']},sort_keys=True))
```

## Review round 1 fixes

The trial remains **Hold**. The dogfood configuration, Makefile, CI and test flags
are unchanged. These corrections do not authorize deployment or approval opt-in.

- Pinned approval ConfigApplied records now always carry explicit window bounds,
  including `{min_depth: 1, max_depth: 1}`. Unpinned legacy depth-1 records retain
  their original bytes. `TestStageAApprovalWindowTransitions` exercises approval
  depth 1 → 4, removal of approvals with depth 1 → 4, and approval depth 4 → 1,
  including restart deduplication. `TestLegacySerialWindowWireUnchanged` checks
  serial wire bytes and legacy 1 → 4 behavior; the existing
  `TestLegacyWindowUpgradeUsesHistoricalPolicyBeforeClamp` retains its legacy
  migration corpus. A historical folded digest lacking bounds becomes a durable,
  actionable config block directing the operator to drain and restore a verified
  pre-activation backup, rather than an endlessly retried digest mismatch
  (`TestStageAHistoricalFoldedWindowBlocksActionably`). Old decoders already
  reject the `policy` field; adding `window` to an already pinned record does not
  change rollback compatibility. `TestApprovalConfigAppliedPinRoundTrip` now
  round-trips the pinned serial window and retains the exact legacy wire vector.
- The public schema harness registers `policy-member`, `policy-login`,
  `policy-glob` and `policy-label`. Portable regex constraints cover account/team
  names, label forms, nonblank globs and dangling escapes; the schema `$comment`
  explicitly identifies loader-only YAML, cross-reference and resolution checks.
  `TestPublicPolicySchemaNegativeFormats` covers `@alice`, agent `a/b`, invalid
  accountable logins, blank/trailing-backslash globs, rule globs and `banana`.
  `TestSchemaKeysMatchLoader` now walks nested policy types, composed schemas and
  selector-object keys using the parser's shared vocabulary.
- Policy projections are parsed directly as YAML nodes, retaining original lines
  and paths instead of remarshal coordinates. `TestPolicyDiagnosticsOriginalLocation`
  asserts exact original lines and paths for invalid counts and nested rule labels.
  `ParseCandidate`'s documentation describes its use by the public loader's shared
  node parser. Candidate selection resets Stage A before lookup and fails closed
  if an approval queue has no candidate.
- `MaxPolicyIdentities` shares the journal's 1,024-identity budget with resolution.
  Exceeding it produces an actionable durable configuration error before append,
  including when governing CODEOWNERS introduces the extra identity.
  `TestResolvedIdentityPinBudget` checks the exact boundary;
  `TestStageAIdentityPinBudgetConfigError` covers configured and governing identities.
- Two deliberate legacy invalid-input diagnostic changes are retained: the unknown
  root key `group: {}` now suggests `groups`; `agents: []` is accepted as empty
  policy data. `groups: {}`, `classification: {}` and `changes: {}` are also empty
  policy data. On a queue without approvals they install no Stage A wiring, fold
  no approval digest and add no warnings. `TestEmptyPolicyKeysPreserveLegacyConfig`
  compares warnings and legacy digests and asserts the new typo suggestion;
  `TestStageAEmptyPolicyKeysDoNotActivate` verifies production wiring/digest behavior.

Round 1 commands use the same host-idle runner and constrained environment as
above, with `r1-*` logs under `/home/riels/.cache/openmerge-11b/`. The initial
focused run exited **1** solely because the new legacy transition assertion
incorrectly expected immediate growth from 1 to 4. Existing adaptive behavior
retains window 1 while increasing its ceiling; that assertion was corrected.
The supplied Astra reproduction had already shown the approval transition failure
before this fix (`depth-increase.log`, exit 1).

### Round 1 gate results

Implementation verified: `e7dfbb0b102b475781b2cf675de9962571ed02cc`. Baseline: `origin/main` /
`6e2b50b9da5c098ebfd76e0b1b20a5990c2663bb`. The final evidence commit changes
only this document.

| Attempt | Actual exit | Wall seconds after host-idle wait | Log SHA-256 |
|---|---:|---:|---|
| `r1-focused` | 1 | 4.121 | `e3fd814da547b45917de9a4dff3cdb910a13160604a72f003cda7f843ceb0124` |
| `r1-focused-retry` | 0 | 4.302 | `a79f5a98391493ee925394218cdc148c5de560096b34112015bd3f8cb884f913` |
| `r1-old-decoder` | 0 | 1.049 | `8c351685cf9ec7b25ef2747c1eef8ec86837be540666ea77b00e53be733da804` |
| `r1-check` | 0 | 407.263 | `c41a381043576047fc47a3d79ce168355a9dc4b27655a0cd61f2e32cde2e5adf` |
| `r1-sim-exits` | 0 | 38.725 | `379d93d9d2a3e2ce39fa5ef44ed41405ceb587a929e034f88c41040f0365ad7a` |
| `r1-compatibility` | 0 | 11.27 | `8609790ee807f78ae7fadd31e6503a6a932f9f874af818197be4849fe5a93749` |
| `r1-legacy-sql` | 0 | 4.189 | `fe2faa0a68410d87ceef36bb932bf7203279378467e8af399b607abf15999e31` |
| `r1-legacy-depths` | 0 | 142.158 | `3c25309b33c985c03a3aff2a5c961878e2dd1b080a03ec27dc5894981e7b3453` |
| `r1-commit-ids` | 0 | 12.15 | `e035b0b425420b54ca99fcc3b5015a6efe27f7d6d9839cb69a4e187516bbc861` |
| `r1-replay-main` | 0 | 63.222 | `3ce2d7660ae76064139152283647674796eecab6fc1748ec8bc413600ea9144b` |
| `r1-replay-deployed` | 1 | 49.754 | `dde7f4c2ed9c7f0a817b36a0848ca946ac477d2a353c00f90a2d879653461d26` |
| `r1-replay-shape` | 0 | 6.372 | `c86c474f8c3cecc6cd556ee0c0fb64b25cdc32280b48089554baacaed467deae` |

Exact commands (all use the previously documented environment):

`r1-focused` — actual exit **1**:

```sh
go test -short -count=1 ./internal/config ./internal/changepolicy ./internal/queue ./internal/serve -run 'TestSchemaKeysMatchLoader|TestPublicPolicySchema|TestPolicyDiagnostics|TestEmptyPolicy|TestStageAApprovalWindow|TestStageAHistoricalFolded|TestLegacySerialWindow|TestStageAIdentityPinBudget|TestStageAEmptyPolicy|TestApprovalConfigAppliedPinRoundTrip'
```

`r1-focused-retry` — actual exit **0**:

```sh
go test -short -count=1 ./internal/config ./internal/changepolicy ./internal/queue ./internal/serve -run 'TestSchema|TestPublished|TestPublicPolicySchema|TestPolicyDiagnostics|TestEmptyPolicy|TestStageAApprovalWindow|TestStageAHistoricalFolded|TestLegacySerialWindow|TestLegacyWindowUpgrade|TestStageAIdentityPinBudget|TestStageAEmptyPolicy|TestApprovalConfigAppliedPinRoundTrip|TestResolvedIdentityPinBudget'
```

`r1-old-decoder` — actual exit **0**:

```sh
python3 /home/riels/.cache/openmerge-11b/r1-old-decoder.py
```

`r1-check` — actual exit **0**:

```sh
make check
```

`r1-sim-exits` — actual exit **0**:

```sh
make test-sim-exits
```

`r1-compatibility` — actual exit **0**:

```sh
python3 scripts/check-phase2-compatibility.py /home/riels/openmerge-sa-11b-main
```

`r1-legacy-sql` — actual exit **0**:

```sh
python3 scripts/check-legacy-landing-sql.py /home/riels/openmerge-sa-11b-main
```

`r1-legacy-depths` — actual exit **0**:

```sh
python3 scripts/check-legacy-plan-action-depths-1-4.py /home/riels/openmerge-sa-11b-main
```

`r1-commit-ids` — actual exit **0**:

```sh
python3 scripts/check-legacy-commit-ids.py --baseline /home/riels/openmerge-sa-11b-main
```

`r1-replay-main` — actual exit **0**:

```sh
python3 scripts/check-live-journal-replay.py --baseline /home/riels/openmerge-sa-11b-main --candidate /home/riels/openmerge-sa-11b --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --strict-state --out /home/riels/.cache/openmerge-11b/r1-replay-main.json
```

`r1-replay-deployed` — actual exit **1**:

```sh
python3 scripts/check-live-journal-replay.py --baseline /home/riels/.cache/sa9b-r2-deployed --candidate /home/riels/openmerge-sa-11b --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --strict-state --out /home/riels/.cache/openmerge-11b/r1-replay-deployed.json
```

`r1-replay-shape` — actual exit **0**:

```sh
python3 /home/riels/.cache/openmerge-11b/inspect-replay-shape.py /home/riels/.cache/openmerge-11b/r1-replay-deployed.json
```

The external old-decoder probe ran `TestR1OldDecoderRejectsPolicyPin` against
unchanged main: pinned serial records both with and without `window` were rejected
for the unknown `policy` field. Its temporary test file was removed; the baseline
worktree remains clean. The first focused failure and successful retry are both
retained above. No foreign process was killed and no requested gate was skipped.

Round 1 `make check`, simulator exits, main compatibility, legacy SQL with negative
controls, depth 1/4 and four-method commit-ID differentials, and strict main replay
all exited **0**. Strict deployed-`3f77a88` replay exited **1**, retaining the known
absent-to-nil `classifications` difference only; its Gate 4 remains PASS. The exact
shape assertion exited **0**, covering 2,159 real and seven synthetic checkpoints.
No replay allowance or comparison harness changed.

Final explicit-refspec main fetch, protected-file comparison (Makefile, CI and
dogfood config), and `git diff --check` exited **0**. Main remained at the baseline
above, so no rebase was needed. Trial class remains **Hold**.

Round 1 control exits: commit-ID identical **0**, mutant **1**, dirty **2**
(all expected); SQL trace Go tests **0** for both revisions and every mutant;
depth-corpus Go tests **0** for both revisions in each corpus. Mandatory replay
synthetic self-tests: main **0**, deployed **1** (the same known classifications
boundary). The outer gate sequencer exited **0** after explicitly checking every
child status, including the deployed replay **1**.
