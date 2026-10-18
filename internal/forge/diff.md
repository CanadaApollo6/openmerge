# Pinned revision inventory (ADR 0007 Stage A 2a)

`RevisionDiffReader` is an optional, independent forge capability. It has no
runtime caller, config keys, journal events, decisions, or approval integration.
The caller supplies full base and head **commit** object IDs. The reader returns
the unique merge base and compares its tree with the head tree. It does not use
PR file pagination, the current default branch, or a speculative build base.
A revision keeps its first observed queue-base pin across forward landings;
a consumer must request a new pin after a rewrite/rewind, even at the same head.

GitHub's `Driver.RevisionDiffReader(ctx, cacheRoot)` creates the read capability
with the installation's authenticated `Remote`. Pass the server's `--git-cache`
root. It uses the existing gitops cache at
`<root>/<installation>/<sha256(owner/repo)>.git`, including the process-wide
repository lock, credential sanitization, fetch lifecycle, and maintenance.
Do not initialize or fetch into that cache separately. As with existing gitops,
one cache root must not be shared across OpenMerge processes. Construction is
dormant; future serve wiring owns activation and request scheduling.

`git-inventory-v1-exact-renames` fixes these semantics:

- Raw NUL-delimited git tree comparison, without external diffs, textconv, or
  heuristic renames. Paths retain their original bytes, including tabs/newlines.
- Pair a rename only when exactly one deletion and one addition share an object
  ID. Unmatched add/delete candidates carry `unpaired` evidence, allowing a
  classifier to preserve uncertainty. A complete path inventory may still have
  uncertain rename evidence. Both modes and immutable object IDs are retained.
- Detect binary blobs by a NUL anywhere in either inspected blob, independently
  of head/governing/host attributes. `BinaryKnown` distinguishes an unread blob
  from a known text file. Symlinks retain mode 120000; gitlinks retain mode
  160000 and `Submodule`, without trying to read their commit IDs as blobs.
- At most 1,000 output files and 2 MiB of raw inventory plus inspected blob bytes
  per comparison; duplicate blob IDs are charged once. Oversized raw output is
  stopped with bounded memory. Limits and failures explicitly mark inventory
  incomplete; observed paths cannot prove absence of unobserved changes.
- Multiple merge bases, unrelated histories, missing objects and read failures
  fail closed with typed reasons. Transport errors also return sanitized errors.
  Callers must inspect coverage even when the error is nil.

Stage 2a emits zero hunks (`MaxDiffHunks = 0`), always with independent incomplete
hunk coverage and `hunks_not_collected`. Stage 2b adds governing-file provenance
and bounded hunk acquisition; a complete inventory is not a complete brief.
The fake reads existing immutable regular-file snapshots without changing its
commit identity or legacy simulator behavior. Mode/gitlink and transport cases
are exercised with local git and an authenticated localhost HTTP git server.
