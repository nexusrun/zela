# Deployment files

[All guides](../docs/README.md) · [Installation instructions](../docs/installation.md)

This folder contains examples for the person installing Zela as a Linux service or setting up backup managers. The files are templates: addresses, certificates, users, and secrets must match your installation.

**First time?** Use [Getting started](../docs/getting-started.md) for a local dashboard or Compose setup. For production service setup, follow [Installation](../docs/installation.md) in order instead of copying a unit file alone.

## Choose the right example

| File                                       | Where it is used                   | What it does                                                                                         |
| ------------------------------------------ | ---------------------------------- | ---------------------------------------------------------------------------------------------------- |
| [zela-server.service](zela-server.service) | Manager running Linux with systemd | Starts Zela with HTTPS, `/var/lib/zela` state, and worker binaries in `/opt/zela/workers`            |
| [worker.service](worker.service)           | Manually installed Linux worker    | Starts a TLS worker agent. Automatic SSH provisioning installs its own `zela-worker.service` instead |
| [zela-server-ha.conf](zela-server-ha.conf) | Manager service override           | Adds active/standby manager options; requires shared PostgreSQL and credentials                      |
| [haproxy-ha.cfg](haproxy-ha.cfg)           | HAProxy load balancer              | Sends traffic to a manager that passes `/api/ready`                                                  |
| [HA.md](HA.md)                             | Installation guide                 | Explains backup managers, common configuration, and takeover testing                                 |

## Install in this order

1. [Check prerequisites and build the binaries](../docs/installation.md#1-prerequisites).
2. [Prepare storage and the manager service](../docs/installation.md#3-prepare-postgresql), including certificates and a protected environment file.
3. Start the manager, then confirm that secure sign-in works.
4. [Connect workers](../docs/console-guide.md#connect-your-first-worker) and deploy a test application.
5. If needed, follow [High availability](HA.md) after the single-manager setup works.

## Before restarting a service

- Wait for cluster operations to finish and back up configuration before an upgrade.
- Keep the original administrator token and vault key; do not generate new keys as a routine restart step.
- Check that the service user can read its certificate/key and write its data directory.
- On SELinux systems, restore the installed files' contexts as described in the installation guide.

Use [Operations](../docs/operations.md) for backups, upgrades, status checks, and troubleshooting. Use the [configuration reference](../docs/configuration.md) to look up a flag or environment variable.
