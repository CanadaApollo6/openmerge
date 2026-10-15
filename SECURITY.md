# Security policy

OpenMerge decides what lands on protected branches, so a security bug here can put unreviewed code into production. We take reports seriously.

## Reporting a vulnerability

Do not open a public issue. Report privately through GitHub's [private vulnerability reporting](https://github.com/CanadaApollo6/openmerge/security/advisories/new) for this repository.

Please include the affected version or commit, how to reproduce the problem, and its impact. We will acknowledge your report within five business days and keep you updated until it is resolved. We credit reporters in the advisory unless you ask us not to.

## Supported versions

OpenMerge has no releases yet. Until 1.0, security fixes land on `main` and in the latest release.

## Scope

Of particular interest:

- landing a commit that was not the one tested, or landing without the configured conditions holding;
- a pull request influencing the configuration or permissions it is judged by;
- untrusted pull request code running in a test ref with the base repository's privileges before a maintainer approved it;
- webhook signature bypass, or forging commands from users without permission;
- leaking forge credentials, installation tokens, or repository contents.
