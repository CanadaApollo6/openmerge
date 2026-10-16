# 0003. Use a native `.openmerge.yml` with a condition grammar, plus a Mergify importer

- **Status:** Accepted; [ADR 0006](0006-multi-repository-serving.md) makes the default branch's file govern every queue in a repository
- **Date:** 2026-10-15

## Context

The configuration file is OpenMerge's most important public interface. Teams commit it to every repository, and it must stay stable for years.

Mergify's format is the reference most users will arrive with, and its research notes ([merge queue](../research/mergify-merge-queue.md), [platform](../research/mergify-platform.md)) show both what works and what hurts:

- **Works:** conditions as short strings (`check-success = ci`) combined with `and`/`or`/`not`; separate conditions for entering the queue and for merging, which enables two-step CI; YAML anchors for reuse.
- **Hurts:** frequent breaking renames (`conditions` → `merge_conditions`, `routing_conditions` → `queue_conditions`, `speculative_checks` → `max_parallel_checks`); Jinja templating removed on 2026-10-31; a dashboard editor that cannot save anchors; a single-level `extends`.

Reading `.mergify.yml` natively would make OpenMerge a drop-in replacement, but it would tie our public interface to a schema that a closed product changes on its own schedule.

## Decision

**File.** OpenMerge reads exactly one of `.openmerge.yml`, `.openmerge.yaml`, `.github/openmerge.yml`, or `.github/openmerge.yaml`. If more than one exists, OpenMerge refuses to act on the repository and reports which files it found.

**Source of truth.** Configuration is always read from the base branch, never from a pull request's head. A PR cannot change the rules it is judged by. A PR that edits the config gets a validation check showing the parsed result and any errors, and the change takes effect once merged.

**Server loading.** The polling server pins all configuration reads to the observed base tip and pauses on missing, invalid, or currently unobservable attributes, reloading when the base moves; it never falls back to a merge-capable configuration.

**Versioned schema.** The file starts with `version: 1`. Within version 1, changes are additive only: new keys and new condition attributes, never renames or removals. A breaking change requires `version: 2` and ships with `openmerge config migrate`, which rewrites the file mechanically. We publish a JSON Schema for each version so editors validate as you type.

**Shape (version 1, Phase 1 surface).**

```yaml
version: 1

queues:
  - name: default
    enter_when:                    # must hold to join and to stay in the queue
      - "#approved-reviews-by >= 1"
      - check-success = lint
    merge_when:                    # checked against the tested batch before landing
      - check-success = integration
    merge_method: squash           # merge | squash | rebase
    batch:
      max_size: 8
      max_wait: 30s
```

Later phases add top-level keys (`priorities`, `rules`, `scopes`, `commands`) as additive changes, each specified in the pull request that implements it.

**Condition grammar.** A condition is a string `[-][#]attribute [operator value]`:

- `-` negates; `#` takes the length of a list attribute; an attribute with no operator is a boolean test (`draft`).
- Operators: `=`, `!=`, `~=` (RE2 regular expression), `glob`, `<`, `<=`, `>`, `>=`.
- On list attributes, an operator holds if any element matches, except `!=`, which holds if no element matches.
- A YAML list of conditions means all must hold. `and:`, `or:`, and `not:` nest.
- An unknown attribute or operator is a validation error; close misspellings name the closest valid one.

Missing or mistyped attribute data makes a condition unknown, and unknown never passes, including under `not`.

Phase 1 requires each queue's `merge_when` conjunction (flattening nested
`and` groups) to contain a positive `check-success = <non-empty name>` term;
checks under `or`/`not` and counts do not qualify. Extra terms may still use
`or`/`not`. Loading also rejects conditions that pass with missing or failed
checks, naming the queue and an example fix such as
`merge_when: [check-success = ci]`. There is no opt-out. CI is evaluated on the
tested SHA. Missing and pending check results remain unknown within logical
groups; an OR fails only when all alternatives are definitely false.

Attributes cover people and reviews, branch and PR metadata, files and line counts, CI results, queue state, and time windows. Each attribute is documented in a generated reference with its type and an example. The concrete Phase 1 list is part of the Phase 1 implementation.

**Templating.** Only `{{ name }}` substitution of documented variables. No expression language.

**Reuse.** YAML anchors and aliases work everywhere. Cross-repository `extends` is deferred to Phase 5 and will need its own design.

**Mergify importer.** `openmerge config import [path]` reads a `.mergify.yml`, writes an equivalent `.openmerge.yml`, and prints a compatibility report: every construct it translated, approximated, or could not express. It maps known concepts directly, for example `queue_conditions` to `enter_when`, `merge_conditions` to `merge_when`, and `batch_size` to `batch.max_size`. The importer is a migration tool; OpenMerge never reads `.mergify.yml` at runtime.

## Consequences

- We control our schema and can promise stability, which is a selling point against churn.
- Migrating from Mergify is one command plus a reviewed diff, not a rewrite.
- The importer must track Mergify's format. Its test fixtures pin the Mergify versions we support, and gaps are reported rather than guessed.
- Reading config from the base branch blocks a class of privilege escalation, at the cost that config changes need a merge before they apply. That cost is intentional.
- Condition strings stay familiar to Mergify users. We write our own documentation for the grammar; we describe compatibility, we do not copy Mergify's docs.

## Alternatives considered

- **Read `.mergify.yml` natively.** The fastest adoption path, but it couples our public interface to a closed product's renames and removals, and it invites confusion about which tool owns a repository.
- **Own format only, no importer.** Simplest, but it throws away the easiest migration path for the users most likely to switch.
- **A general-purpose policy language** (CEL, Rego, or a custom DSL as in Reviewpad). More expressive, but harder to read in review, harder for agents to edit safely, and unnecessary for the conditions real configs use.
