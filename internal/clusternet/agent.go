package clusternet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// Runner executes a host command. Tests substitute a recorder.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// WireGuard configures a kernel WireGuard device.
type WireGuard interface {
	ConfigureDevice(name string, cfg wgtypes.Config) error
}

// Agent applies Config on a worker host. It needs CAP_NET_ADMIN (and
// CAP_NET_RAW for iptables), the wireguard kernel module, iproute2, iptables
// and Docker Engine 28 or newer.
type Agent struct {
	StateDir string
	Docker   *http.Client // talks to the Docker socket at http://docker
	Exec     Runner
	WG       WireGuard
	// ListenDNS overrides how the cluster resolver binds (tests).
	ListenDNS func(network, address string) (net.PacketConn, net.Listener, error)
	// EdgeCertFile and EdgeKeyFile are this worker's local TLS certificate for
	// the edge proxy. Private keys are never distributed by the control plane.
	EdgeCertFile, EdgeKeyFile string
	// ListenEdge overrides how the edge proxy binds (tests).
	ListenEdge func(network, address string) (net.Listener, error)

	mu      sync.Mutex
	desired Config
	status  Status
	dns     *dnsServer
	edge    *edgeServer
}

func NewAgent(stateDir string, docker *http.Client) *Agent {
	return &Agent{StateDir: stateDir, Docker: docker, Exec: execRunner}
}

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (a *Agent) keyPath() string    { return filepath.Join(a.StateDir, "wireguard.key") }
func (a *Agent) configPath() string { return filepath.Join(a.StateDir, "network.json") }

// privateKey loads or creates the host key. It never leaves this host.
func (a *Agent) privateKey() (wgtypes.Key, error) {
	if b, err := os.ReadFile(a.keyPath()); err == nil {
		return wgtypes.ParseKey(strings.TrimSpace(string(b)))
	} else if !os.IsNotExist(err) {
		return wgtypes.Key{}, fmt.Errorf("read WireGuard key: %w", err)
	}
	if err := os.MkdirAll(a.StateDir, 0700); err != nil {
		return wgtypes.Key{}, fmt.Errorf("create network state directory: %w", err)
	}
	k, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return wgtypes.Key{}, err
	}
	f, err := os.OpenFile(a.keyPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return wgtypes.Key{}, fmt.Errorf("save WireGuard key: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(k.String() + "\n"); err != nil {
		return wgtypes.Key{}, err
	}
	return k, f.Sync()
}

// Load restores the last applied config so the network survives agent
// restarts and reboots without a manager.
func (a *Agent) Load() {
	b, err := os.ReadFile(a.configPath())
	if err != nil {
		return
	}
	var c Config
	if json.Unmarshal(b, &c) == nil && c.Validate() == nil {
		a.mu.Lock()
		a.desired = c
		a.mu.Unlock()
	}
}

// Run re-applies the desired config periodically, repairing routes or
// firewall rules removed by a reboot, firewalld reload or Docker restart.
func (a *Agent) Run(ctx context.Context, interval time.Duration) {
	a.Load()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		a.mu.Lock()
		c := a.desired
		a.mu.Unlock()
		if c.Enabled() {
			a.apply(ctx, c)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *Agent) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.status
	if s.PublicKey == "" {
		if k, err := a.privateKey(); err == nil {
			s.PublicKey = k.PublicKey().String()
			a.status.PublicKey = s.PublicKey
		} else if s.Error == "" {
			s.Error = err.Error()
		}
	}
	return s
}

// ServeHTTP implements GET and PUT /zela/network.
func (a *Agent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case "GET":
	case "PUT":
		var c Config
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			http.Error(w, `{"message":"invalid network config"}`, 400)
			return
		}
		if err := c.Validate(); err != nil {
			b, _ := json.Marshal(map[string]string{"message": err.Error()})
			http.Error(w, string(b), 400)
			return
		}
		a.apply(r.Context(), c)
	default:
		http.Error(w, `{"message":"method not allowed"}`, 405)
		return
	}
	_ = json.NewEncoder(w).Encode(a.Status())
}

