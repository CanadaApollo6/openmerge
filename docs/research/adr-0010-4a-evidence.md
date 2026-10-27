# ADR 0010 slice 4a: no-network evaluation and declaration-bound briefs

Status: **Hold until ADR 0009 period B drains.** The calendar estimate is not
permission to deploy. This slice supplies evaluation behind an internal seam;
it does not connect that seam to the live worker.

## Activation and persistence boundary

`serve.newProvenancePass` is package-private and has no production caller.
It accepts a validated baseline and owner-supplied declarations policy, binds
parser/classifier identity and local inventory limits into an effective policy
digest, and validates ordinary entry/merge expressions separately. Classification
must already be recorded under that digest before evaluation can issue a receipt.
There is no flag, environment variable, schema field, CLI option, Target hook or
public loader path that installs this pass. `config.Load` still rejects
`provenance.requirements` and all declaration attributes in ordinary expressions.
`provenance.LoadCandidate` remains a parser-only candidate API.

The pass calls slice 2's `gitops.ReadProvenanceInventory` against already-acquired,
self-contained local objects. It cannot acquire commits, query a forge, or resolve
checkpoints. The caller must keep those objects stable during the read. Git's
process cap and alternates refusal remain in the existing reader. Git failure and
NUL messages yield incomplete/pending inventories; typed limits remain terminal.
No partial inventory can issue a brief identity or pass the pre-CI extraction gate.

`provenanceeval` combines that inventory with immutable queue.State, the attributed
stance fold and the supplied fresh eligibility observation. `EvaluateApprovals`
continues to evaluate the original default/rules/owner obligations. The new
`EvaluateAdditionalApproval` shares its attribution, intent, accountability and
freshness boundary but calls `changepolicy.EvaluateRequirement` for exactly one
independent obligation. No baseline pool, default or owner obligation is replaced.
All triggers see baseline reviewers. A requirement condition sees baseline plus
its own approval pool; unresolved baseline membership remains pending.

The returned evidence separates baseline conditions/approvals, triggers,
rule-local conditions/approvals, inventory, pre-CI extraction outcome and the
final conjunction. Extraction failures exclude only their own member before
composition; ordinary review/CI waits remain speculative and must pass at landing.
No queue selection, trust, admission expression or merge method is changed.

There are **no journal append calls, new events, SQL changes, v9 cursor writes,
or Store/journalreader edits**. The existing worker, receipt producer and landing
transaction continue using their original paths. Slice 4b must atomically bind
these ephemeral results and fence their generation before installing the pass.
The retention/preflight/cursor-prefix writer blockers remain requirements of 4b.

## Brief identity

Only an internally opted-in Fact has `DeclarationDigest`. Its canonical domain
is `openmerge/brief/provenance-v1`. The digest binds slice 1's canonical complete
original declarations, their classes, and all requirement definitions whose
triggers cannot be ruled out from declaration inputs alone. Reviews, actual
applicability, satisfaction, checkpoint tips/outcomes, build-base observation and
read IDs cannot choose those definitions. Every static policy definition, identity
rule, parser/classifier version, Git version and inventory limit independently
enters the effective policy digest. Even a currently false rule edit invalidates
policy identity.

Absent `DeclarationDigest`, Fact JSON and canonical v2 serialization retain their
existing bytes and domains. No public status schema is changed. Existing brief,
receipt, landing-decision, status, wire and snapshot golden fixtures are preserved.
New opt-in canonical bytes and hashes live under
`internal/provenanceeval/testdata/`; separate baseline vectors pin the disabled seam.

## Proof map

