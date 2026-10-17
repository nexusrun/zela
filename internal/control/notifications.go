package control

import (
	"fmt"
	"sort"
	"time"
)

// Notifications and their source cursors live in the monitoring document, so
// recording an alert and its notification is one atomic persistence operation.
type Notification struct {
	ContainerID string     `json:"containerID,omitempty"`
	ID          string     `json:"id"`
	EventKey    string     `json:"eventKey"`
	Kind        string     `json:"kind"`
	Severity    string     `json:"severity"`
	Title       string     `json:"title"`
	Message     string     `json:"message"`
	AlertID     string     `json:"alertID,omitempty"`
	OperationID string     `json:"operationID,omitempty"`
	WorkerID    string     `json:"workerID,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	ReadAt      *time.Time `json:"readAt,omitempty"`
}

func (m *MonitorStore) addNotification(n Notification, now time.Time) {
	for _, existing := range m.data.Notifications {
		if existing.EventKey == n.EventKey {
			return
		}
	}
	n.ID = newID("ntf_")
	n.CreatedAt = now
	m.data.Notifications = append(m.data.Notifications, n)
}
func (m *MonitorStore) alertNotification(a Alert, resolved bool, now time.Time) {
	kind, title, severity := "alert-opened", "Cluster alert", a.Severity
	if resolved {
		kind, title, severity = "alert-resolved", "Cluster recovered", "info"
	}
	message := a.Message
	if resolved {
		message = "Recovered from: " + message
	}
	m.addNotification(Notification{EventKey: a.ID + ":" + kind, Kind: kind, Severity: severity, Title: title, Message: message, AlertID: a.ID, WorkerID: a.WorkerID}, now)
}
func (m *MonitorStore) initializeNotificationsLocked(s State, now time.Time) {
	if m.data.NotificationsInitialized {
		return
	}
	if m.data.NotifiedOperations == nil {
		m.data.NotifiedOperations = map[string]string{}
	}
	// Do not replay completed operation history when upgrading. Existing active
	// alerts are still actionable and appear once in the new notification center.
	for _, op := range s.Operations {
		m.data.NotifiedOperations[op.ID] = op.Status
	}
	for _, a := range m.data.Alerts {
		if a.Active {
			m.alertNotification(a, false, now)
		}
	}
	m.data.NotificationsInitialized = true
}
func (m *MonitorStore) InitializeNotifications(s State, now time.Time) error {
	m.mu.Lock()
	initialized := m.data.NotificationsInitialized
	m.mu.Unlock()
	if initialized {
		return nil
	}
	return m.mutate(func() error { m.initializeNotificationsLocked(s, now); return nil })
}
func (m *MonitorStore) operationNotifications(s State, now time.Time) {
	retained := map[string]string{}
	for _, op := range s.Operations {
		previous := m.data.NotifiedOperations[op.ID]
		if (op.Status == "succeeded" || op.Status == "failed") && previous != op.Status {
			title, message, severity := "Operation completed", op.Title+" completed successfully", "info"
			if op.Status == "failed" {
				title, message, severity = "Operation failed", op.Title+" failed. Open Activity for details.", "critical"
			}
			// Keep raw operation errors out of desktop notifications. Detailed output
			// remains available to administrators in the authenticated Activity view.
			m.addNotification(Notification{EventKey: op.ID + ":" + op.Status, Kind: "operation-" + op.Status, Severity: severity, Title: title, Message: message, OperationID: op.ID}, now)
		}
		retained[op.ID] = op.Status
	}
	m.data.NotifiedOperations = retained
}
func (m *MonitorStore) pruneNotifications(now time.Time) {
	kept := make([]Notification, 0, len(m.data.Notifications))
	for _, n := range m.data.Notifications {
		if !n.CreatedAt.Before(now.Add(-7 * 24 * time.Hour)) {
			kept = append(kept, n)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].CreatedAt.After(kept[j].CreatedAt) })
	if len(kept) > 500 {
		kept = kept[:500]
	}
	m.data.Notifications = kept
}
func (m *MonitorStore) Notifications() ([]Notification, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]Notification{}, m.data.Notifications...)
	unread := 0
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	for _, n := range out {
		if n.ReadAt == nil {
			unread++
		}
	}
	return out, unread
}
func (m *MonitorStore) ReadNotifications(ids []string, now time.Time) error {
	if len(ids) == 0 || len(ids) > 500 {
		return fmt.Errorf("provide between 1 and 500 notification IDs")
	}
	return m.mutate(func() error {
		wanted := map[string]bool{}
		for _, id := range ids {
			wanted[id] = true
		}
		found := 0
		for i, n := range m.data.Notifications {
			if wanted[n.ID] {
				found++
				if n.ReadAt == nil {
					t := now
					m.data.Notifications[i].ReadAt = &t
				}
			}
		}
		if found != len(wanted) {
			return fmt.Errorf("notification not found; refresh the notification center")
		}
		return nil
	})
}

// Docker gateway changes are recorded after a successful Engine response and
// durable ownership update. API requests that fail never report a deployment.
func (m *MonitorStore) ContainerNotification(worker, id, name, action string, now time.Time) error {
	titles := map[string]string{"created": "Container created", "start": "Container started", "stop": "Container stopped", "restart": "Container restarted", "rename": "Container renamed", "deleted": "Container removed"}
	title, ok := titles[action]
	if !ok {
		return nil
	}
	if name == "" {
		name = id
	}
	return m.mutate(func() error {
		key := "container:" + id + ":" + action
		if action != "created" && action != "deleted" {
			key += ":" + newID("")
		}
		m.addNotification(Notification{EventKey: key, Kind: "container-" + action, Severity: "info", Title: title, Message: name + " · worker " + worker, ContainerID: id, WorkerID: worker}, now)
		m.pruneNotifications(now)
		return nil
	})
}
