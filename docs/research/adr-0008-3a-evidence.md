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
