package control

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zela-dev/zela/internal/engine"
)

// unreachable makes the named workers fail every probe while down is set.
func unreachable(c *Controller, down *atomic.Value) {
	factory := c.Factory
	c.Factory = func(w Worker) (engine.Engine, error) {
		if id, _ := down.Load().(string); id == w.ID {
			return nil, errors.New("connection refused")
		}
		return factory(w)
	}
}
func waitIdle(t *testing.T, c *Controller) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(c.gate) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("operation did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWorkerOfflineNeedsConsecutiveFailures(t *testing.T) {
	c, _ := fixture(t)
	var down atomic.Value
	unreachable(c, &down)
	c.Refresh(context.Background())
	down.Store("w1")
	for i := 1; i < offlineAfterFailures; i++ {
		c.Refresh(context.Background())
		if w := c.Store.snapshot().Workers["w1"]; w.Status != "online" || w.FailedProbes != i || w.Error == "" {
			t.Fatalf("after %d failures: %+v", i, w)
		}
	}
	c.Refresh(context.Background())
	if w := c.Store.snapshot().Workers["w1"]; w.Status != "offline" {
		t.Fatalf("status %s after %d failures", w.Status, offlineAfterFailures)
	}
	down.Store("")
	c.Refresh(context.Background())
	if w := c.Store.snapshot().Workers["w1"]; w.Status != "online" || w.FailedProbes != 0 {
		t.Fatalf("not recovered: %+v", w)
	}
}

func TestFailoverReplacesLostReplicaAndReclaimsIt(t *testing.T) {
	c, es := fixture(t)
	c.failoverAfter = time.Millisecond
	var down atomic.Value
	unreachable(c, &down)
	op, err := c.CreateDeployment(Deployment{Name: "web", Image: "nginx:alpine", Replicas: 1, MemoryMB: 64, CPUs: .1})
	succeeded(t, c, op, err)
	var d Deployment
	for _, x := range c.Store.snapshot().Deployments {
		d = x
	}
	lost := d.Tasks[0]
	other := map[string]string{"w1": "w2", "w2": "w1"}[lost.WorkerID]

	down.Store(lost.WorkerID)
	for i := 0; i < offlineAfterFailures; i++ {
		c.Refresh(context.Background())
	}
	time.Sleep(2 * time.Millisecond)
	c.Refresh(context.Background()) // Starts recovery.
	waitIdle(t, c)
	d = c.Store.snapshot().Deployments[d.ID]
	if len(d.Tasks) != 1 || d.Tasks[0].WorkerID != other || d.Status != "running" {
		t.Fatalf("not failed over: %+v", d)
	}
	if len(d.Evicted) != 1 || d.Evicted[0].ID != lost.ID {
		t.Fatalf("evicted = %+v", d.Evicted)
	}

	down.Store("")
	c.Refresh(context.Background())
	d = c.Store.snapshot().Deployments[d.ID]
	if len(d.Evicted) != 0 || len(d.Tasks) != 1 {
		t.Fatalf("evicted replica not reclaimed: %+v", d)
	}
	cts, _ := es[lost.WorkerID].List(context.Background())
	for _, ct := range cts {
		if ct.ID == lost.ID {
			t.Fatal("duplicate replica left running on returning worker")
		}
	}
}

func TestRefreshDoesNotOverwriteProvisioning(t *testing.T) {
	c, es := fixture(t)
	barrier := &inventoryBarrierEngine{Engine: es["w1"], entered: make(chan struct{}), release: make(chan struct{})}
	barrier.block.Store(true)
	factory := c.Factory
	c.Factory = func(w Worker) (engine.Engine, error) {
		if w.ID == "w1" {
			return barrier, nil
		}
		return factory(w)
	}
	done := make(chan struct{})
	go func() { c.Refresh(context.Background()); close(done) }()
	<-barrier.entered
	if err := c.Store.update(func(s *State) error {
		w := s.Workers["w1"]
		w.Status = "provisioning"
		s.Workers["w1"] = w
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	close(barrier.release)
	<-done
	if s := c.Store.snapshot().Workers["w1"].Status; s != "provisioning" {
		t.Fatalf("status overwritten to %s", s)
	}
}

func TestAddWorkerRemovedMidConnectIsNotRecreated(t *testing.T) {
	c, _ := fixture(t)
	release := make(chan struct{})
	factory := c.Factory
	c.Factory = func(w Worker) (engine.Engine, error) {
		if w.Name == "late" {
			<-release
			return nil, errors.New("unreachable")
		}
		return factory(w)
	}
	op, err := c.AddWorker(Worker{Name: "late", Endpoint: "https://late.example:9091", Token: "late-worker-token-at-least-32-characters"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Store.update(func(s *State) error {
		for id, w := range s.Workers {
			if w.Name == "late" {
				delete(s.Workers, id)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitOp(t, c, op, nil)
	for id, w := range c.Store.snapshot().Workers {
		if id == "" || w.Name == "" || w.Name == "late" {
			t.Fatalf("removed worker recreated: %q %+v", id, w)
		}
	}
}
