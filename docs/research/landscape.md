# OpenMerge: landscape and prior-art report on merge queues and PR automation (as of 2026-10-15)

Facts marked "unverified" come from secondary sources or from background knowledge that I did not confirm this session. License and status facts marked "(gh)" come from GitHub's repo API on 2026-10-15.

---

## 1. Why now: the agent PR surge

- **Anthropic's numbers:** Anthropic reports about 2,500 merges/day in May 2026 and over 30,000/day by September 2026. Its engineering post says a traditional merge queue "doesn't make much sense at agent scale." Anthropic says it could not copy the usual global repository lock on the merge queue. It relies on post-merge and pre-deploy testing instead, and Claude fixes conflicts after they happen. Its in-house VCS, Scion, will not be released.
- **GitHub Octoverse 2025:** Copilot coding agent created 1M+ PRs between May and Sept 2025. GitHub merged 518.7M PRs that year (+29% YoY).
- **AIDev dataset (MSR '26, arXiv 2602.09185):** 932,791 agent-authored PRs from Codex, Devin, Copilot, Cursor and Claude Code.
- **GitHub's own response:** GitHub added maintainer controls in 2026: disable PRs or restrict them to collaborators (Feb), and cap open PRs per non-write user, with agent PRs counting toward the cap (June 17). These dates come from secondary reporting (unverified).
- **Vendor positioning:** Mergify (July 2026) quotes Mitchell Hashimoto: queues "work for humans" but break at 100–1000x churn, and it is "really just a release problem." Mergify markets speculation, batching with bisection, scope-aware queues, two-step CI, "change identity over SHA" and agent-readable test health.
- **Cursor:** Cursor acquired Graphite (announced Dec 19, 2025). It is building "Origin," a forge for agents in early beta from Aug 2026. Origin's merge queue and stacked PRs are not confirmed as GA (unverified).
- **Other claims I could not trace to a primary source (unverified):**
  - Linear's test suites grew about 4x in 2026.
  - "One in three GitHub PRs involve an agent" (attributed to Nadella, July 2026).
  - "17M agent PRs/month."
  - LinearB: agent PRs wait 5.3x longer for pickup.

**Takeaway:** agents bring a high arrival rate, high churn per PR (re-pushes and retries), and a higher CI failure rate. Each of these hurts serial queues badly. One major frontier lab has dropped pre-merge gating for its monorepo, so OpenMerge should support a spectrum from strict gating to optimistic merge with post-merge verification and auto-revert.

---

## 2. Prior art, system by system

### 2.1 GitHub native merge queue
- **What it is:** a built-in queue, GA in July 2023. It is available in public org-owned repos and in private repos on **Enterprise Cloud only**. Personal repos and private repos on Free or Team plans can't use it, which matters a lot for OpenMerge's market.
- **How it works:**
  - Each queued PR gets its own temporary branch under `gh-readonly-queue/{base_branch}/…`. That branch holds the latest base, every PR ahead in the queue, and the PR itself. This is Zuul-style cumulative speculation.
  - GitHub fires a `merge_group` webhook (`checks_requested`, plus `destroyed` since GA) with a `head_sha` that CI must report on. Actions workflows must trigger on `merge_group`. Third-party CI triggers on pushes to `gh-readonly-queue/*`.
  - Results posted against the PR's own SHA don't count, because the queue commit has a different SHA.
  - If several groups pass, the newest one merges (it contains all earlier changes).
  - A failure or timeout removes the PR. Later groups are rebuilt without it.
- **Knobs:**
  - Merge method: merge, squash or rebase.
  - Build concurrency (`maximumEntriesToBuild`): 1–100.
  - Min and max entries to merge: 1–100, plus a wait time for the minimum.
  - Check timeout: 1–360 min.
  - Merging strategy: **ALLGREEN** (every entry must pass) or **HEADGREEN** (a failing entry may merge if a later group containing it passes). HEADGREEN is a crude flake-tolerance knob.
  - All of this is settable through the ruleset `merge_queue` rule (REST) or branch protection.
- **What a third-party app can do:**
  - GraphQL `enqueuePullRequest(pullRequestId, expectedHeadOid, jump)` and `dequeuePullRequest`.
  - Read `Repository.mergeQueue` (configuration, entries, `nextEntryEstimatedTimeToMerge`) and `PullRequest.mergeQueueEntry` (state: QUEUED / AWAITING_CHECKS / MERGEABLE / UNMERGEABLE / LOCKED; position; ETA; `solo`; `jump`).
  - Subscribe to `merge_group` (needs the "Merge queues" permission).
  - Act as a required check on merge groups, for example to skip unaffected targets or apply flake policy.
  - I found no REST endpoints for queue entries.
- **What it cannot do:**
  - Choose how PRs are grouped.
  - Bisect a failed group.
  - Run more than one queue or lane per branch.
  - Set priorities beyond "jump," which rebuilds everything in flight.
  - Use affected-target partitioning.
  - Run custom speculation trees.
- **Other limits:**
  - "Merge limits do not combine merge_group builds": each entry costs one CI run, so there is no real batch-to-save-CI mode.
  - Trunk (a competitor, Sept 2025) adds: every PR runs CI twice (on the PR and in the queue), and failures are hard to debug.
- **Borrow:**
  - Read-only, namespaced temp refs.
  - A dedicated CI event, so pipelines can tell queue runs apart.
  - The ALLGREEN/HEADGREEN distinction.
  - `expectedHeadOid` to prevent races.
  - A one-click UX with no ChatOps.
  - GitHub's internal monorepo results: about 2,500 PRs/month, a 33% cut in average time-to-ship, and groups of 30+ versus about 15 under the old train system.

### 2.2 GitLab merge trains
- **Tier, license and status:** Premium/Ultimate only, so part of the proprietary EE code. Actively maintained.
- **How it works:**
  - Each MR gets a "merged results" pipeline on target + all MRs ahead of it + itself. Pipelines run in parallel.
  - Default cap is 20 parallel pipelines. Minimum is 1, and a project value can't exceed the instance limit. The queue itself has no length limit.
  - A failure drops that MR and restarts the pipelines behind it. A failed train pipeline can't be retried; the docs offer the `retry:` keyword for intermittent failures.
  - "Merge immediately" bypasses the train and restarts it. `skip_merge_train` (experimental) merges without restarting the train.
  - Fast-forward and semi-linear merge methods are supported.
- **Limitations:**
  - There is no batching that reduces CI: there is one pipeline per MR.
  - No bisection, no affected-target lanes, no flake recovery beyond `retry`.
  - It is not available to Free-tier GitLab users. That gives an open-source tool an opening on GitLab CE/Free.

### 2.3 bors-ng, Homu and rust-lang/bors
- **bors-ng** (Elixir, Apache-2.0, archived 2024-04-04 (gh)):
  - Design: implemented Graydon Hoare's "Not Rocket Science Rule": only merge commits that have passed tests as merged. `bors r+` batches approved PRs onto a staging branch and **bisects** on failure.
  - Why it was deprecated (spring 2023): GitHub's merge queue became public, and some bors-ng bugs "we can't fix, but they can." Examples: squash-merged PRs can't be marked merged, the admin merge button shows when it shouldn't, and the CODEOWNERS parser differs from GitHub's.
  - **Lesson:** a third-party queue running on top of a forge it can't control has a permanent class of unfixable UX bugs. Design for this from day one.
- **Homu** (Python, MIT):
  - barosl/homu, then the servo/homu fork (archived 2019), then rust-lang/homu (archived Jan 15, 2026 (gh)).
  - Stateful and webhook-driven, unlike original bors, which polled every PR and scaled linearly with open PRs.
  - Introduced **rollups**: low-priority PRs batched together by hand, plus `try` builds and priorities.
- **rust-lang/bors** (Rust, MIT/Apache-2.0 dual, active (gh)):
  - A rewrite of Homu: GitHub App, PostgreSQL, per-repo `rust-bors.toml`, web UI.
  - Try builds on `automation/bors/try`, auto builds on `automation/bors/auto`. The merge queue and rollups were added in GSoC 2025.
  - Ran all rust-lang/rust try builds from July 2025. Took over merges for rust-lang/rust per the Infra team's Q4-2025 recap (Jan 2026).
  - **Borrow:** a modern, permissively licensed Rust codebase handling one of the hardest OSS CI workloads (rust-lang/rust: multi-hour CI, rollups, priority, try builds). It is directly reusable under Apache-2.0.

### 2.4 Zuul: the original speculative gating design
- **License, language, status:** Apache-2.0, Python, actively maintained under OpenInfra (license from background knowledge; unverified this session).
- **Speculation:**
  - The "dependent pipeline manager" tests every change exactly as it will merge. Jobs for change C run on A+B+C in parallel, assuming A and B pass.
  - On a failure, changes ahead still merge. The failed change is ejected. Changes behind it are restarted without it, and their results are treated as ambiguous until retested.
- **Adaptive window (key idea):**
  - The window is how many changes at the head of the queue may run jobs.
  - The documented default starts at 20, grows by 1 per success, and halves per failure: TCP-style AIMD. Floor and ceiling are configurable, with a per-queue window so one flaky project doesn't throttle the others.
  - Defaults vary by version: older docs show linear increase and exponential decrease.
- **Other ideas:**
  - Shared queues across projects for coupled repos.
  - Cross-repo and cross-forge `Depends-On:` (a Gerrit change can depend on a GitHub PR).
  - Freezing repo state per queue item.
  - Optional circular dependencies.
  - Drivers for Gerrit, GitHub, GitLab, Pagure and plain git.
- **Limitations:** heavy to operate (Nodepool/launcher, Ansible executors, ZooKeeper). Batching here means "longer speculative chain"; failed groups are not bisected.

### 2.5 Kubernetes Prow / Tide
- **License, language, status:** Apache-2.0, Go. Active; source now lives in kubernetes-sigs/prow (gh).
- **How it works:**
  - Merge pools per `org/repo:branch`.
  - Uses GitHub GraphQL **search queries** to find mergeable PRs, which keeps rate-limit use low across thousands of repos.
  - **Batch-first:** batches take priority over single PRs. A PR that passes alone waits while a batch runs, and merges alone only if the batch fails.
  - Can skip retesting when results are already against the current base HEAD.
  - Merge can be blocked via labelled issues. Exposes Prometheus metrics and a dashboard (Deck).
  - Replaced mungegithub's SubmitQueue in 2017, where batching had been "an afterthought."
- **Borrow:**
  - Level-triggered reconciliation (compute the desired state from forge queries) rather than trusting webhooks alone.
  - Reusing test results when the base hasn't moved.
- **Limitations:** no speculative chains, Tide is essentially GitHub-only, and it is tightly coupled to the Prow job model.

### 2.6 Uber SubmitQueue ("Keeping Master Green at Scale," EuroSys 2019)
- **Problem:** with about 16 concurrent, potentially conflicting changes there is about a 40% chance of breakage. Before SubmitQueue, Uber's iOS mainline was green only 52% of the time in a sampled week. A serial queue at 1,000 changes/day with 30-minute builds would leave the last change waiting over 20 days.
- **Design:**
  - The **speculation engine** builds a binary tree of outcomes, each edge carrying a predicted probability.
  - The **planner** picks the builds with the highest predicted value and cancels builds that are no longer useful.
  - The **conflict analyzer** uses build-system targets: two changes conflict if their affected target sets intersect, with Merkle-style target hashes. Independent changes turn the tree into a graph and commit in parallel.
  - The success predictor is logistic regression over about 100 features, at 97% accuracy. Strong features: prior speculation outcomes, test plans, how many tests passed before submission, resubmission count.
  - Turnaround is within 1.2x of an oracle that predicts perfectly.
  - The patent filings note that pairwise conflict checks are expensive (the build graph is computed several times per pair).
- **Follow-up, "CI at Scale: Lean, Green, and Fast" (arXiv 2501.03440):** adds build-time prediction to unblock small changes stuck behind slow conflicting ones, plus a speculation threshold. Reports −53% CI resources, −44% CPU and −37% P95 wait.
- **Related:** ICSME 2025 (arXiv 2508.08342) shows that **reordering PRs by predicted pass probability** beats FIFO at peak load, using only build history and PR metadata. No build-graph integration is needed.
- **Borrow:** treat speculation as an expected-value scheduling problem under a CI budget, and use affected-target independence. Uber's own listed limits: FIFO order can make small changes wait behind big ones, and batching was future work.

### 2.7 Commercial queues (closed-source SaaS)

| Vendor | Mechanics | Notable ideas |
|---|---|---|
| **Mergify** (engine moved to a private repo July 2022; it had been Apache-2.0. The public repo is now an issue tracker. `mergify-cli` is public.) | Speculative checks run on **draft PRs** from `mergify/merge-queue/*` branches. It tests **in place** on the original PR only when batch=1, parallelism=1 and there is no two-step CI. | **n-ary bisection** of failed batches, where n = speculative checks + 1. Scopes for monorepos. Two-step CI. Priority rules (`allow_checks_interruption` default flipped to false in 2026). `checks_timeout: auto`. 2026 additions: auto-batching of PRs that touch similar files; direct merge for PRs behind base only by merges in other scopes (July 2026); per-scope speculation caps. Free for OSS. Why it closed: only about half a dozen outside contributors, with zero regulars, and tests needed many special GitHub accounts. **Lesson for OpenMerge:** make the test harness runnable by outsiders, e.g. a fake forge. |
| **Trunk Merge Queue** | `trunk-merge/*` predictive branches. | **Optimistic merging** plus **pending failure depth**: a failed group waits for its successors, and a later pass retroactively clears a flake. Parallel lanes from **impacted targets** reported over an API (Bazel/Nx/custom). Batching up to 100 with bisection. Multiple queues per repo. Enqueue-by-label (May 2026). Pairs with **Trunk Flaky Tests**, which detects and quarantines at runtime and separates "broken" from "flaky." Its uploader `trunk-io/analytics-cli` is MIT and written in Rust (gh). |
| **Aviator MergeQueue** | Parallel mode creates a **draft PR** per cumulative speculation, with a "max bot builds in parallel" cap. | `batch_size`; **optimistic validation**, which waits for other batches before failing on a flake; skip-the-line (resets everything in flight); affected targets (directory, Nx, GitHub Actions; enterprise tier); `override_required_checks`, because GitHub mergeability doesn't apply to drafts; stuck-PR timeouts. The `av` stacked-PR CLI is MIT and written in Go (gh). |
| **Graphite** (Cursor acquisition announced Dec 2025; it continues as a separate product) | **Stack-aware**: tests the top of the stack once, then fast-forward merges the whole stack. On failure it evicts only the failing PR and its dependents. Paid beta per its FAQ. | Treats a stack as one unit of speculation. Must replace GitHub's native queue rather than run alongside it. |

### 2.8 PR automation bots (non-queue)
- **Kodiak** (AGPL-3.0, Python/TS, last push Sept 2026 (gh)): auto-update and auto-merge by label, with a hosted app. Because it is **AGPL**, OpenMerge should not copy its code into Apache-2.0 code.
- **Palantir bulldozer** (Apache-2.0, Go, active (gh)): auto-merge when checks and reviews pass, gated by label, comment or branch rules; keeps branches up to date.
- **Palantir policy-bot** (Apache-2.0, Go, active (gh)): `.policy.yml` approval policies built from and/or rule trees (nested up to five levels), with predicates on files, authors, labels, checks and custom properties. Supports org-wide shared policies, a simulation API, and invalidating approvals on push. **This is the best open model for agent-era approval rules**, such as "agent-authored PR touching `infra/**` needs two human approvals."
- **Mergeable** (AGPL-3.0, Probot/JS, last push July 2024 (gh); effectively stale): declarative PR validation.
- **Reviewpad:** the GitHub repo no longer resolves; a search snippet showed a "maintenance mode, fork it" notice. Treat it as defunct (license was LGPL-3.0, unverified). Its idea worth borrowing: a programmable PR-automation DSL.

### 2.9 Gerrit submit strategies and Chromium CQ / LUCI CV
- **Gerrit** (Apache-2.0, Java):
  - Submit types: MERGE_IF_NECESSARY (default), FAST_FORWARD_ONLY, REBASE_IF_NECESSARY, REBASE_ALWAYS (always produces a new patch set with footers), MERGE_ALWAYS, CHERRY_PICK (ignores dependencies unless `submitWholeTopic`).
  - `change.submitWholeTopic` makes multi-change, multi-repo topics atomic.
  - Gerrit has no queue of its own; Zuul or LUCI CV supplies one.
- **LUCI Change Verifier** (in luci-go, Apache-2.0 (gh)):
  - The successor to Chromium's CQ. Runs try jobs on Buildbucket and submits on success.
  - Its project manager decides Runs from the state of all CLs in a project. Supports multi-CL "combinable" Runs.
  - Commit-Queue +1 is a dry run; +2 is a full run plus submit. CV can reuse recent passing try jobs (from background knowledge; unverified).
  - Ships an end-to-end test suite against a **fake Gerrit**, which is the testing model OpenMerge should copy.
  - Chromium does *not* serialize: it relies on pre-submit try jobs, post-submit sheriffing and reverts.

### 2.10 Forgejo / Gitea (new in 2026)
Neither forge has a native queue. Two small MIT Go projects use the same portable pattern:
- **gitea-mq** (v0.1.0, Aug 2026): Gitea, Forgejo and GitHub.
- **shunt** (v0.13, Aug 2026): validated on Forgejo and Codeberg.

How the pattern works:
1. Register as a **required commit status**.
2. Treat the forge's native "merge when checks succeed" as the enqueue action.
3. Test on bot-owned branches (`gitea-mq/<pr>`, `mq/<base>/staging-*`).
4. Flip the status to green so the forge's own auto-merge lands the PR.

shunt adds batch-then-bisect, about log₂N extra runs per failure. **This is the lowest-common-denominator pattern for forge portability.**

### 2.11 Flaky-test prior art
- **Meta's Probabilistic Flakiness Score** (2020 blog): a per-test statistical flakiness score that works with any language or framework and scales to millions of tests. Third parties claim it is Bayesian, computed with Stan (unverified).
- **LUCI Analysis** (Apache-2.0): clusters failures by test-name rules and failure reason, files bugs past thresholds, and tracks "exonerated" failures.
- **Retry plugins:** Gradle test-retry plugin (Apache-2.0); pytest-rerunfailures; Maven Surefire `rerunFailingTestsCount` (keeps `<flakyFailure>` records); Bazel `flaky = True` / `--flaky_test_attempts`; Jest `retryTimes`.
- **Mozilla Treeherder:** classifies intermittent failures.
- **Academic tools (from background knowledge; unverified this session):** DeFlaker, iDFlakies, FlakeFlagger.
- **Small 2025–26 CLIs:** flakewall, flaktor (JUnit XML, quarantine).
- **Uber data point:** about 1,000 flaky tests out of 600,000 (secondary; unverified).

**Gap:** no widely used, forge-neutral, open-source flaky-test *service* (ingest JUnit XML, score, quarantine list, API) appears to exist. OpenMerge could fill it.

---

## 3. Comparison table

| System | License | Status (Oct 2026) | Lang | Forges | Speculative chain | Batch + bisect | Independent lanes | Flake handling | Priorities |
|---|---|---|---|---|---|---|---|---|---|
| GitHub MQ | Proprietary | Active; Enterprise Cloud for private repos | n/a | GitHub | Yes (cumulative, ≤100) | Merge-time grouping only; no bisect | No (1 per branch) | HEADGREEN | Jump only |
| GitLab merge trains | Proprietary (Premium) | Active | Ruby | GitLab | Yes (≤20 default) | No | No | `retry:` | Merge immediately |
| bors-ng | Apache-2.0 | Archived 2024 | Elixir | GitHub | No | Yes (bisect) | No | Manual retry | Yes |
| Homu | MIT | Archived (2019 / Jan 2026) | Python | GitHub | No | Rollups (manual) | No | Retry | Yes |
| rust-lang/bors | MIT/Apache-2.0 | Active, in production for rust-lang/rust | Rust | GitHub | No (serial auto plus rollups) | Rollups | No | Retry | Yes |
| Zuul | Apache-2.0 | Active | Python | Gerrit, GitHub, GitLab, Pagure | Yes (adaptive AIMD window) | Via chain only | Shared or independent queues | Recheck | Pipeline precedence |
| Prow/Tide | Apache-2.0 | Active | Go | GitHub | No | Batch-first; no bisect | Per branch | Retest | Old PRs first |
| Uber SubmitQueue | Internal (paper) | Production | n/a | Internal | Probabilistic tree/graph | Future work (2019) | Target-conflict graph | Model-based | Value-based |
| Mergify | Closed (since 2022) | Active | Python | GitHub | Yes (draft PRs) | Yes (n-ary) | Scopes | Retries, CI Insights | Yes |
| Trunk | Closed | Active | n/a | GitHub | Yes (optimistic) | Yes (≤100) | Impacted-target lanes | Pending-failure depth + Flaky Tests | Yes |
| Aviator | Closed (`av` CLI MIT) | Active | n/a | GitHub | Yes (draft PRs) | Batch | Affected targets (enterprise) | Optimistic validation | Skip line |
| Graphite | Closed (owned by Cursor) | Active (paid beta per FAQ) | n/a | GitHub | Stack-level | Stack = batch | No | n/a | n/a |
| LUCI CV | Apache-2.0 | Active | Go | Gerrit | No (no serialization) | Multi-CL runs | n/a | Try-job retries + LUCI Analysis | n/a |
| gitea-mq / shunt | MIT | New (v0.x) | Go | Forgejo, Gitea (+GitHub) | No | Yes (shunt bisects) | No | No | No |
| Kodiak / bulldozer / policy-bot / Mergeable | AGPL / Apache / Apache / AGPL | Active / active / active / stale | Py / Go / Go / JS | GitHub | n/a (auto-merge or policy only) | n/a | n/a | n/a | n/a |

---

## 4. Key algorithmic choices and tradeoffs

Notation: N = PRs in the queue, T = CI duration, p = per-PR failure probability (real failures plus flakes).

1. **Serial ("not rocket science"):** test base+PR, merge, repeat.
   - Main is always green. Throughput is 1/T: about 48 PRs/day at T=30 min.
   - Unusable at agent volume except for tiny repos.
2. **Batching + bisection** (bors-ng, Tide, shunt, Mergify, Trunk):
   - Test k PRs in one run. A failure costs about log₂k extra runs (n-ary search with spare parallelism).
   - CI cost per PR approaches 1/k when p is small. Batches fail with probability 1−(1−p)^k, so the useful k shrinks as p grows. Flakes poison bisection.
   - **Make k adaptive to the observed p.**
3. **Speculative cumulative chains** (Zuul, GitHub, GitLab, Mergify, Aviator):
   - Run base+A, base+A+B, base+A+B+C in parallel. Latency is about T at depth d. CI cost grows by roughly d.
   - A failure at position i invalidates everything behind it, so the expected waste is about d·p.
   - **Bound the depth with Zuul's AIMD window.**
4. **Hybrid: speculative chain of batches** (Mergify, Trunk): each speculation slot is a batch. This is the practical sweet spot today.
5. **Optimistic / flake-tolerant resolution** (Trunk's pending failure depth, GitHub's HEADGREEN, Aviator's optimistic validation):
   - Treat a failure as tentative until successors report. If base+A+B passes, A's failure was a flake (or B fixed it).
   - Saves heavy restart cascades, at a small risk of landing a real but intermittent failure.
6. **Probabilistic / value-based scheduling** (SubmitQueue, Uber 2025, ICSME 2025):
   - Predict P(pass) and build time. Spend the CI budget on the speculations with the highest expected value. Reorder to let likely-green and small PRs through first.
   - Needs history and features. Agent PRs carry useful signals: author agent, retry count, pre-queue CI result, diff size.
7. **Affected-target independence** (SubmitQueue's conflict analyzer, Trunk/Aviator lanes, Mergify scopes, Zuul shared queues):
   - Split the single queue into a DAG of lanes. PRs with disjoint affected sets don't need to be tested together.
   - Correctness depends on the build graph being accurate: Bazel, Buck2, Pants, Nx, Turborepo, or path globs as a fallback.
   - Shared targets (lockfiles, toolchains) need a "touches everything" rule.
   - Mergify's July 2026 "direct merge if behind only by other-scope merges" is the cheap version.
8. **Post-merge verification + auto-revert** (Chromium sheriffing, Google TAP-style culprit finding, Anthropic 2026):
   - No pre-merge serialization. Main can break briefly; bisect and revert automatically.
   - Highest throughput. Needs fast revert tooling and tolerance for brief red builds.
9. **Two-tier / two-step CI:** cheap checks pre-queue, the expensive suite only in-queue, deploy-time checks post-merge. This multiplies the effect of every option above.

---

## 5. Recommendations for OpenMerge

**A. Core engine: own the speculation; don't make GitHub's native queue the foundation.**
- GitHub's queue is unavailable for private repos outside Enterprise Cloud. It exposes no control over grouping, bisection, lanes or priorities. It exists only on GitHub.
- bors-ng's history shows what happens when a tool sits on top of forge behavior it can't change.
- Build an engine that owns:
  - an event-sourced queue state in Postgres;
  - Zuul's AIMD-windowed speculative chain of **adaptive batches** with n-ary bisection;
  - Trunk-style pending-failure resolution;
  - affected-target lanes forming a DAG, not one line;
  - priority classes;
  - a pluggable scheduler interface (FIFO first, then predicted-pass and build-time-aware, per Uber and ICSME 2025).
- **Also ship a "GitHub native" adapter** for users who want GitHub's UX. In that mode OpenMerge:
  - enqueues and dequeues via GraphQL (`expectedHeadOid`, `jump`);
  - acts as a smart **required check on `merge_group`**, applying affected-target skip logic, flake quarantine and retry policy;
  - reads queue state for dashboards.
- Same rule set, two backends.

**B. Test refs, not draft PRs.**
- Push bot-owned refs such as `openmerge/<base>/<batch-id>` and have CI trigger on push to that prefix (the same contract as `gh-readonly-queue/*`).
- Draft PRs (Mergify, Aviator) clutter repos and trip GitHub mergeability quirks. Keep them only as a fallback for CI systems that can only test PRs.
- Land by **fast-forwarding the exact tested SHA** onto the protected branch, using a ruleset bypass for the app or a forge merge API with an expected SHA. That way, what you tested is what you merged.
- Offer a squash/rebase mode where the tested tree hash, not the commit SHA, must match.

**C. Forge portability via a narrow driver interface:**
- Operations: list/watch PRs, read approvals and mergeability, create/delete/force-push refs, set commit status or check, merge with expected SHA, comment, label, and webhooks plus periodic reconciliation (Tide-style GraphQL search to stay within rate limits).
- **GitHub:** GitHub App. Native merge-queue adapter optional.
- **GitLab:** own engine. This gives Free/CE users merge trains *with* batching and bisection, which GitLab Premium lacks. Optionally drive native trains through `auto_merge`.
- **Forgejo/Gitea:** the gitea-mq/shunt pattern. Enqueue with the native "merge when checks succeed," gate through a required status, and let the forge's auto-merge land the PR.
- **Gerrit:** later, or point users to Zuul.
- Build a **fake-forge test harness** from day one, as LUCI CV does. Mergify's maintainers cited unrunnable tests as a reason they closed their engine.

**D. Agent-era features nobody fully owns yet:**
- **Change identity across re-pushes:** a queue entry belongs to a logical change, so superseded agent pushes cancel the old run instead of stacking new ones.
- Per-author and per-agent concurrency and retry budgets, with escalation to a human.
- Machine-readable failure reasons: a JSON API and CLI that tell an agent "your PR broke target X" versus "flaky, auto-retried."
- Policy-bot-style approval rules aware of who authored the PR (agent or human).
- CI cost accounting per lane and per author.
- **Selectable gating modes:** strict pre-merge → optimistic/HEADGREEN → post-merge verify plus auto-revert (the Anthropic/Chromium end of the spectrum).

**E. Ship flake management as a first-class, separable component:**
- Ingest JUnit XML and the CTRF report format.
- Compute a PFS-like Bayesian flakiness score per test, keep a quarantine list exposed to CI, and separate "broken" from "flaky" (as Trunk does).
- Feed this into queue decisions: a failure in quarantined-only tests becomes a pending failure, not an eviction.

**F. Licensing and reuse:**
- Apache-2.0 and MIT code can be studied or adapted with attribution: rust-lang/bors, Prow/Tide, Zuul, bulldozer, policy-bot, luci-go, bors-ng, Homu, gitea-mq, shunt.
- **Avoid copying AGPL code** (Kodiak, Mergeable).
- Mergify, Trunk, Aviator and Graphite are closed. Learn from their docs only. A config importer for migrations (e.g., from Mergify or bors) is a reasonable on-ramp.

**G. Suggested order:**
1. GitHub plus a fake forge; serial plus batch-bisect; required-status landing.
2. Speculative chain with an AIMD window; flake service; policy rules.
3. Affected-target lanes (path globs, then Bazel/Nx adapters); GitLab and Forgejo drivers.
4. Predictive scheduling; post-merge auto-revert mode; GitHub native-queue adapter.

**Language:** Go or Rust. rust-lang/bors (Rust, Postgres) is the closest modern permissive codebase to learn from. Go matches Prow, policy-bot, bulldozer and the forge-SDK ecosystem.

---

## Sources
- https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue
- https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/merging-a-pull-request-with-a-merge-queue
- https://docs.github.com/en/enterprise-cloud@latest/graphql/reference/pulls
- https://docs.github.com/zh/enterprise-server@3.21/rest/repos/rules
- https://github.blog/changelog/2023-04-19-pull-request-merge-queue-public-beta-api-support-and-recent-fixes
- https://github.blog/changelog/2023-07-12-pull-request-merge-queue-is-now-generally-available/
- https://github.blog/engineering/engineering-principles/how-github-uses-merge-queue-to-ship-hundreds-of-changes-every-day
- https://github.com/google/go-github/issues/3253
- https://trunk.io/blog/outgrowing-github-merge-queue
- https://docs.gitlab.com/ci/pipelines/merge_trains/
- https://gitlab.com/gitlab-org/gitlab/-/issues/374188
- https://bors.tech/newsletter/2023/04/30/tmib-76/
- https://bors.tech/newsletter/2023/06/30/tmib-78/
- https://github.com/bors-ng/bors-ng
- https://github.com/rust-lang/bors
- https://github.com/rust-lang/homu
- https://blog.rust-lang.org/inside-rust/2026/01/13/infrastructure-team-q4-2025-recap-and-q1-2026-plan/
- https://blog.rust-lang.org/inside-rust/2025/10/16/infrastructure-team-q3-recap-and-q4-plan
- https://internals.rust-lang.org/t/batched-merge-rollup-feature-has-landed-on-bors/1019
- https://zuul-ci.org/docs/zuul/latest/gating.html
- https://zuul-ci.org/docs/zuul/7.0.0/config/pipeline.html
- https://docs.openstack.org/infra/zuul/developer/datamodel.html
- https://docs.prow.k8s.io/docs/components/core/tide/
- https://docs.prow.k8s.io/docs/components/core/tide/maintainers/
- https://blog.acolyer.org/2019/04/18/keeping-master-green-at-scale/
- https://image-ppubs.uspto.gov/dirsearch-public/print/downloadPdf/10942731
- https://arxiv.org/abs/2501.03440
- https://www.uber.com/us/en/blog/slashing-ci-costs-at-uber/
- https://arxiv.org/abs/2508.08342
- https://docs.aviator.co/mergequeue/concepts/parallel-mode
- https://docs.aviator.co/mergequeue/concepts/affected-targets
- https://docs.aviator.co/mergequeue/concepts/managing-flaky-tests-in-mergequeue
- https://github.com/aviator-co/av
- https://docs.trunk.io/merge-queue/how-does-it-work
- https://docs.trunk.io/merge-queue/anti-flake-protection
- https://docs.trunk.io/merge-queue/concepts-and-optimizations/parallel-queues
- https://docs.trunk.io/flaky-tests/overview
- https://github.com/trunk-io/analytics-cli
- https://graphite.com/docs/graphite-merge-queue
- https://graphite.dev/blog/the-first-stack-aware-merge-queue
- https://cursor.com/blog/graphite
- https://techcrunch.com/2025/12/19/cursor-continues-acquisition-spree-with-graphite-deal
- https://www.eesel.ai/blog/what-is-cursor-origin
- https://articles.mergify.com/speculative-check-and-batch-under-the-hood/
- https://articles.mergify.com/why-mergify-codebase-isnt-open-source-anymore-a-tale-of-growth-change-and-adaptation/
- https://github.com/Mergifyio/mergify/discussions/5035
- https://docs.mergify.com/merge-queue/parallel-checks
- https://docs.mergify.com/merge-queue/batches
- https://docs.mergify.com/changelog/
- https://docs.mergify.com/changelog/2026-07-20-direct-merge-for-scope-unaffected-pull-requests/
- https://mergify.com/pricing
- https://mergify.com/blog/merge-queues-and-ai-coding-agents
- https://julien.danjou.info/blog/merge-queues-built-for-humans/
- https://github.com/chdsbd/kodiak
- https://github.com/palantir/bulldozer
- https://github.com/palantir/policy-bot
- https://github.com/mergeability/mergeable
- https://fr.github.com/reviewpad
- https://www.crowdfundinsider.com/2023/04/206389-artificial-intelligence-reviewpad-raises-e1m-to-streamline-code-reviews-by-leveraging-ai-tech/
- https://gerrit.googlesource.com/gerrit/+/d736da341fe1041e879165b0c78177eab9ceed24%5E%21
- https://asterix-gerrit.ics.uci.edu/Documentation/concept-changes.html
- https://chromium.googlesource.com/infra/luci/luci-go/+/HEAD/cv/README.md
- https://chromium.googlesource.com/chromium/src/+/master/docs/infra/cq.md
- https://www.chromium.org/developers/testing/commit-queue/design/
- https://pkg.go.dev/github.com/rbtr/shunt
- https://pkg.go.dev/github.com/Mic92/gitea-mq
- https://code.tokarch.uk/third_party/gitea/commit/a9cc9c0f7afacb2e51cfd1885d392f56dca0c7f5
- https://engineering.fb.com/2020/12/10/developer-tools/probabilistic-flakiness/
- https://arxiv.org/pdf/2212.00908
- https://chromium.googlesource.com/chromium/src/+/cdaaabcabe2bb879117c9894decc54c333a215cc/docs/testing/gtest_flake_tips.md
- https://awesome.ecosyste.ms/projects/github.com%2Fcicatriiz%2Fflakewall
- https://pypi.org/project/flaktor/
- https://anthropic.com/engineering/redesigning-source-control
- https://github.blog/news-insights/octoverse/octoverse-a-new-developer-joins-github-every-second-as-ai-leads-typescript-to-1/
- https://arxiv.org/html/2602.09185v1
- https://arxiv.org/html/2507.15003v1
- https://tianpan.co/blog/2026-07-02-the-merge-queue-is-the-new-bottleneck
- https://bex.co/blog/2026/09/08/ai-coding-agents-open-source-maintainership
- https://www.coderabbit.ai/blog/github-gives-maintainers-a-throttle-for-the-ai-pull-request
- https://gigazine.net/gsc_news/en/20260618-github-limit-pull-request
- https://dev.to/itaisagi/ai-agent-pull-requests-stuck-in-merge-conflicts-what-broke-in-our-merge-path-and-what-we-changed-4ni9
- https://specstory.com/learning/ci-cd/ci-for-coding-agents
- https://blog.codacy.com/ai-breaking-code-review-how-engineering-teams-survive-pr-bottleneck
- https://inside.basepowercompany.com/p/merge-dont-queue
