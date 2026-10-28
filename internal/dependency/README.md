# Dependency candidates (ADR 0011 slices 1–3 Inert; 4a-1 Hold)

Production imports are limited to the shared reader, the store start adapter
and the private serve seam (slice 4a-1 below); no public configuration can
activate them. Its pure core accepts immutable
supplied facts. Slice 3 adds explicit unwired forge acquisitions exercised only
through fake contracts and HTTP fixtures; there are no database, journal or
filesystem reads.
It adds no public configuration or planner gate. Slice 2 supplies a separate,
unwired codec for new event kinds; the deployed queue/store codecs are untouched.
A structurally valid graph grants no permission to test, enqueue or land.

The candidate per-queue value is:

```yaml
dependencies:
  mode: ordered
  allow_from: [https://github.com/acme/api]
  allow_to: [https://github.com/acme/client]
  single_maintainer: false
```

`ParsePolicyCandidate` consumes only the mapping **inside** `dependencies`.
`testdata/policy.candidate.schema.json` describes that component, not a public
configuration. Missing policy disables dependencies. Missing or empty lists
grant no edges. Both endpoints need ordered policy and strict verification.
`ValidatePolicyScope` and `BilateralGrant` resolve every grant against the
supplied served registry in one GitHub installation and organization. Grants
are exact canonical repository URLs with no wildcards or transitive consent.
The candidate also accepts `single_maintainer: true` (omitted/false is strict).
It is included in `PolicyDigest`; this does not enable the public config loader.

## Pure API boundaries

- `ParseReference` accepts the versioned canonical URI and rejects foreign forge,
  installation and journal namespace before any ID lookup. Namespace generation
  and incarnation binding use slice 2 below; this API accepts an already bound
  scope and never manufactures one from declaration text.
- `ParseIntent` consumes the authoritative PR body, including trailer-shaped
  lines in that body. It never accepts a list of commit messages or PR comments.
  The raw body is capped at 65,536 Unicode characters before parsing (including
  line endings and any BOM); overflow returns `ErrBodyLimit`, never truncation.
  A leading BOM is stripped and CRLF and bare CR become LF, following GFM's
  line-ending rules. Goldmark v1.8.6 (pure Go, MIT, no transitive dependencies)
  parses CommonMark with the full GFM extension set (tables, strikethrough,
  linkify and task lists) plus its built-in footnote extension.

  A **declaration paragraph** is a top-level paragraph containing only exact,
  case-sensitive `Depends-On: <ref>` source lines, with at most three leading
  spaces and exactly one non-whitespace reference per line. Several declarations
  may share one paragraph. Its inline AST may contain Text (including soft or
  hard line breaks) and URL autolinks only when their label and destination both
  exactly equal a literal reference on that paragraph's source lines. Thus GFM
  linkify preserves plain URL declarations; angle-bracket autolinks, explicit
  links, HTML, images, code spans and emphasis are ambiguous.
  Source spelling is preserved; escapes or markup cannot manufacture a key or
  reference. Put a blank line between declarations and surrounding prose or
  trailers, for example:

  ```text
  This PR needs the API change.

  Depends-On: https://github.com/acme/api/pull/42
  Depends-On: https://github.com/acme/client/pull/17
  ```

  Candidate detection scans every normalized physical source line independently
  of the AST. It strips leading container markers, decodes Markdown backslash
  escapes and HTML entities, deletes every Unicode format character (category
  Cf, including zero-width characters, mid-line BOM and soft hyphen), applies
  Unicode NFKC normalization and case folding, and maps Unicode Dash characters
  and common hyphen/minus and colon lookalikes to ASCII, in that order. Any
  resulting `depends-on:` substring is a candidate, wherever it occurs on the
  line. A second pass joins rendered inline text in each non-example AST block,
  dropping emphasis, strong, strikethrough, link markup, raw inline HTML tags
  and inline comments. Code-span contents remain text. It applies the same
  normalization, catching keys such as `**Depends-On**:`, `Depends-**On**:`,
  `Depends-<!-- x -->On:` and `[Depends-On](<URL>):`. Neither scan widens the
  accepted grammar: only exact, case-sensitive source declarations produce inputs.

  Every candidate outside an ignored example region must be an exact accepted
  declaration line; otherwise the entire parse returns `ErrDeclarationAmbiguous`
  (`declaration-ambiguous`), with guidance to put each declaration in its own
  paragraph, with nothing else. This includes mixed prose, task/ordinary lists,
  headings, table cells, strikethrough, inline HTML, code spans, hidden link/image
  titles, and declarations inside referenced or unreferenced footnotes. Entity,
  escaped, lowercase and Unicode lookalike keys also block. For example,
  `&#68;epends-On:`, `Depends\-On:`, `depends-on:` and `Depends‑On：` are ambiguous.
  Fenced/indented code blocks, block quotes (including lazy continuations), HTML
  blocks and HTML comments remain ignored examples, including inside footnotes.
  Example coverage and rendered candidates are recorded before unused footnotes
  disappear from the AST. Inline comments shield only their own source; removing
  them may join a rendered candidate, which is ambiguous. A declaration followed
  by a comment is also ambiguous. An error returns no partial inputs. Literal
  comment markers in code spans or escaped text do not start comments; unclosed
  blocks follow CommonMark.

  Accepted residuals: cross-script homoglyphs such as Cyrillic `е` and Greek `Ο`
  are not detected; that would require a confusables skeleton. HTML blocks remain
  ignored example regions by design, so `<div>Depends-On: …</div>` and a line
  beginning with `<!-- x -->` are ignored even if GitHub renders declaration text.
  A declaration hidden in the author's own body is equivalent to never writing
  it. Removing a previously parsed edge is a loosening edit: ADR 0011's
  declaration-change hold catches that edit and requires independent
  acknowledgement. That hold is represented by slice 2 below; this package remains inert.

  This strict grammar refines ADR 0011's “standalone body lines” (~line 181).
  **Slice 6's operating guide must document this rule for users**, including blank
  lines around declarations and blocking ambiguity diagnostics.
