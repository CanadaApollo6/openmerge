# Stage A 4a: classification and replayable evidence

Baseline: `d15bc55` (origin/main). Branch: `stage-a/4a-classification`.
Worktree: `/home/riels/openmerge-stage-a-4a`.
Baseline checkout: `/home/riels/.cache/om-sa-4a-main`.
The implementation commit and evidence-only validation commit identify the
reviewed source in git; no deployment is performed by this PR.

## Dormant boundary

ADR 0007 class: **Inert**. No production path emits `change_classified`; runtime
integration waits for 11b. No config keys, actions, planner/executor/landing-gate
behavior, workflows or queue configuration change. Public loaders still reject
classification/approval keys. The optional owner/attribute interface remains
unimplemented until 4b, with unknown generated attributes and unresolved owners.
No live DB, systemd unit or `openmerge/*` ref was accessed. Only main was fetched;
only this PR branch is pushed. Tests use the supplied shared test Postgres.

**Reviewed replay exception for PR #65:** the only new `queue.State` field is
private `classifications`. It stays **nil/zero at every legacy replay checkpoint**
for queues without approvals: only applying `ChangeClassified` allocates it, and
there is no production emitter. Add this field to the live-journal replay tool's
reviewed exceptions list when that tool checks the merged, undeployed batch.
Do not enable compaction. Snapshot encoding advances to v4 because state shape
changed; v2 and v3 decoding remain supported. Existing wire-v1 bytes stay fixed.

## Evidence and follow-on contract

Classifier version:
`classification-v1/git-evidence-v7-bounded-omissions/governing-v1/hunks-v1/facets-v1`.
It folds in forge v7 inventory/rename/ancestry limits, governing selection and
absence proofs, bounded hunk semantics, and the classifier's mapping/facet rules.
A golden pins the version. Every-file docs/generated and paths remain unknown
on incomplete inventories; any-file touches retains positive observed hits.
Policy paths include all four config candidates, forge CODEOWNERS locations,
all .gitattributes files, workflow definitions and typed sensitive patterns,
on both rename sides and deletions. Forge PR labels are never inputs.

The wire-v1 event carries acquisition fingerprint, pinned endpoints and governing
commit, governing object IDs/checked absences, typed inventory/hunk coverage,
file inventory, facts, label-bound reasons and durable generation. The bounded
hunk evidence lives **in `ChangeClassified.Evidence.Classification.Files[].Hunks`**.
Omitted-range coordinates and summary counts are copied, never expanded. Inventory
completeness and hunk completeness stay independent. PR 7a owns the full record
size budget. There is one registry fixture and two non-registry variants.

The projection is keyed by change/revision. Apply and reads detach mutable
contents. Generation advances on semantic refresh and equal-diff base rewrites;
stale generations and head rebinding within a revision are rejected. Config
blob/governing commit provenance can refresh at the same generation only while
classification meaning stays unchanged. Acquisition fingerprints include
provenance; they are not brief digests. Q7 invalidation uses semantic policy,
owner/attribute content and classifier version, excluding config provenance.
Q2 forward predecessor landings preserve the pinned base and receipt generation.
No landing-gate re-diff is introduced.

`classification.Deriver` is 4b's seam for generated attributes and both independent
shared owner projections, including owned: facts. `BoundPRRevision` derives a
missing legacy ID from an explicitly supplied canonical `PRChangeID` result;
`BoundBatchRevision` retains the tested head/revision and rejects unrecorded zero.
The governing default-branch commit is explicit, including for release queues.
No owners or attributes are parsed here. `docs-codeowners-owner-required` tests
the policy-path half and skips positive ownership acceptance with a 4b pointer.

## Validation

Validation commands use `GOTMPDIR=~/.cache/om-sa-4a-tmp`,
`GOCACHE=~/.cache/go-build`, `GOMEMLIMIT=6GiB` and
`OPENMERGE_TEST_DATABASE_URL=postgres://postgres@localhost:54329/postgres?sslmode=disable`.
Heavy checks run sequentially. No database is initialized under /tmp.

Results are recorded below after the frozen-source runs. No exported live journal
or live effective configuration was read, so the live deployment gates remain
operator work. Offline fixture replay and tracked config digest comparison are
reported separately and do not claim live-journal coverage.
