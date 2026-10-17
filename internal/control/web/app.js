"use strict";
const $ = (s, root = document) => root.querySelector(s);
const esc = (v) =>
  String(v ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
const paths = {
  bell: '<path d="M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9M10 21h4"/>',
  grid: '<rect x="3" y="3" width="7" height="7" rx="1.3"/><rect x="14" y="3" width="7" height="7" rx="1.3"/><rect x="3" y="14" width="7" height="7" rx="1.3"/><rect x="14" y="14" width="7" height="7" rx="1.3"/>',
  server:
    '<rect x="3" y="3" width="18" height="7" rx="2"/><rect x="3" y="14" width="18" height="7" rx="2"/><path d="M7 6.5h.01M7 17.5h.01M16 6.5h2M16 17.5h2"/>',
  box: '<path d="m12 3 9 5v8l-9 5-9-5V8l9-5Zm0 9 9-4M12 12 3 8m9 4v9M7.5 5.5l9 5"/>',
  layers:
    '<path d="m12 3 10 5-10 5L2 8l10-5Zm-10 9 10 5 10-5M2 16l10 5 10-5"/>',
  pulse: '<path d="M3 12h4l3-8 4 16 3-8h4"/>',
  settings:
    '<path d="m9 3-1 3-3 1-2 3 2 2-1 3 2 3 3-1 2 3h3l1-3 3-1 2-3-2-2 1-3-2-3-3 1-2-3H9Z"/><circle cx="12" cy="12" r="3"/>',
  search: '<circle cx="10.5" cy="10.5" r="6.5"/><path d="m16 16 5 5"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  arrow: '<path d="M5 12h14m-5-5 5 5-5 5"/>',
  chevron: '<path d="m9 5 7 7-7 7"/>',
  refresh:
    '<path d="M20 7v5h-5M4 17v-5h5M6.1 6.1A8 8 0 0 1 20 12M4 12a8 8 0 0 0 13.9 5.9"/>',
  cpu: '<rect x="6" y="6" width="12" height="12" rx="2"/><rect x="9" y="9" width="6" height="6" rx="1"/><path d="M9 3v3m6-3v3M9 18v3m6-3v3M3 9h3m-3 6h3m12-6h3m-3 6h3"/>',
  move: '<path d="M4 7h16m-4-4 4 4-4 4M20 17H4m4-4-4 4 4 4"/>',
  globe:
    '<circle cx="12" cy="12" r="9"/><ellipse cx="12" cy="12" rx="4" ry="9"/><path d="M3 12h18"/>',
  dots: '<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>',
  check: '<path d="m5 12 4 4L19 6"/>',
  close: '<path d="m6 6 12 12M6 18 18 6"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  play: '<path d="m8 4 12 8-12 8V4Z"/>',
  pause: '<path d="M8 4v16M16 4v16"/>',
  trash: '<path d="M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7m4-7v7"/>',
  log: '<path d="m4 6 5 5-5 5M12 17h8"/>',
  lock: '<rect x="4" y="10" width="16" height="11" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3M12 14v3"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v6m0-10h.01"/>',
  menu: '<path d="M4 6h16M4 12h16M4 18h16"/>',
  external: '<path d="M14 3h7v7m0-7L10 14M10 3H4v17h17v-6"/>',
  logout: '<path d="M9 4H4v16h5M9 12h12m-4-4 4 4-4 4"/>',
  scale: '<path d="M4 10V4h6M4 4l6 6M20 14v6h-6m6 0-6-6"/>',
  shield:
    '<path d="m12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6l8-3Z"/><path d="m8 12 3 3 5-6"/>',
};
const icon = (name, cls = "") =>
  `<svg class="icon ${cls}" viewBox="0 0 24 24" aria-hidden="true">${paths[name] || paths.box}</svg>`;
const brand = `<svg class="brand-mark" viewBox="0 0 32 34" fill="none" aria-hidden="true"><path d="m3 10 13-7 13 7v14l-13 7-13-7V10Z" stroke="currentColor" stroke-width="2"/><path d="m3 10 13 7 13-7M16 17v14M9 7l14 7v13" stroke="currentColor" stroke-width="2"/></svg><span>Zela</span>`;
let state = null,
  page = location.hash.slice(1) || "overview",
  search = "",
  filter = "all",
  offline = "",
  poller = null,
  mobileOpen = false;
let modalCloseFocus = null,
  toastTimer = null;
const pages = {
  overview: "Overview",
  workers: "Workers",
  deployments: "Deployments",
  containers: "Containers",
  monitoring: "Monitoring",
  alerts: "Alerts",
  activity: "Activity",
  secrets: "Secrets vault",
  settings: "Settings",
};
if (!pages[page]) page = "overview";
const workers = () =>
  Object.values(state?.workers || {}).sort((a, b) =>
    a.name.localeCompare(b.name),
  );
const deployments = () =>
  Object.values(state?.deployments || {}).sort((a, b) =>
    a.name.localeCompare(b.name),
  );
const busy = () => state?.operations.some((x) => x.status === "running");
const shortID = (id) =>
  String(id)
    .replace(/^demo_/, "")
    .slice(0, 12);
const relative = (t) => {
  if (!t) return "never";
  const sec = Math.max(0, Math.floor((Date.now() - new Date(t)) / 1000));
  if (sec < 60) return sec < 5 ? "just now" : `${sec}s ago`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m ago`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h ago`;
  return `${Math.floor(sec / 86400)}d ago`;
};
const badge = (status, label) =>
  `<span class="badge ${esc(status)}">${["online", "running", "active"].includes(status) ? '<i class="dot"></i>' : ""}${esc(label || status)}</span>`;
const btn = (label, action, attrs = "", type = "", ico = "") =>
  `<button class="btn ${type}" data-action="${action}" ${attrs}>${ico ? icon(ico) : ""}<span>${esc(label)}</span></button>`;
const match = (...vs) =>
  vs.some((v) =>
    String(v || "")
      .toLowerCase()
      .includes(search.toLowerCase()),
  );
const running = (w) =>
  (w.containers || []).filter((c) => c.State === "running").length;
const taskRunning = (t) => {
  const w = state.workers[t.workerID];
  return (
    w?.status === "online" &&
    w.containers?.some(
      (c) =>
        c.Id === t.id &&
        c.State === "running" &&
        !c.Status.includes("unhealthy"),
    )
  );
};
const reservations = (w) => {
  let cpu = 0,
    memory = 0;
  for (const d of deployments())
    for (const t of d.tasks || [])
      if (t.workerID === w.id) {
        cpu += d.cpus;
        memory += d.memoryMB;
      }
  for (const ct of Object.values(state.dockerContainers || {}))
    if (ct.workerID === w.id) {
      cpu += (ct.nanoCPUs || 0) / 1e9;
      memory += (ct.memoryBytes || 0) / 1024 / 1024;
    }
  return { cpu, memory };
};
const region = (w) => w.region || "Unassigned region";
const empty = (title, note, action = "", label = "") =>
  `<div class="empty">${icon("layers")}<b>${esc(title)}</b>${esc(note)}${action ? `<div>${btn(label, action, "", "primary", "plus")}</div>` : ""}</div>`;

async function api(path, body) {
  const res = await fetch("/api" + path, {
    method: body === undefined ? "GET" : "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let data;
  try {
    data = await res.json();
  } catch {
    throw new Error(`Server returned ${res.status}`);
  }
  if (!res.ok) {
    if (res.status === 401 && path !== "/login") showLogin();
    throw new Error(data.error || `Request failed (${res.status})`);
  }
  return data;
}
function toast(message, error = false) {
  clearTimeout(toastTimer);
  $("#toast").className = `toast${error ? " error" : ""}`;
  $("#toast").textContent = message;
  toastTimer = setTimeout(
    () => {
      $("#toast").className = "";
      $("#toast").textContent = "";
    },
    error ? 6500 : 3800,
  );
}
async function load(initial = false) {
  try {
    state = await api("/state");
    offline = "";
    render();
    processNotifications();
    refreshNotificationCenter();
    if (page === "monitoring") void fetchMetricHistory();
    if (!poller) poller = setInterval(() => load(), 2500);
  } catch (e) {
    if (state) {
      offline = e.message;
      render();
    } else if (!$("#login-form")) showLogin();
  }
}
function showLogin() {
  resetNotifications();
  clearInterval(poller);
  poller = null;
  state = null;
  closeModal();
  historySequence++;
  historyKey = "";
  historyBusy = false;
  monitorPoints = [];
  $("#app").innerHTML =
    `<div class="login-screen"><section class="login-panel"><div class="brand">${brand}</div><div class="eyebrow">CLUSTER CONTROL</div><h1>Welcome to your control plane.</h1><p>Manage your Docker hosts and workloads from one place. Sign in to access your cluster.</p><form id="login-form"><div class="form-group"><label for="token">Control-plane token</label><input id="token" name="token" type="password" autocomplete="current-password" required placeholder="Enter your admin token"></div><div class="error-text" id="login-error" role="alert"></div><button class="btn primary" type="submit">${icon("lock")}Sign in to Zela</button></form><p class="login-hint">Use <code>ZELA_ADMIN_TOKEN</code> or the token in your data directory’s <code>admin-token</code> file.</p></section></div>`;
  $("#token").focus();
}
function shell() {
  return `<div class="shell"><aside class="sidebar ${mobileOpen ? "open" : ""}"><a class="brand" href="#overview">${brand}</a><div class="brand-label">CLUSTER CONTROL</div><div class="nav-label">WORKSPACE</div><nav class="nav">${Object.entries(
    pages,
  )
    .filter(([p]) => p !== "settings")
    .map(
      ([p, label]) =>
        `<button class="nav-item ${page === p ? "active" : ""}" data-action="nav" data-page="${p}" ${page === p ? 'aria-current="page"' : ""}>${icon({ overview: "grid", workers: "server", deployments: "layers", containers: "box", activity: "pulse", monitoring: "pulse", alerts: "shield", secrets: "lock" }[p])}<span>${label}</span>${p === "workers" ? `<span class="count">${workers().length}</span>` : p === "deployments" ? `<span class="count">${deployments().length}</span>` : ""}</button>`,
    )
    .join(
      "",
    )}</nav><div class="nav-separator"></div><button class="nav-item ${page === "settings" ? "active" : ""}" data-action="nav" data-page="settings">${icon("settings")}<span>Settings & setup</span></button><div class="sidebar-bottom"><div class="cluster-switch">${icon("globe")}<div><b>${state.demo ? "demo-cluster" : "default-cluster"}</b><span><i class="dot ${workers().some((w) => w.status === "offline") ? "gray" : ""}"></i> ${state.demo ? "Simulation environment" : "Docker infrastructure"}</span></div></div><div class="side-footer"><span>CONTROL PLANE · v0.4.0</span><button class="icon-button" data-action="logout" aria-label="Sign out">${icon("logout")}</button></div></div></aside><main class="main"><header class="topbar"><div class="breadcrumbs"><button class="mobile-menu" data-action="menu" aria-label="Toggle menu">${icon("menu")}</button><span>Infrastructure</span><span class="slash">/</span><b>${pages[page]}</b></div><div class="top-controls"><div class="search">${icon("search")}<input id="global-search" aria-label="Search cluster" placeholder="Search your cluster…" value="${esc(search)}"><span class="shortcut">⌘ K</span></div><div class="top-divider"></div><div class="live"><i class="dot ${offline ? "gray" : ""}"></i>${offline ? "Disconnected" : "Live updates"}</div>${notificationBell()}<button class="avatar" data-action="nav" data-page="settings" aria-label="Open settings">AD</button></div></header><div class="content">${offline ? `<div class="connection-error">${esc(offline)} · Reconnecting to the control plane…</div>` : ""}${state.demo ? `<div class="demo-banner">${icon("info")}<span>Demo environment. Docker hosts and workloads are simulated; all controls are available to explore.</span></div>` : ""}${busy() ? `<div class="op-banner">${icon("refresh", "spin")}<span>${esc(state.operations.find((x) => x.status === "running").title)} · Operation in progress</span></div>` : ""}${(state.alerts || []).some((a) => a.active && !a.acknowledged) ? `<div class="notice warn alert-banner">${icon("shield")}<span>${state.alerts.filter((a) => a.active && !a.acknowledged).length} active alerts need attention</span>${btn("View alerts", "nav", 'data-page="alerts"', "text")}</div>` : ""}${state.monitoringError ? `<div class="notice warn">${esc(state.monitoringError)}</div>` : ""}${body()}<footer class="content-footer"><span>${icon("shield")}Authenticated control plane${state.demo ? " · simulation mode" : ""}</span><span>Inventory and metrics poll every 10 seconds · All times local</span></footer></div></main></div>`;
}
function render() {
  if (!state) return;
  const focused = document.activeElement?.id === "global-search",
    pos = focused ? document.activeElement.selectionStart : 0;
  $("#app").innerHTML = shell();
  if (focused) {
    $("#global-search").focus();
    $("#global-search").setSelectionRange(pos, pos);
  }
}
function pageHead(title, sub, type = "deploy") {
  return `<div class="page-head"><div><div class="eyebrow">${page === "overview" ? "CLUSTER OVERVIEW" : "INFRASTRUCTURE / " + pages[page]}</div><h1>${esc(title)}</h1><p class="subtitle">${esc(sub)}</p></div><div class="head-actions">${btn("Refresh", "refresh", busy() ? "disabled" : "", "", "refresh")}${type === "deploy" ? btn("New deployment", "new-deployment", busy() ? "disabled" : "", "primary", "plus") : type === "worker" ? btn("Connect worker", "new-worker", busy() ? "disabled" : "", "", "plus") + btn("Provision worker", "provision-worker", busy() ? "disabled" : "", "primary", "server") : type === "secret" ? btn("Add SSH key", "new-secret", busy() ? "disabled" : "", "primary", "lock") : ""}</div></div>`;
}
function metrics() {
  const ws = workers(),
    online = ws.filter((w) => w.status === "online"),
    cts = ws.reduce((n, w) => n + (w.containers?.length || 0), 0),
    runningC = ws
      .filter((w) => w.status === "online")
      .reduce((n, w) => n + running(w), 0),
    deps = deployments(),
    healthy = deps.filter((d) => d.status === "running").length,
    cpu = online.reduce((n, w) => n + w.info.NCPU, 0),
    res = online.reduce((n, w) => n + reservations(w).cpu, 0);
  const values = [
    [
      "Workers",
      `${online.length}<small>/ ${ws.length}</small>`,
      `${online.length} online`,
      ws.some((w) => w.mode === "draining")
        ? "Maintenance in progress"
        : "Connected Docker hosts",
      "server",
    ],
    [
      "Containers",
      `${runningC}<small>running</small>`,
      `${cts} total`,
      "Across all worker hosts",
      "box",
    ],
    [
      "Deployments",
      `${deps.length}`,
      `${healthy} healthy`,
      "Managed application workloads",
      "layers",
    ],
    [
      "CPU capacity",
      `${cpu}<small>vCPU</small>`,
      `${res.toFixed(1)} reserved`,
      "Cluster resource reservations",
      "cpu",
    ],
  ];
  return `<section class="metrics" aria-label="Cluster metrics">${values.map(([label, value, note, detail, ic]) => `<div class="metric"><div class="metric-label">${label}</div><div class="metric-icon">${icon(ic)}</div><div class="metric-value">${value}</div><div class="metric-note"><span class="positive">${note}</span><span>· ${detail}</span></div></div>`).join("")}</section>`;
}
function workerCard(w, full = false) {
  const r = reservations(w),
    total = w.info.MemTotal / 1024 / 1024 / 1024 || 0,
    cpuPct = w.info.NCPU ? Math.min(100, (r.cpu / w.info.NCPU) * 100) : 0,
    memPct = total ? Math.min(100, (r.memory / 1024 / total) * 100) : 0;
  return `<article class="worker-card"><div class="worker-title"><div class="server-tile">${icon("server")}</div><div><button class="worker-name" data-action="worker-details" data-id="${esc(w.id)}">${esc(w.name)}</button><div class="worker-endpoint mono" title="${esc(w.endpoint)}">${esc(w.endpoint.replace(/^https?:\/\//, ""))}</div></div>${badge(w.status)}</div><div class="worker-tags"><span class="tag">${esc(region(w))}</span><span class="tag">${esc(w.info.Architecture || "—")}</span>${w.mode !== "active" ? badge(w.mode) : ""}</div><div class="resource"><div class="resource-label"><span>CPU reserved</span><b>${r.cpu.toFixed(1)} / ${w.info.NCPU || "—"} cores</b></div><progress aria-label="CPU reserved" value="${cpuPct}" max="100"></progress></div><div class="resource memory"><div class="resource-label"><span>Memory reserved</span><b>${(r.memory / 1024).toFixed(1)} / ${total.toFixed(0)} GB</b></div><progress aria-label="Memory reserved" value="${memPct}" max="100"></progress></div><div class="worker-foot"><span>${icon("box")}${running(w)} running containers</span><span>${icon("clock")}${relative(w.lastSeen && new Date(w.lastSeen).getFullYear() > 1970 ? w.lastSeen : null)}</span></div>${full ? `<div class="worker-actions">${btn("Manage worker", "worker-details", `data-id="${esc(w.id)}"`, "", "settings")}${btn("Deploy here", "new-deployment", `data-worker="${esc(w.id)}" ${w.mode !== "active" || w.status !== "online" || busy() ? "disabled" : ""}`, "green", "plus")}</div>${w.error ? `<div class="worker-error">${esc(w.error)}</div>` : ""}${w.status === "provision-failed" ? `<div class="worker-actions">${btn("Retry provisioning", "retry-provision", `data-id="${esc(w.id)}" ${busy() ? "disabled" : ""}`, "", "refresh")}</div>` : ""}` : ""}</article>`;
}
function topology() {
  const ws = workers().filter((w) => match(w.name, w.endpoint, w.region));
  return `<section class="panel topology"><div class="panel-head"><div><h2>Cluster topology</h2><p>Your control plane and connected Docker hosts</p></div><div class="right"><span class="panel-kicker"><i class="dot"></i>${workers().filter((w) => w.status === "online").length} workers online</span>${btn("Add worker", "new-worker", busy() ? "disabled" : "", "", "plus")}</div></div>${
    ws.length
      ? `<div class="topology-area"><div class="control-node">${icon("layers")}<div><b>Zela control plane</b><small>Scheduling & orchestration</small></div><i class="dot"></i></div><div class="connector"></div><div class="worker-grid overview">${ws
          .slice(0, 3)
          .map((w) => workerCard(w))
          .join("")}</div></div>`
      : empty(
          "Connect your first worker",
          "Register a Docker host to start deploying workloads.",
          "new-worker",
          "Add worker",
        )
  }<div class="panel-footer"><span>${state.demo ? "Simulated worker connections" : "Authenticated worker connections"} · CPU and memory show reservations</span><button class="text-link" data-action="nav" data-page="workers">View all workers ${icon("arrow")}</button></div></section>`;
}
function deploymentTable(list, overview = false) {
  return `<section class="panel"><div class="panel-head"><div><h2>${overview ? "Deployments" : "Application deployments"}</h2>${overview ? "<p>Workloads running across your cluster</p>" : ""}</div><div class="right">${badge("stopped", `${list.length} total`)}${overview ? '<button class="text-link" data-action="nav" data-page="deployments">View all ' + icon("arrow") + "</button>" : ""}</div></div>${
    list.length
      ? `<div class="table-wrap"><table><thead><tr><th>APPLICATION</th><th>STATUS</th><th>REPLICAS</th><th>PLACEMENT</th><th></th></tr></thead><tbody>${list
          .slice(0, overview ? 6 : 1000)
          .map((d) => {
            const names = [
              ...new Set(
                (d.tasks || []).map(
                  (t) => state.workers[t.workerID]?.name || "Unknown",
                ),
              ),
            ];
            return `<tr><td><div class="deployment-cell"><div class="app-tile">${icon("box")}</div><div><button class="name-link" data-action="deployment-details" data-id="${esc(d.id)}">${esc(d.name)}</button><div class="small-sub mono">${esc(d.image)}</div></div></div></td><td>${badge(d.status)}</td><td><span class="replica-count">${(d.tasks || []).filter(taskRunning).length} / ${d.replicas}</span></td><td class="truncated" title="${esc(names.join(", "))}">${names.length > 1 ? `${names.length} workers` : esc(names[0] || "Unscheduled")}</td><td class="more-cell"><button class="dots-button" data-action="deployment-details" data-id="${esc(d.id)}" aria-label="Manage ${esc(d.name)}">${icon("dots")}</button></td></tr>`;
          })
          .join("")}</tbody></table></div>`
      : empty(
          search ? "No matching deployments" : "No deployments yet",
          search
            ? "Try a different search."
            : "Deploy an image to your connected workers.",
          "new-deployment",
          "Create deployment",
        )
  }${overview ? '<div class="panel-footer"><span>Balanced scheduling · Explicit resource limits</span><button class="text-link" data-action="new-deployment">Deploy an application ' + icon("plus") + "</button></div>" : ""}</section>`;
}
function distribution() {
  const ws = workers(),
    total = ws.reduce((n, w) => n + running(w), 0);
  let offset = 0;
  const colors = ["#65ba9e", "#8bb2d5", "#b0a9d6", "#e0b988"];
  return `<section class="panel"><div class="panel-head"><h2>Workload distribution</h2>${icon("box")}</div><div class="distribution"><div class="donut-wrap"><svg class="donut" viewBox="0 0 146 146" aria-label="Running containers by worker"><circle cx="73" cy="73" r="58" fill="none" stroke="#eef3f5" stroke-width="12"/>${ws
    .map((w, i) => {
      const pct = total ? running(w) / total : 0;
      const part = `<circle cx="73" cy="73" r="58" fill="none" stroke="${colors[i % 4]}" stroke-width="12" stroke-dasharray="${Math.max(0, pct * 364.425 - 3)} ${364.425}" stroke-dashoffset="${-offset * 364.425}"/>`;
      offset += pct;
      return pct ? part : "";
    })
    .join(
      "",
    )}</svg><div class="donut-center"><strong>${total}</strong><span>running containers</span></div></div><div class="legend">${
    ws
      .slice(0, 6)
      .map(
        (w, i) =>
          `<div class="legend-item"><i class="color-${i % 4}"></i><span>${esc(w.name)}</span><b>${running(w)}</b></div>`,
      )
      .join("") || '<span class="subtitle">No connected workers</span>'
  }</div></div></section>`;
}
function recent() {
  return `<section class="panel"><div class="panel-head"><h2>Recent activity</h2><button class="text-link" data-action="nav" data-page="activity">${icon("arrow")}</button></div><div class="activity-list">${
    state.operations
      .slice(0, 4)
      .map(
        (o) =>
          `<div class="activity-item"><div class="activity-icon ${o.status}">${icon(o.status === "failed" ? "close" : o.status === "running" ? "refresh" : "check")}</div><div><div class="activity-title">${esc(o.title)}</div><div class="activity-time">${relative(o.startedAt)} · ${esc(o.status)}</div></div></div>`,
      )
      .join("") ||
    '<p class="subtitle">Cluster operations will appear here.</p>'
  }</div></section>`;
}
function filters(statuses) {
  return `<div class="toolbar">${statuses.map((s) => `<button class="pill-filter ${filter === s ? "active" : ""}" data-action="filter" data-filter="${s}">${esc(s === "all" ? "All " + pages[page].toLowerCase() : s[0].toUpperCase() + s.slice(1))}</button>`).join("")}<span class="filter-count">${search ? "Searching for “" + esc(search) + "”" : ""}</span></div>`;
}
function inventory(list) {
  return list.length
    ? `<div class="table-wrap"><table><thead><tr><th>CONTAINER</th><th>IMAGE</th><th>WORKER</th><th>STATE</th><th></th></tr></thead><tbody>${list.map(({ ct, w }) => `<tr><td><div class="deployment-cell"><div class="app-tile">${icon("box")}</div><div><span class="name-link">${esc(ct.Names?.[0]?.replace(/^\//, "") || shortID(ct.Id))}</span><div class="small-sub mono">${esc(shortID(ct.Id))} ${ct.Labels?.["zela.managed"] === "true" ? "· managed" : ""}</div></div></div></td><td class="mono truncated">${esc(ct.Image)}</td><td>${esc(w.name)}</td><td>${badge(ct.State === "running" ? "running" : ct.State === "exited" ? "stopped" : ct.State, ct.State)}${w.status === "offline" ? '<div class="small-sub">Worker offline · cached</div>' : ""}</td><td><div class="row"><button class="dots-button" data-action="logs" data-worker="${esc(w.id)}" data-id="${esc(ct.Id)}" aria-label="Container logs">${icon("log")}</button><button class="dots-button" data-action="container-details" data-worker="${esc(w.id)}" data-id="${esc(ct.Id)}" aria-label="Manage container">${icon("dots")}</button></div></td></tr>`).join("")}</tbody></table></div>`
    : empty(
        "No containers to display",
        "Connect a worker or deploy an application to see containers here.",
      );
}

function accessTokensPanel() {
  const tokens = state.tokens || [];
  return `<section class="panel access-tokens"><div class="panel-head"><h2>Integration API tokens</h2>${btn("Create token", "new-token", "", "primary", "plus")}</div><div class="settings-body"><p>Give an external platform or scheduler an infrastructure token. It can discover worker health and capacity, select a worker, and manage containers it creates through the cluster endpoint. Zela handles application scheduling and lifecycle. Worker management, SSH secrets, platform settings and other tokens are blocked.</p><p>Tokens support stateless containers, image pulls, lifecycle, logs and exec. Privileged containers, host access, builds and volume/network administration require administrator access.</p>${
    tokens.length
      ? `<div class="table-wrap"><table><thead><tr><th>TOKEN</th><th>STATUS</th><th>EXPIRES</th><th></th></tr></thead><tbody>${tokens
          .map((t) => {
            const status = t.revokedAt
              ? "Revoked"
              : t.expiresAt && new Date(t.expiresAt) <= new Date()
                ? "Expired"
                : "Active";
            return `<tr><td><b>${esc(t.name)}</b><div class="small-sub mono">${esc(t.prefix)}…</div><div class="small-sub">${esc(({infrastructure: "Infrastructure · Zela chooses workers", deployments: "Deployments · automatic placement", mcp_read: "MCP · Read cluster", mcp_manage: "MCP · Manage cluster"})[t.scope] || t.scope)} · Created ${esc(new Date(t.createdAt).toLocaleDateString())}</div></td><td>${esc(status)}</td><td>${t.expiresAt ? esc(new Date(t.expiresAt).toLocaleDateString()) : "Never"}</td><td>${status === "Active" ? btn("Revoke", "revoke-token", `data-id="${esc(t.id)}"`, "text", "trash") : ""}</td></tr>`;
          })
          .join("")}</tbody></table></div>`
      : "<p>No integration tokens yet.</p>"
  }<p>Token values are shown once and stored as hashes. Infrastructure and deployment credentials retain their own workload ownership. MCP management credentials can operate workloads across the cluster. Revocation stops API access and open streams; it does not delete applications.</p></div></section>`;
}
function newTokenDialog(selectedScope = "infrastructure") {
  openModal(
    "Create integration token",
    "Choose the permissions for this integration.",
    `<div class="form-grid"><div class="form-group wide"><label for="token-name">Token name</label><input id="token-name" name="name" required maxlength="80" placeholder="my-platform production" autocomplete="off"></div><div class="form-group wide"><label for="token-scope">Permissions</label><select id="token-scope" name="scope"><option value="infrastructure" ${selectedScope === "infrastructure" ? "selected" : ""}>Zela infrastructure</option><option value="deployments">Deployment integration (automatic placement)</option><option value="mcp_read" ${selectedScope === "mcp_read" ? "selected" : ""}>MCP · Read cluster</option><option value="mcp_manage" ${selectedScope === "mcp_manage" ? "selected" : ""}>MCP · Manage cluster</option></select></div><div class="form-group wide"><label for="token-expiry">Expires after</label><select id="token-expiry" name="expiresInDays"><option value="7">7 days</option><option value="30">30 days</option><option value="90" selected>90 days</option><option value="365">365 days</option><option value="0">Never</option></select></div></div><div class="notice note">Infrastructure and deployment tokens manage their own workloads. MCP read tokens can inspect cluster health, inventory, logs and activity. MCP management tokens can also change deployments, containers and workers across the cluster, including workloads created by other clients. MCP credentials cannot use the raw Docker gateway, export private keys or administer tokens. The full token is displayed once.</div>`,
    `${btn("Cancel", "close")}<button type="submit" class="btn primary">Create token</button>`,
    "token-form",
  );
}
function showCreatedToken(result) {
  openModal(
    "Save your integration token",
    "Copy this value now. You cannot view it again after closing this dialog.",
    `<div class="form-group"><label for="created-token">${esc(result.metadata.name)}</label><textarea id="created-token" readonly rows="3" spellcheck="false" autocomplete="off">${esc(result.token)}</textarea></div><p>Keep the endpoint URL unchanged. Supply this token separately as <code>Authorization: Bearer &lt;token&gt;</code>. For MCP, connect to the cluster’s <code>/mcp</code> endpoint or save the token in a zela profile. Do not put the token in the URL.</p>`,
    btn("Done", "close", "", "primary"),
  );
}
function confirmTokenRevoke(id) {
  const token = (state.tokens || []).find((t) => t.id === id);
  if (!token) return;
  openModal(
    "Revoke integration token",
    `Revoke ${token.name}?`,
    `<p>This immediately rejects new requests and closes active API streams for this token. Applications keep running. MCP credentials can be replaced with a new cluster connection; infrastructure and deployment tokens retain separate workload ownership.</p>`,
    `${btn("Cancel", "close")}${btn("Revoke token", "execute-token-revoke", `data-id="${esc(id)}"`, "danger", "trash")}`,
  );
}
function mcpPanel() {
 const path = state.mcp?.endpoint || "/mcp";
 const endpoint = path.startsWith("/") && location.origin ? location.origin + path : path;
 return `<section class="panel"><div class="panel-head"><h2>AI cluster management</h2>${icon("server")}</div><div class="settings-body"><p>Manage your cluster from ChatGPT, Claude, Codex or an MCP-compatible coding assistant using natural language.</p><dl><dt>MCP endpoint</dt><dd><code>${esc(endpoint)}</code></dd><dt>Transport</dt><dd>Streamable HTTP · Local stdio through zela</dd><dt>Browser sign-in</dt><dd>${state.mcp?.oauthEnabled ? "OAuth enabled · approve read or management access when connecting" : "Set ZELA_MCP_PUBLIC_URL to your public HTTPS cluster origin and restart to enable OAuth for ChatGPT and Claude"}</dd></dl><p>Add this endpoint as a custom MCP connector in ChatGPT or Claude and approve the connection in your browser. Management permissions include deploying and scaling applications, reading logs, pausing or draining workers, and provisioning existing SSH hosts with saved keys.</p><p>For local coding assistants, create an MCP token, save it in a private zela profile, and use:</p><pre class="code">zela --profile production mcp serve</pre><p>Read access shows cluster status, inventory, logs and activity. Management can change workloads across the cluster. Revoke connections in Integration API tokens. Private key contents and token administration are excluded.</p><div class="toolbar">${btn("Create MCP management token", "new-mcp-token", "", "primary", "plus")}${btn("Create read-only token", "new-mcp-read-token", "", "", "plus")}</div></div></section>`;
}
function settings() {
  return `${pageHead("Built for your infrastructure.", "Connect Docker hosts and operate your cluster on your private network.", "none")}<div class="settings-grid">${mcpPanel()}${accessTokensPanel()}${notificationsSettingsPanel()}<section class="panel"><div class="panel-head"><h2>Install a worker agent</h2>${icon("server")}</div><div class="settings-body"><p>Build the Go agent, copy it to a Docker host, and run it with access to the Docker socket. The control plane connects to this authenticated endpoint.</p><pre class="code"># Build for a Linux worker
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -o bin/zela-worker ./cmd/worker

# Run on your worker host
export ZELA_WORKER_TOKEN="$(openssl rand -hex 32)"
./zela-worker --listen 0.0.0.0:9091 \
  --tls-cert /etc/zela/worker.crt \
  --tls-key /etc/zela/worker.key</pre><p>Use a certificate trusted by the control-plane host. Restrict the worker port to your private network.</p>${btn("Connect a worker", "new-worker", "", "primary", "plus")}</div></section><section class="panel"><div class="panel-head"><h2>Control-plane configuration</h2>${icon("settings")}</div><div class="settings-body"><p>Configuration uses command-line flags and environment variables. Restart the server to apply changes.</p><dl><dt>Admin authentication</dt><dd><code>ZELA_ADMIN_TOKEN</code> or generated <code>admin-token</code></dd><dt>State persistence</dt><dd>${state.persistence === "postgresql" ? "PostgreSQL · schema initialized on startup" : "Atomic JSON writes · mode 0600"}</dd><dt>Transport security</dt><dd><code>--tls-cert</code> and <code>--tls-key</code></dd><dt>Docker API gateway</dt><dd>Zela worker selection · Control-plane bearer token</dd><dt>Manager availability</dt><dd>${state.highAvailability ? `Active/standby · ${esc(state.highAvailability.managerID)} · ${esc(state.highAvailability.role)}` : "Single manager"}</dd><dt>Worker inventory polling</dt><dd>Every 10 seconds · 5-second connection timeout</dd><dt>Current environment</dt><dd>${state.demo ? "Demo · simulated Docker engines" : "Live Docker hosts"}</dd></dl></div></section><section class="panel"><div class="panel-head"><h2>Docker API access</h2>${icon("server")}</div><div class="settings-body"><p>Connect to this control plane on port 8080 (or your configured port). Create a Zela infrastructure token here. Zela discovers workers at <code>GET /api/cluster/workers</code> and selects a worker for creation or image pulls with <code>X-Zela-Worker: &lt;id&gt;</code>. The gateway checks capacity and connectivity and never changes the selected worker. Later container and exec requests route to their persisted owner.</p><p>Use standard Docker API paths with the worker header, or <code>/docker/workers/&lt;worker-id&gt;/</code>. Scoped tokens see only their own containers, including when a worker is selected. Worker health, capacity reservations, unavailable ports and fresh host usage are available through the infrastructure API. Worker administration, builds and network/volume administration require administrator access. Existing deployment tokens retain automatic placement.</p><p>Existing workers need the updated agent. Docker API workloads appear in Containers. Placement and ownership are persisted; clients manage application lifecycle. Default limits are 128 MB and 0.25 CPU.</p>${state.demo ? '<p class="notice">Docker API forwarding requires live workers.</p>' : ""}</div></section><section class="panel"><div class="panel-head"><h2>Deployment moves</h2>${icon("move")}</div><div class="settings-body"><p>The destination pulls the image, starts a replacement, and passes its health check before the source is removed. Without an image health check, the container must remain running for two seconds.</p><p>Moves recreate stateless containers. Persistent mounts, image volumes, and container filesystem data require a separate data migration. Published endpoints change to the destination host. Configure your load balancer or DNS accordingly.</p><p>Draining moves managed workloads to eligible workers and prevents new scheduling. Unmanaged containers need to be handled separately.</p></div></section><section class="panel"><div class="panel-head"><h2>Operations & recovery</h2>${icon("shield")}</div><div class="settings-body"><p>Console-managed cluster mutations run one at a time. Each operation and container assignment is saved to persistent storage. Automatic Docker API placements share this operation gate. Interrupted operations are flagged for inspection when the control plane restarts.</p><p>Worker capacity charts and scheduling include reservations for console deployments and automatic Docker API containers. Monitoring shows measured usage, history, and alerts. Docker enforces these limits; unmanaged workloads consume additional host resources.</p><p>This control plane manages standalone Docker hosts. Docker restart policies handle local restarts. Host failures are reported for operator-directed recovery.</p>${btn("View activity", "navigate-activity", "", "", "pulse")}</div></section></div>`;
}
function body() {
  if (page === "secrets") return vaultPage();
  if (page === "monitoring") return monitoringPage();
  if (page === "alerts") return alertsPage();
  switch (page) {
    case "overview":
      return `${pageHead("Your cluster, in control.", "A live view of your Docker infrastructure and the workloads it runs.")}${metrics()}${topology()}<div class="dashboard-bottom"><div>${deploymentTable(
        deployments().filter((d) => match(d.name, d.image)),
        true,
      )}</div><div class="stack">${distribution()}${recent()}</div></div>`;
    case "workers":
      return `${pageHead("Every worker. One workspace.", "Connect, inspect, and manage the Docker hosts in your cluster.", "worker")}${filters(["all", "online", "offline"])}<div class="worker-grid full-workers">${
        workers()
          .filter(
            (w) =>
              match(w.name, w.endpoint, w.region) &&
              (filter === "all" || w.status === filter),
          )
          .map((w) => workerCard(w, true))
          .join("") ||
        empty(
          "No workers found",
          "Add a Docker host to build your cluster.",
          "new-worker",
          "Add worker",
        )
      }</div><p class="note">Pause prevents new placement. Drain moves managed workloads to other workers before maintenance.</p>`;
    case "deployments":
      return `${pageHead("Your workloads, orchestrated.", "Deploy images, scale replicas, and move applications across workers.")}${filters(["all", "running", "stopped", "degraded"])}${deploymentTable(deployments().filter((d) => match(d.name, d.image) && (filter === "all" || d.status === filter)))}`;
    case "containers": {
      const list = workers()
        .flatMap((w) => (w.containers || []).map((ct) => ({ ct, w })))
        .filter(
          ({ ct, w }) =>
            match(ct.Names?.[0], ct.Image, w.name, ct.Id) &&
            (filter === "all" ||
              (filter === "running"
                ? ct.State === "running"
                : ct.State !== "running")),
        );
      return `${pageHead("All containers. Full visibility.", "Inspect managed and existing containers across your Docker hosts.", "none")}${filters(["all", "running", "stopped"])}<section class="panel"><div class="panel-head"><h2>Container inventory</h2>${badge("stopped", `${list.length} containers`)}</div>${inventory(list)}</section>`;
    }
    case "activity": {
      const list = state.operations.filter(
        (o) =>
          match(o.title, o.detail) && (filter === "all" || o.status === filter),
      );
      return `${pageHead("A record of every operation.", "Deployment changes, worker maintenance, and their outcomes.", "none")}${filters(["all", "succeeded", "running", "failed"])}<section class="panel"><div class="panel-head"><h2>Cluster operations</h2>${badge("stopped", `${list.length} operations`)}</div>${list.length ? `<div class="table-wrap"><table class="activity-table"><thead><tr><th>OPERATION</th><th>STATUS</th><th>DETAIL</th><th>STARTED</th></tr></thead><tbody>${list.map((o) => `<tr><td><button class="name-link" data-action="operation-details" data-id="${esc(o.id)}">${esc(o.title)}</button><div class="small-sub mono">${esc(shortID(o.id))}</div></td><td>${badge(o.status)}</td><td class="operation-detail">${esc(o.detail)}</td><td>${relative(o.startedAt)}</td></tr>`).join("")}</tbody></table></div>` : empty("No operations yet", "Your cluster activity will appear here.")}</section>`;
    }
    case "settings":
      return settings();
  }
}

function openModal(
  title,
  sub,
  content,
  footer = "",
  formID = "",
  large = false,
) {
  notificationCenterOpen = false;
  modalCloseFocus = document.activeElement;
  $("#modal-root").innerHTML =
    `<div class="modal-overlay"><section class="modal ${large ? "large" : ""}" role="dialog" aria-modal="true" aria-labelledby="modal-title"><div class="modal-header"><div><h2 id="modal-title">${esc(title)}</h2><p>${esc(sub)}</p></div><button class="icon-button" data-action="close" aria-label="Close dialog">${icon("close")}</button></div>${formID ? `<form id="${formID}">` : ""}<div class="modal-body">${content}</div><div class="modal-error" role="alert" id="modal-error"></div>${footer ? `<div class="modal-footer">${footer}</div>` : ""}${formID ? "</form>" : ""}</section></div>`;
  const input = $("#modal-root input, #modal-root select, #modal-root button");
  input?.focus();
}
function closeModal() {
  notificationCenterOpen = false;
  $("#modal-root").innerHTML = "";
  if (modalCloseFocus?.isConnected) modalCloseFocus.focus();
}
function workerOptions(selected = "", all = false) {
  return `${all ? '<option value="">Balanced across available workers</option>' : ""}${workers()
    .filter((w) => w.status === "online" && w.mode === "active")
    .map(
      (w) =>
        `<option value="${esc(w.id)}" ${selected === w.id ? "selected" : ""}>${esc(w.name)} · ${esc(region(w))}</option>`,
    )
    .join("")}`;
}

const secrets = () => state?.secrets || [];
function vaultPage() {
  const list = secrets().filter((k) => match(k.name, k.fingerprint));
  return `${pageHead("Secrets vault", "Store SSH identities securely and use them to provision Docker hosts.", "secret")}<section class="panel"><div class="panel-head"><h2>SSH keys</h2>${badge("online", "AES-256-GCM encryption")}</div>${
    list.length
      ? `<div class="table-wrap"><table><thead><tr><th>SECRET</th><th>FINGERPRINT</th><th>USAGE</th><th>ADDED</th><th></th></tr></thead><tbody>${list
          .map((k) => {
            const used = workers().filter((w) => w.ssh?.secretID === k.id);
            return `<tr><td><div class="deployment-cell"><div class="app-tile">${icon("lock")}</div><div><span class="name-link">${esc(k.name)}</span><div class="small-sub">SSH private key</div></div></div></td><td class="mono">${esc(k.fingerprint)}</td><td>${used.length ? `${used.length} workers` : "Not in use"}</td><td>${relative(k.createdAt)}</td><td>${btn("Delete", "delete-secret", `data-id="${esc(k.id)}" ${used.length || busy() ? "disabled" : ""}`, "text", "trash")}</td></tr>`;
          })
          .join("")}</tbody></table></div>`
      : empty(
          "No SSH keys yet",
          "Add a private key to provision and manage hosts over SSH.",
          "new-secret",
          "Add SSH key",
        )
  }</section><div class="notice note">Private keys and passphrases are encrypted before storage. The API returns names and fingerprints only; it never exports a saved private key. Back up the vault securely with its master key. For separate key storage, set the server’s ZELA_VAULT_KEY environment variable.</div>`;
}
function newSecretDialog() {
  openModal(
    "Add SSH key",
    "Encrypt an SSH identity in the control-plane vault.",
    `<div class="form-grid"><div class="form-group wide"><label for="secret-name">Secret name</label><input id="secret-name" name="name" required maxlength="63" pattern="[a-zA-Z0-9][a-zA-Z0-9_.-]*" placeholder="production-workers"></div><div class="form-group wide"><label for="secret-key">SSH private key</label><textarea id="secret-key" name="privateKey" required rows="8" autocomplete="off" spellcheck="false" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"></textarea><small>OpenSSH or PEM format. The public key must already be authorized for the SSH login on your target hosts.</small></div><div class="form-group wide"><label for="secret-key-file">Or import a key file</label><input id="secret-key-file" type="file"><small>The selected file fills the private-key field.</small></div><div class="form-group wide"><label for="secret-passphrase">Key passphrase (if encrypted)</label><input id="secret-passphrase" name="passphrase" type="password" autocomplete="off"></div></div><div class="notice note">The server validates the key and encrypts both the private key and passphrase. They cannot be viewed or downloaded after saving.</div>`,
    `${btn("Cancel", "close")}<button type="submit" class="btn primary">${icon("lock")}Save SSH key</button>`,
    "secret-form",
  );
}
function confirmSecretDelete(id) {
  const k = secrets().find((x) => x.id === id);
  if (!k) return;
  openModal(
    "Delete SSH key",
    "Remove an unused identity from the vault.",
    `<div class="notice warn">Delete <b>${esc(k.name)}</b> from the vault. This cannot be undone. Keys referenced by registered workers cannot be deleted.</div>`,
    `${btn("Cancel", "close")}${btn("Delete key", "execute-secret-delete", `data-id="${esc(id)}"`, "danger", "trash")}`,
  );
}
function provisionDialog() {
  if (!secrets().length) {
    openModal(
      "Add an SSH key first",
      "Worker provisioning uses an identity from your secrets vault.",
      `<div class="notice">Save the private key for a root login or a user with passwordless sudo. Its public key must be installed on the target host.</div>`,
      `${btn("Cancel", "close")}${btn("Add SSH key", "new-secret", "", "primary", "lock")}`,
    );
    return;
  }
  openModal(
    "Provision Docker worker",
    "Install Docker and register an existing Linux host.",
    `<div class="form-grid"><div class="form-group"><label for="provision-name">Worker name</label><input id="provision-name" name="name" required maxlength="63" pattern="[a-zA-Z0-9][a-zA-Z0-9_.-]*" placeholder="docker-worker-04"></div><div class="form-group"><label for="provision-region">Region / zone</label><input id="provision-region" name="region" maxlength="80" placeholder="us-west-2a"></div><div class="form-group"><label for="provision-host">Host IP or DNS name</label><input id="provision-host" name="host" required placeholder="10.0.0.24"></div><div class="form-group"><label for="provision-port">SSH port</label><input id="provision-port" name="port" type="number" min="1" max="65535" value="22" required></div><div class="form-group"><label for="provision-user">SSH user</label><input id="provision-user" name="user" required placeholder="root, cloud-user, or ec2-user"></div><div class="form-group"><label for="provision-key">SSH key</label><select id="provision-key" name="secretID" required>${secrets()
      .map((k) => `<option value="${esc(k.id)}">${esc(k.name)}</option>`)
      .join(
        "",
      )}</select></div><div class="form-group wide"><label for="provision-host-key">Trusted SSH host public key</label><textarea id="provision-host-key" name="hostKey" required rows="2" spellcheck="false" placeholder="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA…"></textarea><small>Copy from the host console’s /etc/ssh/ssh_host_ed25519_key.pub or your trusted known_hosts record. Supply the key type and base64 key, without a hostname. SSH will reject an identity mismatch.</small></div><div class="form-group"><label for="agent-port">Loopback worker port</label><input id="agent-port" name="agentPort" type="number" min="1024" max="65535" value="9091" required></div><div class="form-group"><label class="checkbox-label"><input name="sudo" type="checkbox" checked> Use passwordless sudo</label><small>Required for non-root SSH users.</small></div></div><div class="notice warn note">This installs Docker Engine, configures its service, and installs the Zela worker on an existing RHEL, Rocky Linux, AlmaLinux, CentOS Stream, Ubuntu, or Debian host (amd64/arm64). The operating system is detected automatically. Supported RPM releases: RHEL/Rocky/AlmaLinux 8–10 and CentOS Stream 9/10. The worker listens on loopback and is reached through verified SSH. Existing healthy Docker installations are reused. Conflicting runtimes, including Podman on RPM hosts, are reported for review. SELinux and firewall settings are preserved. Follow the job in Activity.${state.demo ? "<br><b>Demo: all remote installation steps will be simulated.</b>" : ""}</div>`,
    `${btn("Cancel", "close")}<button type="submit" class="btn primary">${icon("server")}Provision worker</button>`,
    "provision-form",
  );
}
function operationDetails(id) {
  const o = state.operations.find((x) => x.id === id);
  if (!o) return;
  openModal(
    o.title,
    "Provisioning progress and operation result.",
    `<div class="details-meta"><div><small>STATUS</small>${badge(o.status)}</div><div><small>STARTED</small><b>${relative(o.startedAt)}</b></div></div><div class="notice ${o.status === "failed" ? "warn" : ""}">${esc(o.detail)}</div>${o.steps?.length ? `<div class="form-section">Provisioning stages</div><div class="activity-list">${o.steps.map((s) => `<div class="activity-item"><div class="activity-icon ${s.status}">${icon(s.status === "failed" ? "close" : s.status === "running" ? "refresh" : "check")}</div><div><div class="activity-title">${esc(s.name)} · ${esc(s.status)}</div><div class="activity-time">${esc(s.detail)}</div></div></div>`).join("")}</div>` : ""}`,
    `${btn("Refresh progress", "operation-details", `data-id="${esc(id)}"`, "", "refresh")}${btn("Close", "close")}`,
  );
}
document.addEventListener("change", async (e) => {
  if (e.target.id === "monitor-worker") {
    monitorWorker = e.target.value;
    monitorContainer = "";
    render();
    return;
  }
  if (e.target.id === "monitor-container") {
    monitorContainer = e.target.value;
    render();
    return;
  }
  if (e.target.id === "secret-key-file" && e.target.files?.[0]) {
    const f = e.target.files[0];
    if (f.size > 65536) {
      $("#modal-error").textContent = "Private key must be smaller than 64 KB.";
      return;
    }
    try {
      $("#secret-key").value = await f.text();
    } catch {
      $("#modal-error").textContent = "Cannot read the selected key file.";
    }
  }
});
function newWorker() {
  openModal(
    "Connect a worker",
    "Register a Docker host with your cluster.",
    `<div class="form-grid"><div class="form-group"><label for="worker-name">Worker name</label><input id="worker-name" name="name" required maxlength="63" pattern="[a-zA-Z0-9][a-zA-Z0-9_.-]*" placeholder="docker-worker-04"></div><div class="form-group"><label for="region">Region / zone</label><input id="region" name="region" maxlength="80" placeholder="us-west-2a"></div><div class="form-group wide"><label for="endpoint">Agent endpoint or local socket</label><input id="endpoint" name="endpoint" required placeholder="https://10.0.0.24:9091" value="${state.demo ? `demo://worker-${workers().length + 1}` : ""}"><small>Use the worker agent’s origin. For a local Docker host, use unix:///var/run/docker.sock.</small></div><div class="form-group wide"><label for="worker-token">Worker token</label><input id="worker-token" name="token" type="password" autocomplete="off" placeholder="ZELA_WORKER_TOKEN from the worker host"><small>Required for remote agents. Stored privately and never returned by the API.</small></div></div><div class="form-section">Getting your host ready</div><div class="toolbar">${btn("Install Docker on a new host", "provision-worker", "", "green", "server")}</div><div class="notice">Install <code>zela-worker</code> on the Docker host with socket access. Configure <code>ZELA_WORKER_TOKEN</code> and TLS, then register its endpoint here. Full build and run commands are in <b>Settings & setup</b>.${state.demo ? "<br>Demo mode accepts simulated workers; no Docker connection is made." : ""}</div>`,
    `${btn("Cancel", "close")}<button type="submit" class="btn primary">${icon("plus")}Connect worker</button>`,
    "worker-form",
  );
}
function newDeployment(worker = "") {
  openModal(
    "New deployment",
    "Deploy an application image across your Docker workers.",
    `<div class="form-grid"><div class="form-group"><label for="dep-name">Application name</label><input id="dep-name" name="name" required maxlength="63" pattern="[a-zA-Z0-9][a-zA-Z0-9_.-]*" placeholder="my-application"></div><div class="form-group"><label for="dep-image">Container image</label><input id="dep-image" name="image" required placeholder="nginx:alpine"></div><div class="form-group"><label for="replicas">Replicas</label><input id="replicas" name="replicas" type="number" min="1" max="50" value="1" required></div><div class="form-group"><label for="placement">Worker placement</label><select name="placement" id="placement">${workerOptions(worker, true)}</select></div><div class="form-group"><label for="cpus">CPU limit per replica</label><input id="cpus" name="cpus" type="number" min="0.1" max="4096" step="0.1" value="0.5" required></div><div class="form-group"><label for="memory">Memory limit (MB)</label><input id="memory" name="memoryMB" type="number" min="32" max="1048576" value="256" required></div></div><div class="form-section">Container configuration</div><div class="form-grid"><div class="form-group wide"><label for="ports">Published ports</label><input id="ports" name="ports" placeholder="8080:80, 8443:443"><small>Host:container, comma-separated. Use 0 for an automatic host port. Fixed host ports require separate workers for each replica.</small></div><div class="form-group wide"><label for="env">Environment variables</label><textarea id="env" name="env" placeholder="APP_ENV=production&#10;LOG_LEVEL=info"></textarea><small>One KEY=value per line. Values are accessible to the cluster administrator.</small></div><div class="form-group wide"><label for="command">Command override (optional JSON array)</label><input id="command" name="command" placeholder='["node", "server.js"]'><small>Leave blank to use the image’s default command.</small></div></div>`,
    `${btn("Cancel", "close")}<button type="submit" class="btn primary" ${!workers().some((w) => w.status === "online" && w.mode === "active") ? "disabled" : ""}>${icon("layers")}Deploy application</button>`,
    "deployment-form",
  );
}
function deploymentDetails(id) {
  const d = state.deployments[id];
  if (!d) return;
  openModal(
    d.name,
    d.image,
    `<div class="details-meta"><div><small>STATUS</small>${badge(d.status)}</div><div><small>REPLICAS</small><b>${d.tasks.filter(taskRunning).length} / ${d.replicas} running</b></div><div><small>CREATED</small><b>${relative(d.createdAt)}</b></div><div><small>CPU / REPLICA</small><b>${d.cpus} vCPU</b></div><div><small>MEMORY / REPLICA</small><b>${d.memoryMB} MB</b></div><div><small>PLACEMENT</small><b>${d.workerIDs?.length ? "Selected workers" : "Balanced"}</b></div></div>${d.error ? `<div class="notice warn">${esc(d.error)}</div>` : ""}<div class="form-section">Container replicas</div><div class="table-wrap"><table><thead><tr><th>CONTAINER</th><th>WORKER</th><th>STATE</th><th></th></tr></thead><tbody>${d.tasks.map((t) => `<tr><td class="mono">${esc(t.name)}<div class="small-sub">${esc(shortID(t.id))}</div></td><td>${esc(state.workers[t.workerID]?.name || "Unknown")}</td><td>${badge(taskRunning(t) ? "running" : "stopped", taskRunning(t) ? "running" : "not running")}</td><td><button class="dots-button" data-action="logs" data-worker="${esc(t.workerID)}" data-id="${esc(t.id)}" aria-label="View logs">${icon("log")}</button><button class="dots-button" data-action="move-replica" data-id="${esc(id)}" data-replica="${esc(t.id)}" ${busy() || d.desiredState !== "running" ? "disabled" : ""} aria-label="Move this replica">${icon("move")}</button></td></tr>`).join("") || '<tr><td colspan="4">No containers scheduled yet.</td></tr>'}</tbody></table></div><div class="modal-actions">${btn(d.desiredState === "stopped" ? "Start" : "Stop", "deployment-action", `data-id="${esc(id)}" data-kind="${d.desiredState === "stopped" ? "start" : "stop"}" ${busy() ? "disabled" : ""}`, "", d.desiredState === "stopped" ? "play" : "pause")}${btn("Restart", "deployment-action", `data-id="${esc(id)}" data-kind="restart" ${busy() ? "disabled" : ""}`, "", "refresh")}${btn("Update image", "update-image", `data-id="${esc(id)}" ${busy() || d.desiredState !== "running" ? "disabled" : ""}`, "", "layers")}${btn("Scale", "scale", `data-id="${esc(id)}" ${busy() || d.desiredState !== "running" ? "disabled" : ""}`, "", "scale")}${btn("Move to worker", "move", `data-id="${esc(id)}" ${busy() || d.desiredState !== "running" ? "disabled" : ""}`, "green", "move")}${btn("Delete", "confirm-delete", `data-type="deployment" data-id="${esc(id)}" ${busy() ? "disabled" : ""}`, "danger", "trash")}</div>`,
    btn("Done", "close"),
    "",
    true,
  );
}
function moveDialog(id, replica = "") {
  const d = state.deployments[id];
  if (!d) return;
  const source = [
    ...new Set(
      d.tasks.map((t) => state.workers[t.workerID]?.name || "Unknown"),
    ),
  ].join(", ");
  openModal(
    "Move deployment",
    "Recreate this application on another Docker worker.",
    `<div class="flow-line"><div class="flow-box"><small>CURRENT PLACEMENT</small>${esc(source || "Unscheduled")}</div>${icon("arrow")}<div class="flow-box"><small>APPLICATION</small>${esc(d.name)} · ${replica ? 1 : d.tasks.length} replicas</div></div><input type="hidden" name="id" value="${esc(id)}"><div class="form-group"><input type="hidden" name="replica" value="${esc(replica)}"><label for="move-worker">Destination worker</label><select id="move-worker" name="target" required><option value="" disabled selected>Select an active worker</option>${workerOptions()}</select><small>Each replica reserves ${d.cpus} vCPU and ${d.memoryMB} MB. The destination must have enough capacity and available host ports.</small></div><div class="notice warn note">The replacement must pass its startup check before the source is removed. Moves recreate containers from their image and configuration; filesystem changes are not copied. Containers with volumes or mounts cannot be moved here. Update your load balancer or DNS for the destination host.</div>`,
    `${btn("Cancel", "close")}<button class="btn primary" type="submit">${icon("move")}Move deployment</button>`,
    "move-form",
  );
}
function updateDialog(id) {
  const d = state.deployments[id];
  openModal(
    "Update application image",
    "Roll out a new image with checked replacements.",
    `<input type="hidden" name="id" value="${esc(id)}"><div class="form-grid"><div class="form-group wide"><label for="update-image">New image reference</label><input id="update-image" name="image" required value="${esc(d.image)}"></div><div class="form-group wide"><label for="update-target">Rollout destination</label><select id="update-target" name="target" required><option value="" disabled selected>Select an active worker</option>${workerOptions()}</select><small>Choose a worker with spare capacity. A host cannot run two replicas with the same fixed published port.</small></div></div><div class="notice warn note">Replicas are replaced one at a time on the selected destination. Each old replica stays available until the new one starts successfully. Persistent mounts and image volumes require separate data migration. A failed rollout can leave a mix of old and new replicas; inspect the activity record before retrying.</div>`,
    `${btn("Cancel", "close")}<button class="btn primary" type="submit">${icon("layers")}Update image</button>`,
    "image-form",
  );
}
function scaleDialog(id) {
  const d = state.deployments[id];
  openModal(
    "Scale deployment",
    `Set the desired replica count for ${d.name}.`,
    `<input type="hidden" name="id" value="${esc(id)}"><div class="form-group"><label for="scale-count">Desired replicas</label><input id="scale-count" name="replicas" type="number" min="1" max="50" value="${d.replicas}" required><small>Additional replicas are placed according to your worker selection and available resources. Scaling down removes containers.</small></div>`,
    `${btn("Cancel", "close")}<button class="btn primary" type="submit">${icon("scale")}Update replicas</button>`,
    "scale-form",
  );
}
function workerDetails(id) {
  const w = state.workers[id];
  if (!w) return;
  openModal(
    w.name,
    "Docker host details and maintenance controls.",
    `<div class="details-meta"><div><small>CONNECTIVITY</small>${badge(w.status)}</div><div><small>SCHEDULING</small>${badge(w.mode)}</div><div><small>LAST SEEN</small><b>${relative(w.lastSeen && new Date(w.lastSeen).getFullYear() > 1970 ? w.lastSeen : null)}</b></div><div><small>DOCKER</small><b>${esc(w.info.ServerVersion || "—")}</b></div><div><small>OPERATING SYSTEM</small><b>${esc(w.info.OperatingSystem || "—")}</b></div><div><small>CAPACITY</small><b>${w.info.NCPU || 0} vCPU · ${(w.info.MemTotal / 1024 / 1024 / 1024 || 0).toFixed(0)} GB</b></div></div><div class="notice mono">${esc(w.endpoint)} · ${esc(region(w))}</div>${w.error ? `<div class="error-text">${esc(w.error)}</div>` : ""}${w.ssh ? `<div class="notice note">SSH-managed worker · ${esc(w.ssh.user)}@${esc(w.ssh.host)}:${w.ssh.port} · Agent uses encrypted SSH forwarding</div>` : ""}${state.demo ? '<div class="notice note">Docker API gateway requires a live worker.</div>' : `<div class="notice"><b>Docker API via control plane</b><br><code>${esc(w.dockerAPI || `/docker/workers/${encodeURIComponent(w.id)}`)}</code><br>Authenticate with the control-plane bearer token. Stateless container requests at the root gateway use automatic placement. To target this worker explicitly, use <code>X-Zela-Worker: ${esc(w.id)}</code>. Update older worker agents before connecting.</div>`}${liveMetricSummary(w)}<div class="toolbar">${btn("Monitor worker", "monitor-worker-link", `data-worker="${esc(w.id)}"`, "", "pulse")}</div><div class="form-section">Host inventory</div>${inventory((w.containers || []).map((ct) => ({ ct, w })))}<div class="modal-actions">${w.status === "provision-failed" ? btn("Retry provisioning", "retry-provision", `data-id="${esc(id)}" ${busy() ? "disabled" : ""}`, "primary", "refresh") : ""}${btn(w.mode === "active" ? "Pause scheduling" : "Activate scheduling", "worker-action", `data-id="${esc(id)}" data-kind="${w.mode === "active" ? "pause" : "activate"}" ${busy() ? "disabled" : ""}`, "", w.mode === "active" ? "pause" : "play")}${btn("Drain worker", "confirm-drain", `data-id="${esc(id)}" ${busy() || w.status !== "online" ? "disabled" : ""}`, "", "move")}${btn("Unregister", "confirm-delete", `data-type="worker" data-id="${esc(id)}" ${busy() ? "disabled" : ""}`, "danger", "trash")}</div>`,
    btn("Done", "close"),
    "",
    true,
  );
}
function containerDetails(workerID, id) {
  const w = state.workers[workerID],
    ct = w?.containers?.find((c) => c.Id === id);
  if (!ct) return;
  const dep = deployments().find((d) =>
    d.tasks.some((t) => t.id === id && t.workerID === workerID),
  );
  openModal(
    ct.Names?.[0]?.replace(/^\//, "") || shortID(id),
    ct.Image,
    `<div class="details-meta"><div><small>STATE</small>${badge(ct.State === "running" ? "running" : "stopped", ct.State)}</div><div><small>WORKER</small><b>${esc(w.name)}</b></div><div><small>MANAGEMENT</small><b>${dep ? "Zela deployment" : "Existing Docker container"}</b></div></div><div class="notice mono">${esc(ct.Id)}<br>${esc(ct.Status)}</div>${dep ? `<p class="note">This container belongs to <b>${esc(dep.name)}</b>. Use deployment controls to manage its replicas.</p><div class="modal-actions">${btn("Manage deployment", "deployment-details", `data-id="${esc(dep.id)}"`, "primary", "layers")}${btn("View logs", "logs", `data-worker="${esc(workerID)}" data-id="${esc(id)}"`, "", "log")}</div>` : `<div class="modal-actions">${["start", "stop", "restart"].map((kind) => btn(kind[0].toUpperCase() + kind.slice(1), "container-action", `data-worker="${esc(workerID)}" data-id="${esc(id)}" data-kind="${kind}" ${busy() ? "disabled" : ""}`, "", { start: "play", stop: "pause", restart: "refresh" }[kind])).join("")}${btn("Logs", "logs", `data-worker="${esc(workerID)}" data-id="${esc(id)}"`, "", "log")}${btn("Delete", "confirm-delete", `data-type="container" data-worker="${esc(workerID)}" data-id="${esc(id)}" ${busy() ? "disabled" : ""}`, "danger", "trash")}</div>`}`,
    btn("Done", "close"),
  );
}
function confirmDelete(type, id, workerID = "") {
  let name =
    type === "deployment"
      ? state.deployments[id]?.name
      : type === "worker"
        ? state.workers[id]?.name
        : state.workers[workerID]?.containers.find((c) => c.Id === id)
            ?.Names?.[0] || id;
  openModal(
    type === "worker" ? "Unregister worker" : "Delete " + type,
    "Review this action before proceeding.",
    `<div class="notice warn">${type === "worker" ? `Remove <b>${esc(name)}</b> from the cluster registry. Workers with containers or managed replicas cannot be unregistered.` : `Delete <b>${esc(name)}</b> and its ${type === "deployment" ? "managed containers" : "container"}. Container filesystem data will be removed. This action cannot be undone.`}</div>`,
    `${btn("Cancel", "close")}${btn(type === "worker" ? "Unregister" : "Delete", "execute-delete", `data-type="${type}" data-id="${esc(id)}" data-worker="${esc(workerID)}"`, "danger", "trash")}`,
  );
}
function confirmDrain(id) {
  const w = state.workers[id];
  openModal(
    "Drain worker",
    `Prepare ${w.name} for maintenance.`,
    `<div class="notice warn">Zela will stop new scheduling on <b>${esc(w.name)}</b> and move its managed workloads to eligible workers. Each replacement is checked before its source is removed. Volumes, unavailable capacity, and unmanaged containers can prevent a complete drain. Successfully moved replicas stay on their new workers.</div>`,
    `${btn("Cancel", "close")}${btn("Drain worker", "worker-action", `data-id="${esc(id)}" data-kind="drain"`, "primary", "move")}`,
  );
}
async function logs(workerID, id) {
  openModal(
    "Container logs",
    `${state.workers[workerID]?.name || "Worker"} · Last 200 log lines`,
    `<pre class="code logs" id="log-output">Loading log stream…</pre>`,
    `${btn("Refresh logs", "reload-logs", `data-worker="${esc(workerID)}" data-id="${esc(id)}"`, "", "refresh")}${btn("Close", "close")}`,
    "",
    true,
  );
  try {
    const out = await api(
      `/workers/${encodeURIComponent(workerID)}/containers/${encodeURIComponent(id)}/logs`,
    );
    if ($("#log-output"))
      $("#log-output").textContent = out.logs || "No log output.";
  } catch (e) {
    if ($("#log-output")) $("#log-output").textContent = e.message;
  }
}
async function submit(path, body) {
  const o = await api(path, body);
  closeModal();
  toast(o.title + " · started");
  await load();
  return o;
}
function navigate(to) {
  page = pages[to] ? to : "overview";
  search = "";
  filter = "all";
  mobileOpen = false;
  location.hash = page;
  closeModal();
  render();
}
document.addEventListener("click", async (e) => {
  const el = e.target.closest("[data-action]");
  if (!el || el.disabled) return;
  const a = el.dataset.action,
    id = el.dataset.id,
    w = el.dataset.worker;
  try {
    switch (a) {
      case "notifications":
        showNotificationCenter();
        break;
      case "read-notification":
        await readNotifications([id]);
        break;
      case "read-all-notifications":
        await readNotifications(
          notificationItems()
            .filter((n) => !n.readAt)
            .map((n) => n.id),
        );
        break;
      case "open-notification":
        await openNotification(id);
        break;
      case "enable-desktop-notifications":
        await enableDesktopNotifications();
        break;
      case "disable-desktop-notifications":
        disableDesktopNotifications();
        break;
      case "nav":
        navigate(el.dataset.page);
        break;
      case "navigate-activity":
        navigate("activity");
        break;
      case "menu":
        mobileOpen = !mobileOpen;
        render();
        break;
      case "filter":
        filter = el.dataset.filter;
        render();
        break;
      case "refresh":
        await submit("/refresh", {});
        break;
      case "monitor-worker-link":
        monitorWorker = w;
        monitorContainer = "";
        navigate("monitoring");
        break;
      case "monitor-window":
        monitorWindow = el.dataset.window;
        render();
        break;
      case "monitor-history":
        await fetchMetricHistory(true);
        break;
      case "monitor-container":
        monitorContainer = id;
        render();
        break;
      case "alert-rules":
        alertRulesDialog();
        break;
      case "acknowledge-alert":
        await api(`/alerts/${id}/acknowledge`, {});
        await load();
        break;
      case "agent-update-dialog":
        agentUpdateDialog(id);
        break;
      case "execute-agent-update":
        await submit(`/workers/${id}/agent-update`, {});
        break;
      case "provision-worker":
        provisionDialog();
        break;
      case "new-mcp-token":
        newTokenDialog("mcp_manage");
        break;
      case "new-mcp-read-token":
        newTokenDialog("mcp_read");
        break;
      case "new-token":
        newTokenDialog();
        break;
      case "revoke-token":
        confirmTokenRevoke(id);
        break;
      case "execute-token-revoke":
        await api(`/tokens/${id}/revoke`, {});
        closeModal();
        toast("Integration token revoked");
        await load();
        break;
      case "new-secret":
        newSecretDialog();
        break;
      case "delete-secret":
        confirmSecretDelete(id);
        break;
      case "execute-secret-delete":
        await api(`/secrets/${id}/delete`, {});
        closeModal();
        toast("SSH key deleted");
        await load();
        break;
      case "retry-provision":
        await submit(`/workers/${id}/provision-retry`, {});
        break;
      case "operation-details":
        operationDetails(id);
        break;
      case "new-worker":
        newWorker();
        break;
      case "new-deployment":
        newDeployment(w);
        break;
      case "worker-details":
        workerDetails(id);
        break;
      case "deployment-details":
        deploymentDetails(id);
        break;
      case "container-details":
        containerDetails(w, id);
        break;
      case "scale":
        scaleDialog(id);
        break;
      case "move":
        moveDialog(id);
        break;
      case "move-replica":
        moveDialog(id, el.dataset.replica);
        break;
      case "update-image":
        updateDialog(id);
        break;
      case "deployment-action":
        await submit(`/deployments/${id}/actions`, { action: el.dataset.kind });
        break;
      case "worker-action":
        await submit(`/workers/${id}/actions`, { action: el.dataset.kind });
        break;
      case "container-action":
        await submit(`/workers/${w}/containers/${id}/actions`, {
          action: el.dataset.kind,
        });
        break;
      case "confirm-delete":
        confirmDelete(el.dataset.type, id, w);
        break;
      case "confirm-drain":
        confirmDrain(id);
        break;
      case "execute-delete":
        if (el.dataset.type === "deployment")
          await submit(`/deployments/${id}/actions`, { action: "delete" });
        else if (el.dataset.type === "worker")
          await submit(`/workers/${id}/actions`, { action: "delete" });
        else
          await submit(`/workers/${w}/containers/${id}/actions`, {
            action: "delete",
          });
        break;
      case "logs":
      case "reload-logs":
        await logs(w, id);
        break;
      case "logout":
        await api("/logout", {});
        showLogin();
        break;
      case "close":
        closeModal();
        break;
    }
  } catch (err) {
    if ($("#modal-error")) $("#modal-error").textContent = err.message;
    else toast(err.message, true);
  }
});
document.addEventListener("submit", async (e) => {
  e.preventDefault();
  const form = e.target,
    fields = Object.fromEntries(new FormData(form)),
    button = $('button[type="submit"]', form);
  if (button) button.disabled = true;
  const error = $("#modal-error") || $("#login-error");
  if (error) error.textContent = "";
  try {
    switch (form.id) {
      case "login-form":
        await api("/login", { token: fields.token });
        await load(true);
        break;
      case "alert-rules-form": {
        const rules = {};
        for (const key of [
          "cpuPercent",
          "memoryPercent",
          "diskPercent",
          "resourceForSeconds",
          "offlineForSeconds",
          "healthForSeconds",
        ])
          rules[key] = Number(fields[key]);
        await api("/monitoring/rules", rules);
        closeModal();
        toast("Monitoring thresholds saved");
        await load();
        break;
      }
      case "token-form": {
        const result = await api("/tokens", {
          name: fields.name,
          scope: fields.scope || "infrastructure",
          expiresInDays: Number(fields.expiresInDays),
        });
        closeModal();
        await load();
        showCreatedToken(result);
        break;
      }
      case "secret-form": {
        await api("/secrets", {
          name: fields.name,
          privateKey: fields.privateKey,
          passphrase: fields.passphrase,
        });
        closeModal();
        toast("SSH key encrypted and saved");
        await load();
        break;
      }
      case "provision-form": {
        await submit("/workers/provision", {
          name: fields.name,
          region: fields.region,
          ssh: {
            host: fields.host,
            port: Number(fields.port),
            user: fields.user,
            secretID: fields.secretID,
            hostKey: fields.hostKey,
            sudo: fields.sudo === "on",
            agentPort: Number(fields.agentPort),
          },
        });
        break;
      }
      case "worker-form":
        await submit("/workers", fields);
        break;
      case "deployment-form": {
        let command = [];
        if (fields.command.trim()) {
          command = JSON.parse(fields.command);
          if (
            !Array.isArray(command) ||
            command.some((v) => typeof v !== "string")
          )
            throw new Error("Command must be a JSON array of strings.");
        }
        const ports = fields.ports.trim()
          ? fields.ports.split(",").map((p) => {
              const [mapping, protocol = "tcp"] = p.trim().split("/"),
                parts = mapping.split(":");
              if (parts.length !== 2 || parts.some((v) => !/^\d+$/.test(v)))
                throw new Error(
                  "Ports must use host:container, for example 8080:80.",
                );
              return {
                host: Number(parts[0]),
                container: Number(parts[1]),
                protocol,
              };
            })
          : [];
        await submit("/deployments", {
          name: fields.name,
          image: fields.image,
          replicas: Number(fields.replicas),
          cpus: Number(fields.cpus),
          memoryMB: Number(fields.memoryMB),
          workerIDs: fields.placement ? [fields.placement] : [],
          ports,
          env: fields.env
            .split("\n")
            .map((v) => v.trim())
            .filter(Boolean),
          command,
        });
        break;
      }
      case "move-form":
        if (fields.replica)
          await submit(
            `/deployments/${fields.id}/replicas/${fields.replica}/move`,
            { target: fields.target },
          );
        else
          await submit(`/deployments/${fields.id}/actions`, {
            action: "move",
            target: fields.target,
          });
        break;
      case "image-form":
        await submit(`/deployments/${fields.id}/image`, {
          image: fields.image,
          target: fields.target,
        });
        break;
      case "scale-form":
        await submit(`/deployments/${fields.id}/actions`, {
          action: "scale",
          replicas: Number(fields.replicas),
        });
        break;
    }
  } catch (err) {
    if (error) error.textContent = err.message;
    else toast(err.message, true);
  } finally {
    if (button?.isConnected) button.disabled = false;
  }
});
document.addEventListener("input", (e) => {
  if (e.target.id === "global-search") {
    search = e.target.value;
    render();
  }
});
document.addEventListener("keydown", (e) => {
  if ((e.metaKey || e.ctrlKey) && e.key === "k") {
    e.preventDefault();
    $("#global-search")?.focus();
  }
  if (e.key === "Escape") {
    closeModal();
    if (mobileOpen) {
      mobileOpen = false;
      render();
    }
  }
  if (e.key === "Tab" && $("#modal-root").children.length) {
    const all = [
      ...$("#modal-root").querySelectorAll(
        'button:not([disabled]),input:not([type="hidden"]),select,textarea,a[href]',
      ),
    ];
    if (all.length) {
      if (e.shiftKey && document.activeElement === all[0]) {
        e.preventDefault();
        all.at(-1).focus();
      } else if (!e.shiftKey && document.activeElement === all.at(-1)) {
        e.preventDefault();
        all[0].focus();
      }
    }
  }
});
window.addEventListener("hashchange", () => {
  const to = location.hash.slice(1);
  if (pages[to] && to !== page) {
    page = to;
    search = "";
    filter = "all";
    closeModal();
    render();
  }
});
$("#app").innerHTML =
  '<div class="loading-screen">Connecting to your cluster…</div>';
load(true);
