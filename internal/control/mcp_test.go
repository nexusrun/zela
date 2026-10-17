package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zela-dev/zela/internal/engine"
)

type mcpTestTransport struct {
	h     http.Handler
	token string
}

func (tr mcpTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+tr.token)
	w := httptest.NewRecorder()
	tr.h.ServeHTTP(w, r)
	res := w.Result()
	res.Request = r
	return res, nil
}
func mcpTestSession(t *testing.T, h http.Handler, token string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "cluster-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: "http://127.0.0.1/mcp", HTTPClient: &http.Client{Transport: mcpTestTransport{h, token}}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}
func mcpCall(t *testing.T, s *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res
}
func mcpData(t *testing.T, r *mcp.CallToolResult, dst any) {
	t.Helper()
	if r.IsError {
		t.Fatalf("tool failed: %v", r.Content)
	}
	b, err := json.Marshal(r.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var wrapped struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(b, &wrapped) != nil || json.Unmarshal(wrapped.Data, dst) != nil {
		t.Fatalf("invalid structured output: %s", b)
	}
}
func TestMCPHTTPClusterLifecycleAndPermissions(t *testing.T) {
	c, _ := fixture(t)
	h := Handler(c, HTTPConfig{Token: testToken})
	read, readMeta, err := c.Store.createAccessToken("read AI", 30, "mcp_read")
	if err != nil {
		t.Fatal(err)
	}
	manage, _, err := c.Store.createAccessToken("manage AI", 30, "mcp_manage")
	if err != nil {
		t.Fatal(err)
	}
	reader := mcpTestSession(t, h, read)
	list, err := reader.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 7 {
		t.Fatalf("read catalog: %d", len(list.Tools))
	}
	for _, tool := range list.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("unsafe read tool: %s", tool.Name)
		}
	}
	var cluster map[string]any
	mcpData(t, mcpCall(t, reader, "cluster_overview", map[string]any{}), &cluster)
	if _, ok := cluster["workers"]; !ok {
		t.Fatal("missing workers")
	}
	if r, err := reader.CallTool(context.Background(), &mcp.CallToolParams{Name: "worker_action", Arguments: map[string]any{"workerID": "w1", "action": "pause"}}); err == nil && !r.IsError {
		t.Fatal("reader invoked a management tool")
	}
	reader = mcpTestSession(t, h, read)
	for _, path := range []string{"/api/state", "/api/tokens", "/api/deployments", "/api/mcp/secrets", "/v1.51/containers/json"} {
		tokenAPICall(t, h, read, "GET", path, "", 403)
	}
	tokenAPICall(t, h, read, "POST", "/api/workers/w1/actions", `{"action":"pause"}`, 403)
	manager := mcpTestSession(t, h, manage)
	list, err = manager.ListTools(context.Background(), nil)
	if err != nil || len(list.Tools) != 19 {
		t.Fatalf("management catalog: %v (%d)", err, len(list.Tools))
	}
	// SDK schemas reject unsupported actions, extra fields and path injection.
	for _, args := range []map[string]any{{"workerID": "w1", "action": "exec"}, {"workerID": "../w1", "action": "pause"}, {"workerID": "w1", "action": "pause", "admin": true}} {
		if r, err := manager.CallTool(context.Background(), &mcp.CallToolParams{Name: "worker_action", Arguments: args}); err == nil && !r.IsError {
			t.Fatal("invalid arguments accepted")
		}
	}
	var op Operation
	mcpData(t, mcpCall(t, manager, "worker_action", map[string]any{"workerID": "w2", "action": "pause"}), &op)
	succeeded(t, c, op, nil)
	if c.Store.snapshot().Workers["w2"].Mode != "paused" {
		t.Fatal("worker was not paused")
	}
	mcpData(t, mcpCall(t, reader, "get_operation", map[string]any{"operationID": op.ID}), &op)
	if op.Status != "succeeded" {
		t.Fatal(op)
	}
	mcpData(t, mcpCall(t, manager, "create_deployment", map[string]any{"name": "mcp-app", "image": "nginx:alpine", "replicas": 1, "workerIDs": []string{"w1"}, "memoryMB": 128, "cpus": 0.25, "env": []string{"SECRET=never-send-to-ai"}}), &op)
	succeeded(t, c, op, nil)
	var id string
	for _, d := range c.Store.snapshot().Deployments {
		if d.Name == "mcp-app" {
			id = d.ID
		}
	}
	if id == "" {
		t.Fatal("deployment missing")
	}
	mcpData(t, mcpCall(t, manager, "scale_deployment", map[string]any{"deploymentID": id, "replicas": 2}), &op)
	succeeded(t, c, op, nil)
	if c.Store.snapshot().Deployments[id].Replicas != 2 {
		t.Fatal("scale did not complete")
	}
	// Sensitive configuration is excluded even when the caller is an administrator.
	_ = c.Store.update(func(s *State) error {
		w := s.Workers["w1"]
		w.Token = "worker-secret"
		w.Containers = append(w.Containers, engine.Container{ID: "unmanaged", Labels: map[string]string{"secret": "label-secret"}})
		s.Workers["w1"] = w
		return nil
	})
	inventory := mcpCall(t, manager, "cluster_inventory", map[string]any{})
	b, _ := json.Marshal(inventory)
	for _, secret := range []string{"never-send-to-ai", "worker-secret", "label-secret", testToken, manage, "accessTokens", "oauth"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("inventory leaked %q", secret)
		}
	}
	// Management credentials still cannot bypass the named cluster API boundary.
	for _, path := range []string{"/api/state", "/api/tokens", "/v1.51/containers/json"} {
		tokenAPICall(t, h, manage, "GET", path, "", 403)
	}
	if err := c.revokeAccessToken(readMeta.ID); err != nil {
		t.Fatal(err)
	}
	tokenAPICall(t, h, read, "POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, 401)
}

func TestMCPLegacyProtocolAndHTTPBoundary(t *testing.T) {
	c, _ := fixture(t)
	h := Handler(c, HTTPConfig{Token: testToken})
	call := func(method, token, body, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://control.local/mcp", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("MCP-Protocol-Version", "2025-11-25")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, body := range []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"legacy","version":"1"}}}`, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_workers","arguments":{}}}`} {
		w := call("POST", testToken, body, "")
		if w.Code != 200 || strings.Contains(w.Body.String(), `"isError":true`) {
			t.Fatalf("legacy protocol: %d %s", w.Code, w.Body.String())
		}
	}
	if w := call("POST", "", `{}`, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call("POST", testToken, `{}`, "https://attacker.example"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	for _, method := range []string{"GET", "DELETE"} {
		if w := call(method, testToken, "", ""); w.Code != 405 {
			t.Fatalf("%s: %d", method, w.Code)
		}
	}
	if w := call("POST", testToken, strings.Repeat(" ", (1<<20)+1), ""); w.Code != 413 {
		t.Fatalf("oversized body: %d", w.Code)
	}
	for _, scope := range []string{"infrastructure", "deployments"} {
		raw, _, err := c.Store.createAccessToken("existing integration", 1, scope)
		if err != nil {
			t.Fatal(err)
		}
		if w := call("POST", raw, `{}`, ""); w.Code != 403 {
			t.Fatal("existing integration gained MCP access")
		}
	}
	login := httptest.NewRequest("POST", "http://control.local/api/login", strings.NewReader(`{"token":"`+testToken+`"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, login)
	r := httptest.NewRequest("POST", "http://control.local/mcp", strings.NewReader(`{}`))
	r.AddCookie(w.Result().Cookies()[0])
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("browser cookie authorized MCP")
	}
}
