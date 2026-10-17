# Active/standby control-plane managers

Run two or more managers against one authoritative PostgreSQL database and put a readiness-aware load balancer in front of them. Zela continues using one cluster endpoint and its infrastructure token. Zela still owns application scheduling, recovery and routing; manager failover does not move application containers.

## Coordination and failover

Start each manager with `--ha` and a unique `--manager-id` (or `ZELA_MANAGER_ID`; hostname is the fallback). One dedicated PostgreSQL session acquires the existing exclusive cluster advisory lock. Standbys keep their listener online, retry acquisition every two seconds, and never load or serve stale cluster state. Only the leader polls workers, changes state, provisions hosts or handles Docker traffic. PostgreSQL releases session locks when the owning session ends; see its [advisory-lock documentation](https://www.postgresql.org/docs/18/functions-admin.html#FUNCTIONS-ADVISORY-LOCKS).

The leader checks its session every two seconds with a three-second timeout. Requests and worker transports also verify that this session still owns the lock on a writable primary. The check uses PostgreSQL's documented [pg_locks key representation](https://www.postgresql.org/docs/18/view-pg-locks.html). A missing lock, read-only session, failed query or failed state write fences the leader immediately. A check that cannot run because a slow state write holds the session is tolerated: readiness and worker requests are withheld meanwhile, and the leader fences itself if no check succeeds within the readiness window (heartbeat interval plus timeout, five seconds by default). Fencing withdraws readiness, rejects new work, and cancels requests, streams and jobs. An old session never reconnects to resume writes. Graceful shutdown drains work before releasing the lock.

After acquiring leadership, the new manager reloads workers, hashed tokens, reservations, ownership, vault records, monitoring and notifications. It waits ten seconds by default (`--ha-takeover-delay`, range 5s–1m), then refreshes inventory before becoming ready. This settling interval gives previous requests time to terminate. Hard fencing against a stalled former leader is provided by [worker fencing](#worker-fencing). Interrupted jobs become failed/degraded for inspection, not automatically replayed. The process remains online and retries elections after session loss.

### Worker fencing

Each time a manager acquires leadership it increments a **leadership epoch** stored in `zela.leadership`, while holding the cluster lock, so epochs strictly increase. The leader sends its epoch on every worker request in the `X-Zela-Leader-Epoch` header. Each `zela-worker` remembers the highest epoch it has seen and rejects lower ones with `409 Request from a superseded control-plane leader`. The new leader contacts every worker during its pre-ready inventory refresh, so reachable workers learn the new epoch before the old leader could act again, even if that process was frozen (VM pause, long GC) after its last successful lock check.

- Requests without the header (standalone managers) are accepted unchanged.
- The Docker gateway strips client-supplied epoch headers, so only managers can set them.
- The high-water mark is held in agent memory. After an agent restart it is re-learned from the current leader's next request.
- Fencing requires the updated `zela-worker`. Update agents (**Monitoring → Update agent**) when upgrading managers.

### Worker failover

Leadership failover does not move containers, but the active leader replaces **managed deployment replicas** whose worker has been unreachable for 60 seconds (three failed probes mark it offline first). Replicas on a returning worker are re-adopted or removed so they never run twice. Containers created through the Docker gateway are left to their owner (for example Zela) to recover. See [Operations → Failure behavior](../docs/operations.md#failure-behavior).

### In-flight work during failover

An operation already accepted by Docker or a remote shell may finish even if its manager disconnects. A response lost before ownership was saved can leave a container requiring administrator inspection. Zela should inspect before retrying uncertain mutations and reconnect log/exec streams after failover. Failover time includes PostgreSQL session release, acquisition retries, settling, inventory probes and load-balancer checks; no fixed failover deadline is promised. HA connections request bounded PostgreSQL TCP keepalive/user-timeout settings, but upstream network behavior also matters.

## Shared configuration

Every manager needs:

- The same `ZELA_DATABASE_URL`, pointing at the writable database. Use trusted TLS (`sslmode=verify-full`) remotely. Direct connections or **session pooling** are required; transaction pooling cannot preserve session ownership.
- The same `ZELA_ADMIN_TOKEN`, at least 32 characters. HA will not generate per-node administrator tokens. This also preserves browser sessions across failover.
- The same original `ZELA_VAULT_KEY`, a base64-encoded 32-byte key. For an existing cluster, reuse its key; replacing it loses access to encrypted SSH identities.
- A unique manager ID and its own writable `--data` directory, Linux worker binaries and OpenSSH client. Do not share the local directory between processes.
- Access to every remote worker, the same trusted certificate authorities and compatible control-plane/worker binaries.

A private configuration fingerprint binds the shared admin token and vault key to the database. A mismatched manager fails initialization before recovery changes or worker access. API responses never expose this fingerprint. Once enabled, this database requires `--ha`; starting it in standalone mode is rejected.

Workers must use remote TLS/HTTP agents or SSH to remote hosts. Local Unix sockets, loopback and unspecified addresses are rejected, including when converting existing state. SSH agents may still listen on **worker loopback**: each manager tunnels to the worker's remote SSH address. DNS names must resolve to the same worker from all managers. Explicit private HTTP registrations require the existing `--allow-http-workers` flag.

## RHEL/systemd deployment

Install the binaries, OpenSSH clients and base service as described in [the installation guide](README.md). On every manager, put the shared credentials in `/etc/zela/zela-server.env`, mode `0640`, owner `root:zela`, with a different ID:

```ini
ZELA_DATABASE_URL=postgres://zela@db.example.internal:5432/zela?sslmode=verify-full
PGPASSWORD=<database-password>
ZELA_ADMIN_TOKEN=<shared-admin-token>
ZELA_VAULT_KEY=<original-base64-vault-key>
ZELA_MANAGER_ID=manager-a
```

Install the supplied systemd drop-in on both hosts:

```sh
sudo install -d -m 755 /etc/systemd/system/zela-server.service.d
sudo install -m 644 deploy/zela-server-ha.conf /etc/systemd/system/zela-server.service.d/ha.conf
sudo restorecon -RF /etc/zela /etc/systemd/system/zela-server.service.d
sudo systemctl daemon-reload
sudo systemctl restart zela-server
```

To convert an existing cluster, back up PostgreSQL and its keys, stop the old standalone manager, remove/replace any local worker registrations, and configure its original credentials on all HA managers before starting them. The first leader initializes the configuration fingerprint. Existing database records, scoped tokens and container IDs remain valid.

For a direct binary start, securely set the environment above and run:

```sh
./bin/zela-server --ha --manager-id manager-a --data ./data-manager-a \
  --listen 0.0.0.0:8080 --tls-cert /etc/zela/control.crt --tls-key /etc/zela/control.key
```

## Load balancer and probes

| Endpoint | Leader | Standby/fenced |
| --- | --- | --- |
| `GET /api/health` | `200` liveness | `200` liveness |
| `GET /api/ready` | `200` | `503` |
| Cluster/UI/Docker requests | Authenticated normal behavior | `503`, `Retry-After: 2` |

Probe each manager directly using `/api/ready`, not `/api/health`. Both probes expose only node ID, role, readiness and role-change time. Responses include `X-Zela-Manager` and `X-Zela-Manager-Role`; `/api/state` and `/api/cluster` also expose local HA status. The console shows the active manager under Settings.

The supplied [HAProxy configuration](haproxy-ha.cfg) selects only backends returning `200` on `/api/ready`, following [HAProxy's health-check configuration](https://www.haproxy.com/documentation/haproxy-configuration-tutorials/reliability/health-checks/). It starts backends unavailable until their first successful readiness check, supports long Docker streams/upgrades, disables automatic request retries, and refreshes Compose service DNS addresses. The supplied example uses HAProxy 3.2; `init-state fully-down` requires HAProxy 3.1 or newer. It is a private HTTP example. On RHEL, use your stable manager addresses, remove the Docker DNS resolver, terminate TLS on the load balancer and use trusted TLS to the managers, or use TCP TLS passthrough with TLS-aware readiness probes. Preserve the cluster Host header for browser origin checks. Allow long uploads, streams and protocol upgrades in any upstream proxy.

For end-to-end availability, place managers on separate hosts, use a PostgreSQL service with one properly fenced writable primary, and provide redundant ingress/a stable VIP or a managed load balancer. Managers do not implement database replication, database-primary fencing or load-balancer failover. A single PostgreSQL server or single proxy remains a failure point. During database unavailability, control-plane operations stop; existing application containers continue on workers.

## Local Compose example

The separate Compose file runs two managers, PostgreSQL and a readiness-aware gateway at `http://127.0.0.1:8080`:

```sh
cp .env.ha.example .env.ha
chmod 600 .env.ha
# Populate .env.ha with shared credentials. For a new cluster only:
# openssl rand -hex 32    (admin token and database password, separately)
# openssl rand -base64 32 (vault key)
docker compose --env-file .env.ha -f compose.ha.yaml up --build -d
```

This creates a **new, separate** Compose project and volumes; it does not migrate the default Compose database. Stop the default stack first if it uses port 8080. Reuse the existing credentials and external database when migrating an existing cluster. Keep the same `.env.ha` on subsequent starts. This one-machine example demonstrates manager failover, not host/database/ingress redundancy.

## Tests

```sh
make test-ha
# Real SQL integration creates/drops its own isolated database; login needs CREATEDB.
ZELA_TEST_DATABASE_URL='postgres://test_user@localhost/postgres?sslmode=disable' make test-ha
```

Tests exercise elections, standby rejection, shared credential validation, readiness withdrawal, stream cancellation, interrupted jobs, blocked/read-only persistence and gateway ownership after takeover. The real PostgreSQL test terminates the leader's session and explicitly releases the replacement's lock to verify fencing. Worker HTTP traffic runs through simulated engines; it does not prove real SSH host or cloud/network failure behavior.

The optional process test also starts the actual manager binary and HAProxy, kills the active process, and verifies the unchanged endpoint/token/container ID on the standby. On an isolated Linux test host/container, make `worker.test` resolve to that host and set:

```sh
ZELA_TEST_DATABASE_URL='<isolated-test-database-connection>' \
ZELA_TEST_MANAGER_BINARY="$PWD/bin/zela-server-linux-amd64" \
ZELA_TEST_HAPROXY_BINARY=/usr/sbin/haproxy \
ZELA_TEST_HAPROXY_CONFIG="$PWD/deploy/haproxy-ha.cfg" \
go test ./internal/control -run TestHAProcessesThroughClusterEndpoint -v
```

Use the binary matching the test host's architecture and the actual installed HAProxy path. The test binds temporary local ports, creates its own database/data directories and cleans up its child processes. Without these variables it skips; the regular test suite requires neither production credentials nor listeners.

## Cluster network, edge and ACME with HA

Start every manager with the same `--cluster-network`, edge and `--acme-*` flags. Network allocation, VIPs, routes, ACME accounts and certificates live in the shared database. Only the current leader runs ACME orders and configures workers. A new leader carries on with the same account and certificates.

## Upgrading HA clusters

Upgrade every manager in the same maintenance window. Startup migrations are forward-only, and a manager refuses a schema newer than it understands. Schema version 2 introduces the leadership epoch, so pre-epoch managers cannot rejoin a migrated database. Back up PostgreSQL first, and restore that backup if you need to downgrade. Update worker agents in the same window to enable worker fencing. See [Operations → Upgrades](../docs/operations.md#upgrades).

Changing shared admin/vault credentials or returning to standalone mode requires a coordinated offline maintenance procedure; no rolling credential rotation or downgrade tool is included. Keep a database backup and the original secrets before such changes.
