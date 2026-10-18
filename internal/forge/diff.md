# Pinned revision evidence (ADR 0007 Stage A 2a–2b)

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

`git-evidence-v5-single-span` retains the inventory-v4 protections and fixes these semantics:

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
  `rev-list --parents <base> <head> --not <unique merge base>` against raw
  parents. Multiple merge bases return `multiple_merge_bases` before this walk.
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

## Bounded hunks

After inventory blob inspection, the reader computes unified patches from the
same immutable raw blobs, without another fetch. Both fake and gitops use the
same isolated Git process so their matching semantics cannot drift. Git runs
outside the repository in a private directory under the user cache, with neutral
`old`/`new` filenames and a discovery ceiling. No repository `.gitattributes`,
info attributes, local config, or diff drivers are reachable. Its fresh environment
retains only PATH and explicitly sets `LC_ALL=C`, `GIT_CONFIG_NOSYSTEM=1`,
`GIT_CONFIG_GLOBAL=/dev/null`, `GIT_ATTR_NOSYSTEM=1`, and the discovery ceiling;
inherited Git environment/config injection is discarded.

The invocation is `git -c diff.algorithm=myers -c diff.indentHeuristic=false
-c core.attributesFile=/dev/null diff --no-index --no-ext-diff --no-textconv
--no-color --no-renames --diff-algorithm=myers --no-indent-heuristic --minimal
--inter-hunk-context=0 --unified=3 -- old new`. Three context lines match the
usual review UI. Minimal Myers disables shortcuts that can discard repeated
unchanged interior blocks. Separated changes produce separate hunks; unchanged
lines are context, never artificial deletion/addition pairs. Ranges use unified
zero-length conventions; body text preserves CRLF and missing-newline markers.

Retention matches PR 7a: **1,000 files**, **2 MiB UTF-8 body text total**, and
**200 emitted body lines per file**, counting context and missing-newline annotation lines. Files are in deterministic path order. There is no
separate hunk-count cap. A hunk that cannot fit is omitted whole with its exact
old/new ranges and typed `hunk_line_limit` or `hunk_byte_limit`; later smaller
hunks/files may still fit. Nothing is silently truncated.

Computation is independently bounded: at most **2 MiB combined input per file**,
**2 seconds per Git process**, **30 seconds shared per collection**, and bounded
patch output (8 times the input cap plus 4,096 bytes). Work exhaustion is incomplete
`hunk_work_limit` with unknown zero ranges, never complete. Existing inventory
limits still independently cap inspected blobs. Invalid UTF-8 is `invalid_utf8`;
binary/unknown content is `non_text`. Symlinks, gitlinks and mode changes are
`special_mode`, with unknown omission ranges. Adds/deletes have empty absent
sides; exact same-mode renames have complete empty hunks. Unpaired renames keep
observed content but incomplete inventory and aggregate coverage.

`DiffFile.HunkCoverage`, `Hunks` and `Omitted` are per-file evidence. Aggregate
`RevisionDiff.Hunks` also requires complete inventory. Unvisited files have
unknown coverage; neither unknown nor incomplete means an empty change. Read
errors and caller cancellation remain diagnostic `read_failure` errors. The
history check runs again after hunk construction, before reporting completeness.

## Governing files

`GoverningFilesReader` is a separate optional capability; GitHub constructs it
with `Driver.GoverningFilesReader(ctx, cacheRoot)`, using the same installation
cache and credentials as the diff reader. Its request pins a **commit** ID and
supplies changed paths from both rename sides, deletions included, together with
explicit path-set completeness. The commit must be the governing default-branch commit per ADR Classification,
including release queues. 4a/serve selects it; it is not automatically the diff
base or merge base. This reader does not resolve a default branch. The
existing `Driver.ReadFile` accepts a commit pin but returns only bytes, and its
contents API cannot provide bounded raw content, modes and tree-proven absence
together. The git-backed fallback uses hardened diffRead sessions for all
`ls-tree`/`cat-file` operations, with literal pathspecs, bounded output, preflight
blob sizes, no replacement/graft/commit-graph/lazy-fetch effects, and history
rechecks before complete results. It fetches only the explicit governing commit
through the existing cache lifecycle. No API pagination is involved.

