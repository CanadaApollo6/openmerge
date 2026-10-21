# ADR 0008 PR 3b: setup and recovery evidence

Classification: **Inert, setup only**. Dependency PR 3a is Hold; this does not
make a binary containing that dependency eligible for live deployment.

Implementation baseline: `77b703e` (PR 3a, including PRs 1/2). The implementation
commit and source/binary hashes are recorded below after verification. No live
DB, status API, services, GitHub policies, teams, or sandbox state were touched.
The only GitHub operations for this work are fetching main and publishing this
implementation branch/PR. All automated GitHub interactions use `httptest`.

## Implemented contract

```sh
openmerge setup rulesets --repo OWNER/REPO --app-id APP_ID \
  --authority openmerge --config proposed.yml
openmerge setup rulesets --repo OWNER/REPO --app-id APP_ID \
  --authority openmerge --config proposed.yml --apply --backup install.json
openmerge setup rulesets --repo OWNER/REPO --app-id APP_ID \
  --authority openmerge --restore install.json                 # preview
openmerge setup rulesets --repo OWNER/REPO --app-id APP_ID \
  --authority openmerge --restore install.json --apply
openmerge setup rulesets --repo OWNER/REPO --app-id APP_ID \
  --authority github --rollback-preflight --config github.yml \
  --installation-id INSTALLATION_ID --private-key APP_KEY
```

`GITHUB_TOKEN` is a separate repository-admin token, with Administration read for
preview/preflight and Administration write for apply/restore. An installation
credential does not substitute for it. The proposed file uses
`config.LoadAuthorityCandidate`; the runtime loader and published schema still
reject `policy_authority` and `break_glass`. Optional `--break-glass` repeats the
same exclusive actor object as JSON, e.g. `'{"team":"ORG/TEAM"}'` or
`'{"repository_admin":true}'`, and must agree with the file. Teams resolve through
the admin client; a missing, cross-organization or mismatched team is refused.
GitHub's repository-admin ruleset role is ID 5; the App Integration ID is never an
installation ID. Sandbox acceptance of these representations remains pending.

`openmerge-base` has active branch creation, strict update
(`update_allows_fetch_and_merge=false`), deletion and non-fast-forward rules.
Only the App and the resolved declared actor bypass, both `always`. Base includes
come from the 3a compiler, including the default branch and all proposed
selectors, with the OpenMerge namespace excluded.

`openmerge-refs` renames the local `openmerge-fence` in place, retains its ID,
coverage, unknown fields, rules and parameters, and adds paired `*` / `**/*`
patterns for fence, queue, green and verify. The same compiler module generates
these patterns; there is no second translator. Only the App bypasses, `always`.
No submission refs are installed. Inherited managed-name collisions, duplicate
names, ambiguous fence/refs pairs and incompatible managed shapes are refused.
Unrelated/inherited content policy is never edited, including its existing App
bypass. A retained content bypass may be removed only through ADR runbook step 7
after step 6's activation and settlement requirements, outside this setup plan.

Legacy `planRules` recognizes the successor by managed name, active branch
coverage of the entire fence family, strict four-rule shape, and exactly the
App bypass. It preserves additional coverage within fence/queue/green/verify
only, creates no second fence, and rejects ambiguous pairs/incompatible successor names. Its original `github`
setup preview/apply golden remains byte-identical.

Rollback preflight uses the complete paginated prospective branch inventory and
the actual `ValidateRules`, including inherited/push rulesets and protected ref
families. All four future families use the 3a coverage matcher's direct prefix
intersection, including wildcards within family names; no namespace substitution
is used. Partial exclusions cannot certify disjointness. Classic reads use
the admin client for every base and corresponding fence, including separate
signature reads when omitted. Every compatibility-relevant field and nested
value is checked for presence and type before SDK zero values can hide missing
settings (the required-field list is recorded in the round-2 results). The pure
`classicProtectionProblems` receives the actual App ID, `hasMerges` only on merge-method bases, and `needsForce=false`.
`Driver.ValidateClassicProtection` is never called. Explicit `Branch not
protected` establishes absence; generic 404, null/incomplete details and failed
reads block. Installation permission checks use the actual installation driver.

Each blocker is one line, sorted/deduplicated:

