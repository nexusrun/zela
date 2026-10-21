package control

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/zela-dev/zela/internal/clusternet"
	"github.com/zela-dev/zela/internal/engine"
)

type NetworkConfiguration struct {
	EdgeDrainTimeoutSeconds int    `json:"edgeDrainTimeoutSeconds,omitempty"`
	Enabled                 bool   `json:"enabled"`
	CIDR                    string `json:"cidr"`
	Port                    int    `json:"port"`
	EdgeHTTPPort            int    `json:"edgeHTTPPort"`
	EdgeHTTPSPort           int    `json:"edgeHTTPSPort"`
	RedirectHTTPS           bool   `json:"redirectHTTPS"`
}

// ConfigureNetwork changes desired state through the normal operation gate.
// Worker application errors are retained per member and reported on the operation.
func (c *Controller) ConfigureNetwork(in NetworkConfiguration) (Operation, error) {
	if in.Port == 0 {
		in.Port = clusternet.DefaultPort
	}
	if in.EdgeDrainTimeoutSeconds < 0 || in.EdgeDrainTimeoutSeconds > 240 {
		return Operation{}, fmt.Errorf("edge drain timeout must be 0 (60 second default) or between 1 and 240 seconds")
	}
	if in.Enabled {
		prefix, err := clusternet.ParseCluster(in.CIDR)
		if err != nil {
			return Operation{}, err
		}
		in.CIDR = prefix.String()
		if in.Port < 1 || in.Port > 65535 || in.EdgeHTTPPort < 0 || in.EdgeHTTPPort > 65535 || in.EdgeHTTPSPort < 0 || in.EdgeHTTPSPort > 65535 || in.EdgeHTTPPort != 0 && in.EdgeHTTPPort == in.EdgeHTTPSPort || in.EdgeHTTPPort == in.Port || in.EdgeHTTPSPort == in.Port {
			return Operation{}, fmt.Errorf("network and edge ports must be valid, distinct ports; zero disables an edge listener")
		}
		if in.RedirectHTTPS && in.EdgeHTTPSPort == 0 {
			return Operation{}, fmt.Errorf("HTTPS redirection requires an HTTPS listener")
		}
	}
	return c.Submit("Configure cluster network", func(s *State) error {
		if s.Demo {
			return fmt.Errorf("the encrypted cluster network requires live Linux worker agents; Docker network controls remain available in demo mode")
		}
		if c.acme != nil && (!in.Enabled || in.EdgeHTTPPort != 80 || in.EdgeHTTPSPort == 0) {
			return fmt.Errorf("automatic certificates require the cluster network, HTTP port 80 and an HTTPS listener; disable ACME at startup before changing these settings")
		}
		old := s.Network
		if !in.Enabled {
			if old != nil {
				for _, d := range s.Deployments {
					if len(d.Tasks) > 0 {
						return fmt.Errorf("remove managed deployments before disabling the cluster network")
					}
				}
				for _, w := range s.Workers {
					for _, ct := range w.Containers {
						if ct.NetworkIP(clusternet.DockerNetwork) != "" {
							return fmt.Errorf("worker %s still has containers attached to the cluster network", w.Name)
						}
					}
				}
				old.Enabled = false
				old.ConsoleManaged = true
			}
			return nil
		}
		if old != nil && len(old.Members) > 0 && (old.CIDR != in.CIDR || old.Port != in.Port) {
			return fmt.Errorf("disable the existing network and wait for worker teardown before changing its address range or WireGuard port")
		}
		if old == nil || old.CIDR != in.CIDR {
			old = &NetworkState{Members: map[string]NetworkMember{}}
			s.Network = old
		}
		old.EdgeDrainTimeoutSeconds = in.EdgeDrainTimeoutSeconds
		old.Enabled, old.CIDR, old.Port, old.ConsoleManaged = true, in.CIDR, in.Port, true
		old.EdgeHTTPPort, old.EdgeHTTPSPort, old.EdgeRedirectHTTPS = in.EdgeHTTPPort, in.EdgeHTTPSPort, in.RedirectHTTPS
		return nil
	}, c.applyNetworkAdministration)
}
func (c *Controller) applyNetworkAdministration(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.reconcileNetwork(ctx)
	s := c.Store.snapshot()
	if s.Network == nil {
		return nil
	}
	errors := []string{}
	for id, member := range s.Network.Members {
		if w := s.Workers[id]; w.Status == "online" && member.Error != "" {
			errors = append(errors, w.Name+": "+member.Error)
		}
	}
	if len(errors) > 0 {
		return fmt.Errorf("desired configuration saved; worker application failed: %s", strings.Join(errors, "; "))
	}
	return ctx.Err()
}
func (c *Controller) ReapplyNetwork(worker string) (Operation, error) {
	return c.Submit("Reapply cluster network", func(s *State) error {
		if s.Network == nil {
			return fmt.Errorf("cluster network is not configured")
		}
		if worker != "" {
			if _, ok := s.Network.Members[worker]; !ok {
				return fmt.Errorf("worker is not a cluster-network member")
			}
		}
		for id, m := range s.Network.Members {
			if worker == "" || worker == id {
				m.Applied = ""
				s.Network.Members[id] = m
			}
		}
		return nil
	}, c.applyNetworkAdministration)
}
func (c *Controller) UpdateNetworkEndpoint(id, endpoint string) (Operation, error) {
	endpoint = strings.TrimSpace(endpoint)
	if !validNetworkEndpoint(endpoint) {
		return Operation{}, fmt.Errorf("peer endpoint must be a hostname or IP address without a scheme or port")
	}
	return c.Submit("Update worker peer endpoint", func(s *State) error {
		w, ok := s.Workers[id]
		if !ok {
			return fmt.Errorf("worker not found")
		}
		w.NetworkEndpoint = endpoint
		s.Workers[id] = w
		return nil
	}, c.applyNetworkAdministration)
}

