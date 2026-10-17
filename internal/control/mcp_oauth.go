package control

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// OAuth records are part of the normal transactional cluster state, so codes
// remain single-use and refresh/revocation survive restarts and HA failover.
type oauthState struct {
	Clients map[string]oauthClient  `json:"clients"`
	Codes   map[string]oauthCode    `json:"codes"`
	Refresh map[string]oauthRefresh `json:"refresh"`
}
type oauthClient struct {
	ID           string    `json:"client_id"`
	Name         string    `json:"client_name"`
	RedirectURIs []string  `json:"redirect_uris"`
	CreatedAt    time.Time `json:"createdAt"`
	// LastUsedAt is the last successful token exchange; zero if never authorized.
	LastUsedAt time.Time `json:"lastUsedAt,omitempty"`
}

const (
	oauthUnusedClientTTL = 24 * time.Hour
	oauthIdleClientTTL   = 31 * 24 * time.Hour // longer than the refresh lifetime
	oauthTokenRetention  = 24 * time.Hour
)

type oauthCode struct {
	ClientID  string    `json:"clientID"`
	Redirect  string    `json:"redirect"`
	Challenge string    `json:"challenge"`
	Scope     string    `json:"scope"`
	Offline   bool      `json:"offline"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type oauthRefresh struct {
	ClientID  string    `json:"clientID"`
	TokenID   string    `json:"tokenID"`
	Scope     string    `json:"scope"`
	ExpiresAt time.Time `json:"expiresAt"`
	Used      bool      `json:"used,omitempty"`
}
type oauthConsent struct {
	ClientID  string `json:"clientID"`
	Redirect  string `json:"redirect"`
	Challenge string `json:"challenge"`
	Scope     string `json:"scope"`
	State     string `json:"state"`
	Nonce     string `json:"nonce"`
	Expires   int64  `json:"expires"`
}

func oauthDigest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func oauthRandom() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
func oauthInit(s *State) *oauthState {
	if s.OAuth == nil {
		s.OAuth = &oauthState{}
	}
	o := s.OAuth
	if o.Clients == nil {
		o.Clients = map[string]oauthClient{}
	}
	if o.Codes == nil {
		o.Codes = map[string]oauthCode{}
	}
	if o.Refresh == nil {
		o.Refresh = map[string]oauthRefresh{}
	}
	for id, v := range o.Codes {
		if time.Now().After(v.ExpiresAt) {
			delete(o.Codes, id)
		}
	}
	for id, v := range o.Refresh {
		if time.Now().After(v.ExpiresAt) {
			delete(o.Refresh, id)
		}
	}
	// Registration is unauthenticated, so abandoned clients must not
	// accumulate up to the registration cap.
	live := map[string]bool{}
	liveTokens := map[string]bool{}
	for _, v := range o.Codes {
		live[v.ClientID] = true
	}
	for _, v := range o.Refresh {
		live[v.ClientID] = true
		liveTokens[v.TokenID] = true
	}
	now := time.Now()
	for id, v := range o.Clients {
		if live[id] {
			continue
		}
		if (v.LastUsedAt.IsZero() && now.Sub(v.CreatedAt) > oauthUnusedClientTTL) || (!v.LastUsedAt.IsZero() && now.Sub(v.LastUsedAt) > oauthIdleClientTTL) {
			delete(o.Clients, id)
		}
	}
	// Expired OAuth-issued access tokens without a refresh grant are dead.
	for id, t := range s.AccessTokens {
		if !t.OAuth || liveTokens[id] {
			continue
		}
		ended := t.RevokedAt
		if ended == nil {
			ended = t.ExpiresAt
		}
		if ended != nil && now.Sub(*ended) > oauthTokenRetention {
			delete(s.AccessTokens, id)
		}
	}
	return o
}
func ValidateMCPConfig(cfg HTTPConfig) error {
	if err := validateMCPLists(cfg); err != nil {
		return err
	}
	if cfg.MCPPublicURL == "" {
		if len(cfg.MCPRedirectURIs) > 0 {
			return fmt.Errorf("MCP OAuth redirect URIs require ZELA_MCP_PUBLIC_URL")
		}
		return nil
	}
	u, err := url.Parse(cfg.MCPPublicURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && oauthLoopback(u.Hostname()))) {
		return fmt.Errorf("ZELA_MCP_PUBLIC_URL must be an HTTPS origin (HTTP allowed only on loopback)")
	}
	for _, raw := range cfg.MCPRedirectURIs {
		if !oauthValidRedirect(raw) {
			return fmt.Errorf("MCP redirect URIs must be absolute HTTPS URLs or HTTP loopback URLs, without userinfo or fragments")
		}
	}
	return nil
}

// validateMCPLists checks host and proxy lists, which apply even without OAuth.
func validateMCPLists(cfg HTTPConfig) error {
	for _, v := range cfg.MCPProvisionHosts {
		if v == "" || strings.ContainsAny(v, " /") && parseCIDROrIP(v) == nil {
			return fmt.Errorf("ZELA_MCP_PROVISION_HOSTS entries must be IPs, CIDRs or hostnames")
		}
	}
	for _, v := range cfg.MCPTrustedProxies {
		if parseCIDROrIP(v) == nil {
			return fmt.Errorf("ZELA_MCP_TRUSTED_PROXIES entries must be IPs or CIDRs")
		}
	}
	return nil
}
func parseCIDROrIP(v string) *net.IPNet {
	if _, n, err := net.ParseCIDR(v); err == nil {
		return n
	}
	if ip := net.ParseIP(v); ip != nil {
		bits := 128
		if ip.To4() != nil {
			ip, bits = ip.To4(), 32
		}
		return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
	}
	return nil
}
func mcpHostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	ip := net.ParseIP(host)
	for _, v := range allowed {
		if n := parseCIDROrIP(v); n != nil {
			if ip != nil && n.Contains(ip) {
				return true
			}
		} else if ip == nil && host != "" && host == strings.ToLower(strings.TrimSuffix(v, ".")) {
			return true
		}
	}
	return false
}

// clientIP uses the nearest untrusted X-Forwarded-For hop when the request
// arrives from a configured proxy; otherwise the connection address.
func clientIP(r *http.Request, trusted []string) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	isTrusted := func(s string) bool {
		addr := net.ParseIP(strings.TrimSpace(s))
		for _, v := range trusted {
			if n := parseCIDROrIP(v); n != nil && addr != nil && n.Contains(addr) {
				return true
			}
		}
		return false
	}
	if !isTrusted(ip) {
		return ip
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop := strings.TrimSpace(hops[i])
		if net.ParseIP(hop) == nil {
			break
		}
		if !isTrusted(hop) {
			return hop
		}
	}
	return ip
}
func oauthLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}
func oauthValidRedirect(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Hostname() != "" && u.User == nil && u.Fragment == "" && (u.Scheme == "https" || (u.Scheme == "http" && oauthLoopback(u.Hostname())))
}

func (c *Controller) registerMCPOAuth(mux *http.ServeMux, cfg HTTPConfig, authenticated func(*http.Request) (principal, bool), sameOrigin func(*http.Request) bool) string {
	if cfg.MCPPublicURL == "" || len(cfg.Token) < 32 || ValidateMCPConfig(cfg) != nil {
		return ""
	}
	origin := strings.TrimSuffix(cfg.MCPPublicURL, "/")
	resource := origin + "/mcp"
	redirects := cfg.MCPRedirectURIs
	if len(redirects) == 0 {
		redirects = []string{"https://chatgpt.com/connector_platform_oauth_redirect", "https://claude.ai/api/mcp/auth_callback", "https://claude.com/api/mcp/auth_callback"}
	}
	jsonReply := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(w http.ResponseWriter, status int, kind, detail string) {
		jsonReply(w, status, map[string]string{"error": kind, "error_description": detail})
	}
	metadata := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		jsonReply(w, 200, map[string]any{"resource": resource, "authorization_servers": []string{origin}, "scopes_supported": []string{"mcp_read", "mcp_manage", "offline_access"}, "bearer_methods_supported": []string{"header"}, "resource_name": "Zela cluster"})
	}
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", metadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", metadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		jsonReply(w, 200, map[string]any{"authorization_response_iss_parameter_supported": true, "issuer": origin, "authorization_endpoint": origin + "/oauth/authorize", "token_endpoint": origin + "/oauth/token", "registration_endpoint": origin + "/oauth/register", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_methods_supported": []string{"none"}, "code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{"mcp_read", "mcp_manage", "offline_access"}})
	})
	var mu sync.Mutex
	attempts := map[string]authAttempt{}
	limited := func(r *http.Request) bool {
		ip := clientIP(r, cfg.MCPTrustedProxies)
		mu.Lock()
		defer mu.Unlock()
		for k, v := range attempts {
			if time.Now().After(v.reset) {
				delete(attempts, k)
			}
		}
		a := attempts[ip]
		if a.reset.IsZero() {
			a.reset = time.Now().Add(time.Minute)
		}
		a.count++
		if len(attempts) >= 4096 && a.count == 1 {
			return true
		}
		attempts[ip] = a
		return a.count > 20
	}
	mux.HandleFunc("POST /oauth/register", func(w http.ResponseWriter, r *http.Request) {
		if limited(r) {
			fail(w, 429, "temporarily_unavailable", "registration rate limit exceeded")
			return
		}
		var in struct {
			Name      string   `json:"client_name"`
			Redirects []string `json:"redirect_uris"`
			Auth      string   `json:"token_endpoint_auth_method"`
			Grants    []string `json:"grant_types"`
			Responses []string `json:"response_types"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		dec := json.NewDecoder(r.Body)
		if dec.Decode(&in) != nil || len(in.Redirects) == 0 || len(in.Redirects) > 8 || len(in.Name) > 80 || (in.Auth != "" && in.Auth != "none") {
			fail(w, 400, "invalid_client_metadata", "provide a client name, allowed redirect URIs and public-client authentication (none)")
			return
		}
		for _, v := range in.Redirects {
			if !slices.Contains(redirects, v) {
				fail(w, 400, "invalid_redirect_uri", "callback is not in ZELA_MCP_REDIRECT_URIS; ask the cluster administrator to allow the exact callback")
				return
			}
		}
		for _, v := range in.Grants {
			if v != "authorization_code" && v != "refresh_token" {
				fail(w, 400, "invalid_client_metadata", "unsupported grant type")
				return
			}
		}
		for _, v := range in.Responses {
			if v != "code" {
				fail(w, 400, "invalid_client_metadata", "only code responses are supported")
				return
			}
		}
		if in.Name == "" {
			in.Name = "MCP client"
		}
		client := oauthClient{ID: newID("mcpclient_"), Name: in.Name, RedirectURIs: in.Redirects, CreatedAt: time.Now().UTC()}
		err := c.Store.update(func(s *State) error {
			o := oauthInit(s)
			if len(o.Clients) >= 512 {
				return fmt.Errorf("OAuth client limit reached")
			}
			o.Clients[client.ID] = client
			return nil
		})
		if err != nil {
			fail(w, 503, "temporarily_unavailable", "client registration unavailable")
			return
		}
		jsonReply(w, 201, map[string]any{"client_id": client.ID, "client_name": client.Name, "redirect_uris": client.RedirectURIs, "token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}})
	})
	sign := func(payload string) string {
		h := hmac.New(sha256.New, []byte(cfg.Token))
		h.Write([]byte("zela-oauth-consent:" + payload))
		return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	}
	clientFor := func(id, redirect string) (oauthClient, bool) {
		c.Store.mu.RLock()
		defer c.Store.mu.RUnlock()
		if c.Store.state.OAuth == nil {
			return oauthClient{}, false
		}
		v, ok := c.Store.state.OAuth.Clients[id]
		return v, ok && slices.Contains(v.RedirectURIs, redirect)
	}
	render := func(w http.ResponseWriter, r *http.Request, consent oauthConsent, client oauthClient) {
		b, _ := json.Marshal(consent)
		payload := base64.RawURLEncoding.EncodeToString(b)
		p, ok := authenticated(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = oauthConsentPage.Execute(w, map[string]any{"Client": client.Name, "Registered": client.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"), "Redirect": consent.Redirect, "Request": payload + "." + sign(payload), "NeedsLogin": !ok || !p.Admin, "Manage": slices.Contains(strings.Fields(consent.Scope), "mcp_manage")})
	}
	mux.HandleFunc("GET /oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		client, ok := clientFor(q.Get("client_id"), q.Get("redirect_uri"))
		if !ok {
			fail(w, 400, "invalid_request", "unregistered client or redirect URI")
			return
		}
		challenge, err := base64.RawURLEncoding.DecodeString(q.Get("code_challenge"))
		if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || err != nil || len(challenge) != 32 || (q.Get("resource") != "" && q.Get("resource") != resource) || len(q.Get("state")) > 2048 {
			fail(w, 400, "invalid_request", "code flow, PKCE S256 and the configured MCP resource are required")
			return
		}
		scope := q.Get("scope")
		if scope == "" {
			scope = "mcp_read mcp_manage offline_access"
		}
		if !validOAuthScopes(scope) {
			fail(w, 400, "invalid_scope", "request mcp_read or mcp_manage, optionally offline_access")
			return
		}
		nonce, err := oauthRandom()
		if err != nil {
			fail(w, 500, "server_error", "cannot create consent request")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "zela_oauth_consent", Value: nonce, Path: "/oauth/authorize", HttpOnly: true, Secure: strings.HasPrefix(origin, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: 600})
		render(w, r, oauthConsent{ClientID: client.ID, Redirect: q.Get("redirect_uri"), Challenge: q.Get("code_challenge"), Scope: scope, State: q.Get("state"), Nonce: nonce, Expires: time.Now().Add(10 * time.Minute).Unix()}, client)
	})
	mux.HandleFunc("POST /oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			fail(w, 403, "access_denied", "cross-origin consent rejected")
			return
		}
		if limited(r) {
			fail(w, 429, "temporarily_unavailable", "authorization rate limit exceeded")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if r.ParseForm() != nil {
			fail(w, 400, "invalid_request", "invalid consent form")
			return
		}
		payload, sig, ok := strings.Cut(r.PostForm.Get("request"), ".")
		b, err := base64.RawURLEncoding.DecodeString(payload)
		var consent oauthConsent
		cookie, cookieErr := r.Cookie("zela_oauth_consent")
		if !ok || subtle.ConstantTimeCompare([]byte(sig), []byte(sign(payload))) != 1 || err != nil || json.Unmarshal(b, &consent) != nil || consent.Expires <= time.Now().Unix() || cookieErr != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(consent.Nonce)) != 1 {
			fail(w, 403, "access_denied", "consent expired or invalid; restart the connection")
			return
		}
		client, ok := clientFor(consent.ClientID, consent.Redirect)
		if !ok {
			fail(w, 400, "invalid_client", "client no longer registered")
			return
		}
		p, loggedIn := authenticated(r)
		if (!loggedIn || !p.Admin) && subtle.ConstantTimeCompare([]byte(r.PostForm.Get("adminToken")), []byte(cfg.Token)) != 1 {
			fail(w, 401, "access_denied", "administrator sign-in required; return and try again")
			return
		}
		redirect, _ := url.Parse(consent.Redirect)
		q := redirect.Query()
		q.Set("state", consent.State)
		q.Set("iss", origin)
		if r.PostForm.Get("decision") != "allow" {
			q.Set("error", "access_denied")
		} else {
			scope := r.PostForm.Get("scope")
			if scope != "mcp_read" && !(scope == "mcp_manage" && slices.Contains(strings.Fields(consent.Scope), "mcp_manage")) {
				fail(w, 400, "invalid_scope", "scope exceeds the authorization request")
				return
			}
			code, err := oauthRandom()
			if err != nil {
				fail(w, 500, "server_error", "cannot issue code")
				return
			}
			err = c.Store.update(func(s *State) error {
				o := oauthInit(s)
				if len(o.Codes) >= 1024 {
					return fmt.Errorf("too many pending authorizations")
				}
				o.Codes[oauthDigest(code)] = oauthCode{ClientID: client.ID, Redirect: consent.Redirect, Challenge: consent.Challenge, Scope: scope, Offline: slices.Contains(strings.Fields(consent.Scope), "offline_access"), ExpiresAt: time.Now().Add(2 * time.Minute)}
				return nil
			})
			if err != nil {
				fail(w, 503, "temporarily_unavailable", "cannot persist authorization")
				return
			}
			q.Set("code", code)
		}
		http.SetCookie(w, &http.Cookie{Name: "zela_oauth_consent", Value: "", Path: "/oauth/authorize", HttpOnly: true, Secure: strings.HasPrefix(origin, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: -1})
		redirect.RawQuery = q.Encode()
		http.Redirect(w, r, redirect.String(), http.StatusSeeOther)
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if limited(r) {
			fail(w, 429, "temporarily_unavailable", "token exchange rate limit exceeded")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if r.ParseForm() != nil {
			fail(w, 400, "invalid_request", "invalid token request")
			return
		}
		form := r.PostForm
		if form.Get("resource") != "" && form.Get("resource") != resource {
			fail(w, 400, "invalid_target", "token is restricted to this MCP resource")
			return
		}
		access, refresh, scope, err := c.exchangeMCPOAuth(form)
		if err != nil {
			fail(w, 400, "invalid_grant", "authorization code or refresh grant is invalid, expired, used or revoked")
			return
		}
		result := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 3600, "scope": scope}
		if refresh != "" {
			result["refresh_token"] = refresh
			result["scope"] = scope + " offline_access"
		}
		jsonReply(w, 200, result)
	})
	return origin
}

func validOAuthScopes(scope string) bool {
	found := false
	for _, v := range strings.Fields(scope) {
		switch v {
		case "mcp_read", "mcp_manage":
			found = true
		case "offline_access":
		default:
			return false
		}
	}
	return found
}

func (c *Controller) exchangeMCPOAuth(form url.Values) (string, string, string, error) {
	secret, err := oauthRandom()
	if err != nil {
		return "", "", "", err
	}
	nextRefresh, err := oauthRandom()
	if err != nil {
		return "", "", "", err
	}
	var access, scope, oldID string
	var replay bool
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	err = c.Store.update(func(s *State) error {
		o := oauthInit(s)
		client, ok := o.Clients[form.Get("client_id")]
		if !ok {
			return fmt.Errorf("invalid client")
		}
		var offline bool
		id := newID("tok_")
		refreshExpiry := time.Now().Add(30 * 24 * time.Hour)
		switch form.Get("grant_type") {
		case "authorization_code":
			digest := oauthDigest(form.Get("code"))
			code, ok := o.Codes[digest]
			verifier := form.Get("code_verifier")
			sum := sha256.Sum256([]byte(verifier))
			challenge := base64.RawURLEncoding.EncodeToString(sum[:])
			if !ok || code.ClientID != client.ID || code.Redirect != form.Get("redirect_uri") || len(verifier) < 43 || len(verifier) > 128 || !validPKCEVerifier(verifier) || subtle.ConstantTimeCompare([]byte(challenge), []byte(code.Challenge)) != 1 {
				return fmt.Errorf("invalid code")
			}
			scope, offline = code.Scope, code.Offline
			delete(o.Codes, digest)
		case "refresh_token":
			digest := oauthDigest(form.Get("refresh_token"))
			grant, ok := o.Refresh[digest]
			if !ok || grant.ClientID != client.ID {
				return fmt.Errorf("invalid refresh grant")
			}
			token, ok := s.AccessTokens[grant.TokenID]
			if !ok || token.RevokedAt != nil {
				return fmt.Errorf("revoked grant")
			}
			oldID = grant.TokenID
			if grant.Used {
				now := time.Now().UTC()
				token.RevokedAt = &now
				s.AccessTokens[grant.TokenID] = token
				for key, v := range o.Refresh {
					if v.TokenID == grant.TokenID {
						delete(o.Refresh, key)
					}
				}
				replay = true
				return nil // Commit family revocation, then reject the replay.
			}
			if requested := form.Get("scope"); requested != "" && requested != grant.Scope && requested != grant.Scope+" offline_access" {
				return fmt.Errorf("scope change requires consent")
			}
			id, scope, offline, refreshExpiry = grant.TokenID, grant.Scope, true, grant.ExpiresAt
			grant.Used = true
			o.Refresh[digest] = grant
		default:
			return fmt.Errorf("unsupported grant")
		}
		if s.AccessTokens == nil {
			s.AccessTokens = map[string]accessTokenRecord{}
		}
		access = "zela_mcp_" + strings.TrimPrefix(id, "tok_") + "_" + secret
		record := mcpTokenRecord(access, id, "MCP: "+client.Name, scope, time.Now().UTC().Add(time.Hour))
		if old, ok := s.AccessTokens[id]; ok {
			record.CreatedAt = old.CreatedAt
		}
		s.AccessTokens[id] = record
		client.LastUsedAt = time.Now().UTC()
		o.Clients[client.ID] = client
		if offline {
			o.Refresh[oauthDigest(nextRefresh)] = oauthRefresh{ClientID: client.ID, TokenID: id, Scope: scope, ExpiresAt: refreshExpiry}
		} else {
			nextRefresh = ""
		}
		return nil
	})
	if err == nil && oldID != "" {
		for _, cancel := range c.tokenRequests[oldID] {
			cancel()
		}
	}
	if err != nil || replay {
		return "", "", "", fmt.Errorf("invalid OAuth grant")
	}
	return access, nextRefresh, scope, nil
}
func validPKCEVerifier(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~", r)) {
			return false
		}
	}
	return true
}

