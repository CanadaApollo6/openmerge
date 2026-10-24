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
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB go test -race -short ./internal/audit ./cmd/openmerge -run 'Test(OfflineAnchorContract|AnchorTamperAcceptance|AnchorRequiredCheckTamper|AuditSelectOnlyCommand|AuditUsageAndSanitization)$' -v -count=1
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
The table lists source-derived candidate gates: rejection is by at least one
of the listed checks. Tests assert aggregate errIntegrity, not an independently
isolated gate; disabling a redundant gate can leave them green.

E1/E2/E3 are independent base_moved sources 1/2/3, recorded at
2026-10-22T12:00:00Z; data.sha contains respectively 40 a/b/c bytes. ID 4, with
40 e bytes, is outside the request at 2026-10-24T12:00:00Z. Request is the
existing `req()` range [2026-10-22T12:00:00Z, 2026-10-22T13:00:00Z).
H updates affected stream hashes and manifest self-hash; R also relinks events
from zero and updates terminal/count. Unmentioned bytes remain unchanged.

| Case / exact byte edit | Replay / rejected by at least one of | Offline anchor |
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
| swap-RH: exchange E1/E2 lines in events and selection, R+H | reject / source order, fetchPage ID order, closure or rebuilt manifest checks | not run |
| required-check archive: delete source 22 in retry_full events/selection, retain owner 24's reference; R+H | reject / exact closure comparison | not run |
| required-check DB: delete retained source 22 only in independent scratch fixture | reject / retained fetch ID/count, source, closure or rebuilt manifest checks | not run |
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
precondition; **open: attempt writes as replay identity: denied** is unexecuted.
Custody ownership, cadence, retention/access separation and L remain explicit
organization decisions without invented defaults. Backup/lifecycle/role proofs
remain 10b-1; real journal acceptance remains PR 11.

## Round 1 revision (items 1–5 and 7)

Digest extraction now uses the validated regex capture, never splitlines.
Tests pin all eight non-LF separator paths (crafted and benign), each anchor
comparison independently, malformed queue types, tail containment, half-open
bounds, role/interval consistency, empty-bundle identity and bundle-file symlinks.
The stdin guard is redundant defence in depth: a second `-` read gets empty
input, so its rejection is not independently observable. Optional hardening is deferred to a follow-up for size:
selection stream hash verification, bundle-directory symlinks, CRLF rejection,
and negative lowered/raised watermark acceptance. OS write-denial remains open.

