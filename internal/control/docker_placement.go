package control

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zela-dev/zela/internal/engine"
)

var dockerContainerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
var dockerVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

func dockerPlacementBody(body []byte, name string) ([]byte, DockerContainer, string, error) {
	var config map[string]json.RawMessage
	if json.Unmarshal(body, &config) != nil || config == nil {
		return nil, DockerContainer{}, "", routeError(400, "invalid container creation JSON")
	}
	var image string
	if json.Unmarshal(config["Image"], &image) != nil || image == "" || strings.ContainsAny(image, " \t\r\n\x00\\") {
		return nil, DockerContainer{}, "", routeError(400, "a valid Image is required")
	}
	var host map[string]json.RawMessage
	if raw := config["HostConfig"]; len(raw) > 0 && string(raw) != "null" {
		if json.Unmarshal(raw, &host) != nil {
			return nil, DockerContainer{}, "", routeError(400, "invalid HostConfig")
		}
	}
	if host == nil {
		host = map[string]json.RawMessage{}
	}
	var limits struct {
		Memory       int64
		NanoCPUs     int64 `json:"NanoCpus"`
		CPUQuota     int64 `json:"CpuQuota"`
		CPUPeriod    int64 `json:"CpuPeriod"`
		NetworkMode  string
		Binds        []string
		Mounts       []json.RawMessage
		VolumesFrom  []string
		Devices      []json.RawMessage
		PortBindings map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string
		}
	}
	raw, _ := json.Marshal(host)
	if json.Unmarshal(raw, &limits) != nil {
		return nil, DockerContainer{}, "", routeError(400, "invalid Docker resource limits or bindings")
	}
	if limits.Memory < 0 || limits.NanoCPUs < 0 || limits.CPUQuota < -1 || limits.CPUPeriod < 0 {
		return nil, DockerContainer{}, "", routeError(400, "invalid Docker resource limits")
	}
	// Stateful host dependencies need explicit affinity. Never silently create
	// an empty named volume or choose a host with different bind-mounted data.
	if len(limits.Binds)+len(limits.Mounts)+len(limits.VolumesFrom)+len(limits.Devices) > 0 {
		return nil, DockerContainer{}, "", routeError(501, "automatic placement requires a stateless container; host mounts, named volumes, volumes-from and devices require explicit worker affinity")
	}
	if limits.NetworkMode != "" && limits.NetworkMode != "default" && limits.NetworkMode != "bridge" && limits.NetworkMode != "host" && limits.NetworkMode != "none" {
		return nil, DockerContainer{}, "", routeError(501, "custom networks require explicit worker affinity")
	}
	if network := config["NetworkingConfig"]; len(network) > 0 && string(network) != "null" {
		var n struct{ EndpointsConfig map[string]json.RawMessage }
		if json.Unmarshal(network, &n) != nil {
			return nil, DockerContainer{}, "", routeError(400, "invalid NetworkingConfig")
		}
		for name := range n.EndpointsConfig {
			if name != "bridge" && name != "host" && name != "none" {
				return nil, DockerContainer{}, "", routeError(501, "custom networks require explicit worker affinity")
			}
		}
	}
	if limits.Memory == 0 {
		limits.Memory = 128 << 20
		host["Memory"] = json.RawMessage("134217728")
	}
	if limits.NanoCPUs == 0 && limits.CPUQuota <= 0 {
		limits.NanoCPUs = 250000000
		host["NanoCpus"] = json.RawMessage("250000000")
		delete(host, "CpuQuota")
	}
	if limits.NanoCPUs == 0 {
		period := limits.CPUPeriod
		if period == 0 {
			period = 100000
		}
		cpus := float64(limits.CPUQuota) / float64(period) * 1e9
		if cpus > math.MaxInt64 {
			return nil, DockerContainer{}, "", routeError(400, "CPU reservation is too large")
		}
		limits.NanoCPUs = int64(math.Ceil(cpus))
	}
	if limits.Memory < 6<<20 || limits.NanoCPUs < 1000000 || limits.Memory > 1<<50 || limits.NanoCPUs > 1<<50 {
		return nil, DockerContainer{}, "", routeError(400, "CPU or memory limit is outside the supported range")
	}
	ct := DockerContainer{Name: name, MemoryBytes: limits.Memory, NanoCPUs: limits.NanoCPUs}
	for key, bindings := range limits.PortBindings {
		port, proto, _ := strings.Cut(key, "/")
		p, err := strconv.Atoi(port)
		if proto == "" {
			proto = "tcp"
		}
		if err != nil || p < 1 || p > 65535 || (proto != "tcp" && proto != "udp" && proto != "sctp") {
			return nil, ct, "", routeError(400, "invalid container port binding")
		}
		for _, binding := range bindings {
			hostPort := 0
			if binding.HostPort != "" {
				hostPort, err = strconv.Atoi(binding.HostPort)
				if err != nil || hostPort < 0 || hostPort > 65535 {
					return nil, ct, "", routeError(400, "host port ranges require explicit worker affinity; use a single port for automatic placement")
				}
			}
			ct.Ports = append(ct.Ports, engine.Port{Host: hostPort, Container: p, Protocol: proto})
		}
	}
	config["HostConfig"], _ = json.Marshal(host)
	b, _ := json.Marshal(config)
	return b, ct, image, nil
}

