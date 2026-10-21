package clusternet

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEdgeRouteMatching(t *testing.T) {
	e := newEdgeServer("", "")
	e.routes = sortRoutes([]EdgeRoute{
		{PathPrefix: "/", Target: "any"},
		{Host: "*.example.com", PathPrefix: "/", Target: "wild"},
		{Host: "api.example.com", PathPrefix: "/", Target: "api"},
		{Host: "api.example.com", PathPrefix: "/v2", Target: "v2"},
	})
	for _, c := range []struct{ host, path, want string }{
		{"api.example.com", "/v2/users", "v2"},
		{"API.example.com:8080", "/v2", "v2"},
		{"api.example.com", "/v20", "api"}, // prefix matches whole segments only
		{"shop.example.com", "/", "wild"},
		{"a.b.example.com", "/", "any"}, // wildcard covers one label
		{"other.org", "/x", "any"},
	} {
		r := httptest.NewRequest("GET", "http://"+c.host+c.path, nil)
		r.Host = c.host
		rt, ok, done := e.match(r)
		if !ok || rt.Target != c.want {
			t.Fatalf("%s%s -> %q, want %q", c.host, c.path, rt.Target, c.want)
		}
		done()
	}
	e.routes = nil
	if _, ok, _ := e.match(httptest.NewRequest("GET", "/", nil)); ok {
		t.Fatal("matched with no routes")
	}
}

func TestEdgeProxiesWithForwardedHeaders(t *testing.T) {
	var got http.Header
	var host string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, host = r.Header.Clone(), r.Host
		_, _ = io.WriteString(w, "hello from "+r.URL.Path)
	}))
	defer backend.Close()
	e := newEdgeServer("", "")
	e.routes = []EdgeRoute{{Host: "app.example.com", PathPrefix: "/", Target: strings.TrimPrefix(backend.URL, "http://")}}
	edge := httptest.NewServer(e)
	defer edge.Close()

	req, _ := http.NewRequest("GET", edge.URL+"/hi", nil)
	req.Host = "app.example.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "hello from /hi" {
		t.Fatalf("proxied response: %d %s", resp.StatusCode, body)
	}
	if host != "app.example.com" || got.Get("X-Forwarded-For") == "" || got.Get("X-Forwarded-Proto") != "http" || got.Get("X-Forwarded-Host") != "app.example.com" {
		t.Fatalf("forwarding headers: host=%q %v", host, got)
	}

	req.Host = "unknown.example.com"
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 404 {
		t.Fatalf("unknown host: %d", resp.StatusCode)
	}
	backend.Close()
	req.Host = "app.example.com"
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 502 {
		t.Fatalf("dead backend: %d", resp.StatusCode)
	}
}

func TestEdgeUpgradesConnections(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "echo" {
			http.Error(w, "upgrade required", 426)
			return
		}
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\n")
		_ = rw.Flush()
		line, _ := rw.ReadString('\n')
		_, _ = rw.WriteString("echo:" + line)
		_ = rw.Flush()
	}))
	defer backend.Close()
	e := newEdgeServer("", "")
	e.routes = []EdgeRoute{{PathPrefix: "/", Target: strings.TrimPrefix(backend.URL, "http://")}}
	edge := httptest.NewServer(e)
	defer edge.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(edge.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	_, _ = io.WriteString(conn, "ping\n")
	if line, _ := br.ReadString('\n'); line != "echo:ping\n" {
		t.Fatalf("upgraded stream: %q", line)
	}
}

