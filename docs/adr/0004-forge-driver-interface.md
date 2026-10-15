# 0004. Access forges only through a narrow driver interface, with a fake forge from day one

- **Status:** Accepted
- **Date:** 2026-10-15

## Context

The roadmap promises GitHub first, then GitLab, Forgejo/Gitea, and GitHub Enterprise Server. Prior art shows two traps:

- **Forge logic leaking into queue logic.** bors-ng was deprecated partly because of bugs it could not fix from outside GitHub, and Mergify, Trunk, Aviator, and Graphite are all GitHub-only. Portability retrofitted later rarely happens.
- **Tests nobody else can run.** Mergify cited functional tests that needed undocumented GitHub setup as a reason it closed its engine. LUCI Change Verifier, by contrast, ships an end-to-end suite against a fake Gerrit.

Forges also differ in ways the engine must know about. Some have check runs and others only commit statuses. Rules for force-pushing protected branches differ. GitHub has a native merge queue and Forgejo has none.

## Decision

**The engine never imports a forge SDK.** Everything outside `internal/forge/<name>` talks to forges through one Go interface. Its shape, illustrative and to be finalized in Phase 1:

```go
type Driver interface {
	Capabilities() Capabilities

	// Events: verify and normalize an inbound webhook.
	ParseWebhook(r *http.Request) ([]Event, error)

	// Reads, used by the reconciler and planner inputs.
	PullRequest(ctx context.Context, repo RepoRef, number int) (PullRequest, error)
	OpenPullRequests(ctx context.Context, repo RepoRef, base string) ([]PullRequest, error)
	CheckResults(ctx context.Context, repo RepoRef, sha string) ([]CheckResult, error)
	BranchRules(ctx context.Context, repo RepoRef, branch string) (BranchRules, error)
	ReadFile(ctx context.Context, repo RepoRef, ref, path string) ([]byte, error)

	// Git transport for building test refs.
	GitRemote(ctx context.Context, repo RepoRef) (GitRemote, error)

	// Writes. Each is idempotent under the caller's key.
	DeleteRef(ctx context.Context, repo RepoRef, ref string) error
	Land(ctx context.Context, repo RepoRef, req LandRequest) (LandResult, error)
	ReportStatus(ctx context.Context, repo RepoRef, sha string, s Status) error
	Comment(ctx context.Context, repo RepoRef, number int, body string) error
	SetLabels(ctx context.Context, repo RepoRef, number int, add, remove []string) error
}
```

**Normalized events.** Drivers translate forge payloads into a small set of engine events (pull request changed, check completed, review submitted, command comment, base branch moved, config changed). Unknown payloads are dropped with a debug log, never passed through.

**Capabilities are explicit.** Each driver declares what it supports: check runs vs commit statuses, fast-forward landing, a native merge queue, labels, reactions. The planner reads capabilities instead of branching on forge names.

**Test refs are built with git, not forge merge APIs.** A shared git component fetches the base and the queued PRs, composes the speculative commits locally, and pushes them to `openmerge/queue/...` refs over the driver's `GitRemote`. This works the same on every forge. The host running OpenMerge needs the `git` executable, which the container image includes.

**Landing preserves the exact-SHA guarantee.** `LandRequest` carries the PR, the merge method, the PR head SHA we tested, and the tested commit SHA. A driver must refuse to land, not land something else, if the PR head moved or the base is not where the planner expected. A fast-forward lands the tested SHA itself.

**The fake forge ships first.** `internal/forge/fake` is an in-memory driver with a scriptable CI simulator: per-check outcomes as functions of the commit (pass, fail, flaky with a probability, duration) and a virtual clock. It is a first-class driver, not a mock: the planner simulation suite, the end-to-end tests, and the roadmap's throughput exit tests all run on it.

**Every driver passes the same contract suite.** `forgetest.Run(t, newDriver)` exercises each interface method's required semantics, including the landing refusals above. The fake forge runs it in every CI build. Real drivers run it against a sandbox organization when credentials are present and skip with a message otherwise.

## Consequences

- Adding a forge means implementing one interface and passing one suite, with no engine changes.
- Contributors can run the whole system, including end-to-end queue behavior, with no credentials.
- We take on a git dependency at runtime and must handle clone caching and disk cleanup. Mergify's self-hosted edition has the same requirement.
- The interface will grow. Additions go through review against the "narrow" rule: a method belongs in the driver only if the engine cannot express it with existing methods.
- GitHub's native merge queue support (Phase 5) becomes a driver capability plus an alternate executor path, not a fork of the engine.

## Alternatives considered

- **Call the GitHub SDK directly and abstract later.** Fastest to the first demo, and the path every GitHub-only queue took. Rejected because the abstraction would be shaped by GitHub's quirks and the tests would depend on GitHub.
- **Compose test commits with forge merge APIs** (Mergify's 2022 approach on GitHub). No local git needed, but GitLab and Forgejo lack equivalent endpoints, and API merges cost rate limit per PR per batch.
- **Record-and-replay HTTP fixtures instead of a fake forge.** Good for driver tests, which may still use them, but fixtures cannot express scripted CI outcomes over virtual time, which the planner simulation needs.
