# Manage Zela from a terminal

[All guides](README.md) · [Browser alternative](console-guide.md)

The **CLI** is Zela's command-line program, named `zela`. It is optional: use the [browser guide](console-guide.md) if you prefer buttons and forms.

## A first session

After installing the CLI, save your administrator token in a private file. Replace the example URL and file path with your own:

```sh
zela login --url https://cluster.example.com --token-file /secure/admin-token
zela status
zela workers list
zela deployments list
```

**Check:** the listed cluster and workers should match your dashboard. Add `--wait` to supported change commands when you want to wait for the final operation result. An accepted change may still fail; read the final status.

For a command you have not used before, start with `zela help`. The sections below explain installation, connection profiles, permissions, and commands.

`zela` is a Go client for the control plane. Install it on your laptop, bastion, or automation host. Requests go to the cluster HTTP(S) origin, including the HA load-balancer endpoint. The CLI does not read cluster state files, access Docker sockets, open SSH sessions to workers, or implement application scheduling. Manager-side provisioning still installs Docker and the worker agent over SSH.

## Build and install

```sh
make cli                    # Native macOS or Linux binary: bin/zela
make linux                  # Linux amd64 and arm64 binaries, plus manager/worker artifacts
sudo install -m 755 bin/zela-linux-amd64 /usr/local/bin/zela
sudo restorecon /usr/local/bin/zela    # RHEL with SELinux enabled
zela version
zela help
```

On an arm64 RHEL administration host use `bin/zela-linux-arm64`. The CLI needs no Docker installation or Go runtime once built. Building just `make cli` does not rebuild or restart the manager. Install and restart the rebuilt manager when upgrading server behavior; updating the CLI alone does not change the running server.

## Connect to a remote cluster

Securely transfer the administrator token into a private local file, then save a verified connection profile:

```sh
chmod 600 /secure/cluster-admin-token
zela login --profile production \
  --url https://cluster.example.internal:8443 \
  --token-file /secure/cluster-admin-token

# If the endpoint uses a private CA:
zela login --profile production \
  --url https://cluster.example.internal:8443 \
  --token-file /secure/cluster-admin-token \
  --ca-cert /secure/cluster-ca.pem

zela status
zela workers list
zela config list
```

Use your actual cluster origin, without `/api`, credentials, or token query parameters. The token is sent in the Authorization header. TLS hostname and certificate verification remain enabled. The CLI refuses to send a token over plain HTTP unless the host is `localhost` or a loopback address; use HTTPS for remote administration. Token-less `health` and `ready` checks work over HTTP.

Profiles contain the endpoint, optional CA path, and bearer token. They are stored atomically in a mode-`0600` JSON file under the OS user configuration directory: normally `~/.config/zela/config.json` on Linux and `~/Library/Application Support/zela/config.json` on macOS. Newly created directories use `0700`. Tokens in local profiles are protected by file permissions, not encrypted. Listing profiles never prints tokens. Override the path using `--config` or `ZELA_CONFIG`.

```sh
zela config set staging --url https://staging.example.internal --token-file /secure/staging-token
zela config use production
zela --profile staging workers list
zela logout --profile staging         # Remove its local token; does not revoke the server token
zela config delete staging
```

`login` verifies access through `/api/cluster` before saving; `config set` saves offline. `config set` saves only a token supplied with `--token-file`, or keeps the profile's existing token for the same URL; it never stores an ambient `ZELA_TOKEN` or `ZELA_TOKEN_FILE`. Changing a profile's origin clears its old token unless you explicitly supply a new token source. Similarly, a `--url` override cannot silently inherit the saved token of another origin.

For automation, use `ZELA_URL` with `ZELA_TOKEN_FILE` or a securely injected `ZELA_TOKEN`, without saving a profile. Connection precedence is explicit flags, environment, then the selected profile. Token-file sources override `ZELA_TOKEN`; `--token-file` overrides `ZELA_TOKEN_FILE`. Select a profile with `--profile`, `ZELA_PROFILE`, or the saved current profile. `ZELA_CA_CERT` supplies a trusted CA file.

There is no command-line token/password/private-key value flag. `--token-file -` reads a single token line from stdin until EOF. Private token, SSH-key and passphrase input files must have no group/other permissions (normally `0600`). Only one input can consume stdin in a command; use separate files when uploading a key with a passphrase or supplying a token and JSON payload.

## Workers and the SSH vault

