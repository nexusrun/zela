package clusternet

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Zone is the DNS suffix for cluster service names, e.g. web.zela.
const Zone = "zela"

// DNSTTL keeps answers short-lived so moved replicas are picked up quickly.
const DNSTTL = 5

// dnsServer answers <name>.zela from records pushed by the control plane and
// forwards every other query to the host's resolvers. It listens on the local
// bridge gateway, which containers on the zela network use as their DNS server.
type dnsServer struct {
	mu        sync.RWMutex
	records   map[string][]netip.Addr
	cluster   netip.Prefix
	upstreams []string
	bind      string
	rotation  atomic.Uint32
	udp       net.PacketConn
	tcp       net.Listener
	listen    func(network, address string) (net.PacketConn, net.Listener, error)
}

func newDNSServer() *dnsServer {
	return &dnsServer{records: map[string][]netip.Addr{}, listen: listenDNS}
}

func listenDNS(network, address string) (net.PacketConn, net.Listener, error) {
	u, err := net.ListenPacket("udp", address)
	if err != nil {
		return nil, nil, err
	}
	t, err := net.Listen("tcp", address)
	if err != nil {
		u.Close()
		return nil, nil, err
	}
	return u, t, nil
}

// update replaces the record set and (re)binds to gateway:53 if needed.
func (s *dnsServer) update(cluster netip.Prefix, gateway string, records map[string][]string) error {
	parsed := make(map[string][]netip.Addr, len(records))
	for name, ips := range records {
		for _, ip := range ips {
			if a, err := netip.ParseAddr(ip); err == nil {
				parsed[strings.ToLower(name)] = append(parsed[strings.ToLower(name)], a)
			}
		}
	}
	s.mu.Lock()
	s.records, s.cluster = parsed, cluster
	if s.upstreams == nil {
		s.upstreams = resolvConfServers("/etc/resolv.conf", gateway)
	}
	bound := s.bind
	s.mu.Unlock()
	addr := net.JoinHostPort(gateway, "53")
	if bound == addr {
		return nil
	}
	s.stop()
	u, t, err := s.listen("udp", addr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.udp, s.tcp, s.bind = u, t, addr
	s.mu.Unlock()
	go s.serveUDP(u)
	go s.serveTCP(t)
	return nil
}

func (s *dnsServer) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.udp != nil {
		s.udp.Close()
	}
	if s.tcp != nil {
		s.tcp.Close()
	}
	s.udp, s.tcp, s.bind = nil, nil, ""
}

// resolvConfServers reads host nameservers, skipping our own listener.
func resolvConfServers(path, self string) []string {
	f, err := os.Open(path)
	if err != nil {
		return []string{}
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[0] == "nameserver" && f[1] != self {
			if _, err := netip.ParseAddr(f[1]); err == nil {
				out = append(out, net.JoinHostPort(f[1], "53"))
			}
		}
	}
	return out
}

// allowed limits the resolver to cluster and host-local sources so the
// forwarder is not an open resolver for the worker's LAN.
func (s *dnsServer) allowed(addr net.Addr) bool {
	var ip net.IP
	switch a := addr.(type) {
	case *net.UDPAddr:
		ip = a.IP
	case *net.TCPAddr:
		ip = a.IP
	}
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	s.mu.RLock()
	cluster := s.cluster
	s.mu.RUnlock()
	if cluster.Contains(a) || a.IsLoopback() {
		return true
	}
	// Docker's embedded resolver may forward from a host address.
	addrs, _ := net.InterfaceAddrs()
	for _, ia := range addrs {
		if n, ok := ia.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

func (s *dnsServer) serveUDP(c net.PacketConn) {
	buf := make([]byte, 4096)
	for {
		n, from, err := c.ReadFrom(buf)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				log.Printf("cluster DNS: %v", err)
			}
			return
		}
		if !s.allowed(from) {
			continue
		}
		msg := append([]byte(nil), buf[:n]...)
		go func() {
			if resp := s.handle(msg, "udp"); resp != nil {
				_, _ = c.WriteTo(resp, from)
			}
		}()
	}
}

func (s *dnsServer) serveTCP(l net.Listener) {
	sem := make(chan struct{}, 64)
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		if !s.allowed(conn.RemoteAddr()) {
			conn.Close()
			continue
		}
		select {
		case sem <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		go func() {
			defer func() { <-sem }()
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			for {
				var size uint16
				if binary.Read(conn, binary.BigEndian, &size) != nil || size < 12 {
					return
				}
				msg := make([]byte, size)
				if _, err := io.ReadFull(conn, msg); err != nil {
					return
				}
				resp := s.handle(msg, "tcp")
				if resp == nil {
					return
				}
				out := make([]byte, 2+len(resp))
				binary.BigEndian.PutUint16(out, uint16(len(resp)))
				copy(out[2:], resp)
				if _, err := conn.Write(out); err != nil {
					return
				}
			}
		}()
	}
}

