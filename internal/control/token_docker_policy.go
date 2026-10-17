package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
)

// Canonical keys and a reconstructed body prevent case-insensitive Docker JSON
// decoding from accepting a second, unsafe HostConfig under a different spelling.
func tokenJSON(r *http.Request, limit int64) (map[string]json.RawMessage, error) {
	if r.Body == nil {
		return nil, fmt.Errorf("expected a JSON object")
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, fmt.Errorf("request body too large or unreadable")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("expected a JSON object")
	}
	out := map[string]json.RawMessage{}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key := k.(string)
		if _, ok := out[key]; ok {
			return nil, fmt.Errorf("duplicate JSON field %s", key)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("expected one JSON object")
	}
	return out, nil
}
func emptyJSON(v json.RawMessage) bool {
	var x any
	if json.Unmarshal(v, &x) != nil {
		return false
	}
	switch n := x.(type) {
	case nil:
		return true
	case bool:
		return !n
	case string:
		return n == ""
	case float64:
		return n == 0
	case []any:
		return len(n) == 0
	case map[string]any:
		return len(n) == 0
	}
	return false
}
func permittedFields(config map[string]json.RawMessage, allowed string) error {
	for key, v := range config {
		if slices.Contains(strings.Fields(allowed), key) {
			continue
		}
		// Allow SDK defaults for canonical, known unsafe options only. Unknown or
		// differently cased keys are rejected even when their current value is empty.
		if slices.Contains(strings.Fields("Privileged Binds Mounts VolumesFrom Devices DeviceRequests DeviceCgroupRules CapAdd SecurityOpt NetworkMode PidMode IpcMode UTSMode UsernsMode CgroupnsMode CgroupParent ContainerIDFile Runtime GroupAdd Links LxcConf OomKillDisable OomScoreAdj Sysctls StorageOpt Isolation VolumeDriver MaskedPaths ReadonlyPaths KernelMemory KernelMemoryTCP CpuCount CpuPercent BlkioWeight BlkioWeightDevice BlkioDeviceReadBps BlkioDeviceWriteBps BlkioDeviceReadIOps BlkioDeviceWriteIOps CpuRealtimePeriod CpuRealtimeRuntime Annotations NetworkingConfig"), key) && emptyJSON(v) {
			delete(config, key)
			continue
		}
		return fmt.Errorf("deployment tokens cannot set %s", key)
	}
	return nil
}
func replaceTokenBody(r *http.Request, config map[string]json.RawMessage) error {
	b, err := json.Marshal(config)
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	r.ContentLength = int64(len(b))
	r.Header.Del("Content-Length")
	return nil
}
func safeTokenCreate(r *http.Request) error {
	config, err := tokenJSON(r, 16<<20)
	if err != nil {
		return err
	}
	if err := permittedFields(config, "Image Cmd Entrypoint Env Labels Hostname Domainname User WorkingDir ExposedPorts Healthcheck StopSignal StopTimeout AttachStdin AttachStdout AttachStderr Tty OpenStdin StdinOnce Volumes HostConfig"); err != nil {
		return err
	}
	if raw, ok := config["HostConfig"]; ok && string(raw) != "null" {
		// Use the same strict duplicate-key parser for the nested security boundary.
		nested := r.Clone(r.Context())
		nested.Body = io.NopCloser(bytes.NewReader(raw))
		host, err := tokenJSON(nested, 16<<20)
		if err != nil {
			return err
		}
		if err := permittedFields(host, "Memory NanoCpus MemorySwap MemoryReservation CpuShares CpuPeriod CpuQuota CpusetCpus CpusetMems PortBindings PublishAllPorts RestartPolicy AutoRemove Dns DnsSearch DnsOptions ExtraHosts Init ReadonlyRootfs ShmSize CapDrop Ulimits Tmpfs NetworkMode"); err != nil {
			return err
		}
		if raw, ok := host["NetworkMode"]; ok {
			var mode string
			if json.Unmarshal(raw, &mode) != nil {
				return fmt.Errorf("invalid NetworkMode")
			}
			switch mode {
			case "", "default", "bridge", "none":
			default:
				return fmt.Errorf("deployment tokens cannot use host, shared or custom networking")
			}
		}
		config["HostConfig"], err = json.Marshal(host)
		if err != nil {
			return err
		}
	}
	return replaceTokenBody(r, config)
}
func safeTokenExec(r *http.Request, start bool) error {
	config, err := tokenJSON(r, 1<<20)
	if err != nil {
		return err
	}
	allowed := "AttachStdin AttachStdout AttachStderr DetachKeys Tty Env Cmd User WorkingDir ConsoleSize"
	if start {
		allowed = "Detach Tty ConsoleSize"
	}
	if err := permittedFields(config, allowed); err != nil {
		return err
	}
	return replaceTokenBody(r, config)
}
func (c *Controller) deploymentDockerAccess(r *http.Request, ep string) error {
	p := requestPrincipal(r)
	if p.Admin {
		return nil
	}
	denied := fmt.Errorf("deployment token cannot access this Docker API; only its own application workloads are permitted")
	switch {
	case (ep == "/_ping" || ep == "/version" || ep == "/info") && (r.Method == "GET" || r.Method == "HEAD"):
		return nil
	case (ep == "/containers/json" || ep == "/images/json") && r.Method == "GET":
		return nil
	case ep == "/images/create" && r.Method == "POST":
		if r.URL.Query().Get("fromImage") == "" || r.URL.Query().Has("fromSrc") {
			return denied
		}
		return nil
	case strings.HasPrefix(ep, "/images/") && strings.HasSuffix(ep, "/json") && r.Method == "GET":
		return nil
	case ep == "/containers/create" && r.Method == "POST":
		return safeTokenCreate(r)
	case strings.HasPrefix(ep, "/containers/"):
		_, action, _ := strings.Cut(strings.TrimPrefix(ep, "/containers/"), "/")
		switch r.Method {
		case "GET":
			switch action {
			case "json", "logs", "stats", "top", "changes", "archive", "export", "attach/ws":
				return nil
			}
		case "HEAD":
			if action == "archive" {
				return nil
			}
		case "DELETE":
			if action == "" {
				return nil
			}
		case "PUT":
			if action == "archive" {
				return nil
			}
		case "POST":
			if action == "exec" {
				return safeTokenExec(r, false)
			}
			switch action {
			case "start", "stop", "restart", "kill", "wait", "rename", "resize", "pause", "unpause":
				if r.Body != nil {
					body, err := io.ReadAll(io.LimitReader(r.Body, 1025))
					if err != nil || len(bytes.TrimSpace(body)) > 0 {
						return fmt.Errorf("this deployment action must have an empty request body")
					}
					r.Body = io.NopCloser(bytes.NewReader(nil))
					r.ContentLength = 0
				}
				return nil
			case "attach":
				return nil
			}
		}
	case strings.HasPrefix(ep, "/exec/"):
		id, action, _ := strings.Cut(strings.TrimPrefix(ep, "/exec/"), "/")
		record, ok := c.Store.snapshot().DockerExecs[id]
		if !ok || record.AccessTokenID != p.TokenID {
			return denied
		}
		if action == "json" && r.Method == "GET" {
			return nil
		}
		if action == "start" && r.Method == "POST" {
			return safeTokenExec(r, true)
		}
		if action == "resize" && r.Method == "POST" {
			return nil
		}
	}
	return denied
}
