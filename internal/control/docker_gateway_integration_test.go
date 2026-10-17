package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zela-dev/zela/internal/engine"
	"github.com/zela-dev/zela/internal/workerproxy"
)

// These tests run real net/http servers and transports over loopback TCP on
// ephemeral 127.0.0.1 ports. They never use the real state directory or
// require Docker. (In-memory net.Pipe connections were replaced: being
// unbuffered without half-close, they made keep-alive reuse fail randomly.)
func pipeHTTPServer(t *testing.T, h http.Handler) *http.Transport {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &http.Server{Handler: h, ReadHeaderTimeout: time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := s.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("pipe HTTP server: %v", err)
		}
	}()
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", l.Addr().String())
	}}
	t.Cleanup(func() {
		tr.CloseIdleConnections()
		s.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("pipe server failed to stop")
		}
	})
	return tr
}

func gatewayHTTPStack(t *testing.T, dockerTransports map[string]http.RoundTripper) (*Controller, *http.Client) {
	t.Helper()
	s, err := OpenStore(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	workers := map[string]*engine.Client{}
	for id, tr := range dockerTransports {
		token := strings.Repeat(id+"-", 32)
		proxy := workerproxy.DockerProxy(tr)
		workerTransport := pipeHTTPServer(t, workerproxy.Handler(token, proxy, proxy))
		client, err := engine.New("http://"+id+":9091", token)
		if err != nil {
			t.Fatal(err)
		}
		client.HTTP.Transport = workerTransport
		workers[id] = client
		if err := s.update(func(s *State) error {
			s.Workers[id] = Worker{ID: id, Name: id, Endpoint: client.URL, Token: token, Mode: "active", Status: "online"}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	c := New(s, func(w Worker) (engine.Engine, error) { return workers[w.ID], nil })
	t.Cleanup(c.Close)
	transport := pipeHTTPServer(t, Handler(c, HTTPConfig{Token: testToken}))
	return c, &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

func gatewayHTTPCall(t *testing.T, client *http.Client, worker, method, path string, body any, want int) []byte {
	t.Helper()
	return gatewayEndpointCall(t, client, "http://control", testToken, worker, method, path, body, want)
}

func gatewayEndpointCall(t *testing.T, client *http.Client, endpoint, token, worker, method, path string, body any, want int) []byte {
	t.Helper()
	var content io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		content = bytes.NewReader(b)
	}
	r, err := http.NewRequest(method, endpoint+path, content)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	if worker != "" {
		r.Header.Set("X-Zela-Worker", worker)
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s returned %d: %s; want %d", method, path, resp.StatusCode, b, want)
	}
	return b
}

type simulatedDocker struct {
	mu                sync.Mutex
	name              string
	containers        map[string]bool
	images            map[string]bool
	networks, volumes map[string]bool
	requests          int
}

func newSimulatedDocker(name string) *simulatedDocker {
	return &simulatedDocker{name: name, containers: map[string]bool{}, images: map[string]bool{}, networks: map[string]bool{}, volumes: map[string]bool{}}
}
func (d *simulatedDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests++
	if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Zela-Worker") != "" {
		http.Error(w, "credentials reached daemon", 500)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1.51")
	w.Header().Set("API-Version", "1.51")
	w.Header().Set("Content-Type", "application/json")
	write := func(status int, v any) { w.WriteHeader(status); json.NewEncoder(w).Encode(v) }
	switch {
	case path == "/_ping":
		fmt.Fprint(w, "OK")
	case path == "/version":
		write(200, map[string]string{"ApiVersion": "1.51", "MinAPIVersion": "1.44", "Version": "test-engine", "Name": d.name})
	case path == "/info":
		write(200, map[string]any{"Name": d.name, "NCPU": 2, "MemTotal": int64(2 << 30), "ServerVersion": "test-engine"})
	case path == "/images/create":
		d.images[r.URL.Query().Get("fromImage")] = true
		fmt.Fprintln(w, `{"status":"Pull complete"}`)
	case path == "/images/json":
		list := []map[string]any{}
		for image := range d.images {
			list = append(list, map[string]any{"Id": image, "RepoTags": []string{image}})
		}
		write(200, list)
	case strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
		image := strings.TrimSuffix(strings.TrimPrefix(path, "/images/"), "/json")
		if !d.images[image] {
			write(404, map[string]string{"message": "image not found"})
			return
		}
		write(200, map[string]any{"Id": image})
	case path == "/containers/create":
		var body struct{ Image string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			write(400, map[string]string{"message": "bad create JSON"})
			return
		}
		if !d.images[body.Image] {
			write(404, map[string]string{"message": "image not found"})
			return
		}
		id := d.name + "-" + r.URL.Query().Get("name")
		d.containers[id] = false
		write(201, map[string]any{"Id": id, "Warnings": []string{}})
	case path == "/containers/json":
		list := []map[string]any{}
		for id, running := range d.containers {
			state := "created"
			if running {
				state = "running"
			}
			list = append(list, map[string]any{"Id": id, "Names": []string{"/" + strings.TrimPrefix(id, d.name+"-")}, "State": state})
		}
		write(200, list)
	case strings.HasPrefix(path, "/containers/"):
		id, action, _ := strings.Cut(strings.TrimPrefix(path, "/containers/"), "/")
		running, ok := d.containers[id]
		if !ok {
			write(404, map[string]string{"message": "No such container"})
			return
		}
		switch action {
		case "start":
			d.containers[id] = true
			w.WriteHeader(204)
		case "stop":
			d.containers[id] = false
			w.WriteHeader(204)
		case "json":
			write(200, map[string]any{"Id": id, "State": map[string]any{"Running": running}})
		case "logs":
			fmt.Fprint(w, d.name+" gateway-smoke\n")
		case "rename":
			// Keep the simulated ID stable; aliases are tracked by the gateway.
			w.WriteHeader(204)
		case "exec":
			write(201, map[string]string{"Id": "exec-" + id})
		case "":
			if r.Method == "DELETE" {
				delete(d.containers, id)
				w.WriteHeader(204)
			} else {
				write(405, map[string]string{"message": "method not supported"})
			}
		default:
			write(404, map[string]string{"message": "unknown container route"})
		}
	case strings.HasPrefix(path, "/exec/") && strings.HasSuffix(path, "/json"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/exec/exec-"), "/json")
		if _, ok := d.containers[id]; !ok {
			write(404, map[string]string{"message": "No such exec"})
			return
		}
		write(200, map[string]any{"ID": "exec-" + id, "ContainerID": id, "Running": false, "ExitCode": 0})
	case strings.HasPrefix(path, "/exec/") && strings.HasSuffix(path, "/start"):
		if r.Header.Get("Upgrade") != "tcp" {
			fmt.Fprint(w, "gateway-exec\n")
			return
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
		rw.Flush()
		b := make([]byte, 4)
		if _, err := io.ReadFull(rw, b); err == nil && string(b) == "ping" {
			rw.WriteString("pong")
			rw.Flush()
		}
	case path == "/networks/create":
		var v struct{ Name string }
		json.NewDecoder(r.Body).Decode(&v)
		d.networks[v.Name] = true
		write(201, map[string]string{"Id": v.Name})
	case strings.HasPrefix(path, "/networks/") && r.Method == "DELETE":
		delete(d.networks, strings.TrimPrefix(path, "/networks/"))
		w.WriteHeader(204)
	case path == "/volumes/create":
		var v struct{ Name string }
		json.NewDecoder(r.Body).Decode(&v)
		d.volumes[v.Name] = true
		write(201, map[string]string{"Name": v.Name})
	case strings.HasPrefix(path, "/volumes/") && r.Method == "DELETE":
		delete(d.volumes, strings.TrimPrefix(path, "/volumes/"))
		w.WriteHeader(204)
	default:
		write(404, map[string]string{"message": "unknown simulated route"})
	}
}

func TestDockerGatewayHTTPClusterLifecycle(t *testing.T) {
	one, two := newSimulatedDocker("one"), newSimulatedDocker("two")
	c, client := gatewayHTTPStack(t, map[string]http.RoundTripper{"w1": pipeHTTPServer(t, one), "w2": pipeHTTPServer(t, two)})
	for _, worker := range []string{"w1", "w2"} {
		version := gatewayHTTPCall(t, client, worker, "GET", "/version", nil, 200)
		if !bytes.Contains(version, []byte(`"ApiVersion":"1.51"`)) {
			t.Fatal("version negotiation failed")
		}
		gatewayHTTPCall(t, client, worker, "HEAD", "/_ping", nil, 200)
		gatewayHTTPCall(t, client, worker, "POST", "/v1.51/images/create?fromImage=alpine", nil, 200)
		created := gatewayHTTPCall(t, client, worker, "POST", "/v1.51/containers/create?name=smoke", map[string]any{"Image": "alpine", "Cmd": []string{"sh", "-c", "echo gateway-smoke"}}, 201)
		var result struct {
			ID string `json:"Id"`
		}
		if err := json.Unmarshal(created, &result); err != nil || result.ID == "" {
			t.Fatal("create did not return Docker ID")
		}
		gatewayHTTPCall(t, client, worker, "POST", "/v1.51/containers/"+result.ID+"/start", nil, 204)
		inspect := gatewayHTTPCall(t, client, worker, "GET", "/v1.51/containers/"+result.ID+"/json", nil, 200)
		if !bytes.Contains(inspect, []byte(`"Running":true`)) {
			t.Fatal("container did not start")
		}
		c.Refresh(context.Background())
		state := c.Store.public()
		if len(state.Workers[worker].Containers) != 1 || state.Workers[worker].Containers[0].ID != result.ID {
			t.Fatal("raw Docker container not discovered by control-plane inventory")
		}
		if len(state.Deployments) != 0 || len(state.Operations) != 0 {
			t.Fatal("external Docker workload was incorrectly adopted as a platform deployment")
		}
		gatewayHTTPCall(t, client, "", "GET", "/docker/workers/"+worker+"/v1.51/containers/"+result.ID+"/logs?stdout=1", nil, 200)
		other := "w1"
		if worker == other {
			other = "w2"
		}
		gatewayHTTPCall(t, client, other, "GET", "/v1.51/containers/"+result.ID+"/json", nil, 404)
		gatewayHTTPCall(t, client, worker, "POST", "/v1.51/networks/create", map[string]string{"Name": "smoke-net"}, 201)
		gatewayHTTPCall(t, client, worker, "POST", "/v1.51/volumes/create", map[string]string{"Name": "smoke-volume"}, 201)
		gatewayHTTPCall(t, client, worker, "POST", "/v1.51/containers/"+result.ID+"/stop", nil, 204)
		gatewayHTTPCall(t, client, worker, "DELETE", "/v1.51/containers/"+result.ID+"?force=true", nil, 204)
		gatewayHTTPCall(t, client, worker, "DELETE", "/v1.51/networks/smoke-net", nil, 204)
		gatewayHTTPCall(t, client, worker, "DELETE", "/v1.51/volumes/smoke-volume", nil, 204)
	}
	for _, daemon := range []*simulatedDocker{one, two} {
		daemon.mu.Lock()
		empty := len(daemon.containers)+len(daemon.networks)+len(daemon.volumes) == 0
		daemon.mu.Unlock()
		if !empty {
			t.Fatal("test resources not cleaned up")
		}
	}
}

func TestDockerGatewayHTTPExecThroughBothProxies(t *testing.T) {
	d := newSimulatedDocker("one")
	_, client := gatewayHTTPStack(t, map[string]http.RoundTripper{"w1": pipeHTTPServer(t, d)})
	r, err := http.NewRequest("POST", "http://control/docker/workers/w1/v1.51/exec/id/start", strings.NewReader(`{"Tty":true,"Detach":false}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "tcp")
	// http.Client.Timeout wraps response bodies and hides their Write method.
	// Bound the upgrade through its context, as a streaming SDK client does.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	upgradeClient := *client
	upgradeClient.Timeout = 0
	resp, err := upgradeClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 101 {
		t.Fatalf("exec upgrade returned %d", resp.StatusCode)
	}
	duplex, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		t.Fatal("exec stream is not bidirectional")
	}
	if _, err := duplex.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4)
	if _, err := io.ReadFull(duplex, b); err != nil || string(b) != "pong" {
		t.Fatalf("exec echo failed: %s %v", b, err)
	}
}

func TestDockerGatewayAutomaticExecUpgrade(t *testing.T) {
	c, client, _, _ := clusterFixture(t)
	id := autoContainer(t, client, "upgrade", nil)
	var exec struct {
		ID string `json:"Id"`
	}
	_ = json.Unmarshal(gatewayHTTPCall(t, client, "", "POST", "/containers/"+id+"/exec", map[string]any{"Cmd": []string{"cat"}, "AttachStdin": true, "AttachStdout": true}, 201), &exec)
	if c.Store.snapshot().DockerExecs[exec.ID].ContainerID != id {
		t.Fatal("missing exec route")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", "http://control/v1.51/exec/"+exec.ID+"/start", strings.NewReader(`{"Tty":true,"Detach":false}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	streamClient := *client
	streamClient.Timeout = 0
	resp, err := streamClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 101 {
		t.Fatalf("automatic exec upgrade: %d", resp.StatusCode)
	}
	duplex, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		t.Fatal("exec stream is not duplex")
	}
	if _, err := duplex.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4)
	if _, err := io.ReadFull(duplex, b); err != nil || string(b) != "pong" {
		t.Fatalf("automatic exec stream failed: %q %v", b, err)
	}
}

func TestDockerGatewayHTTPBuildUploadAndIncrementalProgress(t *testing.T) {
	payload := bytes.Repeat([]byte{0, 0xff, 't', 'a', 'r'}, 400000)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	build := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/v1.51/build" || r.URL.RawQuery != "t=registry%2Fapp" || r.Header.Get("X-Registry-Config") != "test-registry-auth" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("build request or credentials altered across proxies")
			http.Error(w, "incorrect request", 500)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(body, payload) {
			t.Error("large binary upload lost or truncated")
			http.Error(w, "invalid upload", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"stream":"first build frame"}`)
		w.(http.Flusher).Flush()
		// A buffering proxy will deadlock here until the client's timeout: the
		// second frame is released only after the client receives the first.
		select {
		case <-release:
			fmt.Fprintln(w, `{"stream":"final build frame"}`)
		case <-r.Context().Done():
		}
	})
	_, client := gatewayHTTPStack(t, map[string]http.RoundTripper{"w1": pipeHTTPServer(t, build)})
	r, err := http.NewRequest("POST", "http://control/docker/workers/w1/v1.51/build?t=registry%2Fapp", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Cookie", "control-cookie=must-not-forward")
	r.Header.Set("X-Registry-Config", "test-registry-auth")
	r.Header.Set("Content-Type", "application/x-tar")
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("build upload returned %d", resp.StatusCode)
	}
	decoder := json.NewDecoder(resp.Body)
	var frame struct{ Stream string }
	if err := decoder.Decode(&frame); err != nil || frame.Stream != "first build frame" {
		t.Fatalf("first progress frame missing: %v", err)
	}
	unblock()
	if err := decoder.Decode(&frame); err != nil || frame.Stream != "final build frame" {
		t.Fatalf("final progress frame missing: %v", err)
	}
}

func TestDockerGatewayHTTPStreamCancellationReachesEngine(t *testing.T) {
	canceled := make(chan struct{})
	stream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"status":"stream open"}`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	})
	_, client := gatewayHTTPStack(t, map[string]http.RoundTripper{"w1": pipeHTTPServer(t, stream)})
	r, _ := http.NewRequest("GET", "http://control/events", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("X-Zela-Worker", "w1")
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	var frame map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&frame); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("client disconnect did not cancel daemon stream across both proxies")
	}
}

func TestDockerGatewayLiveDocker(t *testing.T) {
	socket, image := os.Getenv("ZELA_TEST_DOCKER_SOCKET"), os.Getenv("ZELA_TEST_DOCKER_IMAGE")
	if socket == "" || image == "" {
		t.Skip("set ZELA_TEST_DOCKER_SOCKET and ZELA_TEST_DOCKER_IMAGE (an existing Linux image with sh and sleep) for live testing")
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	t.Cleanup(tr.CloseIdleConnections)
	_, client := gatewayHTTPStack(t, map[string]http.RoundTripper{"live": tr})
	runLiveGatewayLifecycle(t, client, "http://control", testToken, "live", image)
}

func runLiveGatewayLifecycle(t *testing.T, client *http.Client, endpoint, token, worker, image string) {
	t.Helper()
	call := func(method, path string, body any, want int) []byte {
		return gatewayEndpointCall(t, client, endpoint, token, worker, method, path, body, want)
	}
	version := call("GET", "/version", nil, 200)
	var v struct {
		APIVersion string `json:"ApiVersion"`
	}
	if err := json.Unmarshal(version, &v); err != nil || !dockerAPIVersionPattern.MatchString(v.APIVersion) {
		t.Fatal("invalid live Docker version")
	}
	prefix := "/v" + v.APIVersion
	name := newID("zela-gateway-smoke-")
	// Register cleanup before create; a lost response can still leave a container.
	t.Cleanup(func() {
		r, _ := http.NewRequest("DELETE", endpoint+prefix+"/containers/"+name+"?force=true", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Zela-Worker", worker)
		resp, err := client.Do(r)
		if err != nil {
			t.Errorf("smoke cleanup failed for %s: %v", name, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != 204 && resp.StatusCode != 404 {
			t.Errorf("smoke cleanup returned %d for %s", resp.StatusCode, name)
		} else {
			t.Logf("Removed smoke container %s through the gateway", name)
		}
	})
	created := call("POST", prefix+"/containers/create?name="+name, map[string]any{"Image": image, "Cmd": []string{"sh", "-c", "echo zela-gateway-smoke; sleep 60"}, "Labels": map[string]string{"zela.gateway-smoke": name}, "HostConfig": map[string]any{"Memory": int64(64 << 20), "NanoCpus": int64(500000000)}}, 201)
	var result struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(created, &result); err != nil || result.ID == "" {
		t.Fatal("live create returned no ID")
	}
	path := prefix + "/containers/" + result.ID
	call("POST", path+"/start", nil, 204)
	inspect := call("GET", path+"/json", nil, 200)
	var state struct{ State struct{ Running bool } }
	if err := json.Unmarshal(inspect, &state); err != nil || !state.State.Running {
		t.Fatal("live container failed to run")
	}
	if worker == "" {
		t.Logf("Container %s is running, automatically placed through %s", result.ID, endpoint)
	} else {
		t.Logf("Container %s is running on worker %s, deployed through %s", result.ID, worker, endpoint)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		logs := call("GET", path+"/logs?stdout=1&stderr=1", nil, 200)
		if bytes.Contains(logs, []byte("zela-gateway-smoke")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("live container logs missing")
		}
		time.Sleep(100 * time.Millisecond)
	}
	execResult := call("POST", path+"/exec", map[string]any{"AttachStdout": true, "Cmd": []string{"sh", "-c", "echo zela-gateway-exec"}}, 201)
	var exec struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(execResult, &exec); err != nil || exec.ID == "" {
		t.Fatal("live exec create failed")
	}
	output := call("POST", prefix+"/exec/"+exec.ID+"/start", map[string]bool{"Detach": false, "Tty": false}, 200)
	if !bytes.Contains(output, []byte("zela-gateway-exec")) {
		t.Fatal("live exec output missing")
	}
	call("POST", path+"/stop?t=1", nil, 204)
	t.Log("Live Docker gateway passed version, create, start, inspect, logs, exec, and stop; container cleanup registered")
}
