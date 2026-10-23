# Frozen serialization v1

V1 is frozen with PR 10a-2b-2. Any serialization shape change after this PR
merges requires a version bump. Do not regenerate expected bytes to accept a
Go implementation change.

`canonical-v1.json` fixes canonical input, exact bytes without LF, and SHA-256.
`journal-v1.json` is the documented literal journal input for three complete
exports: `full`, `empty`, and `closure`. IDs above 2^53, exact decimal tokens,
Unicode, UTC microsecond database timestamps, and offset payload timestamps
are intentional. The full vector has two historical scope events. Empty has
no queues or events. Closure selects the landed event in [12:00,13:00), retains
the preceding landing start as a dependency, and records the start's missing
decision and abandoned edges as explicit absence. Both events share batch b1
and attempt attempt. The start's landed edge and the landed event's start edge
form a retained cycle, not duplicate output. Watermarks are literal inputs.

`scripts/verify-adr0008-10a-2b-2.py` independently derives all expected bytes
using Python Decimal, UTF-8 byte-key ordering, explicit relationships, and an
encoder adapted from `verify-adr0008-10a-1.py`. It never uses Go output as an
expected input. `--write-reference` writes only this independently calculated
reference; normal invocation compares it. `TestCompleteExportV1` separately
loads the journal into scratch Postgres, runs closure/export/replay, and compares
all files byte for byte. The empty vector also exercises the Go builder without
a database. The reference fixes metadata hashes, selection, all chain links,
terminal hash, all four stream hashes, boundaries, and manifest self-hash.

Each export has events.jsonl, selection.jsonl, watermarks.jsonl,
dependency_boundaries.jsonl, and manifest.json. Records include LF; the manifest
self-hash hashes canonical manifest bytes with manifest_sha256 empty and no LF.
Absent references have source null; evidence_kinds and references are arrays,
including when empty. Strings escape U+0085/U+2028/U+2029 as lowercase Unicode
escapes. Payload timestamps retain their spelling; source/request timestamps
are normalized UTC microseconds. Under github authority the pin edge alone is
inapplicable (with its retained source); landing_checks_recorded is not_recorded.
