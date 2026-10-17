# Security policy

## Reporting a vulnerability

**Please do not open a public issue for security problems.**

Report vulnerabilities privately through GitHub's **Security → Report a vulnerability** form on this repository (private vulnerability reporting). Include:

- the affected version or commit;
- the component (control plane, worker agent, zela, gateway, HA);
- steps to reproduce, and the impact you observed.

We aim to acknowledge reports within a few working days, agree on a fix and disclosure timeline with you, and credit you in the release notes unless you prefer otherwise.

## Supported versions

Security fixes are made on the latest release. Upgrade managers, workers and `zela` together; see [docs/operations.md](docs/operations.md#upgrades).

## Scope

Particularly relevant areas: authentication and token scopes, the Docker gateway's workload isolation, worker-agent access control and HA fencing, SSH host-key pinning, vault encryption, and handling of secrets in logs or API responses.

Hardening guidance for operators is in [docs/security.md](docs/security.md).
