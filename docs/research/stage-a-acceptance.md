# Stage A exit report

Trial deployment class: **Hold** (unchanged; this update is **inert, docs only**).
This report retains the automated acceptance baseline on main `d45e709`, with
11b (#78), reporting 12a (#79), and outside pushes (#53) included, and adds the
2026-10-19 real organization-sandbox run completed with landing fixes through
main `521c280` (#82 and #84). It authorizes no deployment or dogfood approval
configuration. The automated gates and their actual statuses are recorded in
[12b evidence](stage-a-12b-evidence.md).

| ADR 0007 Stage A exit criterion | Automated evidence / operating evidence | Status and limit |
| --- | --- | --- |
| Agent opens a PR and reaches landed or definitively rejected through CLI/MCP | [`TestStageAAcceptanceCLI`](../../cmd/openmerge/stage_a_acceptance_test.go) uses real read-only CLI handlers against the production HTTP projections, fake authenticated queue/receipt comments and the real worker/planner/executor/store | CLI journey targets confirmed `landed` in all five scenarios; MCP remains later work. Temporary approval rejection is a wait, not a definitive terminal rejection |
| Low-risk change lands on policy alone | `TestStageAAcceptanceCLI/depth-1/low-risk-none`; [`TestStageANoneStillRequiresExactHeadForkTrust`](../../internal/serve/stage_a_test.go) | `none` requires no reviewer but retains CI and trust checks |
| Human review waits for the exact head | `TestStageAAcceptanceCLI/depth-1/exact-head-human`; [`TestStageAFreshNegativeStancesBeforeStart`](../../internal/serve/stage_a_fresh_gate_test.go) | Wrong-head review cannot land; fresh eligibility is rechecked at atomic start |
| Receipt-required intent review and every landing decision read back | `TestStageAAcceptanceCLI/depth-1/receipt-intent`; [`TestStageAStaleReceiptAfterIntentEdit`](../../internal/serve/stage_a_test.go); [`TestChangeDecisionsIndexedReadbackAndSettlement`](../../internal/store/change_decisions_test.go) | Native review alone cannot satisfy receipt policy; accepted digest/comment source retained in immutable decision, with original eligibility/decision/settlement IDs and tested/landed SHAs |
| Owned and uncovered non-policy files use ADR 0008's two independent rules | `TestStageAAcceptanceCLI/depth-1/owned-non-policy` and `/uncovered-non-policy` | `paths: ['**']` retains human count; `touches: ['**']` adds owner obligations only for covered files. Non-owner count review cannot satisfy the owned file; uncovered file still needs count approval |
| Legacy sibling remains unchanged | Each acceptance scenario audits sibling config digest, no Stage A observations/pins, and no generated trailers; [`TestStageAMixedRepositoryActivation`](../../internal/serve/stage_a_activation_test.go); [legacy reporting comparison](../../scripts/check-legacy-reporting.py) | Full acceptance repeats depths 1/4; legacy checks remain byte-identical. Differential gates compare against main |
| Accountability, classification and approvals are visible without mutable forge reads | [API contract](../api.md#change-reporting-and-immutable-decisions-stage-a-12a), [12a reporting evidence](stage-a-12a-evidence.md), CLI show/brief/decisions in the acceptance journey | Historical absence remains `not recorded`; account authentication cannot prove a person acted. Separate bot/App identities are recommended |
| Organization team owners, installation permission and real receipt acceptance | [Sandbox artifact and exact runbook](stage-a-sandbox-evidence.md) | **EXECUTED 2026-10-19, all five PASS** in Smart-Data-Ohio/openmerge-sandbox: real team owner review, installation permission diagnostic (operator-reported), exact-head exclusion and accepted receipt binding; source/provenance limits retained in the artifact; no GitHub credentials in CI |
| Outside pushes included | [PR #53](https://github.com/CanadaApollo6/openmerge/pull/53), [`outside_push_test.go`](../../internal/serve/outside_push_test.go), [outside-push operating guide](../operating.md#outside-push-observations) | Included in the baseline and required full checks; observations retain their uncertainty and original journal event IDs |
| Safe adoption and rollback | [Final operating guide](../operating.md#stage-a-approval-activation), [006 evidence](stage-a-9b-evidence.md), [007 evidence](stage-a-12a-evidence.md) | All old workers must drain; pre-use backup and database-restore rollback required. Both index migrations run non-concurrently in the drained window |

The acceptance fixture uses a real isolated schema in the shared test Postgres;
it never reads the live OpenMerge database, refs, systemd units or status API.
The fake forge's immutable object map supplies reporting's local-cache reads.
Scripted review identities model accounts; automated tests cannot prove human
intent. The five scenario names identify independent outcomes, not a production
throughput measurement.

Conflict grace is explicitly **excluded**. Delivered conflicts remove work as
today; there is no grace deadline or retry budget. This report does **not** claim
cancellation at depth 1. Existing speculative cancellation at greater depth is
covered by its own tests and differential evidence, not a new Stage A guarantee.
Automatic old-worker detection is **not implemented**: leases lack binary
capability IDs. Operators must verify the all-host drain manually.

Installation grants are checked explicitly with `openmerge setup permissions`;
runtime policy resolution uses `ResolveTeam` and `TeamMembers`, without an
automatic grant diagnostic. Definite identity/membership unavailability creates
a durable `ConfigSource` block for the affected queue; transient or incomplete
reads retry without that durable block. See the [operating guide](../operating.md#organization-membership-permission-stage-a-prerequisite)
for the diagnostic and adoption procedure.

Stage A's organization-sandbox criterion is now evidenced by the
[completed sandbox artifact](stage-a-sandbox-evidence.md#evidence-record--completed-2026-10-19).
All five cases reached confirmed landed outcomes; the artifact records the
operator's setup report, before/after CLI summaries, immutable decision IDs,
receipt binding, public GitHub verification and source-file hashes. Missing
setup transcripts, full exit-status logs and repeated decision comparisons are
explicit evidence limits, not assumed successes. This does not authorize a
production deployment or change the exclusions above.

Known limitations from the real run remain open:

- [#83: fake/real forge parity gaps](https://github.com/CanadaApollo6/openmerge/issues/83).
  The history-limit mismatch ([fixed #82](https://github.com/CanadaApollo6/openmerge/pull/82))
  and missing tested-commit acquisition before cache-only reads
  ([fixed #84](https://github.com/CanadaApollo6/openmerge/pull/84)) each blocked
  every real-git approval landing and were invisible to the fake-forge suite.
- [#85: status API client timeouts](https://github.com/CanadaApollo6/openmerge/issues/85)
  during testing/landing, observed twice in case 2.
- [#86: misleading `unknown_eligibility` briefs](https://github.com/CanadaApollo6/openmerge/issues/86)
  during approval waits, including the receipt-required wait. Successful
  immutable decisions establish the landed evidence despite those brief warnings.

An external GitHub Actions incident also delayed case 1. These findings are
retained alongside the successful acceptance results.
