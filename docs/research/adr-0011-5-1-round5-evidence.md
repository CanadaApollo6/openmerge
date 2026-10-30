# Round 5 privacy projection evidence

Code: `9176c96b` (fix) and `0d280b99` (generator). Dependency activation remains
confined to the test seam. Inactive goldens and v1 wire fields are unchanged.

## Unconfirmed landing

`LandingStarted` is recorded before any forge call (`internal/queue/doc.go`).
Until `Landed`, the forge shows nothing, so a landing in progress is OpenMerge
batch progress. Only a confirmed merge is public, because the forge shows the
push.

- `maskDependencyEntry` normalizes `landing` to `queued` with no reason, like
  `building`, `testing` and `invalidating`. `Gate` (`landing-settlement`) was
  already dropped, and `maskDependencyBatches` already removed the chain, so
  `landingPR` cannot match.
- `DependencyCheckStatus` no longer maps a masked entry to `forge.Landing`.
- Publication cadence follows from the rendered value. The planner emits no
  `ReportStatus` effect at `LandingStarted`; its report effects are `blocked`,
  `merged` (on `Landed`), `dequeued` and pause only. The periodic report sends
  the redacted check through the exact-head dedupe, so an unchanged rendering
  writes nothing. The serve metamorphic test compares the forge arguments,
  the durable records and the write count of the first report. It then
  requires zero writes on an unchanged pass and after a private-only read.
- `TestDependencyUnconfirmedLandingIsGeneric` replays the round 4 reproducer:
  for every PR, the API response, the summary and the redacted check after
  `ChecksCompleted` + `LandingStarted` equal those of the waiting world. Astra's
  original `TestAstraR4UnconfirmedLanding` passes unchanged on this head.

## Grouping on public facts only

Worlds were partitioned on `landing:<members>` as soon as a batch reached the
landing stage, before any confirmed outcome. That excluded exactly the
waiting-versus-landing comparison. Worlds were also partitioned on every queue
exit, whatever its cause. `statusfixture.metamorphicSettlement` now builds the
group key from two kinds of fact only:

| Key | Fact | Why it is public |
| --- | --- | --- |
| `merged:<pr>` | member of a batch whose `Landed` was recorded | The forge shows the push to the base branch and the PR as merged to every reader of B's repository. `LandingStarted` alone yields no key. |
| `left:<pr>:<reason>` | the case's target PR, dequeued for `closed`, `requested`, `not-eligible` or `head-changed` | Each reason is caused by an event on that PR that the forge shows: the PR closed, an `@openmerge dequeue` comment, a retarget that makes the PR ineligible, or a head push. |

### Justifying the left-queue key

The four public reasons are caused by the public transition itself. The
transition is applied to the same target PR in every world, so it never
partitions worlds by private state. `requestDequeue` always records a pending
dequeue, whatever the batch state. For the `-planned` variants, the generator
asserts that the real planner dequeues the target in every world. The key is
therefore the same in every world of a case, and it only labels the public
history.

Any other exit is not a grouping key. In the generator, the only such exit is
`checks-failed`: when B is tested alone (`cobatch=false`), its failed CI
dequeues it. That composition exists only because the private source allowed B
to be tested. The world is still rendered. Serve still checks it for zero
republication, a private-only read, and cache revocation. But
`MetamorphicWorld.Uncompared` excludes it from comparison, and it gets no
group of its own.

**Residual, not hidden:** B's own `checks-failed` dequeue reveals that B was
tested, and so that A had landed when the composition started. Stage A cannot
avoid this without not dequeuing B. It belongs to the same class as a confirmed
merge of B, but the forge does not show it independently of OpenMerge's check.
It needs an ADR-level decision, not a projection fix.

### Restoring the old key

The `regroup` overlay restores only the round 4 `landing:<members>` key. With
the round 5 fix in place it passes, so the key changes nothing. Combined with
the row 9 revert it also passes: **row 9 goes undetected**. Without that key,
row 9 is caught by all three channels. The grouping fix is therefore what lets
the tests see the leak.

## Vacuous cases

Each world records `Applied` (the transition changed its queue; CI, retry and
landing apply only to a started batch). It also records `Preempted` (a private
read before the transition discarded every batch it could advance).
`RequireComparedWorlds` requires the transition to apply in at least one world
that is compared with another.

- **Short:** every case must meet this, or the test fails; no case may skip.
  All 15 short cases meet it.
- **Full:** a case may skip, by name, only when the reason is one of three
  impossible shapes. Anything else fails. The unmutated full product skips 79
  of 672 cases per status/CLI test:

| Reason | Cases per test |
| --- | ---: |
| A private read before the transition discarded every batch | 60 |
| Every applied world left the queue for a private cause (`checks-failed`) | 8 |
| Sole world in a `merged:` group (a private-first read kept only one world landing) | 11 |

