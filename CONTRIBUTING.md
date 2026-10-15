# Contributing to OpenMerge

Thanks for helping build an open merge queue. OpenMerge is early (see the [roadmap](ROADMAP.md)), so the most valuable contributions right now are design feedback, ADR discussion, and focused code that moves a roadmap phase forward.

## Ground rules

- Be kind and assume good faith. Everyone here follows the [Code of Conduct](CODE_OF_CONDUCT.md).
- Discuss before building anything large. Open an issue, or comment on an existing one, so we agree on the approach before you spend time on it.
- Decisions that are expensive to reverse go through an [ADR](docs/adr/README.md).
- Report security problems privately. See [SECURITY.md](SECURITY.md).

## Developer setup

You need Go (the version in `go.mod`), `git`, and `make`. If you use [mise](https://mise.jdx.dev), `mise install` in the repo root installs the pinned Go and golangci-lint versions from `mise.toml`.

```bash
git clone https://github.com/CanadaApollo6/openmerge
cd openmerge
make check   # formatting, lint, and tests: the same gates CI runs
make build   # produces bin/openmerge
```

| Target | What it does |
|---|---|
| `make test` | Runs every test with the race detector |
| `make lint` | Runs golangci-lint |
| `make fmt` | Fails if any file is not gofmt-formatted |
| `make check` | All of the above |
| `make build` | Builds `bin/openmerge` |

The test suite never needs forge credentials. Engine and end-to-end tests run against the in-memory fake forge ([ADR 0004](docs/adr/0004-forge-driver-interface.md)). Once storage lands, tests that need Postgres read `OPENMERGE_TEST_DATABASE_URL` and skip with a message when it is unset ([ADR 0002](docs/adr/0002-storage-and-state-model.md)).

## Making a change

1. Fork the repo and create a branch from `main`.
2. Keep each pull request to one logical change. Small PRs get reviewed faster.
3. Add or update tests for any behavior change. Planner logic gets table-driven tests; queue behavior gets a fake-forge scenario.
4. Run `make check` before pushing.
5. Open a pull request and fill in the template. CI must pass and every commit must be signed off (below).

### Commit messages

Use the imperative mood ("Add batch bisection", not "Added"). Keep the subject under about 72 characters, and use the body to explain why.

## Developer Certificate of Origin

OpenMerge uses the [Developer Certificate of Origin](https://developercertificate.org/) (DCO) instead of a contributor license agreement. By signing off a commit, you certify that you wrote it or otherwise have the right to submit it under the project's Apache 2.0 license.

Sign off every commit by adding a trailer that matches your commit author:

```
Signed-off-by: Your Name <you@example.com>
```

`git commit -s` adds it for you. A CI check rejects pull requests with commits that are not signed off. To fix existing commits:

```bash
git commit --amend -s --no-edit          # the last commit
git rebase --signoff origin/main         # every commit on your branch
git push --force-with-lease
```

We chose the DCO over a CLA deliberately: contributors keep their copyright, and there is no mechanism for relicensing the project away from Apache 2.0.

## AI-assisted contributions

OpenMerge is built for a world of coding agents, and agent-written contributions are welcome under the same rules as any other: a human signs off, and that human is responsible for the change. Review your agent's diff before opening the PR, and say in the description which parts were generated. Do not submit changes you have not read.

## License

By contributing, you agree that your contributions are licensed under the [Apache License 2.0](LICENSE).
