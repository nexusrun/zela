package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (c *Controller) executeHandover(ctx context.Context, d Deployment, task Replica, target string) error {
	if len(d.StorageAttachments) > 0 && len(d.Routes) > 0 && !networkRequired(c.Store.snapshot()) {
		return fmt.Errorf("a deployment with shared storage and public routes requires the cluster network for an acknowledged handover")
	}
	trafficManaged := len(d.Routes) > 0
	if trafficManaged {
		if err := c.applyAndVerifyNetwork(ctx); err != nil {
			return err
		}
	}
	if trafficManaged {
		s := c.Store.snapshot()
		if !networkRequired(s) || s.Network.EdgeHTTPPort == 0 && s.Network.EdgeHTTPSPort == 0 {
			return fmt.Errorf("public route handover requires an enabled cluster edge listener")
		}
	}
	source, _, err := c.worker(ctx, task.WorkerID)
	if err != nil {
		return err
	}
	inspection, err := source.Inspect(ctx, task.ID)
	if err != nil {
		if strings.Contains(err.Error(), "404") && d.Handover != nil && d.Handover.Replacement.ID != "" {
			return c.completeRetiredSource(ctx, d, task)
		}
		return err
	}
	if d.ImageDigest == "" && task.Image == d.Image && strings.HasPrefix(inspection.Image, "sha256:") {
		d.ImageDigest = inspection.Image
		if err := c.editDeployment(d.ID, func(current *Deployment) {
			if current.ImageDigest == "" {
				current.ImageDigest = inspection.Image
			}
		}); err != nil {
			return err
		}
	}
	if err := validateManagedTaskMounts(d, inspection); err != nil {
		return fmt.Errorf("%s: %w", task.Name, err)
	}
	destinationEngine, destination, err := c.worker(ctx, target)
	if err != nil {
		return err
	}
	if destination.Mode != "active" {
		return fmt.Errorf("destination worker is not active")
	}
	// Single-writer workloads stop the source before starting a replacement.
	// This avoids overlapping writers, with a service gap while the new task starts.
	singleWriter := d.WorkloadPolicy == WorkloadSingleWriter
	resumeID, resumeErr := findHandoverContainer(ctx, destinationEngine, d.Handover.ID)
	if resumeErr != nil {
		return resumeErr
	}
	preparedImage := ""
	if !singleWriter && resumeID != "" {
		d, preparedImage, err = c.prewarmProvision(ctx, d, destinationEngine, target)
		if err != nil {
			return err
		}
	}
	if singleWriter {
		if resumeID == "" {
			if err := fits(c.Store.snapshot(), d, destination); err != nil {
				return fmt.Errorf("%s: %w", destination.Name, err)
			}
		}
		if s := c.Store.snapshot(); networkRequired(s) && !s.Network.Members[target].Ready() {
			return fmt.Errorf("%s has not joined the cluster network", destination.Name)
		}
		d, preparedImage, err = c.prewarmProvision(ctx, d, destinationEngine, target)
		if err != nil {
			return err
		}
		if err := c.handoverStage(d.ID, "stopping-source"); err != nil {
			return err
		}
		if err := c.setReplicaLifecycle(d.ID, task.ID, "stopped"); err != nil {
			return err
		}
		if err := source.Stop(ctx, task.ID); err != nil {
			return err
		}
		stopped, inspectErr := source.Inspect(ctx, task.ID)
		if inspectErr != nil || stopped.State.Running {
			return errors.Join(fmt.Errorf("source shutdown was not confirmed"), inspectErr)
		}
	}
	var replacement Replica
	if singleWriter || resumeID != "" {
		replacement, err = c.provisionPrepared(ctx, d, destination, destinationEngine, preparedImage)
	} else {
		replacement, err = c.provision(ctx, d, destination)
	}
	if err != nil {
		if singleWriter {
			restore, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if stopErr := c.stopHandoverReplacement(restore, d, destinationEngine); stopErr != nil {
				return errors.Join(err, stopErr)
			}
			if startErr := c.restoreHandoverSource(restore, d, task, source); startErr != nil {
				return errors.Join(err, startErr)
			}
			if clearErr := c.handoverStage(d.ID, "rolled-back"); clearErr != nil {
				return errors.Join(err, clearErr)
			}
		}
		return err
	}
	rollback := func(cause error) error {
		cleanup, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if singleWriter {
			if err := c.stopHandoverReplacement(cleanup, d, destinationEngine); err != nil {
				return errors.Join(cause, err)
			}
		}
		restartErr := c.restoreHandoverSource(cleanup, d, task, source)
		if restartErr != nil {
			// A failed remove may have reached Docker. Preserve the known-ready
			// replacement if we cannot prove that the source is running again.
			return errors.Join(cause, fmt.Errorf("source restoration was not confirmed; replacement retained for recovery: %w", restartErr))
		}
		_ = c.setReplicaLifecycle(d.ID, task.ID, "serving")
		if trafficManaged {
			if routeErr := c.applyAndVerifyNetwork(cleanup); routeErr != nil {
				return errors.Join(cause, fmt.Errorf("source restarted but traffic restoration is not confirmed; replacement retained: %w", routeErr))
			}
			if routeErr := c.setReplicaLifecycle(d.ID, replacement.ID, "draining"); routeErr != nil {
				return errors.Join(cause, fmt.Errorf("source restarted but replacement drain could not be recorded: %w", routeErr))
			}
			if routeErr := c.applyAndVerifyNetwork(cleanup); routeErr != nil {
				return errors.Join(cause, fmt.Errorf("source restarted but replacement traffic drain is not confirmed: %w", routeErr))
			}
		}
		cleanupErr := c.removeTask(cleanup, d.ID, replacement)
		if cleanupErr == nil {
			cleanupErr = c.handoverStage(d.ID, "rolled-back")
		}
		return errors.Join(cause, restartErr, cleanupErr)
	}
	if err := c.handoverStage(d.ID, "cutting-over"); err != nil {
		return err
	}
	if trafficManaged {
		if err := c.applyAndVerifyNetwork(ctx); err != nil {
			return rollback(fmt.Errorf("replacement is ready but its traffic generation was not acknowledged: %w", err))
		}
		if err := c.setReplicaLifecycle(d.ID, task.ID, "draining"); err != nil {
			return rollback(err)
		}
		if err := c.applyAndVerifyNetwork(ctx); err != nil {
			_ = c.setReplicaLifecycle(d.ID, task.ID, "serving")
			return rollback(fmt.Errorf("source traffic drain was not acknowledged: %w", err))
		}
	}
	if err := c.handoverStage(d.ID, "retiring-source"); err != nil {
		return err
	}
	if !singleWriter {
		if err := source.Stop(ctx, task.ID); err != nil {
			return rollback(err)
		}
	}
	if err := source.Remove(ctx, task.ID); err != nil {
		return rollback(err)
	}
	return c.forgetTask(d.ID, task.ID)
}