Results retain the governing commit, semantics and the path/mode/object ID,
presence, content and coverage of each checked location:

- All four accepted config locations, in `config.Find` order. Parsing and
  rejecting ambiguous configurations remain the consumer's responsibility.
- `.github/CODEOWNERS`, `CODEOWNERS`, then `docs/CODEOWNERS`, stopping at the
  first present entry. `SelectedCodeowners` records the selection. Checked
  earlier absences are retained; unprobed later locations prove nothing. An
  unreadable or special first entry never falls back to a weaker owner file.
- Root `.gitattributes` and one for each ancestor directory of every supplied
  path, deduplicated and sorted. This includes old rename directories and deleted
  paths. The request's incomplete path set makes the result incomplete too.

At most **256 probes**, including absent files, and **1 MiB content** per request
bound tree work and retained policy content independently of diff size. They
allow ordinary nested repositories while failing closed on large/deep layouts.
Path inputs are also capped at 2,000 entries and 2 MiB. Deduplicated ancestor
probe paths have their own 2 MiB bound (reusing `MaxDiffBytes`), so long shared
prefixes cannot amplify metadata without a limit. Directory enumeration also
stops at the probe bound. Exhaustion is incomplete `file_limit`/`byte_limit` with
nil error. An oversized present blob retains object ID and mode but no content.
Non-UTF-8 blobs have no usable content and are incomplete `invalid_utf8`.
Incomplete probes do not stop later config/attribute diagnostics within budget;
CODEOWNERS still stops at the first present location.
Only a complete absent probe proves absence; tree entries such as directories,
symlinks and gitlinks are present but incomplete `special_mode`. Invalid IDs,
missing commits and read failures follow the diff's non-nil error contract.

## Consumer mapping and version discipline

PR 4a combines diff endpoints/merge base with the separate governing commit,
config/owner blob IDs and attribute path-to-object map in
`changepolicy.ClassificationInputs`. It converts octal modes into
`FileEvidence` modes, copies paths/blobs/rename/binary/gitlink observations, maps
`DiffHunk` to `Hunk`, `DiffHunkRange` to `HunkRange` (typed reason to string), and
maps per-file coverage to `HunkEvidence.Complete`. Aggregate hunk coverage feeds
`Classification.HunksComplete`. Governing content and checked absences go to
4a/4b's parsers; missing probes must never become absence. Forge imports no
changepolicy types and performs no classification or policy selection.

The fake observes existing immutable regular-file snapshots, including compact
snapshots, without changing commit identities or legacy simulation behavior.
The shared `forgetest` contracts run against fake, local git, and GitHub's
localhost authenticated HTTP git transport. Mode/gitlink and history mutation
cases use local git.

The fake charges 148 bytes plus the path length per raw record, matching its
SHA-256 object IDs. Gitops charges actual Git output: SHA-1 records cost 100
bytes plus the path length; SHA-256 records cost 148. Both then charge each
distinct inspected blob once. Thus identical file contents near 2 MiB can hit
the bound earlier in the fake than in a SHA-1 repository; coverage remains
fail-closed in both. The semantics bounds golden pins every inventory, ancestry, governing and hunk
bound; it does **not** fingerprint behavior. Any change to rename pairing,
binary detection, ancestry trust, file selection/absence, ordering, hunk shape,
encoding or completeness must bump `DiffSemantics` and add/update behavioral
vectors, even when bounds stay constant. A bounds change also requires a bump
and a new golden entry; never edit an existing version's golden to accept drift.
Consumers include this version in their classifier version. Reviewers must
check semantic changes explicitly; a passing bounds golden alone is insufficient.

The divergence exclusion is sound even with missing objects: suppose a missing
commit K could hide a better common ancestor S than a found merge base C. K
cannot be reachable from C, since then S would be an ancestor of C and could not
be better. Therefore K lies inside the verification walk and is hit. The
C→S→K / C→D / merge(K,D)→B / S→H regression still rejects a missing K
that could make C look like the merge base and conceal H's code revert. Git may
also need to parse commits immediately below an exclusion boundary; those read
failures remain failures. Missing ancestry far below a valid merge base need not
be walked and cannot yield a better common ancestor.
