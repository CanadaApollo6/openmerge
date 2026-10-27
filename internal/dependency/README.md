# Dependency candidates (ADR 0011 slice 1, Inert)

This package is not imported by production code. It accepts immutable supplied
facts and performs no network, forge, database, journal or filesystem reads.
It adds no public configuration, wire payload, event kind or planner gate.
A structurally valid graph grants no permission to test, enqueue or land.

The candidate per-queue value is:

```yaml
dependencies:
  mode: ordered
  allow_from: [https://github.com/acme/api]
  allow_to: [https://github.com/acme/client]
```

`ParsePolicyCandidate` consumes only the mapping **inside** `dependencies`.
`testdata/policy.candidate.schema.json` describes that component, not a public
configuration. Missing policy disables dependencies. Missing or empty lists
grant no edges. Both endpoints need ordered policy and strict verification.
`ValidatePolicyScope` and `BilateralGrant` resolve every grant against the
supplied served registry in one GitHub installation and organization. Grants
are exact canonical repository URLs with no wildcards or transitive consent.
`single_maintainer` is deferred to the declaration-hold slices, with no candidate
exception or acknowledgement behavior in this package.

## Pure API boundaries

- `ParseReference` accepts the versioned canonical URI and rejects foreign forge,
  installation and journal namespace before any ID lookup. Namespace generation
  and incarnation binding belong to slice 2; this API accepts an already bound
  scope and never manufactures one from declaration text.
- `ParseIntent` consumes the authoritative PR body, including trailer-shaped
  lines in that body. It never accepts a list of commit messages or PR comments.
  The raw body is capped at 65,536 Unicode characters before parsing (including
  line endings and any BOM); overflow returns `ErrBodyLimit`, never truncation.
  A leading BOM is stripped and CRLF and bare CR become LF, following GFM's
  line-ending rules. Goldmark v1.8.6 (pure Go, MIT, no transitive dependencies)
  parses CommonMark with the GFM Table extension.

  A **declaration paragraph** is a top-level paragraph containing only exact,
  case-sensitive `Depends-On: <ref>` source lines, with at most three leading
  spaces and exactly one non-whitespace reference per line. Several declarations
  may share one paragraph. Its inline AST may contain only Text (including soft
  or hard line breaks), with no HTML, links, images, code spans or emphasis.
  Source spelling is preserved; escapes or markup cannot manufacture a key or
  reference. Put a blank line between declarations and surrounding prose or
  trailers, for example:

  ```text
  This PR needs the API change.

  Depends-On: https://github.com/acme/api/pull/42
  Depends-On: https://github.com/acme/client/pull/17
  ```

  Any declaration-shaped line elsewhere returns `ErrDeclarationAmbiguous`
  (`declaration-ambiguous`), with guidance to put each declaration in its own
  paragraph, with nothing else. This includes mixed prose, lists, headings,
  table cells, inline HTML, multiline code spans and hidden link/image titles.
  Fenced/indented code blocks, block quotes (including lazy continuations), HTML
  blocks and HTML comments remain ignored examples. Inline comments shield only
  their own source, so a declaration followed by a comment is ambiguous. An error
  returns no partial inputs. Literal comment markers in code spans or escaped
  text do not start comments; unclosed blocks follow CommonMark.

  This strict grammar refines ADR 0011's “standalone body lines” (~line 181).
  **Slice 6's operating guide must document this rule for users**, including blank
  lines around declarations and blocking ambiguity diagnostics. Linkify,
  TaskList and Strikethrough remain disabled: they do not alter eligibility under
  this grammar.
- URI segments use RFC 3986 unreserved-only, uppercase percent encoding of UTF-8
  bytes. Encoded unreserved bytes, lowercase escapes, raw reserved characters,
  credentials, invalid UTF-8, controls, query/fragment syntax and extra segments
  are rejected. Spaces in an installation segment are encoded as `%20`. Origins
  are supplied canonical HTTPS origins, including a configured nondefault port.
  Explicit default port `:443` and trailing-dot host spellings are rejected.
- `Resolve` accepts only supplied binding records. The currently recognized URL
  alias shape is a GitHub HTTPS `/owner/repo/pull/positive-number` URL on the
  configured origin. It binds revision-less to exactly one stored queue key
  including base/lane. Unknown IDs, ambiguous aliases/bindings and unserved or
  foreign-organization repositories fail closed. Callers must retain this binding
  for a declaration; calling Resolve against a later registry is not a refresh
  protocol. At most 1024 raw inputs are accepted; overflow fails before any
  binding scan, even if they would deduplicate. Persistence and binding/edit
  races belong to later slices.
