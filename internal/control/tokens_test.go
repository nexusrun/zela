package control

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tokenAPICall(t *testing.T, h http.Handler, token, method, path, body string, want int) []byte {
	t.Helper()
	r := httptest.NewRequest(method, "http://control"+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: %d %s; want %d", method, path, w.Code, w.Body, want)
	}
	return w.Body.Bytes()
}
func issueToken(t *testing.T, c *Controller, name string) (string, AccessTokenMetadata) {
	t.Helper()
	raw, meta, err := c.Store.createAccessToken(name, 90)
	if err != nil {
		t.Fatal(err)
	}
	return raw, meta
}
func TestDeploymentTokenAdministrationAndAPI(t *testing.T) {
	c, _ := fixture(t)
	h := Handler(c, HTTPConfig{Token: testToken})
	var result struct {
		Token    string
		Metadata AccessTokenMetadata
	}
	b := tokenAPICall(t, h, testToken, "POST", "/api/tokens", `{"name":"NEXUS AI","expiresInDays":30}`, 201)
	if err := json.Unmarshal(b, &result); err != nil || result.Token == "" || result.Metadata.Scope != "deployments" || result.Metadata.ExpiresAt == nil {
		t.Fatal("invalid token response")
	}
	raw, id := result.Token, result.Metadata.ID
	for _, route := range []struct{ method, path, body string }{
		{"GET", "/api/state", ""}, {"GET", "/api/tokens", ""}, {"POST", "/api/tokens", `{"name":"escalation"}`}, {"POST", "/api/tokens/" + id + "/revoke", `{}`},
		{"POST", "/api/workers", `{}`}, {"POST", "/api/workers/w1/actions", `{"action":"pause"}`}, {"POST", "/api/workers/provision", `{}`},
		{"POST", "/api/secrets", `{}`}, {"POST", "/api/secrets/secret/delete", `{}`}, {"POST", "/api/monitoring/rules", `{}`}, {"POST", "/api/refresh", `{}`},
		{"GET", "/api/workers/w1/metrics/history", ""}, {"GET", "/api/workers/w1/containers/one/logs", ""},
	} {
		tokenAPICall(t, h, raw, route.method, route.path, route.body, 403)
	}
	tokenAPICall(t, h, raw, "POST", "/api/login", `{"token":"`+raw+`"}`, 401)
	for _, body := range []string{`{"name":"","expiresInDays":30}`, `{"name":"bad","expiresInDays":-1}`, `{"name":"bad","expiresInDays":366}`, `{"name":"bad","scope":"admin"}`} {
		tokenAPICall(t, h, testToken, "POST", "/api/tokens", body, 400)
	}
	var op Operation
	b = tokenAPICall(t, h, raw, "POST", "/api/deployments", `{"name":"scoped-app","image":"alpine","replicas":1,"memoryMB":128,"cpus":0.25}`, 202)
	if json.Unmarshal(b, &op) != nil || op.AccessTokenID != id {
		t.Fatal("operation not owned by token")
	}
	if waitOp(t, c, op, nil).Status != "succeeded" {
		t.Fatal("deployment failed")
	}
	var deployments map[string]Deployment
	b = tokenAPICall(t, h, raw, "GET", "/api/deployments", "", 200)
	if json.Unmarshal(b, &deployments) != nil || len(deployments) != 1 {
		t.Fatal("deployment listing leaked or lost workloads")
	}
	depID := ""
	for key, dep := range deployments {
		depID = key
		if dep.AccessTokenID != id {
			t.Fatal("deployment owner lost")
		}
	}
	tokenAPICall(t, h, raw, "GET", "/api/deployments/"+depID, "", 200)
	tokenAPICall(t, h, raw, "GET", "/api/operations/"+op.ID, "", 200)
	other, _ := issueToken(t, c, "other")
	tokenAPICall(t, h, other, "GET", "/api/deployments/"+depID, "", 403)
	tokenAPICall(t, h, other, "POST", "/api/deployments/"+depID+"/actions", `{"action":"delete"}`, 403)
	tokenAPICall(t, h, other, "GET", "/api/operations/"+op.ID, "", 403)
	b = tokenAPICall(t, h, raw, "POST", "/api/deployments/"+depID+"/actions", `{"action":"stop"}`, 202)
	_ = json.Unmarshal(b, &op)
	waitOp(t, c, op, nil)
	b = tokenAPICall(t, h, testToken, "GET", "/api/state", "", 200)
	if bytes.Contains(b, []byte(raw)) || bytes.Contains(b, []byte(`"digest"`)) || bytes.Contains(b, []byte(`"accessTokens"`)) {
		t.Fatal("token secret exposed in state")
	}
	b = tokenAPICall(t, h, testToken, "GET", "/api/tokens", "", 200)
	if bytes.Contains(b, []byte(raw)) || bytes.Contains(b, []byte(`"digest"`)) {
		t.Fatal("token secret exposed in list")
	}
	tokenAPICall(t, h, testToken, "POST", "/api/tokens/"+id+"/revoke", `{}`, 200)
	tokenAPICall(t, h, raw, "GET", "/api/deployments", "", 401)
	if _, ok := c.Store.snapshot().Deployments[depID]; !ok {
		t.Fatal("revocation removed a running workload")
	}
}
func TestIntegrationTokenPersistenceExpiryAndCancellation(t *testing.T) {
	for _, scope := range []string{"deployments", "infrastructure"} {
		for _, backend := range []string{"json", "postgresql"} {
			t.Run(backend+"/"+scope, func(t *testing.T) {
				issue := func(c *Controller, name string) (string, AccessTokenMetadata) {
					raw, meta, err := c.Store.createAccessToken(name, 90, scope)
					if err != nil {
						t.Fatal(err)
					}
					return raw, meta
				}
				dir := t.TempDir()
				db := &testDatabase{documents: map[string][]byte{}}
				open := func() (*Store, error) {
					if backend == "json" {
						return OpenStore(dir, false)
					}
					db.closed = false
					return openStore(dir, false, func() (*postgresStore, error) { return testBackend(db), nil })
				}
				s, err := open()
				if err != nil {
					t.Fatal(err)
				}
				c := New(s, nil)
				raw, meta := issue(c, "persistent")
				var saved []byte
				if backend == "json" {
					saved, _ = os.ReadFile(filepath.Join(dir, "cluster.json"))
				} else {
					saved = db.documents["cluster"]
				}
				if bytes.Contains(saved, []byte(raw)) || !bytes.Contains(saved, []byte(`"digest"`)) {
					t.Fatal("token not stored as a hash")
				}
				c.Close()
				s.Close()
				s, err = open()
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				c = New(s, nil)
				defer c.Close()
				r := httptest.NewRequest("GET", "http://control/version", nil)
				r.Header.Set("Authorization", "Bearer "+raw)
				p, ok := c.bearerPrincipal(r, testToken)
				if !ok || p.TokenID != meta.ID || p.Scope != scope {
					t.Fatal("persisted token rejected")
				}
				active, cleanup, ok := c.tokenRequest(r, p)
				if !ok {
					t.Fatal("registration failed")
				}
				defer cleanup()
				if backend == "postgresql" {
					db.fail = "cluster"
					if err := c.revokeAccessToken(meta.ID); err == nil {
						t.Fatal("failed write reported success")
					}
					if active.Context().Err() != nil {
						t.Fatal("failed revoke canceled access")
					}
					if _, ok := c.bearerPrincipal(r, testToken); !ok {
						t.Fatal("failed revoke changed state")
					}
					db.fail = ""
				}
				if err := c.revokeAccessToken(meta.ID); err != nil {
					t.Fatal(err)
				}
				select {
				case <-active.Context().Done():
				case <-time.After(time.Second):
					t.Fatal("revocation did not cancel active request")
				}
				if _, ok := c.bearerPrincipal(r, testToken); ok {
					t.Fatal("revoked token accepted")
				}
				raw, meta = issue(c, "expire")
				expiry := time.Now().Add(30 * time.Millisecond)
				if err := s.update(func(state *State) error {
					v := state.AccessTokens[meta.ID]
					v.ExpiresAt = &expiry
					state.AccessTokens[meta.ID] = v
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				r.Header.Set("Authorization", "Bearer "+raw)
				p, ok = c.bearerPrincipal(r, testToken)
				if !ok {
					t.Fatal("unexpired token rejected")
				}
				active, cleanup, ok = c.tokenRequest(r, p)
				if !ok {
					t.Fatal("request registration failed")
				}
				defer cleanup()
				select {
				case <-active.Context().Done():
				case <-time.After(time.Second):
					t.Fatal("expiry did not close active request")
				}
				if _, ok := c.bearerPrincipal(r, testToken); ok {
					t.Fatal("expired token accepted")
				}
				r.Header.Add("Authorization", "Bearer "+testToken)
				if _, ok := c.bearerPrincipal(r, testToken); ok {
					t.Fatal("duplicate credentials accepted")
				}
			})
		}
	}
}
func TestDeploymentTokenDockerGatewayIsolation(t *testing.T) {
	c, client, _, _ := clusterFixture(t)
	raw, meta := issueToken(t, c, "Zela")
	other, _ := issueToken(t, c, "second")
	call := func(token, method, path string, body any, want int) []byte {
		return gatewayEndpointCall(t, client, "http://control", token, "", method, path, body, want)
	}
	adminID := autoContainer(t, client, "admin-container", nil)
	var created struct {
		ID string `json:"Id"`
	}
	b := call(raw, "POST", "/v1.51/containers/create?name=scoped", map[string]any{"Image": "alpine", "HostConfig": map[string]any{"Privileged": false, "NetworkMode": "bridge"}}, 201)
	_ = json.Unmarshal(b, &created)
	id := created.ID
	if id == "" || c.Store.snapshot().DockerContainers[id].AccessTokenID != meta.ID {
		t.Fatal("missing container ownership")
	}
	call(raw, "HEAD", "/_ping", nil, 200)
	call(raw, "GET", "/version", nil, 200)
	call(raw, "GET", "/info", nil, 200)
	call(raw, "POST", "/containers/"+id+"/start", nil, 204)
	call(raw, "GET", "/containers/scoped/json", nil, 200)
	call(raw, "GET", "/containers/"+id+"/logs?stdout=1", nil, 200)
	var list []map[string]any
	_ = json.Unmarshal(call(raw, "GET", "/containers/json?all=1", nil, 200), &list)
	if len(list) != 1 || list[0]["Id"] != id {
		t.Fatal("container list leaks another owner")
	}
	for _, target := range []string{adminID, "admin-container", "scoped"} {
		call(other, "GET", "/containers/"+target+"/json", nil, 403)
	}
	call(raw, "GET", "/containers/"+adminID+"/json", nil, 403)
	call(raw, "DELETE", "/containers/"+adminID, nil, 403)
	gatewayEndpointCall(t, client, "http://control", raw, "w1", "GET", "/containers/json", nil, 200)
	call(raw, "GET", "/docker/workers/w1/containers/json", nil, 200)
	for _, path := range []string{"/build", "/containers/prune", "/images/prune", "/volumes/create", "/networks/create", "/plugins/create", "/swarm/init", "/auth"} {
		call(raw, "POST", path, map[string]any{}, 403)
	}
	call(raw, "GET", "/events", nil, 403)
	call(raw, "POST", "/images/create?fromSrc=http://internal", nil, 403)
	for _, host := range []map[string]any{
		{"Privileged": true}, {"Binds": []string{"/:/host"}}, {"Mounts": []map[string]any{{"Source": "/var/run/docker.sock", "Target": "/sock", "Type": "bind"}}},
		{"NetworkMode": "host"}, {"NetworkMode": "container:other"}, {"PidMode": "host"}, {"IpcMode": "host"}, {"UsernsMode": "host"},
		{"CapAdd": []string{"SYS_ADMIN"}}, {"SecurityOpt": []string{"seccomp=unconfined"}}, {"Devices": []string{"/dev/sda"}},
		{"Runtime": "unsafe"}, {"DeviceRequests": []map[string]any{{"Driver": "custom"}}}, {"LogConfig": map[string]any{"Type": "syslog"}},
		{"privileged": true}, {"memory": 1}, {"MaskedPaths": []string{""}},
	} {
		call(raw, "POST", "/containers/create", map[string]any{"Image": "alpine", "HostConfig": host}, 403)
	}
	for _, config := range []json.RawMessage{
		json.RawMessage(`{"Image":"alpine","hostconfig":{"Privileged":true}}`),
		json.RawMessage(`{"Image":"alpine","HostConfig":{"Privileged":false,"Privileged":true}}`),
		json.RawMessage(`{"Image":"alpine","HostConfig":{},"HostConfig":{"Privileged":true}}`),
		json.RawMessage(`{"Image":"alpine","NetworkingConfig":{"EndpointsConfig":{"host":{}}}}`),
	} {
		call(raw, "POST", "/containers/create", config, 403)
	}
	call(raw, "POST", "/containers/"+id+"/start", map[string]any{"Privileged": true}, 403)
	call(raw, "POST", "/containers/"+id+"/exec", map[string]any{"Cmd": []string{"sh"}, "Privileged": true}, 403)
	var exec struct {
		ID string `json:"Id"`
	}
	_ = json.Unmarshal(call(raw, "POST", "/containers/"+id+"/exec", map[string]any{"Cmd": []string{"echo", "hi"}, "AttachStdout": true}, 201), &exec)
	call(other, "POST", "/exec/"+exec.ID+"/start", map[string]bool{"Detach": false, "Tty": false}, 403)
	call(raw, "POST", "/exec/"+exec.ID+"/start", map[string]bool{"Detach": false, "Tty": false}, 200)
	call(raw, "GET", "/exec/"+exec.ID+"/json", nil, 200)
	call(raw, "POST", "/containers/"+id+"/stop", nil, 204)
	call(raw, "DELETE", "/containers/"+id, nil, 204)
	if err := c.revokeAccessToken(meta.ID); err != nil {
		t.Fatal(err)
	}
	call(raw, "GET", "/version", nil, 401)
	call(testToken, "GET", "/containers/"+adminID+"/json", nil, 200)
}

func TestDeploymentTokenRevokesGatewayStream(t *testing.T) {
	base := newSimulatedDocker("stream")
	started, canceled := make(chan struct{}), make(chan struct{})
	daemon := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/logs") {
			w.WriteHeader(200)
			_, _ = io.WriteString(w, "stream\n")
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
			close(canceled)
			return
		}
		base.ServeHTTP(w, r)
	})
	c, client := gatewayHTTPStack(t, map[string]http.RoundTripper{"w1": pipeHTTPServer(t, daemon)})
	c.Refresh(context.Background())
	raw, meta := issueToken(t, c, "stream")
	var result struct {
		ID string `json:"Id"`
	}
	_ = json.Unmarshal(gatewayEndpointCall(t, client, "http://control", raw, "", "POST", "/containers/create", map[string]string{"Image": "alpine"}, 201), &result)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://control/containers/"+result.ID+"/logs?follow=1", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	<-started
	if err := c.revokeAccessToken(meta.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("revocation did not propagate to worker stream")
	}
}

