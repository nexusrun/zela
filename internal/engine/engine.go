// Package engine implements the subset of the Docker Engine API used by the control plane.
package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/zela-dev/zela/internal/clusternet"
	"github.com/zela-dev/zela/internal/telemetry"
)

type Info struct {
	Name              string
	NCPU              int
	MemTotal          int64
	ContainersRunning int
	ServerVersion     string
	OperatingSystem   string
	Architecture      string
}

// ContainerMount is the non-secret part of a Docker inventory mount. Host
// source paths are deliberately excluded from cluster state and API views.
type ContainerMount struct {
	Type        string
	Name        string
	Destination string
	RW          bool
}

type Container struct {
	Mounts []ContainerMount `json:",omitempty"`
	ID     string           `json:"Id"`
	Names  []string
	Image  string
	State  string
	Status string
	Labels map[string]string
	Ports  []struct {
		PrivatePort int
		PublicPort  int
		Type        string
	}
	NetworkSettings *struct {
		Networks map[string]struct{ IPAddress string }
	} `json:",omitempty"`
}

// NetworkIP returns the container's address on a named Docker network.
func (c Container) NetworkIP(network string) string {
	if c.NetworkSettings == nil {
		return ""
	}
	return c.NetworkSettings.Networks[network].IPAddress
}

type Inspection struct {
	ID    string `json:"Id"`
	Image string `json:"Image"`
	State struct {
		Running bool
		Status  string
		Error   string
		Health  *struct{ Status string }
	}
	Mounts []struct {
		Type        string
		Name        string
		Source      string
		Destination string
		RW          bool
	}
	Config struct {
		Image   string
		Volumes map[string]json.RawMessage
	}
}

type Port struct {
	Container int    `json:"container"`
	Host      int    `json:"host"`
	Protocol  string `json:"protocol"`
	// HostIP restricts the published port to one host address; empty means all.
	HostIP string `json:"hostIP,omitempty"`
}

type Spec struct {
	Name     string            `json:"name"`
	Image    string            `json:"image"`
	Env      []string          `json:"env,omitempty"`
	Command  []string          `json:"command,omitempty"`
	Ports    []Port            `json:"ports,omitempty"`
	MemoryMB int64             `json:"memoryMB"`
	CPUs     float64           `json:"cpus"`
	Labels   map[string]string `json:"labels,omitempty"`
	// Network attaches the container to a named Docker network instead of the default bridge.
	Network string `json:"network,omitempty"`
	// DNS servers and search domains for the container's resolver.
	DNS        []string `json:"dns,omitempty"`
	DNSSearch  []string `json:"dnsSearch,omitempty"`
	DNSOptions []string `json:"dnsOptions,omitempty"`
	Mounts     []Mount  `json:"mounts,omitempty"`
}

// Mount attaches a named Docker volume to a container. Bind mounts are not
// exposed through this managed deployment API.
type Mount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

type SharedVolumeSpec struct {
	Name, Server, Export string
}

// SharedVolumePreparer is optional so older agents keep serving stateless apps.
type SharedVolumePreparer interface {
	EnsureSharedVolume(context.Context, SharedVolumeSpec) error
}

// ImageResolver returns a pullable immutable digest reference or a local image ID.
type ImageResolver interface {
	ResolveImage(context.Context, string) (string, error)
}

type Engine interface {
	Info(context.Context) (Info, error)
	List(context.Context) ([]Container, error)
	Inspect(context.Context, string) (Inspection, error)
	Pull(context.Context, string) error
	Create(context.Context, Spec) (string, error)
	Start(context.Context, string) error
	Stop(context.Context, string) error
	Restart(context.Context, string) error
	Remove(context.Context, string) error
	Logs(context.Context, string) (string, error)
}

// Monitoring is optional so older workers and local Docker sockets remain manageable.
type MetricsProvider interface {
	Metrics(context.Context) (telemetry.Sample, error)
}

func (c *Client) Metrics(ctx context.Context) (v telemetry.Sample, err error) {
	if c.URL == "http://docker" {
		return v, fmt.Errorf("host metrics require the updated worker agent; direct Docker sockets do not expose /zela/metrics")
	}
	err = c.decode(ctx, "/zela/metrics", &v)
	if err != nil && (strings.Contains(err.Error(), "Docker 403") || strings.Contains(err.Error(), "Docker 404")) {
		err = fmt.Errorf("worker agent does not expose monitoring; update zela-worker: %w", err)
	}
	return
}

// NetworkConfigurer is implemented by worker agents that support the cluster network.
type NetworkConfigurer interface {
	ApplyNetwork(context.Context, clusternet.Config) (clusternet.Status, error)
}

