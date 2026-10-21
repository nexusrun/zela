package control

import (
	"context"
	"fmt"
	"github.com/zela-dev/zela/internal/clusternet"
	"time"
)

// applyAndVerifyNetwork waits for every captured ingress worker to
// acknowledge the new service/edge configuration. The edge agent drains
// requests from its previous route generation before reporting the update.
func (c *Controller) applyAndVerifyNetwork(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	s := c.Store.snapshot()
	if !networkRequired(s) {
		return fmt.Errorf("cluster network is not enabled")
	}
	// Serialize fresh endpoint observations with polling before publishing.
	for _, w := range s.Workers {
		if w.Status == "online" {
			c.refreshWorker(ctx, w)
		}
	}
	s = c.Store.snapshot()
	for _, d := range s.Deployments {
		if pendingHandover(d) {
			for _, task := range d.Tasks {
				if task.ID == d.Handover.Replacement.ID && task.Lifecycle == "serving" {
					w := s.Workers[task.WorkerID]
					found := false
					for _, ct := range w.Containers {
						if ct.ID == task.ID && ct.State == "running" && ct.NetworkIP(clusternet.DockerNetwork) != "" {
							found = true
						}
					}
					if !found {
						return fmt.Errorf("replacement endpoint disappeared before traffic acknowledgment")
					}
				}
			}
		}
	}
	c.reconcileNetwork(ctx)
	s = c.Store.snapshot()
	participants := map[string]bool{}
	for id := range s.Network.Members {
		participants[id] = true
	}
	for _, d := range s.Deployments {
		if pendingHandover(d) {
			for _, id := range d.Handover.IngressWorkerIDs {
				participants[id] = true
			}
		}
	}
	for id := range participants {
		w, exists := s.Workers[id]
		member, joined := s.Network.Members[id]
		if !exists || !joined || w.Status != "online" || !inventoryFresh(w, time.Now()) {
			return fmt.Errorf("ingress worker %s is unreachable or stale; isolate it from public traffic before retrying", id)
		}
		cfg, err := c.networkConfig(s, id)
		if err != nil {
			return fmt.Errorf("worker %s cannot form the traffic configuration: %w", w.Name, err)
		}
		if member.Applied != cfg.Hash() || member.Error != "" || member.EdgeError != "" {
			return fmt.Errorf("worker %s has not acknowledged traffic routing: %s", w.Name, firstNonEmpty(member.Error, member.EdgeError, "configuration is pending"))
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
