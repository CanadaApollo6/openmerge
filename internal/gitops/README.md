# CLI git transport

`Repo` implements `forge.Git` with the `git` executable (2.38 or newer) and a
POSIX-shell askpass helper. Construct one with `New(ctx, Config{...})`:

```go
git, err := gitops.New(ctx, gitops.Config{
    WorkDir: "/var/cache/openmerge/git",
    Remote: func(ctx context.Context, repo forge.RepoRef) (string, gitops.Auth, error) {
        // Resolve the opaque repository path and obtain fresh credentials.
        return resolveRemote(ctx, repo)
    },
    HeadRef: func(pr int) string { return fmt.Sprintf("refs/pull/%d/head", pr) },
})
```

`Auth.Username` and `Auth.Password` supply HTTP credentials; a forge token is
normally the password. URL userinfo is removed before invoking Git and used as
fallback credentials. Credentials travel only in the child environment and
askpass output, never in command arguments, cache config, helper contents, or
returned diagnostics. Inherited Git tracing/config injection and credential
helpers are disabled. SSH authentication is left to the host's SSH setup; use
a host alias with its user configured in SSH config rather than URL userinfo.

`Remote` is resolved at operation start. Its credentials must remain valid through
that operation's reads and fetches. Land and AdvanceFence resolve it again
immediately before their final push, so the refreshed token only needs to remain
valid for that push. Providers should refresh expiring tokens on every resolution.
Authentication failures wrap `gitops.ErrAuth`; callers can use `errors.Is` to
separate them from lease rejections and other infrastructure failures. `New`
includes the configured work directory in its errors.

Each opaque repository path hashes to one lazy bare cache. Operations lock that
cache for their entire lifetime, including fetch and push. Locks also coordinate
multiple `Repo` instances with the same work directory **within one process**.
Different processes must use separate work directories. Phase 1 does not provide
cross-process cache locking or automatic eviction of repository directories.

The cache is disposable and can be removed while OpenMerge is stopped. There is
one bare cache per repository, roughly the disk footprint of a clone: fetching a
head also fetches its reachable history, without a shallow or partial clone limit.
No working tree is created. PR heads already present at their expected SHA are
checked against the remote ref without fetching their objects again. Temporary
`refs/openmerge-cache/pr/<N>` refs are deleted after pin checks, including failed
fetches and mismatches, so old PRs do not permanently retain their histories.

After an operation completes, under the same cache lock, maintenance runs
`git gc --prune=now --quiet` when either `Config.GCThresholdBytes` is reached
(default 512 MiB of loose and packed objects) or `Config.GCInterval` has elapsed
(default one hour). Zero selects the defaults; a negative value disables that
trigger. Collection also removes PR cache refs left by older versions or
interrupted operations. It never runs with a push or another operation in flight
in that cache. Collection is best effort, with a 30-second deadline; failures are
retried on later operations. These are soft triggers, not hard disk limits: a
single fetch can exceed the threshold, and an idle cache waits until its next
operation for collection.

`Author`, `Committer`, and `Now` configure generated commit metadata. Identities
default to `OpenMerge <openmerge@localhost>`; `Now` defaults to `time.Now`. Merge
creates a two-parent commit per PR. Squash creates one single-parent commit per
PR. Rebase replays linear PR histories in topological order, using each original
commit's parent as the explicit merge base. If the history reachable from a PR
head but not the base contains a merge commit, rebase returns a wrapped
`forge.ErrConflict` asking for the merge or squash method. This avoids resurrecting
content deliberately discarded by a merge resolution. Replayed
messages keep the original message and add `OpenMerge-Replayed-From`. The top
commit carries `OpenMerge-Batch`. A fixed clock makes identical builds repeatable.

Git 2.38/2.39 lack `merge-tree --merge-base`. On those versions, replay creates
an unreferenced synthetic commit with the current tree and the desired base as
its only parent, then runs `merge-tree --write-tree` against the original commit.
That forces the same base without an index or worktree. A test compares both
paths' resulting commit IDs. Newer Git uses `--merge-base=<original>^` directly.

