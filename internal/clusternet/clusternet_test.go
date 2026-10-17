package clusternet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func TestAllocationAndValidation(t *testing.T) {
	p, err := ParseCluster("10.200.7.9/16")
	if err != nil || p.String() != "10.200.0.0/16" {
		t.Fatalf("normalize: %v %v", p, err)
	}
	for _, bad := range []string{"10.0.0.0/8", "8.8.0.0/16", "fd00::/16", "nope"} {
		if _, err := ParseCluster(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	addr, subnet, err := Allocate(p, 3)
	if err != nil || addr != "10.200.0.3/24" || subnet != "10.200.3.0/24" {
		t.Fatalf("allocate: %s %s %v", addr, subnet, err)
	}
	if _, _, err := Allocate(p, 255); err == nil {
		t.Fatal("index 255 accepted")
	}
	if g, _ := Gateway(subnet); g != "10.200.3.1" {
		t.Fatalf("gateway %s", g)
	}
	key := strings.Repeat("A", 43) + "="
	good := Config{Cluster: "10.200.0.0/16", Address: addr, Subnet: subnet, ListenPort: 51820, Peers: []Peer{{PublicKey: key, Endpoint: "203.0.113.5:51820", AllowedIPs: []string{"10.200.0.4/32", "10.200.4.0/24"}}}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	escape := good
	escape.Peers = []Peer{{PublicKey: key, Endpoint: "203.0.113.5:51820", AllowedIPs: []string{"0.0.0.0/0"}}}
	if escape.Validate() == nil {
		t.Fatal("peer routing outside the cluster accepted")
	}
	if (Config{Address: "x"}).Validate() == nil {
		t.Fatal("non-empty disabled config accepted")
	}
	swapped := good
	swapped.Peers = []Peer{good.Peers[0], {PublicKey: strings.Repeat("B", 43) + "=", Endpoint: "h:1", AllowedIPs: []string{"10.200.0.5/32"}}}
	reversed := swapped
	reversed.Peers = []Peer{swapped.Peers[1], swapped.Peers[0]}
	if swapped.Hash() != reversed.Hash() || swapped.Hash() == good.Hash() {
		t.Fatal("hash should ignore peer order and reflect content")
	}
}

type recorder struct {
	mu       sync.Mutex
	commands []string
	missing  map[string]bool   // command prefixes that fail
	outputs  map[string]string // command prefix -> output
}

func (r *recorder) run(_ context.Context, name string, args ...string) ([]byte, error) {
	cmd := name + " " + strings.Join(args, " ")
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, cmd)
	for p := range r.missing {
		if strings.HasPrefix(cmd, p) {
			return nil, errors.New("not found")
		}
	}
	for p, out := range r.outputs {
		if strings.HasPrefix(cmd, p) {
			return []byte(out), nil
		}
	}
	if strings.HasPrefix(cmd, "ip -4 route show dev zela0") {
		return []byte("10.200.0.0/24 proto kernel scope link src 10.200.0.1\n10.200.9.0/24 scope link\n"), nil
	}
	return nil, nil
}
func (r *recorder) has(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.commands {
		if strings.Contains(c, s) {
			return true
		}
	}
	return false
}

type fakeWG struct{ last wgtypes.Config }

func (f *fakeWG) ConfigureDevice(_ string, c wgtypes.Config) error { f.last = c; return nil }

type fakeDocker struct {
	mu      sync.Mutex
	api     string
	network map[string]any
}

func (d *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case r.URL.Path == "/version":
		_ = json.NewEncoder(w).Encode(map[string]string{"ApiVersion": d.api})
	case r.Method == "GET" && r.URL.Path == "/networks/zela":
		if d.network == nil {
			http.Error(w, `{"message":"not found"}`, 404)
			return
		}
		_ = json.NewEncoder(w).Encode(d.network)
	case r.Method == "POST" && r.URL.Path == "/networks/create":
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		cfg := body["IPAM"].(map[string]any)["Config"].([]any)[0].(map[string]any)
		d.network = map[string]any{"IPAM": map[string]any{"Config": []any{cfg}}, "Options": body["Options"], "Containers": map[string]any{}}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"Id":"n1"}`))
	case r.Method == "DELETE":
		d.network = nil
		w.WriteHeader(204)
	default:
		http.Error(w, "unexpected", 500)
	}
}

func testAgent(t *testing.T, api string) (*Agent, *recorder, *fakeWG, *fakeDocker) {
	t.Helper()
	d := &fakeDocker{api: api}
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	client := srv.Client()
	client.Transport = rewrite{srv.URL, http.DefaultTransport}
	rec := &recorder{missing: map[string]bool{"ip link show": true, "iptables -w -t filter -n -L": true, "iptables -w -t nat -n -L": true, "iptables -w -t filter -C": true, "iptables -w -t nat -C": true}}
	wg := &fakeWG{}
	a := &Agent{StateDir: filepath.Join(t.TempDir(), "state"), Docker: client, Exec: rec.run, WG: wg, ListenDNS: loopbackDNS}
	t.Cleanup(func() {
		if a.dns != nil {
			a.dns.stop()
		}
	})
	return a, rec, wg, d
}

type rewrite struct {
	base string
	rt   http.RoundTripper
}

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	u := *req.URL
	target, _ := http.NewRequestWithContext(req.Context(), req.Method, r.base+u.Path, req.Body)
	target.Header = req.Header
	return r.rt.RoundTrip(target)
}

func put(t *testing.T, a *Agent, c Config) Status {
	t.Helper()
	b, _ := json.Marshal(c)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("PUT", "/zela/network", bytes.NewReader(b)))
	var s Status
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &s) != nil {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	return s
}

func TestAgentAppliesMeshDockerAndFirewall(t *testing.T) {
	a, rec, wg, docker := testAgent(t, "1.51")
	peerKey, _ := wgtypes.GeneratePrivateKey()
	cfg := Config{Cluster: "10.200.0.0/16", Address: "10.200.0.1/24", Subnet: "10.200.1.0/24", ListenPort: 51820,
		Peers: []Peer{{PublicKey: peerKey.PublicKey().String(), Endpoint: "127.0.0.1:51820", AllowedIPs: []string{"10.200.0.2/32", "10.200.2.0/24"}}}}
	s := put(t, a, cfg)
	if s.Error != "" || s.Applied != cfg.Hash() || len(s.PublicKey) != 44 {
		t.Fatalf("status %+v", s)
	}
	info, err := os.Stat(filepath.Join(a.StateDir, "wireguard.key"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private key file: %v %v", info, err)
	}
	for _, want := range []string{
		"ip link add dev zela0 type wireguard",
		"ip address replace 10.200.0.1/24 dev zela0",
		"ip link set dev zela0 mtu 1420 up",
		"ip route replace 10.200.2.0/24 dev zela0",
		"ip route del 10.200.9.0/24 dev zela0", // stale route from a removed peer
		"-A ZELA-NET -i zela0 -d 10.200.1.0/24 -j RETURN",
		"-A ZELA-NET -d 10.200.1.0/24 -j DROP",
		"-I DOCKER-USER 1 -j ZELA-NET",
		"-t nat -A ZELA-NET-NAT -s 10.200.1.0/24 -d 10.200.0.0/16 -j ACCEPT",
		"-t nat -I POSTROUTING 1 -j ZELA-NET-NAT",
	} {
		if !rec.has(want) {
			t.Fatalf("missing command %q in %v", want, rec.commands)
		}
	}
	if rec.has("ip route del 10.200.0.0/24") || rec.has("ip route del 10.200.2.0/24") {
		t.Fatal("removed a live route")
	}
	if len(wg.last.Peers) != 1 || !wg.last.ReplacePeers || *wg.last.ListenPort != 51820 || wg.last.Peers[0].Endpoint == nil {
		t.Fatalf("wireguard config %+v", wg.last)
	}
	allowed := map[string]bool{}
	for _, n := range wg.last.Peers[0].AllowedIPs {
		allowed[n.String()] = true
	}
	if !allowed["10.200.0.2/32"] || !allowed["10.200.2.0/24"] {
		t.Fatalf("allowed IPs %v", allowed)
	}
	opts := docker.network["Options"].(map[string]any)
	if opts["com.docker.network.bridge.gateway_mode_ipv4"] != "nat-unprotected" || opts["com.docker.network.bridge.name"] != "zelabr0" || opts["com.docker.network.driver.mtu"] != "1420" {
		t.Fatalf("docker network options %v", opts)
	}

	// The persisted config is restored by a fresh agent and keeps the same key.
	b := &Agent{StateDir: a.StateDir}
	b.Load()
	if b.desired.Hash() != cfg.Hash() || b.Status().PublicKey != s.PublicKey {
		t.Fatal("persisted config or key not restored")
	}
}

func TestAgentRejectsOldDockerAndInvalidConfig(t *testing.T) {
	a, _, _, _ := testAgent(t, "1.47")
	s := put(t, a, Config{Cluster: "10.200.0.0/16", Address: "10.200.0.1/24", Subnet: "10.200.1.0/24", ListenPort: 51820})
	if !strings.Contains(s.Error, "Docker Engine 28") || s.Applied != "" {
		t.Fatalf("old Docker accepted: %+v", s)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("PUT", "/zela/network", strings.NewReader(`{"cluster":"10.200.0.0/16","address":"192.168.1.1/24","subnet":"10.200.1.0/24","listenPort":51820}`)))
	if w.Code != 400 {
		t.Fatalf("out-of-cluster address accepted: %d", w.Code)
	}
}

func TestAgentTeardown(t *testing.T) {
	a, rec, _, docker := testAgent(t, "1.51")
	put(t, a, Config{Cluster: "10.200.0.0/16", Address: "10.200.0.1/24", Subnet: "10.200.1.0/24", ListenPort: 51820})
	delete(rec.missing, "ip link show")
	s := put(t, a, Config{})
	if s.Error != "" || !rec.has("ip link del dev zela0") || !rec.has("-X ZELA-NET") || docker.network != nil {
		t.Fatalf("teardown incomplete: %+v %v", s, rec.commands)
	}
	if !netip.MustParsePrefix("10.200.1.0/24").IsValid() {
		t.Fatal()
	}
}

// Regressions found on real hosts: Docker inserts MASQUERADE above our NAT
// jump when it recreates a network, and an old mesh address left on zela0
// becomes the source address that peers reject.
func TestAgentKeepsJumpsFirstAndRemovesStaleAddresses(t *testing.T) {
	a, rec, _, _ := testAgent(t, "1.51")
	rec.outputs = map[string]string{
		"iptables -w -t nat -S POSTROUTING":    "-P POSTROUTING ACCEPT\n-A POSTROUTING -s 10.200.1.0/24 ! -o zelabr0 -j MASQUERADE\n-A POSTROUTING -j ZELA-NET-NAT\n",
		"iptables -w -t filter -S DOCKER-USER": "-N DOCKER-USER\n-A DOCKER-USER -j ZELA-NET\n",
		"ip -4 -o address show dev zela0":      "5: zela0    inet 10.199.0.1/24 scope global zela0\n5: zela0    inet 10.200.0.1/24 scope global zela0\n",
	}
	s := put(t, a, Config{Cluster: "10.200.0.0/16", Address: "10.200.0.1/24", Subnet: "10.200.1.0/24", ListenPort: 51820})
	if s.Error != "" {
		t.Fatal(s.Error)
	}
	if !rec.has("-t nat -D POSTROUTING -j ZELA-NET-NAT") || !rec.has("-t nat -I POSTROUTING 1 -j ZELA-NET-NAT") {
		t.Fatalf("NAT jump not moved to the top: %v", rec.commands)
	}
	if rec.has("-I DOCKER-USER 1 -j ZELA-NET") {
		t.Fatal("already-first filter jump was reinserted")
	}
	if !rec.has("ip address del 10.199.0.1/24 dev zela0") || rec.has("ip address del 10.200.0.1/24") {
		t.Fatalf("stale address handling wrong: %v", rec.commands)
	}
}

func loopbackDNS(string, string) (net.PacketConn, net.Listener, error) {
	return listenDNS("udp", "127.0.0.1:0")
}

func TestAgentServiceVIPRules(t *testing.T) {
	a, rec, _, _ := testAgent(t, "1.51")
	c := Config{Cluster: "10.200.0.0/16", Address: "10.200.0.1/24", Subnet: "10.200.1.0/24", ListenPort: 51820,
		Services: []Service{{VIP: "10.200.255.1", Backends: []string{"10.200.1.2", "10.200.2.2", "10.200.3.2"}}, {VIP: "10.200.255.2"}}}
	if s := put(t, a, c); s.Error != "" {
		t.Fatal(s.Error)
	}
	for _, want := range []string{
		"-A ZELA-SVC -d 10.200.255.1/32 -m statistic --mode random --probability 0.33333 -j DNAT --to-destination 10.200.1.2",
		"-A ZELA-SVC -d 10.200.255.1/32 -m statistic --mode random --probability 0.50000 -j DNAT --to-destination 10.200.2.2",
		"-A ZELA-SVC -d 10.200.255.1/32 -j DNAT --to-destination 10.200.3.2",
		"-t nat -I PREROUTING 1 -j ZELA-SVC",
		"-t nat -I OUTPUT 1 -j ZELA-SVC",
		"ip route replace 10.200.255.0/24 dev zela0",
		"-A ZELA-NET -d 10.200.255.0/24 -j REJECT",
	} {
		if !rec.has(want) {
			t.Fatalf("missing %q in %v", want, rec.commands)
		}
	}
	if rec.has("10.200.255.2/32") {
		t.Fatal("VIP without backends got a DNAT rule")
	}
	bad := c
	bad.Services = []Service{{VIP: "10.200.1.9", Backends: []string{"10.200.1.2"}}}
	if bad.Validate() == nil {
		t.Fatal("VIP outside the service range accepted")
	}
	bad.Services = []Service{{VIP: "10.200.255.3", Backends: []string{"10.200.255.4"}}}
	if bad.Validate() == nil {
		t.Fatal("backend inside the VIP range accepted")
	}
}

func TestEdgeFailureKeepsWorkerReady(t *testing.T) {
	a, _, _, _ := testAgent(t, "1.51")
	a.ListenEdge = func(string, string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
	defer func() {
		if a.edge != nil {
			a.edge.stop()
		}
	}()
	c := Config{Cluster: "10.200.0.0/16", Address: "10.200.0.1/24", Subnet: "10.200.1.0/24", ListenPort: 51820, Edge: &Edge{HTTPPort: 80, HTTPSPort: 443}}
	s := put(t, a, c)
	if s.Error != "" || s.Applied != c.Hash() || !strings.Contains(s.EdgeError, "edge-tls-cert") {
		t.Fatalf("edge failure should be a separate warning: %+v", s)
	}
}
