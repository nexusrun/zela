package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zela-dev/zela/internal/clusternet"
	"github.com/zela-dev/zela/internal/engine"
)

// NetworkState is the cluster network's durable allocation. It contains only
// public keys and addresses; private keys never leave worker hosts.
type NetworkState struct {
	EdgeDrainTimeoutSeconds int                      `json:"edgeDrainTimeoutSeconds,omitempty"`
	ConsoleManaged          bool                     `json:"consoleManaged,omitempty"`
	Enabled                 bool                     `json:"enabled"`
	CIDR                    string                   `json:"cidr"`
	Port                    int                      `json:"port"`
	Members                 map[string]NetworkMember `json:"members"`
	// Services maps deployment IDs to stable virtual IPs.
	Services map[string]NetworkService `json:"services,omitempty"`
	// Edge proxy ports on every worker; zero disables a listener.
	EdgeHTTPPort  int `json:"edgeHTTPPort,omitempty"`
	EdgeHTTPSPort int `json:"edgeHTTPSPort,omitempty"`
	// EdgeRedirectHTTPS redirects HTTP to HTTPS for hosts with a certificate.
	EdgeRedirectHTTPS bool `json:"edgeRedirectHTTPS,omitempty"`
}
type NetworkService struct {
	Index int    `json:"index"`
	VIP   string `json:"vip"`
}
type NetworkMember struct {
	Index     int       `json:"index"`
	Address   string    `json:"address"`
	Subnet    string    `json:"subnet"`
	PublicKey string    `json:"publicKey,omitempty"`
	Applied   string    `json:"applied,omitempty"`
	Error     string    `json:"error,omitempty"`
	EdgeError string    `json:"edgeError,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

// Ready means the worker has applied a network configuration successfully,
// so containers placed there can join the cluster network.
func (m NetworkMember) Ready() bool { return m.Applied != "" && m.Error == "" }

// networkRequired reports whether managed containers must join the network.
func networkRequired(s State) bool { return s.Network != nil && s.Network.Enabled }

// SetClusterNetwork enables the network (cidr non-empty) or schedules its
// removal (cidr empty). Changing the prefix of an enabled network is refused:
// container addresses would silently change underneath running workloads.
func (c *Controller) SetClusterNetwork(cidr string, port int) error {
	if port == 0 {
		port = clusternet.DefaultPort
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("cluster network port must be 1–65535")
	}
	if cidr != "" {
		p, err := clusternet.ParseCluster(cidr)
		if err != nil {
			return err
		}
		cidr = p.String()
	}
	return c.Store.update(func(s *State) error {
		if s.Demo && cidr != "" {
			return fmt.Errorf("the cluster network is not available in demo mode")
		}
		n := s.Network
		if cidr == "" {
			if n != nil {
				n.Enabled = false // reconciliation tears it down on each worker
			}
			return nil
		}
		if n != nil && n.CIDR != cidr && len(n.Members) > 0 {
			return fmt.Errorf("cluster network %s is in use; disable it and let every worker tear it down before changing to %s", n.CIDR, cidr)
		}
		if n == nil || n.CIDR != cidr {
			n = &NetworkState{CIDR: cidr, Members: map[string]NetworkMember{}}
		}
		n.Enabled, n.Port = true, port
		if n.Members == nil {
			n.Members = map[string]NetworkMember{}
		}
		s.Network = n
		return nil
	})
}

// networkHost is the address peers use to reach a worker's WireGuard port.
func networkHost(w Worker) string {
	if w.NetworkEndpoint != "" {
		return w.NetworkEndpoint
	}
	if w.SSH != nil {
		return w.SSH.Host
	}
	u, err := url.Parse(w.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.Hostname()
}

// networkRecords derives DNS records and service VIPs from the latest
// inventory of online workers. <deployment>.zela resolves to the deployment's
// stable VIP (or, without one, to its replicas); <replica>.zela to one
// container. VIP backends are running replicas that are not unhealthy.
func networkRecords(s State) (map[string][]string, []clusternet.Service) {
	prefix, err := clusternet.ParseCluster(s.Network.CIDR)
	if err != nil {
		return nil, nil
	}
	var services []clusternet.Service
	records := map[string][]string{}
	add := func(label, ip string) {
		if label != "" && len(records[label]) < 64 {
			records[label] = append(records[label], ip)
		}
	}
	ids := make([]string, 0, len(s.Deployments))
	for id := range s.Deployments {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		d := s.Deployments[id]
		service := clusternet.DNSLabel(d.Name)
		svc, hasVIP := s.Network.Services[id]
		var backends []string
		for _, task := range d.Tasks {
			if task.Lifecycle != "" && task.Lifecycle != "serving" {
				continue
			}
			w, ok := s.Workers[task.WorkerID]
			if !ok || w.Status != "online" {
				continue
			}
			for _, ct := range w.Containers {
				if ct.ID != task.ID || ct.State != "running" {
					continue
				}
				ip := ct.NetworkIP(clusternet.DockerNetwork)
				if a, err := netip.ParseAddr(ip); err == nil && prefix.Contains(a) {
					if !hasVIP {
						add(service, ip)
					}
					if replica := clusternet.DNSLabel(task.Name); replica != service {
						add(replica, ip)
					}
					if !strings.Contains(ct.Status, "unhealthy") && len(backends) < 64 {
						backends = append(backends, ip)
					}
				}
			}
		}
		if hasVIP && service != "" {
			sort.Strings(backends)
			services = append(services, clusternet.Service{VIP: svc.VIP, Backends: backends})
			records[service] = []string{svc.VIP}
		}
	}
	for label := range records {
		sort.Strings(records[label])
	}
	return records, services
}

func (c *Controller) networkConfig(s State, id string) (clusternet.Config, error) {
	n := s.Network
	m := n.Members[id]
	records, services := networkRecords(s)
	edge := edgeConfig(s)
	if edge != nil {
		edge.RedirectHTTPS = n.EdgeRedirectHTTPS
		// Preserve the legacy wire format for stateless clusters that have
		// never requested a handover. Older agents reject unknown fields.
		for _, deployment := range s.Deployments {
			if deployment.Handover != nil || len(deployment.StorageAttachments) > 0 {
				serviceHash := sha256.Sum256([]byte(fmt.Sprint(services)))
				edge.ServiceGeneration = hex.EncodeToString(serviceHash[:])
				break
			}
		}
		c.acmeEdge(s, edge)
	}
	cfg := clusternet.Config{Cluster: n.CIDR, Address: m.Address, Subnet: m.Subnet, ListenPort: n.Port, Peers: []clusternet.Peer{}, Records: records, Services: services, Edge: edge}
	ids := make([]string, 0, len(n.Members))
	for pid := range n.Members {
		ids = append(ids, pid)
	}
	sort.Strings(ids)
	for _, pid := range ids {
		p := n.Members[pid]
		w, ok := s.Workers[pid]
		host := networkHost(w)
		if pid == id || !ok || p.PublicKey == "" || host == "" {
			continue
		}
		cfg.Peers = append(cfg.Peers, clusternet.Peer{PublicKey: p.PublicKey, Endpoint: net.JoinHostPort(host, fmt.Sprint(n.Port)), AllowedIPs: []string{clusternet.MeshHost(p.Address) + "/32", p.Subnet}})
	}
	return cfg, cfg.Validate()
}

// reconcileNetwork allocates members and pushes each online worker its
// configuration when it differs from what the worker last applied. Learning a
// new public key changes every peer's configuration, so a second pass follows.
func (c *Controller) reconcileNetwork(ctx context.Context) {
	s := c.Store.snapshot()
	if s.Network == nil || s.Demo {
		return
	}
	if err := c.Store.update(func(s *State) error {
		n := s.Network
		if n == nil {
			return nil
		}
		for id := range n.Members {
			if _, ok := s.Workers[id]; !ok {
				delete(n.Members, id) // unregistered; peers drop it on the next push
			}
		}
		if !n.Enabled {
			return nil
		}
		prefix, err := clusternet.ParseCluster(n.CIDR)
		if err != nil {
			return err
		}
		used := map[int]bool{}
		for _, m := range n.Members {
			used[m.Index] = true
		}
		ids := make([]string, 0, len(s.Workers))
		for id := range s.Workers {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			w := s.Workers[id]
			if _, ok := n.Members[id]; ok || w.Status != "online" || networkHost(w) == "" {
				continue
			}
			index := 1
			for used[index] {
				index++
			}
			if index > clusternet.MaxIndex {
				log.Printf("cluster network: no address space left for %s", w.Name)
				continue
			}
			address, subnet, _ := clusternet.Allocate(prefix, index)
			used[index] = true
			n.Members[id] = NetworkMember{Index: index, Address: address, Subnet: subnet}
		}
		// One stable VIP per deployment, released when the deployment is deleted.
		if n.Services == nil {
			n.Services = map[string]NetworkService{}
		}
		taken := map[int]bool{}
		for id, svc := range n.Services {
			if _, ok := s.Deployments[id]; !ok {
				delete(n.Services, id)
				continue
			}
			taken[svc.Index] = true
		}
		deployments := make([]string, 0, len(s.Deployments))
		for id := range s.Deployments {
			deployments = append(deployments, id)
		}
		sort.Strings(deployments)
		for _, id := range deployments {
			if _, ok := n.Services[id]; ok || clusternet.DNSLabel(s.Deployments[id].Name) == "" {
				continue
			}
			index := 1
			for taken[index] {
				index++
			}
			vip, err := clusternet.ServiceVIP(prefix, index)
			if err != nil {
				log.Printf("cluster network: no service VIPs left for %s", s.Deployments[id].Name)
				break
			}
			taken[index] = true
			n.Services[id] = NetworkService{Index: index, VIP: vip}
		}
		return nil
	}); err != nil {
		log.Printf("cluster network allocation: %v", err)
		return
	}
	for pass := 0; pass < 2; pass++ {
		if !c.pushNetwork(ctx) {
			break
		}
	}
	// A disabled network is forgotten once every member has torn down.
	_ = c.Store.update(func(s *State) error {
		if s.Network != nil && !s.Network.Enabled && len(s.Network.Members) == 0 {
			s.Network = nil
		}
		return nil
	})
}

const networkStatusRefresh = 5 * time.Minute

type networkResult struct {
	id     string
	status clusternet.Status
	err    error
	hash   string
	down   bool
}

// pushNetwork reports whether any worker's public key changed.
func (c *Controller) pushNetwork(ctx context.Context) bool {
	s := c.Store.snapshot()
	n := s.Network
	if n == nil {
		return false
	}
	var results []networkResult
	var mu sync.Mutex
	var wg sync.WaitGroup
	gate := make(chan struct{}, 8)
	for id, m := range n.Members {
		w, ok := s.Workers[id]
		if !ok || w.Status != "online" {
			continue
		}
		cfg := clusternet.Config{}
		var cfgErr error
		if n.Enabled {
			cfg, cfgErr = c.networkConfig(s, id)
		}
		hash := cfg.Hash()
		// Already applied; the agent repairs drift locally. A periodic push
		// still refreshes reported status (e.g. edge warnings) and restores an
		// agent that lost its saved configuration.
		if n.Enabled && cfgErr == nil && m.Applied == hash && m.Error == "" && m.PublicKey != "" && time.Since(m.UpdatedAt) < networkStatusRefresh {
			continue
		}
		wg.Add(1)
		go func(id string, w Worker, cfg clusternet.Config, cfgErr error, hash string) {
			defer wg.Done()
			gate <- struct{}{}
			defer func() { <-gate }()
			r := networkResult{id: id, hash: hash, down: !n.Enabled, err: cfgErr}
			if cfgErr == nil {
				r.status, r.err = c.applyNetwork(ctx, w, cfg)
			}
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}(id, w, cfg, cfgErr, hash)
	}
	wg.Wait()
	if len(results) == 0 {
		return false
	}
	keyChanged := false
	if err := c.Store.update(func(s *State) error {
		if s.Network == nil {
			return nil
		}
		for _, r := range results {
			m, ok := s.Network.Members[r.id]
			if !ok {
				continue
			}
			if r.down && r.err == nil && r.status.Error == "" {
				delete(s.Network.Members, r.id)
				continue
			}
			if k := r.status.PublicKey; len(k) == 44 && k != m.PublicKey {
				m.PublicKey, keyChanged = k, true
			}
			m.Applied, m.Error, m.EdgeError = "", "", r.status.EdgeError
			switch {
			case r.err != nil:
				m.Error = r.err.Error()
			case r.status.Error != "":
				m.Error = r.status.Error
			case r.status.Applied != r.hash:
				m.Error = "worker applied a different network configuration"
			default:
				m.Applied = r.hash
			}
			m.UpdatedAt = time.Now().UTC()
			s.Network.Members[r.id] = m
		}
		return nil
	}); err != nil {
		log.Printf("cluster network: %v", err)
	}
	return keyChanged
}

func (c *Controller) applyNetwork(ctx context.Context, w Worker, cfg clusternet.Config) (clusternet.Status, error) {
	e, err := c.Factory(w)
	if err != nil {
		return clusternet.Status{}, err
	}
	nc, ok := e.(engine.NetworkConfigurer)
	if !ok {
		return clusternet.Status{}, errors.New("this worker connection does not support the cluster network")
	}
	drain := 60
	if cfg.Edge != nil && cfg.Edge.DrainTimeoutSeconds > 0 {
		drain = cfg.Edge.DrainTimeoutSeconds
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(drain+40)*time.Second)
	defer cancel()
	return nc.ApplyNetwork(ctx, cfg)
}

var validHostname = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*\.?$`)

