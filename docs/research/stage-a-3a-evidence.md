# Stage A 3a implementation evidence

This is evidence for the dormant identity capability, not a live deployment
attestation. No live database, App settings, service units, or queue refs were
changed. Tests use HTTP fixtures and the requested local test database only.
Native stance acquisition and dismissal times remain PR 3b work.

## Inputs and reproduction

Specification: approved Stage A plan at `f55a31bdc2946e1380ab0f8226b3a8bb6718e079`,
`docs/adr/0007-changes-not-prs.md`, PR 3a.
Baseline: `1383c297453ca04b8ac424babc6659d998634c29` (main).
Candidate: the implementation tree in this PR, built atop that baseline.
Toolchain: mise Go `1.27.2`, Linux amd64.

Main subsequently advanced to `df3af44ffede645c7fb64a37d83d6bae334a6aa7`
(the approved plan merge), changing only `ROADMAP.md`, this ADR, and the dogfood
document. Runtime and test source are identical to the baseline above.

```sh
mise exec -- bash -c 'OPENMERGE_TEST_DATABASE_URL="postgres://postgres@localhost:54329/postgres?sslmode=disable" make check > /tmp/om-sa-3a.log 2>&1; echo exit=$?'
mise exec -- make test-sim-exits > /tmp/om-sa-3a-sim-exits.log 2>&1; echo exit=$?
mise exec -- python3 scripts/check-phase2-compatibility.py /tmp/om-sa-3a-baseline
mise exec -- python3 scripts/check-stage-a-identity-compatibility.py /tmp/om-sa-3a-baseline /tmp/om-sa-3a-evidence-final
mise exec -- make build
(cd /tmp/om-sa-3a-baseline && mise exec -- make build)
```

The baseline is a detached clean worktree at the SHA above. The replay script
requires a fresh output directory; it retains both compiled Go test binaries,
the Go overlay, exported synthetic journals, and the old/new per-revision logs.
It does not install files into the queue or simulator source trees.

## Results

- `make check`: the full run exited **2** on
  `TestHealthyIdleLoopUsesBackstopAndDeliveryInterruptsTimer`, at
  `webhooks_test.go:359` (idle journal change did not trigger reconciliation).
  The same test failed **4/30** repetitions on clean main and **4/30** on the
  candidate (both repeated-test commands exited **1**). No queue/server code
  was changed to work around it. Full-check retry: exit **0**.
  The earlier lint-only run also exited 2; its lint issues were fixed, and
  subsequent lint runs report zero issues. Full-run logs are
  `/tmp/om-sa-3a-first-full-check.log` and `/tmp/om-sa-3a.log`.
- `make test-sim-exits`: exit **0**.
- Separate candidate and baseline `make build` runs: exit **0** each.
- `go test -race -count=1 ./internal/forge/...`: exit **0**.
- The explicit HTTP comment-author/permission-identity rename and reassignment
  test passed under the race detector, including missing response user/ID and
  same-ID rename cases. Shared fake/HTTP contracts also cover reassignment,
  revocation, account kinds, inherited membership and unknown versus empty reads.
- Compatibility script: exit **0**. All **73** legacy wire fixtures are
  byte-identical. All **17** legacy simulator exit records match, SHA-256
  `bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`.
  All **1,055** retry envelope/default-digest records match, SHA-256
  `07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37`.
- Every-revision offline replay: exit **0**. Identical next actions after all
  **697** depth-1 and **1,075** depth-4 events. Including the digest and journal
  identity records, the **1,775** compared lines hash to
  `e1ad29fe27dee98647f3eadcc90ace99e3272cc06d69ef4e453b45167dd947d4`.
- Legacy default effective config digest remains
  `75c86e0f6a55101c55b9de4d07940a8d98c1f110df2b98aad901cf0557bfa4aa`.
- No queue, config, planner, executor, serve, simulator, schema, wire registry,
  or legacy fixture changes. No new event/action types or runtime identity
  callers. Setup adds `members: read`; installed-token inspection treats its
  absence as a warning. Legacy `CanWrite` and `Approval` remain unchanged.

## Offline artifact identities

These are SHA-256 hashes of locally compiled binaries; none was deployed. The
candidate was built from the implementation working tree before commit, so the
replay script reports the baseline as both source HEADs. Server binaries use
the existing Makefile's version metadata, including the candidate's dirty flag.

| Artifact | SHA-256 |
|---|---|
| Old server binary | `3044087aa8f98959512055e0a82379c19fce04051fc6f99a8125bdd55d854992` |
| New server binary | `dcc4ef55e45daa36708563756365e8c659bd0dd8a40d7f023debffacd46eb443` |
| Old replay binary | `bb25876fcf50c4769e154bdb7a8b24819d01f40d9c9217c4128215b768c7167e` |
| New replay binary | `f7abe9f57739b21e4ea6aeb57b53978a7baaa9183063d142f9fe82bca52e919c` |
| Exported synthetic depth-1 journal | `94417e636146833d3b0cb9ac808fc7af94c9e9145b9b5ef7d890a6f91587c0e7` |
| Exported synthetic depth-4 journal | `6fcee2ebba08efbe6fdcb754c2b609d1fb4e95faea2dc94fedab8c7285fb1b6e` |

Synthetic inputs use seed 42, 40 PRs, one-hour arrivals, batch size 8, one-minute
batch wait, broken rate 0.2, flake rate 0.1, one check retry and 50 runners.
Each old/new replay decodes the same baseline journal; neither writes events.
The tested YAML is the legacy default `main` queue with
`merge_when: [check-success = ci]` and no approval configuration.

Before any trial deployment, independently supply the actual old/new server
binary SHAs, an exported live journal identity/hash, its every-revision offline
replay, and the unchanged live effective configuration/digest. This PR does not
read the live journal or claim those deployment gates are complete. Keep the
live App's permissions frozen; permission adoption belongs in an isolated
sandbox until the trial pair drains. See [the operating guide](../operating.md#organization-membership-permission-stage-a-prerequisite).
