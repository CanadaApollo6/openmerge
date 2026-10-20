# Forge validation parity (#83): trial classification evidence

Proposed classification: **inert / non-material** for the ADR 0009 comparison.
This is a merge-time classification proposal, not a deployment record or a claim
that ADR 0007's binary/journal differential deployment gates were run. No live
DB, status API, refs, App permissions, units, workflows, Makefile or test flags
were changed. A live deployment still needs the maintainer's pre-metrics
classification and the applicable trial evidence/log entry.

The production changes centralize existing syntax rules, reject additional
invalid PR numbers and malformed deletion refs, and classify local validation
errors. Valid inputs produce the same API calls and git commands after
validation, except `check-ref-format` is now an equivalent in-process check.
Batch planning, commit messages, landing leases, check observation, and wire
formats are unchanged. Fake-only changes tighten fixture behavior; they do not
alter a deployed binary's valid-input behavior.

Reproduce the production caller inventory (exclude tests):

```sh
rg -n '\.(BuildTestRef|Land|EnsureFence|AdvanceFence|ReadFence|ReadRef|DeleteBranch|DeleteRef|ReportStatus|MarkLanded|Comment|CanWrite|ReadFile|PullRequest|CommitAuthors|ListComments|HeadBranchForDeletion)\(' internal/executor internal/serve -g '*.go' -g '!**/*_test.go'
rg -n 'nextBatchID|openmerge/queue/|openmerge/fence/|Status:' internal/queue internal/executor internal/serve -g '*.go' -g '!**/*_test.go'
rg -n 'receipt :=|key :=|landed/|land/|abandon/|hex.EncodeToString' internal/serve/commands.go internal/serve/stage_a_commands.go internal/executor/executor.go internal/forge/github/driver.go
```

The concrete construction evidence is:

- `internal/queue/apply.go:nextBatchID` formats `b%d`; ordinary and speculative
  build refs in `internal/queue/plan.go` and `internal/queue/chain.go` are
  `openmerge/queue/<base>/<batch-id>`. `Executor.Execute` passes these values,
  observed base SHAs, and positive admitted PR pins into `BuildTestRef`.
  Fast-forward construction in `internal/executor/fast_forward.go` uses the
  same pins and generated ref. These already passed gitops's real validation;
  the fake now rejects the same bad fixtures.
- `internal/executor/executor.go:newID` generates 16 random bytes encoded as
  hexadecimal. Landing constructs `land/<attempt-id>`; abandonment constructs
  `abandon/<attempt-id>`. Fence refs are `openmerge/fence/<base>`. Production
  does not inject `NewID`. Base branches have already been resolved by
  `Git.ReadRef` and the existing real branch validator before queue actions.
- The executor's `Land` call passes the observed full base/fence/tested SHAs
  and the exact pinned PR heads. Immutable SHA rules are unchanged; malformed
  pins now fail consistently in the fake before a misleading lease refusal.
- `HeadBranchForDeletion` freshly returns a plain same-repository branch and
  the executor calls `DeleteBranch` with its pinned head SHA. The real cleanup
  guard already excluded default/base/protected/reserved/used/moved branches.
- The executor's `DeleteRef` calls use generated test refs. Every valid
  generated branch already satisfies the git ref grammar; rejecting malformed
  deletion refs cannot affect that path.
- Legacy command replies use `fmt.Sprintf("comment/%d", c.ID)` plus the fixed
  `/unauthorized`, `/head-changed`, or `/queued` suffix. Stage A uses that same
  decimal key plus `/approval`. Landing comments use `landed/<full-sha>`.
  None can introduce `<`, `>`, CR, LF or `--`. The real adapter already rejected
  malformed keys; the fake marker now matches it.
- Permission reads receive forge-observed comment/review author logins, not
  branch or comment-body text. The real `CanWrite` rejection rule is unchanged;
  the fake now uses it. Stable identity name validation is an exact extraction
  of the real reader's existing alphabet and `[bot]` suffix rule.
- Configuration reads in `internal/serve/reconcile.go` use a resolved commit
  tip and enumerated nonempty config paths. Both real and fake now reject an
  empty ref/path before lookup; valid reads have unchanged content semantics.
- `ReportStatus` receives the planner's finite lifecycle states or explicit
  `forge.Dequeued`; workers also construct normalized status constants. The
  real mapping is unchanged. PR-number-bearing operations consume positive
  PR observations/admissions and parsed positive command targets, not zero or
  negative identifiers.
- History readers retain the preexisting 10,000 limits, now via one validator.
  Full-ID, history-limit, build metadata and identity semantics have no changed
  valid-input bounds.

The complete method-by-method audit and intentional model limits are in
[the driver contract](../forge-driver-contract.md#input-validation-and-fake-parity).
Shared negative tests exercise the implementations, assert error classes and
check that rejected mutations preserve refs/comments/statuses. Qualified-branch
alias tests preserve successful operations. The CLI oracle checks the extracted
ref grammar against Git without accessing any live ref.
