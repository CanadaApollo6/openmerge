# ADR 0010 slice 1 (Inert)

This package consumes supplied strings and observations. It has no acquisition,
network, clock, journal or queue calls. No production package imports it.
`LoadCandidate` projects `provenance` out and validates the remaining document
with `config.Load`, following ADR 0008's candidate-only pattern. The public
loader, public schema, and frozen condition-evidence vocabulary reject these
additions. Candidate condition parsing retains types on the compiled expression
without registering new public attributes. Resolver-dependent attributes can be
compiled here but remain unavailable to public configuration.

The owner's 2026-10-27 acceptance is the authority for this implementation;
marking the ADR Accepted is a separate docs PR.

## Fixed parser choices

The deployed Git binary is the reference trailer-block parser. Slice 1 exposes
`ParseGitTrailers`, which takes the colon-separated, already unfolded output of
`git interpret-trailers --parse`, not a commit message. `IdentityInput.ParsedTrailers`
uses that same output for coauthors. No block locator, heuristic threshold,
comment skipping or continuation folding is implemented here. Git invocation
at runtime belongs to slice 2 (inventory); no production package calls this API.

The test-only adapter in `internal/gitops/provenance_trailers_test.go` reuses
`session.run` and `gitEnv`: inherited `GIT_*` variables (including config
injection) are removed, `GIT_CONFIG_NOSYSTEM=1` disables system config, and
`GIT_CONFIG_GLOBAL` points to the empty `os.DevNull`. It parses in a fresh bare
repository initialized with `--template=`, excluding source-repository local
config, includes and named `trailer.*` aliases. It pins
`-c trailer.separators=:` and both `-c core.commentChar=#` and
`-c core.commentString=#` (the latter for newer Git). `--parse` supplies
`--only-trailers --only-input --unfold`; no trailers are added or commands run.
This combination leaves no ambient trailer settings to alter the result.
Slice 2 must reuse this isolation, including the absence of source-local config,
and include the actual Git version in the effective parser policy identity.
Tests log the deployed version; fix round 1 ran with **git version 2.56.0**.
See [Git's parser documentation](https://git-scm.com/docs/git-interpret-trailers).

Provenance parsing passes the original message bytes to Git. It deliberately
does **not** apply gitops metadata's `stripMetadataLines` or
`rewritePatchDividers`. Those functions prepare attribution for newly generated
merge messages; they change block eligibility, continuation ownership and patch
boundaries. Removing a reserved line can also remove its continuation bytes.
Leaving both untouched preserves every declaration in Git's original trailer
set and never hides one through a rewrite. A declaration after Git's patch
divider remains outside that set. Slice 2 must read original commits, rather
than feed generated/reformatted messages to this extractor.

Named regressions cover cherry-pick markers, patch dividers with text or
trailing whitespace, and URLs in prose. The differential corpus compares
1,152 LF/CRLF messages, including continuations, comments, mixed-case keys,
duplicates, coauthors and Signed-off-by casing, against exactly Git's output.
A hostile system/global/environment config test pins the isolation contract.

Keys normalize to lowercase; values preserve Git's emitted bytes after the
single formatting space following the colon. In particular, CR bytes retained
by Git while unfolding CRLF continuations remain invalid evidence, not stripped
into valid values. Identical unfolded values deduplicate; distinct values remain
separate. Entire IDs are 12 lowercase hex
or 26 canonical uppercase Crockford digits with first digit 0–7. Neutral
sessions follow the ADR's exact ASCII grammar. Recognized invalid trailers
still contribute agent evidence. Values over 161 bytes (the maximum valid
neutral declaration) retain a SHA-256 and `value_too_long`; invalid UTF-8 retains
a SHA-256 and `invalid_encoding`. Neither becomes a truncated valid value.
The slice 2 inventory reader will enforce acquisition and declaration-count
budgets; this string parser makes no completeness claim about truncated input.

## Identity choices

`mailbox-classes-v1` trims only outer ASCII space/tab and lowercases ASCII local
and domain parts. Bare mailbox input must contain exactly one `@`, nonempty
parts, printable ASCII, no whitespace, display-name delimiters, comments,
quotes, comma, semicolon, colon or backslash. Domains contain only ASCII letters,
digits, dots and hyphens. GitHub `[bot]` noreply local parts are supported.
No dot/plus-tag removal, substring matching or account resolution occurs.
Configured bot accounts therefore need an explicit mailbox rule.

Author and original committer rules can contribute human or agent evidence.
Coauthors contribute only configured agent matches, as the ADR's explicit
coauthor exception specifies. Conflicting class rules retain both rule IDs and
yield mixed; they never resolve by order. The trusted caller excludes generated
OpenMerge committers using its build mapping. Contributor replay trailers
cannot set that flag. These matches never yield a forge identity or permission.

## Candidate and evaluation contracts

`testdata/provenance.candidate.schema.json` describes the additions-only
projection; schema tests combine it with full public-projection validation.
Approval opt-in and group references remain semantic parser checks. Raw candidate
input rejects aliases, YAML merge keys and explicit tags, following the earlier
policy candidate parser. It bounds the document to 10,000 nodes and depth 64.
Both `when` and `require.conditions` accept an implicit AND list, atom, or
logical group. Nested operators use the existing grammar; NOT takes one atom
or group. `provenance` uses exact equality/inequality against the four classes.

`DeclarationAttributes` produces only the two declaration-derived attributes.
Incomplete inventories/extractors yield missing typed values, never known zero.
Resolver attributes remain unknown in declaration-only evaluation. The generic
condition evaluator accepts separately supplied resolution observations when
needed for pure tests; there is no resolver in this slice.

`EvaluateRequirements` takes a baseline snapshot and independently evaluated
ADR 0007 approval results, indexed by rule. It preserves baseline approval
eligibility for every trigger. Only a rule's own `require.conditions` can see
its local pool unioned with baseline eligibility; unknown baseline membership
stays unknown. All other inputs retain their baseline values. False triggers
add nothing; pending triggers pass only with a true obligation. Both ADR
reviewer-eligibility cases, their pending-membership variants, rule reordering,
repeated evaluation, and cross-rule pool isolation are pinned in pure tests.
Slice 4a must construct these inputs from one observation generation and wire
policy/review exclusions, default preservation, vetoes and gates into runtime.

## Declaration digest v1

`CanonicalDeclarations` emits compact Go JSON, with fixed struct-field order,
HTML escaping, valid UTF-8, and no trailing newline. The top-level fields are
`Version`, `Commits`, `Rules`; version is `openmerge-declarations-v1`.

Commits sort by object ID and encode `ObjectID`, `Class`, `Declarations`.
Incomplete inventories, missing IDs and duplicate originals are rejected.
Declarations encode the exported `Declaration` fields in declaration order:
`Source`, `Version`, `Kind`, `Value`, `ValueDigest`, `Outcome`, `Class`, `RuleID`.
They sort lexically by Go's `%q` encoding of that field tuple, then deduplicate.
Empty sets encode as `[]`.

Rules whose trigger is not definitely false with declaration-derived inputs
are encoded as compact JSON strings with `When`, `Conditions`, `Approvals`.
Expressions use the shared evaluator's stable display spelling. Absent
conditions/approvals encode as null. Approval requirements use ADR 0007 field
order and canonicalize `human` to `any_writer` plus the human filter. Rule
strings sort lexically and deduplicate. Logical child order is preserved.

`DeclarationDigest` is lowercase SHA-256 of those bytes. Independent Python
JSON/hashlib vectors pin empty, undeclared and agent inventories, including a
mutable-review trigger which must remain bound. Resolution results, ref tips,
metadata IDs and current approvals are not accepted by the digest API. Parser
and classifier policy versions must additionally enter the future effective
policy digest; existing config/policy digest code and vectors stay unchanged.