- URI segments use RFC 3986 unreserved-only, uppercase percent encoding of UTF-8
  bytes. Encoded unreserved bytes, lowercase escapes, raw reserved characters,
  credentials, invalid UTF-8, controls, query/fragment syntax and extra segments
  are rejected. Spaces in an installation segment are encoded as `%20`. Origins
  are supplied canonical HTTPS origins, including a configured nondefault port.
  Explicit default port `:443` and trailing-dot host spellings are rejected.
- `Resolve` accepts only supplied binding records. The currently recognized URL
  alias shape is a GitHub HTTPS `/owner/repo/pull/positive-number` URL on the
  configured origin. It binds revision-less to exactly one stored queue key
  including base/lane. Unknown IDs, ambiguous aliases/bindings and unserved or
  foreign-organization repositories fail closed. Callers must retain this binding
  for a declaration; calling Resolve against a later registry is not a refresh
  protocol. At most 1024 raw inputs are accepted; overflow fails before any
  binding scan, even if they would deduplicate. Slice 2 retains bindings and fences binding/edit races; persistence
  and scoped forge-read adapters belong to later slices.
- `Normalize` sorts by journal-scoped change ID and deduplicates resolved edges.
  Revision zero means revision-less; explicit pins are positive uint64 observation
  sequences without leading zeros, matching ADR 0007's revision type. Conflicting
  pins or revision-less/pinned mixtures for one change block the declaration.
  The 32-edge limit applies after resolution and deduplication, so repeated
  spellings of one edge consume one edge. A rejected declaration returns no
  partial permissive result.
- `Graph.Evaluate` returns derived authorized-only diagnostics, never an admission
  decision or serialized cycle state. It visits only the requested root's closure.
  Unknown discovery is blocking. Traversal counts the root at depth zero, allows
  256 distinct nodes / 512 distinct directed edges / depth 16, and checks each
  traversed edge's bilateral consent. Served repositories are indexed once per
  evaluation, and endpoint queue/policy validation is cached by node for that
  evaluation only. Shared nodes reached through a longer path are rechecked for
  depth, with at most 17 expansions per node; equal/shorter paths do not expand
  again. Self-edges produce `dependency-cycle` before the same-repository consent
  check. Edge order and map order cannot select a cycle winner.
  A supplied confirmed, on-base landing matching an incoming optional pin is
  terminal; current consent is still required. The source's later declaration
  need not be complete and cannot replace that landing's historical decision.
  The API does not establish landing freshness, read access, lifecycle validity,
  eligible acknowledgement, or landing-start authorization.

## Proofs

- `TestCanonicalURIGolden`, `TestCanonicalURIRejectsMalformedAndForeign`:
  fixed canonical URI/escaping vectors, optional pins, malformed and foreign scope.
- `TestAuthoritativeIntentBoundaries`, `TestMalformedRecognizedIntentFailsClosed`,
  `TestIntentCommonMarkCorpus`: body/trailer and CommonMark/GFM/footnote boundary cases,
  including inline/escaped comment markers, list fences, lazy quotes, nested
  lists, setext headings, HTML blocks, multiline code spans, CRLF and tabs.
  `TestIntentAmbiguousDeclarations`, `TestIntentPureDeclarationParagraph`,
  `TestIntentLinkifyPreservesExactReference`, `TestIntentBareCRFence`,
  `TestIntentBodyCharacterLimit`: strict paragraph
  grammar, review repros, normalization and exact Unicode character cap.
- `TestRevisionlessAliasesOptionalPinsAndDedupe`,
  `TestResolutionRejectsUnservedAmbiguousAndForeign`: resolution and conflicts.
- `TestCycleDiagnosticsDerivedAndDeterministic`,
  `TestIncompleteAndInvalidGraphsBlock`, `TestBilateralGrants`,
  `TestMatchingLandingIsTerminalButRetainsConsent`: bounded pure graph contracts.
- `TestDirectEdgeLimitAfterDedupe`, `TestGraphNodeLimit`, `TestGraphEdgeLimit`,
  `TestGraphDepthLimitAndSharedLongPath`, `TestGraphLadderExpansionBound`: exact
  boundaries and overflow diagnostics; a 64-node / 244-edge / 16-layer ladder
  expands exactly 64 times. The test stops at expansion 65 if memoization breaks.
- `TestResolveInputCapBeforeBindingWork`,
  `TestOriginsRejectAlternateCanonicalSpellings`: raw input cap and origin syntax.
- `TestGraphPolicyCacheIsPerNode`: two nodes in one repository with distinct
  policies cannot share validation results; each evaluation rebuilds its cache.
- `BenchmarkEvaluateLadder1024Served`: the ladder with 1024 served repositories
  and full grant lists on every node, exercising scoped-validation caching.
- `TestCandidatePolicySchemaAgreement`, `TestPublicConfigRejectsDependencyKeys`:
  candidate/schema agreement and public rejection.
- `TestLegacyConfigParsingAndDigestsByteIdentical`: frozen origin/main vectors
  for parsed JSON bytes, warnings, errors, both cleanup digest variants and
  depth 1/4 configs. The public loader/schema and all deployed contracts are
  untouched; existing `internal/config` tests retain their legacy digest vectors.
