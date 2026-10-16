# Contributing to OpenMerge

Thanks for helping build an open merge queue. OpenMerge is early (see the [roadmap](ROADMAP.md)), so the most valuable contributions right now are design feedback, ADR discussion, and focused code that moves a roadmap phase forward.

## Ground rules

- Be kind and assume good faith. Everyone here follows the [Code of Conduct](CODE_OF_CONDUCT.md).
- Discuss before building anything large. Open an issue, or comment on an existing one, so we agree on the approach before you spend time on it.
- Decisions that are expensive to reverse go through an [ADR](docs/adr/README.md).
- Report security problems privately. See [SECURITY.md](SECURITY.md).

## Developer setup

You need Go (the version in `go.mod`), `git`, and `make`. If you use [mise](https://mise.jdx.dev), `mise install` in the repo root installs the pinned Go and golangci-lint versions from `mise.toml`. Without mise, install the golangci-lint version listed in `mise.toml` by following [its install guide](https://golangci-lint.run/welcome/install/).

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
| `make build` | Builds `bin/openmerge` |
| `make check` | All of the above |
| `make db` | Starts a disposable Postgres 18.6 container, or user-owned Postgres through mise |
| `make db-stop` | Stops the development Postgres |

The test suite never needs forge credentials. Engine tests use the in-memory fake forge ([ADR 0004](docs/adr/0004-forge-driver-interface.md)). Store tests use real Postgres through `OPENMERGE_TEST_DATABASE_URL`, and skip with a clear message when it is unset ([ADR 0002](docs/adr/0002-storage-and-state-model.md)). CI always supplies a Postgres 18.6 service. Each database test creates and removes its own schema, so the configured database user needs permission to create schemas.

For local store development, run `make db`, export the URL it prints, then run `make check`. The target prints the selected runtime: it uses Docker when `docker info` succeeds without sudo, otherwise it installs `postgres@18.6` through mise and runs `initdb` and `pg_ctl` as your own user. Postgres is deliberately absent from `mise.toml`, so other contributors do not download it. Both runtimes listen only on localhost, on port 54329. Stop the database with `make db-stop`; Docker data is removed with its disposable container, while mise's data remains in `/tmp/om-pg` until you remove it.

The mise path can also be run directly, without Docker or sudo:

```bash
mise install postgres@18.6
PG=$(mise where postgres@18.6)
"$PG/bin/initdb" -D /tmp/om-pg -U postgres --auth=trust
"$PG/bin/pg_ctl" -D /tmp/om-pg -o '-h localhost -p 54329 -k /tmp' -l /tmp/om-pg.log -w start
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
make check
"$PG/bin/pg_ctl" -D /tmp/om-pg -w stop
```

`OPENMERGE_DEV_PGDATA` overrides the mise data directory used by `make db` and `make db-stop`. Its default local cluster uses trust authentication; use it only for development.

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

`git commit -s` adds it for you. A CI check rejects pull requests with commits that are not signed off. Merge commits, and pull requests opened by GitHub bot accounts such as Dependabot, are exempt; the maintainer who merges a bot's PR takes responsibility for it. To fix existing commits:

```bash
git commit --amend -s --no-edit          # the last commit
git rebase --signoff origin/main         # every commit on your branch
git push --force-with-lease
```

We chose the DCO over a CLA deliberately: contributors keep their copyright, and no single party gains the right to relicense their contributions.

## AI-assisted contributions

OpenMerge is built for a world of coding agents, and agent-written contributions are welcome under the same rules as any other: a human signs off, and that human is responsible for the change. Review your agent's diff before opening the PR, and say in the description which parts were generated. Do not submit changes you have not read.

## License

By contributing, you agree that your contributions are licensed under the [Apache License 2.0](LICENSE).