## Landing and real Git findings

Every Git invocation uses these global arguments:

```text
git -c core.hooksPath=<null-device> -c credential.helper=
    -c commit.gpgSign=false -c gc.auto=0 -c maintenance.auto=false
    --git-dir=<cache>
```

Land verifies the pinned PR refs, reads the remote base and fence, obtains the
tested and expected fence objects, and creates the empty-tree fence commit:

```text
git <globals> mktree                         # empty stdin
git <globals> commit-tree <empty-tree> -p <expected-fence>
    # stdin: openmerge fence land/<attempt> tested=<tested-sha> nonce=<random-hex>\n
git <globals> push --atomic --porcelain
    --force-with-lease=refs/heads/<base>:<expected-base>
    --force-with-lease=refs/heads/<fence>:<expected-fence>
    -- <credential-free-remote>
    <tested-sha>:refs/heads/<base> <new-fence>:refs/heads/<fence>
```

AdvanceFence uses the same leased push for one ref. EnsureFence uses
`--force-with-lease=refs/heads/<fence>:` (empty expected value) and reads back a
concurrent creator. Client-side stale/non-fast-forward porcelain rejections map
to exactly `forge.ErrLeaseRejected`, without disclosing actual ref values. A
receiver-side old-OID mismatch after advertisement has different diagnostics
(`cannot lock ref ... is at ... but expected ...`); it maps to the same sentinel.
Unsupported atomic pushes and other transport/server errors remain errors.

### Skipped leases and external rollbacks

Git skips both the update and its lease check when an advertised destination
already equals the source SHA. Every fence commit therefore includes a fresh
nonce containing at least 128 random bits from `crypto/rand`, including copies of
the same attempt with identical parents, messages, and second-resolution clocks.
`ReadFence` ignores the nonce when reading the attempt and tested SHA.

A landing push always changes the fence, so Git always checks its lease. If one
copy lands and an external writer rolls the base back, a delayed copy mints a
different fence object and fails the consumed fence lease. The fence cannot be
an up-to-date no-op that lets the duplicate land again. AdvanceFence uses the
same uniqueness rule for abandonment commits.

If preflight sees the base already equal to the tested SHA, Land returns
`forge.ErrLeaseRejected` without pushing, even when the expected base also equals
that SHA. The executor resolves through the fence: a matching `land/<attempt>`
records the earlier landing; otherwise it abandons the attempt through its lease.

One residual case remains outside OpenMerge's guarantees: an external writer
pushes exactly the tested commit after preflight but before push advertisement,
then rolls it back inside the push window. Git can skip the base update while
still advancing the unique fence. This is indistinguishable from an administrator
rolling main back immediately after a successful landing. OpenMerge does not
protect main from such external rewrites. Another preliminary read cannot close
that window; a strict old-OID check for no-op refs would require server support or
a different transport protocol.

`TestGitSkipsLeaseForUpToDateRef` records the upstream behavior. The shared
contract covers refusal of already-tested bases, including matching expected
leases, and duplicate landings after rollback. `TestReviewDuplicateLandAfterRollback`
stages the duplicate after preflight against real Git;
`TestReviewExternalTestedThenRollbackLimit` preserves the external-writer
reproduction as a test of the documented boundary. The fake checks its leases
under a lock, mints unique fences, and refuses already-tested bases.
Its normal constructor uses cryptographic randomness; the simulator explicitly
uses `fake.NewDeterministic` with unique counter nonces to keep seeded runs
repeatable.

`forgetest.RunGit` runs against local bare remotes without hooks. Competing
bootstrap/landing/abandon operations use distinct caches so remote atomicity and
leases, rather than the cache mutex, determine the winner. The 64 landing races
check the winning fence identity and both final refs. Additional tests directly
push stale leases past Land's preflight, verify HTTP askpass and credential
scrubbing, and drive the executor through three serial real-git landings and a
lost-result recovery using the fake forge API.
