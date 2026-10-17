package clusternet

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func selfSigned(t *testing.T, host string) EdgeCertificate {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(k)
	return EdgeCertificate{CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), KeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}))}
}

func TestEdgeACMEChallengeRedirectAndSNI(t *testing.T) {
	e := newEdgeServer("", "")
	addrs := map[string]string{}
	e.listen = func(network, addr string) (net.Listener, error) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		addrs[addr] = l.Addr().String()
		return l, err
	}
	defer e.stop()
	token := "abcdefghijklmnopqrstuvwxyz012345"
	if err := e.update(&Edge{HTTPPort: 80, HTTPSPort: 443, RedirectHTTPS: true,
		Challenges:   map[string]string{token: token + ".thumb"},
		Certificates: []EdgeCertificate{selfSigned(t, "shop.example.test"), selfSigned(t, "blog.example.test")},
		Routes:       []EdgeRoute{{PathPrefix: "/", Target: "127.0.0.1:1"}}}); err != nil {
		t.Fatalf("HTTPS with pushed certificates only: %v", err)
	}
	httpAddr, httpsAddr := addrs[":80"], addrs[":443"]
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(host, path string) *http.Response {
		req, _ := http.NewRequest("GET", "http://"+httpAddr+path, nil)
		req.Host = host
		resp, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	// The challenge is answered for any host, before redirects and routing.
	resp := get("anything.example.test", "/.well-known/acme-challenge/"+token)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != token+".thumb" {
		t.Fatalf("challenge: %d %q", resp.StatusCode, body)
	}
	resp = get("shop.example.test", "/cart?x=1")
	if resp.StatusCode != 308 || resp.Header.Get("Location") != "https://shop.example.test/cart?x=1" {
		t.Fatalf("redirect: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp := get("nocert.example.test", "/"); resp.StatusCode == 308 {
		t.Fatal("redirected a host without a certificate")
	}
	for _, host := range []string{"shop.example.test", "blog.example.test"} {
		conn, err := tls.Dial("tcp", httpsAddr, &tls.Config{ServerName: host, InsecureSkipVerify: true})
		if err != nil {
			t.Fatalf("%s handshake: %v", host, err)
		}
		if got := conn.ConnectionState().PeerCertificates[0].DNSNames[0]; got != host {
			t.Fatalf("SNI %s served certificate for %s", host, got)
		}
		conn.Close()
	}
	// Challenges are never answered over TLS.
	tr := &http.Transport{TLSClientConfig: &tls.Config{ServerName: "shop.example.test", InsecureSkipVerify: true}}
	req, _ := http.NewRequest("GET", "https://"+httpsAddr+"/.well-known/acme-challenge/"+token, nil)
	req.Host = "shop.example.test"
	if r, err := tr.RoundTrip(req); err == nil {
		b, _ := io.ReadAll(r.Body)
		if string(b) == token+".thumb" {
			t.Fatal("challenge served over TLS")
		}
	}
	_ = httptest.NewRecorder
}
