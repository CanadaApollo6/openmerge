# Dormant classification component (Stage A 4a–4b)

`Classify` performs no I/O and accepts PR 2a/2b's pinned forge DTOs. Typed
`Settings` are component inputs only: the public loader still rejects
`classification` and `approvals`. Forge PR labels have no input here.
The caller supplies the governing **default-branch** commit explicitly,
including for release queues. It must match the governing reader's commit;
it is never inferred from the pinned queue base, merge base or head.

The facets-v3 version pins the literal forge semantics string (v7), independently
of future `forge.DiffSemantics` changes. That string versions
inventory limits, exact-object rename pairing, ancestry proofs, governing file
selection and checked absences, plus bounded hunk semantics. The classifier's
own governing/hunk mapping uses v1 and facet rules use v3. Historical facets-v1/v2
records remain replayable with their recorded meaning. The
version golden must change when any of those rules change.

Paths include both rename sides and deletions. `paths` is every-file matching:
an incomplete inventory is always unknown. `touches` is any-file matching:
an observed hit is true even in an incomplete inventory, while no hit remains
unknown. This asymmetry is intentional and consumed by `changepolicy.MatchRule`.
`docs` and `generated` require every file to qualify, never just the observed
prefix of a truncated inventory. Known policy hits survive all uncertainty.
Binary, unreadable, mode, symlink, gitlink and unpaired rename evidence adds
`unknown`; it does not erase known policy paths. Docs uses paths only, so a
known non-docs path is false even for binary or special files. Generated path
patterns also survive unsafe files; only the attribute half becomes unknown.
On complete inventories a known failing file wins over unknown files. Empty
diffs have docs/generated unknown, policy false, and unknown true. No comment-only detection exists.

`TreeDeriver` implements the `Deriver` seam without I/O. `Generated` resolves
only the attribute half; typed generated path patterns qualify independently.
`Owners` fills both independent owner projections and aggregate/per-file
`owned:` facts. Every governing owner token has explicit false non-matches;
missing tokens remain unknown. `Classify` verifies the CODEOWNERS probe chain
and every ancestor attributes probe (both rename sides and deletion old paths),
regardless of the supplied Deriver. Missing/incomplete coverage is never absence.
It rejects Derivers that mutate inputs, inventory, paths, hunks or other facets,
and forces `unknown` on unresolved code owners or required policy ownership.

`OwnerResolver` closes over supplied PR 3a `UserIdentity`/`TeamIdentity` and
complete, freshly observed membership, using the same stable ID spelling as
policy actors. No network calls occur here. The future acquisition caller must
resolve identities/membership first; a missing/stale resolution blocks the
entry. Emails are never passed to the resolver. Groups have no input here.
The full code-owner pool must resolve, including mixed email/user entries.
Mandatory policy ownership may use the real users in a mixed email/user entry;
email-only, unreadable-team, uncovered and ownerless policy paths block.

