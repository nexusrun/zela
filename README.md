# Zela

**Turn your Linux servers into one Docker cluster, managed from a browser or an AI assistant.**

[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

Zela brings your Linux servers together so you can deploy applications, see how they are doing, and manage them in a browser. You can also connect an AI assistant to help operate the cluster.

You provide the servers and a container image for each application. Zela can install Docker on supported servers, start application copies, monitor them, and replace managed application copies when a worker fails and another suitable worker has room.

## Watch the demo

Take a one-minute tour of the dashboard: workers, deployments, containers, monitoring, alerts, and AI connections.

https://github.com/user-attachments/assets/ccbfa231-7571-4c6c-b9f7-aba1100376bf

[Watch or download the MP4](https://github.com/user-attachments/assets/ccbfa231-7571-4c6c-b9f7-aba1100376bf) · [View the dashboard preview](https://github.com/user-attachments/assets/6d1c7629-9c88-4bcc-9ef8-7c7d2ea15494)

The recording uses **Demo mode** with simulated workers and workloads.

## New here? Start with these three guides

| Step                 | Guide                                                                           | What you will achieve                                                                    |
| -------------------- | ------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| **1. Try it**        | [Getting started](docs/getting-started.md)                                      | Open the dashboard and learn the difference between Demo and Live mode                   |
| **2. Deploy an app** | [Browser management guide](docs/console-guide.md#deploy-your-first-application) | Connect a server, deploy a sample website, and open it in your browser                   |
| **3. Look after it** | [Everyday management](docs/console-guide.md#everyday-management)                | Check health, read logs, change application copies, and prepare a server for maintenance |

No command-line experience? The guides explain where to run each command, what to replace, and what success looks like. Initial server setup still needs someone who can install software and configure server access; everyday management can happen in the dashboard.

## A few words you will see

| Word                | Meaning                                                                |
| ------------------- | ---------------------------------------------------------------------- |
| **Cluster**         | The group of servers managed together                                  |
| **Manager**         | The computer running Zela's dashboard and coordinating the servers     |
| **Worker**          | A Linux server that runs your applications                             |
| **Container image** | A packaged application, such as `nginx:alpine` for a sample web server |
| **Deployment**      | An application configuration saved in Zela                             |
| **Replica**         | One running copy of a deployment                                       |

See the [plain-language glossary](docs/glossary.md) for networking, storage, and access terms.

## Choose how to start

| Your goal                               | Use                            | What you need                                                               |
| --------------------------------------- | ------------------------------ | --------------------------------------------------------------------------- |
| Explore without changing real servers   | **Demo mode**                  | Go 1.25 or newer and the project files                                      |
| Set up a live manager with its database | **Docker Compose**             | Docker with Compose and the project files; add workers afterward            |
| Install as a Linux background service   | **Linux service installation** | A server administrator, a certificate for secure access, and built binaries |
| Run a hosted manager in one click       | **Deploy to NEXUS AI**         | A NEXUS AI account and the `nexus` CLI to read your sign-in token          |

### Try the dashboard in Demo mode

In a terminal opened in the Zela project folder, run:

```sh
go run ./cmd/zela-server --demo
```

Leave that terminal open. In a second terminal in the same folder, display your sign-in token:

```sh
cat ./data-demo/admin-token
```

Open **<http://127.0.0.1:8080>** on that computer and paste the token into the sign-in screen. Keep the token private; it is your administrator password.

**Success looks like:** a dashboard with a **Demo environment** banner and simulated workers. These are practice servers, and no real applications are started.

> **Why is Configure network disabled?** Demo mode cannot create a real encrypted network. Live mode allows configuration; controls can also pause while another operation is running. See [disabled networking controls](docs/networking.md#why-are-the-controls-disabled).

### Deploy to NEXUS AI

[![Deploy to NEXUS AI](https://nexusai.run/deploy-button.svg)](https://nexusai.run/deploy?repo=https://github.com/nexusrun/zela&port=8080)

Select the button above. NEXUS AI builds this repository's Dockerfile and gives the manager a public HTTPS URL. Nothing needs to be configured first.

On first start, Zela creates a random sign-in token in `/data/admin-token`. To read it, install the [NEXUS AI CLI](https://www.npmjs.com/package/nexusapp-cli) and run, with the deployment ID shown in your NEXUS AI dashboard:

```sh
nexus exec <deployment-id> cat /data/admin-token
```

Open your deployment URL and paste the token into the sign-in screen. Keep the token private; it is your administrator password.

**Good to know:** this runs the manager only, storing its state in files under `/data` (no PostgreSQL). Workers are your own servers, connected afterward as in the [live setup steps](docs/getting-started.md#run-a-live-manager-with-docker-compose). Rebuilding the deployment starts with fresh state and a new token.

For a real installation, follow the [live setup steps](docs/getting-started.md#run-a-live-manager-with-docker-compose). Starting a live manager does not automatically connect workers or publish your applications.

## Install the manager on a Linux server

The [installation script](docs/installer.md) installs Zela, PostgreSQL, and systemd
services on Ubuntu, Debian, RHEL, Rocky Linux, AlmaLinux, or CentOS Stream 9/10. From a source
checkout on the manager server, run `make install` after installing the build
prerequisites in the guide. A published release is not required. It includes the CLI and worker binaries, preserves
credentials on repeat runs, and starts the dashboard with private SSH-tunnel access.

## What you can manage

| In the dashboard        | You can                                                                                        |
| ----------------------- | ---------------------------------------------------------------------------------------------- |
| **Overview**            | See worker health, application status, and reserved resources                                  |
| **Workers**             | Connect servers, pause new placements, and drain servers for maintenance                       |
| **Deployments**         | Start applications, change replicas, update images, and move eligible workloads                |
| **Containers**          | Inspect running copies and read their logs                                                     |
| **Networking**          | Configure the encrypted worker network, inspect Docker networks, and manage application routes |
| **Monitoring & Alerts** | Review measured usage, history, and problems that need attention                               |
| **Activity log**        | Check whether a change succeeded or failed                                                     |
| **Settings**            | Manage integration access and AI connections                                                   |

## Before using it for important applications

- Use secure **HTTPS** access for a remotely accessible dashboard, and protect administrator credentials. See [Security](docs/security.md).
- Keep backups of Zela's saved configuration and its encryption key. Back up application data separately. See [Backups](docs/operations.md#backups-and-restore).
- Moving or recovering an application recreates its containers. Zela does **not** copy database files, volumes, or other local application data between workers.
- Applications created outside Zela's managed Deployments do not receive the same automatic recovery. Check the [management guide](docs/console-guide.md#deployments-and-containers-what-is-the-difference).
- Demo and Live mode have separate saved data. Start the manager without `--demo` to work with real servers; the demo workers do not become real workers.

## More help

| I want to…                         | Read                                                                                              |
| ---------------------------------- | ------------------------------------------------------------------------------------------------- |
| Find the right guide               | [Documentation home](docs/README.md)                                                              |
| Install on a server                | [Installation](docs/installation.md)                                                              |
| Understand network settings        | [Networking](docs/networking.md)                                                                  |
| Connect an AI assistant            | [AI cluster management](docs/mcp.md)                                                              |
| Back up, upgrade, or fix a problem | [Operations & troubleshooting](docs/operations.md)                                                |
| Use terminal commands              | [CLI guide](docs/cli.md)                                                                          |
| Look up a setting or API           | [Configuration](docs/configuration.md) · [Administrator & API reference](docs/admin-reference.md) |
| Run backup managers                | [High availability](deploy/HA.md)                                                                 |
| Build or contribute code           | [Contributing](CONTRIBUTING.md)                                                                   |

## License

Zela is available under the [Apache License 2.0](LICENSE). You can use, modify, and share it under the terms of that license.

Zela was first developed as the infrastructure layer for NEXUS AI.
