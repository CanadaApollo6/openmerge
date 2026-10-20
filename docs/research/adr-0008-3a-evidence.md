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
The source accessor supplies only the latest applied observation's basis and
exact observation for drift rejection (highest basis first). Evaluation consumes the
activation's pinned seeing evidence; PR 4 owns source revision interpretation.
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

## Round-1 executed results (2026-10-20 UTC; historical)

The code/evidence-probe revision is `3e64e48b84848b5ded989cf109559f0b0d7db8b5`.
The final binary built by `make check` reports `3e64e48`. The evidence-only
commit following it changes documentation/artifact metadata, not executable Go.
The clean pure-layer revision is `7e34fe2`; the baseline is
`ec09672f85c94540c2bf8287b9fa03f48a08e59c`.

- Pure-layer `make check`: **exit 0**, including race tests and PostgreSQL,
  from its isolated clean checkout. Log: `/home/riels/.cache/openmerge-3a-i-clean-check.log`.
- Integration `make check`: **exit 0**, including all real-git, simulator,
  PostgreSQL and race tests, lint, formatting and binary build. Completed around
  18:17 UTC. Log: `/home/riels/.cache/openmerge-3a-ii-edge-check.log`.
- Final focused registration/refresh/cycle bootstrap matrix: **exit 0**.
  It covers basis change, digest change and actual store-pool closure before the
  final guard. The guard reloads rather than reusing a cached state. Canonical
  installation/repository/queue binding is checked before options are created.
- Final real-git rejection/evidence run: **exit 0**. Sixteen retained scenario
  directories include 36 raw inspected commit records, tree/path/size inventories,
  identical before/after refs and zero-push counts. These include builds for all
  four methods, intermediate messages, markers, fences, atomic landing, deletion,
  path/size/linear rules, validity drift and read-first no-ops. They are retained
  at `/home/riels/.cache/openmerge-3a-rejected`, with individual hashes in the
  results manifest. No test fixture's temporary-directory cleanup removes these.
- Final binary object/ref/command comparison: **exit 0**, 24/24 real-git
  checkpoints and 24/24 fake checkpoints identical. Each raw object dump is
  base64 encoded without trimming or UTF-8 replacement; the manifest collector
  independently recomputes each object's Git OID. Normalized argv traces are
  identical, with cumulative push counts 1–6 for create/build/update/advance/
  land/delete in each merge-method workload. Thus zero options add no Git read
  or push and preserve push order as well as object bytes and refs.
- Final strict offline differential replay: **exit 0; gates 2/4 PASS**.
  State, structural state, snapshot hashes, planned actions and wake-boundary
  checkpoints match **2,159/2,159** over **2,158** events (source IDs 1–2158).
  All **8,676** boundary probes agree; the maximum is 12, below the 128 cap.
  Repeat determinism, 22 tool self-tests and 7/7 synthetic checkpoints pass.
  This is offline reuse of previously exported input, not a live DB/API read.

The [machine-readable results](adr-0008-3a-results.json) retain exact source
revisions, before/after remote SHAs, exported-input watermarks, fixture seeds,
commands/exits and a 135-artifact SHA-256 manifest. Raw object/ref/argv snapshots
are under `/home/riels/.cache/openmerge-3a-legacy-edge`; raw replay streams and
the complete replay report are under `/home/riels/.cache/openmerge-3a-replay-edge`.

Key artifact hashes:

- Real-git snapshot/command stream, baseline **and** candidate:
  `c4540c89e3f93b997c6132bb976532d7701b0a07e434a70868123cb80b626d0e`.
- Fake object/ref stream, baseline **and** candidate:
  `041deff2b2f2fd0985c4c1d185267ad9e759d6af1404ee9ef6eda6eee2f342fd`.
- Each strict replay stream (baseline/candidate/repeat):
  `1fe079295539183b7008e70b940c7f71d45bd4aa4e443c8c1e581fa10d31051a`.
- Replay configuration SHA-256:
  `3efb43bbc05f5f0b03c005331f79064829fa018e9f6446224682e00dd357fead`.
  Its durable digest is
  `e58c893b8b3af7685dc07a086824b3bc1fc044456083d193f9566f8387cb58fc`
  at source event 1287. No replacement `config_applied` would be emitted.

Reproduction uses the existing check/replay flags unchanged:

