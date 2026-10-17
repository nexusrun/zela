package control

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zela-dev/zela/internal/clusternet"
	"github.com/zela-dev/zela/internal/engine"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// netEngine simulates a worker agent that applies network configs.
type netEngine struct {
	engine.Engine
	mu      sync.Mutex
	key     string
	configs []clusternet.Config
	specs   []engine.Spec
	fail    error
}

func (e *netEngine) ApplyNetwork(_ context.Context, c clusternet.Config) (clusternet.Status, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fail != nil {
		return clusternet.Status{}, e.fail
	}
	e.configs = append(e.configs, c)
	return clusternet.Status{PublicKey: e.key, Applied: c.Hash()}, nil
}
func (e *netEngine) Create(ctx context.Context, s engine.Spec) (string, error) {
	e.mu.Lock()
	e.specs = append(e.specs, s)
	e.mu.Unlock()
	return e.Engine.Create(ctx, s)
}
func (e *netEngine) last() clusternet.Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.configs[len(e.configs)-1]
}

func networkFixture(t *testing.T) (*Controller, map[string]*netEngine) {
	t.Helper()
	c, es := fixture(t)
	nets := map[string]*netEngine{}
	for id, e := range es {
		k, _ := wgtypes.GeneratePrivateKey()
		nets[id] = &netEngine{Engine: e, key: k.PublicKey().String()}
	}
	c.Factory = func(w Worker) (engine.Engine, error) { return nets[w.ID], nil }
	if err := c.Store.update(func(s *State) error {
		for id, w := range s.Workers {
			w.NetworkEndpoint = id + ".example.net"
			s.Workers[id] = w
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetClusterNetwork("10.200.0.0/16", 0); err != nil {
		t.Fatal(err)
	}
	return c, nets
}

func TestClusterNetworkAllocatesAndExchangesPeers(t *testing.T) {
	c, nets := networkFixture(t)
	c.Refresh(context.Background())
	n := c.Store.snapshot().Network
	if len(n.Members) != 2 {
		t.Fatalf("members: %+v", n.Members)
	}
	w1, w2 := n.Members["w1"], n.Members["w2"]
	if w1.Index != 1 || w1.Subnet != "10.200.1.0/24" || w2.Address != "10.200.0.2/24" || !w1.Ready() || !w2.Ready() {
		t.Fatalf("allocation: %+v %+v", w1, w2)
	}
	// Keys were learned in the first pass; the second pass distributed peers.
	cfg := nets["w1"].last()
	if len(cfg.Peers) != 1 || cfg.Peers[0].PublicKey != nets["w2"].key || cfg.Peers[0].Endpoint != "w2.example.net:51820" {
		t.Fatalf("w1 peers: %+v", cfg.Peers)
	}
	if got := cfg.Peers[0].AllowedIPs; len(got) != 2 || got[0] != "10.200.0.2/32" || got[1] != "10.200.2.0/24" {
		t.Fatalf("allowed IPs %v", got)
	}
	// An unchanged config is not pushed again.
	before := len(nets["w1"].configs)
	c.Refresh(context.Background())
	if len(nets["w1"].configs) != before {
		t.Fatal("unchanged config re-pushed")
	}

	op, err := c.CreateDeployment(Deployment{Name: "web", Image: "nginx:alpine", Replicas: 2, MemoryMB: 64, CPUs: .1})
	succeeded(t, c, op, err)
	for id, e := range nets {
		if len(e.specs) != 1 || e.specs[0].Network != clusternet.DockerNetwork {
			t.Fatalf("%s replica not on the cluster network: %+v", id, e.specs)
		}
	}
}

func TestClusterNetworkGatesSchedulingAndTearsDown(t *testing.T) {
	c, nets := networkFixture(t)
	nets["w2"].fail = errors.New("wireguard module missing")
	c.Refresh(context.Background())
	m := c.Store.snapshot().Network.Members["w2"]
	if m.Ready() || m.Error == "" {
		t.Fatalf("failed worker reported ready: %+v", m)
	}
	op, err := c.CreateDeployment(Deployment{Name: "web", Image: "nginx:alpine", Replicas: 1, WorkerIDs: []string{"w2"}, MemoryMB: 64, CPUs: .1})
	if o := waitOp(t, c, op, err); o.Status != "failed" {
		t.Fatal("deployment placed on a worker without the cluster network")
	}

	// Changing the prefix while in use is refused.
	if err := c.SetClusterNetwork("10.201.0.0/16", 0); err == nil {
		t.Fatal("prefix change accepted")
	}
	nets["w2"].fail = nil
	if err := c.SetClusterNetwork("", 0); err != nil {
		t.Fatal(err)
	}
	c.Refresh(context.Background())
	if n := c.Store.snapshot().Network; n != nil {
		t.Fatalf("network not removed after teardown: %+v", n)
	}
	for id, e := range nets {
		if e.last().Enabled() {
			t.Fatalf("%s not told to tear down", id)
		}
	}
}

func TestDeploymentPortHostIPValidation(t *testing.T) {
	d := Deployment{Name: "web", Image: "nginx", Replicas: 1, MemoryMB: 64, CPUs: .1, Ports: []engine.Port{{Container: 80, Host: 8080, HostIP: "10.0.0.5"}}}
	if err := validateDeployment(&d); err != nil || d.Ports[0].HostIP != "10.0.0.5" {
		t.Fatalf("valid hostIP rejected: %v", err)
	}
	d.Ports[0].HostIP = "eth0"
	if validateDeployment(&d) == nil {
		t.Fatal("invalid hostIP accepted")
	}
}

func TestNetworkRecordsFromInventory(t *testing.T) {
	ct := func(id, state, ip string) engine.Container {
		c := engine.Container{ID: id, State: state}
		c.NetworkSettings = &struct {
			Networks map[string]struct{ IPAddress string }
		}{Networks: map[string]struct{ IPAddress string }{clusternet.DockerNetwork: {IPAddress: ip}}}
		return c
	}
	s := State{
		Network: &NetworkState{Enabled: true, CIDR: "10.200.0.0/16"},
		Workers: map[string]Worker{
			"w1": {Status: "online", Containers: []engine.Container{ct("a", "running", "10.200.1.2"), ct("b", "exited", "10.200.1.3")}},
			"w2": {Status: "online", Containers: []engine.Container{ct("c", "running", "10.200.2.2")}},
			"w3": {Status: "offline", Containers: []engine.Container{ct("d", "running", "10.200.3.2")}},
		},
		Deployments: map[string]Deployment{"dep": {Name: "Web_API", Tasks: []Replica{
			{ID: "a", WorkerID: "w1", Name: "Web_API-1111"}, {ID: "b", WorkerID: "w1", Name: "Web_API-2222"},
			{ID: "c", WorkerID: "w2", Name: "Web_API-3333"}, {ID: "d", WorkerID: "w3", Name: "Web_API-4444"},
		}}},
	}
	r, _ := networkRecords(s)
	if got := r["web-api"]; len(got) != 2 || got[0] != "10.200.1.2" || got[1] != "10.200.2.2" {
		t.Fatalf("service record: %v (stopped and offline replicas must be excluded)", got)
	}
	if got := r["web-api-1111"]; len(got) != 1 || got[0] != "10.200.1.2" {
		t.Fatalf("replica record: %v", r)
	}
	if _, ok := r["web-api-2222"]; ok {
		t.Fatal("stopped replica published")
	}
}

func TestNetworkDeploymentsGetClusterDNS(t *testing.T) {
	c, nets := networkFixture(t)
	c.Refresh(context.Background())
	op, err := c.CreateDeployment(Deployment{Name: "web", Image: "nginx:alpine", Replicas: 1, WorkerIDs: []string{"w1"}, MemoryMB: 64, CPUs: .1})
	succeeded(t, c, op, err)
	spec := nets["w1"].specs[0]
	if len(spec.DNS) != 1 || spec.DNS[0] != "10.200.1.1" || len(spec.DNSSearch) != 1 || spec.DNSSearch[0] != "zela" {
		t.Fatalf("container DNS settings: %+v", spec)
	}
}

func TestServiceVIPAllocationAndBackends(t *testing.T) {
	c, _ := networkFixture(t)
	c.Refresh(context.Background())
	op, err := c.CreateDeployment(Deployment{Name: "web", Image: "nginx:alpine", Replicas: 1, MemoryMB: 64, CPUs: .1})
	succeeded(t, c, op, err)
	s := c.Store.snapshot()
	var id string
	for k := range s.Deployments {
		id = k
	}
	svc, ok := s.Network.Services[id]
	if !ok || svc.VIP != "10.200.255.1" {
		t.Fatalf("VIP not allocated: %+v", s.Network.Services)
	}
	// Records point the service name at the VIP; backends skip unhealthy replicas.
	ct := func(cid, status, ip string) engine.Container {
		x := engine.Container{ID: cid, State: "running", Status: status}
		x.NetworkSettings = &struct {
			Networks map[string]struct{ IPAddress string }
		}{Networks: map[string]struct{ IPAddress string }{clusternet.DockerNetwork: {IPAddress: ip}}}
		return x
	}
	view := State{Network: &NetworkState{Enabled: true, CIDR: "10.200.0.0/16", Services: map[string]NetworkService{"d": {Index: 1, VIP: "10.200.255.1"}}},
		Workers:     map[string]Worker{"w1": {Status: "online", Containers: []engine.Container{ct("a", "Up 1 minute (healthy)", "10.200.1.2"), ct("b", "Up 1 minute (unhealthy)", "10.200.1.3")}}},
		Deployments: map[string]Deployment{"d": {Name: "web", Tasks: []Replica{{ID: "a", WorkerID: "w1", Name: "web-1"}, {ID: "b", WorkerID: "w1", Name: "web-2"}}}}}
	records, services := networkRecords(view)
	if got := records["web"]; len(got) != 1 || got[0] != "10.200.255.1" {
		t.Fatalf("service name should resolve to the VIP: %v", records)
	}
	if len(services) != 1 || len(services[0].Backends) != 1 || services[0].Backends[0] != "10.200.1.2" {
		t.Fatalf("backends: %+v", services)
	}
	if records["web-2"][0] != "10.200.1.3" {
		t.Fatal("replica names should still resolve directly")
	}
	// Deleting the deployment releases its VIP.
	del, err := c.DeploymentAction(id, "delete", "", 0)
	succeeded(t, c, del, err)
	c.Refresh(context.Background())
	if _, ok := c.Store.snapshot().Network.Services[id]; ok {
		t.Fatal("VIP not released after delete")
	}
}

func TestEdgeRoutesAndConfig(t *testing.T) {
	c, _ := networkFixture(t)
	if err := c.SetEdge(80, 0); err != nil {
		t.Fatal(err)
	}
	c.Refresh(context.Background())
	op, err := c.CreateDeployment(Deployment{Name: "web", Image: "nginx", Replicas: 1, MemoryMB: 64, CPUs: .1, Routes: []Route{{Host: "Shop.Example.com.", Port: 80}}})
	succeeded(t, c, op, err)
	// The same host and path cannot be claimed twice.
	if _, err := c.CreateDeployment(Deployment{Name: "web2", Image: "nginx", Replicas: 1, MemoryMB: 64, CPUs: .1, Routes: []Route{{Host: "shop.example.com", Path: "/", Port: 80}}}); err == nil {
		t.Fatal("conflicting route accepted")
	}
	s := c.Store.snapshot()
	var id string
	for k := range s.Deployments {
		id = k
	}
	e := edgeConfig(s)
	if e == nil || e.HTTPPort != 80 || len(e.Routes) != 1 || e.Routes[0].Host != "shop.example.com" || e.Routes[0].PathPrefix != "/" || e.Routes[0].Target != s.Network.Services[id].VIP+":80" {
		t.Fatalf("edge config: %+v", e)
	}
	if _, err := c.SetRoutes(id, []Route{{Path: "/api", Port: 8080}, {Path: "relative", Port: 1}}); err == nil {
		t.Fatal("invalid route accepted")
	}
	if d, err := c.SetRoutes(id, []Route{{Path: "/api", Port: 8080}}); err != nil || d.Routes[0].Host != "" {
		t.Fatalf("SetRoutes: %+v %v", d, err)
	}
	if err := c.SetEdge(80, 80); err == nil {
		t.Fatal("identical edge ports accepted")
	}
	plain, _ := fixture(t)
	if err := plain.SetEdge(80, 0); err == nil {
		t.Fatal("edge enabled without the cluster network")
	}
}

func TestNetworkStatusRefreshedPeriodically(t *testing.T) {
	c, nets := networkFixture(t)
	c.Refresh(context.Background())
	before := len(nets["w1"].configs)
	c.Refresh(context.Background())
	if len(nets["w1"].configs) != before {
		t.Fatal("fresh, unchanged config re-pushed")
	}
	if err := c.Store.update(func(s *State) error {
		m := s.Network.Members["w1"]
		m.UpdatedAt = time.Now().Add(-networkStatusRefresh - time.Second)
		s.Network.Members["w1"] = m
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.Refresh(context.Background())
	if len(nets["w1"].configs) != before+1 {
		t.Fatal("stale status not refreshed")
	}
}
