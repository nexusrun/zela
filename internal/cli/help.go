package cli

const help = `zela — remote Zela cluster administration

Usage: zela [global flags] COMMAND [flags]

Connections:
  login --url URL --token-file FILE [--profile NAME]  Verify and save a profile
  logout [--profile NAME]                             Remove the saved token
  config set NAME --url URL [--token-file FILE]        Save a profile offline
  config list | use NAME | delete NAME

Cluster:
  status | state | health | ready | refresh
  workers list | get ID
  workers add --name NAME --endpoint URL [--worker-token-file FILE] [--region REGION]
  workers provision --name NAME --host HOST --secret ID --host-key-file FILE
                    [--user root] [--sudo] [--ssh-port 22] [--agent-port 9091]
                    [--region REGION]   (or --file provision.json)
  workers pause | activate | drain | remove ID
  workers retry | update-agent ID
  workers metrics ID [--window 1h|6h|24h]

Administration:
  secrets list | remove ID
  secrets create --name NAME --key-file FILE [--passphrase-file FILE]
  tokens list | revoke ID
  tokens create --name NAME [--scope infrastructure|deployments|mcp_read|mcp_manage]
                [--expires-days 30] [--out NEW_PRIVATE_FILE]
  monitoring rules | set-rules --file FILE
  alerts list | acknowledge ID
  notifications list | read ID... | read --all
  operations list | get ID | wait ID

AI clients (MCP):
  mcp serve [--read-only]     MCP over stdio using this profile or ZELA_* credentials

Existing workload APIs:
  containers list [--worker ID]
  containers logs | start | stop | restart | remove WORKER_ID CONTAINER_ID
  deployments list | get ID | create --file FILE
  deployments start | stop | restart | remove ID
  deployments scale ID --replicas N | move ID --target WORKER_ID
  deployments move-replica ID REPLICA_ID --target WORKER_ID
  deployments update-image ID --image IMAGE [--target WORKER_ID]
  api GET|POST|PUT|PATCH|DELETE|HEAD /PATH [--file JSON_FILE]

Global flags (before or after the command):
  --url URL             Cluster HTTP(S) origin; use HTTPS for remote hosts
  --profile NAME        Saved connection profile
  --token-file FILE     Private token file, or - to read stdin (no token argv flag)
  --ca-cert FILE        Additional trusted CA PEM; TLS verification stays enabled
  --config FILE         Override private CLI configuration path
  --json                JSON output for scripts
  --timeout 30s         Per-request timeout
  --wait                Wait for an accepted operation and report its final status
  --wait-timeout 35m    Bound operation polling
  --help                Show global help; COMMAND SUBCOMMAND --help lists flags

Environment: ZELA_URL, ZELA_TOKEN, ZELA_TOKEN_FILE, ZELA_PROFILE, ZELA_CA_CERT,
             ZELA_CONFIG. Explicit flags override environment and profiles.
Files containing tokens, private keys or passphrases must have mode 0600.
Use - as a file name to read stdin. Tokens create prints the new token once
unless --out is supplied. Token/vault administration requires the admin token;
scoped credentials retain server-enforced permissions. MCP credentials expose
the supported AI tool catalog through mcp serve.
Worker pause prevents new placement; existing
containers keep running. Drain moves legacy managed deployments and can fail
when external containers remain. Remove unregisters a worker, without uninstalling.
No mutations are automatically retried. Inspect operation IDs before retrying.
Version: zela version
`
