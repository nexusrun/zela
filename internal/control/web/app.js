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
  mobileOpen = false,
  workerView = "table",
  settingsTab = "general",
  lastUpdated = null;
let modalCloseFocus = null,
  toastTimer = null;
const pages = {
  overview: "Overview",
  workers: "Workers",
  deployments: "Deployments",
  containers: "Containers",
  network: "Networking",
  storage: "Shared storage",
  monitoring: "Monitoring",
  alerts: "Alerts",
  activity: "Activity log",
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
  if (!Number.isFinite(sec)) return "never";
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
const inventoryFresh = (w) =>
  w.status === "online" &&
  Number.isFinite(new Date(w.lastSeen).getTime()) &&
  Date.now() - new Date(w.lastSeen).getTime() < 45000;
const timeLabel = (t) => {
  const date = new Date(t);
  if (!t || !Number.isFinite(date.getTime()) || date.getFullYear() <= 1970)
    return "Never";
  return `<time datetime="${esc(date.toISOString())}" title="${esc(date.toLocaleString())}">${relative(t)}</time>`;
};
const operationDuration = (operation) => {
  if (!operation.finishedAt)
    return operation.status === "running" ? "In progress" : "—";
  const seconds = Math.max(
    0,
    Math.round(
      (new Date(operation.finishedAt) - new Date(operation.startedAt)) / 1000,
    ),
  );
  if (!Number.isFinite(seconds)) return "—";
  if (seconds < 1) return "<1s";
  if (seconds < 60) return seconds + "s";
  if (seconds < 3600)
    return Math.floor(seconds / 60) + "m " + (seconds % 60) + "s";
  return (
    Math.floor(seconds / 3600) + "h " + Math.floor((seconds % 3600) / 60) + "m"
  );
};
const region = (w) => w.region || "Unassigned region";
const empty = (title, note, action = "", label = "") =>
  `<div class="empty">${icon("layers")}<b>${esc(title)}</b>${esc(note)}${action ? `<div>${btn(label, action, "", "primary", "plus")}</div>` : ""}</div>`;

async function api(path, body, method) {
  const res = await fetch("/api" + path, {
    method: method || (body === undefined ? "GET" : "POST"),
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
    lastUpdated = new Date();
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
  resetNetworkViews();
  clearInterval(poller);
  poller = null;
  state = null;
  closeModal();
  historySequence++;
  historyKey = "";
  historyBusy = false;
  monitorPoints = [];
  $("#app").innerHTML =
    `<div class="login-screen"><aside class="login-intro"><a class="brand" href="#overview">${brand}<span class="brand-edition">CONTROL PLANE</span></a><div class="login-story"><div class="eyebrow">YOUR INFRASTRUCTURE. YOUR CONTROL.</div><h1>A clear view.<br>A capable control plane.</h1><p>One workspace to operate your Docker fleet, manage workloads, and keep every host in view.</p><div class="login-features"><span>${icon("server")}Worker management</span><span>${icon("layers")}Workload orchestration</span><span>${icon("pulse")}Metrics & alerts</span></div></div><div class="login-security">${icon("shield")}Self-hosted infrastructure management</div></aside><main class="login-main" id="main-content" tabindex="-1"><section class="login-panel"><div class="login-lock">${icon("lock")}</div><h2>Sign in to Zela</h2><p>Enter your administrator token to access this cluster.</p><form id="login-form"><div class="form-group"><label for="token">Administrator token</label><input id="token" name="token" type="password" autocomplete="current-password" required placeholder="Enter your admin token"></div><div class="error-text" id="login-error" role="alert"></div><button class="btn primary" type="submit">Sign in to control plane ${icon("arrow")}</button></form><p class="login-hint">Use <code>ZELA_ADMIN_TOKEN</code> or the <code>admin-token</code> file in your data directory.</p><div class="login-session-note">${icon("shield")}Your token stays with this control plane.</div></section></main></div>`;
  $("#token")?.focus();
}
function shell() {
  const online = workers().filter((w) => w.status === "online").length;
  const activeAlerts = (state.alerts || []).filter(
    (a) => a.active && !a.acknowledged,
  ).length;
  const navItem = (p, label) => {
    const count =
      p === "workers"
        ? workers().length
        : p === "deployments"
          ? deployments().length
          : p === "alerts"
            ? activeAlerts
            : null;
    return `<button id="nav-${p}" class="nav-item ${page === p ? "active" : ""}" data-action="nav" data-page="${p}" ${page === p ? 'aria-current="page"' : ""}>${icon({ overview: "grid", workers: "server", deployments: "layers", containers: "box", network: "globe", monitoring: "pulse", alerts: "bell", activity: "clock", secrets: "lock", settings: "settings" }[p])}<span>${esc(label)}</span>${count !== null ? `<span class="count ${p === "alerts" && count ? "attention" : ""}">${count}</span>` : ""}</button>`;
  };
  const groups = [
    ["Workspace", [["overview", "Overview"]]],
    [
      "Infrastructure",
      [
        ["workers", "Workers"],
        ["deployments", "Deployments"],
        ["containers", "Containers"],
        ["network", "Networking"],
        ["storage", "Shared storage"],
      ],
    ],
    [
      "Observability",
      [
        ["monitoring", "Monitoring"],
        ["alerts", "Alerts"],
        ["activity", "Activity log"],
      ],
    ],
    [
      "Administration",
      [
        ["secrets", "Secrets vault"],
        ["settings", "Settings"],
      ],
    ],
  ];
  return `<div class="shell">${mobileOpen ? '<button class="sidebar-scrim" data-action="menu" aria-label="Close navigation"></button>' : ""}<aside class="sidebar ${mobileOpen ? "open" : ""}"><a class="brand" href="#overview" aria-label="Zela cluster overview">${brand}<span class="brand-edition">ADMIN</span></a><div class="cluster-switch">${icon("globe")}<div><b>${state.demo ? "demo-cluster" : "default-cluster"}</b><span>${state.demo ? "Simulation environment" : "Docker infrastructure"}</span></div><span class="environment-dot ${offline ? "disconnected" : ""}"></span></div><nav aria-label="Main navigation">${groups.map(([label, items]) => `<div class="nav-group"><div class="nav-label">${label}</div><div class="nav">${items.map(([p, label]) => navItem(p, label)).join("")}</div></div>`).join("")}</nav><div class="sidebar-bottom"><div class="sidebar-health"><i class="dot ${online === workers().length && online > 0 ? "" : "gray"}"></i><span>${online} of ${workers().length} workers online</span><button class="icon-button" data-action="nav" data-page="monitoring" aria-label="View cluster monitoring">${icon("arrow")}</button></div><div class="admin-profile"><span class="avatar">AD</span><div><b>Administrator</b><span>Cluster administrator</span></div><button class="icon-button" data-action="logout" aria-label="Sign out">${icon("logout")}</button></div></div></aside><main class="main" id="main-content" tabindex="-1"><header class="topbar"><div class="breadcrumbs"><button class="mobile-menu" data-action="menu" aria-label="Toggle navigation" aria-expanded="${mobileOpen}">${icon("menu")}</button><span class="breadcrumb-cluster">${icon("globe")}${state.demo ? "demo-cluster" : "default-cluster"}</span>${icon("chevron")}<b>${esc(pages[page])}</b></div><div class="top-controls">${page === "settings" ? "" : `<div class="search">${icon("search")}<input id="global-search" aria-label="Search cluster" placeholder="${esc(searchPlaceholder())}" value="${esc(search)}" autocomplete="off"><kbd class="shortcut">⌘ K</kbd></div>`}<div class="live" title="${lastUpdated ? "Last synced " + lastUpdated.toLocaleTimeString() : "Waiting for cluster state"}"><i class="dot ${offline ? "gray" : ""}"></i>${offline ? "Disconnected" : "Live"}</div>${notificationBell()}<span class="environment-badge">${state.demo ? "DEMO" : "LIVE CLUSTER"}</span></div></header><div class="content">${offline ? `<div class="connection-error" role="alert">${icon("info")}<span><b>Connection interrupted.</b> ${esc(offline)}. Reconnecting automatically.</span></div>` : ""}${state.demo ? `<div class="demo-banner">${icon("info")}<span><b>Demo environment</b><span class="banner-separator">/</span>Explore the console with simulated workers and workloads.</span><span class="banner-label">SANDBOX</span></div>` : ""}${busy() ? `<div class="op-banner">${icon("refresh", "spin")}<span>${esc(state.operations.find((o) => o.status === "running").title)}</span>${btn("View operation", "operation-details", `data-id="${esc(state.operations.find((o) => o.status === "running").id)}"`, "text", "arrow")}</div>` : ""}${activeAlerts ? `<div class="notice warn alert-banner">${icon("shield")}<span>${activeAlerts} active alert${activeAlerts === 1 ? "" : "s"} need attention</span>${btn("Review alerts", "nav", 'data-page="alerts"', "text", "arrow")}</div>` : ""}${state.monitoringError ? `<div class="notice warn">${esc(state.monitoringError)}</div>` : ""}${body()}<footer class="content-footer"><span>${icon("shield")}Authenticated administrator session</span><span>${lastUpdated ? `Last synced ${esc(lastUpdated.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }))} · ` : ""}Inventory polls every 10s</span></footer></div></main></div>`;
}
function searchPlaceholder() {
  return (
    {
      workers: "Search workers…",
      deployments: "Search deployments…",
      containers: "Search containers…",
      network: "Search networks and services…",
      activity: "Search operations…",
      secrets: "Search SSH keys…",
      alerts: "Search alerts…",
    }[page] || "Search cluster…"
  );
}
// Inline forms live inside the polled app shell. Preserve their draft values
// across redraws; modal forms are outside this shell and need no restoration.
function captureInlineForms(app) {
  return Array.from(app.querySelectorAll?.("form.storage-form") || [], (form) => ({
    id: form.id,
    fields: new Map(Array.from(form.elements)
      .filter((field) => field.name && field.type !== "submit")
      .map((field) => [field.id || `${field.name}:${field.value}`, {
        value: field.value,
        checked: field.checked,
      }])),
  }));
}
function restoreInlineForms(forms) {
  for (const saved of forms) {
    const form = $("#" + saved.id);
    if (!form) continue;
    for (const field of form.elements) {
      const draft = saved.fields.get(field.id || `${field.name}:${field.value}`);
      if (!draft) continue;
      if (field.type === "checkbox" || field.type === "radio")
        field.checked = draft.checked;
      else if (field.type !== "submit") field.value = draft.value;
    }
  }
}
function resetInlineForm(form) {
  // A poll may replace the submitted form while the request is in flight.
  // Reset the visible instance too, so a successful save clears its draft.
  const current = $("#" + form.id);
  form.reset();
  if (current && current !== form) current.reset();
}
function render() {
  if (!state) return;
  const app = $("#app");
  const drafts = captureInlineForms(app);
  const focused = document.activeElement;
  const focusedId = focused?.id;
  const start = focused?.selectionStart;
  const end = focused?.selectionEnd;
  const direction = focused?.selectionDirection;
  app.innerHTML = shell();
  restoreInlineForms(drafts);
  if (focusedId) {
    const target = document.getElementById?.(focusedId) || $("#" + focusedId);
    target?.focus({ preventScroll: true });
    if (typeof start === "number")
      target?.setSelectionRange?.(start, end ?? start, direction);
  }
}

function pageHead(title, sub, type = "deploy") {
  return `<div class="page-head"><div><div class="eyebrow">${page === "overview" ? "WORKSPACE" : ["secrets", "settings"].includes(page) ? "ADMINISTRATION" : ["monitoring", "alerts", "activity"].includes(page) ? "OBSERVABILITY" : "INFRASTRUCTURE"}</div><h1>${esc(title)}</h1><p class="subtitle">${esc(sub)}</p></div><div class="head-actions">${btn("Refresh", "refresh", busy() ? "disabled" : "", "", "refresh")}${type === "deploy" ? btn("Deploy application", "new-deployment", busy() ? "disabled" : "", "primary", "plus") : type === "worker" ? btn("Connect worker", "new-worker", busy() ? "disabled" : "", "", "plus") + btn("Provision worker", "provision-worker", busy() ? "disabled" : "", "primary", "server") : type === "secret" ? btn("Add SSH key", "new-secret", busy() ? "disabled" : "", "primary", "lock") : ""}</div></div>`;
}
function metrics() {
  const ws = workers(),
    online = ws.filter((w) => w.status === "online"),
    deps = deployments();
  const totalContainers = ws.reduce(
    (n, w) => n + (w.containers?.length || 0),
    0,
  );
  const liveContainers = online.reduce((n, w) => n + running(w), 0);
  const cpu = online.reduce((n, w) => n + (w.info?.NCPU || 0), 0);
  const memory =
    online.reduce((n, w) => n + (w.info?.MemTotal || 0), 0) / 1024 ** 3;
  const cpuReserved = online.reduce((n, w) => n + reservations(w).cpu, 0);
  const memoryReserved =
    online.reduce((n, w) => n + reservations(w).memory, 0) / 1024;
  const values = [
    [
      "Connected workers",
      online.length,
      `of ${ws.length} registered`,
      `${ws.filter((w) => w.mode === "active" && inventoryFresh(w)).length} accepting workloads`,
      "server",
      "workers",
    ],
    [
      "Running containers",
      liveContainers,
      `of ${totalContainers} total`,
      `${deps.length} managed deployments`,
      "box",
      "containers",
    ],
    [
      "CPU capacity",
      cpu,
      "vCPU online",
      `${cpuReserved.toFixed(1)} vCPU reserved`,
      "cpu",
      "workers",
    ],
    [
      "Memory capacity",
      memory.toFixed(1),
      "GiB online",
      `${memoryReserved.toFixed(1)} GiB reserved`,
      "layers",
      "workers",
    ],
  ];
  return `<section class="metrics" aria-label="Cluster metrics">${values.map(([label, value, note, detail, ic, to]) => `<article class="metric"><div class="metric-label">${label}${icon(ic)}</div><div class="metric-value">${value}<small>${note}</small></div><div class="metric-note"><span>${detail}</span><button class="metric-link" data-action="nav" data-page="${to}" aria-label="View ${label.toLowerCase()}">${icon("arrow")}</button></div></article>`).join("")}</section>`;
}
function clusterHealth() {
  const ws = workers(),
    unavailable = ws.filter((w) => w.status !== "online").length;
  const unhealthy = deployments().filter(
    (d) => !["running", "stopped"].includes(d.status),
  ).length;
  const stale = ws.filter(
    (w) => w.status === "online" && !inventoryFresh(w),
  ).length;
  const managerUnavailable = state.highAvailability?.ready === false;
  const alertCount = (state.alerts || []).filter(
    (a) => a.active && !a.acknowledged,
  ).length;
  const disconnected = Boolean(offline);
  const needsAttention =
    disconnected ||
    unavailable ||
    unhealthy ||
    alertCount ||
    stale ||
    managerUnavailable;
  const title = disconnected
    ? "Cluster connection interrupted"
    : !ws.length
      ? "Ready for your first worker"
      : needsAttention
        ? "Cluster needs attention"
        : "Cluster is operational";
  const detail = disconnected
    ? "Displaying the last received cluster state."
    : managerUnavailable
      ? "The active manager is not ready. Check manager availability."
      : stale
        ? `${stale} worker${stale === 1 ? " has" : "s have"} stale inventory. Check connectivity before placing workloads.`
        : !ws.length
          ? "Connect a Docker host to begin deploying applications."
          : `${ws.filter((w) => w.status === "online").length} workers online · ${unhealthy ? unhealthy + " deployments need attention" : "No degraded deployments"}`;
  return `<section class="health-summary ${needsAttention ? "attention" : !ws.length ? "neutral" : ""}" aria-label="Cluster health"><div class="health-emblem">${icon(needsAttention ? "info" : !ws.length ? "server" : "check")}</div><div><h2>${title}</h2><p>${detail}</p></div><div class="health-actions"><span class="health-label">${state.highAvailability ? esc(state.highAvailability.role) + " manager" : "Single manager"}</span>${btn(!ws.length ? "Connect worker" : needsAttention ? "Inspect cluster" : "View monitoring", !ws.length ? "new-worker" : "nav", !ws.length ? "" : `data-page="${unavailable || stale ? "workers" : unhealthy ? "deployments" : alertCount ? "alerts" : "monitoring"}"`, "text", "arrow")}</div></section>`;
}
function capacityPanel() {
  const online = workers().filter((w) => w.status === "online");
  const cpu = online.reduce((n, w) => n + (w.info?.NCPU || 0), 0),
    mem = online.reduce((n, w) => n + (w.info?.MemTotal || 0), 0) / 1024 ** 3;
  const usedCPU = online.reduce((n, w) => n + reservations(w).cpu, 0),
    usedMem = online.reduce((n, w) => n + reservations(w).memory, 0) / 1024;
  const capacity = (label, used, total, unit) =>
    `<div class="capacity-row"><div><span>${label}</span><b>${used.toFixed(1)} <span>/ ${total.toFixed(1)} ${unit}</span></b></div><progress value="${Math.min(used, total)}" max="${total || 1}" aria-label="${label} reserved"></progress><small>${total ? Math.round((used / total) * 100) + "% reserved" : "No online capacity"}</small></div>`;
  return `<section class="panel"><div class="panel-head"><h2>Resource allocation</h2><span class="panel-kicker">ONLINE WORKERS</span></div><div class="capacity-body">${capacity("CPU", usedCPU, cpu, "vCPU")}${capacity("Memory", usedMem, mem, "GiB")}<p class="note">Reservations from managed workloads. See Monitoring for measured usage.</p></div></section>`;
}
function workerTable(list, overview = false) {
  const content = list.length
    ? `<div class="table-wrap"><table class="worker-table"><thead><tr><th scope="col">WORKER</th><th scope="col">STATUS</th><th scope="col">REGION</th><th scope="col">CPU RESERVED</th><th scope="col">MEMORY RESERVED</th><th scope="col">CONTAINERS</th>${overview ? "" : '<th scope="col">LAST SEEN</th>'}<th scope="col"></th></tr></thead><tbody>${list
        .map((w) => {
          const r = reservations(w),
            cpu = w.info?.NCPU || 0,
            memory = (w.info?.MemTotal || 0) / 1024 ** 3;
          return `<tr><td><div class="deployment-cell"><div class="server-tile">${icon("server")}</div><div><button class="name-link" data-action="worker-details" data-id="${esc(w.id)}">${esc(w.name)}</button><div class="small-sub mono" title="${esc(w.endpoint)}">${esc((w.endpoint || "").replace(/^https?:\/\//, ""))}</div></div></div></td><td>${badge(w.status === "online" && !inventoryFresh(w) ? "stopped" : w.status, w.status === "online" && !inventoryFresh(w) ? "Stale inventory" : undefined)}${w.mode !== "active" ? `<div class="small-sub">${esc(w.mode)}</div>` : ""}</td><td><span class="region-cell">${icon("globe")}${esc(w.region || "Unassigned")}</span></td><td class="table-resource"><b>${r.cpu.toFixed(1)} <span>/ ${cpu} cores</span></b><progress value="${Math.min(r.cpu, cpu)}" max="${cpu || 1}" aria-label="CPU reserved on ${esc(w.name)}"></progress></td><td class="table-resource memory"><b>${(r.memory / 1024).toFixed(1)} <span>/ ${memory.toFixed(1)} GiB</span></b><progress value="${Math.min(r.memory / 1024, memory)}" max="${memory || 1}" aria-label="Memory reserved on ${esc(w.name)}"></progress></td><td><span class="container-count">${running(w)}<small>${inventoryFresh(w) ? " running" : " cached"}</small></span></td>${overview ? "" : `<td class="table-time">${timeLabel(w.lastSeen)}</td>`}<td>${btn("Manage", "worker-details", `data-id="${esc(w.id)}" aria-label="Manage ${esc(w.name)}"`, "compact", "chevron")}</td></tr>`;
        })
        .join("")}</tbody></table></div>`
    : empty(
        search ? "No matching workers" : "No workers connected",
        search
          ? "Clear the search or choose a different status."
          : "Connect a Docker host or provision an existing Linux server.",
        "new-worker",
        "Connect worker",
      );
  return `<section class="panel"><div class="panel-head"><div class="panel-title"><h2>${overview ? "Worker fleet" : "Registered workers"}</h2><span class="total-count">${list.length}</span></div>${overview ? btn("View all workers", "nav", 'data-page="workers"', "text", "arrow") : `<div class="view-switch" aria-label="Worker view"><button id="worker-view-table" data-action="worker-view" data-view="table" class="${workerView === "table" ? "selected" : ""}" aria-label="Table view" aria-pressed="${workerView === "table"}">${icon("menu")}</button><button id="worker-view-grid" data-action="worker-view" data-view="grid" class="${workerView === "grid" ? "selected" : ""}" aria-label="Grid view" aria-pressed="${workerView === "grid"}">${icon("grid")}</button></div>`}</div>${!overview && workerView === "grid" && list.length ? `<div class="worker-grid full-workers">${list.map((w) => workerCard(w, true)).join("")}</div>` : content}<div class="panel-footer"><span>${overview ? "CPU and memory represent workload reservations" : "Pause blocks new placement. Drain relocates managed workloads."}</span><span>${list.length} worker${list.length === 1 ? "" : ""}</span></div></section>`;
}
function workerCard(w, full = false) {
  const r = reservations(w),
    total = w.info.MemTotal / 1024 / 1024 / 1024 || 0,
    cpuPct = w.info.NCPU ? Math.min(100, (r.cpu / w.info.NCPU) * 100) : 0,
    memPct = total ? Math.min(100, (r.memory / 1024 / total) * 100) : 0;
  return `<article class="worker-card"><div class="worker-title"><div class="server-tile">${icon("server")}</div><div><button class="worker-name" data-action="worker-details" data-id="${esc(w.id)}">${esc(w.name)}</button><div class="worker-endpoint mono" title="${esc(w.endpoint)}">${esc(w.endpoint.replace(/^https?:\/\//, ""))}</div></div>${badge(w.status)}</div><div class="worker-tags"><span class="tag">${esc(region(w))}</span><span class="tag">${esc(w.info.Architecture || "—")}</span>${w.mode !== "active" ? badge(w.mode) : ""}</div><div class="resource"><div class="resource-label"><span>CPU reserved</span><b>${r.cpu.toFixed(1)} / ${w.info.NCPU || "—"} cores</b></div><progress aria-label="CPU reserved" value="${cpuPct}" max="100"></progress></div><div class="resource memory"><div class="resource-label"><span>Memory reserved</span><b>${(r.memory / 1024).toFixed(1)} / ${total.toFixed(0)} GB</b></div><progress aria-label="Memory reserved" value="${memPct}" max="100"></progress></div><div class="worker-foot"><span>${icon("box")}${running(w)} running containers</span><span>${icon("clock")}${relative(w.lastSeen && new Date(w.lastSeen).getFullYear() > 1970 ? w.lastSeen : null)}</span></div>${full ? `<div class="worker-actions">${btn("Manage worker", "worker-details", `data-id="${esc(w.id)}"`, "", "settings")}${btn("Deploy here", "new-deployment", `data-worker="${esc(w.id)}" ${w.mode !== "active" || w.status !== "online" || busy() ? "disabled" : ""}`, "green", "plus")}</div>${w.error ? `<div class="worker-error">${esc(w.error)}</div>` : ""}${w.status === "provision-failed" ? `<div class="worker-actions">${btn("Retry provisioning", "retry-provision", `data-id="${esc(w.id)}" ${busy() ? "disabled" : ""}`, "", "refresh")}</div>` : ""}` : ""}</article>`;
}
function deploymentTable(list, overview = false) {
  return `<section class="panel"><div class="panel-head"><div><h2>${overview ? "Deployments" : "Application deployments"}</h2>${overview ? "<p>Workloads running across your cluster</p>" : ""}</div><div class="right">${badge("stopped", `${list.length} total`)}${overview ? '<button class="text-link" data-action="nav" data-page="deployments">View all ' + icon("arrow") + "</button>" : ""}</div></div>${
    list.length
      ? `<div class="table-wrap"><table><thead><tr><th scope="col">APPLICATION</th><th scope="col">STATUS</th><th scope="col">REPLICAS</th><th scope="col">PLACEMENT</th><th scope="col"></th></tr></thead><tbody>${list
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
function recent() {
  const operations = state.operations.slice(0, 5);
  return `<section class="panel"><div class="panel-head"><h2>Recent operations</h2>${btn("View activity", "nav", 'data-page="activity"', "text", "arrow")}</div><div class="activity-list">${operations.map((o) => `<button class="activity-item" data-action="operation-details" data-id="${esc(o.id)}"><span class="activity-icon ${esc(o.status)}">${icon(o.status === "failed" ? "close" : o.status === "running" ? "refresh" : "check")}</span><span class="activity-copy"><span class="activity-title">${esc(o.title)}</span><span class="activity-time">${relative(o.startedAt)} · ${esc(o.status)}</span></span>${icon("chevron")}</button>`).join("") || empty("No operations yet", "Deployment and worker changes will appear here.")}</div></section>`;
}
function filterItems() {
  if (page === "workers") return workers();
  if (page === "deployments") return deployments();
  if (page === "activity") return state.operations;
  if (page === "alerts") return state.alerts || [];
  if (page === "containers")
    return workers().flatMap((w) => w.containers || []);
  return [];
}
function filterMatches(item, status) {
  if (status === "all") return true;
  if (page === "alerts")
    return status === "active" ? item.active : !item.active;
  if (page === "containers")
    return status === "running"
      ? item.State === "running"
      : item.State !== "running";
  if (page === "workers" && status === "maintenance")
    return ["paused", "draining"].includes(item.mode);
  return item.status === status;
}
function filters(statuses) {
  const items = filterItems();
  return `<div class="toolbar list-toolbar"><div class="status-filters" role="group" aria-label="Filter ${esc(pages[page].toLowerCase())} by status">${statuses.map((status) => `<button id="filter-${page}-${status}" class="pill-filter ${filter === status ? "active" : ""}" data-action="filter" data-filter="${status}" aria-pressed="${filter === status}">${esc(status === "all" ? "All " + pages[page].toLowerCase() : status[0].toUpperCase() + status.slice(1))}<span>${items.filter((item) => filterMatches(item, status)).length}</span></button>`).join("")}</div><span class="filter-count">${search ? `Results for “${esc(search)}” ${btn("Clear", "clear-search", "", "text", "close")}` : ""}</span></div>`;
}
function inventory(list) {
  return list.length
    ? `<div class="table-wrap"><table><thead><tr><th scope="col">CONTAINER</th><th scope="col">IMAGE</th><th scope="col">WORKER</th><th scope="col">STATE</th><th scope="col"></th></tr></thead><tbody>${list.map(({ ct, w }) => `<tr><td><div class="deployment-cell"><div class="app-tile">${icon("box")}</div><div><button class="name-link" data-action="container-details" data-worker="${esc(w.id)}" data-id="${esc(ct.Id)}">${esc(ct.Names?.[0]?.replace(/^\//, "") || shortID(ct.Id))}</button><div class="small-sub mono">${esc(shortID(ct.Id))} ${ct.Labels?.["zela.managed"] === "true" ? "· managed" : "· standalone"}</div></div></div></td><td class="mono truncated">${esc(ct.Image)}</td><td>${esc(w.name)}</td><td>${badge(ct.State === "running" ? "running" : ct.State === "exited" ? "stopped" : ct.State, ct.State)}${w.status === "offline" ? '<div class="small-sub">Worker offline · cached</div>' : ""}</td><td><div class="row"><button class="dots-button" data-action="logs" data-worker="${esc(w.id)}" data-id="${esc(ct.Id)}" aria-label="Logs for ${esc(ct.Names?.[0] || shortID(ct.Id))}">${icon("log")}</button><button class="dots-button" data-action="container-details" data-worker="${esc(w.id)}" data-id="${esc(ct.Id)}" aria-label="Manage ${esc(ct.Names?.[0] || shortID(ct.Id))}">${icon("dots")}</button></div></td></tr>`).join("")}</tbody></table></div>`
    : empty(
        "No containers to display",
        "Connect a worker or deploy an application to see containers here.",
      );
}

function accessTokensPanel() {
  const tokens = state.tokens || [];
  return `<section class="panel access-tokens"><div class="panel-head"><h2>Integration API tokens</h2>${btn("Create token", "new-token", "", "primary", "plus")}</div><div class="settings-body"><p>Give an external platform or scheduler an infrastructure token. It can discover worker health and capacity, select a worker, and manage containers it creates through the cluster endpoint. Zela handles application scheduling and lifecycle. Worker management, SSH secrets, platform settings and other tokens are blocked.</p><p>Tokens support stateless containers, image pulls, lifecycle, logs and exec. Privileged containers, host access, builds and volume/network administration require administrator access.</p>${
    tokens.length
      ? `<div class="table-wrap"><table><thead><tr><th scope="col">TOKEN</th><th scope="col">STATUS</th><th scope="col">EXPIRES</th><th scope="col"></th></tr></thead><tbody>${tokens
          .map((t) => {
            const status = t.revokedAt
              ? "Revoked"
              : t.expiresAt && new Date(t.expiresAt) <= new Date()
                ? "Expired"
                : "Active";
            return `<tr><td><b>${esc(t.name)}</b><div class="small-sub mono">${esc(t.prefix)}…</div><div class="small-sub">${esc({ infrastructure: "Infrastructure · Zela chooses workers", deployments: "Deployments · automatic placement", mcp_read: "MCP · Read cluster", mcp_manage: "MCP · Manage cluster" }[t.scope] || t.scope)} · Created ${esc(new Date(t.createdAt).toLocaleDateString())}</div></td><td>${esc(status)}</td><td>${t.expiresAt ? esc(new Date(t.expiresAt).toLocaleDateString()) : "Never"}</td><td>${status === "Active" ? btn("Revoke", "revoke-token", `data-id="${esc(t.id)}"`, "text", "trash") : ""}</td></tr>`;
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
function mcpEndpoint() {
  const path = state.mcp?.endpoint || "/mcp";
  return path.startsWith("/") && location.origin
    ? location.origin + path
    : path;
}
function mcpPanel() {
  const connections = (state.tokens || []).filter((token) =>
    ["mcp_read", "mcp_manage"].includes(token.scope),
  );
  return `<section class="panel"><div class="panel-head"><h2>AI cluster management</h2>${badge(state.mcp?.oauthEnabled ? "online" : "stopped", state.mcp?.oauthEnabled ? "OAuth enabled" : "Token access")}</div><div class="settings-body"><p>Connect ChatGPT, Claude, Codex, or another MCP client to this cluster.</p><label class="endpoint-label">Streamable HTTP endpoint</label><div class="endpoint-box"><code>${esc(mcpEndpoint())}</code>${btn("Copy endpoint", "copy-mcp-endpoint", "", "compact", "layers")}</div><div class="notice">${state.mcp?.oauthEnabled ? "Add this endpoint as a custom connector and choose OAuth. Approve read or management access on this cluster’s sign-in page." : "For ChatGPT and Claude browser connections, set ZELA_MCP_PUBLIC_URL to your public HTTPS cluster origin and restart the server. Local clients can connect now with a token."}</div><div class="permission-grid"><div><h3>${icon("shield")}Read access</h3><p>Inspect health, capacity, inventory, logs, and operation results.</p>${btn("Create read-only token", "new-mcp-read-token", "", "", "plus")}</div><div><h3>${icon("settings")}Management access</h3><p>Deploy and scale applications, manage containers, and maintain workers across the cluster.</p>${btn("Create management token", "new-mcp-token", "", "primary", "plus")}</div></div><details class="setup-details"><summary>Connect a local coding assistant</summary><p>Save a token in a private CLI profile, then configure your assistant to run:</p><pre class="code">zela --profile production mcp serve</pre><p>Add <code>--read-only</code> to restrict the local tool catalog.</p></details></div></section><section class="panel settings-section-spacer"><div class="panel-head"><h2>AI access credentials</h2><span class="total-count">${connections.length}</span></div>${
    connections.length
      ? `<div class="table-wrap"><table><thead><tr><th scope="col">CONNECTION</th><th scope="col">PERMISSIONS</th><th scope="col">STATUS</th><th scope="col"></th></tr></thead><tbody>${connections
          .map((token) => {
            const active =
              !token.revokedAt &&
              (!token.expiresAt || new Date(token.expiresAt) > new Date());
            return `<tr><td><b>${esc(token.name)}</b><div class="small-sub mono">${esc(token.prefix)}…</div></td><td>${token.scope === "mcp_manage" ? "Management" : "Read only"}</td><td>${badge(active ? "online" : "stopped", token.revokedAt ? "Revoked" : active ? "Active" : "Expired")}</td><td>${active ? btn("Revoke", "revoke-token", `data-id="${esc(token.id)}"`, "text", "trash") : ""}</td></tr>`;
          })
          .join("")}</tbody></table></div>`
      : empty(
          "No AI credentials yet",
          "OAuth connections and MCP tokens will appear here.",
        )
  }</section>`;
}
function settings() {
  const tabs = {
    general: "General",
    access: "API access",
    ai: "AI connections",
    notifications: "Notifications",
    setup: "Worker setup",
  };
  const sections = {
    general: () =>
      `<div class="settings-section-intro"><h2>Cluster configuration</h2><p>Connection, storage, and manager information for this control plane.</p></div><div class="settings-grid"><section class="panel"><div class="panel-head"><h2>Control-plane configuration</h2>${icon("settings")}</div><div class="settings-body"><p>Configuration uses command-line flags and environment variables. Restart the server to apply changes.</p><dl><dt>Admin authentication</dt><dd><code>ZELA_ADMIN_TOKEN</code> or generated <code>admin-token</code></dd><dt>State persistence</dt><dd>${state.persistence === "postgresql" ? "PostgreSQL · schema initialized on startup" : "Atomic JSON writes · mode 0600"}</dd><dt>Transport security</dt><dd><code>--tls-cert</code> and <code>--tls-key</code></dd><dt>Docker API gateway</dt><dd>Zela worker selection · Control-plane bearer token</dd><dt>Manager availability</dt><dd>${state.highAvailability ? `Active/standby · ${esc(state.highAvailability.managerID)} · ${esc(state.highAvailability.role)}` : "Single manager"}</dd><dt>Worker inventory polling</dt><dd>Every 10 seconds · 5-second connection timeout</dd><dt>Current environment</dt><dd>${state.demo ? "Demo · simulated Docker engines" : "Live Docker hosts"}</dd></dl></div></section><section class="panel"><div class="panel-head"><h2>AI cluster management</h2>${badge(state.mcp?.oauthEnabled ? "online" : "stopped", state.mcp?.oauthEnabled ? "OAuth enabled" : "Token access")}</div><div class="settings-body"><p>Connect ChatGPT, Claude, or a coding assistant to the cluster.</p><div class="endpoint-box"><code>${esc(state.mcp?.endpoint || "/mcp")}</code></div>${btn("Configure AI connections", "settings-tab", 'data-tab="ai"', "", "arrow")}</div></section></div>`,
    access: () =>
      `<div class="settings-section-intro"><h2>Integration credentials</h2><p>Create scoped credentials and revoke access for external clients.</p></div>${accessTokensPanel()}<div class="settings-section-spacer"><section class="panel"><div class="panel-head"><h2>Docker API access</h2>${icon("server")}</div><div class="settings-body"><p>Connect to this control plane on port 8080 (or your configured port). Create a Zela infrastructure token here. Zela discovers workers at <code>GET /api/cluster/workers</code> and selects a worker for creation or image pulls with <code>X-Zela-Worker: &lt;id&gt;</code>. The gateway checks capacity and connectivity and never changes the selected worker. Later container and exec requests route to their persisted owner.</p><p>Use standard Docker API paths with the worker header, or <code>/docker/workers/&lt;worker-id&gt;/</code>. Scoped tokens see only their own containers, including when a worker is selected. Worker health, capacity reservations, unavailable ports and fresh host usage are available through the infrastructure API. Worker administration, builds and network/volume administration require administrator access. Existing deployment tokens retain automatic placement.</p><p>Existing workers need the updated agent. Docker API workloads appear in Containers. Placement and ownership are persisted; clients manage application lifecycle. Default limits are 128 MB and 0.25 CPU.</p>${state.demo ? '<p class="notice">Docker API forwarding requires live workers.</p>' : ""}</div></section></div>`,
    ai: () =>
      `<div class="settings-section-intro"><h2>AI connections</h2><p>Manage your cluster from assistants through the Model Context Protocol.</p></div>${mcpPanel()}`,
    notifications: () =>
      `<div class="settings-section-intro"><h2>Notification preferences</h2><p>Choose how this browser receives cluster events.</p></div>${notificationsSettingsPanel()}`,
    setup:
      () => `<div class="settings-section-intro"><h2>Worker setup</h2><p>Install an agent or connect an existing Docker host.</p></div><section class="panel"><div class="panel-head"><h2>Install a worker agent</h2>${icon("server")}</div><div class="settings-body"><p>Build the Go agent, copy it to a Docker host, and run it with access to the Docker socket. The control plane connects to this authenticated endpoint.</p><pre class="code"># Build for a Linux worker
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -o bin/zela-worker ./cmd/worker

# Run on your worker host
export ZELA_WORKER_TOKEN="$(openssl rand -hex 32)"
./zela-worker --listen 0.0.0.0:9091 \
  --tls-cert /etc/zela/worker.crt \
  --tls-key /etc/zela/worker.key</pre><p>Use a certificate trusted by the control-plane host. Restrict the worker port to your private network.</p>${btn("Connect a worker", "new-worker", "", "primary", "plus")}</div></section>`,
  };
  return `${pageHead("Settings", "Configure cluster access, integrations, and administrative preferences.", "none")}<div class="settings-tabs" role="tablist" aria-label="Settings sections">${Object.entries(
    tabs,
  )
    .map(
      ([id, label]) =>
        `<button role="tab" id="settings-tab-${id}" aria-controls="settings-content" aria-selected="${settingsTab === id}" tabindex="${settingsTab === id ? 0 : -1}" data-action="settings-tab" data-tab="${id}" class="${settingsTab === id ? "active" : ""}">${label}</button>`,
    )
    .join(
      "",
    )}</div><div id="settings-content" role="tabpanel" aria-labelledby="settings-tab-${settingsTab}">${sections[settingsTab]()}</div>`;
}
function body() {
  if (page === "network") return networkingPage();
  if (page === "storage") return storagePage();
  if (page === "secrets") return vaultPage();
  if (page === "monitoring") return monitoringPage();
  if (page === "alerts") return alertsPage();
  switch (page) {
    case "overview":
      return `${pageHead("Cluster overview", "Health, capacity, and recent changes across your infrastructure.")}${clusterHealth()}${metrics()}${workerTable(
        workers()
          .filter((w) => match(w.name, w.endpoint, w.region))
          .slice(0, 5),
        true,
      )}<div class="dashboard-bottom"><div>${deploymentTable(
        deployments().filter((d) => match(d.name, d.image)),
        true,
      )}</div><div class="stack">${capacityPanel()}${recent()}</div></div>`;
    case "workers": {
      const list = workers().filter(
        (w) => match(w.name, w.endpoint, w.region) && filterMatches(w, filter),
      );
      return `${pageHead("Workers", "Manage host capacity, placement, and maintenance.", "worker")}${filters(["all", "online", "offline", "maintenance"])}${workerTable(list)}`;
    }
    case "deployments":
      return `${pageHead("Deployments", "Deploy images, scale replicas, and manage application placement.")}${filters(["all", "running", "stopped", "degraded"])}${deploymentTable(deployments().filter((d) => match(d.name, d.image) && (filter === "all" || d.status === filter)))}`;
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
      return `${pageHead("Containers", "Inspect and manage containers across all registered workers.", "none")}${filters(["all", "running", "stopped"])}<section class="panel"><div class="panel-head"><h2>Container inventory</h2>${badge("stopped", `${list.length} containers`)}</div>${inventory(list)}</section>`;
    }
    case "activity": {
      const list = state.operations.filter(
        (o) =>
          match(o.title, o.detail) && (filter === "all" || o.status === filter),
      );
      return `${pageHead("Activity log", "Track deployment changes, worker maintenance, and operation results.", "none")}${filters(["all", "succeeded", "running", "failed"])}<section class="panel"><div class="panel-head"><h2>Cluster operations</h2>${badge("stopped", `${list.length} operations`)}</div>${list.length ? `<div class="table-wrap"><table class="activity-table"><thead><tr><th scope="col">OPERATION</th><th scope="col">STATUS</th><th scope="col">DETAIL</th><th scope="col">DURATION</th><th scope="col">STARTED</th></tr></thead><tbody>${list.map((o) => `<tr><td><button class="name-link" data-action="operation-details" data-id="${esc(o.id)}">${esc(o.title)}</button><div class="small-sub mono">${esc(shortID(o.id))}</div></td><td>${badge(o.status)}</td><td class="operation-detail">${esc(o.detail)}</td><td class="table-time">${esc(operationDuration(o))}</td><td class="table-time">${timeLabel(o.startedAt)}</td></tr>`).join("")}</tbody></table></div>` : empty("No operations yet", "Your cluster activity will appear here.")}</section>`;
    }
    case "settings":
      return settings();
  }
}

function storagePage() {
  const backends = Object.values(state.storageBackends || {}).sort((a, b) => a.name.localeCompare(b.name));
  const volumes = Object.values(state.sharedVolumes || {}).sort((a, b) => a.name.localeCompare(b.name));
  const backendRows = backends.length ? `<div class="table-wrap"><table><thead><tr><th>BACKEND</th><th>NFS ENDPOINT</th><th>APPROVED WORKERS</th><th></th></tr></thead><tbody>${backends.map((b) => `<tr><td><b>${esc(b.name)}</b><div class="small-sub mono">${esc(shortID(b.id))}</div></td><td><code>${esc(b.server)}:${esc(b.export)}</code></td><td>${(b.workerIDs || []).map((id) => esc(workers().find((w) => w.id === id)?.name || id)).join(", ")}</td><td>${btn("Remove", "storage-delete-backend", `data-id="${esc(b.id)}"`, "text", "trash")}</td></tr>`).join("")}</tbody></table></div>` : empty("No NFS backends registered", "Register an existing NFSv4 export and approve the workers that may mount it.");
  const accessStatus = (v) => Object.entries(v.access || {}).map(([id, a]) => `<div class="small-sub">${esc(state.workers[id]?.name || id)}: ${esc(a.error || "Access verified")} · backend revision ${esc(a.backendRevision)} · ${a.readOnly ? "read only" : "read/write"} · ${esc(relative(a.checkedAt))}</div>`).join("") || "Not checked yet";
  const volumeRows = volumes.length ? `<div class="table-wrap"><table><thead><tr><th>VOLUME</th><th>BACKEND</th><th>DIRECTORY / IDENTITY</th><th>WORKER ACCESS</th><th>ATTACHED TO</th><th></th></tr></thead><tbody>${volumes.map((v) => { const b = state.storageBackends?.[v.backendID]; const attached = deployments().filter((d) => (d.storageAttachments || []).some((a) => a.volumeID === v.id)).map((d) => d.name); return `<tr><td><b>${esc(v.name)}</b><div class="small-sub mono">${esc(shortID(v.id))}</div></td><td>${esc(b?.name || "Missing backend")}</td><td><code>${esc(v.directory)}</code><div class="small-sub">Identity: ${esc(v.identity || "Missing")}</div></td><td>${accessStatus(v)}</td><td>${attached.length ? attached.map(esc).join(", ") : "None"}</td><td>${btn("Remove", "storage-delete-volume", `data-id="${esc(v.id)}"`, "text", "trash")}</td></tr>`; }).join("")}</tbody></table></div>` : empty("No shared volumes registered", "Volumes expose a selected subdirectory from a registered NFS export.");
  const backendOptions = backends.map((b) => `<option value="${esc(b.id)}">${esc(b.name)} · ${esc(b.server)}:${esc(b.export)}</option>`).join("");
  const workerOptions = workers().filter((w) => w.status === "online" && w.mode === "active").map((w) => `<label class="storage-worker-option"><input type="checkbox" name="workerIDs" value="${esc(w.id)}" ${state.demo ? "disabled" : ""}><span><b>${esc(w.name)}</b><small>${esc(w.region || "Unassigned region")}</small></span></label>`).join("") || `<p class="small-sub">No active online workers are available.</p>`;
  return `<div class="storage-page">
    ${pageHead("Shared storage", "Attach the same persistent application files to eligible Docker workers.", "none")}
    ${state.demo ? '<div class="notice warn">Storage registration is unavailable in demo mode.</div>' : '<div class="notice warn">Zela manages NFS volume attachments. The NFS server, availability, permissions, and backups remain your responsibility. Test mounts on every approved Linux worker before using production data.</div>'}
    <div class="storage-forms">
      <section class="panel storage-registration">
        <div class="panel-head"><h2>Register an NFSv4 backend</h2>${icon("server")}</div>
        <form id="storage-backend-form" class="settings-body storage-form">
          <div class="form-group"><label for="storage-backend-name">Backend name</label><input id="storage-backend-name" name="name" required maxlength="63" placeholder="production-nfs" ${state.demo ? "disabled" : ""}></div>
          <div class="form-group"><label for="storage-server">NFS server hostname or IP</label><input id="storage-server" name="server" required placeholder="nfs.internal" ${state.demo ? "disabled" : ""}></div>
          <div class="form-group"><label for="storage-export">Export path</label><input id="storage-export" name="export" required placeholder="/exports/apps" ${state.demo ? "disabled" : ""}></div>
          <fieldset class="storage-workers"><legend>Workers allowed to mount this export</legend><div class="storage-worker-list">${workerOptions}</div></fieldset>
          <div class="storage-form-actions"><button class="btn primary" type="submit" ${state.demo || !workers().some((w) => w.status === "online" && w.mode === "active") ? "disabled" : ""}>Register backend</button></div>
        </form>
      </section>
      <section class="panel storage-registration">
        <div class="panel-head"><h2>Register a shared volume</h2>${icon("box")}</div>
        <form id="storage-volume-form" class="settings-body storage-form">
          <div class="form-group"><label for="storage-volume-name">Volume name</label><input id="storage-volume-name" name="name" required maxlength="63" placeholder="web-data" ${state.demo ? "disabled" : ""}></div>
          <div class="form-group"><label for="storage-backend">Backend</label><select id="storage-backend" name="backendID" required ${backends.length && !state.demo ? "" : "disabled"}><option value="">${backends.length ? "Choose a backend" : "Register a backend first"}</option>${backendOptions}</select></div>
          <div class="form-group"><label for="storage-directory">Directory inside the export</label><input id="storage-directory" name="directory" required placeholder="web/uploads" ${state.demo ? "disabled" : ""}></div>
          <div class="form-group"><label for="storage-identity">Identity marker value</label><input id="storage-identity" name="identity" required maxlength="63" pattern="[a-zA-Z0-9][a-zA-Z0-9_.-]*" placeholder="website-uploads-prod" aria-describedby="storage-identity-help" ${state.demo ? "disabled" : ""}><small id="storage-identity-help">Create <code>.zela-identity</code> containing this value and a writable <code>.zela-health</code> directory inside the volume. Zela will not copy or delete its files.</small></div>
          <div class="storage-form-actions"><button class="btn primary" type="submit" ${backends.length && !state.demo ? "" : "disabled"}>Register volume</button></div>
        </form>
      </section>
    </div>
    <section class="panel"><div class="panel-head"><h2>Storage backends</h2>${badge("stopped", `${backends.length} registered`)}</div>${backendRows}</section>
    <section class="panel"><div class="panel-head"><h2>Shared volumes</h2>${badge("stopped", `${volumes.length} registered`)}</div>${volumeRows}</section>
    <section class="panel"><div class="settings-body"><h2>Attach a volume to a deployment</h2><p>Choose a registered volume in the new deployment form. Set the container path and select <code>overlap-safe</code> or <code>single-writer</code>. A single-writer move briefly stops the source first.</p><p>Traffic through Zela edge routes waits for all captured ingress worker acknowledgments and finite requests to drain. Direct service VIP clients are outside this drain path. Existing local, anonymous, and unregistered mounts remain blocked from automatic moves.</p><p>Removing a Zela volume registration does not erase remote data.</p></div></section>
  </div>`;
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
function deploymentWorkerOptions(deployment) {
  let eligibleIDs = null;
  for (const attachment of deployment.storageAttachments || []) {
    const volume = state.sharedVolumes?.[attachment.volumeID];
    const backend = volume && state.storageBackends?.[volume.backendID];
    const approved = new Set(backend?.workerIDs || []);
    eligibleIDs = eligibleIDs === null ? approved : new Set([...eligibleIDs].filter((id) => approved.has(id)));
  }
  return workers().filter((w) => w.status === "online" && w.mode === "active" && (eligibleIDs === null || eligibleIDs.has(w.id))).map((w) => `<option value="${esc(w.id)}">${esc(w.name)} · ${esc(region(w))}</option>`).join("");
}

const secrets = () => state?.secrets || [];
function vaultPage() {
  const list = secrets().filter((k) => match(k.name, k.fingerprint));
  return `${pageHead("Secrets vault", "Store SSH identities securely and use them to provision Docker hosts.", "secret")}<section class="panel"><div class="panel-head"><h2>SSH keys</h2>${badge("online", "AES-256-GCM encryption")}</div>${
    list.length
      ? `<div class="table-wrap"><table><thead><tr><th scope="col">SECRET</th><th scope="col">FINGERPRINT</th><th scope="col">USAGE</th><th scope="col">ADDED</th><th scope="col"></th></tr></thead><tbody>${list
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
  if (e.target.id === "network-worker") {
    networkWorker = e.target.value;
    render();
    return;
  }
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
  const volumes = Object.values(state.sharedVolumes || {});
  const volumeOptions = volumes.map((v) => `<option value="${esc(v.id)}">${esc(v.name)}</option>`).join("");
  openModal(
    "New deployment",
    "Deploy an application image across your Docker workers.",
    `<div class="form-grid"><div class="form-group"><label for="dep-name">Application name</label><input id="dep-name" name="name" required maxlength="63" pattern="[a-zA-Z0-9][a-zA-Z0-9_.-]*" placeholder="my-application"></div><div class="form-group"><label for="dep-image">Container image</label><input id="dep-image" name="image" required placeholder="nginx:alpine"></div><div class="form-group"><label for="replicas">Replicas</label><input id="replicas" name="replicas" type="number" min="1" max="50" value="1" required></div><div class="form-group"><label for="placement">Worker placement</label><select name="placement" id="placement">${workerOptions(worker, true)}</select></div><div class="form-group"><label for="cpus">CPU limit per replica</label><input id="cpus" name="cpus" type="number" min="0.1" max="4096" step="0.1" value="0.5" required></div><div class="form-group"><label for="memory">Memory limit (MB)</label><input id="memory" name="memoryMB" type="number" min="32" max="1048576" value="256" required></div></div><div class="form-section">Container configuration</div><div class="form-grid"><div class="form-group wide"><label for="ports">Published ports</label><input id="ports" name="ports" placeholder="8080:80, 8443:443"><small>Host:container, comma-separated. Use 0 for an automatic host port. Fixed host ports require separate workers for each replica.</small></div><div class="form-group wide"><label for="env">Environment variables</label><textarea id="env" name="env" placeholder="APP_ENV=production&#10;LOG_LEVEL=info"></textarea><small>One KEY=value per line. Values are accessible to the cluster administrator.</small></div><div class="form-group wide"><label for="command">Command override (optional JSON array)</label><input id="command" name="command" placeholder='["node", "server.js"]'><small>Leave blank to use the image’s default command.</small></div></div><div class="form-section">Optional shared storage</div><div class="form-grid"><div class="form-group"><label for="volume-id">Registered shared volume</label><select name="volumeID" id="volume-id"><option value="">No shared volume</option>${volumeOptions}</select></div><div class="form-group"><label for="volume-target">Container path</label><input id="volume-target" name="volumeTarget" placeholder="/var/lib/app/uploads"></div><div class="form-group"><label for="workload-policy">Application concurrency</label><select name="workloadPolicy" id="workload-policy"><option value="">Choose if attaching storage</option><option value="overlap-safe">Overlap safe, permits two copies</option><option value="single-writer">Single writer, brief move interruption</option></select></div><div class="form-group"><label><input type="checkbox" name="volumeReadOnly"> Mount read only</label></div></div><div class="notice note">Shared volume attachments require an administrator session and an explicit concurrency policy. The worker verifies the volume identity and the image user’s file access before startup. The image must include a Docker HEALTHCHECK. A move still starts a replacement container and does not preserve process memory or existing connections.</div>`,
    `${btn("Cancel", "close")}<button type="submit" class="btn primary" ${!workers().some((w) => w.status === "online" && w.mode === "active") ? "disabled" : ""}>${icon("layers")}Deploy application</button>`,
    "deployment-form",
  );
}
function deploymentNetworkDetails(d) {
  const network = state.network;
  const vip = network?.services?.[d.id]?.vip;
  const label = d.name.toLowerCase().replace(/[_.]/g, "-").replace(/[^a-z0-9-]/g, "").replace(/^-+|-+$/g, "");
  const dns = network?.enabled && vip && label.length <= 63 && /^[a-z0-9][a-z0-9-]*$/.test(label)
    ? `${label}.zela` : "Not allocated";
  const listeners = [network?.edgeHTTPPort ? `HTTP :${network.edgeHTTPPort}` : "", network?.edgeHTTPSPort ? `HTTPS :${network.edgeHTTPSPort}` : ""].filter(Boolean).join(" · ");
  const routes = (d.routes || []).map((route) => `<div class="deployment-detail-row"><code>${esc(route.host || "Any host")}${esc(route.path || "/")}</code><span>Container port ${esc(route.port)}</span></div>`).join("");
  const ports = (d.ports || []).map((port) => `<div class="deployment-detail-row"><code>${esc(port.host ? `${port.hostIP || "0.0.0.0"}:${port.host}` : "Automatic host port")} → :${esc(port.container)}/${esc(port.protocol || "tcp")}</code></div>`).join("");
  const replicas = (d.tasks || []).map((task) => {
    const worker = state.workers[task.workerID];
    const container = worker?.containers?.find((ct) => ct.Id === task.id);
    const addresses = Object.entries(container?.NetworkSettings?.Networks || {}).map(([name, info]) => `<div><b>${esc(name)}</b> <code>${esc(info.IPAddress || "No IPv4 address reported")}</code></div>`).join("");
    const bindings = (container?.Ports || []).filter((port) => port.PublicPort).map((port) => `<div><code>:${esc(port.PublicPort)} → :${esc(port.PrivatePort)}/${esc(port.Type || "tcp")}</code></div>`).join("");
    const member = network?.members?.[task.workerID];
    const error = member?.error || member?.edgeError;
    return `<tr><td>${esc(task.name)}<div class="small-sub">${esc(worker?.name || task.workerID)}</div></td><td>${addresses || "Not reported"}${error ? `<div class="small-sub">Worker network: ${esc(error)}</div>` : ""}</td><td>${bindings || "None reported"}</td><td>${worker && inventoryFresh(worker) ? "Current inventory" : "Cached or unavailable"}<div class="small-sub">Last seen ${esc(relative(worker?.lastSeen))}</div></td></tr>`;
  }).join("");
  return `<section class="deployment-detail-section" aria-label="Deployment network"><h3>Network</h3>
    <div class="details-meta"><div><small>CLUSTER NETWORK</small><b>${network?.enabled ? `Enabled · ${esc(network.cidr)}` : "Not enabled"}</b></div><div><small>SERVICE VIP</small><b class="mono">${esc(vip || "Not allocated")}</b></div><div><small>SERVICE DNS</small><b class="mono">${esc(dns)}</b></div></div>
    <div class="deployment-detail-grid"><div><h4>Public routes</h4><p class="small-sub">Edge listeners: ${esc(listeners || "Disabled")}</p>${routes || '<p class="small-sub">No public routes configured.</p>'}${routes && (!network?.enabled || !listeners) ? '<p class="small-sub">These routes require an enabled cluster network and edge listener to serve traffic.</p>' : ""}</div><div><h4>Configured host ports</h4>${ports || '<p class="small-sub">No host ports configured. Edge routes can reach container ports without publishing host ports.</p>'}</div></div>
    <h4>Observed replica connections</h4><div class="table-wrap"><table><thead><tr><th scope="col">REPLICA / WORKER</th><th scope="col">NETWORK / PRIVATE IP</th><th scope="col">PUBLISHED PORTS</th><th scope="col">OBSERVATION</th></tr></thead><tbody>${replicas || '<tr><td colspan="4">No replicas scheduled.</td></tr>'}</tbody></table></div>
  </section>`;
}
function deploymentStorageDetails(d) {
  const attachments = d.storageAttachments || [];
  const policy = d.workloadPolicy === "single-writer" ? "Single writer, source stops before replacement" : d.workloadPolicy === "overlap-safe" ? "Overlap safe, concurrent copies permitted" : "Not declared";
  const volumes = attachments.map((attachment) => {
    const volume = state.sharedVolumes?.[attachment.volumeID];
    const backend = volume && state.storageBackends?.[volume.backendID];
    const workers = [...new Set((d.tasks || []).map((task) => task.workerID))];
    const checks = workers.map((id) => {
      const check = volume?.access?.[id];
      const tasks = d.tasks.filter((task) => task.workerID === id);
      const matches = check && backend && check.backendRevision === backend.revision && Boolean(check.readOnly) === Boolean(attachment.readOnly) && tasks.every((task) => check.image === (task.image || d.imageDigest || d.image));
      const outcome = !check ? "Not checked" : check.error ? check.error : matches ? "Last access check passed" : "Previous check passed; configuration differs";
      return `<div class="deployment-storage-check"><b>${esc(state.workers[id]?.name || id)}</b><span>${esc(outcome)}</span>${check ? `<small>Checked ${esc(relative(check.checkedAt))} · backend revision ${esc(check.backendRevision ?? "unknown")}</small>` : ""}</div>`;
    }).join("");
    return `<article class="deployment-volume"><div class="deployment-volume-heading"><div><h4>${esc(volume?.name || attachment.volumeID)}</h4><span class="small-sub mono">${esc(attachment.volumeID)}</span></div>${badge("stopped", attachment.readOnly ? "Read only" : "Read/write")}</div>
      <dl class="deployment-volume-info"><dt>Container path</dt><dd><code>${esc(attachment.target)}</code></dd><dt>Storage backend</dt><dd>${esc(backend?.name || "Details unavailable")}</dd><dt>NFS directory</dt><dd><code>${backend && volume ? esc(`${backend.server}:${backend.export.replace(/\/$/, "")}/${volume.directory}`) : "Details unavailable"}</code></dd><dt>Volume identity</dt><dd><code>${esc(volume?.identity || "Details unavailable")}</code></dd></dl>
      <div class="deployment-storage-checks"><h4>Last worker access checks</h4>${checks || '<p class="small-sub">No replica workers assigned.</p>'}</div></article>`;
  }).join("");
  const observed = (d.tasks || []).flatMap((task) => {
    const worker = state.workers[task.workerID];
    const container = worker?.containers?.find((ct) => ct.Id === task.id);
    return (container?.Mounts || []).map((mount) => `<tr><td>${esc(task.name)}<div class="small-sub">${esc(worker?.name || task.workerID)}</div></td><td>${esc(mount.Type)}<div class="small-sub mono">${esc(mount.Name || "Unnamed mount")}</div></td><td><code>${esc(mount.Destination)}</code></td><td>${mount.RW ? "Read/write" : "Read only"}<div class="small-sub">${worker && inventoryFresh(worker) ? "Current inventory" : "Cached or unavailable"}</div></td></tr>`);
  }).join("");
  return `<section class="deployment-detail-section" aria-label="Deployment storage"><h3>Storage</h3><p class="small-sub">Workload policy: ${esc(policy)}</p>${volumes || '<p class="small-sub">No registered shared volumes attached.</p>'}<h4>Observed container mounts</h4>${observed ? `<div class="table-wrap"><table><thead><tr><th scope="col">REPLICA / WORKER</th><th scope="col">TYPE / VOLUME</th><th scope="col">CONTAINER PATH</th><th scope="col">ACCESS</th></tr></thead><tbody>${observed}</tbody></table></div>` : '<p class="small-sub">No mount details reported by current inventory.</p>'}<p class="small-sub">Observed mounts can include local or image-declared volumes. Only registered shared attachments are managed by Zela for moves. Access checks are recorded observations, not continuous storage monitoring.</p></section>`;
}
function deploymentDetails(id) {
  const d = state.deployments[id];
  if (!d) return;
  openModal(
    d.name,
    d.image,
    `<div class="details-meta"><div><small>STATUS</small>${badge(d.status)}</div><div><small>REPLICAS</small><b>${d.tasks.filter(taskRunning).length} / ${d.replicas} running</b></div><div><small>CREATED</small><b>${relative(d.createdAt)}</b></div><div><small>CPU / REPLICA</small><b>${d.cpus} vCPU</b></div><div><small>MEMORY / REPLICA</small><b>${d.memoryMB} MB</b></div><div><small>PLACEMENT</small><b>${d.workerIDs?.length ? "Selected workers" : "Balanced"}</b></div></div>${d.error ? `<div class="notice warn">${esc(d.error)}</div>` : ""}${d.handover ? `<div class="notice note"><b>Move progress: ${esc(d.handover.stage)}</b><div>Destination: ${esc(state.workers[d.handover.targetWorkerID]?.name || d.handover.targetWorkerID)}</div>${!["completed", "rolled-back"].includes(d.handover.stage) ? btn("Resume move", "resume-move", `data-id="${esc(id)}" ${busy() ? "disabled" : ""}`, "primary", "move") : ""}</div>` : ""}${deploymentNetworkDetails(d)}${deploymentStorageDetails(d)}<div class="form-section">Container replicas</div><div class="table-wrap"><table><thead><tr><th scope="col">CONTAINER</th><th scope="col">WORKER</th><th scope="col">STATE</th><th scope="col"></th></tr></thead><tbody>${d.tasks.map((t) => `<tr><td class="mono">${esc(t.name)}<div class="small-sub">${esc(shortID(t.id))}</div></td><td>${esc(state.workers[t.workerID]?.name || "Unknown")}</td><td>${badge(taskRunning(t) ? "running" : "stopped", taskRunning(t) ? "running" : "not running")}</td><td><button class="dots-button" data-action="logs" data-worker="${esc(t.workerID)}" data-id="${esc(t.id)}" aria-label="View logs">${icon("log")}</button><button class="dots-button" data-action="move-replica" data-id="${esc(id)}" data-replica="${esc(t.id)}" ${busy() || d.desiredState !== "running" ? "disabled" : ""} aria-label="Move this replica">${icon("move")}</button></td></tr>`).join("") || '<tr><td colspan="4">No containers scheduled yet.</td></tr>'}</tbody></table></div><div class="modal-actions">${btn(d.desiredState === "stopped" ? "Start" : "Stop", "deployment-action", `data-id="${esc(id)}" data-kind="${d.desiredState === "stopped" ? "start" : "stop"}" ${busy() ? "disabled" : ""}`, "", d.desiredState === "stopped" ? "play" : "pause")}${btn("Restart", "deployment-action", `data-id="${esc(id)}" data-kind="restart" ${busy() ? "disabled" : ""}`, "", "refresh")}${btn("Update image", "update-image", `data-id="${esc(id)}" ${busy() || d.desiredState !== "running" ? "disabled" : ""}`, "", "layers")}${btn("Scale", "scale", `data-id="${esc(id)}" ${busy() || d.desiredState !== "running" ? "disabled" : ""}`, "", "scale")}${btn("Move to worker", "move", `data-id="${esc(id)}" ${busy() || d.desiredState !== "running" ? "disabled" : ""}`, "green", "move")}${btn("Delete", "confirm-delete", `data-type="deployment" data-id="${esc(id)}" ${busy() ? "disabled" : ""}`, "danger", "trash")}</div>`,
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
    `<div class="flow-line"><div class="flow-box"><small>CURRENT PLACEMENT</small>${esc(source || "Unscheduled")}</div>${icon("arrow")}<div class="flow-box"><small>APPLICATION</small>${esc(d.name)} · ${replica ? 1 : d.tasks.length} replicas</div></div><input type="hidden" name="id" value="${esc(id)}"><div class="form-group"><input type="hidden" name="replica" value="${esc(replica)}"><label for="move-worker">Destination worker</label><select id="move-worker" name="target" required><option value="" disabled selected>Select an active worker</option>${deploymentWorkerOptions(d)}</select><small>Each replica reserves ${d.cpus} vCPU and ${d.memoryMB} MB. The destination must have enough capacity and available host ports.</small></div><div class="notice warn note">${d.storageAttachments?.length ? `The replacement uses the same registered shared files. ${d.workloadPolicy === "single-writer" ? "The source stops before the replacement starts, so the application has a brief interruption." : "This policy allows both copies to run during startup; confirm the application safely supports concurrent copies."} Zela edge routes wait for all captured ingress worker acknowledgments and finite in-flight requests. Direct service VIP connections are outside this drain path.` : "The replacement must pass its startup check before the source is removed. Containers with local, anonymous, or unregistered mounts cannot be moved automatically."}</div>`,
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
    `<div class="details-meta"><div><small>CONNECTIVITY</small>${badge(w.status)}</div><div><small>SCHEDULING</small>${badge(w.mode)}</div><div><small>LAST SEEN</small><b>${relative(w.lastSeen && new Date(w.lastSeen).getFullYear() > 1970 ? w.lastSeen : null)}</b></div><div><small>DOCKER</small><b>${esc(w.info.ServerVersion || "—")}</b></div><div><small>OPERATING SYSTEM</small><b>${esc(w.info.OperatingSystem || "—")}</b></div><div><small>CAPACITY</small><b>${w.info.NCPU || 0} vCPU · ${(w.info.MemTotal / 1024 / 1024 / 1024 || 0).toFixed(0)} GB</b></div></div><div class="notice mono">${esc(w.endpoint)} · ${esc(region(w))}</div>${w.error ? `<div class="error-text">${esc(w.error)}</div>` : ""}${w.ssh ? `<div class="notice note">SSH-managed worker · ${esc(w.ssh.user)}@${esc(w.ssh.host)}:${w.ssh.port} · Agent uses encrypted SSH forwarding</div>` : ""}${state.demo ? '<div class="notice note">Docker API gateway requires a live worker.</div>' : `<div class="notice"><b>Docker API via control plane</b><br><code>${esc(w.dockerAPI || `/docker/workers/${encodeURIComponent(w.id)}`)}</code><br>Authenticate with the control-plane bearer token. Stateless container requests at the root gateway use automatic placement. To target this worker explicitly, use <code>X-Zela-Worker: ${esc(w.id)}</code>. Update older worker agents before connecting.</div>`}${liveMetricSummary(w)}<div class="toolbar">${btn("Monitor worker", "monitor-worker-link", `data-worker="${esc(w.id)}"`, "", "pulse")}</div><div class="form-section">Host inventory</div>${inventory((w.containers || []).map((ct) => ({ ct, w })))}<div class="modal-actions">${w.status === "provision-failed" ? btn("Retry provisioning", "retry-provision", `data-id="${esc(id)}" ${busy() ? "disabled" : ""}`, "primary", "refresh") : ""}${btn(w.mode === "active" ? "Pause scheduling" : "Activate scheduling", "worker-action", `data-id="${esc(id)}" data-kind="${w.mode === "active" ? "pause" : "activate"}" ${busy() ? "disabled" : ""}`, "", w.mode === "active" ? "pause" : "play")}${btn("Networking", "worker-network-link", `data-worker="${esc(id)}"`, "", "globe")}${btn("Drain worker", "confirm-drain", `data-id="${esc(id)}" ${busy() || w.status !== "online" ? "disabled" : ""}`, "", "move")}${btn("Unregister", "confirm-delete", `data-type="worker" data-id="${esc(id)}" ${busy() ? "disabled" : ""}`, "danger", "trash")}</div>`,
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
      case "network-tab":
        if (["cluster", "workers", "services"].includes(el.dataset.tab))
          networkTab = el.dataset.tab;
        render();
        $("#network-tab-" + networkTab)?.focus();
        break;
      case "network-reload":
        await fetchNetworkView(true);
        break;
      case "worker-network-link":
        networkWorker = w || id;
        networkTab = "workers";
        navigate("network");
        break;
      case "network-worker-details":
        networkWorkerDialog(w || id);
        break;
      case "network-peer-edit":
        peerEndpointDialog(w || id);
        break;
      case "network-config":
        networkConfigDialog();
        break;
      case "network-reapply":
        await submit("/network/reapply", { workerID: w || "" });
        networkViewLoaded = 0;
        break;
      case "docker-networks-reload":
        await fetchDockerNetworks(true);
        break;
      case "docker-network-create":
        createDockerNetworkDialog();
        break;
      case "docker-network-inspect":
        await inspectDockerNetwork(w, id);
        break;
      case "docker-network-connect":
        networkContainerDialog(w, id);
        break;
      case "docker-network-disconnect":
        networkContainerDialog(w, id, el.dataset.container);
        break;
      case "docker-network-delete":
        deleteDockerNetworkDialog(w, id, el.dataset.name);
        break;
      case "docker-network-delete-confirm":
        await submit(
          `/workers/${encodeURIComponent(w)}/networks/${encodeURIComponent(id)}/actions`,
          { action: "delete" },
        );
        dockerNetworksLoaded = 0;
        break;
      case "network-route-add":
        routeDialog(id);
        break;
      case "network-route-edit":
        routeDialog(id, Number(el.dataset.index));
        break;
      case "network-route-remove":
        routeDialog(id, Number(el.dataset.index), true);
        break;
      case "storage-delete-backend":
        if (confirm("Remove this storage backend registration? Its NFS data will not be deleted.")) {
          await api(`/storage/backends/${encodeURIComponent(id)}`, undefined, "DELETE");
          await load();
          toast("Storage backend registration removed");
        }
        break;
      case "storage-delete-volume":
        if (confirm("Remove this shared volume registration? Its NFS data will not be deleted.")) {
          await api(`/storage/volumes/${encodeURIComponent(id)}`, undefined, "DELETE");
          await load();
          toast("Shared volume registration removed");
        }
        break;
      case "skip-content":
        e.preventDefault();
        $("#main-content")?.focus();
        break;
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
      case "worker-view":
        if (["table", "grid"].includes(el.dataset.view))
          workerView = el.dataset.view;
        render();
        break;
      case "settings-tab":
        if (
          ["general", "access", "ai", "notifications", "setup"].includes(
            el.dataset.tab,
          )
        )
          settingsTab = el.dataset.tab;
        render();
        $("#settings-tab-" + settingsTab)?.focus();
        break;
      case "clear-search":
        search = "";
        render();
        $("#global-search")?.focus();
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
      case "copy-mcp-endpoint":
        if (typeof navigator === "undefined" || !navigator.clipboard?.writeText)
          throw new Error(
            "Clipboard access is unavailable. Select and copy the endpoint shown in AI connections.",
          );
        await navigator.clipboard.writeText(mcpEndpoint());
        toast("MCP endpoint copied");
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
      case "resume-move": {
        const d = state.deployments[id];
        if (!d?.handover) throw new Error("No pending move found");
        await submit(`/deployments/${id}/replicas/${d.handover.source.id}/move`, { target: d.handover.targetWorkerID });
        break;
      }
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
      case "storage-backend-form": {
        const workerIDs = new FormData(form).getAll("workerIDs");
        await api("/storage/backends", { name: fields.name, server: fields.server, export: fields.export, workerIDs });
        resetInlineForm(form);
        await load();
        toast("NFS backend registered");
        break;
      }
      case "storage-volume-form":
        await api("/storage/volumes", { name: fields.name, backendID: fields.backendID, directory: fields.directory, identity: fields.identity });
        resetInlineForm(form);
        await load();
        toast("Shared volume registered");
        break;
      case "network-config-form":
        await submit("/network", {
          enabled: fields.enabled === "on",
          cidr: fields.cidr,
          port: Number(fields.port),
          edgeHTTPPort: Number(fields.edgeHTTPPort),
          edgeHTTPSPort: Number(fields.edgeHTTPSPort),
 edgeDrainTimeoutSeconds: Number(fields.edgeDrainTimeoutSeconds || 60),
          redirectHTTPS: fields.redirectHTTPS === "on",
        });
        networkViewLoaded = 0;
        break;
      case "network-peer-form":
        await submit(
          `/workers/${encodeURIComponent(fields.workerID)}/network-endpoint`,
          { endpoint: fields.endpoint },
        );
        networkViewLoaded = 0;
        break;
      case "docker-network-form":
        await submit(
          `/workers/${encodeURIComponent(fields.workerID)}/networks`,
          {
            name: fields.name,
            subnet: fields.subnet || "",
            gateway: fields.gateway || "",
            internal: fields.internal === "on",
          },
        );
        dockerNetworksLoaded = 0;
        break;
      case "docker-network-container-form":
        await submit(
          `/workers/${encodeURIComponent(fields.workerID)}/networks/${encodeURIComponent(fields.networkID)}/actions`,
          { action: fields.action, containerID: fields.containerID },
        );
        dockerNetworksLoaded = 0;
        break;
      case "network-route-form": {
        const expectedRoutes = JSON.parse(fields.baseRoutes);
        const routes = expectedRoutes.map((route) => ({ ...route }));
        const index = Number(fields.index);
        if (fields.remove === "true") routes.splice(index, 1);
        else {
          const route = {
            host: fields.host.trim(),
            path: fields.path.trim(),
            port: Number(fields.port),
          };
          if (index < 0) routes.push(route);
          else routes[index] = route;
        }
        await api(
          `/deployments/${encodeURIComponent(fields.deploymentID)}/routes`,
          { routes, expectedRoutes },
          "PUT",
        );
        closeModal();
        toast(
          "Routes saved. Workers apply changes on the next inventory cycle.",
        );
        networkViewLoaded = 0;
        await load();
        break;
      }
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
        const storageAttachments = fields.volumeID
          ? [{ volumeID: fields.volumeID, target: fields.volumeTarget, readOnly: fields.volumeReadOnly === "on" }]
          : [];
        if (storageAttachments.length && !fields.workloadPolicy)
          throw new Error("Choose how this application handles concurrent copies before attaching shared storage.");
        if (storageAttachments.length && !fields.volumeTarget)
          throw new Error("Set the container path for the shared volume.");
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
          storageAttachments,
          workloadPolicy: fields.workloadPolicy,
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
  if (
    e.target?.id?.startsWith("network-tab-") &&
    ["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)
  ) {
    const tabs = ["cluster", "workers", "services"],
      index = tabs.indexOf(networkTab);
    networkTab =
      e.key === "Home"
        ? tabs[0]
        : e.key === "End"
          ? tabs.at(-1)
          : tabs[
              (index + (e.key === "ArrowRight" ? 1 : -1) + tabs.length) %
                tabs.length
            ];
    e.preventDefault();
    render();
    $("#network-tab-" + networkTab)?.focus();
    return;
  }

  if (
    e.target?.getAttribute?.("role") === "tab" &&
    ["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)
  ) {
    const tabs = ["general", "access", "ai", "notifications", "setup"];
    const index = tabs.indexOf(settingsTab);
    settingsTab =
      e.key === "Home"
        ? tabs[0]
        : e.key === "End"
          ? tabs.at(-1)
          : tabs[
              (index + (e.key === "ArrowRight" ? 1 : -1) + tabs.length) %
                tabs.length
            ];
    e.preventDefault();
    render();
    $("#settings-tab-" + settingsTab)?.focus();
    return;
  }
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
    mobileOpen = false;
    search = "";
    filter = "all";
    closeModal();
    render();
  }
});
$("#app").innerHTML =
  '<div class="loading-screen">Connecting to your cluster…</div>';
load(true);