const (
	typeA    = 1
	typeAAAA = 28
	typeANY  = 255
	classIN  = 1
)

// handle answers cluster names or forwards the raw query.
func (s *dnsServer) handle(msg []byte, network string) []byte {
	if len(msg) < 12 || msg[2]&0x80 != 0 || binary.BigEndian.Uint16(msg[4:]) != 1 {
		return nil // not a single-question query
	}
	name, end, ok := readQName(msg, 12)
	if !ok || end+4 > len(msg) {
		return reply(msg, 12, 1, nil) // FORMERR
	}
	qtype := binary.BigEndian.Uint16(msg[end:])
	qclass := binary.BigEndian.Uint16(msg[end+2:])
	qend := end + 4
	lower := strings.ToLower(strings.TrimSuffix(name, "."))
	if lower == Zone || strings.HasSuffix(lower, "."+Zone) {
		label := strings.TrimSuffix(strings.TrimSuffix(lower, Zone), ".")
		s.mu.RLock()
		ips, found := s.records[label]
		s.mu.RUnlock()
		if !found {
			return reply(msg, qend, 3, nil) // NXDOMAIN
		}
		if qclass != classIN || (qtype != typeA && qtype != typeANY) {
			return reply(msg, qend, 0, nil) // name exists, no data of this type
		}
		// Rotate answers so clients that take the first address spread
		// across replicas (simple round-robin DNS).
		if n := len(ips); n > 1 {
			k := int(s.rotation.Add(1)) % n
			ips = append(append([]netip.Addr(nil), ips[k:]...), ips[:k]...)
		}
		return reply(msg, qend, 0, ips)
	}
	return s.forward(msg, network)
}

// reply builds an authoritative response echoing the question.
func reply(query []byte, qend int, rcode byte, ips []netip.Addr) []byte {
	out := make([]byte, 0, qend+16*len(ips))
	out = append(out, query[:qend]...)
	out[2] = 0x80 | 0x04 | (query[2] & 0x01) // QR, AA, keep RD
	out[3] = 0x80 | rcode                    // RA
	binary.BigEndian.PutUint16(out[6:], uint16(len(ips)))
	binary.BigEndian.PutUint16(out[8:], 0)
	binary.BigEndian.PutUint16(out[10:], 0)
	for _, ip := range ips {
		if !ip.Is4() {
			continue
		}
		b := ip.As4()
		out = append(out, 0xC0, 0x0C) // pointer to the question name
		out = binary.BigEndian.AppendUint16(out, typeA)
		out = binary.BigEndian.AppendUint16(out, classIN)
		out = binary.BigEndian.AppendUint32(out, DNSTTL)
		out = binary.BigEndian.AppendUint16(out, 4)
		out = append(out, b[:]...)
	}
	return out
}

func readQName(msg []byte, off int) (string, int, bool) {
	var labels []string
	for i := 0; i < 128; i++ {
		if off >= len(msg) {
			return "", 0, false
		}
		n := int(msg[off])
		off++
		if n == 0 {
			return strings.Join(labels, ".") + ".", off, true
		}
		if n&0xC0 != 0 || off+n > len(msg) {
			return "", 0, false // compression is not valid in a question
		}
		labels = append(labels, string(msg[off:off+n]))
		off += n
	}
	return "", 0, false
}

func (s *dnsServer) forward(msg []byte, network string) []byte {
	s.mu.RLock()
	upstreams := s.upstreams
	s.mu.RUnlock()
	for _, up := range upstreams {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		resp, err := exchange(ctx, network, up, msg)
		cancel()
		if err == nil {
			return resp
		}
	}
	end := 12
	if _, e, ok := readQName(msg, 12); ok && e+4 <= len(msg) {
		end = e + 4
	}
	return reply(msg, end, 2, nil) // SERVFAIL
}

func exchange(ctx context.Context, network, server string, msg []byte) ([]byte, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if d, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(d)
	}
	if network == "tcp" {
		out := binary.BigEndian.AppendUint16(nil, uint16(len(msg)))
		if _, err := conn.Write(append(out, msg...)); err != nil {
			return nil, err
		}
		var size uint16
		if err := binary.Read(conn, binary.BigEndian, &size); err != nil {
			return nil, err
		}
		resp := make([]byte, size)
		_, err := io.ReadFull(conn, resp)
		return resp, err
	}
	if _, err := conn.Write(msg); err != nil {
		return nil, err
	}
	buf := make([]byte, 65535)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}
