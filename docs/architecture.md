# How Zela fits together

[All guides](README.md) · [Glossary](glossary.md)

## The plain-language version

You use the **dashboard** to ask the **manager** to run an application. The manager chooses a suitable **worker**, and Docker on that worker runs the application container. The manager saves settings and checks workers regularly.

```text
You → Zela dashboard → Manager → Worker servers → Your applications
                          ↓
                 Saved settings and history
```

The manager coordinates work; your applications run on the workers. Restarting the manager normally leaves those application processes running, but management and monitoring pause. A manager backup also does not back up application files on the workers.

| Component                     | Everyday role                                                      |
| ----------------------------- | ------------------------------------------------------------------ |
| Manager                       | Dashboard, decisions about where apps run, and operation tracking  |
| Worker agent                  | Lets the manager manage Docker and read measurements on one server |
| Database or local state files | Remember Zela's configuration and history                          |
| CLI or AI connection          | Another way to ask the same manager to perform supported tasks     |
| Optional encrypted network    | Private communication between application workers                  |

## Technical connection map

```text
     Browser console / zela / Docker CLI & SDKs / AI clients (MCP)
                                     │  HTTPS :8080, Bearer token
                                     ▼
                ┌──────────────── control plane ─────────────────┐
                │ REST API · web console · Docker gateway         │
  (HA only)     │ scheduler · inventory poller · monitoring       │
  load balancer │ SSH provisioner · encrypted SSH-key vault       │
  ──/api/ready─►└───────┬───────────────────────┬────────────────┘
                        │ state                  │ worker traffic
                        ▼                        ▼
        PostgreSQL (or JSON files      ┌─────────────┬─────────────┐
        in --data for one manager)     │ HTTPS :9091 │ SSH tunnel  │  (one per worker)
                                       ▼             ▼
                                  zela-worker  →  /var/run/docker.sock  →  containers
```

## Control plane

A single Go binary (`cmd/zela-server`) that serves:

- **Web console:** static assets embedded in the binary, with no CDN and no frontend build step.
- **REST API:** `/api/...`, the endpoints the console, `zela` and integrations use.
- **MCP server:** `/mcp` (Streamable HTTP) exposes a fixed catalog of 19 named tools to AI clients, plus OAuth endpoints (`/oauth/*`, `/.well-known/*`) when `ZELA_MCP_PUBLIC_URL` is set. Every tool call re-enters the normal REST API in-process, so the usual authentication, scope checks, admission, operation gate and HA fencing all apply. `zela mcp serve` offers the same catalog over stdio for local assistants. See [AI cluster management](mcp.md).
- **Docker gateway:** Docker Engine API paths (`/version`, `/containers/...`, `/v1.51/...`) that are routed to the right worker, so the whole cluster looks like one Docker endpoint.
- **Background work:**
  - **Inventory poller:** checks every worker every 10 seconds.
  - **Operations runner:** one cluster mutation at a time.
  - **Failover:** replaces replicas lost on dead workers.
  - **Monitoring:** alerts and notifications.

Mutations (deploy, scale, move, provision, and so on) run as **operations**. The API returns `202` with an operation ID and you poll for the result. Only one operation runs at a time; a second gets `409` until the first finishes.

## Workers

A worker is a Docker host. The control plane reaches it in one of three ways:

| Mode                 | Endpoint                                  | Transport                   | Typical use                                                      |
| -------------------- | ----------------------------------------- | --------------------------- | ---------------------------------------------------------------- |
| **SSH-provisioned**  | Agent on worker loopback `127.0.0.1:9091` | SSH tunnel; host key pinned | Default. The control plane installs Docker and the agent for you |
| **Manual TLS agent** | `https://host:9091`                       | TLS + bearer token          | Hosts you manage yourself                                        |
| **Local socket**     | `unix:///var/run/docker.sock`             | Unix socket                 | Docker on the manager host (not allowed with HA)                 |

`zela-worker` authenticates every request with its token. It exposes a small allow-list of management routes, `/zela/metrics`, and the full Docker Engine API under `/docker/` for the gateway. Treat its token as Docker-administrator, effectively root, access to that host.

## State

| Backend                          | When                         | Holds                                                                                                         |
| -------------------------------- | ---------------------------- | ------------------------------------------------------------------------------------------------------------- |
| PostgreSQL (`ZELA_DATABASE_URL`) | Recommended; required for HA | Workers, deployments, operations, tokens (hashed), encrypted vault, monitoring history, alerts, notifications |
| JSON files in `--data`           | No database configured       | The same data, as private `0600` files                                                                        |

