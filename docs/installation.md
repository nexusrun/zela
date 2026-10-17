# Installation

This guide installs a production single-manager cluster. For several managers with failover, finish this guide first and then follow [High availability](../deploy/HA.md).

1. [Prerequisites](#1-prerequisites)
2. [Build the binaries](#2-build-the-binaries)
3. [Prepare PostgreSQL](#3-prepare-postgresql) (recommended)
4. [Install the control plane](#4-install-the-control-plane): systemd or container
5. [Connect workers](#5-connect-workers)
6. [Install the CLI](#6-install-the-cli)
7. [Verify the installation](#7-verify-the-installation)
8. [Uninstall](#8-uninstall)

---

## 1. Prerequisites

**Manager host**

- Linux (amd64/arm64) with systemd, or any host that runs containers.
- OpenSSH client (`ssh`, `ssh-keygen`) for automatic worker provisioning:
  - RHEL/Rocky/AlmaLinux: `sudo dnf install openssh-clients`
  - Ubuntu/Debian: `sudo apt-get install openssh-client`
  - The container image already includes it.
- A TLS certificate and key for the console/API if it is reachable beyond localhost.
- Network access to each worker's SSH port (provisioned workers) or agent port 9091 (manual agents), and to PostgreSQL.

**Build host** (can be the manager)

- Go **1.25 or newer**, and `make`.
- Node.js, only to run the frontend tests (`make test`).

**Workers.** See [supported platforms](README.md#supported-platforms). Automatically provisioned hosts need:

- systemd, and an amd64 or arm64 CPU;
- outbound access to their OS package repositories and `download.docker.com`;
- an SSH login that is root or has **passwordless sudo**, with your public key in `authorized_keys`;
- `sshd` permitting TCP forwarding to `127.0.0.1:<agent-port>` (`AllowTcpForwarding yes` or `local`).

RHEL hosts need working BaseOS/AppStream repositories, through a subscription or your cloud provider. Provisioning does not register subscriptions.

## 2. Build the binaries

```sh
git clone <repository-url> zela && cd zela
make build      # native control-plane, zela-worker, zela + Linux worker artifacts
make linux      # Linux amd64 and arm64 builds of everything, in bin/
```

| Artifact | Purpose |
| --- | --- |
| `bin/zela-server-linux-{amd64,arm64}` | Manager |
| `bin/zela-worker-linux-{amd64,arm64}` | Worker agent. The manager also uploads these during SSH provisioning |
| `bin/zela-linux-{amd64,arm64}` | CLI |

All binaries are static (`CGO_ENABLED=0`) with no runtime dependencies. To build a single binary manually:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/zela-worker-linux-arm64 ./cmd/worker
```

Run `make check test` before deploying a build you produced yourself.

## 3. Prepare PostgreSQL

Without `ZELA_DATABASE_URL`, the manager stores state as JSON files in `--data`. PostgreSQL is recommended for anything you care about, and it is **required** for HA.

Create a login and a database it owns. The manager creates the `zela` schema, its tables and all migrations automatically on start; there is no separate migration step.

```sql
CREATE ROLE zela LOGIN PASSWORD '<random password>';
CREATE DATABASE zela OWNER zela;
```

Connection requirements:

- **Session-level connections only.** Use a direct connection or a pooler in *session* mode. Transaction pooling, such as PgBouncer `pool_mode=transaction`, breaks the advisory lock that guarantees a single writer.
- **TLS.** Use `sslmode=verify-full` with a trusted CA for any remote database.
- **Password.** Supply it through `PGPASSWORD` or a pgpass file, not inside the URL. Connection strings never appear in logs or API responses.

```sh
ZELA_DATABASE_URL='postgres://zela@db.example.internal:5432/zela?sslmode=verify-full'
```

**Importing an existing JSON installation.** On the first start against an *empty* database, existing `cluster.json`, `vault.json` and `monitoring.json` in `--data` are validated and imported in one transaction. The files remain as a backup. Keep the original vault key: secrets stay encrypted with it.

## 4. Install the control plane

### Option A: systemd (recommended for production)

```sh
# Service user and directories
sudo useradd --system --no-create-home zela
sudo install -m 755 bin/zela-server-linux-amd64 /usr/local/bin/zela-server
sudo install -d -m 755 /opt/zela/workers
sudo install -m 755 bin/zela-worker-linux-amd64 bin/zela-worker-linux-arm64 /opt/zela/workers/
sudo install -d -m 750 -o root -g zela /etc/zela
sudo install -m 644 deploy/zela-server.service /etc/systemd/system/
```

Put the TLS certificate and key in `/etc/zela/control.crt` and `/etc/zela/control.key`.

Create `/etc/zela/zela-server.env`. Every variable is described in the [configuration reference](configuration.md#control-plane-environment):

```ini
# Leave ZELA_ADMIN_TOKEN unset to generate one in /var/lib/zela/admin-token.
ZELA_ADMIN_TOKEN=<output of: openssl rand -hex 32>
ZELA_DATABASE_URL=postgres://zela@db.example.internal:5432/zela?sslmode=verify-full
PGPASSWORD=<database password>
# Optional: keep the vault master key outside the data directory. Set before the FIRST start.
ZELA_VAULT_KEY=<output of: openssl rand -base64 32>
```

Lock down permissions, restore SELinux contexts where applicable, and start the service:

```sh
sudo chown root:zela /etc/zela/zela-server.env /etc/zela/control.key
sudo chmod 0640 /etc/zela/zela-server.env /etc/zela/control.key
sudo restorecon -RF /usr/local/bin/zela-server /opt/zela /etc/zela /etc/systemd/system/zela-server.service  # SELinux only
sudo systemctl daemon-reload
sudo systemctl enable --now zela-server
journalctl -u zela-server -f
```

The supplied unit listens on `0.0.0.0:8080` with TLS, keeps state in `/var/lib/zela` (mode `0700`) and runs with `ProtectSystem=strict`, `NoNewPrivileges` and `UMask=0077`. Edit `ExecStart` in a drop-in to change flags:

```sh
sudo systemctl edit zela-server
```

If worker agents use certificates from a private CA, add that CA to the manager's system trust store, or set `SSL_CERT_FILE` in the environment file.

### Option B: container image

```sh
docker build -t zela .
docker run -d --name zela --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -v zela-data:/data \
  -e ZELA_DATABASE_URL='postgres://zela@db.example.internal:5432/zela?sslmode=verify-full' \
  -e PGPASSWORD \
  --cap-drop ALL --security-opt no-new-privileges:true \
  zela
docker exec zela cat /data/admin-token
```

About the image:

- Alpine-based, runs as UID 10001, with state in `/data`.
- Includes OpenSSH and both worker architectures in `/opt/zela/workers`.
- Default command: `--listen 0.0.0.0:8080 --data /data --worker-binaries /opt/zela/workers`.
- To serve TLS, mount a certificate and key and append `--tls-cert`/`--tls-key`, or terminate TLS on a reverse proxy (see [TLS](security.md#tls)).

`compose.yaml` runs the same image together with PostgreSQL 18 for a self-contained installation; see [Getting started](getting-started.md#2-local-single-manager-with-docker-compose). Its database traffic stays on the private Compose network without TLS.

> A containerized manager cannot reach the host's loopback or Docker socket. Register workers by their network address, not `unix://` or `127.0.0.1`.

## 5. Connect workers

Choose one method per host.

### Method 1: automatic SSH provisioning (recommended)

The manager installs Docker from Docker's official signed repository, uploads `zela-worker`, configures `zela-worker.service` on worker loopback and verifies it. No agent port is exposed; all traffic goes through a host-key-verified SSH tunnel.

1. **Store the SSH key.** Open **Secrets vault → Add SSH key** and paste or import the private key (and its passphrase if it has one). Or from the CLI:
   ```sh
   zela secrets create --name prod-workers --key-file ~/.ssh/id_ed25519
   ```
2. **Get the host's public key from a trusted source**, such as the cloud console or a known_hosts entry you have already verified:
   ```sh
   # on the worker
   cat /etc/ssh/ssh_host_ed25519_key.pub
   ```
3. **Provision.** Open **Workers → Provision worker**, or:
   ```sh
   zela workers provision --name worker-1 --region eu-1 \
     --host 10.0.0.21 --user deploy --sudo \
     --secret <secret-id> --host-key-file ./worker-1.hostkey.pub --wait
   ```
4. Follow the stages in **Activity**. The worker becomes `active` once Docker inventory is reachable.

Existing healthy Docker installations are reused, not upgraded. Conflicting runtimes, such as Podman, `podman-docker`, old Docker packages, or standalone `containerd`/`runc`, are reported, not removed. Resolve them and select **Retry provisioning**. On RPM hosts, SELinux stays enabled and Docker's SELinux integration is turned on for fresh installs. Firewall rules are not changed.

### Method 2: manual TLS agent

On the worker:

```sh
sudo useradd --system --no-create-home zela-worker
sudo install -m 755 zela-worker-linux-amd64 /usr/local/bin/zela-worker
sudo install -d -m 750 -o root -g docker /etc/zela
echo "ZELA_WORKER_TOKEN=$(openssl rand -hex 32)" | sudo tee /etc/zela/worker.env >/dev/null
# Install worker.crt / worker.key (SAN must match the address managers will use)
sudo chown root:docker /etc/zela/worker.env /etc/zela/worker.key
sudo chmod 0640 /etc/zela/worker.env /etc/zela/worker.key
sudo install -m 644 deploy/worker.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now worker
```

Allow port 9091 **only from manager addresses**. Then register the worker with its HTTPS endpoint and token, in **Workers → Connect worker** or:

```sh
sudo cat /etc/zela/worker.env   # copy the token into a private file, mode 0600
zela workers add --name worker-2 --endpoint https://worker-2.example.internal:9091 \
  --worker-token-file ./worker-2.token --wait
```

On an isolated, trusted network you may run without TLS. Both sides must opt in: start the worker with `--allow-http` and the manager with `--allow-http-workers`.

### Method 3: local Docker socket

For a single-host setup, register `unix:///var/run/docker.sock`. No agent and no token are needed. The manager's service user must be able to access the socket. This method is not available with HA, and local sockets don't provide host metrics.

## 6. Install the CLI

```sh
sudo install -m 755 bin/zela-linux-amd64 /usr/local/bin/zela
zela login --profile prod --url https://cluster.example.internal:8080 --token-file /secure/admin-token
```

Tokens are only sent over HTTPS, or plain HTTP to `localhost`/loopback. See the [CLI guide](cli.md).

### Optional: enable the cluster network

To let containers on different workers talk to each other directly over an encrypted mesh, add `--cluster-network 10.200.0.0/16` (or `ZELA_CLUSTER_NETWORK`) to every manager, open UDP 51820 between workers, and provision or update agents. Requirements and setup are in [Networking → Cluster network](networking.md#cluster-network). To publish HTTP(S) services through every worker, also set `--edge-http-port` / `--edge-https-port`; see [Edge proxy](networking.md#edge-proxy).

### Optional: enable AI clients (MCP)

The `/mcp` endpoint is always available to bearer tokens with an `mcp_read` or `mcp_manage` scope. To let ChatGPT or Claude connect through browser OAuth, set the external HTTPS origin before starting the manager, on every HA manager:

```ini
# /etc/zela/zela-server.env
ZELA_MCP_PUBLIC_URL=https://cluster.example.com
# Optional; replaces the default ChatGPT/Claude callbacks
# ZELA_MCP_REDIRECT_URIS=https://chatgpt.com/connector_platform_oauth_redirect,https://claude.ai/api/mcp/auth_callback
```

Your reverse proxy must forward `/mcp`, `/oauth/*`, `/.well-known/*` and `/oauth.css`. See [AI cluster management](mcp.md) for connecting clients, and read its [security considerations](mcp.md#security-considerations) before issuing `mcp_manage`.

## 7. Verify the installation

```sh
curl -fsS https://cluster.example.internal:8080/api/health   # 200: process is alive
curl -fsS https://cluster.example.internal:8080/api/ready    # 200: ready to serve (503 on an HA standby)
zela status
zela workers list                                         # every worker should be "online"
```

Then deploy a test container ([Getting started → first deployment](getting-started.md#your-first-deployment)), or run the live end-to-end check:

```sh
ZELA_TEST_CLUSTER_URL=https://cluster.example.internal:8080 \
ZELA_TEST_ADMIN_TOKEN_FILE=/secure/admin-token \
ZELA_TEST_DOCKER_IMAGE=alpine:latest \
make test-cluster-live
```

## 8. Uninstall

Unregistering a worker removes it from the cluster only. It does **not** uninstall Docker, the agent or running containers. On each worker:

```sh
sudo systemctl disable --now zela-worker   # or "worker" for manual installs
sudo rm -f /usr/local/bin/zela-worker /etc/systemd/system/zela-worker.service /etc/systemd/system/worker.service
sudo rm -rf /etc/zela
```

On the manager:

```sh
sudo systemctl disable --now zela-server
sudo rm -rf /usr/local/bin/zela-server /opt/zela /etc/systemd/system/zela-server.service*
# Removing state is irreversible. Back it up first:
sudo rm -rf /var/lib/zela /etc/zela
```

Then drop the database, or just the `zela` schema, if you no longer need it.
