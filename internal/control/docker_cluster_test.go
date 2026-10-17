package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func clusterFixture(t *testing.T) (*Controller, *http.Client, *simulatedDocker, *simulatedDocker) {
	t.Helper()
	one, two := newSimulatedDocker("one"), newSimulatedDocker("two")
	c, client := gatewayHTTPStack(t, map[string]http.RoundTripper{"w1": pipeHTTPServer(t, one), "w2": pipeHTTPServer(t, two)})
	c.Refresh(context.Background())
	return c, client, one, two
}
func autoContainer(t *testing.T, client *http.Client, name string, host map[string]any) string {
	t.Helper()
	b := gatewayHTTPCall(t, client, "", "POST", "/v1.51/containers/create?name="+name, map[string]any{"Image": "alpine", "HostConfig": host, "Cmd": []string{"sleep", "60"}}, 201)
	var created struct {
		ID string `json:"Id"`
	}
	if json.Unmarshal(b, &created) != nil || created.ID == "" {
		t.Fatal("missing container ID")
	}
	return created.ID
}

func TestDockerGatewayAutomaticPlacementAndOwnership(t *testing.T) {
	c, client, one, two := clusterFixture(t)
	gatewayHTTPCall(t, client, "", "HEAD", "/_ping", nil, 200)
	version := gatewayHTTPCall(t, client, "", "GET", "/version", nil, 200)
	if !bytes.Contains(version, []byte(`"ApiVersion":"1.51"`)) {
		t.Fatal("bad cluster negotiation")
	}
	info := gatewayHTTPCall(t, client, "", "GET", "/info", nil, 200)
	if !bytes.Contains(info, []byte(`"NCPU":4`)) {
		t.Fatal("capacity not aggregated")
	}
	first := autoContainer(t, client, "first", nil)
	second := autoContainer(t, client, "second", nil)
	s := c.Store.snapshot()
	if s.DockerContainers[first].WorkerID == s.DockerContainers[second].WorkerID {
		t.Fatal("containers not spread across workers before inventory poll")
	}
	for _, daemon := range []*simulatedDocker{one, two} {
		daemon.mu.Lock()
		present := daemon.images["alpine"] && len(daemon.containers) == 1
		daemon.mu.Unlock()
		if !present {
			t.Fatal("image was not ensured on selected worker")
		}
	}
	ct := s.DockerContainers[first]
	if ct.MemoryBytes != 128<<20 || ct.NanoCPUs != 250000000 {
		t.Fatal("default limits not reserved")
	}
	gatewayHTTPCall(t, client, "", "POST", "/v1.51/containers/"+first+"/start", nil, 204)
	inspect := gatewayHTTPCall(t, client, "", "GET", "/v1.51/containers/first/json", nil, 200)
	if !bytes.Contains(inspect, []byte(`"Running":true`)) {
		t.Fatal("name routing failed")
	}
	var list []map[string]any
	if json.Unmarshal(gatewayHTTPCall(t, client, "", "GET", "/v1.51/containers/json?all=1", nil, 200), &list) != nil || len(list) != 2 {
		t.Fatal("cluster container inventory not aggregated")
	}
	gatewayHTTPCall(t, client, "", "POST", "/v1.51/containers/create?name=first", map[string]string{"Image": "alpine"}, 409)
	gatewayHTTPCall(t, client, "", "GET", "/v1.51/containers/"+first+"/logs?stdout=1", nil, 200)
	var exec struct {
		ID string `json:"Id"`
	}
	if json.Unmarshal(gatewayHTTPCall(t, client, "", "POST", "/v1.51/containers/"+first+"/exec", map[string]any{"Cmd": []string{"echo", "hi"}, "AttachStdout": true}, 201), &exec) != nil {
		t.Fatal("invalid exec")
	}
	if c.Store.snapshot().DockerExecs[exec.ID].WorkerID != ct.WorkerID {
		t.Fatal("exec ownership not recorded")
	}
	output := gatewayHTTPCall(t, client, "", "POST", "/v1.51/exec/"+exec.ID+"/start", map[string]bool{"Detach": false}, 200)
	if !bytes.Contains(output, []byte("gateway-exec")) {
		t.Fatal("exec not routed to owner")
	}
	gatewayHTTPCall(t, client, "", "POST", "/containers/"+first+"/rename?name=second", nil, 409)
	gatewayHTTPCall(t, client, "", "POST", "/containers/"+first+"/rename?name=renamed", nil, 204)
	gatewayHTTPCall(t, client, "", "GET", "/containers/renamed/json", nil, 200)
	gatewayHTTPCall(t, client, "", "GET", "/containers/first/json", nil, 404)
	// Pausing excludes new placement, but does not break existing containers.
	if err := c.Store.update(func(s *State) error { w := s.Workers[ct.WorkerID]; w.Mode = "paused"; s.Workers[w.ID] = w; return nil }); err != nil {
		t.Fatal(err)
	}
	gatewayHTTPCall(t, client, "", "POST", "/v1.51/containers/"+first+"/stop", nil, 204)
	third := autoContainer(t, client, "third", nil)
	if c.Store.snapshot().DockerContainers[third].WorkerID == ct.WorkerID {
		t.Fatal("paused worker received automatic placement")
	}
	for _, id := range []string{first, second, third} {
		gatewayHTTPCall(t, client, "", "DELETE", "/v1.51/containers/"+id+"?force=true", nil, 204)
	}
	if len(c.Store.snapshot().DockerContainers)+len(c.Store.snapshot().DockerExecs) != 0 {
		t.Fatal("ownership/reservations not released")
	}
}

