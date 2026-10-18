package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
)

// Network is the public Docker network inventory, without arbitrary driver options.
type Network struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	Scope      string `json:"scope"`
	Internal   bool   `json:"internal"`
	EnableIPv6 bool   `json:"enableIPv6"`
	IPAM       struct {
		Config []NetworkIPAM `json:"config"`
	} `json:"ipam"`
	Containers map[string]NetworkEndpoint `json:"containers,omitempty"`
}
type NetworkIPAM struct {
	Subnet  string `json:"subnet"`
	Gateway string `json:"gateway"`
	IPRange string `json:"ipRange,omitempty"`
}
type NetworkEndpoint struct {
	Name        string `json:"name"`
	IPv4Address string `json:"ipv4Address"`
	IPv6Address string `json:"ipv6Address,omitempty"`
	MacAddress  string `json:"macAddress,omitempty"`
}
type NetworkCreate struct {
	Name     string `json:"name"`
	Subnet   string `json:"subnet,omitempty"`
	Gateway  string `json:"gateway,omitempty"`
	Internal bool   `json:"internal"`
}

// NetworkManager is optional so older engines can report an explicit unsupported error.
type NetworkManager interface {
	Networks(context.Context) ([]Network, error)
	InspectNetwork(context.Context, string) (Network, error)
	CreateNetwork(context.Context, NetworkCreate) (string, error)
	RemoveNetwork(context.Context, string) error
	ConnectNetwork(context.Context, string, string, bool) error
}

func (c *Client) networkPath(path string) string {
	if c.URL != "http://docker" {
		return "/docker" + path
	}
	return path
}
func (c *Client) Networks(ctx context.Context) (v []Network, err error) {
	err = c.decode(ctx, c.networkPath("/networks"), &v)
	return
}
func (c *Client) InspectNetwork(ctx context.Context, id string) (v Network, err error) {
	err = c.decode(ctx, c.networkPath("/networks/"+url.PathEscape(id)), &v)
	return
}
func (c *Client) CreateNetwork(ctx context.Context, in NetworkCreate) (string, error) {
	body := map[string]any{"Name": in.Name, "Driver": "bridge", "Internal": in.Internal, "Labels": map[string]string{"zela.network.owner": "console"}}
	if in.Subnet != "" {
		body["IPAM"] = map[string]any{"Driver": "default", "Config": []map[string]string{{"Subnet": in.Subnet, "Gateway": in.Gateway}}}
	}
	r, err := c.request(ctx, "POST", c.networkPath("/networks/create"), body)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	var out struct {
		ID string `json:"Id"`
	}
	err = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&out)
	return out.ID, err
}
func (c *Client) RemoveNetwork(ctx context.Context, id string) error {
	r, err := c.request(ctx, "DELETE", c.networkPath("/networks/"+url.PathEscape(id)), nil)
	if err == nil {
		r.Body.Close()
	}
	return err
}

// disconnect is explicit and never forced: Docker retains its own endpoint checks.
func (c *Client) ConnectNetwork(ctx context.Context, id, container string, disconnect bool) error {
	action := "connect"
	body := map[string]any{"Container": container}
	if disconnect {
		action = "disconnect"
		body["Force"] = false
	}
	r, err := c.request(ctx, "POST", c.networkPath("/networks/"+url.PathEscape(id)+"/"+action), body)
	if err == nil {
		r.Body.Close()
	}
	return err
}
