# PR 5 component evidence

## Round 1

Baseline: `df3af44ffede645c7fb64a37d83d6bae334a6aa7` (the PR merge base with origin/main).
Candidate: `930973cd29470d931c1af268d7f6847f24d55b41` (before the round-2 rebase). All implementation
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
  result lines (lines indented four spaces, stripped and sorted with
  `LC_ALL=C sort`; harness timing and scheduling lines excluded) are identical, SHA-256 with a final newline
  `418f7487f391c0dcadea5dfa135cd01862038a618ab0333cec2c0564f99dd7bd`.
  The command uses the repository's unchanged seeds and profiles.
- Offline checkpoint comparison uses the stored repository journal fixture
  `internal/serve/testdata/legacy_rules_log.jsonl`, SHA-256
  `2914403563aeb7f04caf038626190ff06b66f769a2973d3bc480b9cc3a8b51bc`.
  Copy `internal/changepolicy/replay/main.go` to the same relative path in the
  old checkout;
  run `mise exec -- go run ./internal/changepolicy/replay` in both.
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
fix-round logs are `/tmp/om-pr55-fix1.log` and `/tmp/om-pr55-fix1-sim.log` on the
implementation host.

## Round 2

Code-validation baseline: `b1587a7eb6347340a2ad0c03683ebe755e2eda74` (#59).
Final rebased main: `f30dd96` (#60), which changes only the two approved ADRs;
the production code and this component are unchanged by that rebase.
The current PR head is recorded in the PR body. The code-owner addition remains
confined to `internal/changepolicy`. The public schema and legacy queue fixtures
have zero diff against this baseline. The unchanged legacy digest vectors and
public-loader/schema rejection tests pass, together with the frozen semantic
policy digest `6fd2a7569b7259c86a11209759ea489ade2113ea715e98ded2ca9791b6c2f4e6`.
No field was added to `Requirement`: its preexisting JSON bytes are pinned.
Code-owner policy vectors, using the fixture plus an all-path `touches` rule:

- `{code_owners: true}`: `cc891b1319abcb4512391fc471ea6fe1389d4c0cd136f63082e8fbd823bf49a7`.
- `{code_owners: true, receipt: true}`: `048bd4409f31d0d3f3236e68bdb94e648100492c0026103455ee7b3961600fc9`.

Production builds using the reproducible command above are byte-identical to
the rebased baseline, SHA-256
`1425a204a6d44ab67d507556af1277c28009118552f4568437d0a8884149be9e`.
Round-2 required-check logs are `/tmp/om-pr55-fix2.log` and
`/tmp/om-pr55-fix2-sim.log`; their real exit statuses are in the PR body.

Offline replay also matches the rebased baseline at all eight checkpoints,
SHA-256 `244be7ab8d14b27fd5d9e3e49a61b492f928ba25d6c554eb7eefd687ce293f5d`.

The required simulator suite exits **0**. All 146 normalized result records
match the round-1 baseline byte-for-byte (SHA-256
`418f7487f391c0dcadea5dfa135cd01862038a618ab0333cec2c0564f99dd7bd`).
