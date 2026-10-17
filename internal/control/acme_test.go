package control

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zela-dev/zela/internal/clusternet"
)

func TestACMEHostsSecretsAndEdgeCertificates(t *testing.T) {
	c, _ := networkFixture(t)
	if err := c.SetACME(ACMEConfig{Email: "ops@example.com"}); err == nil {
		t.Fatal("ACME accepted without the edge on port 80")
	}
	if err := c.SetEdge(80, 443, true); err != nil {
		t.Fatal(err)
	}
	if err := c.SetACME(ACMEConfig{Email: "not-an-email"}); err == nil {
		t.Fatal("invalid email accepted")
	}
	if err := c.SetACME(ACMEConfig{Email: "ops@example.com"}); err != nil || c.acme.Directory != LetsEncryptDirectory {
		t.Fatalf("SetACME: %v", err)
	}
	s := State{Network: &NetworkState{Services: map[string]NetworkService{"a": {VIP: "10.200.255.1"}}}, Deployments: map[string]Deployment{
		"a": {Routes: []Route{{Host: "shop.example.com"}, {Host: "*.example.com"}, {Host: ""}}},
		"b": {Routes: []Route{{Host: "novip.example.com"}}},
	}}
	if got := acmeHosts(s); len(got) != 1 || got[0] != "shop.example.com" {
		t.Fatalf("ACME hosts %v (wildcards, catch-all and VIP-less deployments excluded)", got)
	}

	sealed, err := c.Store.Vault.Seal(acmeKeyPurpose+"shop.example.com", []byte("-----BEGIN EC PRIVATE KEY-----\nsecret\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Store.Vault.Unseal(acmeKeyPurpose+"other.example.com", sealed); err == nil {
		t.Fatal("sealed key opened for another purpose")
	}
	if err := c.Store.update(func(st *State) error {
		a := acmeInit(st)
		a.AccountKey = "sealed-account"
		a.Certs["shop.example.com"] = acmeCert{CertPEM: "-----BEGIN CERTIFICATE-----\nx\n", Key: sealed, NotAfter: time.Now().Add(60 * 24 * time.Hour)}
		a.Challenges["tok"] = "tok.auth"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(c.Store.public())
	if strings.Contains(string(public), sealed) || strings.Contains(string(public), "sealed-account") || strings.Contains(string(public), "secret") {
		t.Fatal("ACME key material exposed in the public state")
	}
	if !strings.Contains(string(public), `"acmeCertificates":[{"host":"shop.example.com"`) {
		t.Fatalf("certificate status missing: %s", public)
	}
	e := &clusternet.Edge{}
	c.acmeEdge(c.Store.snapshot(), e)
	if len(e.Certificates) != 1 || !strings.Contains(e.Certificates[0].KeyPEM, "secret") || e.Challenges["tok"] != "tok.auth" {
		t.Fatalf("edge certificates/challenges: %+v", e)
	}
}

func TestACMEBackoff(t *testing.T) {
	for f, want := range map[int]time.Duration{1: time.Minute, 2: 3 * time.Minute, 3: 9 * time.Minute, 4: 27 * time.Minute, 5: time.Hour, 9: time.Hour} {
		if got := acmeBackoff(f); got != want {
			t.Fatalf("backoff(%d) = %v, want %v", f, got, want)
		}
	}
}
