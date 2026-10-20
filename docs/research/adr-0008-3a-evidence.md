# ADR 0008 PR 3a evidence

Trial classification: **Hold**. This work must not deploy until period B drains.
Public activation belongs to PR 9; no schema keys, events, or migrations are added.

The implementation is split into 3a-i (pure compiler, validator, and bounded
prepared-object contracts) and 3a-ii (transient options and pre-push integration).
Both PRs inherit Hold. The baseline is `ec09672` (PR 2); PR 1 is `addec7d`.

## 3a-i contract

The compiler includes the resolved default branch and every configured selector,
without enumerating today's branches. `release/*` becomes
`refs/heads/release/*`; `release/**` becomes `refs/heads/release/**/*`.
Whole-component `**/` keeps its zero-or-more-directory semantics. The fixed
exclusion `refs/heads/openmerge/**/*` removes all internal namespaces.
Embedded globstars (`qa**`, `a/**b`), three adjacent stars, bracket expressions,
escapes, braces, empty path components, and fully qualified selectors are
unsupported and rejected, never approximated. Candidate authority parsing uses
this same compiler; the public loader remains unchanged.

Containment for setup transitions is a constructive proof: retain each exact
current compiled include, preserve the exclusions, and allow only exact proposed
includes as additions. Report those additions. This deliberately requires the
old/proposed union during a selector change rather than guessing equivalence
between different glob spellings. It does not authorize actor or App-only
namespace changes, which remain setup's separate obligations.

A prepared push lists exact affected refs, old/new SHAs (empty means absent),
every introduced reachable commit, raw messages/signature data, parents, complete
parent comparisons with changed paths (including deletions and rename sources),
introduced post-image blobs with OIDs/sizes, and a proven reachable frontier. Producers must inspect the actual local objects before publication while
holding the existing session/cache lock. Limits are two refs, 10,000 introduced
commit records, 100,000 path records, 100,000 parent edges, 100,000 frontier entries and 16 MiB
of retained metadata including every OID.
Missing graphs, parent comparisons, parents, duplicate or unreachable records, and over-budget
evidence fail closed. Signature presence does not assert cryptographic validity.

The evaluator consumes PR 2's complete visibility-preserving observations. It
checks all applicable active policies, including inherited/push/classic sources.
Content bypass never waives local validation. PR/review/check rules warn only
when that same policy has this App's always bypass; otherwise they block direct
pushes. Linear history, verified signatures, literal message restrictions,
file paths and sizes are checked over every introduced object. Unknown rules,
parameters and unsupported pattern semantics block. Regex message restrictions,
path/size allowed exceptions and other unsupported rule families remain
explicitly unaccepted. No new CI counting or merge semantics are implemented.
`Driver.DeleteRef` test-ref cleanup is outside this contract.

GitHub semantics reference: [creating rulesets](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/creating-rulesets-for-a-repository).
The local matcher supports only the documented pathname-aware subset it can
prove; it does not substitute Go regular expressions for GitHub metadata regexes.

## Acceptance record

Executed locally on 2026-10-20: focused Go tests for `internal/authority` and
`internal/forge`, with the required heavy-work lock, `GOFLAGS=-p=2`, and
`GOMEMLIMIT=4GiB`. Compiler vectors compare the actual `config.Queue.MatchesBase`
implementation over omitted/default, nested, future, and excluded branches.
Rule vectors cover intermediate commits, atomic fences, unsigned objects,
unknown rules/parameters, policy-specific bypasses and deletion restrictions.
Full checks, offline replay and integration evidence are recorded by 3a-ii.

Organization-sandbox rule enforcement is **unavailable/not executed** here.
No sandbox writes, live refs, database, status API, or service configuration were
touched. Fixture success does not claim GitHub enforcement or complete ADR
acceptance; PR 11 must supply the named sandbox results before acceptance.

## Round 2: pure layer

