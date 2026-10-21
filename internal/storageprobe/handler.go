package storageprobe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/zela-dev/zela/internal/engine"
)

// Handler runs one bounded helper at a time. A stuck hard-NFS mount can leave a
// Docker helper behind, but cannot accumulate additional helpers for that volume.
// The application image supplies its normal User (UID/GID); only the executable
// is replaced with the trusted static worker binary, with networking disabled.
type Handler struct {
	Docker *http.Client
	mu     sync.Mutex
}

var managedName = regexp.MustCompile(`^zela-[a-zA-Z0-9_-]{1,64}$`)

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.mu.TryLock() {
		http.Error(w, "storage verification is busy; retry later", http.StatusConflict)
		return
	}
	defer h.mu.Unlock()
	var probe engine.SharedVolumeProbe
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&probe); err != nil {
		http.Error(w, "invalid storage verification", 400)
		return
	}
	if !managedName.MatchString(probe.Volume.Name) || probe.Identity == "" || len(probe.Identity) > 128 || (!strings.Contains(probe.Image, "@sha256:") && !strings.HasPrefix(probe.Image, "sha256:")) {
		http.Error(w, "registered volume identity and pinned image are required", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := h.verify(ctx, probe); err != nil {
		http.Error(w, "storage access not verified: "+err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) call(ctx context.Context, method, path string, body any, out any) error {
	var b bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, &b)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.Docker.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Docker helper %s: HTTP %d", method, resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
	}
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return err
}

func (h *Handler) verify(ctx context.Context, p engine.SharedVolumeProbe) error {
	client := &engine.Client{HTTP: h.Docker, URL: "http://docker"}
	meta, err := client.ImageMetadata(ctx, p.Image)
	if err != nil {
		return err
	}
	if meta.OS != "linux" || meta.Architecture != runtime.GOARCH {
		return fmt.Errorf("application image must match the Linux worker architecture")
	}
	if meta.Config.Healthcheck == nil || len(meta.Config.Healthcheck.Test) == 0 || meta.Config.Healthcheck.Test[0] == "NONE" {
		return fmt.Errorf("application image requires a Docker HEALTHCHECK")
	}
	for target := range meta.Config.Volumes {
		approved := false
		for _, allowed := range p.Targets {
			if target == allowed {
				approved = true
			}
		}
		if !approved {
			return fmt.Errorf("image declares an unregistered volume at %s", target)
		}
	}
	if err = client.EnsureSharedVolume(ctx, p.Volume); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(p.Volume.Name))
	name := "zela-storage-probe-" + hex.EncodeToString(hash[:8])

	var previous struct {
		State  struct{ Running bool }
		Config struct{ Labels map[string]string }
	}
	inspectErr := h.call(ctx, "GET", "/containers/"+name+"/json", nil, &previous)
	if inspectErr == nil {
		if previous.Config.Labels["zela.storage-probe"] != "true" || previous.State.Running {
			return fmt.Errorf("previous helper is running or its identity conflicts; inspect %s on this worker", name)
		}
		if err = h.call(ctx, "DELETE", "/containers/"+name+"?v=true", nil, nil); err != nil {
			return err
		}
	} else if !strings.Contains(inspectErr.Error(), "HTTP 404") {
		return inspectErr
	}
	// Always use the same name, even when UID or access mode changes. Docker
	// refuses a second helper if a timed-out helper is still present.
	body := map[string]any{"Image": p.Image, "Entrypoint": []string{"/zela-probe"}, "Cmd": []string{"storage-probe", p.Identity, fmt.Sprint(p.ReadOnly)}, "Env": []string{}, "Healthcheck": map[string]any{"Test": []string{"NONE"}}, "Labels": map[string]string{"zela.storage-probe": "true"}, "HostConfig": map[string]any{"NetworkMode": "none", "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges"}, "PidsLimit": 32, "Memory": int64(64 << 20), "NanoCpus": int64(100000000), "Mounts": []any{map[string]any{"Type": "bind", "Source": executable, "Target": "/zela-probe", "ReadOnly": true}, map[string]any{"Type": "volume", "Source": p.Volume.Name, "Target": "/zela-volume", "ReadOnly": p.ReadOnly}}}}
	var created struct{ ID string }
	if err = h.call(ctx, "POST", "/containers/create?name="+url.QueryEscape(name), body, &created); err != nil {
		return err
	}
	// Never block the worker on cleanup of an uninterruptible NFS syscall.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = h.call(cleanup, "DELETE", "/containers/"+created.ID+"?force=true&v=true", nil, nil)
	}()
	if err = h.call(ctx, "POST", "/containers/"+created.ID+"/start", nil, nil); err != nil {
		return err
	}
	var result struct {
		StatusCode int64
		Error      *struct{ Message string }
	}
	if err = h.call(ctx, "POST", "/containers/"+created.ID+"/wait?condition=not-running", nil, &result); err != nil {
		return err
	}
	if result.StatusCode != 0 || result.Error != nil {
		return fmt.Errorf("identity or application UID/GID access check failed (exit %d)", result.StatusCode)
	}
	return nil
}
