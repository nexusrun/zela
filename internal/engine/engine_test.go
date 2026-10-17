package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zela-dev/zela/internal/telemetry"
)

type roundTrip func(*http.Request) (*http.Response, error)

func TestMetricsRequestIsAuthenticatedAndDirectSocketsReportUnsupported(t *testing.T) {
	c := fakeClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zela/metrics" || r.Header.Get("Authorization") == "" {
			t.Fatal("metrics route or token missing")
		}
		_ = json.NewEncoder(w).Encode(telemetry.Sample{Time: time.Now(), Host: &telemetry.Host{MemoryPercent: 25}})
	})
	v, err := c.Metrics(context.Background())
	if err != nil || v.Host.MemoryPercent != 25 {
		t.Fatalf("metrics decode failed: %v", err)
	}
	c.URL = "http://docker"
	if _, err := c.Metrics(context.Background()); err == nil {
		t.Fatal("direct Docker socket claimed host monitoring")
	}
}

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fakeClient(h http.HandlerFunc) *Client {
	return &Client{URL: "http://worker.test", Token: strings.Repeat("t", 32), HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Result(), nil
	})}}
}
func TestCreateSendsDockerResourceLimitsPortsAndAuth(t *testing.T) {
	c := fakeClient(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/containers/create" || r.URL.Query().Get("name") != "web" {
			t.Fatalf("bad route %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 32) {
			t.Fatal("missing auth")
		}
		var b struct {
			Image        string
			Env          []string
			Labels       map[string]string
			ExposedPorts map[string]any
			HostConfig   struct {
				Memory        int64
				NanoCpus      int64
				PortBindings  map[string][]map[string]string
				RestartPolicy map[string]string
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Fatal(err)
		}
		if b.Image != "nginx:alpine" || b.HostConfig.Memory != 256*1024*1024 || b.HostConfig.NanoCpus != 500000000 {
			t.Fatalf("wrong resource config: %+v", b)
		}
		if b.HostConfig.PortBindings["80/tcp"][0]["HostPort"] != "8080" || b.Labels["zela.managed"] != "true" {
			t.Fatal("port or labels missing")
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"Id":"container-id"}`))
	})
	id, err := c.Create(context.Background(), Spec{Name: "web", Image: "nginx:alpine", MemoryMB: 256, CPUs: .5, Ports: []Port{{Host: 8080, Container: 80, Protocol: "tcp"}}, Labels: map[string]string{"zela.managed": "true"}})
	if err != nil || id != "container-id" {
		t.Fatalf("%s %v", id, err)
	}
}
func TestPullDetectsStreamErrorsWithHTTP200(t *testing.T) {
	c := fakeClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fromImage") != "private/image:latest" {
			t.Fatal("image not encoded")
		}
		w.Write([]byte("{\"status\":\"pulling\"}\n{\"error\":\"access denied\"}\n"))
	})
	if err := c.Pull(context.Background(), "private/image:latest"); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("stream error ignored: %v", err)
	}
}
func TestIdempotentStartAndStop(t *testing.T) {
	c := fakeClient(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(304) })
	if err := c.Start(context.Background(), "c"); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(context.Background(), "c"); err != nil {
		t.Fatal(err)
	}
}
func TestDockerLogsDecodeMultiplexedStream(t *testing.T) {
	var payload bytes.Buffer
	for _, v := range []struct {
		stream byte
		text   string
	}{{1, "stdout\n"}, {2, "stderr\n"}} {
		header := make([]byte, 8)
		header[0] = v.stream
		binary.BigEndian.PutUint32(header[4:], uint32(len(v.text)))
		payload.Write(header)
		payload.WriteString(v.text)
	}
	c := fakeClient(func(w http.ResponseWriter, r *http.Request) { w.Write(payload.Bytes()) })
	s, err := c.Logs(context.Background(), "c")
	if err != nil || s != "stdout\nstderr\n" {
		t.Fatalf("bad logs %q %v", s, err)
	}
}
func TestEndpointValidation(t *testing.T) {
	for _, endpoint := range []string{"https://user:pass@host:9091", "https://host/path", "file:///sock", "http://host?x=y", "unix://remote/sock"} {
		if _, err := New(endpoint, strings.Repeat("x", 32)); err == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
	if _, err := New("https://host:9091", ""); err == nil {
		t.Fatal("missing token accepted")
	}
	if _, err := New("unix:///var/run/docker.sock", ""); err != nil {
		t.Fatal(err)
	}
}
func TestRedirectsCannotForwardCredentials(t *testing.T) {
	c, err := New("https://worker.test:9091", strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTP.CheckRedirect == nil {
		t.Fatal("redirect protection missing")
	}
	if err := c.HTTP.CheckRedirect(&http.Request{}, nil); err != http.ErrUseLastResponse {
		t.Fatal("redirect allowed")
	}
}
