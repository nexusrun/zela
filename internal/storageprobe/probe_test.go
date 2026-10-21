package storageprobe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestProbeIdentityReadOnlyAndWritableAccess(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".zela-identity"), []byte("app-data\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Run(root, "wrong-export", true); err == nil {
		t.Fatal("accepted wrong export identity")
	}
	if err := Run(root, "app-data", true); err != nil {
		t.Fatal(err)
	}
	if err := Run(root, "app-data", false); err == nil {
		t.Fatal("writable check passed without dedicated health directory")
	}
	if err := os.Mkdir(filepath.Join(root, ".zela-health"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := Run(root, "app-data", false); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".zela-health"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("probe left files: %v %v", entries, err)
	}
}

func TestProbeRejectsSymlinkHealthDirectory(t *testing.T) {
	root := t.TempDir()
	health := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, ".zela-identity"), []byte("app-data"), 0644)
	if err := os.Symlink(health, filepath.Join(root, ".zela-health")); err != nil {
		t.Fatal(err)
	}
	if err := Run(root, "app-data", false); err == nil {
		t.Fatal("followed health directory symlink")
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWorkerProbeUsesPinnedImageAndRestrictedHelper(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "stuck-helper"}[running], func(t *testing.T) {
			created := 0
			h := &Handler{Docker: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				switch {
				case strings.HasPrefix(r.URL.Path, "/images/"):
					_ = json.NewEncoder(w).Encode(map[string]any{"OS": "linux", "Architecture": runtime.GOARCH, "Config": map[string]any{"User": "1000:1000", "Healthcheck": map[string]any{"Test": []string{"CMD", "/app", "health"}}}})
				case strings.HasPrefix(r.URL.Path, "/volumes/"):
					w.Write([]byte(`{"Driver":"local","Options":{"type":"nfs","o":"addr=nas.internal,nfsvers=4,hard,timeo=600,retrans=2,rw","device":":/exports/app"},"Labels":{"zela.managed.shared-volume":"true"}}`))
				case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/containers/zela-storage-probe-"):
					if running {
						w.Write([]byte(`{"State":{"Running":true},"Config":{"Labels":{"zela.storage-probe":"true"}}}`))
					} else {
						w.WriteHeader(404)
					}
				case r.URL.Path == "/containers/create":
					created++
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					host := body["HostConfig"].(map[string]any)
					if host["NetworkMode"] != "none" || host["ReadonlyRootfs"] != true || body["Image"] != "sha256:pinned" || body["User"] != nil {
						t.Fatalf("unsafe probe config: %+v", body)
					}
					entry := body["Entrypoint"].([]any)
					if len(entry) != 1 || entry[0] != "/zela-probe" {
						t.Fatal(entry)
					}
					w.Write([]byte(`{"Id":"helper"}`))
				case strings.HasSuffix(r.URL.Path, "/wait"):
					w.Write([]byte(`{"StatusCode":0}`))
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/start"), r.Method == "DELETE":
					w.WriteHeader(204)
				default:
					t.Fatalf("unexpected Docker request: %s %s", r.Method, r.URL)
				}
				return w.Result(), nil
			})}}
			body := `{"volume":{"Name":"zela-data","Server":"nas.internal","Export":"/exports/app"},"identity":"app-data","image":"sha256:pinned"}`
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "/zela/storage/verify", strings.NewReader(body)))
			if running {
				if w.Code != 409 || created != 0 {
					t.Fatalf("duplicated stuck helper: %d %d", w.Code, created)
				}
			} else if w.Code != 204 || created != 1 {
				t.Fatalf("probe failed: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestWorkerProbeRequestCancellationIsBounded(t *testing.T) {
	h := &Handler{Docker: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	r := httptest.NewRequest("POST", "/zela/storage/verify", strings.NewReader(`{"volume":{"Name":"zela-data"},"identity":"app","image":"sha256:pinned"}`)).WithContext(ctx)
	before := time.Now()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if time.Since(before) > time.Second || w.Code != 409 {
		t.Fatalf("unbounded verification: %d", w.Code)
	}
}