func TestDeploymentTokenCannotBorrowAdminCookie(t *testing.T) {
	c, _ := fixture(t)
	h := Handler(c, HTTPConfig{Token: testToken})
	raw, _ := issueToken(t, c, "restricted")
	login := httptest.NewRequest("POST", "http://control/api/login", strings.NewReader(`{"token":"`+testToken+`"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, login)
	if w.Code != 200 {
		t.Fatal("admin sign-in failed")
	}
	cookie := w.Result().Cookies()[0]
	for _, token := range []string{raw, "invalid"} {
		req := httptest.NewRequest("GET", "http://control/api/state", nil)
		req.AddCookie(cookie)
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		want := 401
		if token == raw {
			want = 403
		}
		if out.Code != want {
			t.Fatal("bearer token borrowed administrator session")
		}
	}
	req := httptest.NewRequest("POST", "http://control/api/tokens", strings.NewReader(`{"name":"cookie-token","expiresInDays":7}`))
	req.AddCookie(cookie)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	if out.Code != 201 {
		t.Fatal("administrator UI cannot create tokens")
	}
}

func TestDeploymentTokenRevokesUpgradedExec(t *testing.T) {
	base := newSimulatedDocker("upgrade")
	closed := make(chan struct{})
	daemon := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/exec/") && strings.HasSuffix(r.URL.Path, "/start") && r.Header.Get("Upgrade") == "tcp" {
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			defer close(closed)
			_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			_ = rw.Flush()
			_, _ = io.Copy(io.Discard, conn)
			return
		}
		base.ServeHTTP(w, r)
	})
	c, client := gatewayHTTPStack(t, map[string]http.RoundTripper{"w1": pipeHTTPServer(t, daemon)})
	c.Refresh(context.Background())
	raw, meta := issueToken(t, c, "upgrade")
	var ct, exec struct {
		ID string `json:"Id"`
	}
	_ = json.Unmarshal(gatewayEndpointCall(t, client, "http://control", raw, "", "POST", "/containers/create?name=upgrade", map[string]string{"Image": "alpine"}, 201), &ct)
	_ = json.Unmarshal(gatewayEndpointCall(t, client, "http://control", raw, "", "POST", "/containers/"+ct.ID+"/exec", map[string]any{"Cmd": []string{"cat"}, "AttachStdin": true, "AttachStdout": true}, 201), &exec)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://control/exec/"+exec.ID+"/start", strings.NewReader(`{"Detach":false,"Tty":true}`))
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	streamClient := *client
	streamClient.Timeout = 0
	resp, err := streamClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 101 {
		t.Fatal("exec upgrade denied")
	}
	if err := c.revokeAccessToken(meta.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("revocation did not close upgraded exec")
	}
}