func (c *Client) ApplyNetwork(ctx context.Context, cfg clusternet.Config) (v clusternet.Status, err error) {
	if c.URL == "http://docker" {
		return v, fmt.Errorf("the cluster network requires a worker agent; direct Docker sockets cannot join")
	}
	r, err := c.request(ctx, "PUT", "/zela/network", cfg)
	if err != nil {
		if strings.Contains(err.Error(), "Docker 403") || strings.Contains(err.Error(), "Docker 404") || strings.Contains(err.Error(), "Docker 405") {
			err = fmt.Errorf("worker agent does not support the cluster network; update zela-worker: %w", err)
		}
		return v, err
	}
	defer r.Body.Close()
	err = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&v)
	return v, err
}

type Client struct {
	HTTP       *http.Client
	URL, Token string
}

func New(endpoint, token string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid worker endpoint")
	}
	if u.Scheme == "unix" {
		if u.Path == "" || u.Host != "" {
			return nil, fmt.Errorf("use unix:///absolute/path/to/docker.sock")
		}
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", u.Path)
		}}
		return &Client{HTTP: &http.Client{Transport: tr, Timeout: 5 * time.Minute}, URL: "http://docker"}, nil
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("endpoint must be an http(s) worker-agent origin or unix socket")
	}
	if len(token) < 32 {
		return nil, fmt.Errorf("remote workers require an agent token of at least 32 characters")
	}
	// Never forward credentials to a redirect destination.
	return &Client{HTTP: &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, URL: strings.TrimRight(endpoint, "/"), Token: token}, nil
}

func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var b io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		b = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, b)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("worker connection failed: %w", err)
	}
	alreadyInState := resp.StatusCode == http.StatusNotModified && method == "POST" && (strings.HasSuffix(path, "/start") || strings.Contains(path, "/stop?"))
	if !alreadyInState && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var msg struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &msg)
		if msg.Message == "" {
			msg.Message = strings.TrimSpace(string(data))
		}
		return nil, fmt.Errorf("Docker %s: %s", resp.Status, msg.Message)
	}
	return resp, nil
}

func (c *Client) decode(ctx context.Context, path string, target any) error {
	r, err := c.request(ctx, "GET", path, nil)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(target)
}
func (c *Client) Info(ctx context.Context) (v Info, err error) {
	err = c.decode(ctx, "/info", &v)
	return
}
func (c *Client) List(ctx context.Context) (v []Container, err error) {
	err = c.decode(ctx, "/containers/json?all=1", &v)
	return
}
func (c *Client) Inspect(ctx context.Context, id string) (v Inspection, err error) {
	err = c.decode(ctx, "/containers/"+url.PathEscape(id)+"/json", &v)
	return
}
func (c *Client) Pull(ctx context.Context, image string) error {
	r, err := c.request(ctx, "POST", "/images/create?fromImage="+url.QueryEscape(image), nil)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	for {
		var line struct {
			Error string `json:"error"`
		}
		err := dec.Decode(&line)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if line.Error != "" {
			return fmt.Errorf("image pull: %s", line.Error)
		}
	}
}

func (c *Client) ResolveImage(ctx context.Context, image string) (string, error) {
	path := "/images/" + url.PathEscape(image) + "/json"
	if c.URL != "http://docker" {
		path = "/docker" + path
	}
	var result struct {
		ID          string   `json:"Id"`
		RepoDigests []string `json:"RepoDigests"`
	}
	if err := c.decode(ctx, path, &result); err != nil {
		return "", err
	}
	if strings.HasPrefix(image, "sha256:") {
		if result.ID == image {
			return result.ID, nil
		}
		return "", fmt.Errorf("image configuration ID does not match pinned revision")
	}
	for _, digest := range result.RepoDigests {
		if strings.Contains(digest, "@sha256:") {
			return digest, nil
		}
	}
	if strings.HasPrefix(result.ID, "sha256:") {
		return result.ID, nil
	}
	return "", fmt.Errorf("Docker image has no immutable digest")
}
func (c *Client) Create(ctx context.Context, s Spec) (string, error) {
	exposed := map[string]any{}
	bindings := map[string][]map[string]string{}
	for _, p := range s.Ports {
		key := fmt.Sprintf("%d/%s", p.Container, p.Protocol)
		exposed[key] = struct{}{}
		hostIP := p.HostIP
		if hostIP == "" {
			hostIP = "0.0.0.0"
		}
		bindings[key] = append(bindings[key], map[string]string{"HostIp": hostIP, "HostPort": fmt.Sprint(p.Host)})
	}
	hostConfig := map[string]any{"Memory": s.MemoryMB * 1024 * 1024, "NanoCpus": int64(s.CPUs * 1e9), "PortBindings": bindings, "RestartPolicy": map[string]string{"Name": "unless-stopped"}}
	if s.Network != "" {
		hostConfig["NetworkMode"] = s.Network
	}
	if len(s.DNS) > 0 {
		hostConfig["Dns"] = s.DNS
		hostConfig["DnsSearch"] = s.DNSSearch
		hostConfig["DnsOptions"] = s.DNSOptions
	}
	if len(s.Mounts) > 0 {
		mounts := make([]map[string]any, 0, len(s.Mounts))
		for _, m := range s.Mounts {
			mounts = append(mounts, map[string]any{"Type": "volume", "Source": m.Source, "Target": m.Target, "ReadOnly": m.ReadOnly})
		}
		hostConfig["Mounts"] = mounts
	}
	body := map[string]any{"Image": s.Image, "Env": s.Env, "Cmd": s.Command, "Labels": s.Labels, "ExposedPorts": exposed, "HostConfig": hostConfig}
	r, err := c.request(ctx, "POST", "/containers/create?name="+url.QueryEscape(s.Name), body)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	var result struct {
		ID string `json:"Id"`
	}
	err = json.NewDecoder(r.Body).Decode(&result)
	if err == nil && result.ID == "" {
		err = fmt.Errorf("Docker returned an empty container ID")
	}
	return result.ID, err
}

