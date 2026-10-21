// Package workerproxy exposes authenticated Docker management and full Engine API routes.
package workerproxy

import (
	"crypto/subtle"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/zela-dev/zela/internal/dockerapi"
)

var containerRoute = regexp.MustCompile(`^/containers/[a-zA-Z0-9_.-]+/(json|logs|start|stop|restart)$`)
var removeRoute = regexp.MustCompile(`^/containers/[a-zA-Z0-9_.-]+$`)
var volumeRoute = regexp.MustCompile(`^/volumes/[a-zA-Z0-9_.-]+$`)

func allowed(method, path string) bool {
	if method == "GET" {
		return path == "/zela/metrics" || path == "/zela/network" || path == "/info" || path == "/containers/json" || volumeRoute.MatchString(path) || (containerRoute.MatchString(path) && (strings.HasSuffix(path, "/json") || strings.HasSuffix(path, "/logs")))
	}
	if method == "POST" {
		return path == "/zela/storage/verify" || path == "/containers/create" || path == "/images/create" || path == "/volumes/create" || (containerRoute.MatchString(path) && (strings.HasSuffix(path, "/start") || strings.HasSuffix(path, "/stop") || strings.HasSuffix(path, "/restart")))
	}
	if method == "PUT" {
		return path == "/zela/network"
	}
	return method == "DELETE" && removeRoute.MatchString(path)
}

func Handler(token string, upstream http.Handler, engineProxy ...http.Handler) http.Handler {
	dockerUpstream := upstream
	if len(engineProxy) > 0 {
		dockerUpstream = engineProxy[0]
	}
	fence := &epochFence{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, `{"message":"Unauthorized"}`, 401)
			return
		}
		if !fence.admit(r.Header.Get(LeaderEpochHeader)) {
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"message":"Request from a superseded control-plane leader"}`, http.StatusConflict)
			return
		}
		r.Header.Del(LeaderEpochHeader)
		if strings.HasPrefix(r.URL.Path, "/docker/") {
			if _, ok := dockerapi.EnginePath(strings.TrimPrefix(r.URL.Path, "/docker")); !ok {
				w.Header().Set("Content-Type", "application/json")
				http.Error(w, `{"message":"Invalid Docker API path"}`, 404)
				return
			}
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Time{})
			_ = rc.SetWriteDeadline(time.Time{})
			r.Header.Del("Authorization")
			r.Header.Del("Cookie")
			r.Header.Del("X-Zela-Worker")
			http.StripPrefix("/docker", dockerUpstream).ServeHTTP(w, r)
			return
		}
		if !allowed(r.Method, r.URL.Path) {
			http.Error(w, `{"message":"Endpoint not allowed"}`, 403)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		r.Header.Del("Authorization")
		upstream.ServeHTTP(w, r)
	})
}

// LeaderEpochHeader matches control.LeaderEpochHeader.
const LeaderEpochHeader = "X-Zela-Leader-Epoch"

// epochFence remembers the highest HA leadership epoch seen. Requests without
// an epoch (single-manager clusters) are admitted. The high-water mark is held
// in memory, so a worker restart re-learns it from the current leader's
// promotion refresh.
type epochFence struct{ max atomic.Int64 }

func (f *epochFence) admit(header string) bool {
	if header == "" {
		return true
	}
	epoch, err := strconv.ParseInt(header, 10, 64)
	if err != nil || epoch <= 0 {
		return false
	}
	for {
		current := f.max.Load()
		if epoch < current {
			return false
		}
		if epoch == current || f.max.CompareAndSwap(current, epoch) {
			return true
		}
	}
}

// DockerProxy is shared by the worker binary and the gateway integration tests.
func DockerProxy(transport http.RoundTripper) *httputil.ReverseProxy {
	target, _ := url.Parse("http://docker")
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"message":"Docker socket unavailable"}`, http.StatusBadGateway)
	}
	return proxy
}