| ADR proof | Tests |
| --- | --- |
| Separate baseline/trigger/local snapshots; both exact Alice/platform and Bob/security cases; pending membership, repeat evaluation and rule order | `provenanceeval.TestEligibilityAcceptance` |
| No cross-rule pool leakage; conditions-only rule uses baseline; unresolved baseline remains unknown in local conditions | `provenanceeval.TestRuleLocalConditionsAndNoCrossRuleLeak` |
| Recommended human rule, shared baseline reviewer, author/submitter/accountable exclusions, configured agents, single-writer block and independent baseline veto | `provenanceeval.TestRecommendedHumanRuleAndBaselineVeto` |
| Mutable reviews and actual applicability do not churn receipts; declaration/potential definition edits do | `provenanceeval.TestDeclarationBoundReceiptIdentity` |
| Mutable checkpoint refs do not churn receipts; per-change classification and speculative review wait | `serve.TestProvenanceNoNetworkPassAndCheckpointMovement` |
| Git failure, NUL and source-limit per-member exclusion; unrelated member progresses; alternates fail closed | `serve.TestProvenancePerChangePreCIExclusion` |
| Incomplete inventories cannot become Human or produce receipts; generation, head, revision, pinned base, parser/limits mismatches refuse | `provenanceeval.TestInventoryFailuresAndBindings` |
| Nested OR/AND/NOT ordinary-policy bypass rejected; no public configuration enables requirements | `serve.TestProvenancePublicLoaderAndNestedBypassRejected`, existing `provenance.TestCandidateSchemaAgreementAndPublicRejection` |
| Internal pass has no runtime caller or persistence dependency | `serve.TestProvenanceSeamHasNoRuntimeCallerOrWriter` (AST tripwire, plus source review) |
| Effective digest includes every definition, ordinary expression, full queue policy, and classifier/parser/identity/limits input; resolver attributes remain unsupported | `provenance.TestNoNetworkPolicyValidationAndIdentity`, `serve.TestProvenanceQueuePolicyEditsInvalidateIdentity` |
| Disabled evaluation is byte-identical | `provenanceeval.TestDisabledProvenanceBaselineBytes` |
| Existing baseline decisions, status, briefs and receipts remain byte-identical | Existing `queue.TestLandingDecisionGolden`, `queue.TestReceiptWireFactGoldenAndRejectedBindings`, `queue.TestWireV1Golden`, `brief.TestCanonicalVectors`, `brief.TestV1VectorsPreserved`, `brief.TestViewGoldensAndJSONAgreement`, `status.TestChangeReportingGoldens`, `status.TestReviewerCheckLegacyGolden`, `changepolicy.TestLegacyDigestAndMixedRepository` |
| All-parent inventory, pinned/build union and base rewrite, ceilings including prlimit behavior | Existing slice-2 real-Git inventory and reader tests, run as part of affected packages |

The added serve tests use a local object-only bare repository with no remote or
forge client. They create no `openmerge/*` refs and do not use a database. All
integration tests use the supplied isolated Postgres instance; no live APIs or
production database are accessed.

## Mutation proofs

Twelve one-at-a-time source mutations were applied, tested and restored. Every
mutation produced an assertion failure in the indicated test (not a build error).
The unmodified positive control passed before the run. No mutation remains in the
change. The local transcript/results are under
`/home/riels/.cache/adr0010-slice4a/mutation-*.log` and `mutation-results.json`.

| Removed or broken guard | Detecting test |
| --- | --- |
| Baseline uses an added pool | `TestEligibilityAcceptance` |
| Trigger uses an added pool | `TestEligibilityAcceptance` |
| Unknown baseline membership becomes a known expanded set | `TestRuleLocalConditionsAndNoCrossRuleLeak` |
| Other rules' pools leak into requirement conditions | `TestRuleLocalConditionsAndNoCrossRuleLeak` |
| Brief omits the declaration digest | `TestDeclarationBoundReceiptIdentity` |
| Potential rules are omitted unless their declaration-only trigger is already true | `TestDeclarationBoundReceiptIdentity` |
| Pre-CI filter includes failed members | `TestProvenancePerChangePreCIExclusion` |
| Static rule definitions omitted from policy hash | `TestNoNetworkPolicyValidationAndIdentity` |
| Nested ordinary-policy placement check removed | `TestProvenancePublicLoaderAndNestedBypassRejected` |
| Inventory observation generation binding removed | `TestInventoryFailuresAndBindings` |
| Ordinary expressions omitted from policy hash | `TestNoNetworkPolicyValidationAndIdentity` |
| Full queue policy omitted from effective hash | `TestProvenanceQueuePolicyEditsInvalidateIdentity` |

## Validation

All Go commands use the required serialization/resource wrapper:

```sh
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3 PATH=/home/riels/.local/share/mise/installs/go/1.27.3/bin:$PATH go <arguments>
```

Both `TEST_DATABASE_URL` and `OPENMERGE_TEST_DATABASE_URL` point to
`postgres://postgres@localhost:54329/postgres?sslmode=disable` for integration
and race tests. Validation logs are in `/home/riels/.cache/adr0010-slice4a/`.