```sh
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB \
  OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable' \
  make check
python3 scripts/check-adr0008-3a-legacy.py \
  --baseline /home/riels/.cache/openmerge-3a-baseline \
  --out /home/riels/.cache/openmerge-3a-legacy-edge
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=6GiB \
  GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local \
  python3 scripts/check-live-journal-replay.py \
  --baseline /home/riels/.cache/openmerge-3a-baseline \
  --events /home/riels/.cache/om-pr72-fix1-input/events.jsonl \
  --queues /home/riels/.cache/om-pr72-fix1-input/queues.jsonl \
  --config-file .openmerge.yml --strict-state \
  --out /home/riels/.cache/openmerge-3a-replay-edge/report.json
```

The object-comparison script acquires the required lock for every Go invocation
itself; do not wrap that Python process in a second copy of the same lock.
Real-git fixtures use the fixed `2026-10-15T12:00:00Z` clock and repeated `5a`
entropy bytes; fake fixtures use counter nonces. Bootstrap uses fixture App ID
42 and installation 7, with isolated PostgreSQL schemas and local bare remotes;
these are test identities, not evidence of a live App grant.

Failed development iterations are excluded from acceptance: incomplete API
plumbing/formatting/lint were corrected; an initial new test wrongly counted
rebase's existing synthetic replay-base object as duplicate generation and was
corrected without changing the builder. An earlier replay refused a checkout
changed during execution; the final clean-commit replay above supersedes it.

No implementation scope was deferred to another 3a PR. Unsupported patterns,
rule families, and trusted-signature verification remain explicit fail-closed
cases rather than compatibility claims. Organization-sandbox enforcement is the
unavailable acceptance item, still pending the dependent sandbox run. This is
**Hold**, not deployment approval, and does not activate authority before PR 9.

The final acceptance revision includes an aggregate 100,000-parent-edge budget
in the shared validator and both local producers, in addition to the per-commit
and object/path/metadata limits. The many-parent regression and shared fake/real
contracts pass (`/home/riels/.cache/openmerge-3a-graph-cap.log`). Full `make check`,
legacy object/ref/argv comparison and strict offline replay were all rerun after
this final tightening and exited 0. The retained earlier rejection inventories
remain the same valid small-graph cases; the final full suite re-exercised them.

## Round 2: integration corrections

Both PRs remain **Hold**. #105 rebased onto `cfa7bf6`; #106 rebased onto
#105's round-2 head before its producer/boundary changes.

Gitops now uses NUL-delimited raw `diff-tree --no-renames` separately for each
parent (root against the empty tree), retains changed names including deletions
and rename sources, and reads sizes only for changed post-image objects. The fake
constructs the same per-parent evidence. Whole unchanged trees are not submitted
to content rules. New queue refs exclude only the ancestry of their target base
read from the remote in the locked session; every frontier entry gets a local
ancestry proof. No caller-supplied arbitrary ancestor can select that source.
Deletion inventories do not hydrate the deleted old object. Parent/frontier caps
are enforced before growing traversal/maps, and OIDs count toward metadata.

A cached GitHub pin permits legacy fallback only when `AuthorityRequired` is
false and its governing commit/digest agree with resolved worker config and the
durable `ConfigDigest`. Unresolved startup config returns unavailable options;
`EnsureFence` can still return an existing ref, while absent-fence bootstrap
recognizes the specific pending-config error and defers until reconciliation
resolves config. Required/inconsistent options never create an absent fence.
The fake now checks dispatch before the fence lease, as gitops does; a shared
contract asserts the error class. All 20 overriding wrappers/test doubles have
compile-time `forge.Git` assertions in addition to both concrete implementations.

Runtime evaluation consumes **the pinned seeing observation in the authority
activation**, with its recorded protection fingerprint, never fresh runtime
protection evidence. The latest retained observation is used solely to reject
source/evidence drift; ordinary unseeing reads do not replace the pinned
inventory or require a fresh seeing fingerprint. Later complete seeing reads
must agree with the pinned fingerprint. Missing activation evidence rejects; constructors
require `SeeingComplete()`. PR 9 activates these hooks/context construction.

Round-2 3a-i verification at `0de94f725b37cbde7fc495e4fe3eb7cc6913590b`:

- `make check`: exit 0, including race tests and test PostgreSQL at localhost:54329.
  Log: `/home/riels/.cache/openmerge-3a-i-round2-final-check.log`.
- Legacy comparison: exit 0, 24/24 real Git and 24/24 fake checkpoints identical.
  Report: `/home/riels/.cache/openmerge-3a-i-round2-legacy/report.json`.
