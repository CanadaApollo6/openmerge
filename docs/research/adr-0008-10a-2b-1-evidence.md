# ADR 0008 PR 10a-2b-1 evidence

Classification: **Inert by its own behaviour; depends on Hold code; deploy only
after period B**. Base: main `4b1fe46`. Production changes stay in
`internal/audit/evidence.go`. Complete-export fixtures and v1 remain
**PROVISIONAL**, with no regeneration or freeze in this step.

## Fold agreement and fail-closed history

Audit streams only `config_applied` and `policy_authority_applied` events before
the decision, in event-ID order within the existing repeatable-read snapshot.
It applies them through `queue.Apply`, keeping one projected state and the
accepted pin's retained event ID. Config transitions preserve the durable pin
as the queue fold does. Payload reads retain the existing 4 MiB cap and errors
are sanitized to `errSource`. No queue, event, wire, formatter, schema, migration,
index, CLI dispatch, Makefile, CI, test-flag or Dockerfile change is included.

The prefix differential compares this projection's `AuthorityPin()` with the
full queue fold after every event, including unrelated events, config changes,
same-digest rebinds, duplicate pins, rollback, Validate-rejected pins and pins
whose effective digest disagrees with the current config. Separate prefixes
start with an ignored pin before config and a clean config-first history.
Every rejected/ignored pin poisons the prefix, even after a valid replacement;
export fails closed rather than silently removing evidence requiredness.
Database-prefix assertions exercise the actual streaming query too. Invalid
payloads may fail at decode before reaching the fold, also closed.

This query scans the config/pin subset for each decision; it does not scan all
check observations or retain the queue journal in memory. Very long config/pin
histories can increase export work. Step 10a-2b-2 retains the realistic growth
measurement and final independent complete-export acceptance work.

## Inapplicable status and normalized attempt ties

A governing github rollback pin retains its source with `inapplicable` status.
`not_recorded` remains the explicit absence representation with no source.
Shape validation restricts inapplicable references to the governing authority
edge. Closure follows the retained source even outside the requested time
range. Replay re-resolves it against saved retained history; fully rehashed
substitutions of recorded/absent status or another real rollback source fail.
Valid rollback replay remains byte-exact. Fully rehashed omissions still need
the future 10b external manifest anchor.

The dead `sameGeneration` helper and its helper-only test are removed.
Generation selection remains enforced by SQL and its existing cross-generation
tests. The new cross-event tie is run 5 attempt 1 (failure), then run 5 attempt 0
(success): SQL normalizes 0 to 1 before ordering and selects the later event.
The reference describes attempt 1. SQL selection and Go normalization within
the chosen event are tested separately; the new mutant removes normalization
from the SQL tuple itself.

## Verification

Verification is pending on the committed implementation. The final evidence
update will record foreground command results, honest mutation outcomes,
owned-burner race stress, regression counts, package timings and artifact hashes.
The revised `scripts/verify-adr0008-10a-2a.py` targets this step despite its
historical filename. Its table drops dead-generation and github-source-erasure
targets. Assertion kills, declared equivalents, survivors, build/setup failures
and infrastructure failures are distinct outcomes; unexpected survivors or
failures reject verification. A known defensive closure-call equivalent is
run and reported separately, never counted as an assertion kill.
