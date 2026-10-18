# Stage A 7b inert evidence

Baseline: `origin/main`, `2d68d6ffad209ac60e63b40f7ed022f78d8e6851`.
Validated code commit: `bd1b41840612e57790600a70af2755a0d146857f`.
Dedicated worktrees: `$HOME/openmerge-sa-7b` and
`$HOME/openmerge-sa-7b-main`. Main was fetched with an explicit main refspec;
no other branch was fetched. The final documentation commit changes no executable logic.

This delta is **Inert**. It adds public display DTOs, recorded-fact renderers,
a read-only route on the existing status listener and a CLI read command. Adding
an HTTP route to the live server is a **non-material read-path change**. It adds
no State fields, events, actions, config keys, planner/executor/landing changes,
or check-observation changes. All queue and serve sources are byte-identical to
main. No deployment was performed; main already includes Stage A 8, whose trial
deployment Hold is not waived by this inert delta.

No live DB, systemd unit, live status API, App permission or `openmerge/*` ref
was accessed or changed. Tests used shared test Postgres at localhost:54329;
code/tests add no real GitHub API calls. Replay reads only the saved export
below. Go temp/cache and Python temporary directories were under `$HOME/.cache`.

## Recorded evidence and rendering

`brief.Fact.Presentation` is optional and omitted from historical Fact JSON.
It projects recorded stance source/time provenance, native-versus-receipt
bindings, Q4 recovery barriers, saturation and check name/URL/status/SHA.
Canonical serialization explicitly selects its existing fields, excluding this
presentation record. The fresh-progress overlay stays outside digest assembly.
Public v1 DTOs embed no queue wire structs. Reviewer/operator text and agent JSON
have goldens, JSON round-trip/render agreement, the full 7a fact fixture, safe
HTTP(S) links, escaped untrusted text, and explicit field/row/aggregate limits.
Safety warnings are collected before display rows are truncated.

The journal currently has no persisted complete expanded requirements/brief.
The read route shows available classification, intent, stances, tests and queue
progress, but leaves obligations and the complete receipt digest **not recorded**.
It never reconstructs missing historical requirements from current configuration,
mutable refs or forge reads. This is an explicit 11a/12a recorded-evidence seam.
Legacy known changes return missing sections; unknown/cross-queue IDs return 404.

## Byte identity

All **88** existing wire JSON fixtures, recursively including variants, match
main byte for byte. Sorted relative path + NUL + bytes SHA-256:
`ac1d5fa4f4bee6c65c28a52439fe868c5a8cc0635c9279f1bb73fbdb8dc8f9f9`.

`TestPresentationPreservesAllFiveV2Vectors` reconstructs every v2 Fact, adds
presentation evidence, renders every audience, and compares canonical bytes and
SHA-256 with the unchanged 7a files. Independent file comparisons against main
also pass. Hashes:

- complete: `a2c579bfd5aa5a9911e0d69bfed2a0ca9dd6036e504db184ab1a811240ec1227`
- incomplete: `7eacfa5f593d2db45045a4f07ff5befc39646dbc4195905d2897e3860ea36fca`
- empty-intent: `af362a32e2cfb3a5d0f7e8d065e28d4ed0ef2f702c5955776fb4d6232eeb2f0f`
- complex: `64c05eff2a713bc8261aeb148c1f0a488ce5a024fd699df4643653ecd5767e50`
- no-requirements: `05fb76ddab586e2c8c842ab8d3ab60d0764a9b669afe99276fc255dcff4ff4f9`

Existing v1 vectors and `internal/queue/testdata/brief-facts.json` are unchanged.
A renderer change alone cannot invalidate a receipt.

## Commands and results

Before each heavy job, the host-load gate checked
`pgrep -af 'go test|\.test |check-live-journal'`, excluding its own wait and
shell wrappers, and waited for other tests/replays, including PR 9 work. Jobs
ran serially with:

```sh
export GOFLAGS=-p=2 GOMEMLIMIT=4GiB GOMAXPROCS=2
export GOTMPDIR=$HOME/.cache/om-sa-7b-tmp GOCACHE=$HOME/.cache/go-build TMPDIR=$HOME/.cache
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
make check > ~/.cache/om-sa-7b.log 2>&1; echo exit=$?
make test-sim-exits > ~/.cache/om-sa-7b-sim.log 2>&1; echo exit=$?
python3 scripts/check-phase2-compatibility.py "$HOME/openmerge-sa-7b-main"
python3 scripts/check-live-journal-replay.py \
  --baseline "$HOME/openmerge-sa-7b-main" --candidate "$HOME/openmerge-sa-7b" \
  --events "$HOME/.cache/om-pr72-fix1-input/events.jsonl" \
  --queues "$HOME/.cache/om-pr72-fix1-input/queues.jsonl" \
  --strict-state --out "$HOME/.cache/om-sa-7b-deployment-pair.json"
```

- Final `make check`: **exit 0**, zero lint issues, all race/short tests passed,
  binary built from `bd1b418`. Queue package: 276.054 seconds.
- `make test-sim-exits`: **exit 0**, 44.212 seconds.
- Compatibility against origin/main: **exit 0**. All 17 legacy exit records
  and 1,055 retry/default-digest records are byte-identical. Stream SHA-256:
  `bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`
  and `07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37`.
  The script compares 83 top-level wire fixtures; the recursive check above
  additionally covers five variants.
- Deployment-pair replay: **exit 0**, gates 2/4 and verdict PASS. Strict mode:
  all 2,159 action, structural-state, snapshot-hash and boundary checkpoints
  match; 8,676 timer probes; derived determinism PASS;
  no first difference or non-allowlisted fields. No new State fields and no
  new events/actions were observed. All 21 mandatory preflight tests passed;
  synthetic comparison 7/7 checkpoints and 15 probes passed.
  Baseline/candidate/repeat stream SHA-256 is identical:
  `2f6e9bb374e85f5a18d95cde65ef187d9bc130f27ddb972a8f80f404c2576aeb`.
  Effective config digest remains
  `e58c893b8b3af7685dc07a086824b3bc1fc044456083d193f9566f8387cb58fc`;
  shadow `would_emit_config_applied` is false. Config source SHA-256:
  `3efb43bbc05f5f0b03c005331f79064829fa018e9f6446224682e00dd357fead`.
  Report: `$HOME/.cache/om-sa-7b-deployment-pair.json`.

Earlier results are retained rather than reported as successes: first focused
run **exit 1** from CLI help expectations (fixed); first full check **exit 2**
from lint findings (fixed). A subsequent full run passed (**exit 0**,
`om-sa-7b-pre-final.log`). A queued rerun overlapped the final source edit and
compiled mixed file versions, failing with **exit 2**; only this turn's failed
Go invocation and descendants were stopped. The final clean committed-tree run
above supersedes it. Logs: `om-sa-7b-attempt{1,2}.log`, `om-sa-7b-focused*.log`,
`om-sa-7b.log`, `om-sa-7b-{sim,compat,replay}.log` under `$HOME/.cache`.

## Saved journal identity

The saved export contains 2,158 events and one queue. Event file SHA-256:
`9c8f78f3e967c7cb030948cfc9871cc67a45da06f80b12f27d1c55d09ba8accf`.
Queue file SHA-256:
`418f025a0e3c8792321b6d71d297f17fff1adf48cb616c8f4fcb774163696886`.
The export is retained evidence from 7a, not a new live database export.
Replay compares each revision, timer boundary and next action, with **no new
State fields expected** and strict snapshot-byte equality enabled.
