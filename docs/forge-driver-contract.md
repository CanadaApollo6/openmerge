# Forge driver contract

This is the reference for anyone writing or reviewing a forge driver. [ADR 0004](adr/0004-forge-driver-interface.md) records why the interface looks this way; this page records exactly what a driver must do. The Go interfaces live in [`internal/forge/forge.go`](../internal/forge/forge.go), and [`internal/forge/forgetest`](../internal/forge/forgetest) checks every rule below that can be checked automatically.

## Repositories

`RepoRef` holds one opaque `Path` (`owner/name`, or `group/subgroup/name` on GitLab). A driver must not assume an owner/name split.

## Pull requests and approvals

`PullRequest` reports the PR number, current head SHA, whether the head branch lives in the base repository, whether the PR is closed, and its approvals.

Each approval reports the approver, whether the approver can write to the base repository, and **the exact head SHA that was approved**. A stale approval, given before a later push, keeps the old SHA. The planner's trust gate depends on this ([ADR 0004](adr/0004-forge-driver-interface.md), "Test refs never run untrusted code").

Some forges, such as GitLab, do not record which commit an approval was for. A driver for such a forge may report approvals only when the forge is configured to reset approvals on every push, and must verify that setting. Otherwise it reports no approvals, which fails closed: fork PRs cannot enter the queue. The driver's documentation states which behavior it uses.

## Statuses

A driver upserts one OpenMerge status per `(repo, PR, head SHA)`. `Status` has a normalized `State`, a stable machine-readable `Reason` (for example `untrusted`, `head-changed`, `build-failed`), and a human-readable `Summary`. The `Reason` is never used as the forge conclusion.

| State | Check-run conclusion | Commit-status API |
|---|---|---|
| `queued`, `testing`, `landing` | pending / in progress | pending |
| `merged` | success | success |
| `dequeued` | cancelled | failure |
| `blocked` | failure | failure |
| `paused` | action required | failure |

Commit-status APIs that lack cancelled and action-required keep the normalized state and reason in the description.

## Check runs

`CheckRun` has a `Name`, a normalized `State` (pending, success, failure, cancelled, skipped, neutral), and an `ID` that increases with each run of the same name. Drivers translate native run identifiers into that ordering.

The executor aggregates as follows:

- The newest run of each name wins, regardless of the order the forge returns them. A pending rerun supersedes an older result.
- Cancelled counts as failure until a newer run supersedes it.
- Skipped and neutral count as failure unless the `AllowSkippedNeutral` option is set.
- A required check with no run, or an unknown state, is pending.
- An empty list of required checks is a configuration error unless `AllowNoRequiredChecks` is set.

## Building test refs

`BuildTestRef` composes the test commit locally with git and pushes it to `openmerge/queue/...`. It must:

- refuse a request whose pinned head is not the PR's current head;
- produce the shape the merge method asks for: merge commits that keep each PR head as an ancestor, a single squashed commit per PR, or each PR commit replayed separately for rebase;
- put the batch ID in the top commit, so a tested SHA identifies exactly one batch;
- return `ErrConflict` for merge conflicts, and any other error for infrastructure failures.

## Landing

`Land` updates the base branch to exactly the tested commit with one atomic git push that leases both refs:

```sh
git push --atomic \
  --force-with-lease=<base>:<expected-base-sha> \
  --force-with-lease=<fence>:<fence-sha> \
  <remote> <tested-sha>:<base> <new-fence-commit>:<fence>
```

- The forge applies the push only if both refs still hold the expected values. A base that moved forward, was rolled back, or was rewritten is refused.
- On refusal the driver returns only `ErrLeaseRejected`. A rejected git push reports no actual ref values.
- Drivers land through git, never through a forge merge or ref-update endpoint. Those check the PR head or ancestry, not the expected old value.
- `Land` refuses a `LandRequest` whose pinned PR heads do not match the PRs.
- OpenMerge needs permission to push to the protected base branch, for example as an always-allowed ruleset bypass on GitHub.

After a landing with merge commits, the forge marks the PRs merged because their heads are reachable. After squash or rebase it does not, so the planner emits `MarkLanded` for each PR. `Driver.MarkLanded` closes the PR with a link to the landed commit, or uses the forge's native mark-merged where one exists. It is idempotent and never merges again.

## The fence

Each base branch has a fence branch, `openmerge/fence/<base>`, derived from the base name and not separately configurable. Its tip is a small commit whose message names an attempt and its purpose:

- `openmerge fence land/<attempt> tested=<sha>`, minted by the landing push;
- `openmerge fence abandon/<attempt>`, minted when OpenMerge gives up on an attempt.

`EnsureFence` runs at setup. It creates the first fence commit with a create-only push, reading back a concurrent winner, or returns the existing fence unchanged.

The `openmerge/fence/*` namespace must be protected so that only OpenMerge can update it and no one can delete, recreate, or reset it. Otherwise a writer could restore an old token and revive an abandoned landing. Setup validates this protection before the queue starts.

### Attempts

Before any push, the executor mints a random attempt ID of at least 128 bits and durably records `LandingStarted`. Batch IDs and action keys are never attempt IDs, because they can repeat after a fresh install. Once `LandingStarted` is recorded, the batch can only be resolved, never landed again.

### Resolving an attempt

A rejected landing push, a crash, and a timeout are all resolved the same way, by reading the fence:

1. The fence names `land/<attempt>` with the attempt's tested SHA: the landing happened. Record `Landed`.
2. The fence names `abandon/<attempt>`: an earlier resolution already abandoned it. Record `LandingAbandoned`.
3. The fence is still the attempt's starting token: advance it to `abandon/<attempt>` with a leased push, then record `LandingAbandoned`. Any delayed copy of the landing push now fails its fence lease. If this lease is rejected, read the fence and decide once more; the landing may have just won.
4. Anything else, including `land/<attempt>` with a different tested SHA, returns `ErrFenceTampered` without settling the attempt. It means the fence protection above is missing.

Only `Landed` or `LandingAbandoned` settles an attempt. A deadline decides when to resolve, never the outcome.

## Recoverable writes

Refs, statuses, and check runs are idempotent by name or external ID. Comments embed the action's idempotency key in a hidden marker. Before writing, the driver searches for an existing comment carrying that key **authored by its own identity**; a marker copied into someone else's comment must not suppress the write. Retrying any write after a crash converges to exactly one effect.

## Running the contract suite

`forgetest.Run(t, newEnv)` runs every rule above that can be checked. The environment provides:

- a fresh, uniquely named base and fence per subtest, created by ordinary pushes, so the suite never rewrites a protected ref;
- PRs that each add a unique file, a pair of PRs whose changes conflict, a fork PR, and approvals from a writer and a non-writer;
- `ContainsPR(sha, pr)` to check that a commit contains a PR's changes.

Scenarios that only the fake can stage, such as rolling a branch back, are gated behind an environment capability and skipped with a message elsewhere. The fake runs the whole suite in every CI build. Real drivers run it against a sandbox repository when credentials are present, and skip with a message otherwise.
