# Shared application storage

Zela can attach administrator-registered NFSv4 directories to managed deployments. This keeps selected files available when Zela starts a replacement container on another worker. A move still creates a new container from the same image revision. It does not transfer process memory or existing connections.

This feature needs an existing NFS service and Linux workers that can mount it. Zela does not install or operate the NFS server, copy existing application files, or back up the export.

## Before you begin

- Set up an NFSv4 export and a dedicated directory for each application's data.
- Install the NFS client utilities on every approved worker (`nfs-common` on Debian or Ubuntu, `nfs-utils` on RHEL-family systems).
- Allow each worker host to reach the NFS server over the private network. Container DNS does not make an NFS server reachable to the host Docker daemon.
- Set export ownership and permissions for the UID and GID used by the container. Root squashing is recommended. Basic NFS `AUTH_SYS` trusts the worker hosts, so it does not isolate data from a worker administrator.
- Back up the export and verify that your application can safely run with the selected concurrency policy.

## Register a backend and volume

In the administrator console, open **Shared storage**. Register the NFS server, export path, and workers allowed to mount it. Then register a volume using a relative directory inside that export.

The same registered volume gets the same deterministic Docker volume name on each approved worker. Its Docker local driver points to the same NFS directory. Existing Docker volume names with a different definition are rejected.

The administrator REST API offers the same actions:

```sh
curl -X POST http://localhost:8080/api/storage/backends \
  -H "Authorization: Bearer $ZELA_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"production-nfs","server":"nfs.internal","export":"/exports/apps","workerIDs":["wrk_worker1","wrk_worker2"]}'
```

Use `GET /api/storage` to find the returned backend ID. Register a volume beneath it:

```sh
curl -X POST http://localhost:8080/api/storage/volumes \
  -H "Authorization: Bearer $ZELA_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"uploads","backendID":"nfs_backend_id","directory":"website/uploads","identity":"website-uploads-prod"}'
```

The administrator CLI offers these same registration commands. Put each JSON request body in a file with mode `0600`:

```sh
zela storage list
zela storage register-backend --file backend.json
zela storage register-volume --file volume.json
```

On the NFS server, prepare the application directory before deploying. Replace the example path with your actual export directory. This example image runs as UID/GID `1000:1000`:

```sh
mkdir -p /exports/apps/website/uploads/.zela-health
printf '%s\n' website-uploads-prod > /exports/apps/website/uploads/.zela-identity
chown 1000:1000 /exports/apps/website/uploads /exports/apps/website/uploads/.zela-health
chmod 750 /exports/apps/website/uploads /exports/apps/website/uploads/.zela-health
chmod 644 /exports/apps/website/uploads/.zela-identity
```

The marker must match the registered identity. If you omit `identity` when registering through the API or CLI, Zela generates it from the returned volume ID. Read that value before creating the marker. Use a separate directory and identity for each volume. Overlapping directories and exclusive-writer attachments shared by separate deployments are rejected.

Storage administration and attaching volumes require the cluster administrator. Deployment and MCP integration tokens cannot register storage or attach volumes.

## Attach a volume to an application

In the deployment form, choose a shared volume, set its container path, and choose an application concurrency policy:

- **Overlap safe** lets the replacement start while the old container is still running. Select this only when the application and its jobs can safely use shared files from two copies at once.
- **Single writer** requires exactly one replica and stops the old container before starting the replacement. This prevents two copies from writing at once, but the application has an expected service interruption during the move.

Shared-storage deployments must have a Docker health check in their image. Zela waits for the replacement to become healthy before changing traffic. The updated worker prepares the NFS Docker volume and runs an isolated helper using the pinned application image's default user. The helper checks `.zela-identity`; writable attachments also create, read, sync, and remove a random file in `.zela-health`. Checks run before stopping an exclusive writer and again immediately before startup. A failed check prevents startup. The Storage page shows the last result, time, and backend revision per worker. These observations describe access checks, not continuous storage monitoring.

Update every cluster ingress agent before enabling handovers. Agents that reject the routing-generation field cannot acknowledge a move, so the source is retained. Stateless clusters that have never used handovers retain the legacy network configuration format. Use the static Linux worker binaries built by `make workers`. The helper executes this trusted worker binary inside the application image with networking disabled, capabilities removed, and a read-only root filesystem. The application image must match the worker's Linux architecture. Direct Docker sockets and older agents cannot perform these checks and remain available for stateless workloads. A timed-out helper can remain blocked in the host kernel; further checks for that volume fail until an administrator inspects and removes the leftover `zela-storage-probe-*` container. Zela never silently falls back to local storage.

The administrator API accepts one or more attachments when creating a deployment:

```json
{
  "name": "website",
  "image": "example/website:1.4.2",
  "replicas": 1,
  "memoryMB": 512,
  "cpus": 0.5,
  "storageAttachments": [
    { "volumeID": "vol_registered_id", "target": "/var/lib/website/uploads", "readOnly": false }
  ],
  "workloadPolicy": "overlap-safe"
}
```

`target` must be an absolute container path. Set `readOnly` to `true` to prevent writes through that mount. Each image-declared volume path must be explicitly attached. Existing bind mounts, anonymous volumes, and unregistered volumes cannot be moved automatically.

## What happens during a routed move