- `FuzzParseIntent`: corpus seeds, successful canonical round trips, and an
  independent AST walk asserting every produced declaration comes from a pure
  top-level declaration paragraph with Text or exact URL-autolink children.
  It also checks every decoded source candidate: a successful parse must account
  for it as an exact accepted line or an AST-classified ignored example, even
  when Markdown hides it. An independent inline-leaf walk checks rendered-text
  candidates through formatting, links, code spans and inline tags/comments.
  The oracle retains unused footnotes for both checks.
- `FuzzParseReference`: accepted input equals its canonical spelling.
- `FuzzParsePolicyCandidate`: successful policies validate and round-trip through
  YAML without changing mode or grants.

## Slice 2 journal and side projections

`Event` is deliberately separate from `queue.Event`. `EncodeEvent`/`DecodeEvent`
accept only the six new wire-v1 kinds supplied by this slice:
`dependency_journal_initialized`, `change_dependencies_declared`,
`dependency_policy_applied`, `dependency_observed`,
`dependency_declaration_acknowledged` and
`dependency_declaration_self_acknowledged`. Composition, invalidation and decision
companions belong to the later planner/start slices. No existing payload acquires
fields. Every field in these new records is required, including false/zero values
and empty arrays. Unknown/case-aliased/duplicate fields, null collections and
trailing JSON fail closed at every nesting level.

`NewJournalIdentity` consumes the exact ADR 0008
`openmerge-db-v2:sha256:<digest>` receipt identity and a supplied nonzero random
256-bit incarnation. It hashes versioned JSON with a namespace-specific domain.
There is no random-token generator, events-lock acquisition, database identity
read or startup call in this package. A future explicit initializer must generate
and journal the token once under the database events lock. Replay validates the
recorded namespace; `VerifyBinding` and dependency snapshot decoding require an
independently verified expected binding. Restore, clone, schema recreation and
reinitialization use a fresh token with stopped/drained workers and reconciliation,
then fresh declarations/authorization. Copies preserving both identity and token
are indistinguishable: fencing copies and rotating tokens is an operator recovery
requirement, not an automatic physical-clone detector. A second initialization
cannot be folded over an existing namespace or remap its declarations.

`Apply` consumes ordered supplied journal IDs, allowing gaps for ordinary queue
events. It returns immutable state. An identical retry of the last dependency
record is idempotent; conflicting duplicate IDs and older/out-of-order records
return `ErrStale`. This bounded high-water dedupe supplements future store dedupe
keys; it is not an unbounded delivery ledger. Alias declarations, queue policy
companions and current consumer-change/revision/source observations are private
side maps. Limits are 4096 alias/queue-policy entries and 4096 × 32 current
observations, plus 1 MiB event / 64 MiB snapshot caps. Overflow blocks rather than
silently dropping a hold. There are no SQL/index/retention changes.

`BindDeclaration` consumes a poll's final normalized inputs and
`CoalesceEditors`' union of all authenticated identities plus any unknown or
unauthenticated attribution. A polled body without identified edit history must
supply unknown attribution. One alias-scoped prior event/digest and increasing poll
sequence fence concurrent bindings and bound observations to one per poll. A
semantic no-op returns nil; explicit authenticated redeclaration may restore
attribution. Surviving aliases keep their old change/queue binding, and surviving
edges keep their baseline revision/head/intent digest. Removal/re-addition in
separate observations binds anew. Baselines stay outside the declaration digest;
source observation facts retain the revision that actually landed separately.

Declaration events include the complete resulting hold/editor/unknown-actor state;
the fold recomputes and checks it. Stable forge instance, installation, repository
ID and PR number key this state across retargeted queue/change IDs. The new change
must supply the prior alias basis, including a final empty declaration. Tightening,
policy disablement, dequeue/re-request, repository rename/transfer and #163 request
admission cannot clear the hold. An authenticated declaration edit or explicit
redeclaration can restore attribution but keeps all editor exclusions.

Acknowledgements bind the current declaration event/digest, freshly supplied
identity-bound B writer grant, and exact editor/author/submitter/accountable-human
exclusions. Reverting a digest or replaying an old action grants nothing. The
distinct self-ack kind additionally binds overridden exclusions and the current
B queue's governing policy companion. `PolicyApplied` records default branch,
read ref/ID, governing commit/digest, enabled state and authenticated writer
observations. A head-only read is rejected. Forge read freshness, identity-bound
permission acquisition and governing-owner policy approval are trusted supplied
facts here; adapters and authority wiring belong to later slices. A's policy has
no effect on B. The hold/status projection carries the distinct acknowledgement
kind and the nonblocking multi-writer warning, counting writers before exclusions.
Revoking the opt-in or disabling its policy restores the hold while retaining the
self-ack evidence; re-enabling cannot resurrect that old action. This pure policy
fold models the recorded landing-start reread; no landing-start transaction is
wired here. Normal approvals/CI/trust/owner review remain separate.

## Snapshot and rollback boundary

`dependency.EncodeSnapshot` returns the existing queue codec's exact bytes when
there is no dependency history. With initialization, it explicitly selects the
new **v8 wrapper**, embedding the unchanged queue snapshot and bounded side state.
`dependency.DecodeSnapshot` supplies the v8 reader through `journalreader`. The
production `queue.SnapshotVersion` (7), its automatic v6/v7 selection and complete
legacy-only store cursors are unchanged. Side-era audit cursors nest this v8
wrapper inside v9. The wrapper supports v2–v7 queue decoding through delegation;
it does not turn on general persistence, retention or compaction.

