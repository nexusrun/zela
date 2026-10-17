package control

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zela-dev/zela/internal/sshclient"
)

// This backend models dedicated PostgreSQL sessions: only one owns the lock,
// disconnection releases it, and old sessions cannot write after takeover.
type haTestDatabase struct {
	mu        sync.Mutex
	documents map[string][]byte
	owner     *haTestConnection
	attempts  int
	readOnly  bool
}
type haTestConnection struct {
	postgresConnection
	shared *haTestDatabase
	data   *testDatabase
	closed bool
}
type haTestTransaction struct {
	pgx.Tx
	conn  *haTestConnection
	inner *testTransaction
}

func (db *haTestDatabase) open(context.Context) (*postgresStore, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.attempts++
	if db.owner != nil {
		return nil, ErrClusterOwned
	}
	d := &testDatabase{documents: db.documents, version: 1}
	conn := &haTestConnection{shared: db, data: d}
	db.owner = conn
	p := testBackend(d)
	p.conn = conn
	p.enableHA()
	return p, nil
}
func (c *haTestConnection) valid(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.closed || c.shared.owner != c {
		return errors.New("session lost")
	}
	return nil
}
func (c *haTestConnection) Ping(ctx context.Context) error {
	c.shared.mu.Lock()
	defer c.shared.mu.Unlock()
	return c.valid(ctx)
}

type haBoolRow struct {
	value bool
	err   error
}

