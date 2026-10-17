package control

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zela-dev/zela/internal/workerproxy"
)

type haProcessLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *haProcessLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(b)
}
func (l *haProcessLog) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.buf.String() }

type haTestProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	log  *haProcessLog
}

func startHAProcess(t *testing.T, binary string, args []string, env []string) *haTestProcess {
	t.Helper()
	p := &haTestProcess{cmd: exec.Command(binary, args...), done: make(chan struct{}), log: &haProcessLog{}}
	p.cmd.Env = env
	p.cmd.Stdout = p.log
	p.cmd.Stderr = p.log
	if err := p.cmd.Start(); err != nil {
		t.Fatal("could not start HA test process", err)
	}
	go func() { _ = p.cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("Process %s: %s", filepath.Base(binary), p.log.String())
		}
	})
	t.Cleanup(func() {
		_ = p.cmd.Process.Kill()
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
			t.Error("HA test process did not stop")
		}
	})
	return p
}
func haFreeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}
func waitHAEndpoint(t *testing.T, client *http.Client, endpoint string, p *haTestProcess) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(endpoint + "/api/ready")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		select {
		case <-p.done:
			t.Fatalf("HA process exited: %s", p.log.String())
		default:
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("HA endpoint did not become ready: %s", p.log.String())
}

// Opt-in: runs the actual manager executable and HAProxy with real PostgreSQL.
// Worker traffic crosses TCP HTTP servers backed by one simulated Docker engine.
// worker.test must resolve to this test container/host (127.0.0.1 is sufficient).
func TestHAProcessesThroughClusterEndpoint(t *testing.T) {
	managerBinary, proxyBinary := os.Getenv("ZELA_TEST_MANAGER_BINARY"), os.Getenv("ZELA_TEST_HAPROXY_BINARY")
	if managerBinary == "" || proxyBinary == "" {
		t.Skip("set ZELA_TEST_MANAGER_BINARY and ZELA_TEST_HAPROXY_BINARY for process/gateway HA integration")
	}
	dbCfg := haIntegrationDatabase(t)
	if ips, err := net.LookupHost("worker.test"); err != nil || len(ips) == 0 {
		t.Fatal("worker.test must resolve to this isolated test host")
	}
	daemon := newSimulatedDocker("stable-worker")
	docker := httptest.NewServer(daemon)
	defer docker.Close()
	dockerAddress := strings.TrimPrefix(docker.URL, "http://")
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", dockerAddress)
	}}
	defer transport.CloseIdleConnections()
	proxy := workerproxy.DockerProxy(transport)
	workerToken := strings.Repeat("worker-process-test-", 4)
	worker := httptest.NewServer(workerproxy.Handler(workerToken, proxy, proxy))
	defer worker.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(worker.URL, "http://"))
	workerURL := "http://worker.test:" + port
	aPort, bPort, clusterPort := haFreeTCPPort(t), haFreeTCPPort(t), haFreeTCPPort(t)
	env := append(os.Environ(), "ZELA_DATABASE_URL="+haIntegrationDatabaseURL(dbCfg), "ZELA_ADMIN_TOKEN="+testToken, "ZELA_VAULT_KEY="+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	manager := func(id string, port int) *haTestProcess {
		return startHAProcess(t, managerBinary, []string{"--ha", "--manager-id", id, "--listen", fmt.Sprintf("127.0.0.1:%d", port), "--data", t.TempDir(), "--ha-takeover-delay", "5s", "--allow-http-workers"}, env)
	}
	a := manager("process-a", aPort)
	client := &http.Client{Timeout: 3 * time.Second}
	waitHAEndpoint(t, client, fmt.Sprintf("http://127.0.0.1:%d", aPort), a)
	b := manager("process-b", bPort)
	templatePath := os.Getenv("ZELA_TEST_HAPROXY_CONFIG")
	if templatePath == "" {
		t.Fatal("set ZELA_TEST_HAPROXY_CONFIG to deploy/haproxy-ha.cfg")
	}
	template, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	config := strings.ReplaceAll(string(template), "bind :8080", fmt.Sprintf("bind 127.0.0.1:%d", clusterPort))
	config = strings.ReplaceAll(config, "manager-a:8080", fmt.Sprintf("127.0.0.1:%d", aPort))
	config = strings.ReplaceAll(config, "manager-b:8080", fmt.Sprintf("127.0.0.1:%d", bPort))
	config = strings.ReplaceAll(config, "inter 1s", "inter 200ms")
	path := filepath.Join(t.TempDir(), "haproxy.cfg")
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	lb := startHAProcess(t, proxyBinary, []string{"-db", "-f", path}, os.Environ())
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", clusterPort)
	waitHAEndpoint(t, client, endpoint, lb)
	call := func(token, worker, method, path string, body any, want int) []byte {
		return gatewayEndpointCall(t, client, endpoint, token, worker, method, path, body, want)
	}
	var operation Operation
	json.Unmarshal(call(testToken, "", "POST", "/api/workers", map[string]string{"name": "stable-worker", "endpoint": workerURL, "token": workerToken}, 202), &operation)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		json.Unmarshal(call(testToken, "", "GET", "/api/operations/"+operation.ID, nil, 200), &operation)
		if operation.Status == "succeeded" {
			break
		}
		if operation.Status == "failed" {
			t.Fatalf("worker connection failed: %s", operation.Detail)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if operation.Status != "succeeded" {
		t.Fatal("worker registration did not finish")
	}
	var inventory struct{ Workers []InfrastructureWorker }
	json.Unmarshal(call(testToken, "", "GET", "/api/cluster/workers", nil, 200), &inventory)
	if len(inventory.Workers) != 1 {
		t.Fatal("missing worker")
	}
	workerID := inventory.Workers[0].ID
	var issued struct {
		Token    string
		Metadata AccessTokenMetadata
	}
	json.Unmarshal(call(testToken, "", "POST", "/api/tokens", map[string]any{"name": "process Zela", "scope": "infrastructure", "expiresInDays": 30}, 201), &issued)
	var created struct {
		ID string `json:"Id"`
	}
	json.Unmarshal(call(issued.Token, workerID, "POST", "/containers/create?name=process-ha", map[string]string{"Image": "alpine"}, 201), &created)
	call(issued.Token, "", "POST", "/containers/"+created.ID+"/start", nil, 204)
	if err := a.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.done:
	case <-time.After(3 * time.Second):
		t.Fatal("active manager did not terminate")
	}
	waitHAEndpoint(t, client, endpoint, lb)
	// The gateway may briefly report the previous healthy backend before its next
	// check. Wait for the replacement's own readiness as well.
	waitHAEndpoint(t, client, fmt.Sprintf("http://127.0.0.1:%d", bPort), b)
	var cluster InfrastructureView
	json.Unmarshal(call(issued.Token, "", "GET", "/api/cluster", nil, 200), &cluster)
	if cluster.HighAvailability == nil || cluster.HighAvailability.ManagerID != "process-b" {
		t.Fatal("cluster endpoint did not switch to standby")
	}
	var inspection struct{ State struct{ Running bool } }
	json.Unmarshal(call(issued.Token, "", "GET", "/containers/"+created.ID+"/json", nil, 200), &inspection)
	if !inspection.State.Running {
		t.Fatal("manager failover interrupted the existing container")
	}
	call(issued.Token, "", "POST", "/containers/"+created.ID+"/stop", nil, 204)
	call(issued.Token, "", "DELETE", "/containers/"+created.ID, nil, 204)
	daemon.mu.Lock()
	remaining := len(daemon.containers)
	daemon.mu.Unlock()
	if remaining != 0 {
		t.Fatal("stable endpoint did not delete on the owning worker")
	}
	t.Log("Verified actual managers and HAProxy on one endpoint; token, container ID and worker ownership survived active-process termination; cluster endpoint port " + strconv.Itoa(clusterPort))
}