```text
BLOCKER source=rulesets ref="refs/heads/main" code=base_bypass_missing: ruleset "content" (123): ... Operator choices: explicitly approve adding an App bypass (loses App content enforcement), disable/remove the owned rule, or ask its inherited-policy owner to act. Retain before/after evidence; if none is acceptable, stay in authority mode.
BLOCKER source=classic ref="main" code=legacy_incompatible: [classic_signed_commits] ...
BLOCKER source=permissions ref="" code=runtime_grant: installed App missing permission contents: write ...
```

Runtime warning grants remain warnings. A missing installation client is a
blocker. Preflight collects blockers and exits 1, with no policy writes.

Authority `setup permissions` (via `--authority openmerge` or proposed `--config`)
recommends Administration read: it reads classic protection, does not grant
ruleset writes, and does not guarantee bypass-list visibility. Setup inspects a
fresh unrestricted installation token grant because runtime intentionally caches
only its existing permission set. This setup diagnostic does not install the
fresh token into the runtime cache. Runtime requirements, warnings and request
traces remain unchanged with and without Administration read.

## Recovery artifact and coordinated edits

The UTF-8 JSON file is exclusively created mode 0600 and synced, including its
parent directory, before the first write. Existing different/invalid captures
are never overwritten. Reusing an identical capture repairs its mode to 0600
and syncs it again before any mutation. Version 1 shape:

```json
{
  "version": 1,
  "repository": "OWNER/REPO",
  "app_id": 5383059,
  "plan_sha256": "<SHA-256 of proposed file, resolved actor and rotation flag>",
  "edits": [
    {"id": 0, "name": "openmerge-base", "before": null, "after": {"name": "openmerge-base", "...": "complete planned settings"}},
    {"id": 123, "name": "openmerge-refs", "before": {"name": "openmerge-fence", "...": "exact prior settings"}, "after": {"name": "openmerge-refs", "...": "complete planned settings"}}
  ]
}
```

Zero IDs mean creations. Response metadata is omitted, but unknown settings,
conditions, rules and parameters are retained. Only managed edits are captured;
credentials and unrelated policies are excluded. Credential-like content is
refused before capture. The immutable file is the recovery intent, not journal
verification. Retry with the same proposed file and `--backup` recognizes an
already-completed write by ID/name **and** full expected settings, including a
write whose HTTP response was lost. No mutable progress file is needed. Output
names completed operations and pending/uncertain operations. Restore traverses
captured edits in reverse, restores renamed/edited IDs and deletes only captured
creations. Already-restored operations are skipped. Every mutation has a fresh
inventory comparison and a read-back; unexpected/intervening edits refuse apply
or restore. Known omitted-false update defaults and unordered actors/patterns
compare semantically; unknown fields compare without omission.

GitHub PUT has no CAS. The operator must serialize all repository policy edits;
a read/write race cannot be eliminated by this client. A byte-identical concurrent
creation is indistinguishable from the captured creation after an uncertain POST.

For later selector edits, preview/apply the reviewed proposed file with a **new**
backup before landing it. Setup installs the union of installed and proposed
coverage; it never prunes old base/fence obligations or changes actors. Admin
verification, policy-owner approval, all affected queue digests and settlement
precede any pruning or creation of a newly matching branch. Applied admin
verification and activation/settlement checks belong to later ADR PRs; this
setup-only component cannot certify them.

For rotation, serialize policy edits and first confirm installation bypass lists
are hidden, with no unresolved finding requiring a seeing clear. Resolve/review
the new actor. Land the owner-approved policy naming only the new actor while
the old actor remains installed; wait for all affected digests and old attempts
to settle. Then use a new capture and the explicit flag:

```sh
openmerge setup rulesets --repo OWNER/REPO --app-id APP_ID \
  --authority openmerge --config landed-new-actor.yml --rotate-break-glass
openmerge setup rulesets --repo OWNER/REPO --app-id APP_ID \
  --authority openmerge --config landed-new-actor.yml --rotate-break-glass \
  --backup rotation.json --apply
```

