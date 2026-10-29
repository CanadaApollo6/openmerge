# ADR 0011 slice 5-1 evidence

This PR delivers the shared WaitingReason and audience-scoped CLI, API and check
presentation. Activation remains the private `dependencyPass` test seam. The
public loader still rejects dependency keys. The base is `939fdf5`.

The full slice includes a second authorization-sensitive export format, retained
self-acknowledgement evidence, wait-age and pagination. That work plus its privacy
and integrity tests is expected to exceed the requested 2,500-line limit when
combined with this implementation. This PR ships **5-1 first**. **5-2** retains
scoped audit export, distinct completed self-acknowledgement status/export, the
opt-in multi-writer warning, wait-age and additional evidence pagination.

## Item-to-test map

| Item | Evidence |
| --- | --- |
| Shared `dependencies` reason and typed authorized diagnostics | `status.TestDependencyAuthorizedDiagnosticsAndPrecedence`; `TestDependencyExplicitPinPresentation` |
| Whole-response source-state confidentiality | `status.TestDependencyPrivacyGoldensNoRenderingFetch`; `testdata/dependency-private-api.json`, `dependency-private-check.json` |
| Actual CLI table and explanation output | `main.TestDependencyCLIPrivacyGolden`; `cmd/openmerge/testdata/dependency-private-cli.txt` |
| Conceal unstarted progress, invalidation, cancellation and retries | `status.TestDependencyUnstartedProgressPrivacy` replays validated composition companions and asserts actual queue phases/discard before byte comparisons |
| Historical status and brief confidentiality | `TestDependencyHistoricalRevisionWithholdsCurrentDetails`; `TestDependencyUnstartedProgressPrivacy` compares historical full status and reviewer brief bytes |
| API grants cannot authorize a public check | `serve.TestDependencyStatusActivationRemainsPrivate` uses a valid API grant with an unknown check audience |
| Source identity cannot be substituted with an unrelated repository grant | `status.TestDependencySourceGrantConfusionFailsClosed`; `TestDependencySourceBindingGuards` |
| Retained decision pagination cannot bypass source scope | `TestDependencyPrivacyGoldensNoRenderingFetch` includes decision-error goldens, authorized diagnostics tests retain paging, and `serve.TestDependencyStatusCacheUsesJournalWatermark` exercises the real server gate |
| Keep public redaction out of the private check-cache identity | `serve.TestDependencyStatusCacheUsesJournalWatermark` fails before the cursor fix and passes after it; repeated rendering reuses the same cache without forge reads |
| Preserve authorized original references and pre-pagination row selection | `status.TestDependencyAuthorizedReferencesRemainOriginal` checks original watermark/IDs and the complete public evidence count |
| No forge reads while rendering | The privacy golden source embeds a nil `forge.Driver`, so any forge method call fails. `ProjectDependencies` accepts only supplied journal records, bindings and recorded grants. Its reader lookup is an in-memory map. |
| Generic errors for invalid binding | `status.TestDependencyMissingDiagnosticAndBindingFailClosed` |
| Strict new companion and exact observation association | `dependency.TestDiagnosticStrictCodec`; `TestDiagnosticTypedCodesAndGuards`; `TestDiagnosticProjectionRejectsInvalidContext`; `TestDiagnosticFullJournalProjectionAndRedaction` |
| No authority or snapshot-format changes | `dependency.TestDiagnosticPreservesAdmissionAndSnapshotTail` |
| Downgrade refusal for the actual new event | `store.TestDependencyDiagnosticRealEventDowngrade` isolates a stored `dependency_diagnostic_recorded` row so other kinds/cursors cannot mask the boundary |
| Private activation and codec ownership | Existing `TestNoProductionDependencyImports` and `TestNoProductionNewEventOrSnapshotWriters` retain narrow read-only presentation exceptions |
| Existing precedence and inactive output | `status.TestWaitingReasonPrecedence`, `TestWaitingProjectionAndPolicy`, `TestChangeReportingGoldens`; the complete status/CLI suites and their unchanged existing goldens |