```sh
zela secrets create --name rpm-workers --key-file /secure/id_ed25519
zela secrets list

# Use the returned key_... ID. Supply the host public key from a trusted source.
zela workers provision \
  --name rhel-worker-01 --host 10.20.0.31 --region dc-east \
  --user zela-admin --sudo \
  --secret key_EXAMPLE --host-key-file /secure/rhel-worker-01.hostkey \
  --wait

zela workers list
zela workers get wrk_EXAMPLE
zela workers retry wrk_EXAMPLE --wait
zela workers update-agent wrk_EXAMPLE --wait
zela workers metrics wrk_EXAMPLE --window 24h --json
```

The installer uses the existing platform detection and Docker repository setup, including supported RHEL/Rocky/AlmaLinux platforms. The target must already exist and be SSH-accessible; provisioning does not create a VM. Non-root users need passwordless sudo. `--host-key-file` contains a pinned host public key (`ssh-ed25519 AAAA...`), not a private key or fingerprint. Upload the SSH private key only with `secrets create`; it remains encrypted in the server vault.

For encrypted SSH keys add `--passphrase-file /secure/key-passphrase`. Provisioning also accepts `--file provision.json`, containing the existing API shape:

```json
{
  "name": "rhel-worker-01",
  "region": "dc-east",
  "ssh": {
    "host": "10.20.0.31",
    "port": 22,
    "user": "zela-admin",
    "secretID": "key_EXAMPLE",
    "hostKey": "ssh-ed25519 REPLACE_WITH_TRUSTED_HOST_PUBLIC_KEY",
    "sudo": true,
    "agentPort": 9091
  }
}
```

Register an already installed HTTPS worker instead:

```sh
zela workers add --name docker-worker-02 --endpoint https://10.20.0.32:9091 \
  --worker-token-file /secure/worker-02-token --region dc-east --wait
```

```sh
zela workers pause wrk_EXAMPLE --wait
# Move or remove its workloads, then refresh inventory.
zela refresh --wait
zela workers activate wrk_EXAMPLE --wait
zela workers remove wrk_EXAMPLE --wait
zela secrets remove key_EXAMPLE
```

Pause prevents new placement and leaves existing containers running. Remove unregisters a worker; it does not uninstall Docker, delete a VM, or stop the worker service. Server checks prevent removing registered containers/replicas or deleting an SSH key that a worker still references. The legacy `workers drain` command moves platform-managed deployments, and fails when externally managed containers remain. For workloads owned by an external scheduler, use pause and let that scheduler relocate them.

## Tokens and access permissions

Token/vault administration and full-state commands require the administrator token. Dedicated `mcp_read`/`mcp_manage` credentials support the MCP tools and their explicitly allowed cluster APIs; use `mcp serve` for the complete supported tool catalog. The existing `infrastructure` and `deployments` token scopes retain their server-enforced restrictions; the CLI does not grant extra permissions or issue administrator-scoped tokens.

```sh
zela tokens create --name my-platform --scope infrastructure \
  --expires-days 30 --out /secure/platform-token
zela tokens list
zela tokens revoke tok_EXAMPLE

# Scoped tokens can discover workers and capacity:
zela --token-file /secure/platform-token workers list --json
```

Without `--out`, creation prints the new token once in its JSON response. With `--out`, it writes the token to a new mode-`0600` file and prints only metadata and the file path. Existing files are never overwritten. Protect stdout if you choose to print a token. Revocation and expiry are checked by the server. `logout` only removes the saved local credential.

## Monitoring and notifications

```sh
zela monitoring rules
zela monitoring set-rules --file alert-rules.json
zela alerts list
zela alerts acknowledge alt_EXAMPLE
zela notifications list
zela notifications read ntf_EXAMPLE
zela notifications read --all
```

`--all` reads the currently visible unread notifications and acknowledges their IDs in batches of at most 500. With no unread notifications it returns the current notification response. It does not acknowledge future notifications.

An alert rules document supplies all six existing fields:

```json
{
  "cpuPercent": 90,
  "memoryPercent": 90,
  "diskPercent": 85,
  "resourceForSeconds": 60,
  "offlineForSeconds": 30,
  "healthForSeconds": 30
}
```

## Jobs and automation

```sh
zela workers update-agent wrk_EXAMPLE --wait --wait-timeout 35m --json
zela operations list
zela operations get op_EXAMPLE
zela operations wait op_EXAMPLE --wait-timeout 35m --json
zela ready
```