The wrapper preserves #163's `HeldRequests` and cached
`PRObserved.PendingConditions`. Neither clearing dependency holds nor using a
legacy version number makes those fields readable by pre-#163 strict readers.
The first new event or persisted v8 wrapper crosses a further binary rollback
boundary. Apply **3c-style reader-first rollout**: deploy explicit compatible
readers everywhere, drain all incompatible workers/readers, and verify the
whole-database backup before side-event emission is enabled in a later slice.
This Hold migration enables only composed audit-cursor persistence after the
format boundary, never event emission. Removing config does not erase history. Rollback
requires a compatible reader, or stopped workers, retained later evidence, the
tested pre-emission backup, token rotation and reconciliation of actual remote
refs/subsequent landings. Never delete/skip new records or blindly resume restored
state. ADR 0009 dogfood period B drain remains the activation prerequisite.

## Slice 2 proofs

- `TestDependencyWireStrictRequiredUnknownNullAndRoundTrip` recursively removes,
  adds, nulls and changes case on every field of all six kinds, including nested
  grant/baseline/policy/hold fields; rejects duplicate keys and oversized inputs.
- `TestDependencySnapshotStrictValidation` checks recursive snapshot structure,
  duplicate aliases, impossible IDs, forged independent/self acknowledgement,
  missing governing opt-in, unknown actors and revoked/cleared hold corruption.
- `TestBindingPollRacesBaselinesAndImmutableDedupe` covers CAS races, poll
  coalescing/no-op edits, immutable events/maps/accessors, duplicate IDs, frozen
  revision-less aliases/baselines and separately observed removal/re-addition.
- `TestObservationRacesAndRevisionlessDigest` rejects delayed success/old basis
  and preserves actual landed revision/intent alongside the fixed baseline.
- `TestJournalNamespaceGoldenAndRecovery` fixes namespace framing bytes and
  rejects foreign histories with identical IDs/revisions, missing binding,
  restore/reinitialization/schema recreation and physical-clone token rotation.
- `TestDeclarationHoldStrictWriterAuthorAndRetarget` rejects writer-author,
  submitter/accountable-human self-ack without opt-in in one-/multi-writer repos;
  retargets to another enrolled base under a new change ID, preserving empty
  prior declaration/hold, and covers rename/transfer and a fresh replacement PR.
- `TestSurvivingNativeApprovalCannotAcknowledgeRemovalAtSameHead` retains a
  real native-review fixture at the same code revision while removal stays held
  without opt-in; opted-in self-ack clears only the dependency hold and preserves
  the exact ordinary-approval snapshot bytes.
- `TestTighteningDigestABAAndFreshGrants` covers exact-pin weakening,
  tightening-after-loosening, digest ABA, permission revocation/ID mismatch and
  authenticated eligible recovery. `TestCoalescedEditorsUnknownAndAuthenticatedRedeclaration`
  covers editor union, unknown/unauthenticated actors even with opt-in, and
  authenticated redeclaration retaining exclusions.
- `TestSingleMaintainerGoverningPolicyAndRevocation` covers one-/multi-writer
  acceptance through the distinct kind, nonblocking warning, head-only refusal,
  A-vs-B policy scope and recorded landing-start opt-in revocation. The event and
  overridden/governing evidence survive replay/snapshot even after revocation.
  `TestDisablingDoesNotClearHoldAndAuthenticatedEditRestoresAttribution` covers
  authenticated semantic-edit recovery, forged cleared holds and policy disablement.
- `TestReplayEverySnapshotTailWithHeldRequests` compares bytes, hold/status and
  next actions at every revision and snapshot cut at planner depths 1 and 4,
  including waiting observations, #163 pending-condition admission, dequeue/re-request,
  exact-head replacement, self-ack, policy revocation and independent ack.
- `internal/store/TestDependencyLegacySnapshotAndCursorGolden` uses the actual
  deployed cursor struct and all deployed wire-v1 event/action golden fixtures.
  The two `legacy-held.*.json` goldens were generated in an isolated `git archive`
  of origin/main **987eb70e6bb02be02636799d529d9d6ded5bed6c**, using the existing
  encoders and #163 held-request sequence, never by this implementation. The
  test does no database or forge I/O.
- `TestNewKindsAreNotRuntimeQueueEvents` and `TestNoProductionDependencyImports`
  guard the unwired boundary and old-reader refusal. Candidate schema tests
  still prove that the public loader rejects dependency configuration.

## Slice 3 scoped acquisition and reconciliation (Inert)

`ObserveEdge` is an explicit, unwired acquisition boundary. It requires trusted
queue-command evidence for an enqueued consumer before any permission or graph
read; neither PR text nor App visibility grants admission. It checks the
consumer's authenticated declarer against a fresh per-user permission response
on the source repository before requesting the source lifecycle/body. The same
operation runs independently at binding, work preparation and landing-start;
no stage reuses the prior stage's permission. `WriteGrant.CanRead` evaluates the
identity-bound response at read level, including triage/read, without changing
`CanWrite` or any existing permission caller.

The optional `forge.DependencyReader` and fake/GitHub adapters are not called by
production. GitHub consumes one fresh PR response, rejecting missing identity,
head, base, body, state or merged fields (including a missing false boolean).
The observer compares stable repository/PR IDs and the retained queue/change
binding. A retarget diagnoses the old binding rather than following the new
change. Closed-unmerged and externally merged sources block. Only supplied
settled OpenMerge evidence plus ancestry at the exact freshly observed base can
produce `landed`; a merged flag or matching head cannot. Historical landed
revision/head is retained separately from the revision-less binding baseline.
Ancestry and settled-journal acquisition remain supplied contracts, not a new
production reader. They must be revalidated/fenced by slice 4's start protocol.