func (c *Controller) dockerAutoCreate(w http.ResponseWriter, r *http.Request, path string) {
	// Share the managed deployment gate: selection and persisted reservations
	// form one critical section, preventing concurrent capacity overcommit.
	if !c.acquireOperationGate() {
		dockerError(w, 409, "another cluster placement is running; retry after it finishes")
		return
	}
	defer func() { <-c.gate }()
	name := strings.TrimPrefix(r.URL.Query().Get("name"), "/")
	if name == "" {
		name = newID("zela-")
	}
	if !dockerContainerName.MatchString(name) || len(name) > 255 {
		dockerError(w, 400, "invalid container name")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		dockerError(w, 413, "container creation JSON exceeds 16 MB or could not be read")
		return
	}
	body, ct, image, err := dockerPlacementBody(body, name)
	ct.AccessTokenID = requestPrincipal(r).TokenID
	if err != nil {
		clusterFailure(w, err)
		return
	}
	if err = c.dockerNameAvailable(r.Context(), name, "", r.Header.Get("X-Zela-Worker")); err != nil {
		clusterFailure(w, err)
		return
	}
	placement := Deployment{MemoryMB: (ct.MemoryBytes + (1 << 20) - 1) / (1 << 20), CPUs: float64(ct.NanoCPUs) / 1e9, Ports: ct.Ports}
	selected := r.Header.Get("X-Zela-Worker")
	if selected != "" {
		placement.WorkerIDs = []string{selected}
	}
	probe, stop := context.WithTimeout(r.Context(), 15*time.Second)
	worker, err := c.choose(probe, placement, "")
	stop()
	if err != nil {
		if selected != "" {
			dockerError(w, 503, "selected worker is unavailable or lacks unreserved CPU, memory or ports; no fallback was attempted")
			return
		}
		dockerError(w, 503, "no active online worker with sufficient CPU, memory and available ports")
		return
	}
	prefix := strings.TrimSuffix(path, "/containers/create")
	if err = c.dockerEnsureImage(r.Context(), worker, prefix, image, r.Header); err != nil {
		clusterFailure(w, err)
		return
	}
	// Image pulls can be slow. Recheck the same worker immediately before
	// creation; a pause, capacity change or connection failure never falls back.
	placement.WorkerIDs = []string{worker.ID}
	probe, stop = context.WithTimeout(r.Context(), 15*time.Second)
	worker, err = c.choose(probe, placement, "")
	stop()
	if err != nil {
		dockerError(w, 503, "chosen worker is no longer available or has insufficient capacity; no fallback was attempted")
		return
	}
	r = r.Clone(r.Context())
	u := *r.URL
	query := u.Query()
	query.Set("name", name)
	u.RawQuery = query.Encode()
	r.URL = &u
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header = r.Header.Clone()
	r.Header.Set("Content-Type", "application/json")
	c.dockerForward(w, r, worker.ID, path, r.URL.RawPath, c.dockerObserve(worker.ID, "", "", r, &ct))
}

func (c *Controller) dockerEnsureImage(ctx context.Context, worker Worker, prefix, image string, headers http.Header) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	resp, err := c.dockerRequest(ctx, worker, "GET", prefix+"/images/"+image+"/json", "", headers, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		return nil
	}
	if resp.StatusCode != 404 {
		return routeError(502, "selected worker could not inspect the requested image")
	}
	query := url.Values{"fromImage": {image}}
	resp, err = c.dockerRequest(ctx, worker, "POST", prefix+"/images/create", query.Encode(), headers, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return routeError(502, "selected worker could not pull the requested image; check image and registry authentication")
	}
	decoder := json.NewDecoder(resp.Body)
	for {
		var frame struct {
			Error       string `json:"error"`
			ErrorDetail struct{ Message string }
		}
		err = decoder.Decode(&frame)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return routeError(502, "worker image pull stream failed")
		}
		if frame.Error != "" || frame.ErrorDetail.Message != "" {
			return routeError(502, "worker image pull failed; check image and registry authentication")
		}
	}
}
