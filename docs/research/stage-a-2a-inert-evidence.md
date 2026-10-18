# Stage A 2a: merge evidence for the dormant component

This is merge evidence for the dormant pinned diff capability, not a deployment
approval. No live database, App permissions, service units, git cache or remote
`openmerge/*` refs were used or changed. Tests use the explicitly supplied test
Postgres, httptest, and temporary git repositories.

## Current inputs (round 3)

- Approved plan: `f55a31bdc2946e1380ab0f8226b3a8bb6718e079`.
- Baseline main: `277c85dbcffdcfbab4aebe2dd7b45eb375b11828`.
- Reviewed round-2 head: `c12874e1a1e6926d355c3d5a339d77876226c7d0`.
- Round-3 implementation and regression tests:
  `9d0a4d5467b008de0ea188829c8d5faa87485713`. The subsequent evidence
  commit changes only this note; the PR body records the pushed head.
- Semantics: `git-inventory-v4-bounded-ancestry`. The golden pins bounds only:
  1,000 files, 2 MiB, zero hunks, 100,000 ancestry commits and a 30-second
  shared discovery/verification deadline. Hunks remain independently incomplete.

## Round-3 design and validation

Verification now walks `rev-list --parents <base> <head> --not <all merge bases>`.
A missing K capable of hiding a better common ancestor S cannot be reachable
from a found base C: otherwise S would be an ancestor of C and not better.
Thus the divergence walk hits K. Raw-parent traversal still disables shallow
boundaries, replacement objects, grafts, commit-graphs and lazy fetching; all
shallow-state rechecks remain. The streaming counter kills Git beyond 100,000
commits. Merge-base discovery and verification share a 30-second deadline,
including with Background callers. Budget exhaustion returns incomplete
`ancestry_limit` with nil error; caller cancellation and missing divergence
commits return incomplete `read_failure` with diagnostic errors.

The fast-import regression builds 2,048 commits, covers exhaustion and the exact
budget boundary, and proves equal endpoints and short divergences do not require
a roots walk. The C→S→K false-base fixture still rejects missing K with/without
a commit-graph and an available promisor remote. On a long shared prefix, removing
an old commit below the valid base still permits the correct S merge base and
code-revert inventory. Deadline/error-contract cases cover both merge-base and
rev-list, internal limits (nil error), and caller deadlines (non-nil error).

Validation from `/tmp/om-pr56-fix3`, Go 1.27.2:

- Focused gitops/forge/fake/GitHub race suite: **exit 0**;
  `/tmp/om-pr56-fix3-focused.log`.
- Final ancestry/promisor regressions with race detection: **exit 0**;
  `/tmp/om-pr56-fix3-history.log`.
- `OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable' make check > /tmp/om-pr56-fix3.log 2>&1; echo exit=$?`:
  **exit 0**. Formatting, lint (zero issues), all short race tests, and build
  passed, including `internal/serve` (292.142 seconds). The full check exercised
  the final production code; final ancestry test refinements were additionally
  verified by the focused history race run above.
- Final lint after those test refinements: **exit 0**;
  `/tmp/om-pr56-fix3-final-lint.log`.

Read-only comparison with baseline `277c85d` confirms no changes to the Forge
interface, queue/config/store/simulator sources, legacy fixtures or tracked config.
The only call sites outside `_test.go` remain the test-only `forgetest` harness;
there is no runtime consumer. Earlier replay/simulator/digest measurements below
are historical evidence, not claims of newly repeated runs.

## Historical round-1 inputs

- Approved plan: `f55a31bdc2946e1380ab0f8226b3a8bb6718e079`.
- Round-1 baseline main: `f30dd96910d6a8832e794be96104b5666c13616d`
  (includes Stage A 1 and the code-owner ADR clarification).
- Earlier review baseline: `1383c297453ca04b8ac424babc6659d998634c29`;
  pre-fix branch base: `df3af44ffede645c7fb64a37d83d6bae334a6aa7`.
