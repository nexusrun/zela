package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNotificationsAlertLifecycleOperationsAndRetention(t *testing.T) {
	m, err := OpenMonitorStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s := monitoringState(monitoredWorker(now))
	rules := defaultAlertRules()
	rules.ResourceForSeconds = 0
	if err := m.SetRules(rules); err != nil {
		t.Fatal(err)
	}
	recordMonitor(t, m, s, now)
	items, unread := m.Notifications()
	if len(items) != 1 || unread != 1 || items[0].Kind != "alert-opened" || items[0].AlertID == "" {
		t.Fatalf("missing opening notification: %+v", items)
	}
	first := items[0]
	recordMonitor(t, m, s, now)
	if all, _ := m.Notifications(); len(all) != 1 {
		t.Fatal("repeated poll duplicated an alert notification")
	}
	if err := m.ReadNotifications([]string{first.ID}, now); err != nil {
		t.Fatal(err)
	}
	alerts, _ := m.Public()
	if alerts[0].Acknowledged {
		t.Fatal("reading a notification acknowledged its alert")
	}
	if _, unread = m.Notifications(); unread != 0 {
		t.Fatal("read count incorrect")
	}
	w := s.Workers["w"]
	low := 10.0
	w.Metrics.Host.CPUPercent = &low
	s.Workers["w"] = w
	recordMonitor(t, m, s, now.Add(time.Second))
	items, unread = m.Notifications()
	if len(items) != 2 || unread != 1 || items[0].Kind != "alert-resolved" {
		t.Fatal("recovery notification missing")
	}
	high := 95.0
	w.Metrics.Host.CPUPercent = &high
	s.Workers["w"] = w
	recordMonitor(t, m, s, now.Add(2*time.Second))
	if items, _ = m.Notifications(); len(items) != 3 {
		t.Fatal("recurrence missing")
	}
	s.Operations = []Operation{{ID: "op", Title: "Deploy application", Status: "running"}}
	recordMonitor(t, m, s, now.Add(3*time.Second))
	if items, _ = m.Notifications(); len(items) != 3 {
		t.Fatal("running operation notified prematurely")
	}
	s.Operations[0].Status = "failed"
	s.Operations[0].Detail = "private worker credential"
	recordMonitor(t, m, s, now.Add(4*time.Second))
	items, _ = m.Notifications()
	if len(items) != 4 || items[0].OperationID != "op" || items[0].Kind != "operation-failed" || strings.Contains(items[0].Message, "credential") {
		t.Fatal("operation failure notification missing or leaked raw output")
	}
	recordMonitor(t, m, s, now.Add(5*time.Second))
	if items, _ = m.Notifications(); len(items) != 4 {
		t.Fatal("operation notification duplicated")
	}
	if err := m.mutate(func() error {
		for i := 0; i < 600; i++ {
			m.addNotification(Notification{EventKey: fmt.Sprint("retention", i)}, now.Add(6*time.Second))
		}
		m.pruneNotifications(now.Add(6 * time.Second))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if items, _ = m.Notifications(); len(items) != 500 {
		t.Fatal("notification retention is unbounded")
	}
	if err := m.mutate(func() error { m.pruneNotifications(now.Add(8 * 24 * time.Hour)); return nil }); err != nil {
		t.Fatal(err)
	}
	if items, _ = m.Notifications(); len(items) != 0 {
		t.Fatal("expired notification history retained")
	}
}
func TestNotificationsPersistenceUpgradeAndFailedWrites(t *testing.T) {
	for _, backend := range []string{"json", "postgresql"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			db := &testDatabase{documents: map[string][]byte{}}
			open := func() (*Store, error) {
				if backend == "json" {
					return OpenStore(dir, false)
				}
				db.closed = false
				return openStore(dir, false, func() (*postgresStore, error) { return testBackend(db), nil })
			}
			s, err := open()
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			state := monitoringState(monitoredWorker(now))
			state.Operations = []Operation{{ID: "op", Title: "Move replica", Status: "succeeded"}}
			rules := defaultAlertRules()
			rules.ResourceForSeconds = 0
			if err := s.Monitor.SetRules(rules); err != nil {
				t.Fatal(err)
			}
			recordMonitor(t, s.Monitor, state, now)
			items, unread := s.Monitor.Notifications()
			if len(items) != 2 || unread != 2 {
				t.Fatal("real cluster events not recorded")
			}
			id := items[0].ID
			if err := s.Monitor.ReadNotifications([]string{id}, now); err != nil {
				t.Fatal(err)
			}
			s.Close()
			s, err = open()
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			items, unread = s.Monitor.Notifications()
			if len(items) != 2 || unread != 1 {
				t.Fatal("notification history/read state lost after restart")
			}
			recordMonitor(t, s.Monitor, state, now.Add(time.Second))
			if items, _ = s.Monitor.Notifications(); len(items) != 2 {
				t.Fatal("restart replayed notification")
			}
			var unreadID string
			for _, n := range items {
				if n.ReadAt == nil {
					unreadID = n.ID
				}
			}
			if err := s.Monitor.ReadNotifications([]string{unreadID, "missing"}, now); err == nil {
				t.Fatal("partial invalid read accepted")
			}
			if _, unread = s.Monitor.Notifications(); unread != 1 {
				t.Fatal("invalid read partially committed")
			}
			if backend == "postgresql" {
				db.fail = "monitoring"
			} else {
				s.Monitor.path = filepath.Join(dir, "missing", "monitoring.json")
			}
			if err := s.Monitor.ReadNotifications([]string{unreadID}, now); err == nil {
				t.Fatal("failed write reported read success")
			}
			if _, unread = s.Monitor.Notifications(); unread != 1 {
				t.Fatal("failed write modified unread state")
			}
			state.Operations = append(state.Operations, Operation{ID: "another", Status: "failed", Title: "Deploy another"})
			if err := s.Monitor.Record(state, now.Add(2*time.Second)); err == nil {
				t.Fatal("failed notification write reported success")
			}
			if items, _ = s.Monitor.Notifications(); len(items) != 2 {
				t.Fatal("failed write exposed an undurable event")
			}
		})
	}
	// Upgrades import actionable alerts, and do not replay old completed jobs.
	m, _ := OpenMonitorStore(t.TempDir())
	now := time.Now().UTC()
	m.data.Alerts = []Alert{{ID: "active", Active: true, Message: "Worker offline", Severity: "critical"}, {ID: "old", Active: false}}
	state := State{Operations: []Operation{{ID: "old-op", Status: "failed"}}}
	if err := m.InitializeNotifications(state, now); err != nil {
		t.Fatal(err)
	}
	items, _ := m.Notifications()
	if len(items) != 1 || items[0].AlertID != "active" {
		t.Fatal("upgrade imported completed history or missed active alerts")
	}
	if err := m.InitializeNotifications(state, now); err != nil {
		t.Fatal(err)
	}
	if items, _ = m.Notifications(); len(items) != 1 {
		t.Fatal("upgrade initialization duplicated notifications")
	}
}
func TestNotificationsAuthenticatedAPIAndRoleIsolation(t *testing.T) {
	c, _ := fixture(t)
	h := Handler(c, HTTPConfig{Token: testToken})
	raw, _ := issueToken(t, c, "deploy-only")
	now := time.Now().UTC()
	if err := c.Store.Monitor.mutate(func() error {
		c.Store.Monitor.addNotification(Notification{EventKey: "test", Kind: "alert-opened", Title: "Worker offline", Message: "worker w1 unreachable"}, now)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	items, _ := c.Store.Monitor.Notifications()
	id := items[0].ID
	for _, route := range []struct{ method, path, body string }{{"GET", "/api/notifications", ""}, {"POST", "/api/notifications/read", `{"ids":["` + id + `"]}`}} {
		tokenAPICall(t, h, "invalid", route.method, route.path, route.body, 401)
		tokenAPICall(t, h, raw, route.method, route.path, route.body, 403)
	}
	b := tokenAPICall(t, h, testToken, "GET", "/api/notifications", "", 200)
	if !bytes.Contains(b, []byte(`"unread":1`)) {
		t.Fatal("notification API has wrong count")
	}
	req := httptest.NewRequest("POST", "http://control/api/notifications/read", strings.NewReader(`{"ids":["`+id+`"]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Origin", "http://evil")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("cross-origin read-state mutation accepted")
	}
	tokenAPICall(t, h, testToken, "POST", "/api/notifications/read", `{"ids":[]}`, 400)
	tokenAPICall(t, h, testToken, "POST", "/api/notifications/read", `{"ids":["missing"]}`, 400)
	b = tokenAPICall(t, h, testToken, "POST", "/api/notifications/read", `{"ids":["`+id+`"]}`, 200)
	if !bytes.Contains(b, []byte(`"unread":0`)) {
		t.Fatal("notification read did not update count")
	}
	b = tokenAPICall(t, h, testToken, "GET", "/api/state", "", 200)
	if !bytes.Contains(b, []byte(`"readAt"`)) {
		t.Fatal("console state excludes notification read status")
	}
	info, err := os.Stat(c.Store.Monitor.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("notification store permissions incorrect")
	}
	tokenAPICall(t, h, testToken, "GET", "/notifications.js", "", 200)
}

func TestNotificationsDockerGatewayReportsActualSuccessOnly(t *testing.T) {
	c, client, _, _ := clusterFixture(t)
	token, _ := issueToken(t, c, "Zela integration")
	body := gatewayEndpointCall(t, client, "http://control", token, "", "POST", "/containers/create?name=notified-app", map[string]string{"Image": "alpine"}, 201)
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	items, _ := c.Store.Monitor.Notifications()
	if len(items) != 1 || items[0].Kind != "container-created" || items[0].ContainerID != created.ID {
		t.Fatal("successful gateway placement not notified")
	}
	gatewayEndpointCall(t, client, "http://control", token, "", "POST", "/containers/create", map[string]any{"Image": "alpine", "HostConfig": map[string]bool{"Privileged": true}}, 403)
	gatewayEndpointCall(t, client, "http://control", token, "", "POST", "/containers/"+created.ID+"/restart", nil, 404)
	if items, _ = c.Store.Monitor.Notifications(); len(items) != 1 {
		t.Fatal("failed request emitted a success notification")
	}
	gatewayEndpointCall(t, client, "http://control", token, "", "POST", "/containers/"+created.ID+"/start", nil, 204)
	gatewayEndpointCall(t, client, "http://control", token, "", "POST", "/containers/"+created.ID+"/stop", nil, 204)
	gatewayEndpointCall(t, client, "http://control", token, "", "DELETE", "/containers/"+created.ID, nil, 204)
	items, unread := c.Store.Monitor.Notifications()
	if len(items) != 4 || unread != 4 {
		t.Fatal("gateway lifecycle notifications missing")
	}
	kinds := map[string]bool{}
	for _, n := range items {
		kinds[n.Kind] = true
		if n.WorkerID == "" || n.ContainerID != created.ID {
			t.Fatal("notification lacks its real source")
		}
	}
	for _, kind := range []string{"container-created", "container-start", "container-stop", "container-deleted"} {
		if !kinds[kind] {
			t.Fatalf("missing %s", kind)
		}
	}
	gatewayEndpointCall(t, client, "http://control", token, "", "GET", "/api/notifications", nil, 403)
}
