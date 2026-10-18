package control

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zela-dev/zela/internal/engine"
)

func TestNetworkConsoleConfigurationAndReapply(t *testing.T) {
	c, nets := networkFixture(t)
	c.Refresh(context.Background())
	op, err := c.ConfigureNetwork(NetworkConfiguration{Enabled: true, CIDR: "10.200.0.0/16", Port: 51820, EdgeHTTPPort: 8080})
	succeeded(t, c, op, err)
	n := c.Store.snapshot().Network
	if !n.ConsoleManaged || n.EdgeHTTPPort != 8080 {
		t.Fatalf("configuration not saved: %+v", n)
	}
	if err = c.ApplyStartupNetwork(NetworkConfiguration{}, false); err != nil {
		t.Fatal(err)
	}
	if !c.Store.snapshot().Network.Enabled {
		t.Fatal("restart disabled console-managed network")
	}
	p := &SSHProvisioner{NetworkPort: 123, EdgePorts: []int{1234}}
	c.Provisioner = p
	copy := c.networkProvisioner().(*SSHProvisioner)
	if copy.NetworkPort != 51820 || copy.EdgePorts[0] != 8080 || p.NetworkPort != 123 {
		t.Fatal("installer did not use live desired state safely")
	}
	op, err = c.UpdateNetworkEndpoint("w2", "other-peer.example.net")
	succeeded(t, c, op, err)
	if peer := nets["w1"].last().Peers[0].Endpoint; peer != "other-peer.example.net:51820" {
		t.Fatal(peer)
	}
	before := len(nets["w1"].configs)
	op, err = c.ReapplyNetwork("w1")
	succeeded(t, c, op, err)
	if len(nets["w1"].configs) <= before {
		t.Fatal("reapply did not reach worker")
	}
	if _, err = c.ConfigureNetwork(NetworkConfiguration{Enabled: true, CIDR: "10.201.0.0/16", Port: 51820}); err == nil {
		t.Fatal("active mesh range changed")
	}
	if _, err = c.ConfigureNetwork(NetworkConfiguration{Enabled: true, CIDR: n.CIDR, Port: 51821}); err == nil {
		t.Fatal("active tunnel port changed")
	}
	if _, err = c.UpdateNetworkEndpoint("w1", "https://evil.example/path"); err == nil {
		t.Fatal("invalid peer address accepted")
	}
	if err = c.ApplyStartupNetwork(NetworkConfiguration{Enabled: true, CIDR: n.CIDR, Port: 51820, EdgeHTTPPort: 9090}, true); err != nil {
		t.Fatal(err)
	}
	if c.Store.snapshot().Network.ConsoleManaged || c.Store.snapshot().Network.EdgeHTTPPort != 9090 {
		t.Fatal("explicit startup configuration did not override console")
	}
}
func TestNetworkConsoleFailureAndDisableProtection(t *testing.T) {
	c, nets := networkFixture(t)
	c.Refresh(context.Background())
	d := deployOne(t, c)
	if _, err := c.ConfigureNetwork(NetworkConfiguration{Enabled: false}); err == nil {
		t.Fatal("disabled mesh with managed replicas")
	}
	op, err := c.DeploymentAction(d.ID, "delete", "", 0)
	succeeded(t, c, op, err)
	nets["w2"].fail = errFailNetwork{}
	op, err = c.ReapplyNetwork("")
	if result := waitOp(t, c, op, err); result.Status != "failed" || !strings.Contains(result.Detail, "desired configuration saved") {
		t.Fatal(result)
	}
	if c.Store.snapshot().Network.Members["w2"].Error == "" {
		t.Fatal("worker apply error lost")
	}
	nets["w2"].fail = nil
	op, err = c.ConfigureNetwork(NetworkConfiguration{Enabled: false})
	succeeded(t, c, op, err)
	if c.Store.snapshot().Network != nil {
		t.Fatal("completed teardown retained network")
	}
}

type errFailNetwork struct{}

func (errFailNetwork) Error() string { return "network capabilities missing" }