For an application with a Zela edge route, the destination uses the pinned image revision and the same registered NFS directory. Zela checks the destination's health, asks every captured ingress worker to apply the new backend list, and keeps the old container until those workers acknowledge the route generation. Each edge generation uses a separate upstream connection pool. Edge workers retire the previous pool and wait up to 60 seconds for requests from the previous generation to finish. If a worker does not acknowledge the update or requests do not drain, the move pauses with the known container retained.

The default drain deadline is 60 seconds. Change **Networking → Configure cluster network → Request drain timeout**, or set `edgeDrainTimeoutSeconds` in the network configuration API, to a value from 1 to 240 seconds. A deadline expiry pauses the move. Retrying the same configuration still waits for outstanding old-generation requests. Offline or stale ingress workers remain required participants; losing manager contact does not prove they stopped receiving public traffic.

A durable handover record stores the source, destination, image revision, replacement, ingress participants, and last stage. Container names and handover labels are deterministic. After an interrupted operation, use **Resume move** in deployment details, or retry the same replica move through the CLI/API. Zela finds the existing replacement before creating anything. Other deployment changes remain blocked until the handover completes or safely rolls back. If replacement shutdown cannot be confirmed, a single-writer source stays stopped. This protects data at the cost of availability during uncertainty.

This handover requires the cluster network and an enabled HTTP or HTTPS edge listener on the workers. Clients that connect directly to a worker IP or service VIP do not use this request-drain path. Long-lived WebSocket, SSE, and streaming requests can prevent a move from completing within the drain limit.

Moving a single-writer deployment has a service gap while the replacement starts. Automatic failover for a declared single-writer workload is blocked when the source worker is unreachable because Zela cannot prove that the old process has stopped writing. An offline heartbeat is not fencing.

## Moving data from an existing container

Registering a volume does not copy existing files. Plan a maintenance window, stop writes using the application's own procedure, copy files to the NFS directory, preserve UID/GID ownership, compare checksums, and keep a rollback copy. Restart with the volume mounted only after verifying the copied data. Do not copy a live database or mutable application directory and assume the copy is consistent.

Removing a volume registration never deletes files on the NFS server. Zela refuses to remove a backend while it has registered volumes, and refuses to remove a volume while a deployment references it.

## Current limits

The first implementation supports registered NFSv4 attachments and planned container moves. It does not install NFS clients, measure application-level storage quotas, migrate local data, configure a backup, provide database replication, or fence a running writer on an unreachable host. Test mounts, permissions, and data continuity on two disposable Linux Docker workers before using important data.

The implementation has local regression and failure-ordering tests. Production release remains gated on live NFS, partition, and manager-failover testing. This development environment cannot run those infrastructure checks.

## Run the live acceptance test

Use a disposable cluster with two updated Linux worker agents, an enabled cluster network, HTTP edge listeners, and a dedicated empty NFS test volume. Approve both workers for the backend. Create the identity marker and health directory as above, owned by UID/GID `1000:1000`. The test writes `acceptance-data` in that dedicated volume.

Build the included test image and push it to a registry both workers can pull:

```sh
docker build -t YOUR_REGISTRY/zela-storage-test:acceptance testdata/shared-storage
docker push YOUR_REGISTRY/zela-storage-test:acceptance
```

Provide the cluster details and run the harness. Use private addresses reachable from the test runner for both ingress workers. The script sends a dedicated Host header, so no public DNS entry is necessary. The token file must contain an administrator token and should have mode `0600`.

```sh
export ZELA_TEST_CLUSTER_URL=https://manager.example.internal
export ZELA_TEST_ADMIN_TOKEN_FILE=/secure/path/admin-token
export ZELA_TEST_SOURCE_WORKER=wrk_first
export ZELA_TEST_TARGET_WORKER=wrk_second
export ZELA_TEST_EDGE_URLS=http://10.0.0.11:8080,http://10.0.0.12:8080
export ZELA_TEST_VOLUME_ID=vol_test_volume
export ZELA_TEST_STORAGE_IMAGE=YOUR_REGISTRY/zela-storage-test:acceptance
make test-storage-live
```

The harness creates an overlap-safe deployment, writes data before moving, sends continuous finite HTTP requests through both ingress workers during three moves, confirms the pinned image and data after every move, then writes through the destination. It requires zero failed requests. On success it deletes the test deployment and retains the NFS files. On failure it retains the deployment and handover for inspection. This test does not substitute for deliberate storage outages, worker partitions, slow streams, restart-at-each-stage, or PostgreSQL HA failover checks.

See the [implementation plan](shared-storage-plan.md) for all release gates.

## Local validation on October 19, 2026

| Check | Result |
| --- | --- |
| Controller, storage, rollback, restart-recovery, and persistence regression tests with the race detector | Passed |
| Engine, worker proxy, probe, CLI, Docker gateway helpers, MCP, platform client, SSH, and telemetry package tests with the race detector | Passed |
| Available network configuration, DNS, and edge-generation tests with the race detector | Passed |
| Console tests | 39 passed |
| Go vet, JavaScript syntax, local documentation links, and whitespace checks | Passed |
| Static Linux worker builds for amd64 and arm64; manager and CLI builds | Passed |
| Full unfiltered `go test ./...` | Blocked by this sandbox's TCP/UDP listener restrictions; 24 controller tests and listener-dependent network tests require a normal development host |
| Live NFS continuity, storage outages, worker partitions, and PostgreSQL HA stage-by-stage recovery | Not run; requires the disposable Linux infrastructure described above |

Run `make test` on a normal development host and `make test-storage-live` against the disposable cluster before release. Passing local fake-engine tests does not establish production NFS or availability behavior.
