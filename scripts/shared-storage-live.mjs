// Run only against a disposable two-worker cluster and a dedicated test volume.
import { readFile } from "node:fs/promises";
import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";

function required(name) {
  const value = process.env[name];
  if (!value) throw new Error(`Set ${name}. See docs/shared-storage.md.`);
  return value;
}
const origin = required("ZELA_TEST_CLUSTER_URL").replace(/\/$/, "");
const token = (await readFile(required("ZELA_TEST_ADMIN_TOKEN_FILE"), "utf8")).trim();
const source = required("ZELA_TEST_SOURCE_WORKER");
const target = required("ZELA_TEST_TARGET_WORKER");
assert.notEqual(source, target, "Two different workers are required");
const edges = required("ZELA_TEST_EDGE_URLS").split(",").map(s => s.trim());
assert.equal(edges.length, 2, "Supply exactly two ingress worker URLs");
assert.notEqual(edges[0], edges[1], "Two different ingress workers are required");
const host = `storage-${randomUUID()}.example.invalid`;
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
async function api(path, body) {
  const response = await fetch(origin + "/api" + path, {
    method: body === undefined ? "GET" : "POST", redirect: "error",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(15000),
  });
  const result = await response.json();
  if (!response.ok) throw new Error(`API ${path}: ${response.status}: ${result.error || "request failed"}`);
  return result;
}
async function wait(operation) {
  const deadline = Date.now() + 10 * 60 * 1000;
  while (Date.now() < deadline) {
    const state = await api("/state");
    const current = state.operations.find(o => o.id === operation.id);
    if (current && current.status !== "running") {
      assert.equal(current.status, "succeeded", current.detail);
      return state;
    }
    await sleep(250);
  }
  throw new Error("Operation deadline expired");
}
async function file(edge, method = "GET", body) {
  const response = await fetch(edge.replace(/\/$/, "") + "/file", {
    method, body, headers: { Host: host }, redirect: "error", signal: AbortSignal.timeout(5000),
  });
  assert.equal(response.status, method === "PUT" ? 204 : 200, `Ingress ${edge}: ${response.status}`);
  return response.text();
}
const name = "storage-test-" + randomUUID().slice(0, 8);
let id, running = false, readers = [], failure;
let requests = 0;
const failures = [];
try {
  let state = await wait(await api("/deployments", {
    name, image: required("ZELA_TEST_STORAGE_IMAGE"), replicas: 1,
    workerIDs: [source], memoryMB: 128, cpus: 0.5, workloadPolicy: "overlap-safe",
    storageAttachments: [{ volumeID: required("ZELA_TEST_VOLUME_ID"), target: "/data", readOnly: false }],
    routes: [{ host, path: "/", port: 8080 }],
  }));
  let deployment = Object.values(state.deployments).find(d => d.name === name);
  id = deployment.id;
  assert.ok(deployment.imageDigest, "Image was not pinned");
  const revision = deployment.imageDigest;
  const payload = "zela-shared-storage-" + randomUUID();
  await file(edges[0], "PUT", payload);
  for (const edge of edges) assert.equal(await file(edge), payload);
  running = true;
  readers = edges.map(edge => (async () => {
    while (running) {
      try { assert.equal(await file(edge), payload); requests++; }
      catch (error) { failures.push(error.message); }
      await sleep(20);
    }
  })());
  for (const destination of [target, source, target]) {
    state = await wait(await api(`/deployments/${id}/replicas/${deployment.tasks[0].id}/move`, { target: destination }));
    deployment = state.deployments[id];
    assert.equal(deployment.tasks.length, 1);
    assert.equal(deployment.tasks[0].workerID, destination);
    assert.equal(deployment.imageDigest, revision);
    assert.equal(deployment.handover.stage, "completed");
    for (const edge of edges) assert.equal(await file(edge), payload);
  }
  const updated = payload + "-updated";
  running = false;
  await Promise.all(readers);
  await file(edges[1], "PUT", updated);
  for (const edge of edges) assert.equal(await file(edge), updated);
  assert.ok(requests > 0);
  assert.deepEqual(failures, [], "Finite HTTP requests failed during handovers");
  console.log(`PASS: 3 moves, immutable revision, shared read/write continuity, ${requests} continuous requests across 2 ingress workers.`);
} catch (error) {
  failure = error;
} finally {
  running = false;
  await Promise.all(readers);
  if (id && !failure) await wait(await api(`/deployments/${id}/actions`, { action: "delete" }));
  if (failure) {
    console.error(`FAIL: ${failure.message}. Deployment ${id || name} retained for inspection.`);
    process.exitCode = 1;
  }
}
