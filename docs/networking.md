# Networking

[All guides](README.md) · [Browser management guide](console-guide.md) · [Glossary](glossary.md)

Networking answers two questions: **how do people reach my app, and how do my apps reach each other?** Start with the task you need. You do not have to enable the encrypted cluster network just to test a website.

## Choose what you need

| Your goal                                             | Use                                                    | Start here                                                       |
| ----------------------------------------------------- | ------------------------------------------------------ | ---------------------------------------------------------------- |
| Open one app in a browser                             | A published worker port, such as `8081:80`             | [Sample website](console-guide.md#deploy-your-first-application) |
| Let applications communicate privately across servers | The encrypted cluster mesh                             | [Cluster network setup](#manage-networking-in-the-dashboard)     |
| Use a website name or path to reach an app            | A service route plus configured edge listeners and DNS | [Website routes](#publish-a-website-route)                       |
| Inspect or create a Docker network on one server      | Worker networks                                        | [Worker network management](#manage-one-workers-docker-networks) |

## Why are the controls disabled?

| Situation                                                    | What it means                                                   | What to do                                                                                                                                                      |
| ------------------------------------------------------------ | --------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Demo environment** banner; Configure network is greyed out | Demo workers are simulated and cannot run a real encrypted mesh | Start a [live manager](getting-started.md#run-a-live-manager-with-docker-compose), then connect Linux workers. The demo has no in-dashboard switch to Live mode |
| An operation is running                                      | Zela allows one cluster operation at a time                     | Wait for its result in Activity log                                                                                                                             |
| Reapply is disabled before setup                             | There is no saved mesh configuration to reapply                 | Use Configure network first                                                                                                                                     |
| Address range or UDP port is read-only                       | Existing members are using those settings                       | Remove dependent workloads, disable the mesh, and wait for teardown before changing them                                                                        |
| A Docker network has no change controls                      | Built-in networks and the Zela-managed network are protected    | Use a custom local bridge for standalone containers                                                                                                             |
| Delete network is disabled                                   | Containers are still attached                                   | Inspect the network and disconnect eligible standalone containers first                                                                                         |

An error after you submit is different from a disabled button. Read **Activity log** and the worker's mesh error to learn why application failed. The mesh also needs an administrator session; scoped integration and MCP tokens cannot configure it.

## Manage networking in the dashboard

### 1. Prepare the real workers

The manager must be in **Live** mode. Workers must be Linux hosts running the Zela agent with Docker Engine 28 or newer, WireGuard support, and the required agent permissions. A direct local Docker socket cannot join this mesh.

Ask your server/network administrator to choose an unused private `/16` range and permit worker-to-worker traffic on the selected UDP port. `10.200.0.0/16` and `51820` are examples, not values guaranteed to fit your network. See the exact [requirements](#requirements) below.

### 2. Save the cluster settings

1. Open **Infrastructure → Networking → Cluster network**.
2. Select **Configure network** and enable the encrypted cluster network.
3. Enter the agreed private range and WireGuard UDP port.
4. If you only need private connectivity, leave HTTP/HTTPS edge ports at `0` for now. `0` means that listener is off.
5. Save, then check **Activity log**.
6. For existing SSH-provisioned workers, use **Monitoring → Update agent** to prepare their capabilities and ports for the enabled network. Manually installed agents need the [manual preparation steps](#enable-it).
7. Return to **Cluster network**, inspect each member's status/error, and use **Reapply** after correcting a problem.

**Check:** workers should receive tunnel addresses and container subnets, with **Config applied** status. This means the agent accepted the settings; it does not test peer handshakes or prove that traffic can pass. Verify application-to-application connectivity before relying on the network.

Existing application containers are not moved onto the mesh automatically. Recreate the managed deployment copies after the workers are ready; plan for interruption and local-data loss if the application stores data in its container. New managed deployments use the mesh once eligible workers are ready.

### 3. Understand what is saved

Settings saved in the console survive restarts. Explicit networking startup options or nonempty networking environment variables override them when a manager starts. Ask the person running the manager to remove conflicting overrides if you want the console to be the configuration source. All HA managers must use consistent startup settings.

To change the range or UDP port later, follow [Changing or disabling](#changing-or-disabling). Do not remove an in-use mesh as a troubleshooting shortcut.

## Manage one worker's Docker networks

1. Open **Networking → Worker networks** and choose the worker.
2. Use **Reload networks** to read its current Docker networks.
3. Select **Inspect** to see subnets, gateways, and attached containers.
4. Use **Create bridge network** for a new network local to that worker. Leave the subnet/gateway blank for Docker allocation, or have your administrator choose a non-overlapping private range.
5. From a custom network's inspection dialog, connect or disconnect a **standalone** container, or delete the network when it is empty.

A local bridge does not connect different workers. These controls do not edit general host network interfaces, OS routes, or firewall policy. Deployment-managed containers keep their declared network configuration and cannot be attached or detached here. Demo Docker network changes are simulated and reset when the manager restarts.

## Publish a website route

Use this when you want a name such as `app.example.com` to reach a managed deployment through the workers' edge proxy.

1. Confirm that the cluster mesh is enabled and your deployment has a service VIP, shown under **Networking → Services & routes**.
2. Have your administrator enable the intended HTTP/HTTPS edge listeners, open those worker ports, and configure certificates for HTTPS. A port already used by another service cannot also be used by the edge proxy.
3. In **Services & routes**, choose **Add route** for the deployment.
4. Enter the hostname, path prefix (normally `/`), and the **container port** the app listens on. For the nginx example, use `80`, not the published worker port `8081`.
5. Save and allow the next inventory cycle to apply the change. Check worker edge errors if it fails.
6. Point your website's public DNS at reachable worker addresses, or at a load balancer that forwards to their edge ports.
7. Open the website by its configured name and test it.

**Check:** the intended app should load, and HTTPS should have a valid certificate when configured. Saving a route does not create public DNS or enable automatic certificate issuance. See [Edge proxy](#edge-proxy) and [Automatic certificates](#automatic-certificates-acme) for administrator setup.

## Technical details and references

The sections below explain port mappings, address allocation, prerequisites, service DNS, certificate configuration, and APIs. For everyday use, the dashboard steps above are enough to locate the controls.

How application traffic reaches your containers and how containers talk to each other across hosts. The control plane offers two models. You can use them together.

|                                     | Published ports (default)            | Cluster network (opt-in)                                                                 |
| ----------------------------------- | ------------------------------------ | ---------------------------------------------------------------------------------------- |
| Container-to-container across hosts | Through `worker-ip:hostPort`         | Directly by container IP, encrypted                                                      |
| Setup                               | A published port mapping             | Console configuration or `--cluster-network`, plus permitted UDP traffic between workers |
| Source address preserved            | No (NAT)                             | Yes, inside the cluster                                                                  |
| External clients                    | Published ports + your load balancer | Same: published ports + your load balancer                                               |
| Service names                       | No                                   | `web.zela` / `web` resolve to the service's stable VIP                                   |
| Load balancing inside the cluster   | No (your load balancer)              | Built-in, per connection, across healthy replicas                                        |
| External HTTP(S) routing            | Your load balancer                   | Optional built-in [edge proxy](#edge-proxy) on every worker                              |
| Requirements                        | Any Docker                           | WireGuard kernel module, Docker Engine 28+, updated agent                                |

For external traffic, use published ports with your own load balancer, or the cluster network's [edge proxy](#edge-proxy).

## Admin console

Open **Infrastructure → Networking** with an administrator session:

- **Cluster network:** enable the mesh, configure its private `/16` range and WireGuard port, adjust edge listeners, inspect each worker's assigned addresses and apply errors, edit peer endpoint overrides, and reapply configuration.
- **Worker networks:** select a worker to fetch its Docker networks, inspect subnets, gateways and connected containers, create a local bridge, connect or disconnect standalone containers, and delete empty custom bridges. Host RX/TX rates appear when fresh telemetry is available.
- **Services & routes:** inspect service VIPs and published cluster DNS records, add/edit/remove deployment edge routes, and review manager-issued certificate expiry and issuance errors. Public DNS and TLS issuance settings still require separate configuration.

Network changes use the normal operation tracker. Configuration errors remain visible per worker; an accepted operation does not prove peer handshakes or end-to-end reachability. Host interfaces, OS routes and host firewall rules are not general-purpose editors in this console.

Console mesh settings are saved in cluster state and survive manager restarts. Explicit networking flags or nonempty networking environment variables override the saved settings at startup; supply the complete desired configuration consistently on every HA manager. For console-managed configuration, remove those overrides from all managers. Worker installation and agent updates use the current saved mesh and edge ports.

The console blocks disabling a mesh with managed replicas or known attached containers, changing its range/UDP port while members remain, modifying built-in or platform-managed Docker networks, and attaching deployment-managed containers to custom bridges. Only local Docker bridge networks are editable. Explicit custom subnets must fit in a private IPv4 range and must not overlap the mesh; Docker also checks overlap with existing Docker networks. Choose ranges that do not overlap host/VPN routes.

These APIs require administrator access, including reads. Scoped integration and MCP tokens cannot use them:

| Method     | Endpoint                                       | Purpose                                                                      |
| ---------- | ---------------------------------------------- | ---------------------------------------------------------------------------- |
| GET / POST | `/api/network`                                 | Inspect metadata / save desired mesh and edge configuration                  |
| POST       | `/api/network/reapply`                         | Reapply all members, or one `workerID`                                       |
| POST       | `/api/workers/{id}/network-endpoint`           | Set the peer `endpoint` hostname/IP; empty resets to the worker address      |
| GET / POST | `/api/workers/{id}/networks`                   | List Docker networks / create a bridge                                       |
| GET        | `/api/workers/{id}/networks/{network}`         | Inspect a network and its endpoints                                          |
| POST       | `/api/workers/{id}/networks/{network}/actions` | `delete`, `connect` or `disconnect`, with `containerID` for endpoint actions |
| PUT        | `/api/deployments/{id}/routes`                 | Replace routes; optional `expectedRoutes` rejects stale edits                |

The encrypted mesh requires live Linux agents. Demo worker network operations are simulated and custom demo networks reset when the manager restarts.

## Published ports

Each deployment port maps a container port to a host port on whichever worker runs the replica:

```json
{
  "ports": [
    { "container": 80, "host": 8080, "protocol": "tcp", "hostIP": "10.0.0.21" }
  ]
}
```

- **`host: 0`** lets Docker pick a free port. Look it up in **Containers** or the API.
- **A fixed host port** can run only one replica per worker, so replicas spread across workers.
- **`hostIP`** (optional) binds the port to a single host address, such as a private interface, instead of every interface. Without it, the port listens on **all interfaces, including public ones**. Set it, or firewall the worker.

### Getting external traffic in

Because replicas can move (moves, rollouts, drains, automatic failover), point clients at **all workers**, not at one container:

**Pattern A: fixed port everywhere, external load balancer (simplest).** Deploy with a fixed host port and up to one replica per worker. Configure HAProxy, nginx, keepalived with a virtual IP, or a cloud load balancer with every worker as a backend on that port, plus a health check. Workers without a replica fail the check and receive no traffic. After a failover, the new location starts passing automatically, with no configuration change.

```haproxy
frontend web
    bind :80
    default_backend web
backend web
    option httpchk GET /
    server worker-1 10.0.0.21:8080 check
    server worker-2 10.0.0.22:8080 check
    server worker-3 10.0.0.23:8080 check
```

**Pattern B: generated load-balancer configuration.** For variable replica counts, poll `GET /api/state` with the admin token, read each deployment's `tasks` (`workerID`) and the worker's container `Ports`, then render and reload your proxy's configuration.

## Cluster network

An opt-in encrypted network that gives containers on different workers direct, routable IP connectivity.

```text
   worker 1                         WireGuard (UDP 51820)                    worker 2
 ┌──────────────────────┐   ◄───────────────────────────────────────►   ┌──────────────────────┐
 │ zela0   10.200.0.1  │                                               │ zela0   10.200.0.2  │
 │ zela    10.200.1/24 │   web-1 10.200.1.2  ──────────►  api-1 10.200.2.2  │ zela 10.200.2/24 │
 └──────────────────────┘                                               └──────────────────────┘
```

### Address plan

You choose one private IPv4 `/16`. Worker _N_ (1–254) gets:

|                  | Address                              | Purpose                                                |
| ---------------- | ------------------------------------ | ------------------------------------------------------ |
| Mesh address     | `10.200.0.N/24` on interface `zela0` | Host-to-host over WireGuard                            |
| Container subnet | `10.200.N.0/24`                      | Docker network `zela` (bridge `zelabr0`, gateway `.1`) |
| Service VIPs     | `10.200.255.0/24`                    | One stable virtual IP per deployment (up to 254)       |

Choose a range that doesn't overlap your LAN, VPN or cloud VPC. The range can't be changed while workers are using it. See [Changing or disabling](#changing-or-disabling).

### How it works

1. The control plane assigns each online worker an index, which it stores in the database.
2. Each worker agent generates its own WireGuard key. The private key stays on the host (`/var/lib/zela-worker/wireguard.key`, mode `0600`), and only the public key is reported.
3. The control plane sends every worker its addresses and the list of peers. Each peer entry has a public key, an endpoint, and allowed addresses limited to that peer's mesh IP and container subnet. The agent rejects any configuration that would route addresses outside the cluster range.
4. The agent then sets up:
   - the `zela0` interface (MTU 1420);
   - a route per peer subnet;
   - the `zela` Docker network, with outbound NAT still on so containers keep internet access;
   - two firewall chains:
     - `ZELA-NET`, from `DOCKER-USER`: only the mesh, local containers and published ports may open connections to local containers;
     - `ZELA-NET-NAT`, from `POSTROUTING`: no address translation inside the cluster, so source addresses are preserved.
5. Managed deployments are created on the `zela` network. While the network is enabled, replicas are placed **only on workers whose network is up**. A worker that fails to apply its network shows the error and receives no new replicas.
6. Each deployment gets a stable **service VIP**, load-balanced by NAT rules on every worker. See [Service names and VIPs](#service-names-and-vips).
7. Each worker agent runs a **cluster DNS server** on its bridge gateway (`10.200.N.1:53`). Managed containers use it through Docker's embedded resolver. See [Service names](#service-names-and-vips).
8. The agent saves the last configuration and re-applies it every 30 seconds. Routes and firewall rules are repaired after reboots, `firewalld` reloads or Docker restarts, **even while every manager is down**.

### Requirements

|              | Requirement                                                                                                                                    |
| ------------ | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| Kernel       | WireGuard module: Linux 5.6+, Ubuntu 22.04+, Debian 12+, RHEL/Rocky/AlmaLinux 9+. RHEL 8 needs a third-party `kmod-wireguard`                  |
| Docker       | Engine **28 or newer** (API 1.48+), for the bridge `gateway_mode_ipv4=nat-unprotected` option                                                  |
| Tools        | `ip` (iproute2) and `iptables` (legacy or nft), already present wherever Docker is installed                                                   |
| Agent        | The updated `zela-worker`, with `CAP_NET_ADMIN`, `CAP_NET_RAW` and `CAP_NET_BIND_SERVICE` (for DNS on port 53), and a writable state directory |
| Firewall     | **UDP 51820** (or `--cluster-network-port`) open between all workers                                                                           |
| Connectivity | Every worker must reach every other worker's endpoint directly (full mesh)                                                                     |

Direct Docker socket registrations (`unix://`) can't join. With the network enabled they receive no managed replicas.

### Enable it

1. **Enable the network** under **Networking → Cluster network → Configure network**, or start every manager with the same range:
   ```sh
   zela-server --cluster-network 10.200.0.0/16 ...       # or ZELA_CLUSTER_NETWORK=10.200.0.0/16
   ```
2. **Prepare the workers.**
   - **SSH-provisioned workers:** provisioning (and **Monitoring → Update agent** for existing workers) does all of this when the network is enabled:
     - loads the `wireguard` module and persists it in `/etc/modules-load.d/zela-wireguard.conf`;
     - adds the drop-in `/etc/systemd/system/zela-worker.service.d/cluster-network.conf`, which grants the capabilities and the state directory;
     - opens the UDP port in `firewalld` or `ufw` if one is active.
   - **Manually installed agents:** do the same by hand:
     ```sh
     sudo modprobe wireguard && echo wireguard | sudo tee /etc/modules-load.d/zela-wireguard.conf
     sudo systemctl edit worker      # add the lines below
     ```
     ```ini
     [Service]
     AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE
     CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE
     StateDirectory=zela-worker
     StateDirectoryMode=0700
     ReadWritePaths=/run
     ```
     Then open UDP 51820 between workers and restart the agent.
3. **Check it.** Within one or two inventory cycles (about 10–20 seconds), `GET /api/state` shows `network.members` with an `index`, `subnet`, and `applied` for each worker, or an `error` explaining what's missing. On a worker:
   ```sh
   ip -4 addr show zela0 && ip route show dev zela0
   docker network inspect zela --format '{{range .IPAM.Config}}{{.Subnet}}{{end}}'
   ```
4. **Recreate existing workloads** (for example, through an image rollout) to move their replicas onto the `zela` network. Existing containers stay on the default bridge until they are recreated.

### Peer endpoints

Peers connect to each worker at its **SSH host** (provisioned workers) or **agent endpoint host** (manual agents). If that address isn't reachable from other workers, set a separate address when registering:

```sh
zela workers provision ... --network-endpoint 203.0.113.21
zela workers add ... --network-endpoint worker-2.public.example.com
```

The API equivalent is the `networkEndpoint` field on `POST /api/workers` and `POST /api/workers/provision`. For existing workers, use **Networking → Cluster network → Manage → Edit peer endpoint**, or `POST /api/workers/{id}/network-endpoint` with `{"endpoint":"worker.example.com"}`.

### Service names and VIPs

Each deployment gets a **stable virtual IP** from `10.200.255.0/24`, kept for the life of the deployment and released when it is deleted. Managed containers reach services by name:

| Name                                        | Resolves to                                                   |
| ------------------------------------------- | ------------------------------------------------------------- |
| `<deployment>.zela`, or just `<deployment>` | The deployment's **VIP**, which load-balances to its replicas |
| `<replica-container-name>.zela`             | That one replica's own IP                                     |

The VIP is shown in `GET /api/state` → `network.services` (keyed by deployment ID).

**How load balancing works** (like Kubernetes' kube-proxy in iptables mode):

- Every worker has a NAT chain, `ZELA-SVC`, jumped from `PREROUTING` and `OUTPUT`, that sends each **new connection** to a VIP to a random backend, with equal shares.
- **Backends** are replicas that are running and not `unhealthy`, on online workers.
- Translation happens on the **client's** host, so replies return through connection tracking and the backend sees the **real client address**.
- **Any port** on the VIP maps to the same port on the chosen replica.
- Host processes can use VIPs too.
- From a container, a connection to a VIP with no backends, or to an unassigned VIP, is **refused immediately**. From a host process, it times out instead, because WireGuard silently drops traffic that matches no peer.

**When replicas change.** Backend lists come from inventory, so a replica that stops or fails over leaves the rotation within **one inventory cycle, about 10 seconds**. During that window, new connections have a chance of reaching the dead replica. Clients should retry failed connections. Existing connections to a removed backend are not migrated.

```sh
# inside any managed container
wget -qO- http://api/            # short name, via the "zela" search domain
psql -h db.zela ...
```

How it works:

- **Record names:** deployment and container names are converted to DNS labels: lowercase, with `_` and `.` turned into `-`. For example `Web_API` becomes `web-api.zela`. Names that can't be converted aren't published.
- **Building the records:** the control plane builds them from inventory each cycle and sends them with the network configuration. Each worker answers from its local copy, so lookups keep working while managers are down.
- **Replica changes:** a replica that stops, is moved or is replaced after failover drops out of, or joins, the answers within about one inventory cycle (10 seconds). Answers have a **5-second TTL**.
- **Stable answers:** because service names resolve to the VIP, cached DNS answers stay valid while replicas move. Replica names rotate between addresses when there is more than one.
- **Other names:** names outside `.zela` are forwarded to the worker's own resolvers from `/etc/resolv.conf`, so internet names resolve as before.
- **Container settings:** new containers get `dns=<gateway>`, `dns-search=zela` and `ndots:1`. Single-label names try `.zela` first; dotted names are resolved as-is.
- **Access:** the resolver answers only clients inside the cluster range, or on the host itself, so it isn't an open resolver on your LAN. An `iptables` rule (chain `ZELA-NET-IN`) allows DNS from the bridge even on hosts with a default-deny input policy.
- **IPv6:** AAAA queries for cluster names return no records; the network is IPv4 only.

Existing containers pick up DNS only after they are recreated, for example through an image rollout. **Restart** only restarts existing containers and does not change their network or DNS configuration.

### Changing or disabling

- **Disable:** remove managed deployments and attached containers first, then turn off the mesh in **Configure network**. For startup-managed configuration, restart every manager with an explicit empty range (`--cluster-network ''`) and edge listeners disabled. Online workers remove `zela0` and the firewall chains. The `zela` Docker network is removed once no containers use it. When every member has torn down, the network state is deleted. Offline members keep teardown pending until they return.
- **Change the range or UDP port:** disable first, wait until `network` disappears from `/api/state`, then enable the new configuration and recreate workloads. Starting a manager with a different range while the old one is in use is refused.
- **Unregistering a worker** removes it from every peer list immediately, so it can no longer exchange traffic, but its own `zela0` interface stays until you remove the agent.

### Limits and caveats

- At most **254 workers**, and a `/24` (about 250 containers) per worker.
- A full mesh: every worker peers with every other. Fine for tens of workers; hundreds of workers mean many tunnels.
- Hosts behind NAT need a reachable `--network-endpoint` or port forward. There's no relay.
- Network policy is flat: any container may reach any other container in the cluster. Separate untrusted workloads onto separate clusters.
- IPv4 only.
- Service names and VIPs cover **managed deployments** only. Gateway-created containers aren't published.
- Load balancing is random per connection. There's no session affinity, no weighting and no per-request (L7) balancing.
- A replica that connects to its **own** service VIP and is chosen as its own backend may fail (no hairpin NAT). Use `localhost` for self-calls.
- Containers created through the Docker gateway (for example by Zela) aren't attached automatically. With an administrator token and an explicit worker selector, a client can join them with `HostConfig.NetworkMode: "zela"`. Scoped integration tokens can't select custom networks.

## Edge proxy

An optional HTTP(S) reverse proxy built into every worker agent. It routes external requests by **host name and path** to a deployment's service VIP, so it gets the VIP's load balancing and its failover handling (dead replicas drop out within one inventory cycle). **Every worker serves every route**, wherever the replicas run. Point DNS or an external load balancer at any or all workers.

```text
client ──► any worker :80/:443 ──(Host + path)──► service VIP ──► healthy replica (any worker)
```

### Enable it

It requires the cluster network.

```sh
zela-server --cluster-network 10.200.0.0/16 --edge-http-port 80 --edge-https-port 443 ...
# or ZELA_EDGE_HTTP_PORT / ZELA_EDGE_HTTPS_PORT; set both to the same values on every HA manager
```

- **HTTP** works on every worker straight away.
- **HTTPS** uses certificates from [automatic certificates (ACME)](#automatic-certificates-acme), a certificate stored **on each worker** (which can be a wildcard such as `*.example.com`), or both. Each connection gets the certificate matching its requested host name, and ACME certificates take precedence.
  ```sh
  zela-worker ... --edge-tls-cert /etc/zela/edge.crt --edge-tls-key /etc/zela/edge.key
  ```
  - Keep the key readable only by the agent (`0640 root:docker`, or inside its state directory).
  - Private keys are never sent through the control plane.
  - A renewed certificate is loaded when the agent restarts.
  - A worker with no certificate at all keeps serving HTTP and reports an `edgeError` in `GET /api/state` → `network.members`, but stays a normal, schedulable worker.
  - **`--edge-redirect-https`** answers HTTP with `308` to the HTTPS URL for hosts that have a certificate. Other hosts and ACME challenges stay on HTTP.
- **Firewall:** SSH provisioning and **Update agent** open the edge ports in `firewalld`/`ufw` when they're active. On other hosts, open TCP 80/443 yourself.

### Automatic certificates (ACME)

The control plane can obtain and renew certificates from Let's Encrypt, or any ACME CA, for every **exact host name** in your routes:

```sh
zela-server --cluster-network 10.200.0.0/16 \
  --edge-http-port 80 --edge-https-port 443 --edge-redirect-https \
  --acme-email ops@example.com          # ZELA_ACME_EMAIL; optional --acme-directory (default Let's Encrypt production)
```

Requirements:

- `--edge-http-port 80` (the CA validates over port 80) and an HTTPS port.
- **Public DNS** for each host pointing at your workers.
- **Inbound TCP 80** from the internet to those workers.

How it works:

- The CA's validation request may arrive at **any** worker. The control plane therefore sends the HTTP-01 challenge to every worker, and asks the CA to validate only after every online worker has applied it.
- **One certificate per host name** is shared by all workers, which keeps you inside CA rate limits.
- **Renewal** starts 30 days before expiry.
- **Failures** are retried with backoff (1, 3, 9 and 27 minutes, then hourly). Errors are shown in `GET /api/state` → `acmeCertificates`.
- **Workers** pick up new and renewed certificates on the fly, with no restart.
- **Wildcard routes** (`*.example.com`) and the catch-all aren't issued automatically; wildcards need the DNS-01 challenge. Use a local `--edge-tls-cert` for them.

**Key handling (differs from local certificates):**

- **Storage:** ACME private keys are generated on the control plane and **sealed with the vault master key** (AES-256-GCM) in its state. The ACME account key is stored the same way.
- **Delivery:** keys travel to workers over the authenticated agent channel. Workers keep them in their private network state file (`network.json`, mode `0600`).
- **Exposure:** `/api/state` shows only host, expiry and error.

To test against a staging CA without rate limits, use `--acme-directory https://acme-staging-v02.api.letsencrypt.org/directory` (browsers won't trust the result). For a CA with a private root, add that root to the manager's trust store (`SSL_CERT_FILE`).

### Publish a deployment

Add `routes` when creating a deployment:

```json
{
  "name": "shop",
  "image": "ghcr.io/acme/shop:1.4",
  "replicas": 3,
  "memoryMB": 256,
  "cpus": 0.5,
  "routes": [
    { "host": "shop.example.com", "port": 8080 },
    { "host": "example.com", "path": "/api", "port": 8080 }
  ]
}
```

Or replace the routes of an existing deployment. This takes effect within one inventory cycle and **doesn't recreate containers**:

```sh
zela api PUT /api/deployments/dep_ID/routes --file routes.json   # {"routes":[...]}
```

| Field  | Meaning                                                                                               |
| ------ | ----------------------------------------------------------------------------------------------------- |
| `host` | Exact host name, `*.example.com` (one label), or empty to match any host                              |
| `path` | Path prefix, matched on whole segments (`/api` matches `/api` and `/api/x`, not `/apis`); default `/` |
| `port` | **Container** port to send to (no published port needed)                                              |

**Matching order:** exact hosts first, then wildcards, then the catch-all, and the longest path within each. No match returns `404`; a service with no healthy replicas returns `502`. A host and path can belong to only one deployment.

### What the backend sees

- **`Host`:** the original public host is preserved.
- **`X-Forwarded-For`:** the client address.
- **`X-Forwarded-Host`** and **`X-Forwarded-Proto`:** the public host, and `http` or `https`.
- **Connection source address:** the edge worker's mesh address. Use `X-Forwarded-For` for the client address.
- **Protocols:** WebSockets and other HTTP upgrades are passed through, streaming responses are flushed immediately, and there's no response-size limit or write timeout.
- **TLS:** HTTPS is terminated at the edge, so the backend sees plain HTTP.

### Limits

- **ACME uses HTTP-01 only:** no wildcard certificates, and the CA must reach port 80 on the workers.
- **No rate limiting, authentication, header rules or sticky sessions.** Put a dedicated proxy in front if you need them.
- **HTTP only:** no plain-TCP or UDP routing. Use published ports for non-HTTP services.
- **Client address on the connection:** backends see the edge worker's mesh address as the connection source; the client address is only in `X-Forwarded-For`.
- **Shared ports:** each worker listens on the edge ports on all interfaces, so they can't also be published by containers.

## Roadmap

Planned, not yet implemented:

1. ~~Service names~~ — done, see [Service names](#service-names-and-vips).
2. ~~Stable service virtual IPs~~ — done, see [Service names and VIPs](#service-names-and-vips).
3. ~~Built-in edge proxy~~ — done, see [Edge proxy](#edge-proxy), including [ACME certificates](#automatic-certificates-acme) and HTTPS redirects. Next: DNS-01 for wildcard certificates.
4. **Network policy** between deployments.
5. **Faster backend removal**, using worker-local container health instead of the inventory cycle.