This permits one base update changing only its bypass list, never an actor union
or a simultaneous selector/ref repair. Immediately run the future applied admin
`--verify` against landed policy and retain its event. If apply/verification
fails, stop the service until exact-list repair and applied verification succeed.
Visible bypass lists require a separately reviewed admission-pause/recovery
procedure. These operational preconditions are operator attestations, not
runtime checks added by this PR. Do not interpret setup's successful exit as
policy activation, settled attempts, or an applied verification receipt.

## Automated verification

Results and hash manifest are appended after the final check. Named coverage:

- `TestAuthorityFaultEveryHTTPBoundary` / `TestAuthorityRestoreFaultEveryHTTPBoundary`:
  inject HTTP 503 at every individual request, including both writes and all
  per-mutation guards/read-backs; retry and exact restoration.
- `TestAuthorityUncertainWritesAndActorAPIRejection`: committed POST/PUT with
  lost response, API bypass rejection, safe partial diagnostics, idempotent retry.
- `TestAuthorityCrashAfterFirstRuleset`: subprocess exit 73 after the first
  completed ruleset, then a new process/client resumes the immutable capture.
- Install/restore/union/rotation tests retain unknown top-level fields, rule
  fields and parameters; reject concurrent edits, unsafe actors, name collisions,
  unsupported conditions and inherited mutation; retain preactivation bypasses.
- Legacy preview/apply after rename and restoration, extra successor coverage,
  ambiguous pairs, and unchanged `github` golden; setup missing/adopted grant
  diagnostics with unchanged runtime permission results and HTTP traces.
- Rollback tests exercise real legacy validators, admin-only classic/signature
  reads, nested merge-method bases, all blockers, inherited unbypassed compatible
  content and missing installation credentials.
- `TestSetupWriteRestrictionsRejectGeneratedAtomicPush`: fresh bare remote/cache,
  real queue/fence generated objects, rejecting receive hooks on either atomic
  ref, stale leases, unchanged remote SHAs on failure and successful exact retry.
  This models local transport rejection; it does not prove GitHub enforcement.

## Sandbox acceptance runbook — planned, NOT executed

Only the maintainer/orchestrator runs this section. Do not unarchive the sandbox,
create teams, or change unrelated protection as part of PR 3b implementation.
Prerequisites: `Smart-Data-Ohio/openmerge-sandbox` is already writable through an
approved process; App **5383059**, installation **173082682**, and existing team
`Smart-Data-Ohio/break-glass` are present. Use separate tokens for the admin,
an ordinary writer outside the team, and a writer in that team. Stop if any
identity, existing old protection, or team resolution differs from the reviewed
inventory. Do not grant the ordinary writer an admin bypass.

Run in Bash, with the checked PR binary already built. Never enable `set -x` or
put tokens in command arguments, URLs, committed files or evidence. Keep this
entire evidence directory outside the repository and restrict it to its owner.

