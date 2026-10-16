# 0004. Access forges only through a narrow driver interface, with a fake forge from day one

- **Status:** Accepted
- **Date:** 2026-10-15

## Context

The roadmap promises GitHub first, then GitLab, Forgejo/Gitea, and GitHub Enterprise Server. Prior art shows two traps:

- **Forge logic leaking into queue logic.** bors-ng was deprecated partly because of bugs it could not fix from outside GitHub, and Mergify, Trunk, Aviator, and Graphite are all GitHub-only. Portability retrofitted later rarely happens.
- **Tests nobody else can run.** Mergify cited functional tests that needed undocumented GitHub setup as a reason it closed its engine. LUCI Change Verifier, by contrast, ships an end-to-end suite against a fake Gerrit.

Forges also differ in ways the engine must know about. Some have check runs and others only commit statuses. Rules for force-pushing protected branches differ. GitHub has a native merge queue and Forgejo has none.

## Decision

**The engine never imports a forge SDK.** Everything outside `internal/forge/<name>` talks to forges through two narrow interfaces: `Driver` for forge API reads and writes, and `Git` for building test refs and landing. Webhook parsing, branch rules, labels, and capabilities are added when their consumers arrive. The exact rules every driver must follow are in the [forge driver contract](../forge-driver-contract.md).

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

**Test refs are built with git, not forge merge APIs.** OpenMerge composes test commits locally and pushes them to `openmerge/queue/...`, so building works the same on every forge. The top commit of every test ref carries the batch ID, so a tested SHA identifies exactly one batch. The host needs the `git` executable, which the container image includes.

**Landing is a compare-and-swap on the base branch.** One atomic git push moves the base to the tested commit and the fence to a new token, leased on both, so a moved, rolled-back, or rewritten base is refused. What lands is byte-for-byte what was tested. Landing through a forge's merge API is not allowed: on GitHub, the merge endpoint's `sha` guards the PR head, not the base. A configuration that needs a landing the driver cannot make atomic fails validation.

**A fence ref makes abandoned landings impossible to complete late.** Each base branch has a protected fence branch whose commits record which attempt moved it and why (`land/<attempt> tested=<sha>` or `abandon/<attempt>`). Attempt IDs are random and recorded durably before any push. A rejected, crashed, or timed-out landing is resolved by reading the fence, advancing it to `abandon/<attempt>` if nothing moved, so any delayed copy of the landing push fails. The forge enforces the outcome, so it never depends on clocks or deadlines.

**Statuses and checks are normalized.** Statuses attach to a PR head commit and use a fixed set of states (queued, testing, landing, merged, dequeued, blocked, paused). Check runs are aggregated by the newest run of each name.

**Writes are recoverable after a crash.** Every write is idempotent by key. Comments carry a hidden marker, and a driver only trusts markers in comments it authored itself.

**Test refs never run untrusted code with trusted privileges.** Pushing a test ref runs CI with the base repository's privileges, often including secrets. So, independent of any `enter_when` conditions, OpenMerge will not put a PR into a test ref unless its head branch lives in the base repository, or someone with write access approved its current head SHA. Who opened the PR does not matter: a fork PR needs an exact-head approval even when a maintainer opened it, because others may be able to push to that fork. A new push revokes the approval. Repositories should still scope deployment secrets to protected environments that queue refs cannot reach.

**The fake forge ships first, and every driver passes the same contract suite.** `internal/forge/fake` is an in-memory driver used by all engine and end-to-end tests. `forgetest.Run` checks the rules a driver is responsible for. The fake runs it in every CI build, and real drivers run it against a sandbox repository when credentials are present.

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
