package control

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zela-dev/zela/internal/clusternet"
	"github.com/zela-dev/zela/internal/engine"
)

func storageDeployment(t *testing.T, c *Controller) Deployment {
	t.Helper()
	backend, err := c.RegisterStorageBackend(StorageBackend{Name: "files", Server: "nas.internal", Export: "/exports", WorkerIDs: []string{"w1", "w2"}})
	if err != nil {
		t.Fatal(err)
	}
	volume, err := c.RegisterSharedVolume(SharedVolume{Name: "data", BackendID: backend.ID, Directory: "app/data"})
	if err != nil {
		t.Fatal(err)
	}
	return Deployment{Name: "persistent", Image: "example/app:v1", Replicas: 1, WorkerIDs: []string{"w1"}, MemoryMB: 128, CPUs: .5, WorkloadPolicy: WorkloadSingleWriter, StorageAttachments: []VolumeAttachment{{VolumeID: volume.ID, Target: "/data"}}}
}

func TestSingleWriterRejectsMultipleReplicasAndOtherWriters(t *testing.T) {
	c, _ := fixture(t)
	d := storageDeployment(t, c)
	d.Replicas = 2
	if _, err := c.CreateDeployment(d); err == nil {
		t.Fatal("accepted two exclusive writers")
	}
	d.Replicas = 1
	op, err := c.CreateDeployment(d)
	succeeded(t, c, op, err)
	current := c.Store.snapshot().Deployments[findDeploymentID(t, c, d.Name)]
	if _, err = c.DeploymentAction(current.ID, "scale", "", 2); err == nil {
		t.Fatal("scaled exclusive writer")
	}
	d.Name = "other"
	d.WorkloadPolicy = WorkloadOverlapSafe
	if _, err = c.CreateDeployment(d); err == nil {
		t.Fatal("another deployment acquired exclusive volume")
	}
}

func TestSingleWriterRollbackStopsTargetBeforeRestoringSource(t *testing.T) {
	c, es := fixture(t)
	d := storageDeployment(t, c)
	op, err := c.CreateDeployment(d)
	succeeded(t, c, op, err)
	d = c.Store.snapshot().Deployments[findDeploymentID(t, c, d.Name)]
	es["w1"].failRemove = true
	es["w1"].eventMu.Lock()
	*es["w1"].events = nil
	es["w1"].eventMu.Unlock()
	op, err = c.MoveReplica(d.ID, d.Tasks[0].ID, "w2")
	result := waitOp(t, c, op, err)
	if result.Status != "failed" {
		t.Fatal("expected cleanup failure")
	}
	stop, start := -1, -1
	es["w1"].eventMu.Lock()
	defer es["w1"].eventMu.Unlock()
	for i, event := range *es["w1"].events {
		if event == "w2:stop" {
			stop = i
		}
		if event == "w1:start" {
			start = i
		}
	}
	if stop < 0 || start < 0 || start < stop {
		t.Fatalf("unsafe rollback ordering: %v", *es["w1"].events)
	}
}

type uncertainTarget struct{ *testEngine }

func (e uncertainTarget) Start(ctx context.Context, id string) error {
	_ = e.testEngine.Start(ctx, id)
	return errors.New("lost start response")
}
func (e uncertainTarget) Stop(context.Context, string) error { return errors.New("worker unreachable") }
func (e uncertainTarget) Remove(context.Context, string) error {
	return errors.New("worker unreachable")
}

func TestSingleWriterUncertainTargetKeepsSourceStopped(t *testing.T) {
	c, es := fixture(t)
	d := storageDeployment(t, c)
	op, err := c.CreateDeployment(d)
	succeeded(t, c, op, err)
	d = c.Store.snapshot().Deployments[findDeploymentID(t, c, d.Name)]
	c.Factory = func(w Worker) (engine.Engine, error) {
		if w.ID == "w2" {
			return uncertainTarget{es[w.ID]}, nil
		}
		return es[w.ID], nil
	}
	op, err = c.MoveReplica(d.ID, d.Tasks[0].ID, "w2")
	result := waitOp(t, c, op, err)
	if result.Status != "failed" {
		t.Fatal("expected uncertainty to fail closed")
	}
	source, _ := es["w1"].Inspect(context.Background(), d.Tasks[0].ID)
	if source.State.Running {
		t.Fatal("source restarted while target could still be writing")
	}
	current, _ := c.deployment(d.ID)
	if !pendingHandover(current) {
		t.Fatal("lost recovery record")
	}
	if _, err := c.DeploymentAction(d.ID, "start", "", 0); err == nil {
		t.Fatal("start bypassed pending handover")
	}
}

