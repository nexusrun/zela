# Manage Zela with an AI assistant

[All guides](README.md) · [Browser management guide](console-guide.md)

**MCP** is the connection that lets an assistant ask Zela to run supported tasks. You can use it to ask questions such as “Which workers need attention?” or, with management permission, “Scale the web application to two copies.” Zela still checks the assistant's access and records changes.

## Choose the right access

| You want the assistant to…                 | Choose                   | Important limit                                                  |
| ------------------------------------------ | ------------------------ | ---------------------------------------------------------------- |
| Inspect health, inventory, and logs        | **MCP · Read cluster**   | Logs can contain application data                                |
| Deploy, scale, stop, or maintain workloads | **MCP · Manage cluster** | Can change workloads across the cluster, including deleting them |

In Zela, open **Settings → AI connections** to find connection information and credential shortcuts. Review connections in the integration-token settings; revoke a connection when you no longer need it. Do not paste your administrator token into an AI conversation.

## Pick a connection method

| Where the assistant runs                  | Follow                                                                   | What must be ready                                                                   |
| ----------------------------------------- | ------------------------------------------------------------------------ | ------------------------------------------------------------------------------------ |
| A cloud service such as ChatGPT or Claude | [Remote HTTPS connection](#connect-chatgpt-or-claude-over-https)         | A trusted HTTPS address reachable by that service; manager OAuth settings configured |
| A local coding assistant                  | [Local assistant connection](#local-coding-assistants-stdio)             | The Zela CLI and a saved MCP credential/profile                                      |
| Your own integration code                 | [Direct bearer clients](#direct-bearer-clients-and-gpt-api-applications) | An MCP token and a client that supplies it securely                                  |

Ask the person running the manager to complete the connection setup below if HTTPS, callback URLs, or CLI profiles are unfamiliar. A localhost address on your laptop is not reachable by a cloud assistant.

**After asking for a change:** have the assistant check the returned operation until it succeeds or fails, then review Activity log if needed. “Accepted” means work started. These tools do not copy application data, create cloud servers, or edit cluster networking; use the browser's Networking section for its supported controls.

## Connection details

The control plane includes an MCP server at **`/mcp`**, using the official Go MCP SDK and Streamable HTTP. ChatGPT, Claude and coding assistants can inspect the cluster, deploy images, scale applications, read logs and maintain workers through named tools. No model API key or LLM runs inside the platform: the AI client translates your request into tools, and the control plane authenticates and executes them.

The AI section in Settings shows the endpoint, OAuth status and shortcuts for creating credentials.

## Connect ChatGPT or Claude over HTTPS

1. Serve the control plane at a trusted HTTPS origin reachable by your client, such as `https://cluster.example.com`. Set the externally visible origin before starting the manager:

   ```sh
   export ZELA_MCP_PUBLIC_URL=https://cluster.example.com
   ./bin/zela-server --listen 127.0.0.1:8080
   ```

   Terminate HTTPS at your reverse proxy, or use the server's `--tls-cert` and `--tls-key`. The public URL is an **origin without `/mcp`**. Forward `/mcp`, `/oauth/*`, `/.well-known/*`, `/oauth.css` and the console to the active manager, preserving the Host header. Forward Authorization and MCP headers. Do not cache these responses or retry POST requests. For the supplied Compose stacks, the environment variables are forwarded automatically. Set the same public URL and callback allowlist on every HA manager.

2. In ChatGPT, add a custom MCP server with URL `https://cluster.example.com/mcp`, select OAuth, and request scopes **`mcp_manage offline_access`** for management or **`mcp_read offline_access`** for inspection. Depending on the interface, this is under Plugins → Add custom MCP server or Settings → Apps → Create. Workspace permissions and account availability apply. Follow [OpenAI's connection guide](https://help.openai.com/en/articles/12584461-connecting-custom-mcp-servers-in-chatgpt).

   In Claude, open Settings → Connectors → Add custom connector and use the same endpoint. Follow [Claude's connector guide](https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp).

3. Complete the Zela consent page in your browser. Use your existing administrator session, or enter the administrator token **on the cluster's own page**. Choose read or management access and select Connect cluster. The client receives a separate credential; it never receives your administrator token. A client that omits scopes gets the same read/management choice, with offline access. An explicit read-only scope cannot be upgraded on the consent page.

4. Start a conversation with the connector enabled. For example:

   - “Show me worker health, available CPU and memory, and any stale metrics.”
   - “Deploy nginx:alpine as web with two replicas, 128 MB and 0.25 CPU per replica.”
   - “Scale web to three replicas and report when the operation finishes.”
   - “Show the recent logs for the failing container.”
   - “Drain worker west-02 and tell me whether any workloads need attention.”

Cloud clients need network access to the endpoint. A localhost URL on your laptop is available only to local clients. Private HTTPS networks require a client-supported network connection or tunnel; the platform does not create a public tunnel automatically.

### OAuth callbacks

Dynamic registration accepts **exact, administrator-approved callback URLs**. The defaults are:

```text
https://chatgpt.com/connector_platform_oauth_redirect
https://claude.ai/api/mcp/auth_callback
https://claude.com/api/mcp/auth_callback
```

ChatGPT can assign a connection-specific callback. Copy the exact callback shown by the connector and set a comma-separated allowlist before restarting:

```sh
export ZELA_MCP_REDIRECT_URIS='https://chatgpt.com/connector/oauth/YOUR_CALLBACK_ID,https://claude.ai/api/mcp/auth_callback'
```

An explicit list replaces the defaults. Include every callback you need; no wildcard matching is supported. Local OAuth clients can use an explicitly allowlisted HTTP loopback callback. This server advertises authorization-response issuer identification and returns `iss`, allowing compatible clients to use stable redirects. See [OpenAI's callback guidance](https://developers.openai.com/plugins/build/auth).

OAuth uses public clients (`token_endpoint_auth_method=none`), authorization code flow with PKCE S256, protected-resource discovery and authorization-server metadata. No client secret is required. Access tokens last one hour. Requested offline access issues rotating refresh tokens with an absolute 30-day lifetime; reconnect after that. Codes expire in two minutes and can be used once. Reusing an old refresh token revokes the connection family. OAuth state and token hashes persist in JSON or PostgreSQL, including through manager restarts and HA failover.

Connections appear as **MCP: client-name** in Integration API tokens. Revoke one there to stop access and refresh immediately. Existing accepted cluster operations may finish; revocation does not undo a deployment.

## Local coding assistants: stdio

Build the CLI with `make cli`. In the console, create an **MCP · Manage cluster** token, or an **MCP · Read cluster** token. Save it in a private file, then save a CLI profile:

```sh
chmod 600 /secure/zela-mcp-token
./bin/zela login --profile production \
  --url https://cluster.example.com \
  --token-file /secure/zela-mcp-token

./bin/zela --profile production mcp serve
```

Login verifies the cluster discovery endpoint; `mcp serve` then checks MCP access and the credential’s read/management scope. Profiles retain verified TLS and the existing private-file checks. Use `--ca-cert /secure/cluster-ca.pem` when your private CA is not in the system trust store.

Configure a stdio client, such as Claude Desktop, with the **absolute** CLI path:

```json
{
  "mcpServers": {
    "zela-cluster": {
      "command": "/absolute/path/to/zela",
      "args": ["--profile", "production", "mcp", "serve"]
    }
  }
}
```

The process uses the saved profile. Alternatively, configure `ZELA_URL` and `ZELA_TOKEN_FILE` in the client's process environment. Do not put bearer tokens in command-line arguments. Stdin and stdout carry MCP protocol messages; `--token-file -` is rejected. Errors go to stderr.

For a local read-only connection, use a read token or add `--read-only` to the server arguments. A read token always exposes only the read tools, even when that flag is omitted. A management credential can still be used with other clients; the local flag restricts this process's tool catalog.

## Direct bearer clients and GPT API applications

The same endpoint accepts `Authorization: Bearer <mcp-token>`. This works for clients that support custom headers and for applications using remote MCP through a model API. OAuth is optional for these clients; it is enabled only when `ZELA_MCP_PUBLIC_URL` is set. Never put a token in the endpoint URL.

For an OpenAI Responses API application, pass the MCP token separately in `authorization`; use a supported model configured by your application:

```python
import os
from openai import OpenAI

response = OpenAI().responses.create(
    model=os.environ["OPENAI_MODEL"],
    input="Check my cluster health and show which workers can accept workloads.",
    tools=[{
        "type": "mcp",
        "server_label": "zela_cluster",
        "server_url": os.environ["Zela_MCP_URL"],
        "authorization": os.environ["Zela_MCP_TOKEN"],
        "allowed_tools": ["cluster_overview", "list_workers", "cluster_inventory"],
        "require_approval": "always",
    }],
)
```

The example keeps the application's approval flow enabled. Handle any returned approval requests before execution, as described in [OpenAI's remote MCP guide](https://developers.openai.com/api/docs/guides/tools-connectors-mcp). No OpenAI SDK dependency is required by the control plane or CLI.

## Tools and permissions

| Tools                                                               | Purpose                                                         | Permission |
| ------------------------------------------------------------------- | --------------------------------------------------------------- | ---------- |
| `cluster_overview`, `list_workers`                                  | Worker health, freshness, usage, reservations and capacity      | Read       |
| `cluster_inventory`                                                 | Deployments, containers, placement, alerts and recent activity  | Read       |
| `worker_metrics`                                                    | Host metrics history: 1h, 6h or 24h                             | Read       |
| `container_logs`                                                    | Recent logs for a container on its worker                       | Read       |
| `get_operation`                                                     | Poll accepted work until succeeded or failed                    | Read       |
| `list_notifications`                                                | Cluster notifications                                           | Read       |
| `refresh_inventory`                                                 | Refresh inventory through an operation                          | Management |
| `create_deployment`, `deployment_action`, `scale_deployment`        | Deploy, start, stop, restart, delete or scale                   | Management |
| `move_deployment`, `update_deployment_image`                        | Move a deployment or roll out an image                          | Management |
| `worker_action`                                                     | Pause, activate, drain or unregister                            | Management |
| `container_action`                                                  | Start, stop, restart or delete an unmanaged container           | Management |
| `list_ssh_keys`                                                     | Saved key metadata and fingerprints                             | Management |
| `provision_worker`, `retry_worker_provision`, `update_worker_agent` | Provision an existing host with a saved key; maintain its agent | Management |

**`mcp_read`** is cluster-wide inspection, including application logs. **`mcp_manage`** includes changes to workloads created by any integration and to workers; issue it only to people or clients intended to operate the entire cluster. These differ from the existing infrastructure/deployment integration tokens, which retain their original ownership boundaries and cannot connect to MCP.

MCP credentials cannot access the raw Docker gateway, full administrative state, token administration, private-key import/export/deletion or monitoring configuration. Inventory omits environment values, command arguments, container labels, worker credentials and SSH connection settings. Logs and operational messages can still contain application data; tool descriptions identify those as untrusted input. There is no arbitrary shell or raw API tool. Administrator bearer credentials also work, but use dedicated MCP credentials for ordinary connections.

All actions use existing API validation, resource admission, serialized cluster operations, persistence and HA fencing. Mutations return an operation ID: **accepted is not completed**. Poll `get_operation`, inspect any failure, and do not retry a mutation blindly after a timeout. Tool annotations describe reads and disruptive writes to the client; the platform does not add a second approval protocol. Your AI client's authorization/approval rules still apply.

Draining relocates managed stateless deployments and can fail when unmanaged containers remain. Moving and rolling out images recreate containers and do not migrate persistent volumes or filesystem changes. Provisioning installs Docker and an agent on an existing SSH host; it does not create cloud VMs. Cloud provider provisioning, shell access and backups remain outside these tools.

## Security considerations

- **`mcp_manage` is powerful.** It can stop or delete any workload and drain or unregister workers. Issue it only to clients that may operate the whole cluster, and keep client-side approval enabled for write tools.
- **Provisioning is restricted to an allowlist.** A newly provisioned host receives workloads, including their environment values. If an MCP credential could attach any host, it could read secrets that `cluster_inventory` hides. So `provision_worker` with an MCP credential is refused (`403`) unless the SSH host matches `ZELA_MCP_PROVISION_HOSTS`: IPs, CIDRs or exact hostnames, comma-separated. When that variable is empty, only administrators can provision. Allow only networks you control. A hostname entry trusts whoever controls its DNS. Retrying and updating already-registered workers are not affected.
- **Treat tool output as hostile.** Container logs, image names, operation details and alert messages come from workloads and can contain prompt-injection text. Assistants holding `mcp_manage` should run with per-call approval (`require_approval: "always"`, or your client's equivalent).
- **Only approve consent requests you started.** OAuth dynamic registration is open: anyone who can reach the endpoint can register a client with any display name and an allowlisted callback, then send an administrator the authorization link. If the administrator approves, the requester's ChatGPT or Claude account gets the access. The consent page warns that the client name is unverified, and shows the callback and when the client registered. A client registered seconds before _you_ clicked Connect is expected; anything else is suspicious.
- **Reduce exposure.** Leave `ZELA_MCP_PUBLIC_URL` unset unless you need browser OAuth (bearer MCP tokens work without it). Restrict `ZELA_MCP_REDIRECT_URIS` to the exact callbacks you use, and review **MCP: …** entries in Integration API tokens regularly.
- **Transport.** Serve `/mcp` over HTTPS only. Browser cookies never authorize MCP calls, and cross-origin browser requests are rejected.

## Limits

| Limit                           | Value                         | Notes                                                                                                                                                                                                                                                   |
| ------------------------------- | ----------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Registered OAuth clients        | 512                           | Clients that never completed authorization are removed after 24 hours, and clients idle for 31 days (longer than the refresh lifetime) are removed too. At the cap, registration returns `503 client registration unavailable` until pruning frees room |
| Pending authorization codes     | 1,024                         | Codes expire after 2 minutes                                                                                                                                                                                                                            |
| OAuth rate limit                | 20 requests/min per client IP | Behind a reverse proxy, set `ZELA_MCP_TRUSTED_PROXIES` to the proxy addresses. The nearest untrusted `X-Forwarded-For` hop is then used. `X-Forwarded-For` from any other address is ignored, so it can't be spoofed                                    |
| Request body                    | 1 MiB                         |                                                                                                                                                                                                                                                         |
| Tool response                   | 32 MiB                        |                                                                                                                                                                                                                                                         |
| Access token / refresh lifetime | 1 h / 30 days                 | Each authorization adds an **MCP: client** token entry. An entry is removed 24 hours after it expires or is revoked, once no refresh grant remains                                                                                                      |

## Verification

`make test check` runs the complete Go race suite, console tests and static checks. MCP tests use the official SDK over Streamable HTTP and stdio, exercise the older `2025-11-25` handshake and current SDK negotiation, perform real controller operations against simulated workers, and test credential boundaries, input validation, OAuth consent, PKCE, one-time codes, refresh rotation/replay, expiry, revocation and persisted state. They do not log into a real ChatGPT or Claude account or mutate a live cluster.