// ApplyStartupNetwork preserves console-managed settings unless an operator
// explicitly supplied networking flags/environment on this manager.
func (c *Controller) ApplyStartupNetwork(in NetworkConfiguration, explicit bool) error {
	if n := c.Store.snapshot().Network; n != nil && n.ConsoleManaged && !explicit {
		return nil
	}
	cidr := ""
	if in.Enabled {
		cidr = in.CIDR
	}
	if err := c.SetClusterNetwork(cidr, in.Port); err != nil {
		return err
	}
	if err := c.SetEdge(in.EdgeHTTPPort, in.EdgeHTTPSPort, in.RedirectHTTPS); err != nil {
		return err
	}
	return c.Store.update(func(s *State) error {
		if s.Network != nil {
			s.Network.ConsoleManaged = false
		}
		return nil
	})
}

// Read the current desired state when provisioning, without mutating a shared installer.
func (c *Controller) networkProvisioner() Provisioner {
	p, ok := c.Provisioner.(*SSHProvisioner)
	if !ok {
		return c.Provisioner
	}
	clone := *p
	clone.NetworkPort = 0
	clone.EdgePorts = nil
	if n := c.Store.snapshot().Network; n != nil && n.Enabled {
		clone.NetworkPort = n.Port
		clone.EdgePorts = []int{n.EdgeHTTPPort, n.EdgeHTTPSPort}
	}
	return &clone
}