type lostCreateEngine struct {
	*testEngine
	lose bool
}

func (e *lostCreateEngine) Create(ctx context.Context, s engine.Spec) (string, error) {
	id, err := e.testEngine.Create(ctx, s)
	if err == nil && e.lose {
		e.lose = false
		return "", errors.New("lost create response")
	}
	return id, err
}

func TestHandoverRetryFindsReplacementAfterLostCreateResponse(t *testing.T) {
	c, es := fixture(t)
	d := deployOne(t, c)
	target := &lostCreateEngine{testEngine: es["w2"], lose: true}
	c.Factory = func(w Worker) (engine.Engine, error) {
		if w.ID == "w2" {
			return target, nil
		}
		return es[w.ID], nil
	}
	if err := c.replaceTask(context.Background(), d, d.Tasks[0], "w2"); err == nil {
		t.Fatal("expected lost response")
	}
	current, _ := c.deployment(d.ID)
	if !pendingHandover(current) {
		t.Fatal("missing durable handover")
	}
	// Reopen the on-disk journal, preserving the simulated Docker host.
	dir := filepath.Dir(c.Store.path)
	if err := c.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	c.Store = reopened
	current, _ = c.deployment(d.ID)
	recovered := New(c.Store, c.Factory)
	defer recovered.cancel()
	if err := recovered.replaceTask(context.Background(), current, d.Tasks[0], "w2"); err != nil {
		t.Fatal(err)
	}
	containers, _ := target.List(context.Background())
	if len(containers) != 1 {
		t.Fatalf("created duplicate replacement: %d", len(containers))
	}
	current, _ = c.deployment(d.ID)
	if pendingHandover(current) || len(current.Tasks) != 1 {
		t.Fatalf("handover incomplete: %+v", current)
	}
}

type legacyImageEngine struct{ *testEngine }

func (e legacyImageEngine) Inspect(ctx context.Context, id string) (engine.Inspection, error) {
	v, err := e.testEngine.Inspect(ctx, id)
	v.Image = "sha256:config-id"
	return v, err
}
func (e legacyImageEngine) ResolveImage(_ context.Context, ref string) (string, error) {
	if strings.HasPrefix(ref, "sha256:") {
		return "sha256:config-id", nil
	}
	return "example/app@sha256:manifest", nil
}

func TestLegacyMoveComparesConfigurationIDsToConfigurationIDs(t *testing.T) {
	c, es := fixture(t)
	d := deployOne(t, c)
	c.Factory = func(w Worker) (engine.Engine, error) { return legacyImageEngine{es[w.ID]}, nil }
	op, err := c.MoveReplica(d.ID, d.Tasks[0].ID, "w2")
	succeeded(t, c, op, err)
	current, _ := c.deployment(d.ID)
	if current.ImageDigest != "sha256:config-id" {
		t.Fatal(current.ImageDigest)
	}
}

type endpointEngine struct {
	*netEngine
	ip string
}

func (e endpointEngine) List(ctx context.Context) ([]engine.Container, error) {
	list, err := e.netEngine.List(ctx)
	for i := range list {
		list[i].NetworkSettings = &struct {
			Networks map[string]struct{ IPAddress string }
		}{Networks: map[string]struct{ IPAddress string }{clusternet.DockerNetwork: {IPAddress: e.ip}}}
	}
	return list, err
}

func TestHandoverAcknowledgesObservedReplacementEndpoint(t *testing.T) {
	c, nets := networkFixture(t)
	c.Factory = func(w Worker) (engine.Engine, error) {
		ip := "10.200.1.2"
		if w.ID == "w2" {
			ip = "10.200.2.2"
		}
		return endpointEngine{nets[w.ID], ip}, nil
	}
	if err := c.SetEdge(80, 0); err != nil {
		t.Fatal(err)
	}
	c.Refresh(context.Background())
	op, err := c.CreateDeployment(Deployment{Name: "web", Image: "nginx", Replicas: 1, WorkerIDs: []string{"w1"}, MemoryMB: 128, CPUs: .5, Routes: []Route{{Host: "app.example", Port: 80}}})
	succeeded(t, c, op, err)
	d, _ := c.deployment(findDeploymentID(t, c, "web"))
	if err := c.replaceTask(context.Background(), d, d.Tasks[0], "w2"); err != nil {
		t.Fatal(err)
	}
	for id, network := range nets {
		services := network.last().Services
		if len(services) != 1 || len(services[0].Backends) != 1 || services[0].Backends[0] != "10.200.2.2" {
			t.Fatalf("%s acknowledged missing replacement: %+v", id, services)
		}
	}
}

