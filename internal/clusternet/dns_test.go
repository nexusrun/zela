package clusternet

import (
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
)

func query(name string, qtype uint16) []byte {
	m := []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, l := range splitLabels(name) {
		m = append(m, byte(len(l)))
		m = append(m, l...)
	}
	m = append(m, 0)
	m = binary.BigEndian.AppendUint16(m, qtype)
	return binary.BigEndian.AppendUint16(m, classIN)
}
func splitLabels(n string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(n); i++ {
		if i == len(n) || n[i] == '.' {
			if i > start {
				out = append(out, n[start:i])
			}
			start = i + 1
		}
	}
	return out
}
func answers(t *testing.T, resp []byte) (rcode int, ips []string) {
	t.Helper()
	if len(resp) < 12 || resp[0] != 0x12 || resp[1] != 0x34 || resp[2]&0x80 == 0 {
		t.Fatalf("bad response %x", resp)
	}
	count := int(binary.BigEndian.Uint16(resp[6:]))
	_, off, _ := readQName(resp, 12)
	off += 4
	for i := 0; i < count; i++ {
		off += 2 + 2 + 2 + 4
		n := int(binary.BigEndian.Uint16(resp[off:]))
		off += 2
		ips = append(ips, net.IP(resp[off:off+n]).String())
		off += n
	}
	return int(resp[3] & 0x0F), ips
}

func TestDNSAnswersClusterNames(t *testing.T) {
	s := newDNSServer()
	s.cluster = netip.MustParsePrefix("10.200.0.0/16")
	s.records = map[string][]netip.Addr{"web": {netip.MustParseAddr("10.200.1.2"), netip.MustParseAddr("10.200.2.2")}}
	rc, ips := answers(t, s.handle(query("Web.Zela.", typeA), "udp"))
	if rc != 0 || len(ips) != 2 {
		t.Fatalf("A web.zela: rcode %d ips %v", rc, ips)
	}
	_, next := answers(t, s.handle(query("web.zela", typeA), "udp"))
	if next[0] == ips[0] {
		t.Fatalf("answers not rotated: %v then %v", ips, next)
	}
	if rc, ips := answers(t, s.handle(query("web.zela", typeAAAA), "udp")); rc != 0 || len(ips) != 0 {
		t.Fatalf("AAAA should be NOERROR/empty: %d %v", rc, ips)
	}
	if rc, _ := answers(t, s.handle(query("missing.zela", typeA), "udp")); rc != 3 {
		t.Fatalf("unknown name rcode %d, want NXDOMAIN", rc)
	}
	if s.handle([]byte{1, 2, 3}, "udp") != nil {
		t.Fatal("garbage answered")
	}
}

func TestDNSForwardsOtherNames(t *testing.T) {
	up, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	go func() {
		buf := make([]byte, 512)
		n, from, err := up.ReadFrom(buf)
		if err != nil {
			return
		}
		resp := reply(buf[:n], n, 0, []netip.Addr{netip.MustParseAddr("93.184.216.34")})
		_, _ = up.WriteTo(resp, from)
	}()
	s := newDNSServer()
	s.cluster = netip.MustParsePrefix("10.200.0.0/16")
	s.upstreams = []string{up.LocalAddr().String()}
	rc, ips := answers(t, s.handle(query("example.com", typeA), "udp"))
	if rc != 0 || len(ips) != 1 || ips[0] != "93.184.216.34" {
		t.Fatalf("forwarded answer: %d %v", rc, ips)
	}
	s.upstreams = []string{}
	if rc, _ := answers(t, s.handle(query("example.com", typeA), "udp")); rc != 2 {
		t.Fatalf("no upstream rcode %d, want SERVFAIL", rc)
	}
}

func TestDNSRefusesOutsideSources(t *testing.T) {
	s := newDNSServer()
	s.cluster = netip.MustParsePrefix("10.200.0.0/16")
	if !s.allowed(&net.UDPAddr{IP: net.ParseIP("10.200.3.4")}) || !s.allowed(&net.UDPAddr{IP: net.ParseIP("127.0.0.1")}) {
		t.Fatal("cluster/loopback source refused")
	}
	if s.allowed(&net.UDPAddr{IP: net.ParseIP("198.51.100.7")}) {
		t.Fatal("outside source allowed (open resolver)")
	}
}

func TestDNSLabelAndRecordValidation(t *testing.T) {
	for in, want := range map[string]string{"Web_API.v2": "web-api-v2", "-x-": "x", "ok": "ok", "日本": ""} {
		if got := DNSLabel(in); got != want {
			t.Fatalf("DNSLabel(%q) = %q, want %q", in, got, want)
		}
	}
	c := Config{Cluster: "10.200.0.0/16", Address: "10.200.0.1/24", Subnet: "10.200.1.0/24", ListenPort: 51820, Records: map[string][]string{"web": {"8.8.8.8"}}}
	if c.Validate() == nil {
		t.Fatal("record outside the cluster accepted")
	}
	c.Records = map[string][]string{"Bad_Name": {"10.200.1.2"}}
	if c.Validate() == nil {
		t.Fatal("invalid label accepted")
	}
}