// validNetworkEndpoint accepts a host name or IP for peers to reach.
func validNetworkEndpoint(v string) bool {
	if v == "" {
		return true
	}
	if _, err := netip.ParseAddr(v); err == nil {
		return true
	}
	return validHostname.MatchString(v)
}

// validateRoutes normalizes routes in place: lowercase host, default path "/".
func validateRoutes(routes []Route) error {
	if len(routes) > 32 {
		return fmt.Errorf("a deployment can have at most 32 routes")
	}
	seen := map[string]bool{}
	for i, r := range routes {
		r.Host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.Host), "."))
		if r.Path == "" {
			r.Path = "/"
		}
		if !clusternet.ValidEdgeHost(r.Host) || !clusternet.ValidEdgePath(r.Path) || r.Port < 1 || r.Port > 65535 {
			return fmt.Errorf("route needs a valid host name (or none), an absolute path and a container port")
		}
		key := r.Host + r.Path
		if seen[key] {
			return fmt.Errorf("duplicate route %s%s", r.Host, r.Path)
		}
		seen[key] = true
		routes[i] = r
	}
	return nil
}

// routeConflict rejects a host+path already published by another deployment.
func routeConflict(s State, id string, routes []Route) error {
	for _, r := range routes {
		for oid, d := range s.Deployments {
			if oid == id {
				continue
			}
			for _, o := range d.Routes {
				if o.Host == r.Host && o.Path == r.Path {
					return fmt.Errorf("route %s%s is already used by deployment %s", r.Host, r.Path, d.Name)
				}
			}
		}
	}
	return nil
}

