package control

import (
	"context"
	"fmt"
	"time"

	"github.com/zela-dev/zela/internal/engine"
)

func (c *Controller) UpgradeWorker(id string) (Operation, error) {
	w, ok := c.Store.snapshot().Workers[id]
	if !ok || w.SSH == nil {
		return Operation{}, fmt.Errorf("automatic agent updates require an SSH-managed worker; copy the new zela-worker binary and restart the service for manual agents")
	}
	if w.Status == "provisioning" || w.Status == "provision-failed" {
		return Operation{}, fmt.Errorf("finish or retry provisioning before updating the worker agent")
	}
	if c.Provisioner == nil && !c.Store.snapshot().Demo {
		return Operation{}, fmt.Errorf("SSH provisioning is not configured")
	}
	return c.submit("Update agent on "+w.Name, 30*time.Minute, func(s *State) error {
		if _, ok := s.Workers[id]; !ok {
			return fmt.Errorf("worker no longer exists")
		}
		return nil
	}, func(ctx context.Context, opID string) error {
		identity, err := c.Store.Vault.Identity(w.SSH.SecretID)
		if err != nil {
			return err
		}
		progress := func(name, status, detail string) error { return c.provisionStep(opID, name, status, detail) }
		if !c.Store.snapshot().Demo {
			if err := c.networkProvisioner().Install(ctx, w, identity, progress); err != nil {
				return err
			}
		}
		e, err := c.Factory(w)
		if err != nil {
			return err
		}
		provider, ok := e.(engine.MetricsProvider)
		if !ok {
			return fmt.Errorf("updated agent does not expose monitoring")
		}
		if err := progress("Verify metrics collection", "running", "Waiting for the updated agent to collect its first sample"); err != nil {
			return err
		}
		probe, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		for {
			attempt, stop := context.WithTimeout(probe, 5*time.Second)
			v, err := provider.Metrics(attempt)
			stop()
			if err == nil {
				err = validateMetrics(v, time.Now())
			}
			if err == nil {
				return progress("Verify metrics collection", "succeeded", "Host and container monitoring is available")
			}
			select {
			case <-probe.Done():
				_ = progress("Verify metrics collection", "failed", "Check the zela-worker service and disk paths")
				return fmt.Errorf("agent metrics verification failed: %w", err)
			case <-time.After(time.Second):
			}
		}
	})
}
