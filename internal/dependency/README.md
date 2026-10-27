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
  parses CommonMark with the full GFM extension set (tables, strikethrough,
  linkify and task lists) plus its built-in footnote extension.

  A **declaration paragraph** is a top-level paragraph containing only exact,
  case-sensitive `Depends-On: <ref>` source lines, with at most three leading
  spaces and exactly one non-whitespace reference per line. Several declarations
  may share one paragraph. Its inline AST may contain Text (including soft or
  hard line breaks) and URL autolinks only when their label and destination both
  exactly equal a literal reference on that paragraph's source lines. Thus GFM
  linkify preserves plain URL declarations; angle-bracket autolinks, explicit
  links, HTML, images, code spans and emphasis are ambiguous.
  Source spelling is preserved; escapes or markup cannot manufacture a key or
  reference. Put a blank line between declarations and surrounding prose or
  trailers, for example:

  ```text
  This PR needs the API change.

  Depends-On: https://github.com/acme/api/pull/42
  Depends-On: https://github.com/acme/client/pull/17
  ```

  Candidate detection scans every normalized physical source line independently
  of the AST. It strips leading container markers, decodes Markdown backslash
  escapes and HTML entities, deletes every Unicode format character (category
  Cf, including zero-width characters, mid-line BOM and soft hyphen), applies
  Unicode NFKC normalization and case folding, and maps Unicode Dash characters
  and common hyphen/minus and colon lookalikes to ASCII, in that order. Any
  resulting `depends-on:` substring is a candidate, wherever it occurs on the
  line. A second pass joins rendered inline text in each non-example AST block,
  dropping emphasis, strong, strikethrough, link markup, raw inline HTML tags
  and inline comments. Code-span contents remain text. It applies the same
  normalization, catching keys such as `**Depends-On**:`, `Depends-**On**:`,
  `Depends-<!-- x -->On:` and `[Depends-On](<URL>):`. Neither scan widens the
  accepted grammar: only exact, case-sensitive source declarations produce inputs.

  Every candidate outside an ignored example region must be an exact accepted
  declaration line; otherwise the entire parse returns `ErrDeclarationAmbiguous`
  (`declaration-ambiguous`), with guidance to put each declaration in its own
  paragraph, with nothing else. This includes mixed prose, task/ordinary lists,
  headings, table cells, strikethrough, inline HTML, code spans, hidden link/image
  titles, and declarations inside referenced or unreferenced footnotes. Entity,
  escaped, lowercase and Unicode lookalike keys also block. For example,
  `&#68;epends-On:`, `Depends\-On:`, `depends-on:` and `Depends‑On：` are ambiguous.
  Fenced/indented code blocks, block quotes (including lazy continuations), HTML
  blocks and HTML comments remain ignored examples, including inside footnotes.
  Example coverage and rendered candidates are recorded before unused footnotes
  disappear from the AST. Inline comments shield only their own source; removing
  them may join a rendered candidate, which is ambiguous. A declaration followed
  by a comment is also ambiguous. An error returns no partial inputs. Literal
  comment markers in code spans or escaped text do not start comments; unclosed
  blocks follow CommonMark.

  Accepted residuals: cross-script homoglyphs such as Cyrillic `е` and Greek `Ο`
  are not detected; that would require a confusables skeleton. HTML blocks remain
  ignored example regions by design, so `<div>Depends-On: …</div>` and a line
  beginning with `<!-- x -->` are ignored even if GitHub renders declaration text.
  A declaration hidden in the author's own body is equivalent to never writing
  it. Removing a previously parsed edge is a loosening edit: ADR 0011's
  declaration-change hold catches that edit and requires independent
  acknowledgement. That hold belongs to later slices; this package remains inert.

  This strict grammar refines ADR 0011's “standalone body lines” (~line 181).
  **Slice 6's operating guide must document this rule for users**, including blank
  lines around declarations and blocking ambiguity diagnostics.
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
  `TestIntentCommonMarkCorpus`: body/trailer and CommonMark/GFM/footnote boundary cases,
  including inline/escaped comment markers, list fences, lazy quotes, nested
  lists, setext headings, HTML blocks, multiline code spans, CRLF and tabs.
  `TestIntentAmbiguousDeclarations`, `TestIntentPureDeclarationParagraph`,
  `TestIntentLinkifyPreservesExactReference`, `TestIntentBareCRFence`,
  `TestIntentBodyCharacterLimit`: strict paragraph
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
  independent AST walk asserting every produced declaration comes from a pure
  top-level declaration paragraph with Text or exact URL-autolink children.
  It also checks every decoded source candidate: a successful parse must account
  for it as an exact accepted line or an AST-classified ignored example, even
  when Markdown hides it. An independent inline-leaf walk checks rendered-text
  candidates through formatting, links, code spans and inline tags/comments.
  The oracle retains unused footnotes for both checks.
- `FuzzParseReference`: accepted input equals its canonical spelling.
- `FuzzParsePolicyCandidate`: successful policies validate and round-trip through
  YAML without changing mode or grants.