func (r haBoolRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*dest[0].(*bool) = r.value
	return nil
}
func (c *haTestConnection) QueryRow(ctx context.Context, _ string, _ ...any) pgx.Row {
	c.shared.mu.Lock()
	defer c.shared.mu.Unlock()
	return haBoolRow{value: !c.shared.readOnly, err: c.valid(ctx)}
}
func (c *haTestConnection) Close(context.Context) error {
	c.shared.mu.Lock()
	defer c.shared.mu.Unlock()
	c.closed = true
	if c.shared.owner == c {
		c.shared.owner = nil
	}
	return nil
}
func (c *haTestConnection) Exec(ctx context.Context, q string, args ...any) (pgconn.CommandTag, error) {
	c.shared.mu.Lock()
	defer c.shared.mu.Unlock()
	if err := c.valid(ctx); err != nil {
		return pgconn.CommandTag{}, err
	}
	return c.data.Exec(ctx, q, args...)
}
func (c *haTestConnection) Begin(ctx context.Context) (pgx.Tx, error) {
	c.shared.mu.Lock()
	defer c.shared.mu.Unlock()
	if err := c.valid(ctx); err != nil {
		return nil, err
	}
	return &haTestTransaction{conn: c, inner: &testTransaction{db: c.data, pending: map[string][]byte{}, version: 1}}, nil
}
func (tx *haTestTransaction) Exec(ctx context.Context, q string, args ...any) (pgconn.CommandTag, error) {
	tx.conn.shared.mu.Lock()
	defer tx.conn.shared.mu.Unlock()
	if err := tx.conn.valid(ctx); err != nil {
		return pgconn.CommandTag{}, err
	}
	return tx.inner.Exec(ctx, q, args...)
}
func (tx *haTestTransaction) QueryRow(ctx context.Context, q string, args ...any) pgx.Row {
	return testRow{1}
}
func (tx *haTestTransaction) Commit(ctx context.Context) error {
	tx.conn.shared.mu.Lock()
	defer tx.conn.shared.mu.Unlock()
	if err := tx.conn.valid(ctx); err != nil {
		return err
	}
	return tx.inner.Commit(ctx)
}
func (tx *haTestTransaction) Rollback(context.Context) error { return nil }
func (db *haTestDatabase) disconnect() {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.owner != nil {
		db.owner.closed = true
		db.owner = nil
	}
}
func (db *haTestDatabase) cluster() State {
	db.mu.Lock()
	defer db.mu.Unlock()
	var s State
	_ = json.Unmarshal(db.documents["cluster"], &s)
	return s
}
func haTestConfig(t *testing.T, id string) HAConfig {
	t.Helper()
	t.Setenv("ZELA_VAULT_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	return HAConfig{DataDir: t.TempDir(), DatabaseURL: "postgres://unused/test", ManagerID: id, HTTP: HTTPConfig{Token: testToken, AllowHTTPWorkers: true}}
}
func haTestManager(t *testing.T, cfg HAConfig, db *haTestDatabase) *HAManager {
	t.Helper()
	h, err := NewHAManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.openDatabase = db.open
	h.retryInterval = 10 * time.Millisecond
	h.heartbeatInterval = 20 * time.Millisecond
	h.checkTimeout = time.Second
	h.takeoverDelay = 0
	t.Cleanup(h.Close)
	return h
}
func waitHA(t *testing.T, h *HAManager, ready bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if h.Status().Ready == ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("HA state did not converge: %+v", h.Status())
}
func haController(t *testing.T, h *HAManager) *Controller {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.generation == nil {
		t.Fatal("missing leader generation")
	}
	return h.generation.controller
}

func TestHAManagerElectionAndGatewayFailover(t *testing.T) {
	base, _, one, two := clusterFixture(t)
	db := &haTestDatabase{documents: map[string][]byte{}}
	cfgA, cfgB := haTestConfig(t, "manager-a"), haTestConfig(t, "manager-b")
	configure := func(c *Controller) error { c.Factory = base.Factory; return nil }
	cfgA.Configure = configure
	cfgB.Configure = configure
	a, b := haTestManager(t, cfgA, db), haTestManager(t, cfgB, db)
	clientA := &http.Client{Transport: pipeHTTPServer(t, a), Timeout: 10 * time.Second}
	clientB := &http.Client{Transport: pipeHTTPServer(t, b), Timeout: 10 * time.Second}
	a.Start(context.Background())
	waitHA(t, a, true)
	b.Start(context.Background())
	c := haController(t, a)
	if err := c.Store.update(func(s *State) error { s.Workers = base.Store.snapshot().Workers; return nil }); err != nil {
		t.Fatal(err)
	}
	gatewayEndpointCall(t, clientB, "http://control", testToken, "", "GET", "/api/health", nil, 200)
	gatewayEndpointCall(t, clientB, "http://control", testToken, "", "GET", "/api/ready", nil, 503)
	gatewayEndpointCall(t, clientB, "http://control", testToken, "w2", "POST", "/containers/create", map[string]string{"Image": "alpine"}, 503)
	if b.Status().Ready {
		t.Fatal("standby accepted leadership while lock was held")
	}
	var issued struct {
		Token    string
		Metadata AccessTokenMetadata
	}
	json.Unmarshal(gatewayEndpointCall(t, clientA, "http://control", testToken, "", "POST", "/api/tokens", map[string]any{"name": "Zela HA", "scope": "infrastructure", "expiresInDays": 90}, 201), &issued)
	var created struct {
		ID string `json:"Id"`
	}
	json.Unmarshal(gatewayEndpointCall(t, clientA, "http://control", issued.Token, "w2", "POST", "/containers/create?name=ha-existing", map[string]string{"Image": "alpine"}, 201), &created)
	gatewayEndpointCall(t, clientA, "http://control", issued.Token, "", "POST", "/containers/"+created.ID+"/start", nil, 204)
	public := gatewayEndpointCall(t, clientA, "http://control", testToken, "", "GET", "/api/state", nil, 200)
	if bytes.Contains(public, []byte("haConfigurationHash")) || !bytes.Contains(public, []byte(`"managerID":"manager-a"`)) {
		t.Fatal("invalid HA public state")
	}
	// Stop the active manager; the standby must reload credentials, ownership,
	// reservations, and worker transports instead of using stale in-memory state.
	a.Close()
	waitHA(t, b, true)
	state := db.cluster()
	if state.DockerContainers[created.ID].WorkerID != "w2" || state.DockerContainers[created.ID].AccessTokenID != issued.Metadata.ID {
		t.Fatal("failover lost container ownership")
	}
	gatewayEndpointCall(t, clientB, "http://control", issued.Token, "", "GET", "/api/cluster", nil, 200)
	gatewayEndpointCall(t, clientB, "http://control", issued.Token, "", "GET", "/containers/"+created.ID+"/json", nil, 200)
	gatewayEndpointCall(t, clientB, "http://control", issued.Token, "", "POST", "/containers/"+created.ID+"/stop", nil, 204)
	gatewayEndpointCall(t, clientB, "http://control", issued.Token, "", "DELETE", "/containers/"+created.ID, nil, 204)
	one.mu.Lock()
	if len(one.containers) != 0 {
		t.Error("container was moved during manager failover")
	}
	one.mu.Unlock()
	two.mu.Lock()
	if len(two.containers) != 0 {
		t.Error("container deletion did not reach persisted owner")
	}
	two.mu.Unlock()
	if _, ok := db.cluster().DockerContainers[created.ID]; ok {
		t.Fatal("reservation not released after failover")
	}
	gatewayEndpointCall(t, clientA, "http://control", testToken, "", "GET", "/api/ready", nil, 503)
}

func TestHALostSessionFencesRequestsAndCancelsStreams(t *testing.T) {
	daemon := newSimulatedDocker("one")
	started, canceled := make(chan struct{}), make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/logs") {
			w.WriteHeader(200)
			_, _ = w.Write([]byte("first\n"))
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
			close(canceled)
			return
		}
		daemon.ServeHTTP(w, r)
	})
	base, _ := gatewayHTTPStack(t, map[string]http.RoundTripper{"w1": pipeHTTPServer(t, handler)})
	base.Refresh(context.Background())
	db := &haTestDatabase{documents: map[string][]byte{}}
	cfg := haTestConfig(t, "manager-stream")
	cfg.Configure = func(c *Controller) error { c.Factory = base.Factory; return nil }
	h := haTestManager(t, cfg, db)
	// Slow automatic checks prove that request/worker guards independently detect
	// loss instead of waiting for periodic polling.
	h.heartbeatInterval = time.Minute
	h.Start(context.Background())
	waitHA(t, h, true)
	c := haController(t, h)
	if err := c.Store.update(func(s *State) error { s.Workers = base.Store.snapshot().Workers; return nil }); err != nil {
		t.Fatal(err)
	}
	raw, _ := issueInfrastructureToken(t, c)
	client := &http.Client{Transport: pipeHTTPServer(t, h), Timeout: 10 * time.Second}
	var created struct {
		ID string `json:"Id"`
	}
	json.Unmarshal(gatewayEndpointCall(t, client, "http://control", raw, "w1", "POST", "/containers/create?name=stream-ha", map[string]string{"Image": "alpine"}, 201), &created)
	req, _ := http.NewRequest("GET", "http://control/containers/"+created.ID+"/logs?follow=1", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	<-started
	db.disconnect()
	gatewayEndpointCall(t, client, "http://control", raw, "w1", "POST", "/containers/create?name=must-not-dispatch", map[string]string{"Image": "alpine"}, 503)
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("leadership loss left an old gateway stream active")
	}
	_, _ = io.ReadAll(resp.Body)
	daemon.mu.Lock()
	if _, ok := daemon.containers["one-must-not-dispatch"]; ok {
		t.Error("lost leader dispatched a new mutation")
	}
	daemon.mu.Unlock()
}

