# Forge driver contract

This is the reference for anyone writing or reviewing a forge driver. [ADR 0004](adr/0004-forge-driver-interface.md) records why the interface looks this way; this page records exactly what a driver must do. The Go interfaces live in [`internal/forge/forge.go`](../internal/forge/forge.go), [`internal/forge/forgetest`](../internal/forge/forgetest) checks the rules a driver is responsible for. Rules the executor applies on top of a driver, such as check aggregation and attempt resolution, are covered by the executor's tests instead.

## Repositories

`RepoRef` holds one opaque `Path` (`owner/name`, or `group/subgroup/name` on GitLab). Forge-neutral code must not assume an owner/name split; a concrete driver interprets and validates the path for its forge.

`ReadFile` reads a file at an explicit branch or commit. A missing file or ref is an error, not an empty configuration.

## Pull requests and approvals

`PullRequest` reports the PR number, current head SHA, target branch (`Base`), draft flag, author login, whether the head branch lives in the base repository, whether the PR is closed, and its approvals.

Each approval reports the approver, a `CanWrite` trust decision, and **the exact head SHA that was approved**. For a current-head fork approval, the driver freshly reads write permission on every observation. Conditional HTTP revalidation is allowed; a locally cached grant without contacting the forge is not. Stale approvals and approvals on same-repository PRs have `CanWrite: false`, without permission reads. Closed PRs may omit approvals and must not trigger review or permission reads. A stale approval, given before a later push, keeps the old SHA. The planner's trust gate depends on this ([ADR 0004](adr/0004-forge-driver-interface.md), "Test refs never run untrusted code").

Some forges, such as GitLab, do not record which commit an approval was for. A driver for such a forge may report approvals only when the forge is configured to reset approvals on every push, and must verify that setting. Otherwise it reports no approvals, which fails closed: fork PRs cannot enter the queue. The driver's documentation states which behavior it uses.

`ListOpenPullRequests(repo, base)` returns every open PR targeting `base`, with
number, head SHA, and updated-at timestamp. Empty `base` lists all open PRs.
It is a paginated reconciliation read, without per-PR review/permission lookups.

`ListComments(repo, pr, since)` returns comment ID, author login, body, and
created-at time for human and bot issue-thread comments created at or after
`since` (inclusive); zero means all comments. Drivers follow every page. The server dedupes
commands by comment ID and must overlap time cursors to handle timestamp precision
and comments created during pagination. GitHub has a second-granular exclusive
updated-time filter; its driver queries from the preceding second, then filters
inclusively by creation time. Boundary comments can therefore repeat, including
distinct comments created in the same second. Edits of old comments do not create new
commands. `CanWrite(repo, user)` freshly authorizes commands by current repository
permission; webhook author association is never sufficient.

