# Shared storage and deployment handover plan

**Status: implementation and local safety hardening added, October 19, 2026; live release gates pending.** The working tree includes registered NFSv4 volumes, bounded identity and application-user access probes, immutable image references, persistent handover checkpoints and duplicate-safe retry, lifecycle traffic gating, captured ingress acknowledgments, independent edge connection pools, admin storage status and resume controls, CLI/API operations, and a disposable live acceptance harness. No production-readiness claim is made until the infrastructure gates below pass. See the [user guide](shared-storage.md) for setup, recovery, and the live test command.

Local coverage includes exclusive-writer admission and rollback ordering, uncertain startup responses, lost Create response reconciliation, immutable image identity, observed destination endpoints, offline ingress rejection, connection-pool retirement, drain timeout retries, storage identity/access checks, permissions isolation, API access control, persistence, and console workflows. Real NFS behavior and manager HA stage-by-stage recovery still require the disposable Linux environment.

## Outcome

Move an application between workers while keeping the same application version, persistent files, and public URL. For an application that can safely run overlapping copies, start and check the destination before moving traffic and stopping the source.

A move starts a replacement container. It does not rebuild the application, transfer a running process, preserve process memory, or move existing TCP connections. An image might need to be downloaded on the destination. Shared storage supplies persistent files, not the executable process.

The first release targets Linux workers in the same region, an existing NFSv4 service, and HTTP applications behind Zela's cluster edge routes. Storage-server installation, database replication, and Ceph administration are separate work.

## Baseline before this work

| Area                | Previous implementation                                                            | Required change                                                                         |
| ------------------- | --------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| Replacement         | `replaceTask` starts and checks the destination before stopping the source        | Retain this ordering and add durable handover stages                                    |
| Startup check       | Docker health check when available; otherwise three running observations          | Require an explicit readiness check for a planned seamless handover                     |
| Application version | `provision` pulls the configured image reference                                  | Record an immutable revision and move by the resolved digest, not a mutable tag         |
| Persistent files    | Managed deployment specifications have no volume attachments; moves reject mounts | Add registered shared volumes and validate every attachment                             |
| Routing             | Backends come from running container inventory and exclude unhealthy containers   | Explicitly exclude starting and draining replicas; acknowledge traffic changes          |
| Recovery            | Offline replicas are replaced and returning replicas are reconciled later         | Apply storage and workload concurrency rules before replacement or re-adoption          |
| Persistence         | Cluster state supports JSON files or PostgreSQL                                   | Persist volume definitions, revisions, replica states, and handover checkpoints in both |

Relevant code: [deployment state](../internal/control/store.go), [provisioning and moves](../internal/control/control.go), [placement restrictions](../internal/control/docker_placement.go), [network backends](../internal/control/network.go), [engine specifications](../internal/engine/engine.go), [worker network rules](../internal/clusternet/agent.go), and [edge proxy](../internal/clusternet/edge.go).

## First-release decisions

- Use an administrator-provided NFSv4 endpoint. Zela manages application volume attachments, not the storage server or its replication.
- Keep code in container images. Store only explicitly selected persistent application paths on shared volumes.
- Support shared read-only and shared read/write mounts. Read/write sharing requires an explicit declaration that the application supports concurrent copies; filesystem support alone does not establish this.
- Keep local volumes bound to their worker. Reject their automatic cross-worker moves rather than creating an empty replacement volume.
- Treat single-writer applications separately. A planned handover must confirm the old writer stopped before starting a new writer. Automatic recovery remains blocked until real fencing is available.
- Use the existing stable service VIP and edge route for supported handovers. Traffic sent directly to a worker IP and published port does not follow the application to another host.
- Keep storage administration admin-only initially. Existing integration and MCP credentials do not acquire volume-creation, host-mount, or arbitrary-path privileges.
- Preserve current behavior for existing stateless deployments. Require an explicit opt-in and capability checks for the new handover mode.

## Phase 1: deployment revisions and handover state

Add small, focused types rather than expanding the existing controller file substantially:

- `DeploymentRevision`: image reference, resolved image digest, runtime configuration, and immutable attachment definitions. For mixed architectures, record compatible platform-specific digests and verify destination compatibility.
- `Replica` lifecycle: `starting`, `ready`, `serving`, `draining`, `stopped`, or `failed`. Inventory refresh must not overwrite an active transition or make an unready replica eligible for traffic.
- `Handover`: operation ID, deployment revision, source and destination IDs, stage, expected routing generation, deadlines, and last confirmed observations.
- Workload policy: overlap permitted, single writer, or worker-local. Record this separately from filesystem mount permissions.

Checkpoint intent before each external change and record its result afterward. Label replacement containers with operation, deployment, and revision IDs so retrying after a manager restart can find them. Never create another replacement merely because an API response was lost.

**Done when:** retries and manager restarts cannot silently change the image version, lose track of a replacement, or exceed the intended replica count. Old state files load with explicit compatibility defaults.

## Phase 2: registered NFS storage and worker checks

Add proposed `StorageBackend`, `SharedVolume`, and `VolumeAttachment` records:

| Record          | Minimum information                                                                                    |
| --------------- | ------------------------------------------------------------------------------------------------------ |
| Storage backend | ID, NFSv4 server, export root, approved workers, version, and health observations                      |
| Shared volume   | ID, backend, administrator-approved application directory, expected identity marker, and access policy |
| Attachment      | Volume ID, absolute container mount path, read-only setting, and application UID/GID requirements      |

The worker implements an optional storage capability, following the existing optional network-capability pattern. Older agents remain usable for existing workloads and are ineligible for deployments requiring the new storage capability.

Use Docker's local NFS volume driver with deterministic Zela-managed volume names and labels. Volume definitions exist on each eligible Docker host but refer to the same remote export and application directory. No user-controlled shell command, arbitrary mount flags, or unrestricted host path is accepted.

The preparation flow must:

1. Validate the server, export, directory, mount path, access mode, and backend revision.
2. Verify host-level private network reachability. Container `.zela` DNS alone does not provide an NFS endpoint reachable by the host Docker daemon.
3. Create or inspect the Docker volume and reject conflicting definitions instead of silently reusing them.
4. Verify the expected export identity from a read-only marker configured during backend registration. Probe read/write permission only in a dedicated health directory, never arbitrary user files.
5. Check with the application's UID/GID and record a timestamped result per worker and backend revision.
6. Recheck immediately before starting the replacement. Fail closed if the remote mount is unavailable or identifies the wrong export. Never fall back to an ordinary local directory.

Use bounded checks that cannot stall the main worker loop when NFS is unavailable. Measure logical volume access rather than presenting filesystem-wide free space as an application quota.

Keep exports private, use root squashing and deliberate UID/GID permissions, and document that basic NFS AUTH_SYS assumes trusted workers. This does not establish isolation from a compromised worker administrator. Do not disable host security protections to make mounts work.

**Done when:** two real workers can access a test application's same files, an incorrect or unavailable export prevents startup, and existing stateless workloads still run on older agents.

## Phase 3: deployment attachments and placement

Extend the managed deployment API and Docker engine specification to attach registered volumes. Persist attachment configuration as part of a revision so moves and recovery use the same definition.

- Placement requires current storage capability, matching volume preparation, available resources and ports, and existing network eligibility.
- Read-only mounts are enforced by Docker. Shared read/write overlap additionally requires the declared workload policy.
- Existing containers with unknown bind mounts, anonymous volumes, or unregistered named volumes remain blocked from automatic cross-worker moves.
- Image-declared volumes must be explicitly accounted for; attaching one shared directory must not accidentally permit unrelated anonymous volumes.
- Deleting an application removes its attachment records and containers, not its persistent files. Backend and volume removal are refused while referenced; first-release APIs do not erase remote data.
- Raw Docker gateway placement restrictions stay in place. Managed attachments do not imply unrestricted volume support for arbitrary gateway requests.

**Done when:** creating, scaling, restarting, updating, draining, and moving a managed application all apply the same storage and access rules. No lifecycle path bypasses them.

## Phase 4: readiness, routing, and connection draining

The first supported handover path uses Zela-managed HTTP(S) ingress and an explicit Docker health check or configured HTTP readiness probe. A container merely staying alive is insufficient for the new mode. Run checks from the appropriate worker network context, with bounded timeouts and restricted probe targets.

