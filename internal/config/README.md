# Repository configuration

`config.Find(read)` checks the four paths from ADR 0003. The reader reports
existence separately from read failures, so an empty file still counts toward
ambiguity. Repository readers must fetch from the base branch. The validation
CLI reads local files so authors can check edits before merging them.

`config.Load(data)` returns `(Config, []Diagnostic, error)`. Errors are a
`config.Errors` slice with one-based YAML lines and paths such as
`queues[0].enter_when[1].or[0]`. Unknown keys, including nested keys, get an edit
distance suggestion. Invalid input returns an empty config. YAML aliases retain
the anchor definition's line numbers. YAML syntax errors without a position from
the parser use line 1. Multiple YAML documents, duplicate keys, cyclic aliases,
and excessive expansion (100 levels or 10,000 expanded nodes) are rejected.
YAML merge keys support mapping/sequence inheritance and explicit overrides.

Version 1 requires exactly one named queue. Defaults are:

- `merge_method: squash` (also accepts `merge` and `rebase`).
- `check_timeout: 1h`; `0`, `"0"`, or `0s` disables it. Durations must be
  nonnegative Go duration strings, with unquoted integer zero also accepted.
- `enter_when: []` and `merge_when: []`, both true.
- `batch: {max_size: 1, max_wait: 0s}`. Sizes 1 through 128 are accepted.
  A larger size or positive wait produces a warning: Phase 1 always executes
  serially without a batch wait. The values remain in the loaded config.

`Queue.QueueConfig()` supplies planner defaults of `ResolveAfter: 1m`,
`RefreshInterval: 1m`, `BuildRetries: 3`, and `BuildBackoff: 5s`. The backoff
doubles on consecutive build failures. These operational settings are deliberately
not new YAML keys in Phase 1.

## Conditions

`condition.Parse(text)` compiles an immutable expression. Evaluate it against a
`condition.PR`, whose `Attribute(name)` returns a value made with `String`,
`Strings`, or `Bool`. `condition.Snapshot` is a map implementation. An adapter
must supply a consistent snapshot; `head` is the branch name, not a commit SHA.
Use an empty `Strings()` value for a known empty list. A missing or mistyped
attribute fails an atomic condition, even with the `-` prefix. Logical NOT
negates its child's result; adapters should supply every referenced attribute.

Phase 1 attributes are:

- Strings: `base`, `head`, `author`, `title`.
- Booleans: `draft`, `conflict`.
- Lists of strings: `label`, `approved-reviews-by`, `files`, `check-success`,
  `check-failure`, `check-pending`.

Strings and lists accept `=`, `!=`, `~=`, and `glob`. Lists match any element;
`!=` succeeds only when no element equals the value, including on empty lists.
`#` takes list length and accepts integer comparisons `=`, `!=`, `<`, `<=`, `>`,
`>=`. Booleans accept a bare name or `= true`, `= false`, `!= true`, `!= false`.
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

`Expression.Evaluate(pr)` returns `(bool, []string)`. Success returns nil failures.
AND collects failed children in source order. Failed OR/NOT groups are reported
as a whole, for example `or (draft; conflict)` or `not (draft)`, so reports retain
the logical meaning. Atomic reports use normalized strings such as
`#approved-reviews-by >= 1`. These strings are stable and human-readable.

## Dependency and scope

The sole new dependency is `go.yaml.in/yaml/v3 v3.0.5`, the current stable YAML
organization module. The standard library does not parse YAML. This parser
provides syntax handling, source nodes, anchors, and aliases; custom validation
adds the repository's schema paths and suggestions. The archived
`gopkg.in/yaml.v3` path is not used. At implementation time the v4 module's latest
tag is `v4.0.0-rc.6`, so this PR uses stable v3 as requested. See the
[maintainer's version policy](https://github.com/yaml/go-yaml#version-intentions).

The ADR example in `testdata/openmerge.yml` is byte-identical to ADR 0003 and
validates with the expected batching warning. Its comment also lists
`fast-forward`; Phase 1 accepts only the three methods in `queue.MergeMethod`.
The ADR leaves group operand shapes, glob details, and operational defaults
unspecified; the rules above make those choices explicit. The ADR is unchanged.

JSON Schema generation is a follow-up. Template substitution, importing,
migration, and multi-queue execution are outside this PR's Phase 1 surface.