```bash
set -euo pipefail
umask 077
export REPO=Smart-Data-Ohio/openmerge-sandbox APP_ID=5383059 INSTALLATION_ID=173082682
export APP_KEY=/absolute/path/to/sandbox-app-private-key.pem
export OM=/absolute/path/to/reviewed/bin/openmerge
EVIDENCE="$HOME/openmerge-3b-sandbox-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -m 700 "$EVIDENCE"
read -rs -p 'Separate admin token (Administration write): ' ADMIN_TOKEN; echo
read -rs -p 'Ordinary writer token: ' WRITER_TOKEN; echo
read -rs -p 'Break-glass team writer token: ' GLASS_TOKEN; echo
export GITHUB_TOKEN="$ADMIN_TOKEN"
date -u +%FT%TZ > "$EVIDENCE/start-utc.txt"
"$OM" version > "$EVIDENCE/binary-version.txt"
sha256sum "$OM" > "$EVIDENCE/binary.sha256"
GH_TOKEN="$ADMIN_TOKEN" gh api "repos/$REPO" > "$EVIDENCE/repository-before.json"
DEFAULT=$(jq -r .default_branch "$EVIDENCE/repository-before.json")
GH_TOKEN="$ADMIN_TOKEN" gh api orgs/Smart-Data-Ohio/teams/break-glass > "$EVIDENCE/team.json"
for actor in ADMIN WRITER GLASS; do
  var="${actor}_TOKEN"
  GH_TOKEN="${!var}" gh api user > "$EVIDENCE/identity-$actor.json"
done
capture_rules() {
  local phase=$1 id
  GH_TOKEN="$ADMIN_TOKEN" gh api --paginate --slurp \
    "repos/$REPO/rulesets?includes_parents=true&per_page=100" > "$EVIDENCE/$phase-list.json"
  while read -r id; do
    GH_TOKEN="$ADMIN_TOKEN" gh api \
      "repos/$REPO/rulesets/$id?includes_parents=true" > "$EVIDENCE/$phase-ruleset-$id.json"
  done < <(jq -r '.[][] | .id' "$EVIDENCE/$phase-list.json")
}
capture_rules before
# Retain the HTTP status/body even for an explicitly unprotected default.
GH_TOKEN="$ADMIN_TOKEN" gh api --include \
  "repos/$REPO/branches/$(jq -rn --arg ref "$DEFAULT" '$ref|@uri')/protection" \
  > "$EVIDENCE/classic-before.http" 2>&1 || true
cat > "$EVIDENCE/proposed.yml" <<'YAML'
version: 1
policy_authority: openmerge
break_glass: {team: Smart-Data-Ohio/break-glass}
queues:
- name: main
  merge_when: [check-success = ci]
- name: sandbox-probes
  base: sandbox-3b/**
  merge_when: [check-success = ci]
YAML
sha256sum "$EVIDENCE/proposed.yml" > "$EVIDENCE/config.sha256"
"$OM" setup permissions --app-id "$APP_ID" --installation-id "$INSTALLATION_ID" \
  --private-key "$APP_KEY" --config "$EVIDENCE/proposed.yml" | tee "$EVIDENCE/permissions.txt"
"$OM" setup rulesets --repo "$REPO" --app-id "$APP_ID" --authority openmerge \
  --config "$EVIDENCE/proposed.yml" | tee "$EVIDENCE/preview.txt"
# Review the exact plan and existing old protection before this write.
"$OM" setup rulesets --repo "$REPO" --app-id "$APP_ID" --authority openmerge \
  --config "$EVIDENCE/proposed.yml" --apply --backup "$EVIDENCE/install.json" \
  | tee "$EVIDENCE/install.txt"
echo 0 > "$EVIDENCE/install.exit"
capture_rules after
GH_TOKEN="$ADMIN_TOKEN" gh api --include \
  "repos/$REPO/branches/$(jq -rn --arg ref "$DEFAULT" '$ref|@uri')/protection" \
  > "$EVIDENCE/classic-after.http" 2>&1 || true
"$OM" setup rulesets --repo "$REPO" --app-id "$APP_ID" --authority openmerge \
  --config "$EVIDENCE/proposed.yml" --apply --backup "$EVIDENCE/rerun.json" \
  | tee "$EVIDENCE/rerun.txt"
echo 0 > "$EVIDENCE/rerun.exit"
# Expected: "No ruleset changes needed.", no second fence, no edits to old
# content/classic protection. A zero-change fresh run creates no backup file.
```

Retain all before/after bodies. Confirm old protections remain byte/semantically
unchanged apart from response metadata, and any retained content App bypass
remains present. The default branch is always covered even when not `main`.
The probe selector is sandbox-only and is not landed or activated by setup.

Mint an App token using its key, without printing JWT/token credentials:

```bash
b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
NOW=$(date +%s)
HEADER=$(printf '%s' '{"alg":"RS256","typ":"JWT"}' | b64url)
PAYLOAD=$(printf '{"iat":%s,"exp":%s,"iss":"5383059"}' "$((NOW-60))" "$((NOW+540))" | b64url)
SIGNATURE=$(printf '%s' "$HEADER.$PAYLOAD" | openssl dgst -sha256 -sign "$APP_KEY" | b64url)
APP_JWT="$HEADER.$PAYLOAD.$SIGNATURE"
APP_TOKEN=$(GH_TOKEN="$APP_JWT" gh api --method POST \
  "app/installations/173082682/access_tokens" --jq .token)
unset APP_JWT SIGNATURE
cat > "$EVIDENCE/askpass.sh" <<'SH'
#!/bin/sh
case "$1" in
  *Username*) printf '%s\n' x-access-token ;;
  *Password*) printf '%s\n' "$PUSH_TOKEN" ;;
  *) exit 1 ;;
esac
SH
chmod 700 "$EVIDENCE/askpass.sh"
export GIT_ASKPASS="$EVIDENCE/askpass.sh" GIT_TERMINAL_PROMPT=0
mkdir -m 700 "$EVIDENCE/git-home"
export HOME="$EVIDENCE/git-home" GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
# Clear helper chains on every authenticated invocation, including clone.
git_probe() { git -c credential.helper= -c credential.useHttpPath=true "$@"; }
PUSH_TOKEN="$APP_TOKEN" git_probe clone --bare "https://github.com/$REPO.git" "$EVIDENCE/probe.git" \
  > "$EVIDENCE/clone.txt" 2>&1
export GIT_AUTHOR_NAME='3b sandbox acceptance' GIT_AUTHOR_EMAIL='sandbox@example.invalid'
export GIT_COMMITTER_NAME="$GIT_AUTHOR_NAME" GIT_COMMITTER_EMAIL="$GIT_AUTHOR_EMAIL"
GIT_DIR="$EVIDENCE/probe.git"; export GIT_DIR
TIP=$(git rev-parse "refs/heads/$DEFAULT")
TREE=$(git rev-parse "$TIP^{tree}")
S0=$(printf '3b probe root\n' | git commit-tree "$TREE" -p "$TIP")
S1=$(printf '3b probe child\n' | git commit-tree "$TREE" -p "$S0")
DIVERGED=$(printf '3b probe sibling\n' | git commit-tree "$TREE" -p "$TIP")
DEFAULT_DIVERGED=$(printf '3b default non-FF probe\n' | git commit-tree "$TREE")
printf '%s\n' "default=$TIP" "S0=$S0" "S1=$S1" "DIVERGED=$DIVERGED" "DEFAULT_DIVERGED=$DEFAULT_DIVERGED" > "$EVIDENCE/objects.txt"
REFS=(sandbox-3b/base)
for family in fence queue green verify; do
  REFS+=("openmerge/$family/3b-single" "openmerge/$family/3b/nested")
done
expect_reject() {
  local label=$1; shift
  local code=0
  "$@" > "$EVIDENCE/$label.response" 2>&1 || code=$?
  printf '%s\n' "$code" > "$EVIDENCE/$label.exit"
  if [ "$code" -eq 0 ]; then echo "UNEXPECTED SUCCESS: $label; stop and repair disposable refs" >&2; return 1; fi
}
# App creation works for a selector base and both depths in each ref family.
for ref in "${REFS[@]}"; do
  PUSH_TOKEN="$APP_TOKEN" git_probe push --porcelain origin "$S0:refs/heads/$ref" \
    > "$EVIDENCE/app-create-${ref//\//_}.response" 2>&1
done
# The actual default rejects both writer FF update and non-FF replacement.
PUSH_TOKEN="$WRITER_TOKEN" expect_reject writer-default-update git_probe push --porcelain \
  origin "$S0:refs/heads/$DEFAULT"
PUSH_TOKEN="$WRITER_TOKEN" expect_reject writer-default-force git_probe push --porcelain \
  "--force-with-lease=refs/heads/$DEFAULT:$TIP" origin "$DEFAULT_DIVERGED:refs/heads/$DEFAULT"
# Ordinary writer: create/update/delete/non-FF are all rejected on bases+refs.
# Team break-glass: those same operations are rejected on refs only.
for actor in writer glass; do
  token=$WRITER_TOKEN; [ "$actor" = writer ] || token=$GLASS_TOKEN
  for ref in "${REFS[@]}"; do
    [ "$actor" != glass ] || [ "$ref" != sandbox-3b/base ] || continue
    label="$actor-${ref//\//_}"
    PUSH_TOKEN="$token" expect_reject "$label-create" git_probe push --porcelain origin "$S0:refs/heads/$ref-create"
    PUSH_TOKEN="$token" expect_reject "$label-update" git_probe push --porcelain origin "$S1:refs/heads/$ref"
    PUSH_TOKEN="$token" expect_reject "$label-delete" git_probe push --porcelain origin ":refs/heads/$ref"
    PUSH_TOKEN="$token" expect_reject "$label-force" git_probe push --porcelain \
      "--force-with-lease=refs/heads/$ref:$S0" origin "$DIVERGED:refs/heads/$ref"
  done
done
PUSH_TOKEN="$APP_TOKEN" git_probe ls-remote origin > "$EVIDENCE/remote-after-rejections.txt"
# Expected: default remains TIP; every initialized probe remains S0.
# No *-create probe exists.
[ "$(awk -v ref="refs/heads/$DEFAULT" '$2 == ref {print $1}' "$EVIDENCE/remote-after-rejections.txt")" = "$TIP" ]
# The declared team can create/update/force/delete a covered disposable base.
for spec in "$S0:refs/heads/sandbox-3b/glass" "$S1:refs/heads/sandbox-3b/glass" \
            "+$DIVERGED:refs/heads/sandbox-3b/glass" ':refs/heads/sandbox-3b/glass'; do
  PUSH_TOKEN="$GLASS_TOKEN" git_probe push --porcelain origin "$spec" >> "$EVIDENCE/glass-base.response" 2>&1
done
# App update/non-FF/delete work on every covered probe; lease the known tip.
for ref in "${REFS[@]}"; do
  label="app-${ref//\//_}"
  PUSH_TOKEN="$APP_TOKEN" git_probe push --porcelain "--force-with-lease=refs/heads/$ref:$S0" \
    origin "$S1:refs/heads/$ref" > "$EVIDENCE/$label-update.response" 2>&1
  PUSH_TOKEN="$APP_TOKEN" git_probe push --porcelain "--force-with-lease=refs/heads/$ref:$S1" \
    origin "$DIVERGED:refs/heads/$ref" > "$EVIDENCE/$label-force.response" 2>&1
  PUSH_TOKEN="$APP_TOKEN" git_probe push --porcelain "--force-with-lease=refs/heads/$ref:$DIVERGED" \
    origin ":refs/heads/$ref" > "$EVIDENCE/$label-delete.response" 2>&1
done
PUSH_TOKEN="$APP_TOKEN" git_probe ls-remote origin > "$EVIDENCE/remote-after.txt"
unset GIT_DIR
# Guarded ruleset restore is optional and separately reviewed; preview first.
"$OM" setup rulesets --repo "$REPO" --app-id "$APP_ID" --authority openmerge \
  --restore "$EVIDENCE/install.json" | tee "$EVIDENCE/restore-preview.txt"
# Execute only if ending this isolated acceptance and restoring old settings:
# "$OM" setup rulesets --repo "$REPO" --app-id "$APP_ID" --authority openmerge \
#   --restore "$EVIDENCE/install.json" --apply | tee "$EVIDENCE/restore.txt"
unset APP_TOKEN ADMIN_TOKEN WRITER_TOKEN GLASS_TOKEN GITHUB_TOKEN
find "$EVIDENCE" -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > "$EVIDENCE/SHA256SUMS"
```

