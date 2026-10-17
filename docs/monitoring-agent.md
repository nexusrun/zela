# nexus-agent

Runs on each worker Docker host. Every 10 s it pushes host metrics (CPUs,
memory total and in use, disk free, running containers) to
`POST /api/agent/heartbeat`, and it pushes container events as soon as Docker
reports them: non-zero exits, OOM kills and `unhealthy` health checks.

While a host's heartbeats are fresh (under 30 s old), the backend stops
polling it over SSH. If heartbeats stop, polling resumes and the usual
3-failure down alert applies.

Standard library only; one static binary of about 6 MB.

## Build

```sh
./build.sh   # writes deploy/bin/nexus-agent-linux-{amd64,arm64}
go test ./...
```

## Install

`deploy/40-worker.sh` installs it when `AGENT_HOST_NAME` (this host's name in
`DOCKER_HOSTS`) and `HOST_AGENT_SECRET` are set in `deploy.env`. The backend
needs the same `HOST_AGENT_SECRET`; the endpoint answers 503 without it.

Auth: `NEXUS_AGENT_TOKEN = hex(HMAC-SHA256(HOST_AGENT_SECRET, hostName))`, so
no per-host secret is stored anywhere.

Manual install on an existing worker:

```sh
install -m 755 nexus-agent-linux-arm64 /usr/local/bin/nexus-agent
cat > /etc/nexus-agent.env <<ENV
NEXUS_API_URL=http://<core-private-ip>:3001
NEXUS_AGENT_HOST=worker1
NEXUS_AGENT_TOKEN=$(printf '%s' worker1 | openssl dgst -sha256 -hmac "$HOST_AGENT_SECRET" -r | cut -d' ' -f1)
ENV
chmod 600 /etc/nexus-agent.env
install -m 644 deploy/templates/nexus-agent.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now nexus-agent
journalctl -u nexus-agent -f
```
