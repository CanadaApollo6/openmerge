# ADR 0008 PR 10a-2 approved size-gate split

Assessment against origin/main `25d309d80e7a8b2944d642bd17c2277c127fe274`
(PR #146), on branch `adr0008/10a-2-audit-evidence`.

The user approved this split on 2026-10-22. Implement 10a-2a now; 10a-2b
remains deferred. The size estimates below record the original gate assessment,
not measured implementation sizes. Current measured acceptance belongs in
`adr-0008-10a-2a-evidence.md`.

## Original remaining-size estimate

| Work | Estimated changed lines |
| --- | ---: |
| Bounded disk-spooled closure, typed associations and dependency boundaries | 700–850 |
| Governing check/retry join and excluded-attempt evidence | 300–400 |
| Absence identity, replay validation and closure verification | 300–400 |
| Operator CLI and command tests | 250–350 |
| Semantic, database, real-git, growth and race acceptance tests | 1,050–1,300 |
| Independent golden derivation and final fixture changes | 300–400 |
| Scratch verifier, mutation table and evidence/design documentation | 450–550 |
| **Total** | **3,350–4,250** |

At the original gate, the source exporter streamed selected records directly
and did not resolve dependency closure or record dependency boundaries.
Its replay authenticates retained selections against the database but does not
interpret evidence relationships. LandingDecisionEvidence opens a separate
transaction and cannot simply be called from the audit snapshot. The design
also requires bounded external merge/dedup bookkeeping rather than a growing
in-memory dependency map. These constraints make a small CLI-only completion
insufficient.

## Approved split

### 10a-2a: complete internal evidence semantics (this PR)

Implement the bounded spool/merge pipeline in the audit snapshot, typed
relationships, owning decision identities, per-record evidence kinds,
out-of-range roles, manifest dependency boundaries, and source deduplication.
Resolve all seven required evidence families with identity checks. References
may point forward and sort by a documented total comparator before deduplication.

Represent historical absence with an explicit owner plus expected-kind/type
identity and no invented source event ID. Required retained evidence that cannot
be found fails. Validate recorded/not_recorded statuses against the database on
replay, including a claim of absence contradicted by a retained source.

Join admitted checks to batch/tested-SHA and producer/run retry history. Select
the newest applicable attempt at the decision boundary, retain generation and
dispatch/observation provenance, and explicitly exclude later retries. Test both
success-then-failure and failure-then-success, plus identity mismatches.

Verify closure during export by exhausting the typed resolver worklist and
checking resolved references against the selected sources. Replay verifies the
retained relationships and sources. Replay accepts a fully rehashed export with
dropped events/queues; 10b external anchoring detects that substitution relative
to a previously anchored manifest. Internal hashes are not a completeness proof
against deliberate recomputation.

Include cross-range, shared-source, unknown-history, missing-evidence, role
(`""` and `"Scope"`) rejection and database-status contradiction tests. Add
complete-pipeline growth and bounded-memory probes outside short mode, plus
focused mutation and race evidence. Estimated size: 2,200–2,750 lines.

Keep v1 explicitly PROVISIONAL. Do not regenerate or freeze the final complete
export goldens in this layer. Add focused independently specified semantic
expectations without presenting provisional output as the final v1 contract.
The operator command remains unavailable until completion.

### 10a-2b: operator command, final v1 freeze and complete acceptance (deferred)

Expose range export and exact --manifest re-export through cmd/openmerge/audit.go.
Use only the read-only reader; validate tenancy, normalize bounds, sanitize
errors, and document exit codes. No migration, worker lock, write or forge call.

Add real-git journal acceptance against a bare remote, restricted-role command
acceptance, and operator end-to-end scratch verification using adr0008_scratch_db.py.
Keep DSN guards compatible with localhost:5432 and localhost:54329. Complete
mutation coverage and measure the actual closure/export/replay pipeline.

Independently derive the final complete-export goldens once, documenting inputs,
exact bytes and digests without using the Go serializer as the oracle. Update
the design and testdata README together: v1 becomes frozen with this completion;
after merge any serialization shape change requires a version bump. Remove the
obsolete dependency_boundaries.jsonl/four-stream claims and fix the prior
evidence document's comma typo.

10a-2a already requires full regression comparisons, fixture immutability,
GOMAXPROCS=1 race stress with eight owned CPU burners and main-versus-branch
CI-like package timings. Repeat relevant acceptance for the command/freeze
layer; report measured results only. Estimated size:
1,150–1,500 lines. This layer completes the original ADR PR 10a row.

Both layers remain Inert by their own behavior but depend on Hold PRs and may
only deploy after period B drains. All original prohibited-change and database
safety constraints continue to apply. Neither layer changes queue/wire/schema,
indexes, forge behavior, shared formatters, Stage A retries, build/CI settings,
or existing fixtures outside internal/audit/testdata.
