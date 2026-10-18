# Security: protect your cluster

[All guides](README.md) · [Browser management guide](console-guide.md)

## Start with these habits

| Do this                                                         | Why it matters                                                                             |
| --------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| Treat the administrator token like a password                   | It can control all workers and applications                                                |
| Use an HTTPS dashboard address for remote access                | Protects sign-in and management traffic                                                    |
| Give integrations their own access token                        | You can limit access and revoke one connection without changing your administrator sign-in |
| Start AI connections with read access when inspection is enough | Management access can stop or delete applications across the cluster                       |
| Verify the server's SSH host key before provisioning            | Helps prevent connecting to an unintended server                                           |
| Back up the original vault key securely                         | Without it, saved encrypted SSH keys cannot be recovered                                   |
| Review what is in a screenshot or log before sharing            | App settings, logs, and error details may contain secrets                                  |

**Before making a dashboard or application public**, have the server administrator review HTTPS, firewall rules, and published application ports. The detailed checklist below is for that review.

The control plane can run arbitrary containers on every host it manages. Anyone holding the admin token, a worker token, the vault key or database access effectively has **root on your Docker hosts**. Protect those secrets accordingly.

## Hardening checklist

- [ ] The console/API is served over **TLS** (`--tls-cert/--tls-key`, or a TLS-terminating proxy) whenever it is reachable beyond localhost.
- [ ] Port 8080 is reachable only from administrators, integrations and the load balancer, not the internet.
- [ ] Worker agents use SSH provisioning (loopback-only agent), or TLS with port 9091 firewalled to **manager addresses only**.
- [ ] `--allow-http-workers` and `--allow-http` are **off**, except on isolated lab networks.
- [ ] `ZELA_ADMIN_TOKEN`, `ZELA_VAULT_KEY` and `PGPASSWORD` live in a `0640 root:zela` environment file or a secret manager, not in shell history or unit files.
- [ ] Backups of the vault key are stored **separately** from database backups.
- [ ] PostgreSQL uses `sslmode=verify-full` and a dedicated login that owns only the `zela` database.
- [ ] Integrations use **scoped integration tokens** with an expiry, never the admin token.
- [ ] Worker host keys were obtained from a trusted channel before provisioning.
- [ ] Deployment ports that must not be public set `hostIP` to a private address, or workers firewall published ports. Without `hostIP`, ports listen on all interfaces.
- [ ] With the cluster network on, UDP 51820 is reachable only from other workers, and its range doesn't overlap existing networks.
- [ ] Managers and workers run the same release (see [Operations → Upgrades](operations.md#upgrades)).

## Credentials

### Administrator token

- At least 32 characters. Generated into `<data>/admin-token` if `ZELA_ADMIN_TOKEN` is unset, and **required** to be set explicitly and identically on all managers in HA.
- Grants everything: worker administration, the SSH vault, full Docker API access on every worker, and token management.
- Browser sign-in exchanges it for a signed, `HttpOnly`, `SameSite=Strict` session cookie that lasts 12 hours. The cookie gets the `Secure` attribute only when the manager itself terminates TLS. If TLS is terminated by a proxy, make sure the proxy-to-manager hop is also encrypted or private.
- **Rotation:** set a new `ZELA_ADMIN_TOKEN` and restart the managers. In HA, the admin token is part of a configuration fingerprint stored in the database, so rotation is an offline, coordinated change (see [HA](../deploy/HA.md)).

### Integration tokens

Created in **Settings & setup → Integration API tokens** or with `zela tokens create`.

| Scope                                | Can                                                                                                     | Cannot                                                                                                       |
| ------------------------------------ | ------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| `infrastructure` (default in UI/CLI) | Read worker health and capacity; create, manage and inspect **its own** containers on a selected worker | Deployments API, scheduling, other tokens' containers, worker administration, vault, settings, notifications |
| `deployments`                        | Everything above plus automatic placement and the deployment APIs                                       | The same administrative areas                                                                                |

- Both scopes block privileged containers, host mounts/devices/namespaces/networking, added capabilities and custom runtimes. Builds, plugins, custom networks and volume administration need the admin token.
- Values contain 256 bits of randomness. Only SHA-256 hashes are stored, and the value is shown **once**.
- **Revocation** takes effect immediately and cancels active streams. Running containers are not stopped.
- **MCP scopes** (`mcp_read`, `mcp_manage`) are cluster-wide operator roles, not ownership-scoped. They work only through the named MCP tools (`/mcp`), never the raw Docker gateway, token administration or private-key handling. `mcp_manage` can stop or delete any workload and drain or unregister workers. It can only provision hosts allowlisted in `ZELA_MCP_PROVISION_HOSTS`, which is empty by default. See [MCP security considerations](mcp.md#security-considerations).
- Prefer `--expires-days` (maximum 365) and `zela tokens create --out <file>` so the token never appears on screen.

### Worker tokens

- At least 32 characters, in `/etc/zela/worker.env` on the host and stored in private manager state.
- Grant full Docker Engine access on that host, which is root-equivalent.
- SSH-provisioned agents only listen on host loopback, so the token is never exposed to the network.

### SSH vault

- Private keys and passphrases are encrypted with AES-256-GCM using the vault master key (`ZELA_VAULT_KEY` or `<data>/vault-key`).
- There is no API to export a private key. Keys referenced by a registered worker can't be deleted.
- OpenSSH needs plaintext identity files while connecting. They are written with mode `0600` to `<data>/ssh-tmp` and deleted after each connection.
- **Host keys are pinned:** provisioning requires the worker's public host key and rejects mismatches. Get it from a trusted channel, never from a first connection.
- Keep the vault key and database backups in **different places**. Either one alone is useless to an attacker; together they reveal every SSH key.

## TLS

**Console/API.** Pass `--tls-cert` and `--tls-key`. TLS 1.2 is the minimum. Behind a reverse proxy or load balancer:

- preserve the `Host` header, which browser origin checks use;
- allow long-lived connections and HTTP upgrades, for Docker `attach`, `exec` and log streams;
- disable automatic request retries, because mutations are not idempotent.

**Workers.** The certificate's SAN must match the endpoint the managers use. Add a private CA to the manager's trust store, or set `SSL_CERT_FILE`.

**zela** always verifies certificates (`--ca-cert` adds a CA). It refuses to send a token over plain HTTP except to `localhost` or loopback addresses.

**PostgreSQL.** Use `sslmode=verify-full`.

## Network exposure

| Source → destination                  | Port        | Required for                                                                    |
| ------------------------------------- | ----------- | ------------------------------------------------------------------------------- |
| Admins / integrations / LB → managers | 8080        | Console, API, Docker gateway                                                    |
| Managers → workers                    | 22          | SSH-provisioned workers                                                         |
| Managers → workers                    | 9091        | Manual TLS agents only. Block it from everything else                           |
| Managers → PostgreSQL                 | 5432        | State                                                                           |
| Workers ↔ workers                     | 51820/udp   | Cluster network (WireGuard), only when enabled                                  |
| Clients → workers                     | 80, 443/tcp | Edge proxy, only when enabled                                                   |
| Workers → internet                    | 443         | Package repositories and `download.docker.com` during provisioning; image pulls |

## Cluster network

- **Encryption:** traffic between workers is encrypted and authenticated by WireGuard. Private keys are generated on each host and never sent to the control plane, which stores only public keys.
- **Scope:** agents refuse configurations that route addresses outside the cluster range, so a compromised manager can't steer arbitrary networks through a worker.
- **Isolation:** the `ZELA-NET` firewall chain accepts new connections to local containers only from the mesh, local containers and published ports. Hosts on the same LAN can't route into container subnets.
- **Cluster DNS:** each worker's resolver answers only sources inside the cluster range or on the host, so it can't be used as an open resolver or amplifier from the LAN. It accepts DNS on the bridge interface only (chain `ZELA-NET-IN`).
- **Capabilities:** the agent gains `CAP_NET_ADMIN`, `CAP_NET_RAW` and `CAP_NET_BIND_SERVICE` when the network is enabled. It already had Docker-administrator access, so this doesn't widen what a compromised agent could do.
- **Flat network:** every container can reach every other container. Isolate untrusted tenants in separate clusters.

## Edge proxy

- **Exposure:** the edge ports are meant to face clients. Every routed deployment is reachable through them on **every** worker, so only add routes for services that should be public, and authenticate inside your application.
- **Local TLS keys** (`--edge-tls-cert/--edge-tls-key`) stay on each worker and are never stored in or sent through the control plane. Restrict the key file to the agent.
- **ACME keys** are generated by the control plane and sealed with the vault master key at rest. They're sent to every worker over the authenticated agent channel, and workers store them in their private `network.json`. Anyone with the vault key and the database, or root on a worker, can read them. `/api/state` never includes them.
- **ACME challenges** are served on every worker's HTTP port while an order is in progress, and removed afterwards.
- **Spoofed headers:** the proxy overwrites `X-Forwarded-For` (appending the client address), `X-Forwarded-Host` and `X-Forwarded-Proto`. Backends should trust them only from the edge, which reaches them from the mesh.
- **Not provided:** rate limiting, authentication and a web application firewall. Put a dedicated proxy in front for those.

## MCP and OAuth

- Prefer `mcp_read` for assistants. Issue `mcp_manage` only to clients that may operate the whole cluster, and keep the AI client's own approval prompts on for write tools.
- **Prompt injection.** Logs, image metadata and operation messages are attacker-influenced text. A management-capable assistant that reads them can be steered into destructive calls. The server's instructions and tool descriptions mark this output as untrusted, but only your approval rules actually stop it.
- **Consent phishing.** Anyone can register an OAuth client with a name of their choosing, using an allowlisted callback such as chatgpt.com, and send an administrator the authorization link. The consent page labels the client name as unverified and shows when the client registered. Only approve connections you started yourself. Revoke unknown **MCP: …** entries in Integration API tokens.
- Keep `ZELA_MCP_PROVISION_HOSTS` empty unless assistants must add workers, and then list only networks you control.
- Behind a reverse proxy, set `ZELA_MCP_TRUSTED_PROXIES` so OAuth rate limits apply per client rather than to everyone.
- Keep `ZELA_MCP_REDIRECT_URIS` to the exact callbacks you use. Leave `ZELA_MCP_PUBLIC_URL` unset if you don't need browser OAuth; bearer MCP tokens still work.
- The consent page is served with `frame-ancestors 'none'` and `X-Frame-Options: DENY`. Consent POSTs require the same origin, a signed request and a nonce cookie.

## HA-specific protections

- **Single writer:** only the manager holding the PostgreSQL advisory lock serves requests or talks to workers. Every worker request re-checks the lock.
- **Worker fencing:** each leadership term has an increasing epoch, and workers reject lower epochs, so a frozen former leader can't act after a failover.
- **Shared credentials:** a fingerprint of the admin token and vault key is bound to the database, so a misconfigured manager refuses to start.

## Reporting a vulnerability

Please report security issues privately rather than in a public issue. See [SECURITY.md](../SECURITY.md) for the process.
