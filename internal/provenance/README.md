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
per original, 128 MiB cumulative reconstructed object bytes, 1 MiB per raw
commit, 16 MiB cumulative message bytes, and 30 seconds per local read.
`GraphCommits` now bounds each PR frontier, including trusted exclusions, rather
than whole repository ancestry. Its default remains 16,384; the actual query
limit is the smaller of that and the source budget plus exclusion count. Old
base history does not consume a commit-count budget. The sorted union after
exclusions must still fit 1,024 source commits. Git graph output has an explicit
`GraphEdgeBytes` ceiling (4 MiB); stderr is bounded to 8 KiB. Callers supply
positive limits and can only lower them.

One long-lived `cat-file --batch-command` process serves metadata-only `info`
queries and original `contents` reads. Before a body read, and before traversing
an endpoint, the reader walks `%(deltabase)` metadata, checks
`%(objectsize)` and `%(objectsize:disk)`, and charges every reconstructed object
in the chain against `GraphBytes`. Shared bases are charged again for each
independently requested object. A chain may contain at most `DeltaDepth` objects
(default 128); depth or delta expansion overflow is terminal
`limit_exceeded:expansion`. Raw non-delta size and cumulative byte overflows
retain `commit_bytes` and `graph_bytes` diagnostics. No raw body is retained
in returned evidence.

This metadata path does not reconstruct delta bodies in Git
[2.55](https://github.com/git/git/blob/v2.55.0/packfile.c) or
[2.56](https://github.com/git/git/blob/v2.56.0/packfile.c):
`packed_object_info_with_index_pos` uses `get_size_from_delta` to inflate only a
20-byte delta header when no `contentp` is requested. Verification also ran
metadata queries for the review's 212-byte target backed by a 192 MiB base under
a 64 MiB address-space limit on both versions. The CI regression uses a 32 KiB + 1 byte
base and a lowered 32 KiB budget, plus cumulative-chain and depth boundaries.

Metadata preflight alone cannot bound Git's internal traversal of old ancestry
or its delta-instruction buffers. Consequently **this inert adapter requires
`prlimit`**, and runs all source-object Git commands under
`--as=GraphBytes+256MiB` (384 MiB by default). The extra address space covers Git,
pack mappings and allocator overhead; pack windows are 1 MiB, mapped-pack budget
16 MiB, and the delta-base cache is disabled. This is a separate per-process
memory ceiling, not a whole-history expansion accounting claim. Missing or
unusable supervision returns pending without an unbounded fallback. Git
allocation-limit diagnostics produce `limit_exceeded:expansion`; other process
failures stay pending. The context deadline also bounds Git's internal walks.
The production transport lifecycle is unchanged.

The inventory also retains the effective `Limits` for evidence.
`Diagnostic` retains only an outcome, resource and optional original object ID.
It never exposes stderr or message contents. Git failures, missing ancestors,
external cancellation, malformed commit headers and embedded message NULs yield
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