func (a *Agent) apply(ctx context.Context, c Config) {
	drain := 60
	if c.Edge != nil && c.Edge.DrainTimeoutSeconds > 0 {
		drain = c.Edge.DrainTimeoutSeconds
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(drain+30)*time.Second)
	defer cancel()
	a.mu.Lock()
	defer a.mu.Unlock()
	var err error
	if c.Enabled() {
		err = a.up(ctx, c)
	} else {
		err = a.down(ctx)
	}
	a.status.Error = ""
	a.status.Applied = ""
	if err != nil {
		a.status.Error = err.Error()
		log.Printf("cluster network: %v", err)
	} else {
		a.status.Applied = c.Hash()
	}
	// Persist the desired state even when applying failed, so it is retried.
	a.desired = c
	if b, mErr := json.Marshal(c); mErr == nil {
		_ = os.MkdirAll(a.StateDir, 0700)
		tmp := a.configPath() + ".tmp"
		if os.WriteFile(tmp, b, 0600) == nil {
			_ = os.Rename(tmp, a.configPath())
		}
	}
}

func (a *Agent) up(ctx context.Context, c Config) error {
	key, err := a.privateKey()
	if err != nil {
		return err
	}
	a.status.PublicKey = key.PublicKey().String()
	if _, err := a.Exec(ctx, "ip", "link", "show", "dev", Interface); err != nil {
		if _, err := a.Exec(ctx, "ip", "link", "add", "dev", Interface, "type", "wireguard"); err != nil {
			return fmt.Errorf("create %s (is the wireguard kernel module loaded and CAP_NET_ADMIN granted?): %w", Interface, err)
		}
	}
	peers := make([]wgtypes.PeerConfig, 0, len(c.Peers))
	keepalive := 25 * time.Second
	for _, p := range c.Peers {
		pk, err := wgtypes.ParseKey(p.PublicKey)
		if err != nil {
			return fmt.Errorf("invalid peer key")
		}
		pc := wgtypes.PeerConfig{PublicKey: pk, ReplaceAllowedIPs: true, PersistentKeepaliveInterval: &keepalive}
		// Unresolvable peers are configured without an endpoint; they can
		// still connect inbound and are retried on the next apply.
		if ep, err := net.ResolveUDPAddr("udp", p.Endpoint); err == nil {
			pc.Endpoint = ep
		}
		for _, s := range p.AllowedIPs {
			_, n, _ := net.ParseCIDR(s)
			pc.AllowedIPs = append(pc.AllowedIPs, *n)
		}
		peers = append(peers, pc)
	}
	port := c.ListenPort
	if err := a.wg().ConfigureDevice(Interface, wgtypes.Config{PrivateKey: &key, ListenPort: &port, ReplacePeers: true, Peers: peers}); err != nil {
		return fmt.Errorf("configure WireGuard: %w", err)
	}
	for _, args := range [][]string{
		{"address", "replace", c.Address, "dev", Interface},
		{"link", "set", "dev", Interface, "mtu", strconv.Itoa(MTU), "up"},
	} {
		if _, err := a.Exec(ctx, "ip", args...); err != nil {
			return err
		}
	}
	// A previous address (another prefix or index) would be chosen as the
	// source for host traffic and rejected by peers' allowed IPs.
	out, err := a.Exec(ctx, "ip", "-4", "-o", "address", "show", "dev", Interface)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "inet" && f[i+1] != c.Address {
				if _, err := a.Exec(ctx, "ip", "address", "del", f[i+1], "dev", Interface); err != nil {
					return err
				}
			}
		}
	}
	if err := a.routes(ctx, c); err != nil {
		return err
	}
	if err := a.dockerNetwork(ctx, c); err != nil {
		return err
	}
	if err := a.firewall(ctx, c); err != nil {
		return err
	}
	gateway, _ := Gateway(c.Subnet)
	cluster, _ := ParseCluster(c.Cluster)
	if a.dns == nil {
		a.dns = newDNSServer()
		if a.ListenDNS != nil {
			a.dns.listen = a.ListenDNS
		}
	}
	if err := a.dns.update(cluster, gateway, c.Records); err != nil {
		return fmt.Errorf("start cluster DNS on %s:53 (is CAP_NET_BIND_SERVICE granted?): %w", gateway, err)
	}
	if a.edge == nil && c.Edge != nil {
		a.edge = newEdgeServer(a.EdgeCertFile, a.EdgeKeyFile)
		if a.ListenEdge != nil {
			a.edge.listen = a.ListenEdge
		}
	}
	a.status.EdgeError = ""
	if a.edge != nil {
		// Edge failures must not take the worker out of the cluster network:
		// the listeners that did start keep serving, and the error is reported.
		if err := a.edge.update(c.Edge); err != nil {
			a.status.EdgeError = err.Error()
			log.Printf("cluster edge: %v", err)
		}
	}
	return nil
}

