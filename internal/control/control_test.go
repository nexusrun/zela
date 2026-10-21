package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zela-dev/zela/internal/engine"
)

type testEngine struct {
	*DemoEngine
	failStart, failRemove, failRestore, unhealthy, volumes, unreachable bool
	sharedVolumes                                                       []engine.SharedVolumeSpec
	events                                                              *[]string
	eventMu                                                             *sync.Mutex
}

func (e *testEngine) Create(ctx context.Context, spec engine.Spec) (string, error) {
	e.record("create")
	return e.DemoEngine.Create(ctx, spec)
}
func (e *testEngine) EnsureSharedVolume(_ context.Context, spec engine.SharedVolumeSpec) error {
	e.sharedVolumes = append(e.sharedVolumes, spec)
	e.record("prepare-volume")
	return nil
}

func (e *testEngine) record(v string) {
	e.eventMu.Lock()
	defer e.eventMu.Unlock()
	*e.events = append(*e.events, e.name+":"+v)
}
func (e *testEngine) Info(ctx context.Context) (engine.Info, error) {
	if e.unreachable {
		return engine.Info{}, errors.New("offline")
	}
	return e.DemoEngine.Info(ctx)
}
func (e *testEngine) Start(ctx context.Context, id string) error {
	e.record("start")
	if e.failStart || e.failRestore {
		return errors.New("start failed")
	}
	return e.DemoEngine.Start(ctx, id)
}
func (e *testEngine) Stop(ctx context.Context, id string) error {
	e.record("stop")
	return e.DemoEngine.Stop(ctx, id)
}
func (e *testEngine) Remove(ctx context.Context, id string) error {
	e.record("remove")
	if e.failRemove {
		return errors.New("remove failed")
	}
	return e.DemoEngine.Remove(ctx, id)
}
func (e *testEngine) Inspect(ctx context.Context, id string) (engine.Inspection, error) {
	e.record("inspect")
	v, err := e.DemoEngine.Inspect(ctx, id)
	if err != nil {
		return v, err
	}
	v.State.Health = &struct{ Status string }{Status: "healthy"}
	if e.unhealthy {
		v.State.Health.Status = "unhealthy"
	}
	if e.volumes {
		v.Mounts = append(v.Mounts, struct {
			Type        string
			Name        string
			Source      string
			Destination string
			RW          bool
		}{Type: "volume", Name: "data", Source: "data", Destination: "/data"})
	}
	return v, nil
}

