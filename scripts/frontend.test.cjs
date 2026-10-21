// Test the framework-free console with an in-memory DOM and API, without opening ports.
const { test } = require("node:test");
const assert = require("node:assert/strict");
const vm = require("node:vm");
const fs = require("node:fs");
const path = require("node:path");
const monitoringSource = fs.readFileSync(
  path.join(__dirname, "../internal/control/web/monitoring.js"),
  "utf8",
);
const source =
  fs.readFileSync(
    path.join(__dirname, "../internal/control/web/notifications.js"),
    "utf8",
  ) +
  "\n" +
  monitoringSource +
  "\n" +
  fs.readFileSync(
    path.join(__dirname, "../internal/control/web/networking.js"),
    "utf8",
  ) +
  "\n" +
  fs.readFileSync(
    path.join(__dirname, "../internal/control/web/app.js"),
    "utf8",
  );
function cluster() {
  return {
    demo: true,
    workers: {
      w1: {
        id: "w1",
        name: "worker-one",
        endpoint: "demo://one",
        region: "us-west",
        mode: "active",
        status: "online",
        lastSeen: new Date().toISOString(),
        info: {
          NCPU: 8,
          MemTotal: 16 * 1024 ** 3,
          Architecture: "x86_64",
          ServerVersion: "28.0.4",
          OperatingSystem: "Ubuntu",
        },
        containers: [
          {
            Id: "container-one",
            Names: ["/web-one"],
            Image: "nginx:alpine",
            State: "running",
            Status: "Up",
            Labels: { "zela.managed": "true" },
          },
        ],
      },
    },
    deployments: {
      d1: {
        id: "d1",
        name: "web",
        image: "nginx:alpine",
        replicas: 1,
        workerIDs: [],
        tasks: [{ id: "container-one", workerID: "w1", name: "web-one" }],
        memoryMB: 256,
        cpus: 0.5,
        desiredState: "running",
        status: "running",
        createdAt: new Date().toISOString(),
        ports: [],
      },
    },
    operations: [
      {
        id: "op-one",
        title: "Demo initialized",
        status: "succeeded",
        detail: "Ready",
        startedAt: new Date().toISOString(),
      },
    ],
  };
}
async function harness(data = cluster(), respond, browser = {}) {
  const handlers = {},
    nodes = {},
    requests = [];
  function node() {
    return {
      innerHTML: "",
      textContent: "",
      className: "",
      children: [],
      disabled: false,
      isConnected: true,
      focus() {},
      querySelector() {
        return null;
      },
    };
  }
  for (const id of [
    "#app",
    "#modal-root",
    "#toast",
    "#modal-error",
    "#global-search",
    "#notification-center-content",
  ])
    nodes[id] = node();
  const document = {
    activeElement: null,
    querySelector: (s) => nodes[s] || null,
    addEventListener: (name, f) => (handlers[name] = f),
  };
  const sandbox = {
    document,
    location: { hash: "" },
    window: { addEventListener() {}, focus() {}, ...browser.window },
    localStorage: browser.storage || {
      getItem() {
        return null;
      },
      setItem() {},
      removeItem() {},
    },
    setInterval: () => 1,
    clearInterval() {},
    setTimeout: () => 1,
    clearTimeout() {},
    console,
    FormData: class {
      constructor(form) {
        this.data = form.fields;
      }
      [Symbol.iterator]() {
        return Object.entries(this.data)[Symbol.iterator]();
      }
      getAll(name) {
        const value = this.data[name];
        return value === undefined ? [] : Array.isArray(value) ? value : [value];
      }
    },
    fetch: async (url, init) => {
      requests.push({
        url,
        ...init,
        body: init.body ? JSON.parse(init.body) : undefined,
      });
      return {
        ok: true,
        status: 200,
        json: async () =>
          respond && url !== "/api/state"
            ? respond(url, init)
            : url === "/api/state"
              ? data
              : { id: "operation", title: "Operation", status: "running" },
      };
    },
  };
  vm.createContext(sandbox);
  vm.runInContext(source, sandbox);
  await new Promise((resolve) => setImmediate(resolve));
  return {
    sandbox,
    nodes,
    requests,
    handlers,
    run: (code) => vm.runInContext(code, sandbox),
    click: (dataset) =>
      handlers.click({
        target: { closest: () => ({ dataset, disabled: false }) },
      }),
    form: (id, fields) =>
      handlers.submit({
        preventDefault() {},
        target: { id, fields, querySelector: () => null },
      }),
  };
}
test("overview renders live inventory and resource reservations", async () => {
  const h = await harness();
  assert.match(h.nodes["#app"].innerHTML, /Cluster overview/);
  assert.match(h.nodes["#app"].innerHTML, /worker-one/);
  assert.match(h.nodes["#app"].innerHTML, /0.5 <span>\/ 8 cores/);
  assert.match(h.nodes["#app"].innerHTML, /Demo environment/);
  assert.doesNotMatch(h.nodes["#app"].innerHTML, /NaN|undefined/);
});
test("every console page renders populated and empty clusters", async () => {
  for (const data of [
    cluster(),
    { demo: false, workers: {}, deployments: {}, operations: [] },
  ]) {
    const h = await harness(data);
    for (const p of [
      "overview",
      "workers",
      "deployments",
      "containers",
      "network",
      "activity",
      "monitoring",
      "alerts",
      "settings",
    ]) {
      h.run(`page='${p}';render()`);
      const html = h.nodes["#app"].innerHTML;
      assert.ok(html.length > 1000);
      assert.doesNotMatch(html, /NaN|undefined/);
    }
  }
});
test("worker and image text is escaped before HTML rendering", async () => {
  const data = cluster();
  data.workers.w1.name = "<img src=x onerror=alert(1)>";
  data.deployments.d1.image = "bad<script>alert(1)</script>";
  const h = await harness(data);
  assert.doesNotMatch(h.nodes["#app"].innerHTML, /<img src=x|<script>alert/);
  assert.match(h.nodes["#app"].innerHTML, /&lt;img/);
});
test("worker registration sends only form configuration", async () => {
  const h = await harness();
  await h.form("worker-form", {
    name: "worker-new",
    endpoint: "https://10.0.0.2:9091",
    token: "secret",
    region: "west",
  });
  const req = h.requests.find((r) => r.url === "/api/workers");
  assert.equal(req.method, "POST");
  assert.equal(req.body.name, "worker-new");
  assert.equal(req.body.region, "west");
});
test("deployment form parses command, env, ports, and resource limits", async () => {
  const h = await harness();
  await h.form("deployment-form", {
    name: "api",
    image: "nginx:alpine",
    replicas: "2",
    cpus: "0.5",
    memoryMB: "256",
    placement: "w1",
    ports: "8080:80, 5353:53/udp",
    env: "APP_ENV=production\nLOG_LEVEL=info",
    command: '["nginx","-g","daemon off;"]',
  });
  const body = h.requests.find((r) => r.url === "/api/deployments").body;
  assert.equal(body.replicas, 2);
  assert.deepEqual(body.workerIDs, ["w1"]);
  assert.deepEqual(body.ports, [
    { host: 8080, container: 80, protocol: "tcp" },
    { host: 5353, container: 53, protocol: "udp" },
  ]);
  assert.deepEqual(body.env, ["APP_ENV=production", "LOG_LEVEL=info"]);
  assert.deepEqual(body.command, ["nginx", "-g", "daemon off;"]);
});
test("invalid command prevents deployment submission and displays error", async () => {
  const h = await harness();
  await h.form("deployment-form", {
    name: "api",
    image: "nginx",
    replicas: "1",
    cpus: "0.5",
    memoryMB: "256",
    placement: "",
    ports: "",
    env: "",
    command: '"not-an-array"',
  });
  assert.ok(!h.requests.some((r) => r.url === "/api/deployments"));
  assert.match(h.nodes["#modal-error"].textContent, /JSON array/);
});
test("individual replica moves and image rollouts use their API routes", async () => {
  const h = await harness();
  await h.form("move-form", {
    id: "d1",
    replica: "container-one",
    target: "w2",
  });
  assert.ok(
    h.requests.some(
      (r) =>
        r.url === "/api/deployments/d1/replicas/container-one/move" &&
        r.body.target === "w2",
    ),
  );
  await h.form("image-form", { id: "d1", image: "nginx:stable", target: "w2" });
  assert.ok(
    h.requests.some(
      (r) =>
        r.url === "/api/deployments/d1/image" &&
        r.body.image === "nginx:stable",
    ),
  );
});
test("deployment controls open accessible dialogs with working replica actions", async () => {
  const h = await harness();
  await h.click({ action: "deployment-details", id: "d1" });
  assert.match(h.nodes["#modal-root"].innerHTML, /role="dialog"/);
  assert.match(h.nodes["#modal-root"].innerHTML, /data-action="move-replica"/);
  assert.match(h.nodes["#modal-root"].innerHTML, /data-action="update-image"/);
  await h.click({ action: "move-replica", id: "d1", replica: "container-one" });
  assert.match(
    h.nodes["#modal-root"].innerHTML,
    /name="replica" value="container-one"/,
  );
});
test("vault page renders key metadata and guards keys referenced by workers", async () => {
  const data = cluster();
  data.secrets = [
    {
      id: "k1",
      name: "production-key",
      fingerprint: "SHA256:public-fingerprint",
      createdAt: new Date().toISOString(),
    },
  ];
  data.workers.w1.ssh = { secretID: "k1" };
  const h = await harness(data);
  h.run("page='secrets';render()");
  assert.match(h.nodes["#app"].innerHTML, /production-key/);
  assert.match(h.nodes["#app"].innerHTML, /AES-256-GCM/);
  assert.match(
    h.nodes["#app"].innerHTML,
    /data-action="delete-secret"[^>]*disabled/,
  );
  assert.doesNotMatch(h.nodes["#app"].innerHTML, /BEGIN OPENSSH PRIVATE KEY/);
});
test("secret import posts the key to the vault endpoint and clears the dialog", async () => {
  const h = await harness();
  await h.form("secret-form", {
    name: "workers",
    privateKey: "private-key-placeholder",
    passphrase: "test-passphrase",
  });
  const r = h.requests.find((r) => r.url === "/api/secrets");
  assert.equal(r.method, "POST");
  assert.equal(r.body.privateKey, "private-key-placeholder");
  assert.equal(h.nodes["#modal-root"].innerHTML, "");
});
test("provision form references the vault key and supplies host identity without private material", async () => {
  const h = await harness();
  await h.form("provision-form", {
    name: "worker-new",
    region: "west",
    host: "10.0.0.4",
    port: "2222",
    user: "ubuntu",
    secretID: "key-one",
    hostKey: "ssh-ed25519 host-public-key",
    sudo: "on",
    agentPort: "9091",
  });
  const r = h.requests.find((r) => r.url === "/api/workers/provision");
  assert.deepEqual(r.body, {
    name: "worker-new",
    region: "west",
    ssh: {
      host: "10.0.0.4",
      port: 2222,
      user: "ubuntu",
      secretID: "key-one",
      hostKey: "ssh-ed25519 host-public-key",
      sudo: true,
      agentPort: 9091,
    },
  });
  assert.ok(!("privateKey" in r.body));
});
test("provisioning requires a saved key and then displays a trusted-host form", async () => {
  const h = await harness();
  await h.click({ action: "provision-worker" });
  assert.match(h.nodes["#modal-root"].innerHTML, /Add an SSH key first/);
  h.run("state.secrets=[{id:'key',name:'workers'}]");
  await h.click({ action: "provision-worker" });
  assert.match(h.nodes["#modal-root"].innerHTML, /name="hostKey"/);
  assert.match(h.nodes["#modal-root"].innerHTML, /name="secretID"/);
  assert.match(h.nodes["#modal-root"].innerHTML, /RHEL\/Rocky\/AlmaLinux 8–10/);
  assert.match(
    h.nodes["#modal-root"].innerHTML,
    /operating system is detected automatically/,
  );
  assert.match(
    h.nodes["#modal-root"].innerHTML,
    /SELinux and firewall settings are preserved/,
  );
  assert.match(
    h.nodes["#modal-root"].innerHTML,
    /Demo: all remote installation steps will be simulated/,
  );
});
test("operation dialog shows provisioning stages and retry uses the saved worker record", async () => {
  const data = cluster();
  data.operations[0].steps = [
    { name: "Install Docker", status: "failed", detail: "Install failed" },
  ];
  const h = await harness(data);
  await h.click({ action: "operation-details", id: "op-one" });
  assert.match(h.nodes["#modal-root"].innerHTML, /Install Docker/);
  await h.click({ action: "retry-provision", id: "w1" });
  assert.ok(
    h.requests.some((r) => r.url === "/api/workers/w1/provision-retry"),
  );
});