- `Normalize` sorts by journal-scoped change ID and deduplicates resolved edges.
  Revision zero means revision-less; explicit pins are positive uint64 observation
  sequences without leading zeros, matching ADR 0007's revision type. Conflicting
  pins or revision-less/pinned mixtures for one change block the declaration.
  The 32-edge limit applies after resolution and deduplication, so repeated
  spellings of one edge consume one edge. A rejected declaration returns no
  partial permissive result.
- `Graph.Evaluate` returns derived authorized-only diagnostics, never an admission
  decision or serialized cycle state. It visits only the requested root's closure.
  Unknown discovery is blocking. Traversal counts the root at depth zero, allows
  256 distinct nodes / 512 distinct directed edges / depth 16, and checks each
  traversed edge's bilateral consent. Served repositories are indexed once per
  evaluation, and endpoint queue/policy validation is cached by node for that
  evaluation only. Shared nodes reached through a longer path are rechecked for
  depth, with at most 17 expansions per node; equal/shorter paths do not expand
  again. Self-edges produce `dependency-cycle` before the same-repository consent
  check. Edge order and map order cannot select a cycle winner.
  A supplied confirmed, on-base landing matching an incoming optional pin is
  terminal; current consent is still required. The source's later declaration
  need not be complete and cannot replace that landing's historical decision.
  The API does not establish landing freshness, read access, lifecycle validity,
  eligible acknowledgement, or landing-start authorization.

## Proofs

- `TestCanonicalURIGolden`, `TestCanonicalURIRejectsMalformedAndForeign`:
  fixed canonical URI/escaping vectors, optional pins, malformed and foreign scope.
- `TestAuthoritativeIntentBoundaries`, `TestMalformedRecognizedIntentFailsClosed`,
  `TestIntentCommonMarkCorpus`: body/trailer and 49 CommonMark/GFM boundary cases,
  including inline/escaped comment markers, list fences, lazy quotes, nested
  lists, setext headings, HTML blocks, multiline code spans, CRLF and tabs.
  `TestIntentAmbiguousDeclarations`, `TestIntentPureDeclarationParagraph`,
  `TestIntentBareCRFence`, `TestIntentBodyCharacterLimit`: strict paragraph
  grammar, review repros, normalization and exact Unicode character cap.
- `TestRevisionlessAliasesOptionalPinsAndDedupe`,
  `TestResolutionRejectsUnservedAmbiguousAndForeign`: resolution and conflicts.
- `TestCycleDiagnosticsDerivedAndDeterministic`,
  `TestIncompleteAndInvalidGraphsBlock`, `TestBilateralGrants`,
  `TestMatchingLandingIsTerminalButRetainsConsent`: bounded pure graph contracts.
- `TestDirectEdgeLimitAfterDedupe`, `TestGraphNodeLimit`, `TestGraphEdgeLimit`,
  `TestGraphDepthLimitAndSharedLongPath`, `TestGraphLadderExpansionBound`: exact
  boundaries and overflow diagnostics; a 64-node / 244-edge / 16-layer ladder
  expands exactly 64 times. The test stops at expansion 65 if memoization breaks.
- `TestResolveInputCapBeforeBindingWork`,
  `TestOriginsRejectAlternateCanonicalSpellings`: raw input cap and origin syntax.
- `TestGraphPolicyCacheIsPerNode`: two nodes in one repository with distinct
  policies cannot share validation results; each evaluation rebuilds its cache.
- `BenchmarkEvaluateLadder1024Served`: the ladder with 1024 served repositories
  and full grant lists on every node, exercising scoped-validation caching.
- `TestCandidatePolicySchemaAgreement`, `TestPublicConfigRejectsDependencyKeys`:
  candidate/schema agreement and public rejection.
- `TestLegacyConfigParsingAndDigestsByteIdentical`: frozen origin/main vectors
  for parsed JSON bytes, warnings, errors, both cleanup digest variants and
  depth 1/4 configs. The public loader/schema and all deployed contracts are
  untouched; existing `internal/config` tests retain their legacy digest vectors.
- `FuzzParseIntent`: corpus seeds, successful canonical round trips, and an
  independent full-AST walk asserting every produced declaration comes from a
  pure top-level declaration paragraph with Text-only inline children.
- `FuzzParseReference`: accepted input equals its canonical spelling.
- `FuzzParsePolicyCandidate`: successful policies validate and round-trip through
  YAML without changing mode or grants.