## Privacy matrix

The source-state matrix holds B's own journal facts fixed. Each source receives a
different number of observation, diagnostic and clock records. The API request
uses `events=1`, exposing pagination/count/ID leaks if filtering happens too late.

| Source A state | Unknown audience | Denied reader | Identity mismatch or stale grant | One allowed and one denied reader | Complete allowed audience with matching source binding |
| --- | --- | --- | --- | --- | --- |
| Missing observation or unresolved source | Generic, byte-identical | Same | Same | Same | Generic without a diagnostic; typed unresolved when recorded |
| Open and waiting | Same | Same | Same | Same | `dependency-waiting` |
| Closed without verified landing | Same | Same | Same | Same | `dependency-closed` |
| Explicit revision superseded | Same | Same | Same | Same | `dependency-stale` |
| Landed | Same, with no success signal | Same | Same | Same | `dependency-landed`; the dependency presentation does not block |
| Unavailable, out of scope, retargeted, absent from base or unverified | Same | Same | Same | Same | Recorded typed diagnostic |

All unauthorized cells use `code: dependencies` and exactly
`Dependency cannot be verified`. Details, source links and typed diagnostics are
absent. Public checks use their own complete audience proof, independently of API
readers. An API token, App access or `audience=operator` is not that proof.

A second matrix compares queued, building, testing, latched invalidation, exact
discard with cancellation, retryable build failure and fast-forward histories.
Full current API/check output and historical API/brief output remain identical
for unauthorized readers. The fixture uses the composed reader and verifies that
work actually starts and that discard actually retires a composition.

B's own terminal outcome, in-flight landing settlement, pauses and independent
admission gates retain precedence. These externally visible B facts are not
claimed to remain identical if B itself closes or lands. This confidentiality
contract hides source diagnostics and unstarted progress, not B's actual terminal
outcome.

## Mutations

Run the mutation verifier with:

```sh
python3 scripts/verify-adr0011-slice5.py --output /tmp/slice5-mutations.json
```

It archives committed HEAD into a temporary directory, runs a passing control,
mutates only that isolated copy, restores each file and requires an assertion
failure. Compiler failures, timeouts and surviving mutants do not count as kills.
The final run killed all **24** mutants:

| Mutation | Killing test |
| --- | --- |
| One-bit landed success leaks through the generic view | `TestDependencyPrivacyGoldensNoRenderingFetch` |
| Authorize only the first reader | Same |
| Accept a denied reader | Same |
| Use an unrelated repository's source grant | `TestDependencySourceGrantConfusionFailsClosed`, `TestDependencySourceBindingGuards` |
| Let private rows consume explanation pagination | `TestDependencyAuthorizedReferencesRemainOriginal` |
| Let clock rows consume pagination | Same |
| Expose private journal ID gaps | `TestDependencyPrivacyGoldensNoRenderingFetch` |
| Reveal unstarted batch presence | `TestDependencyUnstartedProgressPrivacy` |
| Publish the raw check prefix | `TestDependencyPrivacyGoldensNoRenderingFetch` |
| Reuse an API grant for the public check audience | `TestDependencyStatusActivationRemainsPrivate` |
| Attach current diagnostics to historical revisions | `TestDependencyHistoricalRevisionWithholdsCurrentDetails` |
| Attach old declaration details to a new consumer revision | Same |
| Accept inconsistent diagnostic code/result | `TestDiagnosticTypedCodesAndGuards` |
| Accept a diagnostic for another observation | Same |
| Expose redacted explanation rows | `TestDependencyPrivacyGoldensNoRenderingFetch`, `TestDependencyUnstartedProgressPrivacy` |
| Remove the generic historical hold | `TestDependencyUnstartedProgressPrivacy` |
| Reveal consumer progress fields | Same |
| Drop the composed invalidation overlay | Same |
| Remove the diagnostic history bound | `TestDiagnosticProjectionLimit` |
| Show a dependency hold for an empty independent declaration | `TestDependencyEmptyDeclarationHasNoPresentation` |
| Use the public withheld watermark as a cache key | `TestDependencyStatusCacheUsesJournalWatermark` |
| Expose raw retained decision positions | `TestDependencyPrivacyGoldensNoRenderingFetch` |
| Bypass the server decision scope check | `TestDependencyStatusCacheUsesJournalWatermark` |
| Omit the new kind from the downgrade inventory | `TestDependencyDiagnosticRealEventDowngrade` |

