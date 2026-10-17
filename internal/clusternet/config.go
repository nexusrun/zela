// Package clusternet implements the optional encrypted cluster network: a
// WireGuard mesh between workers with one routed container subnet per worker.
//
// Address plan for a cluster prefix such as 10.200.0.0/16:
//
//	10.200.0.N/24   mesh address of worker N on the zela0 interface
//	10.200.N.0/24   container subnet of worker N (Docker network "zela")
//
// Worker indexes run from 1 to 254. The control plane allocates indexes and
// distributes peers; each worker keeps its private key and applies its own
// configuration, so the data plane keeps working while managers are down.
package clusternet

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	Interface     = "zela0"
	Bridge        = "zelabr0"
	DockerNetwork = "zela"
	DefaultPort   = 51820
	// MTU leaves room for WireGuard's IPv4/IPv6 encapsulation overhead.
	MTU      = 1420
	MaxIndex = 254
)

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// DNSLabel converts a deployment or container name into a DNS label:
// lowercase, with dots and underscores replaced by hyphens. It returns ""
// when no valid label results.
func DNSLabel(name string) string {
	l := strings.Trim(strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		case r == '_' || r == '.':
			return '-'
		}
		return -1
	}, name), "-")
	if len(l) > 63 || !dnsLabel.MatchString(l) {
		return ""
	}
	return l
}

// Peer is another worker as seen by the receiving worker.
type Peer struct {
	PublicKey  string   `json:"publicKey"`
	Endpoint   string   `json:"endpoint"`
	AllowedIPs []string `json:"allowedIPs"`
}

// Config is the desired network state for one worker. An empty Cluster means
// the network is disabled and should be removed.
type Config struct {
	Cluster    string `json:"cluster"`
	Address    string `json:"address"`
	Subnet     string `json:"subnet"`
	ListenPort int    `json:"listenPort"`
	Peers      []Peer `json:"peers"`
	// Records maps service labels (web for web.zela) to container IPs.
	Records map[string][]string `json:"records,omitempty"`
	// Services are stable virtual IPs load-balanced across replica IPs.
	Services []Service `json:"services,omitempty"`
	// Edge configures the external HTTP(S) proxy on this worker; nil disables it.
	Edge *Edge `json:"edge,omitempty"`
}

// Edge routes external requests to service VIPs by host name and path.
type Edge struct {
	HTTPPort  int         `json:"httpPort"`
	HTTPSPort int         `json:"httpsPort,omitempty"`
	Routes    []EdgeRoute `json:"routes"`
	// RedirectHTTPS answers plain HTTP with a redirect for hosts that have a
	// certificate (ACME challenges are always served over HTTP).
	RedirectHTTPS bool `json:"redirectHTTPS,omitempty"`
	// Challenges maps ACME HTTP-01 tokens to key authorizations.
	Challenges map[string]string `json:"challenges,omitempty"`
	// Certificates are issued by the control plane's ACME client.
	Certificates []EdgeCertificate `json:"certificates,omitempty"`
}

// EdgeCertificate is a PEM chain and key, selected by SNI.
type EdgeCertificate struct {
	CertPEM string `json:"certPEM"`
	KeyPEM  string `json:"keyPEM"`
}

var acmeToken = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// EdgeRoute sends requests for Host (empty matches any) whose path starts with
// PathPrefix to Target, a service VIP and port.
type EdgeRoute struct {
	Host       string `json:"host,omitempty"`
	PathPrefix string `json:"pathPrefix"`
	Target     string `json:"target"`
}

