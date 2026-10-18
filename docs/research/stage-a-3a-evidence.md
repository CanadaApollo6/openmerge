# Stage A 3a merge evidence

This evidence supports merging the dormant identity capability. Deployment gates
are a separate operator step below. No live database, App settings, service units,
or queue refs were changed. Tests use HTTP fixtures and the requested local test
database only. Native stance acquisition and dismissal times remain PR 3b work.

## Inputs and reproduction

Specification: approved Stage A plan at `f55a31bdc2946e1380ab0f8226b3a8bb6718e079`,
`docs/adr/0007-changes-not-prs.md`, PR 3a.
Original PR parent: `df3af44ffede645c7fb64a37d83d6bae334a6aa7`.
Final rebased parent: `f30dd96910d6a8832e794be96104b5666c13616d` (main, #60).
Full-check and clean comparison baseline:
`b1587a7eb6347340a2ad0c03683ebe755e2eda74` (Stage A 1 / #59).
The final rebase adds only #60's two ADR documents; runtime, test, and build
sources are byte-identical to the passing checked tree.
Candidate: the clean final PR checkout; the replay log prints both source HEADs.
Toolchain: mise Go `1.27.2`, Linux amd64.

```sh
mise exec -- bash -c 'OPENMERGE_TEST_DATABASE_URL="postgres://postgres@localhost:54329/postgres?sslmode=disable" make check > /tmp/om-pr58-fix1.log 2>&1; echo exit=$?'
mise exec -- go test -race -count=1 ./internal/forge/... ./cmd/openmerge
mise exec -- make test-sim-exits
mise exec -- python3 scripts/check-phase2-compatibility.py /tmp/om-pr58-fix1-rebased-baseline
mise exec -- python3 scripts/check-stage-a-grant-compatibility.py /tmp/om-pr58-fix1-rebased-baseline /tmp/om-pr58-fix1-evidence-final
mise exec -- env CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-X main.version=stage-a-3a-evidence' -o /tmp/om-pr58-fix1-new-server ./cmd/openmerge
(cd /tmp/om-pr58-fix1-rebased-baseline && mise exec -- env CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-X main.version=stage-a-3a-evidence' -o /tmp/om-pr58-fix1-old-server ./cmd/openmerge)
```

The baseline is a detached clean worktree at the SHA above. The replay script
refuses dirty candidate or baseline trees (including untracked files), requires
a fresh output directory, and checks that `internal/queue/wire.go` is unchanged.
The export/replay script uses the `grant` filename to preserve Stage A 1's
existing `check-stage-a-identity-compatibility.py` and its baseline-only CLI.
It retains both compiled Go test binaries, the Go overlay, synthetic journals,
and old/new per-revision logs. Replay builds use `-trimpath -buildvcs=false` and
the fixed version ldflag above. Neither script installs files in the source tree.
Binary SHAs are not asserted in this merge evidence; deployment artifact hashes
must be supplied independently by the operator.

## Results

- Required database-backed `make check` after rebasing onto #59: exit **0**,
  including formatting, lint, the full short race suite, and CLI build.
  Log: `/tmp/om-pr58-fix1.log`. The pre-rebase full run also exited **0**
  (`/tmp/om-pr58-fix1-pre-rebase.log`). No backstop failure occurred in either
  full run, so the conditional isolated reruns were not needed.
  The initial attempt exited **2** at lint (a new tagged-switch suggestion);
  it was fixed before the full runs. Log: `/tmp/om-pr58-fix1-first.log`.
- Forge and CLI tests under the race detector: exit **0**. Coverage includes no
  new warning from legacy permission validation, setup's missing-members warning
  with the conditional wording, and the dormant policy hook's grant check.
- `make test-sim-exits`: exit **0**.
- Candidate and baseline standalone reproducible server builds: exit **0** each.
- Shared fake/HTTP contracts pin missing `WriteGrant` observations as errors
  wrapping `ErrIdentityUnavailable`, including HTTP 404. A successful non-writing
  response is a known denial; an error is unknown and any grant must be discarded.
  Rename/reassignment, response ID omission, revocation, account kinds and
  incomplete pagination also remain covered.
- Membership fixtures pin `role=all` and consumption of the full paginated result.
  Child-team inclusion follows the endpoint's documented semantics; these offline
  fixtures do not independently verify GitHub's hierarchy expansion.
- Compatibility script: exit **0**. All **74** baseline wire fixtures (including the **73** legacy fixtures) are
  byte-identical. All **17** legacy simulator exit records match, SHA-256
  `bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`.
  All **1,055** retry envelope/default-digest records match, SHA-256
  `07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37`.
- Every-revision offline replay: exit **0**. Identical next actions after all
  **697** depth-1 and **1,075** depth-4 events. Including digest and journal
  identity records, the **1,775** compared lines hash to
  `e1ad29fe27dee98647f3eadcc90ace99e3272cc06d69ef4e453b45167dd947d4`.
- Legacy default effective config digest remains
  `75c86e0f6a55101c55b9de4d07940a8d98c1f110df2b98aad901cf0557bfa4aa`.
- Dirty candidate and dirty baseline refusal probes each exit **1**, before
  creating an evidence directory.
- Queue, config, planner, executor, serve, simulator, schema, wire registry and
  legacy fixture sources are unchanged. Legacy `CanWrite` and `Approval` are
  unchanged. No runtime caller uses the new identity or team-policy capability.
  `members: read` is excluded from unconditional permission validation; only
  explicit setup diagnostics and the dormant policy hook inspect this grant.

## Synthetic input identities

| Exported journal | SHA-256 |
|---|---|
| Depth 1 | `94417e636146833d3b0cb9ac808fc7af94c9e9145b9b5ef7d890a6f91587c0e7` |
| Depth 4 | `6fcee2ebba08efbe6fdcb754c2b609d1fb4e95faea2dc94fedab8c7285fb1b6e` |

Synthetic inputs use seed 42, 40 PRs, one-hour arrivals, batch size 8, one-minute
batch wait, broken rate 0.2, flake rate 0.1, one check retry and 50 runners.
Each old/new replay decodes the same baseline journal; neither writes events.
The tested YAML is the legacy default `main` queue with
`merge_when: [check-success = ci]` and no approval configuration.

## Operator deployment gates

Before any trial deployment, independently supply the actual old/new server
binary SHAs, an exported live journal identity/hash, its every-revision offline
replay, and the unchanged live effective configuration/digest. This merge evidence
does not read the live journal or complete those deployment gates. Keep the live
App's permissions frozen; permission adoption belongs in an isolated sandbox
until the trial pair drains. See [the operating guide](../operating.md#organization-membership-permission-stage-a-prerequisite).
