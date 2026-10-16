# 0004. Access forges only through a narrow driver interface, with a fake forge from day one

- **Status:** Accepted
- **Date:** 2026-10-15

## Context

The roadmap promises GitHub first, then GitLab, Forgejo/Gitea, and GitHub Enterprise Server. Prior art shows two traps:

- **Forge logic leaking into queue logic.** bors-ng was deprecated partly because of bugs it could not fix from outside GitHub, and Mergify, Trunk, Aviator, and Graphite are all GitHub-only. Portability retrofitted later rarely happens.
- **Tests nobody else can run.** Mergify cited functional tests that needed undocumented GitHub setup as a reason it closed its engine. LUCI Change Verifier, by contrast, ships an end-to-end suite against a fake Gerrit.

Forges also differ in ways the engine must know about. Some have check runs and others only commit statuses. Rules for force-pushing protected branches differ. GitHub has a native merge queue and Forgejo has none.

## Decision

**The engine never imports a forge SDK.** Everything outside `internal/forge/<name>` talks to forges through the current Phase 1 interfaces below. Webhook parsing, branch rules, labels, and capabilities will be added when their consumers arrive. `RepoRef` contains one opaque `Path string` (for example, `owner/name` or `group/subgroup/name`), so a driver cannot assume an owner/name split.

```go
type Driver interface {
 PullRequest(ctx context.Context, repo RepoRef, number int) (PullRequest, error)
 CheckRuns(ctx context.Context, repo RepoRef, sha string) ([]CheckRun, error)
 DeleteRef(ctx context.Context, repo RepoRef, ref string) error
 ReportStatus(ctx context.Context, repo RepoRef, pr int, headSHA string, s Status) error
 MarkLanded(ctx context.Context, repo RepoRef, pr int, landedSHA string) error
 Comment(ctx context.Context, repo RepoRef, pr int, key, body string) error
}

type Git interface {
 BuildTestRef(ctx context.Context, repo RepoRef, req BuildRequest) (testedSHA string, err error)
 Land(ctx context.Context, repo RepoRef, req LandRequest) (newFenceSHA string, err error)
 EnsureFence(ctx context.Context, repo RepoRef, fenceRef string) (sha string, err error)
 AdvanceFence(ctx context.Context, repo RepoRef, fenceRef, expectedSHA, nextID string) (newSHA string, err error)
 ReadFence(ctx context.Context, repo RepoRef, fenceRef string) (sha, id, testedSHA string, err error)
 ReadRef(ctx context.Context, repo RepoRef, ref string) (sha string, err error)
 IsAncestor(ctx context.Context, repo RepoRef, ancestor, descendant string) (bool, error)
}
```

**Statuses refer to a PR head commit.** `Status` has a normalized `State`, a stable machine-readable `Reason`, and a human-readable `Summary`. The states are `queued`, `testing`, `landing`, `merged`, `dequeued`, `blocked`, and `paused`. Drivers upsert their OpenMerge check on `(repo, PR, headSHA)`, mapping queued/testing/landing to pending or in-progress, merged to success, dequeued to cancelled, blocked to failure, and paused to action-required. Commit-status APIs without cancelled/action-required conclusions use failure for those outcomes and retain the normalized state and reason in their description. A reason such as `untrusted`, `head-changed`, or `build-failed` is never used as the forge conclusion. Queue status and dequeue actions retain the admitted/observed head even if a newer PR head arrives before the effect executes.

**Check aggregation uses the newest run of each name.** A `CheckRun` has a monotonically increasing `ID` per check name, a `Name`, and a normalized `State`: pending, success, failure, cancelled, skipped, or neutral. Drivers normalize native run identifiers into this ordering. A newer pending rerun supersedes an older failure or success, regardless of response order. Cancelled is terminal failure until superseded. Skipped and neutral are terminal failure by default; the executor's explicit `AllowSkippedNeutral` option permits them as success. Missing or unknown required check states remain pending. Empty `RequiredChecks` is a configuration error unless `AllowNoRequiredChecks` is explicitly enabled.

**Normalized events.** Drivers translate forge payloads into a small set of engine events (pull request changed, check completed, review submitted, command comment, base branch moved, config changed). Unknown payloads are dropped with a debug log, never passed through.

