# Stage A approval policy component

This package is dormant. Production packages do not import it. It imports the
standalone `config/condition` grammar/glob package, never `config` or `queue`.
No function reads a forge, filesystem, database, clock or environment.

`classification.go`, `requirement.go`, `receipt.go` and `stance.go` own the
shared value contracts for PRs 4, 6 and 7. Consumers convert queue and forge DTOs
at their boundaries; these values are not new wire envelopes.

`ParseCandidate` is a fixture-only projection parser, not the future loader core.
It accepts a policy-only projection. Legacy fields continue through
the existing config parser. `testdata/policy.schema.json` describes that
projection; `candidate.schema.json` composes its additions into the complete
published schema for review. Tests pin that composition. `.candidate` fixtures
are deliberately separate from the public loader's accepted YAML corpus. PR 11b
must parse the proposed keys from the public loader's expanded YAML node, reuse
these typed validators, and publish the complete schema and loader together with
enforcement. Its integration tests must cover existing aliases/merge expansion
and the new keys in the same document. YAML alias/merge expansion remains the existing loader's responsibility; this
component rejects aliases and explicit tags rather than partially expanding them.

The evaluator rejects missing or stale change/revision/base/head/merge-base,
policy digest and generation bindings before matching any exemption. Revisions
and generations start at 1; zero and empty binding strings are unresolved. It
requires recorded classification facts with explicit false values
for proven absences. A missing label is unknown. Incomplete inventory makes
`docs`, `generated` and every-file paths unknown; any-file touches and owner
labels retain observed hits. An empty `Paths` list always leaves the every-file
`paths` facet unknown, even with complete inventory; it cannot authorize a
vacuous path exemption. A complete empty inventory gives `touches` false.
`Files` carries paired old/new paths and blob IDs, modes, rename evidence,
binary/submodule truth values, per-file facets and label-bound reasons. Each
file has bounded hunk evidence, omitted ranges and its own completeness flag;
`HunksComplete` records aggregate completeness separately from inventory.
PR 4a supplies the inventory/facets; PRs 2b/7a supply bounded hunks. This package
does not derive them. `Reasons` binds explanations to labels for the brief digest.
`ClassificationInputs.Recorded`, `Classification.Label`, `ReceiptIdentity.Recorded`
and `WriteGrant.Permits` expose the same completeness rules to consumers.
`PolicyOwners` carries the effective union per policy path (both rename sides). Missing coverage and unreadable teams are
unresolved, never empty approval pools. Incomplete inventory always adds an
unresolvable owner obligation directing the author to split the change.

`CodeOwnersKnown` and `CodeOwners` independently record every changed path's
side, last-matching governing entry, resolved IDs and status. Uncovered and
explicitly ownerless entries add no code-owner obligation; unresolved entries
(including any email in a mixed entry or unknown team membership) block even
when a known owner approves. These statuses must not be reused for `PolicyOwners`:
ownerless policy paths still block, while policy-owner resolution treats only
email-only entries as unresolvable on that basis. Rule-only `{code_owners: true}`
expands into count-1 obligations per owned file and per covered rename side,
using old-path owners for deletions. Only `receipt: true` may combine with it;
receipts and exclusions apply independently per file. Complete known inventories
with no covered files add none. Incomplete inventories or unknown ownership
block. This selector cannot be the default. Imported counts need their own
`paths: ["**"]` rule as well as a count default and a separate owner `touches`
rule: on an empty diff the paths match is unknown, enforced alongside the default.

The ADR 0007 facet contract remains:

| Facet | Complete, safe inputs | Incomplete or unknown relevant inputs |
|---|---|---|
| `docs` / `generated` | True iff every file qualifies; false on a known failure | Unknown |
| `paths` | True iff every path matches; false on a failure; empty paths unknown | Unknown |
| `touches` | True on a hit, otherwise false (including empty paths) | True on an observed hit, otherwise unknown |
| `policy` / `owned:` | Recorded path facts and owners | Retain positive facts; absence unknown; policy-owner and applicable code-owner obligations unresolved with incomplete inventory or unknown owners |

Callers supply fresh memberships and identity-bound grants, effective account
kinds after agent mapping, and exclusions for the author, submitter and all
accountable humans. Stances must already be folded and revision-attributed by
PR 6. `Input.Actors` contains observations keyed by stable user ID, not a list
of everyone who must be eligible. Supply observations for current approval or
request-changes holders and observed members of applicable groups/owner pools.
A broader cache is accepted: entries without a current applicable stance or
pool membership do not affect evaluation. Stale revision/head stances and
withdrawals do not introduce candidates. A missing actor for a current relevant
stance remains unresolved; one outside a fresh resolved pool is ineligible.
Unobserved pool members with no stance introduce no approval or blocker.
A fresh, identity-bound write denial makes an actor definitely ineligible even
when account kind or display identity is unknown. Group names reject surrounding
whitespace. Native approvals retain no receipt binding. PR 7a computes/validates
the current receipt identity; this evaluator compares supplied bindings without
substituting current values. A stale latest receipt cannot revive a prior stance.
`RequirementResult.ReliedOn` carries all counted stances with their source records
and receipts where used; `BlockingStances` carries eligible current requests for
changes for that requirement. Both are ordered by stable ID, and receipt values
are copied. PR 9 can persist this provenance without re-deriving it. Source
records also retain acting user, dismissal target and structured time precision
for PRs 3b/6; ordering and time interpretation remain the fold's responsibility.
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

The offline replay command lives in `replay/`, so `make check` compiles it via
`go test ./...`. Run it from the repository root with
`mise exec -- go run ./internal/changepolicy/replay`.