- Strict offline replay: exit 0, gates 2/4 PASS, 2,159/2,159 checkpoints and 8,676
  boundary probes agree; maximum 12, cap not hit. Repeat determinism, all 22
  script self-tests and 7/7 synthetic checkpoints pass. Report:
  `/home/riels/.cache/openmerge-3a-i-round2-replay/report.json`.
- 3a-ii focused tests: exit 0 for forge/fake/GitHub/gitops/serve, plus the 21-case
  real-Git bootstrap matrix (registration/refresh/cycle × success, basis drift,
  config drift, reload failure, missing seeing activation, fingerprint drift, unseeing runtime evidence).
  Logs: `/home/riels/.cache/openmerge-3a-ii-round2-focused.log` and
  `/home/riels/.cache/openmerge-3a-ii-round2-seeing-final.log`.

All commands used the required host lock, `GOFLAGS=-p=2`, and 4 GiB memory limit
(6 GiB for replay). The initial external-package test compile typo was corrected;
a superseded full run was canceled after the final cap vectors were added. Only
the final successful run above is acceptance evidence. No flags, Makefile, CI,
live database/status API, service units, or live refs were changed.

## Final round-2 executed results

The tested 3a-ii executable revision is
`e8f77f9487552832fc5cc143f23b67bb6c87df34`, stacked on 3a-i
`0de94f725b37cbde7fc495e4fe3eb7cc6913590b`. The following evidence-only commit
changes documentation/results, not Go source. Both PRs remain **Hold**.

- Final 3a-ii `make check`: **exit 0**, formatting/lint (zero issues), all race
  tests including PostgreSQL at localhost:54329, real Git, simulator, and build.
  Log: `/home/riels/.cache/openmerge-3a-ii-round2-final-check.log`.
- Final 3a-ii legacy comparison: **exit 0**, 24/24 real Git and 24/24 fake
  checkpoints identical in object bytes, OIDs, refs, and normalized command/push
  traces. Report: `/home/riels/.cache/openmerge-3a-ii-round2-final-legacy/report.json`.
- Final strict offline replay: **exit 0**, gates 2/4 **PASS**. State, structural
  state, actions, snapshot hashes and boundary checkpoints match **2,159/2,159**;
  **8,676** boundary probes agree, maximum 12 (cap 128 not hit). Repeat determinism,
  all 22 self-tests, and 7/7 synthetic checkpoints pass. Report:
  `/home/riels/.cache/openmerge-3a-ii-round2-final-replay/report.json`.
- Retained real-Git rerun: **exit 0**, 15 rejection scenarios independently have
  zero pushes and byte-identical before/after refs. Capture includes 40 raw
  parent/root diff commands and 146 cat-file reads, plus deletion/rename and
  second-parent merge cases and creation excluding existing oversized content.
  The 447-artifact index is
  `/home/riels/.cache/openmerge-3a-round2-object-evidence/manifest.json`; its raw
  commit OIDs were independently recomputed from Git's object framing. The run's
  log is adjacent at `test.log`. Actual deletion inventories accept an absent
  old object, proving that inspection does not fetch it.

The [round-2 machine results](adr-0008-3a-round2-results.json) record both tested
source revisions, metrics, exact artifact paths and SHA-256 hashes (including
exported input). Legacy stream hashes remain
`c4540c89e3f93b997c6132bb976532d7701b0a07e434a70868123cb80b626d0e`
(real Git) and
`041deff2b2f2fd0985c4c1d185267ad9e759d6af1404ee9ef6eda6eee2f342fd`
(fake). All strict replay streams hash to
`1fe079295539183b7008e70b940c7f71d45bd4aa4e443c8c1e581fa10d31051a`.

These results supersede round-1 size/fast-forward rejection claims: the old size
rule spelling was unsupported, and sibling fast-forward heads failed before
validation. The final tests use the REST `max_file_size` type and a valid chain
with a specific signature-validation error assertion. A superseded 3a-ii check
was canceled while correcting the seeing-source guard; the final successful
check/replays above include the hidden-bypass steady-state case. Context evaluation
always uses activation evidence, while the guard binds the exact latest durable
observation/revision without requiring that ordinary runtime read to be seeing.

The earlier reproduction commands apply unchanged, substituting the final
round-2 candidate/output paths above. No GitHub enforcement sandbox was used;
merge-parent parity remains explicitly unverified because the cited documentation
does not prescribe parent selection. This is test/evidence completion, with
activation still owned by PR 9 and no deployment authorization before period B drains.