**Capabilities are explicit.** Each driver declares what it supports: check runs vs commit statuses, fast-forward landing, a native merge queue, labels, reactions. The planner reads capabilities instead of branching on forge names.

**Test refs are built with git, not forge merge APIs.** A shared git component fetches the base and the queued PRs, composes the speculative commits locally, and pushes them to `openmerge/queue/...` refs over the driver's `GitRemote`. The top commit of every test ref carries the batch ID, so a tested SHA identifies exactly one batch. This works the same on every forge. The host running OpenMerge needs the `git` executable, which the container image includes.

**Landing is a compare-and-swap on the base branch.** `LandRequest` carries the PR numbers and pinned heads, the expected base SHA, the tested commit SHA, and the base branch's current fence token (below). The build result durably records the actual merge method in the batch. A driver must make the base check atomic with the write: a preflight read followed by a merge call is not enough, because the base can move in between.

The test ref is built in the shape the merge method asks for (merge commits, squashed commits, or rebased commits on top of the base), and landing updates the base branch to exactly that commit with an atomic git push that asserts the base's current value and the fence token together (`git push --atomic --force-with-lease=<base>:<expected-sha> --force-with-lease=<fence>:<token> <remote> <tested-sha>:<base> <next-token>:<fence>`). The forge applies the update only if the base still points at the expected SHA, so a base that moved forward, was rolled back, or was rewritten all cause a refusal. Forge REST endpoints that update refs check ancestry at best, not the expected old value, so drivers land through git, not those endpoints. What lands is byte-for-byte what was tested. This needs OpenMerge to be allowed to push to the protected branch, for example as a ruleset bypass actor on GitHub. With merge commits, forges mark the PRs merged automatically. With squash or rebase the PR heads are not in the base history. Applying `Landed` therefore records a pending `MarkLanded{PR, HeadSHA, LandedSHA}` effect per PR, emitted by the planner until acknowledged with `ActionCompleted`. `Driver.MarkLanded` closes each PR with a link to the commit that landed it, or uses the forge's native way to mark it merged where one exists; retries must converge without another git merge. Merge-method landings need no closing effect because the heads are reachable.

**A fence ref makes abandoned landings impossible to complete late.** Each base branch has a fence branch, `openmerge/fence/<base>`, pointing at a small commit that serves as a token. The executor derives this ref from its base configuration; it is not independently configurable. Setup calls `EnsureFence` to create an initial commit with a create-only push (a concurrent winner is read back), or returns the existing fence without changing it. Every landing push moves the fence to a new token in the same atomic push that moves the base, and leases on the old token. Before any push, the executor generates a random attempt ID with at least 128 bits from `crypto/rand` and durably records `LandingStarted`. Action keys and batch counters are not attempt IDs: they may repeat in a fresh queue generation. Once recorded, that batch can execute only `ResolveLanding`, never another `Land`. Fence commits record the attempt and purpose: `openmerge fence land/<attempt> tested=<sha>` or `openmerge fence abandon/<attempt>`. Recovery accepts a landed fence only when both the attempt ID and tested SHA match, independently of the mutable base. To abandon a landing whose outcome is uncertain (the worker crashed, or the push timed out), OpenMerge advances an unchanged fence with its own leased push; if the lease is rejected, it reads the fence again to learn which outcome won. Any delayed copy of an abandoned push now fails its fence lease. The forge enforces this, so the outcome is definitive without trusting clocks or deadlines. A rejected landing push returns only `ErrLeaseRejected`, without actual ref values. Rejection uses exactly the same resolution as an uncertain push: read the fence, accept matching landed/abandoned evidence, or lease the unchanged attempt token to `abandon/<attempt>`. On lease loss, read and decide once more. Any other fence move or mismatched landed tested SHA returns `ErrFenceTampered` without settling the attempt. There is no queue rejection event: only a recorded `LandingAbandoned` permits rebuilding or acknowledging a pending dequeue. Base observations are reconciled separately. Tokens are never reused, and the `openmerge/fence/*` namespace must be protected so that only OpenMerge can update it, and no one can delete, recreate, or reset it (a ruleset on GitHub). Otherwise a writer could restore an old token and revive an abandoned landing. Setup validates this protection before the queue starts.

