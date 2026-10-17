package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Creates a separate database; never touches the configured database's state.
func haIntegrationDatabase(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	dsn := os.Getenv("ZELA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set ZELA_TEST_DATABASE_URL for isolated real PostgreSQL HA integration")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(databaseError("test connection", err))
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = admin.Close(ctx)
	})
	name := newID("zela_ha_test_")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(databaseError("test database creation", err))
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(databaseError("test database cleanup", err))
		}
	})
	cfg.Database = name
	return cfg
}

// ConnConfig.ConnString preserves the original parsed text; changing Database
// does not rewrite it. Explicitly select the newly created isolated database.
func haIntegrationDatabaseURL(cfg *pgx.ConnConfig) string {
	raw := cfg.ConnString()
	if u, err := url.Parse(raw); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		u.Path = "/" + cfg.Database
		u.RawPath = ""
		q := u.Query()
		q.Del("dbname")
		q.Del("database")
		u.RawQuery = q.Encode()
		return u.String()
	}
	return raw + " dbname='" + strings.ReplaceAll(strings.ReplaceAll(cfg.Database, `\`, `\\`), "'", `\'`) + "'"
}

func TestHAPostgresIntegration(t *testing.T) {
	dbCfg := haIntegrationDatabase(t)
	base, _, one, two := clusterFixture(t)
	newManager := func(id string) *HAManager {
		cfg := haTestConfig(t, id)
		cfg.DatabaseURL = haIntegrationDatabaseURL(dbCfg)
		cfg.Configure = func(c *Controller) error { c.Factory = base.Factory; return nil }
		h, err := NewHAManager(cfg)
		if err != nil {
			t.Fatal(err)
		}
		h.retryInterval = 20 * time.Millisecond
		h.heartbeatInterval = 50 * time.Millisecond
		h.checkTimeout = 3 * time.Second
		h.takeoverDelay = 0
		t.Cleanup(h.Close)
		return h
	}
	a, b := newManager("sql-manager-a"), newManager("sql-manager-b")
	clientA := &http.Client{Transport: pipeHTTPServer(t, a), Timeout: 10 * time.Second}
	clientB := &http.Client{Transport: pipeHTTPServer(t, b), Timeout: 10 * time.Second}
	a.Start(context.Background())
	waitHA(t, a, true)
	b.Start(context.Background())
	c := haController(t, a)
	if err := c.Store.update(func(s *State) error { s.Workers = base.Store.snapshot().Workers; return nil }); err != nil {
		t.Fatal(err)
	}
	var issued struct {
		Token    string
		Metadata AccessTokenMetadata
	}
	json.Unmarshal(gatewayEndpointCall(t, clientA, "http://control", testToken, "", "POST", "/api/tokens", map[string]any{"name": "real SQL Zela", "scope": "infrastructure", "expiresInDays": 90}, 201), &issued)
	gatewayEndpointCall(t, clientB, "http://control", issued.Token, "", "GET", "/api/ready", nil, 503)
	var created struct {
		ID string `json:"Id"`
	}
	json.Unmarshal(gatewayEndpointCall(t, clientA, "http://control", issued.Token, "w2", "POST", "/containers/create?name=real-sql-ha", map[string]string{"Image": "alpine"}, 201), &created)
	gatewayEndpointCall(t, clientA, "http://control", issued.Token, "", "POST", "/containers/"+created.ID+"/start", nil, 204)
	// Terminate only this test leader's SQL session, on this test database.
	p := c.Store.database
	p.mu.Lock()
	var pid int
	err := p.conn.QueryRow(context.Background(), "SELECT pg_catalog.pg_backend_pid()").Scan(&pid)
	p.mu.Unlock()
	if err != nil {
		t.Fatal(databaseError("test session lookup", err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, dbCfg.Copy())
	if err != nil {
		t.Fatal(databaseError("test connection", err))
	}
	defer admin.Close(ctx)
	var killed bool
	if err := admin.QueryRow(ctx, "SELECT pg_catalog.pg_terminate_backend($1)", pid).Scan(&killed); err != nil || !killed {
		t.Fatal("failed to terminate isolated test leader session")
	}
	if err := c.Store.CheckPersistence(); err == nil {
		t.Fatal("terminated session retained cluster leadership")
	}
	a.Close()
	waitHA(t, b, true)
	bController := haController(t, b)
	owner := bController.Store.snapshot().DockerContainers[created.ID]
	if owner.WorkerID != "w2" || owner.AccessTokenID != issued.Metadata.ID {
		t.Fatal("real SQL takeover lost ownership")
	}
	gatewayEndpointCall(t, clientB, "http://control", issued.Token, "", "GET", "/containers/"+created.ID+"/json", nil, 200)
	gatewayEndpointCall(t, clientB, "http://control", issued.Token, "", "POST", "/containers/"+created.ID+"/stop", nil, 204)
	gatewayEndpointCall(t, clientB, "http://control", issued.Token, "", "DELETE", "/containers/"+created.ID, nil, 204)
	if bytes.Contains(gatewayEndpointCall(t, clientB, "http://control", testToken, "", "GET", "/api/state", nil, 200), []byte("haConfigurationHash")) {
		t.Fatal("private HA credential hash exposed")
	}
	one.mu.Lock()
	if len(one.containers) != 0 {
		t.Error("failover relocated the container")
	}
	one.mu.Unlock()
	two.mu.Lock()
	if len(two.containers) != 0 {
		t.Error("takeover did not route deletion to owner")
	}
	two.mu.Unlock()
	// A healthy session with an explicitly released lock must also be fenced.
	p = bController.Store.database
	p.mu.Lock()
	_, err = p.conn.Exec(ctx, "SELECT pg_catalog.pg_advisory_unlock(1129598032, 1)")
	p.mu.Unlock()
	if err != nil {
		t.Fatal(databaseError("test lock release", err))
	}
	if err := bController.Store.CheckPersistence(); err == nil {
		t.Fatal("unlocked session retained leadership")
	}
	b.Close()
}

func TestHAIntegrationDatabaseURLIsolation(t *testing.T) {
	for _, raw := range []string{"postgres://test_user:test_password@localhost/postgres?sslmode=disable", "postgres://test_user@localhost/postgres?dbname=original&sslmode=disable", "host=localhost user=test_user dbname=original password=test_password sslmode=disable"} {
		cfg, err := pgx.ParseConfig(raw)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Database = "zela_isolated_test"
		actual, err := pgx.ParseConfig(haIntegrationDatabaseURL(cfg))
		if err != nil || actual.Database != cfg.Database || actual.Host != cfg.Host || actual.User != cfg.User || actual.Password != cfg.Password {
			t.Fatal("integration URL did not preserve connection settings and isolate the database")
		}
	}
}
