package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zela-dev/zela/internal/engine"
)

func TestStorageBackendValidationRejectsOptionInjection(t *testing.T) {
	base := StorageBackend{Name: "nfs-main", Server: "nas.internal", Export: "/exports/apps", WorkerIDs: []string{"w1"}}
	for _, server := range []string{"nas.internal,soft", "nas.internal:2049", "bad host", "nas/host"} {
		candidate := base
		candidate.Server = server
		if err := validateStorageBackend(candidate); err == nil {
			t.Fatalf("accepted unsafe NFS server %q", server)
		}
	}
	for _, export := range []string{"relative", "/exports/../etc"} {
		candidate := base
		candidate.Export = export
		if err := validateStorageBackend(candidate); err == nil {
			t.Fatalf("accepted invalid export %q", export)
		}
	}
	base.Server = "2001:db8::10"
	if err := validateStorageBackend(base); err != nil {
		t.Fatalf("valid IPv6 server rejected: %v", err)
	}
}

func TestStorageStateLoadsLegacyJSONAndPersistsRegistrations(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"workers":{},"deployments":{},"operations":[],"demo":false}`
	if err := os.WriteFile(filepath.Join(dir, "cluster.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.update(func(s *State) error {
		s.StorageBackends = map[string]StorageBackend{"nfs_1": {ID: "nfs_1", Name: "files", Server: "nfs.internal", Export: "/exports", WorkerIDs: []string{"worker_1"}}}
		s.SharedVolumes = map[string]SharedVolume{"vol_1": {ID: "vol_1", Name: "uploads", BackendID: "nfs_1", Directory: "app/uploads"}}
		d := Deployment{ID: "dep_1", Name: "app", Image: "example/app:v1", ImageDigest: "example/app@sha256:abc", WorkloadPolicy: WorkloadSingleWriter, StorageAttachments: []VolumeAttachment{{VolumeID: "vol_1", Target: "/data"}}, Tasks: []Replica{{ID: "task_1", WorkerID: "worker_1", Lifecycle: "serving"}}}
		s.Deployments[d.ID] = d
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s := reopened.snapshot()
	if s.SharedVolumes["vol_1"].Directory != "app/uploads" || s.StorageBackends["nfs_1"].Server != "nfs.internal" || s.Deployments["dep_1"].ImageDigest != "example/app@sha256:abc" || s.Deployments["dep_1"].Tasks[0].Lifecycle != "serving" {
		t.Fatalf("storage or handover state did not round-trip: %+v", s)
	}
}

func TestValidateStorageAttachmentsAndMoveMounts(t *testing.T) {
	s := State{
		StorageBackends: map[string]StorageBackend{"nfs": {ID: "nfs", Server: "nas.internal", Export: "/exports"}},
		SharedVolumes:   map[string]SharedVolume{"vol_a": {ID: "vol_a", BackendID: "nfs"}},
	}
	d := Deployment{StorageAttachments: []VolumeAttachment{{VolumeID: "vol_a", Target: "/var/lib/app"}}, WorkloadPolicy: WorkloadOverlapSafe}
	if err := validateAttachments(&d, s); err != nil {
		t.Fatal(err)
	}
	d.WorkloadPolicy = ""
	if err := validateAttachments(&d, s); err == nil {
		t.Fatal("accepted writable shared volume without explicit policy")
	}
	d.WorkloadPolicy = WorkloadOverlapSafe
	for _, target := range []string{"relative", "/", "/data/../etc"} {
		d.StorageAttachments[0].Target = target
		if err := validateAttachments(&d, s); err == nil {
			t.Fatalf("accepted unsafe target %q", target)
		}
	}
	d.StorageAttachments[0].Target = "/var/lib/app"
	inspection := engine.Inspection{}
	inspection.Mounts = append(inspection.Mounts, struct {
		Type        string
		Name        string
		Source      string
		Destination string
		RW          bool
	}{Type: "volume", Name: "zela-a", Destination: "/var/lib/app", RW: true})
	if err := validateManagedTaskMounts(d, inspection); err != nil {
		t.Fatalf("registered mount rejected: %v", err)
	}
	inspection.Mounts[0].Name = "unregistered"
	if err := validateManagedTaskMounts(d, inspection); err == nil {
		t.Fatal("unregistered mount accepted")
	}
}

func TestManagedSharedVolumeSurvivesSingleWriterMove(t *testing.T) {
	c, engines := fixture(t)
	backend, err := c.RegisterStorageBackend(StorageBackend{Name: "test-nfs", Server: "nas.internal", Export: "/exports", WorkerIDs: []string{"w1", "w2"}})
	if err != nil {
		t.Fatal(err)
	}
	volume, err := c.RegisterSharedVolume(SharedVolume{Name: "uploads", BackendID: backend.ID, Directory: "app/uploads"})
	if err != nil {
		t.Fatal(err)
	}
	op, err := c.CreateDeployment(Deployment{Name: "persistent-app", Image: "example/app:v1", Replicas: 1, WorkerIDs: []string{"w1"}, MemoryMB: 128, CPUs: .5, WorkloadPolicy: WorkloadSingleWriter, StorageAttachments: []VolumeAttachment{{VolumeID: volume.ID, Target: "/srv/uploads"}}})
	succeeded(t, c, op, err)
	d := c.Store.snapshot().Deployments[findDeploymentID(t, c, "persistent-app")]
	if len(d.Tasks) != 1 || d.Tasks[0].Lifecycle != "serving" {
		t.Fatalf("deployment task was not marked serving: %+v", d.Tasks)
	}
	engines["w1"].eventMu.Lock()
	*engines["w1"].events = nil
	engines["w1"].eventMu.Unlock()
	engines["w2"].eventMu.Lock()
	*engines["w2"].events = nil
	engines["w2"].eventMu.Unlock()
	op, err = c.MoveReplica(d.ID, d.Tasks[0].ID, "w2")
	succeeded(t, c, op, err)
	updated := c.Store.snapshot().Deployments[d.ID]
	if len(updated.Tasks) != 1 || updated.Tasks[0].WorkerID != "w2" {
		t.Fatalf("task did not move: %+v", updated.Tasks)
	}
	if len(engines["w2"].sharedVolumes) != 1 || engines["w2"].sharedVolumes[0].Server != "nas.internal" || engines["w2"].sharedVolumes[0].Export != "/exports/app/uploads" {
		t.Fatalf("destination volume was not prepared: %+v", engines["w2"].sharedVolumes)
	}
	createdID := updated.Tasks[0].ID
	inspect, err := engines["w2"].Inspect(context.Background(), createdID)
	if err != nil || len(inspect.Mounts) != 1 || inspect.Mounts[0].Name != managedVolumeName(volume.ID) || inspect.Mounts[0].Destination != "/srv/uploads" {
		t.Fatalf("replacement did not use the same registered volume: %+v %v", inspect.Mounts, err)
	}
	var stopAt, createAt int = -1, -1
	engines["w1"].eventMu.Lock()
	for i, event := range *engines["w1"].events {
		if event == "w1:stop" {
			stopAt = i
		}
	}
	engines["w1"].eventMu.Unlock()
	engines["w2"].eventMu.Lock()
	for i, event := range *engines["w2"].events {
		if event == "w2:create" {
			createAt = i
		}
	}
	engines["w2"].eventMu.Unlock()
	if stopAt < 0 || createAt < 0 {
		t.Fatalf("missing move ordering events: source=%d destination=%d", stopAt, createAt)
	}
}

func findDeploymentID(t *testing.T, c *Controller, name string) string {
	t.Helper()
	for id, d := range c.Store.snapshot().Deployments {
		if d.Name == name {
			return id
		}
	}
	t.Fatalf("deployment %q not found", name)
	return ""
}

func TestStorageAPIIsAdministratorOnly(t *testing.T) {
	c, _ := fixture(t)
	h := Handler(c, HTTPConfig{Token: testToken})
	for _, scope := range []string{"mcp_manage", "deployments", "infrastructure"} {
		token, _, err := c.Store.createAccessToken("test", 1, scope)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("GET", "http://control.local/api/storage", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("scope %s received %d: %s", scope, w.Code, w.Body.String())
		}
	}
	deploymentToken, _, err := c.Store.createAccessToken("deploy", 1, "deployments")
	if err != nil {
		t.Fatal(err)
	}
	create := httptest.NewRequest("POST", "http://control.local/api/deployments", strings.NewReader(`{"name":"app","image":"nginx","replicas":1,"memoryMB":128,"cpus":0.5,"storageAttachments":[{"volumeID":"vol_secret","target":"/data"}],"workloadPolicy":"overlap-safe"}`))
	create.Header.Set("Authorization", "Bearer "+deploymentToken)
	created := httptest.NewRecorder()
	h.ServeHTTP(created, create)
	if created.Code != http.StatusForbidden {
		t.Fatalf("deployment token could attach a shared volume: %d %s", created.Code, created.Body.String())
	}
	r := httptest.NewRequest("GET", "http://control.local/api/storage", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"backends"`) {
		t.Fatalf("admin storage listing failed: %d %s", w.Code, w.Body.String())
	}
}
