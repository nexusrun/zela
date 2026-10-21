package control

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zela-dev/zela/internal/clusternet"
	"github.com/zela-dev/zela/internal/engine"
	"github.com/zela-dev/zela/internal/telemetry"
)

type Factory func(Worker) (engine.Engine, error)
type Controller struct {
	operationMu  sync.Mutex // Serialize admission with publishing terminal job status.
	Provisioner  Provisioner
	Store        *Store
	Factory      Factory
	gate         chan struct{}
	wg           sync.WaitGroup
	ctx          context.Context
	cancel       context.CancelFunc
	readyTimeout time.Duration
	// failoverAfter is how long a worker must be unseen before its replicas are replaced.
	failoverAfter time.Duration
	// acme, when set, issues edge certificates automatically.
	acme             *ACMEConfig
	refreshMu        sync.Mutex
	tokenMu          sync.Mutex
	tokenRequests    map[string]map[uint64]context.CancelFunc
	nextTokenRequest uint64
}

func New(store *Store, factory Factory) *Controller {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Controller{Store: store, Factory: factory, gate: make(chan struct{}, 1), ctx: ctx, cancel: cancel, readyTimeout: 90 * time.Second, failoverAfter: 60 * time.Second}
	if factory == nil {
		c.Factory = c.workerEngine
	}
	return c
}
func (c *Controller) Close() { c.cancel(); c.wg.Wait(); _ = c.Store.Close() }
func (c *Controller) StartPolling() {
	if c.acme != nil {
		c.wg.Add(1)
		go func() { defer c.wg.Done(); c.acmeLoop(c.ctx) }()
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.Refresh(c.ctx)
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-c.ctx.Done():
				return
			case <-tick.C:
				c.Refresh(c.ctx)
			}
		}
	}()
}
func (c *Controller) Refresh(ctx context.Context) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	gate := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, w := range c.Store.snapshot().Workers {
		if ctx.Err() != nil {
			break
		}
		select {
		case gate <- struct{}{}:
		case <-ctx.Done():
			break
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(w Worker) { defer wg.Done(); defer func() { <-gate }(); c.refreshWorker(ctx, w) }(w)
	}
	wg.Wait()
	if err := c.Store.update(func(s *State) error {
		for id, d := range s.Deployments {
			if strings.HasPrefix(d.Error, "blocked: source writer is not fenced") && len(c.lostTasks(*s, d, time.Now().UTC())) == 0 {
				d.Error = ""
				d.Status = ""
			}
			if d.Status == "deploying" || d.Status == "moving" || d.Status == "updating" || d.Error != "" {
				continue
			}
			running, stopped, missing := 0, 0, 0
			for _, task := range d.Tasks {
				w := s.Workers[task.WorkerID]
				found := false
				if w.Status == "online" {
					for _, ct := range w.Containers {
						if ct.ID == task.ID {
							found = true
							if ct.State == "running" && !strings.Contains(ct.Status, "unhealthy") {
								running++
							} else {
								stopped++
							}
							break
						}
					}
				}
				if !found {
					missing++
				}
			}
			d.Status = "degraded"
			if d.DesiredState == "running" && running == d.Replicas && len(d.Tasks) == d.Replicas {
				d.Status = "running"
			}
			if d.DesiredState == "stopped" && stopped == len(d.Tasks) && missing == 0 {
				d.Status = "stopped"
			}
			s.Deployments[id] = d
		}
		return nil
	}); err != nil {
		log.Printf("refresh deployments: %v", err)
	}
	c.reconcileNetwork(ctx)
	c.reclaimEvicted(ctx)
	if err := c.Store.Monitor.Record(c.Store.snapshot(), time.Now().UTC()); err != nil {
		log.Printf("monitoring: %v", err)
	}
	c.failover()
}

// lostTasks returns replicas whose worker has been offline longer than failoverAfter.
func (c *Controller) lostTasks(s State, d Deployment, now time.Time) []Replica {
	var lost []Replica
	for _, t := range d.Tasks {
		w, ok := s.Workers[t.WorkerID]
		if !ok || (w.Status == "offline" && !w.LastSeen.IsZero() && now.Sub(w.LastSeen) >= c.failoverAfter) {
			lost = append(lost, t)
		}
	}
	return lost
}

// failover replaces replicas on long-offline workers. It runs as a normal
// operation, so it waits for the operation gate and retries on later refreshes.
func (c *Controller) failover() {
	if c.ctx.Err() != nil {
		return
	}
	s := c.Store.snapshot()
	if s.Demo {
		return
	}
	now := time.Now().UTC()
	for id, d := range s.Deployments {
		if pendingHandover(d) || d.DesiredState != "running" || d.Error != "" || d.Status == "deploying" || d.Status == "moving" || d.Status == "updating" {
			continue
		}
		if len(c.lostTasks(s, d, now)) == 0 {
			continue
		}
		if d.WorkloadPolicy == WorkloadSingleWriter {
			_ = c.editDeployment(id, func(current *Deployment) {
				current.Status = "blocked"
				current.Error = "blocked: source writer is not fenced; automatic recovery was not started"
			})
			continue
		}
		_, err := c.Submit("Recover "+d.Name, func(s *State) error {
			x, ok := s.Deployments[id]
			if !ok || pendingHandover(x) || x.DesiredState != "running" || x.Error != "" || x.Status == "deploying" || x.Status == "moving" || x.Status == "updating" {
				return errFailoverSkipped
			}
			lost := c.lostTasks(*s, x, time.Now().UTC())
			if len(lost) == 0 {
				return errFailoverSkipped
			}
			gone := map[string]bool{}
			for _, t := range lost {
				gone[t.ID] = true
				// Without a reachable worker we cannot delete the container;
				// remember it so a returning worker does not run a duplicate.
				if _, ok := s.Workers[t.WorkerID]; ok {
					x.Evicted = append(x.Evicted, t)
				}
			}
			kept := []Replica{}
			for _, t := range x.Tasks {
				if !gone[t.ID] {
					kept = append(kept, t)
				}
			}
			x.Tasks = kept
			x.Status = "updating"
			s.Deployments[id] = x
			return nil
		}, func(ctx context.Context) error { return c.finishDeployment(id, c.scale(ctx, id)) }, d.AccessTokenID)
		if err != nil {
			// Busy gate or nothing to do; a later refresh retries.
			return
		}
		log.Printf("failover: replacing replicas of %s lost on offline workers", d.Name)
		return
	}
}

