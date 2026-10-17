package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zela-dev/zela/internal/mcpserver"
	"github.com/zela-dev/zela/internal/platformclient"
)

type MCPStatus struct {
	Endpoint     string `json:"endpoint"`
	OAuthEnabled bool   `json:"oauthEnabled"`
}

func mcpScope(scope string) bool { return scope == "mcp_read" || scope == "mcp_manage" }

// A separate allowlist keeps cluster credentials out of the Docker gateway,
// raw state, environment values, private keys and token administration.
func mcpAPIAccess(pattern string, manage bool) bool {
	switch pattern {
	case "GET /api/mcp/access", "GET /api/cluster", "GET /api/cluster/workers", "GET /api/cluster/workers/{id}", "GET /api/mcp/inventory", "GET /api/workers/{id}/metrics/history", "GET /api/operations/{id}", "GET /api/notifications", "GET /api/workers/{worker}/containers/{id}/logs":
		return true
	}
	if !manage {
		return false
	}
	switch pattern {
	case "GET /api/mcp/secrets", "POST /api/refresh", "POST /api/workers/provision", "POST /api/workers/{id}/provision-retry", "POST /api/workers/{id}/agent-update", "POST /api/workers/{id}/actions", "POST /api/deployments", "POST /api/deployments/{id}/actions", "POST /api/deployments/{id}/replicas/{replica}/move", "POST /api/deployments/{id}/image", "POST /api/workers/{worker}/containers/{id}/actions":
		return true
	}
	return false
}

func (c *Controller) registerMCP(mux *http.ServeMux, cfg HTTPConfig, api func(string, http.HandlerFunc), authenticated func(*http.Request) (principal, bool), sameOrigin func(*http.Request) bool) {
	api("GET /api/mcp/access", func(w http.ResponseWriter, r *http.Request) {
		p := requestPrincipal(r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"readOnly": !p.Admin && p.Scope != "mcp_manage", "scope": p.Scope, "oauthEnabled": cfg.MCPPublicURL != ""})
	})
	api("GET /api/mcp/inventory", func(w http.ResponseWriter, r *http.Request) {
		// Explicitly project inventory so newly added private state fields cannot leak.
		state := c.Store.snapshot()
		workers := make([]map[string]any, 0, len(state.Workers))
		for _, worker := range state.Workers {
			containers := make([]map[string]any, 0, len(worker.Containers))
			for _, ct := range worker.Containers {
				containers = append(containers, map[string]any{"id": ct.ID, "names": ct.Names, "image": ct.Image, "state": ct.State, "status": ct.Status, "ports": ct.Ports})
			}
			workers = append(workers, map[string]any{"id": worker.ID, "name": worker.Name, "region": worker.Region, "mode": worker.Mode, "status": worker.Status, "lastSeen": worker.LastSeen, "containers": containers})
		}
		deployments := make([]map[string]any, 0, len(state.Deployments))
		for _, d := range state.Deployments {
			deployments = append(deployments, map[string]any{"id": d.ID, "name": d.Name, "image": d.Image, "replicas": d.Replicas, "workerIDs": d.WorkerIDs, "ports": d.Ports, "memoryMB": d.MemoryMB, "cpus": d.CPUs, "desiredState": d.DesiredState, "status": d.Status, "tasks": d.Tasks, "createdAt": d.CreatedAt})
		}
		alerts, _ := c.Store.Monitor.Public()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"observedAt": time.Now().UTC(), "demo": state.Demo, "workers": workers, "deployments": deployments, "operations": state.Operations, "alerts": alerts})
	})
	api("GET /api/mcp/secrets", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(c.Store.Vault.List())
	})
	oauth := c.registerMCPOAuth(mux, cfg, authenticated, sameOrigin)
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		p := requestPrincipal(r)
		// A loopback-only, in-process transport re-enters the normal API middleware.
		// Every tool revalidates the original credential and uses normal admission,
		// operation locking, fencing and ownership policies. No administrator impersonation.
		client, err := platformclient.New(platformclient.Config{URL: "http://127.0.0.1", Token: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), UserAgent: "zela-mcp/1.0", Timeout: 30 * time.Second, Transport: localAPITransport{mux}})
		if err != nil {
			return nil
		}
		return mcpserver.New(client.Do, !p.Admin && p.Scope != "mcp_manage")
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 1 << 20, PropagateRequestCancellation: true})
	serveMCP := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !sameOrigin(r) {
			http.Error(w, "cross-origin MCP request rejected", 403)
			return
		}
		// Browser cookies deliberately do not authorize machine-to-machine MCP calls.
		p, ok := c.bearerPrincipal(r, cfg.Token)
		if !ok {
			challenge := `Bearer realm="zela-mcp"`
			if oauth != "" {
				challenge += `, resource_metadata="` + oauth + `/.well-known/oauth-protected-resource/mcp"`
			}
			w.Header().Set("WWW-Authenticate", challenge)
			http.Error(w, "valid MCP bearer credential required", 401)
			return
		}
		if !p.Admin && !mcpScope(p.Scope) {
			http.Error(w, "create an mcp_read or mcp_manage credential", 403)
			return
		}
		r, cleanup, ok := c.tokenRequest(r, p)
		if !ok {
			http.Error(w, "token expired or revoked", 401)
			return
		}
		defer cleanup()
		handler.ServeHTTP(w, r)
	}
	for _, method := range []string{"GET", "POST", "DELETE"} {
		mux.HandleFunc(method+" /mcp", serveMCP)
	}
}

type localAPITransport struct{ handler http.Handler }

func (t localAPITransport) RoundTrip(r *http.Request) (*http.Response, error) {
	reply := &localAPIReply{header: make(http.Header)}
	t.handler.ServeHTTP(reply, r)
	if reply.status == 0 {
		reply.status = 200
	}
	return &http.Response{StatusCode: reply.status, Header: reply.header, Body: io.NopCloser(bytes.NewReader(reply.body.Bytes())), Request: r}, nil
}

type localAPIReply struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *localAPIReply) Header() http.Header { return w.header }
func (w *localAPIReply) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *localAPIReply) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.body.Len()+len(b) > platformclient.MaxResponseBytes {
		return 0, fmt.Errorf("MCP response too large")
	}
	return w.body.Write(b)
}

// Used by OAuth exchange to mint token hashes in the same state transaction.
func mcpTokenRecord(raw, id, name, scope string, expiry time.Time) accessTokenRecord {
	return accessTokenRecord{AccessTokenMetadata: AccessTokenMetadata{ID: id, Name: name, Scope: scope, Prefix: "zela_mcp_" + strings.TrimPrefix(id, "tok_"), CreatedAt: time.Now().UTC(), ExpiresAt: &expiry}, Digest: oauthDigest(raw), OAuth: true}
}
