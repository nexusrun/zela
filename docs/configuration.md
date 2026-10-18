# Configuration reference

[All guides](README.md) · [Installation](installation.md) · [Glossary](glossary.md)

Use this page to look up a specific setting. You do not need to configure every option to get started.

| Term in this reference | Meaning                                                                      | Example                            |
| ---------------------- | ---------------------------------------------------------------------------- | ---------------------------------- |
| Flag                   | An option added when starting a program                                      | `--demo`                           |
| Environment variable   | A named value supplied to the program, often in a protected environment file | `ZELA_DATABASE_URL`                |
| Default                | The value used when you do not specify one                                   | Dashboard port `8080`              |
| Data directory         | The folder containing saved configuration and local keys                     | `./data` for a native live manager |

**Where to make changes:** use the dashboard for day-to-day deployment, alert, and supported network settings. Startup flags and environment changes need a manager restart. With Compose, use your `.env` and the service configuration; with systemd, use the protected environment file or a service override. Preserve credentials across restarts.

**Network settings:** console-managed mesh settings survive restarts. Explicit networking flags or nonempty networking environment values override saved console settings at startup. Omitting all those options preserves console-managed settings; an explicitly empty `--cluster-network` disables the mesh. Coordinate changes across every HA manager.

The tables below list configurable options and built-in limits.

