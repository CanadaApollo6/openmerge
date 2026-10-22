# ADR 0008 PR 10a-1 evidence

Classification: **Inert by its own behavior; deploy after B**. Depends on Hold
PR 8 and transitively 3a–4. This foundation does not authorize deployment before
period B drains. It has no runtime caller and no advertised audit CLI.

Base main: `7aa71a3f20b7c7b1b7538aaaa5caf1fdf77dff3d`.

The implementation and normative v1 format are described in
[the approved design](adr-0008-10a-design.md#10a-1-implementation-contract).
Dependency closure, check/retry joins, historical-versus-missing evidence, CLI
and real-git evidence acceptance remain 10a-2. Its closure producer can supply
additional roles and references through the existing v1 fields without changing
serialization rules or bytes for existing inputs.

Acceptance results will be appended after running the committed verifier.
No passing result is claimed by this initial implementation checkpoint.

Reproduction (shared test Postgres only; isolated schemas, no database creation):

```sh
export GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3
export PATH="$GOROOT/bin:$PATH"
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
python3 scripts/verify-adr0008-10a-1.py \
  --baseline /home/riels/.cache/openmerge-10a-1/main \
  --out /home/riels/.cache/openmerge-10a-1/acceptance
```

The verifier applies `scripts/adr0008_scratch_db.py`'s DSN guard before any
subprocess. Every Go invocation runs under the shared heavy flock, GOFLAGS=-p=2,
4 GiB memory (6 GiB strict replay), with GOROOT and PATH pinned. All temporary
build/log artifacts are under the cache directory; export staging stays under
its output parent. Eight owned CPU burners are terminated and reaped in finally.
