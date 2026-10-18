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
one cache root must not be shared across OpenMerge processes. Construction:
no network; writes only the askpass helper inside the cache root. Bare-cache
initialization happens lazily at first open.
Future serve wiring owns activation and request scheduling.

`git-inventory-v4-bounded-ancestry` fixes these semantics:

- Raw NUL-delimited git tree comparison, without external diffs, textconv, or
  heuristic renames. Paths retain their original bytes, including tabs/newlines.
- Pair a rename only when exactly one deletion and one addition share an object
  ID. Unmatched add/delete candidates carry `unpaired` evidence and make
  inventory incomplete with `unpaired_rename`. Consumers can rely on completeness
  without scanning reasons for hidden uncertainty. Both modes and immutable
  object IDs are retained.
- Detect binary blobs by a NUL anywhere in either inspected blob, independently
  of head/governing/host attributes. `BinaryKnown` distinguishes an unread blob
  from a known text file. Symlinks retain mode 120000; gitlinks retain mode
  160000 and `Submodule`, without trying to read their commit IDs as blobs.
- At most 1,000 output files and 2 MiB of raw inventory plus inspected blob bytes
  per comparison; duplicate blob IDs are charged once. Oversized raw output is
  stopped with bounded memory. Limits and failures explicitly mark inventory
  incomplete; observed paths cannot prove absence of unobserved changes.
- Shallow repositories fail closed with `shallow_history` before accepting a
  merge base, even when both endpoint objects exist. Git's shallow status and
  the shallow file's identity/content are checked after fetching, after each
  ancestry-dependent read, and before returning complete evidence. Ancestry
  commands ignore the mutable shallow file and follow raw commit parents.
- Replace refs (loose or packed) and any `info/grafts` file fail closed with
  `altered_history`. Every Git command in a diff session disables replacement
  objects, grafts, lazy fetching and the commit-graph. Verification runs
  `rev-list --parents <base> <head> --not <all merge bases>` against raw parents.
  Missing commits in this divergence are read failures, never fetched.
- Verification permits at most 100,000 emitted commits (`MaxAncestryCommits`),
  comfortably above ordinary PR divergence while independent of repository age.
  A streaming line counter cancels and kills Git on the first excess commit,
  without retaining output. Exactly the budget is allowed only if Git finishes
  successfully. Exhaustion returns incomplete `ancestry_limit` with nil error;
  truncated traversal never establishes completeness. Equal endpoints have an
  empty verification walk; ancestor endpoints require only their divergence.
- Merge-base discovery can explore history before producing output, so discovery
  and verification share a 30-second internal deadline (`MaxAncestryDuration`),
  including when the caller uses `context.Background()`. Deadline exhaustion
  also returns incomplete `ancestry_limit` with nil error. Caller cancellation
  remains `read_failure` with non-nil error. Shallow-state rechecks still surround
  both commands, and observed history mutations take precedence.
- Multiple merge bases, unrelated histories, missing objects and read failures
  fail closed with typed reasons. Invalid IDs, missing objects and read failures
  return non-nil diagnostic errors (transport diagnostics are sanitized). Other
  limitations return nil errors and incomplete coverage. Complete inventory never
  has failure reasons. Callers must inspect coverage even when the error is nil.

Stage 2a emits zero hunks (`MaxDiffHunks = 0`), always with independent incomplete
hunk coverage and `hunks_not_collected`. Stage 2b adds governing-file provenance
and bounded hunk acquisition; a complete inventory is not a complete brief.
The fake reads existing immutable regular-file snapshots without changing its
commit identity or legacy simulator behavior. Mode/gitlink and transport cases
are exercised with local git and an authenticated localhost HTTP git server.

The fake charges 148 bytes plus the path length per raw record, matching its
SHA-256 object IDs. Gitops charges actual Git output: SHA-1 records cost 100
bytes plus the path length; SHA-256 records cost 148. Both then charge each
distinct inspected blob once. Thus identical file contents near 2 MiB can hit
the bound earlier in the fake than in a SHA-1 repository; coverage remains
fail-closed in both. The semantics bounds golden pins the file, byte, hunk, ancestry-commit and
ancestry-duration bounds only; it does not fingerprint the implementation.

The divergence exclusion is sound even with missing objects: suppose a missing
commit K could hide a better common ancestor S than a found merge base C. K
cannot be reachable from C, since then S would be an ancestor of C and could not
be better. Therefore K lies inside the verification walk and is hit. The
C→S→K / C→D / merge(K,D)→B / S→H regression still rejects a missing K
that could make C look like the merge base and conceal H's code revert. Git may
also need to parse commits immediately below an exclusion boundary; those read
failures remain failures. Missing ancestry far below a valid merge base need not
be walked and cannot yield a better common ancestor.
