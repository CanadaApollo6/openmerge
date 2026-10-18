# Stage A 7a inert evidence

Baseline/current main: `3e1536093441594ec6b1abd723426e29b8465ad5`.
Verified implementation: `ce7999630ae4478dbf7b6f9300b21ee8223bd815`.
The following commit adds only this evidence note. Candidate worktree:
`$HOME/openmerge-stage-a-7a`, branch `stage-a/7a-receipts`; baseline:
`$HOME/openmerge-stage-a-7a-main`, detached at origin/main. Both replay inputs
were clean. Fetches used only the explicit main refspec; pushes name only the
7a branch. No live DB, service units, App permissions or `openmerge/*` refs were
operated on. Tests use the shared test Postgres; no real GitHub API calls were
added to code/tests. Heavy checks ran serially.

This is an **Inert component change**: no producer/command, config key,
planner/executor/landing change, endpoint or CLI. 7b owns rendering and reads;
9/11a own atomic decision storage and current-evidence landing wiring. Only one
new State field exists: **`receipts`**, a `queue.receiptProjection` map, **nil**
for every legacy queue. Snapshot v6's corresponding `Receipts` is null; v2–v5
upgrade with nil receipts. Existing classifications/approvalFacts remain nil
on legacy inputs. The deployment comparator permits only the exact new nil-map
shape, and rejects empty/populated/wrong-type receipts in its self-test.

## Facts, canonical digest and receipt records

The protocol and absent/empty rules are specified in
`internal/brief/README.md`. Canonical v1 uses domain `openmerge/brief/v1`,
compact Go JSON with deterministic map/set ordering and SHA-256. It binds
change/revision/head, pinned diff endpoints/merge-base, durable classification
generation, inventory/hunks/completeness, labels/reasons, intent digest,
semantic policy/content inputs, recorded ownership and expanded requirements.
It excludes queue position, progress, timestamps, tests, rendering and
provenance-only config blob/governing commit. Eligible.Reason and freshness
are erased; owner status/resolution remain stable bindings. Intent uses the
exact shared PR 6 normalization/digest code. InputFingerprint is never used as
a brief digest. Hunk evidence is retained verbatim, with no second truncation.

Pinned canonical bytes and SHA-256 vectors:

- Complete: `3afa3ae419e1691b99efedebaccf78dfcde22a4419fe122c45006f8cb627a31b`.
- Incomplete: `41a0a7a649f620188dc1f39bc03050695d61acce1e12b95c2ee77e77b5d6b34b`.
- Recorded empty intent: `237afe4a534dff8ca82a0e76dd93b47f7f26d85da8924c883a8a2198616d8663`.

`approval_receipt_recorded` retains supplied/current explicit bindings, actor
ID/login, comment ID, creation time and accepted/rejected outcome. One registry
fixture is added; ten rejected-binding vectors live outside its glob. Late
rejections are keyed by comment ID and never become stances. First outcomes
survive duplicate polls, revision changes and snapshots; unapprove remains
`review_stance_observed`. Pure validation recomputes current evidence at
acceptance and is provided for reuse at landing; it never replaces the supplied
digest. Tests cover intent edits, equal-path generation refreshes, excluded and
unknown owners/inventory, mixed owner pools, native-after-receipt and
receipt-after-native-then-intent-edit using actual receipt records. Intent edits
leave tested compositions and planner actions unchanged. Rendered/JSON agreement
is deferred to 7b; the full JSON fact golden is included here.

## Mandatory carry-overs

- #64: v8 adds 4096 total retained hunks, `hunk_count_limit` through the existing
  omitted-summary machinery, and a 4 MiB JSON-escaped text budget alongside the
  existing raw/line caps. Persistence validates v8 caps without truncating.
  The hunk subrecord is conservatively ~5.8 MiB. diff.md corrects the unattainable
  39.5 MB v7 estimate to approximately 17.6 MB before the new caps. Historical
  v7 codecs/fixtures remain unchanged; v7 was never emitted in production.
- #69: source diagnostics retain eight entries plus `+N more`, each diagnostic
  capped at 1024 UTF-8 bytes with a truncation marker. README worst-case accounting
  includes diagnostics (up to 12 MiB escaped across 256 probes, plus summaries
  and independently budgeted path prefixes). Brief hashing strips free-text
  Eligible.Reason for both code and policy owner rows; stable status/resolution
  fields remain. Positive and exclusion mutation tests pin the contract.
