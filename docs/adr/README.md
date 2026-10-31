# Architecture decision records

An ADR records one significant decision: the context that forced it, what we chose, and what follows from it. We keep them short and never rewrite history. To change a decision, write a new ADR that supersedes the old one and update the old one's status line.

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-implementation-language.md) | Implement OpenMerge in Go | Accepted |
| [0002](0002-storage-and-state-model.md) | Postgres only, with queue state derived from an append-only event log | Accepted |
| [0003](0003-configuration-format.md) | Native `.openmerge.yml` with a condition grammar, plus a Mergify importer | Accepted |
| [0004](0004-forge-driver-interface.md) | All forge access goes through a narrow driver interface, with a fake forge from day one | Accepted |
| [0005](0005-verification-modes.md) | Offer post-merge verification as a per-queue mode alongside strict landing | Accepted |
| [0006](0006-multi-repository-serving.md) | Serve every installed repository from one process, driven by webhooks with polling as the backstop | Accepted |
| [0007](0007-changes-not-prs.md) | Make the change the unit of work, with pull requests as one optional view | Accepted |
| [0008](0008-openmerge-policy-authority.md) | Make OpenMerge authoritative for queue policy behind forge write restrictions | Accepted |
| [0009](0009-speculative-batching.md) | Test a speculative chain of batches with an adaptive window | Accepted |
| [0010](0010-agent-provenance.md) | Agent provenance in queue policy and audit | Accepted |
| [0011](0011-cross-repository-dependent-changes.md) | Cross-repository dependent changes | Accepted |
| [0012](0012-hosted-service-architecture.md) | Hosted service architecture | Accepted |
| [0013](0013-ci-cost-executors.md) | Reduce CI cost with pluggable CI executors | Proposed |
| [0015](0015-ci-efficiency-engine.md) | CI efficiency engine | Proposed |

| [0014](0014-review-cards-risk-aware-batching.md) | Review cards and risk-aware batching | Proposed |

## Writing a new ADR

1. Copy [`template.md`](template.md) to `NNNN-short-title.md` with the next free number.
2. Open a pull request with status `Proposed`. Discussion happens on the PR.
3. The project lead sets the status to `Accepted` or `Rejected` when merging. See [GOVERNANCE.md](../../GOVERNANCE.md).

Write an ADR when a choice is expensive to reverse: a dependency we build on, a storage or wire format, a public interface (config, API, CLI, CI contract), or a security boundary.
