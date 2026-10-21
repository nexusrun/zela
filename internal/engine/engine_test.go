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

func TestCreateEncodesManagedNamedMount(t *testing.T) {
	c := fakeClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/create" {
			t.Fatalf("unexpected endpoint %s", r.URL)
		}
		var body struct {
			HostConfig struct {
				Mounts []struct {
					Type, Source, Target string
					ReadOnly             bool
				}
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.HostConfig.Mounts) != 1 || body.HostConfig.Mounts[0].Type != "volume" || body.HostConfig.Mounts[0].Source != "zela-data" || body.HostConfig.Mounts[0].Target != "/srv/app" || !body.HostConfig.Mounts[0].ReadOnly {
			t.Fatalf("wrong volume mount: %+v", body.HostConfig.Mounts)
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"Id":"mounted-container"}`))
	})
	_, err := c.Create(context.Background(), Spec{Name: "app", Image: "example:v1", Mounts: []Mount{{Source: "zela-data", Target: "/srv/app", ReadOnly: true}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnsureSharedVolumeCreatesNFSv4AndRejectsConflict(t *testing.T) {
	created := false
	conflicting := false
	c := fakeClient(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/volumes/zela-data" && !created:
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"not found"}`))
		case r.Method == "POST" && r.URL.Path == "/volumes/create":
			var body struct {
				Name, Driver string
				DriverOpts   map[string]string
				Labels       map[string]string
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Name != "zela-data" || body.Driver != "local" || body.DriverOpts["type"] != "nfs" || !strings.Contains(body.DriverOpts["o"], "nfsvers=4") || body.DriverOpts["device"] != ":/exports/app" || body.Labels["zela.managed.shared-volume"] != "true" {
				t.Fatalf("wrong volume create request: %+v", body)
			}
			created = true
			w.WriteHeader(201)
			w.Write([]byte(`{"Name":"zela-data"}`))
		case r.Method == "GET" && r.URL.Path == "/volumes/zela-data" && created:
			if conflicting {
				w.Write([]byte(`{"Driver":"local","Options":{"type":"nfs","o":"addr=nas.internal,nfsvers=4,hard,timeo=600,retrans=2,ro","device":":/exports/app"},"Labels":{"zela.managed.shared-volume":"true"}}`))
			} else {
				w.Write([]byte(`{"Driver":"local","Options":{"type":"nfs","o":"addr=nas.internal,nfsvers=4,hard,timeo=600,retrans=2,rw","device":":/exports/app"},"Labels":{"zela.managed.shared-volume":"true"}}`))
			}
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
		}
	})
	spec := SharedVolumeSpec{Name: "zela-data", Server: "nas.internal", Export: "/exports/app"}
	if err := c.EnsureSharedVolume(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureSharedVolume(context.Background(), spec); err != nil {
		t.Fatalf("idempotent prepare failed: %v", err)
	}
	conflicting = true
	if err := c.EnsureSharedVolume(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "conflicting definition") {
		t.Fatalf("conflicting existing volume was not rejected: %v", err)
	}
}

func TestResolveImageReturnsImmutableRepositoryDigest(t *testing.T) {
	c := fakeClient(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || !strings.HasPrefix(r.URL.Path, "/docker/images/") || !strings.HasSuffix(r.URL.Path, "/json") {
			t.Fatalf("unexpected image inspect route: %s", r.URL)
		}
		w.Write([]byte(`{"Id":"sha256:config","RepoDigests":["docker.io/library/nginx@sha256:manifest"]}`))
	})
	got, err := c.ResolveImage(context.Background(), "nginx:alpine")
	if err != nil || got != "docker.io/library/nginx@sha256:manifest" {
		t.Fatalf("got %q, %v", got, err)
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

func TestSharedVolumeCreationRaceCannotReuseConflictingVolume(t *testing.T) {
	inspections := 0
	c := fakeClient(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(201)
			w.Write([]byte(`{"Name":"zela-data"}`))
			return
		}
		inspections++
		if inspections == 1 {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"Driver":"local","Options":{"type":"none","o":"bind","device":"/empty-local-directory"}}`))
	})
	if err := c.EnsureSharedVolume(context.Background(), SharedVolumeSpec{Name: "zela-data", Server: "nas.internal", Export: "/exports/app"}); err == nil {
		t.Fatal("accepted a local fallback created by a racing request")
	}
	if inspections != 2 {
		t.Fatal("did not inspect volume after creation")
	}
}

func TestContainerInventoryRetainsMountDetailsWithoutHostSourcePaths(t *testing.T) {
	c := fakeClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/json" {
			t.Fatal(r.URL.Path)
		}
		w.Write([]byte(`[{"Id":"app","Mounts":[{"Type":"bind","Source":"/private/host-config","Destination":"/config","RW":false},{"Type":"volume","Name":"zela-data","Source":"/var/lib/docker/volumes/zela-data/_data","Destination":"/data","RW":true}]}]`))
	})
	containers, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 1 || len(containers[0].Mounts) != 2 {
		t.Fatalf("missing mount observations: %+v", containers)
	}
	if containers[0].Mounts[0].Destination != "/config" || containers[0].Mounts[0].RW || containers[0].Mounts[1].Name != "zela-data" || !containers[0].Mounts[1].RW {
		t.Fatalf("incorrect mount observations: %+v", containers[0].Mounts)
	}
	encoded, err := json.Marshal(containers)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("/private/host-config")) || bytes.Contains(encoded, []byte("/var/lib/docker")) {
		t.Fatal("host source paths exposed in inventory")
	}
}
