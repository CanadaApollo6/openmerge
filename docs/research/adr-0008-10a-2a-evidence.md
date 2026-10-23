# ADR 0008 PR 10a-2a evidence

Classification: **Inert by its own behavior; deploy only after period B**.
This layer depends on Hold PRs. It has no operator CLI, migration, schema,
index, event/wire/YAML change or forge read. The base is main
`25d309d80e7a8b2944d642bd17c2277c127fe274`.

## Contract and limits

The approved size-gate split is recorded in
[the size-gate note](adr-0008-10a-2-size-gate.md). This layer completes internal
evidence semantics. 10a-2b owns the operator command, real-git command acceptance
and independently derived final complete-export goldens. **v1 remains
PROVISIONAL**. Existing provisional complete-export fixtures are not regenerated
or represented as a final frozen contract.

An evidence absence is a statement about an explicit owner and expected kind /
type identity. It does not invent a source event ID. Required retained evidence
that is missing is an integrity failure. Replay checks both recorded and
not_recorded claims against retained database history, including recorded claims
for nonexistent sources and absence contradicted by a retained matching source.

Export verifies closure within its repeatable-read, read-only snapshot by
exhausting a typed resolver worklist and checking references against selected
sources. Source order is the UTF-8 queue tuple (installation, repository, base, lane),
then queue ID and event ID. Queue ID distinguishes malformed duplicate
queue keys and does not alter order for a valid unique queue inventory; identical source identities deduplicate and contradictory copies
fail. Reference order is bytewise canonical JSON order
of the complete typed reference, including owner and source identity;
equal encodings deduplicate. References may point forward in source order.
Spool files belong to the private staging directory under the output
parent. Sorting uses at most 32 records per chunk and stops after MaxBytes
(plus the final bounded record), with fixed 16-way merge fan-in. Frozen per-queue maximum IDs travel with spool records;
resolving each record does not query a moving maximum. Queue inventory also
uses disk sorting, and replay does not build repository-sized queue maps. Replay verifies retained sources and relationships; it does not prove
that a malicious author retained every original queue/event. A fully rehashed
export with dropped events or queues can replay. The external anchor in 10b
detects substitution relative to a previously anchored manifest.

Check evidence is historical: admitted checks are joined to the decision's
batch and tested SHA, then to producer/run retry history. The newest applicable
attempt at the decision boundary governs, with generation and dispatch /
observation provenance; later retries are explicitly excluded. No current forge
read repairs missing historical evidence.

LandingDecisionEvidence is reused through transaction-local helpers so the
audit snapshot does not open a second transaction. Existing callers retain
their transaction and validation behavior. Their preexisting tests remain
byte-identical and are run unchanged.

## Reproduction

Use the existing shared test PostgreSQL, with isolated test schemas; do not
create a large scratch database. The verifier validates the DSN with
`scripts/adr0008_scratch_db.py` before any subprocess. Its allowlist accepts the
explicit test TCP endpoints `localhost:5432` (CI) and `localhost:54329` (local),
rebuilds the destination URL and refuses libpq routing overrides. The local
run uses only port 54329. This compatibility adjustment to the shared guard and
its connection-free tests is required by the requested CI/local acceptance.

```sh
export GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3
export PATH="$GOROOT/bin:$PATH"
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
python3 scripts/verify-adr0008-10a-2a.py \
  --baseline /home/riels/.cache/openmerge-10a-2a/main \
  --out /home/riels/.cache/openmerge-10a-2a/acceptance
```

The baseline is a detached checkout of the exact base above, never a checkout
change in the main workspace. Both trees must be clean. Every Go invocation,
including Go invoked by make or the replay driver, runs inside
`flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2
GOMEMLIMIT=4GiB`; strict replay uses the requested `GOMEMLIMIT=6GiB` instead.
GOROOT and PATH select the pinned 1.27.3 toolchain. Build/test temporary paths
are under the evidence cache, never `/tmp`.

The verifier writes per-command stdout, stderr and exact command logs, command
exit codes, package and wall timings, a structured summary and a SHA-256
artifact manifest. Error output is sanitized. It never calls a real GitHub API,
live status API or service controller. It compares the fixed offline journal
export rather than querying a live OpenMerge database.

## Acceptance results

Measurements are pending completion of the implementation. No result from a
previous PR is claimed as evidence for this layer. The required acceptance is:

- `make check` exit 0 and unchanged LandingDecisionEvidence caller tests.
- Strict replay: 2,159 identical checkpoints at 6 GiB, simulator 146 identical
  normalized blocks, depth-1/4 contracts 90/90 identical.
- GitHub projection and actual CLI bytes, Stage A exact output and
  MixedGithubBytes unchanged; all fixtures outside `internal/audit/testdata`
  byte-identical.
- All seven evidence families; cross-range and shared sources; unknown history;
  missing evidence; invalid roles `""` and `"Scope"`; database-status
  contradictions; success→failure / failure→success; identity mismatches.
- Complete closure/export/replay growth and bounded-memory probes outside short
  mode, focused semantic mutation coverage and race evidence.
- New-test stress with `GOMAXPROCS=1 -race -count=20` and eight owned CPU
  burners; only those burners are terminated and reaped.
- Both trees timed with `GOMAXPROCS=2 go test -race -short -count=1 -p=1`.
  Package elapsed time excludes compilation and lock waiting. A single local
  pair is descriptive evidence, not a causal throughput benchmark.

The verifier's focused overlay mutation table requires each mutant to compile
and fail a semantic assertion; a surviving mutant or a build/setup failure
fails acceptance. The checkout itself is never edited by these experiments.

| Mutation | Independent semantic expectation |
| --- | --- |
| Reuse spool decode storage | Heterogeneous immutable source payloads survive sorting and replay. |
| Permit a missing required retained source | Export fails for the explicitly required owner/source identity. |
| Choose oldest instead of newest applicable observation | The last observation before the decision boundary governs. |
| Ignore selected check result mismatch | A result from a different attempt cannot satisfy the decision. |
| Accept arbitrary role strings | Empty and `Scope` roles fail strict replay. |
| Skip retained closure verification | Rehashed omission of a referenced dependency still fails replay. |

## Baseline timing run

The exact base checkout completed the requested CI-like command with exit 0.
The command, toolchain/checkout provenance and complete stdout/stderr are in
`/home/riels/.cache/openmerge-10a-2a/timing-before`. Candidate timing and the
remaining acceptance measurements are pending. Go-reported seconds:

| Package | Base main (s) |
| --- | ---: |
| `cmd/openmerge` | 68.443 |
| `internal/ascii` | 1.007 |
| `internal/audit` | 2.599 |
| `internal/authority` | 1.111 |
| `internal/brief` | 3.400 |
| `internal/changepolicy` | 1.750 |
| `internal/classification` | 2.265 |
| `internal/config` | 1.652 |
| `internal/config/condition` | 1.020 |
| `internal/diagnostic` | 1.019 |
| `internal/executor` | 1.168 |
| `internal/forge` | 8.306 |
| `internal/forge/fake` | 1.460 |
| `internal/forge/github` | 65.667 |
| `internal/gitops` | 103.400 |
| `internal/queue` | 179.622 |
| `internal/serve` | 337.080 |
| `internal/sim` | 37.558 |
| `internal/sim/capacity` | 1.009 |
| `internal/status` | 11.853 |
| `internal/store` | 74.253 |
| `scripts` | 1.081 |
| `tools/dogfood` | 1.689 |
