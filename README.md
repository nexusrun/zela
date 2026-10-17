# Zela

*Self-hosted container orchestration, minus the Kubernetes overhead.*

**Zela** is a lightweight container orchestrator for teams who want Kubernetes-style resilience without Kubernetes-style complexity. Point it at the Linux servers you already have, and Zela turns them into one cluster, with an encrypted WireGuard mesh, service discovery, load balancing, and automatic HTTPS built in. One binary per host, no etcd, no YAML sprawl, and an MCP server so your AI assistant can run the cluster too.

- **Bring your own hosts:** provision any SSH-reachable Linux server, then deploy, scale, roll out, move and drain workloads from the web console, the `zela` CLI or the API.
- **Self-healing:** replicas on an unreachable host are replaced on healthy workers after 60 seconds, and optional active/standby managers on PostgreSQL take over when the leader fails.
- **Networking included:** opt-in cluster network with `<service>.zela` DNS names, load-balanced service IPs and an edge proxy with Let's Encrypt certificates (see [networking](docs/networking.md)).
- **Docker-compatible:** existing Docker CLI and SDK clients work through the cluster gateway.

The web console is embedded in the `zela-server` binary and works without an external CDN or frontend build. Workers use only the Go standard library plus WireGuard support.

## Documentation

Full guides live in [docs/](docs/README.md):

- [Getting started](docs/getting-started.md): demo, Compose and first deployment
- [Architecture](docs/architecture.md): components, ports, availability model
- [Installation](docs/installation.md): binaries, PostgreSQL, systemd, containers, workers, CLI
- [Configuration reference](docs/configuration.md): every flag, variable, file and default
- [Networking](docs/networking.md): published ports, load balancers and the encrypted cluster network
- [High availability](deploy/HA.md) · [Security](docs/security.md) · [Operations & troubleshooting](docs/operations.md) · [CLI](docs/cli.md)
- [Contributing](CONTRIBUTING.md) · [Security policy](SECURITY.md)

The sections below cover the console, the Docker gateway and API behavior in depth.

## Start the console

Requires Go 1.25+ on Linux or macOS.

```sh
go run ./cmd/zela-server
```

Open **http://127.0.0.1:8080**. On first start, the server generates an admin token in `./data/admin-token`, with mode `0600`:

```sh
cat ./data/admin-token
```

Use that token on the sign-in screen. You can instead set `ZELA_ADMIN_TOKEN` to a secret with at least 32 characters. Browser sessions last 12 hours; scripts authenticate with `Authorization: Bearer <admin-token>`.

To explore a populated, isolated simulation:

```sh
go run ./cmd/zela-server --demo
cat ./data-demo/admin-token
```

Demo mode simulates Docker engines and persists its own state. It never connects to Docker or modifies real hosts.

## Remote CLI

Build `zela` with `make cli` or use the Linux amd64/arm64 artifacts from `make linux`:

```sh
./bin/zela login --profile production --url https://cluster.example.internal:8443 --token-file /secure/admin-token
./bin/zela workers list
./bin/zela workers pause wrk_EXAMPLE --wait
./bin/zela notifications list
```

The CLI connects to the same cluster endpoint as the UI and API clients, supports private profiles, verified TLS, scoped tokens, JSON output, SSH worker provisioning, vault and monitoring administration, and operation polling. It does not need Docker or SSH access on the client machine. See the [CLI guide](docs/cli.md) for RHEL installation, provisioning, permissions, and automation. See the [MCP guide](docs/mcp.md) to connect AI assistants.

## AI cluster management (MCP)

Connect ChatGPT, Claude and coding assistants to **`https://your-cluster/mcp`** for natural-language cluster operations. The platform exposes 19 named tools for health, inventory, logs, deployment lifecycle and scaling, worker maintenance and provisioning. Dedicated `mcp_read` and `mcp_manage` credentials separate cluster inspection from changes.

Set `ZELA_MCP_PUBLIC_URL=https://your-cluster` before starting the manager to enable browser OAuth for remote connectors. The consent page grants a separate revocable credential. For local assistants, save an MCP token in a CLI profile and run `zela --profile production mcp serve`. Settings & setup → AI cluster management shows setup information and credential shortcuts. See the [MCP connection guide](docs/mcp.md) for ChatGPT/Claude setup, OAuth callbacks, stdio configuration and permissions.

## PostgreSQL storage and automatic initialization

Set `ZELA_DATABASE_URL` before starting the control plane:

```sh
export ZELA_DATABASE_URL='postgres://zela@db.example.internal:5432/zela?sslmode=verify-full'
# Supply the database password securely through PGPASSWORD or a protected pgpass file.
go run ./cmd/zela-server --data ./data
```

