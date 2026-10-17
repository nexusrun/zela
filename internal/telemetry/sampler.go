package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

type Sampler struct {
	Collector *Collector
	mu        sync.RWMutex
	latest    Sample
}

func (s *Sampler) Run(ctx context.Context) {
	collect := func() {
		probe, cancel := context.WithTimeout(ctx, 8*time.Second)
		v := s.Collector.Collect(probe)
		cancel()
		s.mu.Lock()
		s.latest = v
		s.mu.Unlock()
	}
	collect()
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			collect()
		}
	}
}
func (s *Sampler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if s.latest.Time.IsZero() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Metrics collector is warming up"})
		return
	}
	_ = json.NewEncoder(w).Encode(s.latest)
}
