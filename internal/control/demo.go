package control

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zela-dev/zela/internal/engine"
	"github.com/zela-dev/zela/internal/telemetry"
)

// Demo engines never access Docker. They support the same control-plane flows.
type DemoEngine struct {
	networks   map[string]engine.Network
	mu         sync.Mutex
	name       string
	containers map[string]engine.Container
	specs      map[string]engine.Spec
}

func (d *DemoEngine) Metrics(context.Context) (telemetry.Sample, error) {
	return d.demoMetrics(time.Now()), nil
}
func (d *DemoEngine) demoMetrics(now time.Time) telemetry.Sample {
	d.mu.Lock()
	defer d.mu.Unlock()
	wave := math.Sin(float64(now.Unix()) / 120)
	cpu := 38 + wave*14
	rx, tx := 380000+wave*90000, 180000-wave*50000
	read, write := 1200000+wave*500000, 560000-wave*120000
	h := telemetry.Host{CPUPercent: &cpu, MemoryTotalBytes: 16 << 30, MemoryUsedBytes: 6 << 30, MemoryPercent: 37.5, UptimeSeconds: 86500, Disks: []telemetry.Disk{{Path: "/", TotalBytes: 200 << 30, UsedBytes: 122 << 30, AvailableBytes: 78 << 30, UsedPercent: 61}}, Network: telemetry.IO{ReadBPS: &rx, WriteBPS: &tx}, BlockIO: telemetry.IO{ReadBPS: &read, WriteBPS: &write}}
	s := telemetry.Sample{Time: now.UTC(), Host: &h, Containers: []telemetry.Container{}}
	for _, ct := range d.containers {
		if ct.State != "running" {
			continue
		}
		p := 12 + wave*6
		s.Containers = append(s.Containers, telemetry.Container{ID: ct.ID, Name: strings.TrimPrefix(firstName(ct), "/"), CPUPercent: &p, MemoryUsedBytes: 96 << 20, MemoryLimitBytes: 256 << 20, MemoryPercent: 37.5, Network: h.Network, BlockIO: h.BlockIO})
	}
	sort.Slice(s.Containers, func(i, j int) bool { return s.Containers[i].ID < s.Containers[j].ID })
	return s
}

func (d *DemoEngine) Info(context.Context) (engine.Info, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	running := 0
	for _, ct := range d.containers {
		if ct.State == "running" {
			running++
		}
	}
	return engine.Info{Name: d.name, NCPU: 8, MemTotal: 16 * 1024 * 1024 * 1024, ContainersRunning: running, ServerVersion: "28.0.4 (simulated)", OperatingSystem: "Ubuntu 24.04 LTS", Architecture: "x86_64"}, nil
}
func (d *DemoEngine) List(context.Context) ([]engine.Container, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []engine.Container{}
	for _, ct := range d.containers {
		out = append(out, ct)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (d *DemoEngine) Inspect(_ context.Context, id string) (engine.Inspection, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ct, ok := d.containers[id]
	if !ok {
		return engine.Inspection{}, fmt.Errorf("Docker 404: container not found")
	}
	v := engine.Inspection{ID: id}
	v.State.Running = ct.State == "running"
	v.State.Status = ct.State
	v.Config.Image = ct.Image
	if spec, exists := d.specs[id]; exists {
		if len(spec.Mounts) > 0 {
			v.Config.Volumes = map[string]json.RawMessage{}
			for _, mount := range spec.Mounts {
				v.Config.Volumes[mount.Target] = json.RawMessage(`{}`)
				v.Mounts = append(v.Mounts, struct {
					Type        string
					Name        string
					Source      string
					Destination string
					RW          bool
				}{Type: "volume", Name: mount.Source, Source: mount.Source, Destination: mount.Target, RW: !mount.ReadOnly})
			}
		}
	}
	return v, nil
}
func (d *DemoEngine) Pull(context.Context, string) error { return nil }
func (d *DemoEngine) Create(_ context.Context, s engine.Spec) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	id := newID("demo_")
	d.specs[id] = s
	container := engine.Container{ID: id, Names: []string{"/" + s.Name}, Image: s.Image, State: "created", Status: "Created", Labels: s.Labels}
	for _, mount := range s.Mounts {
		container.Mounts = append(container.Mounts, engine.ContainerMount{Type: "volume", Name: mount.Source, Destination: mount.Target, RW: !mount.ReadOnly})
	}
	d.containers[id] = container
	if d.networks != nil {
		name := s.Network
		if name == "" {
			name = "bridge"
		}
		for key, network := range d.networks {
			if network.Name == name {
				network.Containers[id] = engine.NetworkEndpoint{Name: s.Name}
				d.networks[key] = network
			}
		}
	}
	return id, nil
}
func (d *DemoEngine) set(id, state string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	ct, ok := d.containers[id]
	if !ok {
		return fmt.Errorf("Docker 404: container not found")
	}
	ct.State = state
	ct.Status = "Up (simulated)"
	if state != "running" {
		ct.Status = "Exited (0)"
	}
	d.containers[id] = ct
	return nil
}
func (d *DemoEngine) Start(_ context.Context, id string) error   { return d.set(id, "running") }
func (d *DemoEngine) Stop(_ context.Context, id string) error    { return d.set(id, "exited") }
func (d *DemoEngine) Restart(_ context.Context, id string) error { return d.set(id, "running") }
func (d *DemoEngine) Remove(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.containers, id)
	delete(d.specs, id)
	for key, network := range d.networks {
		delete(network.Containers, id)
		d.networks[key] = network
	}
	return nil
}
func (d *DemoEngine) Logs(_ context.Context, id string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ct, ok := d.containers[id]
	if !ok {
		return "", fmt.Errorf("container not found")
	}
	return fmt.Sprintf("%s [demo] %s\n%s [demo] Image: %s\n%s [demo] State: %s\nThis log stream is simulated. Connect a real worker for Docker logs.\n", time.Now().UTC().Format(time.RFC3339), strings.TrimPrefix(ct.Names[0], "/"), time.Now().UTC().Format(time.RFC3339), ct.Image, time.Now().UTC().Format(time.RFC3339), ct.State), nil
}

