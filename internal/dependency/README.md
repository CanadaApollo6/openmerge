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
  `Depends-On:` is case-sensitive and must start a standalone prose line, with
  at most three leading spaces and exactly one non-whitespace reference. CRLF is
  normalized. Fenced/indented code, block quotes and HTML comments are ignored;
  unclosed fences/comments shield the remaining body. HTML comments are replaced
  by whitespace, so an inline comment cannot concatenate a reference or key.
- URI segments use RFC 3986 unreserved-only, uppercase percent encoding of UTF-8
  bytes. Encoded unreserved bytes, lowercase escapes, raw reserved characters,
  credentials, invalid UTF-8, controls, query/fragment syntax and extra segments
  are rejected. Spaces in an installation segment are encoded as `%20`. Origins
  are supplied canonical HTTPS origins, including a configured nondefault port.
- `Resolve` accepts only supplied binding records. The currently recognized URL
  alias shape is a GitHub HTTPS `/owner/repo/pull/positive-number` URL on the
  configured origin. It binds revision-less to exactly one stored queue key
  including base/lane. Unknown IDs, ambiguous aliases/bindings and unserved or
  foreign-organization repositories fail closed. Callers must retain this binding
  for a declaration; calling Resolve against a later registry is not a refresh
  protocol. Persistence and binding/edit races belong to later slices.
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
  traversed edge's bilateral consent. Shared nodes reached through a longer path
  are rechecked for depth. Edge order and map order cannot select a cycle winner.
  A supplied confirmed, on-base landing matching an incoming optional pin is
  terminal; current consent is still required. The source's later declaration
  need not be complete and cannot replace that landing's historical decision.
  The API does not establish landing freshness, read access, lifecycle validity,
  eligible acknowledgement, or landing-start authorization.

## Proofs

- `TestCanonicalURIGolden`, `TestCanonicalURIRejectsMalformedAndForeign`:
  fixed canonical URI/escaping vectors, optional pins, malformed and foreign scope.
- `TestAuthoritativeIntentBoundaries`, `TestMalformedRecognizedIntentFailsClosed`:
  body/trailer and Markdown boundary fixtures, CRLF, fail-closed recognized lines.
- `TestRevisionlessAliasesOptionalPinsAndDedupe`,
  `TestResolutionRejectsUnservedAmbiguousAndForeign`: resolution and conflicts.
- `TestCycleDiagnosticsDerivedAndDeterministic`,
  `TestIncompleteAndInvalidGraphsBlock`, `TestBilateralGrants`,
  `TestMatchingLandingIsTerminalButRetainsConsent`: bounded pure graph contracts.
- `TestDirectEdgeLimitAfterDedupe`, `TestGraphNodeLimit`, `TestGraphEdgeLimit`,
  `TestGraphDepthLimitAndSharedLongPath`: exact boundaries and overflow diagnostics.
- `TestCandidatePolicySchemaAgreement`, `TestPublicConfigRejectsDependencyKeys`:
  candidate/schema agreement and public rejection.
- `TestLegacyConfigParsingAndDigestsByteIdentical`: frozen origin/main vectors
  for parsed JSON bytes, warnings, errors, both cleanup digest variants and
  depth 1/4 configs. The public loader/schema and all deployed contracts are
  untouched; existing `internal/config` tests retain their legacy digest vectors.
- `FuzzParseIntent`: parser seeds and successful canonical round-trip invariants.
