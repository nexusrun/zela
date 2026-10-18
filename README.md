# Zela

**Run and manage your applications from one dashboard.**

Zela brings your Linux servers together so you can deploy applications, see how they are doing, and manage them in a browser. You can also connect an AI assistant to help operate the cluster.

You provide the servers and a container image for each application. Zela can install Docker on supported servers, start application copies, monitor them, and replace managed application copies when a worker fails and another suitable worker has room.

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

For a real installation, follow the [live setup steps](docs/getting-started.md#run-a-live-manager-with-docker-compose). Starting a live manager does not automatically connect workers or publish your applications.

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

Zela was first developed as the infrastructure layer for NEXUS AI.