- #71: a dismissal of review D cannot time-barrier a native approval whose review
  sequence is greater than D. Both original review kinds, three dismissal time
  sources and all six permutations round-trip after each event. Requests and
  command withdrawals retain their conservative barriers. PR 6's existing
  Current.IntentDigest assertion is exercised explicitly. Receipt interaction
  tests now use acceptance records rather than synthetic command approvals.
- #66: classification projection retains only the newest recorded revision per
  change/latest generation; late older revisions cannot resurrect evidence.
  Original evidence remains in the journal; compaction stays disconnected.
  EncodeSnapshot validates evidence and rejects classification above 16 MiB per
  change, combined receipt/classification/approval evidence above 48 MiB per
  queue, and total snapshots above the decoder's 64 MiB cap. Typed per-change
  admission preflights let 11a roll back a failed admission without truncating
  evidence or damaging the previous recoverable queue.

## Commands and actual results

Environment for the required checks:

```sh
export GOFLAGS=-p=2 GOMEMLIMIT=4GiB GOMAXPROCS=2
export GOTMPDIR=$HOME/.cache/om-sa-7a-tmp GOCACHE=$HOME/.cache/go-build TMPDIR=$HOME/.cache
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
make check > ~/.cache/om-sa-7a.log 2>&1; echo exit=$?
make test-sim-exits > ~/.cache/om-sa-7a-sim.log 2>&1; echo exit=$?
python3 scripts/check-phase2-compatibility.py "$HOME/openmerge-stage-a-7a-main"
python3 scripts/check-live-journal-replay.py \
  --baseline "$HOME/openmerge-stage-a-7a-main" --candidate "$HOME/openmerge-stage-a-7a" \
  --events "$HOME/.cache/om-pr71-fix2-journal/events.jsonl" \
  --queues "$HOME/.cache/om-pr71-fix2-journal/queues.jsonl" \
  --out "$HOME/.cache/om-sa-7a-deployment-pair.json"
```

Actual final exits: **check 0; sim-exits 0; compatibility 0; deployment-pair
replay 0**. Check includes lint (0 issues), race/short tests and build. Required
simulator exits completed in 48.860 s. The final code-head check followed an
initial passing full run and the final mixed-owner reason correction.
Logs are `~/.cache/om-sa-7a{,-sim,-compat,-replay}.log`.

Compatibility: 17 legacy exit records byte-identical, SHA-256
`bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`;
1055 retry/default-digest records byte-identical, SHA-256
`07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37`.
The compatibility tool checked 80 wire fixtures. Independently, all **84 tracked
baseline wire files**, including variants, are byte-identical; sorted
path + NUL + bytes SHA-256:
`73371fde38dce0aa7325ae4177c346c2bd8fc1b64972efd7963624cfc029d878`.
All pre-existing fixture files are untouched. The code diff was 1960 changed
lines before this evidence note, below the ADR's 3000-line ceiling.

Deployment-pair structural mode: **gates 2/4 PASS**, actions/state/boundaries
**10/10** identical, **27** timer probes, derived determinism PASS, no first
difference or unreviewed fields. Mandatory preflight: **20 tests PASS**, synthetic
pair **7/7** checkpoints and **15** boundary probes. Snapshot hashes are **0/10**
identical, expected for v5 → v6 and the added null snapshot field; typed structural
state and next actions remain equal with only the nil-only exception above.

Config SHA-256: `3efb43bbc05f5f0b03c005331f79064829fa018e9f6446224682e00dd357fead`.
The recorded/recomputed effective digest is unchanged in all three streams:
`e58c893b8b3af7685dc07a086824b3bc1fc044456083d193f9566f8387cb58fc`.
No shadow ConfigApplied would be emitted and no new event producer exists.
Export hashes: events
`838a9655db51af90e0e63b93ad7c68be8b868d6c5dc1dbc13004638f0c3305ae`;
queues `b34e493c77be1a9ba76d78b27a40db4a767a4027254279f4327d97756b454617`.

**Deployment limitation:** the available cached export is the previous PR 6
synthetic nine-event/one-queue journal, not an exported live journal. This proves
the deployment-pair comparator against current main, but does **not** authorize
live deployment. An operator still needs the ADR's exported-live-journal shadow
replay/effective-config gate and dogfood log entry. No deployment was performed.
