# ADR 0010 declarations and local inventory (Inert)

This package consumes supplied strings and observations. It has no acquisition,
network, clock, journal or queue calls. The unwired local gitops reader imports
it; live queue execution does not.
`LoadCandidate` projects `provenance` out and validates the remaining document
with `config.Load`, following ADR 0008's candidate-only pattern. The public
loader, public schema, and frozen condition-evidence vocabulary reject these
additions. Candidate condition parsing retains types on the compiled expression
without registering new public attributes. Resolver-dependent attributes can be
compiled here but remain unavailable to public configuration.

The owner's 2026-10-27 acceptance is the authority for this implementation.

## Fixed parser choices

The deployed Git binary is the reference trailer-block parser. Slice 1 exposes
`ParseGitTrailers`, which takes `GitTrailerOutput`: the colon-separated, already
unfolded output of `git interpret-trailers --parse --no-divider`, not a commit
message. `IdentityInput.ParsedTrailers` uses that same named type for coauthors.
The isolated Git adapter is the only intended producer of `GitTrailerOutput`;
raw message strings require an explicit conversion and must never be supplied.
No block locator, heuristic threshold, comment skipping or continuation folding
is implemented here. The unwired slice-2 local reader calls this API; live
queues do not.

The production `gitops.TrailerParser` adapter in
`internal/gitops/provenance_parser.go` reuses
`session.runTo` and `gitEnv`: inherited `GIT_*` variables (including config
injection) are removed, `GIT_CONFIG_NOSYSTEM=1` disables system config, and
`GIT_CONFIG_GLOBAL` points to the empty `os.DevNull`. It parses in a fresh bare
repository initialized with `--template=`, excluding source-repository local
config, includes and named `trailer.*` aliases. It pins
`-c trailer.separators=:` and both `-c core.commentChar=#` and
`-c core.commentString=#` (the latter for newer Git). `--parse` supplies
`--only-trailers --only-input --unfold`; no trailers are added or commands run.
`--no-divider` uses commit-message semantics, matching Git's commit reader
`git log --format=%(trailers:only,unfold)` and the
[forge-driver contract](../../docs/forge-driver-contract.md#building-test-refs).
This combination leaves no ambient trailer settings to alter the result.
Tests log the deployed version. PR #171's CI used **git version 2.55.0**
(`/usr/bin/git`, Ubuntu 24.04 runner image `20261012.334.1`); the local binary
is **git version 2.56.0**.
See [Git's parser documentation](https://git-scm.com/docs/git-interpret-trailers).

Provenance parsing passes the original message bytes to Git. It deliberately
does **not** apply gitops metadata's `stripMetadataLines` or
`rewritePatchDividers`. Those functions prepare attribution for newly generated
merge messages; they change block eligibility, continuation ownership and patch
boundaries. Removing a reserved line can also remove its continuation bytes.
Leaving both untouched and using `--parse --no-divider` preserves Git's
commit-message trailer set. Markdown rules and patch dividers must not hide an
appended attribution block, as required by the forge-driver contract. A divider
and following prose can still make an earlier declaration ineligible for the
final trailer block. Slice 2 must read original commits, rather than feed
generated/reformatted messages to this extractor.

Named regressions cover cherry-pick markers, patch dividers with text or
trailing whitespace, and URLs in prose. For every named case, the adapter is
checked against `git log %(trailers:only,unfold)` from the same Git binary;
tests do not assume version-dependent output is identical. Git versions older
than 2.56 parse the URL line in the URL-in-prose case as a `https: //…` trailer,
so the commit yields agent evidence on those versions. Git 2.56 no longer parses
it as a trailer. This 2.55-to-2.56 classification difference only tightens
policy on older Git. The differential corpus compares
1,152 LF/CRLF messages, including continuations, comments, mixed-case keys,
duplicates, coauthors and Signed-off-by casing. Each original message is written
byte-for-byte to a real commit object; `--parse --no-divider` output must equal
`git log --format=%(trailers:only,unfold)` output before declarations and coauthors
are checked against the commit reader's set. Named regressions also compare
both readers, including an appended checkpoint after a markdown rule.
A hostile system/global/environment config test pins both readers' isolation.

Keys normalize to lowercase; values preserve Git's emitted bytes after the
single formatting space following the colon. In particular, CR bytes retained
by Git while unfolding CRLF continuations remain invalid evidence, not stripped
into valid values. Identical unfolded values deduplicate; distinct values remain
separate. Entire IDs are 12 lowercase hex
or 26 canonical uppercase Crockford digits with first digit 0–7. Neutral
sessions follow the ADR's exact ASCII grammar. Recognized invalid trailers
still contribute agent evidence. Values over 161 bytes (the maximum valid
neutral declaration) retain a SHA-256 and `value_too_long`; invalid UTF-8 retains
a SHA-256 and `invalid_encoding`. Neither becomes a truncated valid value.
The slice-2 inventory reader enforces acquisition and declaration-count
budgets; this string parser makes no completeness claim about truncated input.

### Parser safety requirements

- Produce `GitTrailerOutput` only after a successful isolated Git invocation
  with `--parse --no-divider` and the pinned separator/comment settings above.
- On **any** Git failure (including execution, cancellation or nonzero exit),
  mark the commit inventory incomplete/pending. Empty or partial failure output
  must never be parsed as an empty trailer set and classified as Human.
- Reject an embedded NUL in the original message **before** invoking Git and
  mark the inventory incomplete/pending. Git truncates at NUL: for example,
  `subject\n\nbody\0\n\nEntire-Checkpoint: abcdef012345\n` can hide the checkpoint.
  This is an acquisition failure, never evidence that the commit is Human.
- Preserve the environment/config isolation, including no source-local config
  or named trailer aliases, and pass original message bytes without rewriting.
- Bind the actual Git version, pinned invocation/settings and parser/classifier
  versions into the effective parser policy identity and evidence. Provenance
  results depend on Git version, so an upgrade that changes classification is
  a policy-identity change. The concrete example is Git 2.55 parsing the URL
  line in the URL-in-prose regression as a `https: //…` trailer and counting
  agent evidence, while Git 2.56 does not.

## Identity choices

`mailbox-classes-v1` trims only outer ASCII space/tab and lowercases ASCII local
and domain parts. Bare mailbox input must contain exactly one `@`, nonempty
parts, printable ASCII, no whitespace, display-name delimiters, comments,
quotes, comma, semicolon, colon or backslash. Domains contain only ASCII letters,
digits, dots and hyphens. GitHub `[bot]` noreply local parts are supported.
No dot/plus-tag removal, substring matching or account resolution occurs.
Configured bot accounts therefore need an explicit mailbox rule.

Author and original committer rules can contribute human or agent evidence.
Coauthors contribute only configured agent matches, as the ADR's explicit
coauthor exception specifies. Conflicting class rules retain both rule IDs and
yield mixed; they never resolve by order. The trusted caller excludes generated
OpenMerge committers using its build mapping. Contributor replay trailers
cannot set that flag. These matches never yield a forge identity or permission.

## Candidate and evaluation contracts

`testdata/provenance.candidate.schema.json` describes the additions-only
projection; schema tests combine it with full public-projection validation.
Approval opt-in and group references remain semantic parser checks. Raw candidate
input rejects aliases, YAML merge keys and explicit tags, following the earlier
policy candidate parser. It bounds the document to 10,000 nodes and depth 64.
Both `when` and `require.conditions` accept an implicit AND list, atom, or
logical group. Nested operators use the existing grammar; NOT takes one atom
or group. `provenance` uses exact equality/inequality against the four classes.

`DeclarationAttributes` produces only the two declaration-derived attributes.
Incomplete inventories/extractors yield missing typed values, never known zero.
Resolver attributes remain unknown in declaration-only evaluation. The generic
condition evaluator accepts separately supplied resolution observations when
needed for pure tests; there is no resolver in this slice.

`EvaluateRequirements` takes a baseline snapshot and independently evaluated
ADR 0007 approval results, indexed by rule. It preserves baseline approval
eligibility for every trigger. Only a rule's own `require.conditions` can see
its local pool unioned with baseline eligibility; unknown baseline membership
stays unknown. All other inputs retain their baseline values. False triggers
add nothing; pending triggers pass only with a true obligation. Both ADR
reviewer-eligibility cases, their pending-membership variants, rule reordering,
repeated evaluation, and cross-rule pool isolation are pinned in pure tests.
Slice 4a must construct these inputs from one observation generation and wire
policy/review exclusions, default preservation, vetoes and gates into runtime.

## Declaration digest v1

`CanonicalDeclarations` emits compact Go JSON, with fixed struct-field order,
HTML escaping, valid UTF-8, and no trailing newline. The top-level fields are
`Version`, `Commits`, `Rules`; version is `openmerge-declarations-v1`.

Commits sort by object ID and encode `ObjectID`, `Class`, `Declarations`.
Incomplete inventories, missing IDs and duplicate originals are rejected.
Declarations encode the exported `Declaration` fields in declaration order:
`Source`, `Version`, `Kind`, `Value`, `ValueDigest`, `Outcome`, `Class`, `RuleID`.
They sort lexically by Go's `%q` encoding of that field tuple, then deduplicate.
Empty sets encode as `[]`.

Rules whose trigger is not definitely false with declaration-derived inputs
are encoded as compact JSON strings with `When`, `Conditions`, `Approvals`.
Expressions use the shared evaluator's stable display spelling. Absent
conditions/approvals encode as null. Approval requirements use ADR 0007 field
order and canonicalize `human` to `any_writer` plus the human filter. Rule
strings sort lexically and deduplicate. Logical child order is preserved.

`DeclarationDigest` is lowercase SHA-256 of those bytes. Independent Python
JSON/hashlib vectors pin empty, undeclared and agent inventories, including a
mutable-review trigger which must remain bound. Resolution results, ref tips,
metadata IDs and current approvals are not accepted by the digest API. Parser
and classifier policy versions must additionally enter the future effective
policy digest; existing config/policy digest code and vectors stay unchanged.

## Slice 2 local inventory

`gitops.ReadProvenanceInventory` takes an absolute path to an already-acquired
bare repository, private scratch parent, pinned `InventoryRequest`, classifier,
and `InventoryLimits`. It never opens the transport-enabled `Repo` lifecycle.
`NewTrailerParser` is reusable separately and removes its template-free private
repository on `Close`. The caller must close it. Inventory creates and closes
its own parser, including on failed reads. Mapping uses a hardened local read session directly and needs no parser repository.

Each request names a repository, queue, change, revision and observation
generation. `Stable` is `PinnedBase..Head`. `Additional` contains originals in
`BuildBase..Head` that were absent from `Stable`; `Commits` is their sorted,
deduplicated union. Both sets of all best merge bases are retained, including
criss-cross histories. Advancing the build base cannot erase stable originals.
A rewritten base can add originals without changing the pinned head. Re-pushes
supply a new revision; a base refresh supplies a new generation. Callers supply
trusted exact `Excluded` IDs for synthetic markers or other members' originals.
Contributor trailer spelling never determines exclusions.

Git derives ancestry with `rev-list --parents --topo-order`, bounded by
`--max-count=<frontier limit + 1>`, and best merge bases with `merge-base --all`.
Two frontiers (`Head ^PinnedBase` and `Head ^BuildBase`) preserve the stable union
when either base advances or rewrites. Replacements, grafts, commit-graph
acceleration, shallow boundaries and lazy/network fetching are disabled in the
same hardened diff-read session. Missing required ancestors fail the observation.
Graph edges come only from Git's parser: a late `parent` header after author or
committer cannot alter ancestry. Raw original identity extraction requires the
strict tree/parents/author/committer prefix; unexpected reserved headers return
`pending:commit_shape`. The source repository's trailer config and mailmaps
cannot affect raw identities or trailer parsing. Only normalized declarations
are retained. Invalid overlong values remain agent evidence with digests, never
copied raw values or shortened declarations.

`Policy` records `git version`, parser/classifier versions, the pinned invocation
and configuration isolation. `PolicyID` is SHA-256 of the compact Go JSON of
that struct, in field order. Future effective policy identity must include it.
A different Git version changes the identity even if a particular commit's
classification happens to stay the same. Identity-rule and requirement policy
bindings remain the responsibility of the later integration slice.

`DefaultInventoryLimits` fixes ceilings of 1,024 source commits, 16 declarations
per original, a 128 MiB `GraphBytes` budget, 1 MiB per raw commit, 16 MiB
cumulative message bytes, and 30 seconds per local read.
`GraphCommits` now bounds each PR frontier, including trusted exclusions, rather
than whole repository ancestry. Its default remains 16,384; the actual query
limit is the smaller of that and the source budget plus exclusion count. Old
base history does not consume a commit-count budget. The topo-order walk can
still traverse old ancestry: its cost scales with base drift and is bounded
by the deadline and process cap. The sorted union after exclusions must still
fit 1,024 source commits. Git graph output has an explicit `GraphEdgeBytes`
ceiling (4 MiB); stderr is bounded to 8 KiB. Callers supply
positive limits and can only lower them.

**The enforced expansion and memory guarantee is the per-process address-space
cap.** This inert adapter requires `prlimit` and runs every Git process that
reads source objects or traverses ancestry under
`--as=min(2 GiB, GraphBytes + 16 MiB + indexBytes)`. The 16 MiB base overhead
covers Git, shared libraries and allocator overhead; the default cap starts at
144 MiB before indexes. `indexBytes` sums the stat sizes of `objects/pack/*.idx`,
`*.rev` and `multi-pack-index`. For each `.idx` without a matching `.rev`, it
also reserves `12 * ceil(idxSize / 24)` bytes for a heap reverse index: about
12 bytes/object, conservatively deriving the count from the minimum 24 bytes
per object in a SHA-1 v1 index (v2 and SHA-256 indexes use more). Headers and
large-offset tables make this estimate conservative. All arithmetic saturates
at the hard **2 GiB per-process ceiling**; huge indexes may still fail closed.
Below that ceiling, lowering `GraphBytes` lowers the cap by the same amount.

Sizing uses Go's root-constrained filesystem API, so symlinks cannot escape the
supplied Git directory; nonregular indexes and sizing failures stay pending.
Repositories must remain stable during the read. Alternate object directories
(`objects/info/alternates`, even an empty file) and linked-worktree `commondir`
files are unsupported and fail closed before any source Git reader starts.
Inherited alternate-object environment variables are scrubbed as well.
The extra index allowance accounts for read-only file mappings and potential
heap reverse indexes; it does not weaken the expansion guarantee: every source
read remains supervised under the computed cap, with no unbounded fallback.
It does not turn `GraphBytes` into an exact reconstruction-only memory limit.

Every capped invocation (including `rev-list`, `merge-base`,
and the persistent `cat-file --batch-command` metadata/body reader) pins
`core.packedGitLimit=min(8 MiB, cap/8)` and
`core.packedGitWindowSize=min(1 MiB, packedGitLimit/4)`, rounded down to twice
the OS page size to match Git's alignment. `core.deltaBaseCacheLimit=0` minimizes
the read-side delta cache; Git can still retain the latest base. At a 144 MiB
cap (before adding indexes) this allows 8 MiB of pack
windows, each at most 1 MiB; at a 24 MiB cap (`GraphBytes=8 MiB`, before
indexes) it allows
3 MiB, each at most 768 KiB. Even at the smallest accepted budget the targets
are 2 MiB and 512 KiB. At least seven eighths of the cap remains for Git,
libraries, pack indexes, allocator overhead and object reconstruction. A pack
can exceed the cap because Git unmaps unused windows instead of mapping it all.
These are mapping/cache targets: in-use windows and pack indexes can exceed
them, so the process cap remains authoritative. `pack.windowMemory` and
`pack.deltaCacheSize` configure `pack-objects` writes and do not apply to these
reads. Build mapping uses this same reader and the same settings.

The settings use command-line `-c` overrides through the hardened Git command
path: inherited `GIT_*` injection is removed, system/global config is disabled,
and repo-local values lose to `-c`. Real-Git tests query effective settings
against hostile 1 GiB local values, including with lowered caps.
This bounds each process's address space;
it does not promise exact cumulative expansion accounting across processes or
that a reconstruction uses at most `GraphBytes` bytes without the overhead.
Missing or unusable supervision returns pending without an unbounded fallback.
Allocator failures (including OOM and calloc failures) and SIGKILL while capped
produce terminal `limit_exceeded:expansion`. Allocation failures remain terminal
even if Git recovers through a duplicate object and exits successfully.
Context cancellation stays pending; the local deadline produces terminal
`limit_exceeded:time`. Other process failures stay pending. The production transport lifecycle is unchanged.

One long-lived `cat-file --batch-command` process serves metadata-only `info`
queries and original `contents` reads. Before a body read or endpoint traversal,
the reader follows `%(deltabase)` metadata, checks `%(objectsize)`, and charges
the visible chain against `GraphBytes`. It does not request `%(objectsize:disk)`,
which can cause Git to build a reverse index for the entire pack.
Shared bases are charged again for each independently requested object.
**This delta walk is advisory early rejection only.** Each base ID lookup can
select a standalone duplicate in a different pack, including through a
multi-pack index, while Git reconstructs the target along the original pack's
deeper chain. The walk can therefore under-count both expansion bytes and depth.
`DeltaDepth` (default 128 objects) bounds only this visible metadata chain;
passing either advisory check never establishes safety. The mandatory process
cap supplies the guarantee. A visible depth or delta byte overflow returns
`limit_exceeded:expansion`; raw object and visible non-delta cumulative overflows
retain `commit_bytes` and `graph_bytes` diagnostics. No raw body is retained
in returned evidence.

The metadata path does not reconstruct delta bodies in Git
[2.55](https://github.com/git/git/blob/v2.55.0/packfile.c) or
[2.56](https://github.com/git/git/blob/v2.56.0/packfile.c):
`packed_object_info_with_index_pos` uses `get_size_from_delta` to inflate only a
20-byte delta header when no `contentp` is requested. Small fixtures cover
visible byte/depth boundaries. The multi-pack regression covers both REF_DELTA
and OFS_DELTA: a preferred standalone 512-byte duplicate hides a 32 MiB base
from a 20 KiB advisory budget and depth limit of two. Batch body observations,
inventory traversal and build mapping still fail with `limit_exceeded:expansion`
under the cap; the same inventory and mapping complete with a 64 MiB budget.
The base padding is streamed into a compressed pack of tens of KiB, keeping the
Go fixture's memory small.
The expanded base must exceed the fixed overhead to exercise the process cap.
`TestProvenanceInventoryIndexLargerThanFixedOverhead` imports 610,000 tiny blobs
into one pack whose `.idx` alone exceeds 16 MiB. A small PR completes with an
8 MiB `GraphBytes` budget, including a MIDX, both with and without `.rev`.
Sizing regressions check the exact allowance, sparse-file saturation at 2 GiB,
and fail-closed external symlinks and alternates.
PATH-shim regressions also cover missing/broken `prlimit`, allocator failures,
SIGKILL, ordinary Git errors and external cancellation.
`TestProvenanceInventoryPackLargerThanProcessCap` streams 32 MiB of
incompressible blob data into one pack larger than its approximately 24 MiB
process cap (including its small index allowance).
The unpinned control walk fails to mmap it; inventory and fast-forward mapping
complete through the hardened path even with hostile local memory settings.
A 32 MiB delta base in that same repository still returns
`limit_exceeded:expansion`; the hidden-base multi-pack tests above additionally
prove the mandatory cap catches reconstruction that passes advisory metadata.

Topo-order traversal also allocates for base drift, even when the PR frontier
is small. Measurements with commit-graph disabled found about 240–425 bytes per
visited drift commit. The default 128 MiB work budget gives an arithmetic upper
envelope of roughly 315,000–559,000 such commits before pack windows, object
reconstruction and other working allocations; practical capacity is lower and
depends on topology and Git version. This is a sizing estimate, not an ancestry
completion guarantee. Large base drift can still fail closed under the process
cap or 30-second deadline; `GraphCommits` only bounds the retained PR frontier.

The inventory also retains the effective `Limits` for evidence.
`Diagnostic` retains only an outcome, resource and optional original object ID.
It never exposes stderr or message contents. Ordinary Git failures, missing
ancestors, external cancellation, malformed commit headers and embedded message
NULs yield
`pending`. Internal work deadlines and exhausted deterministic budgets yield
terminal `limit_exceeded`. Invalid requests yield `invalid_input`. Failed
commits retain no partially extracted human declarations. Always pass both
`Commits` and `Complete` to declaration evaluation. Partial counts are unknown,
including zero counts and negations, and cannot authorize progress.

## Slice 2 composition evidence and attribution

`gitops.MapProvenanceBuild` takes a complete inventory and a trusted
`BuildObservation` for one member's local composition. The later composer must
capture `Replays` at creation time; the current live builder still returns its
legacy SHA and is unchanged. A replay trailer is never a mapping authority.
The reader checks actual local graph reachability and composition shape:

- Merge and fast-forward map preserved originals to themselves. A fast-forward
  test marker is recorded separately and cannot become a contributor.
- Squash maps newly included originals to that member's generated commit.
- Rebase requires one explicit original-to-generated pair for every newly
  replayed original, with no duplicate targets or unrecorded generated commits.
- Stable originals already reachable from the build base map to themselves.
  Their declaration obligations remain in the stable inventory.

Squash and rebase mapping verify graph shape and trusted replay records only.
They trust the composer's resulting tree; this is not tree-derivation verification
and does not prove patch equivalence with the originals.

A mapping retains the request, generation, parser policy identity and composition
ID. Each rebuild supplies a new composition observation. `tested` and `landed`
are separate stages, so changed landed compositions never reuse tested identity.
Batches supply an inventory and observation per member, with that member's
actual build base; they never pool classifications or counts.

`provenance.RetainAttribution` takes the existing generated trailer block and
complete original inventory. It adds distinct normalized Entire and neutral
trailers before the existing authoritative `OpenMerge-*` suffix. It never copies
reserved contributor trailers. Existing coauthors, sign-offs, suffixes, and every
line's terminating newline count toward the 64 KiB UTF-8 bound. The one-byte
lookahead boundary is terminal `limit_exceeded`, returning no partial block.
Overlong digest-only declarations also fail retention explicitly; invalid UTF-8
cannot be regenerated as valid text. The inventory classification remains
available independently of whether a generated attribution block can fit.
No live commit message changes in this slice.

Real-Git proof lives in `provenance_inventory_test.go`,
`provenance_mapping_test.go`, `provenance_review_test.go`, and
`provenance_attribution_test.go` in gitops.
The promoted parser retains the full commit-reader differential corpus and
hostile configuration tests in `provenance_trailers_test.go`. Tests intentionally
compare the deployed Git binary's own commit reader where 2.55 and 2.56 differ.

## Slice 3a event contracts and snapshot decoder (Inert)

`internal/provenance/journal` is a separate, unwired package. Its import guard
rejects every production importer, including gitops; slices 1–2 retain their
existing local reader. There are no append calls, SQL changes, production
snapshot writes, or live replay changes. New events use an independent wire-v1
envelope, not additional fields in any existing queue event:

- `change_provenance_observed` retains a bounded source-inventory chunk. Its
  request binds repository, queue, change ID, revision, pinned head/base, actual
  build base, excluded synthetic/other-member originals, and generation. It
  retains stable and additional originals, merge bases for both traversals,
  normalized per-commit declarations and outcomes, effective limits, completeness,
  and a typed diagnostic. The parser evidence includes the actual Git version,
  fixed invocation/isolation and parser/classifier versions, plus their verified
  `ParserPolicy.Identity()`. Governing policy evidence records the default-branch
  ref/commit and effective policy digest; the later evaluator must compute that
  digest including parser identity, identity rules, sources and requirements.
- `landing_provenance_recorded` is companion evidence for an attempt, batch,
  stage and existing decision position. Every member references *all* of its
  exact observation positions, its generation and inventory/declaration digests,
  its trusted original-to-build mapping, and its independent baseline, trigger
  and rule-local gate inputs, eligibility pools, evidence references and truth
  values. Only `true` passes; pending triggers cannot skip an unsatisfied
  obligation. Tested T and landed L retain distinct composition IDs/stages.
  Replay validates references and result combination; it does not re-evaluate
  expressions, acquire Git objects, authenticate supplied eligibility, or perform
  graph verification. Those are the trusted slice-2/4a producers' responsibilities.

All fields, including false/zero/empty collections, are required. Collections
encode as `[]`, not null. Unknown/missing fields, case aliases, duplicate keys,
invalid UTF-8, trailing JSON, unsupported versions and oversized payloads fail
closed. Existing slice-1/2 structs embedded in these *new* payloads retain their
current field names; goldens freeze those shapes too. Future schema changes
require another new kind/version, not fields in an existing event.

### Chunking, bindings and digests

An inventory has 1–1024 chunks, each with at most 128 commits and a 1 MiB encoded
event bound. The effective source-commit/declaration limits may lower ceilings.
Commit IDs, declaration sets, partitions, exclusions, mappings, typed input
names/list values and eligibility IDs use sorted unique order. All header fields
other than index and commit contents must match across chunks. Indices start at
zero, arrive in order per generation, and cannot repeat. Partial generations may
survive snapshots but never supply complete evidence. The last chunk verifies
the full digest, disjoint stable/additional partitions and exclusion of synthetic
markers/other members. Complete empty inventories are explicitly representable.

`PreviousObservationID` compares against the previous generation's first chunk
position for that repository/queue/change, including a prior incomplete read.
A new revision or base rewrite needs a new generation. New generations immediately
supersede the old one for landing; no stale success is reused while a refresh is
pending. Historical records remain addressable for audit. A generation's full
request must match each landing member; batch members cannot pool references.

Evidence-record v1 hashes use SHA-256 of the UTF-8 domain prefix
`openmerge-provenance-record-v1` followed by NUL and compact Go JSON, with fixed
struct field order, HTML escaping and no trailing newline. `InventoryDigest`
hashes `{header,commits}` after clearing the header's digest, index, chunk count
and commit array. Thus physical chunking does not change record identity.
`MemberDigest` hashes the member with its own record digest cleared; mutable
condition/approval evidence and build mappings belong here. The separate
`DeclarationDigest` is supplied by slice 1's complete inventory plus potentially
applicable rule definitions, not recomputed from mutable gate evidence. Failed
inventories carry no declaration digest. This preserves the distinction between
receipt identity and refreshed evidence. MVP extraction outcomes retain
`not_requested` checkpoint resolution; resolver observations require a later
new event kind and are not fabricated by this decoder.

The pure fold retains at most 4096 provenance records / 32 MiB of canonical
event payloads, within a 64 MiB wrapper bound. It returns detached immutable
values and leaves prior state unchanged on error. This is a bounded retained
checkpoint contract, not an activated compaction strategy; future snapshot
writers must design retention/export closure before deployment. The eventual
writer must commit all chunks and member/decision/landing-start bindings
atomically and reject stale generations under the transaction fence. There is
no writer or transaction code in 3a.

### Snapshot coordination: v9 wraps unchanged v8 or legacy bytes

Dependency's ADR 0011 slice 2 already reserves **v8** as an explicit, unwired
wrapper. Provenance reserves **v9**, with `Version`, `Base`, `LastID`, and
`Records`. `Base` is exactly one unchanged queue snapshot (currently v6/v7), or
the complete existing dependency-v8 wrapper. Sharing v8 would change an already
introduced strict schema and require editing dependency. Distinct outer v9
preserves that boundary and needs no changes to `internal/dependency`.

`journal.DecodeSnapshot` is an **explicit, unwired decoder only**. It delegates
base validation through a required pure `BaseDecoder`, returning the exact base
bytes and the folded provenance state. Slice 3c can wire **one composed reader**:
call this decoder with a closure over `dependency.DecodeSnapshot` and its
independently verified journal identity. That existing decoder handles v8 and
legacy queue bytes. Legacy/v8 input without provenance returns unchanged base
bytes and empty provenance state. V9 accepts neither nested v9 nor unknown base
versions. Dependency identity errors propagate; the wrapper grants no bypass.
There is no `EncodeSnapshot` here; only test fixtures construct v9 bytes.

Keeping the inner bytes unchanged preserves #163's `HeldRequests` and cached
`PRObserved.PendingConditions`, as well as dependency state and existing legacy
cursor encoding. Old queue and dependency decoders strictly refuse v9 and new
provenance events. This does **not** make post-emission downgrade safe. Slice 3b
must add audit-v2 closure first, then 3c installs the composed reader everywhere,
drains old workers and takes a verified pre-emission backup before 4b writers.
No reader/writer activation or v1 audit-format change occurs in 3a.

### Slice 3a proof

- `TestGoldenPayloadsAndStrictOldDecoderRefusal`: committed complete/chunked,
  pending/terminal, tested/landed event goldens; strict old decoder refusal.
- `TestInventoryAtSourceCeilingAndBoundedChunks`: a complete 1024-commit
  inventory across bounded chunks and refusal above the per-chunk ceiling.
- `TestCompleteChunkedInventoryAndGenerationBindings`,
  `TestInventoryChunkFailuresAndOutcomes`,
  `TestPendingCommitEvidenceAndRebuildFences`: assembly, completeness, empty inventory,
  original partitions, exclusions, Git-policy identity and stale/reference fences.
- `TestLandingLayersMembersAndTestedLandedBindings`: per-member isolation,
  three-valued gate combinations and separate T/L composition identity.
  `TestTypedGateInputsAndDeclarationDigestSeparation` pins string/bool/list
  inputs, unknown values and mutable record versus declaration identity.
- `TestStrictEventShape`, `TestStrictSnapshotShapeAndBindings`: every nested
  field's required/unknown/null/case rules and corrupt version/reference refusal.
- `TestSnapshotPlusTailEveryCutWithDependencyV8AndHeldRequests`: every cut,
  including partial chunks, with dependency v8 and #163 held state retained.
  `TestSnapshotPlusMixedQueueTail` also interleaves queue events and compares
  planner output as held requests enter and leave the queue.
- `TestLegacySnapshotBytesRemainUnchanged` and store's
  `TestProvenanceLegacyHeldSnapshotAndCursorGolden`: unchanged deployed snapshot
  and actual cursor bytes against the pre-existing dependency goldens. Store's
  existing `TestDependencyLegacySnapshotAndCursorGolden` also pins every deployed
  queue event/action wire fixture.
- `TestNoProductionProvenanceJournalImports` and
  `TestStateDoesNotAliasCallerEvidence`: inertness and immutable evidence.
