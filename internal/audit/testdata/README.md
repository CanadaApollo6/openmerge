# Frozen serialization v1

V1 is frozen with PR 10a-2b-2. Any serialization shape change after this PR
merges requires a version bump. Do not regenerate expected bytes to accept a
Go implementation change.

`canonical-v1.json` fixes canonical input, exact bytes without LF, and SHA-256.
`journal-v1.json` contains literal retained journal inputs for six exports:
full, empty on an existing queue, closure, approval under openmerge authority,
github authority with rerun and a later excluded retry, and two queues whose
key order reverses their database IDs. The approval journal inputs originate
from the existing landing-start fixture and its prepared receipt/decision;
only raw journal inputs were captured, never Go export output or references.
The github retry input extends those literals using the existing wire samples.
IDs above 2^53, decimals, Unicode and offset payload timestamps are intentional.

`scripts/verify-adr0008-10a-2b-2.py` computes edges independently from journal
semantic keys and boundaries and derives all 80 complete expected files with
Python Decimal. No Go output or saved selection is an expected input. These
selectors are a deliberately small reference for these literal journals, not a
second general exporter. No expected edges are copied from Go. The original literal journals retain their hand-written annotations. For all
journals the reference independently computes range selection and transitive
dependency closure over the full retained universe. `--write-reference`
regenerates only this independent reference. `verify-audit-v1-node.cjs` separately
re-encodes exact numeric tokens and verifies all canonical bytes, manifest self
hashes and stream hashes. Node does not supply a second semantic resolver.
`TestCompleteExportV1` loads every vector into scratch Postgres and compares
Export and Replay with all five reference files, including the existing empty
queue. Every database reader uses a SELECT-only role.

The normative contract is the
[v1 frozen specification](../../../docs/research/adr-0008-10a-design.md#v1-frozen-specification).

Each export has events.jsonl, selection.jsonl, watermarks.jsonl,
dependency_boundaries.jsonl, and manifest.json. Records include LF; the manifest
self-hash hashes canonical manifest bytes with manifest_sha256 empty and no LF.
Absent references have source null; evidence_kinds and references are arrays,
including when empty. Strings escape U+0085/U+2028/U+2029 as lowercase Unicode
escapes. Payload timestamps retain their spelling; source/request timestamps
are normalized UTC microseconds. Under github authority the pin edge alone is
inapplicable (with its retained source); landing_checks_recorded is not_recorded.

Round 2 adds `journal-r2.json` and 50 new frozen files; the original 30 files
are unchanged. Six Sonnet journals cover range closure (including dependency
queues with counts 11 and 16), run/event ordering ties, two attempts for one
batch and a decision without a pin. All 30 corresponding Python derivations
match Sonnet's independent Node semantic resolver outputs byte-for-byte.
Four additional journals cover a remedy owner, multiple decisions per start,
and repeated accountability/test-ref/rerun sources with wrong-workflow decoys.
The new journal stores one compact literal case per line; JSONL expectations
still store one canonical record per line. No Go output supplies expectations.