`ReadEvidence` returns the declarer, ID-bound source grant/read ID, read stage,
declaration basis, read sequence/CAS, actual observed revision/base and typed
reason for trusted persistence/planner callers. A binding read operates on a
prospective declaration; it must complete before that declaration is committed.
A future store adapter must assign/check its final declaration event basis and
persist grant evidence in a **new** companion kind. This slice adds no event
fields, codec kinds or runtime emission. Acquisition alone grants no permission
to test or land. Failed reads produce unavailable observations with the retained
baseline, never inferred fresh source facts or a reusable prior success.

`ScopedObservation` defaults to generic JSON. `View` accepts separately recorded
complete audience grants; one missing, unknown, mismatched or denied reader
returns exactly `Dependency cannot be verified`, without typed codes, source
facts or a success signal, across all source states. `Evidence` is explicitly
for authorized internal adapters and must not be rendered or logged publicly.
This fixes the acquisition contract; the complete status renderer and scoped
audit export remain slice 5. Rendering performs no forge reads.

`ReconcileEdges` accepts exactly the current declaration's edge set and checks
its basis before I/O. Read failures affect their own edge, and Apply's existing
CAS/read-sequence fence rejects delayed successes. `RebuildReverse` derives
sorted direct-consumer wakes from replay/snapshot facts, including unqueued
sources. An absent or outdated index is an error, never an empty-graph proof.
`PollDeclarations` independently enumerates dependency-bearing declarations so
missed wakes are repaired through ordinary polling and start-time rereads.
Neither wakes nor polling enqueue sources. Runtime scheduling, durable replay
binding and declaration/graph authorization must remain mandatory when wired;
`State.VerifyBinding` now provides the full-replay identity fence.

Both permission and scoped source reads use the existing per-installation
transport, conditional revalidation and quota/backoff accounting. They downgrade
settlement priority even when called from a landing-start context. The final
10% remains reserved for settlement; no second budget or central scheduler is
introduced. Runtime poll scheduling remains unwired.

Proofs:

- `TestDependencyObservationContract` runs the same `dependencytest.Run` against
  `forge/fake` and the GitHub HTTP fixture. It covers read-level grants, denial,
  identity mismatch, fresh revocation at all three stages, closure, source
  retargeting, merged flags, scope, superseded pins, settled/on-base evidence,
  failed/partial reads, writer-command admission and mixed-audience redaction.
- `TestDependencyHTTPMissingFieldsFailClosed` rejects partial HTTP observations.
- `TestDependencyReadsPreserveInstallationSettlementReserve` charges permission
  and source reads, blocks both at the final 10% even with settlement context,
  preserves another installation's capacity, and admits actual settlement.
- `TestReverseWakesRestartAndPollingRepair`,
  `TestReverseWakesSortedConsumersAndRemovedEdges`, and
  `TestReconcilePartialReadsAndDelayedSuccess` prove reverse-index rebuilding,
  polling without a wake, exact complete edge sets, partial-read isolation and
  delayed-read fencing.
- `projection_guards_test.go` closes #174 with direct forged-event, stale-ID,
  policy-CAS, retarget revocation, warning, editor reset, unheld-ack, snapshot
  basis/duplicate/order, two-alias sort and full-replay binding tests. Each
  associated guard was disabled locally and its focused test failed; the PR body
  records all 16 mutations. The original guards were restored before validation.

The shared reader migration now composes this codec through
`internal/journalreader` (Hold; see provenance's README). Production code still
cannot append dependency events; v8 is encoded only inside composed v9 audit
cursors after the format boundary. Unverified namespace and
incarnation projections remain quarantined, never satisfied dependencies; the
future startup/writer integration must supply independently verified identity.


## Shared-reader writer blockers

**Writer prerequisites delivered under Hold for ADR 0010 slice 4b and ADR 0011
slices 4a/4b (production integration remains disabled):**

- **Shared transaction.** `appendSideTx` accepts the existing queue-locked
  transaction. The unwired `startLandingWithSide` seam supplies exact inserted
  decision IDs and commits decisions, companions, v9 cursor upgrade, dedupe
  manifest and append checkpoint together. A failure after decision insertion
  rolls everything back. The ordinary production start supplies no companion
  builder. Acquire the queue advisory before action/observation locks or event
  INSERTs; FK `KEY SHARE` on queues is part of this order. Never call the public
  wrapper in a second transaction while holding the first transaction's lock.
- **Landed recovery.** The unwired `RecoverLanded` helper checks an immutable
  `Landed` row against the immediately preceding queue state, rejecting stale
  outcomes and mismatched attempts/decisions. It validates exact historical batch
  membership, revisions and heads, the accepted decision's tested identity, and
  the terminal member's final landed composition (intermediate results remain
  valid). Close, dequeue and head-change events preserve a pending landed
  companion until recovery completes. V9 checkpoints optionally retain bounded
  `Awaiting` attempt/outcome markers, including alongside `FenceTable`; deploy
  this reader before writing those checkpoints. Original event fields do not
  change. An existing companion under another key
  returns typed `LandedAlreadyCompleteError`. Its deterministic outcome key
  compares complete canonical bundles on retry. The caller must reconstruct the
  actual landed composition from immutable inputs: a `Landed` row alone cannot
  supply source-to-build mappings. Recovery appends the missing companion once,
  allowing tested evidence to retire. Future startup/recovery wiring must invoke
  it before starting new work; no production caller is enabled here.
  A recovery bundle may contain only the landed companion and inventory chunks
  consumed by its exact member generations. Reconstructed inventories must match
  the historical tested inventory after rebinding the generation and exact
  predecessor; unrelated side records are refused. Recovery preserves the dynamic
  retirement reserve for every remaining active inventory and attempt, even when
  bypassing the configurable admission high water.
