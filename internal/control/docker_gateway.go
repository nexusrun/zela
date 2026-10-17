package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/zela-dev/zela/internal/dockerapi"
	"github.com/zela-dev/zela/internal/engine"
)

// Docker streams and HTTP upgrades need HTTP/1.1 on the agent connection.
var dockerGatewayTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Protocols = new(http.Protocols)
	t.Protocols.SetHTTP1(true)
	return t
}()

func dockerError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
}

// Administrator selectors retain the full worker API. Scoped selectors keep the
// workload allowlist, capacity checks, and durable container/exec ownership routing.
func (c *Controller) dockerGateway(token string, w http.ResponseWriter, r *http.Request) {
	p, ok := c.bearerPrincipal(r, token)
	if !ok {
		dockerError(w, 401, "valid control-plane bearer token required")
		return
	}
	if mcpScope(p.Scope) {
		dockerError(w, 403, "MCP credentials cannot access the Docker gateway")
		return
	}
	r, cleanup, ok := c.tokenRequest(r, p)
	if !ok {
		dockerError(w, 401, "token expired or revoked")
		return
	}
	defer cleanup()

	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			dockerError(w, http.StatusForbidden, "cross-origin Docker API request rejected")
			return
		}
	}
	workerID := r.Header.Get("X-Zela-Worker")
	if len(r.Header.Values("X-Zela-Worker")) > 1 {
		dockerError(w, http.StatusBadRequest, "supply exactly one X-Zela-Worker header")
		return
	}
	path, rawPath := r.URL.Path, r.URL.RawPath
	if strings.HasPrefix(path, "/docker/") {
		const prefix = "/docker/workers/"
		if !strings.HasPrefix(path, prefix) || !strings.HasPrefix(r.URL.EscapedPath(), prefix) {
			dockerError(w, http.StatusNotFound, "use /docker/workers/{worker-id}/ followed by a Docker API path")
			return
		}
		selected, suffix, ok := strings.Cut(strings.TrimPrefix(path, prefix), "/")
		if !ok || selected == "" {
			dockerError(w, http.StatusBadRequest, "worker ID and Docker API path are required")
			return
		}
		if workerID != "" && workerID != selected {
			dockerError(w, http.StatusBadRequest, "worker path and X-Zela-Worker header disagree")
			return
		}
		// IDs cannot contain escapes or slashes; preserve escaping only in the
		// Docker path (image references, filters, archive paths, and so on).
		if !strings.HasPrefix(r.URL.EscapedPath(), prefix+selected+"/") {
			dockerError(w, http.StatusBadRequest, "worker ID must be unescaped")
			return
		}
		workerID, path = selected, "/"+suffix
		if rawPath != "" {
			rawPath = strings.TrimPrefix(rawPath, prefix+selected)
		}
	}
	enginePath, ok := dockerapi.EnginePath(path)
	if !ok {
		dockerError(w, http.StatusNotFound, "unknown or invalid Docker Engine API path")
		return
	}
	if err := c.deploymentDockerAccess(r, enginePath); err != nil {
		dockerError(w, 403, err.Error())
		return
	}
	if !p.Admin {
		if p.Scope == "infrastructure" && workerID == "" && r.Method == "POST" && (enginePath == "/containers/create" || enginePath == "/images/create") {
			dockerError(w, 400, "Zela must select a worker with X-Zela-Worker or /docker/workers/{id}/; discover workers at /api/cluster/workers")
			return
		}
		if workerID != "" {
			if _, exists := c.Store.snapshot().Workers[workerID]; !exists {
				dockerError(w, 404, "worker not found")
				return
			}
		}
		r = r.Clone(r.Context())
		u := *r.URL
		u.Path, u.RawPath = path, rawPath
		r.URL = &u
		r.Header.Set("X-Zela-Worker", workerID)
		c.dockerCluster(w, r, path, enginePath)
		return
	}
	if workerID == "" {
		c.dockerCluster(w, r, path, enginePath)
		return
	}
	var observe func(*http.Response) error
	if strings.HasPrefix(enginePath, "/containers/") && enginePath != "/containers/create" && enginePath != "/containers/json" {
		ref, action, _ := strings.Cut(strings.TrimPrefix(enginePath, "/containers/"), "/")
		id := ref
		for candidate, ct := range c.Store.snapshot().DockerContainers {
			if ct.WorkerID == workerID && containerMatches(candidate, ct.Name, ref) {
				id = candidate
				break
			}
		}
		observe = c.dockerObserve(workerID, id, action, r, nil)
	}
	c.dockerForward(w, r, workerID, path, rawPath, observe)
}

func (c *Controller) dockerForward(w http.ResponseWriter, r *http.Request, workerID, path, rawPath string, observe func(*http.Response) error) {
	s := c.Store.snapshot()
	worker, ok := s.Workers[workerID]
	if !ok {
		dockerError(w, http.StatusNotFound, "worker not found; discover workers through /api/cluster/workers")
		return
	}
	if s.Demo {
		dockerError(w, http.StatusNotImplemented, "Docker API gateway requires live workers; demo mode does not proxy to Docker")
		return
	}
	if worker.Status == "provisioning" || worker.Status == "provision-failed" {
		dockerError(w, http.StatusServiceUnavailable, "worker provisioning is not complete")
		return
	}
	e, err := c.Factory(worker)
	if err != nil {
		dockerError(w, http.StatusBadGateway, "worker transport unavailable; check worker connectivity and vault configuration")
		return
	}
	client, ok := e.(*engine.Client)
	if !ok || client.HTTP == nil {
		dockerError(w, http.StatusNotImplemented, "worker transport does not support Docker API forwarding")
		return
	}
	target, err := url.Parse(client.URL)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		dockerError(w, http.StatusBadGateway, "invalid worker transport")
		return
	}
	// Remote agents expose a separate full-API namespace. Local Unix sockets
	// connect directly to the Engine; the existing monitoring subset is unchanged.
	if !strings.HasPrefix(worker.Endpoint, "unix://") {
		target.Path = "/docker"
	}
	transport := client.HTTP.Transport
	if transport == nil {
		transport = dockerGatewayTransport
	}
	if tr, ok := transport.(interface{ CloseIdleConnections() }); ok && transport != dockerGatewayTransport {
		defer tr.CloseIdleConnections()
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	defer cancel()
	r = r.Clone(ctx)
	u := *r.URL
	u.Path, u.RawPath = path, rawPath
	r.URL = &u
	// Preserve long uploads, log/event streams, wait requests, and hijacked exec
	// sessions. Header deadlines and normal console/API deadlines still apply.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	proxy := &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("X-Zela-Worker")
			pr.Out.Header.Del(LeaderEpochHeader) // Only the manager transport may set it.
			pr.Out.Header.Del("Proxy-Authorization")
			if client.Token != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+client.Token)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del("Authorization")
			resp.Header.Del("Set-Cookie")
			resp.Header.Set("X-Zela-Worker", workerID)
			resp.Header.Set("Cache-Control", "no-store")
			if observe != nil {
				return observe(resp)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			dockerError(w, http.StatusBadGateway, "Docker worker unavailable; verify connectivity and update zela-worker for gateway support")
		},
	}
	proxy.ServeHTTP(w, r)
}