func DemoFactory(store *Store) (Factory, error) {
	if len(store.snapshot().Workers) == 0 && len(store.snapshot().Operations) == 0 {
		if err := store.update(func(s *State) error {
			for i, region := range []string{"us-west-2a", "us-west-2b", "us-east-1a"} {
				id := fmt.Sprintf("wrk_demo%d", i+1)
				s.Workers[id] = Worker{ID: id, Name: fmt.Sprintf("docker-worker-%02d", i+1), Endpoint: fmt.Sprintf("demo://worker-%d", i+1), Region: region, Mode: "active", Status: "online", LastSeen: time.Now().UTC(), Containers: []engine.Container{}}
			}
			for i, name := range []string{"api-gateway", "web-frontend", "background-jobs", "edge-proxy", "webhooks"} {
				id := fmt.Sprintf("dep_demo%d", i+1)
				replicas := 1
				if i < 2 {
					replicas = 2
				}
				tasks := []Replica{}
				for j := 0; j < replicas; j++ {
					tasks = append(tasks, Replica{ID: fmt.Sprintf("demo_%d_%d", i, j), WorkerID: fmt.Sprintf("wrk_demo%d", (i+j)%3+1), Name: fmt.Sprintf("%s-%d", name, j+1)})
				}
				s.Deployments[id] = Deployment{ID: id, Name: name, Image: []string{"nginx:alpine", "nginx:alpine", "alpine:3.21", "nginx:alpine", "nginx:alpine"}[i], Replicas: replicas, Tasks: tasks, Ports: []engine.Port{}, MemoryMB: 512, CPUs: .5, Status: "running", DesiredState: "running", CreatedAt: time.Now().UTC().Add(-time.Duration(i+1) * time.Hour)}
			}
			now := time.Now().UTC().Add(-10 * time.Minute)
			s.Operations = []Operation{{ID: "op_demo", Title: "Demo cluster initialized", Status: "succeeded", Detail: "Simulated Docker hosts. Changes affect demo data only.", StartedAt: now, FinishedAt: &now}}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	engines := map[string]*DemoEngine{}
	var mu sync.Mutex
	factory := func(w Worker) (engine.Engine, error) {
		mu.Lock()
		defer mu.Unlock()
		if engines[w.ID] == nil {
			engines[w.ID] = &DemoEngine{name: w.Name, containers: map[string]engine.Container{}, specs: map[string]engine.Spec{}}
		}
		return engines[w.ID], nil
	}
	s := store.snapshot()
	for _, w := range s.Workers {
		e, _ := factory(w)
		de := e.(*DemoEngine)
		for _, ct := range w.Containers {
			de.containers[ct.ID] = ct
		}
	}
	for _, d := range s.Deployments {
		for _, task := range d.Tasks {
			w, ok := s.Workers[task.WorkerID]
			if !ok {
				continue
			}
			e, _ := factory(w)
			de := e.(*DemoEngine)
			state := "running"
			if d.DesiredState == "stopped" {
				state = "exited"
			}
			de.containers[task.ID] = engine.Container{ID: task.ID, Names: []string{"/" + task.Name}, Image: func() string {
				if task.Image != "" {
					return task.Image
				}
				return d.Image
			}(), State: state, Status: "Up (simulated)", Labels: map[string]string{"zela.deployment.id": d.ID, "zela.managed": "true"}}
		}
	}
	// Seed clearly simulated history so the demo charts are useful immediately.
	if err := store.Monitor.mutate(func() error {
		now := time.Now().UTC().Truncate(time.Minute)
		for _, w := range s.Workers {
			if len(store.Monitor.data.History[w.ID]) > 0 {
				continue
			}
			e, _ := factory(w)
			de := e.(*DemoEngine)
			for i := 60; i >= 0; i-- {
				sample := de.demoMetrics(now.Add(-time.Duration(i) * time.Minute))
				store.Monitor.data.History[w.ID] = append(store.Monitor.data.History[w.ID], HistoryPoint{Time: sample.Time, Host: *sample.Host, Containers: sample.Containers})
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return factory, nil
}
