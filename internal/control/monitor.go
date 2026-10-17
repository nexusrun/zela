package control

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zela-dev/zela/internal/engine"
	"github.com/zela-dev/zela/internal/telemetry"
)

type AlertRules struct {
	CPUPercent         float64 `json:"cpuPercent"`
	MemoryPercent      float64 `json:"memoryPercent"`
	DiskPercent        float64 `json:"diskPercent"`
	ResourceForSeconds int     `json:"resourceForSeconds"`
	OfflineForSeconds  int     `json:"offlineForSeconds"`
	HealthForSeconds   int     `json:"healthForSeconds"`
}

func defaultAlertRules() AlertRules { return AlertRules{90, 90, 85, 60, 30, 30} }
func (r AlertRules) validate() error {
	for _, v := range []float64{r.CPUPercent, r.MemoryPercent, r.DiskPercent} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 1 || v > 100 {
			return fmt.Errorf("thresholds must be between 1 and 100 percent")
		}
	}
	for _, v := range []int{r.ResourceForSeconds, r.OfflineForSeconds, r.HealthForSeconds} {
		if v < 0 || v > 3600 {
			return fmt.Errorf("alert durations must be between 0 and 3600 seconds")
		}
	}
	return nil
}

type Alert struct {
	ID           string     `json:"id"`
	Key          string     `json:"key"`
	WorkerID     string     `json:"workerID,omitempty"`
	ContainerID  string     `json:"containerID,omitempty"`
	Kind         string     `json:"kind"`
	Severity     string     `json:"severity"`
	Message      string     `json:"message"`
	Active       bool       `json:"active"`
	Acknowledged bool       `json:"acknowledged"`
	OpenedAt     time.Time  `json:"openedAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	ResolvedAt   *time.Time `json:"resolvedAt,omitempty"`
}
type HistoryPoint struct {
	Time       time.Time             `json:"time"`
	Host       telemetry.Host        `json:"host"`
	Containers []telemetry.Container `json:"containers,omitempty"`
}
type monitorData struct {
	Notifications            []Notification            `json:"notifications"`
	NotificationsInitialized bool                      `json:"notificationsInitialized"`
	NotifiedOperations       map[string]string         `json:"notifiedOperations,omitempty"`
	Rules                    AlertRules                `json:"rules"`
	Alerts                   []Alert                   `json:"alerts"`
	Pending                  map[string]time.Time      `json:"pending"`
	History                  map[string][]HistoryPoint `json:"history"`
}
type MonitorStore struct {
	database  *postgresStore
	mu        sync.Mutex
	path      string
	data      monitorData
	lastError string
}

func (m *MonitorStore) Error() string { m.mu.Lock(); defer m.mu.Unlock(); return m.lastError }

func OpenMonitorStore(dir string) (*MonitorStore, error) {
	return openMonitorStore(dir, nil)
}
func openMonitorStore(dir string, database *postgresStore) (*MonitorStore, error) {
	m := &MonitorStore{database: database, path: filepath.Join(dir, "monitoring.json"), data: monitorData{Rules: defaultAlertRules(), Alerts: []Alert{}, Pending: map[string]time.Time{}, History: map[string][]HistoryPoint{}}}
	b, err := readState(database, "monitoring", m.path)
	if err == nil {
		if err = json.Unmarshal(b, &m.data); err != nil {
			return nil, fmt.Errorf("invalid monitoring state: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := m.data.Rules.validate(); err != nil {
		return nil, err
	}
	if m.data.Pending == nil {
		m.data.Pending = map[string]time.Time{}
	}
	// A manager outage is not evidence that a threshold remained breached.
	m.data.Pending = map[string]time.Time{}
	if m.data.History == nil {
		m.data.History = map[string][]HistoryPoint{}
	}
	return m, nil
}
func (m *MonitorStore) Public() ([]Alert, AlertRules) {
	m.mu.Lock()
	defer m.mu.Unlock()
	alerts := append([]Alert{}, m.data.Alerts...)
	sort.Slice(alerts, func(i, j int) bool {
		if alerts[i].Active != alerts[j].Active {
			return alerts[i].Active
		}
		return alerts[i].UpdatedAt.After(alerts[j].UpdatedAt)
	})
	return alerts, m.data.Rules
}
func (m *MonitorStore) History(workerID string, since time.Time) []HistoryPoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []HistoryPoint{}
	for _, p := range m.data.History[workerID] {
		if !p.Time.Before(since) {
			out = append(out, p)
		}
	}
	b, _ := json.Marshal(out)
	var copy []HistoryPoint
	_ = json.Unmarshal(b, &copy)
	return copy
}
func (m *MonitorStore) mutate(fn func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	before, _ := json.Marshal(m.data)
	rollback := func() { var restored monitorData; _ = json.Unmarshal(before, &restored); m.data = restored }
	if err := fn(); err != nil {
		rollback()
		return err
	}
	b, err := json.Marshal(m.data)
	if err == nil && m.database != nil {
		err = m.database.save("monitoring", b)
	} else if err == nil {
		var f *os.File
		f, err = os.CreateTemp(filepath.Dir(m.path), ".monitoring-*")
		if err == nil {
			defer os.Remove(f.Name())
			_, err = f.Write(b)
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil {
				err = os.Rename(f.Name(), m.path)
			}
		}
	}
	if err != nil {
		rollback()
		m.lastError = "Monitoring persistence failed: " + err.Error()
		return fmt.Errorf("persist monitoring state: %w", err)
	}
	m.lastError = ""
	return nil
}
func (m *MonitorStore) SetRules(r AlertRules) error {
	if err := r.validate(); err != nil {
		return err
	}
	return m.mutate(func() error { m.data.Rules = r; m.data.Pending = map[string]time.Time{}; return nil })
}
func (m *MonitorStore) Acknowledge(id string) error {
	return m.mutate(func() error {
		for i := range m.data.Alerts {
			if m.data.Alerts[i].ID == id {
				m.data.Alerts[i].Acknowledged = true
				return nil
			}
		}
		return fmt.Errorf("alert not found")
	})
}

type condition struct {
	key, workerID, containerID, kind, severity, message string
	seconds                                             int
	// since backdates a new condition, e.g. to a worker's last successful probe.
	since time.Time
}

// inventoryStaleAfter matches InventoryFresh in the infrastructure view.
const inventoryStaleAfter = 45 * time.Second

// inventoryFresh treats a never-probed worker as fresh: its first probe decides.
func inventoryFresh(w Worker, now time.Time) bool {
	return w.LastSeen.IsZero() || (now.Sub(w.LastSeen) <= inventoryStaleAfter && w.LastSeen.Sub(now) <= 30*time.Second)
}
func freshMetrics(w Worker, now time.Time) bool {
	return w.Status == "online" && w.Metrics != nil && w.Metrics.Host != nil && w.MetricsError == "" && now.Sub(w.Metrics.Time) <= 45*time.Second && w.Metrics.Time.Sub(now) <= 30*time.Second
}
func (m *MonitorStore) Record(s State, now time.Time) error {
	return m.mutate(func() error {
		m.initializeNotificationsLocked(s, now)
		m.operationNotifications(s, now)
		r := m.data.Rules
		conditions := map[string]condition{}
		var since time.Time
		add := func(w Worker, kind, severity, message, ref string, seconds int) {
			key := w.ID + ":" + kind + ":" + ref
			containerID := ""
			if kind == "container-unhealthy" {
				containerID = ref
			}
			conditions[key] = condition{key, w.ID, containerID, kind, severity, message, seconds, since}
		}
		for id, w := range s.Workers {
			if w.Status == "provisioning" || w.Status == "provision-failed" {
				continue
			}
			if w.Status == "offline" {
				since = w.LastSeen
				add(w, "worker-offline", "critical", w.Name+" is unreachable", "", r.OfflineForSeconds)
				since = time.Time{}
				continue
			}
			if !inventoryFresh(w, now) {
				add(w, "inventory-stale", "warning", w.Name+": inventory has not refreshed recently; check the control-plane poller", "", 0)
			}
			if !freshMetrics(w, now) {
				add(w, "metrics-unavailable", "warning", w.Name+": metrics unavailable or stale; check or update the worker agent", "", 60)
			} else {
				h := w.Metrics.Host
				if h.CPUPercent != nil && *h.CPUPercent >= r.CPUPercent {
					add(w, "cpu-high", "warning", fmt.Sprintf("%s: CPU %.1f%% exceeds %.1f%%", w.Name, *h.CPUPercent, r.CPUPercent), "", r.ResourceForSeconds)
				}
				if h.MemoryPercent >= r.MemoryPercent {
					add(w, "memory-high", "warning", fmt.Sprintf("%s: memory %.1f%% exceeds %.1f%%", w.Name, h.MemoryPercent, r.MemoryPercent), "", r.ResourceForSeconds)
				}
				for _, d := range h.Disks {
					if d.UsedPercent >= r.DiskPercent {
						add(w, "disk-high", "critical", fmt.Sprintf("%s: %s disk %.1f%% exceeds %.1f%%", w.Name, d.Path, d.UsedPercent, r.DiskPercent), d.Path, r.ResourceForSeconds)
					}
				}
				if len(w.Metrics.Errors) > 0 || w.Metrics.Truncated > 0 {
					add(w, "metrics-partial", "warning", w.Name+": some container metrics could not be collected; inspect monitoring details", "", 60)
				}
				history := m.data.History[id]
				if len(history) == 0 || w.Metrics.Time.Sub(history[len(history)-1].Time) >= time.Minute {
					history = append(history, HistoryPoint{w.Metrics.Time, *h, w.Metrics.Containers})
				}
				kept := history[:0]
				for _, p := range history {
					if p.Time.Before(now.Add(-24 * time.Hour)) {
						continue
					}
					if p.Time.Before(now.Add(-time.Hour)) {
						p.Containers = nil
					}
					kept = append(kept, p)
				}
				if len(kept) > 1440 {
					kept = kept[len(kept)-1440:]
				}
				m.data.History[id] = kept
			}
			for _, ct := range w.Containers {
				if strings.Contains(ct.Status, "unhealthy") {
					add(w, "container-unhealthy", "warning", w.Name+": container "+strings.TrimPrefix(firstName(ct), "/")+" is unhealthy", ct.ID, r.HealthForSeconds)
				}
			}
		}
		for _, d := range s.Deployments {
			if d.DesiredState == "running" && d.Status == "degraded" {
				key := "deployment:" + d.ID
				conditions[key] = condition{key, "", "", "deployment-degraded", "critical", "Deployment " + d.Name + " has missing, stopped, or unhealthy replicas", r.HealthForSeconds, time.Time{}}
			}
		}
		for id, history := range m.data.History {
			if _, ok := s.Workers[id]; !ok {
				delete(m.data.History, id)
				continue
			}
			kept := history[:0]
			for _, p := range history {
				if p.Time.Before(now.Add(-24 * time.Hour)) {
					continue
				}
				if p.Time.Before(now.Add(-time.Hour)) {
					p.Containers = nil
				}
				kept = append(kept, p)
			}
			m.data.History[id] = kept
		}
		for key := range m.data.Pending {
			if _, ok := conditions[key]; !ok {
				delete(m.data.Pending, key)
			}
		}
		for i := range m.data.Alerts {
			a := &m.data.Alerts[i]
			if !a.Active {
				continue
			}
			if cond, ok := conditions[a.Key]; ok {
				a.Message = cond.message
			} else {
				if w, exists := s.Workers[a.WorkerID]; exists {
					if (a.Kind == "cpu-high" || a.Kind == "memory-high" || a.Kind == "disk-high") && !freshMetrics(w, now) {
						continue
					}
					if a.Kind == "cpu-high" && w.Metrics != nil && w.Metrics.Host != nil && w.Metrics.Host.CPUPercent == nil {
						continue
					}
					if a.Kind == "container-unhealthy" && w.Status != "online" {
						continue
					}
				}
				a.Active = false
				t := now
				a.ResolvedAt = &t
				a.UpdatedAt = now
				m.alertNotification(*a, true, now)
			}
		}
		for key, cond := range conditions {
			first, ok := m.data.Pending[key]
			if !ok {
				first = now
				if !cond.since.IsZero() && cond.since.Before(now) {
					first = cond.since
				}
				m.data.Pending[key] = first
			}
			if now.Sub(first) < time.Duration(cond.seconds)*time.Second {
				continue
			}
			found := false
			for _, a := range m.data.Alerts {
				if a.Key == key && a.Active {
					found = true
					break
				}
			}
			if found {
				continue
			}
			m.data.Alerts = append(m.data.Alerts, Alert{ID: newID("alt_"), Key: key, WorkerID: cond.workerID, ContainerID: cond.containerID, Kind: cond.kind, Severity: cond.severity, Message: cond.message, Active: true, OpenedAt: now, UpdatedAt: now})
			m.alertNotification(m.data.Alerts[len(m.data.Alerts)-1], false, now)
		}
		active, resolved := []Alert{}, []Alert{}
		for _, a := range m.data.Alerts {
			if a.Active {
				active = append(active, a)
			} else if a.UpdatedAt.After(now.Add(-7 * 24 * time.Hour)) {
				resolved = append(resolved, a)
			}
		}
		sort.Slice(resolved, func(i, j int) bool { return resolved[i].UpdatedAt.After(resolved[j].UpdatedAt) })
		if len(resolved) > 500 {
			resolved = resolved[:500]
		}
		m.data.Alerts = append(active, resolved...)
		m.pruneNotifications(now)
		return nil
	})
}
func firstName(c engine.Container) string {
	if len(c.Names) > 0 {
		return c.Names[0]
	}
	return c.ID
}

func validateMetrics(v telemetry.Sample, now time.Time) error {
	if v.Time.IsZero() || now.Sub(v.Time) > 45*time.Second || v.Time.Sub(now) > 30*time.Second {
		return fmt.Errorf("worker metrics are stale or its clock differs; check the agent service and time synchronization")
	}
	if v.Host == nil {
		return fmt.Errorf("host metrics unavailable; check Linux /proc and configured disk paths")
	}
	if len(v.Containers) > telemetry.MaxContainers || len(v.Host.Disks) > 32 || len(v.Errors) > telemetry.MaxContainers+2 {
		return fmt.Errorf("worker metrics exceed collection limits")
	}
	percent := func(p float64) bool { return !math.IsNaN(p) && !math.IsInf(p, 0) && p >= 0 && p <= 100 }
	if !percent(v.Host.MemoryPercent) || v.Host.CPUPercent != nil && !percent(*v.Host.CPUPercent) {
		return fmt.Errorf("invalid host metric percentages")
	}
	for _, d := range v.Host.Disks {
		if !percent(d.UsedPercent) {
			return fmt.Errorf("invalid disk percentage")
		}
	}
	return nil
}
