# Stage A 7a inert evidence — fix round 2

Branch: `stage-a/7a-receipts`; worktree: `$HOME/openmerge-stage-a-7a`.
Round-2 starting head: `0b203f3`. Validated code commit:
`8e9313a42b690ce024b8f3907157292dd966645e` (only this evidence note remains
uncommitted during validation). Explicit-refspec fetch of main and this
branch found origin/main unchanged at
`3e1536093441594ec6b1abd723426e29b8465ad5`; no rebase was needed.

This remains **inert**: no production producer/command, endpoint, config key,
planner/executor/landing change or permission change. No live DB, service unit
or `openmerge/*` ref was touched. Tests use shared test Postgres. No real
GitHub API calls were added to code/tests. State.receipts stays nil for legacy
queues; v6 upgrades v2–v5 with its null field. Receipt boundary history is
allocated only when an existing receipt's processing revision becomes old.

## Round-2 changes

1. Definite `require: none` can expand to zero requirements. Canonical now
   permits that result; Build still validates defaults, rules and current basis.
   `ApprovalsRequired` preserves the opt-in distinction. Tests prepare, admit,
   apply and restore accepted explicit and bare receipts for a docs/none rule.
2. `brief.OverlayProgress` joins fresh evaluator progress onto recorded
   obligations by (Cause, RuleIndex, Path, Side, Requirement). It replaces only
   progress, marks unmatched rows unresolved and preserves recorded identity.
   Tests cover fresh approval, changed membership, missing rows and each join
   key; the full fact golden now demonstrates fresh progress. Consumers apply
   this helper after constructing the recorded Fact, without feeding fresh
   classification pools through Build.
3. Malformed bare digests prepare a rejection with stable reason
   `receipt_digest_malformed`. Raw audit strings have a rune-aligned 1,024-byte
   bound; invalid UTF-8 becomes U+FFFD. These attempts share the 64-per-actor
   rejection cap. Tests cover round trips, preservation, bounds and admission.
   Missing recorded evidence still defers; parsing failure alone does not.
4. `Policy.Digest` carries the live semantic digest from the configuration
   boundary. RecordedBrief requires equality with Inputs.PolicyDigest, returning
   typed `BriefPolicyMismatchError` for absent/different digests. The evaluator
   Policy DTO lacks the complete semantic hashing inputs; callers compute this
   digest with the existing PolicyDigest function.
5. Unsafe paths no longer become safe in the inventory-only counterfactual;
   unsafe-path uncertainty cannot be mislabeled `unknown_inventory`.
6. Old receipt processing revisions retain their durable RevisionRecord in
   ApprovalFacts. Snapshot validation checks that revision's head and boundary,
   including after restart; tampering tests cover both. Nil history is omitted
   from JSON, preserving existing legacy bytes.
7. RecordedBrief uses BuildRecorded to expand without hashing; receipt preparation
   hashes once. V1 receipts are explicitly unsupported (none emitted in
   production); leaf validation rejects them as binding mismatch.
8. Replaced the low-throughput saturation fuzz target with four deterministic
   transition cases. Each starts near source capacity and checks receipt pins,
   encode/decode and byte-identical re-encoding after every action.
9. The 11a contract documents full EncodeSnapshot on each admission preflight
   and its polling-budget cost: approximately 14 ms at 1,500 sources in the
   round-2 review measurement, not a latency guarantee.

## Canonical vectors

The four existing v2 byte/hash pairs are unchanged from `0b203f3`:

- Complete: `a2c579bfd5aa5a9911e0d69bfed2a0ca9dd6036e504db184ab1a811240ec1227`.
- Incomplete: `7eacfa5f593d2db45045a4f07ff5befc39646dbc4195905d2897e3860ea36fca`.
- Empty intent: `af362a32e2cfb3a5d0f7e8d065e28d4ed0ef2f702c5955776fb4d6232eeb2f0f`.
- Complex: `64c05eff2a713bc8261aeb148c1f0a488ce5a024fd699df4643653ecd5767e50`.

New `no-requirements` v2 vector has `ApprovalsRequired:true` and
`"Requirements":[]`. SHA-256:
`05fb76ddab586e2c8c842ab8d3ab60d0764a9b669afe99276fc255dcff4ff4f9`.
Independent Python reconstruction from the complete vector with empty
requirements reproduces its exact bytes and hash. V1 bytes remain pinned.

