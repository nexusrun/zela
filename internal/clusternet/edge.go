package clusternet

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// edgeServer is the worker's external HTTP(S) entry point. It matches the
// request host and longest path prefix, then proxies to a service VIP, whose
// DNAT rules pick a healthy replica. Any worker can receive any route.
type edgeServer struct {
	mu                sync.RWMutex
	routes            []EdgeRoute
	challenges        map[string]string
	certs             []*tls.Certificate // pushed (ACME) certificates, leaf parsed
	local             *tls.Certificate   // --edge-tls-cert, reloaded on each apply
	redirect          bool
	http              *http.Server
	https             *http.Server
	httpPort          int
	httpsPort         int
	certFile          string
	keyFile           string
	listen            func(network, address string) (net.Listener, error)
	transport         http.RoundTripper
	generation        uint64
	active            map[uint64]int
	drained           chan struct{}
	serviceGeneration string
	retired           map[uint64]*http.Transport
	drainTimeout      time.Duration
}

func newEdgeServer(certFile, keyFile string) *edgeServer {
	return &edgeServer{certFile: certFile, keyFile: keyFile, listen: net.Listen, generation: 1, active: map[uint64]int{}, drained: make(chan struct{}), transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}}
}

// sortRoutes orders exact hosts before wildcards before catch-all, and longer
// path prefixes first, so the first match is the most specific.
func sortRoutes(routes []EdgeRoute) []EdgeRoute {
	out := append([]EdgeRoute(nil), routes...)
	rank := func(h string) int {
		switch {
		case h == "":
			return 2
		case strings.HasPrefix(h, "*."):
			return 1
		}
		return 0
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := rank(out[i].Host), rank(out[j].Host); a != b {
			return a < b
		}
		return len(out[i].PathPrefix) > len(out[j].PathPrefix)
	})
	return out
}

func hostMatches(pattern, host string) bool {
	switch {
	case pattern == "":
		return true
	case strings.HasPrefix(pattern, "*."):
		suffix := pattern[1:] // ".example.com"
		return strings.HasSuffix(host, suffix) && !strings.Contains(strings.TrimSuffix(host, suffix), ".")
	}
	return host == pattern
}

func pathMatches(prefix, path string) bool {
	if prefix == "/" {
		return true
	}
	p := strings.TrimSuffix(prefix, "/")
	return path == p || strings.HasPrefix(path, p+"/")
}

func (e *edgeServer) match(r *http.Request) (EdgeRoute, bool, func()) {
	rt, _, ok, done := e.acquireRoute(r)
	return rt, ok, done
}
func (e *edgeServer) acquireRoute(r *http.Request) (EdgeRoute, http.RoundTripper, bool, func()) {
	host := requestHost(r)
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, rt := range e.routes {
		if hostMatches(strings.ToLower(rt.Host), host) && pathMatches(rt.PathPrefix, r.URL.Path) {
			generation := e.generation
			e.active[generation]++
			return rt, e.transport, true, func() {
				e.mu.Lock()
				e.active[generation]--
				if generation < e.generation && e.active[generation] == 0 {
					if transport := e.retired[generation]; transport != nil {
						transport.CloseIdleConnections()
						delete(e.retired, generation)
					}
					delete(e.active, generation)
				}
				close(e.drained)
				e.drained = make(chan struct{})
				e.mu.Unlock()
			}
		}
	}
	return EdgeRoute{}, nil, false, nil
}

// certificateFor picks a pushed certificate valid for the SNI name, then the
// local certificate. Clients without SNI get the local or first certificate.
func (e *edgeServer) certificateFor(name string) *tls.Certificate {
	e.mu.RLock()
	defer e.mu.RUnlock()
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if name != "" {
		for _, c := range e.certs {
			if c.Leaf != nil && c.Leaf.VerifyHostname(name) == nil {
				return c
			}
		}
		if e.local != nil && e.local.Leaf != nil && e.local.Leaf.VerifyHostname(name) == nil {
			return e.local
		}
	}
	if e.local != nil {
		return e.local
	}
	if len(e.certs) > 0 && name == "" {
		return e.certs[0]
	}
	return nil
}

func (e *edgeServer) hasCertificate(host string) bool {
	c := e.certificateFor(host)
	return c != nil && c.Leaf != nil && c.Leaf.VerifyHostname(host) == nil
}

func requestHost(r *http.Request) string {
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(host, ".")
}

