package control

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const oauthTestOrigin = "https://cluster.example"
const oauthTestRedirect = "https://claude.ai/api/mcp/auth_callback"
const oauthVerifier = "abcdefghijklmnopqrstuvwxyz0123456789-._~ABCDEFG"

func oauthRequest(h http.Handler, method, path, body, content string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, oauthTestOrigin+path, strings.NewReader(body))
	if content != "" {
		r.Header.Set("Content-Type", content)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func oauthRegister(t *testing.T, h http.Handler) string {
	t.Helper()
	w := oauthRequest(h, "POST", "/oauth/register", `{"client_name":"Claude test","redirect_uris":["`+oauthTestRedirect+`"],"token_endpoint_auth_method":"none"}`, "application/json", nil)
	if w.Code != 201 {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	var client map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &client)
	return client["client_id"].(string)
}
func oauthAuthorize(t *testing.T, h http.Handler, client, scope string) (url.Values, *http.Cookie) {
	t.Helper()
	hash := sha256.Sum256([]byte(oauthVerifier))
	q := url.Values{"client_id": {client}, "redirect_uri": {oauthTestRedirect}, "response_type": {"code"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "resource": {oauthTestOrigin + "/mcp"}, "scope": {scope}, "state": {"opaque-client-state"}}
	w := oauthRequest(h, "GET", "/oauth/authorize?"+q.Encode(), "", "", nil)
	if w.Code != 200 {
		t.Fatalf("authorize: %d %s", w.Code, w.Body.String())
	}
	match := regexp.MustCompile(`name="request" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
	if len(match) != 2 {
		t.Fatal("consent form missing")
	}
	var cookie *http.Cookie
	for _, v := range w.Result().Cookies() {
		if v.Name == "zela_oauth_consent" {
			cookie = v
		}
	}
	if cookie == nil || !cookie.HttpOnly || !cookie.Secure {
		t.Fatal("consent cookie not protected")
	}
	return url.Values{"request": {match[1]}, "decision": {"allow"}, "scope": {"mcp_manage"}, "adminToken": {testToken}}, cookie
}
func oauthApprove(t *testing.T, h http.Handler, client string) string {
	t.Helper()
	form, cookie := oauthAuthorize(t, h, client, "mcp_manage offline_access")
	w := oauthRequest(h, "POST", "/oauth/authorize", form.Encode(), "application/x-www-form-urlencoded", cookie)
	if w.Code != 303 {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	u, _ := url.Parse(w.Header().Get("Location"))
	if u.Query().Get("state") != "opaque-client-state" || u.Query().Get("code") == "" {
		t.Fatal("missing code/state")
	}
	return u.Query().Get("code")
}
func oauthToken(t *testing.T, h http.Handler, form url.Values, status int) map[string]any {
	t.Helper()
	w := oauthRequest(h, "POST", "/oauth/token", form.Encode(), "application/x-www-form-urlencoded", nil)
	if w.Code != status {
		t.Fatalf("exchange: want %d got %d %s", status, w.Code, w.Body.String())
	}
	var v map[string]any
	if json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatal(w.Body.String())
	}
	return v
}
func oauthCodeForm(client, code string) url.Values {
	return url.Values{"grant_type": {"authorization_code"}, "client_id": {client}, "code": {code}, "redirect_uri": {oauthTestRedirect}, "code_verifier": {oauthVerifier}, "resource": {oauthTestOrigin + "/mcp"}}
}
func oauthRefreshForm(client, refresh string) url.Values {
	return url.Values{"grant_type": {"refresh_token"}, "client_id": {client}, "refresh_token": {refresh}, "resource": {oauthTestOrigin + "/mcp"}}
}

func TestMCPOAuthPKCEConsentRotationAndRevocation(t *testing.T) {
	c, _ := fixture(t)
	h := Handler(c, HTTPConfig{Token: testToken, MCPPublicURL: oauthTestOrigin})
	for _, path := range []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-authorization-server"} {
		w := oauthRequest(h, "GET", path, "", "", nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), oauthTestOrigin) {
			t.Fatal(w.Body.String())
		}
	}
	w := oauthRequest(h, "POST", "/mcp", `{}`, "application/json", nil)
	if w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), "resource_metadata=") {
		t.Fatal("missing OAuth discovery challenge")
	}
	client := oauthRegister(t, h)
	code := oauthApprove(t, h, client)
	form := oauthCodeForm(client, code)
	form.Set("code_verifier", strings.Repeat("x", 43))
	oauthToken(t, h, form, 400)
	form = oauthCodeForm(client, code)
	form.Set("resource", "https://other.example/mcp")
	oauthToken(t, h, form, 400)
	result := oauthToken(t, h, oauthCodeForm(client, code), 200)
	access, refresh := result["access_token"].(string), result["refresh_token"].(string)
	if result["scope"] != "mcp_manage offline_access" || result["expires_in"] != float64(3600) {
		t.Fatal(result)
	}
	tokenAPICall(t, h, access, "GET", "/api/cluster", "", 200)
	req := httptest.NewRequest("GET", oauthTestOrigin+"/api/cluster", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	oldPrincipal, ok := c.bearerPrincipal(req, testToken)
	if !ok {
		t.Fatal("token not recognized")
	}
	oauthToken(t, h, oauthCodeForm(client, code), 400) // Code is single-use.
	// Tokens and OAuth internals are never included in public cluster state.
	public := tokenAPICall(t, h, testToken, "GET", "/api/state", "", 200)
	for _, secret := range []string{access, refresh, code, `"oauth"`, `"digest"`} {
		if strings.Contains(string(public), secret) {
			t.Fatalf("public state leaked %q", secret)
		}
	}
	first := oauthToken(t, h, oauthRefreshForm(client, refresh), 200)
	nextAccess, nextRefresh := first["access_token"].(string), first["refresh_token"].(string)
	if access == nextAccess || refresh == nextRefresh {
		t.Fatal("refresh failed to rotate secrets")
	}
	tokenAPICall(t, h, access, "GET", "/api/cluster", "", 401)
	tokenAPICall(t, h, nextAccess, "GET", "/api/cluster", "", 200)
	if _, cleanup, ok := c.tokenRequest(req, oldPrincipal); ok {
		cleanup()
		t.Fatal("old principal registered after token rotation")
	}
	// Replaying a rotated refresh token revokes its entire connection family.
	oauthToken(t, h, oauthRefreshForm(client, refresh), 400)
	tokenAPICall(t, h, nextAccess, "GET", "/api/cluster", "", 401)
	oauthToken(t, h, oauthRefreshForm(client, nextRefresh), 400)
	// The normal UI revoke action also prevents future refresh grants.
	result = oauthToken(t, h, oauthCodeForm(client, oauthApprove(t, h, client)), 200)
	raw := result["access_token"].(string)
	r := httptest.NewRequest("GET", oauthTestOrigin+"/api/cluster", nil)
	r.Header.Set("Authorization", "Bearer "+raw)
	p, ok := c.bearerPrincipal(r, testToken)
	if !ok {
		t.Fatal("new grant invalid")
	}
	if err := c.revokeAccessToken(p.TokenID); err != nil {
		t.Fatal(err)
	}
	oauthToken(t, h, oauthRefreshForm(client, result["refresh_token"].(string)), 400)
}

func TestMCPOAuthRejectsEscalationAndInvalidConsent(t *testing.T) {
	c, _ := fixture(t)
	h := Handler(c, HTTPConfig{Token: testToken, MCPPublicURL: oauthTestOrigin})
	w := oauthRequest(h, "POST", "/oauth/register", `{"client_name":"evil","redirect_uris":["https://evil.example/callback"]}`, "application/json", nil)
	if w.Code != 400 {
		t.Fatal("unapproved callback registered")
	}
	client := oauthRegister(t, h)
	form, cookie := oauthAuthorize(t, h, client, "mcp_read")
	w = oauthRequest(h, "POST", "/oauth/authorize", form.Encode(), "application/x-www-form-urlencoded", cookie)
	if w.Code != 400 {
		t.Fatal("read authorization escalated")
	}
	form.Set("scope", "mcp_read")
	w = oauthRequest(h, "POST", "/oauth/authorize", form.Encode(), "application/x-www-form-urlencoded", nil)
	if w.Code != 403 {
		t.Fatal("missing consent cookie accepted")
	}
	valid := form.Get("request")
	form.Set("request", valid+"tampered")
	w = oauthRequest(h, "POST", "/oauth/authorize", form.Encode(), "application/x-www-form-urlencoded", cookie)
	if w.Code != 403 {
		t.Fatal("tampered consent accepted")
	}
	form.Set("request", valid)
	form.Set("adminToken", "wrong")
	w = oauthRequest(h, "POST", "/oauth/authorize", form.Encode(), "application/x-www-form-urlencoded", cookie)
	if w.Code != 401 {
		t.Fatal("bad administrator token accepted")
	}
	form.Set("adminToken", testToken)
	w = oauthRequest(h, "POST", "/oauth/authorize", form.Encode(), "application/x-www-form-urlencoded", cookie)
	if w.Code != 303 {
		t.Fatal(w.Body.String())
	}
	u, _ := url.Parse(w.Header().Get("Location"))
	code := u.Query().Get("code")
	other := oauthRegister(t, h)
	wrong := oauthCodeForm(other, code)
	oauthToken(t, h, wrong, 400)
	result := oauthToken(t, h, oauthCodeForm(client, code), 200)
	if _, ok := result["refresh_token"]; ok {
		t.Fatal("issued offline access without consent")
	}
	tokenAPICall(t, h, result["access_token"].(string), "POST", "/api/refresh", "", 403)
}

func TestMCPOAuthPersistenceAndExpiry(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	c := New(s, nil)
	h := Handler(c, HTTPConfig{Token: testToken, MCPPublicURL: oauthTestOrigin})
	client := oauthRegister(t, h)
	code := oauthApprove(t, h, client)
	c.Close()
	s, err = OpenStore(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	c = New(s, nil)
	t.Cleanup(c.Close)
	h = Handler(c, HTTPConfig{Token: testToken, MCPPublicURL: oauthTestOrigin})
	result := oauthToken(t, h, oauthCodeForm(client, code), 200)
	raw, refresh := result["access_token"].(string), result["refresh_token"].(string)
	data, err := os.ReadFile(filepath.Join(dir, "cluster.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{raw, refresh, code} {
		if strings.Contains(string(data), secret) {
			t.Fatal("plaintext OAuth secret persisted")
		}
	}
	// Expired access tokens can still refresh during the authorized offline period.
	err = c.Store.update(func(s *State) error {
		for id, v := range s.AccessTokens {
			expired := time.Now().Add(-time.Minute)
			v.ExpiresAt = &expired
			s.AccessTokens[id] = v
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	tokenAPICall(t, h, raw, "GET", "/api/cluster", "", 401)
	rotated := oauthToken(t, h, oauthRefreshForm(client, refresh), 200)
	err = c.Store.update(func(s *State) error {
		for id, v := range s.OAuth.Refresh {
			v.ExpiresAt = time.Now().Add(-time.Minute)
			s.OAuth.Refresh[id] = v
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	oauthToken(t, h, oauthRefreshForm(client, rotated["refresh_token"].(string)), 400)
}

func TestMCPConfigurationValidation(t *testing.T) {
	for _, raw := range []string{"http://cluster.example", "https://user:secret@cluster.example", "https://cluster.example/mcp", "https://cluster.example?x=1"} {
		if ValidateMCPConfig(HTTPConfig{MCPPublicURL: raw}) == nil {
			t.Fatalf("accepted invalid origin: %s", raw)
		}
	}
	for _, raw := range []string{"https://cluster.example", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		if err := ValidateMCPConfig(HTTPConfig{MCPPublicURL: raw}); err != nil {
			t.Fatal(err)
		}
	}
	if ValidateMCPConfig(HTTPConfig{MCPPublicURL: oauthTestOrigin, MCPRedirectURIs: []string{"http://evil.example/callback"}}) == nil {
		t.Fatal("cleartext callback accepted")
	}
}
