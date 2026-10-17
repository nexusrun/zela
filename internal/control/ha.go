package control

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zela-dev/zela/internal/engine"
)

type HAStatus struct {
	Enabled   bool      `json:"enabled"`
	ManagerID string    `json:"managerID"`
	Role      string    `json:"role"`
	Ready     bool      `json:"ready"`
	Since     time.Time `json:"since"`
}

type HAConfig struct {
	DataDir       string
	DatabaseURL   string
	ManagerID     string
	HTTP          HTTPConfig
	TakeoverDelay time.Duration
	Configure     func(*Controller) error
}

type haGeneration struct {
	controller *Controller
	handler    http.Handler
	requests   sync.WaitGroup
	verified   atomic.Int64
}

// HAManager exposes one local listener throughout elections. Only a lock-owning,
// healthy generation serves cluster requests; standby nodes only serve probes.
type HAManager struct {
	cfg                                                           HAConfig
	configurationHash                                             string
	ctx                                                           context.Context
	cancel                                                        context.CancelFunc
	once                                                          sync.Once
	wg                                                            sync.WaitGroup
	mu                                                            sync.Mutex
	status                                                        HAStatus
	generation                                                    *haGeneration
	openDatabase                                                  func(context.Context) (*postgresStore, error)
	retryInterval, heartbeatInterval, checkTimeout, takeoverDelay time.Duration
}

func NewHAManager(cfg HAConfig) (*HAManager, error) {
	if err := ValidateMCPConfig(cfg.HTTP); err != nil {
		return nil, err
	}
	if cfg.DataDir == "" || cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("HA requires --data and ZELA_DATABASE_URL; JSON storage cannot coordinate managers")
	}
	if !validName.MatchString(cfg.ManagerID) {
		return nil, fmt.Errorf("HA requires a manager ID of 1–63 letters, numbers, dots, underscores or hyphens")
	}
	if len(cfg.HTTP.Token) < 32 {
		return nil, fmt.Errorf("HA requires the same ZELA_ADMIN_TOKEN of at least 32 characters on every manager")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("ZELA_VAULT_KEY")))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("HA requires the same base64-encoded 32-byte ZELA_VAULT_KEY on every manager")
	}
	defer clear(key)
	if _, err := pgx.ParseConfig(cfg.DatabaseURL); err != nil {
		return nil, fmt.Errorf("invalid ZELA_DATABASE_URL")
	}
	if cfg.TakeoverDelay == 0 {
		cfg.TakeoverDelay = 10 * time.Second
	}
	if cfg.TakeoverDelay < 5*time.Second || cfg.TakeoverDelay > time.Minute {
		return nil, fmt.Errorf("HA takeover delay must be between 5 seconds and one minute")
	}
	digest := sha256.New()
	digest.Write([]byte("zela-ha-v1\x00"))
	digest.Write([]byte(cfg.HTTP.Token))
	digest.Write([]byte{0})
	digest.Write(key)
	ctx, cancel := context.WithCancel(context.Background())
	h := &HAManager{cfg: cfg, configurationHash: hex.EncodeToString(digest.Sum(nil)), ctx: ctx, cancel: cancel,
		status:        HAStatus{Enabled: true, ManagerID: cfg.ManagerID, Role: "standby", Since: time.Now().UTC()},
		retryInterval: 2 * time.Second, heartbeatInterval: 2 * time.Second, checkTimeout: 3 * time.Second, takeoverDelay: cfg.TakeoverDelay}
	h.openDatabase = func(ctx context.Context) (*postgresStore, error) { return openPostgresHAContext(ctx, cfg.DatabaseURL) }
	return h, nil
}

func validateHAWorker(w Worker) error {
	var host string
	if w.SSH != nil {
		host = w.SSH.Host
	} else {
		u, err := url.Parse(strings.TrimSpace(w.Endpoint))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
			return fmt.Errorf("HA workers require a remote HTTPS agent or a remote SSH-managed agent; local Docker sockets are not supported")
		}
		host = u.Hostname()
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	ip := net.ParseIP(host)
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || (ip != nil && (ip.IsLoopback() || ip.IsUnspecified())) {
		return fmt.Errorf("HA worker addresses must be reachable from every manager; loopback and unspecified hosts are not supported")
	}
	return nil
}