var errFailoverSkipped = errors.New("failover no longer needed")

// reclaimEvicted re-adopts evicted replicas while a deployment is short of
// replicas, and otherwise removes them, once their worker is reachable again.
func (c *Controller) reclaimEvicted(ctx context.Context) {
	s := c.Store.snapshot()
	for id, d := range s.Deployments {
		if pendingHandover(d) {
			continue
		}
		for _, t := range d.Evicted {
			w, ok := s.Workers[t.WorkerID]
			if ctx.Err() != nil {
				return
			}
			if ok && w.Status != "online" {
				continue
			}
			present := false
			for _, ct := range w.Containers {
				if ct.ID == t.ID {
					present = true
				}
			}
			adopt := false
			if ok && present {
				if err := c.editDeployment(id, func(x *Deployment) {
					if len(x.StorageAttachments) == 0 && !pendingHandover(*x) && (x.ImageDigest == "" || t.Image == x.ImageDigest) && x.DesiredState == "running" && x.Status != "updating" && x.Status != "deploying" && x.Status != "moving" && len(x.Tasks) < x.Replicas {
						x.Tasks = append(x.Tasks, t)
						adopt = true
					}
					x.Evicted = withoutReplica(x.Evicted, t.ID)
				}); err != nil {
					log.Printf("reclaim %s: %v", t.Name, err)
					continue
				}
				if adopt {
					continue
				}
				if err := c.removeTask(ctx, id, t); err != nil {
					// Keep it listed for the next refresh.
					_ = c.editDeployment(id, func(x *Deployment) { x.Evicted = append(x.Evicted, t) })
					log.Printf("remove evicted replica %s: %v", t.Name, err)
				}
				continue
			}
			// Worker removed, or container already gone.
			if err := c.editDeployment(id, func(x *Deployment) { x.Evicted = withoutReplica(x.Evicted, t.ID) }); err != nil {
				log.Printf("reclaim %s: %v", t.Name, err)
			}
		}
	}
}
func withoutReplica(list []Replica, id string) []Replica {
	out := []Replica{}
	for _, r := range list {
		if r.ID != id {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

const offlineAfterFailures = 3

func (c *Controller) refreshWorker(ctx context.Context, w Worker) {
	if w.Status == "provisioning" || w.Status == "provision-failed" {
		return
	}
	if ctx.Err() != nil {
		return
	}
	// Only reconcile reservations that existed before this inventory probe.
	// A creation completing concurrently must survive an older empty list.
	ownedBefore := c.Store.snapshot().DockerContainers
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	e, err := c.Factory(w)
	var info engine.Info
	var containers []engine.Container
	if err == nil {
		info, err = e.Info(probe)
	}
	if err == nil {
		containers, err = e.List(probe)
	}
	cancel()
	var sample *telemetry.Sample
	metricsError := ""
	if err == nil {
		if provider, ok := e.(engine.MetricsProvider); ok {
			probe, stop := context.WithTimeout(ctx, 5*time.Second)
			v, metricErr := provider.Metrics(probe)
			stop()
			if metricErr == nil {
				metricErr = validateMetrics(v, time.Now())
			}
			if metricErr != nil {
				metricsError = metricErr.Error()
			} else {
				sample = &v
			}
		} else {
			metricsError = "Worker agent does not support metrics; update the agent"
		}
	}
	if saveErr := c.Store.update(func(s *State) error {
		current, ok := s.Workers[w.ID]
		if !ok {
			return nil
		}
		// Provisioning may have restarted while this probe was in flight.
		if current.Status == "provisioning" || current.Status == "provision-failed" {
			return nil
		}
		if err != nil {
			current.FailedProbes++
			current.Error = err.Error()
			// Tolerate isolated slow probes; an online worker goes offline only
			// after consecutive failures.
			if current.Status != "online" || current.FailedProbes >= offlineAfterFailures {
				current.Status = "offline"
			}
		} else {
			current.Status = "online"
			current.FailedProbes = 0
			current.Error = ""
			current.Info = info
			current.Containers = containers
			current.LastSeen = time.Now().UTC()
			present := map[string]bool{}
			for _, ct := range containers {
				present[ct.ID] = true
			}
			for id, ct := range ownedBefore {
				if ct.WorkerID == w.ID && !present[id] {
					delete(s.DockerContainers, id)
					for execID, exec := range s.DockerExecs {
						if exec.ContainerID == id {
							delete(s.DockerExecs, execID)
						}
					}
				}
			}
			current.MetricsError = metricsError
			if sample != nil {
				current.Metrics = sample
			}
		}
		s.Workers[w.ID] = current
		return nil
	}); saveErr != nil {
		log.Printf("refresh: %v", saveErr)
	}
}

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

func validateDeployment(d *Deployment) error {
	if d.WorkloadPolicy == WorkloadSingleWriter && d.Replicas != 1 {
		return fmt.Errorf("single-writer deployments require exactly one replica")
	}
	if !validName.MatchString(d.Name) {
		return fmt.Errorf("name must be 1–63 letters, numbers, dots, underscores or hyphens")
	}
	if d.Image == "" || len(d.Image) > 255 || strings.ContainsAny(d.Image, " \n\r\t") {
		return fmt.Errorf("a valid image reference is required")
	}
	if d.Replicas < 1 || d.Replicas > 50 {
		return fmt.Errorf("replicas must be between 1 and 50")
	}
	if d.MemoryMB < 32 || d.MemoryMB > 1048576 || math.IsNaN(d.CPUs) || math.IsInf(d.CPUs, 0) || d.CPUs < .1 || d.CPUs > 4096 {
		return fmt.Errorf("set memory between 32 and 1048576 MB and CPUs between 0.1 and 4096")
	}
	seen := map[string]bool{}
	for i, p := range d.Ports {
		if p.Protocol == "" {
			p.Protocol = "tcp"
		}
		if p.Host < 0 || p.Host > 65535 || p.Container < 1 || p.Container > 65535 || (p.Protocol != "tcp" && p.Protocol != "udp") {
			return fmt.Errorf("invalid port mapping")
		}
		if p.HostIP != "" {
			ip, err := netip.ParseAddr(p.HostIP)
			if err != nil || ip.Zone() != "" {
				return fmt.Errorf("port hostIP must be an IP address")
			}
			p.HostIP = ip.String()
		}
		key := fmt.Sprintf("%d/%s", p.Container, p.Protocol)
		if seen[key] {
			return fmt.Errorf("duplicate container port")
		}
		seen[key] = true
		d.Ports[i] = p
	}
	if err := validateRoutes(d.Routes); err != nil {
		return err
	}
	if len(d.Env) > 200 || len(d.Command) > 100 {
		return fmt.Errorf("too many environment variables or command arguments")
	}
	for _, v := range d.Env {
		key, _, ok := strings.Cut(v, "=")
		if !ok || key == "" || strings.ContainsAny(v, "\x00\n\r") {
			return fmt.Errorf("environment entries must be KEY=value")
		}
	}
	return nil
}

// acquireOperationGate coordinates admission with terminal status publication.
// During real work it returns immediately with busy; during finalization it
// waits briefly so an observed completion permits the next mutation.
func (c *Controller) acquireOperationGate() bool {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	select {
	case c.gate <- struct{}{}:
		return true
	default:
		return false
	}
}
func (c *Controller) Submit(title string, prepare func(*State) error, run func(context.Context) error, owner ...string) (Operation, error) {
	return c.submit(title, 10*time.Minute, prepare, func(ctx context.Context, _ string) error { return run(ctx) }, owner...)
}
func (c *Controller) submit(title string, timeout time.Duration, prepare func(*State) error, run func(context.Context, string) error, owner ...string) (Operation, error) {
	if !c.acquireOperationGate() {
		return Operation{}, fmt.Errorf("another cluster operation is running; wait for it to finish")
	}
	op := Operation{ID: newID("op_"), Title: title, Status: "running", Detail: "Operation started", StartedAt: time.Now().UTC()}
	if len(owner) > 0 {
		op.AccessTokenID = owner[0]
	}
	err := c.Store.update(func(s *State) error {
		if prepare != nil {
			if err := prepare(s); err != nil {
				return err
			}
		}
		s.Operations = append([]Operation{op}, s.Operations...)
		if len(s.Operations) > 100 {
			s.Operations = s.Operations[:100]
		}
		return nil
	})
	if err != nil {
		<-c.gate
		return Operation{}, err
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ctx, cancel := context.WithTimeout(c.ctx, timeout)
		defer cancel()
		err := run(ctx, op.ID)
		// Inventory is part of completing the job. A client observing a terminal
		// status must be able to submit its next operation without a spurious 409.
		c.Refresh(c.ctx)
		c.operationMu.Lock()
		defer c.operationMu.Unlock()
		defer func() { <-c.gate }()
		if saveErr := c.Store.update(func(s *State) error {
			for i := range s.Operations {
				if s.Operations[i].ID == op.ID {
					now := time.Now().UTC()
					s.Operations[i].FinishedAt = &now
					s.Operations[i].Status = "succeeded"
					s.Operations[i].Detail = "Completed successfully"
					if err != nil {
						s.Operations[i].Status = "failed"
						s.Operations[i].Detail = err.Error()
					}
					break
				}
			}
			return nil
		}); saveErr != nil {
			log.Printf("operation finalization: %v", saveErr)
		}
		if err := c.Store.Monitor.Record(c.Store.snapshot(), time.Now().UTC()); err != nil {
			log.Printf("operation monitoring: %v", err)
		}
	}()
	return op, nil
}
func (c *Controller) worker(ctx context.Context, id string) (engine.Engine, Worker, error) {
	w, ok := c.Store.snapshot().Workers[id]
	if !ok {
		return nil, w, fmt.Errorf("worker not found")
	}
	e, err := c.Factory(w)
	if err != nil {
		return nil, w, err
	}
	// Probe at the point of action; never schedule using a stale online flag.
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	info, err := e.Info(probe)
	if err != nil {
		return nil, w, fmt.Errorf("%s is unreachable: %w", w.Name, err)
	}
	w.Info = info
	return e, w, nil
}
func (c *Controller) deployment(id string) (Deployment, error) {
	d, ok := c.Store.snapshot().Deployments[id]
	if !ok {
		return d, fmt.Errorf("deployment not found")
	}
	return d, nil
}
func (c *Controller) editDeployment(id string, fn func(*Deployment)) error {
	return c.Store.update(func(s *State) error {
		d, ok := s.Deployments[id]
		if !ok {
			return fmt.Errorf("deployment not found")
		}
		fn(&d)
		s.Deployments[id] = d
		return nil
	})
}
func (c *Controller) finishDeployment(id string, err error) error {
	saveErr := c.editDeployment(id, func(d *Deployment) {
		d.Error = ""
		d.Status = d.DesiredState
		if err != nil {
			d.Status = "degraded"
			if pendingHandover(*d) {
				d.Status = "paused"
			}
			d.Error = err.Error()
		}
	})
	return errors.Join(err, saveErr)
}

func eligible(s State, d Deployment, w Worker) bool {
	if w.Mode != "active" || w.Status != "online" || !inventoryFresh(w, time.Now()) {
		return false
	}
	// With the cluster network on, replicas are placed only where it works.
	if networkRequired(s) && !s.Network.Members[w.ID].Ready() {
		return false
	}
	for _, attachment := range d.StorageAttachments {
		volume, ok := s.SharedVolumes[attachment.VolumeID]
		if !ok {
			return false
		}
		backend, ok := s.StorageBackends[volume.BackendID]
		if !ok {
			return false
		}
		approved := false
		for _, workerID := range backend.WorkerIDs {
			if workerID == w.ID {
				approved = true
				break
			}
		}
		if !approved {
			return false
		}
	}
	if len(d.WorkerIDs) == 0 {
		return true
	}
	for _, id := range d.WorkerIDs {
		if id == w.ID {
			return true
		}
	}
	return false
}
func fits(s State, d Deployment, w Worker) error {
	var mem int64
	var cpus float64
	for _, ct := range s.DockerContainers {
		if ct.WorkerID != w.ID {
			continue
		}
		mem += (ct.MemoryBytes + (1 << 20) - 1) / (1 << 20)
		cpus += float64(ct.NanoCPUs) / 1e9
		for _, p := range d.Ports {
			for _, q := range ct.Ports {
				if p.Host != 0 && p.Host == q.Host && p.Protocol == q.Protocol {
					return fmt.Errorf("host port %d/%s already reserved", p.Host, p.Protocol)
				}
			}
		}
	}
	for _, existing := range s.Deployments {
		for _, task := range existing.Tasks {
			if task.WorkerID == w.ID {
				mem += existing.MemoryMB
				cpus += existing.CPUs
				for _, p := range d.Ports {
					for _, q := range existing.Ports {
						if p.Host != 0 && p.Host == q.Host && p.Protocol == q.Protocol {
							return fmt.Errorf("host port %d/%s already reserved", p.Host, p.Protocol)
						}
					}
				}
			}
		}
	}
	for _, ct := range w.Containers {
		for _, p := range d.Ports {
			for _, q := range ct.Ports {
				if p.Host != 0 && p.Host == q.PublicPort && p.Protocol == q.Type {
					return fmt.Errorf("host port %d/%s is in use", p.Host, p.Protocol)
				}
			}
		}
	}
	if mem+d.MemoryMB > w.Info.MemTotal/(1024*1024) || cpus+d.CPUs > float64(w.Info.NCPU) {
		return fmt.Errorf("insufficient unreserved CPU or memory")
	}
	return nil
}
func (c *Controller) choose(ctx context.Context, d Deployment, exclude string) (Worker, error) {
	s := c.Store.snapshot()
	var candidates []Worker
	for _, w := range s.Workers {
		if w.ID != exclude && eligible(s, d, w) && fits(s, d, w) == nil {
			candidates = append(candidates, w)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		count := func(w Worker) int {
			n := len(w.Containers)
			seen := map[string]bool{}
			for _, ct := range w.Containers {
				seen[ct.ID] = true
			}
			for id, ct := range s.DockerContainers {
				if ct.WorkerID == w.ID && !seen[id] {
					n++
				}
			}
			for _, x := range s.Deployments {
				for _, t := range x.Tasks {
					if t.WorkerID == w.ID {
						n++
					}
				}
			}
			return n
		}
		a, b := count(candidates[i]), count(candidates[j])
		if a == b {
			return candidates[i].Name < candidates[j].Name
		}
		return a < b
	})
	for _, w := range candidates {
		e, live, err := c.worker(ctx, w.ID)
		if err == nil {
			probe, stop := context.WithTimeout(ctx, 5*time.Second)
			live.Containers, err = e.List(probe)
			stop()
		}
		latest := c.Store.snapshot()
		current, registered := latest.Workers[w.ID]
		if err == nil && registered && eligible(latest, d, current) && fits(latest, d, live) == nil {
			return live, nil
		}
	}
	return Worker{}, fmt.Errorf("no eligible worker with sufficient resources and available ports; check placement, capacity and worker status")
}

func (c *Controller) ready(ctx context.Context, e engine.Engine, id string, explicit ...bool) error {
	ctx, cancel := context.WithTimeout(ctx, c.readyTimeout)
	defer cancel()
	stable := 0
	for {
		v, err := e.Inspect(ctx, id)
		if err != nil {
			return err
		}
		if !v.State.Running {
			return fmt.Errorf("container stopped during startup: %s %s", v.State.Status, v.State.Error)
		}
		if v.State.Health != nil {
			if v.State.Health.Status == "healthy" {
				return nil
			}
			if v.State.Health.Status == "unhealthy" {
				return fmt.Errorf("container health check failed")
			}
		} else {
			if len(explicit) > 0 && explicit[0] {
				return fmt.Errorf("shared-storage deployments require a Docker health check before traffic can move")
			}
			stable++
			if stable >= 3 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("startup check: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func (c *Controller) provision(ctx context.Context, d Deployment, w Worker) (Replica, error) {
	e, live, err := c.worker(ctx, w.ID)
	if err != nil {
		return Replica{}, err
	}
	if live.Mode != "active" {
		return Replica{}, fmt.Errorf("destination worker is not active")
	}
	if err := fits(c.Store.snapshot(), d, live); err != nil {
		return Replica{}, fmt.Errorf("%s: %w", w.Name, err)
	}
	d, imageToCreate, err := c.prewarmProvision(ctx, d, e, w.ID)
	if err != nil {
		return Replica{}, err
	}
	return c.provisionPrepared(ctx, d, w, e, imageToCreate)
}

func (c *Controller) provisionPrepared(ctx context.Context, d Deployment, w Worker, e engine.Engine, imageToCreate string) (Replica, error) {
	name := d.Name + "-" + newID("")[:8]
	if d.Handover != nil {
		name = "zela-" + d.Handover.ID
	}
	spec := engine.Spec{Name: name, Image: imageToCreate, Env: d.Env, Command: d.Command, Ports: d.Ports, MemoryMB: d.MemoryMB, CPUs: d.CPUs, Labels: map[string]string{"zela.deployment.id": d.ID, "zela.managed": "true", "zela.image.revision": d.ImageDigest}}
	if d.Handover != nil {
		spec.Labels["zela.handover.id"] = d.Handover.ID
	}
	for _, attachment := range d.StorageAttachments {
		spec.Mounts = append(spec.Mounts, engine.Mount{Source: managedVolumeName(attachment.VolumeID), Target: attachment.Target, ReadOnly: attachment.ReadOnly})
	}
	if s := c.Store.snapshot(); networkRequired(s) {
		if !s.Network.Members[w.ID].Ready() {
			return Replica{}, fmt.Errorf("%s has not joined the cluster network", w.Name)
		}
		spec.Network = clusternet.DockerNetwork
		if gw, err := clusternet.Gateway(s.Network.Members[w.ID].Subnet); err == nil {
			spec.DNS, spec.DNSSearch, spec.DNSOptions = []string{gw}, []string{clusternet.Zone}, []string{"ndots:1"}
		}
	}
	var id string
	var err error
	if d.Handover != nil {
		if err = c.handoverStage(d.ID, "creating-replacement"); err != nil {
			return Replica{}, err
		}
		id, err = findHandoverContainer(ctx, e, d.Handover.ID)
		if err != nil {
			return Replica{}, err
		}
	}
	if id == "" {
		id, err = e.Create(ctx, spec)
	}
	if err != nil {
		return Replica{}, err
	}
	if len(d.StorageAttachments) > 0 {
		inspection, inspectErr := e.Inspect(ctx, id)
		if inspectErr == nil {
			inspectErr = validateManagedTaskMounts(d, inspection)
		}
		if inspectErr != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return Replica{}, errors.Join(fmt.Errorf("verify shared mounts before startup: %w", inspectErr), e.Remove(cleanup, id))
		}
	}
	task := Replica{ID: id, WorkerID: w.ID, Name: name, Image: imageToCreate, Lifecycle: "starting"}
	if err := c.editDeployment(d.ID, func(d *Deployment) {
		found := false
		for _, existing := range d.Tasks {
			if existing.ID == task.ID {
				found = true
			}
		}
		if !found {
			d.Tasks = append(d.Tasks, task)
		}
		if d.Handover != nil {
			d.Handover.Replacement = task
			d.Handover.Stage = "starting-replacement"
		}
	}); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return Replica{}, errors.Join(err, e.Remove(cleanup, id))
	}
	if err := c.verifyVolumeAccess(ctx, d, e, w.ID, imageToCreate); err != nil {
		return task, errors.Join(err, c.removeTask(context.Background(), d.ID, task))
	}
	if err := e.Start(ctx, id); err != nil {
		return task, errors.Join(err, c.removeTask(context.Background(), d.ID, task))
	}
	if err := c.ready(ctx, e, id, len(d.StorageAttachments) > 0); err != nil {
		return task, errors.Join(err, c.removeTask(context.Background(), d.ID, task))
	}
	if d.Handover != nil && len(d.Routes) > 0 {
		if err := c.observeReplicaEndpoint(ctx, w, task); err != nil {
			return task, err
		}
	}
	if err := c.editDeployment(d.ID, func(d *Deployment) {
		for i := range d.Tasks {
			if d.Tasks[i].ID == id {
				d.Tasks[i].Lifecycle = "serving"
			}
		}
	}); err != nil {
		return task, errors.Join(err, c.removeTask(context.Background(), d.ID, task))
	}
	task.Lifecycle = "serving"
	return task, nil
}

func (c *Controller) prewarmProvision(ctx context.Context, d Deployment, e engine.Engine, workerID string) (Deployment, string, error) {
	if err := c.prepareVolumes(ctx, e, workerID, d.StorageAttachments); err != nil {
		return d, "", err
	}
	imageToCreate := d.Image
	resolver, resolvesImages := e.(engine.ImageResolver)
	if d.ImageDigest == "" {
		if err := e.Pull(ctx, d.Image); err != nil {
			return d, "", err
		}
		if resolvesImages {
			revision, err := resolver.ResolveImage(ctx, d.Image)
			if err != nil {
				return d, "", fmt.Errorf("resolve immutable image revision: %w", err)
			}
			current, err := c.deployment(d.ID)
			if err != nil {
				return d, "", err
			}
			if current.ImageDigest != "" && current.ImageDigest != revision {
				return d, "", fmt.Errorf("image reference resolved to a different revision during deployment")
			}
			if current.ImageDigest == "" {
				if err := c.editDeployment(d.ID, func(current *Deployment) { current.ImageDigest = revision }); err != nil {
					return d, "", err
				}
			}
			d.ImageDigest, imageToCreate = revision, revision
		}
	} else if strings.Contains(d.ImageDigest, "@sha256:") {
		if err := e.Pull(ctx, d.ImageDigest); err != nil {
			return d, "", err
		}
		imageToCreate = d.ImageDigest
	} else {
		if err := e.Pull(ctx, d.Image); err != nil {
			return d, "", err
		}
		if !resolvesImages {
			return d, "", fmt.Errorf("worker cannot verify the pinned image revision")
		}
		actual, err := resolver.ResolveImage(ctx, d.ImageDigest)
		if err != nil {
			return d, "", err
		}
		if actual != d.ImageDigest {
			return d, "", fmt.Errorf("image tag now points to a different revision; refusing to move the deployment")
		}
		imageToCreate = d.ImageDigest
	}
	if err := c.verifyVolumeAccess(ctx, d, e, workerID, imageToCreate); err != nil {
		return d, "", err
	}
	return d, imageToCreate, nil
}
func (c *Controller) forgetTask(id, taskID string) error {
	return c.editDeployment(id, func(d *Deployment) {
		tasks := []Replica{}
		for _, t := range d.Tasks {
			if t.ID != taskID {
				tasks = append(tasks, t)
			}
		}
		d.Tasks = tasks
	})
}
func (c *Controller) removeTask(ctx context.Context, id string, task Replica) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	e, _, err := c.worker(ctx, task.WorkerID)
	if err != nil {
		return err
	}
	if err := e.Remove(ctx, task.ID); err != nil {
		if !strings.Contains(err.Error(), "404") {
			return err
		}
	}
	return c.forgetTask(id, task.ID)
}

func (c *Controller) CreateDeployment(d Deployment) (Operation, error) {
	if err := validateDeployment(&d); err != nil {
		return Operation{}, err
	}
	d.ID = newID("dep_")
	d.Tasks = []Replica{}
	d.CreatedAt = time.Now().UTC()
	d.DesiredState = "running"
	d.Status = "deploying"
	return c.Submit("Deploy "+d.Name, func(s *State) error {
		if err := validateAttachments(&d, *s); err != nil {
			return err
		}
		for _, x := range s.Deployments {
			if x.Name == d.Name {
				return fmt.Errorf("deployment name already exists")
			}
		}
		if err := routeConflict(*s, d.ID, d.Routes); err != nil {
			return err
		}
		for _, id := range d.WorkerIDs {
			w, ok := s.Workers[id]
			if !ok || w.Mode != "active" {
				return fmt.Errorf("placement includes an unavailable worker")
			}
		}
		s.Deployments[d.ID] = d
		return nil
	}, func(ctx context.Context) error { return c.finishDeployment(d.ID, c.scale(ctx, d.ID)) }, d.AccessTokenID)
}
func (c *Controller) scale(ctx context.Context, id string) error {
	d, err := c.deployment(id)
	if err != nil {
		return err
	}
	for len(d.Tasks) < d.Replicas {
		w, err := c.choose(ctx, d, "")
		if err != nil {
			return err
		}
		if _, err := c.provision(ctx, d, w); err != nil {
			return err
		}
		d, _ = c.deployment(id)
	}
	for len(d.Tasks) > d.Replicas {
		if err := c.removeTask(ctx, id, d.Tasks[len(d.Tasks)-1]); err != nil {
			return err
		}
		d, _ = c.deployment(id)
	}
	return nil
}

func (c *Controller) moveTask(ctx context.Context, d Deployment, task Replica, target string) error {
	if task.WorkerID == target && !pendingHandover(d) {
		return nil
	}
	return c.replaceTask(ctx, d, task, target)
}

func (c *Controller) setReplicaLifecycle(deploymentID, replicaID, lifecycle string) error {
	return c.editDeployment(deploymentID, func(d *Deployment) {
		for i := range d.Tasks {
			if d.Tasks[i].ID == replicaID {
				d.Tasks[i].Lifecycle = lifecycle
			}
		}
	})
}

func (c *Controller) DeploymentAction(id, action, target string, replicas int, owner ...string) (Operation, error) {
	d, err := c.deployment(id)
	if err != nil {
		return Operation{}, err
	}
	if pendingHandover(d) {
		return Operation{}, fmt.Errorf("resume the pending replica move before changing this deployment")
	}
	switch action {
	case "start", "stop", "restart", "delete", "move", "scale":
	default:
		return Operation{}, fmt.Errorf("unknown deployment action")
	}
	if action == "scale" && d.WorkloadPolicy == WorkloadSingleWriter && replicas != 1 {
		return Operation{}, fmt.Errorf("single-writer deployments require exactly one replica")
	}
	if action == "scale" && (replicas < 1 || replicas > 50) {
		return Operation{}, fmt.Errorf("replicas must be between 1 and 50")
	}
	if (action == "move" || action == "scale") && d.DesiredState != "running" {
		return Operation{}, fmt.Errorf("start the deployment before moving or scaling")
	}
	if action == "move" {
		w, ok := c.Store.snapshot().Workers[target]
		if !ok || w.Mode != "active" || w.Status != "online" {
			return Operation{}, fmt.Errorf("destination must be an active online worker")
		}
	}
	return c.Submit(strings.Title(action)+" "+d.Name, func(s *State) error {
		x, ok := s.Deployments[id]
		if !ok {
			return fmt.Errorf("deployment not found")
		}
		x.Status = "updating"
		if action == "move" {
			x.Status = "moving"
		}
		x.Error = ""
		if action == "scale" {
			x.Replicas = replicas
		}
		if action == "stop" {
			x.DesiredState = "stopped"
		}
		if action == "start" || action == "restart" {
			x.DesiredState = "running"
		}
		s.Deployments[id] = x
		return nil
	}, func(ctx context.Context) error {
		d, _ = c.deployment(id)
		var actionErr error
		switch action {
		case "scale":
			actionErr = c.scale(ctx, id)
		case "move":
			for _, t := range d.Tasks {
				if err := c.moveTask(ctx, d, t, target); err != nil {
					actionErr = err
					break
				}
			}
			if actionErr == nil {
				actionErr = c.editDeployment(id, func(d *Deployment) { d.WorkerIDs = []string{target} })
			}
		case "delete":
			for _, t := range d.Tasks {
				if err := c.removeTask(ctx, id, t); err != nil {
					actionErr = err
					break
				}
			}
			if actionErr == nil {
				return c.Store.update(func(s *State) error { delete(s.Deployments, id); return nil })
			}
		default:
			for _, t := range d.Tasks {
				e, _, err := c.worker(ctx, t.WorkerID)
				if err == nil && action != "stop" && len(d.StorageAttachments) > 0 {
					var inspection engine.Inspection
					inspection, err = e.Inspect(ctx, t.ID)
					if err == nil {
						err = validateManagedTaskMounts(d, inspection)
					}
					if err == nil {
						err = c.prepareVolumes(ctx, e, t.WorkerID, d.StorageAttachments)
					}
					image := inspection.Image
					if image == "" {
						image = t.Image
					}
					if err == nil {
						err = c.verifyVolumeAccess(ctx, d, e, t.WorkerID, image)
					}
				}
				if err == nil && action != "stop" {
					err = c.setReplicaLifecycle(d.ID, t.ID, "starting")
				}
				if err == nil {
					switch action {
					case "stop":
						err = e.Stop(ctx, t.ID)
					case "start":
						err = e.Start(ctx, t.ID)
					case "restart":
						err = e.Restart(ctx, t.ID)
					}
					if err == nil && action == "stop" {
						err = c.setReplicaLifecycle(d.ID, t.ID, "stopped")
					}
					if err == nil && action != "stop" {
						err = c.ready(ctx, e, t.ID, len(d.StorageAttachments) > 0)
					}
					if err == nil && action != "stop" && len(d.Routes) > 0 {
						w := c.Store.snapshot().Workers[t.WorkerID]
						err = c.observeReplicaEndpoint(ctx, w, t)
					}
					if err == nil && action != "stop" {
						err = c.setReplicaLifecycle(d.ID, t.ID, "serving")
					}
				}
				if err != nil {
					actionErr = err
					break
				}
			}
			if actionErr == nil && action == "start" {
				actionErr = c.scale(ctx, id)
			}
		}
		return c.finishDeployment(id, actionErr)
	}, owner...)
}

func (c *Controller) AddWorker(w Worker) (Operation, error) {
	if c.Store.database != nil && c.Store.database.ha {
		if err := validateHAWorker(w); err != nil {
			return Operation{}, err
		}
	}
	w.Name = strings.TrimSpace(w.Name)
	w.Endpoint = strings.TrimRight(strings.TrimSpace(w.Endpoint), "/")
	if !validName.MatchString(w.Name) {
		return Operation{}, fmt.Errorf("invalid worker name")
	}
	if len(w.Region) > 80 {
		return Operation{}, fmt.Errorf("region is too long")
	}
	if !c.Store.snapshot().Demo {
		if _, err := engine.New(w.Endpoint, w.Token); err != nil {
			return Operation{}, err
		}
	}
	w.ID = newID("wrk_")
	w.Mode = "active"
	w.Status = "connecting"
	w.Containers = []engine.Container{}
	return c.Submit("Connect "+w.Name, func(s *State) error {
		for _, x := range s.Workers {
			if x.Name == w.Name || x.Endpoint == w.Endpoint {
				return fmt.Errorf("worker name or endpoint already exists")
			}
		}
		s.Workers[w.ID] = w
		return nil
	}, func(ctx context.Context) error {
		_, live, err := c.worker(ctx, w.ID)
		if err != nil {
			return errors.Join(err, c.Store.update(func(s *State) error {
				x, ok := s.Workers[w.ID]
				if !ok {
					return nil // Removed while connecting.
				}
				x.Status = "offline"
				x.Error = err.Error()
				s.Workers[w.ID] = x
				return nil
			}))
		}
		return c.Store.update(func(s *State) error {
			x, ok := s.Workers[w.ID]
			if !ok {
				return nil // Removed while connecting.
			}
			x.Info = live.Info
			x.Status = "online"
			x.FailedProbes = 0
			x.LastSeen = time.Now().UTC()
			s.Workers[w.ID] = x
			return nil
		})
	})
}

func (c *Controller) WorkerAction(id, action string) (Operation, error) {
	w, ok := c.Store.snapshot().Workers[id]
	if !ok {
		return Operation{}, fmt.Errorf("worker not found")
	}
	if action != "drain" && action != "activate" && action != "pause" && action != "delete" {
		return Operation{}, fmt.Errorf("unknown worker action")
	}
	return c.Submit(strings.Title(action)+" "+w.Name, func(s *State) error {
		x, ok := s.Workers[id]
		if !ok {
			return fmt.Errorf("worker not found")
		}
		if action == "drain" {
			x.Mode = "draining"
		}
		s.Workers[id] = x
		return nil
	}, func(ctx context.Context) error {
		if action == "delete" && w.Status == "online" {
			e, _, err := c.worker(ctx, id)
			if err != nil {
				return err
			}
			containers, err := e.List(ctx)
			if err != nil {
				return err
			}
			if len(containers) != 0 {
				return fmt.Errorf("worker still has containers; remove them before unregistering")
			}
		}
		if action == "drain" {
			state := c.Store.snapshot()
			for depID, d := range state.Deployments {
				for _, t := range d.Tasks {
					if t.WorkerID != id {
						continue
					}
					if d.DesiredState != "running" {
						return fmt.Errorf("start or delete stopped deployment %s before draining", d.Name)
					}
					// Draining overrides a source-only placement restriction.
					candidate := d
					candidate.WorkerIDs = nil
					target, err := c.choose(ctx, candidate, id)
					if err != nil {
						return err
					}
					if err := c.moveTask(ctx, d, t, target.ID); err != nil {
						return c.finishDeployment(depID, err)
					}
					if err := c.editDeployment(depID, func(d *Deployment) { d.WorkerIDs = nil; d.Error = "" }); err != nil {
						return err
					}
				}
			}
			e, _, err := c.worker(ctx, id)
			if err != nil {
				return err
			}
			list, err := e.List(ctx)
			if err != nil {
				return err
			}
			if len(list) > 0 {
				return fmt.Errorf("managed workloads moved; %d unmanaged or leftover containers remain. Handle them before completing drain", len(list))
			}
		}
		return c.Store.update(func(s *State) error {
			x := s.Workers[id]
			switch action {
			case "activate":
				x.Mode = "active"
			case "pause":
				x.Mode = "paused"
			case "drain":
				x.Mode = "drained"
			case "delete":
				for _, ct := range s.DockerContainers {
					if ct.WorkerID == id {
						return fmt.Errorf("worker still owns Docker API containers; remove them before unregistering")
					}
				}
				for _, d := range s.Deployments {
					for _, t := range d.Tasks {
						if t.WorkerID == id {
							return fmt.Errorf("worker still has managed replicas; move or delete them first")
						}
					}
				}
				if len(x.Containers) > 0 {
					return fmt.Errorf("worker still has containers; remove them before unregistering")
				}
				delete(s.Workers, id)
				return nil
			}
			s.Workers[id] = x
			return nil
		})
	})
}

func (c *Controller) ContainerAction(workerID, id, action string) (Operation, error) {
	if action != "start" && action != "stop" && action != "restart" && action != "delete" {
		return Operation{}, fmt.Errorf("unknown container action")
	}
	s := c.Store.snapshot()
	w, ok := s.Workers[workerID]
	if !ok {
		return Operation{}, fmt.Errorf("worker not found")
	}
	found := false
	for _, ct := range w.Containers {
		if ct.ID == id {
			found = true
		}
	}
	if !found {
		return Operation{}, fmt.Errorf("container not found in worker inventory")
	}
	for _, d := range s.Deployments {
		for _, t := range d.Tasks {
			if t.ID == id && t.WorkerID == workerID {
				return Operation{}, fmt.Errorf("use deployment actions for managed containers")
			}
		}
	}
	return c.Submit(strings.Title(action)+" container on "+w.Name, nil, func(ctx context.Context) error {
		e, _, err := c.worker(ctx, workerID)
		if err != nil {
			return err
		}
		switch action {
		case "start":
			return e.Start(ctx, id)
		case "stop":
			return e.Stop(ctx, id)
		case "restart":
			return e.Restart(ctx, id)
		default:
			if err := e.Remove(ctx, id); err != nil {
				return err
			}
			return c.Store.update(func(s *State) error {
				delete(s.DockerContainers, id)
				for execID, exec := range s.DockerExecs {
					if exec.ContainerID == id {
						delete(s.DockerExecs, execID)
					}
				}
				return nil
			})
		}
	})
}

// MoveReplica changes one replica's assignment without consolidating the application.
func (c *Controller) MoveReplica(id, taskID, target string, owner ...string) (Operation, error) {
	d, err := c.deployment(id)
	if err != nil {
		return Operation{}, err
	}
	if d.DesiredState != "running" {
		return Operation{}, fmt.Errorf("start the deployment before moving a replica")
	}
	var task Replica
	for _, t := range d.Tasks {
		if t.ID == taskID {
			task = t
			break
		}
	}
	if task.ID == "" && pendingHandover(d) && d.Handover.Source.ID == taskID {
		task = d.Handover.Source
	}
	if task.ID == "" {
		return Operation{}, fmt.Errorf("replica not found")
	}
	w, ok := c.Store.snapshot().Workers[target]
	if !ok || w.Status != "online" || w.Mode != "active" {
		return Operation{}, fmt.Errorf("destination must be an active online worker")
	}
	return c.Submit("Move replica of "+d.Name, func(s *State) error {
		x := s.Deployments[id]
		x.Status = "moving"
		x.Error = ""
		s.Deployments[id] = x
		return nil
	}, func(ctx context.Context) error {
		err := c.moveTask(ctx, d, task, target)
		if err == nil {
			err = c.editDeployment(id, func(d *Deployment) { d.WorkerIDs = nil })
		}
		return c.finishDeployment(id, err)
	}, owner...)
}

// UpdateImage replaces replicas one at a time, keeping each old container until its replacement is ready.
func (c *Controller) UpdateImage(id, image, target string, owner ...string) (Operation, error) {
	d, err := c.deployment(id)
	if err != nil {
		return Operation{}, err
	}
	if d.DesiredState != "running" {
		return Operation{}, fmt.Errorf("start the deployment before updating its image")
	}
	if pendingHandover(d) {
		return Operation{}, fmt.Errorf("resume the pending replica move before updating the image")
	}
	d.Image = image
	d.ImageDigest = ""
	if err := validateDeployment(&d); err != nil {
		return Operation{}, err
	}
	w, ok := c.Store.snapshot().Workers[target]
	if !ok || w.Mode != "active" || w.Status != "online" {
		return Operation{}, fmt.Errorf("choose an active online rollout destination")
	}
	return c.Submit("Update image of "+d.Name, func(s *State) error {
		x := s.Deployments[id]
		x.Image = image
		x.ImageDigest = ""
		x.Status = "updating"
		x.Error = ""
		s.Deployments[id] = x
		return nil
	}, func(ctx context.Context) error {
		for _, task := range d.Tasks {
			current, loadErr := c.deployment(id)
			if loadErr != nil {
				return c.finishDeployment(id, loadErr)
			}
			if task.Image == current.ImageDigest && current.ImageDigest != "" && task.WorkerID == target || task.Image == image && current.ImageDigest == "" && task.WorkerID == target {
				continue
			}
			if err := c.replaceTask(ctx, current, task, target); err != nil {
				return c.finishDeployment(id, err)
			}
		}
		err := c.editDeployment(id, func(d *Deployment) { d.WorkerIDs = []string{target} })
		return c.finishDeployment(id, err)
	}, owner...)
}
