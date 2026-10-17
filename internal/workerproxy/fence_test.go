package workerproxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLeaderEpochFence(t *testing.T) {
	const token = "worker-token-at-least-32-characters-long"
	var seen []string
	up := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get(LeaderEpochHeader))
	})
	h := Handler(token, up)
	call := func(epoch string) int {
		r := httptest.NewRequest("GET", "/info", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		if epoch != "" {
			r.Header.Set(LeaderEpochHeader, epoch)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for _, v := range []struct {
		epoch string
		code  int
	}{{"", 200}, {"5", 200}, {"5", 200}, {"4", 409}, {"6", 200}, {"5", 409}, {"", 200}, {"x", 409}, {"0", 409}} {
		if got := call(v.epoch); got != v.code {
			t.Fatalf("epoch %q: got %d want %d", v.epoch, got, v.code)
		}
	}
	for _, s := range seen {
		if s != "" {
			t.Fatal("epoch header forwarded to Docker")
		}
	}
}
