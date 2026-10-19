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
  is rejected; the legacy sibling lands. Its worker has nil Stage A wiring,
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

All heavy commands wait until no foreign `go test`, `*.test`, or journal replay
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
verification. No foreign process was killed. No live database, systemd unit,
production `openmerge/*` ref or live status API was accessed. Tests and simulator
use fake forges/local git only. The existing immutable journal exports are used
offline, never re-exported from the live database. Makefile, CI workflows and
their test flags are unchanged.

Fetch: `git fetch origin refs/heads/main:refs/remotes/origin/main` exited 0 and
confirmed the requested base. Both worktrees were created under `/home/riels`,
not `/tmp`; no database initialization was performed.

## Attempts and results

Pending final gate results. The final table below will retain failed attempts,
exact commands, actual exits, durations, and log hashes.