func (e *edgeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// ACME HTTP-01: the CA may reach any worker, so every worker answers.
	if token, ok := strings.CutPrefix(r.URL.Path, "/.well-known/acme-challenge/"); ok && r.TLS == nil {
		e.mu.RLock()
		auth, found := e.challenges[token]
		e.mu.RUnlock()
		if found {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, auth)
			return
		}
	}
	e.mu.RLock()
	redirect, httpsPort := e.redirect, e.httpsPort
	e.mu.RUnlock()
	if redirect && r.TLS == nil && httpsPort > 0 && e.hasCertificate(requestHost(r)) {
		target := "https://" + requestHost(r)
		if httpsPort != 443 {
			target += fmt.Sprintf(":%d", httpsPort)
		}
		http.Redirect(w, r, target+r.URL.RequestURI(), http.StatusPermanentRedirect)
		return
	}
	rt, transport, ok, done := e.acquireRoute(r)
	if !ok {
		http.Error(w, "no route for this host and path", http.StatusNotFound)
		return
	}
	defer done()
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	proxy := &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1, // stream responses (SSE, long polling) immediately
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&url.URL{Scheme: "http", Host: rt.Target})
			pr.Out.Host = pr.In.Host // preserve the public host for virtual hosting
			pr.SetXForwarded()
			pr.Out.Header.Set("X-Forwarded-Proto", scheme)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("edge: %s%s -> %s: %v", r.Host, r.URL.Path, rt.Target, err)
			http.Error(w, "service unavailable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// update applies routes and certificates, and manages each listener
// independently: a listener is (re)started only when its port changed or it is
// not running, so a failing HTTPS setup never interrupts healthy HTTP.
// Certificates are chosen per handshake, so renewals need no restart.
func (e *edgeServer) update(cfg *Edge) error {
	if cfg == nil {
		e.stop()
		return nil
	}
	var errs []error
	var local *tls.Certificate
	if e.certFile != "" && e.keyFile != "" {
		if c, err := tls.LoadX509KeyPair(e.certFile, e.keyFile); err != nil {
			errs = append(errs, fmt.Errorf("load edge TLS certificate: %w", err))
		} else {
			local = &c
		}
	}
	var pushed []*tls.Certificate
	for _, pc := range cfg.Certificates {
		c, err := tls.X509KeyPair([]byte(pc.CertPEM), []byte(pc.KeyPEM))
		if err != nil {
			errs = append(errs, fmt.Errorf("invalid pushed edge certificate: %w", err))
			continue
		}
		pushed = append(pushed, &c)
	}
	if cfg.DrainTimeoutSeconds < 0 || cfg.DrainTimeoutSeconds > 600 {
		return fmt.Errorf("edge drain timeout must be between 0 and 600 seconds")
	}
	newRoutes := sortRoutes(cfg.Routes)
	e.mu.Lock()
	routesChanged := !reflect.DeepEqual(e.routes, newRoutes) || e.serviceGeneration != cfg.ServiceGeneration
	if routesChanged {
		if transport, ok := e.transport.(*http.Transport); ok {
			if e.retired == nil {
				e.retired = map[uint64]*http.Transport{}
			}
			if e.active[e.generation] > 0 {
				e.retired[e.generation] = transport
			}
			e.transport = transport.Clone()
			transport.CloseIdleConnections()
		}
		e.generation++
	}
	e.routes = newRoutes
	e.serviceGeneration = cfg.ServiceGeneration
	e.challenges = cfg.Challenges
	e.certs, e.local, e.redirect = pushed, local, cfg.RedirectHTTPS
	e.mu.Unlock()
	if err := e.ensure(&e.http, &e.httpPort, cfg.HTTPPort, nil); err != nil {
		errs = append(errs, err)
	}
	httpsPort := cfg.HTTPSPort
	if httpsPort > 0 && local == nil && len(pushed) == 0 {
		errs = append(errs, fmt.Errorf("edge HTTPS has no certificate yet: set --edge-tls-cert/--edge-tls-key on this worker, or enable ACME on the control plane"))
		httpsPort = 0
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}, GetCertificate: func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if c := e.certificateFor(h.ServerName); c != nil {
			return c, nil
		}
		return nil, fmt.Errorf("no edge certificate for %q", h.ServerName)
	}}
	if err := e.ensure(&e.https, &e.httpsPort, httpsPort, tlsConfig); err != nil {
		errs = append(errs, err)
	}
	{
		timeout := e.drainTimeout
		if cfg.DrainTimeoutSeconds > 0 {
			timeout = time.Duration(cfg.DrainTimeoutSeconds) * time.Second
		}
		if timeout == 0 {
			timeout = 60 * time.Second
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			e.mu.RLock()
			active := 0
			for generation, count := range e.active {
				if generation < e.generation {
					active += count
				}
			}
			changed := e.drained
			e.mu.RUnlock()
			if active == 0 {
				break
			}
			select {
			case <-changed:
			case <-timer.C:
				errs = append(errs, fmt.Errorf("edge requests from the previous route generation did not drain within %s", timeout))
				return errors.Join(errs...)
			}
		}
	}
	return errors.Join(errs...)
}

// ensure makes *srv listen on port (0 = stopped).
func (e *edgeServer) ensure(srv **http.Server, current *int, port int, tlsConfig *tls.Config) error {
	e.mu.Lock()
	running := *srv
	same := running != nil && *current == port
	if !same {
		*srv, *current = nil, 0
	}
	e.mu.Unlock()
	if same {
		return nil
	}
	if running != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = running.Shutdown(ctx)
		cancel()
	}
	if port == 0 {
		return nil
	}
	next, err := e.serve(port, tlsConfig)
	if err != nil {
		return err
	}
	e.mu.Lock()
	*srv, *current = next, port
	e.mu.Unlock()
	return nil
}

func (e *edgeServer) serve(port int, tlsConfig *tls.Config) (*http.Server, error) {
	l, err := e.listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, fmt.Errorf("edge listen on port %d (is CAP_NET_BIND_SERVICE granted and the port free?): %w", port, err)
	}
	// No write timeout: proxied streams and WebSockets may be long-lived.
	srv := &http.Server{Handler: e, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second, TLSConfig: tlsConfig, MaxHeaderBytes: 1 << 20}
	go func() {
		var err error
		if tlsConfig != nil {
			err = srv.ServeTLS(l, "", "")
		} else {
			err = srv.Serve(l)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("edge: %v", err)
		}
	}()
	return srv, nil
}

func (e *edgeServer) stop() {
	e.mu.Lock()
	servers := []*http.Server{e.http, e.https}
	e.http, e.https, e.httpPort, e.httpsPort = nil, nil, 0, 0
	e.mu.Unlock()
	for _, s := range servers {
		if s != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.Shutdown(ctx)
			cancel()
		}
	}
}
