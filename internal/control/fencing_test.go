package control

import (
	"context"
	"net/http"
	"testing"
	"time"
)

type recordTransport struct{ got http.Header }

func (r *recordTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.got = req.Header.Clone()
	return &http.Response{StatusCode: 200, Body: http.NoBody, Request: req}, nil
}

func TestHAWorkerTransportSendsEpoch(t *testing.T) {
	rec := &recordTransport{}
	tr := &haWorkerTransport{base: rec, check: func() error { return nil }, epoch: 7}
	req, _ := http.NewRequest("GET", "https://w.example/info", nil)
	req.Header.Set(LeaderEpochHeader, "999")
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if rec.got.Get(LeaderEpochHeader) != "7" {
		t.Fatalf("epoch header = %q", rec.got.Get(LeaderEpochHeader))
	}
	if req.Header.Get(LeaderEpochHeader) != "999" {
		t.Fatal("caller's request was mutated")
	}
}

func TestOfflineAlertBackdatedToLastSeen(t *testing.T) {
	c, _ := fixture(t)
	now := time.Now().UTC()
	if err := c.Store.update(func(s *State) error {
		w := s.Workers["w1"]
		w.Status = "offline"
		w.LastSeen = now.Add(-5 * time.Minute)
		s.Workers["w1"] = w
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Store.Monitor.Record(c.Store.snapshot(), now); err != nil {
		t.Fatal(err)
	}
	alerts, _ := c.Store.Monitor.Public()
	for _, a := range alerts {
		if a.Kind == "worker-offline" && a.WorkerID == "w1" && a.Active {
			return
		}
	}
	t.Fatalf("offline alert not raised immediately for a long-unseen worker: %+v", alerts)
}

func TestStaleInventoryIsNotEligible(t *testing.T) {
	w := Worker{ID: "w", Mode: "active", Status: "online"}
	if !eligible(State{}, Deployment{}, w) {
		t.Fatal("never-probed worker should defer to its live probe")
	}
	w.LastSeen = time.Now().Add(-2 * inventoryStaleAfter)
	if eligible(State{}, Deployment{}, w) {
		t.Fatal("stale online worker was eligible")
	}
	c, _ := fixture(t)
	c.Refresh(context.Background())
	if err := c.Store.update(func(s *State) error {
		x := s.Workers["w1"]
		x.LastSeen = time.Now().Add(-2 * inventoryStaleAfter)
		s.Workers["w1"] = x
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Store.Monitor.Record(c.Store.snapshot(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	alerts, _ := c.Store.Monitor.Public()
	for _, a := range alerts {
		if a.Kind == "inventory-stale" && a.WorkerID == "w1" {
			return
		}
	}
	t.Fatal("no inventory-stale alert")
}
