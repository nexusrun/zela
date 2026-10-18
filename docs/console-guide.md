# Manage Zela in your browser

[All guides](README.md) · [Getting started](getting-started.md) · [Glossary](glossary.md)

This guide takes you from an empty live dashboard to a running sample website, then explains everyday management. If you have not opened Zela yet, complete [Getting started](getting-started.md) first.

## Before you begin

You need a running **Live** manager, its administrator token, and at least one Linux server you can use as a worker. The worker needs access to download Docker packages and application images.

**Check the banner:** a **Demo environment** banner means everything is simulated. You can practice, but the example below will not create a real website. [Switch to a live manager](getting-started.md#run-a-live-manager-with-docker-compose) to use real servers.

## Find your way around

| Page              | Use it for                                                                          |
| ----------------- | ----------------------------------------------------------------------------------- |
| **Overview**      | A quick check of workers, applications, and reserved resources                      |
| **Workers**       | Add servers, see their status, and prepare them for maintenance                     |
| **Deployments**   | Create and manage applications                                                      |
| **Containers**    | See individual application copies and read their logs                               |
| **Networking**    | Inspect worker networks, configure the encrypted cluster network, and manage routes |
| **Monitoring**    | See measured CPU, memory, disk, and network activity                                |
| **Alerts**        | Review problems and adjust alert thresholds                                         |
| **Activity log**  | See the outcome of changes you requested                                            |
| **Secrets vault** | Save SSH login keys for worker installation                                         |
| **Settings**      | Manage connection information, access tokens, notifications, and AI integrations    |

Menu labels may be shortened in some views. A change can take an inventory cycle, normally about 10 seconds, to appear.

## Connect your first worker

For a separate Linux server, use **Provision worker**. Zela installs Docker and its agent on that existing server. It does not buy or create a cloud server.

### 1. Gather the server details

Ask the person who manages the server for these items:

| Item                         | Example                                                 | Why it is needed                                   |
| ---------------------------- | ------------------------------------------------------- | -------------------------------------------------- |
| Server address               | `10.0.0.21` or `worker.example.com`                     | The manager must be able to reach it               |
| SSH port and username        | Port `22`, user `deploy`                                | Used to sign in and install software               |
| SSH private login key        | A key file you already use for that server              | Authorizes access; do not share it publicly        |
| Administrator permission     | Root login or passwordless `sudo`                       | Allows installation of Docker and the worker agent |
| Verified SSH host public key | Starts with `ssh-ed25519` or another supported key type | Confirms this is the intended server               |

The **login key** proves who you are. The **host public key** proves which server you are connecting to. They are different keys. Obtain the host key from the provider's server console or another source you already trust.

For supported operating systems and installation requirements, see [Installation prerequisites](installation.md#1-prerequisites).

### 2. Save the login key

1. Open **Secrets vault**.
2. Choose **Add SSH key**.
3. Give it a recognizable name, such as `production-workers`.
4. Paste or import the private login key. Add its passphrase if it has one.
5. Save it.

**Check:** the saved key appears by name and fingerprint. Zela does not show the private key again.

### 3. Provision the server

1. Open **Workers → Provision worker**.
2. Name the worker, for example `worker-1`. A region is a label you can use to organize servers.
3. Enter its server address, SSH port, and SSH user.
4. Select the key you saved.
5. Paste the verified **host public key**.
6. Keep **Use passwordless sudo** selected for a non-root user. Leave the agent port at its default unless your administrator requires another port.
7. Submit, then open **Activity log** to follow the installation.

**Check:** the operation must finish successfully, and the worker should show **Online** with scheduling **Active**. A worker that is merely listed, paused, or showing an installation error is not ready for deployment.

If installation fails, read the failed stage in Activity log. Correct the reported problem, then use **Retry provisioning**. Do not create another worker record for the same failed installation.

### Already have an agent or local Docker?

Use **Connect worker** when Docker and a Zela agent are already installed. You will need the agent's HTTPS address and token. A native single-host manager can also connect to its local Docker socket. See [worker connection methods](installation.md#5-connect-workers).

**Using Docker Compose for the manager?** Its container cannot use your host's Docker socket unless separately configured. Use a reachable worker address; the default Compose setup does not mount that socket.

## Deploy your first application

This example creates a small test website. It uses a published worker port so you can verify the application before setting up domain names or the cluster mesh.

### 1. Enter the application settings

Open **Deployments** and choose the button to create a new deployment. Use:

| Field                 | Enter                            | What it means                                                          |
| --------------------- | -------------------------------- | ---------------------------------------------------------------------- |
| Application name      | `hello-web`                      | A name for this application in Zela                                    |
| Container image       | `nginx:alpine`                   | A sample web server image                                              |
| Replicas              | `1`                              | Run one copy                                                           |
| Worker placement      | Your first online, active worker | Makes the test website's location easy to find                         |
| CPU limit per replica | `0.5`                            | Allow up to half a CPU core per copy                                   |
| Memory limit          | `128` MB                         | Allow 128 MB per copy                                                  |
| Published ports       | `8081:80`                        | Send traffic from worker port 8081 to the website on container port 80 |
| Environment variables | Leave blank                      | This example needs none                                                |
| Command override      | Leave blank                      | Use the image's normal startup command                                 |

Choose an unused worker port if 8081 is already taken. The manager dashboard uses 8080 by default; your application address is separate.

### 2. Deploy and check the result

1. Select **Deploy application**.
2. Open **Activity log** and wait for the operation to finish.
3. Return to **Deployments** and open `hello-web`.
4. Confirm that its one replica is running. Check **Containers** if you need to identify its worker or read logs.

**Success looks like:** a successful operation and one running copy. “Operation started” alone does not mean the website is ready.

### 3. Open the website

Use your worker's address, not the manager's address:

```text
http://WORKER_ADDRESS:8081
```

For example, if that worker is `10.0.0.21`, open `http://10.0.0.21:8081` from a computer that can reach it. Replace `WORKER_ADDRESS` with your real address.

**Check:** the browser should show the nginx welcome page.

If it does not open, confirm the replica is running, check the worker address and port, and ask your server administrator whether the firewall permits that connection. Published ports may be reachable on public interfaces too; restrict them to the intended audience.

Demo deployments do not serve a website. A running container also does not automatically create a public domain or HTTPS certificate. Follow [Networking](networking.md) when you are ready for those.

### 4. Remove the sample when finished

Open the deployment and choose **Delete** if you no longer need it. This removes the managed application copies and frees their reserved resources. It does not remove the worker.

## Everyday management

### Check whether things are healthy

1. Open **Overview** for the cluster summary.
2. Check **Workers** for offline servers or paused scheduling.
3. Open **Monitoring** for current measured usage and history.
4. Review **Alerts**, then check **Activity log** for failed changes.

Resource reservations help Zela decide where an application will fit. Monitoring shows what the servers actually use. Check both, especially if the server runs applications outside Zela.

### Change an application

Open **Deployments**, select the application, and choose the action:

| Goal                             | Action           | What to expect                                                                        |
| -------------------------------- | ---------------- | ------------------------------------------------------------------------------------- |
| Run more or fewer copies         | **Scale**        | Change the desired replica count, then check the operation                            |
| Temporarily stop the application | **Stop**         | Its website/service becomes unavailable until started again                           |
| Run a stopped application        | **Start**        | Zela starts or creates the required copies                                            |
| Restart the application process  | **Restart**      | Restarts existing containers; does not move them to a newly configured network        |
| Deploy another image version     | **Update image** | Choose the image and destination worker; inspect the result before retrying a failure |
| Relocate an eligible app         | **Move**         | Zela recreates copies on the destination; local data is not copied                    |
| Remove the application           | **Delete**       | Removes the managed copies and saved deployment                                       |

Before scaling the sample above, remember that a fixed published port can be used by only one replica per worker. Two copies using worker port 8081 need two eligible workers. Use **Networking → Services & routes** for shared application routing when the cluster network is configured.

Moves and image updates need spare capacity. They do not migrate volumes, database files, or connections in memory. Plan storage and backups separately for applications that keep important data.

### Read application logs

Open **Containers**, select the container, and choose its logs action. Logs can explain application startup failures. They may also contain sensitive application data; remove secrets before sharing them for support.

### Prepare a server for maintenance

1. Open the worker in **Workers**.
2. Choose **Pause scheduling** to prevent new applications from being placed there; existing containers keep running.
3. Choose **Drain worker** when you want eligible managed applications moved off.
4. Follow the operation in **Activity log** and inspect what remains in **Containers**.
5. Reboot or patch only after you have confirmed the remaining workloads can be interrupted.
6. When the server returns, verify it is online and choose **Activate scheduling**.

Draining can fail if another server has no room, an app uses local storage, or unmanaged containers remain. **Unregister** removes the worker from Zela's records; it does not uninstall software or stop its containers.

### Handle an alert or notification

Open **Alerts** to read the cause. **Acknowledge** records that someone reviewed it; the problem stays active until the observed condition recovers. Reading an item in the notification bell is separate from acknowledging an alert.

For desktop popups, use the notification settings and allow them in your browser. The console must remain open and signed in.

## Why is a button disabled?

| What you see                                         | Explanation and next step                                                                                                         |
| ---------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| Configure network is disabled in Demo mode           | The mesh needs live Linux workers. Start a live manager to configure it                                                           |
| Several action buttons are disabled                  | Check for a running operation in Activity log; Zela handles one cluster operation at a time                                       |
| Deploy application is disabled                       | Connect an online worker with **Active** scheduling                                                                               |
| Cluster address range or UDP port cannot be edited   | Existing mesh members lock these fields. Remove dependent workloads, disable the mesh, and wait for teardown before changing them |
| A Docker network cannot be changed                   | Built-in and Zela-managed networks are protected; only custom local bridges are editable here                                     |
| An AI assistant can inspect but cannot change things | It may have read access. Review its permission in Settings; grant management access only if intended                              |

For networking details, see [Networking controls](networking.md#why-are-the-controls-disabled). For errors after an action is accepted, inspect the operation result rather than repeatedly clicking.

## Deployments and containers: what is the difference?

A **Deployment** is the application configuration Zela manages. Its **containers** are the individual copies currently running on workers.

Containers created outside managed Deployments, including through the Docker gateway, can appear in **Containers**. Seeing them there does not give them deployment scaling, moves, or automatic recreation on worker failure. Their owner is responsible for recovery.

## What to set up next

| Next task                                              | Guide                           |
| ------------------------------------------------------ | ------------------------------- |
| Encrypt traffic between workers or add website routes  | [Networking](networking.md)     |
| Connect an AI assistant with read or management access | [AI cluster management](mcp.md) |
| Protect credentials and remote access                  | [Security](security.md)         |
| Make backups and plan upgrades                         | [Operations](operations.md)     |