- Rebased/tested implementation: `15ab80e84620b82db009067269d1dd8cac229f1f`.
  This identifies the historical round-1 validation, not the current PR head.
- Toolchain: `mise exec -- go version` → `go1.27.2 linux/amd64`.
- Baseline binary SHA-256 (`mise exec -- go build -o /tmp/om-pr56-fix1-main-binary ./cmd/openmerge`):
  `d597a3e7b9b3b86083b742e482658df6203816499fbd604fb2a2dd618bd4eaa8`.
- Rebased binary SHA-256 (same command, output `/tmp/om-pr56-fix1-rebased-binary`):
  `631380f571158d92fa4ac11750c158e5299973053ed5c209e7bae5bd03ad4e4d`.

## Differential results (plan gate numbering)

1. All **73** legacy wire fixtures are byte-identical. Hash the sorted
   `internal/queue/testdata/wire/v1/*.json` files, excluding Stage A 1's
   `event_change_revision_observed.json`, as concatenated relative path, NUL,
   then file bytes: both trees yield
   `c99ff4e8c5450148ca60c378521a4e3bf03c69ee34707b69e0f910583d5a1c5c`.
   Including Stage A 1's new fixture, all **74** fixtures are byte-identical
   between final baseline and rebased implementation (combined hash
   `1c52cf31d0a2239756b7b179f16d6736ddc5177d7bb146ff5e61034180914fe3`).
2. Offline fixture-journal replay is identical at all **66** event checkpoints
   (33 events at each of depths 1 and 4), including serialized next actions and
   idempotency keys. The complete output SHA-256 is
   `b989daef369fff9b42f0f0542329ab4d6b2854ae1d55665e637ba25e28e7cb26`.
   The reproducible helper below uses the legacy block-replay sequence and
   records the injected `BatchStarted` checkpoint too. This is explicitly
   fixture evidence, not a claim about an exported deployment journal.
   **Deployment gate 2 remains an operator step at deploy time.**
3. `make test-sim-exits` exited **0** on both trees with the same unchanged test
   sources/seeds/inputs. All **146** logged result blocks are identical after
   sorting blocks to remove parallel test interleaving and excluding harness
   timing lines. The SHA-256 of sorted blocks encoded with Python
   `json.dumps(blocks, indent=2) + "\n"` is
   `96b1a0800565918e92d33e403ee784e45108ae64b6528d0905d8b9ba38034bbe`.
4. The tracked depth-4 `.openmerge.yml` and its effective `Digest(true)` are
   unchanged: `e58c893b8b3af7685dc07a086824b3bc1fc044456083d193f9566f8387cb58fc`.
   Queue/config source trees are byte-identical. Existing legacy digest-vector,
   wire registry, wire golden, and block-replay golden tests also pass.
   **Deployment gate 4 remains an operator step at deploy time:** verify the live
   effective config digest and no new event types or `ConfigApplied` on offline
   shadow replay of the actual exported journal.
5. Identical composition commit IDs per merge method applies to PR 10; it is
   **not applicable** to this PR 2a.

There are **no new event types**, actions, runtime reads, records or decisions.
The capability has no runtime caller. Shared `forge.go`, driver/fake structs,
queue state/wire, config loader/schema and `internal/changepolicy` are untouched.
Existing git command setup uses a writer-based helper and an isolated diff-read
session mode for bounded output and raw-history settings. Existing build
sessions retain their behavior.

## Round-1 rebased validation (historical)

Tested commit: `15ab80e84620b82db009067269d1dd8cac229f1f`, based on `f30dd96`.
From `/tmp/om-sa-2a`, inside mise's Go 1.27.2 environment:

```sh
OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable' make check > /tmp/om-pr56-fix1.log 2>&1; echo exit=$?
```

**Exit 0.** Formatting, lint (zero issues), all packages with race detection,
and the final build passed. This includes the full `internal/serve` package
(486.466 seconds) and all diff regressions. The only working-tree edits during
validation were to this evidence note; its subsequent commit changes no code
or tests. No additional isolated serve rerun was needed after this passing run.

