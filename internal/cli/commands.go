package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zela-dev/zela/internal/platformclient"
)

func segment(s string) string              { return url.PathEscape(s) }
func filepathAbs(s string) (string, error) { return filepath.Abs(s) }
func (r *Runner) command(ctx context.Context, c *platformclient.Client, args []string) (result, error) {
	group := args[0]
	verb := ""
	tail := args[1:]
	if len(tail) > 0 && !strings.HasPrefix(tail[0], "-") {
		verb = tail[0]
		tail = tail[1:]
	}
	if group == "status" || group == "state" || group == "health" || group == "ready" || group == "refresh" {
		if verb != "" {
			return result{}, fmt.Errorf("%s takes no arguments", group)
		}
	} else if verb == "" {
		return result{}, fmt.Errorf("choose a %s subcommand; see zela help", group)
	}
	fs := r.flags("zela " + group + " " + verb)
	var networkEndpoint string
	var name, endpoint, region, workerTokenFile, file, keyFile, passFile, host, user, secret, hostKeyFile, window, scope, output, target, image, worker string
	var sshPort, agentPort, days, replicas int
	var sudo, all bool
	switch group + " " + verb {
	case "workers add":
		fs.StringVar(&name, "name", "", "Worker name")
		fs.StringVar(&endpoint, "endpoint", "", "Worker HTTPS endpoint")
		fs.StringVar(&region, "region", "", "Worker region")
		fs.StringVar(&workerTokenFile, "worker-token-file", "", "Private worker token file, or - for stdin")
		fs.StringVar(&networkEndpoint, "network-endpoint", "", "Address peers use for the cluster network (default: endpoint host)")
	case "workers provision":
		fs.StringVar(&file, "file", "", "Provision request JSON file, or -")
		fs.StringVar(&name, "name", "", "Worker name")
		fs.StringVar(&region, "region", "", "Worker region")
		fs.StringVar(&host, "host", "", "SSH hostname/IP")
		fs.StringVar(&user, "user", "root", "SSH user")
		fs.IntVar(&sshPort, "ssh-port", 22, "SSH port")
		fs.IntVar(&agentPort, "agent-port", 9091, "Worker agent port")
		fs.StringVar(&secret, "secret", "", "Vault secret ID")
		fs.StringVar(&hostKeyFile, "host-key-file", "", "Trusted SSH host public key file")
		fs.BoolVar(&sudo, "sudo", false, "Use passwordless sudo")
		fs.StringVar(&networkEndpoint, "network-endpoint", "", "Address peers use for the cluster network (default: SSH host)")
	case "workers metrics":
		fs.StringVar(&window, "window", "1h", "History window: 1h, 6h, 24h")
	case "secrets create":
		fs.StringVar(&name, "name", "", "Secret name")
		fs.StringVar(&keyFile, "key-file", "", "Private SSH key file, or -")
		fs.StringVar(&passFile, "passphrase-file", "", "Private passphrase file")
	case "tokens create":
		fs.StringVar(&name, "name", "", "Token name")
		fs.StringVar(&scope, "scope", "infrastructure", "infrastructure, deployments, mcp_read or mcp_manage")
		fs.IntVar(&days, "expires-days", 30, "Expiry in days (0 means no expiry)")
		fs.StringVar(&output, "out", "", "Write token to a new private file instead of stdout")
	case "notifications read":
		fs.BoolVar(&all, "all", false, "Mark all notifications read")
	case "monitoring set-rules", "deployments create", "api GET", "api POST", "api PUT", "api PATCH", "api DELETE", "api HEAD":
		fs.StringVar(&file, "file", "", "JSON request file, or -")
	case "storage register-backend", "storage register-volume":
		fs.StringVar(&file, "file", "", "JSON request file, or -")
	case "deployments move", "deployments move-replica":
		fs.StringVar(&target, "target", "", "Target worker ID")
	case "deployments scale":
		fs.IntVar(&replicas, "replicas", 0, "Desired replica count")
	case "deployments update-image":
		fs.StringVar(&image, "image", "", "Container image")
		fs.StringVar(&target, "target", "", "Target worker ID (optional)")
	case "containers list":
		fs.StringVar(&worker, "worker", "", "Filter inventory to a worker ID")
	}
	if err := parseFlags(fs, tail); err != nil {
		return result{}, err
	}
	if c == nil {
		return result{}, errNoClient
	}
	pos := fs.Args()
	count := func(n int) error {
		if len(pos) != n {
			return fmt.Errorf("%s %s requires %d positional argument(s); see zela help", group, verb, n)
		}
		return nil
	}
	call := func(method, path string, body any) (result, error) {
		var b json.RawMessage
		var err error
		if raw, ok := body.(json.RawMessage); ok {
			b = raw
		} else if body != nil {
			b, err = json.Marshal(body)
			if err != nil {
				return result{}, err
			}
		}
		data, err := c.Do(ctx, method, path, b)
		return result{data: data}, err
	}
	post := func(path string, body any) (result, error) {
		res, err := call("POST", path, body)
		if err == nil {
			var op struct{ ID, Status string }
			if json.Unmarshal(res.data, &op) == nil && op.ID != "" && op.Status != "" {
				res.operation = true
			}
		}
		return res, err
	}
	stateField := func(field string) (result, error) {
		res, err := call("GET", "/api/state", nil)
		if err != nil {
			return res, err
		}
		var s map[string]json.RawMessage
		if json.Unmarshal(res.data, &s) != nil {
			return res, fmt.Errorf("invalid state response")
		}
		res.data = s[field]
		if len(res.data) == 0 {
			res.data = json.RawMessage(`[]`)
		}
		return res, nil
	}
	table := func(res result, err error, columns ...string) (result, error) {
		if err != nil {
			return res, err
		}
		rows, err := objectRows(res.data)
		res.columns = columns
		res.rows = rows
		return res, err
	}
	fileBody := func() (json.RawMessage, error) {
		if file == "" {
			return nil, fmt.Errorf("--file is required")
		}
		b, err := readInputFile(file, r.In, false)
		if err != nil {
			return nil, err
		}
		if !json.Valid(b) {
			return nil, fmt.Errorf("input must contain one valid JSON value")
		}
		return b, nil
	}
	switch group {
	case "status", "state", "health", "ready", "refresh":
		if err := count(0); err != nil {
			return result{}, err
		}
		path := map[string]string{"status": "/api/cluster", "state": "/api/state", "health": "/api/health", "ready": "/api/ready", "refresh": "/api/refresh"}[group]
		if group == "refresh" {
			return post(path, nil)
		}
		return call("GET", path, nil)
	case "workers":
		switch verb {
		case "list":
			if err := count(0); err != nil {
				return result{}, err
			}
			res, err := call("GET", "/api/cluster/workers", nil)
			if err != nil {
				return res, err
			}
			var v struct{ Workers json.RawMessage }
			if json.Unmarshal(res.data, &v) != nil {
				return res, fmt.Errorf("invalid workers response")
			}
			res.data = v.Workers
			return table(res, nil, "id", "name", "status", "mode", "region", "acceptingContainers")
		case "get":
			if err := count(1); err != nil {
				return result{}, err
			}
			return call("GET", "/api/cluster/workers/"+segment(pos[0]), nil)
		case "add":
			if err := count(0); err != nil {
				return result{}, err
			}
			if name == "" || endpoint == "" {
				return result{}, fmt.Errorf("--name and --endpoint are required")
			}
			token := ""
			var err error
			if workerTokenFile != "" {
				token, err = readSecret(workerTokenFile, r.In)
				if err != nil {
					return result{}, err
				}
			}
			return post("/api/workers", map[string]any{"name": name, "endpoint": endpoint, "token": token, "region": region, "networkEndpoint": networkEndpoint})
		case "provision":
			if err := count(0); err != nil {
				return result{}, err
			}
			if file != "" {
				for _, key := range []string{"name", "region", "host", "user", "ssh-port", "agent-port", "secret", "host-key-file", "sudo", "network-endpoint"} {
					found := false
					fs.Visit(func(f *flag.Flag) {
						if f.Name == key {
							found = true
						}
					})
					if found {
						return result{}, fmt.Errorf("--file cannot be combined with provisioning flags")
					}
				}
				b, err := fileBody()
				if err != nil {
					return result{}, err
				}
				return post("/api/workers/provision", b)
			}
			if name == "" || host == "" || secret == "" || hostKeyFile == "" {
				return result{}, fmt.Errorf("--name, --host, --secret and --host-key-file are required")
			}
			b, err := readInputFile(hostKeyFile, r.In, false)
			if err != nil {
				return result{}, err
			}
			return post("/api/workers/provision", map[string]any{"name": name, "region": region, "networkEndpoint": networkEndpoint, "ssh": map[string]any{"host": host, "port": sshPort, "user": user, "secretID": secret, "hostKey": strings.TrimSpace(string(b)), "sudo": sudo, "agentPort": agentPort}})
		case "metrics":
			if err := count(1); err != nil {
				return result{}, err
			}
			if window != "1h" && window != "6h" && window != "24h" {
				return result{}, fmt.Errorf("--window must be 1h, 6h or 24h")
			}
			return call("GET", "/api/workers/"+segment(pos[0])+"/metrics/history?window="+url.QueryEscape(window), nil)
		case "retry", "update-agent":
			if err := count(1); err != nil {
				return result{}, err
			}
			suffix := "provision-retry"
			if verb == "update-agent" {
				suffix = "agent-update"
			}
			return post("/api/workers/"+segment(pos[0])+"/"+suffix, nil)
		case "pause", "activate", "drain", "remove":
			if err := count(1); err != nil {
				return result{}, err
			}
			action := verb
			if action == "remove" {
				action = "delete"
			}
			return post("/api/workers/"+segment(pos[0])+"/actions", map[string]string{"action": action})
		}
	case "secrets":
		switch verb {
		case "list":
			if err := count(0); err != nil {
				return result{}, err
			}
			res, err := stateField("secrets")
			return table(res, err, "id", "name", "kind", "fingerprint")
		case "create":
			if err := count(0); err != nil {
				return result{}, err
			}
			if name == "" || keyFile == "" {
				return result{}, fmt.Errorf("--name and --key-file are required")
			}
			if keyFile == "-" && passFile == "-" {
				return result{}, fmt.Errorf("only one secret may be read from stdin")
			}
			b, err := readInputFile(keyFile, r.In, true)
			if err != nil {
				return result{}, err
			}
			pass := ""
			if passFile != "" {
				pass, err = readSecret(passFile, r.In)
				if err != nil {
					return result{}, err
				}
			}
			return post("/api/secrets", map[string]string{"name": name, "privateKey": string(b), "passphrase": pass})
		case "remove":
			if err := count(1); err != nil {
				return result{}, err
			}
			return post("/api/secrets/"+segment(pos[0])+"/delete", nil)
		}
	case "tokens":
		switch verb {
		case "list":
			if err := count(0); err != nil {
				return result{}, err
			}
			res, err := call("GET", "/api/tokens", nil)
			return table(res, err, "id", "name", "scope", "expiresAt", "revokedAt")
		case "revoke":
			if err := count(1); err != nil {
				return result{}, err
			}
			return post("/api/tokens/"+segment(pos[0])+"/revoke", nil)
		case "create":
			if err := count(0); err != nil {
				return result{}, err
			}
			if name == "" {
				return result{}, fmt.Errorf("--name is required")
			}
			if scope != "infrastructure" && scope != "deployments" {
				return result{}, fmt.Errorf("--scope must be infrastructure or deployments")
			}
			if days < 0 || days > 365 {
				return result{}, fmt.Errorf("--expires-days must be 0–365")
			}
			var f *os.File
			if output != "" {
				var err error
				f, err = os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil {
					return result{}, fmt.Errorf("create token output file: %w", err)
				}
				defer f.Close()
			}
			res, err := post("/api/tokens", map[string]any{"name": name, "scope": scope, "expiresInDays": days})
			if err != nil {
				if f != nil {
					os.Remove(output)
				}
				return res, err
			}
			if f != nil {
				var v struct {
					Token    string          `json:"token"`
					Metadata json.RawMessage `json:"metadata"`
				}
				if err := json.Unmarshal(res.data, &v); err != nil || v.Token == "" {
					os.Remove(output)
					return result{}, fmt.Errorf("invalid token creation response")
				}
				if _, err := f.WriteString(v.Token + "\n"); err != nil {
					return result{}, fmt.Errorf("token created but saving failed; inspect token metadata and revoke it: %w", err)
				}
				if err := f.Sync(); err != nil {
					return result{}, fmt.Errorf("token created but syncing failed: %w", err)
				}
				res.data, _ = json.Marshal(map[string]any{"metadata": v.Metadata, "savedTo": output})
			}
			return res, nil
		}
	case "monitoring":
		switch verb {
		case "rules":
			if err := count(0); err != nil {
				return result{}, err
			}
			return stateField("monitoringRules")
		case "set-rules":
			if err := count(0); err != nil {
				return result{}, err
			}
			b, err := fileBody()
			if err != nil {
				return result{}, err
			}
			return post("/api/monitoring/rules", b)
		}
	case "alerts":
		switch verb {
		case "list":
			if err := count(0); err != nil {
				return result{}, err
			}
			res, err := stateField("alerts")
			return table(res, err, "id", "severity", "active", "acknowledged", "workerID", "message")
		case "acknowledge":
			if err := count(1); err != nil {
				return result{}, err
			}
			return post("/api/alerts/"+segment(pos[0])+"/acknowledge", nil)
		}
	case "notifications":
		switch verb {
		case "list":
			if err := count(0); err != nil {
				return result{}, err
			}
			res, err := call("GET", "/api/notifications", nil)
			if err != nil {
				return res, err
			}
			var v struct{ Notifications json.RawMessage }
			if json.Unmarshal(res.data, &v) != nil {
				return res, fmt.Errorf("invalid notifications response")
			}
			res.columns = []string{"id", "severity", "title", "message", "readAt"}
			res.rows, err = objectRows(v.Notifications)
			return res, err
		case "read":
			if !all && len(pos) == 0 {
				return result{}, fmt.Errorf("provide notification IDs or --all")
			}
			if all && len(pos) > 0 {
				return result{}, fmt.Errorf("--all cannot be combined with notification IDs")
			}
			ids := pos
			var res result
			if all {
				var err error
				res, err = call("GET", "/api/notifications", nil)
				if err != nil {
					return res, err
				}
				var v struct {
					Notifications []struct {
						ID     string
						ReadAt *string
					}
				}
				if json.Unmarshal(res.data, &v) != nil {
					return res, fmt.Errorf("invalid notifications response")
				}
				for _, n := range v.Notifications {
					if n.ReadAt == nil {
						ids = append(ids, n.ID)
					}
				}
			}
			if len(ids) == 0 {
				return result{data: json.RawMessage(`{"ok":true,"read":0}`)}, nil
			}
			for len(ids) > 0 {
				n := min(len(ids), 500)
				var err error
				res, err = post("/api/notifications/read", map[string]any{"ids": ids[:n]})
				if err != nil {
					return res, err
				}
				ids = ids[n:]
			}
			return res, nil
		}
	case "operations":
		switch verb {
		case "list":
			if err := count(0); err != nil {
				return result{}, err
			}
			res, err := stateField("operations")
			return table(res, err, "id", "status", "title", "detail")
		case "get", "wait":
			if err := count(1); err != nil {
				return result{}, err
			}
			res, err := call("GET", "/api/operations/"+segment(pos[0]), nil)
			res.operation = verb == "wait"
			return res, err
		}
	case "containers":
		switch verb {
		case "list":
			if err := count(0); err != nil {
				return result{}, err
			}
			res, err := stateField("workers")
			if err != nil {
				return res, err
			}
			var workers map[string]struct {
				Name       string
				Containers []map[string]any
			}
			if json.Unmarshal(res.data, &workers) != nil {
				return res, fmt.Errorf("invalid container inventory")
			}
			if worker != "" {
				if _, ok := workers[worker]; !ok {
					return result{}, fmt.Errorf("worker not found")
				}
			}
			rows := []map[string]any{}
			for id, w := range workers {
				if worker != "" && worker != id {
					continue
				}
				for _, ct := range w.Containers {
					ct["workerID"] = id
					ct["workerName"] = w.Name
					rows = append(rows, ct)
				}
			}
			res.data, _ = json.Marshal(rows)
			res, err = table(res, nil, "workerID", "Id", "Names", "Image", "State")
			// objectRows sorts by lowercase "id"; containers use Docker's "Id".
			sort.SliceStable(res.rows, func(i, j int) bool {
				a, b := res.rows[i], res.rows[j]
				if x, y := fmt.Sprint(a["workerID"]), fmt.Sprint(b["workerID"]); x != y {
					return x < y
				}
				return fmt.Sprint(a["Id"]) < fmt.Sprint(b["Id"])
			})
			res.data, _ = json.Marshal(res.rows)
			return res, err
		case "logs", "start", "stop", "restart", "remove":
			if err := count(2); err != nil {
				return result{}, err
			}
			path := "/api/workers/" + segment(pos[0]) + "/containers/" + segment(pos[1])
			if verb == "logs" {
				res, err := call("GET", path+"/logs", nil)
				if err == nil {
					var v struct{ Logs string }
					if json.Unmarshal(res.data, &v) != nil {
						return res, fmt.Errorf("invalid logs response")
					}
					res.rawText = v.Logs
				}
				return res, err
			}
			action := verb
			if action == "remove" {
				action = "delete"
			}
			return post(path+"/actions", map[string]string{"action": action})
		}
	case "storage":
		switch verb {
		case "list":
			if err := count(0); err != nil {
				return result{}, err
			}
			return call("GET", "/api/storage", nil)
		case "register-backend":
			if err := count(0); err != nil {
				return result{}, err
			}
			body, err := fileBody()
			if err != nil {
				return result{}, err
			}
			return call("POST", "/api/storage/backends", body)
		case "register-volume":
			if err := count(0); err != nil {
				return result{}, err
			}
			body, err := fileBody()
			if err != nil {
				return result{}, err
			}
			return call("POST", "/api/storage/volumes", body)
		case "remove-backend", "remove-volume":
			if err := count(1); err != nil {
				return result{}, err
			}
			collection := "backends"
			if verb == "remove-volume" {
				collection = "volumes"
			}
			return call("DELETE", "/api/storage/"+collection+"/"+segment(pos[0]), nil)
		default:
			return result{}, fmt.Errorf("unknown storage command %q", verb)
		}
	case "deployments":
		switch verb {
		case "list":
			if err := count(0); err != nil {
				return result{}, err
			}
			res, err := call("GET", "/api/deployments", nil)
			return table(res, err, "id", "name", "image", "replicas", "status")
		case "get":
			if err := count(1); err != nil {
				return result{}, err
			}
			return call("GET", "/api/deployments/"+segment(pos[0]), nil)
		case "create":
			if err := count(0); err != nil {
				return result{}, err
			}
			b, err := fileBody()
			if err != nil {
				return result{}, err
			}
			return post("/api/deployments", b)
		case "start", "stop", "restart", "remove", "scale", "move":
			if err := count(1); err != nil {
				return result{}, err
			}
			if verb == "move" && target == "" {
				return result{}, fmt.Errorf("--target is required")
			}
			if verb == "scale" && replicas <= 0 {
				return result{}, fmt.Errorf("--replicas must be positive")
			}
			action := verb
			if action == "remove" {
				action = "delete"
			}
			body := map[string]any{"action": action}
			if verb == "move" {
				body["target"] = target
			}
			if verb == "scale" {
				body["replicas"] = replicas
			}
			return post("/api/deployments/"+segment(pos[0])+"/actions", body)
		case "move-replica":
			if err := count(2); err != nil {
				return result{}, err
			}
			if target == "" {
				return result{}, fmt.Errorf("--target is required")
			}
			return post("/api/deployments/"+segment(pos[0])+"/replicas/"+segment(pos[1])+"/move", map[string]string{"target": target})
		case "update-image":
			if err := count(1); err != nil {
				return result{}, err
			}
			if image == "" {
				return result{}, fmt.Errorf("--image is required")
			}
			return post("/api/deployments/"+segment(pos[0])+"/image", map[string]string{"image": image, "target": target})
		}
	case "api":
		if verb != "GET" && verb != "POST" && verb != "PUT" && verb != "PATCH" && verb != "DELETE" && verb != "HEAD" {
			return result{}, fmt.Errorf("unsupported API method")
		}
		if err := count(1); err != nil {
			return result{}, err
		}
		var b json.RawMessage
		var err error
		if file != "" {
			b, err = fileBody()
			if err != nil {
				return result{}, err
			}
		}
		if (verb == "GET" || verb == "HEAD") && file != "" {
			return result{}, fmt.Errorf("%s cannot have --file", verb)
		}
		if verb == "POST" {
			return post(pos[0], b)
		}
		return call(verb, pos[0], b)
	}
	return result{}, fmt.Errorf("unknown command %s %s; see zela help", group, verb)
}
func objectRows(b json.RawMessage) ([]map[string]any, error) {
	var v any
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return nil, fmt.Errorf("invalid list response")
	}
	rows := []map[string]any{}
	switch v := v.(type) {
	case nil:
	case []any:
		for _, x := range v {
			m, ok := x.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid list item")
			}
			rows = append(rows, m)
		}
	case map[string]any:
		for _, x := range v {
			m, ok := x.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid list item")
			}
			rows = append(rows, m)
		}
	default:
		return nil, fmt.Errorf("expected a list response")
	}
	sort.SliceStable(rows, func(i, j int) bool { return fmt.Sprint(rows[i]["id"]) < fmt.Sprint(rows[j]["id"]) })
	return rows, nil
}