Sonnet's count of 60 vacuous CI/landing cases is the first row; they now skip
by name instead of passing vacuously.

### Short subset (15 cases)

| Change | Reason |
| --- | --- |
| `ci-passed` is `private-first=false` | It previously applied in 0 of 6 worlds. It now reaches `landing-preflight` against the unbatched world. |
| `ci-failed` is `cobatch=true` at depth 4, plus a new depth-1 `cobatch=true` case | Provisional multi-PR failure in a chain at depth 4, and `bisection` at depth 1. Depth 4 never renders `bisection`. |
| `check-retry` is `cobatch=false` | Unbatched followers render `predecessor-checks`. |
| Serve's first five now include `ci-passed` | `head` moved to sixth place; status and CLI catch its building-predecessor shape (row 1). |

## Revert table

Each row was reintroduced alone, through `go test -overlay` with scratch copies
under `~/.cache/r5-202/mut`. Nothing was committed. Only
`TestDependencyMetamorphic(Privacy|CLI|Publication)$` ran, across status, CLI
and serve, with `-short`. Every detection is an assertion failure; none is a
build error, panic or timeout. The unmutated short run passes with no skips.

| # | Leak | Mutation | `-short` |
| --- | --- | --- | --- |
| 1 | `predecessor-build` on an independent PR behind a building dependent | keep `Gate` when `predecessor-build` | CAUGHT: status, world 7, change view |
| 2 | Check cache survives revocation without a journal advance | drop `authorization` from ledger-cache validity | CAUGHT: serve, "kept authorized output after revocation" |
| 3 | Co-batched C shows `dependency-changed` | keep `Reason` when `dependency-changed` | CAUGHT: status, world 5 |
| 4 | `dequeue-pending` batch ID, position and ahead | copy the three fields | CAUGHT: status, world 4 |
| 5 | Duplicate publication under the authority pin | `if admitted && w.dependencies == nil` | CAUGHT: serve, "republished 6 unchanged checks" |
| 6 | `DependencyCheckStatus` bypass for `dequeue-pending` | return `previous` | CAUGHT: serve, world 4 forge payload |
| 7 | Batch-derived `Detail` | keep `Detail` for `dequeue-pending`/`paused` | CAUGHT: status, world 4 |
| 8 | Landing-batch metadata before a confirmed merge | keep `landing` batches in `maskDependencyBatches` | CAUGHT: status, world 4 `/v1/queues` |
| 9 | Unconfirmed landing status | revert `9176c96b` (no `landing` normalization; `forge.Landing` in the check) | CAUGHT: status world 4 `/v1/queues`; CLI world 4; serve world 4 forge payload |
| 10 | CI gates `landing-preflight` / `bisection` | keep `Gate` when either | CAUGHT: status, world 4 change view |

Row 10 was also split to show that each gate is caught on its own:

- `landing-preflight` only: caught by status, CLI and serve in the `ci-passed`
  case.
- `bisection` only: caught by status and CLI in the depth-1 `ci-failed` case.
- `predecessor-checks` (extra probe): caught by status and CLI in `check-retry`.

Grouping control:

| Overlay | `-short` |
| --- | --- |
| restore `landing:<members>` key only | passes (the key no longer matters) |
| restore `landing:<members>` key + row 9 | **passes: row 9 undetected** |

## Validation and timings

Unmutated runs pass in both modes:

| Run | Result |
| --- | --- |
| `-short` metamorphic (status, CLI, serve) | pass; no skips |
| Full product, status | 83.6s; 79 named skips per test |
| Full product, CLI | 49.3s; 79 named skips per test |
| Full product, serve | 447.4s; 593 pass, 79 named skips |
| Astra's round 4 reproducer, unchanged | passes (`testing: status=queued landing=<nil> active=<nil>`) |
| `TestDependencyUnconfirmedLandingIsGeneric` with row 9 reverted | fails for PRs 1, 2 and 3 |

Full-mode times are without `-race`.

`golangci-lint run ./...` reports 0 issues. gofmt and `git diff --check` are
clean, so no style commit was needed. No real GitHub requests are made; the
forge is the in-process fake. All Go commands used the heavy lock, Go 1.27.3,
`GOFLAGS=-p=2`, `GOMEMLIMIT=4GiB`, and both test database variables pointing to
port 54329.

Package times for `go test -race -short -count=1`, both test database variables
exported:

| Package | round 4 (`1ec64c66`) | this round |
| --- | ---: | ---: |
| status | 54.746s | 58.373s |
| serve | 333.473s | 353.730s |

The serve metamorphic subset still has five cases; `ci-passed` replaces `head`.
