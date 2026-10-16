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

Each opaque repository path hashes to one lazy bare cache. Operations lock that
cache for their entire lifetime, including fetch and push. Locks also coordinate
multiple `Repo` instances with the same work directory **within one process**.
Different processes must use separate work directories. Phase 1 does not provide
cross-process cache locking or automatic cache eviction. Only requested heads and
missing commit objects are fetched; there is no full clone or worktree.

`Author`, `Committer`, and `Now` configure generated commit metadata. Identities
default to `OpenMerge <openmerge@localhost>`; `Now` defaults to `time.Now`. Merge
creates a two-parent commit per PR. Squash creates one single-parent commit per
PR. Rebase replays commits in topological order, using each original commit's
first parent as the explicit merge base, including for merge commits. Replayed
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
    # stdin: openmerge fence land/<attempt> tested=<tested-sha>\n
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

**Unresolved contract gap:** Git skips both the update and its lease check when
an advertised destination already equals the source SHA. An atomic push can
therefore ignore a stale base lease while updating the fence. Land's preflight
refuses this when already observable. An external writer can still move the
base to exactly the tested SHA between preflight and push advertisement. Under
the prescribed CLI invocation, the absolute contract statement that every stale
base lease is refused cannot be guaranteed in that narrow race. Fixing that
requires a server-enforced old-OID check even for no-op refs, or a different
transport protocol; another preliminary read cannot close the race. No server
hooks or alternative landing mechanism are installed here.

`TestGitSkipsLeaseForUpToDateRef` reproduces this behavior against unmodified Git,
and `TestLandRefusesStaleUpToDateBase` covers the preflight mitigation. The fake
checks every lease under its lock and has no corresponding no-op exception.
The fake also originally squashed a whole batch into one commit; it now creates
one squash per PR, with a shared contract assertion for that shape.

`forgetest.RunGit` runs against local bare remotes without hooks. Competing
bootstrap/landing/abandon operations use distinct caches so remote atomicity and
leases, rather than the cache mutex, determine the winner. The 64 landing races
check the winning fence identity and both final refs. Additional tests directly
push stale leases past Land's preflight, verify HTTP askpass and credential
scrubbing, and drive the executor through three serial real-git landings and a
lost-result recovery using the fake forge API.