CODEOWNERS uses `.github/CODEOWNERS`, `CODEOWNERS`, then `docs/CODEOWNERS`,
advancing only past a definite absent probe. The last matching line wins;
a pattern without owners intentionally clears ownership. Source lines and
invalid-owner diagnostics are retained. User/team names lose `@` and lowercase;
email local parts preserve case, domains lowercase. Patterns are case sensitive:
leading slash anchors, basename patterns search all depths, trailing slash
covers directory contents, `*`/`?` exclude slash, and component `**` crosses
zero or more directories, including runs of three or more stars. Literal leaves
such as `/apps/github` and `**/logs` cover descendants. GitHub explicitly makes
`docs/*` narrower than gitignore; wildcard basename directory propagation (for
example `*.md` on `x.md/b`) is undocumented. If a match depends solely on
propagating a wildcard directory match, that path's ownership and owner labels
are Unknown with a diagnostic. This protects both inherited obligations and
ownerless overrides; direct file matches remain supported. Escaped whitespace
and escaped interior hashes are supported. An unescaped hash inside a token is ambiguous and makes
the file unknown; only a hash at a token boundary begins a comment. Leading
`\#`, negation and ranges are diagnosed
as unsupported, following [GitHub's documented exclusions](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners#codeowners-syntax).
`condition.CompileGlob` is never used for these patterns.

Conservative differences: GitHub skips invalid syntax lines, whereas an invalid
pattern/file makes this derivation unknown, retaining other observations for
explanation. Invalid owner tokens make the affected effective entry unresolved;
they do not silently clear ownership. Files at/over 3 MiB, invalid UTF-8, NULs,
unreadable or non-regular blobs are unknown. A BOM anywhere in CODEOWNERS is
invalid/unknown because GitHub's BOM handling is undocumented. PR 2b's aggregate
1 MiB acquisition
budget can make evidence unknown sooner. Email resolution is intentionally
unsupported and mixed entries fail closed for code-owner rules (ADR 0007).
All sources use the default-branch governing tree, including release queues.

Attributes follow [gitattributes(5)](https://git-scm.com/docs/gitattributes):
deeper governing files override shallower ones; later assignments win per
attribute. Basename/slash/`**` rules, character ranges and C-quoted paths are
supported. Wildmatch operates on bytes (so `?` consumes one UTF-8 byte). Directory patterns do not recurse: use `dir/**`. Negative patterns
are diagnosed. Set and `=true` mean generated; unset (`-`), unspecified (`!`)
and `=false` mean no explicit generated mark. Unspecified resets inherited
marks; no Linguist content heuristic is inferred. Other values are diagnosed
and unknown. The built-in `binary` macro affects text/diff/merge only and cannot
mark a file generated. Custom macro definitions (including overrides of binary)
and unsupported pattern/range syntax are diagnosed and make affected attribute
ancestry unknown. Host/global/info attributes and head contents are never read.
A file-initial UTF-8 BOM is stripped, matching Git; later BOMs are literal.
Lines of 2048 bytes or more become unknown rather than Git's silent ignore,
because ignoring an unset could expose an inherited generated exemption.
C-quoted patterns containing an escaped NUL are diagnosed/unknown: Git
truncates at that byte, so treating it as a literal could miss an override.
Attribute files are parsed/compiled once per `Classify` call (including both
rename sides and diagnostics); caches are never retained across calls. At 4096
derived assignments, remaining input is diagnosed/unknown. Byte classes always
exclude slash, including ranges crossing slash such as `[,-0]`.

`oracle_test.go` uses isolated local Git with an empty HOME, cleared inherited
Git environment, no system/global config/attributes, and no attributesFile or
excludesFile. Its deterministic seed-69 corpus compares 243 attribute inputs
(6,318 path comparisons) and 116 CODEOWNERS patterns (3,016 comparisons),
including both assignment and inherited-override directions. Unsupported
syntax may produce Unknown; every other disagreement fails the test. These
run in the ordinary CI suite and skip only when Git is absent. No forge API
is involved. Parser and Evaluate regressions also live in `parsers_test.go`
and `derive_test.go`.

Parser resource limits are conservative Unknown boundaries: CODEOWNERS has at
most 1024 nonempty entries, 2048 source bytes per entry, 32 owner tokens per
entry, 256 distinct canonical owner tokens, and 128 bytes per owner token.
Oversized tokens leave their entry unresolved; other budget excesses invalidate
the file. At the forge's 1000-file inventory limit and two rename sides per
file, LastMatchingEntry contributes at most 4,096,000 raw bytes. Owner label
keys contribute at most about 36 MB raw across aggregate/per-file maps
(256 × at most 140 bytes × 1001); JSON framing adds overhead. Source diagnostics,
paths, membership IDs and resolver reasons are additional. These are component
bounds, **not** a whole-record wire budget; PR 7a must enforce that budget and
bound memberships before activation.

GitHub.com actor keys are exactly `github.com:user:<positive decimal ID>`,
without leading zeros, constructed only by `GitHubUserID`. Resolver pools,
PR 6's Actors map, grants and stances must all use this helper. Team members
produce user keys; logins, team IDs and email addresses are never actor IDs.
Other forge hosts require an explicit separate namespace before support.
`IdentitySet.Reason` is free-text diagnostic evidence: `SemanticInputs` already
excludes it, and PR 7a must exclude it (including fresh eligibility reasons)
from every semantic policy/brief/receipt digest. Do not hash whole Evidence.

**Hard gate for PR 6/9:** classification-time owner pools are snapshots, not
fresh eligibility at evaluation/landing. Acquire fresh user/team membership,
then call pure `ResolveOwnerEligibility(storedEvidence, freshResolver)` using
the stored default-branch governing files, and replace both owner projections
and both known flags in a detached decision input. Never union old and new
pools or let cached Fresh=true authorize a later decision. Missing/incomplete/
stale membership must block. Do this immediately before both Evaluate and
landing, without re-reading the governing forge tree or changing generation,
facets or receipt bindings. The helper leaves durable evidence untouched.
Started attempts keep their frozen decision evidence as required by ADR 0007.

Config provenance mirrors `config.Find`: it accepts exactly one of the four
config candidates. There is **no precedence winner when two exist**: ConfigBlob
is empty and `unknown` explains ambiguity; missing/incomplete config probes do
the same. All four paths remain policy-sensitive. Composite actions under
`.github/actions/**` are **not** automatically policy; users must list that path
in future `classification.sensitive` settings when those actions need protection.
Typed `Settings.Sensitive` exercises this behavior before public activation.

`Evidence` carries the shared classification and bounded governing DTO.
`ChangeClassified.Evidence.Classification.Files[].Hunks` is the durable hunk
subrecord, not a reference to a future event or an unbounded patch. Its omitted
ranges, summary counts and enclosing spans are copied verbatim. Aggregate
`HunksComplete` remains independent of `InventoryComplete`; complete paths are
not a complete brief. PR 7a owns the whole-record transport/size budget.

`InputFingerprint` identifies recorded acquisition inputs, including provenance.
It is deliberately **not** a policy or brief digest. `RefreshReasons` and
`SemanticInputs` exclude config blob and governing commit provenance, while
retaining semantic policy, owner/attribute content and classifier versions (Q7).
Provenance-only refreshes must preserve generation and the future brief digest.
The caller's ancestry observation distinguishes ordinary forward predecessor
landings from rewrites/rewinds (Q2). `NextGeneration` advances on a rewrite even
when the new diff is equal; an unchanged head is insufficient. The landing gate
will consume recorded evidence without re-diffing; production integration waits
for 11b. Started attempts keep their frozen decision evidence.

At the queue read boundary use `BoundPRRevision` with `PRChangeID` derived from
the stored canonical queue key. Legacy `PRRevision` may contain an empty ID.
`BoundBatchRevision` preserves the batch's tested head and revision rather than
reading the latest PR revision. Both reject revision zero; absence is unrecorded.

The new event is wire v1 with a strict nested presence/unknown-field decoder.
Its shared DTO field spelling and non-null wire collections are pinned by one registry
historical golden; complete/incomplete and current facets-v3 examples (alongside historical facets-v2) live under `variants/`, outside the glob.
Snapshots advance to v4 and retain v2/v3 decoding. The classification projection
is immutable and keyed by change and revision, with durable refresh generation.
It does not advance planner revisions or create effects. Compaction stays off.

Wire and snapshot validation share the version allowlist and nested enum,
truth, label/token, object ID, path, mode, omission count/range, path projection
and hunk completeness checks. Empty object IDs retain explicit absence or
unavailable provenance; they never prove absence without the governing probe.
Historical facets-v1 permits its original unpaired-rename flag; facets-v2/v3 check
path-derived rename coherence. 11a must still enforce whole-record resource
budgets and cross-check derivation against acquired evidence before activation.
Workflow policy paths are only direct `.github/workflows/*.yml`/`*.yaml` files.