An initial mutation pass found surviving clock/batch mutants and one mutant that
failed compilation. The tests now verify complete authorized evidence counts and
actual batch phases, and the diagnostic mutant preserves compilation. Only the
subsequent assertion failures count above.

## Validation and timing

Every Go command uses the requested lock, `GOFLAGS=-p=2`, `GOMEMLIMIT=4GiB`, Go
1.27.3 and its PATH. Both Postgres environment variables point to the disposable
test service on port 54329. No command uses the protected main checkout, live DB,
status ports, real GitHub APIs, systemd or live `openmerge/*` refs.

The comparison uses an isolated detached main worktree at `939fdf5`. The paired
serve commands use `go test -race -short -count=1 -timeout=580s ./internal/serve`.
The shell imposes a 590-second timeout. Reader packages run with the same flags
and package order. The heavy lock prevents concurrent Go runs. The host has 16
logical cores; load was 2.16 at the initial check. All reported runs must exit
successfully, and `-count=1` prevents cached test results.

All seven package suites pass with `-race -short -count=1`. Go test-package times:

| Package | Main | Branch | Delta |
| --- | ---: | ---: | ---: |
| serve | 311.946s | 313.562s | +1.616s |
| store | 232.095s | 230.751s | -1.344s |
| status | 10.463s | 12.019s | +1.556s |
| dependency | 8.563s | 8.797s | +0.234s |
| journalreader | 21.304s | 20.479s | -0.825s |
| audit | 106.009s | 104.978s | -1.031s |
| CLI | 103.192s | 103.179s | -0.013s |

The serve row is the final consecutive main/head pair on the frozen implementation.
Earlier candidate pairs were 311.631s → 313.481s (+1.850s), then 315.295s →
313.104s (−2.191s). These show suite variation; they are not speedup evidence.

Whole-command times, including the wrapper and build/link work:

| Command | Main | Branch | Delta |
| --- | ---: | ---: | ---: |
| Final consecutive serve pair | 312.502s | 319.351s | +6.849s |
| Store pair | 235.257s | 234.172s | −1.085s |
| Five-package reader group | 140.191s | 139.725s | −0.466s |

The final serve test-time delta is **+1.616s**, within the requested approximately
+3s test budget. The command delta includes a branch rebuild against a cached main
binary: work outside test execution was 0.556s on main and 5.789s on the branch.
That extra 5.233s is not test execution. The cached branch confirmation is below.

With the branch build cached, the confirmation passes in **313.878s** test time
and **314.400s** command time. Against the cached main baseline, those deltas are
**+1.932s / +1.898s**. This supplementary run separates compilation-cache cost
from suite runtime; it is not another independent consecutive main/head pair.

The reader rows are the paired five-package runs. After the final history-bound
and empty-declaration tests were added, the complete status/dependency packages
were rerun with race/short: **11.999s / 8.972s**, both passing. After retained
paging was gated, the targeted status/serve dependency and cache matrix also
passed with race/short. The final full serve pair includes that gate.


These are local paired samples, not statistical estimates of CI duration. The
suite includes race instrumentation, disposable database fixtures and deliberate
poll/settlement waits. The numbers measure complete test execution, not a renderer
microbenchmark or production throughput. No speedup is claimed.

`golangci-lint run ./...`: **0 issues**. Gofmt and `git diff --check` are clean.

## Contract interpretations

- API v1 explicitly permits additive fields. `Entry.dependency` and optional
  typed diagnostics are omitted from inactive responses, preserving existing
  golden bytes. Persisted event payloads are untouched.