Both PRs remain **Hold**. Path/extension/length rules inspect changed paths;
size inspects introduced post-images, so an unchanged restricted file does not
reject unrelated work and deletion cannot evade a path restriction. A rename
is delete/add (both names retained). Roots compare against the empty tree.
Merges compare separately against **every parent**, taking the union of paths
and post-images; combined diff would omit one-sided changes. The
[GitHub available-rules documentation](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets#restrict-file-paths)
specifies changes, but does not specify merge-parent selection. All-parent union
is the explicit conservative local choice, covered by regression tests; exact
GitHub merge parity remains unverified pending sandbox acceptance.

Creation inventories may declare `CreationBaseSHA`, an existing target base or
ref tip observed under the producer's session lock. The producer must prove
`merge-base --is-ancestor boundary base` for every frontier entry, and exclude
only ancestry of that observed tip. The shared validator rejects boundaries on
creations without this source and validates graph closure and source shape.
Like old-ref reachability, ancestry and existing-ref provenance are producer
obligations, never assertions inferred from arbitrary caller-selected OIDs.

The [REST rules schema](https://docs.github.com/en/rest/repos/rules) was audited:
`max_file_size` (1–100 MiB), `workflows`, `file_path_restriction`,
`file_extension_restriction`, `max_file_path_length` (1–32767 characters),
creation/update/deletion/non-fast-forward, signatures/linear history, commit
message, PR/status/deployment/code-scanning parameters. Other families and
unrecognized parameters remain unsupported. Only workflows/status checks have
`do_not_enforce_on_create`; it is evaluated after parameter validation and before
requiring bypass. Transport tests use the real PR 2 decoder/normalizer.
Unknown actors cannot satisfy App bypass; `unexpected_bypass` remains PR 4.

PR 3b must separately assert **proposed coverage was installed**, even though
containment permits retaining current coverage during transition. Its actor and
App-only checks must remain. Exclusion comparison is order-insensitive.
## 3a-ii integration

`forge.PushOptions` is a new trailing argument on BuildTestRef, Land,
EnsureFence, AdvanceFence and DeleteBranch. The sweep updates 35 existing
method implementations/overrides: ten fake/gitops implementations and 25
wrapper overrides across 19 existing wrapper types (including the simulator's
fault wrapper). All production callers and test callers supply options. The
simulator retains the exact options on its scheduled delayed-push event and
replays them; a dedicated test proves the retained guard executes.

The immutable validation context owns cloned policy/ref inputs and binds the
repository/queue, governing commit, effective digest, protection basis and
fingerprint, operation, exact refs/old leases, and known landing OIDs. The guard
reloads `Store.Load` at dispatch and immediately before push. Missing context,
missing guard, ref/lease mismatches, incomplete graphs and changed durable
policy/evidence fail before publication. Warnings identify the particular
bypassed PR policy and are logged through the transient options callback.

Gitops prepares once, inspects those actual local OIDs under the existing
cache/session lock, validates every affected ref, then pushes. Inspection
rejects shallow/altered history, disables replacements/grafts/lazy fetching,
bounds graph/tree output and proves every old-history frontier edge. The
required-authority build path leases the observed old test-ref SHA in its push;
the zero-options build retains its original force-push command exactly.
Atomic landing validates the base and newly minted fence together. Base-only
retained content rules also validate queue builds, including fast-forward
markers. The current producers do not assert trusted signature verification;
required signatures remain incompatible until such proof exists.

Serve's dormant preparation hooks separate governing-policy resolution,
durable config/mode pinning and protection observation. Serve enforces that
order before constructing options, reloads the durable pin/evidence, and never
uses zero options for an unresolved authority policy. PR 9 supplies the adapters;
none is installed by this PR, and public authority config stays rejected.
The source accessor uses the latest retained applied observation's basis
(highest basis first); PR 4 retains ownership of source revision interpretation.
Proposed verification never supplies runtime content evidence.

The registration, actual `refreshWorker` bootstrap, and reconciliation-cycle
paths all reach the same `initializeFence` preparation and guard. The existing
legacy calls remain at their original locations for byte-identical legacy I/O;
a required-authority call cannot create a fence until resolve/pin/observe has
completed. This conditional ordering preserves existing queue behavior while
providing the PR 9 activation boundary. Existing fences and missing/changed
cleanup refs return their no-op results before dispatch/guard checks, including
when preparation produced required-but-unavailable options.

Focused real-git tests executed with exit 0 on 2026-10-20 cover rejected
merge/squash/rebase/fast-forward builds, intermediate commits, unsigned markers,
fence creation/advance, atomic base+fence landing and branch cleanup. Push spies
prove no push invocation and compare complete bare-remote `show-ref` output.
Accepted-build tests inspect the returned OID before push and check the expected
construction count, including the legacy unreferenced replay-base fallback,
without rebuilding a preview. Actual blob-size/path and merge-parent rules are exercised.
Shared fake/real contracts reject unsigned objects and preserve read-first
no-ops. PostgreSQL bootstrap tests use isolated schemas on localhost:54329 and
real bare remotes: resolve/pin/observe precedes the first fence, and a basis
change during preparation leaves the fence absent without a push.

`scripts/check-adr0008-3a-legacy.py` supplies an identical reflection-based probe
to both revisions. Fixed timestamps and entropy make raw Git objects comparable.
For each of merge/squash/rebase/fast-forward it captures refs and every local
bare-remote object after fence create, build, build update, fence advance,
landing and cleanup. It separately captures the deterministic fake's complete
ref/object maps for the same 24 checkpoints. Only the candidate's extra trailing
zero argument differs at invocation; no remote service is contacted.

Final full-check, object-equivalence, replay exits and artifact hashes follow
below after execution. Earlier check iterations caught a missing lint comment
and incomplete plumbing during implementation; those failed iterations are not
acceptance evidence.