func (a *Agent) wg() WireGuard {
	if a.WG != nil {
		return a.WG
	}
	return kernelWG{}
}

type kernelWG struct{}

func (kernelWG) ConfigureDevice(name string, cfg wgtypes.Config) error {
	c, err := wgctrl.New()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.ConfigureDevice(name, cfg)
}

// routes points each peer's container subnet at zela0 and removes stale ones.
func (a *Agent) routes(ctx context.Context, c Config) error {
	cluster, _ := ParseCluster(c.Cluster)
	mesh, _ := netip.ParsePrefix(c.Address)
	// The VIP block is routed into zela0 so local route lookups succeed and
	// OUTPUT DNAT can rewrite the destination (an unreachable route fails
	// host connections before netfilter runs). Unmatched VIPs are rejected
	// by ZELA-NET, and WireGuard drops anything without a peer.
	want := map[string]bool{ServiceRange(cluster).String(): true}
	for _, p := range c.Peers {
		for _, s := range p.AllowedIPs {
			pp := netip.MustParsePrefix(s).Masked()
			if pp.Bits() == 24 && pp != mesh.Masked() {
				want[pp.String()] = true
			}
		}
	}
	for s := range want {
		if _, err := a.Exec(ctx, "ip", "route", "replace", s, "dev", Interface); err != nil {
			return err
		}
	}
	out, err := a.Exec(ctx, "ip", "-4", "route", "show", "dev", Interface)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		p, err := netip.ParsePrefix(f[0])
		if err != nil || want[p.Masked().String()] || (p.Masked() == mesh.Masked() && cluster.Contains(p.Addr())) {
			continue
		}
		if _, err := a.Exec(ctx, "ip", "route", "del", f[0], "dev", Interface); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) docker(ctx context.Context, method, path string, body any, out any) (int, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, r)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.Docker.Do(req)
	if err != nil {
		return 0, fmt.Errorf("Docker unavailable: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		var m struct{ Message string }
		_ = json.Unmarshal(data, &m)
		return resp.StatusCode, fmt.Errorf("Docker %s %s: %d %s", method, path, resp.StatusCode, m.Message)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("invalid Docker response")
		}
	}
	return resp.StatusCode, nil
}

