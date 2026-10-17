# Getting started

Three ways to see the control plane running, from the quickest to the closest to production.

## 1. Demo mode (no Docker, no hosts)

Demo mode simulates Docker hosts. It never connects to Docker or changes a real machine.

```sh
git clone <repository-url> zela && cd zela
go run ./cmd/zela-server --demo
```

Open <http://127.0.0.1:8080> and sign in with the generated token:

```sh
cat ./data-demo/admin-token
```

Demo state is kept in `./data-demo`, separate from real state in `./data`. Delete that directory to start over.

## 2. Local single manager with Docker Compose

This runs the control plane and a PostgreSQL database in containers.

```sh
cp .env.example .env
chmod 600 .env
# Put a random password into .env:
sed -i.bak "s/replace-with-a-random-password/$(openssl rand -hex 32)/" .env && rm .env.bak

docker compose up --build -d
docker compose exec zela-server cat /data/admin-token
```

Open <http://127.0.0.1:8080>. The console is bound to host loopback only. Keep `.env`: the same database password is needed on every start.

Connect a Docker host next. See [Connect workers](installation.md#5-connect-workers).

## 3. Native binaries

```sh
make build                    # bin/zela-server, bin/zela-worker, bin/zela, Linux worker artifacts
./bin/zela-server --worker-binaries ./bin
cat ./data/admin-token
```

Without `ZELA_DATABASE_URL`, state is stored as private JSON files in `./data`. That is fine for evaluation and small single-manager clusters. See [Installation](installation.md) for PostgreSQL, TLS and systemd.

## Your first deployment

1. **Add a worker.** For the quickest path on the same machine, open **Workers → Connect worker** and register `unix:///var/run/docker.sock`. Docker Desktop on macOS normally uses `unix:///Users/<you>/.docker/run/docker.sock`. Registering a local socket needs no token.
2. **Deploy.** Open **Deployments → New deployment**, enter `nginx:alpine`, 1 replica, 128 MB, 0.25 CPU, and submit.
3. **Watch it.** **Activity** shows the operation, and **Containers** lists the new replica after the next inventory poll (every 10 seconds).

From a terminal:

```sh
./bin/zela login --url http://127.0.0.1:8080 --token-file ./data/admin-token
./bin/zela workers list
./bin/zela deployments list
```

## Next steps

- [Architecture](architecture.md): what each component does and which ports it uses.
- [Installation](installation.md): production setup.
- [Security](security.md): read this before exposing anything beyond localhost.