// SetRoutes replaces a deployment's edge routes. Workers pick them up on the
// next inventory cycle; no containers are recreated.
func (c *Controller) SetRoutes(id string, routes []Route, expected ...[]Route) (Deployment, error) {
	if routes == nil {
		routes = []Route{}
	}
	if err := validateRoutes(routes); err != nil {
		return Deployment{}, err
	}
	var out Deployment
	err := c.Store.update(func(s *State) error {
		d, ok := s.Deployments[id]
		if !ok {
			return fmt.Errorf("deployment not found")
		}
		if pendingHandover(d) {
			return fmt.Errorf("resume the pending move before changing its routes")
		}
		if len(expected) > 0 && !slices.Equal(d.Routes, expected[0]) {
			return fmt.Errorf("routes changed while editing; reload the deployment and try again")
		}
		if err := routeConflict(*s, id, routes); err != nil {
			return err
		}
		d.Routes = routes
		s.Deployments[id] = d
		out = d
		return nil
	})
	return out, err
}

// SetEdge enables the edge proxy on every worker of the cluster network.
// Zero ports disable it.
func (c *Controller) SetEdge(httpPort, httpsPort int, redirect ...bool) error {
	if httpPort < 0 || httpPort > 65535 || httpsPort < 0 || httpsPort > 65535 || (httpPort != 0 && httpPort == httpsPort) {
		return fmt.Errorf("edge ports must be distinct values between 1 and 65535")
	}
	return c.Store.update(func(s *State) error {
		if httpPort+httpsPort == 0 {
			if s.Network != nil {
				s.Network.EdgeHTTPPort, s.Network.EdgeHTTPSPort = 0, 0
			}
			return nil
		}
		if !networkRequired(*s) {
			return fmt.Errorf("the edge proxy requires the cluster network (--cluster-network)")
		}
		if httpPort == s.Network.Port || httpsPort == s.Network.Port {
			return fmt.Errorf("edge ports must differ from the cluster network port")
		}
		s.Network.EdgeHTTPPort, s.Network.EdgeHTTPSPort = httpPort, httpsPort
		s.Network.EdgeRedirectHTTPS = len(redirect) > 0 && redirect[0] && httpsPort > 0
		return nil
	})
}

// edgeConfig maps every routed deployment with a VIP to edge routes.
func edgeConfig(s State) *clusternet.Edge {
	n := s.Network
	if n.EdgeHTTPPort == 0 && n.EdgeHTTPSPort == 0 {
		return nil
	}
	e := &clusternet.Edge{DrainTimeoutSeconds: n.EdgeDrainTimeoutSeconds, HTTPPort: n.EdgeHTTPPort, HTTPSPort: n.EdgeHTTPSPort, Routes: []clusternet.EdgeRoute{}}
	if e.DrainTimeoutSeconds == 60 {
		e.DrainTimeoutSeconds = 0
	} // Omit the wire default for older stateless agents.
	ids := make([]string, 0, len(s.Deployments))
	for id := range s.Deployments {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		svc, ok := n.Services[id]
		if !ok {
			continue
		}
		for _, r := range s.Deployments[id].Routes {
			e.Routes = append(e.Routes, clusternet.EdgeRoute{Host: r.Host, PathPrefix: r.Path, Target: net.JoinHostPort(svc.VIP, fmt.Sprint(r.Port))})
		}
	}
	return e
}