func TestHAWriteFailureAndConfigurationMismatch(t *testing.T) {
	db := &haTestDatabase{documents: map[string][]byte{}}
	cfg := haTestConfig(t, "manager-original")
	a := haTestManager(t, cfg, db)
	a.Start(context.Background())
	waitHA(t, a, true)
	c := haController(t, a)
	if err := c.Store.update(func(s *State) error { s.Operations = []Operation{{ID: "interrupted", Status: "running"}}; return nil }); err != nil {
		t.Fatal(err)
	}
	// Credentials must be validated before restart recovery or any state write.
	badCfg := haTestConfig(t, "wrong-credentials")
	badCfg.HTTP.Token = strings.Repeat("wrong", 10)
	bad := haTestManager(t, badCfg, db)
	a.Close()
	bad.Start(context.Background())
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		db.mu.Lock()
		attempts := db.attempts
		db.mu.Unlock()
		if attempts >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if bad.Status().Ready || db.cluster().Operations[0].Status != "running" {
		t.Fatal("mismatched manager modified or served cluster state")
	}
	bad.Close()
	good := haTestManager(t, haTestConfig(t, "replacement"), db)
	good.Start(context.Background())
	waitHA(t, good, true)
	if db.cluster().Operations[0].Status != "failed" {
		t.Fatal("takeover replayed or ignored interrupted jobs")
	}
	c = haController(t, good)
	db.mu.Lock()
	db.owner.data.fail = "cluster"
	db.mu.Unlock()
	if err := c.Store.update(func(s *State) error { s.Workers["bad"] = Worker{ID: "bad"}; return nil }); err == nil {
		t.Fatal("write failure acknowledged")
	}
	if good.Status().Ready {
		t.Fatal("failed persistence retained readiness")
	}
	if _, ok := db.cluster().Workers["bad"]; ok {
		t.Fatal("failed write changed durable state")
	}
	good.Close()
}

