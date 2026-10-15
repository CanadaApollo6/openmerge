# 0001. Implement OpenMerge in Go

- **Status:** Accepted
- **Date:** 2026-10-15

## Context

OpenMerge is a long-running service: it ingests forge webhooks, reconciles state, schedules speculative CI runs, and talks to several forges. The [roadmap](../../ROADMAP.md) commits us to three properties that the language choice affects directly.

- **Self-hostable in five minutes.** One binary plus Postgres, on Linux, macOS, and containers.
- **Forge-portable.** GitHub first, then GitLab, Forgejo/Gitea, and GitHub Enterprise Server.
- **Outsiders can run the tests.** Contributors need a toolchain they can install quickly and a test suite that runs without credentials.

The closest open-source prior art is split between Go (Prow/Tide, policy-bot, bulldozer, luci-go, gitea-mq, shunt) and Rust (rust-lang/bors). See [the landscape research](../research/landscape.md).

## Decision

OpenMerge is written in Go, targeting the current stable release (Go 1.27 at the time of writing). The module path is `github.com/CanadaApollo6/openmerge`. Release artifacts are statically linked binaries named `openmerge`.

We prefer the standard library and add dependencies only when they carry real weight (a forge SDK, the Postgres driver, a YAML parser). Each new direct dependency is called out in its pull request.

## Consequences

- One `go build` produces the single binary the roadmap promises, with trivial cross-compilation.
- Mature, maintained SDKs exist for every forge we target: `google/go-github`, GitLab's official Go client, and the Gitea SDK.
- Goroutines and `context` fit the workload: many concurrent queues, each a serial owner of its own state, with cancellable CI waits.
- The contributor pool for Go infrastructure tooling is large, and the domain's prior art reads naturally.
- We give up Rust's stronger compile-time guarantees. We compensate with the pure, table-tested planner described in [ADR 0002](0002-storage-and-state-model.md), the race detector in CI, and a fake-forge simulation suite ([ADR 0004](0004-forge-driver-interface.md)).

## Alternatives considered

- **Rust.** rust-lang/bors proves the model works in production on one of the hardest open-source CI workloads, and the type system would help encode queue states. It lost on iteration speed for a young project, a smaller contributor pool for this kind of service, and less mature forge SDKs beyond GitHub.
- **TypeScript (Probot).** The easiest path to a GitHub App and the largest contributor pool. It lost because it is the weakest fit for a long-running scheduler, single-binary self-hosting, and multi-forge support; Probot is GitHub-only.
- **Python.** Mergify's 2022 engine was Python with asyncio and Redis. It lost on packaging and deployment (no single binary) and on throughput per core for the reconciliation loop.
