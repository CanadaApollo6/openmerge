# ADR 0008 PR 10b-2 evidence

Base: origin/main `9c74239e2af82e2c642182ad3093dfd583f531b7` (#149); independent of #152. Classification: **Inert;
deploy only after period B drains**, including the transitively held audit binary.
This slice does not complete 10b/Part 3 or PR 11's real-journal acceptance.
No production Go, workflow, Makefile, Dockerfile, migration or index changes.

## Commands and executed checks

Run from this worktree, with these exports (the URL is the shared scratch endpoint):

```sh
export GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3
export PATH="$GOROOT/bin:$PATH"
export TMPDIR=/home/riels/.cache/openmerge-10b2
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres:openmerge@localhost:54329/postgres?sslmode=disable'
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB go test ./... -count=1
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB go test ./internal/audit ./cmd/openmerge -run 'Test(OfflineAnchorContract|AnchorTamperAcceptance|AnchorRequiredCheckTamper|AuditSelectOnlyCommand|AuditUsageAndSanitization)$' -v -count=1
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB go test -race -short ./internal/audit -run 'Test(OfflineAnchorContract|AnchorTamperAcceptance|AnchorRequiredCheckTamper)$' -v -count=1
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB golangci-lint run
python3 scripts/test_verify_audit_anchor.py
git diff --check
```

Full suite exit 0; initial lint exit 0 (zero issues); Python and diff check exit 0.
The intermediate focused run exited 1: its DB-removal assertion incorrectly
expected errSource. fetchPage returns errIntegrity for missing selected IDs;
the corrected assertion requires errIntegrity (the documented CLI gate is unchanged).
Corrected race run exit 0: new tests 0.89s + 0.78s + 0.59s; package 3.272s
including race runtime overhead (excludes compile/lock wait). This is the local
CI-flag cost; hosted CI timing is unmeasured. Logs in /home/riels/.cache/openmerge-10b2:
all-tests.log SHA-256 `b3be9ee5ba5c44b9d26f3eda5bbab669ff2a5e6f3b474a1556ca8bb36901036e`;
acceptance.log SHA-256 `20bc77d52492b5caf0e72aca11df607c967a4a3140a5c2b7ba180e112d647ff1`;
race.log SHA-256 `e94193fd6e0fbeb0579f35ed65d2a3c1c7c044c088baa4b4f3f000fbd6a58763`.
Final lint exit 0, zero issues; lint-final.log SHA-256 `e92606b0bf483111dff0a120c315ea165821348f31365020e2468a0059095c47`.
Go's existing CI invokes the Python contract through `TestOfflineAnchorContract`;
there is no CI change. Database cases skip without OPENMERGE_TEST_DATABASE_URL.
The new connection-free guard invokes `adr0008_scratch_db.scratch_url` before
connecting, then allocates owned `om_9c2c_<32 hex>` scratch databases and guards
each resulting DSN again. Only localhost:54329 is admitted locally; port 5432
requires both CI=true and GITHUB_ACTIONS=true. Routing/PG overrides are rejected.
Existing audit helpers create owned schemas and SELECT-only roles inside those
DBs and close readers before dropping schemas/roles. Cleanup drops each owned DB
and asserts its catalog entry is absent. No live DB, service or forge is used.

## Tamper matrix

`TestAnchorTamperAcceptance` asserts each Replay success/failure and each
successful replay's actual offline-verifier exit. The command test separately
asserts exact successful two-line CLI stdout; usage/sanitization covers combined
manifest/range selectors (exit 2). Replay API failures below are **not** passed
to the anchor verifier and are not claimed as anchor successes. Its generic CLI
mapping is `audit: connection, source, integrity or publication failure` (exit 1).
The matrix's rejection gates follow the changed bytes and Replay's implementation;
it does not expose separate public error codes for every internal gate.

E1/E2/E3 are independent base_moved sources 1/2/3, recorded at
2026-10-22T12:00:00Z; data.sha contains respectively 40 a/b/c bytes. ID 4, with
40 e bytes, is outside the request at 2026-10-24T12:00:00Z. Request is the
existing `req()` range [2026-10-22T12:00:00Z, 2026-10-22T13:00:00Z).
H updates affected stream hashes and manifest self-hash; R also relinks events
from zero and updates terminal/count. Unmentioned bytes remain unchanged.

| Case / exact byte edit | Replay / first check | Offline anchor |
| --- | --- | --- |
| unchanged | success | 0 |
| payload: E1's 40 a bytes → 40 d bytes; no H | reject / events file hash | not run |
| swap: exchange complete E1/E2 event lines; no H | reject / events file hash | not run |
| drop: delete complete E2 event line; no H | reject / events file hash | not run |
| suffix: delete complete E3 event line; no H | reject / events file hash | not run |
| truncate: delete E3's final `}` and LF; no H | reject / events file hash | not run |
| bad-link-H: E2.previous_sha256 → 64 zeroes, H; terminal unchanged | reject / rebuilt manifest equality | not run |
| payload-H: E1's 40 a bytes → 40 d bytes, H without relinking | reject / rebuilt manifest equality against unchanged DB | not run |
| relink-old-manifest: delete E2 event line, relink E3 to hash(E1); original manifest/selection | reject / events file hash | not run |
| swap-RH: exchange E1/E2 lines in events and selection, R+H | reject / selection source order | not run |
| required-check archive: delete source 22 in retry_full events/selection, retain owner 24's reference; R+H | reject / exact closure comparison | not run |
| required-check DB: delete retained source 22 only in independent scratch fixture | reject / retained source fetch | not run |
| drop-RH: remove E2 events/selection lines; R+H | success / valid smaller selection | 1, anchor mismatch |
| suffix-RH: remove E3 events/selection lines; R+H | success / valid suffix omission | 1, anchor mismatch |
| payload-db-RH: E1's 40 a → 40 d bytes in archive and scratch JSONB; selection payload digest updated; R+H | success / matching source | 1, anchor mismatch |
| watermark-H: watermark id 4 → 3; H only | success / valid retained bound | 1, anchor mismatch |

Replay rebuilds from the DB; **none** of these gates is a supplied-link walker.
DB edits use only owned scratch fixtures; the payload edit is restored before
subsequent cases. Required-check archive/DB cases use independent schemas seeded
from the frozen retry_full journal, not production writes or historical follow-up.

## Executed watermark demonstrator and commitments

The extended fixture lowers 4 → 3, exactly the highest selected ID. Assertions
require replay success, unchanged events bytes and terminal, changed manifest,
and offline exit 1. Thus terminal-only custody misses this edit.

| Result | Manifest SHA-256 |
| --- | --- |
| original | `59bbc1384bed05890120859563b24c6baa3801d9f4949495e90a894c64708694` |
| drop-RH | `d49db33f7e79d0f4507cd858f95cddee393e0215bcf9b994348c3569782f7959` |
| suffix-RH | `e5516f195c7e6be0b8212e92f179ba9870377d25e8117cd44c7a8a424acf68dd` |
| payload-db-RH | `d5c80c8b50f28f9895e27f8a71b857de8bf9824795aab501205776a25586d31e` |
| watermark-H | `851980c1316a880de1ecbeb3a86815ab65616ade0f748f58c1e82c150a05cde3` |

Original and lowered-watermark terminal:
`1816c6c81382ad5b6a383fa751e05b754be0fc0837a3778867d7373e251b5296`.
Events-file SHA-256, unchanged by lowering:
`d5bc318412e41f65e6ceadcbe19340696a0488e4fada1b6ca46003de4a69c88e`.
The verbose test logs also print candidate manifest and all mutated stream hashes.

## Overlap, strict inputs and scope limits

The DB acceptance verifies replay-valid missing E2/E3 bundles fail with
`missing source`, both partial/default and full modes. Later inserts at 12:30
and deliberately backdated 12:15 produce replay-valid fresh B bundles and exit
1 / `in-window addition`. A replay after both additions still equals A in all
five files. The Python contract separately asserts true partial/disjoint ranges,
closure-only omission (allowed partial, rejected full), contextual differences,
source mismatch, duplicate identities/JSON keys, malformed IDs/times, malformed
receipts, valid mismatched anchors, wrong stdout path and duplicate anchor lines.
Empty A with an empty interval and disjoint B fails full coverage (exit 1).

Implementation extension: `--replay-out` is required so line 1 can be checked
against the exact command argument. The verifier trusts prior DB replay; it does
not independently validate the entire v1 archive or prove DB replay occurred.
API acceptance constructs captured stdout from successful Replay results; the
existing command integration asserts the exact real stdout format independently.
The three-source watermark fixture extends the proposed one-source recipe.
Operator identity separation/private-copy write denial remains a runbook
precondition; this slice does not claim a new OS-user test.
Custody ownership, cadence, retention/access separation and L remain explicit
organization decisions without invented defaults. Backup/lifecycle/role proofs
remain 10b-1; real journal acceptance remains PR 11.
