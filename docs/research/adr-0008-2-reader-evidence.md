# ADR 0008 PR 2: authority reader evidence

## Scope and revisions

Executed on 2026-10-20 against archived
`Smart-Data-Ohio/openmerge-sandbox`. Manual API observations are independent of
application code and tests. All protection, repository, organization and team
requests were GETs. The only POST minted an installation token, explicitly
authorized by the maintainer; it changed no repository, organization or ruleset
state. The token remained in process memory, was never printed or saved, and
was left to expire naturally at `2026-10-20T15:37:25Z`.

- Current PR 1 base: `0d1396a` (#97), itself based on main `e9b24e8`.
- PR 2 was rebased with `--onto 0d1396a dbb0a4e`; only its two commits were
  replayed. The old main merge was dropped; there are no merges above main.
- Rebased reader implementation: `bfdd078`; initial evidence: `f309213`.
- Reader adaptations and expanded contracts: `ccbe40f7dac4c926b883cd44ed3e957958f1c061`.
- The original sandbox read used reader `7b5351b` on old PR 1 `118b3a8` plus
  prerequisite merge `dbb0a4e`. Its retained transcript remains applicable:
  targets and bypass JSON were already recorded, and no sandbox GET was repeated.
- No binary was deployed or invoked against GitHub. No simulator used GitHub.
- Governing sandbox commit: `b4e0468c1838b4f944677dddcaa762516b992018`.
- `.openmerge.yml` blob: `f576d185500c612b0a422d1bc7b23118b6f98589`;
  SHA-256 `ff2eb2fd76625af26743796656827c1e3f617b84af2984ecd9bedcaf761f1731`.
  Its existing queue selects literal `main`. No authority configuration/digest
  exists in that file; no configuration or policy was changed.
- `.github/CODEOWNERS` blob: `634d06e7af4add773133353cd51e20aae80d2ac2`.
- No source event IDs exist: this PR emits no events and made no DB observations.

## Callers and installed grant

API transcript timestamp: `2026-10-20T14:37:24.426383+00:00`.
The [sanitized JSON transcript](adr-0008-2-reader-evidence.json) retains the
method, path, HTTP status, numeric command-equivalent exit, request ID and
sanitized response body for every read. Token response retention excludes the
token and includes only status, expiry and accepted permission grants. Identity
objects are reduced to stable ID/login/type. No response bodies are logs from a
production reader invocation.

- Admin token: fetched locally with `gh auth token -u CanadaApollo6`; repository
  metadata reports `permissions.admin: true`.
- Sandbox App: `openmerge-sandbox-smartdata`, App ID `5383059`.
- Installation/accepted-grant identity: `173082682`, selected repositories;
  target organization `Smart-Data-Ohio`, ID `262436228`.
- Repository grant target: repository ID `1425820965`.
- App declaration, installation metadata and newly minted installation token all
  reported: `actions`, `checks`, `contents`, `pull_requests`, `workflows`: `write`;
  `issues`, `members`, `metadata`, `statuses`: `read`.
- `administration` is absent in the actual installation grant. Nothing requested
  or granted it. It remains diagnostic guidance, with sandbox adoption at PR 11.
- GitHub exposes no separate numeric ID for each permission grant in these
  responses; the installation ID identifies this accepted grant, and repository
  and organization IDs identify its targets. No grant ID is invented.
- Break-glass declaration remains `{team: Smart-Data-Ohio/break-glass}`. Both
  callers got generic HTTP 404 from that team's endpoint. Its stable team ID
  was **unavailable**, not proven absent. No fallback actor was substituted.

## Observed protection

Both `includes_parents=false` and `includes_parents=true` returned exactly one
repository ruleset: `openmerge-fence`, ID `25415566`, source type `Repository`,
source `Smart-Data-Ohio/openmerge-sandbox`, target `branch`, enforcement `active`.
All four inventories completed on page 1 with no next Link. Its detail contains:

```json
{
  "conditions": {"ref_name": {
    "include": ["refs/heads/openmerge/fence/*", "refs/heads/openmerge/fence/**/*"],
    "exclude": []
  }},
  "rules": [
    {"type": "creation"}, {"type": "update"},
    {"type": "deletion"}, {"type": "non_fast_forward"}
  ]
}
```

The admin detail includes:

```json
"bypass_actors": [
  {"actor_id": 5383059, "actor_type": "Integration", "bypass_mode": "always"}
]
```

The installation detail **omits** `bypass_actors`, despite reporting
`current_user_can_bypass: "always"`. That latter field does not prove the whole
bypass list or exclusive App ownership. The reader's fixture-tested mapping is
visible targets/rules and hidden bypasses; it supplies no synthetic actor.
The admin's visible list maps to App ID `5383059`, never installation ID
`173082682`. Both timestamps normalize to revision `2026-10-19T15:40:58.537Z`.

Independent classic reads for `main` and `openmerge/fence/main` returned:

- Admin: HTTP 404 with exact message `Branch not protected`; this explicitly
  establishes classic-protection absence for those two existing branches.
- Installation: HTTP 403 `Resource not accessible by integration`; classic
  protection is unreadable, not absent. This does not erase ruleset evidence.

The org-level inventory (`GET /orgs/Smart-Data-Ohio/rulesets?per_page=100&page=1`)
returned HTTP 403 for both callers, with different reasons: the admin received
`Upgrade to GitHub Team to enable this feature.`, while the installation received
`Resource not accessible by integration`. No inherited rulesets appeared in
either completed repository inventory. The unavailable org endpoint is not
treated as a completed empty org inventory or a blanket claim that no
organization protection exists.

The sandbox already has the older fence, but not the future pair of authority
write rulesets. Comparison against actual base/fence/queue authority rules and
break-glass enforcement remains deferred to PRs 3b/11. This read-only run proves
real bypass visibility and independent source permissions, not authority-mode
adoption or enforcement.

## Read transcript and real git

Manual harness commands, numeric exits and artifacts:

```text
python3 /tmp/adr0008-2-sandbox-read.py
exit 0; 22 GETs, one explicitly authorized token-mint POST (HTTP 201)
GET success -> command_exit 0; HTTP 403/404 -> command_exit 22
(the Python HTTP harness records these curl-compatible HTTP exits per request)

git clone --mirror https://github.com/Smart-Data-Ohio/openmerge-sandbox.git \
  /home/riels/.cache/openmerge-adr0008-2/sandbox.git
exit 0

git --git-dir=/home/riels/.cache/openmerge-adr0008-2/sandbox.git \
  for-each-ref --format='%(refname) %(objectname)' refs/heads/
exit 0
refs/heads/main b4e0468c1838b4f944677dddcaa762516b992018
refs/heads/openmerge/fence/main f250fa16051cf3b1c0dce01f9b4301983daa1e6c

git --git-dir=/home/riels/.cache/openmerge-adr0008-2/sandbox.git ls-tree -r main -- \
  .openmerge.yml .openmerge.yaml .github/openmerge.yml .github/openmerge.yaml \
  .github/CODEOWNERS CODEOWNERS docs/CODEOWNERS
exit 0
100644 blob 634d06e7af4add773133353cd51e20aae80d2ac2 .github/CODEOWNERS
100644 blob f576d185500c612b0a422d1bc7b23118b6f98589 .openmerge.yml
```

These are reads of a local mirror. No remote ref, archived flag, ruleset or App
setting was changed. The real sandbox has no nested base or queue-family ref to
enumerate; no branches were invented in its evidence. The disposable automated
`TestAuthorityReaderWithRealGitRefInventory` exercises nested
`release/2026/oct`, `openmerge/fence/release/2026/oct`,
`openmerge/queue/nested/batch-1` and `main`/its fence against a fresh bare remote
and gitops cache, enumerates and reads their actual SHAs, and verifies the reader
leaves every remote ref unchanged. This follows the existing CLI-backed contract
and Stage A worker fixture pattern without selecting a production consumer.

## Parity matrix and verification

`forgetest.RunWriteRestrictionReader` runs for both fake and HTTP GitHub drivers:

- visible, visible-empty, omitted and null bypass lists;
- unauthorized users, deploy keys, organization admins and exempt team bypasses,
  retained as visible inventory without errors or findings;
- unknown actor kinds with exact `RawKind`, with and without an actor ID;
- branch, tag, push and repository targets;
- 1,001-item overflow for includes, excludes, conditions, condition values,
  rules, rule parameters, rule values and bypass actors, retaining other elements;
- inherited visible/hidden bypasses and inaccessible inherited details;
- completed empty inventories, later-page failure, generic inventory 403/404,
  and inaccessible details retaining listed identity/enforcement;
- quota errors, deep-copy isolation, fresh organization/team ID comparison,
  unavailable actors, repository-admin actors and invalid reader inputs.

HTTP-only fixtures additionally check malformed/repeated IDs and pagination,
primary versus secondary quota errors (including partial inventories), unknown
parameters, malformed/hidden rules and conditions, malformed bypass actors,
optional class-wide actor IDs, enterprise actors, nested object-array associations,
JSON pointer escaping, all three org-condition forms and exact 1,000-item limits,
UTC revisions, binding mismatch, conditional revalidation, settlement reserves
and independence from legacy permission/validation behavior. Tests and the fake
never contact the actual API. Seeds are fixed: App `42`, installation `7`, repo
`99`, rulesets `10`/`11`, team `44`, organization `55`; the real-git fixture uses
`2026-10-20T00:00:00Z` commit timestamps.

Initial validation executed against reader code revision `7b5351b16aa040f84647bf83340500b2068f3cc6`
(with only documentation changes uncommitted), starting at
`2026-10-20T14:41:55.797981985Z`, completed at `2026-10-20T14:50:18.539869+00:00`:

```sh
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB \
  OPENMERGE_TEST_DATABASE_URL='postgres://postgres:openmerge@localhost:54329/postgres?sslmode=disable' \
  make check
# exit 0
```

Formatting, lint (zero issues), the full race-enabled short test suite (including
PostgreSQL integration tests on localhost:54329) and the build all passed. The
new reader/shared/real-git fixtures also passed a focused wrapped `go test
./internal/forge/... -run 'Test(WriteRestriction|UnconfiguredAuthority|Authority)'
-count=1` (exit 0). No test flag or Makefile was changed. The built binary's
version is `7b5351b-dirty` because the evidence documentation was still being
written; that binary was never invoked against GitHub. The
original check log was superseded by the rebased validation log below.

## Inert evidence

Only tests invoke `ObserveWriteRestriction`. No serve/config/queue/executor
consumer selects it. This PR changes no event, wire fixture, schema, migration,
Makefile, CI file or test flag. `forge.PushOptions` remains PR 3a work.
Classic and installation-permission sources remain independent.

`runtime_rules.go`, `rules.go`, `classic_protection.go`, `permissions.go`,
`auth.go` and `client.go` are byte-identical to merged main `e9b24e8` and PR 1. The runtime reader file SHA-256 is
`972447dbe976f95a9ae9904b1f384af660331555ae8362ca71a042ee2b01f2e8` in both
baseline and candidate. The full legacy fixture suite plus
`TestAuthorityConditionalReadsKeepLegacyValidationIndependent` verify unchanged
problem/warning output before and after authority reads, continued conditional
revalidation, unchanged hidden-list App substitution, and no new Administration
grant requirement. No production deployment, live DB access, systemd change, live
status call, event emission, or actual remote ref write occurred.

The current stack is main → PR 1 at `0d1396a` → PR 2 commits, with no merge
commits. Shared validation now delegates to PR 1's `WriteRestrictionInput.Validate`,
including `ErrInvalidInput` wrapping and #92's ref validators, plus the reader's
1,000-ref input bound. Real enforcement comparison remains deferred to PRs 3b/11;
org/team visibility limitations above remain explicit.

## Verification after rebase

`ccbe40f` maps `Protection.Target` directly, separates ref includes/excludes from
other condition leaves, and normalizes nested rule/condition paths according to
`authority/evidence.go`. Scalar arrays retain their order and empty arrays remain
explicit. The observed bypass inventory covers every known actor kind, optional
IDs and unknown kinds, and accepts `always`, `pull_request` and `exempt`. Missing
or null bypass JSON remains hidden. Collection overflow fails only its evidence
element; independently acquired facts remain available.

The focused shared fake/HTTP, authority and real-git reader suite passed (exit 0):

```sh
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB \
  go test ./internal/forge/... -run 'Test(WriteRestriction|UnconfiguredAuthority|Authority)' -count=1
```

The clean implementation checkout `ccbe40f` passed `make check` from
`2026-10-20T15:08:37Z` through `2026-10-20T15:17:11Z`, using the wrapped
`make check` command in the log and PostgreSQL only on localhost:54329. Formatting, lint (zero
issues), the full short race suite and production build all passed. The binary
version is `ccbe40f`; it was never deployed or invoked against GitHub. The
[complete check log](adr-0008-2-reader-check.log) records exit 0.

The existing `scripts/adr-0008-1-verify.py` wrapper was rerun against clean main
`e9b24e8` and candidate `ccbe40f`, using the retained offline exports. Its `invoke`
function was overridden in process to force `GOMEMLIMIT=4GiB` for every command,
including replay; the script and test flags were unchanged. All invocations
retained `flock`, `GOFLAGS=-p=2`, and disabled module/toolchain downloads.
The [retained inert report](adr-0008-2-reader-inert.json) includes exact commands,
exits, export hashes and detailed artifact paths.

- Wrapper exit 0; strict replay gates 2 and 4 PASS. All 2,159/2,159 checkpoint
  actions, structural states, snapshot hashes and deadline-boundary checkpoints
  match, including the repeated candidate stream. No non-allowlisted differences.
- The authority projection is nil at all 2,159 checkpoints in each stream.
- All 90 approval/legacy contract checkpoints at depths 1 and 4 match exactly;
  action counts, action bytes and snapshot bytes have zero deltas.
- The untouched baseline decoder preserves the populated v6 snapshot and
  outside-push cursor bytes and rejects candidate authority v7.
- All 94 baseline wire fixtures remain byte-identical. This reader adds none.

Focused fixture construction initially exposed a list/detail target mismatch in
the new test harness; it was corrected before the successful full check. The
first replay preflight rejected a generated Python bytecode directory; it was
removed and bytecode generation disabled before the passing run. No failed run
was treated as acceptance evidence. No sandbox request was repeated.

The [artifact SHA-256 manifest](adr-0008-2-reader-evidence.sha256) covers this
report, the sanitized transcript, the check log and the inert report. No raw
credential-bearing artifact is included.
