package control

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestMCPProvisionRequiresAllowlistedHost(t *testing.T) {
	c, _ := fixture(t)
	manage, _, err := c.Store.createAccessToken("manage AI", 30, "mcp_manage")
	if err != nil {
		t.Fatal(err)
	}
	body := func(host string) string {
		return `{"name":"w9","ssh":{"host":"` + host + `","port":22,"user":"root","secretID":"sec_missing","hostKey":"ssh-ed25519 AAAA","agentPort":9091}}`
	}
	for _, v := range []struct {
		allowed []string
		host    string
		denied  bool
	}{
		{nil, "10.0.0.5", true},
		{[]string{"10.0.0.0/24"}, "10.0.1.5", true},
		{[]string{"10.0.0.0/24"}, "10.0.0.5", false},
		{[]string{"build.example.com"}, "BUILD.example.com.", false},
		{[]string{"build.example.com"}, "evil.example.com", true},
	} {
		h := Handler(c, HTTPConfig{Token: testToken, MCPProvisionHosts: v.allowed})
		w := rawAPICall(h, manage, "POST", "/api/workers/provision", body(v.host))
		if got := w.Code == 403 && strings.Contains(w.Body.String(), "ZELA_MCP_PROVISION_HOSTS"); got != v.denied {
			t.Fatalf("%v %s: denied=%v (%d %s)", v.allowed, v.host, got, w.Code, w.Body.String())
		}
	}
	// Administrators are unaffected by the MCP allowlist.
	h := Handler(c, HTTPConfig{Token: testToken})
	if w := rawAPICall(h, testToken, "POST", "/api/workers/provision", body("10.9.9.9")); strings.Contains(w.Body.String(), "ZELA_MCP_PROVISION_HOSTS") {
		t.Fatal("admin provisioning was restricted")
	}
}

func TestMCPOAuthPrunesAbandonedClientsAndTokens(t *testing.T) {
	c, _ := fixture(t)
	h := Handler(c, HTTPConfig{Token: testToken, MCPPublicURL: oauthTestOrigin})
	abandoned := oauthRegister(t, h)
	used := oauthRegister(t, h)
	code := oauthApprove(t, h, used)
	tok := oauthToken(t, h, oauthCodeForm(used, code), 200)
	if tok["access_token"] == "" {
		t.Fatal("no token")
	}
	old := time.Now().Add(-48 * time.Hour)
	var tokenID string
	if err := c.Store.update(func(s *State) error {
		o := s.OAuth
		a := o.Clients[abandoned]
		a.CreatedAt = old
		o.Clients[abandoned] = a
		if o.Clients[used].LastUsedAt.IsZero() {
			t.Error("LastUsedAt not recorded")
		}
		for id, r := range s.AccessTokens {
			if r.OAuth {
				tokenID = id
				r.ExpiresAt = &old
				s.AccessTokens[id] = r
			}
		}
		for k := range o.Refresh {
			delete(o.Refresh, k) // simulate an expired refresh grant
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	oauthRegister(t, h) // any registration prunes
	s := c.Store.snapshot()
	if _, ok := s.OAuth.Clients[abandoned]; ok {
		t.Fatal("abandoned client not pruned")
	}
	if _, ok := s.OAuth.Clients[used]; !ok {
		t.Fatal("recently used client pruned")
	}
	if _, ok := s.AccessTokens[tokenID]; ok || tokenID == "" {
		t.Fatal("expired OAuth token not pruned")
	}
}

func TestMCPConsentMarksClientNameUnverified(t *testing.T) {
	c, _ := fixture(t)
	h := Handler(c, HTTPConfig{Token: testToken, MCPPublicURL: oauthTestOrigin})
	client := oauthRegister(t, h)
	hash := sha256.Sum256([]byte(oauthVerifier))
	q := url.Values{"client_id": {client}, "redirect_uri": {oauthTestRedirect}, "response_type": {"code"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "scope": {"mcp_read"}}
	w := oauthRequest(h, "GET", "/oauth/authorize?"+q.Encode(), "", "", nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "not verified") || !strings.Contains(body, "Client registered:") {
		t.Fatalf("consent page lacks provenance warning: %s", body)
	}
}

func rawAPICall(h http.Handler, token, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://control"+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestClientIPTrustedProxies(t *testing.T) {
	r := httptest.NewRequest("POST", "/oauth/token", nil)
	r.RemoteAddr = "10.0.0.2:5555"
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.3")
	if got := clientIP(r, nil); got != "10.0.0.2" {
		t.Fatalf("untrusted proxy: %s", got)
	}
	if got := clientIP(r, []string{"10.0.0.0/24"}); got != "203.0.113.9" {
		t.Fatalf("trusted proxy chain: %s", got)
	}
	r.RemoteAddr = "198.51.100.1:1"
	if got := clientIP(r, []string{"10.0.0.0/24"}); got != "198.51.100.1" {
		t.Fatalf("spoofed header honored: %s", got)
	}
}

func TestMCPListValidation(t *testing.T) {
	for _, bad := range []HTTPConfig{{MCPProvisionHosts: []string{"10.0.0.0/99"}}, {MCPProvisionHosts: []string{"a b"}}, {MCPTrustedProxies: []string{"proxy.example"}}} {
		if ValidateMCPConfig(bad) == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if err := ValidateMCPConfig(HTTPConfig{MCPProvisionHosts: []string{"10.0.0.0/24", "192.0.2.7", "host.example"}, MCPTrustedProxies: []string{"10.1.0.0/16", "::1"}}); err != nil {
		t.Fatal(err)
	}
}
