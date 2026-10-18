# PR 5 component evidence

Baseline: `1383c297453ca04b8ac424babc6659d998634c29` (origin/main).
Candidate: the PR head; the PR body records its exact SHA. All implementation
changes are confined to `internal/changepolicy` and its fixtures. Go 1.27.2 is
selected through `mise exec --` for every command below.

- Legacy queue fixture directory: zero diff against baseline. The SHA-256 of
  sorted `sha256sum` records from `git ls-files internal/queue/testdata` is
  `4114ff96f752fadb7be370b026baf286c4be055bd6643f5cb206fc783b1f06cc`.
- The public schema is unchanged, SHA-256
  `1bf2d6072fa4acaf89409bf8243b6ff79b3d07baf9c5f1b1b6d984e63706cbe9`.
  Public loader/schema rejection tests cover every new key. No production
  package imports changepolicy. No event/action type, writer or registry changes.
- Reproducible old/new production binary builds are byte-identical, SHA-256
  `e366f194f1d7e08404166ea06410e60059c3fde14c9f270ab9fdaa97bd067a4d`.
  Command in each checkout: `mise exec -- go build -trimpath -buildvcs=false
  -ldflags '-X main.version=stage-a-5-inert-proof' -o <output> ./cmd/openmerge`.
- `mise exec -- make test-sim-exits` exits **0** in each checkout. All 146
  result lines (lines indented four spaces, stripped and sorted; harness timing
  and scheduling lines excluded) are identical, SHA-256 with a final newline
  `418f7487f391c0dcadea5dfa135cd01862038a618ab0333cec2c0564f99dd7bd`.
  The command uses the repository's unchanged seeds and profiles.
- Offline checkpoint comparison uses the stored repository journal fixture
  `internal/serve/testdata/legacy_rules_log.jsonl`, SHA-256
  `2914403563aeb7f04caf038626190ff06b66f769a2973d3bc480b9cc3a8b51bc`.
  Copy `testdata/replay/main.go` to the same relative path in the old checkout;
  run `mise exec -- go run ./internal/changepolicy/testdata/replay` in both.
  Outputs are byte-identical at every event, at depths 1 and 4 (8 checkpoints),
  including complete action wire payloads, keys and effective config digests.
  Output SHA-256: `244be7ab8d14b27fd5d9e3e49a61b492f928ba25d6c554eb7eefd687ce293f5d`.
  The harness only reads the fixture and emits JSON; it writes no events or refs.
- Legacy digest vectors remain `75c86e0f6a55101c55b9de4d07940a8d98c1f110df2b98aad901cf0557bfa4aa`
  and `936a86848e84606ba89a7ae1ee56db20d204957d800189accb6a83ea9aa505e8`.
  The policy component is absent for legacy siblings, even with repository
  policy settings and an opted-in queue. No queue.Config fields were added.

This is component evidence for an inert PR, not a deployment record. No live
journal, database, effective config, service or refs were accessed or changed.
A fresh live-journal export and the full trial deployment gates remain required
before an operator deploys. Final required check statuses are in the PR body;
logs are `/tmp/om-sa-5.log` and `/tmp/om-sa-5-sim.log` on the implementation host.