The PostgreSQL server and database must already exist for an external connection. The login needs connection access and permission to create the `zela` schema and its tables. Startup automatically applies versioned schema migrations and initializes the state; no manual SQL migration command is needed. Database connection strings are never included in logs or API responses. Remote connections should use `sslmode=verify-full` and a trusted CA, following [PostgreSQL's TLS guidance](https://www.postgresql.org/docs/current/libpq-ssl.html).

For a complete local container installation, the supplied Compose stack creates the PostgreSQL database automatically and waits for it to be ready before starting the control plane:

```sh
# Set this once in your current shell; keep the password for subsequent starts.
export ZELA_POSTGRES_PASSWORD="$(openssl rand -hex 32)"
docker compose up --build -d
docker compose exec zela-server cat /data/admin-token
```

Alternatively copy `.env.example` to `.env` and replace its placeholder with a generated password; keep `.env` private. PostgreSQL is not published on a host port. Compose uses unencrypted database traffic within its private container network; use TLS for an external database.

Workers, deployment configurations, operations, encrypted SSH secrets, monitoring history, alert rules, and acknowledgements are stored in PostgreSQL. The schema (currently version 2) stores three transactional JSONB documents in `zela.state`, plus the HA leadership epoch in `zela.leadership`, supporting single-writer state and bounded monitoring history. Updates replace the relevant document atomically. This design retains the full monitoring document in memory and rewrites it each poll; it is intended for modest clusters, not large telemetry workloads.

On the first start against an **empty database**, existing `cluster.json`, `vault.json`, and `monitoring.json` in `--data` are validated and imported in one transaction. The original files remain as a pre-import backup. Keep the original `vault-key` or `ZELA_VAULT_KEY`; private keys remain AES-256-GCM encrypted in the database. An invalid or undecryptable import fails startup without committing partial state. Once initialized, the database is authoritative and local JSON files are ignored. Switching back to JSON uses those older files; it does not export current database state.

The data directory remains required for the local admin token, vault master key (unless supplied externally), and temporary SSH identities. Back up PostgreSQL together with the original vault master key and admin token. Compose retains both volumes across restarts; deleting volumes deletes their contents. Use a separate database and data directory for demo mode.

A dedicated PostgreSQL session holds an [advisory cluster lock](https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS), so a second standalone manager sharing the database fails startup even with a different data directory. With `--ha`, additional managers stay online as standbys and take over after the session lock is released. Use a direct database connection or session pooling; transaction-pooling proxies cannot preserve this session lock. The session never reconnects silently. Failed writes roll back in-memory changes, and the manager checks the session every ten seconds and shuts down on database connectivity loss. Resolve the database issue and restart the manager; interrupted jobs are marked failed for inspection. Enable active/standby manager high availability with `--ha`, shared credentials and a readiness-aware cluster gateway; see the [HA deployment guide](deploy/HA.md). Without `ZELA_DATABASE_URL`, local JSON storage remains available.

For an optional integration test against a reachable PostgreSQL server:

```sh
# Use a test login with CREATEDB; the test creates and drops its own isolated database.
ZELA_TEST_DATABASE_URL='postgres://test_user@localhost/postgres?sslmode=disable' go test ./internal/control -run TestPostgresIntegration -v
```

## Provision a Docker worker from the console

Provisioning installs and configures an **existing SSH-accessible host**; it does not create cloud VMs. The control-plane machine needs OpenSSH (`ssh` and `ssh-keygen`) and Linux worker artifacts:

```sh
make build
./bin/zela-server --worker-binaries ./bin
```

The Docker image already includes OpenSSH and both worker architectures. To use `go run`, build the artifacts first with `make workers`.

The installer detects the operating system automatically. Supported target platforms, all requiring systemd and an amd64 or arm64 CPU:

| Platform               | Releases            | Package manager / Docker repository |
| ---------------------- | ------------------- | ----------------------------------- |
| RHEL                   | 8, 9, 10            | DNF / Docker RHEL RPM repository    |
| Rocky Linux, AlmaLinux | 8, 9, 10            | DNF / Docker RHEL RPM repository    |
| CentOS Stream          | 9, 10               | DNF / Docker CentOS RPM repository  |
| Ubuntu LTS             | 22.04, 24.04, 26.04 | apt / Docker Ubuntu repository      |
| Debian                 | 12, 13              | apt / Docker Debian repository      |

Repository setup follows Docker's [RHEL](https://docs.docker.com/engine/install/rhel/), [CentOS](https://docs.docker.com/engine/install/centos/), [Ubuntu](https://docs.docker.com/engine/install/ubuntu/), and [Debian](https://docs.docker.com/engine/install/debian/) guides. Rocky Linux's [Docker guide](https://docs.rockylinux.org/gemstones/containers/docker/) also uses the RHEL repository; AlmaLinux uses the same compatible RPM path. Other RPM distributions, including Fedora, Oracle Linux, and Amazon Linux, are not automatically provisioned by this installer.

The host needs access to its package repositories and `download.docker.com`. RHEL must already have working BaseOS/AppStream repositories, through a subscription or your cloud provider; provisioning does not register subscriptions. The SSH login must be root or have passwordless sudo; its public key must already be in the login user's `authorized_keys`. The SSH server must permit forwarding to `127.0.0.1:<agent-port>`.

1. Open **Secrets vault → Add SSH key**. Paste or import the private key, give it a name, and supply its passphrase if encrypted.
2. Open **Workers → Provision worker**. Enter the host address, SSH port/user, region, and select the saved key.
3. Supply the **trusted host public key**, obtained through the host console or another verified source. For example, read `/etc/ssh/ssh_host_ed25519_key.pub` on the host and copy the `ssh-ed25519 AAAA…` portion. This is the server's identity, separate from your login key. SSH rejects a different host key.
4. Submit and follow the stages in **Activity**: verify host, install Docker, upload agent, configure service, and verify connectivity. The worker becomes active only after Docker inventory is reachable.

The installer uses Docker's official signed apt or RPM repository and installs Engine, CLI, containerd, Buildx, and Compose. The RPM path verifies Docker's signing-key fingerprint before importing it and keeps package signature checking enabled. It uses the host's major release for the Docker repository without changing the OS's DNF release lock. Fresh installations receive bounded log rotation if no daemon configuration exists. Fresh RPM Docker configurations also enable Docker's SELinux integration; SELinux mode and firewall rules remain unchanged. File security contexts are restored for the agent binary, configuration, and service unit. A healthy existing Docker installation is reused without upgrading it or overwriting its daemon settings.

Conflicting runtimes are reported for operator review; the installer does not uninstall them or use DNF's `--allowerasing`. On RPM hosts this includes Podman, `podman-docker`, old Docker packages, standalone `containerd`, and `runc`. If your RHEL image includes Podman, review its workloads and resolve the package conflict yourself before provisioning or retrying. Provisioning does not convert Podman containers to Docker containers.

The agent runs as a systemd service named `zela-worker.service`, under a dedicated `zela-worker` user with Docker group access. Its randomly generated token is stored in `/etc/zela/worker.env`. It listens only on host loopback (default port `9091`); the control plane accesses it through a host-key-verified SSH tunnel. No public agent port or unauthenticated Docker TCP socket is opened. SSH remains necessary for ongoing management, so keep the login key authorized and the vault key available.

Failed jobs leave the worker paused with error details. Inspect `journalctl -u zela-worker.service`, Docker service logs, SSH access, and sudo privileges before selecting **Retry provisioning**. Retry preserves the worker ID and token. An interrupted manager job is marked failed on restart and is not automatically replayed. Installation may have already changed packages or services; failure and unregistering do not uninstall Docker or remove the agent. Demo mode simulates remote steps, but imported SSH keys are still validated and encrypted locally.

### SSH secrets vault

Private keys and passphrases are encrypted with AES-256-GCM in PostgreSQL when configured, or in `<data>/vault.json` with local storage. The API exposes only metadata and fingerprints, with no private-key export endpoint. A referenced key cannot be deleted while its worker remains registered.

By default, the manager generates `<data>/vault-key`, a base64-encoded 32-byte master key with mode `0600`. For separate master-key storage, set `ZELA_VAULT_KEY` to a securely supplied base64-encoded 32-byte key **before the first start**. Keep the same key across restarts; changing it does not rotate existing secrets. Missing or incorrect keys fail startup instead of replacing the vault. Back up the database (or local state files) and the original master key securely; encryption cannot protect a vault from someone who also obtains its master key.

OpenSSH requires temporary plaintext identity files. These are created with mode `0600` inside a private `<data>/ssh-tmp` directory, removed after each connection, and cleared after a successful vault open on restart. Protect the entire data directory and use HTTPS for console access across a network.

## Connect a Docker host

Build and copy the worker binary to each host:

```sh
mkdir -p bin
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/zela-worker ./cmd/worker
# Use GOARCH=arm64 for ARM hosts.
```

On the worker, generate and securely retain a unique token, then run the agent with access to its Docker socket:

```sh
export ZELA_WORKER_TOKEN="$(openssl rand -hex 32)"
./zela-worker --listen 0.0.0.0:9091 \
  --tls-cert /etc/zela/worker.crt \
  --tls-key /etc/zela/worker.key
```

Use a certificate trusted by the control-plane machine, with a subject alternative name matching the endpoint's DNS name or IP. A private CA can be added to the control-plane system trust store or supplied with Go's `SSL_CERT_FILE`. Worker tokens authorize Docker control and should be treated as host administrator credentials.

In the console, select **Workers → Connect worker**, enter its name, region, `https://worker-host:9091`, and token. Zela checks the connection and discovers its containers. An unsuccessful connection remains visible with the error so you can inspect it or unregister the empty record.

For Docker on the same machine as the control plane, register `unix:///var/run/docker.sock`; a local socket needs no worker token. Docker Desktop commonly uses `unix:///Users/<username>/.docker/run/docker.sock`.

For a trusted private network without TLS, both sides require an explicit opt-in:

```sh
# Worker host
./zela-worker --listen 0.0.0.0:9091 --allow-http
# Control plane
./zela-server --allow-http-workers
```

Restrict access to the worker port to the control-plane host. The worker authenticates every request. Management and monitoring use restricted routes; the `/docker/` namespace provides full Docker Engine API access for the control-plane gateway. Treat the agent token as a Docker administrator credential.

## Integration API tokens

Sign in as administrator, then open **Settings & setup → Integration API tokens → Create token**. **Zela infrastructure** is the default permission choice. Choose a name and expiry, then copy the token from the one-time dialog. Configure the cluster URL separately and send `Authorization: Bearer <token>` on every request. Never put the token in the URL.

- **Infrastructure (`infrastructure`)**: reads cluster worker health, capabilities, capacity reservations and current host usage. Zela selects a worker for container creation and image pulls. The control plane verifies admission and routes subsequent requests to the container's persisted owner. This role cannot invoke the console's deployment, scheduling, move or scaling APIs.
- **MCP read (`mcp_read`)**: cluster-wide health, inventory, logs and operation inspection through the named MCP tools.
- **MCP management (`mcp_manage`)**: additionally changes deployments, containers and workers across the cluster, including workloads created by other clients. These credentials cannot access the raw Docker gateway, full state, token administration or private-key contents. See [MCP permissions](docs/mcp.md#tools-and-permissions).
- **Deployment integration (`deployments`)**: retains automatic placement and the console deployment APIs for backward compatibility. This role can also read the infrastructure API and explicitly select a worker within its workload permissions. Existing tokens keep this scope.

Infrastructure and deployment integration roles can manage **only containers created with that token**, including inventory, lifecycle, logs, stats, attach, archive and exec. A worker selector never grants access to administrator-created or another token's containers. Both roles reject worker administration/provisioning, SSH vault access, platform settings/state, notifications, token administration and administrator UI sign-in. Privileged creation/exec, host mounts/devices/namespaces/networking, added capabilities and custom runtimes/security options are blocked. **Builds, plugins, custom networks and volume administration require administrator access.** Build and publish images separately for scoped integrations.

Token values contain 256 bits of randomness. Only SHA-256 hashes and metadata are persisted in JSON or PostgreSQL; state and token listings never expose hashes or full values. The UI cannot create administrator tokens. MCP credentials are separate cluster-wide operator roles, not workload ownership credentials. For infrastructure and deployment integrations, a replacement token has separate workload ownership; their existing containers require the original active token or an administrator/MCP management credential.

**Revoke** disables API access immediately and cancels active gateway streams. Expiry also terminates active requests. Running containers remain; accepted asynchronous legacy deployment operations may finish. Revocation survives restarts. A mutation may already have reached Docker when access ends; inspect state before retrying.

Administrator-only token APIs:

- `POST /api/tokens` with `{"name":"my-platform","scope":"infrastructure","expiresInDays":90}` returns `{token,metadata}` once (`201`). Zero days means no expiry. An omitted scope defaults to `deployments` for API compatibility; the UI explicitly sends `infrastructure` by default.
- `GET /api/tokens` returns metadata only.
- `POST /api/tokens/{id}/revoke` disables a token.

## Infrastructure API for external schedulers

Zela owns application scheduling, deployment, recovery and routing. The control plane supplies worker provisioning/management, monitoring, secure Docker access, admission checks and durable ownership routing. Infrastructure tokens use these authenticated, read-only endpoints:

| Endpoint | Response |
| --- | --- |
| `GET /api/cluster` | `{observedAt,demo,capabilities,workers}` |
| `GET /api/cluster/workers` | `{observedAt,workers}` |
| `GET /api/cluster/workers/{id}` | One worker; unknown IDs return `404` |

Each worker exposes ID, name, region, placement mode, connectivity status, Docker version, operating system, architecture, inventory `lastSeen`, `inventoryFresh`, and `acceptingContainers`. `capacity` contains total/reserved/available CPU and memory, plus unavailable host ports (port and protocol). `usageFresh` and optional `usage` contain current CPU percentage, memory usage and disk usage, with `sampledAt`. Worker endpoints, credentials, SSH settings, raw errors, other applications' names/configuration and container telemetry are excluded.

`observedAt` is the response snapshot time; `lastSeen` and `sampledAt` identify the actual measurements. Online inventory and host metrics expire after 45 seconds. Check freshness and placement mode before choosing a worker. `acceptingContainers` is an advisory snapshot; admission also checks the request's limits, ports and fresh worker connectivity before creation. Resource availability uses persisted reservations (memory rounded to MiB), **not measured free memory**. Unmanaged workloads do not contribute resource-limit reservations; inspect measured usage as well. Port checks conservatively reserve each port/protocol across host IPs. Disk figures describe sampled filesystems, not a managed Docker storage pool.

Discover and choose a worker, then send standard Docker requests to the same cluster endpoint:

```bash
# Set CLUSTER_URL and CLUSTER_TOKEN through your integration's environment.
curl -sS "$CLUSTER_URL/api/cluster/workers" \
  -H "Authorization: Bearer $CLUSTER_TOKEN"

# Zela sets WORKER_ID after applying its scheduling policy.
curl -sS -X POST "$CLUSTER_URL/v1.51/containers/create?name=zela-app" \
  -H "Authorization: Bearer $CLUSTER_TOKEN" \
  -H "X-Zela-Worker: $WORKER_ID" \
  -H 'Content-Type: application/json' \
  -d '{"Image":"nginx:alpine","HostConfig":{"Memory":134217728,"NanoCpus":250000000}}'

# Use the returned Id as CONTAINER_ID. The worker selector is optional here.
curl -sS -X POST "$CLUSTER_URL/v1.51/containers/$CONTAINER_ID/start" \
  -H "Authorization: Bearer $CLUSTER_TOKEN"
```

Negotiate the Docker API version at `/version`; the example's `v1.51` should be replaced with the negotiated version. `/docker/workers/<worker-id>/containers/create` is an alternative to the header. Infrastructure tokens require a worker selector for container creation and image pulls (`400` if omitted); the gateway never falls back to a different worker. An unknown worker returns `404`; paused/draining/offline/unreachable workers or insufficient CPU/memory/ports reject creation with `503`. Concurrent creations may return `409`; retry after the current placement finishes. Default limits remain 128 MiB and 0.25 CPU when omitted. Image pulls occur only on the selected worker. Admission is checked again after image preparation.

Container and exec lifecycle requests may omit the selector: persisted ownership determines the host. If supplied, the selector must match the owner (`409` otherwise). Own containers remain manageable on an online paused/draining worker; new creations are blocked. Selected container inventories contain only that token's containers on that worker, and do not depend on other workers being reachable. Selected names are checked against persisted/cached cluster inventory and Docker's live namespace on the chosen worker; unmanaged names on other hosts may not be known until polling. Use full container IDs when names are ambiguous. Containers created through the gateway are not moved or recreated automatically on host failure; Zela owns their recovery. (Console-managed deployments do fail over; see [Operations](docs/operations.md#failure-behavior).) Optional active/standby manager high availability uses PostgreSQL coordination and a readiness-aware load balancer; see the [HA deployment guide](deploy/HA.md).

## Docker API access

Application clients connect to the control plane's HTTP/HTTPS port, **8080** by default, with **`Authorization: Bearer <token>`** (an administrator or integration token created in Settings). Zela infrastructure integrations discover/select worker IDs through the control plane and never need worker credentials or direct access to worker management ports:

```text
Docker client → cluster gateway:8080 → Zela-selected worker → Docker Engine
```

The following automatic-placement flow remains available to administrator and legacy deployment tokens. Infrastructure tokens use explicit selection as described above. Send standard Docker requests to the control-plane origin without `X-Zela-Worker`. Container creation chooses an active, online worker with sufficient reserved CPU/memory and available published ports. Placements spread across eligible workers. The selected host is probed before creation, and the requested image is inspected and pulled there if necessary; `X-Registry-Auth` is forwarded for private registries. Without resource limits, automatic placement applies **128 MB memory and 0.25 CPU** to the container. Specified limits are preserved and reserved until deletion. Creation shares the managed deployment operation gate; concurrent placement requests can return `409` and should be retried after the current operation completes. No eligible capacity returns `503`.

The gateway persists container ID, name, owner, CPU/memory/port reservations and exec ownership in the configured JSON or PostgreSQL backend. Start, stop, restart, inspect, logs, stats, wait, attach, archive, rename and delete requests resolve the owning worker by container ID or name. Exec creation records its owner so later exec requests work without a selector. Full IDs remain routable after a manager restart or inventory delay. Pausing a worker prevents new placement while existing containers remain manageable. An offline owner returns `503`; running processes and persistent data are not relocated automatically. Inventory polling releases reservations for containers deleted externally. Existing unindexed containers can be discovered across workers; duplicate names or ambiguous short IDs return `409` instead of choosing an arbitrary host.

Examples, after securely setting `ZELA_ADMIN_TOKEN`:

```sh
export ZELA_CONTROL_URL=http://127.0.0.1:8080

curl -H "Authorization: Bearer $ZELA_ADMIN_TOKEN" "$ZELA_CONTROL_URL/version"
curl -H "Authorization: Bearer $ZELA_ADMIN_TOKEN" "$ZELA_CONTROL_URL/containers/json?all=true"

# The gateway chooses the worker and ensures the image exists there.
curl -X POST -H "Authorization: Bearer $ZELA_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"Image":"nginx:alpine","HostConfig":{"Memory":134217728,"NanoCpus":250000000,"RestartPolicy":{"Name":"unless-stopped"}}}' \
  "$ZELA_CONTROL_URL/containers/create?name=zela-web"

# Set ZELA_CONTAINER_ID to the Id returned by create.
curl -X POST -H "Authorization: Bearer $ZELA_ADMIN_TOKEN" \
  "$ZELA_CONTROL_URL/containers/$ZELA_CONTAINER_ID/start"
curl -H "Authorization: Bearer $ZELA_ADMIN_TOKEN" \
  "$ZELA_CONTROL_URL/containers/$ZELA_CONTAINER_ID/logs?stdout=true"
```

Cluster container lists aggregate all registered workers and fail if a host is offline rather than silently returning incomplete inventory. Image lists deduplicate identical IDs. A registry pull through `POST /images/create?fromImage=...` streams progress while pulling to each active online worker. Cluster version negotiation uses the lowest supported maximum API version and highest minimum across online hosts; hosts must have a common API version. `/info` aggregates capacity and container counts.

Docker CLI v27.1+ can send authentication using [Docker's custom header support](https://docs.docker.com/reference/cli/docker/#custom-http-headers):

```sh
export DOCKER_CUSTOM_HEADERS="Authorization=Bearer ${ZELA_ADMIN_TOKEN}"
docker -H tcp://127.0.0.1:8080 version
docker -H tcp://127.0.0.1:8080 ps
docker -H tcp://127.0.0.1:8080 run -d --name zela-web nginx:alpine
```

`DOCKER_CUSTOM_HEADERS` is experimental; the CLI's `HttpHeaders` configuration provides an alternative. Docker SDK clients should attach the bearer token to **all** requests, including negotiation. Browser session cookies do not authenticate Docker requests. Both versioned and unversioned paths follow the [Docker Engine API](https://docs.docker.com/reference/api/engine/).

Automatic placement currently supports **stateless containers with the built-in bridge, host or none network**. Host mounts, named volumes, devices, volumes-from, custom networks, resource-limit updates, builds, image imports, plugins and other worker-specific APIs require explicit worker affinity. Their data and namespaces are local to a Docker host; the gateway does not provide a shared filesystem, overlay network, Swarm API or automatic failover. Image-defined anonymous volumes remain local to the selected worker. Docker Compose networks and volumes also require a fixed worker. Unsupported cluster routes return `501`.

For deliberate worker-specific operations, discover IDs through authenticated `GET /api/cluster/workers` and supply `X-Zela-Worker: <worker-id>`, or use `/docker/workers/<worker-id>/` before the Docker path. With an administrator token, explicit selection preserves the full worker API, including builds, volumes and custom networks, and bypasses automatic placement/default limits. Scoped infrastructure and deployment tokens retain workload restrictions, capacity reservations and default limits even with explicit selection. Conflicting or duplicate selectors return an error. Explicit operations that alter automatically reserved resource limits require the operator to manage capacity.

Docker streams and binary uploads are preserved. Authenticated log/stats/wait and exec/attach streams continue until the client or manager disconnects; upstream reverse proxies must permit long streams and HTTP upgrades. Automatic and scoped container-create JSON is limited to 16 MB; administrator Docker uploads retain streaming behavior.

Newly provisioned workers include gateway support. **Existing agents must be updated**: rebuild with `make build` or `make linux`, then use **Monitoring → Update agent** for SSH-managed workers, or copy the worker binary and restart its service for manual agents. The manager uses existing pinned SSH or authenticated TLS connections; direct Unix-socket registrations also work. Demo mode returns `501` for Docker gateway requests.

The administrator bearer token grants full Docker administrator access. Integration tokens enforce the restricted role described above. Keep manager and worker tokens private, and use HTTPS with trusted certificates for remote clients. The Compose binding remains loopback by default.

Docker API containers appear in **Containers** after the next poll. They are not converted into console Deployments or Activity jobs. Automatic requests use cluster placement and reservations; the client owns the subsequent application lifecycle and retries. Console deployment/move features remain separate. App traffic needs published worker ports or an ingress/load balancer.

### Test the Docker gateway

```sh
make test-gateway
```

This runs race-checked tests for authentication, worker selection, uploads, streams, and protocol upgrades. The integration tests run actual HTTP servers and transports through **client → control-plane gateway → worker proxy → two simulated Docker engines**, over loopback TCP on ephemeral `127.0.0.1` ports, without touching real state or Docker. They verify image pull/create/start/inspect/logs/stop/delete, automatic placement and routing without worker headers, capacity and port reservations under concurrent requests, ownership across restarts, duplicate names, offline owners, rollback on failed persistence, worker isolation, network/volume lifecycle, inventory discovery, bidirectional exec across both proxies, binary build uploads above 1 MB, incremental progress delivery, and stream cancellation on client disconnect. These simulations do not prove compatibility with a live Docker daemon or exercise real SSH/TLS connectivity.

To deploy a real test container through an **already running cluster endpoint**, use:

```sh
ZELA_TEST_CLUSTER_URL=http://127.0.0.1:8080 \
ZELA_TEST_ADMIN_TOKEN_FILE="$PWD/data/admin-token" \
ZELA_TEST_DOCKER_IMAGE=alpine:latest \
make test-cluster-live
```

Use an absolute token file path and a Linux image containing `sh` and `sleep`. Omit `ZELA_TEST_WORKER_ID` to test automatic placement; set it only when deliberately testing explicit routing. The test connects only to the control-plane HTTP(S) endpoint, authenticates with the token file, checks that this is a real cluster, and sends version/create/start/inspect/logs/exec/stop/delete requests through the cluster gateway without a worker selector by default. It reports the running container ID and removes the uniquely named test container afterward, including on failure. It does not access worker sockets, start a replacement control plane, publish ports, or mount host directories. Automatic placement may pull the image on the selected worker if needed. HTTPS uses normal certificate validation. Missing configuration fails the Make target; an unreachable endpoint fails the test. No deployment is counted as successful unless container inspection confirms it is running.

For a separate component-level live Docker test with an isolated gateway and worker, use an accessible Docker Unix socket and a **locally available Linux image containing `sh` and `sleep`**, such as Alpine or BusyBox:

```sh
ZELA_TEST_DOCKER_SOCKET=/var/run/docker.sock \
ZELA_TEST_DOCKER_IMAGE=alpine:latest \
make test-docker-live
# Docker Desktop on macOS typically uses /Users/<user>/.docker/run/docker.sock.
```

The live test uses the production gateway and worker proxy with isolated temporary control-plane state. It negotiates the daemon version, creates a uniquely named container with a 64 MB memory limit and half a CPU, then tests start, inspect, logs, exec, and stop. Cleanup removes only that test container, including when a step fails. It does not pull images, publish ports, mount host directories, or change existing cluster state. Its HTTP connections to the gateway and worker use loopback TCP on ephemeral `127.0.0.1` ports; Docker execution uses the selected real Unix socket. `make test-docker-live` rejects missing configuration instead of reporting a skipped test as success.

## Operate the cluster

- **Workers:** Provision hosts over SSH or connect existing agents; inspect Docker version, CPU/memory capacity, and containers; pause or activate placement; drain for maintenance; unregister empty hosts.
- **Secrets vault:** Import and validate SSH identities, inspect their fingerprints and worker references, and delete unused keys.
- **Deployments:** Specify an image, replicas, placement, environment variables, command, host ports, and per-replica CPU/memory limits. Schedule across eligible hosts or select one host.
- **Lifecycle:** Start, stop, restart, scale, and delete managed workloads. Image updates replace replicas one at a time on the selected destination.
- **Moves:** Move an entire deployment or one replica to another active worker. The destination pulls the image, creates and starts a replacement, and passes a startup check before the source is removed.
- **Containers:** Inspect all Docker containers, including pre-existing workloads. Read the last 200 log lines. Start, stop, restart, or delete unmanaged containers. Use deployment controls for managed replicas.
- **Activity:** Inspect provisioning stages, results, and error details of the latest 100 cluster operations.
- **Monitoring:** Measure host and container usage, select workers and containers, and explore recorded charts.
- **Alerts:** Review sustained resource breaches and failures, acknowledge alerts, and configure cluster-wide thresholds.

A Docker image health check must report `healthy` within 90 seconds. Without a health check, the replacement must remain running through three probes over two seconds. Configure meaningful image health checks for application readiness.

Fixed host ports must be available on each destination. Multiple replicas with the same fixed host port cannot share a worker. Use host port `0` for Docker-assigned ports, or distribute replicas across hosts.

### Migration and recovery behavior

Moves recreate **managed stateless containers** from their recorded image and configuration. They do not copy container filesystem changes, volumes, process memory, or live network connections. Containers with mounts or image-defined volumes are rejected by the move/rollout operation. Pre-existing containers can be controlled, but must be deployed through Zela to use its migration controls.

Published endpoints change to the destination host. Configure your load balancer or DNS to route to the new host; Zela does not provide ingress, a cross-host overlay network, or traffic switching. A startup check does not prove zero downtime for client traffic.

Failures before cutover leave the source intact. If retiring the source fails, Zela attempts to restore it and remove the replacement. If source restoration also fails, the ready replacement is retained and both assignments remain recorded for inspection. A failed multi-replica move or rollout can have partially completed: inspect replicas and activity before retrying. The application never blindly retries an interrupted Docker mutation.

Drain disables scheduling and moves managed replicas to other hosts, overriding source-only placement. Mounts, stopped deployments, unavailable capacity, or unmanaged/leftover containers can prevent a complete drain. The host stays in `draining` mode until you resolve the issue and retry or activate it.

CPU/memory capacity graphs on Workers show reservations for managed workloads. The Monitoring page shows measured usage. Docker enforces configured limits on Zela-created containers; unmanaged containers consume additional resources. Inventory and metrics polling starts on a 10-second cycle, with up to eight hosts polled concurrently and five-second budgets for inventory and metrics requests. Large or unavailable clusters can take longer to complete a cycle. A worker is marked offline after three consecutive failed probes; it retains its last inventory and readings for inspection, and its managed deployments are marked degraded. After the worker has been unseen for 60 seconds, a **Recover** operation recreates those deployments' replicas on other eligible workers. When the worker returns, its old replicas are re-adopted if still needed, otherwise removed. Recovery recreates containers from their image and configuration; it does not move volumes or local data.

### Live monitoring, history, and alerts

Newly provisioned workers collect metrics automatically. After updating the control-plane binary and its Linux worker artifacts with `make build` or `make linux`, use **Monitoring → Update agent** for an existing SSH-managed worker. The update preserves the worker token, placement mode, existing service flags, existing healthy Docker installation, and running containers; it restarts the management agent. Follow the update in Activity. For manually installed TLS/HTTP agents, copy the rebuilt `zela-worker` binary to the host and restart its service. Direct Docker socket registrations remain manageable but need a worker agent endpoint to provide host metrics.

The agent collects every 10 seconds with an eight-second collection budget and at most eight concurrent Docker stats requests. The authenticated `/zela/metrics` endpoint returns the latest snapshot over the same SSH or TLS transport as management. The collection limit is 256 running containers per worker; truncation and failed readings are reported as partial collection. Stopped containers have no live usage samples.

| Metric                     | Meaning                                                                                                                |
| -------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| Host CPU                   | Utilization across all cores, calculated from Linux `/proc/stat` deltas                                                |
| Host memory                | Total RAM minus `MemAvailable`, including usage by unmanaged workloads                                                 |
| Disk capacity              | Used and available space on configured filesystems; repeated paths on the same device are deduplicated                 |
| Host network               | RX/TX rates across interfaces, excluding loopback, Docker bridges/veth interfaces, and enslaved interfaces             |
| Host disk I/O              | Read/write rates on physical block devices; partitions and virtual device layers are excluded to avoid double counting |
| Container CPU              | Docker CPU usage; 100% represents one core and values can exceed 100%                                                  |
| Container memory           | Working set with inactive file cache subtracted, plus the Docker-reported limit                                        |
| Container network/disk I/O | RX/TX and read/write rates derived from Docker's counters                                                              |

Container memory handling follows [Docker's stats guidance](https://docs.docker.com/reference/cli/docker/container/stats/). Host measurements use Linux interfaces described in the [kernel proc documentation](https://docs.kernel.org/filesystems/proc.html). Rate and CPU fields show `—` during the first sample or after a counter reset. Partial or stale readings are marked in the console; a metrics failure does not mark an otherwise reachable Docker host offline.

The worker monitors `/` and `/var/lib` by default. For Docker data on another filesystem, set `--metrics-disk-paths /,/your/data/path` in the worker's systemd command; paths must be accessible to its service user. Capacity monitoring covers configured filesystems, not per-container writable-layer sizes or individual Docker volumes. SELinux and firewall settings remain enabled as configured by the host.

History is saved in PostgreSQL when configured, or privately in `<data>/monitoring.json` with mode `0600`: one recorded snapshot per minute, host samples retained for 24 hours (up to 1,440 points per worker), and container samples retained for one hour. Select **1h**, **6h**, or **24h**; hover or focus chart markers to inspect values. Charts leave gaps when samples are missing. Historical samples for recently stopped containers remain selectable. These are minute snapshots, not a full record of every short-lived spike. Unregistering a worker removes its retained metrics.

Automatic alerts appear on the Alerts page and create notifications in the top-bar bell. Defaults:

| Condition                                         | Threshold / sustained duration |
| ------------------------------------------------- | ------------------------------ |
| Host CPU / RAM                                    | 90% for 60 seconds             |
| Configured filesystem                             | 85% used for 60 seconds        |
| Worker unreachable                                | 30 seconds                     |
| Unhealthy container / degraded running deployment | 30 seconds                     |
| Missing or partial metrics                        | 60 seconds                     |

Use **Alerts → Configure thresholds** to adjust percentages and durations. Zero seconds triggers at the next poll. Ongoing conditions produce one active alert each. Acknowledgement marks an alert as reviewed without clearing its condition. Observed recovery resolves it; recurrence creates a new alert. Missing measurements do not prove recovery for an active resource alert. Alerts and acknowledgements survive restarts, but pending durations restart after a manager outage. Resolved alerts are retained for up to seven days, limited to the latest 500. Persistence failures are displayed in the console.

### Notifications

The top-bar **notification bell** shows an unread count and a durable inbox. Notifications come from real cluster events: alert openings and recoveries, completed/failed console operations, and successful container create/start/stop/restart/rename/delete requests through the Docker gateway. Opening a notification takes you to its alert, operation or container. Mark individual notifications read or use **Mark all read**. Reading a notification does not acknowledge or resolve an alert. Read status is shared by administrators on this control plane.

Notifications are saved atomically with alert changes and operation delivery cursors in the monitoring document, using PostgreSQL or private JSON storage. They survive restarts, retain seven days of history, and are bounded to the latest 500 events. Ongoing conditions and repeated polling do not produce duplicate notifications; a new alert recurrence does. On upgrade, existing active alerts appear once and old completed operations are not replayed. Docker gateway notifications are recorded after successful responses and durable ownership updates; failed or forbidden requests do not announce successful deployments. Notification persistence errors appear as monitoring errors, and do not undo an already successful Docker action.

For desktop delivery, open the bell or **Settings & setup → Notifications → Enable**, then allow browser notifications. This preference applies to this browser. Notification permission is requested only after clicking Enable; the console never requests it on page load. Use HTTPS or localhost, and a browser supporting the Notification API. Blocked permission or an unsupported browser leaves the inbox available. **Disable** stops desktop delivery for this browser. Desktop notifications link back to their source; raw operation error output is kept in the authenticated Activity view.

Desktop delivery requires the console to remain open and signed in; it uses the existing state polling (2.5 seconds) and worker monitoring (10 seconds, plus configured alert durations). It does not implement background push, email or webhooks. Existing notification history is not replayed as desktop popups after sign-in/reload. Reconnect bursts show at most five desktop popups; every retained event remains in the inbox. Closing the console or signing out stops desktop delivery. Other open tabs may also receive events.

Administrator-only APIs: `GET /api/notifications` returns `{notifications, unread}`; `POST /api/notifications/read` with `{"ids":["ntf_..."]}` persists read status and returns the updated feed. `/api/state` includes `notifications` and `unreadNotifications`. Integration tokens cannot list or change these cluster-wide notifications.

State is durable in PostgreSQL or local files. Managed deployments fail over automatically from unreachable workers, and optional active/standby managers are described in the [HA guide](deploy/HA.md). Docker restart policies handle local container restarts. It does not implement Docker Swarm, private-registry credentials, shared storage or volume replication.

## Build, verify, and deploy

```sh
make build             # Native binaries plus both Linux worker artifacts
make workers           # Linux amd64/arm64 worker artifacts only
make test              # Go tests with race detector and frontend interaction tests
make check             # go vet and JavaScript syntax
make linux             # Linux amd64 and arm64 binaries in bin/
```

Run `./bin/zela-server --help` and `./bin/zela-worker --help` for flags. For remote console access, bind a private interface and configure TLS:

```sh
./bin/zela-server --listen 0.0.0.0:8080 --data /var/lib/zela --worker-binaries ./bin \
  --tls-cert /etc/zela/control.crt --tls-key /etc/zela/control.key
```

Example systemd units and an environment file are in [deploy/](deploy/). A container setup is provided:

```sh
export ZELA_POSTGRES_PASSWORD="$(openssl rand -hex 32)" # retain for subsequent starts
docker compose up --build -d
docker compose exec zela-server cat /data/admin-token
```

The console is bound to host loopback port 8080 by default. Register remote agents from inside the container; host loopback sockets are not mounted into it. The private CA can be mounted and set through `SSL_CERT_FILE` if needed.

When PostgreSQL is not configured, state is atomically saved to `<data>/cluster.json`, with mode `0600`, and includes private worker credentials and deployment environment values. Keep that directory private and back it up securely. An advisory lock prevents two instances from sharing a state directory. Restarted in-progress jobs are marked failed for operator inspection. Use separate directories for demo and live state.

## API examples

```sh
# ZELA_ADMIN_TOKEN should already be set securely in your shell.
curl -H "Authorization: Bearer $ZELA_ADMIN_TOKEN" http://127.0.0.1:8080/api/state

curl -H "Authorization: Bearer $ZELA_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"web","image":"nginx:alpine","replicas":2,"cpus":0.5,"memoryMB":256,"ports":[]}' \
  http://127.0.0.1:8080/api/deployments

curl -H "Authorization: Bearer $ZELA_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"action":"move","target":"wrk_DESTINATION"}' \
  http://127.0.0.1:8080/api/deployments/dep_ID/actions
```

Cluster jobs return `202` with an operation ID. Poll `/api/state` to see completion and provisioning stages. Only one mutation runs at a time; additional cluster jobs return `409` until it finishes. Secret creation returns `201` with metadata and deletion returns `200`. Worker credentials and vault private keys are never included in API responses.

| Route                                                      | Purpose                                                                                                 |
| ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| `GET /api/ready`                                         | Leader readiness for load-balancer checks (`503` on HA standby)                                           |
| `GET /api/health`                                          | Public process health                                                                                   |
| `POST /api/login`, `POST /api/logout`                      | Browser session                                                                                         |
| `GET /api/state`                                           | Sanitized cluster inventory and activity                                                                |
| `POST /api/refresh`                                        | Refresh worker inventory                                                                                |
| `POST /api/workers`                                        | Register `{name, endpoint, token, region}`                                                              |
| `POST /api/secrets`                                        | Import `{name, privateKey, passphrase?}` into the encrypted vault                                       |
| `POST /api/secrets/{id}/delete`                            | Delete an unused SSH key                                                                                |
| `POST /api/workers/provision`                              | Provision `{name, region, ssh: {host, port, user, secretID, hostKey, sudo, agentPort}}`                 |
| `POST /api/workers/{id}/provision-retry`                   | Retry failed provisioning with the original configuration                                               |
| `POST /api/workers/{id}/agent-update`                      | Update an SSH-managed worker agent and verify metrics                                                   |
| `GET /api/workers/{id}/metrics/history?window=1h\|6h\|24h` | Recorded host and container metrics                                                                     |
| `POST /api/monitoring/rules`                               | Set `{cpuPercent, memoryPercent, diskPercent, resourceForSeconds, offlineForSeconds, healthForSeconds}` |
| `POST /api/alerts/{id}/acknowledge`                        | Acknowledge an alert without clearing its condition                                                     |
| `POST /api/workers/{id}/actions`                           | `{action: activate\|pause\|drain\|delete}`                                                              |
| `POST /api/deployments`                                    | Create a workload                                                                                       |
| `POST /api/deployments/{id}/actions`                       | `{action: start\|stop\|restart\|scale\|move\|delete, replicas?, target?}`                               |
| `POST /api/deployments/{id}/image`                         | Roll out `{image, target}`                                                                              |
| `POST /api/deployments/{id}/replicas/{containerID}/move`   | Move one replica with `{target}`                                                                        |
| `POST /api/workers/{worker}/containers/{id}/actions`       | Unmanaged container lifecycle                                                                           |
| `GET /api/workers/{worker}/containers/{id}/logs`           | Container logs                                                                                          |

## Acknowledgements

Zela powers the infrastructure layer of NEXUS AI, where it was first developed.
