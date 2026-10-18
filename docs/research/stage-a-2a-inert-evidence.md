# Stage A 2a: inert-component evidence

This is merge evidence for the dormant pinned diff capability, not a deployment
approval. No live database, App permissions, service units, git cache or remote
`openmerge/*` refs were used or changed. Tests use the explicitly supplied test
Postgres, httptest, and temporary git repositories.

## Inputs

- Approved plan: `f55a31bdc2946e1380ab0f8226b3a8bb6718e079`.
- Baseline main: `1383c297453ca04b8ac424babc6659d998634c29`.
- Current main: `df3af44ffede645c7fb64a37d83d6bae334a6aa7` (approved-plan
  documentation only; production/test inputs unchanged from the baseline).
- Rebased implementation: `bcc22acaf0266760eba001df80fa97fcf4694331`.
- Tested implementation: `0185ea41a7d4d254210eb119f10b005b592c33a5`.
  The following evidence-only commit changes documentation, not implementation.
- Toolchain: `mise exec -- go version` → `go1.27.2 linux/amd64`.
- Baseline binary SHA-256 (`mise exec -- go build -o /tmp/om-sa-2a-old ./cmd/openmerge`):
  `d8943447c0b12948dd314bc8266082e1647bb829704e7bbcb862e72b5d960b77`.
- New binary SHA-256 (same command, output `/tmp/om-sa-2a-new`):
  `4d9f4fcfeebf54185fb8dbb6aad4193464b9a8fdc7834c734448a44aa0884a69`.

## Differential results

1. All **73** legacy wire fixtures are byte-identical. Hash the sorted
   `internal/queue/testdata/wire/v1/*.json` files as concatenated relative path,
   NUL, then file bytes: both trees yield
   `c99ff4e8c5450148ca60c378521a4e3bf03c69ee34707b69e0f910583d5a1c5c`.
2. Offline fixture-journal replay is identical at all **66** event checkpoints
   (33 events at each of depths 1 and 4), including serialized next actions and
   idempotency keys. The complete output SHA-256 is
   `b989daef369fff9b42f0f0542329ab4d6b2854ae1d55665e637ba25e28e7cb26`.
   The reproducible helper below uses the legacy block-replay sequence and
   records the injected `BatchStarted` checkpoint too. This is explicitly
   fixture evidence, not a claim about an exported deployment journal.
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
5. There are **no new event types**, actions, runtime reads, records or decisions.
   The capability has no runtime caller. Shared `forge.go`, driver/fake structs,
   queue state/wire, config loader/schema and `internal/changepolicy` are untouched.
   The sole existing implementation edit factors the unchanged git command
   setup into a writer-based helper so the new reader can bound its output.

## Validation

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

Deployment remains governed by ADR 0007: compare an actual exported journal at
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
