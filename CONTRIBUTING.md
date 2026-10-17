# Contributing

Thanks for helping. This guide covers the development setup, repository layout, tests and pull-request expectations.

## Development setup

- Go **1.25+**, `make`, and Node.js (for the frontend tests).
- Optional: Docker (live gateway tests), PostgreSQL (database and HA tests), HAProxy (HA process test).

```sh
make build                       # native binaries + Linux worker artifacts in bin/
go run ./cmd/zela-server --demo   # simulated cluster at http://127.0.0.1:8080
cat ./data-demo/admin-token
```

Demo mode is the fastest way to work on the console or API without real hosts.

## Repository layout

| Path | Contents |
| --- | --- |
| `cmd/zela-server` | Manager entry point (standalone and HA) |
| `cmd/worker` | `zela-worker` agent |
| `cmd/zela` | CLI entry point |
| `internal/control` | Scheduler, state store, PostgreSQL, HA, Docker gateway, provisioning, monitoring, HTTP API; `web/` holds the embedded console |
| `internal/cli`, `internal/platformclient` | CLI commands and the shared HTTP client |
| `internal/workerproxy` | Worker-side authentication, route allow-list, leader-epoch fencing |
| `internal/engine`, `internal/dockerapi` | Docker Engine client and API path rules |
| `internal/sshclient` | SSH connections with pinned host keys |
| `internal/telemetry` | Host and container metrics collection |
| `deploy/` | systemd units, HA drop-in, HAProxy example |
| `docs/` | User documentation |
| repository root (`main.go`, …) | The separate legacy `nexus-agent` |

## Tests

```sh
make check        # go vet + JavaScript syntax
make test         # all Go tests with -race, plus frontend tests
make test-ha      # HA election/fencing tests
make test-gateway # Docker gateway, worker proxy and API path tests
```

Optional integration tests, skipped unless configured:

```sh
# PostgreSQL + HA (the login needs CREATEDB; tests create and drop their own database)
docker run -d --rm --name zela-pg -e POSTGRES_PASSWORD=pw -p 55432:5432 postgres:18-alpine
ZELA_TEST_DATABASE_URL='postgres://postgres:pw@127.0.0.1:55432/postgres?sslmode=disable' \
  go test ./internal/control -run 'Postgres|HA' -count=1 -v

# Real Docker daemon, through the production gateway and worker proxy
ZELA_TEST_DOCKER_SOCKET=/var/run/docker.sock ZELA_TEST_DOCKER_IMAGE=alpine:latest make test-docker-live
```

See [Configuration → Test-only variables](docs/configuration.md#test-only-variables) for the full list.

## Guidelines

- **Match the surrounding code.** Standard library first; the only notable dependency is `pgx`. Keep comment density and naming consistent with nearby code.
- **Fail closed.** Validate inputs, never log secrets (`redact` helpers exist), keep `0600`/`0700` permissions on anything private, and never silently retry a Docker mutation.
- **State changes go through `Store.update`.** Re-read records inside the update closure; don't write back a snapshot taken before a network call.
- **Tests with every change.** Bug fixes need a regression test. Concurrency changes should pass under `-race` with `-count=3` or more.
- **Docs with every behavior change.** Update `docs/` (especially [configuration.md](docs/configuration.md)) when you add a flag, variable, default or schema migration.
- **Schema migrations** are append-only (`postgresStore.migrate`). Bump the accepted version and note the downgrade impact in the release notes.

## Pull requests

1. Fork, branch from `main`, and keep each PR focused on one change.
2. Run `gofmt`, `make check` and `make test`.
3. Describe what changed, why, and how you tested it. Call out security, compatibility or migration impact.

Report security issues privately; see [SECURITY.md](SECURITY.md).