// dockerNetwork ensures the "zela" bridge exists with this worker's subnet.
// gateway_mode_ipv4=nat-unprotected keeps outbound NAT while allowing routed
// access from peers; the firewall chain below restricts who may route in.
func (a *Agent) dockerNetwork(ctx context.Context, c Config) error {
	var version struct {
		APIVersion string `json:"ApiVersion"`
	}
	if _, err := a.docker(ctx, "GET", "/version", nil, &version); err != nil {
		return err
	}
	if !apiAtLeast(version.APIVersion, 1, 48) {
		return fmt.Errorf("cluster network requires Docker Engine 28 or newer (API 1.48+); this host reports API %s", version.APIVersion)
	}
	gateway, err := Gateway(c.Subnet)
	if err != nil {
		return err
	}
	var existing struct {
		IPAM struct {
			Config []struct{ Subnet, Gateway string }
		}
		Options    map[string]string
		Containers map[string]any
	}
	code, err := a.docker(ctx, "GET", "/networks/"+DockerNetwork, nil, &existing)
	switch {
	case code == 404:
	case err != nil:
		return err
	default:
		ok := len(existing.IPAM.Config) == 1 && existing.IPAM.Config[0].Subnet == c.Subnet && existing.Options["com.docker.network.bridge.name"] == Bridge
		if ok {
			return nil
		}
		if len(existing.Containers) > 0 {
			return fmt.Errorf("Docker network %q has a different subnet and attached containers; remove them to change the cluster network", DockerNetwork)
		}
		if _, err := a.docker(ctx, "DELETE", "/networks/"+DockerNetwork, nil, nil); err != nil {
			return err
		}
	}
	_, err = a.docker(ctx, "POST", "/networks/create", map[string]any{
		"Name": DockerNetwork, "Driver": "bridge", "CheckDuplicate": true,
		"IPAM":    map[string]any{"Config": []map[string]string{{"Subnet": c.Subnet, "Gateway": gateway}}},
		"Options": map[string]string{"com.docker.network.bridge.name": Bridge, "com.docker.network.driver.mtu": strconv.Itoa(MTU), "com.docker.network.bridge.gateway_mode_ipv4": "nat-unprotected"},
		"Labels":  map[string]string{"zela.managed": "true"},
	}, nil)
	return err
}

func apiAtLeast(v string, major, minor int) bool {
	a, b, ok := strings.Cut(v, ".")
	x, err1 := strconv.Atoi(a)
	y, err2 := strconv.Atoi(b)
	return ok && err1 == nil && err2 == nil && (x > major || (x == major && y >= minor))
}

const (
	filterChain  = "ZELA-NET"
	natChain     = "ZELA-NET-NAT"
	inputChain   = "ZELA-NET-IN"
	serviceChain = "ZELA-SVC"
)

// firewall owns two dedicated chains, rebuilt on every apply:
//   - filter ZELA-NET (from DOCKER-USER): new connections to local containers
//     are accepted only from the mesh, the local bridge, or published ports.
//   - nat ZELA-NET-NAT (from POSTROUTING): cluster-internal traffic is not
//     masqueraded, preserving container source addresses across hosts.
func (a *Agent) firewall(ctx context.Context, c Config) error {
	ipt := func(args ...string) error {
		_, err := a.Exec(ctx, "iptables", append([]string{"-w"}, args...)...)
		return err
	}
	ensureChain := func(table, chain string) error {
		if ipt("-t", table, "-n", "-L", chain) != nil {
			if err := ipt("-t", table, "-N", chain); err != nil {
				return err
			}
		}
		return ipt("-t", table, "-F", chain)
	}
	// Our jump must stay first: Docker inserts rules at the top of
	// POSTROUTING (MASQUERADE) whenever a network is created, which would
	// otherwise NAT cluster traffic before ZELA-NET-NAT can exempt it.
	ensureJump := func(table, parent, chain string) error {
		out, err := a.Exec(ctx, "iptables", "-w", "-t", table, "-S", parent)
		if err != nil {
			return err
		}
		jump := "-A " + parent + " -j " + chain
		first, count := "", 0
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "-A ") {
				continue
			}
			if first == "" {
				first = line
			}
			if line == jump {
				count++
			}
		}
		if first == jump && count == 1 {
			return nil
		}
		for ; count > 0; count-- {
			if err := ipt("-t", table, "-D", parent, "-j", chain); err != nil {
				return err
			}
		}
		return ipt("-t", table, "-I", parent, "1", "-j", chain)
	}
	if err := ensureChain("filter", filterChain); err != nil {
		return err
	}
	cluster, _ := ParseCluster(c.Cluster)
	for _, rule := range [][]string{
		// Traffic still addressed to a VIP after DNAT matched no service.
		{"-A", filterChain, "-d", ServiceRange(cluster).String(), "-j", "REJECT"},
		{"-A", filterChain, "-i", Interface, "-d", c.Subnet, "-j", "RETURN"},
		{"-A", filterChain, "-i", Bridge, "-j", "RETURN"},
		{"-A", filterChain, "-d", c.Subnet, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED,DNAT", "-j", "RETURN"},
		{"-A", filterChain, "-d", c.Subnet, "-j", "DROP"},
	} {
		if err := ipt(append([]string{"-t", "filter"}, rule...)...); err != nil {
			return err
		}
	}
	if err := ensureJump("filter", "DOCKER-USER", filterChain); err != nil {
		return fmt.Errorf("install firewall jump (is Docker running?): %w", err)
	}
	// Containers query the cluster resolver on the bridge gateway, which host
	// firewalls with a default-deny INPUT policy (ufw, nftables) would drop.
	if err := ensureChain("filter", inputChain); err != nil {
		return err
	}
	for _, proto := range []string{"udp", "tcp"} {
		if err := ipt("-t", "filter", "-A", inputChain, "-i", Bridge, "-p", proto, "--dport", "53", "-j", "ACCEPT"); err != nil {
			return err
		}
	}
	if err := ensureJump("filter", "INPUT", inputChain); err != nil {
		return err
	}
	if err := ensureChain("nat", natChain); err != nil {
		return err
	}
	if err := ipt("-t", "nat", "-A", natChain, "-s", c.Subnet, "-d", c.Cluster, "-j", "ACCEPT"); err != nil {
		return err
	}
	if err := ensureJump("nat", "POSTROUTING", natChain); err != nil {
		return err
	}
	// Service VIPs: each new connection to a VIP is DNATed on the client's
	// host to one backend, chosen uniformly at random. Conntrack reverses it
	// for replies, and ZELA-NET-NAT keeps the client's source address.
	if err := ensureChain("nat", serviceChain); err != nil {
		return err
	}
	for _, svc := range c.Services {
		n := len(svc.Backends)
		for i, b := range svc.Backends {
			rule := []string{"-t", "nat", "-A", serviceChain, "-d", svc.VIP + "/32"}
			if i < n-1 {
				// Probability 1/(remaining) gives every backend an equal share.
				rule = append(rule, "-m", "statistic", "--mode", "random", "--probability", strconv.FormatFloat(1/float64(n-i), 'f', 5, 64))
			}
			if err := ipt(append(rule, "-j", "DNAT", "--to-destination", b)...); err != nil {
				return err
			}
		}
	}
	for _, parent := range []string{"PREROUTING", "OUTPUT"} {
		if err := ensureJump("nat", parent, serviceChain); err != nil {
			return err
		}
	}
	return nil
}