- [Control plane flags](#control-plane-flags)
- [Control plane environment](#control-plane-environment)
- [Data directory](#data-directory)
- [Worker agent](#worker-agent)
- [zela](#zela)
- [Alert rules](#alert-rules)
- [Built-in timings and limits](#built-in-timings-and-limits)
- [Test-only variables](#test-only-variables)

## Control plane flags

`zela-server [flags]`. Run `zela-server --help` for the authoritative list for your build.

| Flag                      | Default                                               | Description                                                                                                                                                                                                                                                                                        |
| ------------------------- | ----------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `--listen`                | `127.0.0.1:8080`                                      | HTTP(S) listen address. Use `0.0.0.0:8080` (with TLS) for remote access                                                                                                                                                                                                                            |
| `--data`                  | `./data` (`./data-demo` with `--demo`)                | Private state directory. Must be writable and not shared between processes                                                                                                                                                                                                                         |
| `--tls-cert`, `--tls-key` | none                                                  | PEM certificate and key. When set, the server speaks HTTPS only                                                                                                                                                                                                                                    |
| `--worker-binaries`       | `./bin`                                               | Directory containing `zela-worker-linux-amd64` and `zela-worker-linux-arm64`, uploaded during SSH provisioning and agent updates                                                                                                                                                                   |
| `--allow-http-workers`    | `false`                                               | Permit registering `http://` worker agents. Only for trusted private networks                                                                                                                                                                                                                      |
| `--demo`                  | `false`                                               | Simulated Docker hosts. Never touches real Docker. Not compatible with `--ha`                                                                                                                                                                                                                      |
| `--ha`                    | `false`                                               | Active/standby mode. Requires PostgreSQL, a shared `ZELA_ADMIN_TOKEN` and a shared `ZELA_VAULT_KEY`                                                                                                                                                                                                |
| `--manager-id`            | `$ZELA_MANAGER_ID`, else hostname                     | Unique name of this manager in an HA group: 1–63 letters, digits, `.`, `_` or `-`                                                                                                                                                                                                                  |
| `--ha-takeover-delay`     | `10s`                                                 | Settling time after acquiring leadership before serving. Range 5s–1m                                                                                                                                                                                                                               |
| `--cluster-network`       | `$ZELA_CLUSTER_NETWORK`                               | Enable the encrypted [cluster network](networking.md#cluster-network) on this private IPv4 `/16`, such as `10.200.0.0/16`. An explicitly empty flag disables it; console-managed settings survive when networking startup options are omitted. Use consistent startup settings on every HA manager |
| `--cluster-network-port`  | `51820`                                               | WireGuard UDP port on workers                                                                                                                                                                                                                                                                      |
| `--edge-http-port`        | `$ZELA_EDGE_HTTP_PORT`, else `0`                      | [Edge proxy](networking.md#edge-proxy) HTTP port on every worker (requires the cluster network; `0` disables)                                                                                                                                                                                      |
| `--edge-https-port`       | `$ZELA_EDGE_HTTPS_PORT`, else `0`                     | Edge HTTPS port. Certificates come from ACME or from a worker's local `--edge-tls-cert`                                                                                                                                                                                                            |
| `--edge-redirect-https`   | `$ZELA_EDGE_REDIRECT_HTTPS=true`, else off            | Redirect edge HTTP to HTTPS (`308`) for hosts with a certificate                                                                                                                                                                                                                                   |
| `--acme-email`            | `$ZELA_ACME_EMAIL`                                    | Enable [automatic certificates](networking.md#automatic-certificates-acme) with this ACME account email. Requires `--edge-http-port 80` and an HTTPS port                                                                                                                                          |
| `--acme-directory`        | `$ZELA_ACME_DIRECTORY`, else Let's Encrypt production | ACME directory URL, for example Let's Encrypt staging                                                                                                                                                                                                                                              |

## Control plane environment

| Variable                         | Required                    | Description                                                                                                                                                                                                                                                                                                                           |
| -------------------------------- | --------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ZELA_ADMIN_TOKEN`               | HA: yes. Otherwise optional | Administrator bearer token, at least 32 characters. If unset (non-HA), one is generated in `<data>/admin-token`                                                                                                                                                                                                                       |
| `ZELA_DATABASE_URL`              | HA: yes. Otherwise optional | PostgreSQL connection URL. Without it, state is stored as JSON in `--data`. Use `sslmode=verify-full` remotely and keep the password out of the URL                                                                                                                                                                                   |
| `PGPASSWORD` / `PGPASSFILE`      | With PostgreSQL             | Standard libpq password sources, read by the pgx driver                                                                                                                                                                                                                                                                               |
| `ZELA_VAULT_KEY`                 | HA: yes. Otherwise optional | Base64-encoded 32-byte vault master key (`openssl rand -base64 32`). Must be set **before the first start** and never changed afterwards. If unset (non-HA), one is generated in `<data>/vault-key`                                                                                                                                   |
| `ZELA_MANAGER_ID`                | No                          | Default for `--manager-id`                                                                                                                                                                                                                                                                                                            |
| `ZELA_CLUSTER_NETWORK`           | No                          | Default for `--cluster-network`                                                                                                                                                                                                                                                                                                       |
| `ZELA_MCP_PUBLIC_URL`            | No                          | External origin, such as `https://cluster.example.com` (no `/mcp`, path, query or credentials), that enables MCP **OAuth** for remote AI clients. Must be HTTPS; HTTP is allowed only for loopback. Bearer-token MCP works without it. Set identically on all HA managers                                                             |
| `ZELA_MCP_PROVISION_HOSTS`       | No                          | Comma-separated IPs, CIDRs or exact hostnames that **MCP credentials** may provision. Empty (the default) means only administrators can provision workers. See [MCP security](mcp.md#security-considerations)                                                                                                                         |
| `ZELA_MCP_TRUSTED_PROXIES`       | No                          | Comma-separated proxy IPs/CIDRs. Requests from them use the nearest untrusted `X-Forwarded-For` hop as the client address for OAuth rate limiting                                                                                                                                                                                     |
| `ZELA_MCP_REDIRECT_URIS`         | No                          | Comma-separated exact OAuth callback allowlist. Requires `ZELA_MCP_PUBLIC_URL`. Default: `https://chatgpt.com/connector_platform_oauth_redirect`, `https://claude.ai/api/mcp/auth_callback`, `https://claude.com/api/mcp/auth_callback`. An explicit list replaces the defaults; no wildcards. Entries must be HTTPS or HTTP loopback |
| `SSL_CERT_FILE` / `SSL_CERT_DIR` | No                          | Extra trusted CAs for verifying worker agent certificates (Go standard behavior)                                                                                                                                                                                                                                                      |

Generate secrets with:

```sh
openssl rand -hex 32      # admin token, worker token, database password
openssl rand -base64 32   # vault key
```

## Data directory

`--data` is created with private permissions and locked with `<data>/.lock`, so two processes can't share it.

| Path              | Present when             | Contents                                                                         |
| ----------------- | ------------------------ | -------------------------------------------------------------------------------- |
| `admin-token`     | `ZELA_ADMIN_TOKEN` unset | Generated administrator token, mode `0600`                                       |
| `vault-key`       | `ZELA_VAULT_KEY` unset   | Vault master key, mode `0600`. **Losing it makes stored SSH keys unrecoverable** |
| `cluster.json`    | JSON storage             | Workers (including agent tokens), deployments, operations, API token hashes      |
| `vault.json`      | JSON storage             | AES-256-GCM-encrypted SSH keys and passphrases                                   |
| `monitoring.json` | JSON storage             | Alert rules, alerts, notifications, 24h metrics history                          |
| `ssh-tmp/`        | Provisioning             | Short-lived `0600` SSH identity files, deleted after each connection             |
| `.lock`           | Always                   | Advisory lock file                                                               |

With PostgreSQL, the three JSON documents live in the `zela.state` table instead. Leftover JSON files are ignored once the database is initialized.

## Worker agent

`zela-worker [flags]`, normally run by systemd.

| Flag / variable                     | Default                | Description                                                                                                       |
| ----------------------------------- | ---------------------- | ----------------------------------------------------------------------------------------------------------------- |
| `ZELA_WORKER_TOKEN` (env, required) | none                   | Bearer token managers must present, at least 32 characters                                                        |
| `--listen`                          | `127.0.0.1:9091`       | Listen address. A non-loopback address requires TLS or `--allow-http`                                             |
| `--tls-cert`, `--tls-key`           | none                   | PEM certificate and key for HTTPS                                                                                 |
| `--allow-http`                      | `false`                | Allow plaintext on a non-loopback address (trusted private networks only)                                         |
| `--socket`                          | `/var/run/docker.sock` | Docker Engine Unix socket                                                                                         |
| `--edge-tls-cert`, `--edge-tls-key` | none                   | Local PEM certificate and key for the edge proxy's HTTPS listener; a wildcard is fine                             |
| `--state-dir`                       | `/var/lib/zela-worker` | Private directory for the cluster-network WireGuard key (`wireguard.key`) and last configuration (`network.json`) |
| `--metrics-disk-paths`              | `/,/var/lib`           | Comma-separated absolute paths whose filesystems are reported. Add your Docker data root if it is elsewhere       |

SSH-provisioned workers use `/etc/zela/worker.env` (mode `0640`, `root:docker`) and `zela-worker.service`, listening on `127.0.0.1:<agent-port>` (default 9091). Change flags with `systemctl edit zela-worker`. **Monitoring → Update agent** preserves existing service flags.

In HA clusters, the agent rejects requests carrying a leadership epoch lower than the highest it has seen (HTTP `409`). See [HA fencing](../deploy/HA.md#worker-fencing).

## zela

| Setting         | Flag             | Environment       | Notes                                                                                                                 |
| --------------- | ---------------- | ----------------- | --------------------------------------------------------------------------------------------------------------------- |
| Config file     | `--config`       | `ZELA_CONFIG`     | Default `~/.config/zela/config.json` (Linux) or `~/Library/Application Support/zela/config.json` (macOS), mode `0600` |
| Profile         | `--profile`      | `ZELA_PROFILE`    | Falls back to the saved current profile                                                                               |
| Endpoint        | `--url`          | `ZELA_URL`        | Origin only, such as `https://cluster.example:8080`                                                                   |
| Token file      | `--token-file`   | `ZELA_TOKEN_FILE` | File mode must be `0600`; `-` reads stdin                                                                             |
| Token value     | none             | `ZELA_TOKEN`      | For CI secret injection. Never saved by `config set`                                                                  |
| CA bundle       | `--ca-cert`      | `ZELA_CA_CERT`    | Extra trusted CA for a private PKI                                                                                    |
| Request timeout | `--timeout`      | none              | Default `30s`                                                                                                         |
| Wait timeout    | `--wait-timeout` | none              | Default `35m`, used by `--wait`                                                                                       |
| Output          | `--json`         | none              | Machine-readable output                                                                                               |

`zela mcp serve [--read-only]` runs an MCP server over stdio using the same connection settings. stdout carries protocol frames and diagnostics go to stderr, so `--token-file -` is rejected there. Create the token with `zela tokens create --scope mcp_read|mcp_manage`.

Precedence: flags, then environment, then profile. Tokens are only sent over HTTPS, or plain HTTP to loopback. See the [CLI guide](cli.md).

## Deployment ports

Each entry in a deployment's `ports` has these fields:

| Field       | Description                                            |
| ----------- | ------------------------------------------------------ |
| `container` | Container port, 1–65535                                |
| `host`      | Host port; `0` lets Docker choose                      |
| `protocol`  | `tcp` (default) or `udp`                               |
| `hostIP`    | Optional host address to bind. Default: all interfaces |

Deployments also accept `routes` for the [edge proxy](networking.md#publish-a-deployment): `[{"host": "shop.example.com", "path": "/", "port": 8080}]`. Replace them later with `PUT /api/deployments/{id}/routes`.

Workers also accept an optional `networkEndpoint` (`zela … --network-endpoint`), the address cluster-network peers use instead of the SSH or agent host.

## Alert rules

Cluster-wide, set in **Alerts → Configure thresholds**, `zela monitoring set-rules --file rules.json`, or `POST /api/monitoring/rules`:

```json
{
  "cpuPercent": 90,
  "memoryPercent": 90,
  "diskPercent": 85,
  "resourceForSeconds": 60,
  "offlineForSeconds": 30,
  "healthForSeconds": 30
}
```

| Rule                           | Default | Meaning                                                |
| ------------------------------ | ------- | ------------------------------------------------------ |
| `cpuPercent` / `memoryPercent` | 90      | Host usage threshold                                   |
| `diskPercent`                  | 85      | Usage of each monitored filesystem                     |
| `resourceForSeconds`           | 60      | How long CPU, memory or disk must stay above threshold |
| `offlineForSeconds`            | 30      | Counted from the worker's **last successful probe**    |
| `healthForSeconds`             | 30      | Unhealthy container, or degraded deployment            |

Fixed alerts:

| Alert                 | Severity | Raised when                                                                             |
| --------------------- | -------- | --------------------------------------------------------------------------------------- |
| `metrics-unavailable` | warning  | Metrics missing or stale for 60s                                                        |
| `metrics-partial`     | warning  | Some container metrics failed or were truncated (more than 256 containers) for 60s      |
| `inventory-stale`     | warning  | An online worker hasn't been refreshed for 45s, which usually means the poller is stuck |

## Built-in timings and limits

These are compiled-in defaults, listed so you can reason about behavior. They are not configurable at runtime.

| Behavior                                       | Value                                                                                                                                     |
| ---------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| Inventory poll interval                        | 10s, up to 8 workers in parallel                                                                                                          |
| Probe budget per worker                        | 5s inventory + 5s metrics                                                                                                                 |
| Worker marked `offline`                        | After 3 consecutive failed probes                                                                                                         |
| Inventory considered stale                     | 45s since last success (worker becomes unschedulable)                                                                                     |
| Automatic replica failover                     | Worker unseen for 60s                                                                                                                     |
| Cluster-network push                           | Each inventory cycle, only when a worker's desired config changed; the agent re-applies locally every 30s                                 |
| Cluster-network size                           | 254 workers, one `/24` of container addresses each; MTU 1420                                                                              |
| Edge proxy                                     | Header read timeout 10s, idle 120s, no write timeout (streams); upstream connect 5s, response headers 60s                                 |
| ACME                                           | HTTP-01; renew 30 days before expiry; retry after 1, 3, 9, 27 min, then hourly; checked every 30s                                         |
| Network status refresh                         | Each worker's config is re-pushed at least every 5 min                                                                                    |
| Service VIPs                                   | One per deployment from `x.y.255.0/24` (254 max); random per-connection balancing; backends refresh each inventory cycle                  |
| Cluster DNS                                    | `<deployment>.zela` on each worker's bridge gateway, port 53; TTL 5s; at most 64 addresses per name; records refresh each inventory cycle |
| Replica readiness check                        | Healthy within 90s, or running for 3 probes over 2s without a health check                                                                |
| Default container limits (automatic placement) | 128 MB memory, 0.25 CPU                                                                                                                   |
| Operation timeout                              | 10 min; provisioning and agent updates 30 min                                                                                             |
| Concurrent mutations                           | 1 (others get `409`)                                                                                                                      |
| Operation history kept                         | Latest 100                                                                                                                                |
| Metrics history                                | 1 point/min; host 24h (1,440 points), containers 1h                                                                                       |
| Alerts and notifications kept                  | 7 days, latest 500                                                                                                                        |
| Browser session                                | 12h                                                                                                                                       |
| HA leader check                                | Every 2s, 3s timeout; readiness lost after 5s without a successful check                                                                  |
| HTTP server                                    | 5s header read, 30s body read, 60s write (Docker streams are exempt)                                                                      |
| MCP request body                               | 1 MiB; tool responses are capped at 32 MiB                                                                                                |
| MCP OAuth access token                         | 1h                                                                                                                                        |
| MCP OAuth refresh token                        | Rotating; absolute 30-day lifetime; reuse revokes the connection                                                                          |
| MCP OAuth authorization code                   | 2 min, single use, PKCE S256 required                                                                                                     |
| MCP OAuth consent request                      | 10 min                                                                                                                                    |
| MCP OAuth rate limit                           | 20 requests/min per client IP (see `ZELA_MCP_TRUSTED_PROXIES`) for register, authorize and token, tracking at most 4,096 IPs              |
| MCP OAuth registered clients                   | At most 512. Never-authorized clients are pruned after 24h, idle ones after 31 days                                                       |
| Expired MCP OAuth token entries                | Pruned 24h after expiry or revocation, once no refresh grant remains                                                                      |

## Test-only variables

These are used by optional integration tests; production never reads them. See [CONTRIBUTING](../CONTRIBUTING.md#tests).

| Variable                                                                           | Used by                                                      |
| ---------------------------------------------------------------------------------- | ------------------------------------------------------------ |
| `ZELA_TEST_DATABASE_URL`                                                           | PostgreSQL and HA integration tests (login needs `CREATEDB`) |
| `ZELA_TEST_DOCKER_SOCKET`, `ZELA_TEST_DOCKER_IMAGE`                                | `make test-docker-live`                                      |
| `ZELA_TEST_CLUSTER_URL`, `ZELA_TEST_ADMIN_TOKEN_FILE`, `ZELA_TEST_WORKER_ID`       | `make test-cluster-live`                                     |
| `ZELA_TEST_MANAGER_BINARY`, `ZELA_TEST_HAPROXY_BINARY`, `ZELA_TEST_HAPROXY_CONFIG` | HA process test                                              |
| `ZELA_TEST_CLI_BINARY`                                                             | CLI process test                                             |
