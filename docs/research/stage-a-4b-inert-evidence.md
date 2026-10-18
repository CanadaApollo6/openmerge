# Stage A 4b: CODEOWNERS and attributes parsing

Baseline: `aeefc769dbe66679f7937e2538ca735371a3b64e` (origin/main, PR 4a #66).
Frozen implementation: `ac0ef82f36d259898028dee054cd80882299ab8b`.
Acceptance-case naming and defensive seam tests:
`9b5ab22ff7e486fc3d0fb43ea255597ce02a47eb` (test-only follow-up).
Branch: `stage-a/4b-owners-attributes`.
Worktree: `/home/riels/openmerge-sa-4b`.
Baseline checkout: `/home/riels/openmerge-sa-4b-main`, detached at origin/main.
The subsequent evidence-only commit records validation of that implementation.

## Dormant boundary

ADR 0007 deployment class: **Inert**. This PR implements the pure `Deriver`
seam; no production caller invokes `Classify` or `TreeDeriver`, and no production
path emits `change_classified`. No public config keys, event/state schema,
actions, planner, executor, landing gate, live configuration, workflows or
legacy policy digests change. Production integration remains PR 11b work.
Public loaders continue to reject classification/approval keys.

No live DB, systemd units or `openmerge/*` refs were accessed. Only main was
fetched. Only this PR's branch is pushed. Code, tests and simulators make no real
GitHub API calls; PR publication is the separately authorized exception.
Database tests use only the supplied shared test Postgres. No initdb is run.

The `classifications` projection introduced by 4a remains **nil** for queues
without approvals/without classification events; this PR introduces no state.
**Before the next deployment**, add `classifications` to
`scripts/check-live-journal-replay.py`'s `STATE_EXCEPTIONS`, as a reviewed
**zero-only** exception. The replay tool must still reject any nonzero value.
This PR does not edit that exception list, deploy a binary, read/export a live
journal, or claim live-journal replay coverage. Compaction stays off.

## Parsing and producer contract

New classifier version:
`classification-v1/git-evidence-v7-bounded-omissions/governing-v1/hunks-v1/facets-v3`.
The version-keyed replay allowlist keeps facets-v1's old rename flag meaning
and facets-v2's path-derived rename checks. Facets-v3 keeps those checks and
adds these derivations. Existing registry/variant fixtures remain byte-identical;
the version golden advances and a separate facets-v3 wire variant is added.
No event payload shape changes.

CODEOWNERS selection advances through `.github/CODEOWNERS`, `CODEOWNERS`, then
`docs/CODEOWNERS` only after definite checked absences. Last matching line wins;
an ownerless line clears ownership. Patterns have their own parser, independent
of `condition.CompileGlob`. Vectors pin basename versus anchored patterns,
directory recursion, wildcard leaves, `*`, `**`, `?`, comments, escaped whitespace
and interior hashes. Leading `\#`, negation and character ranges are diagnosed,
matching GitHub's documented exclusions. Names and team slugs canonicalize to
lowercase without `@`; email local case is preserved and domain case lowered.
Email owners never enter the resolver.

Conservative differences from GitHub: invalid patterns/file contents make the
source unknown rather than merely skipping invalid syntax lines. Invalid owners
retain diagnostics and make an affected entry unresolved. Files at/over 3 MiB
are unknown; the existing forge aggregate 1 MiB governing budget can produce
unknown sooner. CODEOWNERS always comes from the default-branch governing tree,
including for release queues. Mixed email/user entries block code-owner rules,
while a real user pool can satisfy independent mandatory policy ownership, as
specified by the dated ADR clarification.

Attributes use governing root-to-parent precedence and later-line precedence,
including set, unset, unspecified and false/true string values. Unspecified
clears an inherited generated mark; it does not infer Linguist content heuristics.
Patterns use Git's byte-oriented wildmatch semantics, C-quoted paths and ranges;
directory patterns do not recurse. The built-in binary macro has no generated
effect. Custom macro definitions, unsupported range syntax, invalid tokens,
negative patterns and unsupported generated values are diagnosed and unknown.
There are no head, host/global/info attribute reads. Isolated local Git oracle
vectors verify the supported attribute dialect without any forge/network use.

Injected owner resolution closes over supplied PR 3a stable user identities,
team identities and complete fresh membership; the classifier performs no I/O.
Missing, unreadable or stale observations remain unresolved. Groups own no paths.
Every observed inventory `(path, side)` gets exactly one code-owner row, and
policy paths get independent policy-owner rows. Ordinary edits/additions use
the new side, deletions the old side, renames both. Every governing owner token
has explicit false non-matches, including per-file facts; incomplete inventories
retain positive observations without proving aggregate absences.

## Mandatory 4a carry-overs

1. `Classify` checks each ancestor `.gitattributes` probe and the definite
   CODEOWNERS selection chain itself. Missing probes never prove absence.
   A root generated wildcard cannot bypass a missing nested probe. The
   `labels: [generated]` / `require: none` Evaluate regressions cover missing
   root/nested, unreadable nested, rename old-side and deleted old-side probes.
2. `Classify` serializes protected classification fields before `Owners`,
   rejects changes to inputs, inventories, paths, hunks or other facets, and
   detaches governing DTOs before invoking any Deriver. Mutated governing
   evidence is rejected without altering caller probes. Unknown ownership
   forces `unknown`, independently of whether the Deriver set it.
3. A version-keyed allowlist has explicit facets-v1/v2/v3 rows. Wire/snapshot
   replay tests exercise all three; historical fixtures are unchanged.
4. `docs-codeowners-owner-required` is unskipped and proves an actual owner
   approval is necessary despite a docs `none` rule. Unreadable attributes
   are exercised through Evaluate, including the missing-ancestor regression.
5. Diagnostics remain in `Eligible.Reason` and `LabelReasons`; no error channel
   is added to Deriver. Explicit false owner facts prove non-matches, while
   absent tokens remain unknown. Email-only and unreadable-team pools block.
6. The README tells users to include `.github/actions/**` in sensitive settings
   when composite actions need protection. ConfigBlob follows `config.Find`:
   exactly one complete config candidate; two files have no precedence winner,
   leave ConfigBlob empty and add unknown/ambiguity diagnostics.
7. Real TreeDeriver-to-Evaluate tests cover renames, deletions, policy changes,
   uncovered/ownerless entries, email-only/mixed owners, readable/unreadable
   teams, stale membership, disjoint per-file obligations, and no cross-file
   owner union. Coherent evidence never produces owner_evidence_incoherent.
   Both code-owner rules and mandatory policy ownership are tested separately.

Named acceptance also covers precedence across all three owner locations,
path dialects, policy deletion/rename, governing-tree self-weakening protection,
attribute overrides/unsets and repeated classification/fingerprint equality.
See [the component contract](../../internal/classification/README.md).

## Validation

All checks use `GOTMPDIR` and `TMPDIR=~/.cache/om-sa-4b-tmp`,
`GOCACHE=~/.cache/go-build`, `GOMEMLIMIT=6GiB`, `GOMAXPROCS=2`, `GOFLAGS=-p=1`
and `OPENMERGE_TEST_DATABASE_URL=postgres://postgres@localhost:54329/postgres?sslmode=disable`.
Final verification additionally disables module/toolchain downloads.
Heavy checks run sequentially. The reproducible inertness helper is
`scripts/check-stage-a-4b-inert.py BASELINE [BASELINE_SIM_LOG CANDIDATE_SIM_LOG]`;
it performs no DB or forge reads. It compares every legacy JSON fixture and
replays the same fixture sequence at depths 1/4, comparing actions/keys at every
checkpoint, tracking the config digest and asserting classifications stays nil.
Optional simulator comparison drops only test-runner framing/timing and sorts
the unchanged logged result blocks, preserving all reported scenario metrics.

- `make check > ~/.cache/om-sa-4b.log 2>&1; echo exit=$?`: **exit=0**.
  Formatting, zero lint issues, the full short race suite with shared test
  Postgres, and production build pass. Source behavior is frozen at `ac0ef82`;
  the test-only follow-up adds exact ADR names and two adversarial seam cases.
  Its focused `go test -race -short ./internal/classification`: **exit=0**
  (`~/.cache/om-sa-4b-acceptance.log`). Final `make fmt lint` after that
  follow-up: **exit=0**, zero issues (`~/.cache/om-sa-4b-final-lint.log`).
- Candidate `make test-sim-exits`: **exit=0**, `~/.cache/om-sa-4b-sim.log`.
  Fresh `origin/main` baseline `make test-sim-exits`: **exit=0**,
  `~/.cache/om-sa-4b-main-sim.log`. All **146** normalized result blocks match;
  SHA-256 `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
- `python3 scripts/check-phase2-compatibility.py /home/riels/openmerge-sa-4b-main`:
  **exit=0**, `~/.cache/om-sa-4b-compatibility.log`. All **17** legacy exit
  records match, SHA-256
  `bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`.
  All **1,055** depth-1 retry/default-digest records match, SHA-256
  `07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37`.
  All **76** existing registry wire fixtures match byte-for-byte.
- `python3 scripts/check-stage-a-4b-inert.py /home/riels/openmerge-sa-4b-main
  ~/.cache/om-sa-4b-main-sim.log ~/.cache/om-sa-4b-sim.log`: **exit=0**,
  `~/.cache/om-sa-4b-inert.log`. All **112** existing JSON testdata fixtures
  are byte-identical. Offline fixture replay compares **67** records
  (**66 event checkpoints** plus the config-digest header), with identical
  next actions/keys at each checkpoint and nil classifications throughout.
  Replay comparison SHA-256:
  `94e97da35ba05d72dcc0c5c1b326b49783b7f994f6060bf1b98dc402d60c2d6f`.
  Tracked config digest remains
  `e58c893b8b3af7685dc07a086824b3bc1fc044456083d193f9566f8387cb58fc`.

The first development `make check` exited **2** on four lint findings (builtin
name shadowing and a boolean-expression simplification); those were corrected.
Early focused runs exited **1** while correcting fixture expectations, adding
the new version golden and fixing literal directory matching after `**`.
The intermediate complete `make check` also exited **0**; its log is
`~/.cache/om-sa-4b-intermediate.log`. The final status ledger is
`~/.cache/om-sa-4b-statuses.log`, driven sequentially by
`~/.cache/om-sa-4b-validate.sh`.

The final aggregate diff is below 2,000 changed lines against origin/main,
within the ADR's approximately 3,000-line ceiling. No live deployment gates
are claimed: a real exported-journal comparison and live effective-config
verification remain required before deployment.