- **Lifetime fences.** Exact arbitrary string identities and original journal
  anchors are retained in a lossless, sorted, front-coded compressed fence table.
  A watermark/range is unsound because these IDs have no monotonic allocation
  contract. The strict `FenceTable` v9 checkpoint shape reads both earlier v9
  shapes; legacy-only queues retain their full byte identity. Replay measures
  compressed capacity geometrically; bundle preflight measures once and final
  checkpoint encoding reuses the unchanged pack. The 32 MiB projection ceiling
  still applies,
  including base64 framing. Decoding also bounds expansion to 128 MiB and
  2,097,152 fences; preflight and encoding enforce these same memory bounds.
  This reduces lifetime metadata substantially without
  pretending that arbitrary, unbounded histories fit a finite cap. The scale test
  folds 200,001 real landings outside short mode (400 in short mode), asserts
  linear capacity-measurement work, and checks restart, suffix equivalence and
  exact generation/attempt reuse rejection. Original audit rows never change.

- **Capacity and queue-event reserve.** Side append preflights once per complete
  bundle. It caches the exact fence pack for the final cursor encode, rather
  than packing per record. `Store.WithSideHighWaterPercent` configures the
  admission threshold (default 80% of the 32 MiB provenance projection cap;
  values must be 1–99). The same fraction reserves expanded fence bytes and
  entry capacity. Queue-driven retirement and recovery replay retain the
  hard cap, reserving space for terminal fence growth. Queue status JSON exposes
  `provenance_capacity.fence_count`, `projected_bytes`, `limit_bytes` and
  `high_water_bytes` for queues with retained side evidence. Alert at 70% of
  `limit_bytes`, before the default 80% admission refusal. Track fence count and
  growth rate to plan rollover well before refusal; raising the threshold spends
  the queue-event reserve. Projected bytes include active canonical evidence,
  compressed/base64 fences and framing, but exclude the queue/dependency base;
  final snapshot encoding independently enforces the complete snapshot cap.

**Still BLOCKING before enabling writers on a high-volume queue:** a structural
fence bound (e.g., deriving attempt/generation IDs from monotonic journal positions
so a watermark can replace per-ID fences), or an explicit lifetime/rollover
policy. Lossless compaction only delays the cliff: the reviewed identity
distribution reaches the hard projection cap at about 281k lifetime landings;
the default high-water mark refuses new side evidence earlier. This estimate
is workload-dependent, not a supported lifetime guarantee. No structural bound
or rollover policy is implemented here. Production integration, reader-first
rollout, authority/start fencing, namespace recovery and owner opt-in remain
gated; no production writer is enabled.

The **Hold writer foundations** provide the unwired `Store.AppendSide` API.
It validates an entire bundle through the composed replay fold under the queue
lock before any INSERT, preflights capacity, and upgrades the existing audit
cursor to v9 in that same transaction. No production caller is enabled. The
writer guard allowlists only this adapter and forbids production callers.