func (h *HAManager) validateState(s *State) error {
	if s.HAConfigurationHash != "" && subtle.ConstantTimeCompare([]byte(s.HAConfigurationHash), []byte(h.configurationHash)) != 1 {
		return fmt.Errorf("HA shared administrator token or vault key does not match this cluster")
	}
	for _, w := range s.Workers {
		if err := validateHAWorker(w); err != nil {
			return err
		}
	}
	s.HAConfigurationHash = h.configurationHash
	return nil
}

func (h *HAManager) Start(parent context.Context) {
	h.once.Do(func() {
		h.wg.Add(1)
		go func() { defer h.wg.Done(); stop := context.AfterFunc(parent, h.cancel); defer stop(); h.run() }()
	})
}
func (h *HAManager) Close() { h.cancel(); h.wg.Wait() }

func (h *HAManager) readyLocked() bool {
	g := h.generation
	return h.status.Role == "leader" && g != nil && g.controller.ctx.Err() == nil && !g.controller.Store.database.fenced() &&
		time.Since(time.Unix(0, g.verified.Load())) <= h.heartbeatInterval+h.checkTimeout
}
func (h *HAManager) Status() HAStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	status := h.status
	status.Ready = h.readyLocked()
	if status.Role == "leader" && !status.Ready {
		status.Role = "fenced"
	}
	return status
}
func (h *HAManager) setRole(role string) {
	h.mu.Lock()
	h.status.Role = role
	h.status.Since = time.Now().UTC()
	h.mu.Unlock()
	log.Printf("HA manager %s: %s", h.cfg.ManagerID, role)
}
func (h *HAManager) waitRetry() bool {
	timer := time.NewTimer(h.retryInterval)
	defer timer.Stop()
	select {
	case <-h.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func (h *HAManager) heartbeat(g *haGeneration, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(h.heartbeatInterval)
	defer ticker.Stop()
	db := g.controller.Store.database
	for {
		ctx, cancel := context.WithTimeout(g.controller.ctx, h.checkTimeout)
		err := db.checkContext(ctx)
		cancel()
		if err == nil {
			g.verified.Store(time.Now().UnixNano())
		} else if db.fenced() || g.controller.ctx.Err() != nil {
			return
		} else if time.Since(time.Unix(0, g.verified.Load())) > h.heartbeatInterval+h.checkTimeout {
			// Transient failures are tolerated only within the readiness window.
			db.fence()
			return
		}
		select {
		case <-g.controller.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type haWorkerTransport struct {
	base  http.RoundTripper
	check func() error
	epoch int64
}

// LeaderEpochHeader carries the HA leadership epoch; workers reject epochs lower
// than the highest they have seen, fencing a stalled former leader.
const LeaderEpochHeader = "X-Zela-Leader-Epoch"

func (t *haWorkerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	if t.epoch > 0 {
		r = r.Clone(r.Context())
		r.Header.Set(LeaderEpochHeader, strconv.FormatInt(t.epoch, 10))
	}
	return t.base.RoundTrip(r)
}
func (t *haWorkerTransport) CloseIdleConnections() {
	if t.base == dockerGatewayTransport || t.base == http.DefaultTransport {
		return
	}
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
func (h *HAManager) checkLeader(c *Controller) error {
	ctx, cancel := context.WithTimeout(c.ctx, h.checkTimeout)
	defer cancel()
	return c.Store.database.checkContext(ctx)
}
func (h *HAManager) guardWorkers(c *Controller) {
	factory := c.Factory
	c.Factory = func(w Worker) (engine.Engine, error) {
		if err := validateHAWorker(w); err != nil {
			return nil, err
		}
		if err := h.checkLeader(c); err != nil {
			return nil, err
		}
		e, err := factory(w)
		if err != nil {
			return nil, err
		}
		original, ok := e.(*engine.Client)
		if !ok || original.HTTP == nil {
			return nil, fmt.Errorf("HA requires remote worker HTTP transports")
		}
		client := *original
		httpClient := *original.HTTP
		transport := httpClient.Transport
		if transport == nil {
			transport = dockerGatewayTransport
		}
		httpClient.Transport = &haWorkerTransport{base: transport, check: func() error { return h.checkLeader(c) }, epoch: c.Store.database.epoch}
		client.HTTP = &httpClient
		return &client, nil
	}
}

func (h *HAManager) startup(g *haGeneration, done chan<- struct{}) {
	defer close(done)
	timer := time.NewTimer(h.takeoverDelay)
	defer timer.Stop()
	select {
	case <-g.controller.ctx.Done():
		return
	case <-g.controller.Store.database.lost:
		return
	case <-timer.C:
	}
	g.controller.Refresh(g.controller.ctx)
}
func (h *HAManager) run() {
	defer h.setRole("stopped")
	for h.ctx.Err() == nil {
		s, err := openStore(h.cfg.DataDir, false, func() (*postgresStore, error) { return h.openDatabase(h.ctx) }, h.validateState)
		if err != nil {
			if !errors.Is(err, ErrClusterOwned) && h.ctx.Err() == nil {
				log.Printf("HA manager %s: initialization failed: %v", h.cfg.ManagerID, err)
			}
			if !h.waitRetry() {
				return
			}
			continue
		}
		c := New(s, nil)
		if h.cfg.Configure != nil {
			if err := h.cfg.Configure(c); err != nil {
				c.Close()
				log.Printf("HA manager %s: controller configuration failed", h.cfg.ManagerID)
				if !h.waitRetry() {
					return
				}
				continue
			}
		}
		h.guardWorkers(c)
		s.haStatus = h.Status
		g := &haGeneration{controller: c, handler: Handler(c, h.cfg.HTTP)}
		stop := context.AfterFunc(h.ctx, c.cancel)
		h.mu.Lock()
		h.generation = g
		h.mu.Unlock()
		h.setRole("promoting")
		heartbeatDone, startupDone := make(chan struct{}), make(chan struct{})
		go h.heartbeat(g, heartbeatDone)
		c.wg.Add(1)
		go func() { defer c.wg.Done(); h.startup(g, startupDone) }()
		select {
		case <-h.ctx.Done():
		case <-s.database.lost:
		case <-startupDone:
			if c.ctx.Err() == nil && !s.database.fenced() && h.checkLeader(c) == nil {
				g.verified.Store(time.Now().UnixNano())
				h.setRole("leader")
				c.StartPolling()
				select {
				case <-h.ctx.Done():
				case <-s.database.lost:
				}
			}
		}
		// Withdraw readiness before canceling requests or releasing the session lock.
		h.mu.Lock()
		h.status.Role = "fenced"
		h.status.Since = time.Now().UTC()
		h.generation = nil
		h.mu.Unlock()
		s.database.fence()
		c.cancel()
		stop()
		g.requests.Wait()
		<-heartbeatDone
		c.Close() // Stops polling/jobs before releasing the advisory lock.
		if h.ctx.Err() != nil {
			return
		}
		h.setRole("standby")
		if !h.waitRetry() {
			return
		}
	}
}

func (h *HAManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	status := h.Status()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Zela-Manager", h.cfg.ManagerID)
	w.Header().Set("X-Zela-Manager-Role", status.Role)
	if (r.Method == "GET" || r.Method == "HEAD") && (r.URL.Path == "/api/health" || r.URL.Path == "/api/ready") {
		code := http.StatusOK
		if r.URL.Path == "/api/ready" && !status.Ready {
			code = http.StatusServiceUnavailable
		}
		dockerJSONResponse(w, code, map[string]any{"status": map[bool]string{true: "ready", false: "standby"}[status.Ready], "highAvailability": status})
		return
	}
	h.mu.Lock()
	if !h.readyLocked() {
		h.mu.Unlock()
		w.Header().Set("Retry-After", "2")
		dockerError(w, 503, "no active manager on this node; use the cluster load balancer and retry after failover")
		return
	}
	g := h.generation
	g.requests.Add(1)
	h.mu.Unlock()
	defer g.requests.Done()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(g.controller.ctx, cancel)
	defer stop()
	if err := h.checkLeader(g.controller); err != nil {
		dockerError(w, 503, "manager leadership lost; reconnect through the cluster endpoint")
		return
	}
	g.verified.Store(time.Now().UnixNano())
	if g.controller.ctx.Err() != nil {
		dockerError(w, 503, "manager leadership lost; reconnect through the cluster endpoint")
		return
	}
	// Cancellation must also unblock request bodies whose Docker route cleared
	// the normal HTTP deadline. Proxy streams and upgrades use this context too.
	ioDone := make(chan struct{})
	stopIO := context.AfterFunc(g.controller.ctx, func() {
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now())
		_ = rc.SetWriteDeadline(time.Now())
		close(ioDone)
	})
	defer func() {
		if !stopIO() {
			<-ioDone
		}
	}()

	g.handler.ServeHTTP(w, r.WithContext(ctx))
}
