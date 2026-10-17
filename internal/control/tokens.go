package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type AccessTokenMetadata struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Scope     string     `json:"scope"`
	Prefix    string     `json:"prefix"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
}
type accessTokenRecord struct {
	AccessTokenMetadata
	Digest string `json:"digest"`
	// OAuth marks short-lived tokens minted by the MCP OAuth flow; expired ones are pruned.
	OAuth bool `json:"oauth,omitempty"`
}
type principal struct {
	Digest    string
	Admin     bool
	TokenID   string
	Scope     string
	ExpiresAt *time.Time
}
type principalKey struct{}

func requestPrincipal(r *http.Request) principal {
	p, _ := r.Context().Value(principalKey{}).(principal)
	return p
}
func tokenMetadata(records map[string]accessTokenRecord) []AccessTokenMetadata {
	out := make([]AccessTokenMetadata, 0, len(records))
	for _, v := range records {
		out = append(out, v.AccessTokenMetadata)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}
func tokenActive(t accessTokenRecord) bool {
	return t.RevokedAt == nil && (t.ExpiresAt == nil || time.Now().Before(*t.ExpiresAt))
}
func (s *Store) createAccessToken(name string, days int, scopes ...string) (string, AccessTokenMetadata, error) {
	scope := "deployments"
	if len(scopes) > 0 && scopes[0] != "" {
		scope = scopes[0]
	}
	if scope != "deployments" && scope != "infrastructure" && !mcpScope(scope) {
		return "", AccessTokenMetadata{}, fmt.Errorf("scope must be infrastructure, deployments, mcp_read or mcp_manage")
	}
	prefix := "zela_deploy_"
	if scope == "infrastructure" {
		prefix = "zela_infra_"
	}
	if mcpScope(scope) {
		prefix = "zela_mcp_"
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 || days < 0 || days > 365 {
		return "", AccessTokenMetadata{}, fmt.Errorf("provide a name up to 80 characters and an expiry between 0 and 365 days")
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", AccessTokenMetadata{}, err
	}
	id := newID("tok_")
	raw := prefix + strings.TrimPrefix(id, "tok_") + "_" + base64.RawURLEncoding.EncodeToString(secret[:])
	digest := sha256.Sum256([]byte(raw))
	meta := AccessTokenMetadata{ID: id, Name: name, Scope: scope, Prefix: prefix + strings.TrimPrefix(id, "tok_"), CreatedAt: time.Now().UTC()}
	if days > 0 {
		expiry := meta.CreatedAt.Add(time.Duration(days) * 24 * time.Hour)
		meta.ExpiresAt = &expiry
	}
	err := s.update(func(state *State) error {
		if state.AccessTokens == nil {
			state.AccessTokens = map[string]accessTokenRecord{}
		}
		state.AccessTokens[id] = accessTokenRecord{AccessTokenMetadata: meta, Digest: hex.EncodeToString(digest[:])}
		return nil
	})
	if err != nil {
		return "", AccessTokenMetadata{}, err
	}
	return raw, meta, nil
}
func (c *Controller) bearerPrincipal(r *http.Request, admin string) (principal, bool) {
	headers := r.Header.Values("Authorization")
	if len(headers) != 1 {
		return principal{}, false
	}
	value := headers[0]
	if len(admin) >= 32 && subtle.ConstantTimeCompare([]byte(value), []byte("Bearer "+admin)) == 1 {
		return principal{Admin: true}, true
	}
	if len(value) > 256 || (!strings.HasPrefix(value, "Bearer zela_deploy_") && !strings.HasPrefix(value, "Bearer zela_infra_") && !strings.HasPrefix(value, "Bearer zela_mcp_")) {
		return principal{}, false
	}
	raw := strings.TrimPrefix(value, "Bearer ")
	parts := strings.Split(raw, "_")
	if len(parts) < 4 || len(parts[2]) != 16 {
		return principal{}, false
	}
	c.Store.mu.RLock()
	record, ok := c.Store.state.AccessTokens["tok_"+parts[2]]
	c.Store.mu.RUnlock()
	digest := sha256.Sum256([]byte(raw))
	valid := subtle.ConstantTimeCompare([]byte(record.Digest), []byte(hex.EncodeToString(digest[:]))) == 1
	if !ok || !valid || !tokenActive(record) || (record.Scope != "deployments" && record.Scope != "infrastructure" && !mcpScope(record.Scope)) {
		return principal{}, false
	}
	return principal{Digest: record.Digest, TokenID: record.ID, Scope: record.Scope, ExpiresAt: record.ExpiresAt}, true
}

// Revalidation and registration share the revocation lock so a request cannot
// register a live stream after its token has been revoked.
func (c *Controller) tokenRequest(r *http.Request, p principal) (*http.Request, func(), bool) {
	if p.Admin {
		return r.WithContext(context.WithValue(r.Context(), principalKey{}, p)), func() {}, true
	}
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.Store.mu.RLock()
	record, ok := c.Store.state.AccessTokens[p.TokenID]
	c.Store.mu.RUnlock()
	if !ok || !tokenActive(record) || subtle.ConstantTimeCompare([]byte(record.Digest), []byte(p.Digest)) != 1 {
		return r, func() {}, false
	}
	p.Scope = record.Scope
	ctx, cancel := context.WithCancel(r.Context())
	if record.ExpiresAt != nil {
		cancel()
		ctx, cancel = context.WithDeadline(r.Context(), *record.ExpiresAt)
	}
	c.nextTokenRequest++
	id := c.nextTokenRequest
	if c.tokenRequests == nil {
		c.tokenRequests = map[string]map[uint64]context.CancelFunc{}
	}
	if c.tokenRequests[p.TokenID] == nil {
		c.tokenRequests[p.TokenID] = map[uint64]context.CancelFunc{}
	}
	c.tokenRequests[p.TokenID][id] = cancel
	cleanup := func() {
		cancel()
		c.tokenMu.Lock()
		delete(c.tokenRequests[p.TokenID], id)
		if len(c.tokenRequests[p.TokenID]) == 0 {
			delete(c.tokenRequests, p.TokenID)
		}
		c.tokenMu.Unlock()
	}
	return r.WithContext(context.WithValue(ctx, principalKey{}, p)), cleanup, true
}
func (c *Controller) revokeAccessToken(id string) error {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	err := c.Store.update(func(s *State) error {
		t, ok := s.AccessTokens[id]
		if !ok {
			return fmt.Errorf("token not found")
		}
		if t.RevokedAt == nil {
			now := time.Now().UTC()
			t.RevokedAt = &now
			s.AccessTokens[id] = t
		}
		return nil
	})
	if err == nil {
		for _, cancel := range c.tokenRequests[id] {
			cancel()
		}
	}
	return err
}
func ownsContainer(s State, p principal, id string) bool {
	if p.Admin {
		return true
	}
	if p.TokenID == "" {
		return false
	}
	if ct, ok := s.DockerContainers[id]; ok && ct.AccessTokenID == p.TokenID {
		return true
	}
	for _, d := range s.Deployments {
		if d.AccessTokenID == p.TokenID {
			for _, task := range d.Tasks {
				if task.ID == id {
					return true
				}
			}
		}
	}
	return false
}
func (c *Controller) deploymentAPIAccess(pattern string, r *http.Request, p principal) bool {
	if p.Admin {
		return true
	}
	if mcpScope(p.Scope) {
		return mcpAPIAccess(pattern, p.Scope == "mcp_manage")
	}
	switch pattern {
	case "GET /api/cluster", "GET /api/cluster/workers", "GET /api/cluster/workers/{id}":
		return true
	}
	if p.Scope == "infrastructure" {
		return false
	}
	switch pattern {
	case "POST /api/deployments", "GET /api/deployments":
		return true
	case "GET /api/deployments/{id}", "POST /api/deployments/{id}/actions", "POST /api/deployments/{id}/replicas/{replica}/move", "POST /api/deployments/{id}/image", "PUT /api/deployments/{id}/routes":
		d, ok := c.Store.snapshot().Deployments[r.PathValue("id")]
		return ok && d.AccessTokenID == p.TokenID
	case "GET /api/operations/{id}":
		for _, op := range c.Store.snapshot().Operations {
			if op.ID == r.PathValue("id") {
				return op.AccessTokenID == p.TokenID
			}
		}
	}
	return false
}