For conditional HTTP reads, cache only a single syntactically valid strong or weak entity-tag ([RFC 9110 §8.8.3](https://www.rfc-editor.org/rfc/rfc9110.html#section-8.8.3)). Wildcards, lists, multiple field lines, and unquoted validators are invalid. A 304 must carry one valid ETag that matches the stored opaque-tag using weak comparison (ignore the optional `W/` prefix), as required for `If-None-Match` ([RFC 9110 §13.1.2](https://www.rfc-editor.org/rfc/rfc9110.html#section-13.1.2)). Missing, malformed, or nonmatching validators must evict the entry and fail closed or refetch unconditionally once; they must never replay the stored body. These entries do not qualify for validatorless reuse. Persist allowed 304 header updates ([RFC 9111 §4.3.4](https://www.rfc-editor.org/rfc/rfc9111.html#section-4.3.4)); `no-store` or `Vary: *` on a 200 or 304 prevents caching and replay. Inspect every field line and comma-separated value case-insensitively.

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

`CheckRun` has a `Name`, a stable nonempty `Source`, a normalized `State` (pending, success, failure, cancelled, skipped, neutral), and an `ID` that increases with each run of the same name **from the same source**. Drivers translate native run identifiers into that ordering. GitHub uses `app:<numeric-app-id>` for check runs and `status:<lowercase-creator-login>` for commit statuses. The fake preserves explicit sources and defaults unspecified test sources to `fake:ci`. IDs from different sources are never comparable.

The executor aggregates as follows:

- A required name reported by more than one source is ambiguous and stays pending, even if every source reports success. A missing source also stays pending. The executor emits `ChecksReopened` followed by `ChecksRefreshed`, emits no terminal `ChecksCompleted`, and returns `executor.CheckSourceAmbiguityError` with the name and sorted sources for the server to surface. Unrequired names do not block the batch.
- Within one source, the newest run of each name wins, regardless of response order. A pending rerun supersedes an older result.
- A GitHub check run whose suite is not completed is pending even if the run still reports an old success. Missing suite observations and unknown producer identities also fail safe as pending. Drivers must refresh suite state as well as run state. GitHub `check_suite` requested/rerequested/completed and `check_run` rerequested/completed webhooks normalize to `ChecksChanged{SHA}` and trigger these reads.
- Cancelled counts as failure until a newer run supersedes it.
- Skipped and neutral count as failure unless the `AllowSkippedNeutral` option is set.
- A required check with no run, or an unknown state, is pending.
- An empty list of required checks is a configuration error unless `AllowNoRequiredChecks` is set.

`ChecksCompleted{TestedSHA, Success, At}`, `ChecksReopened{TestedSHA, At}`, and `ChecksRefreshed{BatchID, TestedSHA, At}` carry observation times; events from the same read use the same time. For the active testing batch, the projection ignores observations older than the newest applied evidence for that tested SHA, with equal times following event-log order. Retained observations received before the build result use the same ordering. An accepted reopen clears recorded success and supersedes any retained terminal result for that SHA. It preserves the original test-start time and timeout deadline, so repeated reruns cannot postpone timeout indefinitely. A delayed reopen cannot discard a newer pass or reuse its refresh timestamp for timeout; a delayed old completion cannot restore success after a newer reopen. The stateless executor records a reopen on every non-terminal refresh, even without a prior pass, to establish the observation time durably before recording the refresh. Suppressing it based on a previously planned projection would race other observations and leave an append-failure gap. Immediately before `LandingStarted`, the executor freshly reads `CheckRuns` for the tested SHA and requires terminal success. Pending or ambiguous results emit `ChecksReopened`; terminal failure emits `ChecksCompleted{Success:false}`. Both decline `Land` with an error before minting an attempt or pushing. The action remains unacknowledged for replanning after a later passing rerun. A read error also prevents starting the landing. This preflight cannot prevent CI from reopening after the read or revoke an already-started fenced attempt.

## Building test refs

`BuildTestRef` composes the test commit locally with git and pushes it to `openmerge/queue/...`. It must:

- refuse a request whose pinned head is not the PR's current head;
- produce the shape the merge method asks for: merge commits that keep each PR head as an ancestor, a single squashed commit per PR, or each PR commit replayed separately for rebase;
- put the batch ID in the top commit, so a tested SHA identifies exactly one batch;
- return `ErrConflict` for merge conflicts or rebase histories containing PR merge commits, and any other error for infrastructure failures.

## Landing

`Land` updates the base branch to exactly the tested commit with one atomic git push that leases both refs:

```sh
git push --atomic \
  --force-with-lease=<base>:<expected-base-sha> \
  --force-with-lease=<fence>:<fence-sha> \
  <remote> <tested-sha>:<base> <new-fence-commit>:<fence>
```

- The forge applies the push only if both refs still hold the expected values. A base that moved forward, was rolled back, or was rewritten is refused.
- Every landing push changes the fence. Mint a fresh fence nonce with at least 128 bits from `crypto/rand` for each push, including copies of the same attempt. Git skips leases on up-to-date refs; unique fences ensure that a duplicate cannot skip its consumed fence lease after a base rollback.
- If preflight sees the base already equal to the tested SHA, return `ErrLeaseRejected` without pushing, even if it also equals the expected base. The executor settles the attempt through the fence.
- The CLI transport's [skipped-lease notes](../internal/gitops/README.md#skipped-leases-and-external-rollbacks) describe a residual external-writer case: pushing exactly the tested commit and rolling it back inside the push window can advance only the fence. The executor detects the missing tested commit with a fresh base read and reachability check before recording `Landed`, in both normal completion and recovery. It returns `executor.ErrLandingNotOnBase` with the tested SHA, base tip, and attempt ID, emits no result event, and leaves the batch in `Landing`. Every resolution retry repeats the check and returns the error while the commit is absent.
- On refusal the driver returns only `ErrLeaseRejected`. A rejected git push reports no actual ref values.
- Drivers land through git, never through a forge merge or ref-update endpoint. Those check the PR head or ancestry, not the expected old value.
- `Land` refuses a `LandRequest` whose pinned PR heads do not match the PRs.
- A refused build or landing leaves every ref and PR unchanged.
- OpenMerge needs permission to push to the protected base branch, for example as an always-allowed ruleset bypass on GitHub.

After a landing with merge commits, the forge marks the PRs merged because their heads are reachable. After squash or rebase it does not, so the planner emits `MarkLanded` for each PR. `Driver.MarkLanded` closes the PR with a link to the landed commit, or uses the forge's native mark-merged where one exists. It is idempotent and never merges again. Immediately before calling `Driver.MarkLanded`, the executor freshly reads the base and verifies that the landed commit equals the tip or is its ancestor. An absent commit returns `executor.ErrLandingNotOnBase` without calling the driver, closing the PR, or acknowledging the action. `MarkLanded` carries no attempt ID, so that error's attempt ID is empty. These checks detect external rollbacks observed at verification; they cannot prevent subsequent external rewrites.

## The fence

Each base branch has a fence branch, `openmerge/fence/<base>`, derived from the base name and not separately configurable. Its tip is a small commit whose message names an attempt and its purpose:

- `openmerge fence land/<attempt> tested=<sha> nonce=<random-hex>`, minted by the landing push;
- `openmerge fence abandon/<attempt> nonce=<random-hex>`, minted when OpenMerge gives up on an attempt.

`ReadFence` accepts only these exact one-line formats, with one space between tokens, a nonempty single-token attempt ID, a tested SHA of 40 or 64 hex characters, and a nonce of exactly 32 hex characters. A terminal newline is allowed. Each key occurs exactly once in the shown order; extra tokens, multiline messages, invalid hex, and all other formats return the fence SHA with empty identity and tested-SHA fields. A changed unrecognized fence is treated as tampered by the executor. Bootstrap fences also have empty identity fields. The fake uses the same parser and must mint unique fences too.

`EnsureFence` runs at setup. It creates the first fence commit with a create-only push, reading back a concurrent winner, or returns the existing fence unchanged.

The `openmerge/fence/*` namespace must be protected so that only OpenMerge can update it and no one can delete, recreate, or reset it. Otherwise a writer could restore an old token and revive an abandoned landing. `openmerge serve` will validate this protection at startup; until then it is an operator requirement.

### Attempts

Before any push, the executor mints a random attempt ID of at least 128 bits and durably records `LandingStarted`. Batch IDs and action keys are never attempt IDs, because they can repeat after a fresh install. Once `LandingStarted` is recorded, the batch can only be resolved, never landed again.

### Resolving an attempt

A rejected landing push, a crash, and a timeout are all resolved the same way, by reading the fence:

1. The fence names `land/<attempt>` with the attempt's tested SHA: freshly read the base and require the tested commit to equal its tip or be its ancestor. Only then record `Landed`. Otherwise return `executor.ErrLandingNotOnBase` without an event, leaving the batch in `Landing` and reporting the same error on each retry while the commit is absent.
2. The fence names `abandon/<attempt>`: an earlier resolution already abandoned it. Record `LandingAbandoned`.
3. The fence is still the attempt's starting token: advance it to `abandon/<attempt>` with a leased push, then record `LandingAbandoned`. Any delayed copy of the landing push now fails its fence lease. If this lease is rejected, read the fence and decide once more; the landing may have just won.
4. Anything else, including `land/<attempt>` with a different tested SHA, returns `ErrFenceTampered` without settling the attempt. It means the fence protection above is missing.

Only `Landed` or `LandingAbandoned` settles an attempt. A deadline decides when to resolve, never the outcome.

## Recoverable writes

Refs, statuses, and check runs are idempotent by name or external ID. Comments embed the action's idempotency key in a hidden marker. Before writing, the driver searches for an existing comment carrying that key **authored by its own identity**; a marker copied into someone else's comment must not suppress the write. Retrying any write after a crash converges to exactly one effect.

## Running the contract suite

`forgetest.Run(t, newEnv)` runs the driver rules above. It checks the normalized status round trip, not each forge's native conclusion mapping, which each driver documents.

`forgetest.RunDriver(t, newDriverEnv)` runs the API-side contracts alone against
an API sandbox or HTTP fixture. Its environment needs Driver, Repo, PRs,
Observations, BaseSHA, NewBase (a disposable ref), RefExists, and the API setup
and observation hooks below; it does not need Git, fences, or conflicting PRs.
`Run` retains the Git/API integration scenarios, including deleting a built test
ref and closing PRs after squash/rebase landings.

`forgetest.RunGit(t, newGitEnv)` runs the Git-side rules alone, without a forge API
implementation or API fixture hooks. `internal/gitops` runs it against temporary
local bare repositories, including rollback scenarios, unique fence updates,
and refusal when preflight observes an already-tested base. The transport boundary
is documented alongside the Landing rules above.

The environment provides:

- a fresh, uniquely named base and fence per subtest, created by ordinary pushes, so the suite never rewrites a protected ref;
- PRs that each add a unique file, a pair of PRs whose changes conflict, a fork PR, and approvals from a writer and a non-writer;
- `ContainsPR(sha, pr)` to check that a commit contains a PR's changes.

Scenarios that only the fake can stage, such as rolling a branch back, are gated behind an environment capability and skipped with a message elsewhere. The fake runs the whole suite in every CI build. Real drivers run it against a sandbox repository when credentials are present, and skip with a message otherwise.
