# Security policy

nanoflux is a self-hosted reader intended for a private network. The threat
model and the deployment assumptions are in the [README](README.md#security-and-threat-model).

## Reporting a vulnerability

Please report security issues privately through GitHub's security advisory
feature:

https://github.com/metruzanca/nanoflux/security/advisories/new

Do not open a public issue for a vulnerability. Include the version or commit,
a description, and a minimal reproduction. I will acknowledge it as soon as I
can and coordinate a fix and disclosure.

Non-sensitive hardening suggestions and ordinary bugs can go in the issue
tracker.

## Supported versions

The project is pre-1.0. Fixes land on `main`; there are no long-lived release
branches yet. If you self-host a tagged release, upgrade to the newest tag.

## Scope notes

- **Plugins are trusted executables.** A plugin runs with the server's
  privileges. A malicious or compromised plugin is not a vulnerability in
  nanoflux itself; audit plugins before installing them.
- **The expected deployment is private.** A reverse proxy on a LAN, NAS, or
  Tailscale network is the target. Exposing the app directly to the public
  internet broadens the attack surface beyond what the project is designed for.
- **Set `NF_TRUST_PROXY=1` only behind a proxy you control.** When it is off,
  forwarded headers are ignored on purpose.
