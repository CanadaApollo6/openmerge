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

ADR 0010 requires Git trailer semantics without defining every Git default.
`git-trailers-v1` fixes colon separators, ASCII case-insensitive keys, LF/CRLF,
a final nonblank paragraph after the subject, and space/tab continuations
unfolded with one ASCII space and trimmed at the edges. It fixes Git's default
column-zero `#` comment prefix and `---` patch divider. A block must consist
entirely of trailers/continuations, or include Git's built-in `Signed-off-by`
and at least 25% trailer lines. The custom declaration keys do not make mixed
prose a trailer block. Display examples indented or outside the final block
are not declarations. No Git config, hooks or external parser is read or run.

Keys normalize to lowercase; values preserve case. Identical unfolded values
deduplicate, distinct values remain separate. Entire IDs are 12 lowercase hex
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
