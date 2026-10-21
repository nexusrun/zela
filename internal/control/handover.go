package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/zela-dev/zela/internal/clusternet"
	"github.com/zela-dev/zela/internal/engine"
)

// Handover checkpoints intent before Docker or routing changes. Its stable ID
// is also a container label, so a lost Create response cannot create duplicates.
// Retrying the same replica move resumes this record; other mutations are blocked.
type Handover struct {
	OperationID      string    `json:"operationID,omitempty"`
	RevisionID       string    `json:"revisionID,omitempty"`
	ID               string    `json:"id"`
	Source           Replica   `json:"source"`
	TargetWorkerID   string    `json:"targetWorkerID"`
	Replacement      Replica   `json:"replacement"`
	ImageDigest      string    `json:"imageDigest,omitempty"`
	Stage            string    `json:"stage"`
	Error            string    `json:"error,omitempty"`
	IngressWorkerIDs []string  `json:"ingressWorkerIDs,omitempty"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

func pendingHandover(d Deployment) bool {
	return d.Handover != nil && d.Handover.Stage != "completed" && d.Handover.Stage != "rolled-back"
}

func (c *Controller) handoverStage(id, stage string) error {
	return c.editDeployment(id, func(d *Deployment) {
		if d.Handover != nil {
			d.Handover.Stage = stage
			if stage == "completed" || stage == "rolled-back" {
				d.Handover.Error = ""
			}
			d.Handover.UpdatedAt = time.Now().UTC()
			d.Handover.ImageDigest = d.ImageDigest
			revision := *d
			revision.Handover = nil
			revision.Tasks = nil
			revision.Evicted = nil
			revision.Status = ""
			revision.Error = ""
			revision.WorkerIDs = nil
			payload, _ := json.Marshal(revision)
			digest := sha256.Sum256(payload)
			d.Handover.RevisionID = hex.EncodeToString(digest[:])
		}
	})
}

func (c *Controller) replaceTask(ctx context.Context, d Deployment, task Replica, target string) error {
	if pendingHandover(d) {
		if d.Handover.Source.ID != task.ID || d.Handover.TargetWorkerID != target {
			return fmt.Errorf("resume pending move of replica %s to %s first", d.Handover.Source.ID, d.Handover.TargetWorkerID)
		}
	} else {
		h := &Handover{ID: newID("move_"), Source: task, TargetWorkerID: target, Stage: "preflight", UpdatedAt: time.Now().UTC()}
		for _, operation := range c.Store.snapshot().Operations {
			if operation.Status == "running" {
				h.OperationID = operation.ID
				break
			}
		}
		if s := c.Store.snapshot(); len(d.Routes) > 0 && s.Network != nil {
			for id := range s.Network.Members {
				h.IngressWorkerIDs = append(h.IngressWorkerIDs, id)
			}
			sort.Strings(h.IngressWorkerIDs)
		}
		if err := c.editDeployment(d.ID, func(d *Deployment) { d.Handover = h }); err != nil {
			return err
		}
		d.Handover = h
	}
	err := c.executeHandover(ctx, d, task, target)
	if err == nil {
		return c.handoverStage(d.ID, "completed")
	}
	saveErr := c.editDeployment(d.ID, func(d *Deployment) {
		if d.Handover != nil {
			d.Handover.Error = err.Error()
			d.Handover.UpdatedAt = time.Now().UTC()
		}
	})
	return errors.Join(err, saveErr)
}

func findHandoverContainer(ctx context.Context, e engine.Engine, id string) (string, error) {
	containers, err := e.List(ctx)
	if err != nil {
		return "", fmt.Errorf("cannot reconcile handover replacement: %w", err)
	}
	found := ""
	for _, ct := range containers {
		if ct.Labels["zela.handover.id"] == id {
			if found != "" {
				return "", fmt.Errorf("multiple containers claim handover %s; manual reconciliation required", id)
			}
			found = ct.ID
		}
	}
	return found, nil
}

// Never restore an exclusive writer until a fresh observation proves that the
// destination writer has stopped (or a successful inventory proves it absent).
func (c *Controller) stopHandoverReplacement(ctx context.Context, d Deployment, e engine.Engine) error {
	current, err := c.deployment(d.ID)
	if err != nil {
		return err
	}
	if current.Handover == nil {
		return fmt.Errorf("source remains stopped: handover intent is missing")
	}
	id, err := findHandoverContainer(ctx, e, current.Handover.ID)
	if err != nil {
		return fmt.Errorf("source remains stopped: %w", err)
	}
	if id == "" {
		return nil
	}
	stopErr := e.Stop(ctx, id)
	state, inspectErr := e.Inspect(ctx, id)
	if inspectErr != nil || state.State.Running {
		return errors.Join(fmt.Errorf("source remains stopped: replacement shutdown is not confirmed"), stopErr, inspectErr)
	}
	return c.setReplicaLifecycle(d.ID, id, "stopped")
}

// A crash after Docker removed the source but before state was saved is safe
// to finish only after observing and acknowledging the existing replacement.
func (c *Controller) completeRetiredSource(ctx context.Context, d Deployment, source Replica) error {
	target := d.Handover.Replacement
	e, w, err := c.worker(ctx, target.WorkerID)
	if err != nil {
		return err
	}
	if err = c.prepareVolumes(ctx, e, w.ID, d.StorageAttachments); err != nil {
		return err
	}
	if err = c.verifyVolumeAccess(ctx, d, e, w.ID, target.Image); err != nil {
		return err
	}
	inspection, err := e.Inspect(ctx, target.ID)
	if err != nil {
		return err
	}
	if !inspection.State.Running {
		if err = e.Start(ctx, target.ID); err != nil {
			return err
		}
	}
	if err = c.ready(ctx, e, target.ID, len(d.StorageAttachments) > 0); err != nil {
		return err
	}
	if len(d.Routes) > 0 {
		if err = c.observeReplicaEndpoint(ctx, w, target); err != nil {
			return err
		}
	}
	if err = c.setReplicaLifecycle(d.ID, target.ID, "serving"); err != nil {
		return err
	}
	if err = c.setReplicaLifecycle(d.ID, source.ID, "draining"); err != nil {
		return err
	}
	if len(d.Routes) > 0 {
		if err = c.applyAndVerifyNetwork(ctx); err != nil {
			return err
		}
	}
	return c.forgetTask(d.ID, source.ID)
}

// Publish only a freshly observed running endpoint on the destination subnet.
// Docker Create/Start succeeding is insufficient: inventory can still be stale.
func (c *Controller) observeReplicaEndpoint(ctx context.Context, w Worker, task Replica) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	e, err := c.Factory(w)
	if err != nil {
		return err
	}
	containers, err := e.List(ctx)
	if err != nil {
		return fmt.Errorf("observe replacement network address: %w", err)
	}
	s := c.Store.snapshot()
	if s.Network == nil {
		return fmt.Errorf("cluster network is not enabled")
	}
	subnet, err := netip.ParsePrefix(s.Network.Members[w.ID].Subnet)
	if err != nil {
		return err
	}
	found := false
	for _, ct := range containers {
		if ct.ID == task.ID && ct.State == "running" && !strings.Contains(ct.Status, "unhealthy") {
			ip, err := netip.ParseAddr(ct.NetworkIP(clusternet.DockerNetwork))
			found = err == nil && subnet.Contains(ip)
		}
	}
	if !found {
		return fmt.Errorf("ready replacement has no observed running endpoint on worker %s", w.Name)
	}
	return c.Store.update(func(s *State) error {
		current, ok := s.Workers[w.ID]
		if !ok {
			return fmt.Errorf("destination worker disappeared")
		}
		current.Containers = containers
		current.LastSeen = time.Now().UTC()
		s.Workers[w.ID] = current
		return nil
	})
}

// Restore traffic only after the original container is healthy again. A
// restarted Docker container can report running before its HEALTHCHECK passes.
func (c *Controller) restoreHandoverSource(ctx context.Context, d Deployment, task Replica, e engine.Engine) error {
	if err := e.Start(ctx, task.ID); err != nil {
		return err
	}
	if err := c.ready(ctx, e, task.ID, len(d.StorageAttachments) > 0); err != nil {
		return err
	}
	if len(d.Routes) > 0 {
		w := c.Store.snapshot().Workers[task.WorkerID]
		if err := c.observeReplicaEndpoint(ctx, w, task); err != nil {
			return err
		}
	}
	return c.setReplicaLifecycle(d.ID, task.ID, "serving")
}