var edgeHost = regexp.MustCompile(`^(?i)(\*\.)?[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

// ValidEdgeHost accepts a host name, or *.domain for one wildcard label.
func ValidEdgeHost(h string) bool { return h == "" || (len(h) <= 253 && edgeHost.MatchString(h)) }

// ValidEdgePath accepts an absolute path prefix without query or fragment.
func ValidEdgePath(p string) bool {
	return strings.HasPrefix(p, "/") && len(p) <= 512 && !strings.ContainsAny(p, "?#\\ \t\r\n") && !strings.Contains(p, "..")
}

// Service is a virtual IP whose new connections are spread across Backends.
// An empty Backends list makes the VIP unreachable rather than unrouted.
type Service struct {
	VIP      string   `json:"vip"`
	Backends []string `json:"backends"`
}

// MaxServices is the number of service VIPs in the reserved x.y.255.0/24 block.
const MaxServices = 254

// ServiceRange is the reserved VIP block of a cluster prefix.
func ServiceRange(cluster netip.Prefix) netip.Prefix {
	b := cluster.Addr().As4()
	return netip.PrefixFrom(netip.AddrFrom4([4]byte{b[0], b[1], 255, 0}), 24)
}

// ServiceVIP returns the virtual IP for service index 1–254.
func ServiceVIP(cluster netip.Prefix, index int) (string, error) {
	if index < 1 || index > MaxServices {
		return "", fmt.Errorf("service index %d out of range", index)
	}
	b := cluster.Addr().As4()
	return netip.AddrFrom4([4]byte{b[0], b[1], 255, byte(index)}).String(), nil
}

// Status is reported by a worker after applying (or attempting) a Config.
type Status struct {
	PublicKey string `json:"publicKey"`
	Applied   string `json:"applied,omitempty"`
	Error     string `json:"error,omitempty"`
	// EdgeError reports edge-proxy problems (missing certificate, port in
	// use). It does not affect the worker's cluster-network readiness.
	EdgeError string `json:"edgeError,omitempty"`
}

// ParseCluster accepts an IPv4 /16 prefix, normalized to its network address.
func ParseCluster(cidr string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil || !p.Addr().Is4() || p.Bits() != 16 {
		return netip.Prefix{}, fmt.Errorf("cluster network must be an IPv4 /16 prefix, such as 10.200.0.0/16")
	}
	p = p.Masked()
	if !p.Addr().IsPrivate() {
		return netip.Prefix{}, fmt.Errorf("cluster network must use private (RFC 1918) addresses")
	}
	return p, nil
}

// Allocate returns the mesh address (with /24) and container subnet for index.
func Allocate(cluster netip.Prefix, index int) (address, subnet string, err error) {
	if index < 1 || index > MaxIndex {
		return "", "", fmt.Errorf("worker network index %d out of range", index)
	}
	b := cluster.Addr().As4()
	address = netip.AddrFrom4([4]byte{b[0], b[1], 0, byte(index)}).String() + "/24"
	subnet = netip.PrefixFrom(netip.AddrFrom4([4]byte{b[0], b[1], byte(index), 0}), 24).String()
	return address, subnet, nil
}

// Gateway is the host-side bridge address of a container subnet.
func Gateway(subnet string) (string, error) {
	p, err := netip.ParsePrefix(subnet)
	if err != nil || !p.Addr().Is4() {
		return "", fmt.Errorf("invalid subnet")
	}
	b := p.Masked().Addr().As4()
	b[3] = 1
	return netip.AddrFrom4(b).String(), nil
}

// MeshHost returns the bare mesh IP from an address such as 10.200.0.3/24.
func MeshHost(address string) string {
	p, err := netip.ParsePrefix(address)
	if err != nil {
		return ""
	}
	return p.Addr().String()
}

// Enabled reports whether this config describes an active network.
func (c Config) Enabled() bool { return c.Cluster != "" }

// Validate checks that every address belongs to the cluster prefix, so a
// compromised or buggy manager cannot route arbitrary networks into a host.
func (c Config) Validate() error {
	if !c.Enabled() {
		if c.Address != "" || c.Subnet != "" || len(c.Peers) > 0 {
			return fmt.Errorf("a disabled network config must be empty")
		}
		return nil
	}
	cluster, err := ParseCluster(c.Cluster)
	if err != nil {
		return err
	}
	addr, err := netip.ParsePrefix(c.Address)
	if err != nil || addr.Bits() != 24 || !cluster.Contains(addr.Addr()) {
		return fmt.Errorf("mesh address must be a /24 address inside the cluster network")
	}
	subnet, err := netip.ParsePrefix(c.Subnet)
	if err != nil || subnet.Bits() != 24 || subnet != subnet.Masked() || !cluster.Contains(subnet.Addr()) {
		return fmt.Errorf("container subnet must be a /24 inside the cluster network")
	}
	if c.ListenPort < 1 || c.ListenPort > 65535 {
		return fmt.Errorf("invalid WireGuard listen port")
	}
	if len(c.Records) > 8192 {
		return fmt.Errorf("too many DNS records")
	}
	for name, ips := range c.Records {
		if !dnsLabel.MatchString(name) || len(ips) == 0 || len(ips) > 64 {
			return fmt.Errorf("invalid DNS record %q", name)
		}
		for _, ip := range ips {
			a, err := netip.ParseAddr(ip)
			if err != nil || !cluster.Contains(a) {
				return fmt.Errorf("DNS record %q points outside the cluster network", name)
			}
		}
	}
	if len(c.Services) > MaxServices {
		return fmt.Errorf("too many services")
	}
	vips := ServiceRange(cluster)
	seenVIP := map[string]bool{}
	for _, svc := range c.Services {
		v, err := netip.ParseAddr(svc.VIP)
		if err != nil || !vips.Contains(v) || seenVIP[svc.VIP] || len(svc.Backends) > 64 {
			return fmt.Errorf("invalid service VIP %q", svc.VIP)
		}
		seenVIP[svc.VIP] = true
		for _, b := range svc.Backends {
			a, err := netip.ParseAddr(b)
			if err != nil || !cluster.Contains(a) || vips.Contains(a) {
				return fmt.Errorf("service %s has a backend outside the cluster's container subnets", svc.VIP)
			}
		}
	}
	if e := c.Edge; e != nil {
		if e.HTTPPort < 0 || e.HTTPPort > 65535 || e.HTTPSPort < 0 || e.HTTPSPort > 65535 || (e.HTTPPort == 0 && e.HTTPSPort == 0) || (e.HTTPPort != 0 && e.HTTPPort == e.HTTPSPort) {
			return fmt.Errorf("invalid edge ports")
		}
		if len(e.Routes) > 4096 || len(e.Challenges) > 256 || len(e.Certificates) > 1024 {
			return fmt.Errorf("too many edge routes, challenges or certificates")
		}
		for token, auth := range e.Challenges {
			if !acmeToken.MatchString(token) || !strings.HasPrefix(auth, token+".") || len(auth) > 256 {
				return fmt.Errorf("invalid ACME challenge")
			}
		}
		for _, cert := range e.Certificates {
			if len(cert.CertPEM) > 64<<10 || len(cert.KeyPEM) > 16<<10 || !strings.Contains(cert.CertPEM, "BEGIN CERTIFICATE") {
				return fmt.Errorf("invalid edge certificate")
			}
		}
		for _, r := range e.Routes {
			host, port, err := net.SplitHostPort(r.Target)
			v, perr := netip.ParseAddr(host)
			n, aerr := strconv.Atoi(port)
			if !ValidEdgeHost(r.Host) || !ValidEdgePath(r.PathPrefix) || err != nil || perr != nil || !vips.Contains(v) || aerr != nil || n < 1 || n > 65535 {
				return fmt.Errorf("invalid edge route %s%s -> %s", r.Host, r.PathPrefix, r.Target)
			}
		}
	}
	if len(c.Peers) > MaxIndex {
		return fmt.Errorf("too many peers")
	}
	seen := map[string]bool{}
	for _, p := range c.Peers {
		if len(p.PublicKey) != 44 || seen[p.PublicKey] {
			return fmt.Errorf("invalid or duplicate peer public key")
		}
		seen[p.PublicKey] = true
		host, port, err := net.SplitHostPort(p.Endpoint)
		if err != nil || host == "" {
			return fmt.Errorf("peer endpoint must be host:port")
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("peer endpoint has an invalid port")
		}
		if len(p.AllowedIPs) == 0 || len(p.AllowedIPs) > 4 {
			return fmt.Errorf("peer must have between one and four allowed prefixes")
		}
		for _, a := range p.AllowedIPs {
			pp, err := netip.ParsePrefix(a)
			if err != nil || !pp.Addr().Is4() || pp.Bits() < 24 || !cluster.Contains(pp.Addr()) {
				return fmt.Errorf("peer allowed prefixes must lie inside the cluster network")
			}
		}
	}
	return nil
}

// Hash identifies a config independently of peer ordering.
func (c Config) Hash() string {
	peers := append([]Peer(nil), c.Peers...)
	sort.Slice(peers, func(i, j int) bool { return peers[i].PublicKey < peers[j].PublicKey })
	c.Peers = peers
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}
