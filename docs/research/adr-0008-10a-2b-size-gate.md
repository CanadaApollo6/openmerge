# ADR 0008 PR 10a-2b size gate

Assessment on 2026-10-23 against origin/main
`4b1fe46` after #146 and #147, in isolated branch
`adr0008/10a-2b-audit-cli`.

The expanded completion is estimated at **2,250–2,950 changed lines**,
counting additions and deletions. This exceeds the requested approximately
2,000-line gate. The user approved the two-step sequence on 2026-10-23. The estimate is
not a measured diff, and no acceptance or performance result is claimed.

## Remaining work

| Work package | Estimated changed lines |
| --- | ---: |
| CLI, dispatch/help entry, argument validation, sanitized exit paths, focused command tests | 350–450 |
| Governing-pin fold agreement, prefix differential tests, invalid/digest-mismatched pin rejection | 350–450 |
| `inapplicable` authority status with retained github source, replay tests, SQL attempt tie, dead guard removal | 250–350 |
| Bare-remote journal acceptance and SELECT-only role command acceptance | 350–450 |
| Independent complete-export reference, fixture updates, Go fixture comparisons | 350–450 |
| Scratch command verifier, mutation corrections, realistic growth probe | 350–450 |
| Frozen specification, operating guidance, evidence and timing reports | 250–350 |
| **Total** | **2,250–2,950** |

The earlier 1,150–1,500-line estimate in
`adr-0008-10a-2-size-gate.md` predates the additional review carry-overs.
Reusing existing readers, test fixtures, the Python canonical encoder, and
regression runners is assumed in this estimate. Their full sizes are not
counted again.

## Reasons the carry-overs precede the freeze

`internal/audit/evidence.go` currently selects the largest pin event ID before
the decision and derives requiredness directly from its authority field.
The queue fold also validates pins and checks configuration digest agreement.
Comparing only valid authority/rollback histories does not prove agreement.
The implementation needs a prefix differential test against
`queue.Apply(...).AuthorityPin()`, including config transitions and rejected
pins. A rejected pin must cause export to fail closed rather than silently
erase authority requiredness. Keep this correction inside `internal/audit`.

The same resolver currently erases a github rollback pin's source and emits
`not_recorded`. Before deriving complete goldens, preserve that source and use
`inapplicable` for this distinct case. Reserve `not_recorded` for absent
evidence. Update reference validation, closure, replay, and rollback assertions
together. A rehashed status or source substitution must fail against retained
history.

The SQL already constrains retry generation, so remove `sameGeneration` and
its equivalent mutation claim. Add the cross-event normalized-attempt tie:
run 5 attempt 1 followed by run 5 attempt 0 must select the later event.
Mutate the SQL normalization itself. Distinguish SQL selection from the
within-event normalization used to compare and describe the chosen run.

`scripts/verify-adr0008-10a-2a.py` still mutates both the dead generation check
and github source erasure. The completion verifier must revise these targets,
add the missing differential/status/tie mutations, and report equivalent or
surviving mutants separately from assertion kills.

## Proposed implementation sequence

1. **10a-2b-1 — governing-pin fold agreement, inapplicable status, attempt tie.** Resolve evidence semantics and their differential, replay, and mutation
   coverage first. Keep complete-export fixtures provisional in this step.
   Estimate 850–1,150 changed lines including focused evidence documentation.
2. **10a-2b-2 — operator command and complete-export acceptance.** Add the operator command and finish real-git, restricted-role, scratch,
   independent-golden, growth, and regression acceptance. Freeze v1 only after
   the first step lands. Estimate 1,400–1,800 changed lines including final
   operating and freeze documentation.

The user authorized step 10a-2b-1 now. Step 10a-2b-2 remains separate.
Both steps remain subject to the existing prohibited-change rules.

The command should use `audit.Open`, `Reader.Export`, and `Reader.Replay`.
Use exit 0 for success, 2 for usage/selector/bounds errors, and 1 for sanitized
connection, source, integrity, or publication failures. Manifest replay must
reject conflicting range selectors. Publish only through the existing private
sibling-directory mechanism, without migrations, worker locks, or forge calls.

Derive final complete-export bytes from documented literal journal inputs
with an independent Python reference, adapting the existing independent encoder
in `scripts/verify-adr0008-10a-1.py`. Do not feed Go-produced export bytes back
as the expected result. Compare all streams, chain links, selection metadata,
manifest self-hash, and file hashes. Update the design and testdata README
together, remove obsolete fourth-stream claims, and require a version bump
for serialization shape changes after merge.

Measure closure/export/replay with realistic `checks_observed` filler outside
short mode. The reported Sonnet 16 ms lookup and 12.5 s export/12.7 s replay
at 64 owners and 80k rows are prior review observations, not measurements
from this branch. New measurements must state inputs, plans, and projected
costs. Account for existing production decision/settlement expression indexes
from migrations 006/007; report remaining lookup scans separately. No migration
or index belongs in either step.

The final verifier must retain the strict scratch-DB guard and note that
`act` sets both CI variables. It must prove the requested regression counts,
fixture immutability, additive CLI help, race stress with eight owned burners,
and branch-versus-main CI-like timings. It must also document that a fully
rehashed omission can pass replay and needs a 10b external manifest anchor
for detection.

## Classification and current state

The proposed production edits stay in `internal/audit`, `cmd/openmerge/audit.go`,
and the additive command dispatch/help wiring in `cmd/openmerge/main.go`.
Step 10a-2b-1 changes production code only in `internal/audit`: **Inert by
its own behaviour; depends on Hold code; deploy only after period B**.
Step 10a-2b-2 includes the additive `main.go` dispatch wiring and is **Hold**;
that classification is accepted. Deploy only after period B in either case.

This note is committed with step 10a-2b-1. Complete-export fixtures remain
**PROVISIONAL**; there is no v1 freeze in this step. Measured implementation
and verification evidence is recorded in `adr-0008-10a-2b-1-evidence.md`.
