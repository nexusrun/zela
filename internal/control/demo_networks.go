package control

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/zela-dev/zela/internal/engine"
)

func (d *DemoEngine) initNetworks() {
	if d.networks != nil {
		return
	}
	d.networks = map[string]engine.Network{}
	for _, name := range []string{"bridge", "host", "none"} {
		n := engine.Network{ID: "demo-network-" + name, Name: name, Driver: name, Scope: "local", Containers: map[string]engine.NetworkEndpoint{}}
		if name == "none" {
			n.Driver = "null"
		}
		if name == "bridge" {
			for id, ct := range d.containers {
				if mode := d.specs[id].Network; mode == "" || mode == "bridge" {
					n.Containers[id] = engine.NetworkEndpoint{Name: firstName(ct)}
				}
			}
			n.IPAM.Config = []engine.NetworkIPAM{{Subnet: "172.17.0.0/16", Gateway: "172.17.0.1"}}
		}
		d.networks[n.ID] = n
	}
}
func copyDemoNetwork(n engine.Network) engine.Network {
	b, _ := json.Marshal(n)
	var out engine.Network
	_ = json.Unmarshal(b, &out)
	return out
}
func (d *DemoEngine) Networks(context.Context) ([]engine.Network, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.initNetworks()
	out := []engine.Network{}
	for _, n := range d.networks {
		out = append(out, copyDemoNetwork(n))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (d *DemoEngine) InspectNetwork(_ context.Context, id string) (engine.Network, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.initNetworks()
	for _, n := range d.networks {
		if n.ID == id || n.Name == id {
			return copyDemoNetwork(n), nil
		}
	}
	return engine.Network{}, fmt.Errorf("network not found")
}
func (d *DemoEngine) CreateNetwork(_ context.Context, in engine.NetworkCreate) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.initNetworks()
	for _, n := range d.networks {
		if n.Name == in.Name {
			return "", fmt.Errorf("network name already exists")
		}
	}
	n := engine.Network{ID: newID("net_demo_"), Name: in.Name, Driver: "bridge", Scope: "local", Internal: in.Internal, Containers: map[string]engine.NetworkEndpoint{}}
	if in.Subnet != "" {
		n.IPAM.Config = []engine.NetworkIPAM{{Subnet: in.Subnet, Gateway: in.Gateway}}
	}
	d.networks[n.ID] = n
	return n.ID, nil
}
func (d *DemoEngine) RemoveNetwork(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.initNetworks()
	n, ok := d.networks[id]
	if !ok {
		return fmt.Errorf("network not found")
	}
	if len(n.Containers) > 0 {
		return fmt.Errorf("network has connected containers")
	}
	delete(d.networks, id)
	return nil
}
func (d *DemoEngine) ConnectNetwork(_ context.Context, id, container string, disconnect bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.initNetworks()
	n, ok := d.networks[id]
	if !ok {
		return fmt.Errorf("network not found")
	}
	ct, ok := d.containers[container]
	if !ok {
		return fmt.Errorf("container not found")
	}
	if n.Containers == nil {
		n.Containers = map[string]engine.NetworkEndpoint{}
	}
	if disconnect {
		delete(n.Containers, container)
	} else {
		n.Containers[container] = engine.NetworkEndpoint{Name: firstName(ct)}
	}
	d.networks[id] = n
	return nil
}