- Redacted activated views use `0` to mean an original event reference or
  watermark is withheld. They never substitute an ordinal that could name an
  unrelated journal event. Redacted explanations are withheld before pagination;
  authorized views retain original IDs. This new sentinel convention belongs
  only to the test-activated dependency view, not existing deployed queues.
- Source identity comes from a supplied recorded source-journal binding. The
  diagnostic's alias cannot authorize itself by naming an unrelated repository.
  Audience grants reuse `ScopedObservation.View` and its all-readers rule.
- The new diagnostic companion preserves distinctions that existing
  `dependency_observed` events intentionally cannot encode. It changes neither
  admission nor clocks. Missing or stale companions fail closed. Presentation
  rebuilds them from the full journal; snapshot codecs and versions are unchanged.
- Current composed queue state supplies latched invalidation even before the
  explicit discard row. Historical views clear the current overlay and details,
  while unauthorized historical views retain a generic hold.
- Source authorization is the supplied recorded observation boundary. Rendering
  does not recheck a forge or claim continuous permission enforcement.
- Canonical ADR 0008 operator exports retain their existing scope and integrity
  contract. A redacted B-audience export is a separate 5-2 deliverable; this PR
  does not silently modify canonical exports or claim their privacy acceptance.

## Round 1 fixes

The original privacy matrix covered only B. Independent queue entries and
co-batched changes could reveal private progress or `dependency-changed`
invalidation; the check projection cache also outlived its authorization.

Redacted views now withhold pre-landing progress, batch membership, derived gates,
and CI run history across every entry, including independent co-batched changes
and speculative suffixes. Dependency check prefixes use the queue's redaction
scope. Terminal outcomes, admission conditions, pauses and durable landings keep
their existing precedence. The generic dependency hold also appears consistently
in `why.summary`.

The ledger cache key includes a digest of the dependency binding, complete check
audience and recorded source bindings, in addition to the private journal
watermark. Missing, denied or stale authorization rebuilds a fail-closed view.
Queued check actions use the same prefix redaction as periodic checks. Persisted
outgoing checks are used for deduplication, but their text is rebuilt from current
authorization before authority paragraph refreshes.

Cache audit: API reads load journals through `Server.Journals` and reapply
`dependencyJournals`; briefs derive from those projections. HTTP responses retain
`Cache-Control: no-store`. Reviewer-check cache reuse refreshes dependency
projections before use, although Stage A/dependency co-activation remains
rejected. Governing-content caches contain immutable commit-pinned inputs, not
dependency audience decisions. Durable outgoing checks are compared with newly
rendered bytes before deduplication, and the authority refresh path no longer
reuses their previously authorized display text.

A single unauthorized dependency redacts the **whole queue view**: the public
watermark becomes zero, `why.events` is empty, `/decisions` returns 503, and
pre-landing batch progress and run ledgers are withheld for every entry. This is
an accepted degradation for slice 5-1; revisit audience isolation at activation
(slice 6). Activation remains test-seam only, with no new public configuration
or persisted wire changes. Inactive compatibility goldens remain unchanged.

The expanded matrix uses three entries at depth one (independent A, dependent B,
independent C) and six at depth four (plus three independent suffix changes).
Both separate-batch and co-batched layouts cover eight private-source states,
five progress histories and unknown/denied audiences. It compares full response
bytes for every entry, then retains SHA-256 goldens for each channel: queue list,
queue detail, PR, change/current and revision-one, brief/current and revision-one
for reviewer/operator/agent, decisions, policy projection, check state/reason/
summary and ledger text. CLI tests render the real queue and every PR display.
The fixture validates actual compositions, suffix invalidations and discards.

Revocation coverage renders and publishes an authorized diagnostic, revokes its
grant without a journal write, then requires the next projection and emitted
fake-forge check to be generic. Removing the whole audience or source binding
also fails closed. Cheap status-only tests cover non-admitted hold precedence,
current-brief warnings and both admitted/fallback ledger attachment.