Projection compaction retires completed tested/landed pairs, terminal non-landed
attempts (including abandonment), and generations dropped by dequeue, closure,
head/revision supersession or queue retirement. Uncertain fenced landings retain
evidence until their definitive outcome. Exact generation/CAS and attempt fences
survive restart; retired identities cannot be reused.
Original journal evidence is retained forever for ADR 0008/0010 audit closure;
settled and superseded generations remain exportable by original ID. A fresh
generation is required before further use of a retired inventory. The active
budget is 4256 records / 32 MiB (including compact metadata), with at most 32
unfinished tested companions, 385 reference targets per companion and 12,320
in aggregate. These derive from batch size 128 and speculation depth 32, with
per-member headroom, and match lookup caching. Writer preflight stays below the record/byte caps;
capacity refusal must never publish effects or partial bundles. Dependency holds
are not pruned. See the complete [retention decision, cursor invariant and
recovery procedure](../../docs/operating.md#shared-reader-retention-recovery).

The owned replay fold updates lifetime fence maps without copying them per event.
Append and Load resume a separate append/audit checkpoint and its paged suffix;
append checkpoints never advance outside-push audit progress. Checkpoint work still follows lifetime identities, with exact rejection fences
compressed in the derived checkpoint. The finite byte budget remains enforced.
AppendSide refuses queue events, including decisions, which remain on the existing
authorized landing path. Dedupe retries compare complete canonical bundle bytes,
length, reserved IDs and independent binding before returning success.

V9 loads revalidate only retained unfinished companion references. Audits retain
the 256-row suffix bound, including after large completed histories. The original
v9 shape remains readable, while a strict `Compaction` checkpoint shape persists
retired fences, and the strict `FenceTable` shape compresses those same fences
losslessly. Legacy-only journals retain their exact full cursor bytes.
The first future side append/compact cursor crosses the rollback boundary:
deploy compatible readers and audit v2 everywhere first, drain incompatible
workers/readers, verify the pre-emission backup, and reconcile external effects
before any restore. No writer may activate until ADR 0009 period B drains and
its later authority/startup/owner rollout gates are satisfied.

Invalid records or unresolved references abort queue `Load` and fail `/status`
and `Journals` for every queue in the requested key set. Events are immutable;
recovery requires a code fix or restore. This availability risk is comparable to
the 2026-10-16 wedge: writers must validate before append. Follow the
[operator procedure](../../docs/operating.md#invalid-records-and-unresolved-references).

The AST writer guard is a **tripwire**, not proof of writer absence: it does not
catch string concatenation, `.sql` files or `_test.go` files. The only allowed
production v8/v9 composer serves derived audit cursors; review any new
persistence adapter independently.

## Slice 4a-1 planner gate, decision companion and landing-start writer (Hold)

**Activation is test-seam only.** The public loader still rejects every
`dependencies` key (`TestPublicConfigRejectsDependencyKeys` is unchanged). Only
the private `serve.dependencyPass`, installed by internal tests, may call
`store.WithDependencyActivation` or `store.StartDependencyLanding`, and only
inside `if w.dependencies != nil` branches (`TestActivatedDependencyWriterGuard`,
`TestNoProductionNewEventOrSnapshotWriters`). Without activation every load,
plan, start, journal row, cursor and digest is byte-identical: the overlay field
is nil, `Plan` takes its original path, and `startLandingWithSide` still passes
literal nil provenance/dependency evidence. Existing goldens are untouched.

- **Admission from recorded facts.** `State.Admission(change, revision)` returns
  a typed blocking code or the exact `AdmissionBasis`: the alias declaration at
  this revision (an absent declaration is `dependency-unresolved`, never an empty
  graph), the queue's enabled policy companion and `allow_from`, any current
  acknowledgement (a revoked self-acknowledgement re-holds), and one current
  `landed` observation per edge. It performs no I/O and is not landing authority.
- **Planner gate.** An activated load attaches `queue.State.WithDependencyWaits`,
  a planner-only overlay keyed by PR and pinned head. Apply, snapshots and wire
  bytes never see it. `batchCandidates` skips waiting changes and prunes them from
  pending bisection/retry groups, so a wait consumes no slot, is never bisected or
  blamed, and never dequeues. The same `batchCandidates` drives depth 1, ADR 0009
  chained admission at depth 4 and `NextWake`; `CanStart` revalidates the same
  gated view under the queue lock. A missing value fails closed.
- **Journaled regroup.** The fold starts only its exact first pending group, so
  the planner's prune must be durable before a start that relies on it. When the
  gated view withholds pending members, the start authorization appends the new
  queue kind `dependency_waits_withheld` (exact members and heads) immediately
  before `BatchStarted`/`ChainedBatchStarted` in the same transaction, and
  refuses (cancels as stale) unless the fold then accepts the start. Withheld
  members leave their groups and keep their entry order: unrelated eligible
  changes pass them (ADR 0011 Stage A), and once eligible they compose freshly
  from the head queue. Holding the whole group instead would block every start,
  because the fold admits no fresh batch while a pending group exists. Replay
  folds the record without the overlay; it is a no-op unless every member is
  still pending at that head, and unactivated queues never emit it.
- **Wait clocks.** Batch `max_wait` is computed only from eligible candidates.
  Slice 4b-1 replaces the 4a restart approximation with accumulated eligible
  intervals. `EligibleAt` is the current eligible interval's start minus earlier
  eligible duration since enqueue. Rewinds suspend the clock; recovery resumes
  its remaining budget. Equivalent refreshes do not change it. Clock history is
  streamed only for activated queues, bounded to 32,768 eligibility records and
  32 MiB, and fails closed at either bound. Planning, consequential admission and
  emission prepare clocks before advisory locks; the locked load compares side
  position and exact entry identities. Landing transactions skip formation clocks.
  There is no conflict-grace clock in the codebase yet.
- **Decision companion.** `dependency_decision_recorded` is a new wire-v1 kind,
  one per batch member so each stays far below the 1 MiB event cap with 32 edges.
  It binds namespace, batch/attempt, tested and landed SHAs, the exact
  `LandingStarted` row, the member's own decision row (zero only when the queue
  records no approval decision), member index/count, alias/change/revision/head,
  the admission basis and per-edge source settlements (declarer read grant,
  landed revision/head/commit, settlement event, source watermark and base). The
  fold requires it to equal the CURRENT admission; the shared reader requires the
  start, decision and batch member to resolve in this queue and every observation
  to be recorded after the start and before the companion.
- **Landing-start transaction.** `StartDependencyLanding` enters
  `startLandingTransaction` with dependency evidence (never together with the
  provenance writer). It locks B plus the supplied direct sources in ascending
  queue-ID order before any other lock, then re-derives the direct-source set from
  B's current declarations and refuses any difference. Before taking any lock it
  verifies each distinct source settlement once: replaying the immutable prefix
  before it, the outcome is an accepted `Landed` for a landing batch. That prefix
  cannot change once the row commits, because every journal insert holds its
  queue's advisory lock through commit. Under the source locks it replays each
  source's current fold once (from its checkpoint), re-reads the settlement row
  byte-for-byte, and checks the source's own policy companion (`allow_to` B), an
  unchanged journal base since the fresh ancestry read, and that the settled
  batch bound the exact source change revision/head and commit. It refuses while any accepted
  `Landed` awaits `RecoverLanded`, compares the fresh governing policy reread with
  the recorded companion, and folds the supplied landing-start reads before any
  insert. Fresh eligibility, decisions, unchanged `LandingStarted`, the landing-
  start observations and companions then commit through `appendSideTx` in one
  transaction, which also upgrades the outside-push cursor to v9 at its existing
  watermark. `AppendSide` cannot admit a companion: only the start supplies its
  bound start row. Every refusal returns `ErrLandingDecisionStale` and rolls back.
- **Worker wiring.** Each cycle and execution calls the pass's recovery
  (`Store.RecoverProvenance`, i.e. `RecoverLanded`) before planning, claiming or
  any landing-start read, then installs the activation for every later load. The
  durable start is intercepted from the ordinary emitter; other events are unchanged.
  Stage A approval queues and provenance-activated queues refuse dependency
  activation in 4a-1; the store transaction already links approval decisions.

ADR ambiguities resolved here:

- "Canonical key order" for landing-start locks is ascending stored queue ID, the
  order the existing tenancy-gated multi-queue paths already use; a key-string
  order could deadlock against them. The mutation proof shows a real deadlock.
- Legacy revision observations carry no `ChangeID`. Like audit, the store derives
  it from the stored canonical queue key and never accepts a different recorded ID.
  The shared reader binds each companion the same way: alias number equal to the
  batch member's PR, and `ChangeID` equal to the stored key's derivation (the
  store installs a lazy deriver; without one, companions fail closed).
- Composition and invalidation companions were deferred by 4a and are delivered
  in 4b-1 below. Acknowledgement now requires a fresh composition after an edit.

The 4a-2 hold and acknowledgement fixtures shipped in #197. Production acquisition (ObserveEdge,
source journal reads and ancestry) is wired in slice 6. The fence-bound
**BLOCKING** item above still applies to every high-volume activation.