func TestDockerGatewayConcurrentCreatesDoNotOvercommit(t *testing.T) {
	c, client, _, _ := clusterFixture(t)
	start := make(chan struct{})
	results := make(chan int, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			req, err := http.NewRequest("POST", fmt.Sprintf("http://control/containers/create?name=parallel-%d", i), strings.NewReader(`{"Image":"alpine","HostConfig":{"Memory":134217728,"NanoCpus":2000000000}}`))
			if err != nil {
				results <- 0
				return
			}
			req.Header.Set("Authorization", "Bearer "+testToken)
			resp, err := client.Do(req)
			if err != nil {
				results <- 0
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			results <- resp.StatusCode
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	accepted := 0
	for status := range results {
		if status == 201 {
			accepted++
		} else if status != 409 && status != 503 {
			t.Fatalf("unexpected concurrent create response: %d", status)
		}
	}
	if accepted == 0 || accepted > 2 {
		t.Fatalf("accepted %d creations on two 2-CPU workers", accepted)
	}
	reservations := map[string]int64{}
	for _, ct := range c.Store.snapshot().DockerContainers {
		reservations[ct.WorkerID] += ct.NanoCPUs
	}
	for _, cpus := range reservations {
		if cpus > 2000000000 {
			t.Fatal("CPU capacity overcommitted")
		}
	}
}

func TestDockerGatewayReservationsReconcileExternalDeletion(t *testing.T) {
	c, client, one, two := clusterFixture(t)
	id := autoContainer(t, client, "external", nil)
	owner := c.Store.snapshot().DockerContainers[id].WorkerID
	var daemon *simulatedDocker
	if owner == "w1" {
		daemon = one
	} else {
		daemon = two
	}
	daemon.mu.Lock()
	delete(daemon.containers, id)
	daemon.mu.Unlock()
	c.Refresh(context.Background())
	if _, ok := c.Store.snapshot().DockerContainers[id]; ok {
		t.Fatal("deleted container retained capacity reservation")
	}
	// Placement continues on a healthy worker when another host is offline.
	if err := c.Store.update(func(s *State) error { w := s.Workers[owner]; w.Status = "offline"; s.Workers[owner] = w; return nil }); err != nil {
		t.Fatal(err)
	}
	id = autoContainer(t, client, "healthy", nil)
	if c.Store.snapshot().DockerContainers[id].WorkerID == owner {
		t.Fatal("offline worker received placement")
	}
}

func TestDockerGatewayReservationsPortsAndOfflineOwner(t *testing.T) {
	c, client, _, _ := clusterFixture(t)
	first := autoContainer(t, client, "large", map[string]any{"Memory": int64(1900 << 20), "NanoCpus": int64(1800000000), "PortBindings": map[string]any{"80/tcp": []map[string]string{{"HostPort": "18080"}}}})
	second := autoContainer(t, client, "port", map[string]any{"PortBindings": map[string]any{"80/tcp": []map[string]string{{"HostPort": "18080"}}}})
	s := c.Store.snapshot()
	owner := s.DockerContainers[first].WorkerID
	if owner == s.DockerContainers[second].WorkerID {
		t.Fatal("capacity/port reservation ignored")
	}
	if err := c.Store.update(func(s *State) error { w := s.Workers[owner]; w.Status = "offline"; s.Workers[owner] = w; return nil }); err != nil {
		t.Fatal(err)
	}
	gatewayHTTPCall(t, client, "", "POST", "/v1.51/containers/"+first+"/start", nil, 503)
	gatewayHTTPCall(t, client, "", "GET", "/v1.51/containers/json?all=1", nil, 503)
	// Offline ownership must not silently route mutations to a different host.
	if c.Store.snapshot().DockerContainers[first].WorkerID != owner {
		t.Fatal("offline container was relocated")
	}
}

func TestDockerGatewayOwnershipSurvivesRestart(t *testing.T) {
	c, client, _, _ := clusterFixture(t)
	id := autoContainer(t, client, "restart", nil)
	var exec struct {
		ID string `json:"Id"`
	}
	_ = json.Unmarshal(gatewayHTTPCall(t, client, "", "POST", "/v1.51/containers/"+id+"/exec", map[string]any{"Cmd": []string{"echo", "hi"}}, 201), &exec)
	dir := filepath.Dir(c.Store.path)
	if err := c.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(store, c.Factory)
	t.Cleanup(restarted.Close)
	client = &http.Client{Transport: pipeHTTPServer(t, Handler(restarted, HTTPConfig{Token: testToken}))}
	gatewayHTTPCall(t, client, "", "POST", "/v1.51/containers/"+id+"/start", nil, 204)
	gatewayHTTPCall(t, client, "", "POST", "/v1.51/exec/"+exec.ID+"/start", map[string]bool{"Detach": false}, 200)
	gatewayHTTPCall(t, client, "", "DELETE", "/v1.51/containers/"+id+"?force=true", nil, 204)
}

func TestDockerGatewayUnindexedAndAmbiguousContainers(t *testing.T) {
	_, client, one, two := clusterFixture(t)
	one.containers["one-legacy"] = true
	gatewayHTTPCall(t, client, "", "GET", "/containers/legacy/json", nil, 200)
	two.containers["two-legacy"] = true
	gatewayHTTPCall(t, client, "", "POST", "/containers/legacy/stop", nil, 409)
	gatewayHTTPCall(t, client, "", "POST", "/containers/one-legacy/stop", nil, 204)
}

func TestDockerGatewayAutoCreationFailureAndLimits(t *testing.T) {
	c, client, one, two := clusterFixture(t)
	for _, host := range []map[string]any{{"Mounts": []map[string]string{{"Type": "volume", "Source": "data", "Target": "/data"}}}, {"NetworkMode": "custom"}} {
		gatewayHTTPCall(t, client, "", "POST", "/containers/create?name=unsupported", map[string]any{"Image": "alpine", "HostConfig": host}, 501)
	}
	gatewayHTTPCall(t, client, "", "POST", "/containers/create?name=invalid", map[string]any{"Image": "alpine", "HostConfig": map[string]any{"Memory": -1}}, 400)
	path := c.Store.path
	c.Store.path = filepath.Join(t.TempDir(), "missing", "cluster.json")
	gatewayHTTPCall(t, client, "", "POST", "/containers/create?name=rollback", map[string]string{"Image": "alpine"}, 502)
	c.Store.path = path
	for _, daemon := range []*simulatedDocker{one, two} {
		daemon.mu.Lock()
		count := len(daemon.containers)
		daemon.mu.Unlock()
		if count != 0 {
			t.Fatal("unpersisted creation not rolled back")
		}
	}
	if len(c.Store.snapshot().DockerContainers) != 0 {
		t.Fatal("failed save left reservation")
	}
}

func TestDockerGatewayClusterPullAndImages(t *testing.T) {
	_, client, one, two := clusterFixture(t)
	gatewayHTTPCall(t, client, "", "POST", "/v1.51/images/create?fromImage=alpine", nil, 200)
	for _, d := range []*simulatedDocker{one, two} {
		d.mu.Lock()
		pulled := d.images["alpine"]
		d.mu.Unlock()
		if !pulled {
			t.Fatal("pull did not reach all active workers")
		}
	}
	var images []map[string]any
	_ = json.Unmarshal(gatewayHTTPCall(t, client, "", "GET", "/v1.51/images/json", nil, 200), &images)
	if len(images) != 1 {
		t.Fatal("identical images not deduplicated")
	}
	gatewayHTTPCall(t, client, "", "GET", "/v1.51/images/alpine/json", nil, 200)
}
