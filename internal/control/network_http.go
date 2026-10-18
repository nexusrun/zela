package control

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/zela-dev/zela/internal/engine"
)

// All routes use the existing administrator-only API policy and CSRF checks.
func (c *Controller) registerNetworkAPI(api func(string, http.HandlerFunc), reply func(http.ResponseWriter, int, any), fail func(http.ResponseWriter, int, error), decode func(http.ResponseWriter, *http.Request, any) bool, operation func(http.ResponseWriter, Operation, error)) {
	api("GET /api/network", func(w http.ResponseWriter, r *http.Request) {
		s := c.Store.snapshot()
		records := map[string][]string{}
		if s.Network != nil {
			records, _ = networkRecords(s)
		}
		members := map[string]any{}
		if s.Network != nil {
			for id, m := range s.Network.Members {
				worker := s.Workers[id]
				status := "pending"
				if !s.Network.Enabled {
					status = "removing"
				} else if worker.Status != "online" {
					status = "offline"
				} else if m.Error != "" {
					status = "error"
				} else if desired, err := c.networkConfig(s, id); err == nil && m.Applied == desired.Hash() {
					status = "applied"
				}
				endpoint := ""
				if host := networkHost(worker); host != "" {
					endpoint = net.JoinHostPort(host, fmt.Sprint(s.Network.Port))
				}
				members[id] = map[string]any{"status": status, "endpoint": endpoint, "applied": m.Applied}
			}
		}
		reply(w, 200, map[string]any{"members": members, "observedAt": time.Now().UTC(), "network": s.Network, "records": records, "certificates": acmePublic(s.ACME), "automaticCertificates": c.acme != nil})
	})
	api("POST /api/network", func(w http.ResponseWriter, r *http.Request) {
		var in NetworkConfiguration
		if !decode(w, r, &in) {
			return
		}
		op, err := c.ConfigureNetwork(in)
		operation(w, op, err)
	})
	api("POST /api/network/reapply", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			WorkerID string `json:"workerID"`
		}
		if !decode(w, r, &in) {
			return
		}
		op, err := c.ReapplyNetwork(in.WorkerID)
		operation(w, op, err)
	})
	api("POST /api/workers/{id}/network-endpoint", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Endpoint string `json:"endpoint"`
		}
		if !decode(w, r, &in) {
			return
		}
		op, err := c.UpdateNetworkEndpoint(r.PathValue("id"), in.Endpoint)
		operation(w, op, err)
	})
	api("GET /api/workers/{id}/networks", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := c.Store.snapshot().Workers[r.PathValue("id")]; !ok {
			fail(w, 404, fmt.Errorf("worker not found"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		manager, err := c.workerNetworkManager(ctx, r.PathValue("id"))
		if err != nil {
			fail(w, 502, err)
			return
		}
		networks, err := manager.Networks(ctx)
		if err != nil {
			fail(w, 502, err)
			return
		}
		if networks == nil {
			networks = []engine.Network{}
		}
		reply(w, 200, map[string]any{"workerID": r.PathValue("id"), "observedAt": time.Now().UTC(), "networks": networks})
	})
	api("GET /api/workers/{id}/networks/{network}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := c.Store.snapshot().Workers[r.PathValue("id")]; !ok {
			fail(w, 404, fmt.Errorf("worker not found"))
			return
		}
		id := r.PathValue("network")
		if !validResourceID(id) {
			fail(w, 400, fmt.Errorf("invalid network ID"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		manager, err := c.workerNetworkManager(ctx, r.PathValue("id"))
		if err != nil {
			fail(w, 502, err)
			return
		}
		network, err := manager.InspectNetwork(ctx, id)
		if err != nil {
			fail(w, 502, err)
			return
		}
		reply(w, 200, network)
	})
	api("POST /api/workers/{id}/networks", func(w http.ResponseWriter, r *http.Request) {
		var in engine.NetworkCreate
		if !decode(w, r, &in) {
			return
		}
		op, err := c.CreateDockerNetwork(r.PathValue("id"), in)
		operation(w, op, err)
	})
	api("POST /api/workers/{id}/networks/{network}/actions", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Action      string `json:"action"`
			ContainerID string `json:"containerID"`
		}
		if !decode(w, r, &in) {
			return
		}
		op, err := c.DockerNetworkAction(r.PathValue("id"), r.PathValue("network"), in.Action, in.ContainerID)
		operation(w, op, err)
	})
}
