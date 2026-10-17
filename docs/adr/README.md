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
| [0008](0008-openmerge-policy-authority.md) | Let GitHub enforce who can write, and OpenMerge enforce everything else | Proposed |

## Writing a new ADR

1. Copy [`template.md`](template.md) to `NNNN-short-title.md` with the next free number.
2. Open a pull request with status `Proposed`. Discussion happens on the PR.
3. The project lead sets the status to `Accepted` or `Rejected` when merging. See [GOVERNANCE.md](../../GOVERNANCE.md).

Write an ADR when a choice is expensive to reverse: a dependency we build on, a storage or wire format, a public interface (config, API, CLI, CI contract), or a security boundary.