The `--data` directory is always needed, even with PostgreSQL. It holds the generated admin token, the vault master key (unless `ZELA_VAULT_KEY` is set) and temporary SSH identity files. See [Configuration → Data directory](configuration.md#data-directory).

A single PostgreSQL session holds an advisory lock. Exactly one manager writes at a time; a second standalone manager is refused.

## Availability model

**Workers.** The poller marks a worker `offline` after **3 consecutive failed probes**, about 20–30 seconds. Once a worker has not been seen for **60 seconds**, the control plane:

1. starts a _Recover_ operation for every running deployment with replicas on that worker;
2. recreates those replicas on healthy workers;
3. remembers the old replicas. When the worker returns, they are re-adopted if the deployment is short of replicas, or removed otherwise, so a returning host never runs duplicates.

Failover recreates containers from their image and configuration. It does **not** move volumes, filesystem changes or in-memory state. Use it for stateless workloads.

**Managers (optional HA).** Several managers share one PostgreSQL database. The leader holds the advisory lock and checks it every 2 seconds; standbys answer `503` on `/api/ready` until they win the lock. Each new leader takes a higher **leadership epoch** and sends it on every worker request, and workers reject requests that carry a lower epoch. This fences off a stalled former leader. See [High availability](../deploy/HA.md).

**Networking.** By default, replicas publish host ports on their worker. The opt-in **cluster network** (`--cluster-network`) adds an encrypted WireGuard mesh with a routed container subnet per worker, so containers reach each other across hosts by IP, service names (`web.zela`) answered by a resolver on every worker, a stable load-balanced virtual IP per deployment, and an optional edge proxy on every worker for external HTTP(S). See [Networking](networking.md).

**Not provided:** shared application storage, volume replication, or database replication. Automatic website certificates are available only when [ACME](networking.md#automatic-certificates-acme) is configured; they are not enabled merely by starting Zela. Docker restart policies handle restarts on a single host.

## Trust boundaries

| Credential                             | Grants                                                                                                                                  | Where it lives                                                              |
| -------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| Admin token                            | Full control of the cluster and every Docker host                                                                                       | `ZELA_ADMIN_TOKEN` or `<data>/admin-token`                                  |
| Integration tokens                     | Scoped access to containers created with that token only                                                                                | SHA-256 hashes in state; value shown once                                   |
| MCP tokens (`mcp_read` / `mcp_manage`) | Cluster-wide inspection, or cluster-wide operation (provisioning only for hosts in `ZELA_MCP_PROVISION_HOSTS`) through named tools only | SHA-256 hashes in state; OAuth access tokens last 1h, refresh up to 30 days |
| Worker token                           | Docker-administrator access on one host                                                                                                 | `/etc/zela/worker.env` on the worker; stored in private manager state       |
| SSH keys                               | Shell on worker hosts                                                                                                                   | Vault, AES-256-GCM, key in `ZELA_VAULT_KEY` or `<data>/vault-key`           |
| Database login                         | All cluster state                                                                                                                       | `ZELA_DATABASE_URL` + `PGPASSWORD`/pgpass                                   |

See [Security](security.md) for hardening guidance.

## Ports

| Port                       | Component                                  | Exposure                                                                            |
| -------------------------- | ------------------------------------------ | ----------------------------------------------------------------------------------- |
| 8080/tcp                   | Control plane API, console, Docker gateway | Admins and integrations; behind a load balancer for HA                              |
| 9091/tcp                   | `zela-worker`                              | Loopback only when SSH-provisioned; otherwise reachable **only from managers**      |
| 22/tcp                     | SSH on workers                             | From managers, for provisioning and SSH-tunnelled agents                            |
| 5432/tcp                   | PostgreSQL                                 | From managers only                                                                  |
| 51820/udp                  | Cluster network (WireGuard)                | Between workers only, when `--cluster-network` is enabled                           |
| 80, 443/tcp (configurable) | Edge proxy on workers                      | Public or load-balancer-facing, when `--edge-http-port`/`--edge-https-port` are set |

## Shared application storage

See [Shared application storage](shared-storage.md) for NFS setup, application permissions, deployment policies, safe moves, interrupted-move recovery, and live acceptance tests. Storage administration requires an administrator. Single-writer automatic failover remains blocked without independently enforced fencing.
