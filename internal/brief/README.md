# Recorded briefs and receipt bindings (Stage A 7a)

`Fact` is the JSON fact model for 7b's three renderers and 9's decision storage.
It includes the pinned classification, explicitly claimed intent, per-file
owner rows, independently expanded obligations, eligible/approved pools and test
links. Classification carries complete-or-explicitly-incomplete file/hunk
inventory, label reasons and immutable object IDs. It is copied verbatim from
2b/4b; no second truncation or omitted-range expansion occurs. Historical absent
Classification or Intent is `null` (not recorded); a recorded empty intent is
an object with empty strings and its valid digest. Unknown owners are distinct
from uncovered/explicitly ownerless paths. Obligations carry explicit path/side,
a stable reason code and diagnostic explanation; consumers never parse prose.
No endpoint, CLI, rendering, config, production emission or landing wiring exists.

## Canonical digest v1

`Canonical` emits compact UTF-8 Go `encoding/json` bytes, no trailing newline.
SHA-256 is lowercase hex. The outer ordered fields are `Version` (exactly
`openmerge/brief/v1`), `Classification`, `IntentDigest`, `Requirements`.
The nested classification uses the shared PR 5 DTO's declaration order and JSON
field spellings. Map keys use encoding/json lexical order; HTML-sensitive text,
controls and U+2028/U+2029 use that encoder's standard escapes. A schema change
requires a new canonical version, never replacement of the v1 vectors.

Included: change/revision/head; pinned base and merge base; durable evidence
generation; classifier version; semantic policy digest; owner/attribute object
IDs; paths, file modes/blobs/rename/binary/submodule facts; inventory/path/hunk
completeness; exact retained hunk text/ranges and omitted ranges/counts; labels
and their recorded reasons; governing per-file owners/status/recorded ID pools;
intent digest; each expanded requirement's origin/path/side/selector/count/group,
human and receipt constraints. Generation advances on every base rewrite even
when the re-diff has identical paths. Semantic policy and governing owner or
attribute content changes remain included through the policy/object bindings.

Excluded: config blob and governing commit (serialized as fixed empty strings),
InputFingerprint (never supplied to this model), queue position, progress,
timestamps, test links and rendered text. Obligation eligibility/progress,
explanations and transient reason codes are excluded. `IdentitySet.Reason` is
always erased, and freshness is fixed false: owner resolution is bound by stable
status/resolution values, never arbitrary resolver diagnostics. Label reasons
remain recorded classification facts, distinct from free-text Eligible.Reason.
Policy hashing remains PR 5's job; no whole Evidence or effective-config hash is
substituted for semantic policy.

Unordered paths, pool IDs and reason strings are sorted and deduplicated.
File rows sort by (new path, old path); owner rows by (path, side), policy owners
by path; label reasons by canonical row bytes; requirements by canonical row
bytes. Hunk/omitted-range order is meaningful and retained. The canonical
collections are always `[]`/`{}`: nil and empty collections are equivalent.
Empty path/blob/mode denotes an absent file side. Unknown facts/statuses keep
their explicit shared zero values; they are never manufactured as empty known
facts. Missing classification/intent rejects hashing. Intent CRLF and CR become
LF and surrounding Unicode whitespace is trimmed by the exact shared PR 6
`changepolicy.NormalizeIntent` / `IntentDigest` implementation. Only its digest
enters canonical bytes.

`testdata/{complete,incomplete,empty-intent}.{json,sha256}` pins canonical bytes
and SHA-256 vectors. Mutation tests cover included fields, exclusions, set order,
normalization and absence. `internal/queue/testdata/brief-facts.json` pins the
full JSON fact model; renderer/JSON agreement belongs to 7b.

## Acceptance and landing

`brief.ValidateReceipt(supplied, currentFact)` recomputes the current canonical
identity and compares **all** components to the supplied identity. Call it at
acceptance and again at landing, with current recorded evidence. It never
replaces a supplied digest. `queue.PrepareReceipt` produces the additive
`approval_receipt_recorded` event, including processing basis, supplied/current
bindings, actor ID/login, comment ID, creation time, accepted flag and stable
`receipt_binding_mismatch` rejection. Authentication and fresh writer checks
belong to 11a/11b; no token or command authority is added here.

The receipt projection retains first acceptance **or rejection**, keyed by change
and comment ID across revisions/restarts. A late receipt can name an earlier
revision in Supplied while Basis names the processing revision. Rejection never
enters PR 6's stance fold; accepted receipts become exactly its receipt stance.
`unapprove` remains `review_stance_observed`. Native reviews keep null receipts.
Intent edits invalidate receipts without changing revision, test composition or
CI keys. 9/11a must use these pure validation functions with current facts before
landing, alongside fresh eligibility; passing an old Current identity alone is
not landing validation. Admission preflights must run before journaling and
roll back a rejected change while retaining the prior recoverable snapshot.

Snapshot v6 adds only State.receipts (nil for every legacy queue); v2–v5 upgrade
with absent receipts. Snapshot byte budgets include receipts, classification and
approval facts in the shared 48 MiB evidence reserve plus 16 MiB legacy reserve.
No source pins are evicted to make receipts authorize again. Journal history
remains the audit record; classification projections retain only the latest
recorded revision per change (and latest generation within that revision).