Drivers must support atomic multi-ref pushes with per-ref leases. Landing through a forge's merge API is not allowed: on GitHub, for example, the merge endpoint's `sha` guards the PR head, not the base, and "require branches to be up to date" does not stop a merge after the base is rolled back. A configuration that needs a landing the driver cannot make atomic fails validation. OpenMerge never falls back to an unchecked merge.

**Writes are recoverable after a crash.** Refs, check runs, and labels are idempotent by nature, keyed by name or external ID. Comments and other append-only writes embed the action's idempotency key in a hidden marker, and before retrying, the driver searches for an existing write carrying that key **authored by its own authenticated identity**. A marker copied into a contributor's comment must not suppress the bot's write. The contract suite crashes a driver between each write and its recording and checks that a retry converges to exactly one effect.

**Test refs never run untrusted code with trusted privileges.** Pushing a test ref into the base repository runs CI with that repository's privileges, often including secrets. A pull request from a fork, or from an author without write access, could change a build script and read those secrets before anyone merges it. So, independent of any `enter_when` conditions, OpenMerge will not put a PR into a test ref unless one of two things holds. Either its head branch lives in the base repository, where only people with write access can push, or its current head SHA is approved by someone with write access. Who opened the PR does not matter: a PR from a fork needs an exact-head approval even when a maintainer opened it, because others may be able to push to that fork. A new push revokes the approval for this purpose. Draft-PR mode does not avoid this, because a PR from a branch in the base repository runs with the same privileges. Repositories should still scope deployment secrets to protected environments that queue refs cannot reach.

**The fake forge ships first.** `internal/forge/fake` is an in-memory driver with file snapshots, parent edges, per-PR commit lists, authored comments, and scriptable check snapshots/test-ref callbacks. Rebase replays each PR commit separately; squash combines changes without preserving the PR heads as ancestors. A build verifies each requested head is the PR's actual head and never substitutes a different PR's commit. The executor accepts an injected clock and ID generator for deterministic tests. Richer CI scheduling and throughput simulations remain roadmap work.

**Every driver passes the same contract suite.** `forgetest.Run(t, newEnv)` exercises each interface method's required semantics, including the landing refusals above. The environment supplies PRs adding unique files, a conflicting PR pair created with ordinary repository changes, expected fork/same-repository observations and approvals, and a `ContainsPR(sha, pr)` content inspector. The suite uses `IsAncestor` to pin base/head ancestry for each merge method, checks preserved content, races landing against fence advancement repeatedly, tests concurrent fence bootstrap, and verifies commit-scoped statuses, PR closing, and authored comment dedupe. No synthetic conflict toggle is required. The fake forge runs it in every CI build. Real drivers run it against a sandbox organization when credentials are present and skip with a message otherwise.

## Consequences

- Adding a forge means implementing one interface and passing one suite, with no engine changes.
- Landing needs push access to the protected branch, for example as an always-allowed ruleset bypass for the GitHub App. Teams that cannot grant it can use the GitHub native merge queue adapter planned for Phase 5, where GitHub performs the landing.
- Fork contributions queue only after a maintainer approves the exact head being tested. This is slower for outside contributors and is intentional.
- Contributors can run the whole system, including end-to-end queue behavior, with no credentials.
- We take on a git dependency at runtime and must handle clone caching and disk cleanup. Mergify's self-hosted edition has the same requirement.
- The interface will grow. Additions go through review against the "narrow" rule: a method belongs in the driver only if the engine cannot express it with existing methods.
- GitHub's native merge queue support (Phase 5) becomes a driver capability plus an alternate executor path, not a fork of the engine.

## Alternatives considered

- **Call the GitHub SDK directly and abstract later.** Fastest to the first demo, and the path every GitHub-only queue took. Rejected because the abstraction would be shaped by GitHub's quirks and the tests would depend on GitHub.
- **Compose test commits with forge merge APIs** (Mergify's 2022 approach on GitHub). No local git needed, but GitLab and Forgejo lack equivalent endpoints, and API merges cost rate limit per PR per batch.
- **Record-and-replay HTTP fixtures instead of a fake forge.** Good for driver tests, which may still use them, but fixtures cannot express scripted CI outcomes over virtual time, which the planner simulation needs.