func TestWorkerDockerNetworkLifecycleAndGuards(t *testing.T) {
	c, engines := fixture(t)
	ctx := context.Background()
	standalone, err := engines["w1"].Create(ctx, engine.Spec{Name: "standalone", Image: "alpine"})
	if err != nil {
		t.Fatal(err)
	}
	c.Refresh(ctx)
	op, err := c.CreateDockerNetwork("w1", engine.NetworkCreate{Name: "application", Subnet: "172.28.0.0/24", Gateway: "172.28.0.1", Internal: true})
	succeeded(t, c, op, err)
	networks, err := engines["w1"].Networks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, n := range networks {
		if n.Name == "application" {
			id = n.ID
		}
	}
	if id == "" {
		t.Fatal("created network missing")
	}
	op, err = c.DockerNetworkAction("w1", id, "connect", standalone)
	succeeded(t, c, op, err)
	network, err := engines["w1"].InspectNetwork(ctx, id)
	if err != nil || len(network.Containers) != 1 {
		t.Fatal(network, err)
	}
	op, err = c.DockerNetworkAction("w1", id, "delete", "")
	if result := waitOp(t, c, op, err); result.Status != "failed" {
		t.Fatal("removed a connected network")
	}
	op, err = c.DockerNetworkAction("w1", id, "disconnect", standalone)
	succeeded(t, c, op, err)
	op, err = c.DockerNetworkAction("w1", id, "delete", "")
	succeeded(t, c, op, err)
	for _, name := range []string{"bridge", "zela", "host", "none", "bad/name"} {
		if _, err := c.CreateDockerNetwork("w1", engine.NetworkCreate{Name: name}); err == nil {
			t.Fatalf("reserved/invalid network %q accepted", name)
		}
	}
	op, err = c.DockerNetworkAction("w1", "demo-network-bridge", "delete", "")
	if result := waitOp(t, c, op, err); result.Status != "failed" {
		t.Fatal("modified built-in network")
	}
	d := deployOne(t, c)
	managedWorker := d.Tasks[0].WorkerID
	op, err = c.CreateDockerNetwork(managedWorker, engine.NetworkCreate{Name: "isolated"})
	succeeded(t, c, op, err)
	networks, _ = engines[managedWorker].Networks(ctx)
	for _, n := range networks {
		if n.Name == "isolated" {
			id = n.ID
		}
	}
	op, err = c.DockerNetworkAction(d.Tasks[0].WorkerID, id, "connect", d.Tasks[0].ID)
	if result := waitOp(t, c, op, err); result.Status != "failed" || !strings.Contains(result.Detail, "managed") {
		t.Fatal("changed declared network of managed container")
	}
	for _, subnet := range []string{"172.16.0.0/8", "192.168.0.0/8", "8.8.8.0/24", "not-a-subnet"} {
		if _, err := c.CreateDockerNetwork("w1", engine.NetworkCreate{Name: "bad-subnet", Subnet: subnet}); err == nil {
			t.Fatalf("invalid private range %s accepted", subnet)
		}
	}
}

func TestNetworkAdministrationHTTPAuthorizationAndRoutes(t *testing.T) {
	c, _ := fixture(t)
	h := Handler(c, HTTPConfig{Token: testToken})
	routes := []struct{ method, path, body string }{{"GET", "/api/network", ""}, {"GET", "/api/workers/w1/networks", ""}, {"POST", "/api/network/reapply", `{}`}, {"POST", "/api/workers/w1/network-endpoint", `{"endpoint":"w1.example.net"}`}, {"POST", "/api/workers/w1/networks", `{"name":"test"}`}, {"POST", "/api/workers/w1/networks/bridge/actions", `{"action":"delete"}`}}
	for _, scope := range []string{"infrastructure", "deployments", "mcp_read", "mcp_manage"} {
		raw, _, err := c.Store.createAccessToken("network denial", 1, scope)
		if err != nil {
			t.Fatal(err)
		}
		for _, route := range routes {
			tokenAPICall(t, h, raw, route.method, route.path, route.body, 403)
		}
	}
	tokenAPICall(t, h, "", "GET", "/api/network", "", 401)
	tokenAPICall(t, h, testToken, "GET", "/api/network", "", 200)
	tokenAPICall(t, h, testToken, "GET", "/api/workers/w1/networks", "", 200)
	tokenAPICall(t, h, testToken, "GET", "/api/workers/missing/networks", "", 404)
	tokenAPICall(t, h, testToken, "POST", "/api/workers/w1/networks", `{"name":"test","driver":"host"}`, 400)
	r := httptest.NewRequest("POST", "http://control.local/api/workers/w1/networks", strings.NewReader(`{"name":"test"}`))
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin network mutation accepted")
	}
	d := deployOne(t, c)
	body := `{"routes":[{"host":"app.example.com","path":"/","port":80}],"expectedRoutes":[]}`
	tokenAPICall(t, h, testToken, "PUT", "/api/deployments/"+d.ID+"/routes", body, 200)
	tokenAPICall(t, h, testToken, "PUT", "/api/deployments/"+d.ID+"/routes", `{"routes":[],"expectedRoutes":[]}`, 400)
	if len(c.Store.snapshot().Deployments[d.ID].Routes) != 1 {
		t.Fatal("stale editor overwrote another route change")
	}
}
func TestNetworkAdministrationStatusDoesNotExposePrivateKeys(t *testing.T) {
	c, _ := networkFixture(t)
	c.Refresh(context.Background())
	h := Handler(c, HTTPConfig{Token: testToken})
	data := tokenAPICall(t, h, testToken, "GET", "/api/network", "", 200)
	var out struct {
		Members map[string]struct {
			Status string `json:"status"`
		} `json:"members"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.Members["w1"].Status != "applied" {
		t.Fatalf("bad membership status: %s %v", data, err)
	}
	for _, key := range []string{`"keyPEM"`, `"accountKey"`, `"privateKey"`, `"token"`} {
		if strings.Contains(string(data), key) {
			t.Fatal("sensitive data in network view")
		}
	}
	_ = c.Store.update(func(s *State) error { n := s.Network; n.EdgeHTTPPort = 8080; return nil })
	data = tokenAPICall(t, h, testToken, "GET", "/api/network", "", 200)
	_ = json.Unmarshal(data, &out)
	if out.Members["w1"].Status != "pending" {
		t.Fatal("stale applied hash shown as current")
	}
}
