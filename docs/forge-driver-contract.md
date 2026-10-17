# Forge driver contract

This is the reference for anyone writing or reviewing a forge driver. [ADR 0004](adr/0004-forge-driver-interface.md) records why the interface looks this way; this page records exactly what a driver must do. The Go interfaces live in [`internal/forge/forge.go`](../internal/forge/forge.go), [`internal/forge/forgetest`](../internal/forge/forgetest) checks the rules a driver is responsible for. Rules the executor applies on top of a driver, such as check aggregation and attempt resolution, are covered by the executor's tests instead.

## Repositories

`RepoRef` holds one opaque `Path` (`owner/name`, or `group/subgroup/name` on GitLab). Forge-neutral code must not assume an owner/name split; a concrete driver interprets and validates the path for its forge.

`ReadFile` reads a file at an explicit branch or commit. A missing file or ref is an error, not an empty configuration.

## Pull requests and approvals

`PullRequest` reports the PR number, current head SHA, target branch (`Base`), draft flag, title, body, head branch label, author login, numeric author ID,
and forge-provided commit email, whether the head branch lives in the base repository, whether the PR is closed, and its approvals.

GitHub returns `<id>+<login>@users.noreply.github.com` as `AuthorEmail` and
`<owner>:<head-branch>` as `HeadBranch` (falling back to the branch ref when the
label is absent). Other forges supply their equivalent commit identity; gitops
does not construct forge-specific email addresses. The fake supplies fixture
metadata on the same observation. Closed PRs retain commit metadata.

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

`CheckRun` has a `Name`, a stable nonempty `Source`, a normalized `State` (pending, success, failure, cancelled, skipped, neutral), and an `ID` that increases with each run of the same name **from the same source**. `Attempt` is one-based when the forge supplies it; zero means unknown for a legacy observation. It distinguishes repeated native IDs when known, and drivers must never invent attempts from response order. GitHub uses `app:<numeric-app-id>` for check runs and `status:<lowercase-creator-login>` for commit statuses. The fake preserves explicit sources and defaults unspecified test sources to `fake:ci`. IDs from different sources are never comparable.

The executor aggregates as follows:

- Drivers preserve the suite's triggering branch in `CheckRun.HeadBranch` (without `refs/heads/`), with empty meaning unknown. The executor applies the [queue CI provenance contract](operating.md#queue-ci-provenance) for every method before either aggregation policy. `status:` sources identify commit statuses, which lack branch provenance and remain accepted under the trust requirements documented there.
- A required name reported by more than one source is ambiguous and stays pending, even if every source reports success. A missing source also stays pending. The executor emits `ChecksReopened` followed by `ChecksRefreshed`, emits no terminal `ChecksCompleted`, and returns `executor.CheckSourceAmbiguityError` with the name and sorted sources for the server to surface. Unrequired names do not block the batch.
- Within one source, the newest run of each name wins, regardless of response order. A pending rerun supersedes an older result.
- A matching GitHub check run whose suite is not completed is pending even if the run still reports an old success. Missing suite observations have unknown provenance and are ignored; unknown producer identities stay pending when provenance matches. Drivers must refresh suite state and branch as well as run state. GitHub `check_suite` requested/rerequested/completed and `check_run` rerequested/completed webhooks normalize to `ChecksChanged{SHA}` and trigger these reads.
- Cancelled counts as failure until a newer run supersedes it.
- Skipped and neutral count as failure unless the `AllowSkippedNeutral` option is set.
- A required check with no run, or an unknown state, is pending.
- An empty list of required checks is a configuration error unless `AllowNoRequiredChecks` is set.

`CheckRerunner` is an optional capability. `RerunFailedChecks` receives the
tested SHA, queue test ref, exact failed `Runs`, and an idempotency `Key`. A
driver may rerun only those identities on that SHA/ref/source; it must return
`Unsupported: true` rather than wedge a queue when the producer, state, native
ID, or permission cannot support a safe rerun. A successful request only means
that a new pending observation may appear; callers must refresh `CheckRuns`.
Repeated keys are idempotent, including after a process-cache loss: the driver
checks the newest same-name/source identity before dispatching and never
replays an older failed observation over a newer pending or completed run.

For retry-off queues, `ChecksCompleted{TestedSHA, Success}`, `ChecksReopened{TestedSHA, At}`, and `ChecksRefreshed{BatchID, TestedSHA, At}` fold in event-log order, including retained observations received before the build result. Every reopen for the active tested SHA clears recorded success, increments the batch's checks generation, and invalidates timeout evidence. It preserves the original test-start time and timeout deadline, so repeated reruns cannot postpone timeout indefinitely. Every matching refresh advances the action epoch and records its own read time, even if that time precedes another worker's timestamp. A timeout requires a refresh read at or after the original deadline, with no reopen applied since it. A delayed pending observation can cost another read; a delayed pass may restore projected success. A delayed older terminal failure after a passing rerun still fails the batch conservatively. Worker clocks affect timeout liveness, never landing authorization. The stateless executor records a reopen on every non-terminal refresh, even without a prior pass, so revocation survives a failure to append the following refresh.