`mise exec -- make test-sim-exits` exited **0** on both final baseline and rebased
implementation (`/tmp/om-pr56-fix1-main-sim.log` and
`/tmp/om-pr56-fix1-rebased-sim.log`). All 146 normalized blocks match each other
and the original baseline, with the hash above. The offline replay outputs
(`/tmp/om-pr56-fix1-main-replay.txt` and
`/tmp/om-pr56-fix1-rebased-replay.txt`) also match each other and the original
baseline byte-for-byte at all 66 checkpoints. Both standalone binary builds
listed under Inputs exited **0**. All 74 current fixtures are byte-identical
between final baseline and rebased head; the original 73 retain their hash.
Queue, config, simulator, store and tracked config sources are unchanged by 2a
relative to the final baseline.


## Round-1 fix validation before its rebase (historical)

On `ee39f74a45c649609d2433258709b2dcde815151` (based on `df3af44`), the required
`make check` exited **2**. The complete log is retained at
`/tmp/om-pr56-fix1-before-rebase.log`. Lint reported zero issues and every package
except `internal/serve` passed with race detection. Its three unchanged deadline
failures were `TestBackstopReplansInterruptedPeerEnqueue`,
`TestBackstopRecomputesPeerPartialBatchDeadline`, and
`TestHealthyIdleLoopUsesBackstopAndDeliveryInterruptsTimer`. There was no race
failure or package timeout; the serve package took 804.614 seconds.

