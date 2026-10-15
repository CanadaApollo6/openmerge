# Governance

OpenMerge is in its founding phase. This document says who decides what today, and how that changes as the project grows.

## Today: project lead

The project lead, [@CanadaApollo6](https://github.com/CanadaApollo6), makes final decisions on scope, architecture, releases, and who becomes a maintainer. The lead decides in the open: on issues, pull requests, and [ADRs](docs/adr/README.md), with reasons written down.

## Maintainers

Maintainers review and merge pull requests and triage issues. A contributor becomes a maintainer after a sustained record of good reviews and merged work, by nomination from an existing maintainer and agreement of the lead. Maintainers are listed in [MAINTAINERS.md](MAINTAINERS.md).

## How decisions are made

- **Routine changes** (bug fixes, features within an accepted design): one maintainer approval and passing CI.
- **Significant or hard-to-reverse changes** (public interfaces, storage formats, new dependencies on external services, security boundaries): an ADR, open for comment for at least five days, accepted by the lead.
- **Disagreements** are resolved on the issue or PR. If maintainers cannot agree, the lead decides and records why.

## Path to a steering committee

When the project has at least three active maintainers from at least two organizations, governance moves to a steering committee of those maintainers, deciding by lazy consensus with a majority vote as fallback. The lead proposes that change as an ADR.

## Licensing commitments

OpenMerge is licensed under Apache 2.0, and contributions are accepted under the [Developer Certificate of Origin](CONTRIBUTING.md#developer-certificate-of-origin), not a CLA. Contributors keep their copyright. As project policy, OpenMerge itself will stay Apache 2.0 and will not adopt a CLA. Apache 2.0 still lets anyone, including companies, build and distribute differently licensed derivative works; that freedom is intentional.