Expected positive pushes exit 0; each `expect_reject` retains a nonzero numeric
exit and the actual GitHub rejection response (normally GH013/ruleset failure).
A generic auth/network failure is **not** an enforcement pass: inspect the
response and rerun after fixing credentials. Confirm remote SHAs remain `S0`
after the rejection matrix, and capture that inventory before App success tests.
Add timestamps/exits for every positive command to the retained terminal log.
Keep the capture, rejection responses, before/after API bodies, config and object
SHAs and hash manifest. Do not publish credentials or the private key. If old
protection blocks an App push, preserve its rejection and defer acceptance;
never silently add bypasses or remove content protections to make it pass.

## Round-1 executed results

The executable source revision is
`4d4441956291e5e07d945e046e8dac9dfd4c65ac`; the follow-up evidence commit changes
only documentation/artifacts. Verification was executed on 2026-10-20 with the
required host lock, `GOFLAGS=-p=2`, and `GOMEMLIMIT=4GiB`. Earlier development
failures were corrected and superseded checks are not final acceptance evidence.

- Final `make check`: **exit 0**, formatting, zero lint issues, all race tests,
  PostgreSQL integration at `localhost:54329`, simulator, real Git and build.
  Log: `/home/riels/.cache/openmerge-3b-final-check.log`.