A read-only check of the supplied test Postgres during that run observed five
sessions waiting on advisory locks in OpenMerge's queue-lock namespace,
consistent with [issue #42](https://github.com/CanadaApollo6/openmerge/issues/42).
The full serve-only rerun with a 15-minute timeout exited **1** after 612.230
seconds with the two backstop deadline failures (no timeout or race failure):
`/tmp/om-pr56-fix1-serve-15m.log`. An earlier shortened three-minute serve-only
run exited **1** at its package timeout while progressing through
`TestServeFastForwardExactHeadAndDivergence/land`:
`/tmp/om-pr56-fix1-serve.log`. All runs used the supplied test database URL,
`mise exec -- go test -race -short -count=1`, and only `./internal/serve` for
isolated reruns. These results are retained, not reported green.

Focused gitops/forge/fake/GitHub tests exited **0**
(`/tmp/om-pr56-fix1-focused.log`). New regression tests against the original
readers exited **1 as expected** (`/tmp/om-pr56-fix1-regression-before.log`),
reproducing the false complete docs-only shallow diff, 1,248-file fake result,
nil fake errors for invalid/missing objects, and complete-but-unpaired inventory.
The pre-rebase simulator run exited **0**, with all 146 normalized result blocks
matching the original baseline (`/tmp/om-pr56-fix1-sim.log`).

The reproductions are now regression tests, including both shallow setup paths
and real GC followed by repeated reads, exact file/byte boundaries, one-byte
overflow, late NUL detection, host rename-limit/submodule settings, and the fake's
raw-byte failure cap. Additional fake tests cover no/multiple merge bases and
exact byte boundaries. Unpaired renames now make inventory incomplete;
`forgetest` pins non-nil errors for invalid IDs and missing objects.
Round 1 used `git-inventory-v2-exact-renames`. Current round-3 semantics are
`git-inventory-v4-bounded-ancestry`; its golden pins bounds only, including the
ancestry commit and duration budgets.

## Initial validation (retained from the original review)

Original tested implementation: `0185ea41a7d4d254210eb119f10b005b592c33a5`.
From `/tmp/om-sa-2a`, using mise:

```sh
OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable' make check > /tmp/om-sa-2a.log 2>&1; echo exit=$?
make test-sim-exits > /tmp/om-sa-2a-sim.log 2>&1; echo exit=$?
```

`make check`: **2**. Its sole failure was the unchanged
`internal/serve.TestBackstopRecomputesPeerPartialBatchDeadline`:
“peer enqueue did not introduce a planner deadline.” The test uses a 300 ms
partial-batch deadline; the full-run test took 1.15 s under concurrent database
suite load. All other packages passed, including the new diff tests. No timeout
or race detector failure was reported. This is consistent with timing/load
sensitivity; the failed full-run result is retained rather than reported green.

An isolated rerun on **both baseline and new code exited 0**:

```sh
OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable' mise exec -- go test -race -short -count=1 -timeout=2m ./internal/serve -run '^TestBackstopRecomputesPeerPartialBatchDeadline$' -v
```

Logs: `/tmp/om-sa-2a-backstop-baseline.log` and
`/tmp/om-sa-2a-backstop-new.log`. The test and all serve/queue/config production
sources are unchanged by this PR. No unrelated timing-test adjustment is included.

`make test-sim-exits`: **0**.
The baseline simulator log is `/tmp/om-sa-2a-baseline-sim.log`.
Focused diff, GitHub localhost HTTP git, fake contract, mode/symlink/gitlink,
rewrite, rename uncertainty, missing/corrupt objects and resource-limit tests
pass. Lint reports zero issues. No GitHub credentials are needed by these tests.

Deployment gates 2 and 4 are operator steps at deploy time, governed by ADR 0007.
Compare an actual exported journal at
every revision against the intended deployment binaries, verify the actual live
configuration/digest and no new `ConfigApplied` or event types on offline shadow
replay, and record the deployment evidence and maintainer classification in the
dogfood log. No live journal export was supplied or accessed for this PR.

## Reproduce the fixture replay

Place the following file at `.stage-a2a-evidence/main.go` in both checkouts,
run `mise exec -- go run ./.stage-a2a-evidence` in each, compare stdout with
`cmp`, then remove the temporary directory. The first line is the tracked
configuration digest; each subsequent line is one event checkpoint.

```go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func main() {
	live, err := os.ReadFile(".openmerge.yml")
	must(err)
	parsed, _, err := config.Load(live)
	must(err)
	fmt.Println("tracked-config-digest", parsed.Queues[0].Digest(true))
	names := []string{"pr_observed", "enqueue_requested", "pause_requested", "action_poisoned", "action_poisoned", "pause_requested", "rules_blocked", "rules_source_blocked", "resume_requested", "rules_cleared", "rules_source_cleared", "rules_cleared", "rules_source_cleared", "pause_requested", "resume_requested", "resume_requested", "test_ref_build_failed", "test_ref_build_failed", "test_ref_build_failed", "resume_requested", "test_ref_built", "checks_completed", "pause_requested", "action_poisoned", "resume_requested", "landing_started", "rules_source_blocked", "pause_requested", "action_poisoned", "resume_requested", "rules_cleared", "landing_abandoned"}
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for _, depth := range []int{1, 4} {
		cfg := parsed.Queues[0].QueueConfig()
		cfg.SpeculationMaxDepth = depth
		state := queue.State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}
		step := 0
		emit := func(name string) {
			actions := []json.RawMessage{}
			keys := []string{}
			for _, action := range queue.Plan(cfg, state, at.Add(24*time.Hour)) {
				_, wire, err := queue.EncodeAction(action)
				must(err)
				actions = append(actions, wire)
				keys = append(keys, action.Key())
			}
			row := struct {
				Depth, Step int
				Event       string
				Actions     []json.RawMessage
				Keys        []string
			}{depth, step, name, actions, keys}
			encoded, err := json.Marshal(row)
			must(err)
			fmt.Println(string(encoded))
			step++
		}
		for i, name := range names {
			if i == 16 {
				state = queue.Apply(state, queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 2, HeadSHA: "head"}}, At: at})
				emit("batch_started")
			}
			data, err := os.ReadFile("internal/queue/testdata/wire/v1/event_" + name + ".json")
			must(err)
			event, err := queue.DecodeEvent(data)
			must(err)
			state = queue.Apply(state, event)
			emit(name)

		}
	}
}
```