// down removes the interface and firewall chains. The Docker network is kept
// while containers are attached, and removed otherwise.
func (a *Agent) down(ctx context.Context) error {
	var errs []error
	if a.dns != nil {
		a.dns.stop()
	}
	if a.edge != nil {
		a.edge.stop()
	}

	if _, err := a.Exec(ctx, "ip", "link", "show", "dev", Interface); err == nil {
		if _, err := a.Exec(ctx, "ip", "link", "del", "dev", Interface); err != nil {
			errs = append(errs, err)
		}
	}
	for _, t := range [][3]string{{"filter", "DOCKER-USER", filterChain}, {"filter", "INPUT", inputChain}, {"nat", "POSTROUTING", natChain}, {"nat", "PREROUTING", serviceChain}, {"nat", "OUTPUT", serviceChain}} {
		chain := t[2]
		for i := 0; i < 8; i++ { // remove duplicate jumps, bounded
			if _, err := a.Exec(ctx, "iptables", "-w", "-t", t[0], "-D", t[1], "-j", chain); err != nil {
				break
			}
		}
		_, _ = a.Exec(ctx, "iptables", "-w", "-t", t[0], "-F", chain)
		_, _ = a.Exec(ctx, "iptables", "-w", "-t", t[0], "-X", chain)
	}
	if a.Docker != nil {
		var n struct{ Containers map[string]any }
		if code, err := a.docker(ctx, "GET", "/networks/"+DockerNetwork, nil, &n); err == nil && len(n.Containers) == 0 {
			if _, err := a.docker(ctx, "DELETE", "/networks/"+DockerNetwork, nil, nil); err != nil {
				errs = append(errs, err)
			}
		} else if err != nil && code != 404 {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