- Final focused suite: **exit 0**. Fault injection passed at all **32 apply** and
  **30 restore** HTTP boundaries; lost-response retries, API actor rejection,
  subprocess crash (expected exit **73**), exact restore, numeric/unknown fields,
  collision/concurrency/inherited policy and single-update rotation passed.
  Log: `/home/riels/.cache/openmerge-3b-focused.log`.
- Three original `github` setup goldens (default, repair preview, repair apply)
  were captured by running the unmodified setup on **77b703e**, then compared
  byte-for-byte against this implementation. All pass. The baseline capture log
  is `/home/riels/.cache/openmerge-3b-baseline.log`; captured files are under
  `internal/forge/github/testdata/setup-rules/`. Runtime permission results and
  request traces are unchanged with and without Administration read.
- Strict offline replay: **exit 0**, gates 2/4 **PASS**. State, structural state,
  actions, snapshot hashes and boundary checkpoints match **2,159/2,159**;
  **8,676** boundary probes agree, maximum **12**, cap **128** not hit. Repeat
  determinism, all **22** self-tests and **7/7** synthetic checkpoints pass.
  Report: `/home/riels/.cache/openmerge-3b-replay/report.json`.
- The baseline/candidate/repeat replay stream SHA-256 is identically
  `1fe079295539183b7008e70b940c7f71d45bd4aa4e443c8c1e581fa10d31051a`.
  Governing config commit `6fa7d082a9888f86fdec9f4f80473070c7fb4be6`, config SHA-256
  `3efb43bbc05f5f0b03c005331f79064829fa018e9f6446224682e00dd357fead`, durable digest
  `e58c893b8b3af7685dc07a086824b3bc1fc044456083d193f9566f8387cb58fc` at source
  event **1287** remain unchanged; no replacement config event would be emitted.
  The cached export contains 2,158 events through event 2158; its latest recorded
  timestamp is `2026-10-18T01:08:55.427607-04:00`. No live journal was read.
- Real-Git rejection/lease cases pass with the fixed fixture clock
  `2026-10-15T12:00:00Z`. A per-ref `update` hook rejects either base or fence;
  both remote refs stay unchanged under the atomic push. A non-atomic control
  advances only the allowed base, proving the atomic assertion is meaningful.
  Stale leases reject before the hook. Removing the hook lets the same generated
  test/fence objects and original leases land together. The focused log and
  manifest retain actual OIDs and rejection text; fence nonces are intentionally
  random per fixture.
- Runbook Bash syntax: **exit 0**, all five shell blocks, without executing them.
  Real-GitHub enforcement remains **planned/unexecuted**, as requested.

The checked-in [artifact manifest](adr-0008-3b-artifacts/manifest.json) records
revision/timestamp, log and binary hashes, fixture hashes, replay metrics and
actual generated-object evidence. No implementation-plan deviations; activation,
applied admin verification and coordinated operational attestations remain with
their owning later PRs. The sandbox run above is pending maintainer execution.

Reproduce the offline comparison without contacting GitHub or a database:

```sh
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB \
  GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local \
  python3 scripts/check-live-journal-replay.py \
  --baseline /home/riels/.cache/openmerge-3b-baseline-77b703e \
  --events /home/riels/.cache/om-pr72-fix1-input/events.jsonl \
  --queues /home/riels/.cache/om-pr72-fix1-input/queues.jsonl \
  --config-file .openmerge.yml --strict-state \
  --out /home/riels/.cache/openmerge-3b-replay/report.json
```


## Round-2 review fixes

The rollback overlap proof now consumes each literal family prefix through the
3a matcher's pathname-glob states. `*` and `?` match within one segment;
nonterminal `**` has a zero-directory transition and an any-directory loop;
terminal `**` remains segment-local. After consuming the prefix, any reachable
state with a remaining segment conservatively means a future descendant can be
matched. This checks fence, queue, green and verify at both depths and beyond,
without requiring an existing ref. Existing bases retain `ValidateRules`' exact
include-minus-exclude matching. A whole-family recursive exclusion (including
wildcards in its fixed prefix), `~ALL`, or exact include/exclude cancellation
can prove absence; other partial exclusions retain the potential overlap.
Unsupported include or exclude grammar fails closed before exclusion proofs.

