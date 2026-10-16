# Repository configuration

`config.Find(read)` checks the four paths from ADR 0003. The reader reports
existence separately from read failures, so an empty file still counts toward
ambiguity. Repository readers must fetch from the base branch. The validation
CLI reads local files so authors can check edits before merging them.

`config.Load(data)` returns `(Config, []Diagnostic, error)`. Errors are a
`config.Errors` slice with one-based YAML lines and paths such as
`queues[0].enter_when[1].or[0]`. Diagnostics are sorted by line. Unknown keys,
attributes, and operators get a spelling suggestion only within
`max(1, word length / 3)` edits; adjacent transpositions count as one edit.
Invalid input returns an empty config. YAML aliases retain
the anchor definition's line numbers. YAML syntax errors without a position from
the parser use line 1. Multiple YAML documents, duplicate keys, cyclic aliases,
and excessive expansion (100 levels or 10,000 expanded nodes) are rejected.
Duplicate keys are rejected within every source mapping before merges are
flattened, including unused overridden values. A merge key must be a scalar
whose value is exactly `<<`; a `!!merge` tag on another key is invalid.
Mapping/sequence inheritance and explicit overrides between distinct mappings
are supported.

`openmerge config validate [path]` prints exactly `ok` on stdout for valid input.
Warnings and errors go to stderr. Discovery errors list every found file or all
four searched paths, without inventing a file/line location. `--help` prints usage.

Version 1 requires exactly one named queue. Its `merge_when` is a conjunction
(the condition list, flattening nested `and` groups) with at least one positive
`check-success = <non-empty name>` term directly in that conjunction. Checks
under `or`/`not`, counts, exclusions, and patterns do not supply this requirement.
Additional terms may use `or`/`not`. For example:

```yaml
merge_when:
  - check-success = ci
  - or: [base = main, base = release]
```

Loading also evaluates the condition with missing checks and with failed checks;
either passing result rejects the configuration. Invalid conditions report the
queue name and an example fix. Phase 1 has no opt-out. Defaults are:

- `merge_method: squash` (also accepts `merge`, `rebase`, and `fast-forward`).
- `check_timeout: 1h`; `0`, `"0"`, or `0s` disables it. Durations must be
  nonnegative Go duration strings, with unquoted integer zero also accepted.
- `enter_when: []`, true. `merge_when` requires a positive named success check.
- `batch: {max_size: 1, max_wait: 0s}`. Sizes 1 through 128 are accepted.
  The queue tests up to `max_size` PRs together, with one active batch at a time.
  A partial batch waits until its oldest member has waited `max_wait`; size 1
  starts immediately. Failing batches are bisected serially, preserving passing
  PRs. Pending bisection groups run before fresh work without waiting.

`Queue.QueueConfig()` supplies planner defaults of `ResolveAfter: 1m`,
`RefreshInterval: 1m`, `BuildRetries: 3`, and `BuildBackoff: 5s`. The backoff
doubles on consecutive build failures. These operational settings are deliberately
not new YAML keys in Phase 1.

## Conditions

`condition.Parse(text)` compiles an immutable expression. Evaluate it against a
`condition.PR`, whose `Attribute(name)` returns a value made with `String`,
`Strings`, or `Bool`. `condition.Snapshot` is a map implementation. An adapter
must supply a consistent snapshot; `head` is the branch name, not a commit SHA.
Use an empty `Strings()` value for a known empty list.
`Expression.Attributes()` returns a sorted, unique copy of all referenced names,
including those under negation and in every alternative, so forge adapters can
supply every attribute before evaluation.

Evaluation has three results: met, not met, and unknown. Missing or mistyped
attribute data makes an atom unknown. Both `-` and `not` preserve unknown.
AND is not met if any child is not met; otherwise it is unknown if any child is
unknown, and met if all are met. OR is met if any child is met; otherwise it is
unknown if any child is unknown, and not met if all are not met. A condition list
is implicit AND and admits or merges only when met. Unknown never passes.
`condition.CheckResults(success, failure, pending)` supplies per-check unknown
values for missing or pending CI. Logical groups use the same three-valued
evaluation: a failed OR alternative cannot fail the batch while another can
still pass; only a definitely false whole merge condition is terminal failure.

Phase 1 attributes are:

- Strings: `base`, `head`, `author`, `title`.
- Booleans: `draft`, `conflict`.
- Lists of strings: `label`, `approved-reviews-by`, `files`, `check-success`,
  `check-failure`, `check-pending`.

Serve observes every attribute above. Labels come from the PR; `files` contains
the changed filenames (including deleted paths, and the new path of a rename).
File reads follow pagination and are capped at 1,000 paths. Above that cap the
entire attribute is unknown: a matching path in a partial list cannot make a
condition pass or fail, including `!=`, `#files`, `-`, and `not`. An observed
empty list is known. `conflict` is the inverse of GitHub's nullable `mergeable`;
null stays unknown and is retried even when the PR's update timestamp is unchanged.
Head/base changes while reading file pages reject the mixed observation and retry.
Current-head approvals supply `approved-reviews-by`. Entry checks read the PR
head; merge checks read the exact tested batch SHA. `head` is GitHub's
`owner:branch` label, falling back to the branch ref.

Changed non-CI inputs are journaled as `pr_metadata_observed` alongside
`pr_observed` eligibility/trust, using an additive wire-v1 event with a golden
fixture. Existing persisted event/action formats are unchanged. Core attribute
names and condition syntax remain Mergify-compatible; there is no importer yet.
Changing an active member's metadata revokes cached merge permission and
rechecks its merge conditions, even if the tested SHA's CI is still green.