Introduce a routing generation and track which traffic-serving workers have applied it. Keep starting replicas out of service backends until readiness succeeds. Explicitly stop sending new requests to draining replicas.

The current edge proxy forwards requests to a VIP using a shared upstream transport. Changing VIP backend rules alone does not guarantee request-level cutover: an existing upstream keep-alive connection can still reach the source. Implement generation-aware backend selection and draining in the edge path, including upstream connection-pool retirement and per-backend in-flight accounting. Preserve stable VIP behavior for service discovery and separately test its connection-level semantics.

Capture all ingress participants for a handover. Require their acknowledgments or an independently verified removal from the external load balancer before retiring the source. An offline or stale edge worker is not safely excluded merely because it cannot reach the manager. If acknowledgment or isolation cannot be established, pause the handover and retain the source.

Wait for in-flight requests to finish, then send the application's configured graceful stop signal and honor its stop timeout. Add a configurable drain deadline, initially 60 seconds. WebSocket, SSE, long-lived TCP, and UDP traffic receive explicit limits; they cannot be promised uninterrupted migration. Surface a deadline expiry before forcing termination, and make force a separate explicit action.

**Done when:** starting replicas receive no application traffic, new HTTP requests stop reaching a draining source after acknowledged cutover, and finite in-flight requests can finish before source removal.

## Phase 5: one handover workflow

Reuse one durable workflow for planned replica moves, eligible rollouts, and worker drains:

1. **Preflight:** check the revision, workload policy, source state, destination resources, storage, readiness configuration, and ingress capability.
2. **Prepare:** pull the pinned image and prepare the registered volumes on the destination while the source keeps serving.
3. **Start:** create the destination as a non-serving replacement. Overlap is allowed only for the approved workload policy.
4. **Check:** wait for application readiness and confirm the replacement can access the expected shared files.
5. **Cut over:** publish the desired serving endpoints, withdraw the source from new traffic, and wait for the required generation acknowledgments.
6. **Drain:** allow old requests to finish within the configured deadline.
7. **Retire:** stop and remove the source, retain persistent data, then commit the final placement and operation result.

Reserve surge capacity before creating the replacement. Reject insufficient capacity or host-port conflicts without stopping the original. Same-worker updates cannot assume that two containers can bind the same published host port.

Before cutover, a failed preparation or readiness check removes only the replacement. After partial cutover, any rollback must publish and acknowledge a compensating routing generation before deleting a backend that some ingress workers may still use. On uncertainty, retain the known-good container and expose a recoverable operation state.

For single-writer planned moves, use a separate stop-then-start branch with an acknowledged source shutdown and an expected service gap. Background jobs, startup migrations, and scheduled tasks must also tolerate overlap before an application can choose the overlapping branch.

**Done when:** a planned move of the test HTTP application retains the same image digest, shared-file content, and public URL throughout the handover, with rollback demonstrated at each stage.

## Phase 6: failure recovery and writer safety

Unexpected worker failure is different from a planned move. Readiness and restart time can cause an interruption; existing connections to a failed worker are lost.

- Allow automatic recovery only for workloads whose declared policy permits duplicates during a network partition and whose required storage is verified on the destination.
- For single-writer workloads, an offline probe is not proof the previous writer stopped. First release reports `blocked: source writer not fenced` and does not start a replacement automatically.
- A manager leadership epoch fences management requests, not a running process's NFS writes. An advisory database lease alone is not sufficient storage fencing either.
- A later exclusive-writer feature needs an independently enforced mechanism, such as verified power fencing or storage-side access revocation, before replacement can start. Do not expose a misleading safe-force action without that mechanism.
- Returning replicas must follow revision, storage, lifecycle, and serving-state rules before re-adoption. Stale replicas must never silently regain serving status.
- Storage outage alerts describe a shared backend failure. Do not repeatedly relocate every application when all destinations use the same unavailable storage.

**Done when:** partition tests cannot produce two writers for a single-writer workload, and shared-safe applications recover without empty replacement storage.

## Phase 7: admin interface, CLI, and documentation

Add a **Storage** navigation item with backends, volumes, attached applications, and per-worker access status. Show registration, preparation, capacity scope, errors, and last verification time in plain language.

