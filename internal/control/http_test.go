package control

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "a-long-control-plane-test-token-123456789"

func TestHTTPAuthenticationAndCSRF(t *testing.T) {
	c, _ := fixture(t)
	h := Handler(c, HTTPConfig{Token: testToken})
	req := func(method, path, body, origin, token string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://control.local"+path, strings.NewReader(body))
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := req("GET", "/api/state", "", "", "", nil); w.Code != 401 {
		t.Fatal("unauthenticated state accessible")
	}
	login := req("POST", "/api/login", `{"token":"`+testToken+`"}`, "http://control.local", "", nil)
	if login.Code != 200 {
		t.Fatal(login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe session cookie")
	}
	if w := req("GET", "/api/state", "", "", "", cookies[0]); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := req("POST", "/api/workers/w1/actions", `{"action":"pause"}`, "http://evil.local", "", cookies[0]); w.Code != 403 {
		t.Fatal("cross-origin mutation accepted")
	}
	if w := req("GET", "/api/state", "", "", testToken, nil); w.Code != 200 {
		t.Fatal("API bearer auth rejected")
	}
	tampered := *cookies[0]
	tampered.Value += "x"
	if w := req("GET", "/api/state", "", "", "", &tampered); w.Code != 401 {
		t.Fatal("tampered cookie accepted")
	}
}
func TestHTTPNeverReturnsWorkerCredentials(t *testing.T) {
	c, _ := fixture(t)
	_ = c.Store.update(func(s *State) error {
		w := s.Workers["w1"]
		w.Token = "secret-that-must-not-leak"
		s.Workers["w1"] = w
		return nil
	})
	r := httptest.NewRequest("GET", "http://control.local/api/state", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	Handler(c, HTTPConfig{Token: testToken}).ServeHTTP(w, r)
	if bytes.Contains(w.Body.Bytes(), []byte("secret-that-must-not-leak")) {
		t.Fatal("token leaked")
	}
}
func TestHTTPWorkerRegistrationRejectsUnexpectedFieldsAndPlaintextRemote(t *testing.T) {
	c, _ := fixture(t)
	h := Handler(c, HTTPConfig{Token: testToken})
	for _, body := range []string{`{"name":"w3","endpoint":"http://10.0.0.2:9091","token":"remote-token"}`, `{"name":"w3","endpoint":"unix:///sock","mode":"active"}`, `{"name":"w3"} {"name":"w4"}`} {
		r := httptest.NewRequest("POST", "http://control.local/api/workers", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("body=%s status=%d", body, w.Code)
		}
	}
}
func TestHTTPDeploymentRequestAndEmbeddedAssets(t *testing.T) {
	c, _ := fixture(t)
	h := Handler(c, HTTPConfig{Token: testToken})
	r := httptest.NewRequest("POST", "http://control.local/api/deployments", strings.NewReader(`{"name":"api","image":"nginx:alpine","replicas":1,"cpus":0.5,"memoryMB":128,"workerIDs":["w1"]}`))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	var op Operation
	if err := json.Unmarshal(w.Body.Bytes(), &op); err != nil {
		t.Fatal(err)
	}
	succeeded(t, c, op, nil)
	for _, path := range []string{"/", "/app.js", "/networking.js", "/style.css", "/favicon.svg"} {
		r := httptest.NewRequest("GET", "http://control.local"+path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Fatalf("missing embedded asset %s: %d", path, w.Code)
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatal("missing CSP")
		}
	}
}
func TestHTTPLoginRateLimit(t *testing.T) {
	c, _ := fixture(t)
	h := Handler(c, HTTPConfig{Token: testToken})
	for i := 0; i < 11; i++ {
		r := httptest.NewRequest("POST", "http://control.local/api/login", strings.NewReader(`{"token":"bad"}`))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 401
		if i == 10 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("attempt %d got %d want %d", i, w.Code, want)
		}
	}
}
