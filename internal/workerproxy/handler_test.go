package workerproxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkerFullDockerNamespaceStreamsAndStripsControlCredentials(t *testing.T) {
	token := strings.Repeat("t", 32)
	body := bytes.Repeat([]byte("tar-data"), 200000)
	calls := 0
	h := Handler(token, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("full API used the restricted route mux") }), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1.51/build" || r.URL.RawQuery != "t=app" {
			t.Error("version or query lost")
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Zela-Worker") != "" || r.Header.Get("X-Registry-Config") != "registry-credentials" {
			t.Error("credentials not isolated")
		}
		got, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(got, body) {
			t.Error("Docker build archive truncated")
		}
		w.Header().Set("API-Version", "1.51")
		w.WriteHeader(200)
	}))
	r := httptest.NewRequest("POST", "http://worker/docker/v1.51/build?t=app", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Cookie", "control-cookie=private")
	r.Header.Set("X-Zela-Worker", "w1")
	r.Header.Set("X-Registry-Config", "registry-credentials")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if calls != 1 || w.Code != 200 {
		t.Fatalf("full API failed: %d %s", w.Code, w.Body)
	}
}

func TestWorkerFullDockerAPIAuthenticationAndPathValidation(t *testing.T) {
	token := strings.Repeat("t", 32)
	calls := 0
	h := Handler(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(204) }))
	for _, v := range []struct {
		method, path, auth string
		want               int
	}{
		{"GET", "/docker/_ping", "", 401},
		{"HEAD", "/docker/_ping", token, 204},
		{"GET", "/docker/version", token, 204},
		{"POST", "/docker/v1.51/containers/one/exec", token, 204},
		{"POST", "/docker/v1.51/networks/create", token, 204},
		{"POST", "/docker/volumes/create", token, 204},
		{"GET", "/docker/containers/one/stats", token, 204},
		{"POST", "/docker/session", token, 204},
		{"GET", "/docker/containers/../info", token, 404},
		{"GET", "/docker/containers//create", token, 404},
		{"GET", "/docker/zela/metrics", token, 404},
	} {
		r := httptest.NewRequest(v.method, "http://worker"+v.path, nil)
		if v.auth != "" {
			r.Header.Set("Authorization", "Bearer "+v.auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != v.want {
			t.Errorf("%s %s got %d want %d", v.method, v.path, w.Code, v.want)
		}
	}
	if calls != 7 {
		t.Fatal("invalid request reached Docker")
	}
}

func TestWorkerProxyAllowsOnlyRequiredDockerOperations(t *testing.T) {
	for _, v := range []struct {
		method, path string
		want         bool
	}{{"GET", "/info", true}, {"GET", "/containers/json", true}, {"GET", "/containers/abc/json", true}, {"GET", "/containers/abc/logs", true}, {"POST", "/containers/create", true}, {"POST", "/containers/abc/start", true}, {"POST", "/containers/abc/stop", true}, {"POST", "/images/create", true}, {"DELETE", "/containers/abc", true}, {"GET", "/volumes/zela-data", true}, {"POST", "/volumes/create", true}, {"DELETE", "/volumes/zela-data", false}, {"GET", "/volumes/../data", false}, {"GET", "/secrets", false}, {"POST", "/containers/abc/exec", false}, {"POST", "/swarm/init", false}, {"GET", "/containers/../info", false}, {"POST", "/info", false}, {"DELETE", "/images/abc", false}, {"GET", "/containers/abc/start", false}, {"POST", "/containers/abc/json", false}} {
		if allowed(v.method, v.path) != v.want {
			t.Errorf("%s %s allowed=%v", v.method, v.path, !v.want)
		}
	}
}

func TestWorkerAuthenticationProtectsDockerAndDoesNotForwardToken(t *testing.T) {
	token := strings.Repeat("t", 32)
	calls := 0
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "" {
			t.Fatal("worker credential forwarded to Docker")
		}
		w.WriteHeader(200)
	})
	h := Handler(token, upstream)
	for _, v := range []struct {
		path, auth string
		want       int
	}{{"/info", "", 401}, {"/info", "Bearer wrong", 401}, {"/secrets", "Bearer " + token, 403}, {"/info", "Bearer " + token, 200}} {
		r := httptest.NewRequest("GET", "http://worker.local"+v.path, nil)
		r.Header.Set("Authorization", v.auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != v.want {
			t.Fatalf("%s status %d want %d", v.path, w.Code, v.want)
		}
	}
	if calls != 1 {
		t.Fatalf("unauthorized upstream access: %d calls", calls)
	}
}
func TestMonitoringRouteUsesWorkerAuthenticationAndRejectsProxyBypass(t *testing.T) {
	token := strings.Repeat("t", 32)
	calls := 0
	h := Handler(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/zela/metrics" {
			t.Error("unexpected route")
		}
		w.WriteHeader(200)
	}))
	for _, v := range []struct {
		method, path, auth string
		code               int
	}{{"GET", "/zela/metrics", "", 401}, {"GET", "/zela/metrics", "Bearer " + token, 200}, {"POST", "/zela/metrics", "Bearer " + token, 403}, {"GET", "/containers/one/stats", "Bearer " + token, 403}} {
		r := httptest.NewRequest(v.method, "http://worker"+v.path, nil)
		r.Header.Set("Authorization", v.auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != v.code {
			t.Fatalf("%s %s: %d", v.method, v.path, w.Code)
		}
	}
	if calls != 1 {
		t.Fatal("unauthorized monitoring access")
	}
}
