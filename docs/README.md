# Zela guides

[Project home](../README.md) · [Apache License 2.0](../LICENSE)

Zela is an open-source project licensed under the Apache License 2.0.

Start here if you are new to managing servers. You can use the browser for everyday work; the setup guides explain the terminal steps needed to get there.

**Installing on a fresh Linux server?** The [cluster installer](installer.md) sets up
the manager, PostgreSQL, and background services for you.

## Follow this path the first time

| Order | Guide                                        | Use it to                                                          |
| ----- | -------------------------------------------- | ------------------------------------------------------------------ |
| **1** | [Getting started](getting-started.md)        | Try Demo mode or start a live manager                              |
| **2** | [Browser management guide](console-guide.md) | Connect a worker and deploy your first application                 |
| **3** | [Networking](networking.md)                  | Open your app, connect workers, and understand disabled controls   |
| **4** | [Operations](operations.md)                  | Make backups, maintain servers, and find help when something fails |

**A word is unfamiliar?** Keep the [glossary](glossary.md) open alongside the guide.

## Find help by task

| I want to…                             | Start here                                                                      |
| -------------------------------------- | ------------------------------------------------------------------------------- |
| Sign in or understand Demo versus Live | [Getting started](getting-started.md)                                           |
| Add a server                           | [Connect your first worker](console-guide.md#connect-your-first-worker)         |
| Deploy a website and open it           | [Deploy your first application](console-guide.md#deploy-your-first-application) |
| Scale, restart, update, or stop an app | [Everyday management](console-guide.md#everyday-management)                     |
| Understand why a button is unavailable | [Disabled controls](console-guide.md#why-is-a-button-disabled)                  |
| Change cluster or worker networking    | [Networking in the dashboard](networking.md#manage-networking-in-the-dashboard) |
| Attach shared files to a deployment    | [Shared application storage](shared-storage.md)                                  |
| Connect an AI assistant                | [AI cluster management](mcp.md)                                                 |
| Protect access and credentials         | [Security](security.md)                                                         |
| Back up or upgrade Zela                | [Operations](operations.md)                                                     |
| Investigate a problem                  | [Troubleshooting](operations.md#troubleshooting)                                |

## Guides for the person installing Zela

These pages include commands and server configuration. Read the beginner guides first if the terms are unfamiliar.

| Guide                                               | Covers                                                                           |
| --------------------------------------------------- | -------------------------------------------------------------------------------- |
| [Installation](installation.md)                     | Linux services, containers, database setup, worker connections, and verification |
| [Configuration reference](configuration.md)         | Settings, startup options, file locations, ports, and defaults                   |
| [Architecture](architecture.md)                     | How the manager, workers, database, and clients connect                          |
| [High availability](../deploy/HA.md)                | Extra managers that can take over if the active manager fails                    |
| [CLI guide](cli.md)                                 | Managing Zela from a terminal                                                    |
| [Administrator & API reference](admin-reference.md) | Detailed storage, provisioning, Docker gateway, and API behavior                 |
| [Deployment files](../deploy/README.md)             | Which service and load-balancer examples to use                                  |
| [Contributing](../CONTRIBUTING.md)                  | Building and testing changes to the project                                      |

## Shared storage development

[Shared application storage](shared-storage.md) explains how to register NFS storage, attach it to an application, and understand move behavior. The [implementation plan](shared-storage-plan.md) tracks the remaining hardening and live-validation work.

## Supported platforms

| Part                           | Supported setup                                                                                                                                                  |
| ------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Manager                        | Linux or macOS on amd64 or arm64; Go 1.25+ to build from source. Live server installations usually use Linux or the supplied container image                     |
| Automatically installed worker | A Linux server using systemd: RHEL/Rocky/AlmaLinux 8–10, CentOS Stream 9–10, Ubuntu LTS 22.04/24.04/26.04, or Debian 12/13; amd64 or arm64                       |
| Manually installed worker      | A Linux host that runs Docker Engine and the `zela-worker` program                                                                                               |
| Database                       | PostgreSQL using a direct connection or session pooling; the supplied Compose setup uses PostgreSQL 18. Optional for one manager, required for multiple managers |

The encrypted cluster network has extra requirements, including WireGuard support and Docker Engine 28 or newer. Check [network requirements](networking.md#requirements) before enabling it.

## How to read commands in these guides

- A **terminal** is the app where you type commands. A **project folder** is the downloaded or cloned Zela folder containing `README.md` and `compose.yaml`.
- Run project commands in that folder unless a step says **on the worker** or **on the manager**.
- Copy one command block at a time. Lines starting with `#` explain the commands.
- Replace example names such as `worker.example.com`, `/secure/admin-token`, and `<worker-id>` with your own values. Do not type angle brackets around the replacement.
- `sudo` asks to run a command as the computer's administrator. It may ask for your computer password.
- A command that keeps running may be a server or live log viewer. Leave it open until the guide says to stop it.
- Never paste passwords, tokens, or private keys into a public issue or support conversation.
