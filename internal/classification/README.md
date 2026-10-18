# Dormant classification component (Stage A 4a)

`Classify` performs no I/O and accepts PR 2a/2b's pinned forge DTOs. Typed
`Settings` are component inputs only: the public loader still rejects
`classification` and `approvals`. Forge PR labels have no input here.
The caller supplies the governing **default-branch** commit explicitly,
including for release queues. It must match the governing reader's commit;
it is never inferred from the pinned queue base, merge base or head.

The classifier version includes forge `DiffSemantics` (v7), which versions
inventory limits, exact-object rename pairing, ancestry proofs, governing file
selection and checked absences, plus bounded hunk semantics. The classifier's
own governing/hunk mapping uses v1 and facet rules use v2. Historical facets-v1
records remain replayable with their recorded meaning. The
version golden must change when any of those rules change.

Paths include both rename sides and deletions. `paths` is every-file matching:
an incomplete inventory is always unknown. `touches` is any-file matching:
an observed hit is true even in an incomplete inventory, while no hit remains
unknown. This asymmetry is intentional and consumed by `changepolicy.MatchRule`.
`docs` and `generated` require every file to qualify, never just the observed
prefix of a truncated inventory. Known policy hits survive all uncertainty.
Binary, unreadable, mode, symlink, gitlink and unpaired rename evidence adds
`unknown`; it does not erase known policy paths. Docs uses paths only, so a
known non-docs path is false even for binary or special files. Generated path
patterns also survive unsafe files; only the attribute half becomes unknown.
On complete inventories a known failing file wins over unknown files. Empty
diffs have docs/generated unknown, policy false, and unknown true. No comment-only detection exists.

`Deriver` is 4b's seam. Its `Generated` method resolves the attribute half from
only the supplied governing tree; a matching generated path pattern already
qualifies independently. Without 4b, the attribute half is unknown, never false.
Its `Owners` method fills the shared `CodeOwners`/`PolicyOwners` evidence and
`owned:` facts using the evaluator's exact side/coverage contract. Until then
both owner completeness flags remain false, with an explicit unknown reason.
4b **MUST bump the classifier version** (e.g. facets-v3 after this facets-v2
fix) when it begins deriving owners/attributes. `Owners` must set `unknown`
itself whenever ownership is unresolved, and must record explicit False for
every governing owner token absent from the change. Missing `owned:` facts
read as unknown even with complete inventories.
4b owns last-match parsing, attribute precedence/unset semantics and identity
resolution; nothing in 4a claims those parsers are ready.

`Evidence` carries the shared classification and bounded governing DTO.
`ChangeClassified.Evidence.Classification.Files[].Hunks` is the durable hunk
subrecord, not a reference to a future event or an unbounded patch. Its omitted
ranges, summary counts and enclosing spans are copied verbatim. Aggregate
`HunksComplete` remains independent of `InventoryComplete`; complete paths are
not a complete brief. PR 7a owns the whole-record transport/size budget.

`InputFingerprint` identifies recorded acquisition inputs, including provenance.
It is deliberately **not** a policy or brief digest. `RefreshReasons` and
`SemanticInputs` exclude config blob and governing commit provenance, while
retaining semantic policy, owner/attribute content and classifier versions (Q7).
Provenance-only refreshes must preserve generation and the future brief digest.
The caller's ancestry observation distinguishes ordinary forward predecessor
landings from rewrites/rewinds (Q2). `NextGeneration` advances on a rewrite even
when the new diff is equal; an unchanged head is insufficient. The landing gate
will consume recorded evidence without re-diffing; production integration waits
for 11b. Started attempts keep their frozen decision evidence.

At the queue read boundary use `BoundPRRevision` with `PRChangeID` derived from
the stored canonical queue key. Legacy `PRRevision` may contain an empty ID.
`BoundBatchRevision` preserves the batch's tested head and revision rather than
reading the latest PR revision. Both reject revision zero; absence is unrecorded.

The new event is wire v1 with a strict nested presence/unknown-field decoder.
Its shared DTO field spelling and non-null wire collections are pinned by one registry
historical golden; complete/incomplete and current facets-v2 examples live under `variants/`, outside the glob.
Snapshots advance to v4 and retain v2/v3 decoding. The classification projection
is immutable and keyed by change and revision, with durable refresh generation.
It does not advance planner revisions or create effects. Compaction stays off.

Wire and snapshot validation share the version allowlist and nested enum,
truth, label/token, object ID, path, mode, omission count/range, path projection
and hunk completeness checks. Empty object IDs retain explicit absence or
unavailable provenance; they never prove absence without the governing probe.
Historical facets-v1 permits its original unpaired-rename flag; facets-v2 checks
path-derived rename coherence. 11a must still enforce whole-record resource
budgets and cross-check derivation against acquired evidence before activation.
Workflow policy paths are only direct `.github/workflows/*.yml`/`*.yaml` files.