var oauthConsentPage = template.Must(template.New("oauth").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Connect Zela cluster</title><link rel="stylesheet" href="/oauth.css"></head><body><main><p class="eyebrow">Zela CLUSTER CONTROL</p><h1>Connect your cluster</h1><p><strong>{{.Client}}</strong> is requesting access to this cluster.</p><p class="warning">This name was supplied by the client and is not verified. Approve only a connection you started yourself, just now, from your own AI client.</p><p class="callback">Return address: {{.Redirect}}<br>Client registered: {{.Registered}}</p><form action="/oauth/authorize" method="post"><input type="hidden" name="request" value="{{.Request}}">{{if .NeedsLogin}}<label>Administrator token<input type="password" name="adminToken" required autocomplete="off" spellcheck="false"></label><p>Sent only to this cluster to approve the connection. The AI client receives a separate, revocable credential.</p>{{end}}<fieldset><legend>Connection permissions</legend><label class="choice"><input type="radio" name="scope" value="mcp_read" checked> Read cluster health, inventory, logs and activity</label>{{if .Manage}}<label class="choice"><input type="radio" name="scope" value="mcp_manage"> Manage deployments, containers and workers</label>{{end}}</fieldset><p>Management can stop or delete workloads, drain hosts and provision existing SSH hosts using saved keys. Private keys and token administration are excluded. Access lasts one hour; requested offline access can renew it for up to 30 days. Revoke the connection in Settings &amp; setup → Integration API tokens.</p><div class="actions"><button name="decision" value="allow">Connect cluster</button><button class="secondary" name="decision" value="deny" formnovalidate>Cancel</button></div></form></main></body></html>`))