Mutating commands return accepted operation IDs immediately unless `--wait` is supplied. `operations wait` always polls. Waiting returns the final operation object; a failed job, timeout, authorization error, or other command failure produces a nonzero exit code and an error on stderr. Ctrl-C/SIGTERM cancels the client and exits `130`; it does **not** cancel an already accepted manager job. Use its ID to inspect the outcome.

`--timeout` (default `30s`) bounds each HTTP request; `--wait-timeout` (default `35m`) bounds polling. Polling can retry read-only connection failures and HTTP 502/503 during manager failover. Mutations are never automatically retried. A lost response may mean a mutation was accepted: inspect `operations list` before resubmitting. For HA, connect to the readiness-aware cluster endpoint rather than an individual standby.

List commands print tables by default; detail/mutation responses print readable JSON. `--json` emits one JSON value on stdout for scripting and preserves integer precision. Progress/errors go to stderr. There are no interactive confirmations; server validation and existing token permissions apply to every request.

## Existing workload and JSON API access

Container troubleshooting is available through the manager:

```sh
zela containers list --worker wrk_EXAMPLE
zela containers logs wrk_EXAMPLE CONTAINER_ID
zela containers restart wrk_EXAMPLE CONTAINER_ID --wait
```

Container inventory listing requires administrator access. MCP read credentials can read container logs, and MCP management credentials can perform container actions. Managed replicas must use the existing deployment APIs. `deployments` exposes the existing create/list/get/start/stop/restart/remove/scale/move/move-replica/update-image operations for compatibility; it adds no scheduling algorithm. In an infrastructure-only setup, the external platform remains the owner of the containers it creates.

`api` provides authenticated access to JSON endpoints, useful for future API additions and selected Docker Engine JSON requests:

```sh
zela api GET /api/cluster --json
zela api GET '/docker/workers/wrk_EXAMPLE/containers/json?all=1' --json
zela api POST /api/monitoring/rules --file alert-rules.json
```

Paths stay on the configured cluster origin and redirects are not followed. API request files must contain a single JSON value, up to 1 MiB. Response JSON is bounded to 32 MiB. This command does not handle Docker binary logs, streaming pull/build output, exec/attach, or interactive shells; use a Docker-compatible client through the same gateway for those protocols. CLI log output is the existing bounded log snapshot, not `logs -f`.

This CLI does not administer the operating system, restart manager services, replace service flags, configure PostgreSQL replication, or perform backups. Those remain host/database administration tasks. It covers the platform's existing administrative and workload HTTP APIs.

## MCP integration

Run `zela --profile production mcp serve` to expose the platform tools over stdio to a coding assistant. Use `--read-only` to limit the local catalog. The command uses the same saved profile, TLS policy and shared API client; it never opens worker SSH connections or Docker sockets. Create an `mcp_read` or `mcp_manage` credential in Settings & setup. Stdin/stdout are reserved for MCP, so a token cannot be read from stdin. The control plane also serves remote MCP at `/mcp`, with optional OAuth for ChatGPT and Claude. See the [MCP guide](mcp.md) for connection examples and permissions.

## Verification

`make test check` runs the Go race tests, the UI tests and static checks. CLI tests exercise the real control-plane handler with isolated demo workers, including vault uploads, simulated SSH provisioning, scoped authorization, revocation, private files and job sequencing. These are not live RHEL installation tests.

For an executable-level HTTP smoke test on a Linux/macOS host that permits local TCP listeners, build matching native binaries and run:

```sh
make build
ZELA_TEST_MANAGER_BINARY="$PWD/bin/zela-server" \
ZELA_TEST_CLI_BINARY="$PWD/bin/zela" \
go test ./internal/cli -run '^TestCLIProcessesOverHTTP$' -count=1 -v
```

The opt-in test starts a fresh demo manager in a temporary directory, generates its own administrator token, and verifies the compiled CLI over HTTP. It checks profiles, worker pause/activate, monitoring, scoped-token denial and revocation, notification acknowledgements, and nonzero exits for failed asynchronous jobs. It does not connect to your running manager, use real worker credentials or access a Docker socket. The test is skipped unless both binary paths are supplied.

## Shared application storage

See [Shared application storage](shared-storage.md) for NFS setup, application permissions, deployment policies, safe moves, interrupted-move recovery, and live acceptance tests. Storage administration requires an administrator. Single-writer automatic failover remains blocked without independently enforced fencing.
