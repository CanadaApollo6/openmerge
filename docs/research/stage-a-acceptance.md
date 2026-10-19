# Stage A exit report

Trial deployment class: **Hold** (depends on held PRs). This is an acceptance
report for Stage A on main `d45e709`, with 11b (#78), reporting 12a (#79), and
outside pushes (#53) included. It authorizes no deployment or dogfood approval
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
| Organization team owners, installation permission and real receipt acceptance | [Sandbox artifact and exact runbook](stage-a-sandbox-evidence.md) | **PENDING maintainer-provided sandbox**; no real resources/actions executed, no GitHub credentials in CI |
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

Stage A's real organization-sandbox exit remains pending until a maintainer
fills and reviews the sandbox artifact. Passing fake-forge and differential
gates does not close that outstanding acceptance criterion.