func TestHAConfigurationAndRemoteWorkers(t *testing.T) {
	cfg := haTestConfig(t, "manager-config")
	for _, change := range []string{"database", "token", "id", "delay", "vault"} {
		t.Run(change, func(t *testing.T) {
			c := cfg
			switch change {
			case "database":
				c.DatabaseURL = ""
			case "token":
				c.HTTP.Token = ""
			case "id":
				c.ManagerID = "invalid id"
			case "delay":
				c.TakeoverDelay = time.Second
			case "vault":
				t.Setenv("ZELA_VAULT_KEY", "")
			}
			if _, err := NewHAManager(c); err == nil {
				t.Fatal("invalid HA configuration accepted")
			}
		})
	}
	for _, endpoint := range []string{"unix:///var/run/docker.sock", "http://127.0.0.1:9091", "https://localhost.:9091", "http://[::1]:9091", "http://0.0.0.0:9091"} {
		if err := validateHAWorker(Worker{Endpoint: endpoint}); err == nil {
			t.Fatalf("local endpoint accepted: %s", endpoint)
		}
	}
	if err := validateHAWorker(Worker{SSH: &sshclient.Config{Host: "localhost"}}); err == nil {
		t.Fatal("local SSH accepted")
	}
	for _, w := range []Worker{{Endpoint: "https://worker.example:9091"}, {SSH: &sshclient.Config{Host: "192.0.2.10"}}} {
		if err := validateHAWorker(w); err != nil {
			t.Fatal(err)
		}
	}
	db := &haTestDatabase{documents: map[string][]byte{}}
	h := haTestManager(t, cfg, db)
	h.Start(context.Background())
	waitHA(t, h, true)
	c := haController(t, h)
	tokenAPICall(t, h, testToken, "POST", "/api/workers", `{"name":"local","endpoint":"unix:///var/run/docker.sock"}`, 400)
	if _, err := c.AddWorker(Worker{Name: "local", Endpoint: "http://localhost:9091"}); err == nil {
		t.Fatal("controller allowed a local HA worker")
	}
	if err := h.validateState(&State{Workers: map[string]Worker{"local": {Endpoint: "unix:///docker.sock"}}}); err == nil {
		t.Fatal("existing local worker was accepted")
	}
	h.Close()
	// HA state cannot be accidentally reopened through the uncoordinated mode.
	if _, err := openStore(cfg.DataDir, false, func() (*postgresStore, error) {
		p, err := db.open(context.Background())
		if err == nil {
			p.ha = false
		}
		return p, err
	}); err == nil {
		t.Fatal("HA cluster reopened without HA mode")
	}
}

func TestHABlockedPersistenceProbeFailsClosed(t *testing.T) {
	db := &haTestDatabase{documents: map[string][]byte{}}
	h := haTestManager(t, haTestConfig(t, "blocked-session"), db)
	h.Start(context.Background())
	waitHA(t, h, true)
	p := haController(t, h).Store.database
	p.mu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := p.checkContext(ctx)
	cancel()
	if err == nil {
		p.mu.Unlock()
		t.Fatal("blocked persistence probe succeeded")
	}
	// One blocked probe is tolerated; staying blocked past the readiness
	// window must fence leadership.
	deadline := time.Now().Add(5 * time.Second)
	for !p.fenced() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	p.mu.Unlock()
	if !p.fenced() || h.Status().Ready {
		t.Fatal("persistently blocked persistence probe did not fence leadership")
	}
	h.Close()
}

func TestReadinessWithoutHA(t *testing.T) {
	c, _ := fixture(t)
	h := Handler(c, HTTPConfig{Token: testToken})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/ready", nil))
	if w.Code != 200 {
		t.Fatal("single manager is not ready")
	}
	c.cancel()
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/ready", nil))
	if w.Code != 503 {
		t.Fatal("stopped manager stayed ready")
	}
}

func TestHAReadOnlyPrimaryFencesWorkerDispatch(t *testing.T) {
	base, _, one, _ := clusterFixture(t)
	db := &haTestDatabase{documents: map[string][]byte{}}
	cfg := haTestConfig(t, "primary-check")
	cfg.Configure = func(c *Controller) error { c.Factory = base.Factory; return nil }
	h := haTestManager(t, cfg, db)
	h.Start(context.Background())
	waitHA(t, h, true)
	c := haController(t, h)
	if err := c.Store.update(func(s *State) error { s.Workers = base.Store.snapshot().Workers; return nil }); err != nil {
		t.Fatal(err)
	}
	raw, _ := issueInfrastructureToken(t, c)
	one.mu.Lock()
	before := one.requests
	one.mu.Unlock()
	db.mu.Lock()
	db.readOnly = true
	db.mu.Unlock()
	client := &http.Client{Transport: pipeHTTPServer(t, h), Timeout: 10 * time.Second}
	gatewayEndpointCall(t, client, "http://control", raw, "w1", "POST", "/containers/create?name=read-only-reject", map[string]string{"Image": "alpine"}, 503)
	one.mu.Lock()
	if one.requests != before {
		t.Error("read-only session contacted the worker")
	}
	one.mu.Unlock()
	if h.Status().Ready {
		t.Fatal("read-only primary remained ready")
	}
	h.Close()
}