The deployment form gains volume selection, container path, read-only choice, application concurrency policy, and readiness settings. The move dialog shows destination eligibility, the handover mode, surge requirements, and specific reasons for blocking a move.

The activity view shows preparation, startup, readiness, traffic transfer, drain, and completion. Distinguish `blocked`, `paused`, `failed`, and `completed with cleanup required`. Do not display success while traffic acknowledgments or source cleanup are unresolved.

Add corresponding admin REST and CLI operations. Existing MCP inspection can show non-secret volume references and move progress; existing manage operations must enforce the same eligibility checks. New storage-admin MCP tools or scoped-token mount permissions require a separate access review and are outside the first release.

Update the installation, management, networking, security, backup, CLI, MCP, and architecture guides. Explain that a shared NFS server still needs its own availability and backup plan. Provide a maintenance-window guide for copying an existing local application's data, validating ownership and checksums, and keeping a rollback copy. Do not imply live data copying is consistent without application coordination.

## Validation and release gates

| Gate            | Required evidence                                                                                                                                                      |
| --------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Persistence     | JSON and PostgreSQL round trips; old-state loading; failed writes; manager restart and HA leader change at every handover stage                                        |
| Storage         | Real NFS access from two Linux Docker workers; correct export identity, UID/GID, read-only enforcement, stale checks, mount outage, and conflicting volume definitions |
| Placement       | Volume, capacity, architecture, placement, network, and surge constraints hold for every lifecycle operation                                                           |
| Traffic         | Continuous requests through at least two ingress workers during repeated moves; no early traffic to starting replicas; finite in-flight requests finish                |
| Drain limits    | Keep-alive, slow requests, WebSocket/SSE, direct VIP clients, failed acknowledgments, and drain timeout have tested and documented behavior                            |
| Data continuity | Upload before a move, read and update after it, restart workers, and confirm unchanged data identity and content                                                       |
| Failure safety  | Partitioned source, destination loss, storage outage, source stop/remove failure, and lost API responses produce safe, recoverable states                              |
| Access          | Anonymous, cross-origin, integration-token, and MCP requests cannot create arbitrary mounts or expose storage configuration beyond their role                          |
| Regression      | Existing deployment, networking, gateway, HA, CLI, frontend, and race checks pass; old workers retain supported stateless behavior                                     |

Use controller and worker fakes for failure ordering and persistence checks, then a disposable live Linux environment with two Docker workers and a separate NFS service. Listener or privileged-mount restrictions in a development sandbox must be reported as missing live validation, not counted as passing tests.

The service-continuity acceptance target is zero failed finite HTTP requests in the controlled planned-move scenario with working ingress, healthy storage, an overlap-safe application, sufficient capacity, and a bounded request duration. This is a test target, not an unconditional availability guarantee.

## Delivery order

| Milestone | Deliverable                                                               | Dependency                                       |
| --------- | ------------------------------------------------------------------------- | ------------------------------------------------ |
| 1         | Immutable revisions and durable replica/handover state                    | Existing store and controller                    |
| 2         | Registered NFS volumes, worker preparation, deployment attachment rules   | Milestone 1                                      |
| 3         | Explicit readiness and acknowledged routing/drain control                 | Milestone 1; integration tested with milestone 2 |
| 4         | Planned moves, rollouts, and worker drains using the shared workflow      | Milestones 2 and 3                               |
| 5         | Storage-aware failure recovery and returning-replica rules                | Milestone 4                                      |
| 6         | Complete admin workflows, CLI/API parity, docs, and live release evidence | Milestones 2 through 5                           |

Build the interface alongside the backend milestones, but keep incomplete actions disabled with concrete explanations. Ship an opt-in beta only after the live storage and planned-handover gates pass. Keep exclusive-writer automatic failover out of scope until independently enforced fencing is implemented and tested.

## Infrastructure details to settle before live setup

- Worker count, regions, architectures, and available spare CPU/memory.
- Existing NFS service or a dedicated/managed storage endpoint, with its own failure and backup strategy.
- Required capacity, latency, throughput, and recovery-time expectations.
- Which applications can overlap, which require a single writer, and where sessions and job queues live.
- Which applications use Zela ingress versus direct worker ports, and their longest normal request duration.

These details affect deployment settings and live acceptance tests. They do not block implementing the state model, storage registration, and safe capability checks.
