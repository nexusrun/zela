# Operations

- [Backups and restore](#backups-and-restore)
- [Upgrades](#upgrades)
- [Worker maintenance](#worker-maintenance)
- [Failure behavior](#failure-behavior)
- [Monitoring the control plane](#monitoring-the-control-plane)
- [Troubleshooting](#troubleshooting)

## Backups and restore

Back up all three together. Each is useless without the others:

| What | How |
| --- | --- |
| PostgreSQL `zela` schema | `pg_dump --schema=zela -Fc zela > zela.dump` (or your managed-database snapshots) |
| Vault master key | `ZELA_VAULT_KEY`, or `<data>/vault-key`. Store it **separately** from the dump |
| Admin token | `ZELA_ADMIN_TOKEN`, or `<data>/admin-token` |

With JSON storage, back up the whole `--data` directory while the manager is stopped, or copy the `*.json` files. They are replaced atomically, so a live copy is consistent per file.

**Restore**

1. Stop every manager.
2. Restore the schema: `pg_restore --clean -d zela zela.dump`.
3. Provide the **original** vault key and admin token.
4. Start a manager. Operations that were running at backup time show as failed; inspect them before retrying.

Restored state may be older than reality. After the first inventory poll, compare **Containers** with **Deployments** and remove any leftovers.

## Upgrades

1. Read the release notes for schema migrations and agent protocol changes.
2. Build or download the new release (`make linux`).
3. **Back up** (see above).
4. **Upgrade managers.** Replace the `zela-server` binary and the worker artifacts in `--worker-binaries`, then restart. Migrations run automatically on start.
   - **HA:** upgrade **all managers in the same maintenance window**. A manager that sees a newer schema version refuses to start, so don't roll back an individual manager after the database has been migrated. Restore the backup if you need to downgrade.
   - **Schema version 2** adds the HA leadership-epoch table. Managers built before it refuse to start against a migrated database.
5. **Upgrade agents.**
   - SSH-provisioned workers: **Monitoring → Update agent**, or `zela workers update-agent <id> --wait`. This preserves the token, service flags, placement mode and running containers.
   - Manual agents: copy the new `zela-worker` and run `systemctl restart worker`.
6. **Upgrade `zela`** on admin machines.

Restarting an agent doesn't affect running containers. Restarting a manager interrupts its running operation, which is marked failed, and drops open Docker streams. Clients must reconnect.

## Worker maintenance

| Goal | Action |
| --- | --- |
| Stop new placements, keep workloads | `zela workers pause <id>` |
| Move managed replicas off, for patching or reboot | `zela workers drain <id> --wait` |
| Return to service | `zela workers activate <id>` |
| Remove an empty worker | `zela workers remove <id>` |
| Retry failed provisioning | `zela workers retry <id> --wait` |

Drain moves only **managed, stateless** replicas. Containers with mounts, stopped deployments, missing capacity, or unmanaged containers leave the worker in `draining` until you resolve them. Unregistering never uninstalls Docker or the agent.

## Failure behavior

### A worker goes down

| Time after last successful probe | What happens |
| --- | --- |
| 0–30s | Probes fail. The worker stays `online` with an error until 3 consecutive failures, then turns `offline` and unschedulable. Its deployments show `degraded` |
| from about 30s | `worker-offline` alert (counted from the last successful probe) |
| 60s | A **Recover** operation recreates the lost replicas on other eligible workers |
| Worker returns | Old replicas are re-adopted if the deployment is still short of replicas, or removed otherwise |

Recovery is not possible when no other worker matches the deployment's placement (`WorkerIDs`) or has capacity. The deployment then stays `degraded` with an error. Fix capacity or placement, then run `zela deployments start <id>` to retry.

Containers created through the Docker gateway (NEXUS AI or Docker CLI clients), and containers that weren't created by Zela, are **not** recreated automatically. Their owner handles recovery. An offline owner returns `503` for their requests.

### The database becomes unavailable

- **Standalone:** the manager checks persistence every 10 seconds and **exits** when the session is lost, so systemd or Compose restarts it. A session never reconnects silently. Workloads keep running on workers.
- **HA:** the leader stops reporting ready and fences itself if checks keep failing for about 5 seconds. Standbys keep trying to take the lock. See [HA failover](../deploy/HA.md#coordination-and-failover).

### A manager crashes

The interrupted operation is marked failed on the next start (or by the new leader in HA). It is **not** replayed, because a Docker mutation may already have happened. Inspect it in **Activity** before retrying.

## Monitoring the control plane

| Signal | Use |
| --- | --- |
| `GET /api/health` | Liveness: the process is serving |
| `GET /api/ready` | Readiness: `200` only on a leader that can serve. Use this for load balancers |
| `journalctl -u zela-server` | Startup, persistence, HA role changes (`HA manager X: leader/standby/fenced`), failover (`failover: replacing replicas of …`) |
| `inventory-stale` alert | The poller has stalled |
| `zela operations list` | Recent operations and their errors |

## Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `CLI configuration must be a regular private file` | Config file or directory permissions | `chmod 600 <file>`, `chmod 700 <dir>` |
| `refusing to send a token over plain HTTP` (zela) | `http://` URL to a remote host | Use HTTPS, or connect from the manager to `http://127.0.0.1:8080` |
| `401 … check the configured token` | Wrong, expired or revoked token | `zela login` again, or check `ZELA_TOKEN*` |
| `403 … may require the administrator token` | Integration token used for admin APIs | Use the admin token |
| `409 another cluster operation is running` | One mutation at a time | Wait, or `zela operations wait <id>` |
| `503 no active manager on this node` | Request reached an HA standby | Send traffic through the readiness-aware load balancer |
| `PostgreSQL database is already in use by another control plane` | Another standalone manager holds the lock | Stop it, or use `--ha` on both |
| `HA shared administrator token or vault key does not match this cluster` | Managers configured with different secrets | Use identical `ZELA_ADMIN_TOKEN` and `ZELA_VAULT_KEY` everywhere |
| `unsupported PostgreSQL schema version` | Old binary against a newer schema | Upgrade the binary, or restore the pre-upgrade backup |
| Worker `offline`, error `connection refused` | Agent stopped, or firewall | `systemctl status zela-worker` on the host; check port 9091 or SSH |
| Worker `offline`, SSH host key error | Host was reinstalled or its key changed | Verify the new key out-of-band and re-provision |
| Provisioning fails at "Install Docker" | Repository access, RHEL subscription, Podman conflict | `journalctl -u zela-worker`, check package manager output in Activity, resolve, then retry |
| `409 Request from a superseded control-plane leader` (in worker logs or errors) | A former HA leader tried to act after failover | Expected and safe. Check that the old manager has restarted as standby |
| Deployment stuck `degraded` with error | Recovery or replica placement failed | Read the error, fix capacity or placement, then `zela deployments start <id>` |
| Metrics missing on a worker | Old agent, or a local-socket registration | Update the agent; local sockets have no metrics |
| Network: `create zela0 (is the wireguard kernel module loaded and CAP_NET_ADMIN granted?)` | Missing module, or agent lacks capabilities | `modprobe wireguard`; update the agent (provisioned workers) or add the [systemd drop-in](networking.md#enable-it) |
| Network: `requires Docker Engine 28 or newer` | Old Docker on the worker | Upgrade Docker Engine |
| Network: `Docker network "zela" has a different subnet and attached containers` | The range or index changed while containers were attached | Remove or recreate those containers, then wait for the next re-apply |
| Worker gets no new replicas after enabling the network | Its network member shows an `error` | Fix the error shown in `/api/state` → `network.members` |
| Network: `start cluster DNS on 10.200.N.1:53 (is CAP_NET_BIND_SERVICE granted?)` | Old drop-in without the bind capability, or port 53 taken on the bridge address | Update the agent, or add `CAP_NET_BIND_SERVICE` to the drop-in |
| `web.zela` doesn't resolve in a container | Container created before the network or DNS existed, or deployment not running | Restart the deployment so its replicas are recreated; check `docker exec … cat /etc/resolv.conf` shows `search zela` |
| `edgeError: edge HTTPS needs --edge-tls-cert …` on a worker | Edge HTTPS enabled but that worker has no local certificate | Add `--edge-tls-cert/--edge-tls-key` to its agent and restart it. HTTP keeps working meanwhile |
| `edgeError: edge listen on port 80 …` | Port in use (another web server, or a published container port), or missing `CAP_NET_BIND_SERVICE` | Free the port, or choose other `--edge-*-port` values |
| `acmeCertificates` error `validation of http://HOST/.well-known/acme-challenge/ failed` | DNS for the host doesn't point at the workers, or inbound port 80 is blocked | Fix DNS or the firewall. The next retry follows the backoff, at most an hour, and the retry time persists across manager restarts |
| `acmeCertificates` error `workers did not apply the ACME challenge: …` | Some online workers can't be configured | Fix those workers' network errors first |
| `edgeError: edge HTTPS has no certificate yet …` | HTTPS enabled, but no ACME certificate is issued yet and there's no local certificate | Normal right after enabling ACME; it clears once the first certificate is issued |
| Edge returns `404 no route for this host and path` | No route matches the `Host` header and path | Check the deployment's `routes`; the client must send the public host name |
| Edge returns `502 service unavailable` | The routed service has no running, healthy replica | Check the deployment and its replicas' health |
| Connections to a service VIP fail intermittently | A replica stopped and its backend entry is still listed (up to one inventory cycle) | Retry in the client; check `iptables -t nat -S ZELA-SVC` on the client's worker |
| `Connection refused` on a service VIP | The service has no running, healthy replicas | Check the deployment's status and replica health |
| Containers can't reach other hosts | UDP 51820 blocked, or a peer endpoint unreachable | Open the port between workers; set `--network-endpoint` for NATed hosts; check `ip route show dev zela0` |
| `cluster network 10.200.0.0/16 is in use; disable it …` at manager start | Started with a different `--cluster-network` | Use the original range, or [disable it first](networking.md#changing-or-disabling) |
| MCP: `valid MCP bearer credential required` (401) | Missing or expired token, or OAuth not completed | Reconnect the connector, or check the token file |
| MCP: `create an mcp_read or mcp_manage credential` (403) | Infrastructure/deployment token used for MCP | Create an MCP-scoped token |
| MCP: write tools missing | `mcp_read` token, or `mcp serve --read-only` | Use an `mcp_manage` credential without `--read-only` |
| OAuth: `callback is not in ZELA_MCP_REDIRECT_URIS` | The client uses a callback that isn't allowlisted | Add the exact callback shown by the client, then restart the managers |
| MCP: `MCP provisioning is limited to ZELA_MCP_PROVISION_HOSTS` (403) | The host isn't on the MCP provisioning allowlist | Provision it as an administrator, or add its network to `ZELA_MCP_PROVISION_HOSTS` |
| OAuth: `client registration unavailable` (503) | 512 active clients | Unused clients are pruned after 24h; revoke stale **MCP: …** connections |
| OAuth: `429 … rate limit exceeded` | 20 requests/min per IP. Behind a proxy without `ZELA_MCP_TRUSTED_PROXIES`, everyone shares the proxy's IP | Set `ZELA_MCP_TRUSTED_PROXIES` and enable `X-Forwarded-For` on the proxy |
| Disk alerts don't cover Docker data | Docker data root not monitored | Add it with `--metrics-disk-paths` on the agent |

For more detail, run the manager in the foreground (`./bin/zela-server --data ./data ...`) and reproduce the problem. All errors are logged, with secrets redacted.
