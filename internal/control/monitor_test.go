package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zela-dev/zela/internal/engine"
	"github.com/zela-dev/zela/internal/telemetry"
)

func monitoredWorker(now time.Time) Worker {
	cpu := 95.0
	return Worker{ID: "w", Name: "worker", Status: "online", Mode: "active", Metrics: &telemetry.Sample{Time: now, Host: &telemetry.Host{CPUPercent: &cpu, MemoryPercent: 20, Disks: []telemetry.Disk{{Path: "/", UsedPercent: 10}}}, Containers: []telemetry.Container{{ID: "c", Name: "web", CPUPercent: &cpu}}}}
}
func monitoringState(w Worker) State {
	return State{Workers: map[string]Worker{w.ID: w}, Deployments: map[string]Deployment{}}
}
func recordMonitor(t *testing.T, m *MonitorStore, s State, now time.Time) {
	t.Helper()
	if err := m.Record(s, now); err != nil {
		t.Fatal(err)
	}
}
func TestSustainedAlertDeduplicatesAcknowledgesResolvesAndRecurs(t *testing.T) {
	dir := t.TempDir()
	m, err := OpenMonitorStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	w := monitoredWorker(now)
	recordMonitor(t, m, monitoringState(w), now)
	alerts, _ := m.Public()
	if len(alerts) != 0 {
		t.Fatal("threshold alerted before sustained duration")
	}
	w.Metrics.Time = now.Add(time.Minute)
	recordMonitor(t, m, monitoringState(w), w.Metrics.Time)
	alerts, _ = m.Public()
	if len(alerts) != 1 || !alerts[0].Active || alerts[0].Kind != "cpu-high" {
		t.Fatalf("missing sustained alert: %+v", alerts)
	}
	id := alerts[0].ID
	if err := m.Acknowledge(id); err != nil {
		t.Fatal(err)
	}
	w.Metrics.Time = now.Add(2 * time.Minute)
	recordMonitor(t, m, monitoringState(w), w.Metrics.Time)
	alerts, _ = m.Public()
	if len(alerts) != 1 || !alerts[0].Active || !alerts[0].Acknowledged {
		t.Fatal("ongoing acknowledged alert duplicated or cleared")
	}
	low := 10.0
	w.Metrics.Host.CPUPercent = &low
	w.Metrics.Time = now.Add(3 * time.Minute)
	recordMonitor(t, m, monitoringState(w), w.Metrics.Time)
	alerts, _ = m.Public()
	if alerts[0].Active || alerts[0].ResolvedAt == nil {
		t.Fatal("recovery did not resolve alert")
	}
	hi := 95.0
	w.Metrics.Host.CPUPercent = &hi
	w.Metrics.Time = now.Add(4 * time.Minute)
	recordMonitor(t, m, monitoringState(w), w.Metrics.Time)
	w.Metrics.Time = now.Add(5 * time.Minute)
	recordMonitor(t, m, monitoringState(w), w.Metrics.Time)
	alerts, _ = m.Public()
	if len(alerts) != 2 || alerts[0].ID == id || !alerts[0].Active || alerts[0].Acknowledged {
		t.Fatal("recurrence did not create a new unacknowledged alert")
	}
	reloaded, err := OpenMonitorStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := reloaded.Public()
	if len(saved) != 2 {
		t.Fatal("alert history lost after restart")
	}
	info, err := os.Stat(filepath.Join(dir, "monitoring.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("monitoring state not stored privately")
	}
}
func TestStaleMetricsDoNotResolveActiveResourceAlertOrExtendPendingBreach(t *testing.T) {
	m, _ := OpenMonitorStore(t.TempDir())
	now := time.Now().UTC()
	w := monitoredWorker(now)
	s := monitoringState(w)
	r := defaultAlertRules()
	r.ResourceForSeconds = 0
	_ = m.SetRules(r)
	recordMonitor(t, m, s, now)
	w.Status = "offline"
	s = monitoringState(w)
	recordMonitor(t, m, s, now.Add(time.Minute))
	alerts, _ := m.Public()
	if !alerts[0].Active {
		t.Fatal("missing data reported resource recovery")
	}
	w.Status = "online"
	low := 5.0
	w.Metrics.Host.CPUPercent = &low
	w.Metrics.Time = now.Add(2 * time.Minute)
	recordMonitor(t, m, monitoringState(w), w.Metrics.Time)
	alerts, _ = m.Public()
	for _, a := range alerts {
		if a.Kind == "cpu-high" && a.Active {
			t.Fatal("actual recovery did not resolve CPU alert")
		}
	}
}
func TestHistoryRetentionIncludesStoppedContainerHistoryAndBoundsStorage(t *testing.T) {
	m, _ := OpenMonitorStore(t.TempDir())
	now := time.Now().UTC()
	w := monitoredWorker(now)
	low := 10.0
	w.Metrics.Host.CPUPercent = &low
	// Seed old frames to verify pruning independently of polling frequency.
	m.data.History[w.ID] = []HistoryPoint{{Time: now.Add(-25 * time.Hour), Host: *w.Metrics.Host, Containers: w.Metrics.Containers}, {Time: now.Add(-2 * time.Hour), Host: *w.Metrics.Host, Containers: w.Metrics.Containers}, {Time: now.Add(-30 * time.Minute), Host: *w.Metrics.Host, Containers: w.Metrics.Containers}}
	recordMonitor(t, m, monitoringState(w), now)
	points := m.History("w", now.Add(-24*time.Hour))
	if len(points) != 3 || len(points[0].Containers) != 0 || len(points[1].Containers) != 1 {
		t.Fatalf("incorrect retention: %+v", points)
	}
	w.Status = "offline"
	recordMonitor(t, m, monitoringState(w), now.Add(26*time.Hour))
	if len(m.History("w", time.Time{})) != 0 {
		t.Fatal("offline host history retained beyond retention window")
	}
	// Unregister must prune all retained samples.
	recordMonitor(t, m, State{Workers: map[string]Worker{}}, now.Add(27*time.Hour))
	if _, ok := m.data.History["w"]; ok {
		t.Fatal("unregistered worker retained history")
	}
}
func TestMonitoringWriteFailureRollsBackAndRulesAreValidated(t *testing.T) {
	m, _ := OpenMonitorStore(t.TempDir())
	before := m.data.Rules
	bad := before
	bad.CPUPercent = 101
	if err := m.SetRules(bad); err == nil {
		t.Fatal("invalid CPU threshold accepted")
	}
	m.path = filepath.Join(t.TempDir(), "missing", "monitoring.json")
	good := before
	good.CPUPercent = 70
	if err := m.SetRules(good); err == nil {
		t.Fatal("write failure hidden")
	}
	if m.data.Rules != before {
		t.Fatal("failed settings persisted in memory")
	}
	w := monitoredWorker(time.Now())
	if err := m.Record(monitoringState(w), w.Metrics.Time); err == nil {
		t.Fatal("history persistence failure hidden")
	}
	if len(m.data.History) != 0 {
		t.Fatal("failed history mutation not rolled back")
	}
}
func TestAutomaticOfflineHealthDiskAndDegradedDeploymentAlerts(t *testing.T) {
	m, _ := OpenMonitorStore(t.TempDir())
	now := time.Now().UTC()
	r := defaultAlertRules()
	r.OfflineForSeconds = 0
	r.HealthForSeconds = 0
	r.ResourceForSeconds = 0
	_ = m.SetRules(r)
	w := monitoredWorker(now)
	cpu := 10.0
	w.Metrics.Host.CPUPercent = &cpu
	w.Metrics.Host.Disks[0].UsedPercent = 95
	w.Containers = []engine.Container{{ID: "c", Names: []string{"/web"}, Status: "Up (unhealthy)"}}
	s := monitoringState(w)
	s.Workers["offline"] = Worker{ID: "offline", Name: "offline", Status: "offline"}
	s.Deployments["d"] = Deployment{ID: "d", Name: "web", DesiredState: "running", Status: "degraded", Error: "Deployment failure"}
	recordMonitor(t, m, s, now)
	alerts, _ := m.Public()
	kinds := map[string]bool{}
	for _, a := range alerts {
		kinds[a.Kind] = true
	}
	for _, kind := range []string{"worker-offline", "disk-high", "container-unhealthy", "deployment-degraded"} {
		if !kinds[kind] {
			t.Fatalf("missing automatic %s alert", kind)
		}
	}
}

type unavailableMetrics struct{ engine.Engine }

func (e unavailableMetrics) Metrics(context.Context) (telemetry.Sample, error) {
	return telemetry.Sample{}, errors.New("old agent")
}
func TestMetricsFailureDoesNotMarkHealthyDockerHostOffline(t *testing.T) {
	c, _ := fixture(t)
	original := c.Factory
	c.Factory = func(w Worker) (engine.Engine, error) { e, err := original(w); return unavailableMetrics{e}, err }
	c.Refresh(context.Background())
	for _, w := range c.Store.snapshot().Workers {
		if w.Status != "online" || w.MetricsError != "old agent" {
			t.Fatalf("metric failure broke Docker management: %+v", w)
		}
	}
}
func TestMonitoringAPIProtectsHistoryRulesAndAcknowledgements(t *testing.T) {
	c, _ := fixture(t)
	handler := Handler(c, HTTPConfig{Token: testToken})
	for _, route := range []string{"/api/workers/w1/metrics/history", "/api/monitoring/rules", "/api/alerts/a/acknowledge"} {
		method := "POST"
		if strings.HasSuffix(route, "history") {
			method = "GET"
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, "http://control.local"+route, nil))
		if w.Code != 401 {
			t.Fatalf("unauthenticated monitoring access: %s %d", route, w.Code)
		}
	}
	for id := range c.Store.snapshot().Workers {
		r := httptest.NewRequest("GET", "http://control.local/api/workers/"+id+"/metrics/history?window=24h", nil)
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var result struct {
			Points []HistoryPoint `json:"points"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		break
	}
}
