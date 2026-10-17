package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zela-dev/zela/internal/engine"
)

type dockerRouteError struct {
	status  int
	message string
}

func (e *dockerRouteError) Error() string         { return e.message }
func routeError(status int, message string) error { return &dockerRouteError{status, message} }
func clusterFailure(w http.ResponseWriter, err error) {
	if e, ok := err.(*dockerRouteError); ok {
		dockerError(w, e.status, e.message)
		return
	}
	dockerError(w, 502, "cluster worker request failed")
}

func dockerJSONResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func clusterWorkers(s State, active bool) []Worker {
	var ws []Worker
	for _, w := range s.Workers {
		if w.Status == "online" && (!active || w.Mode == "active") {
			ws = append(ws, w)
		}
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].ID < ws[j].ID })
	return ws
}

// Scoped selectors narrow discovery without weakening workload ownership checks.
func selectedClusterWorkers(s State, r *http.Request, active bool) []Worker {
	workers := clusterWorkers(s, active)
	selected := r.Header.Get("X-Zela-Worker")
	if selected == "" {
		return workers
	}
	for _, worker := range workers {
		if worker.ID == selected {
			return []Worker{worker}
		}
	}
	return nil
}
func (c *Controller) dockerNameAvailable(ctx context.Context, name, ignore, selected string) error {
	if selected == "" {
		_, id, err := c.dockerFindContainer(ctx, name, true)
		if err == nil {
			if id != ignore {
				return routeError(409, "container name already exists in the cluster")
			}
			return nil
		}
		if e, ok := err.(*dockerRouteError); ok && e.status == 404 {
			return nil
		}
		return err
	}
	// Explicit placement never depends on probes to unrelated workers. Check
	// persisted/cached names; Docker enforces live names on the selected host.
	s := c.Store.snapshot()
	conflict := func(id, existing string) bool { return id != ignore && strings.TrimPrefix(existing, "/") == name }
	for id, ct := range s.DockerContainers {
		if conflict(id, ct.Name) {
			return routeError(409, "container name already exists in the cluster")
		}
	}
	for _, d := range s.Deployments {
		for _, task := range d.Tasks {
			if conflict(task.ID, task.Name) {
				return routeError(409, "container name already exists in the cluster")
			}
		}
	}
	for _, worker := range s.Workers {
		for _, ct := range worker.Containers {
			for _, existing := range ct.Names {
				if conflict(ct.ID, existing) {
					return routeError(409, "container name already exists in the cluster")
				}
			}
		}
	}
	return nil
}

func (c *Controller) dockerOwnedContainer(p principal, ref string) (Worker, string, error) {
	s := c.Store.snapshot()
	if ct, ok := s.DockerContainers[ref]; ok {
		if ct.AccessTokenID != p.TokenID {
			return Worker{}, "", routeError(403, "token cannot access this container")
		}
		worker, err := ownerWorker(s, ct.WorkerID)
		return worker, ref, err
	}
	for _, d := range s.Deployments {
		for _, task := range d.Tasks {
			if task.ID == ref {
				if d.AccessTokenID != p.TokenID {
					return Worker{}, "", routeError(403, "token cannot access this container")
				}
				worker, err := ownerWorker(s, task.WorkerID)
				return worker, ref, err
			}
		}
	}
	matches := map[string]string{}
	for id, ct := range s.DockerContainers {
		if ct.AccessTokenID == p.TokenID && containerMatches(id, ct.Name, ref) {
			matches[id] = ct.WorkerID
		}
	}
	for _, d := range s.Deployments {
		if d.AccessTokenID == p.TokenID {
			for _, task := range d.Tasks {
				if containerMatches(task.ID, task.Name, ref) {
					matches[task.ID] = task.WorkerID
				}
			}
		}
	}
	if len(matches) > 1 {
		return Worker{}, "", routeError(409, "container reference is ambiguous; use its full ID")
	}
	for id, workerID := range matches {
		worker, err := ownerWorker(s, workerID)
		return worker, id, err
	}
	return Worker{}, "", routeError(403, "token cannot access this container")
}

