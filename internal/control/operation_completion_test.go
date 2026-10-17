package control

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zela-dev/zela/internal/engine"
)

type inventoryBarrierEngine struct {
	engine.Engine
	block            atomic.Bool
	entered, release chan struct{}
}

func (e *inventoryBarrierEngine) Info(ctx context.Context) (engine.Info, error) {
	if e.block.Swap(false) {
		close(e.entered)
		select {
		case <-e.release:
		case <-ctx.Done():
			return engine.Info{}, ctx.Err()
		}
	}
	return e.Engine.Info(ctx)
}
func TestOperationCompletionIncludesInventoryAndAllowsNextMutation(t *testing.T) {
	c, es := fixture(t)
	barrier := &inventoryBarrierEngine{Engine: es["w1"], entered: make(chan struct{}), release: make(chan struct{})}
	barrier.block.Store(true)
	var release sync.Once
	defer release.Do(func() { close(barrier.release) })
	factory := c.Factory
	c.Factory = func(w Worker) (engine.Engine, error) {
		if w.ID == "w1" {
			return barrier, nil
		}
		return factory(w)
	}
	op, err := c.Submit("first mutation", nil, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-barrier.entered:
	case <-time.After(time.Second):
		t.Fatal("inventory did not begin")
	}
	for _, v := range c.Store.snapshot().Operations {
		if v.ID == op.ID && v.Status != "running" {
			t.Fatal("terminal operation exposed before inventory finished")
		}
	}
	release.Do(func() { close(barrier.release) })
	deadline := time.Now().Add(3 * time.Second)
	for {
		complete := false
		for _, v := range c.Store.snapshot().Operations {
			if v.ID == op.ID && v.Status == "succeeded" {
				complete = true
			}
		}
		if complete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first job did not complete")
		}
		time.Sleep(time.Millisecond)
	}
	// Do not wait on the internal gate: emulate a client reacting to terminal status.
	next, err := c.Submit("next mutation", nil, func(context.Context) error { return nil })
	succeeded(t, c, next, err)
}
