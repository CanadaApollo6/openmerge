# Stage A organization sandbox acceptance — EXECUTED

**EXECUTED 2026-10-19: all five cases PASS.** The maintainer's operator
executed this runbook against real GitHub in the isolated sandbox. The evidence
record below summarizes retained JSON read-backs and per-case `RESULT.md` files;
this documentation PR is trial class **inert (docs only)**. It does not authorize
a deployment or dogfood approval configuration. CI still uses a fake forge and
needs no GitHub credentials. The commands below are the retained operator
procedure, not instructions to rerun against the dogfood repository or live
OpenMerge database/status endpoint.

## Resources and setup

1. Use organization `Smart-Data-Ohio` and repository
   `Smart-Data-Ohio/openmerge-sandbox`, a public throwaway repository to archive
   after the run. The organization uses GitHub Free, where repository rulesets
   are available only for public repositories. Seed the repository with a `ci`
   GitHub Actions workflow triggered by `pull_request` and by `push` to
   `openmerge/queue/**`. Supply a separate sandbox OpenMerge App, installation,
   database, cache, webhook/API endpoints and service process.
   Record the App/installation IDs, binary commit and configuration commit.
   Follow [App/ruleset setup](../../README.md#running-openmerge-serve) in those
   resources only. CI must run on `openmerge/queue/**`. Drain old sandbox workers,
   verify the backup, and run startup migrations 006/007 in the drained window
   as described in [operating](../operating.md#stage-a-approval-activation).
2. Request organization `members: read` in sandbox App settings, then explicitly
   accept the grant in the sandbox organization's installed-App settings.
   Renew installation tokens/restart the sandbox process. Capture both the
   declaration and actual accepted installation permissions. Record existing
   repository grants (contents write, pull requests write, checks write, metadata
   read, and any setup/ruleset grants); the command below reports missing grants.
3. Use `RielStAmandSD` as the agent/PR author and `CanadaApollo6` as the
   independent count reviewer. The `Smart-Data-Ohio/owners` team member is
   `chrisstamand`, needed only for the owned-file owner approval. Add that
   colleague to `Smart-Data-Ohio/owners`, verify team visibility with the
   installation, and give eligible humans repository write permission. The PR
   author, submitter and accountable humans cannot satisfy their own approvals.
   Keep `CanadaApollo6` outside those exclusions and outside the owners team for
   the owned/uncovered comparison. Keep credentials outside the artifact.
4. Use a read-only sandbox status token for CLI/API reads and the selected
   human's forge credentials for review/comment commands. A token authenticates
   an account, not proof that a person acted. Use separate bot/App identities.

```sh
# Repository selected; fill in the remaining sandbox values before execution.
export REPO='Smart-Data-Ohio/openmerge-sandbox'
export OPENMERGE_API_URL='https://SANDBOX_STATUS_ORIGIN'
export OPENMERGE_API_TOKEN_FILE='/secure/sandbox-status-token'
openmerge setup permissions --app-id APP_ID --installation-id INSTALLATION_ID --private-key /secure/sandbox-app.pem
```

The command above is the explicit installation-grant diagnostic; the runtime
Stage A pipeline does not run it automatically. Runtime `ResolveTeam` and
`TeamMembers` reads validate referenced identities and membership. Definite
unavailability creates a durable `ConfigSource` block for the affected queue;
transient or incomplete reads retry without a durable configuration block.

Commit this configuration to the sandbox default branch, verifying the selected
team and CI names first. Preserve policy-file ownership before activation.
The rules are additive: `paths` retains the human count for the selected
uncovered files; `touches` adds ownership only where CODEOWNERS coverage exists.

```yaml
version: 1
classification:
  docs: ['docs/**']
queues:
  - name: sandbox
    base: main
    merge_when: [check-success = ci]
    approvals:
      default: {human: true, count: 1}
      rules:
        - when: {labels: [docs]}
          require: none
        - when: {paths: ['receipt/**']}
          require: {human: true, count: 1, receipt: true}
        - when: {paths: ['owned/**', 'uncovered/**']}
          require: {human: true, count: 1}
        - when: {touches: ['owned/**', 'uncovered/**']}
          require: {code_owners: true}
```

```text
# .github/CODEOWNERS — deliberately no catch-all owner
/.openmerge.yml @Smart-Data-Ohio/owners
/.openmerge.yaml @Smart-Data-Ohio/owners
/openmerge.yml @Smart-Data-Ohio/owners
/openmerge.yaml @Smart-Data-Ohio/owners
/.github/CODEOWNERS @Smart-Data-Ohio/owners
/CODEOWNERS @Smart-Data-Ohio/owners
/docs/CODEOWNERS @Smart-Data-Ohio/owners
/owned/ @Smart-Data-Ohio/owners
```

## Execute and retain read-back

For each case, have the separate agent account open a new PR. Use a temporary
sandbox checkout and a new branch for each case; put a small change in the path
below, commit and push that branch, then run `gh pr create --repo "$REPO"`.
Record its author ID, head SHA, number and URL. As the authorized queue-command
account, enqueue through the authenticated comment path:

```sh
gh pr comment <number> --repo "$REPO" --body '@openmerge queue'
```

Derive the canonical change ID from the PR alias lookup (the CLI has no
submit/write command). The ID is null until the first revision is observed;
retry the lookup for at most 30 attempts, then fail clearly. Once recorded,
follow the journey using only change reads and forge-authenticated commands. Escape the base as one API path component if it
contains `/`.

```sh
attempt=0
CHANGE=''
while [ "$attempt" -lt 30 ]; do
  openmerge queue status --repo "$REPO" --base main --pr <number> --json > alias.json || exit 1
  CHANGE=$(jq -er '.pr.change.id // empty' alias.json) && break
  attempt=$((attempt + 1))
  sleep 2
done
if [ -z "$CHANGE" ]; then
  echo 'ERROR: no canonical change ID after 30 alias lookups; first revision not observed.' >&2
  exit 1
fi
openmerge change show "$CHANGE" --repo "$REPO" --base main --json > before-show.json
openmerge change brief "$CHANGE" --repo "$REPO" --base main --audience reviewer --json > before-brief.json
openmerge change decisions "$CHANGE" --repo "$REPO" --base main --json > before-decisions.json
```

Record these cases in separate rows. The executed run used the single
path-scoped configuration above for all cases.
For a future policy change, use a reviewed sandbox configuration change and wait
for the new effective digest.

| Case | Configuration/path | Expected observation before landing |
| --- | --- | --- |
| Low-risk none | The `when: {labels: [docs]}` / `require: none` rule; change `docs/example.md` | CI and policy alone permit landing; no human approval required |
| Exact head | `default: {human: true, count: 1}`; no path rule matches; `src/example.txt` | Wrong-head review does not count; exact-head human review permits landing |
| Receipt intent | `receipt/**` rule requires human count 1 and receipt; `receipt/example.txt` | Native exact-head review alone waits; exact brief receipt is required |
| Owned non-policy | Path count and touches owner rules above; `owned/example.txt` | Count review by non-owner still waits for an eligible team owner |
| Uncovered non-policy | Same path count and touches owner rules; `uncovered/example.txt` | No owner obligation; human count still applies and must be satisfied |

For native review, select the independent human's forge session and submit an
approval for the exact `head_sha` recorded in the brief/show (GitHub review UI
or `gh pr review <number> --repo "$REPO" --approve`). Record the native review
ID and its actual `commit_id`; do not assume the branch remained unchanged.

For receipt intent review, re-read the reviewer brief until its digest is a
recorded lowercase 64-hex value. A `policy_busy` warning can leave the digest
as `not recorded`; never post that placeholder. Retry for at most 30 attempts,
then fail clearly. Inspect the saved brief before posting its exact digest as
the independent human reviewer:

```sh
attempt=0
DIGEST=''
while [ "$attempt" -lt 30 ]; do
  openmerge change brief "$CHANGE" --repo "$REPO" --base main --audience reviewer --json > before-brief.json || exit 1
  DIGEST=$(jq -er '.digest | select(type == "string") | select(test("^[0-9a-f]{64}$"))' before-brief.json) && break
  attempt=$((attempt + 1))
  sleep 2
done
if [ -z "$DIGEST" ]; then
  echo 'ERROR: no recorded 64-hex brief digest after 30 reads; do not post approval.' >&2
  exit 1
fi
# Review before-brief.json before running the posting command below.
gh pr comment <number> --repo "$REPO" --body "@openmerge approve $DIGEST"
```

Save the posted comment's URL and numeric ID. Read back the receipt and immutable landing decision, including all
pages (`next_cursor` is an exclusive event cursor; repeat with `--after N` until
zero). Repeat reads until the receipt is recorded and the attempt settles; a
worker-busy or still-waiting response is not a successful landing. Keep
before/after files and actual CLI exit statuses. Do not treat a
recorded approval-gate rejection as a terminal rejection: it can be satisfied.

```sh
openmerge change brief "$CHANGE" --repo "$REPO" --base main --json > accepted-brief.json
jq '.receipts[] | {source_id,user_id,revision,accepted,reason,brief_digest,binding}' accepted-brief.json
openmerge change show "$CHANGE" --repo "$REPO" --base main --json > after-show.json
openmerge change decisions "$CHANGE" --repo "$REPO" --base main --limit 20 --json > after-decisions.json
jq '.decisions[] | {event_id,eligibility_event_id,settlement_event_id,attempt_id,revision,outcome,brief_digest,intent_digest,policy_digest,sources,requirements,tested_sha,intended_landing_sha,landed_sha}' after-decisions.json
# API reads use only the sandbox status token; never paste it in evidence.
API_TOKEN=$(cat "$OPENMERGE_API_TOKEN_FILE")
curl --fail-with-body -H "Authorization: Bearer $API_TOKEN" "$OPENMERGE_API_URL/v1/queues/$REPO/main/changes/$CHANGE/brief?audience=reviewer" > api-brief.json
curl --fail-with-body -H "Authorization: Bearer $API_TOKEN" "$OPENMERGE_API_URL/v1/queues/$REPO/main/changes/$CHANGE/decisions?limit=20" > api-decisions.json
unset API_TOKEN
```

Require `accepted: true`, the posted comment's `source_id`, revision and digest
in the receipt history, and the same source/receipt/brief binding in the landed
decision. Verify eligibility and settlement event IDs are nonzero and the
outcome is `landed`, with the confirmed landed SHA matching the forge base
history. Keep tested and landed SHAs separately. Save another decision read
and verify immutable decision fields remain identical; only settlement changes
an outstanding attempt's outcome. If not landed, retain the explicit terminal
queue/PR outcome and reason; a temporary approval wait is not success.

## Evidence record — completed 2026-10-19

The operator supplied the setup facts below. The retained case files directly
verify installation `173082682`, policy digest, identities, classifications,
approval sources and landed decisions. They do **not** contain separate setup
command transcripts, App grant snapshots, organization/team numeric IDs, or a
complete command/exit-status ledger; those details must not be inferred from
successful JSON. The documentation pass read only `evidence/case{1,2,3,4,5}/`
and public GitHub data; it did not query a running status API or database.

| Resource | Executed setup / provenance |
| --- | --- |
| Operator/date | Maintainer's operator, 2026-10-19; each retained `RESULT.md` reports PASS |
| Organization/repository | `Smart-Data-Ohio`, GitHub Free; public [openmerge-sandbox](https://github.com/Smart-Data-Ohio/openmerge-sandbox) so rulesets are available; seed `6b60c45` (operator report) |
| Configuration | Commit `aed8f3ff038c04e336612169dc7498dd73c1ef8f`; YAML above and policy-file plus `/owned/` CODEOWNERS entries verified via public GitHub contents; no catch-all owner |
| App/installation | `openmerge-sandbox-smartdata`, App ID `5383059`, installation `173082682`, selected sandbox repository only; operator reports requested and accepted `members: read`, and `setup permissions` reported no missing grants. Exact setup command exit status and full grant list not retained |
| Ruleset/team | Operator reports `openmerge-fence` applied by `setup rulesets --apply`; `owners` contains only `chrisstamand`. Decision owner pool independently records `github.com:user:9450995`; organization/team numeric IDs not retained |
| Instance/binaries | Operator reports separate OpenMerge process, own Postgres cluster, 15s polling, loopback status API; binaries `39e6d73` → `8197a2e` → `521c280` as landing fixes were installed |
| Roles | Agent/PR author `RielStAmandSD` (`github.com:user:82830362`); independent count reviewer `CanadaApollo6` (`github.com:user:54451475`, outside owners); owner `chrisstamand` (`github.com:user:9450995`) |
| Effective policy | All five landed decisions record `b8d4fd81384a0ff905591a1761820c1eb575e24b6bc1ee50b615ba35fc056149` |

### Before/after CLI read-backs

Filenames below are relative to
`~/.local/share/openmerge-sandbox/evidence/`. All five `after-show.json` files
report `merged` and a `landed` landing with matching batch/attempt/event/SHA.
All five `after-decisions.json` files record `outcome: landed` and
`next_cursor: 0`. A temporary wait was not treated as success.

| Case / public PR | Before / held read-back | After / decision sources | Result |
| --- | --- | --- | --- |
| case1 low-risk-none, [#1](https://github.com/Smart-Data-Ohio/openmerge-sandbox/pull/1), `docs/example.md` | `before-show.json` and `before-brief.json`: not queued, `approvals_required: true` but `obligations: []`, no stances. Initial brief `4c0e9b8fab64106088a2b6ac27095dc676b510596a0ae8fed529c10a53780daa` | `after-brief.json`: merged, no obligations or stances; decision 173 has zero requirements and zero sources. Landed `3c75242` | PASS; policy + CI alone |
| case2 exact-head-human, [#2](https://github.com/Smart-Data-Ohio/openmerge-sandbox/pull/2), `src/example.txt` | Old-head [review 5577216542](https://github.com/Smart-Data-Ohio/openmerge-sandbox/pull/2#pullrequestreview-5577216542) on `9b614f1a2c673af1095624c57d3a9a8dca2aa0ef` excluded after rev2. `before-brief.json`: count 1, have 0, no stances. `waiting.json`: queued, `approval_wait`; RESULT records `approval-wait` after CI | Current-head [review 5577262751](https://github.com/Smart-Data-Ohio/openmerge-sandbox/pull/2#pullrequestreview-5577262751) is the sole `native_review` source in decision 262. Landed `e2dbb07` | PASS; exact head enforced |
| case3 receipt-intent, [#3](https://github.com/Smart-Data-Ohio/openmerge-sandbox/pull/3), `receipt/example.txt` | `before-brief.json` / `receipt-brief.json`: queued, `approval_wait`, receipt true; native [review 5577338501](https://github.com/Smart-Data-Ohio/openmerge-sandbox/pull/3#pullrequestreview-5577338501) alone held. Brief misleadingly says `unknown_eligibility` | [Comment 6276735317](https://github.com/Smart-Data-Ohio/openmerge-sandbox/pull/3#issuecomment-6276735317): `@openmerge approve 8905f6cf651cf1796553e7e6f68ad57d7a3ab3dd6b75895a4b0a94cc85e05a8e`. `accepted-brief.json`: accepted true, revision 1, binding current; decision 341 sole source `comment_command` with receipt ID equal to comment ID and matching brief/intent digests. Landed `878c04d` | PASS; native approval alone insufficient |
| case5 uncovered-non-policy, [#4](https://github.com/Smart-Data-Ohio/openmerge-sandbox/pull/4), `uncovered/example.txt` | `before-brief.json`: testing, human count 1, have 0; no owner obligation | Decision 398: only human count 1, sole source [native review 5577465217](https://github.com/Smart-Data-Ohio/openmerge-sandbox/pull/4#pullrequestreview-5577465217). Landed `575314b` | PASS; uncovered still needs count |
| case4 owned-non-policy, [#5](https://github.com/Smart-Data-Ohio/openmerge-sandbox/pull/5), `owned/example.txt` | Non-owner [review 5577526295](https://github.com/Smart-Data-Ohio/openmerge-sandbox/pull/5#pullrequestreview-5577526295) recorded; `owner-pending-brief.json`: queued, unmet `code_owners` on `owned/example.txt`; RESULT records `approval-wait` after CI | Owner [review 5577932124](https://github.com/Smart-Data-Ohio/openmerge-sandbox/pull/5#pullrequestreview-5577932124) then permits decision 497: human count 1 plus `code_owners` for `owned/example.txt` new side. Both native reviews retained; only owner relied on for owner requirement. Landed `b4e0468` | PASS; count and ownership independent |

Public GitHub read-only verification confirmed review authors and commit IDs,
the receipt comment author/body, and that each landed SHA is in sandbox `main`
history (last SHA identical to `main`). GitHub's PR `merged` field was false for
all five at documentation time; the landing claim is the recorded OpenMerge
settlement plus confirmed base history, not GitHub's PR merge flag or synthetic
`merge_commit_sha` field.

### Immutable decision identifiers and digests

The following values are copied from each `after-decisions.json`. Tested,
intended landing and confirmed landed SHA are separate fields and happen to be
equal in every case. PR head is separately retained below.

#### case1 — decision 173

| Field | Recorded value |
| --- | --- |
| Change ID | chg1_8fb08e5d60eabb55faf1ad95a318098290ea65854f7040693e9a4f3206bbf8cc |
| Revision / PR head | 1 / `6e4ae880d27d6cfeb9301049ebc88ee80f475521` |
| Batch / attempt | b1 / `0835a5ad6430b81537be91e38d32c482` |
| Eligibility event / read | 172 / `1792440970626548004` |
| Decision / settlement event | 173 / 177 |
| Brief digest | 9b4a319037452ae07a856086d6cd5c6e29a91ee2adb0bd41a32a0adb752c49d9 |
| Intent digest | 893d506193c3f2db73ffc603d0c824cc8cb4271d16ead3fe3cde42c2ff020949 |
| Tested SHA | 3c75242be3776e20b8ec1575fc367884f8a6748d |
| Intended landing SHA | 3c75242be3776e20b8ec1575fc367884f8a6748d |
| Confirmed landed SHA | 3c75242be3776e20b8ec1575fc367884f8a6748d |

#### case2 — decision 262

| Field | Recorded value |
| --- | --- |
| Change ID | chg1_7edbe68805193ef23146993023e4778c2062fa84758016c06bfe4b4c5bebb73d |
| Revision / PR head | 2 / `cfcf72784b03b32a8f1b8d703d34d5bf612c1ebe` |
| Batch / attempt | b3 / `d60d337fbae7fda92b72be0a67555781` |
| Eligibility event / read | 261 / `1792441625812821339` |
| Decision / settlement event | 262 / 266 |
| Brief digest | ee1b7c425c2c1c0edf620a385f8ee80132944c8b095278cbddc0d1f9c1f3f33e |
| Intent digest | e7411d663471e51291889e9eb72d4b8dfbb9ba0a042968adc237f77179692a65 |
| Tested SHA | e2dbb07d55c7fec7a1a0f8af91de8d606c134aac |
| Intended landing SHA | e2dbb07d55c7fec7a1a0f8af91de8d606c134aac |
| Confirmed landed SHA | e2dbb07d55c7fec7a1a0f8af91de8d606c134aac |

#### case3 — decision 341

| Field | Recorded value |
| --- | --- |
| Change ID | chg1_5af629b39ace1e1f59a3b27bb78a8e422a10c14bf9826101d4c760fc828e44e1 |
| Revision / PR head | 1 / `76b1d0e25c6e00668de6ac8db7f076b0202f63d4` |
| Batch / attempt | b5 / `555135ba32f8faa94d907003aa40ee39` |
| Eligibility event / read | 340 / `1792442230773006576` |
| Decision / settlement event | 341 / 345 |
| Brief digest | 8905f6cf651cf1796553e7e6f68ad57d7a3ab3dd6b75895a4b0a94cc85e05a8e |
| Intent digest | 282f7ed8a1726c197fb9d26513f66d2224bff131959f0c1110f7312273ccbd23 |
| Tested SHA | 878c04d677d6e8d85ad96602bafa12629fca6e65 |
| Intended landing SHA | 878c04d677d6e8d85ad96602bafa12629fca6e65 |
| Confirmed landed SHA | 878c04d677d6e8d85ad96602bafa12629fca6e65 |

#### case5 — decision 398

| Field | Recorded value |
| --- | --- |
| Change ID | chg1_7b9f2c2d84d71c1d37fe029e6a44baf6b12a2a883545ebc49ffeffa3c2b4d221 |
| Revision / PR head | 1 / `a06b13821e012691fee5ded7aef98667f5565f5a` |
| Batch / attempt | b6 / `852fd75cbeb8e7330153bf87ede1208f` |
| Eligibility event / read | 397 / `1792442611159065953` |
| Decision / settlement event | 398 / 402 |
| Brief digest | 6ca0e88849c98dbe7cda3d71493a16b6bc5ca5c9aff198411962650a5de1f89f |
| Intent digest | 72053aef5f6d9c6c7b9501209535cdf2747a6b234892586558c4e8bd2f3e53a4 |
| Tested SHA | 575314b8f00f9a33747029a4d4482351d26cf9a8 |
| Intended landing SHA | 575314b8f00f9a33747029a4d4482351d26cf9a8 |
| Confirmed landed SHA | 575314b8f00f9a33747029a4d4482351d26cf9a8 |

#### case4 — decision 497

| Field | Recorded value |
| --- | --- |
| Change ID | chg1_112832b97233cbef5c3383bba94986372e4119d36956b1002320b7bac301000c |
| Revision / PR head | 1 / `9a6cd0371a3aea15a00baf7c6a702f80734ae3ba` |
| Batch / attempt | b8 / `16371f80ee1e83b054beaaafef32d3c8` |
| Eligibility event / read | 496 / `1792445213987570935` |
| Decision / settlement event | 497 / 501 |
| Brief digest | b8887d1e849e869252db397861ed1cc0d256783d116ad1250c505695ada1bab3 |
| Intent digest | 84c5f2664f03f94b65b9466927a16cc0a6d0fdd2610fd6fb6f6b3d82c1d50a0b |
| Tested SHA | b4e0468c1838b4f944677dddcaa762516b992018 |
| Intended landing SHA | b4e0468c1838b4f944677dddcaa762516b992018 |
| Confirmed landed SHA | b4e0468c1838b4f944677dddcaa762516b992018 |

### Retained artifact manifest and limits

SHA-256 hashes identify the read-only source artifacts without copying full
JSON, signed third-party URLs, or credentials into the repository. Successful
JSON files establish read-back content, not every shell exit status. The source
files contain no complete exit-status log, separate raw `api-*.json` snapshots,
or second `after-decisions` capture for an immutable-field comparison. Those
runbook details are **not independently evidenced** here. Case 2's RESULT records
two client timeouts; exact numeric statuses were not retained. Case 1's
`alias.err` is empty. Operator-reported completion and the five landed outcomes
are recorded without inventing missing command transcripts or sign-offs.

| Case | File | SHA-256 |
| --- | --- | --- |
| case1 | `RESULT.md` | `a96cb7a575986f5740df0346de0436b673a6247db316b26a7f853da83e879f26` |
| case1 | `after-brief.json` | `dfa825afd637ee5e3c932139e9528115eedd509eb6d9c06482a5d4e928c0ed3c` |
| case1 | `after-decisions.json` | `c6ebc1774298708c28dfd46964b4ee959e032a8cc8876e4884c083d4cb1a9be9` |
| case1 | `after-show.json` | `4aa991bb9baa12fdc8b1680cf1e345be31a6700f5bc0a3c3976ae1153f59e012` |
| case1 | `alias.err` | `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` |
| case1 | `alias.json` | `31ce10d41ee11216d6dd84ce9baa670900496e2d6ca4f6392eeacfdccdb0b20c` |
| case1 | `before-brief.json` | `df943143075fcb326e79ccc6535970f4bb6647fd7803df018d1c04443353ad92` |
| case1 | `before-show.json` | `b6afeea0a3e3605bfb53fbd79f4886edeb214ed8027164256f3f54afbc49d72e` |
| case2 | `RESULT.md` | `97d59f39c42293ac8c56595aea1b105f0e57f124913d9eaf1ad2b148821ec15e` |
| case2 | `after-brief.json` | `131ecd3e9b8cfb37e5a6ad0890aa00ca617f76de7977657bb6b62424954d9312` |
| case2 | `after-decisions.json` | `997db897efc0301a609ecef13b22386a7ea68903c73c8d00b83e550c5b2a1ff2` |
| case2 | `after-show.json` | `3dc6747028e6faceac6d79aae9100339ec999b7c59569f58a730feb9969a5808` |
| case2 | `alias.json` | `35f51f87d0c50aaad7e2061ec6303a758da26ef19750c4bfde42f00d6410be7d` |
| case2 | `before-brief.json` | `d513b75cd14fb2b54b1aefc8b44326128161b79ac6790d5321247c2451bca3cc` |
| case2 | `waiting.json` | `e57897d1e0dd460d6e24b44af6fff3e2c486e85c4e3e0da922ba24fa2f8a39aa` |
| case3 | `RESULT.md` | `8c3cf837e8504703405f8ef3f25f3d69de2f8c8beb1ec390b39b854b82091b1a` |
| case3 | `accepted-brief.json` | `a4f729cc461f0ca2b8dd3a84faa4af2c820d53eb9bcdbcf10e96333cdf3f4c79` |
| case3 | `after-decisions.json` | `57cb608d103c1df815e6c2ad6ab867258d67c47535e78864805560b3263cb2a9` |
| case3 | `after-show.json` | `250d84947df2a6485166894fd7d77db58c591ba9d837daa1e05ac333103c22c4` |
| case3 | `alias.json` | `11b426e637b16ecc21d68aa427d93833a11e6efd0e13f80b59e55a1bb5dce651` |
| case3 | `before-brief.json` | `84e0c0528c7aa090fea7655ac18f14f371266df367284fe6ffa99ff082502533` |
| case3 | `receipt-brief.json` | `84e0c0528c7aa090fea7655ac18f14f371266df367284fe6ffa99ff082502533` |
| case5 | `RESULT.md` | `360f015766fb3d875a0f349f7e169d83453223f951775561c3a96e92fd31fe11` |
| case5 | `after-decisions.json` | `f909cc7ffe7bb55d3f5f0219a503e8eee9b97750f2a0aadceaf1b4341fbc72a2` |
| case5 | `after-show.json` | `692093a862ed94c403131fc6deec80294337a06d51dd177e2560223334d8e394` |
| case5 | `alias.json` | `ede9af755c0f6d37ce9e64d34a3d5e8ef7c78ba846eeb85138a8cfce5c1eff64` |
| case5 | `before-brief.json` | `2b9b833613bbcbe8616742ee2c9c0350e348518cc3db15831a0a57944f6250ad` |
| case4 | `RESULT.md` | `12d1ab1c22d0c7ef7e8041cc2bc1800d4c16565bd0283e3f6a0b9a470878f4c4` |
| case4 | `after-decisions.json` | `92b2dd679ca5bebff64a8de7f4f4487f3999079225c943c4254a4c6670c4d987` |
| case4 | `after-show.json` | `e83eff18473de8857596833ff19fea2f49fe5c2b0146a97dda5df6a677df23ec` |
| case4 | `alias.json` | `c310ca646a4a7d973f487765a0f9ff9720fe16f962a04f0313619a00968f1ee9` |
| case4 | `owner-pending-brief.json` | `709cd52ee8c8b369e6952f84c838992fbb5241a6618bdbb4eaeda402c2974348` |

### Findings and known limitations

- Finding #1: history limit mismatch blocked every approval landing on real git;
  fixed by [#82](https://github.com/CanadaApollo6/openmerge/pull/82) (`8197a2e`).
- Finding #2: tested commit was not acquired before cache-only reads, also
  blocking every approval landing on real git; fixed by
  [#84](https://github.com/CanadaApollo6/openmerge/pull/84) (`521c280`). Both were
  invisible to the fake-forge suite. The broader fake/real parity class remains
  tracked in [#83](https://github.com/CanadaApollo6/openmerge/issues/83).
- Observation #3: two status API client timeouts during case 2 testing/landing,
  with no server error logged per RESULT; open
  [#85](https://github.com/CanadaApollo6/openmerge/issues/85).
- Observation #4: approval waits can show `unknown_eligibility`, including case
  3's receipt wait, rather than the actionable missing approval/receipt. This
  also appears in final briefs; open
  [#86](https://github.com/CanadaApollo6/openmerge/issues/86). Case 1 and case 3
  `after-show.json` include `policy_busy` and missing digest/binding fields;
  separate successful brief/decision reads supply the recorded values above.
- An external GitHub Actions incident delayed case 1, in addition to the two
  landing blockers (operator report).

The organization-sandbox criterion is evidenced by this completed run. This does
not add conflict grace, depth-1 cancellation, or automatic old-worker detection,
and does not convert fake-forge results into a real-forge parity guarantee.
