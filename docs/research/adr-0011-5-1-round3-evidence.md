# Round 3 privacy projection evidence

Implementation and test tree: `b3882d6a` (the later evidence-only commit does
not change the tested code). Dependency activation remains confined to the test
seam. Existing v1 wire fields and inactive golden files are unchanged.

The redacted entry is built as a fresh value. Its allowlist retains PR and head,
position and admission time, public status and reason and last dequeue reason,
change/approval metadata, the authorized dependency warning, and confirmed
landing outcome, SHA and event ID. Waiting-entry conditions and the public
pending-dequeue gate are retained. Pre-landing progress becomes queued; batch
references, run ledger and batch-derived detail are withheld. Checks are rebuilt
from the projected entry and retain independent dequeue reasons; pending dequeues
remain pending checks.

The deterministic generator uses seeds 0–4 in short tests and 0–39 otherwise,
with depths 1 and 4 and seven paired private worlds. It appends private source
observations before or after the common public transition: waiting, closed,
stale, landed and unavailable. Histories include testing, latched invalidation
and discard; public transitions cover head changes, closure, dequeue request,
retarget and pause, with CI provenance in batched worlds. API queue lists, queue,
PR, change and revision, briefs and decisions, CLI output, real `Server.report`
forge arguments and durable records are compared byte for byte. The report test
also checks initial write counts and unchanged-pass deduplication. Fixed round 3
cases exercise all five public transitions with CI provenance, and the retained
round 1/2 matrix and regressions remain in place.

The larger 40-seed non-short generator and short race generator pass. The short
race generator's serve package took 14.362 seconds; its wall command included
lock wait and is not a CI comparison. No real GitHub requests are made by these
fixtures.

Nine targeted mutations were applied one at a time to a disposable copy within
the worktree. Each produced an assertion failure, not a compiler or fixture
failure:

| Removed or changed behavior | Assertion that failed |
| --- | --- |
| Queue-wide entry projection | `TestDependencyMultiQueuePrivacyGoldens` |
| Batch-reference withholding | `TestDependencyEntryAllowlist` |
| Pending-detail withholding | `TestDependencyPendingPublicationPrivacy` |
| Complete check projection | `TestDependencyPendingPublicationPrivacy` |
| Authorization-sensitive cache identity | `TestDependencyCheckGrantRevocation` |
| Missing-journal fail-closed behavior | `TestDependencyCheckMissingJournalFailsClosed` |
| Admitted-check deduplication | `TestAstraR2PersistedCadence` |
| Missing-ledger isolation | `TestDependencyMissingLedgerEntryDoesNotAbortReport` |
| Retained-check revocation refresh | `TestDependencyRetainedCheckRevocation` |

These are targeted mutations of the named behaviors, not a claim that every
historical commit was reversed wholesale. The temporary source copy was removed.
Local command logs and individual assertion output are in `.r3-evidence/` in the
worktree. The first full serve race run failed because the repository-wide
provenance scan saw that temporary copy; it is not counted as a successful run.

Final full `-race -short -count=1 -timeout=580s` package times were 49.290s
for status and 348.832s for serve. Both database environment variables were
exported to the disposable Postgres endpoint, and all Go invocations used the
requested lock and toolchain. The full CLI short suite passed; `golangci-lint
run ./...` reported zero issues and the changed Go files were formatted.