// EnsureSharedVolume creates or verifies a deterministic local-driver NFSv4
// volume. It rejects an existing volume whose definition differs.
func (c *Client) EnsureSharedVolume(ctx context.Context, spec SharedVolumeSpec) error {
	if spec.Name == "" || spec.Server == "" || !path.IsAbs(spec.Export) || path.Clean(spec.Export) != spec.Export || strings.ContainsAny(spec.Server, " ,/\\\t\x00\r\n") || strings.ContainsAny(spec.Export, "\x00\r\n") {
		return fmt.Errorf("invalid managed NFS volume definition")
	}
	inspect := func() (map[string]any, error) {
		r, err := c.request(ctx, "GET", "/volumes/"+url.PathEscape(spec.Name), nil)
		if err != nil {
			return nil, err
		}
		defer r.Body.Close()
		var volume map[string]any
		err = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&volume)
		return volume, err
	}
	volume, err := inspect()
	if err != nil && !strings.Contains(err.Error(), "Docker 404") {
		return err
	}
	options := map[string]string{"type": "nfs", "o": "addr=" + spec.Server + ",nfsvers=4,hard,timeo=600,retrans=2,rw", "device": ":" + spec.Export}

	if err != nil {
		body := map[string]any{"Name": spec.Name, "Driver": "local", "DriverOpts": options, "Labels": map[string]string{"zela.managed.shared-volume": "true"}}
		response, createErr := c.request(ctx, "POST", "/volumes/create", body)
		if createErr != nil {
			return createErr
		}
		_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if readErr != nil {
			return readErr
		}
		volume, err = inspect()
		if err != nil {
			return err
		}
	}
	if err == nil {
		driver, _ := volume["Driver"].(string)
		driverOpts, _ := volume["Options"].(map[string]any)
		for key, value := range options {
			if driver != "local" || fmt.Sprint(driverOpts[key]) != value {
				return fmt.Errorf("existing Docker volume %q has a conflicting definition", spec.Name)
			}
		}
		labels, _ := volume["Labels"].(map[string]any)
		if fmt.Sprint(labels["zela.managed.shared-volume"]) != "true" {
			return fmt.Errorf("existing Docker volume %q is not labeled as Zela-managed", spec.Name)
		}
		return nil
	}
	return fmt.Errorf("Docker volume could not be verified")
}
func (c *Client) action(ctx context.Context, method, path string) error {
	r, err := c.request(ctx, method, path, nil)
	if r != nil {
		defer r.Body.Close()
		_, _ = io.Copy(io.Discard, r.Body)
	}
	return err
}
func (c *Client) Start(ctx context.Context, id string) error {
	return c.action(ctx, "POST", "/containers/"+url.PathEscape(id)+"/start")
}
func (c *Client) Stop(ctx context.Context, id string) error {
	return c.action(ctx, "POST", "/containers/"+url.PathEscape(id)+"/stop?t=15")
}
func (c *Client) Restart(ctx context.Context, id string) error {
	return c.action(ctx, "POST", "/containers/"+url.PathEscape(id)+"/restart?t=15")
}
func (c *Client) Remove(ctx context.Context, id string) error {
	return c.action(ctx, "DELETE", "/containers/"+url.PathEscape(id)+"?force=true")
}
func (c *Client) Logs(ctx context.Context, id string) (string, error) {
	r, err := c.request(ctx, "GET", "/containers/"+url.PathEscape(id)+"/logs?stdout=1&stderr=1&tail=200&timestamps=1", nil)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		return "", err
	}
	// Non-TTY streams use Docker's eight-byte multiplexing header.
	var out bytes.Buffer
	for len(data) >= 8 && (data[0] == 1 || data[0] == 2) && data[1] == 0 && data[2] == 0 && data[3] == 0 {
		n := int(binary.BigEndian.Uint32(data[4:8]))
		if n > len(data)-8 {
			break
		}
		out.Write(data[8 : 8+n])
		data = data[8+n:]
	}
	out.Write(data)
	return out.String(), nil
}