Proofs: `TestAdmissionFromRecordedFactsOnly`, `TestDecisionCompanionWireAndFold`,
`TestDependencyDecisionBindsDurableStart`,
`TestDependencyDecisionBindsConsumerIdentity` (reader),
`TestDependencyWaitsGatePlannerAtDepthsOneAndFour`,
`TestDependencyWaitsNeverBisectedRetriedOrDequeued`,
`TestDependencyWaitSuspendsBatchMaxWait`,
`TestDependencyWaitsWithheldFoldAgreesWithPlanner`,
`TestDependencyWaitsWithheldFoldIsExact`, store
`TestDependencyWaitsWithheldStartFoldsThroughStore`,
`TestDependencyEligibilityStableAcrossUnchangedRefresh`,
`TestDependencyLockRefusesSelfSource`,
`TestDependencyPlannerGateFromRecordedFacts`,
`TestDependencyStartCommitsCompanionAtomically`,
`TestDependencyStartRefusesEachAdmissionGateAtomically` (12 gates),
`TestDependencyStartLockOrderAndConcurrentCycle`,
`TestDependencyStartWithApprovalDecisionsAndPendingRecovery`, and serve
`TestDependencyWorkerGateStartAndRecoveryBeforeAdmission`.


## ADR 0011 slice 4b-1 — composition invalidation (Hold)

Build admission atomically appends `dependency_composition_recorded` beside the
unchanged `BatchStarted`/`ChainedBatchStarted`. It pins the exact admission row,
base, ordered members, declaration and policy digests, acknowledgement and original
observation IDs, plus a versioned digest of the satisfying source revisions.
The store refuses a landing without a current companion. `AppendSide` cannot
append a composition outside its build-start transaction. The maximum 128-member,
32-edge framing fits the existing event cap.

The composed reader latches the first invalidating side fact and marks affected
unstarted batches and their existing ADR 0009 suffix. Later success cannot revive
that composition. An activated discard emits the NEW queue kind
`dependency_compositions_invalidated`, pinning exact batch IDs, composition IDs
and first-cause event IDs. Both raw queue folding and composed store replay accept
the emitted discard. Entries keep their admission order. A durable landing always
settles through its existing fence. Existing depth/capability cancellation rules
are reused; their full dependency-specific matrix is reserved for 4b-2.

Equivalent policy/observation refreshes and source-base advances that retain the
same satisfying landing do not rebuild. A source rewind removing that landing,
lost consent, or a declaration/policy change retires old work. Acknowledgement
clears a declaration hold but cannot reauthorize pre-edit CI: the 4a hold fixtures
now explicitly discard and rebuild before a successful post-edit start.

Derived composition state uses dependency base v10 inside the existing v9 cursor.
Readers retain v8 and legacy support; queues without composition history keep
those exact encodings. Retirement removes active projections, never journal
history, and cannot downgrade the format. Snapshot/tail tests cover every cut,
first-cause latching and forged wrappers. Both new kinds are inventoried in the
rollback gate and tested with real stored rows. Public dependency configuration
remains rejected, and the private seam is still the only activation path.

`RebuildReverseScope`/`Cascade` and `Store.DependencyCascade` rebuild bounded reverse
wakes from an explicit enrolled queue set at a consistent read boundary. Wakes
schedule direct-edge reconciliation; they do not grant satisfaction or invalidate
through a still-confirmed landing. Only command-admitted consumers are returned.
Polling repairs missed wakes. Forge acquisition and runtime wake dispatch remain
slice 6 wiring, as in slices 3 and 4a.

**Scope split:** 4b-2 still owns the complete suffix-order/restart/late-result and
both-capability acceptance matrix, plus unlanded-push explicit-pin/revision-less
semantics. The basic suffix and snapshot tests here protect the 4b-1 implementation;
they do not claim the complete 4b exit matrix. High-volume activation remains
blocked on the existing lifetime/fence policy and a scalable clock-history strategy;
this slice does not silently remove the new clock acquisition bounds.

See [4b-1 evidence](../../docs/research/adr-0011-4b1-evidence.md) for the item/test
map, mutation results, timing samples and ADR interpretations.