func TestHandoverRejectsOfflineCapturedIngress(t *testing.T) {
	c, _ := networkFixture(t)
	c.Refresh(context.Background())
	if err := c.Store.update(func(s *State) error {
		w := s.Workers["w2"]
		w.Status = "offline"
		w.LastSeen = time.Now().Add(-time.Hour)
		s.Workers[w.ID] = w
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.applyAndVerifyNetwork(context.Background()); err == nil {
		t.Fatal("offline ingress incorrectly excluded from barrier")
	}
}

type deniedStorageEngine struct{ *testEngine }

func (e deniedStorageEngine) VerifySharedVolume(context.Context, engine.SharedVolumeProbe) error {
	return errors.New("identity mismatch")
}

func TestStorageAccessFailureDoesNotStopOriginalWriter(t *testing.T) {
	c, es := fixture(t)
	d := storageDeployment(t, c)
	op, err := c.CreateDeployment(d)
	succeeded(t, c, op, err)
	d, _ = c.deployment(findDeploymentID(t, c, d.Name))
	c.Factory = func(w Worker) (engine.Engine, error) {
		if w.ID == "w2" {
			return deniedStorageEngine{es[w.ID]}, nil
		}
		return es[w.ID], nil
	}
	op, err = c.MoveReplica(d.ID, d.Tasks[0].ID, "w2")
	result := waitOp(t, c, op, err)
	if result.Status != "failed" || !strings.Contains(result.Detail, "identity mismatch") {
		t.Fatalf("unexpected outcome: %+v", result)
	}
	state, _ := es["w1"].Inspect(context.Background(), d.Tasks[0].ID)
	if !state.State.Running {
		t.Fatal("storage preflight stopped source")
	}
	access := c.Store.snapshot().SharedVolumes[d.StorageAttachments[0].VolumeID].Access["w2"]
	if access.CheckedAt.IsZero() || access.Error == "" || access.BackendRevision != 1 {
		t.Fatalf("lost storage health observation: %+v", access)
	}
}

func TestHandoverCanResumeAfterSourceForgotten(t *testing.T) {
	c, es := fixture(t)
	d := deployOne(t, c)
	target, err := c.provision(context.Background(), d, c.Store.snapshot().Workers["w2"])
	if err != nil {
		t.Fatal(err)
	}
	if err = es["w1"].Remove(context.Background(), d.Tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = c.forgetTask(d.ID, d.Tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = c.editDeployment(d.ID, func(current *Deployment) {
		current.Handover = &Handover{ID: "move_recovered", Source: d.Tasks[0], TargetWorkerID: "w2", Replacement: target, Stage: "retiring-source"}
	}); err != nil {
		t.Fatal(err)
	}
	op, err := c.MoveReplica(d.ID, d.Tasks[0].ID, "w2")
	succeeded(t, c, op, err)
	current, _ := c.deployment(d.ID)
	if pendingHandover(current) || len(current.Tasks) != 1 {
		t.Fatalf("could not finish final checkpoint: %+v", current)
	}
}

func TestStatelessNetworkRetainsLegacyFormatUntilHandover(t *testing.T) {
	c, _ := networkFixture(t)
	if err := c.SetEdge(80, 0); err != nil {
		t.Fatal(err)
	}
	c.Refresh(context.Background())
	d := deployOne(t, c)
	cfg, err := c.networkConfig(c.Store.snapshot(), "w1")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Edge.ServiceGeneration != "" {
		t.Fatal("added unknown generation field to legacy stateless config")
	}
	if err = c.editDeployment(d.ID, func(d *Deployment) { d.Handover = &Handover{ID: "move_test", Stage: "preflight"} }); err != nil {
		t.Fatal(err)
	}
	cfg, err = c.networkConfig(c.Store.snapshot(), "w1")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Edge.ServiceGeneration == "" {
		t.Fatal("handover missing generation")
	}
}
