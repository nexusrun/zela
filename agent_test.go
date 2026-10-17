package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseMeminfo(t *testing.T) {
	total, used, err := parseMeminfo(strings.NewReader(
		"MemTotal:        8192000 kB\nMemFree:          100000 kB\nMemAvailable:    6144000 kB\n"))
	if err != nil {
		t.Fatal(err)
	}
	if total != 8000 || used != 2000 {
		t.Fatalf("got total=%d used=%d, want 8000 and 2000", total, used)
	}
	if _, _, err := parseMeminfo(strings.NewReader("MemTotal: 1 kB\n")); err == nil {
		t.Fatal("expected an error without MemAvailable")
	}
}

func event(action string, attrs map[string]string) dockerEvent {
	var e dockerEvent
	e.Type, e.Action, e.Time = "container", action, 1760000000
	e.Actor.Attributes = attrs
	return e
}

func TestToContainerEvent(t *testing.T) {
	attrs := map[string]string{"name": "app-1", "zela.deployment.id": "d1", "exitCode": "137"}

	ev, ok := toContainerEvent(event("die", attrs))
	if !ok || ev.Type != "die" || *ev.ExitCode != 137 || ev.DeploymentID != "d1" || ev.ContainerName != "app-1" {
		t.Fatalf("die not mapped: %+v %v", ev, ok)
	}
	if _, ok := toContainerEvent(event("die", map[string]string{"exitCode": "0"})); ok {
		t.Fatal("a clean exit must not be reported")
	}
	if ev, ok := toContainerEvent(event("oom", attrs)); !ok || ev.Type != "oom" {
		t.Fatalf("oom not mapped: %+v", ev)
	}
	if ev, ok := toContainerEvent(event("health_status: unhealthy", attrs)); !ok || ev.Health != "unhealthy" {
		t.Fatalf("unhealthy not mapped: %+v", ev)
	}
	if _, ok := toContainerEvent(event("health_status: healthy", attrs)); ok {
		t.Fatal("a healthy transition must not be reported")
	}
	if _, ok := toContainerEvent(event("start", attrs)); ok {
		t.Fatal("start must not be reported")
	}
}

func TestPushSendsAuthAndBody(t *testing.T) {
	var gotAuth string
	var got heartbeat
	srv := newInMemoryServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/heartbeat" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := config{apiURL: srv.URL, host: "worker1", token: "abc"}
	err := push(context.Background(), srv.Client(), cfg, heartbeat{Host: "worker1", MemUsedMb: 42})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer abc" || got.Host != "worker1" || got.MemUsedMb != 42 {
		t.Fatalf("auth=%q body=%+v", gotAuth, got)
	}
}

func TestPushReportsRejection(t *testing.T) {
	srv := newInMemoryServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	err := push(context.Background(), srv.Client(), config{apiURL: srv.URL}, heartbeat{})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected a 401 error, got %v", err)
	}
}

// These HTTP client tests exercise request/response handling without binding ports.
type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type memoryServer struct {
	URL    string
	client *http.Client
}

func newInMemoryServer(h http.Handler) *memoryServer {
	return &memoryServer{URL: "http://backend.test", client: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Result(), nil
	})}}
}
func (s *memoryServer) Client() *http.Client { return s.client }
func (s *memoryServer) Close()               {}