func TestEdgeListenersAndValidation(t *testing.T) {
	e := newEdgeServer("", "")
	var bound []string
	e.listen = func(network, addr string) (net.Listener, error) {
		bound = append(bound, addr)
		return net.Listen("tcp", "127.0.0.1:0")
	}
	defer e.stop()
	if err := e.update(&Edge{HTTPPort: 8080}); err != nil {
		t.Fatal(err)
	}
	if err := e.update(&Edge{HTTPPort: 8080, Routes: []EdgeRoute{{PathPrefix: "/", Target: "10.200.255.1:80"}}}); err != nil || len(bound) != 1 {
		t.Fatalf("route-only change rebound listeners: %v %v", bound, err)
	}
	if err := e.update(&Edge{HTTPPort: 8081, HTTPSPort: 8443}); err == nil {
		t.Fatal("HTTPS without a local certificate accepted")
	}
	if e.http == nil {
		t.Fatal("HTTP listener not kept when HTTPS failed")
	}
	base := Config{Cluster: "10.200.0.0/16", Address: "10.200.0.1/24", Subnet: "10.200.1.0/24", ListenPort: 51820}
	for _, bad := range []Edge{
		{HTTPPort: 80, Routes: []EdgeRoute{{PathPrefix: "/", Target: "10.200.1.5:80"}}},   // not a VIP
		{HTTPPort: 80, Routes: []EdgeRoute{{PathPrefix: "x", Target: "10.200.255.1:80"}}}, // relative path
		{HTTPPort: 80, Routes: []EdgeRoute{{Host: "bad host", PathPrefix: "/", Target: "10.200.255.1:80"}}},
		{HTTPPort: 80, HTTPSPort: 80},
	} {
		c := base
		c.Edge = &bad
		if c.Validate() == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestEdgeHTTPSFailureDoesNotRestartHTTP(t *testing.T) {
	e := newEdgeServer("", "")
	binds := 0
	e.listen = func(string, string) (net.Listener, error) { binds++; return net.Listen("tcp", "127.0.0.1:0") }
	defer e.stop()
	for i := 0; i < 3; i++ {
		_ = e.update(&Edge{HTTPPort: 80, HTTPSPort: 443}) // HTTPS keeps failing: no cert
	}
	if binds != 1 || e.http == nil {
		t.Fatalf("HTTP listener restarted on re-apply: %d binds", binds)
	}
	_ = e.update(&Edge{HTTPPort: 81})
	if binds != 2 || e.httpPort != 81 {
		t.Fatal("port change not applied")
	}
}

func TestEdgeRouteGenerationWaitsForInflightRequests(t *testing.T) {
	e := newEdgeServer("", "")
	if err := e.update(&Edge{Routes: []EdgeRoute{{Host: "app.example", Target: "10.0.0.1:8080"}}}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://app.example/", nil)
	_, ok, done := e.match(r)
	if !ok {
		t.Fatal("route did not match")
	}
	updated := make(chan error, 1)
	go func() {
		updated <- e.update(&Edge{Routes: []EdgeRoute{{Host: "app.example", Target: "10.0.0.2:8080"}}})
	}()
	select {
	case err := <-updated:
		t.Fatalf("route update acknowledged before old request drained: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	done()
	select {
	case err := <-updated:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("route update did not finish after request drained")
	}
}

func TestEdgeGenerationUsesIndependentConnectionPools(t *testing.T) {
	e := newEdgeServer("", "")
	initial := &Edge{Routes: []EdgeRoute{{Host: "app.example", PathPrefix: "/", Target: "10.0.0.1:80"}}, ServiceGeneration: "old"}
	if err := e.update(initial); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "http://app.example/", nil)
	_, old, ok, done := e.acquireRoute(request)
	if !ok {
		t.Fatal("missing initial route")
	}
	e.drainTimeout = time.Millisecond
	next := &Edge{Routes: initial.Routes, ServiceGeneration: "new"}
	if err := e.update(next); err == nil {
		t.Fatal("acknowledged active old generation")
	}
	_, current, ok, release := e.acquireRoute(request)
	if !ok {
		t.Fatal("missing new route")
	}
	release()
	if old == current {
		t.Fatal("new requests share old generation connection pool")
	}
	if err := e.update(next); err == nil {
		t.Fatal("same-config retry bypassed pending drain")
	}
	done()
	if err := e.update(next); err != nil {
		t.Fatal(err)
	}
	if len(e.retired) != 0 {
		t.Fatal("retired connection pool was not released")
	}
}