// Internal discovery uses the exact same transport and credential boundary as
// forwarding, including SSH tunnels and the worker's /docker namespace.
func (c *Controller) dockerRequest(ctx context.Context, worker Worker, method, path, query string, headers http.Header, body io.Reader) (*http.Response, error) {
	e, err := c.Factory(worker)
	if err != nil {
		return nil, routeError(502, "worker transport unavailable")
	}
	client, ok := e.(*engine.Client)
	if !ok || client.HTTP == nil {
		return nil, routeError(501, "worker does not support Docker API forwarding")
	}
	u, err := url.Parse(client.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, routeError(502, "invalid worker transport")
	}
	u.Path = path
	if !strings.HasPrefix(worker.Endpoint, "unix://") {
		u.Path = "/docker" + path
	}
	u.RawQuery = query
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, routeError(400, "invalid Docker request")
	}
	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	for _, h := range []string{"Authorization", "Cookie", "X-Zela-Worker", "Proxy-Authorization"} {
		req.Header.Del(h)
	}
	if client.Token != "" {
		req.Header.Set("Authorization", "Bearer "+client.Token)
	}
	tr := client.HTTP.Transport
	if tr == nil {
		tr = dockerGatewayTransport
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		if transport, ok := tr.(interface{ CloseIdleConnections() }); ok && tr != dockerGatewayTransport {
			transport.CloseIdleConnections()
		}
		return nil, routeError(502, "Docker worker unavailable")
	}
	if transport, ok := tr.(interface{ CloseIdleConnections() }); ok && tr != dockerGatewayTransport {
		resp.Body = &dockerResponseBody{ReadCloser: resp.Body, closeIdle: transport.CloseIdleConnections}
	}
	return resp, nil
}

type dockerResponseBody struct {
	io.ReadCloser
	closeIdle func()
}

func (b *dockerResponseBody) Close() error { err := b.ReadCloser.Close(); b.closeIdle(); return err }

func (c *Controller) dockerJSON(ctx context.Context, worker Worker, path, query string, out any) error {
	resp, err := c.dockerRequest(ctx, worker, "GET", path, query, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return routeError(resp.StatusCode, "worker could not serve cluster discovery")
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out); err != nil {
		return routeError(502, "invalid worker discovery response")
	}
	return nil
}