Retry-enabled queues instead record identified `ChecksObserved` results and a
single `CheckRerunRequested` before dispatch. Original run duplicates cannot
confirm a rerun failure. Reopening retains the consumed budget and both recorded
deadlines; only a newer same-producer attempt can pass or fail the rerun.
`ChecksFailureConfirmed` makes failure final, including unsupported reruns and
qualifying timeouts. Their landing preflight uses the same identified observation
path and requires a newer identity for every originally failed check. See
[ADR 0009](adr/0009-speculative-batching.md#confirmed-failures).

Immediately before `LandingStarted`, the executor freshly reads `CheckRuns` for the tested SHA and requires terminal success. Pending or ambiguous results emit `ChecksReopened` (or retry-aware `ChecksObserved`) followed by `ChecksRefreshed` stamped at read time; this read qualifies for timeout at or after the original deadline. A passing refresh after the deadline may still land if its preflight also passes. Terminal failure emits `ChecksCompleted{Success:false}` for retry-off queues, or the identified `ChecksObserved` for retry-enabled queues. Both decline `Land` with an error before minting an attempt or pushing. The action remains unacknowledged for replanning after a later passing rerun. A read error also prevents starting the landing. `Land` pins the checks generation in its key and payload. The store re-plans under the queue lock at `BeginConsequential` and `LandingStarted`; any intervening reopen invalidates the old action even if a delayed pass restores projected success. A new action at the new generation must perform its own preflight. The preflight cannot revoke an already-started fenced attempt or prevent a later rerun whose reopen has not committed.

## Building test refs

`Driver.DefaultBranch` freshly observes the repository's default branch and
errors if it is missing or unreadable. Configuration authority never falls back
to the queue's base. The server resolves the observed default branch to a commit
with `Git.ReadRef`, then pins every config path read to that commit.
`Driver.ReadFile` identifies an absent file with `forge.ErrFileNotFound` so
default-branch configuration discovery can distinguish absence from a failed read.

`BuildTestRef` composes the test commit locally with git and pushes it to `openmerge/queue/...`. It must:

- refuse a request whose pinned head is not the PR's current head;
- produce the shape the merge method asks for: merge commits that keep each PR head as an ancestor, a single squashed commit per PR, each PR commit replayed separately for rebase, or one empty marker above the final head of a strictly increasing ancestry chain for fast-forward;
- for generated commits, capture each PR's current title/body, head label and
  commit identity at build time, rejecting metadata whose number/head differs from the pin, or whose
  author identity/title is missing or invalid;
- squash to `<title> (#<number>)` plus the trimmed PR body, removing
  `<!-- openmerge:... -->` marker comments and reserved metadata; merge to
  `Merge pull request #<number> from <head>` followed by the PR title;
- author squash/merge commits as the PR author; collect distinct other original
  commit authors and existing `Co-authored-by` trailers, and every distinct
  `Signed-off-by` trailer, in one trailer block (not sign-off-like prose);
- replay rebase commits with their original author name, email and author date,
  retaining their message/trailers and adding `OpenMerge-Replayed-From`;
- use the configured committer on all generated commits;
- put the batch ID in the top commit's trailer block only, so a tested SHA
  identifies exactly one batch (including the fast-forward marker); every earlier squash/merge still carries its
  own PR metadata;
- return `ErrConflict` for merge conflicts or rebase histories containing PR merge commits; return `ErrNotFastForward` when a fast-forward head does not strictly extend its predecessor/base; use other errors for infrastructure failures.

For fast-forward, the executor records `FastForwardBatchSelected` before the push,
choosing the longest consecutive linear prefix and retaining the remaining PRs in
queue order. A non-descendant first head is dequeued as `fast-forward-required`;
the author must update and requeue it. No rebase or attribution rewrite occurs.
Before publishing, `LinearHistoryReader` checks whether either readable base
protection source requires linear history. GitHub reads classic protection and
the effective active branch rules, including inherited rulesets. When required,
`Git.HasMergeCommits` inspects all new history (`base..head`, all parent edges).
An offending selected head is dequeued as `merge-commits-not-allowed` through
the new `FastForwardMergeCommitsBlocked` event; other heads keep queue order.
Unknown requirements preserve existing behavior; graph read errors are retryable
build failures. Existing event/action formats remain unchanged.
Fast-forward tests the exact head tree and history via a unique marker commit;
it lands the head itself. `FastForwardRefBuilt` records the marker and pinned head.
The marker has the head as its only parent and the same tree, uses the configured
App committer, and carries only queue-authored text with a random 128-bit nonce
and the authoritative batch trailer. Every build, including retries with the
same ref, batch ID and clock, mints a fresh marker.

Before `LandingStarted` and again inside `Git.Land`, `VerifyFastForward` requires
the marker's only parent to be the pinned final head, the marker tree to equal
the head tree, and the head to strictly descend from the leased base. Landing
uses the existing atomic base/fence leases, pushes the head to base, and records
`tested=<marker>` in the unchanged strict fence form. The attempt captures the
landing head; the additive `FastForwardResolveLanding` action persists that head
and the tested marker independently of current configuration. Successful landing,
recovery, base-membership verification and head cleanup use the head as landed
SHA. Other methods retain their exact-tested-SHA behavior.

Batch identity is OpenMerge's own format, independent of Git trailer parsing:
the final line is exactly `OpenMerge-Batch: <id>` with an ID matching
`[A-Za-z0-9._-]+`, and no other line starts with `OpenMerge-Batch`
(case-insensitive after trimming leading whitespace); `ParseBatchMessage` rejects
anything else, normalizing CRLF and allowing only a single terminating LF.
Reserved lines and their folded continuations are removed from contributor
metadata; PR bodies also cannot supply sign-offs or coauthors. Contributor lines
starting with `---` become `- - -` in generated prose and replayed messages so
Git's patch-divider handling cannot hide the authoritative attribution block.

The CLI git transport receives the metadata reader as `gitops.Config.PullRequest`.
`serve` always supplies the installation driver's reader. Standalone Git-only
fixtures may omit it and use generic titles and `Config.Author`; that fallback
is not the production attribution contract. The fake models commit graph/content
and supplies PR metadata; actual Git identity/trailer behavior is verified with
local bare repositories and the GitHub HTTP-fixture server integration test.

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

After a landing with merge commits or fast-forward, the forge marks the PRs merged because their heads are reachable. After squash or rebase it does not, so the planner emits `MarkLanded` for each PR. `Driver.MarkLanded` closes the PR with a link to the landed commit, or uses the forge's native mark-merged where one exists. It is idempotent and never merges again. Immediately before calling `Driver.MarkLanded`, the executor freshly reads the base and verifies that the landed commit equals the tip or is its ancestor. An absent commit returns `executor.ErrLandingNotOnBase` without calling the driver, closing the PR, or acknowledging the action. `MarkLanded` carries no attempt ID, so that error's attempt ID is empty. These checks detect external rollbacks observed at verification; they cannot prevent subsequent external rewrites.

## The fence

Each base branch has a fence branch, `openmerge/fence/<base>`, derived from the base name and not separately configurable. Its tip is a small commit whose message names an attempt and its purpose:

- `openmerge fence land/<attempt> tested=<sha> nonce=<random-hex>`, minted by the landing push;
- `openmerge fence abandon/<attempt> nonce=<random-hex>`, minted when OpenMerge gives up on an attempt.

`ReadFence` accepts only these exact one-line formats, with one space between tokens, a nonempty single-token attempt ID, a tested SHA of 40 or 64 hex characters, and a nonce of exactly 32 hex characters. A terminal newline is allowed. Each key occurs exactly once in the shown order; extra tokens, multiline messages, invalid hex, and all other formats return the fence SHA with empty identity and tested-SHA fields. A changed unrecognized fence is treated as tampered by the executor. Bootstrap fences also have empty identity fields. The fake uses the same parser and must mint unique fences too.

`EnsureFence` runs at setup. It creates the first fence commit with a create-only push, reading back a concurrent winner, or returns the existing fence unchanged.

The `openmerge/fence/*` namespace must be protected so that only OpenMerge can update it and no one can delete, recreate, or reset it. Otherwise a writer could restore an old token and revive an abandoned landing. `openmerge serve` currently leaves validation of this protection to the operator; automated ruleset validation is a follow-up.

### Attempts

Before any push, the executor mints a random attempt ID of at least 128 bits and durably records `LandingStarted`. Batch IDs and action keys are never attempt IDs, because they can repeat after a fresh install. Once `LandingStarted` is recorded, the batch can only be resolved, never landed again.

### Resolving an attempt

A rejected landing push, a crash, and a timeout are all resolved the same way, by reading the fence:

1. The fence names `land/<attempt>` with the attempt's tested SHA: freshly read the base and require the landed commit (the pinned marker parent for a fast-forward attempt, otherwise the tested commit) to equal its tip or be its ancestor. Only then record `Landed`. Otherwise return `executor.ErrLandingNotOnBase` without an event, leaving the batch in `Landing` and reporting the same error on each retry while the commit is absent.
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


The [operating guide](operating.md#landed-head-cleanup) defines the landed-head
cleanup protocol, including `Driver.HeadBranchForDeletion` and `Git.DeleteBranch`.
