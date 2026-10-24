# ADR 0008 PR 10b-1 execution evidence

Implementation source: `d8a170346640bf6b67a10f58a1db890df8eb44a2`, stacked on
#149 `40930cc5f2f4b5ea6dec9229eb5fa5cc76fb877d`. Executed 2026-10-24 UTC.
Inert by behavior, transitively **Hold**: no deploy before period B drains.
This proves the 10b-1 fixture slice, not all of 10b or Part 3. Offline anchoring,
tamper/overlap acceptance and operator guidance remain 10b-2; real-journal replay remains PR 11.

## Executed scope

`TestRetentionLifecycle` bootstraps journal genesis through ordinary GitHub discovery,
then uses `productionAuthorityRuntime`, `productionServeOptions`, `NewRepositories`,
`Run` and `RefreshBranches`. Setup verification uses `CommitWriteRestrictionVerification`;
the adoption, config/pin transitions and retirement are never appended by the test.
The first-emission receipt is a valid synthetic fixture attestation, following the existing
activation-test pattern. The actual dump/restore proof below is independently executed.
Fake identities, reviews and checks accompany real private Git configuration and landings.
An independent completion cache avoids reentering the writing Git cache's lock.

The journey covers OpenMerge adoption/landing, accepted and rejected receipt commands via
the CLI brief, GitHub rollback/landing, fresh proposed verification and OpenMerge return,
selector removal while retaining main, and full store close/reopen with reconstructed servers.
Complete canonical JSONB inventories compare every previous events/queues row after each leg;
all five pre-switch export files remain byte-identical on replay, including dependencies
before the selection's lower bound. Config/pin adjacency and retained source families are pinned.
The final inventory has 74 events, two decisions, five eligibility reads,
two receipt records (one accepted, one rejected), three authority pins and one retirement.
GitHub-mode ordinary approval has no receipt requirement; its decision retains that historical
policy, while OpenMerge's decision retains the actual receipt dependency.

Three uniquely owned guarded databases and one role are removed after the run, with zero
remaining owned resources asserted. The role exists before restore to preserve dumped ACLs.
Source/restore inventories and all five fresh/replayed export files match. A clone with a
required eligibility dependency deleted fails replay (exit 1); the source remains untouched.
SELECT on exactly events/queues plus schema USAGE succeeds; CREATE/TEMP/schema-write
privileges are false. INSERT/UPDATE/DELETE/table CREATE/TEMP probes fail (psql exit 3).
Omitting events or queues independently makes export exit 1 with the generic diagnostic.
The PostgreSQL login is `om_10b_b618831f229a4d8da077343baf06390a`. Publishing, reading and byte comparison use OS
identity `riels` (uid 1000); output directories/files are owned by it
with 0700/0600 publication permissions. This is no claim of independently administered custody.
Real Git read-back checks both final and restored exports: tested/intended/confirmed SHAs,
including unsuccessful builds, agree per batch; merge parents and outside moves agree with
private `cat-file`/`rev-list` objects. No GitHub API, live DB, service or production ref is used.

## Commands and exits

Artifacts: `/home/riels/.cache/om-10b-40930cc`. `commands.json` retains subprocess argv,
SQL, raw stdout/stderr and exits (credential-bearing DSNs redacted; owned names in summary).
All following checks exited 0; expected failures above are asserted by the driver.
The full package race run used the immediately preceding #149 head `913407a`; after the
`40930cc` lint-only test fix, audit race tests, touched-package lint and the entire race-enabled
acceptance were rerun. No production Go change belongs to this slice.

```sh
export GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3
export PATH=$GOROOT/bin:$PATH
export TMPDIR=/home/riels/.cache/om-10b-build
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB go build -o /home/riels/.cache/om-10b-build/openmerge ./cmd/openmerge
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB go test -race -c -o /home/riels/.cache/om-10b-build/openmerge.test ./cmd/openmerge
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB go test -race -short -count=1 -timeout=15m ./cmd/openmerge ./internal/config ./internal/store ./internal/audit
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB go test -race -short -count=1 -timeout=15m ./internal/audit
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB golangci-lint run ./cmd/openmerge/... ./internal/config/...
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB golangci-lint run ./internal/audit/...
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts -p test_adr0008_scratch_db.py
$GOROOT/bin/gofmt -l cmd/openmerge/retention_acceptance_test.go internal/config/config_test.go internal/config/schema_test.go
OPENMERGE_TEST_DATABASE_URL='postgresql://postgres@localhost:54329/postgres?sslmode=disable' python3 scripts/check-adr0008-10b-1.py --binary /home/riels/.cache/om-10b-build/openmerge --test-binary /home/riels/.cache/om-10b-build/openmerge.test --out /home/riels/.cache/om-10b-40930cc
git diff --check
```

Package tests used an independently created/dropped `scratch_url`-guarded database via
`/home/riels/.cache/om-10b-build/verify.py` and `verify-audit.py`; no shared database schema was
used. cmd/config/store/audit race results: 62.702s / 1.676s / 77.094s / 14.812s; final audit:
16.128s. Lint: zero issues. Scratch guard: four tests passed. Formatting: no output.
Development fixture/driver failures and their cleanup are retained under `~/.cache/om-10b-run*`;
none required a production fix. The implementation is 675 added lines before this evidence,
above the 400–550 target but below the approximately 800-line gate. No #149 files are edited.

## Hash receipts

PostgreSQL and pg_dump/pg_restore: 18.6. Dump SHA-256: `e5e515b0f19eed3f919d6bf297647e0d5defcf33fb4e3fcbed988958e2591a07`.
Restored canonical inventory SHA-256: `a4b511e82d2c0684a8f51aeda124f5a3c4485e11404f45dca580fbdd2536ebea`.
Printed primary manifest commitment: `0459bb9ae91c23b177ab827b2c46e69d2f12f6a8c20134510bfccc575bdcb60a` (not the manifest file hash).
Successful CLI stdout is asserted as exactly two LF-terminated lines: output path, then digest.
The script's per-command outputs also retain Git objects/parents and exact normalized bounds.

| Artifact | SHA-256 |
| --- | --- |
| `openmerge` | `698b1caabfe22c19c77bb5759c41066b1713c1465c075d6422d4c0ec2eb887d6` |
| `openmerge.test` | `b6ce48bf057de43d00d60d6aaaac4d6a2d668e2016e317015e9255d779afe33a` |
| `commands.json` | `f771f897cdd00a8d4720ffb8726b5d56b66592b0d737b1031ff2964bf9078a3c` |
| `summary.json` | `5280e4a88ff98cee8228ecc8521b36be6362302c246d213439c9c549eeb08288` |
| `packages.log` | `1d473452da6fd84779a7a6edaaf85e60ede066d9f588e81d609337aae3c9cef2` |
| `audit-40930cc.log` | `d71e60c6913005ce2ed3150e4c817fb19d7102263781c6d35a314cbb8183cd62` |
| `manifest.json` | `74b8543bd5d741ef6c7b994e604ae9da0dd764869307028dca6df51b723ead43` |
| `events.jsonl` | `52b6e3dddc704cfbbf07f7fe2a65d69a025640ffff040786e3d45c4503896762` |
| `selection.jsonl` | `32cde51bef9619956c62d071fc93d434a952b416ab9c334d0b029ee30c7a99c7` |
| `watermarks.jsonl` | `247286a9c9e2a3a535ab17ca60f7bf64c7320f3d215715ab78e670d6ca05de6a` |
| `dependency_boundaries.jsonl` | `8afde16a972116f539d220a23ed31a1ecd1747812cd14e2c447dc8bc084a33a0` |
