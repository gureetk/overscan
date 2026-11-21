# Security policy

## Reporting a vulnerability

Report vulnerabilities privately, through GitHub's private vulnerability reporting: the repository's **Security** tab, then **Report a vulnerability**. Please do not open a public issue or pull request for a security problem.

Say what you found, how to reproduce it, which version or commit you tested, and what an attacker could do with it. Redact real credentials, MAC addresses, serial numbers and portal addresses.

If something in the repository looks like proprietary code or data, report it the same way; see [docs/clean-room.md](docs/clean-room.md).

## Supported versions

Fixes go into the next release, from the latest. Upgrading keeps everything that a household set up, from every full release since v0.3.0 ([docs/upgrading.md](docs/upgrading.md)), so upgrading is how to get a fix. Older releases and pre-releases get no fixes of their own.

## Scope

Overscan is meant to run on a home network. Exposing it to the internet is discouraged, and doing so without a reverse proxy with TLS is outside what it is designed for. [docs/security.md](docs/security.md) describes the security model, what protects what, and the known limits.
