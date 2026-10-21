"use strict";
let networkTab = "cluster",
  networkWorker = "",
  networkView = null,
  networkViewBusy = false,
  networkViewLoaded = 0,
  networkViewError = "",
  networkViewSequence = 0,
  dockerNetworks = null,
  dockerNetworksBusy = false,
  dockerNetworksLoaded = 0,
  dockerNetworksError = "",
  dockerNetworksSequence = 0,
  dockerNetworksWorker = "";
function resetNetworkViews() {
  networkView = null;
  networkViewLoaded = 0;
  networkViewBusy = false;
  networkViewError = "";
  dockerNetworks = null;
  dockerNetworksLoaded = 0;
  dockerNetworksBusy = false;
  dockerNetworksError = "";
  networkWorker = "";
  dockerNetworksWorker = "";
  networkViewSequence++;
  dockerNetworksSequence++;
}
async function fetchNetworkView(force = false) {
  if (
    !state ||
    page !== "network" ||
    networkViewBusy ||
    (!force && Date.now() - networkViewLoaded < 10000)
  )
    return;
  const seq = ++networkViewSequence;
  networkViewBusy = true;
  try {
    const result = await api("/network");
    if (seq === networkViewSequence) {
      networkView = result;
      networkViewError = "";
    }
  } catch (err) {
    if (seq === networkViewSequence) networkViewError = err.message;
  } finally {
    if (seq === networkViewSequence) {
      networkViewBusy = false;
      networkViewLoaded = Date.now();
      if (state && page === "network") render();
    }
  }
}
async function fetchDockerNetworks(force = false) {
  if (!state || !networkWorker || page !== "network") return;
  const worker = networkWorker;
  if (dockerNetworksBusy && dockerNetworksWorker === worker) return;
  if (
    !force &&
    dockerNetworksWorker === worker &&
    Date.now() - dockerNetworksLoaded < 10000
  )
    return;
  const seq = ++dockerNetworksSequence;
  dockerNetworksBusy = true;
  dockerNetworksWorker = worker;
  try {
    const result = await api(`/workers/${encodeURIComponent(worker)}/networks`);
    if (seq === dockerNetworksSequence) {
      dockerNetworks = result;
      dockerNetworksError = "";
    }
  } catch (err) {
    if (seq === dockerNetworksSequence) {
      dockerNetworks = null;
      dockerNetworksError = err.message;
    }
  } finally {
    if (seq === dockerNetworksSequence) {
      dockerNetworksBusy = false;
      dockerNetworksLoaded = Date.now();
      if (state && page === "network") render();
    }
  }
}
function networkMemberStatus(worker) {
  const n = state.network,
    member = n?.members?.[worker.id];
  if (!member)
    return n?.enabled
      ? ["stopped", "Not joined"]
      : ["stopped", "Not configured"];
  if (!n.enabled) return ["paused", "Removing"];
  if (worker.status !== "online") return ["offline", "Worker offline"];
  if (member.error) return ["failed", "Apply failed"];
  const reported = networkView?.members?.[worker.id];
  if (reported?.applied === member.applied && reported.status === "applied")
    return ["online", "Config applied"];
  return ["paused", "Pending apply"];
}
function networkSummary() {
  const n = state.network,
    joined = Object.keys(n?.members || {}).length;
  const values = [
    [
      "Cluster range",
      n?.cidr || "Not configured",
      "Private IPv4 network",
      "globe",
    ],
    [
      "WireGuard",
      n?.enabled ? `UDP ${n.port}` : "Disabled",
      "Encrypted worker mesh",
      "shield",
    ],
    [
      "Worker members",
      joined,
      `${workers().length} registered workers`,
      "server",
    ],
    [
      "Service VIPs",
      Object.keys(n?.services || {}).length,
      "Stable deployment addresses",
      "layers",
    ],
  ];
  return `<section class="metrics network-metrics" aria-label="Network summary">${values.map(([label, value, note, ico]) => `<article class="metric"><div class="metric-label">${label}${icon(ico)}</div><div class="network-metric-value">${esc(value)}</div><div class="network-metric-note">${esc(note)}</div></article>`).join("")}</section>`;
}
function networkingPage() {
  if (!state.workers[networkWorker]) networkWorker = workers()[0]?.id || "";
  void fetchNetworkView();
  const tabs = {
    cluster: "Cluster network",
    workers: "Worker networks",
    services: "Services & routes",
  };
  return `${pageHead("Networking", "Inspect connectivity, manage worker networks, and publish application routes.", "none")}${networkSummary()}<div class="settings-tabs" role="tablist" aria-label="Networking sections">${Object.entries(
    tabs,
  )
    .map(
      ([id, label]) =>
        `<button role="tab" id="network-tab-${id}" aria-selected="${networkTab === id}" aria-controls="network-content" tabindex="${networkTab === id ? 0 : -1}" data-action="network-tab" data-tab="${id}" class="${networkTab === id ? "active" : ""}">${label}</button>`,
    )
    .join(
      "",
    )}</div>${networkViewError ? `<div class="notice warn" role="alert">${esc(networkViewError)} ${btn("Retry", "network-reload", "", "text", "refresh")}</div>` : ""}<div id="network-content" role="tabpanel" aria-labelledby="network-tab-${networkTab}">${networkTab === "cluster" ? clusterNetworkPanel() : networkTab === "workers" ? workerNetworksPanel() : networkServicesPanel()}</div>`;
}
function clusterNetworkPanel() {
  const n = state.network;
  const ready = workers().filter(
    (w) => networkMemberStatus(w)[1] === "Config applied",
  ).length;
  const config = `<section class="panel"><div class="panel-head"><div class="panel-title"><h2>Encrypted cluster mesh</h2>${badge(n?.enabled ? "online" : n ? "paused" : "stopped", n?.enabled ? "Enabled" : n ? "Tearing down" : "Disabled")}</div>${btn("Configure network", "network-config", state.demo || busy() ? "disabled" : "", "", "settings")}</div><div class="settings-body"><p>WireGuard links worker hosts. Each member receives a container subnet, and managed deployments use the <code>zela</code> Docker network.</p><dl><dt>Cluster address range</dt><dd class="mono">${esc(n?.cidr || "Not configured")}</dd><dt>Worker tunnel port</dt><dd>${n?.enabled ? esc(n.port) + " / UDP" : "—"}</dd><dt>Interface / bridge</dt><dd><code>zela0</code> / <code>zelabr0</code></dd><dt>Applied configuration</dt><dd>${ready} of ${Object.keys(n?.members || {}).length} members</dd><dt>Edge listeners</dt><dd>${n?.edgeHTTPPort ? `HTTP :${n.edgeHTTPPort}` : "HTTP disabled"} · ${n?.edgeHTTPSPort ? `HTTPS :${n.edgeHTTPSPort}` : "HTTPS disabled"}</dd><dt>Configuration source</dt><dd>${n?.consoleManaged ? "Saved through the admin console" : "Manager startup configuration"}</dd></dl><p class="note">Applied means the agent accepted its configuration. It does not verify WireGuard handshakes or end-to-end reachability.</p>${state.demo ? '<div class="notice">The encrypted mesh requires live Linux agents. Worker Docker network controls below use simulated Docker engines in this demo.</div>' : !n ? '<div class="notice">Enable the mesh to give managed containers private addresses across workers. Existing containers are not moved automatically; recreate them after workers join.</div>' : ""}</div></section>`;
  return `${config}<section class="panel settings-section-spacer"><div class="panel-head"><div class="panel-title"><h2>Worker connectivity</h2><span class="total-count">${workers().length}</span></div>${btn("Reapply all", "network-reapply", !n || state.demo || busy() ? "disabled" : "", "", "refresh")}</div>${
    workers().length
      ? `<div class="table-wrap"><table class="network-members"><thead><tr><th scope="col">WORKER</th><th scope="col">MESH STATUS</th><th scope="col">PEER ENDPOINT</th><th scope="col">TUNNEL ADDRESS</th><th scope="col">CONTAINER SUBNET</th><th scope="col">LAST APPLY</th><th scope="col"></th></tr></thead><tbody>${workers()
          .filter((w) =>
            match(
              w.name,
              w.networkEndpoint,
              w.endpoint,
              n?.members?.[w.id]?.address,
            ),
          )
          .map((w) => {
            const m = n?.members?.[w.id],
              status = networkMemberStatus(w);
            return `<tr><td><button class="name-link" data-action="network-worker-details" data-worker="${esc(w.id)}">${esc(w.name)}</button><div class="small-sub">${esc(w.region || "No region")}</div></td><td>${badge(...status)}${m?.error ? `<div class="small-sub network-error">${esc(m.error)}</div>` : ""}${m?.edgeError ? `<div class="small-sub network-error">Edge: ${esc(m.edgeError)}</div>` : ""}</td><td class="mono">${esc(networkView?.members?.[w.id]?.endpoint || w.networkEndpoint || w.ssh?.host || "Default from agent address")}</td><td class="mono">${esc(m?.address || "—")}</td><td class="mono">${esc(m?.subnet || "—")}</td><td class="table-time">${timeLabel(m?.updatedAt)}</td><td>${btn("Manage", "network-worker-details", `data-worker="${esc(w.id)}"`, "compact", "chevron")}</td></tr>`;
          })
          .join("")}</tbody></table></div>`
      : empty(
          "No workers connected",
          "Connect a worker to inspect its networking.",
          "new-worker",
          "Connect worker",
        )
  }</section>`;
}
function workerNetworksPanel() {
  if (!networkWorker)
    return empty(
      "No workers connected",
      "Connect a worker before creating Docker networks.",
      "new-worker",
      "Connect worker",
    );
  void fetchDockerNetworks();
  const w = state.workers[networkWorker],
    h = w.metrics?.host;
  const loaded = dockerNetworks?.workerID === networkWorker,
    list = loaded ? dockerNetworks.networks || [] : [];
  const fresh = metricsFresh(w);
  return `<div class="network-worker-toolbar"><label for="network-worker">Worker<select id="network-worker">${workers()
    .map(
      (w) =>
        `<option value="${esc(w.id)}" ${w.id === networkWorker ? "selected" : ""}>${esc(w.name)} · ${esc(w.status)}</option>`,
    )
    .join(
      "",
    )}</select></label><div class="head-actions">${btn("Reload networks", "docker-networks-reload", dockerNetworksBusy ? "disabled" : "", "", "refresh")}${btn("Create bridge network", "docker-network-create", busy() || w.status !== "online" ? "disabled" : "", "primary", "plus")}</div></div><div class="network-traffic"><span>Host network RX <b>${fresh ? metricRate(h?.network?.readBps) : "—"}</b></span><span>TX <b>${fresh ? metricRate(h?.network?.writeBps) : "—"}</b></span><span>${fresh ? `Measured ${relative(w.metrics.time)}` : "Host traffic measurements unavailable or stale"}</span></div>${dockerNetworksError ? `<div class="notice warn" role="alert">${esc(dockerNetworksError)}</div>` : ""}<section class="panel"><div class="panel-head"><div class="panel-title"><h2>Docker networks</h2><span class="total-count">${list.length}</span></div><span class="small-sub">${loaded ? `Fetched ${relative(dockerNetworks.observedAt)}` : dockerNetworksBusy ? "Loading from worker…" : "Live worker inventory"}</span></div>${
    list.length
      ? `<div class="table-wrap"><table><thead><tr><th scope="col">NETWORK</th><th scope="col">DRIVER / SCOPE</th><th scope="col">SUBNET</th><th scope="col">GATEWAY</th><th scope="col">ACCESS</th><th scope="col"></th></tr></thead><tbody>${list
          .filter((n) =>
            match(
              n.name,
              n.id,
              n.driver,
              ...(n.ipam?.config || []).map((v) => v.subnet),
            ),
          )
          .map(
            (n) =>
              `<tr><td><button class="name-link" data-action="docker-network-inspect" data-id="${esc(n.id)}" data-worker="${esc(w.id)}">${esc(n.name)}</button><div class="small-sub mono">${esc(shortID(n.id))}</div></td><td>${esc(n.driver)}<div class="small-sub">${esc(n.scope)}</div></td><td class="mono">${esc((n.ipam?.config || []).map((v) => v.subnet).join(", ") || "Automatic / none")}</td><td class="mono">${esc(
                (n.ipam?.config || [])
                  .map((v) => v.gateway)
                  .filter(Boolean)
                  .join(", ") || "—",
              )}</td><td>${badge(n.internal ? "paused" : "stopped", n.internal ? "Internal" : "External access")}${["zela", "host", "none", "bridge"].includes(n.name) ? '<div class="small-sub">Protected network</div>' : ""}</td><td>${btn("Inspect", "docker-network-inspect", `data-id="${esc(n.id)}" data-worker="${esc(w.id)}"`, "compact", "chevron")}</td></tr>`,
          )
          .join("")}</tbody></table></div>`
      : empty(
          dockerNetworksBusy
            ? "Loading worker networks…"
            : dockerNetworksError
              ? "Worker networks unavailable"
              : "No Docker networks returned",
          dockerNetworksBusy
            ? "Reading network configuration from the selected Docker engine."
            : "Use Reload networks to fetch current worker inventory.",
        )
  }</section><p class="note">Bridge networks belong to this worker only. The encrypted cluster mesh is managed separately. Host network interfaces, OS routes, and firewall policy are not edited by these Docker network controls.</p>`;
}
function networkServicesPanel() {
  const n = state.network,
    list = deployments().filter((d) =>
      match(d.name, ...(d.routes || []).map((r) => r.host)),
    );
  return `${!n?.enabled ? '<div class="notice">Cluster service DNS and edge routing require the encrypted cluster network. Published host ports remain available without it.</div>' : ""}<section class="panel"><div class="panel-head"><h2>Service discovery & published routes</h2><span class="small-sub">${n?.edgeHTTPPort || n?.edgeHTTPSPort ? "Edge listeners configured" : "Edge listeners disabled"}</span></div>${
    list.length
      ? `<div class="table-wrap"><table><thead><tr><th scope="col">DEPLOYMENT</th><th scope="col">SERVICE VIP</th><th scope="col">CLUSTER DNS</th><th scope="col">PUBLISHED ROUTES</th><th scope="col"></th></tr></thead><tbody>${list
          .map((d) => {
            const vip = n?.services?.[d.id]?.vip,
              records = networkView?.records || {};
            const dns = Object.entries(records)
              .filter(([_, ips]) => vip && ips.includes(vip))
              .map(([name]) => name + ".zela");
            return `<tr><td><button class="name-link" data-action="deployment-details" data-id="${esc(d.id)}">${esc(d.name)}</button><div class="small-sub">${d.replicas} replicas</div></td><td class="mono">${esc(vip || "Pending allocation")}</td><td class="mono">${esc(dns.join(", ") || "Not published")}</td><td>${(d.routes || []).map((r, i) => `<div class="network-route"><code>${esc(r.host || "Any host")}${esc(r.path || "/")}</code><span>→ :${r.port}</span>${btn("Edit", "network-route-edit", `data-id="${esc(d.id)}" data-index="${i}"`, "text", "settings")}${btn("Remove", "network-route-remove", `data-id="${esc(d.id)}" data-index="${i}"`, "text link-danger", "trash")}</div>`).join("") || '<span class="small-sub">No edge routes</span>'}</td><td>${btn("Add route", "network-route-add", `data-id="${esc(d.id)}" ${busy() ? "disabled" : ""}`, "compact", "plus")}</td></tr>`;
          })
          .join("")}</tbody></table></div>`
      : empty(
          "No deployments found",
          "Create a deployment to configure its external routes.",
          "new-deployment",
          "Deploy application",
        )
  }</section>${networkCertificatesPanel()}`;
}
function networkCertificatesPanel() {
  const certificates =
    networkView?.certificates || state.acmeCertificates || [];
  return `<section class="panel settings-section-spacer"><div class="panel-head"><h2>Edge certificates</h2><span class="small-sub">${networkView?.automaticCertificates ? "Automatic ACME issuance configured" : "TLS certificates are configured on the manager or workers"}</span></div>${
    certificates.length
      ? `<div class="table-wrap"><table><thead><tr><th scope="col">HOST</th><th scope="col">EXPIRY</th><th scope="col">STATUS</th></tr></thead><tbody>${certificates
          .map((cert) => {
            const expiry = new Date(cert.notAfter),
              issued =
                Number.isFinite(expiry.getTime()) && expiry.getTime() > 0,
              expired = issued && expiry.getTime() <= Date.now();
            return `<tr><td>${esc(cert.host || cert.domain)}</td><td>${issued ? `<time datetime="${esc(expiry.toISOString())}">${esc(expiry.toLocaleString())}</time>` : "Not issued"}</td><td>${cert.error ? badge("failed", "Issuance error") + `<div class="small-sub network-error">${esc(cert.error)}</div>` : !issued ? badge("paused", "Pending issuance") : expired ? badge("failed", "Expired") : badge("online", "Certificate available")}</td></tr>`;
          })
          .join("")}</tbody></table></div>`
      : empty(
          "No issued certificates reported",
          "Certificate issuance and renewal are configured on the manager; worker edge errors appear under Cluster network.",
        )
  }</section>`;
}
function networkConfigDialog() {
  const n = state.network,
    locked = Object.keys(n?.members || {}).length > 0;
  openModal(
    "Configure cluster network",
    "Save the desired mesh and edge-listener configuration.",
    `<div class="form-group"><label class="checkbox-label"><input name="enabled" type="checkbox" ${n?.enabled ? "checked" : ""}>Enable the encrypted cluster network</label></div><div class="form-grid"><div class="form-group"><label for="network-cidr">Private IPv4 cluster range</label><input id="network-cidr" name="cidr" value="${esc(n?.cidr || "10.200.0.0/16")}" required ${locked ? "readonly" : ""}><small>A private /16 range that does not overlap host, VPN, or existing Docker networks.</small></div><div class="form-group"><label for="network-port">WireGuard UDP port</label><input id="network-port" name="port" type="number" min="1" max="65535" value="${n?.port || 51820}" required ${locked ? "readonly" : ""}></div><div class="form-group"><label for="edge-http-port">Edge HTTP port</label><input id="edge-http-port" name="edgeHTTPPort" type="number" min="0" max="65535" value="${n?.edgeHTTPPort || 0}" required><small>0 disables this listener.</small></div><div class="form-group"><label for="edge-https-port">Edge HTTPS port</label><input id="edge-https-port" name="edgeHTTPSPort" type="number" min="0" max="65535" value="${n?.edgeHTTPSPort || 0}" required><small>Requires worker TLS certificates or manager ACME configuration.</small></div></div><div class="form-group"><label for="edge-drain-timeout">Request drain timeout (seconds)</label><input id="edge-drain-timeout" name="edgeDrainTimeoutSeconds" type="number" min="1" max="240" value="${n?.edgeDrainTimeoutSeconds || 60}" required><small>A move pauses if previous requests exceed this deadline.</small></div><div class="form-group"><label class="checkbox-label"><input name="redirectHTTPS" type="checkbox" ${n?.edgeRedirectHTTPS ? "checked" : ""}>Redirect HTTP to HTTPS for hosts with certificates</label></div><div class="notice warn">Workers need WireGuard, agent network capabilities, and reachable peer ports. Update SSH-managed agents after changing network requirements; prepare manual agents on their hosts. Existing containers are not reattached automatically. Remove managed deployments and attached containers before disabling the mesh.</div><p class="note">Console settings persist across restarts. Explicit networking flags or environment variables on a manager override them at startup. ${locked ? "Remove the existing network and complete teardown before changing its range or UDP port." : ""}</p>`,
    `${btn("Cancel", "close")}<button class="btn primary" type="submit">Save network configuration</button>`,
    "network-config-form",
    true,
  );
}
function peerEndpointDialog(id) {
  const w = state.workers[id];
  if (!w) return;
  openModal(
    "Worker peer endpoint",
    `Set the address other workers use to reach ${w.name}.`,
    `<input type="hidden" name="workerID" value="${esc(id)}"><div class="form-group"><label for="network-peer-endpoint">Hostname or IP address</label><input id="network-peer-endpoint" name="endpoint" value="${esc(w.networkEndpoint || "")}" placeholder="worker-1.example.com"><small>Leave empty to use the SSH host or agent endpoint host. Do not include a scheme or port.</small></div><div class="notice">Changing this address updates mesh peer configuration. The management endpoint is unchanged. The address must be reachable directly from the other workers.</div>`,
    `${btn("Cancel", "close")}<button class="btn primary" type="submit">Save peer endpoint</button>`,
    "network-peer-form",
  );
}
function networkWorkerDialog(id) {
  const w = state.workers[id];
  if (!w) return;
  const m = state.network?.members?.[id];
  openModal(
    "Worker networking",
    w.name,
    `<div class="details-meta"><div><small>MESH STATUS</small>${badge(...networkMemberStatus(w))}</div><div><small>TUNNEL ADDRESS</small><b class="mono">${esc(m?.address || "Not allocated")}</b></div><div><small>CONTAINER SUBNET</small><b class="mono">${esc(m?.subnet || "Not allocated")}</b></div></div><dl class="network-key-details"><dt>Peer endpoint override</dt><dd>${esc(w.networkEndpoint || "Default from worker address")}</dd><dt>Worker public key</dt><dd class="mono">${esc(m?.publicKey || "Not reported")}</dd><dt>Last apply attempt</dt><dd>${timeLabel(m?.updatedAt)}</dd></dl>${m?.error ? `<div class="notice warn">${esc(m.error)}</div>` : ""}${m?.edgeError ? `<div class="notice warn">Edge proxy: ${esc(m.edgeError)}</div>` : ""}<p class="note">Private WireGuard keys stay on the worker. Applied configuration is not a peer handshake check.</p>`,
    `${btn("Docker networks", "worker-network-link", `data-worker="${esc(id)}"`, "", "globe")}${btn("Edit peer endpoint", "network-peer-edit", `data-worker="${esc(id)}" ${busy() ? "disabled" : ""}`, "", "settings")}${m ? btn("Reapply", "network-reapply", `data-worker="${esc(id)}" ${busy() || state.demo ? "disabled" : ""}`, "", "refresh") : ""}${w.ssh ? btn("Update agent", "agent-update-dialog", `data-id="${esc(id)}" ${busy() ? "disabled" : ""}`, "", "refresh") : ""}`,
    "",
    true,
  );
}
function createDockerNetworkDialog() {
  const w = state.workers[networkWorker];
  if (!w) return;
  openModal(
    "Create bridge network",
    `Create a local Docker bridge on ${w.name}.`,
    `<input type="hidden" name="workerID" value="${esc(w.id)}"><div class="form-group"><label for="docker-network-name">Network name</label><input id="docker-network-name" name="name" required maxlength="63" pattern="[a-zA-Z0-9][a-zA-Z0-9_.-]*" placeholder="application-backend"></div><div class="form-grid"><div class="form-group"><label for="docker-network-subnet">Subnet (optional)</label><input id="docker-network-subnet" name="subnet" placeholder="172.28.0.0/24"><small>Leave blank for Docker-managed IP allocation.</small></div><div class="form-group"><label for="docker-network-gateway">Gateway (optional)</label><input id="docker-network-gateway" name="gateway" placeholder="172.28.0.1"></div></div><div class="form-group"><label class="checkbox-label"><input name="internal" type="checkbox">Internal network (no external routing)</label></div><div class="notice">This bridge exists only on the selected worker. Choose a non-overlapping private subnet. Existing containers are unchanged until attached.</div>`,
    `${btn("Cancel", "close")}<button class="btn primary" type="submit">Create network</button>`,
    "docker-network-form",
  );
}
function standaloneOnWorker(worker, id) {
  return !deployments().some((d) =>
    (d.tasks || []).some((t) => t.workerID === worker && t.id === id),
  );
}
async function inspectDockerNetwork(worker, id) {
  const n = await api(
    `/workers/${encodeURIComponent(worker)}/networks/${encodeURIComponent(id)}`,
  );
  if (!state) return;
  const protectedNetwork =
    ["bridge", "host", "none", "zela"].includes(n.name) ||
    n.driver !== "bridge" ||
    n.scope !== "local";
  openModal(
    "Docker network",
    `${n.name} · ${state.workers[worker]?.name || worker}`,
    `<div class="details-meta"><div><small>DRIVER / SCOPE</small><b>${esc(n.driver)} / ${esc(n.scope)}</b></div><div><small>SUBNET</small><b class="mono">${esc((n.ipam?.config || []).map((v) => v.subnet).join(", ") || "Automatic / none")}</b></div><div><small>EXTERNAL ROUTING</small><b>${n.internal ? "Internal only" : "Available"}</b></div></div><section class="panel"><div class="panel-head"><h2>Connected containers</h2><span class="total-count">${Object.keys(n.containers || {}).length}</span></div>${
      Object.keys(n.containers || {}).length
        ? `<div class="table-wrap"><table><thead><tr><th scope="col">CONTAINER</th><th scope="col">IPv4</th><th scope="col"></th></tr></thead><tbody>${Object.entries(
            n.containers,
          )
            .map(
              ([id, ct]) =>
                `<tr><td>${esc(ct.name || shortID(id))}<div class="small-sub mono">${esc(shortID(id))}</div></td><td class="mono">${esc(ct.ipv4Address || "Not reported")}</td><td>${!protectedNetwork && standaloneOnWorker(worker, id) ? btn("Disconnect", "docker-network-disconnect", `data-id="${esc(n.id)}" data-worker="${esc(worker)}" data-container="${esc(id)}" ${busy() ? "disabled" : ""}`, "text link-danger", "close") : ""}</td></tr>`,
            )
            .join("")}</tbody></table></div>`
        : empty(
            "No containers attached",
            "Attach a standalone container to use this network.",
          )
    }</section>${protectedNetwork ? '<div class="notice">Built-in, cluster-managed, and non-local bridge networks are read-only here.</div>' : ""}`,
    `${btn("Close", "close")}${!protectedNetwork ? btn("Connect container", "docker-network-connect", `data-id="${esc(n.id)}" data-worker="${esc(worker)}" ${busy() ? "disabled" : ""}`, "primary", "plus") + btn("Delete network", "docker-network-delete", `data-id="${esc(n.id)}" data-worker="${esc(worker)}" data-name="${esc(n.name)}" ${busy() || Object.keys(n.containers || {}).length ? "disabled" : ""}`, "danger", "trash") : ""}`,
    "",
    true,
  );
}
function networkContainerDialog(worker, id, disconnect = "") {
  const w = state.workers[worker];
  if (!w) return;
  const list = (w.containers || []).filter((ct) =>
    standaloneOnWorker(worker, ct.Id),
  );
  openModal(
    disconnect ? "Disconnect container" : "Connect container",
    disconnect
      ? "This removes a container's connectivity through this Docker network."
      : "Attach a standalone container on this worker.",
    `<input type="hidden" name="workerID" value="${esc(worker)}"><input type="hidden" name="networkID" value="${esc(id)}"><input type="hidden" name="action" value="${disconnect ? "disconnect" : "connect"}">${disconnect ? `<input type="hidden" name="containerID" value="${esc(disconnect)}"><div class="notice warn">Disconnect ${esc(shortID(disconnect))} from this network. Existing connections through this network will be interrupted.</div>` : list.length ? `<div class="form-group"><label for="network-container">Standalone container</label><select id="network-container" name="containerID">${list.map((ct) => `<option value="${esc(ct.Id)}">${esc(ct.Names?.[0] || shortID(ct.Id))}</option>`).join("")}</select></div>` : '<div class="notice">No standalone containers are available. Deployment-managed containers keep their declared network configuration.</div>'}`,
    `${btn("Cancel", "close")}${disconnect || list.length ? '<button type="submit" class="btn primary">' + (disconnect ? "Disconnect container" : "Connect container") + "</button>" : ""}`,
    "docker-network-container-form",
  );
}
function deleteDockerNetworkDialog(worker, id, name) {
  openModal(
    "Delete Docker network",
    `Remove ${name} from ${state.workers[worker]?.name || worker}.`,
    `<div class="notice warn">This deletes the network configuration on the selected worker. All containers must be disconnected first. Built-in and cluster-managed networks cannot be deleted here.</div>`,
    `${btn("Cancel", "close")}${btn("Delete network", "docker-network-delete-confirm", `data-worker="${esc(worker)}" data-id="${esc(id)}"`, "danger", "trash")}`,
  );
}
function routeDialog(id, index = -1, remove = false) {
  const d = state.deployments[id];
  if (!d) return;
  const route = d.routes?.[index] || { host: "", path: "/", port: 80 };
  openModal(
    remove
      ? "Remove edge route"
      : index < 0
        ? "Add edge route"
        : "Edit edge route",
    d.name,
    `<input type="hidden" name="deploymentID" value="${esc(id)}"><input type="hidden" name="index" value="${index}"><input type="hidden" name="remove" value="${remove}"><input type="hidden" name="baseRoutes" value="${esc(JSON.stringify(d.routes || []))}">${remove ? `<div class="notice warn">Remove ${esc(route.host || "Any host")}${esc(route.path || "/")} from this deployment. Workers apply the change on the next inventory cycle.</div>` : `<div class="form-group"><label for="route-host">Hostname (optional)</label><input id="route-host" name="host" value="${esc(route.host || "")}" placeholder="app.example.com"><small>Leave empty to match any host. Point DNS to reachable worker edge addresses.</small></div><div class="form-grid"><div class="form-group"><label for="route-path">Path prefix</label><input id="route-path" name="path" value="${esc(route.path || "/")}" required></div><div class="form-group"><label for="route-port">Container port</label><input id="route-port" name="port" type="number" min="1" max="65535" value="${route.port}" required></div></div><div class="notice">The edge proxy forwards to this deployment's service VIP. A configured mesh and HTTP/HTTPS listener are required to serve the route. Saving a route does not create public DNS records or enable a listener.</div>`}`,
    `${btn("Cancel", "close")}<button type="submit" class="btn ${remove ? "danger" : "primary"}">${remove ? "Remove route" : "Save route"}</button>`,
    "network-route-form",
  );
}