func (c *Controller) dockerCluster(w http.ResponseWriter, r *http.Request, path, ep string) {
	if c.Store.snapshot().Demo {
		dockerError(w, 501, "Docker API requires live workers")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(c.ctx, cancel)
	defer cancel()
	defer stop()
	r = r.Clone(ctx)
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	switch {
	case ep == "/containers/create" && r.Method == "POST":
		c.dockerAutoCreate(w, r, path)
		return
	case ep == "/containers/json" && r.Method == "GET":
		c.dockerClusterList(w, r, path, false)
		return
	case ep == "/images/json" && r.Method == "GET":
		c.dockerClusterList(w, r, path, true)
		return
	case ep == "/images/create" && r.Method == "POST":
		c.dockerClusterPull(w, r, path)
		return
	case (ep == "/version" || ep == "/_ping" || ep == "/info") && (r.Method == "GET" || r.Method == "HEAD"):
		c.dockerClusterInfo(w, r, path, ep)
		return
	case strings.HasPrefix(ep, "/containers/"):
		ref, action, _ := strings.Cut(strings.TrimPrefix(ep, "/containers/"), "/")
		var worker Worker
		var id string
		var err error
		if requestPrincipal(r).Admin {
			worker, id, err = c.dockerContainerOwner(ctx, ref)
		} else {
			worker, id, err = c.dockerOwnedContainer(requestPrincipal(r), ref)
		}
		if err != nil {
			clusterFailure(w, err)
			return
		}
		if selected := r.Header.Get("X-Zela-Worker"); selected != "" && selected != worker.ID {
			dockerError(w, 409, "selected worker does not own this container")
			return
		}
		if !ownsContainer(c.Store.snapshot(), requestPrincipal(r), id) {
			dockerError(w, 403, "deployment token cannot access this container")
			return
		}
		if action == "update" {
			dockerError(w, 501, "resource-limit updates require explicit worker affinity; automatic placement reservations are fixed at creation")
			return
		}
		if action == "rename" && r.Method == "POST" {
			if !c.acquireOperationGate() {
				dockerError(w, 409, "another cluster placement is running; retry rename after it finishes")
				return
			}
			defer func() { <-c.gate }()
			name := strings.TrimPrefix(r.URL.Query().Get("name"), "/")
			if !dockerContainerName.MatchString(name) || len(name) > 255 {
				dockerError(w, 400, "invalid container name")
				return
			}
			if err := c.dockerNameAvailable(ctx, name, id, r.Header.Get("X-Zela-Worker")); err != nil {
				clusterFailure(w, err)
				return
			}
		}
		// Resolve aliases once, then address the exact ID on the owning host.
		path = strings.Replace(path, "/containers/"+ref, "/containers/"+id, 1)
		rawPath := r.URL.RawPath
		if rawPath != "" {
			before, rest, _ := strings.Cut(rawPath, "/containers/")
			_, suffix, slash := strings.Cut(rest, "/")
			rawPath = before + "/containers/" + id
			if slash {
				rawPath += "/" + suffix
			}
		}
		c.dockerForward(w, r, worker.ID, path, rawPath, c.dockerObserve(worker.ID, id, action, r, nil))
		return
	case strings.HasPrefix(ep, "/exec/"):
		ref, _, _ := strings.Cut(strings.TrimPrefix(ep, "/exec/"), "/")
		worker, err := c.dockerExecOwner(ctx, ref)
		if err != nil {
			clusterFailure(w, err)
			return
		}
		if selected := r.Header.Get("X-Zela-Worker"); selected != "" && selected != worker.ID {
			dockerError(w, 409, "selected worker does not own this exec session")
			return
		}
		c.dockerForward(w, r, worker.ID, path, r.URL.RawPath, nil)
		return
	case strings.HasPrefix(ep, "/images/") && r.Method == "GET":
		// Image inspection before docker run may consult any copy. Container
		// creation ensures that the image exists on the scheduled worker.
		for _, worker := range selectedClusterWorkers(c.Store.snapshot(), r, false) {
			probe, stop := context.WithTimeout(ctx, 10*time.Second)
			resp, err := c.dockerRequest(probe, worker, "GET", path, r.URL.RawQuery, r.Header, nil)
			if err == nil {
				resp.Body.Close()
			}
			stop()
			if err == nil && resp.StatusCode == 200 {
				c.dockerForward(w, r, worker.ID, path, r.URL.RawPath, nil)
				return
			}
			if err != nil {
				clusterFailure(w, err)
				return
			}
		}
		dockerError(w, 404, "image not found in cluster")
		return
	default:
		dockerError(w, 501, "this worker-scoped Docker API requires an explicit worker selector; cluster mode supports container lifecycle, exec, image pull/list/inspect, ping, version and info")
	}
}

func ownerWorker(s State, id string) (Worker, error) {
	w, ok := s.Workers[id]
	if !ok {
		return Worker{}, routeError(503, "container owner is no longer registered")
	}
	if w.Status != "online" {
		return Worker{}, routeError(503, "container owner is offline; the container cannot be routed to another worker")
	}
	return w, nil
}

func containerMatches(id, name, ref string) bool {
	return id == ref || (len(ref) >= 12 && strings.HasPrefix(id, ref)) || strings.TrimPrefix(name, "/") == ref
}

func (c *Controller) dockerContainerOwner(ctx context.Context, ref string) (Worker, string, error) {
	return c.dockerFindContainer(ctx, ref, false)
}

func (c *Controller) dockerFindContainer(ctx context.Context, ref string, creating bool) (Worker, string, error) {
	s := c.Store.snapshot()
	matches := map[string]string{}
	for id, ct := range s.DockerContainers {
		if containerMatches(id, ct.Name, ref) {
			matches[id] = ct.WorkerID
		}
	}
	// Query all workers for aliases/short IDs and unindexed containers. An exact
	// persisted ID has authoritative ownership, even before the next poll.
	if ct, ok := s.DockerContainers[ref]; ok {
		w, err := ownerWorker(s, ct.WorkerID)
		return w, ref, err
	}
	probe, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	for _, w := range clusterWorkers(s, false) {
		var containers []engine.Container
		if err := c.dockerJSON(probe, w, "/containers/json", "all=1", &containers); err != nil {
			return Worker{}, "", err
		}
		for _, ct := range containers {
			matched := containerMatches(ct.ID, "", ref)
			if indexed, ok := s.DockerContainers[ct.ID]; ok {
				matched = matched || containerMatches(ct.ID, indexed.Name, ref)
			} else {
				for _, name := range ct.Names {
					matched = matched || containerMatches(ct.ID, name, ref)
				}
			}
			if matched {
				key := ct.ID
				if prev, ok := matches[key]; ok && prev != w.ID {
					return Worker{}, "", routeError(409, "container reference is ambiguous; use the full container ID or an explicit worker selector")
				}
				matches[key] = w.ID
			}
		}
	}
	if len(matches) > 1 {
		return Worker{}, "", routeError(409, "container reference is ambiguous; use the full container ID or an explicit worker selector")
	}
	for id, owner := range matches {
		w, err := ownerWorker(s, owner)
		return w, id, err
	}
	for _, w := range s.Workers {
		if w.Status != "online" && !creating {
			return Worker{}, "", routeError(503, "cannot resolve an unknown container while a worker is offline")
		}
	}
	return Worker{}, "", routeError(404, "No such container: "+ref)
}

func (c *Controller) dockerExecOwner(ctx context.Context, ref string) (Worker, error) {
	s := c.Store.snapshot()
	if exec, ok := s.DockerExecs[ref]; ok {
		return ownerWorker(s, exec.WorkerID)
	}
	var owners []Worker
	probe, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	for _, worker := range clusterWorkers(s, false) {
		resp, err := c.dockerRequest(probe, worker, "GET", "/exec/"+url.PathEscape(ref)+"/json", "", nil, nil)
		if err != nil {
			return Worker{}, err
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			owners = append(owners, worker)
		} else if resp.StatusCode != 404 {
			return Worker{}, routeError(502, "cannot resolve exec session")
		}
	}
	if len(owners) > 1 {
		return Worker{}, routeError(409, "exec ID is ambiguous")
	}
	if len(owners) == 1 {
		return owners[0], nil
	}
	return Worker{}, routeError(404, "No such exec session")
}

func (c *Controller) dockerObserve(worker, id, action string, request *http.Request, created *DockerContainer) func(*http.Response) error {
	return func(resp *http.Response) error {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil
		}
		notificationName := c.Store.snapshot().DockerContainers[id].Name
		notify := func(containerID, event string) {
			if err := c.Store.Monitor.ContainerNotification(worker, containerID, notificationName, event, time.Now().UTC()); err != nil {
				log.Printf("container notification persistence: %v", err)
			}
		}

		var result struct {
			ID string `json:"Id"`
		}
		if created != nil || (action == "exec" && request.Method == "POST") {
			b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if err != nil || json.Unmarshal(b, &result) != nil || result.ID == "" {
				return fmt.Errorf("invalid Docker creation response")
			}
			resp.Body = io.NopCloser(bytes.NewReader(b))
			resp.ContentLength = int64(len(b))
			resp.Header.Set("Content-Length", strconv.Itoa(len(b)))
		}
		if created == nil && action != "exec" && action != "rename" && !(action == "" && request.Method == "DELETE") {
			if request.Method == "POST" {
				notify(id, action)
			}
			return nil
		}
		err := c.Store.update(func(s *State) error {
			if s.DockerContainers == nil {
				s.DockerContainers = map[string]DockerContainer{}
			}
			if s.DockerExecs == nil {
				s.DockerExecs = map[string]DockerExec{}
			}
			switch {
			case created != nil:
				ct := *created
				ct.ID = result.ID
				ct.WorkerID = worker
				s.DockerContainers[ct.ID] = ct
			case action == "exec":
				s.DockerExecs[result.ID] = DockerExec{AccessTokenID: requestPrincipal(request).TokenID, WorkerID: worker, ContainerID: id}
			case action == "rename":
				ct, ok := s.DockerContainers[id]
				if ok {
					ct.Name = request.URL.Query().Get("name")
					s.DockerContainers[id] = ct
				}
			case action == "" && request.Method == "DELETE":
				delete(s.DockerContainers, id)
				for execID, exec := range s.DockerExecs {
					if exec.ContainerID == id {
						delete(s.DockerExecs, execID)
					}
				}
			}
			return nil
		})
		if err != nil && created != nil {
			// Never acknowledge an unrecorded placement. Remove only the exact
			// newly created ID; discovery can recover it if cleanup also fails.
			cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			if w, ok := c.Store.snapshot().Workers[worker]; ok {
				if reply, e := c.dockerRequest(cleanup, w, "DELETE", "/containers/"+result.ID, "force=true", nil, nil); e == nil {
					reply.Body.Close()
				}
			}
		}
		if err == nil {
			switch {
			case created != nil:
				notificationName = created.Name
				notify(result.ID, "created")
			case action == "rename":
				notificationName = request.URL.Query().Get("name")
				notify(id, "rename")
			case action == "" && request.Method == "DELETE":
				notify(id, "deleted")
			}
		}

		return err
	}
}