The design-document revision (item 6 and the original watermark correction) is
in [companion PR #154](https://github.com/CanadaApollo6/openmerge/pull/154); it should land
after #152 and #153. The two PRs deliberately share no changed files.

Round-1 foreground verification: the combined Go command above, Python tests,
and golangci-lint all exited 0 (lint: zero issues). New logs under
`/home/riels/.cache/openmerge-10b2-r1` have SHA-256:

- go.log: `babb5a53b87c79257045c839662e49391afac8fb44d139efda9db6f6f15ec447`
- python.log: `220be22afb09a7f549445dfc985daef81ac717a363dfeb846ee08244b8a0e5b9`
- lint.log: `e92606b0bf483111dff0a120c315ea165821348f31365020e2468a0059095c47`

## Round 2 revision

Acceptance now covers valid B-only sources exactly at A.until with extended B,
equal-bound full coverage, full coverage without B, and repository mismatch.
Strict-parser negatives cover queue/request identity, empty repository/type,
surrogates, selection final LF/size and manifest size, reversed bounds, role,
metadata digest, and queue/type/time source-fact comparison. No verifier logic
changed; the catch-all comment documents its sanitized exit-2 policy.

Round-2 verification and log digests are retained in commit `69a6e9c`.

## Round 3 operator-class sweep

Operator classes: all 26 require sites and 37 Python comparison atoms were deleted/forced or boundary-weakened;
SQL predicates, type/nonempty conjuncts, validation calls, decoder hooks and identity/source-fact fields were also mutated.
Every generated script compiled; the real verifier stayed unchanged. **178 tested, 152 killed, 26 equivalent, zero untriaged within that inventory.**
Run: `python3 /home/riels/.cache/openmerge-10b2-r3/sweep.py`, then `python3 /home/riels/.cache/openmerge-10b2-r3/triage.py`.
The complete per-mutant old/new/result/reason table is `sweep-table.csv` in that directory.
New controls isolate out-of-window unknown roles, payload digest and LF/CR/empty out, alongside the same-push parser/join cases.
Padded manifest limit/limit+1 and selection 4 MiB+1 including LF/one-byte-over controls pass; final-LF rejection is separate. No bug was exposed.

| Mutants (verifier line/site) | Result/count | Equivalent reason (single mutation only) |
| --- | --- | --- |
| Remaining guard/comparison, parsing, SQL, field mutations | Killed / 152 | Contract assertions distinguish their result/output. |
| Type guards L42/48/79/82/83/85/89 | Equivalent / 7 | Remaining validation or regex/subscript/encode failure still gives sanitized exit 2. |
| Receipt cap L119; fullmatch→prefix L120 | Equivalent / 2 | Strict fullmatch or the 65-byte cap leaves no room for trailing receipt bytes. |
| Both-stdin guard L118 | Equivalent / 3 | Blocking reads consume the receipt or reject it; the second read is empty. |
| Match-presence L124 | Equivalent / 2 | None.group fails into the same sanitized exit 2. |
| Int64 upper-bound deletion L37 | Equivalent / 1 | SQLite bind rejects overflow with exit 2; tightening to exclude max is killed. |
| readline length cap L76 | Equivalent / 1 | readline(LIMIT+1) already caps length; final-LF guard remains. |
| Identity version term L140 | Equivalent / 1 | Both bundles already require integer version 1. |
| Role comparisons L92/147 | Equivalent / 3 | Allowed roles make >=scope equivalent to ==scope; dependencies cannot lie in A's overlap. |
| TIME fullmatch→prefix L42 | Equivalent / 1 | strptime still rejects trailing junk; calendar-check deletion is killed. |
| Shared/anti-join label filters L144/153 | Equivalent / 3 | Extra B self-matches have equal facts/context; extra A self-matches fail the anti-join NULL test. |
| CLI entry guard L165 | Equivalent / 2 | CLI execution always has __name__=__main__; import behavior is outside this command contract. |

Foreground Python, focused race-enabled Go tests and lint exited 0 (zero lint issues); scratch cleanup assertions passed.
`triage.log` (driver/result/log digests) SHA-256: `ff3079128f710d061704bcbb3c7b589dff31795c5fbc72f382a42f00b39dc990`.
`sweep-table.csv` SHA-256: `37f66c1a2d686b0f0bcd385d1b990c578021c3835442cd24b3684badc5bebb27`.

## Round 4 final acceptance

Pinned: context role/kinds, literal metacharacter out and near-miss, missing stdout LF,
stdin receipt plus X, uppercase payload/manifest hashes, queue-key order in facts and
nested reference context, and exact identity-sorted multi-finding messages.
Optional cases pinned: invalid UTF-8 (manifest/selection), unpadded timestamp,
argparse exit 2 and exact sanitized stderr (no stdout for malformed input).
No verifier changes or bug found. Further operator classes/remaining non-defect
mutation gaps are deferred, as are the existing hardening and OS write-denial items.

Foreground Python, combined race-enabled Go tests above and lint exited 0 (zero issues); 17 targeted variants killed. Run `python3 /home/riels/.cache/openmerge-10b2-r4/targeted.py`. Logs in that directory:

- python.log SHA-256: `7d90a631e1b43875a6ca72b153e18691d0a603a4c945a796e61c7f8e20e45ddd`
- go.log SHA-256: `57c3acab46f31c42e4339e92ff7b30cfdec9d6fda55a347b7ec44193ad5a2723`
- lint.log SHA-256: `e92606b0bf483111dff0a120c315ea165821348f31365020e2468a0059095c47`
- targeted.json SHA-256: `337471e75253a20c204e5c8aaf63e06534b99b939f441acc93ff3b8edfe1addd`
