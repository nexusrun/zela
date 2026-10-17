package control

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zela-dev/zela/internal/dockerapi"
	"github.com/zela-dev/zela/internal/engine"
	"github.com/zela-dev/zela/internal/sshclient"
)

//go:embed web/*
var assets embed.FS

type HTTPConfig struct {
	Token            string
	AllowHTTPWorkers bool
	MCPPublicURL     string
	MCPRedirectURIs  []string
	// MCPProvisionHosts lists IPs, CIDRs or exact hostnames that mcp_manage
	// credentials may provision. Empty means MCP cannot provision new hosts.
	MCPProvisionHosts []string
	// MCPTrustedProxies lists proxy IPs/CIDRs whose X-Forwarded-For is used
	// to identify OAuth clients for rate limiting.
	MCPTrustedProxies []string
}
type authAttempt struct {
	count int
	reset time.Time
}

func Handler(c *Controller, cfg HTTPConfig) http.Handler {
	mux := http.NewServeMux()
	jsonResponse := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(w http.ResponseWriter, code int, err error) {
		jsonResponse(w, code, map[string]string{"error": err.Error()})
	}
	sameOrigin := func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		return err == nil && u.Host == r.Host && (u.Scheme == "http" || u.Scheme == "https")
	}
	sign := func(expiry string) string {
		m := hmac.New(sha256.New, []byte(cfg.Token))
		m.Write([]byte("zela-session:" + expiry))
		return hex.EncodeToString(m.Sum(nil))
	}
	authenticated := func(r *http.Request) (principal, bool) {
		if len(r.Header.Values("Authorization")) > 0 {
			return c.bearerPrincipal(r, cfg.Token)
		}
		cookie, err := r.Cookie("zela_session")
		if err != nil {
			return principal{}, false
		}
		expiry, sig, ok := strings.Cut(cookie.Value, ".")
		if !ok {
			return principal{}, false
		}
		exp, err := strconv.ParseInt(expiry, 10, 64)
		return principal{Admin: true}, len(cfg.Token) >= 32 && err == nil && time.Now().Unix() < exp && subtle.ConstantTimeCompare([]byte(sig), []byte(sign(expiry))) == 1
	}
	api := func(pattern string, f http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			p, ok := authenticated(r)
			if !ok {
				fail(w, 401, fmt.Errorf("sign in with your control-plane token"))
				return
			}
			if r.Method != "GET" && !sameOrigin(r) {
				fail(w, 403, fmt.Errorf("cross-origin request rejected"))
				return
			}
			if !c.deploymentAPIAccess(pattern, r, p) {
				fail(w, 403, fmt.Errorf("integration token cannot access this resource"))
				return
			}
			r, cleanup, ok := c.tokenRequest(r, p)
			if !ok {
				fail(w, 401, fmt.Errorf("token expired or revoked"))
				return
			}
			defer cleanup()
			f(w, r)
		})
	}
	operation := func(w http.ResponseWriter, op Operation, err error) {
		if err != nil {
			code := 400
			if strings.Contains(err.Error(), "another cluster operation") {
				code = 409
			}
			fail(w, code, err)
			return
		}
		jsonResponse(w, 202, op)
	}
	decode := func(w http.ResponseWriter, r *http.Request, v any) bool {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if err := d.Decode(v); err != nil {
			fail(w, 400, fmt.Errorf("invalid request: %w", err))
			return false
		}
		if err := d.Decode(&struct{}{}); err != io.EOF {
			fail(w, 400, fmt.Errorf("request must contain one JSON object"))
			return false
		}
		return true
	}
	var attemptsMu sync.Mutex
	attempts := map[string]authAttempt{}
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]string{"status": "ok", "version": "0.4.0"})
	})
	mux.HandleFunc("GET /api/ready", func(w http.ResponseWriter, r *http.Request) {
		if c.ctx.Err() != nil || c.Store.CheckPersistence() != nil {
			jsonResponse(w, 503, map[string]bool{"ready": false})
			return
		}
		jsonResponse(w, 200, map[string]bool{"ready": true})
	})
	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			fail(w, 403, fmt.Errorf("cross-origin request rejected"))
			return
		}
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		attemptsMu.Lock()
		a := attempts[ip]
		if time.Now().After(a.reset) {
			a = authAttempt{reset: time.Now().Add(time.Minute)}
		}
		a.count++
		attempts[ip] = a
		for k, v := range attempts {
			if time.Now().After(v.reset) {
				delete(attempts, k)
			}
		}
		attemptsMu.Unlock()
		if a.count > 10 {
			fail(w, 429, fmt.Errorf("too many sign-in attempts; wait one minute"))
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		if !decode(w, r, &body) {
			return
		}
		if subtle.ConstantTimeCompare([]byte(body.Token), []byte(cfg.Token)) != 1 {
			fail(w, 401, fmt.Errorf("invalid control-plane token"))
			return
		}
		expiry := strconv.FormatInt(time.Now().Add(12*time.Hour).Unix(), 10)
		http.SetCookie(w, &http.Cookie{Name: "zela_session", Value: expiry + "." + sign(expiry), Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 12 * 60 * 60})
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
	api("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "zela_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
	api("POST /api/tokens", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name          string `json:"name"`
			Scope         string `json:"scope"`
			ExpiresInDays int    `json:"expiresInDays"`
		}
		if !decode(w, r, &body) {
			return
		}
		raw, meta, err := c.Store.createAccessToken(body.Name, body.ExpiresInDays, body.Scope)
		if err != nil {
			fail(w, 400, err)
			return
		}
		jsonResponse(w, 201, map[string]any{"token": raw, "metadata": meta})
	})
	api("GET /api/tokens", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, tokenMetadata(c.Store.snapshot().AccessTokens))
	})
	api("POST /api/tokens/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		if err := c.revokeAccessToken(r.PathValue("id")); err != nil {
			fail(w, 400, err)
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
	api("GET /api/deployments", func(w http.ResponseWriter, r *http.Request) {
		out := map[string]Deployment{}
		p := requestPrincipal(r)
		for id, d := range c.Store.snapshot().Deployments {
			if p.Admin || d.AccessTokenID == p.TokenID {
				out[id] = d
			}
		}
		jsonResponse(w, 200, out)
	})
	api("GET /api/deployments/{id}", func(w http.ResponseWriter, r *http.Request) {
		d, ok := c.Store.snapshot().Deployments[r.PathValue("id")]
		if !ok {
			fail(w, 404, fmt.Errorf("deployment not found"))
			return
		}
		jsonResponse(w, 200, d)
	})
	api("GET /api/operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		for _, op := range c.Store.snapshot().Operations {
			if op.ID == r.PathValue("id") {
				jsonResponse(w, 200, op)
				return
			}
		}
		fail(w, 404, fmt.Errorf("operation not found"))
	})

	api("GET /api/cluster", func(w http.ResponseWriter, r *http.Request) {
		view := infrastructureSnapshot(c.Store.snapshot(), time.Now().UTC())
		if c.Store.haStatus != nil {
			status := c.Store.haStatus()
			view.HighAvailability = &status
		}
		jsonResponse(w, 200, view)
	})
	api("GET /api/cluster/workers", func(w http.ResponseWriter, r *http.Request) {
		view := infrastructureSnapshot(c.Store.snapshot(), time.Now().UTC())
		jsonResponse(w, 200, map[string]any{"observedAt": view.ObservedAt, "workers": view.Workers})
	})
	api("GET /api/cluster/workers/{id}", func(w http.ResponseWriter, r *http.Request) {
		s := c.Store.snapshot()
		worker, ok := s.Workers[r.PathValue("id")]
		if !ok {
			fail(w, 404, fmt.Errorf("worker not found"))
			return
		}
		jsonResponse(w, 200, infrastructureWorker(s, worker, time.Now().UTC()))
	})
	api("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		state := c.Store.public()
		state.MCP = &MCPStatus{Endpoint: strings.TrimSuffix(cfg.MCPPublicURL, "/") + "/mcp", OAuthEnabled: cfg.MCPPublicURL != ""}
		state.ProvisioningAvailable = c.Provisioner != nil || state.Demo
		jsonResponse(w, 200, state)
	})
	api("GET /api/workers/{id}/metrics/history", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, ok := c.Store.snapshot().Workers[id]; !ok {
			fail(w, 404, fmt.Errorf("worker not found"))
			return
		}
		window := r.URL.Query().Get("window")
		duration := time.Hour
		switch window {
		case "", "1h":
		case "6h":
			duration = 6 * time.Hour
		case "24h":
			duration = 24 * time.Hour
		default:
			fail(w, 400, fmt.Errorf("window must be 1h, 6h, or 24h"))
			return
		}
		jsonResponse(w, 200, map[string]any{"points": c.Store.Monitor.History(id, time.Now().Add(-duration)), "sampleIntervalSeconds": 60})
	})
	api("GET /api/notifications", func(w http.ResponseWriter, r *http.Request) {
		items, unread := c.Store.Monitor.Notifications()
		jsonResponse(w, 200, map[string]any{"notifications": items, "unread": unread})
	})
	api("POST /api/notifications/read", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs []string `json:"ids"`
		}
		if !decode(w, r, &body) {
			return
		}
		if err := c.Store.Monitor.ReadNotifications(body.IDs, time.Now().UTC()); err != nil {
			fail(w, 400, err)
			return
		}
		items, unread := c.Store.Monitor.Notifications()
		jsonResponse(w, 200, map[string]any{"notifications": items, "unread": unread})
	})

	api("POST /api/monitoring/rules", func(w http.ResponseWriter, r *http.Request) {
		var rules AlertRules
		if !decode(w, r, &rules) {
			return
		}
		if err := c.Store.Monitor.SetRules(rules); err != nil {
			fail(w, 400, err)
			return
		}
		jsonResponse(w, 200, rules)
	})
	api("POST /api/alerts/{id}/acknowledge", func(w http.ResponseWriter, r *http.Request) {
		if err := c.Store.Monitor.Acknowledge(r.PathValue("id")); err != nil {
			fail(w, 404, err)
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
	api("POST /api/refresh", func(w http.ResponseWriter, r *http.Request) {
		op, err := c.Submit("Refresh cluster inventory", nil, func(_ctx context.Context) error { c.Refresh(_ctx); return nil })
		operation(w, op, err)
	})
	api("POST /api/secrets", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name       string `json:"name"`
			PrivateKey string `json:"privateKey"`
			Passphrase string `json:"passphrase"`
		}
		if !decode(w, r, &body) {
			return
		}
		meta, err := c.CreateSSHSecret(r.Context(), body.Name, sshclient.Identity{PrivateKey: body.PrivateKey, Passphrase: body.Passphrase})
		if err != nil {
			fail(w, 400, err)
			return
		}
		jsonResponse(w, 201, meta)
	})
	api("POST /api/secrets/{id}/delete", func(w http.ResponseWriter, r *http.Request) {
		if err := c.DeleteSSHSecret(r.PathValue("id")); err != nil {
			fail(w, 400, err)
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
	api("POST /api/workers/provision", func(w http.ResponseWriter, r *http.Request) {
		var req ProvisionRequest
		if !decode(w, r, &req) {
			return
		}
		// A management credential must not attach a host of its choosing: that
		// host would then receive workloads and their environment values.
		if p := requestPrincipal(r); mcpScope(p.Scope) && !mcpHostAllowed(req.SSH.Host, cfg.MCPProvisionHosts) {
			fail(w, 403, fmt.Errorf("MCP provisioning is limited to ZELA_MCP_PROVISION_HOSTS; ask an administrator to provision this host"))
			return
		}
		op, err := c.ProvisionWorker(req)
		operation(w, op, err)
	})
	api("POST /api/workers/{id}/provision-retry", func(w http.ResponseWriter, r *http.Request) {
		op, err := c.RetryProvision(r.PathValue("id"))
		operation(w, op, err)
	})
	api("POST /api/workers/{id}/agent-update", func(w http.ResponseWriter, r *http.Request) {
		op, err := c.UpgradeWorker(r.PathValue("id"))
		operation(w, op, err)
	})

	api("POST /api/workers", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name     string `json:"name"`
			Endpoint string `json:"endpoint"`
			Token    string `json:"token"`
			Region   string `json:"region"`
			// Optional address peers use for the cluster network.
			NetworkEndpoint string `json:"networkEndpoint"`
		}
		if !decode(w, r, &body) {
			return
		}
		if !validNetworkEndpoint(body.NetworkEndpoint) {
			fail(w, 400, fmt.Errorf("networkEndpoint must be a hostname or IP address"))
			return
		}
		if !cfg.AllowHTTPWorkers && !c.Store.snapshot().Demo {
			u, _ := url.Parse(body.Endpoint)
			if u != nil && u.Scheme == "http" {
				ip := net.ParseIP(u.Hostname())
				if ip == nil || !ip.IsLoopback() {
					fail(w, 400, fmt.Errorf("remote agents require HTTPS; --allow-http-workers explicitly permits private-network HTTP"))
					return
				}
			}
		}
		op, err := c.AddWorker(Worker{Name: body.Name, Endpoint: body.Endpoint, Token: body.Token, Region: body.Region, NetworkEndpoint: body.NetworkEndpoint})
		operation(w, op, err)
	})
	api("POST /api/workers/{id}/actions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Action string `json:"action"`
		}
		if !decode(w, r, &body) {
			return
		}
		op, err := c.WorkerAction(r.PathValue("id"), body.Action)
		operation(w, op, err)
	})
	api("POST /api/deployments", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name      string        `json:"name"`
			Image     string        `json:"image"`
			Replicas  int           `json:"replicas"`
			WorkerIDs []string      `json:"workerIDs"`
			Env       []string      `json:"env"`
			Command   []string      `json:"command"`
			Ports     []engine.Port `json:"ports"`
			MemoryMB  int64         `json:"memoryMB"`
			CPUs      float64       `json:"cpus"`
			Routes    []Route       `json:"routes"`
		}
		if !decode(w, r, &body) {
			return
		}
		op, err := c.CreateDeployment(Deployment{AccessTokenID: requestPrincipal(r).TokenID, Name: body.Name, Image: body.Image, Replicas: body.Replicas, WorkerIDs: body.WorkerIDs, Env: body.Env, Command: body.Command, Ports: body.Ports, MemoryMB: body.MemoryMB, CPUs: body.CPUs, Routes: body.Routes})
		operation(w, op, err)
	})
	api("POST /api/deployments/{id}/actions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Action   string `json:"action"`
			Target   string `json:"target"`
			Replicas int    `json:"replicas"`
		}
		if !decode(w, r, &body) {
			return
		}
		op, err := c.DeploymentAction(r.PathValue("id"), body.Action, body.Target, body.Replicas, requestPrincipal(r).TokenID)
		operation(w, op, err)
	})
	api("POST /api/deployments/{id}/replicas/{replica}/move", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Target string `json:"target"`
		}
		if !decode(w, r, &body) {
			return
		}
		op, err := c.MoveReplica(r.PathValue("id"), r.PathValue("replica"), body.Target, requestPrincipal(r).TokenID)
		operation(w, op, err)
	})
	api("POST /api/deployments/{id}/image", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Image  string `json:"image"`
			Target string `json:"target"`
		}
		if !decode(w, r, &body) {
			return
		}
		op, err := c.UpdateImage(r.PathValue("id"), body.Image, body.Target, requestPrincipal(r).TokenID)
		operation(w, op, err)
	})

	api("PUT /api/deployments/{id}/routes", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Routes []Route `json:"routes"`
		}
		if !decode(w, r, &body) {
			return
		}
		d, err := c.SetRoutes(r.PathValue("id"), body.Routes)
		if err != nil {
			fail(w, 400, err)
			return
		}
		jsonResponse(w, 200, d)
	})
	api("POST /api/workers/{worker}/containers/{id}/actions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Action string `json:"action"`
		}
		if !decode(w, r, &body) {
			return
		}
		op, err := c.ContainerAction(r.PathValue("worker"), r.PathValue("id"), body.Action)
		operation(w, op, err)
	})
	api("GET /api/workers/{worker}/containers/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		state := c.Store.snapshot()
		worker, ok := state.Workers[r.PathValue("worker")]
		found := false
		if ok {
			for _, ct := range worker.Containers {
				if ct.ID == r.PathValue("id") {
					found = true
				}
			}
		}
		if !found {
			fail(w, 404, fmt.Errorf("container not found"))
			return
		}
		e, _, err := c.worker(r.Context(), worker.ID)
		if err != nil {
			fail(w, 502, err)
			return
		}
		logs, err := e.Logs(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, 502, err)
			return
		}
		jsonResponse(w, 200, map[string]string{"logs": logs})
	})
	c.registerMCP(mux, cfg, api, authenticated, sameOrigin)
	files, _ := fs.Sub(assets, "web")
	mux.Handle("GET /", http.FileServer(http.FS(files)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		// Dispatch before ServeMux, which canonicalizes paths and could alter a
		// Docker request or redirect it before gateway authentication.
		if dockerapi.IsGatewayRequest(r.URL.Path) {
			c.dockerGateway(cfg.Token, w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