func fixture(t *testing.T) (*Controller, map[string]*testEngine) {
	t.Helper()
	s, err := OpenStore(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	var mu sync.Mutex
	es := map[string]*testEngine{}
	for _, id := range []string{"w1", "w2"} {
		e := &testEngine{DemoEngine: &DemoEngine{name: id, containers: map[string]engine.Container{}, specs: map[string]engine.Spec{}}, events: &events, eventMu: &mu}
		es[id] = e
		info, _ := e.Info(context.Background())
		if err := s.update(func(s *State) error {
			s.Workers[id] = Worker{ID: id, Name: id, Endpoint: "unix:///" + id, Status: "online", Mode: "active", Info: info, Containers: []engine.Container{}}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	c := New(s, func(w Worker) (engine.Engine, error) {
		e := es[w.ID]
		if e == nil {
			return nil, errors.New("not found")
		}
		return e, nil
	})
	c.readyTimeout = 100 * time.Millisecond
	t.Cleanup(c.Close)
	return c, es
}
func waitOp(t *testing.T, c *Controller, op Operation, err error) Operation {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(c.gate) == 0 {
			for _, o := range c.Store.snapshot().Operations {
				if o.ID == op.ID {
					return o
				}
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("operation timed out")
	return Operation{}
}
func succeeded(t *testing.T, c *Controller, op Operation, err error) {
	t.Helper()
	o := waitOp(t, c, op, err)
	if o.Status != "succeeded" {
		t.Fatalf("%s: %s", o.Title, o.Detail)
	}
}
func deployOne(t *testing.T, c *Controller) Deployment {
	t.Helper()
	op, err := c.CreateDeployment(Deployment{Name: "web", Image: "nginx:alpine", Replicas: 1, WorkerIDs: []string{"w1"}, MemoryMB: 128, CPUs: .5})
	succeeded(t, c, op, err)
	for _, d := range c.Store.snapshot().Deployments {
		return d
	}
	t.Fatal("missing deployment")
	return Deployment{}
}
func TestDeploymentLifecycle(t *testing.T) {
	c, es := fixture(t)
	d := deployOne(t, c)
	for _, v := range []struct {
		action string
		n      int
	}{{"scale", 2}, {"stop", 0}, {"start", 0}, {"restart", 0}, {"scale", 1}} {
		op, err := c.DeploymentAction(d.ID, v.action, "", v.n)
		succeeded(t, c, op, err)
	}
	op, err := c.DeploymentAction(d.ID, "delete", "", 0)
	succeeded(t, c, op, err)
	if len(c.Store.snapshot().Deployments) != 0 {
		t.Fatal("deployment not removed")
	}
	cts, _ := es["w1"].List(context.Background())
	if len(cts) != 0 {
		t.Fatal("containers leaked")
	}
}
func TestMigrationChecksTargetBeforeStoppingSource(t *testing.T) {
	c, es := fixture(t)
	d := deployOne(t, c)
	op, err := c.DeploymentAction(d.ID, "move", "w2", 0)
	succeeded(t, c, op, err)
	d, _ = c.deployment(d.ID)
	if len(d.Tasks) != 1 || d.Tasks[0].WorkerID != "w2" {
		t.Fatalf("wrong assignments: %+v", d.Tasks)
	}
	events := *es["w1"].events
	inspect, stop := -1, -1
	for i, v := range events {
		if v == "w2:inspect" {
			inspect = i
		}
		if v == "w1:stop" {
			stop = i
		}
	}
	if inspect < 0 || stop < inspect {
		t.Fatalf("unsafe order: %v", events)
	}
}
func TestMigrationFailureRetainsSource(t *testing.T) {
	for _, failure := range []string{"start", "health", "volume", "offline"} {
		t.Run(failure, func(t *testing.T) {
			c, es := fixture(t)
			d := deployOne(t, c)
			source := d.Tasks[0].ID
			switch failure {
			case "start":
				es["w2"].failStart = true
			case "health":
				es["w2"].unhealthy = true
			case "volume":
				es["w1"].volumes = true
			case "offline":
				es["w2"].unreachable = true
			}
			op, err := c.DeploymentAction(d.ID, "move", "w2", 0)
			o := waitOp(t, c, op, err)
			if o.Status != "failed" {
				t.Fatalf("unexpected success: %+v", o)
			}
			v, _ := es["w1"].DemoEngine.Inspect(context.Background(), source)
			if !v.State.Running {
				t.Fatal("source stopped on failed move")
			}
			cts, _ := es["w2"].List(context.Background())
			if len(cts) != 0 {
				t.Fatal("failed target leaked")
			}
			d, _ = c.deployment(d.ID)
			if len(d.Tasks) != 1 || d.Tasks[0].ID != source {
				t.Fatalf("source assignment lost: %+v", d.Tasks)
			}
		})
	}
}
func TestMigrationRemovalFailureRollsBack(t *testing.T) {
	c, es := fixture(t)
	d := deployOne(t, c)
	source := d.Tasks[0].ID
	es["w1"].failRemove = true
	op, err := c.DeploymentAction(d.ID, "move", "w2", 0)
	o := waitOp(t, c, op, err)
	if o.Status != "failed" {
		t.Fatal("expected failure")
	}
	v, _ := es["w1"].DemoEngine.Inspect(context.Background(), source)
	if !v.State.Running {
		t.Fatal("source not restored")
	}
	cts, _ := es["w2"].List(context.Background())
	if len(cts) != 0 {
		t.Fatal("target not rolled back")
	}
}
func TestRollbackKeepsReadyTargetWhenSourceCannotBeRestored(t *testing.T) {
	c, es := fixture(t)
	d := deployOne(t, c)
	es["w1"].failRemove = true
	es["w1"].failRestore = true
	op, err := c.DeploymentAction(d.ID, "move", "w2", 0)
	o := waitOp(t, c, op, err)
	if o.Status != "failed" || !strings.Contains(o.Detail, "retained") {
		t.Fatalf("wrong recovery: %+v", o)
	}
	cts, _ := es["w2"].List(context.Background())
	if len(cts) != 1 || cts[0].State != "running" {
		t.Fatal("ready replacement discarded")
	}
	d, _ = c.deployment(d.ID)
	if len(d.Tasks) != 2 {
		t.Fatal("recovery assignments not recorded")
	}
}
func TestResourceAndPortScheduling(t *testing.T) {
	c, _ := fixture(t)
	d := deployOne(t, c)
	s := c.Store.snapshot()
	w := s.Workers["w1"]
	w.Info.NCPU = 1
	d.CPUs = 1
	if fits(s, d, w) == nil {
		t.Fatal("overcommitted CPU")
	}
	d.CPUs = .1
	d.Ports = []engine.Port{{Host: 8080, Container: 80, Protocol: "tcp"}}
	original := s.Deployments[d.ID]
	original.Ports = d.Ports
	s.Deployments[d.ID] = original
	if fits(s, d, s.Workers["w1"]) == nil {
		t.Fatal("duplicate host port permitted")
	}
	d.Ports[0].Host = 0
	if err := fits(s, d, s.Workers["w1"]); err != nil {
		t.Fatal(err)
	}
}
func TestDrainAndPause(t *testing.T) {
	c, _ := fixture(t)
	d := deployOne(t, c)
	op, err := c.WorkerAction("w1", "pause")
	succeeded(t, c, op, err)
	if c.Store.snapshot().Workers["w1"].Mode != "paused" {
		t.Fatal("pause failed")
	}
	op, err = c.WorkerAction("w1", "drain")
	succeeded(t, c, op, err)
	d, _ = c.deployment(d.ID)
	if d.Tasks[0].WorkerID != "w2" || c.Store.snapshot().Workers["w1"].Mode != "drained" {
		t.Fatal("drain failed")
	}
	op, err = c.WorkerAction("w1", "delete")
	succeeded(t, c, op, err)
}
func TestDrainFlagsUnmanagedContainers(t *testing.T) {
	c, es := fixture(t)
	id, err := es["w1"].Create(context.Background(), engine.Spec{Name: "existing", Image: "nginx"})
	if err != nil {
		t.Fatal(err)
	}
	_ = es["w1"].Start(context.Background(), id)
	op, err := c.WorkerAction("w1", "drain")
	o := waitOp(t, c, op, err)
	if o.Status != "failed" || !strings.Contains(o.Detail, "unmanaged") {
		t.Fatalf("unexpected result: %+v", o)
	}
	if c.Store.snapshot().Workers["w1"].Mode != "draining" {
		t.Fatal("draining host became schedulable")
	}
}
func TestWorkerDeleteUsesLiveInventory(t *testing.T) {
	c, es := fixture(t)
	_, err := es["w1"].Create(context.Background(), engine.Spec{Name: "new", Image: "nginx"})
	if err != nil {
		t.Fatal(err)
	}
	op, err := c.WorkerAction("w1", "delete")
	o := waitOp(t, c, op, err)
	if o.Status != "failed" {
		t.Fatal("unregistered host with unpolled containers")
	}
}
func TestUpdateImageAndMoveReplica(t *testing.T) {
	c, es := fixture(t)
	d := deployOne(t, c)
	op, err := c.UpdateImage(d.ID, "nginx:stable", "w2")
	succeeded(t, c, op, err)
	d, _ = c.deployment(d.ID)
	if d.Image != "nginx:stable" || d.Tasks[0].Image != "nginx:stable" {
		t.Fatal("image not updated")
	}
	ct, _ := es["w2"].List(context.Background())
	if ct[0].Image != "nginx:stable" {
		t.Fatal("Docker received old image")
	}
	op, err = c.MoveReplica(d.ID, d.Tasks[0].ID, "w1")
	succeeded(t, c, op, err)
	d, _ = c.deployment(d.ID)
	if d.Tasks[0].WorkerID != "w1" {
		t.Fatal("replica not moved")
	}
}
func TestOperationsSerialize(t *testing.T) {
	c, _ := fixture(t)
	release := make(chan struct{})
	op, err := c.Submit("blocked", nil, func(ctx context.Context) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Submit("second", nil, func(context.Context) error { return nil }); err == nil {
		t.Fatal("concurrent mutation accepted")
	}
	close(release)
	succeeded(t, c, op, nil)
}
func TestStatePersistenceRedactsSecretsAndRecoversInterruptedJobs(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.update(func(s *State) error {
		s.Workers["w"] = Worker{ID: "w", Token: "private-worker-token", Status: "provisioning"}
		s.Operations = []Operation{{ID: "o", Status: "running", Steps: []OperationStep{{Name: "Install Docker", Status: "running"}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if s.public().Workers["w"].Token != "" {
		t.Fatal("public state leaks worker token")
	}
	info, err := os.Stat(filepath.Join(dir, "cluster.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe mode: %o", info.Mode().Perm())
	}
	_ = s.Close()
	reloaded, err := OpenStore(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.snapshot().Workers["w"].Token != "private-worker-token" {
		t.Fatal("secret not persisted")
	}
	if reloaded.snapshot().Operations[0].Status != "failed" {
		t.Fatal("interrupted job not flagged")
	}
	if reloaded.snapshot().Operations[0].Steps[0].Status != "failed" || reloaded.snapshot().Workers["w"].Status != "provision-failed" || reloaded.snapshot().Workers["w"].Mode != "paused" {
		t.Fatal("interrupted provisioning remained running or schedulable")
	}
	_ = reloaded.Close()
	if _, err := OpenStore(dir, true); err == nil {
		t.Fatal("demo accepted live state")
	}
}
func TestStoreWriteFailureRollsBackMemory(t *testing.T) {
	s, err := OpenStore(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(t.TempDir(), "missing", "cluster.json")
	err = s.update(func(s *State) error { s.Workers["w"] = Worker{ID: "w"}; return nil })
	if err == nil {
		t.Fatal("expected write failure")
	}
	if len(s.snapshot().Workers) != 0 {
		t.Fatal("unpersisted state exposed")
	}
}

func TestStateDirectoryCannotBeSharedByTwoManagers(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if second, err := OpenStore(dir, false); err == nil {
		second.Close()
		t.Fatal("second manager acquired same state directory")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
}

func (e *testEngine) VerifySharedVolume(ctx context.Context, p engine.SharedVolumeProbe) error {
	e.record("verify-volume")
	return nil
}
