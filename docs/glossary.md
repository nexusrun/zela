# Plain-language glossary

[All guides](README.md) · [Browser management guide](console-guide.md)

You do not need to memorize these terms. Look them up when a setting or message uses one.

## Applications and servers

| Term                            | What it means in Zela                                                                                       |
| ------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| **Cluster**                     | Your group of servers, managed together                                                                     |
| **Manager / control plane**     | The computer and Zela program that provide the dashboard and coordinate work                                |
| **Worker / host / node**        | A Linux server where applications run                                                                       |
| **Agent**                       | The small `zela-worker` program that lets the manager communicate with Docker and collect host measurements |
| **Docker**                      | Software on a worker that runs packaged applications in containers                                          |
| **Container image**             | The packaged application used to create a container. `nginx:alpine` is an example                           |
| **Image tag**                   | The version label after `:` in an image name. Changing it can change the software you deploy                |
| **Container**                   | One instance created from an image, with its own running process and local writable files                   |
| **Deployment**                  | The saved application settings Zela manages: image, copies, resources, and placement                        |
| **Replica**                     | One copy of a deployment. Two replicas mean two containers                                                  |
| **Stateless application**       | An app whose important data is stored elsewhere, so its containers can be replaced without losing that data |
| **Volume / persistent storage** | Storage intended to keep data when a container is replaced. Zela does not copy it between workers           |
| **Environment variable**        | A named application setting, such as `APP_ENV=production`; it may contain a secret                          |
| **CPU / memory / RAM**          | Processing power and working memory available to an application                                             |
| **Reservation**                 | Resources Zela accounts for when deciding whether a worker has room. It is different from measured usage    |

## Status and actions

| Term                       | Meaning                                                                                                                   |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| **Online**                 | The manager can reach the worker. Check freshness and any reported errors too                                             |
| **Active**                 | A worker may receive new application placements                                                                           |
| **Paused**                 | No new placements; existing containers continue running                                                                   |
| **Drain**                  | Try to move eligible managed applications away before server maintenance. Check the result before rebooting               |
| **Scale**                  | Change the number of application copies                                                                                   |
| **Rollout / update image** | Replace application copies using another image                                                                            |
| **Degraded**               | The deployment is not meeting its intended running state; inspect its error and replicas                                  |
| **Operation**              | A tracked change, such as deploying or draining. Starting it is not the same as completing it                             |
| **Failover / recovery**    | Recreate lost managed application copies on another suitable worker when possible                                         |
| **High availability / HA** | Extra managers waiting to take over management if the active manager fails; this does not replicate your application data |
| **Acknowledge**            | Mark an alert as reviewed. It does not fix the underlying problem                                                         |

## Networking and access

| Term                         | Meaning                                                                                                                                          |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| **IP address / hostname**    | An address identifying a computer, such as `10.0.0.21` or `worker.example.com`                                                                   |
| **Port**                     | A numbered entry point on a computer. One computer can serve different programs on different ports                                               |
| **Published port**           | A mapping from a worker's port to an application's container port. `8081:80` means worker port 8081 leads to container port 80                   |
| **localhost / 127.0.0.1**    | The computer where the command or browser is running; inside a container it refers to that container                                             |
| **Cluster mesh / WireGuard** | The optional encrypted private network connecting Linux workers                                                                                  |
| **Subnet / CIDR**            | An address range. `10.200.0.0/16` describes the size and location of one range; ask your network administrator to choose a non-overlapping range |
| **Peer endpoint**            | The worker hostname or IP address the other workers use for their encrypted network connection                                                   |
| **Bridge network**           | A Docker network local to one worker. Creating one does not connect different workers                                                            |
| **VIP / virtual IP**         | A stable private service address that can send traffic to the deployment's healthy copies                                                        |
| **DNS**                      | The system that turns a name into an address. Internal names such as `web.zela` are different from public website DNS                            |
| **Edge proxy / route**       | A worker service and rule that forward a website hostname/path to an application                                                                 |
| **HTTPS / TLS certificate**  | Encrypted browser communication and the certificate that identifies the server                                                                   |
| **SSH**                      | A secure way to access a server. Zela can use it to install and manage a worker                                                                  |
| **SSH login key**            | Your private key used to sign in to a server; store it in the Secrets vault                                                                      |
| **SSH host public key**      | The server's identity. Verify it through a trusted source before giving it to Zela                                                               |
| **Administrator token**      | The secret used to sign in and fully control the cluster                                                                                         |
| **Vault key**                | The master encryption key used to unlock saved SSH secrets. Keep it with your recovery materials                                                 |
| **MCP**                      | The connection that lets an AI assistant call Zela's supported tools                                                                             |
| **Read / management access** | Permission to inspect information / permission to make changes as well                                                                           |
| **PostgreSQL**               | The database used to save Zela's configuration and history; separate from application databases                                                  |
