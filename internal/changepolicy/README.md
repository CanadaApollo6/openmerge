# Stage A approval policy component

This package is dormant. Production packages do not import it. It imports the
standalone `config/condition` grammar/glob package, never `config` or `queue`.
No function reads a forge, filesystem, database, clock or environment.

`classification.go`, `requirement.go`, `receipt.go` and `stance.go` own the
shared value contracts for PRs 4, 6 and 7. Consumers convert queue and forge DTOs
at their boundaries; these values are not new wire envelopes.

`ParseCandidate` accepts a policy-only projection. Legacy fields continue through
the existing config parser. `testdata/policy.schema.json` describes that
projection; `candidate.schema.json` composes its additions into the complete
published schema for review. Tests pin that composition. `.candidate` fixtures
are deliberately separate from the public loader's accepted YAML corpus. PR 11b
must publish the complete schema and loader together with enforcement. YAML
alias/merge expansion remains the existing loader's responsibility; this
component rejects aliases and explicit tags rather than partially expanding them.

The evaluator rejects stale head/policy/generation classification bindings and
requires recorded classification facts with explicit false values
for proven absences. A missing label is unknown. Incomplete inventory makes
`docs`, `generated` and every-file paths unknown; any-file touches and owner
labels retain observed hits. `PolicyOwners` carries the effective union per
policy path (both rename sides). Missing coverage and unreadable teams are
unresolved, never empty approval pools. Incomplete inventory always adds an
unresolvable owner obligation directing the author to split the change.

Callers supply fresh memberships and identity-bound grants, effective account
kinds after agent mapping, and exclusions for the author, submitter and all
accountable humans. Stances must already be folded and revision-attributed by
PR 6. Native approvals retain no receipt binding. PR 7a computes/validates the
current receipt identity; this evaluator compares supplied bindings without
substituting current values. A stale latest receipt cannot revive a prior stance.
`ReviewAttribute` shares the applicable review set with existing conditions,
including native reviews for receipt-only pools. A resolved empty list retains
normal negation; unresolved eligibility supplies an unknown condition value.

`PolicyDigest` hashes canonical static policy, resolved IDs, classifier version,
and governing owners/attributes content or proven absence. Rule/set order and
explicit defaults are canonicalized. Governing config commit/blob provenance,
other queues, memberships and current grants do not enter this digest.
`WithPolicyDigest` leaves the old queue digest byte-identical when there is no
component. The future integration point composes the existing digest with this
component without adding fields to `queue.Config` serialization.

The tests pin true/false/unknown matching, additive requirements, writer/human
and receipt eligibility, per-path owners, self/accountable exclusions, permission
reassignment, revision/head binding, Q5 diagnostics, candidate schema agreement,
and both existing legacy digest vectors (including `75c86e0f…`).