function measuredSample(time = new Date().toISOString()) {
  return {
    time,
    host: {
      cpuPercent: 42,
      memoryUsedBytes: 4 * 1024 ** 3,
      memoryTotalBytes: 8 * 1024 ** 3,
      memoryPercent: 50,
      disks: [{ path: "/", usedPercent: 61 }],
      network: { readBps: 2048, writeBps: 1024 },
      blockIO: { readBps: 4096, writeBps: 2048 },
    },
    containers: [
      {
        id: "container-one",
        name: "web-one",
        cpuPercent: 125,
        memoryUsedBytes: 96 * 1024 ** 2,
        memoryLimitBytes: 256 * 1024 ** 2,
        memoryPercent: 37.5,
        network: { readBps: 100, writeBps: 200 },
        blockIO: { readBps: 300, writeBps: 400 },
      },
    ],
  };
}
test("monitoring renders measured usage distinctly from reservations and handles stale readings", async () => {
  const data = cluster();
  data.workers.w1.metrics = measuredSample();
  const h = await harness(data);
  h.run("page='monitoring';render()");
  const html = h.nodes["#app"].innerHTML;
  assert.match(html, /42.0%/);
  assert.match(html, /4.0 GiB/);
  assert.match(html, /125.0%/);
  assert.match(html, /100% CPU = one core/);
  assert.doesNotMatch(html, /NaN|undefined/);
  data.workers.w1.metrics.time = new Date(Date.now() - 120000).toISOString();
  h.run("render()");
  assert.match(h.nodes["#app"].innerHTML, /readings are stale/);
});
test("history windows request the selected worker and charts preserve missing-data gaps", async () => {
  const data = cluster();
  data.workers.w1.metrics = measuredSample();
  const h = await harness(data);
  h.run("page='monitoring';render()");
  await h.click({ action: "monitor-window", window: "6h" });
  assert.ok(
    h.requests.some(
      (r) => r.url === "/api/workers/w1/metrics/history?window=6h",
    ),
  );
  const first = measuredSample(new Date(Date.now() - 600000).toISOString()),
    second = measuredSample(new Date(Date.now() - 60000).toISOString());
  h.run(`monitorPoints=${JSON.stringify([first, second])}`);
  const chart = h.run(
    "metricChart('CPU',[{label:'CPU',value:p=>p.host.cpuPercent}],v=>v.toFixed(0)+'%',true)",
  );
  assert.match(chart, /<svg/);
  assert.match(chart, /d="M[^\"]* M/);
  assert.match(chart, /<title>CPU/);
});
test("alerts escape reported messages and acknowledge through the authenticated API", async () => {
  const data = cluster();
  data.alerts = [
    {
      id: "a1",
      kind: "cpu-high",
      severity: "warning",
      message: "<script>bad</script>",
      workerID: "w1",
      active: true,
      acknowledged: false,
      openedAt: new Date().toISOString(),
    },
  ];
  const h = await harness(data);
  h.run("page='alerts';render()");
  assert.match(h.nodes["#app"].innerHTML, /&lt;script&gt;/);
  assert.doesNotMatch(h.nodes["#app"].innerHTML, /<script>bad/);
  await h.click({ action: "acknowledge-alert", id: "a1" });
  assert.ok(
    h.requests.some(
      (r) => r.url === "/api/alerts/a1/acknowledge" && r.method === "POST",
    ),
  );
});
test("threshold forms save validated numeric values and agent updates use the worker route", async () => {
  const data = cluster();
  data.workers.w1.ssh = { host: "10.0.0.3", secretID: "k" };
  const h = await harness(data);
  await h.click({ action: "alert-rules" });
  assert.match(h.nodes["#modal-root"].innerHTML, /Host CPU threshold/);
  await h.form("alert-rules-form", {
    cpuPercent: "85",
    memoryPercent: "90",
    diskPercent: "80",
    resourceForSeconds: "60",
    offlineForSeconds: "30",
    healthForSeconds: "30",
  });
  const request = h.requests.find((r) => r.url === "/api/monitoring/rules");
  assert.equal(request.body.cpuPercent, 85);
  assert.equal(request.body.resourceForSeconds, 60);
  await h.click({ action: "agent-update-dialog", id: "w1" });
  assert.match(h.nodes["#modal-root"].innerHTML, /restart the agent service/);
  await h.click({ action: "execute-agent-update", id: "w1" });
  assert.ok(h.requests.some((r) => r.url === "/api/workers/w1/agent-update"));
});

test("infrastructure token UI creates, reveals once, and revokes without caching its value", async () => {
  const data = cluster();
  data.tokens = [
    {
      id: "tok_example",
      name: "Zela <production>",
      scope: "infrastructure",
      prefix: "zela_infra_example",
      createdAt: new Date().toISOString(),
      expiresAt: new Date(Date.now() + 86400000).toISOString(),
    },
  ];
  const raw = "zela_infra_example_one-time-secret";
  const h = await harness(data, (url) =>
    url === "/api/tokens"
      ? { token: raw, metadata: data.tokens[0] }
      : { ok: true },
  );
  await h.click({ action: "nav", page: "settings" });
  await h.click({ action: "settings-tab", tab: "access" });
  assert.match(h.nodes["#app"].innerHTML, /Integration API tokens/);
  assert.match(h.nodes["#app"].innerHTML, /Zela &lt;production&gt;/);
  assert.doesNotMatch(h.nodes["#app"].innerHTML, /one-time-secret/);
  await h.click({ action: "new-token" });
  assert.match(h.nodes["#modal-root"].innerHTML, /id="token-form"/);
  assert.match(h.nodes["#modal-root"].innerHTML, /value="90" selected/);
  assert.match(
    h.nodes["#modal-root"].innerHTML,
    /value="infrastructure" selected/,
  );
  await h.form("token-form", {
    name: "Zela <production>",
    expiresInDays: "90",
  });
  assert.deepEqual(h.requests.find((r) => r.url === "/api/tokens").body, {
    name: "Zela <production>",
    scope: "infrastructure",
    expiresInDays: 90,
  });
  assert.match(h.nodes["#modal-root"].innerHTML, /one-time-secret/);
  assert.doesNotMatch(h.nodes["#app"].innerHTML, /one-time-secret/);
  assert.equal(
    h.run("JSON.stringify(state).includes('one-time-secret')"),
    false,
  );
  await h.click({ action: "close" });
  assert.equal(h.nodes["#modal-root"].innerHTML, "");
  await h.click({ action: "revoke-token", id: "tok_example" });
  assert.match(h.nodes["#modal-root"].innerHTML, /Revoke integration token/);
  assert.doesNotMatch(h.nodes["#modal-root"].innerHTML, /one-time-secret/);
  await h.click({ action: "execute-token-revoke", id: "tok_example" });
  assert.ok(
    h.requests.some(
      (r) => r.url === "/api/tokens/tok_example/revoke" && r.method === "POST",
    ),
  );
  assert.equal(h.nodes["#modal-root"].innerHTML, "");
});
test("token UI can explicitly choose the legacy deployment scope", async () => {
  const data = cluster();
  const h = await harness(data, () => ({
    token: "zela_deploy_example_once",
    metadata: { name: "Legacy integration", scope: "deployments" },
  }));
  await h.click({ action: "new-token" });
  await h.form("token-form", {
    name: "Legacy integration",
    scope: "deployments",
    expiresInDays: "30",
  });
  assert.deepEqual(h.requests.find((r) => r.url === "/api/tokens").body, {
    name: "Legacy integration",
    scope: "deployments",
    expiresInDays: 30,
  });
});

test("deployment token UI shows expired and revoked credentials without active revoke controls", async () => {
  const data = cluster();
  data.tokens = [
    {
      id: "expired",
      name: "Expired integration",
      prefix: "expired-prefix",
      createdAt: new Date().toISOString(),
      expiresAt: "2000-01-01T00:00:00Z",
    },
    {
      id: "revoked",
      name: "Old integration",
      prefix: "revoked-prefix",
      createdAt: new Date().toISOString(),
      revokedAt: new Date().toISOString(),
    },
  ];
  const h = await harness(data);
  await h.click({ action: "nav", page: "settings" });
  await h.click({ action: "settings-tab", tab: "access" });
  assert.match(h.nodes["#app"].innerHTML, /Expired integration/);
  assert.match(h.nodes["#app"].innerHTML, /Revoked/);
  assert.doesNotMatch(h.nodes["#app"].innerHTML, /data-action="revoke-token"/);
});

function notification(id, extra = {}) {
  return {
    id,
    eventKey: id,
    kind: "alert-opened",
    title: "Worker offline",
    message: "worker-one is unreachable",
    severity: "critical",
    alertID: "alert-one",
    createdAt: new Date().toISOString(),
    ...extra,
  };
}
function desktopBrowser(permission = "default", preference = null) {
  const popups = [],
    requests = [];
  const stored = new Map(
    preference ? [["zela.desktopNotifications", preference]] : [],
  );
  class DesktopNotification {
    static permission = permission;
    static async requestPermission() {
      requests.push(true);
      this.permission = "granted";
      return "granted";
    }
    constructor(title, options) {
      this.title = title;
      this.options = options;
      popups.push(this);
    }
    close() {
      this.closed = true;
      this.onclose?.();
    }
  }
  return {
    window: { Notification: DesktopNotification, isSecureContext: true },
    storage: {
      getItem: (key) => stored.get(key) || null,
      setItem: (key, value) => stored.set(key, value),
      removeItem: (key) => stored.delete(key),
    },
    popups,
    requests,
  };
}
test("notification bell shows real events, escapes content, and read status stays separate from alerts", async () => {
  const data = cluster();
  data.notifications = [
    notification("new", { message: "<script>bad</script> worker offline" }),
  ];
  data.alerts = [{ id: "alert-one", active: true, acknowledged: false }];
  const h = await harness(data, (url, init) => {
    if (url === "/api/notifications/read") {
      const ids = JSON.parse(init.body).ids;
      data.notifications = data.notifications.map((n) =>
        ids.includes(n.id) ? { ...n, readAt: new Date().toISOString() } : n,
      );
      return {
        notifications: data.notifications,
        unread: data.notifications.filter((n) => !n.readAt).length,
      };
    }
    return {};
  });
  assert.match(h.nodes["#app"].innerHTML, /Notifications, 1 unread/);
  await h.click({ action: "notifications" });
  assert.match(h.nodes["#modal-root"].innerHTML, /Worker offline/);
  assert.match(
    h.nodes["#modal-root"].innerHTML,
    /&lt;script&gt;bad&lt;\/script&gt;/,
  );
  assert.doesNotMatch(h.nodes["#modal-root"].innerHTML, /<script>bad/);
  await h.click({ action: "read-notification", id: "new" });
  assert.deepEqual(
    h.requests.find((r) => r.url === "/api/notifications/read").body,
    { ids: ["new"] },
  );
  assert.match(h.nodes["#app"].innerHTML, /Notifications, none unread/);
  assert.equal(data.alerts[0].acknowledged, false);
  assert.ok(!h.requests.some((r) => r.url.includes("acknowledge")));
});
test("notifications use explicit browser consent and deliver fresh events once across polls", async () => {
  const data = cluster();
  data.notifications = [notification("old")];
  const browser = desktopBrowser();
  const h = await harness(data, undefined, browser);
  assert.equal(browser.requests.length, 0);
  assert.equal(browser.popups.length, 0);
  await h.click({ action: "notifications" });
  await h.click({ action: "enable-desktop-notifications" });
  assert.equal(browser.requests.length, 1);
  assert.equal(browser.storage.getItem("zela.desktopNotifications"), "enabled");
  assert.equal(browser.popups.length, 1);
  data.notifications.unshift(notification("new"));
  await h.run("load()");
  assert.equal(browser.popups.length, 2);
  assert.equal(browser.popups[1].options.tag, "new");
  await h.run("load()");
  assert.equal(browser.popups.length, 2);
  assert.match(h.nodes["#toast"].textContent, /Worker offline/);
  await h.click({ action: "disable-desktop-notifications" });
  assert.equal(browser.storage.getItem("zela.desktopNotifications"), null);
  assert.ok(browser.popups.every((n) => n.closed));
  data.notifications.unshift(notification("later"));
  await h.run("load()");
  assert.equal(browser.popups.length, 2);
});
test("notification inbox remains usable when desktop permission is denied or the origin is insecure", async () => {
  for (const window of [
    {
      Notification: desktopBrowser("denied").window.Notification,
      isSecureContext: true,
    },
    {
      Notification: desktopBrowser().window.Notification,
      isSecureContext: false,
    },
    {},
  ]) {
    const data = cluster();
    data.notifications = [notification("one")];
    const h = await harness(data, undefined, { window });
    await h.click({ action: "notifications" });
    assert.match(
      h.nodes["#modal-root"].innerHTML,
      /data-action="enable-desktop-notifications" disabled/,
    );
    assert.match(h.nodes["#modal-root"].innerHTML, /Worker offline/);
  }
});
test("notification bursts are bounded, existing history does not replay, and other dialogs survive polling", async () => {
  const data = cluster();
  data.notifications = [notification("existing")];
  const browser = desktopBrowser("granted", "enabled");
  const h = await harness(data, undefined, browser);
  assert.equal(browser.popups.length, 0);
  await h.click({ action: "new-token" });
  const modal = h.nodes["#modal-root"].innerHTML;
  for (let i = 0; i < 12; i++)
    data.notifications.unshift(notification("fresh" + i));
  await h.run("load()");
  assert.equal(browser.popups.length, 5);
  assert.equal(h.nodes["#modal-root"].innerHTML, modal);
  assert.match(h.nodes["#toast"].textContent, /12 new cluster notifications/);
  h.run("resetNotifications()");
  assert.ok(browser.popups.every((n) => n.closed));
  assert.equal(h.run("seenNotifications"), null);
});
test("mark all read sends the visible unread IDs and notification actions open their real source", async () => {
  const data = cluster();
  data.notifications = [
    notification("alert"),
    notification("operation", {
      kind: "operation-succeeded",
      operationID: "op-one",
      alertID: null,
    }),
    notification("read", { readAt: new Date().toISOString() }),
  ];
  data.alerts = [
    {
      id: "alert-one",
      active: true,
      message: "Worker offline",
      kind: "worker-offline",
      severity: "critical",
      openedAt: new Date().toISOString(),
    },
  ];
  const h = await harness(data, (url, init) => {
    if (url === "/api/notifications/read") {
      const ids = JSON.parse(init.body).ids;
      data.notifications = data.notifications.map((n) =>
        ids.includes(n.id) ? { ...n, readAt: new Date().toISOString() } : n,
      );
      return { notifications: data.notifications, unread: 0 };
    }
    return {};
  });
  await h.click({ action: "notifications" });
  await h.click({ action: "read-all-notifications" });
  assert.deepEqual(
    h.requests.find((r) => r.url === "/api/notifications/read").body,
    { ids: ["alert", "operation"] },
  );
  await h.click({ action: "open-notification", id: "alert" });
  assert.equal(h.run("page"), "alerts");
  await h.click({ action: "open-notification", id: "operation" });
  assert.equal(h.run("page"), "activity");
  assert.match(h.nodes["#modal-root"].innerHTML, /Demo initialized/);
});

test("settings display the active HA manager and escape its identity", async () => {
  const data = cluster();
  data.highAvailability = {
    enabled: true,
    managerID: "manager<a>",
    role: "leader",
    ready: true,
  };
  const h = await harness(data);
  await h.click({ action: "nav", page: "settings" });
  assert.match(h.nodes["#app"].innerHTML, /Active\/standby/);
  assert.match(h.nodes["#app"].innerHTML, /manager&lt;a&gt;/);
  assert.doesNotMatch(h.nodes["#app"].innerHTML, /manager<a>/);
});

test("MCP settings expose endpoint, OAuth status and scoped token choices", async () => {
  const data = cluster();
  data.mcp = { endpoint: "https://cluster.example/mcp", oauthEnabled: true };
  const h = await harness(data);
  const panel = vm.runInContext("settings()", h.sandbox);
  assert.match(panel, /AI cluster management/);
  assert.match(panel, /https:\/\/cluster.example\/mcp/);
  assert.match(panel, /OAuth enabled/);
  vm.runInContext('newTokenDialog("mcp_manage")', h.sandbox);
  assert.match(h.nodes["#modal-root"].innerHTML, /value="mcp_manage" selected/);
  assert.match(h.nodes["#modal-root"].innerHTML, /value="mcp_read"/);
  assert.match(h.nodes["#modal-root"].innerHTML, /across the cluster/);
});

test("worker maintenance filters and view switching preserve the selected inventory", async () => {
  const data = cluster();
  data.workers.w2 = {
    ...data.workers.w1,
    id: "w2",
    name: "paused-worker",
    mode: "paused",
  };
  data.workers.w3 = {
    ...data.workers.w1,
    id: "w3",
    name: "offline-worker",
    status: "offline",
  };
  const h = await harness(data);
  await h.click({ action: "nav", page: "workers" });
  assert.match(h.nodes["#app"].innerHTML, /class="worker-table"/);
  await h.click({ action: "filter", filter: "maintenance" });
  assert.match(h.nodes["#app"].innerHTML, /paused-worker/);
  assert.doesNotMatch(h.nodes["#app"].innerHTML, /offline-worker/);
  assert.doesNotMatch(h.nodes["#app"].innerHTML, />worker-one</);
  await h.click({ action: "worker-view", view: "grid" });
  assert.match(h.nodes["#app"].innerHTML, /worker-card/);
  assert.match(h.nodes["#app"].innerHTML, /Manage worker/);
  await h.run("load()");
  assert.equal(h.run("workerView"), "grid");
  assert.equal(h.run("filter"), "maintenance");
  await h.click({ action: "worker-view", view: "table" });
  assert.match(h.nodes["#app"].innerHTML, /class="worker-table"/);
});

test("settings tabs isolate administrative workflows and support keyboard navigation", async () => {
  const h = await harness();
  await h.click({ action: "nav", page: "settings" });
  assert.match(h.nodes["#app"].innerHTML, /Control-plane configuration/);
  assert.doesNotMatch(h.nodes["#app"].innerHTML, /id="global-search"/);
  await h.click({ action: "settings-tab", tab: "access" });
  assert.match(h.nodes["#app"].innerHTML, /Integration API tokens/);
  assert.doesNotMatch(
    h.nodes["#app"].innerHTML,
    /<h2>Control-plane configuration/,
  );
  await h.click({ action: "settings-tab", tab: "ai" });
  assert.match(h.nodes["#app"].innerHTML, /AI access credentials/);
  assert.match(h.nodes["#app"].innerHTML, /data-action="new-mcp-token"/);
  assert.match(h.nodes["#app"].innerHTML, /aria-labelledby="settings-tab-ai"/);
  const event = {
    target: { getAttribute: () => "tab" },
    key: "End",
    preventDefault() {},
  };
  h.handlers.keydown(event);
  assert.equal(h.run("settingsTab"), "setup");
  assert.match(h.nodes["#app"].innerHTML, /Install a worker agent/);
  h.handlers.keydown({ ...event, key: "Home" });
  assert.equal(h.run("settingsTab"), "general");
});

test("overview identifies stale inventory and unavailable managers without reporting a healthy cluster", async () => {
  const stale = cluster();
  stale.workers.w1.lastSeen = new Date(Date.now() - 120000).toISOString();
  const h = await harness(stale);
  assert.match(h.nodes["#app"].innerHTML, /Cluster needs attention/);
  assert.match(h.nodes["#app"].innerHTML, /stale inventory/);
  assert.doesNotMatch(h.nodes["#app"].innerHTML, /Cluster is operational/);
  await h.click({ action: "nav", page: "workers" });
  assert.match(h.nodes["#app"].innerHTML, /Stale inventory/);
  const unavailable = cluster();
  unavailable.highAvailability = { role: "standby", ready: false };
  const g = await harness(unavailable);
  assert.match(g.nodes["#app"].innerHTML, /active manager is not ready/);
});

test("AI endpoint copy and credential revocation use the existing cluster connection", async () => {
  const data = cluster();
  data.mcp = { endpoint: "https://cluster.example/mcp", oauthEnabled: true };
  data.tokens = [
    {
      id: "mcp-one",
      name: "Claude <admin>",
      prefix: "zela_mcp_example",
      scope: "mcp_manage",
    },
  ];
  const h = await harness(data);
  let copied = "";
  h.sandbox.navigator = {
    clipboard: {
      writeText: async (text) => {
        copied = text;
      },
    },
  };
  await h.click({ action: "nav", page: "settings" });
  await h.click({ action: "settings-tab", tab: "ai" });
  assert.match(h.nodes["#app"].innerHTML, /Claude &lt;admin&gt;/);
  await h.click({ action: "copy-mcp-endpoint" });
  assert.equal(copied, data.mcp.endpoint);
  assert.match(h.nodes["#toast"].textContent, /endpoint copied/);
  await h.click({ action: "revoke-token", id: "mcp-one" });
  assert.match(h.nodes["#modal-root"].innerHTML, /Revoke integration token/);
});

test("activity shows measured duration and mobile navigation closes on selection", async () => {
  const data = cluster();
  data.operations[0].startedAt = "2026-10-17T12:00:00Z";
  data.operations[0].finishedAt = "2026-10-17T12:01:25Z";
  const h = await harness(data);
  await h.click({ action: "menu" });
  assert.match(h.nodes["#app"].innerHTML, /sidebar-scrim/);
  await h.click({ action: "nav", page: "activity" });
  assert.doesNotMatch(h.nodes["#app"].innerHTML, /sidebar-scrim/);
  assert.match(h.nodes["#app"].innerHTML, /1m 25s/);
  assert.match(
    h.nodes["#app"].innerHTML,
    /datetime="2026-10-17T12:00:00.000Z"/,
  );
});

test("networking tabs expose real metadata, demo limits and worker selection", async () => {
  const data = cluster();
  data.workers.w2 = { ...data.workers.w1, id: "w2", name: "second-worker" };
  const h = await harness(data, (url) => {
    if (url === "/api/network")
      return { members: {}, records: {}, certificates: [] };
    const workerID = url.includes("/w2/") ? "w2" : "w1";
    return {
      workerID,
      observedAt: new Date().toISOString(),
      networks: [
        {
          id: "n-" + workerID,
          name: "network-" + workerID,
          driver: "bridge",
          scope: "local",
          ipam: { config: [] },
        },
      ],
    };
  });
  await h.click({ action: "nav", page: "network" });
  await new Promise((resolve) => setImmediate(resolve));
  assert.match(h.nodes["#app"].innerHTML, /Encrypted cluster mesh/);
  assert.match(
    h.nodes["#app"].innerHTML,
    /data-action="network-config" disabled/,
  );
  assert.match(
    h.nodes["#app"].innerHTML,
    /does not verify WireGuard handshakes/,
  );
  await h.click({ action: "worker-network-link", worker: "w2" });
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(h.run("networkWorker"), "w2");
  assert.match(h.nodes["#app"].innerHTML, /network-w2/);
  assert.doesNotMatch(h.nodes["#app"].innerHTML, /network-w1/);
  assert.match(
    h.nodes["#app"].innerHTML,
    /Host traffic measurements unavailable or stale/,
  );
  h.handlers.keydown({
    target: { id: "network-tab-workers", getAttribute: () => "tab" },
    key: "End",
    preventDefault() {},
  });
  assert.equal(h.run("networkTab"), "services");
  assert.match(h.nodes["#app"].innerHTML, /Service discovery/);
});

test("network configuration and Docker changes send typed worker-specific requests", async () => {
  const h = await harness();
  await h.form("network-config-form", {
    enabled: "on",
    cidr: "10.201.0.0/16",
    port: "51820",
    edgeHTTPPort: "80",
    edgeHTTPSPort: "443",
    redirectHTTPS: "on",
  });
  assert.deepEqual(h.requests.find((r) => r.url === "/api/network").body, {
    enabled: true,
    cidr: "10.201.0.0/16",
    port: 51820,
    edgeHTTPPort: 80,
    edgeHTTPSPort: 443,
    edgeDrainTimeoutSeconds: 60,
    redirectHTTPS: true,
  });
  await h.form("network-peer-form", {
    workerID: "w1",
    endpoint: "peer.example.net",
  });
  assert.equal(
    h.requests.find((r) => r.url === "/api/workers/w1/network-endpoint").body
      .endpoint,
    "peer.example.net",
  );
  await h.form("docker-network-form", {
    workerID: "w1",
    name: "private-app",
    subnet: "172.28.0.0/24",
    gateway: "172.28.0.1",
    internal: "on",
  });
  assert.deepEqual(
    h.requests.find((r) => r.url === "/api/workers/w1/networks").body,
    {
      name: "private-app",
      subnet: "172.28.0.0/24",
      gateway: "172.28.0.1",
      internal: true,
    },
  );
  await h.form("docker-network-container-form", {
    workerID: "w1",
    networkID: "net-one",
    action: "disconnect",
    containerID: "standalone",
  });
  assert.deepEqual(
    h.requests.find((r) => r.url === "/api/workers/w1/networks/net-one/actions")
      .body,
    {
      action: "disconnect",
      containerID: "standalone",
    },
  );
});

test("network inspection escapes worker data and protects built-ins and managed containers", async () => {
  const data = cluster();
  data.workers.w1.containers.push({ Id: "standalone", Names: ["/standalone"] });
  const h = await harness(data, (url) => ({
    id: "net-one",
    name: url.endsWith("/builtin") ? "zela" : "<script>network</script>",
    driver: "bridge",
    scope: "local",
    ipam: { config: [{ subnet: "172.28.0.0/24" }] },
    containers: {
      "container-one": { name: "managed", ipv4Address: "172.28.0.2/24" },
      standalone: { name: "<img src=x>", ipv4Address: "172.28.0.3/24" },
    },
  }));
  await h.click({
    action: "docker-network-inspect",
    worker: "w1",
    id: "net-one",
  });
  let html = h.nodes["#modal-root"].innerHTML;
  assert.match(html, /&lt;script&gt;network/);
  assert.match(html, /&lt;img/);
  assert.doesNotMatch(html, /<script>network|<img src=x/);
  assert.match(html, /data-container="standalone"/);
  assert.doesNotMatch(html, /data-container="container-one"/);
  assert.match(html, /data-action="docker-network-delete"[^>]*disabled/);
  await h.click({
    action: "docker-network-connect",
    worker: "w1",
    id: "net-one",
  });
  html = h.nodes["#modal-root"].innerHTML;
  assert.match(html, /option value="standalone"/);
  assert.doesNotMatch(html, /option value="container-one"/);
  await h.click({
    action: "docker-network-inspect",
    worker: "w1",
    id: "builtin",
  });
  html = h.nodes["#modal-root"].innerHTML;
  assert.match(html, /read-only here/);
  assert.doesNotMatch(
    html,
    /data-action="docker-network-connect"|data-action="docker-network-delete"|data-action="docker-network-disconnect"/,
  );
});

test("route editing preserves other routes and submits a concurrency check", async () => {
  const data = cluster();
  const base = [
    { host: "one.example.com", path: "/", port: 80 },
    { host: "two.example.com", path: "/api", port: 8080 },
  ];
  data.deployments.d1.routes = base;
  const h = await harness(data);
  await h.form("network-route-form", {
    deploymentID: "d1",
    baseRoutes: JSON.stringify(base),
    index: "1",
    remove: "false",
    host: " api.example.com ",
    path: " /v2 ",
    port: "9090",
  });
  const edited = h.requests.find((r) => r.url === "/api/deployments/d1/routes");
  assert.equal(edited.method, "PUT");
  assert.deepEqual(edited.body.expectedRoutes, base);
  assert.deepEqual(edited.body.routes, [
    base[0],
    { host: "api.example.com", path: "/v2", port: 9090 },
  ]);
  await h.form("network-route-form", {
    deploymentID: "d1",
    baseRoutes: JSON.stringify(base),
    index: "0",
    remove: "true",
  });
  const removed = h.requests.filter((r) => r.url === edited.url).at(-1);
  assert.deepEqual(removed.body.routes, [base[1]]);
});

test("network fetches discard obsolete worker responses and session data", async () => {
  const data = cluster();
  data.workers.w2 = { ...data.workers.w1, id: "w2" };
  const pending = {};
  const h = await harness(data, (url) =>
    url === "/api/network"
      ? { members: {} }
      : new Promise((resolve) => {
          pending[url] = resolve;
        }),
  );
  h.run("page='network';networkWorker='w1'");
  const first = h.run("fetchDockerNetworks(true)");
  h.run("networkWorker='w2'");
  const second = h.run("fetchDockerNetworks(true)");
  await new Promise((resolve) => setImmediate(resolve));
  pending["/api/workers/w2/networks"]({ workerID: "w2", networks: [] });
  await second;
  pending["/api/workers/w1/networks"]({
    workerID: "w1",
    networks: [{ name: "stale" }],
  });
  await first;
  assert.equal(h.run("dockerNetworks.workerID"), "w2");
  const late = h.run("fetchDockerNetworks(true)");
  await new Promise((resolve) => setImmediate(resolve));
  h.run("resetNetworkViews()");
  pending["/api/workers/w2/networks"]({
    workerID: "w2",
    networks: [{ name: "old-session" }],
  });
  await late;
  assert.equal(h.run("dockerNetworks"), null);
});

test("certificates show actual expiry, issuance failures and pending status", async () => {
  const data = cluster();
  data.acmeCertificates = [
    { host: "future.example", notAfter: "2099-01-01T00:00:00Z" },
    { host: "expired.example", notAfter: "2020-01-01T00:00:00Z" },
    { host: "pending.example", notAfter: "0001-01-01T00:00:00Z" },
    { host: "failed.example", error: "<script>error</script>" },
  ];
  const h = await harness(data);
  const html = h.run("networkCertificatesPanel()");
  assert.match(html, /datetime="2099-01-01T00:00:00.000Z"/);
  assert.match(html, /Certificate available/);
  assert.match(html, /Expired/);
  assert.match(html, /Pending issuance/);
  assert.match(html, /Issuance error/);
  assert.match(html, /&lt;script&gt;error/);
  assert.doesNotMatch(html, /just now|<script>error/);
});

test("storage access status and resumable move are visible", async () => {
 const data=cluster();data.demo=false;
 data.storageBackends={nfs:{id:"nfs",name:"files",server:"nas.internal",export:"/exports",workerIDs:["w1"]}};
 data.sharedVolumes={vol:{id:"vol",name:"uploads",backendID:"nfs",directory:"app/uploads",identity:"uploads-prod",access:{w1:{checkedAt:new Date().toISOString(),error:"identity mismatch <unsafe>"}}}};
 data.deployments.d1.handover={stage:"cutting-over",source:data.deployments.d1.tasks[0],targetWorkerID:"w1"};
 const h=await harness(data);h.run("page='storage';render()");
 assert.match(h.nodes["#app"].innerHTML,/Identity: uploads-prod/);
 assert.match(h.nodes["#app"].innerHTML,/identity mismatch &lt;unsafe&gt;/);
 h.run("deploymentDetails('d1')");
 assert.match(h.nodes["#modal-root"].innerHTML,/Resume move/);
 await h.click({action:"resume-move",id:"d1"});
 const request=h.requests.find(r=>r.url.includes("replicas/container-one/move"));
 assert.ok(request);assert.equal(request.body.target,"w1");
});

// Model the browser replacing inline form controls whenever #app is redrawn.
// Unlike the general string-only harness, each redraw creates blank controls.
async function storageFormHarness(respond) {
  const data = cluster();
  data.demo = false;
  const h = await harness(data, respond);
  h.run("page='storage';render()");
  const definitions = {
    "storage-backend-form": [
      ["storage-backend-name", "name", "text", ""],
      ["storage-server", "server", "text", ""],
      ["storage-export", "export", "text", ""],
      ["", "workerIDs", "checkbox", "w1"],
      ["", "workerIDs", "checkbox", "w2"],
    ],
    "storage-volume-form": [
      ["storage-volume-name", "name", "text", ""],
      ["storage-backend", "backendID", "select-one", ""],
      ["storage-directory", "directory", "text", ""],
      ["storage-identity", "identity", "text", ""],
    ],
  };
  let forms = [];
  function replaceForms() {
    forms = Object.entries(definitions).map(([id, definitions]) => {
      const elements = definitions.map(([id, name, type, value]) => ({
        id, name, type, value, checked: false,
        focus() { h.sandbox.document.activeElement = this; },
        setSelectionRange(start, end, direction) {
          this.selectionStart = start;
          this.selectionEnd = end;
          this.selectionDirection = direction;
        },
      }));
      const form = {
        id, elements,
        querySelector() { return null; },
        reset() { elements.forEach((field, index) => {
          field.value = definitions[index][3];
          field.checked = false;
        }); },
        get fields() {
          const values = {};
          for (const field of elements) {
            if (field.type === "checkbox") {
              if (field.checked) (values[field.name] ||= []).push(field.value);
            } else values[field.name] = field.value;
          }
          return values;
        },
      };
      h.nodes["#" + id] = form;
      for (const field of elements) if (field.id) h.nodes["#" + field.id] = field;
      return form;
    });
  }
  let html = h.nodes["#app"].innerHTML;
  Object.defineProperty(h.nodes["#app"], "innerHTML", {
    get: () => html,
    set: value => { html = value; replaceForms(); },
  });
  h.nodes["#app"].querySelectorAll = () => forms;
  replaceForms();
  return h;
}

test("storage drafts, worker choices and text selection survive inventory redraws", async () => {
  const h = await storageFormHarness();
  h.nodes["#storage-backend-name"].value = "production-nfs";
  h.nodes["#storage-server"].value = "nas.internal";
  h.nodes["#storage-export"].value = "/exports/apps";
  h.nodes["#storage-backend-form"].elements[3].checked = true;
  h.nodes["#storage-volume-name"].value = "uploads";
  h.nodes["#storage-backend"].value = "nfs_existing";
  h.nodes["#storage-directory"].value = "app/uploads";
  h.nodes["#storage-identity"].value = "uploads-prod";
  const input = h.nodes["#storage-server"];
  input.setSelectionRange(0, 3, "backward");
  input.focus();
  for (let poll = 0; poll < 3; poll++) await h.run("load()");
  assert.equal(h.nodes["#storage-backend-name"].value, "production-nfs");
  assert.equal(h.nodes["#storage-server"].value, "nas.internal");
  assert.equal(h.nodes["#storage-export"].value, "/exports/apps");
  assert.equal(h.nodes["#storage-backend-form"].elements[3].checked, true);
  assert.equal(h.nodes["#storage-backend-form"].elements[4].checked, false);
  assert.equal(h.nodes["#storage-volume-name"].value, "uploads");
  assert.equal(h.nodes["#storage-backend"].value, "nfs_existing");
  assert.equal(h.nodes["#storage-directory"].value, "app/uploads");
  assert.equal(h.nodes["#storage-identity"].value, "uploads-prod");
  assert.equal(h.sandbox.document.activeElement, h.nodes["#storage-server"]);
  assert.equal(h.nodes["#storage-server"].selectionStart, 0);
  assert.equal(h.nodes["#storage-server"].selectionEnd, 3);
  assert.equal(h.nodes["#storage-server"].selectionDirection, "backward");
});

test("storage save failures retain drafts and success resets the visible form after an in-flight poll", async () => {
  let resolveSave;
  let fail = true;
  const h = await storageFormHarness(url => {
    if (url === "/api/storage/backends") {
      if (fail) throw new Error("registration failed");
      return new Promise(resolve => { resolveSave = resolve; });
    }
    return {};
  });
  h.nodes["#storage-backend-name"].value = "production-nfs";
  h.nodes["#storage-server"].value = "nas.internal";
  h.nodes["#storage-export"].value = "/exports/apps";
  h.nodes["#storage-backend-form"].elements[3].checked = true;
  h.nodes["#storage-volume-name"].value = "keep-volume-draft";
  const submit = () => h.handlers.submit({ preventDefault() {}, target: h.nodes["#storage-backend-form"] });
  await submit();
  await h.run("load()");
  assert.equal(h.nodes["#storage-server"].value, "nas.internal");
  fail = false;
  const saving = submit();
  await new Promise(resolve => setImmediate(resolve));
  await h.run("load()");
  assert.equal(h.nodes["#storage-server"].value, "nas.internal");
  resolveSave({ id: "nfs_created" });
  await saving;
  assert.equal(h.nodes["#storage-backend-name"].value, "");
  assert.equal(h.nodes["#storage-server"].value, "");
  assert.equal(h.nodes["#storage-export"].value, "");
  assert.equal(h.nodes["#storage-backend-form"].elements[3].checked, false);
  assert.equal(h.nodes["#storage-volume-name"].value, "keep-volume-draft");
  const request = h.requests.filter(r => r.url === "/api/storage/backends").at(-1);
  assert.equal(request.body.server, "nas.internal");
  assert.deepEqual(request.body.workerIDs, ["w1"]);
});

test("deployment details show configured networking, observed connections and shared storage", async () => {
  const data = cluster();
  const d = data.deployments.d1;
  d.name = "web_api.v2";
  d.imageDigest = "example/app@sha256:pinned";
  d.tasks[0].image = d.imageDigest;
  d.ports = [{host: 8080, container: 80, protocol: "tcp", hostIP: "127.0.0.1"}];
  d.routes = [{host: "app.example.com", path: "/api", port: 80}];
  d.workloadPolicy = "overlap-safe";
  d.storageAttachments = [{volumeID: "vol_uploads", target: "/srv/uploads", readOnly: false}];
  data.network = {enabled: true, cidr: "10.200.0.0/16", services: {d1: {vip: "10.200.255.1"}}, edgeHTTPPort: 80, edgeHTTPSPort: 443};
  data.workers.w1.containers[0].NetworkSettings = {Networks: {zela: {IPAddress: "10.200.1.2"}}};
  data.workers.w1.containers[0].Ports = [{PublicPort: 8080, PrivatePort: 80, Type: "tcp"}];
  data.workers.w1.containers[0].Mounts = [{Type: "volume", Name: "zela-uploads", Destination: "/srv/uploads", RW: true}, {Type: "bind", Destination: "/etc/app-config", RW: false}];
  data.storageBackends = {nfs: {name: "production-files", server: "nas.internal", export: "/exports", revision: 7}};
  data.sharedVolumes = {vol_uploads: {name: "uploads", identity: "uploads-prod", backendID: "nfs", directory: "web/uploads", access: {w1: {checkedAt: new Date().toISOString(), backendRevision: 7, image: d.imageDigest, readOnly: false}}}};
  const h = await harness(data);
  await h.click({action: "deployment-details", id: "d1"});
  const html = h.nodes["#modal-root"].innerHTML;
  for (const text of ["Deployment network", "10.200.255.1", "web-api-v2.zela", "app.example.com/api", "HTTP :80", "HTTPS :443", "127.0.0.1:8080", "10.200.1.2", "Current inventory", "Deployment storage", "Overlap safe", "production-files", "nas.internal:/exports/web/uploads", "/srv/uploads", "uploads-prod", "Read/write", "Last access check passed", "backend revision 7", "Observed container mounts", "zela-uploads", "/etc/app-config", "Read only"])
    assert.ok(html.includes(text), `Missing deployment detail: ${text}`);
});

test("deployment details explain unconfigured network and storage without claiming unobserved mounts", async () => {
  const h = await harness();
  h.run("deploymentDetails('d1')");
  const html = h.nodes["#modal-root"].innerHTML;
  for (const text of ["Deployment network", "Not enabled", "Not allocated", "No public routes configured", "No host ports configured", "Not reported", "No registered shared volumes attached", "No mount details reported by current inventory"])
    assert.ok(html.includes(text), `Missing empty-state explanation: ${text}`);
  assert.ok(!html.includes("Last access check passed"));
});

test("deployment detail observations distinguish stale configurations, errors and unavailable storage metadata", async () => {
  const data = cluster();
  const d = data.deployments.d1;
  d.workloadPolicy = "single-writer";
  d.imageDigest = "example/app@sha256:new";
  d.tasks[0].image = d.imageDigest;
  d.storageAttachments = [
    {volumeID: "old", target: "/data", readOnly: true},
    {volumeID: "failed", target: "/failed", readOnly: false},
    {volumeID: "unavailable", target: "/unknown", readOnly: true},
  ];
  d.routes = [{host: "<unsafe>", path: "/", port: 80}];
  data.workers.w1.status = "offline";
  data.storageBackends = {nfs: {name: "files", server: "nas.internal", export: "/exports", revision: 2}};
  data.sharedVolumes = {
    old: {name: "old-data", backendID: "nfs", directory: "old", access: {w1: {checkedAt: new Date().toISOString(), image: "example/app@sha256:old", backendRevision: 1, readOnly: true}}},
    failed: {name: "unsafe <name>", backendID: "nfs", directory: "failed", access: {w1: {checkedAt: new Date().toISOString(), error: "Access denied <unsafe>"}}},
  };
  const h = await harness(data);
  h.run("deploymentDetails('d1')");
  const html = h.nodes["#modal-root"].innerHTML;
  for (const text of ["Single writer", "Read only", "Cached or unavailable", "Previous check passed; configuration differs", "Access denied &lt;unsafe&gt;", "unsafe &lt;name&gt;", "Details unavailable", "Not checked", "These routes require an enabled cluster network"])
    assert.ok(html.includes(text), `Missing safety distinction: ${text}`);
  assert.ok(!html.includes("Last access check passed"));
  assert.ok(!html.includes("<unsafe>"));
});
