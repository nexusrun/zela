# systemd installation

Build Linux binaries with `make linux`, then copy the appropriate binary and unit to each machine. Create a dedicated `zela` user on the control-plane host and `zela-worker` user on workers. The worker unit uses the `docker` group to reach `/var/run/docker.sock`.

Control-plane host:

```sh
sudo useradd --system --no-create-home zela
sudo install -m 755 bin/zela-server-linux-amd64 /usr/local/bin/zela-server
sudo install -d -m 755 /opt/zela/workers
sudo install -m 755 bin/zela-worker-linux-amd64 bin/zela-worker-linux-arm64 /opt/zela/workers/
sudo install -m 644 deploy/zela-server.service /etc/systemd/system/
sudo install -d -m 750 -o root -g zela /etc/zela
```

Place `control.crt` and `control.key` in `/etc/zela`; the service user must be able to read them. Create `/etc/zela/zela-server.env` containing an `ZELA_ADMIN_TOKEN` of at least 32 characters (or leave this file empty to generate the token in `/var/lib/zela/admin-token`). Set the environment file and private key to mode `0640`, owner `root:zela`. Install your CA into the system trust store when worker certificates use a private CA.

For PostgreSQL, also set `ZELA_DATABASE_URL=postgres://zela@db.example.internal:5432/zela?sslmode=verify-full` and securely supply `PGPASSWORD` in `/etc/zela/zela-server.env`. Use an existing database whose login can create the application schema. The manager creates tables and applies migrations automatically before accepting requests. Existing JSON files are imported only into an empty database; retain the original vault master key. The local state directory remains necessary. See [database setup and backups](../README.md#postgresql-storage-and-automatic-initialization). RHEL managers use the same binary and configuration; a separate PostgreSQL server can be hosted on your RPM-based infrastructure.

Install the OpenSSH client on the manager (`dnf install openssh-clients` on RHEL/Rocky/AlmaLinux; `apt-get install openssh-client` on Ubuntu/Debian). The unit reads worker artifacts from `/opt/zela/workers`. Optionally supply `ZELA_VAULT_KEY` in the protected environment file before the first start; otherwise a master key is generated under `/var/lib/zela`. Back up the vault with its original master key; see the [vault guide](../README.md#ssh-secrets-vault).

For automated worker installation, enable the manager, add an SSH identity in **Secrets vault**, then select **Workers → Provision worker**. Use an existing supported RHEL/Rocky/AlmaLinux 8–10, CentOS Stream 9/10, Ubuntu, or Debian host with root or passwordless sudo and a trusted host public key. The installed `zela-worker.service` binds only loopback, with traffic tunneled through SSH. No worker TLS certificates or public agent port are needed for this path. RHEL requires working package repositories; Podman and conflicting runtime packages require operator review before installation. See the [platform and provisioning guide](../README.md#provision-a-docker-worker-from-the-console).

On an SELinux-enabled manager, restore contexts after copying files and before enabling its service:

```sh
sudo restorecon -RF /usr/local/bin/zela-server /opt/zela /etc/zela /etc/systemd/system/zela-server.service
```

Alternatively, install a TLS worker manually:

```sh
sudo useradd --system --no-create-home zela-worker
sudo install -m 755 bin/zela-worker-linux-amd64 /usr/local/bin/zela-worker
sudo install -m 644 deploy/worker.service /etc/systemd/system/
sudo install -d -m 750 -o root -g docker /etc/zela
```

Create `/etc/zela/worker.env` containing `ZELA_WORKER_TOKEN=<unique 64-character random hex token>`. Generate the token with `openssl rand -hex 32`. Place a trusted `worker.crt` and `worker.key` in `/etc/zela`, and set the environment file and private key to mode `0640`, owner `root:docker`. Register this worker's endpoint and token in the console.

Enable the corresponding service:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now zela-server   # on the manager
sudo systemctl enable --now worker          # on each worker
journalctl -u zela-server -f
journalctl -u worker -f
```

Adjust addresses, certificate paths, socket location, and firewall rules to match your private network. Protect Docker socket access as host administrator access. These units use TLS by default.

Clients access Docker through the manager’s HTTP/HTTPS port with its bearer token. Container creation without a worker selector uses automatic placement; later container and exec requests route to the persisted owner. `X-Zela-Worker` and `/docker/workers/<worker-id>/` remain optional overrides for worker-specific networks, volumes and other APIs. Newly installed agents expose the authenticated `/docker/` namespace automatically. Update older agents to the rebuilt binary first; existing service flags can remain. This grants Docker administrator access, so keep both manager and agent tokens private. See [Docker API access](../README.md#docker-api-access) for SDK/CLI examples, stream support, and workload ownership.

For multiple active/standby managers behind one cluster endpoint, see [high availability setup](HA.md). The supplied systemd drop-in supports RHEL managers and shares PostgreSQL state and credentials while retaining separate local data directories.

For remote platform administration, install `zela-linux-amd64` or `zela-linux-arm64` on your administration host. It needs only HTTPS access to the cluster endpoint. See the [CLI guide](../docs/cli.md) for RHEL installation and authentication.