Strings and lists accept `=`, `!=`, `~=`, and `glob`. Lists match any element;
`!=` succeeds only when no element equals the value, including on empty lists.
`#` takes list length and accepts integer comparisons `=`, `!=`, `<`, `<=`, `>`,
`>=`. Conditions starting with `#` must be quoted to avoid YAML comments.
Booleans accept a bare name or `= true`, `= false`, `!= true`, `!= false`.
Prefix `-` negates an atomic test, for example `-#label >= 2`.

Regexes use Go's RE2 syntax and match substrings unless explicitly anchored.
Globs match whole slash-separated paths: `*` and `?` stay in a segment, `**`
crosses slashes, and `**/` also matches zero directories. Backslash escapes the
next character; all other characters, including brackets, are literal. Both
regexes and globs are compiled during loading. Values are the trimmed remainder
of a condition string; YAML quoting is sufficient, with no inner quote syntax.

YAML lists mean AND. Each group mapping contains exactly one key: `and` or `or`
takes a list, and `not` takes one condition string or group mapping. Empty AND
is true; empty OR is false. For example:

```yaml
enter_when:
  - not:
      or:
        - draft
        - conflict
  - "#approved-reviews-by >= 1"
```

`Expression.Evaluate(pr)` returns `(bool, []Failure)`: true only for met, with nil
failures on success. `Failure` has lowercase JSON fields `condition`, `attribute`,
`actual`, `missing`, `unknown`, and `children`. Atomic failures name the attribute
and carry its actual string, boolean, or list elements; a `#` comparison carries
the actual list length. Missing data has `actual: null`, `missing: true`, and
`unknown: true`; mistyped data retains its actual value with `missing: false`
and `unknown: true`. Empty lists are reported as `[]`.

AND collects failed children in source order. Failed OR/NOT groups retain the
whole condition and nested `children` evidence, including the met operands that
cause a NOT to fail. Group `unknown` describes that group's result; its children
can contain unknown data even when an AND is definitively not met. Group actual
values are null. `Failure.String()` returns its condition. Normalized expression
strings quote string operands, for example `or (title = "a; b"; draft)`, so values
cannot be confused with group separators. These display quotes do not add an
inner quoting syntax to the input grammar.

## Dependency and scope

YAML parsing uses `go.yaml.in/yaml/v3 v3.0.5`, the stable YAML
organization module. The schema test validator is described below. The standard library does not parse YAML. This parser
provides syntax handling, source nodes, anchors, and aliases; custom validation
adds the repository's schema paths and suggestions. The archived
`gopkg.in/yaml.v3` path is not used. At implementation time the v4 module's latest
tag is `v4.0.0-rc.6`, so this PR uses stable v3 as requested. See the
[maintainer's version policy](https://github.com/yaml/go-yaml#version-intentions).

The ADR example in `testdata/openmerge.yml` is byte-identical to ADR 0003 and
validates without warnings. The example uses squash; supported
methods are merge, squash, rebase, and fast-forward.
The ADR leaves group operand shapes, glob details, and operational defaults
unspecified; the rules above make those choices explicit. ADR 0003 also records
that missing or mistyped data remains unknown under negation.

## JSON Schema

The hand-maintained [version-1 JSON Schema](../../schema/openmerge.v1.json) uses
JSON Schema 2020-12, rejects unknown properties, and covers all loader keys,
types, method enums, defaults and nested condition groups. It is embedded from
that one file: `openmerge config schema` prints it byte for byte. Editor hint:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/CanadaApollo6/openmerge/main/schema/openmerge.v1.json
```

Use a comment; `$schema` is not a configuration key. For offline editors,
`openmerge config schema > openmerge.v1.json` exports a local copy.

`schema_test.go` validates repository config fixtures, full YAML examples in
Markdown, defaults/overrides, the loader's invalid-config corpus, and positive
and negative merge-check cases through both the loader and a JSON Schema
validator. Required checks are expressed with recursive `contains` restricted
to `and` groups. The loader additionally evaluates missing/failed-check rejection.
Patterns document the atom syntax and Go duration spelling. JSON Schema cannot
compile Go RE2/globs or enforce Go integer/duration range exactly. The test
validator asserts `openmerge-condition` and `openmerge-duration` formats with
the loader's atom parser and `time.ParseDuration`; editors may ignore these
formats. Always run `openmerge config validate` for complete validation.

YAML-only rules (one document, aliases/merges, duplicate keys, strict scalar tags,
resource limits) run before schema validation. In particular, YAML `1.0` is a
float and fails the loader even though JSON Schema treats JSON `1.0` as an
integer. Only literal unquoted `0` is accepted as an integer duration.

The test-only validator is the pure-Go, Apache-2.0
[`github.com/santhosh-tekuri/jsonschema/v6`](https://pkg.go.dev/github.com/santhosh-tekuri/jsonschema/v6).
It supports 2020-12 without a runtime process or network fetch and shares the
already-present `x/text` dependency. The production loader retains source-line
diagnostics and does not depend on the JSON Schema validator.

## Fast-forward

`merge_method: fast-forward` publishes the unchanged last head as the test ref.
Only consecutive heads forming a strictly increasing ancestry chain can batch;
the longest linear prefix is selected before the push, with remaining PRs kept
in queue order. Independent heads start singly. A head that does not strictly
extend the base is dequeued with `fast-forward-required`, asking the author to
update the branch and queue again. There is no automatic rebase. After a base
move the batch is rebuilt and ancestry is checked against the new tip. An
unchanged head may be tested again after an abandoned attempt or failed batch;
the landing attempt's unique fence, rather than a rewritten batch trailer,
identifies the atomic write. No commit attribution changes are made. Confirmed
landings use the existing head-branch cleanup protocol.

Template substitution, importing, migration, and multi-queue execution remain
outside the Phase 1 surface.