func (c *Controller) dockerClusterList(w http.ResponseWriter, r *http.Request, path string, images bool) {
	s := c.Store.snapshot()
	workers := selectedClusterWorkers(s, r, false)
	if len(workers) == 0 {
		dockerError(w, 503, "no online Docker workers")
		return
	}
	for _, worker := range s.Workers {
		if selected := r.Header.Get("X-Zela-Worker"); selected != "" && selected != worker.ID {
			continue
		}
		if worker.Status != "online" {
			dockerError(w, 503, "cluster inventory unavailable while a worker is offline")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result := []map[string]json.RawMessage{}
	seen := map[string]bool{}
	query := r.URL.Query()
	limit, _ := strconv.Atoi(query.Get("limit"))
	if !images {
		query.Del("limit")
	}
	for _, worker := range workers {
		var list []map[string]json.RawMessage
		if err := c.dockerJSON(ctx, worker, path, query.Encode(), &list); err != nil {
			clusterFailure(w, err)
			return
		}
		for _, item := range list {
			var id string
			_ = json.Unmarshal(item["Id"], &id)
			if !images && !ownsContainer(s, requestPrincipal(r), id) {
				continue
			}
			if images && seen[id] {
				continue
			}
			seen[id] = true
			result = append(result, item)
		}
	}
	if !images {
		sort.SliceStable(result, func(i, j int) bool {
			var a, b int64
			_ = json.Unmarshal(result[i]["Created"], &a)
			_ = json.Unmarshal(result[j]["Created"], &b)
			return a > b
		})
		if limit > 0 && len(result) > limit {
			result = result[:limit]
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	dockerJSONResponse(w, 200, result)
}

func apiVersionLess(a, b string) bool {
	aa := strings.Split(a, ".")
	bb := strings.Split(b, ".")
	if len(aa) != 2 || len(bb) != 2 {
		return a < b
	}
	for i := 0; i < 2; i++ {
		x, _ := strconv.Atoi(aa[i])
		y, _ := strconv.Atoi(bb[i])
		if x != y {
			return x < y
		}
	}
	return false
}
func (c *Controller) dockerClusterInfo(w http.ResponseWriter, r *http.Request, path, ep string) {
	workers := selectedClusterWorkers(c.Store.snapshot(), r, false)
	if len(workers) == 0 {
		dockerError(w, 503, "no online Docker workers")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if ep == "/info" {
		var result map[string]any
		totals := map[string]float64{}
		for _, worker := range workers {
			var info map[string]any
			if err := c.dockerJSON(ctx, worker, path, "", &info); err != nil {
				clusterFailure(w, err)
				return
			}
			if result == nil {
				result = info
			}
			for _, key := range []string{"NCPU", "MemTotal", "Containers", "ContainersRunning", "ContainersPaused", "ContainersStopped", "Images"} {
				if n, ok := info[key].(float64); ok {
					totals[key] += n
				}
			}
		}
		for key, n := range totals {
			result[key] = n
		}
		if !requestPrincipal(r).Admin {
			safe := map[string]any{}
			for _, key := range []string{"ServerVersion", "OSType", "Architecture", "NCPU", "MemTotal"} {
				safe[key] = result[key]
			}
			result = safe
		}
		result["Name"] = "Zela Docker cluster"
		if selected := r.Header.Get("X-Zela-Worker"); selected != "" {
			result["Name"] = workers[0].Name
			result["ZelaWorkerID"] = selected
		}
		result["ID"] = "zela-cluster"
		result["ZelaWorkers"] = len(workers)
		dockerJSONResponse(w, 200, result)
		return
	}
	var result map[string]any
	maxMin := ""
	for _, worker := range workers {
		var v map[string]any
		if err := c.dockerJSON(ctx, worker, "/version", "", &v); err != nil {
			clusterFailure(w, err)
			return
		}
		version, _ := v["ApiVersion"].(string)
		minimum, _ := v["MinAPIVersion"].(string)
		if !dockerVersionPattern.MatchString(version) || (minimum != "" && !dockerVersionPattern.MatchString(minimum)) {
			dockerError(w, 502, "invalid worker API version")
			return
		}
		current, _ := result["ApiVersion"].(string)
		if result == nil || apiVersionLess(version, current) {
			result = v
		}
		if apiVersionLess(maxMin, minimum) {
			maxMin = minimum
		}
	}
	version, _ := result["ApiVersion"].(string)
	if version == "" || (maxMin != "" && apiVersionLess(version, maxMin)) {
		dockerError(w, 503, "workers have no common Docker API version")
		return
	}
	result["MinAPIVersion"] = maxMin
	w.Header().Set("API-Version", version)
	if ep == "/_ping" {
		w.Header().Set("Docker-Experimental", "false")
		w.Header().Set("OSType", "linux")
		w.Header().Set("Content-Type", "text/plain")
		if r.Method != "HEAD" {
			_, _ = io.WriteString(w, "OK")
		}
		return
	}
	dockerJSONResponse(w, 200, result)
}

func (c *Controller) dockerClusterPull(w http.ResponseWriter, r *http.Request, path string) {
	if r.URL.Query().Get("fromImage") == "" || r.URL.Query().Get("fromSrc") != "" {
		dockerError(w, 501, "cluster image import is not supported; supply fromImage for a registry pull")
		return
	}
	workers := selectedClusterWorkers(c.Store.snapshot(), r, true)
	if len(workers) == 0 {
		dockerError(w, 503, "no active online Docker workers")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	encoder := json.NewEncoder(w)
	for _, worker := range workers {
		resp, err := c.dockerRequest(r.Context(), worker, "POST", path, r.URL.RawQuery, r.Header, nil)
		if err != nil {
			_ = encoder.Encode(map[string]string{"error": "cluster image pull failed"})
			return
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			_ = encoder.Encode(map[string]string{"error": "worker could not pull the requested image"})
			return
		}
		decoder := json.NewDecoder(resp.Body)
		for {
			var frame json.RawMessage
			err = decoder.Decode(&frame)
			if err == io.EOF {
				break
			}
			if err != nil {
				resp.Body.Close()
				_ = encoder.Encode(map[string]string{"error": "cluster image pull stream failed"})
				return
			}
			if encoder.Encode(frame) != nil {
				resp.Body.Close()
				return
			}
			_ = http.NewResponseController(w).Flush()
			var progress struct{ Error string }
			_ = json.Unmarshal(frame, &progress)
			if progress.Error != "" {
				resp.Body.Close()
				return
			}
		}
		resp.Body.Close()
	}
}
