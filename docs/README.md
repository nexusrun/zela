# Documentation

Zela Cluster Control manages a fleet of standalone Docker hosts from one control plane: provision hosts over SSH, deploy and move containers, monitor hosts, and expose a single authenticated Docker API endpoint for the whole cluster.

## Start here

| If you want to… | Read |
| --- | --- |
| Try it in five minutes, with no real hosts | [Getting started](getting-started.md) |
| Understand the components and how they talk | [Architecture](architecture.md) |
| Install for real: binaries, Compose, systemd, PostgreSQL, TLS, workers | [Installation](installation.md) |
| Look up a flag, environment variable, file, port or default | [Configuration reference](configuration.md) |
| Run several managers with automatic failover | [High availability](../deploy/HA.md) |
| Harden a deployment and understand tokens and secrets | [Security](security.md) |
| Get traffic to containers, or connect containers across hosts | [Networking](networking.md) |
| Back up, upgrade, recover from failures, troubleshoot | [Operations](operations.md) |
| Administer the cluster from a terminal | [zela CLI](cli.md) |
| Connect ChatGPT, Claude or a coding assistant (MCP) | [AI cluster management](mcp.md) |
| Call the Docker gateway or REST API | [Project README: API sections](../README.md#docker-api-access) |
| Contribute code | [Contributing](../CONTRIBUTING.md) |

## Components at a glance

| Binary | Runs on | Purpose |
| --- | --- | --- |
| `zela-server` | One or more manager hosts | Web console, REST API, Docker gateway, scheduler, monitoring, SSH provisioning |
| `zela-worker` | Every Docker host | Authenticated agent in front of the local Docker socket; host metrics |
| `zela` | Admin laptops, bastions, CI | Remote CLI over the control plane's HTTPS API |

## Supported platforms

- **Manager:** Linux or macOS, amd64 or arm64. Built with Go 1.25+. Production deployments normally run Linux with systemd or the supplied container image.
- **Workers (automatic SSH provisioning):** systemd hosts on amd64/arm64 running RHEL, Rocky Linux or AlmaLinux 8–10, CentOS Stream 9–10, Ubuntu 22.04/24.04/26.04 LTS, or Debian 12/13.
- **Workers (manual install):** any Linux host that can run Docker Engine and the static `zela-worker` binary.
- **Database:** PostgreSQL (direct connection or session pooling). The supplied Compose files use PostgreSQL 18. Optional for a single manager; required for high availability.