func (c *Controller) workerNetworkManager(ctx context.Context, id string) (engine.NetworkManager, error) {
	e, _, err := c.worker(ctx, id)
	if err != nil {
		return nil, err
	}
	manager, ok := e.(engine.NetworkManager)
	if !ok {
		return nil, fmt.Errorf("this worker connection does not support Docker network administration")
	}
	return manager, nil
}
func protectedDockerNetwork(name string) bool {
	return name == "host" || name == "none" || name == "bridge" || name == clusternet.DockerNetwork
}
func validateDockerNetwork(in *engine.NetworkCreate, s State) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Subnet = strings.TrimSpace(in.Subnet)
	in.Gateway = strings.TrimSpace(in.Gateway)
	if !validName.MatchString(in.Name) || protectedDockerNetwork(in.Name) {
		return fmt.Errorf("choose a custom network name of 1–63 letters, numbers, dots, underscores or hyphens; built-in and cluster networks are reserved")
	}
	if in.Subnet == "" {
		if in.Gateway != "" {
			return fmt.Errorf("a gateway requires an explicit subnet")
		}
		return nil
	}
	prefix, err := netip.ParsePrefix(in.Subnet)
	if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() || prefix.Bits() < 8 || prefix.Bits() > 29 {
		return fmt.Errorf("subnet must be a private IPv4 CIDR between /8 and /29")
	}
	prefix = prefix.Masked()
	privateRange := false
	for _, raw := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		allowed := netip.MustParsePrefix(raw)
		if prefix.Bits() >= allowed.Bits() && allowed.Contains(prefix.Addr()) {
			privateRange = true
		}
	}
	if !privateRange {
		return fmt.Errorf("subnet must fit entirely inside a private IPv4 range")
	}
	in.Subnet = prefix.String()
	if n := s.Network; n != nil {
		mesh, err := netip.ParsePrefix(n.CIDR)
		if err == nil && mesh.Overlaps(prefix) {
			return fmt.Errorf("Docker subnet overlaps the cluster network")
		}
	}
	if in.Gateway != "" {
		gateway, err := netip.ParseAddr(in.Gateway)
		if err != nil || !prefix.Contains(gateway) || gateway == prefix.Addr() {
			return fmt.Errorf("gateway must be a usable IPv4 address in the subnet")
		}
	}
	return nil
}
func (c *Controller) CreateDockerNetwork(worker string, in engine.NetworkCreate) (Operation, error) {
	return c.Submit("Create Docker network "+in.Name, func(s *State) error {
		if _, ok := s.Workers[worker]; !ok {
			return fmt.Errorf("worker not found")
		}
		return validateDockerNetwork(&in, *s)
	}, func(ctx context.Context) error {
		manager, err := c.workerNetworkManager(ctx, worker)
		if err != nil {
			return err
		}
		_, err = manager.CreateNetwork(ctx, in)
		return err
	})
}
func (c *Controller) DockerNetworkAction(worker, id, action, container string) (Operation, error) {
	if !validResourceID(id) || container != "" && !validResourceID(container) {
		return Operation{}, fmt.Errorf("invalid network or container ID")
	}
	if action != "delete" && action != "connect" && action != "disconnect" {
		return Operation{}, fmt.Errorf("unknown network action")
	}
	return c.Submit("Docker network "+action, func(s *State) error {
		if _, ok := s.Workers[worker]; !ok {
			return fmt.Errorf("worker not found")
		}
		return nil
	}, func(ctx context.Context) error {
		manager, err := c.workerNetworkManager(ctx, worker)
		if err != nil {
			return err
		}
		network, err := manager.InspectNetwork(ctx, id)
		if err != nil {
			return err
		}
		if protectedDockerNetwork(network.Name) {
			return fmt.Errorf("built-in and cluster-managed networks cannot be modified here")
		}
		if network.Driver != "bridge" || network.Scope != "local" {
			return fmt.Errorf("only local bridge networks can be modified here")
		}
		if action == "delete" {
			if len(network.Containers) > 0 {
				return fmt.Errorf("disconnect containers before deleting this network")
			}
			return manager.RemoveNetwork(ctx, network.ID)
		}
		if container == "" {
			return fmt.Errorf("select a container")
		}
		e, _, err := c.worker(ctx, worker)
		if err != nil {
			return err
		}
		containers, err := e.List(ctx)
		if err != nil {
			return err
		}
		found := false
		for _, ct := range containers {
			if ct.ID == container {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("container not found on this worker")
		}
		for _, d := range c.Store.snapshot().Deployments {
			for _, task := range d.Tasks {
				if task.ID == container && task.WorkerID == worker {
					return fmt.Errorf("deployment-managed containers use their declared network; only standalone containers can be attached or detached here")
				}
			}
		}
		return manager.ConnectNetwork(ctx, network.ID, container, action == "disconnect")
	})
}

var resourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func validResourceID(id string) bool {
	return id != "." && id != ".." && resourceIDPattern.MatchString(id)
}
