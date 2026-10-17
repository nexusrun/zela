"use strict";
let monitorWorker = "",
  monitorWindow = "1h",
  monitorContainer = "",
  monitorPoints = [],
  historyKey = "",
  historyBusy = false,
  historyError = "",
  historyLoaded = 0,
  historySequence = 0;
const metricNumber = (v) =>
  v == null || !Number.isFinite(v) ? "—" : v.toFixed(1);
const metricBytes = (v) => {
  if (v == null || !Number.isFinite(v)) return "—";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i ? 1 : 0)} ${units[i]}`;
};
const metricRate = (v) => (v == null ? "—" : `${metricBytes(v)}/s`);
const metricsFresh = (w) =>
  w?.status === "online" &&
  !w.metricsError &&
  w.metrics?.host &&
  Date.now() - new Date(w.metrics.time) < 45000;
function liveMetricSummary(w) {
  const h = w.metrics?.host;
  if (!h)
    return `<div class="notice note">${esc(w.metricsError || "Metrics will appear after the agent collects its first sample.")}</div>`;
  return `<div class="live-metric-strip"><span>CPU <b>${metricNumber(h.cpuPercent)}%</b></span><span>RAM <b>${metricBytes(h.memoryUsedBytes)}</b></span><span>Network RX <b>${metricRate(h.network?.readBps)}</b></span><span>${metricsFresh(w) ? "Measured" : "Stale"} · ${relative(w.metrics.time)}</span></div>`;
}
async function fetchMetricHistory(force = false) {
  if (page !== "monitoring" || !monitorWorker) return;
  const key = `${monitorWorker}:${monitorWindow}`;
  if (
    (historyBusy && historyKey === key) ||
    (!force && historyKey === key && Date.now() - historyLoaded < 60000)
  )
    return;
  const seq = ++historySequence;
  if (historyKey !== key) monitorPoints = [];
  historyKey = key;
  historyBusy = true;
  historyError = "";
  try {
    const data = await api(
      `/workers/${encodeURIComponent(monitorWorker)}/metrics/history?window=${monitorWindow}`,
    );
    if (seq !== historySequence) return;
    monitorPoints = data.points || [];
    historyLoaded = Date.now();
  } catch (e) {
    if (seq === historySequence) {
      historyError = e.message;
      historyLoaded = Date.now();
    }
  } finally {
    if (seq === historySequence) {
      historyBusy = false;
      if (page === "monitoring" && state) render();
    }
  }
}
function metricChart(title, series, format, percent = false) {
  const points = monitorPoints;
  const values = series.flatMap((s) =>
    points
      .map((p) => s.value(p))
      .filter((v) => v != null && Number.isFinite(v)),
  );
  if (!values.length)
    return `<section class="panel metric-chart"><div class="panel-head"><h2>${esc(title)}</h2></div><div class="chart-empty">${historyBusy ? "Loading history…" : "No samples yet. History records once per minute."}</div></section>`;
  const max = percent ? 100 : Math.max(1, ...values) * 1.12;
  const start =
      Date.now() -
      ({ "1h": 1, "6h": 6, "24h": 24 }[monitorWindow] || 1) * 3600000,
    end = Date.now();
  const x = (t) =>
    48 +
    Math.max(0, Math.min(1, (new Date(t).getTime() - start) / (end - start))) *
      512;
  const y = (v) => 150 - Math.max(0, Math.min(1, v / max)) * 122;
  const paths = series
    .map((s, i) => {
      let path = "",
        last = 0,
        open = false;
      const dots = [];
      let index = 0;
      const markerStride = Math.max(1, Math.ceil(points.length / 240));
      for (const p of points) {
        const showMarker =
          index++ % markerStride === 0 || index === points.length;
        const v = s.value(p),
          t = new Date(p.time).getTime();
        if (v == null || !Number.isFinite(v)) {
          open = false;
          continue;
        }
        const px = x(p.time),
          py = y(v);
        path += `${!open || t - last > 120000 ? "M" : "L"}${px.toFixed(2)},${py.toFixed(2)} `;
        open = true;
        last = t;
        if (showMarker)
          dots.push(
            `<circle class="chart-point" cx="${px.toFixed(2)}" cy="${py.toFixed(2)}" r="5" tabindex="0"><title>${esc(s.label)} · ${esc(new Date(p.time).toLocaleString())} · ${esc(format(v))}</title></circle>`,
          );
      }
      return `<g class="chart-series series-${i}"><path d="${path}" fill="none" stroke-width="2"/>${dots.join("")}</g>`;
    })
    .join("");
  return `<section class="panel metric-chart"><div class="panel-head"><h2>${esc(title)}</h2><div class="chart-legend">${series.map((s, i) => `<span class="series-${i}">${esc(s.label)}</span>`).join("")}</div></div><svg viewBox="0 0 585 190" role="img" aria-label="${esc(title)} historical chart. Hover or focus a sample for its value."><path class="chart-grid" d="M48 28H560 M48 89H560 M48 150H560"/><text x="3" y="32">${esc(format(max))}</text><text x="3" y="154">${esc(format(0))}</text>${paths}<text x="48" y="181">${esc(new Date(start).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }))}</text><text x="540" y="181">Now</text></svg></section>`;
}
function monitoringPage() {
  const list = workers();
  if (!state.workers[monitorWorker]) {
    monitorWorker = list[0]?.id || "";
    historyKey = "";
    monitorContainer = "";
  }
  const w = state.workers[monitorWorker];
  if (!w)
    return `${pageHead("Monitoring", "Measured host and container usage, history, and alerts.", "none")}${empty("No workers to monitor", "Provision or connect a worker to begin collecting metrics.", "new-worker", "Connect worker")}`;
  void fetchMetricHistory();
  const s = w.metrics,
    h = s?.host,
    fresh = metricsFresh(w),
    cs = s?.containers || [];
  const choices = [
    ...new Map(
      [...cs, ...monitorPoints.flatMap((p) => p.containers || [])].map((c) => [
        c.id,
        c,
      ]),
    ).values(),
  ];
  if (!choices.some((c) => c.id === monitorContainer))
    monitorContainer = choices[0]?.id || "";
  const chosen = (p) => p.containers?.find((c) => c.id === monitorContainer);
  const card = (label, value, note) =>
    `<article class="metric-live-card"><span>${esc(label)}</span><strong>${esc(value)}</strong><small>${esc(note)}</small></article>`;
  const disks = h?.disks || [],
    fullest = disks.length
      ? Math.max(...disks.map((d) => d.usedPercent))
      : null;
  const notice = !fresh
    ? `<div class="notice warn">${esc(w.metricsError || (s ? "These readings are stale. Check worker connectivity and the agent service." : "Waiting for the updated agent’s first sample. Existing agents need an update to expose metrics."))}</div>`
    : "";
  const charts = `<div class="metric-chart-grid">${metricChart("Host CPU", [{ label: "CPU", value: (p) => p.host.cpuPercent }], (v) => `${v.toFixed(0)}%`, true)}${metricChart(
    "Host memory & disk",
    [
      { label: "RAM", value: (p) => p.host.memoryPercent },
      {
        label: "Disk",
        value: (p) =>
          p.host.disks?.length
            ? Math.max(...p.host.disks.map((d) => d.usedPercent))
            : null,
      },
    ],
    (v) => `${v.toFixed(0)}%`,
    true,
  )}${metricChart(
    "Host network",
    [
      { label: "RX", value: (p) => p.host.network?.readBps },
      { label: "TX", value: (p) => p.host.network?.writeBps },
    ],
    metricRate,
  )}${metricChart(
    "Host disk I/O",
    [
      { label: "Read", value: (p) => p.host.blockIO?.readBps },
      { label: "Write", value: (p) => p.host.blockIO?.writeBps },
    ],
    metricRate,
  )}</div>`;
  const live = cs.length
    ? `<div class="table-wrap"><table><thead><tr><th>CONTAINER</th><th>CPU</th><th>MEMORY WORKING SET</th><th>NETWORK RX / TX</th><th>DISK READ / WRITE</th></tr></thead><tbody>${cs
        .filter((c) => match(c.name, c.id))
        .map(
          (c) =>
            `<tr><td><button class="name-link" data-action="monitor-container" data-id="${esc(c.id)}">${esc(c.name)}</button><div class="small-sub mono">${esc(shortID(c.id))}</div></td><td>${metricNumber(c.cpuPercent)}%</td><td>${metricBytes(c.memoryUsedBytes)}<div class="small-sub">${metricNumber(c.memoryPercent)}% of ${metricBytes(c.memoryLimitBytes)}</div></td><td>${metricRate(c.network?.readBps)} / ${metricRate(c.network?.writeBps)}</td><td>${metricRate(c.blockIO?.readBps)} / ${metricRate(c.blockIO?.writeBps)}</td></tr>`,
        )
        .join("")}</tbody></table></div>`
    : empty(
        "No running container samples",
        "Stopped containers do not produce usage samples.",
      );
  return `${pageHead("Monitoring", "Measured usage across your Docker infrastructure.", "none")}<div class="monitor-toolbar"><label>Worker <select id="monitor-worker">${list.map((v) => `<option value="${esc(v.id)}" ${v.id === monitorWorker ? "selected" : ""}>${esc(v.name)}</option>`).join("")}</select></label><div class="filter-bar">${["1h", "6h", "24h"].map((v) => btn(v, "monitor-window", `data-window="${v}"`, monitorWindow === v ? "primary" : "")).join("")}</div>${btn("Reload history", "monitor-history", "", "", "refresh")}${w.ssh ? btn("Update agent", "agent-update-dialog", `data-id="${esc(w.id)}" ${busy() ? "disabled" : ""}`, "", "refresh") : ""}</div>${notice}<div class="metric-live-grid">${card("Host CPU", h ? `${metricNumber(h.cpuPercent)}%` : "—", "Across all CPU cores")}${card("Host memory", h ? metricBytes(h.memoryUsedBytes) : "—", h ? `${metricNumber(h.memoryPercent)}% of ${metricBytes(h.memoryTotalBytes)}` : "Waiting for sample")}${card("Disk used", fullest == null ? "—" : `${metricNumber(fullest)}%`, disks.map((d) => d.path).join(" · ") || "No disk sample")}${card("Network RX / TX", h ? `${metricRate(h.network?.readBps)} / ${metricRate(h.network?.writeBps)}` : "—", fresh ? `Measured · ${relative(s.time)}` : "Last recorded readings")}</div>${s?.errors?.length ? `<div class="notice warn">Partial collection: ${s.errors.map(esc).join(" · ")}${s.truncated ? ` · ${s.truncated} containers exceed the collection limit` : ""}</div>` : ""}${historyError ? `<div class="notice warn">${esc(historyError)}</div>` : ""}${charts}<section class="panel"><div class="panel-head"><h2>Container usage</h2><span class="small-sub">100% CPU = one core · cache excluded from memory</span></div>${live}</section>${
    monitorContainer
      ? `<div class="monitor-toolbar"><label>Container history <select id="monitor-container">${choices.map((c) => `<option value="${esc(c.id)}" ${c.id === monitorContainer ? "selected" : ""}>${esc(c.name)}</option>`).join("")}</select></label><span class="note">Container history retains one hour.</span></div><div class="metric-chart-grid">${metricChart("Container CPU", [{ label: "CPU", value: (p) => chosen(p)?.cpuPercent }], (v) => `${v.toFixed(0)}%`)}${metricChart("Container memory", [{ label: "Working set", value: (p) => chosen(p)?.memoryUsedBytes }], metricBytes)}${metricChart(
          "Container network",
          [
            { label: "RX", value: (p) => chosen(p)?.network?.readBps },
            { label: "TX", value: (p) => chosen(p)?.network?.writeBps },
          ],
          metricRate,
        )}${metricChart(
          "Container disk I/O",
          [
            { label: "Read", value: (p) => chosen(p)?.blockIO?.readBps },
            { label: "Write", value: (p) => chosen(p)?.blockIO?.writeBps },
          ],
          metricRate,
        )}</div>`
      : ""
  }<p class="note">Collection runs every 10 seconds. History stores minute samples: hosts for 24 hours, containers for one hour. Chart gaps indicate missing data; “—” indicates unavailable or warming metrics. Reservation charts on Workers remain separate.</p>`;
}
function alertsPage() {
  const all = state.alerts || [],
    list = all.filter(
      (a) =>
        match(a.message, a.kind) &&
        (filter === "all" ||
          (filter === "active" && a.active) ||
          (filter === "resolved" && !a.active)),
    );
  return `${pageHead("Alerts", "Conditions detected automatically across your cluster.", "none")}<div class="monitor-toolbar">${filters(["all", "active", "resolved"])}${btn("Configure thresholds", "alert-rules", "", "", "settings")}</div><section class="panel"><div class="panel-head"><h2>Cluster alerts</h2>${badge(all.some((a) => a.active && !a.acknowledged) ? "failed" : "online", `${all.filter((a) => a.active).length} active`)}</div>${list.length ? `<div class="table-wrap"><table><thead><tr><th>CONDITION</th><th>SEVERITY</th><th>STATUS</th><th>OPENED</th><th></th></tr></thead><tbody>${list.map((a) => `<tr><td>${esc(a.message)}<div class="small-sub">${esc(a.kind)}</div></td><td>${badge(a.severity === "critical" ? "failed" : "paused", a.severity)}</td><td>${badge(a.active ? "failed" : "online", a.active ? "Active" : "Resolved")}${a.acknowledged ? '<div class="small-sub">Acknowledged</div>' : ""}</td><td>${relative(a.openedAt)}${a.resolvedAt ? `<div class="small-sub">Resolved ${relative(a.resolvedAt)}</div>` : ""}</td><td>${a.workerID ? btn("Monitor", "monitor-worker-link", `data-worker="${esc(a.workerID)}"`, "text", "pulse") : ""}${a.active && !a.acknowledged ? btn("Acknowledge", "acknowledge-alert", `data-id="${esc(a.id)}"`, "text", "check") : ""}</td></tr>`).join("")}</tbody></table></div>` : empty("No alerts", "Threshold breaches and host or container failures will appear here.")}</section><p class="note">Acknowledging an alert does not clear its condition. Alerts resolve after observed recovery, and return as new alerts if the condition recurs. Resolved alerts are retained for up to seven days (latest 500). Alert openings and recoveries appear in the notification bell. Enable desktop notifications in the bell or Settings to receive browser notifications while this console is open.</p>`;
}
function alertRulesDialog() {
  const r = state.monitoringRules || {
    cpuPercent: 90,
    memoryPercent: 90,
    diskPercent: 85,
    resourceForSeconds: 60,
    offlineForSeconds: 30,
    healthForSeconds: 30,
  };
  const field = (key, label, max) =>
    `<div class="form-group"><label for="rule-${key}">${esc(label)}</label><input id="rule-${key}" name="${key}" type="number" min="${key.endsWith("Percent") ? 1 : 0}" max="${max}" value="${r[key]}" required></div>`;
  openModal(
    "Alert thresholds",
    "Apply cluster-wide thresholds and sustained-condition durations.",
    `<div class="form-grid">${field("cpuPercent", "Host CPU threshold (%)", 100)}${field("memoryPercent", "Host memory threshold (%)", 100)}${field("diskPercent", "Disk usage threshold (%)", 100)}${field("resourceForSeconds", "Resource breach duration (seconds)", 3600)}${field("offlineForSeconds", "Worker offline duration (seconds)", 3600)}${field("healthForSeconds", "Container / deployment failure duration (seconds)", 3600)}</div><div class="notice note">Zero seconds triggers on the next poll. Missing or partial metrics trigger after 60 seconds. Sustained conditions are evaluated on each polling cycle.</div>`,
    `${btn("Cancel", "close")}<button type="submit" class="btn primary">Save thresholds</button>`,
    "alert-rules-form",
  );
}
function agentUpdateDialog(id) {
  const w = state.workers[id];
  if (!w) return;
  openModal(
    "Update worker agent",
    w.name,
    `<div class="notice warn">Upload the control plane’s current worker binary over SSH and restart the agent service. Management connectivity may briefly pause. Existing healthy Docker installations and running containers are preserved.</div>`,
    `${btn("Cancel", "close")}${btn("Update agent", "execute-agent-update", `data-id="${esc(id)}"`, "primary", "refresh")}`,
  );
}
