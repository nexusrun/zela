# Getting started

[All guides](README.md) · Next: [Manage applications in the browser](console-guide.md)

**Choose one path below.** Demo mode lets you practice. Live mode connects to real servers. You do not need to set up both.

| Path                                                                        | What it does                                                       | Before you begin                                                           |
| --------------------------------------------------------------------------- | ------------------------------------------------------------------ | -------------------------------------------------------------------------- |
| [Demo mode](#try-demo-mode)                                                 | Opens a populated practice dashboard without changing real servers | Go 1.25+ and the Zela project folder                                       |
| [Live manager with Docker Compose](#run-a-live-manager-with-docker-compose) | Runs Zela and its database; you connect workers afterward          | Docker with Compose, the Zela project folder, and access to a Linux worker |
| [Native binaries](#use-native-binaries)                                     | Runs the built manager directly                                    | Go 1.25+, `make`, and the project folder                                   |

**Where to run the commands:** on the computer that will run the manager, in the Zela project folder. The browser address `127.0.0.1` always means the computer where that browser is running. If the manager is on another computer, ask your server administrator to set up [secure remote access](installation.md#4-install-the-control-plane).

## Try Demo mode

### 1. Start the practice dashboard

In a terminal in the project folder:

```sh
go run ./cmd/zela-server --demo
```

Leave this terminal open. The first run may take a while as Go downloads dependencies and builds Zela.

### 2. Find your sign-in token

Open a second terminal in the same project folder:

```sh
cat ./data-demo/admin-token
```

Copy the displayed value. This token is your administrator password; keep it private.

### 3. Open Zela

Open **<http://127.0.0.1:8080>** and paste the token into the sign-in screen.

**Check:** you should see a **Demo environment** banner and sample workers. Try opening Workers, Deployments, Monitoring, and Activity log.

> Demo deployments and Docker network changes are simulated. They do not create websites or change real servers. **Configure network** is disabled because the encrypted cluster network needs real Linux agents.

### 4. Stop or return later

Press **Ctrl+C** in the first terminal to stop the demo. Run the same start command to return to it. Saved demo configuration lives in `data-demo`; live configuration uses a separate location.

To switch to real servers, stop the demo and choose a live path below. There is no Demo/Live switch inside the dashboard, and the sample workers are not converted into real servers.

## Run a live manager with Docker Compose

This starts **the manager and database only**. It does not install an application worker. You will add a real worker after signing in.

### 1. Confirm Docker is available

Start Docker, then run:

```sh
docker --version
docker compose version
```

**Check:** both commands should print a version. If either command is missing, install Docker with its Compose plugin before continuing.

### 2. Set the database password

For a **new installation**, copy the example configuration:

```sh
cp .env.example .env
chmod 600 .env
openssl rand -hex 32
```

Copy the random value from the last command. Open `.env` in a text editor and replace `replace-with-a-random-password` with that value. Save the file. Leave the other settings as they are for now.

**Keep this file:** it contains your database password. Do not recreate or change the password on an existing installation without coordinating the database change. Do not commit `.env` to source control.

### 3. Start Zela

```sh
docker compose up --build -d
docker compose ps
```

The first command builds the image and starts the services in the background. The second lists them.

**Check:** `postgres` should become **healthy**, and `zela-server` should be running. If startup fails, inspect the output:

```sh
docker compose logs --tail=80 zela-server postgres
```

### 4. Sign in

```sh
docker compose exec zela-server cat /data/admin-token
```

Open **<http://127.0.0.1:8080>** on the manager computer and use the displayed token. With this default setup, the dashboard is accessible only on that computer.

**Check:** there should be no Demo environment banner. An empty worker list is expected.

### 5. Connect a worker and deploy

Follow [Connect your first worker](console-guide.md#connect-your-first-worker), then [Deploy your first application](console-guide.md#deploy-your-first-application).

> The manager container does not have access to your computer's Docker socket. For this path, use a reachable Linux worker through SSH provisioning or a separately installed worker agent. Do not enter `unix:///var/run/docker.sock` or `127.0.0.1` to reach the computer outside the manager container.

### Stop, start, or restart later

Run these from the same project folder:

| Goal                              | Command                                     |
| --------------------------------- | ------------------------------------------- |
| Stop the services                 | `docker compose stop`                       |
| Start the existing services again | `docker compose start`                      |
| Restart the manager               | `docker compose restart zela-server`        |
| View the latest manager logs      | `docker compose logs --tail=80 zela-server` |

Keep the same `.env` file and named data volumes. **Do not use `docker compose down -v` to stop Zela:** the `-v` option deletes its saved data volumes. Back up important installations using [Operations](operations.md#backups-and-restore).

## Use native binaries

This path runs Zela directly instead of inside Docker. Run in the project folder:

```sh
make build
./bin/zela-server --worker-binaries ./bin
```

Leave the server running. In a second terminal in the same folder:

```sh
cat ./data/admin-token
```

Open **<http://127.0.0.1:8080>** and sign in. With no database configured, Zela stores its configuration in private files in `data`.

For a local single-host trial, a native manager can connect to the same computer's Docker socket if its user has access. See [local Docker connection](installation.md#method-3-local-docker-socket). Linux workers running the agent are needed for the encrypted cluster network and host measurements.

For a background Linux service, remote HTTPS access, or a separately managed database, follow [Installation](installation.md).

## Your first deployment

The full walkthrough is in the [browser management guide](console-guide.md#deploy-your-first-application). You will connect an online, active worker, deploy one copy of `nginx:alpine`, watch Activity log, and open the website using the worker's address.

## If something does not work

| What you see                           | What to do                                                                                                      |
| -------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| `go: command not found`                | Install Go 1.25+ or use the Docker Compose path                                                                 |
| Browser cannot connect                 | Confirm the manager is still running and the browser is on the manager computer                                 |
| `address already in use` for port 8080 | Another program or Zela instance is using the port. Stop the instance you started before starting another       |
| Sign-in rejected                       | Use the token for the instance you started: `data-demo`, `data`, or the Compose command above                   |
| Empty worker list in Live mode         | Expected until you connect a real worker                                                                        |
| Networking configuration is greyed out | Check for the Demo banner or a running operation; see [Networking](networking.md#why-are-the-controls-disabled) |

**Next:** [connect a worker and manage an application](console-guide.md).
