# Round 4 privacy projection evidence

Test tree: `f1c199d7` (reflection test `5e80c7c2`, landing-batch fix
`af0a7d40`). Dependency activation remains confined to the test seam. Inactive
goldens and v1 wire fields are unchanged.

## Reflection allowlist

`TestDependencyEntryReflectAllowlist` fills every `Entry` field by reflection,
including the unexported `dependencyWaiting`, every `LandingView` field, run
ledger identities, change evidence and dependency diagnostics. It then masks
the entry under all 13 statuses plus an unknown one. A masked entry may keep only these
leaves: PR, head, position, admission time, status, reason, last dequeue reason,
change, dependency, `dependencyWaiting`, landing outcome/SHA/event ID, waiting-entry detail
and the public dequeue gate. Kept leaves must equal the input, except for the
documented progress normalization. `TestDependencyBatchViewReflectAllowlist`
fills every `Batch` (including `CheckRetryState`) and `BatchesInvalidated` field
and requires a redacted queue to carry no batch leaf at all. A new field
therefore defaults to absent. An overlay that copies `BatchID` into the
projection fails the test.

## Metamorphic generator

The seed-derived variants (period 10) are replaced with an explicit cross
product. `statusfixture.MetamorphicCases`:

- transitions (14): head change, closure, dequeue request, retarget, pause,
  each of the first four followed by the real `queue.Plan` follow-through
  (`batches_invalidated` including `change-left-queue`, then `dequeued`), CI
  pass, CI failure (plus planner follow-through), check retry with
  `CheckRerunRequested`, landing start without confirmation, and confirmed
  landing;
- CI provenance absent or present;
- private observation before or after the public transition;
- co-batched or not;
- target PR 1, 2 or 3;
- depth 1 or 4.

That is 672 cases. Each case is rendered in nine private worlds. The worlds are A waiting, closed,
stale or unavailable with nothing batched; A landed with B (and co-batched
independents) testing; latched `dependency-changed` invalidation; discarded
compositions; a **building** dependent predecessor with the independent PRs
**unbatched** when `cobatch=false`; and a private flap that discards the first
attempt and rebuilds a new composition (`b2`/`b5`) that reaches testing. Every
event is accepted by the real journal reader. The generator asserts that planned
transitions dequeue the target and that chained batches record their
invalidation.

Worlds are compared only within the same public settlement facts: the PRs in a
landing or landed batch, and every PR that left the queue. Examples are a
confirmed landing moving the base SHA, or a failed solo batch dequeuing B. Each
case must compare at least two worlds. Cases with a later private read must
produce at least two landing worlds.

Status and CLI tests apply a queue policy, so derived holding gates such as
`predecessor-build` are rendered. The serve test compares actual forge arguments,
durable records and publication counts across worlds. It then requires:

- zero publications on an unchanged pass and after a private-only read;
- that an authorized check audience, revoked **without advancing the journal**,
  publish exactly the fresh unauthorized records (at least one world must show
  that authorization changed a record);
- on alternating cases, a GitHub authority pin with retained checks for
  departed PRs, which drives the retained-check refresh path.

`-short` is a fixed 14-case subset, one per transition, covering every
dimension value and every leak shape, over worlds {waiting, testing,
invalidating, discarded, building, rebuilt}. Serve uses its first five cases.
The full product runs outside `-short`. Unmutated full runs pass: status 77.8s,
CLI 45.3s, serve 436.4s (no race).

## Leak reintroduction

Each leak was reintroduced alone through `go test -overlay` with a scratch copy
under `~/.cache/r4-202/mut`; no mutation was committed. Only
`TestDependencyMetamorphic(Privacy|CLI|Publication)$` ran across status, CLI and
serve, with `-failfast`, in `-short` and full mode. Every mutation failed by
assertion; none failed by build error, panic or timeout. The unmutated short
run passes.

| # | Leak | Mutation | Short | Full |
| --- | --- | --- | --- | --- |
| 1 | `predecessor-build` on an independent PR behind a building dependent | `maskDependencyEntry`: keep `Gate` when it is `predecessor-build` | CAUGHT: status, world 7 (building, unbatched followers) `/v1/queues` differs | CAUGHT: status |
| 2 | Check cache survives revocation without a journal advance | `ledgerSummary`: drop `authorization` from cache validity | CAUGHT: serve, "kept authorized output after revocation" | CAUGHT: serve |
| 3 | Co-batched C shows `dependency-changed` | `maskDependencyEntry`: keep `Reason` when it is `dependency-changed` | CAUGHT: status, world 5 (invalidating) | CAUGHT: status |
| 4 | `dequeue-pending` exposes `batch_id`/`batch_position`/`batches_ahead` | `maskDependencyEntry`: copy the three fields for `dequeue-pending` | CAUGHT: status, world 4 | CAUGHT: status |
| 5 | Duplicate publication on unchanged passes under the authority pin | `report`: `if admitted && w.dependencies == nil` (drop admitted skip) | CAUGHT: serve, "republished 6 unchanged checks" (GitHub pin) | CAUGHT: serve |
| 6 | `DependencyCheckStatus` bypasses redaction for `dequeue-pending` | return `previous` for `dequeue-pending` | CAUGHT: serve, world 4 forge payload differs | CAUGHT: serve |
| 7 | Batch-derived `Detail` exposes CI provenance / `openmerge/queue/main/b1` | `maskDependencyEntry`: keep `Detail` for `dequeue-pending`/`paused` | CAUGHT: status, world 4 | CAUGHT: status and CLI |
| 8 | Landing-batch metadata before a confirmed merge | restore pre-`af0a7d40` `maskDependencyBatches` (keep `landing` batches) | CAUGHT: status, world 8 (rebuilt `b2`) `active_batch` differs | CAUGHT: status and CLI |

Round 4 review found that the previous generator missed leaks 1, 2 and 5. All
three are now caught in both modes, and the five leaks it already caught (3, 4,
6, 7 and 8) still are. Logs are under `~/.cache/r4-202/logs` locally.

No real GitHub requests are made; the forge is the in-process fake. All Go
commands used the heavy lock, Go 1.27.3, `GOFLAGS=-p=2`, `GOMEMLIMIT=4GiB`, and
both test database variables pointing to port 54329.

## Validation and timings

`golangci-lint run ./...` reports 0 issues; gofmt is clean. Package times for
`go test -race -short -count=1`, both test database variables exported:

| Package | `dc8e7eef` | this round |
| --- | ---: | ---: |
| status | 48.475s | 54.746s |
| serve | 331.751s | 333.473s |

The serve metamorphic test schema is migrated once per case. Queue tables are
truncated between worlds.