## Validation

All heavy jobs run serially after checking
`pgrep -af 'go test|\.test |check-live-journal'` for other running test/replay
processes (excluding shell wrappers and the checking process).

```sh
export GOFLAGS=-p=2 GOMEMLIMIT=4GiB GOMAXPROCS=2
export GOTMPDIR=$HOME/.cache/om-pr72-fix2-tmp GOCACHE=$HOME/.cache/go-build TMPDIR=$HOME/.cache
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
make check > ~/.cache/om-pr72-fix2.log 2>&1; echo exit=$?
make test-sim-exits > ~/.cache/om-pr72-fix2-sim.log 2>&1; echo exit=$?
python3 scripts/check-phase2-compatibility.py "$HOME/openmerge-stage-a-7a-main"
```

- Final `make check`: **exit 0**, zero lint issues, all race/short tests
  passed, binary built. Queue package: 274.254 seconds.
- The first full check exited **2** because its old unsafe-path test required
  the mislabeling fixed in this round. That assertion was corrected. Its log
  is retained as `~/.cache/om-pr72-fix2-attempt1.log`.
- Focused regressions: final **exit 0**. The first focused run exited **1**
  from a test-copy helper that omitted State's private maps; the test now
  copies via EncodeSnapshot/DecodeSnapshot. No production fix was needed.
- Independent new-vector reconstruction: pass; four existing v2 vectors and
  v1 fixtures unchanged.
- All **84** baseline wire JSON files (including variants) are byte-identical;
  sorted path + NUL + bytes SHA-256:
  `73371fde38dce0aa7325ae4177c346c2bd8fc1b64972efd7963624cfc029d878`.

- `make test-sim-exits`: **exit 0**, 44.224 seconds.
- Compatibility against origin/main: **exit 0**. All **17** legacy exit
  records and **1,055** retry/default-digest records are byte-identical.
  SHA-256 respectively:
  `bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`;
  `07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37`.
  The script compares 80 top-level wire fixtures; the recursive comparison
  above additionally covers four variants.
- Logs: `~/.cache/om-pr72-fix2{,-sim,-compat,-targeted,-replay}.log`.

- Fresh exported-journal replay: **exit 0**, candidate clean commit
  `8e9313a42b690ce024b8f3907157292dd966645e` versus the origin/main above.
  Gates 2/4 PASS; all 2,159 action/structural-state/boundary checkpoints agree;
  8,676 timer probes, derived determinism PASS, no first difference or
  non-allowlisted fields. Mandatory preflight: 20 tests PASS, synthetic 7/7
  checkpoints and 15 boundary probes. Snapshot bytes differ as expected for
  v5 → v6/null receipts; structural state and actions agree.
  Report: `~/.cache/om-pr72-fix2-deployment-pair.json`.
  The first replay preflight exited **2** because the evidence note was dirty;
  it replayed no events. Saving the note outside the worktree and restoring
  the tracked version allowed the clean rerun. The note was restored afterward.

Replay command (same environment and host-load gate as above):

```sh
python3 scripts/check-live-journal-replay.py \
  --baseline "$HOME/openmerge-stage-a-7a-main" --candidate "$HOME/openmerge-stage-a-7a" \
  --events "$HOME/.cache/om-pr72-fix1-input/events.jsonl" \
  --queues "$HOME/.cache/om-pr72-fix1-input/queues.jsonl" \
  --out "$HOME/.cache/om-pr72-fix2-deployment-pair.json"
```

## Retained round-1 deployment evidence

The previous round replayed the existing 2,158-event/one-queue export read-only,
against the same origin/main: replay exit 0, gates 2/4 PASS, 2,159/2,159
action/state/boundary checkpoints identical, 8,676 timer probes, 20 mandatory
preflight tests PASS. Source files remain in `~/.cache/om-pr72-fix1-input/`.
Event SHA-256:
`9c8f78f3e967c7cb030948cfc9871cc67a45da06f80b12f27d1c55d09ba8accf`.
Queue SHA-256:
`418f025a0e3c8792321b6d71d297f17fff1adf48cb616c8f4fcb774163696886`.
This historical evidence is not a new deployment; no deployment was performed.