Classic protection reads require these wire fields before the pure validator:

- `required_status_checks`: explicit null, or an object containing boolean
  `strict`, a string array `contexts`, and an object array `checks` whose entries
  have string `context` values.
- `required_pull_request_reviews`: explicit null, or an object with
  `bypass_pull_request_allowances.apps`, an array of objects with positive
  integer App `id` values.
- `restrictions`: explicit null, or an object with the same complete `apps`
  array representation.
- `required_linear_history.enabled`, `allow_force_pushes.enabled`, and
  `lock_branch.enabled`: present booleans.
- `required_signatures.enabled`: present boolean when embedded; omission of
  `required_signatures` alone triggers the separate signatures endpoint, which
  must return a non-null object with boolean `enabled`. Embedded null or partial
  signatures are incomplete reads.

Only the exact GitHub 404 message `Branch not protected` establishes unprotected
status. Generic 404s, missing fields, wrong types, and unknown nested structures
block rollback. Regression coverage includes Astra's URL-only response and a
54-case missing/null/wrong-type field matrix, plus malformed array members.

Legacy successor recognition now uses `managedRefsShape`, bounding all extra
coverage to fence/queue/green/verify. Base branches, `~ALL`, unrelated namespaces
and wildcard family prefixes are refused without writes. Reused identical
recovery files are chmodded to 0600 and synced again; the regression checks
0644, 0666, 0700 and already-safe 0600 modes without changing capture bytes.

The planned sandbox runbook uses an isolated Git HOME, no global/system Git
configuration, and clears credential helpers with `credential.useHttpPath=true`
for clone and every authenticated probe. The ordinary writer rejection matrix
now also attempts a fast-forward update and a truly non-fast-forward replacement
of `$DEFAULT`, leases its original tip, and verifies that tip remains unchanged.
These GitHub probes remain planned and unexecuted. This round fetched Git refs
but performed no GitHub writes or live-system operations.

Round-2 verification on 2026-10-20 used the required host lock,
`GOFLAGS=-p=2`, and `GOMEMLIMIT=4GiB` for every Go command:

- `make check`: **exit 0**, formatting, zero lint issues, all race tests,
  PostgreSQL integration at `localhost:54329`, simulator and build.
  Log: `/home/riels/.cache/openmerge-3b-round2-check.log`.
- Explicit `TestAuthorityFaultEveryHTTPBoundary` and
  `TestAuthorityRestoreFaultEveryHTTPBoundary`: **exit 0**, all **32 apply**
  and **30 restore** boundaries, including retry and exact restoration.
- `TestLegacySetupMainGoldenByteIdentical`: **exit 0**, all three original
  `github` outputs still match byte-for-byte. Their checked-in bytes also match
  the round-1 head `fb79c302a4cd0d4f447a77daae5ac424f4fff5ec` exactly.
- `TestAuthoritySetupPermissionGuidanceAndUnchangedRuntimeTrace`: **exit 0**,
  unchanged runtime permission results and HTTP request traces with and without
  Administration read. The fault/golden/trace log is
  `/home/riels/.cache/openmerge-3b-round2-fault-golden.log`.
- Corrected targeted coverage/rollback/classic/successor/backup/trace/golden
  regression run: **exit 0** for `internal/authority` and `internal/forge/github`.
  Log: `/home/riels/.cache/openmerge-3b-round2-targeted.log`.
- All five Bash/sh runbook blocks pass `bash -n`; none was executed.

The first targeted run exposed a test-data error (a valid string context was
listed as invalid). Replacing that value with a numeric context corrected the
fixture; the full race suite above includes the corrected test. No production
change was needed for that development failure. Round-1 replay evidence and its
artifact manifest remain historical records; no new replay or live deployment
was required for these setup-only review fixes.

## Operator implementation refinement (#114)

`openmerge admin acknowledge --queue <queue-id>` uses the numeric durable
`queues.id`, including for retired queues. Read the queue identity with
`openmerge queue status --json` or `GET /v1/queues`
on the operator API, then resolve its numeric ID using the operator SQL lookup in
[the operating guide](../operating.md#acknowledging-outside-pushes). Current status
responses expose identity fields, not `queues.id`.
This refines the implementation notation; the accepted ADR Decision is unchanged.
