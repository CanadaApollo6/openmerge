# Stage A organization sandbox acceptance — PENDING

**PENDING maintainer-provided sandbox resources. Nothing in this runbook has
been executed.** No organization, repository, App, installation, review, comment,
or permission change was created for PR 12b. These commands are for a maintainer
using an isolated organization sandbox after supplying its resources. Never run
against the dogfood repository or live OpenMerge database/status endpoint.
CI uses a fake forge and needs no GitHub credentials.

## Resources and setup

1. Supply a sandbox organization/repository, separate sandbox OpenMerge App,
   installation, database, cache, webhook/API endpoints and service process.
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
3. Supply separate agent/bot, human reviewer and owner-team identities. Add the
   owner to `SANDBOX_ORG/owners`, verify team visibility with the installation,
   and give eligible humans repository write permission. The PR author,
   submitter and accountable humans cannot satisfy their own approvals. Use a
   reviewer outside those exclusions; use a different non-owner count reviewer
   for the owned/uncovered comparison. Keep credentials outside the artifact.
4. Use a read-only sandbox status token for CLI/API reads and the selected
   human's forge credentials for review/comment commands. A token authenticates
   an account, not proof that a person acted. Use separate bot/App identities.

```sh
# Fill these with maintainer-provided sandbox values; no production defaults.
export REPO='SANDBOX_ORG/SANDBOX_REPO'
export OPENMERGE_API_URL='https://SANDBOX_STATUS_ORIGIN'
export OPENMERGE_API_TOKEN_FILE='/secure/sandbox-status-token'
openmerge setup permissions --app-id APP_ID --installation-id INSTALLATION_ID --private-key /secure/sandbox-app.pem
```

Commit this configuration to the sandbox default branch, replacing team and CI
names with verified values. Preserve policy-file ownership before activation.
The rules are additive: `paths` retains the human count for uncovered files;
`touches` adds ownership only where CODEOWNERS coverage exists.

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
        - when: {paths: ['**']}
          require: {human: true, count: 1}
        - when: {touches: ['**']}
          require: {code_owners: true}
```

```text
# .github/CODEOWNERS — deliberately no catch-all owner
/.openmerge.yml @SANDBOX_ORG/owners
/.openmerge.yaml @SANDBOX_ORG/owners
/openmerge.yml @SANDBOX_ORG/owners
/openmerge.yaml @SANDBOX_ORG/owners
/.github/CODEOWNERS @SANDBOX_ORG/owners
/CODEOWNERS @SANDBOX_ORG/owners
/docs/CODEOWNERS @SANDBOX_ORG/owners
/owned/ @SANDBOX_ORG/owners
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

Derive the canonical change ID from the PR alias lookup once (the CLI has no
submit/write command); then follow the journey using only change reads and
forge-authenticated commands. Escape the base as one API path component if it
contains `/`.

```sh
openmerge queue status --repo "$REPO" --base main --pr <number> --json > alias.json
CHANGE=$(jq -r '.pr.change.id' alias.json)
openmerge change show "$CHANGE" --repo "$REPO" --base main --json > before-show.json
openmerge change brief "$CHANGE" --repo "$REPO" --base main --audience reviewer --json > before-brief.json
openmerge change decisions "$CHANGE" --repo "$REPO" --base main --json > before-decisions.json
```

Record these cases in separate rows. Change policy only through a reviewed
sandbox configuration change between cases; wait for the new effective digest.

| Case | Configuration/path | Expected observation before landing |
| --- | --- | --- |
| Low-risk none | Use `default: {human: true, count: 1}` and only a `when: {labels: [docs]}` / `require: none` rule; change `docs/example.md` | CI and policy alone permit landing; no human approval required |
| Exact head | `default: {human: true, count: 1}` without the two owner rules; `src/example.txt` | Wrong-head review does not count; exact-head human review permits landing |
| Receipt intent | `default: {human: true, count: 1, receipt: true}` without the owner rules | Native exact-head review alone waits; exact brief receipt is required |
| Owned non-policy | Full two-rule config above; `owned/example.txt` | Count review by non-owner still waits for an eligible team owner |
| Uncovered non-policy | Same full config; `uncovered/example.txt` | No owner obligation; human count still applies and must be satisfied |

For native review, select the independent human's forge session and submit an
approval for the exact `head_sha` recorded in the brief/show (GitHub review UI
or `gh pr review <number> --repo "$REPO" --approve`). Record the native review
ID and its actual `commit_id`; do not assume the branch remained unchanged.

For receipt intent review, inspect the brief first, copy its exact digest, then
post this command as the independent human reviewer (substitute the digest):

```sh
gh pr comment <number> --repo "$REPO" --body '@openmerge approve <brief-digest>'
# Equivalent after reviewing before-brief.json:
DIGEST=$(jq -r '.digest' before-brief.json)
gh pr comment <number> --repo "$REPO" --body "@openmerge approve $DIGEST"
```

Run only one of the two equivalent posting commands. Save its comment URL and
numeric ID. Read back the receipt and immutable landing decision, including all
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

## Evidence record — all fields PENDING

| Field | Value / retained artifact |
| --- | --- |
| Date/operator; binary commit; configuration commit | PENDING |
| Repository/PR number/URL; agent account ID | PENDING |
| App ID; installation ID; organization/team IDs | PENDING |
| App requested grants; installation accepted grants; diagnostic command/exit | PENDING |
| Case; path; effective policy digest | PENDING |
| Exact commands and every exit status | PENDING |
| Change ID; revision; PR head SHA | PENDING |
| Native review ID/URL/author ID/commit ID | PENDING |
| Posted receipt comment URL/ID/author ID | PENDING |
| Brief digest; receipt source/ID and accepted/binding state; intent digest | PENDING |
| Decision event ID; eligibility event/read ID; attempt ID; settlement event ID | PENDING |
| Tested SHA; intended landing SHA; confirmed landed SHA | PENDING |
| CLI/API JSON filenames and SHA-256 hashes; repeated decision comparison | PENDING |
| Actual result; discrepancies; maintainer sign-off | PENDING |

Retain one row set per case, including failures. Completion requires a maintainer
review of the filled artifact. PR 12b does not claim sandbox acceptance.
