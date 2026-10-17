package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zela-dev/zela/internal/engine"
	"github.com/zela-dev/zela/internal/sshclient"
	"github.com/zela-dev/zela/internal/telemetry"
)

func issueInfrastructureToken(t *testing.T, c *Controller) (string, AccessTokenMetadata) {
	t.Helper()
	raw, meta, err := c.Store.createAccessToken("Zela infrastructure", 90, "infrastructure")
	if err != nil {
		t.Fatal(err)
	}
	return raw, meta
}
func TestInfrastructureDiscoveryAccessAndRedaction(t *testing.T) {
	c, client, _, _ := clusterFixture(t)
	raw, _ := issueInfrastructureToken(t, c)
	now := time.Now().UTC()
	cpu := 12.5
	if err := c.Store.update(func(s *State) error {
		w := s.Workers["w1"]
		w.Endpoint = "https://private-worker.example:9091"
		w.Token = "private-worker-token"
		w.Error = "private-connection-error"
		w.MetricsError = ""
		w.DockerAPI = "private-docker-url"
		w.SSH = &sshclient.Config{Host: "private-ssh-host", User: "private-ssh-user"}
		w.Metrics = &telemetry.Sample{Time: now, Host: &telemetry.Host{CPUPercent: &cpu, MemoryUsedBytes: 90, MemoryTotalBytes: 100}, Errors: []string{"private-metric-error"}, Containers: []telemetry.Container{{ID: "private-application", Name: "private-application"}}}
		w.Containers = []engine.Container{{ID: "private-application", Names: []string{"/private-application"}, Labels: map[string]string{"private-label": "private-label"}}}
		s.Workers[w.ID] = w
		s.DockerContainers = map[string]DockerContainer{"private-reservation": {ID: "private-reservation", Name: "private-reservation", WorkerID: "w1", MemoryBytes: 128 << 20, NanoCPUs: 250000000, Ports: []engine.Port{{Host: 8088, Protocol: "tcp"}}}}
		s.Deployments["private-deployment"] = Deployment{ID: "private-deployment", Name: "private-deployment", Env: []string{"private-env"}, MemoryMB: 256, CPUs: 0.5, Tasks: []Replica{{ID: "private-task", WorkerID: "w1"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	call := func(token, path string, want int) []byte {
		return gatewayEndpointCall(t, client, "http://control", token, "", "GET", path, nil, want)
	}
	b := call(raw, "/api/cluster", 200)
	for _, secret := range []string{"private-", `"endpoint"`, `"token"`, `"ssh"`, `"error"`, `"containers"`, `"deployments"`} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatalf("discovery exposed %s: %s", secret, b)
		}
	}
	var view InfrastructureView
	if json.Unmarshal(b, &view) != nil || len(view.Workers) != 2 || view.Workers[0].ID != "w1" {
		t.Fatalf("invalid cluster: %s", b)
	}
	w := view.Workers[0]
	if !w.AcceptingContainers || !w.InventoryFresh || !w.UsageFresh || w.Usage == nil || *w.Usage.CPUPercent != 12.5 {
		t.Fatalf("health/usage missing: %+v", w)
	}
	if w.Capacity.ReservedCPUs != 0.75 || w.Capacity.AvailableCPUs != 1.25 || w.Capacity.ReservedMemoryBytes != 384<<20 || w.Capacity.AvailableMemoryBytes != (2<<30)-(384<<20) || len(w.Capacity.UnavailableHostPorts) != 1 {
		t.Fatalf("bad reservation accounting: %+v", w.Capacity)
	}
	call(raw, "/api/cluster/workers", 200)
	call(raw, "/api/cluster/workers/w1", 200)
	call(raw, "/api/cluster/workers/missing", 404)
	legacy, _ := issueToken(t, c, "legacy")
	call(legacy, "/api/cluster", 200)
	call(testToken, "/api/cluster", 200)
	call("invalid", "/api/cluster", 401)
	for _, path := range []string{"/api/state", "/api/tokens", "/api/notifications", "/api/deployments", "/api/workers/w1/metrics/history"} {
		call(raw, path, 403)
	}
	gatewayEndpointCall(t, client, "http://control", raw, "", "POST", "/api/deployments", map[string]any{}, 403)
	for _, change := range []string{"paused", "draining", "offline", "stale", "metric-error"} {
		if err := c.Store.update(func(s *State) error {
			w := s.Workers["w1"]
			w.Mode = "active"
			w.Status = "online"
			w.LastSeen = now
			w.MetricsError = ""
			switch change {
			case "paused", "draining":
				w.Mode = change
			case "offline":
				w.Status = change
			case "stale":
				w.LastSeen = now.Add(-time.Minute)
			case "metric-error":
				w.MetricsError = "private-metric-error"
			}
			s.Workers[w.ID] = w
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		b = call(raw, "/api/cluster/workers/w1", 200)
		var worker InfrastructureWorker
		json.Unmarshal(b, &worker)
		if change != "metric-error" && worker.AcceptingContainers {
			t.Fatalf("%s worker accepting containers", change)
		}
		if (change == "metric-error" || change == "offline") && (worker.UsageFresh || worker.Usage != nil) {
			t.Fatalf("invalid metrics presented as current for %s", change)
		}
	}
}

func TestInfrastructureDockerSelectionAndOwnership(t *testing.T) {
	c, client, one, two := clusterFixture(t)
	raw, meta := issueInfrastructureToken(t, c)
	other, _ := issueInfrastructureToken(t, c)
	call := func(worker, method, path string, body any, want int) []byte {
		return gatewayEndpointCall(t, client, "http://control", raw, worker, method, path, body, want)
	}
	config := map[string]any{"Image": "alpine"}
	call("", "POST", "/containers/create?name=no-choice", config, 400)
	call("", "POST", "/images/create?fromImage=alpine", nil, 400)
	call("missing", "POST", "/containers/create?name=missing", config, 404)
	one.mu.Lock()
	initialRequests := one.requests
	one.mu.Unlock()
	b := call("w2", "POST", "/v1.51/containers/create?name=zela-choice", config, 201)
	var ct struct {
		ID string `json:"Id"`
	}
	json.Unmarshal(b, &ct)
	indexed := c.Store.snapshot().DockerContainers[ct.ID]
	if indexed.WorkerID != "w2" || indexed.AccessTokenID != meta.ID || indexed.MemoryBytes != 128<<20 || indexed.NanoCPUs != 250000000 {
		t.Fatalf("choice/ownership/defaults lost: %+v", indexed)
	}
	one.mu.Lock()
	if one.requests != initialRequests || len(one.containers) != 0 {
		t.Error("selected placement contacted another worker")
	}
	one.mu.Unlock()
	call("", "POST", "/containers/"+ct.ID+"/start", nil, 204)
	call("w2", "GET", "/containers/zela-choice/json", nil, 200)
	call("", "GET", "/docker/workers/w2/v1.51/containers/zela-choice/json", nil, 200)
	call("w1", "POST", "/containers/"+ct.ID+"/stop", nil, 409)
	gatewayEndpointCall(t, client, "http://control", other, "w2", "GET", "/containers/"+ct.ID+"/json", nil, 403)
	// Unmanaged containers on the selected worker must remain hidden.
	two.mu.Lock()
	two.containers["two-private"] = false
	two.mu.Unlock()
	var list []map[string]any
	json.Unmarshal(call("w2", "GET", "/containers/json?all=1&limit=1", nil, 200), &list)
	if len(list) != 1 || list[0]["Id"] != ct.ID {
		t.Fatal("selected inventory exposed another container")
	}
	json.Unmarshal(call("w1", "GET", "/containers/json?all=1", nil, 200), &list)
	if len(list) != 0 {
		t.Fatal("wrong selected inventory")
	}
	var exec struct {
		ID string `json:"Id"`
	}
	json.Unmarshal(call("w2", "POST", "/containers/"+ct.ID+"/exec", map[string]any{"Cmd": []string{"echo", "zela"}}, 201), &exec)
	call("w1", "GET", "/exec/"+exec.ID+"/json", nil, 409)
	call("", "POST", "/exec/"+exec.ID+"/start", map[string]bool{"Detach": false, "Tty": false}, 200)
	for _, path := range []string{"/build", "/volumes/create", "/networks/create", "/swarm/init"} {
		call("w2", "POST", path, map[string]any{}, 403)
	}
	call("w2", "POST", "/containers/create?name=privileged", map[string]any{"Image": "alpine", "HostConfig": map[string]any{"Privileged": true}}, 403)
	call("", "POST", "/docker/workers/w1/containers/create?name=prefix-choice", config, 201)
	call("w1", "GET", "/docker/workers/w2/containers/json", nil, 400)
	// Pausing admission still allows Zela to stop and remove its existing work.
	if err := c.Store.update(func(s *State) error { w := s.Workers["w2"]; w.Mode = "paused"; s.Workers[w.ID] = w; return nil }); err != nil {
		t.Fatal(err)
	}
	call("w2", "POST", "/containers/create?name=paused", config, 503)
	call("w2", "POST", "/containers/"+ct.ID+"/stop", nil, 204)
	call("w2", "DELETE", "/containers/"+ct.ID, nil, 204)
	if _, ok := c.Store.snapshot().DockerContainers[ct.ID]; ok {
		t.Fatal("deleted reservation retained")
	}
	if err := c.revokeAccessToken(meta.ID); err != nil {
		t.Fatal(err)
	}
	call("", "GET", "/api/cluster", nil, 401)
}

func TestInfrastructureSelectedWorkerAdmission(t *testing.T) {
	for _, failure := range []string{"offline", "draining", "capacity", "live-capacity", "live-port", "unreachable"} {
		t.Run(failure, func(t *testing.T) {
			one, two := newSimulatedDocker("one"), newSimulatedDocker("two")
			var broken atomic.Bool
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if broken.Load() {
					switch failure {
					case "live-capacity":
						if r.URL.Path == "/info" {
							dockerJSONResponse(w, 200, engine.Info{NCPU: 0, MemTotal: 1})
							return
						}
					case "live-port":
						if r.URL.Path == "/containers/json" {
							dockerJSONResponse(w, 200, []map[string]any{{"Id": "unmanaged", "Ports": []map[string]any{{"PublicPort": 8088, "Type": "tcp"}}}})
							return
						}
					case "unreachable":
						http.Error(w, "worker unavailable", 503)
						return
					}
				}
				two.ServeHTTP(w, r)
			})
			c, client := gatewayHTTPStack(t, map[string]http.RoundTripper{"w1": pipeHTTPServer(t, one), "w2": pipeHTTPServer(t, handler)})
			c.Refresh(context.Background())
			raw, _ := issueInfrastructureToken(t, c)
			broken.Store(true)
			if err := c.Store.update(func(s *State) error {
				w := s.Workers["w2"]
				switch failure {
				case "offline":
					w.Status = "offline"
				case "draining":
					w.Mode = "draining"
				case "capacity":
					s.DockerContainers = map[string]DockerContainer{"reserved": {ID: "reserved", WorkerID: "w2", MemoryBytes: 2 << 30, NanoCPUs: 2000000000}}
				}
				s.Workers[w.ID] = w
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			gatewayEndpointCall(t, client, "http://control", raw, "w2", "POST", "/containers/create?name=reject", map[string]any{"Image": "alpine", "HostConfig": map[string]any{"PortBindings": map[string]any{"80/tcp": []map[string]string{{"HostPort": "8088"}}}}}, 503)
			one.mu.Lock()
			if len(one.containers) != 0 {
				t.Error("fell back to unselected worker")
			}
			one.mu.Unlock()
			two.mu.Lock()
			if len(two.containers) != 0 {
				t.Error("created on inadmissible worker")
			}
			two.mu.Unlock()
		})
	}
}

func TestInfrastructureRechecksWorkerAfterImagePull(t *testing.T) {
	one, two := newSimulatedDocker("one"), newSimulatedDocker("two")
	var c *Controller
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/images/create" {
			if err := c.Store.update(func(s *State) error {
				worker := s.Workers["w2"]
				worker.Mode = "paused"
				s.Workers["w2"] = worker
				return nil
			}); err != nil {
				t.Error(err)
			}
		}
		two.ServeHTTP(w, r)
	})
	var client *http.Client
	c, client = gatewayHTTPStack(t, map[string]http.RoundTripper{"w1": pipeHTTPServer(t, one), "w2": pipeHTTPServer(t, handler)})
	c.Refresh(context.Background())
	raw, _ := issueInfrastructureToken(t, c)
	gatewayEndpointCall(t, client, "http://control", raw, "w2", "POST", "/containers/create?name=pause-during-pull", map[string]any{"Image": "alpine"}, 503)
	if len(c.Store.snapshot().DockerContainers) != 0 {
		t.Fatal("persisted a placement after worker was paused")
	}
	one.mu.Lock()
	if len(one.containers) != 0 {
		t.Error("fell back after pause")
	}
	one.mu.Unlock()
	two.mu.Lock()
	if len(two.containers) != 0 {
		t.Error("created after pause")
	}
	two.mu.Unlock()
}

func TestInfrastructureTokenCreationAPI(t *testing.T) {
	c, _ := fixture(t)
	h := Handler(c, HTTPConfig{Token: testToken})
	var result struct {
		Token    string              `json:"token"`
		Metadata AccessTokenMetadata `json:"metadata"`
	}
	body := tokenAPICall(t, h, testToken, "POST", "/api/tokens", `{"name":"my-platform","scope":"infrastructure","expiresInDays":30}`, 201)
	if json.Unmarshal(body, &result) != nil || result.Metadata.Scope != "infrastructure" || result.Metadata.ExpiresAt == nil || !bytes.HasPrefix([]byte(result.Token), []byte("zela_infra_")) {
		t.Fatal("invalid infrastructure token")
	}
	tokenAPICall(t, h, result.Token, "GET", "/api/cluster/workers", "", 200)
	tokenAPICall(t, h, result.Token, "POST", "/api/workers", "{}", 403)
	tokenAPICall(t, h, result.Token, "POST", "/api/tokens", `{"name":"escalate","scope":"deployments"}`, 403)
	tokenAPICall(t, h, result.Token, "POST", "/api/deployments", "{}", 403)
	tokenAPICall(t, h, testToken, "POST", "/api/tokens", `{"name":"invalid","scope":"read-all"}`, 400)
	tokenAPICall(t, h, testToken, "POST", "/api/tokens/"+result.Metadata.ID+"/revoke", "{}", 200)
	tokenAPICall(t, h, result.Token, "GET", "/api/cluster/workers", "", 401)
}
