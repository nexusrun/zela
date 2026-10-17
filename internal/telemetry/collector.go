package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const MaxContainers = 256

type counters struct {
	total, idle              uint64
	network, block           IO
	time                     time.Time
	networkValid, blockValid bool
}
type containerCounters struct {
	cpu, system    uint64
	time           time.Time
	network, block IO
}
type Collector struct {
	Client            *http.Client
	URL               string
	ProcRoot, SysRoot string
	DiskPaths         []string
	Now               func() time.Time
	mu                sync.Mutex
	previous          counters
	containers        map[string]containerCounters
}

func NewCollector(client *http.Client, baseURL string, diskPaths []string) *Collector {
	return &Collector{Client: client, URL: baseURL, ProcRoot: "/proc", SysRoot: "/sys", DiskPaths: diskPaths, Now: time.Now, containers: map[string]containerCounters{}}
}
func (c *Collector) Collect(ctx context.Context) Sample {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now().UTC()
	s := Sample{Time: now, Containers: []Container{}}
	h, counters, err := c.host(now)
	if err != nil {
		s.Errors = append(s.Errors, "Host metrics unavailable: "+err.Error())
	} else {
		s.Host = &h
		c.previous = counters
	}
	var list []struct {
		ID    string `json:"Id"`
		Names []string
	}
	if err := c.get(ctx, "/containers/json", &list); err != nil {
		s.Errors = append(s.Errors, "Docker container metrics unavailable")
		return s
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	if len(list) > MaxContainers {
		s.Truncated = len(list) - MaxContainers
		list = list[:MaxContainers]
	}
	next := map[string]containerCounters{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	gate := make(chan struct{}, 8)
	for _, item := range list {
		if ctx.Err() != nil {
			break
		}
		select {
		case gate <- struct{}{}:
		case <-ctx.Done():
			break
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(id string, names []string) {
			defer wg.Done()
			defer func() { <-gate }()
			var raw DockerStats
			err := c.get(ctx, "/containers/"+url.PathEscape(id)+"/stats?stream=false&one-shot=true", &raw)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				s.Errors = append(s.Errors, "Container stats unavailable: "+id)
				return
			}
			name := id
			if len(names) > 0 {
				name = strings.TrimPrefix(names[0], "/")
			}
			v, prev := containerSample(id, name, raw, c.containers[id], now)
			s.Containers = append(s.Containers, v)
			next[id] = prev
		}(item.ID, item.Names)
	}
	wg.Wait()
	if ctx.Err() != nil {
		s.Errors = append(s.Errors, "Metrics collection exceeded its time budget")
	}
	sort.Slice(s.Containers, func(i, j int) bool { return s.Containers[i].ID < s.Containers[j].ID })
	sort.Strings(s.Errors)
	c.containers = next
	return s
}
func (c *Collector) get(ctx context.Context, path string, out any) error {
	r, err := http.NewRequestWithContext(ctx, "GET", c.URL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.Client.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Docker response %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}
func (c *Collector) read(path string) ([]byte, error) {
	return os.ReadFile(filepath.Join(c.ProcRoot, path))
}
func parseCPU(b []byte) (total, idle uint64, err error) {
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		// guest and guest_nice are already included in user and nice.
		for i := 1; i < len(f) && i <= 8; i++ {
			v, e := strconv.ParseUint(f[i], 10, 64)
			if e != nil {
				return 0, 0, e
			}
			total += v
			if i == 4 || i == 5 {
				idle += v
			}
		}
		return total, idle, nil
	}
	return 0, 0, fmt.Errorf("invalid /proc/stat")
}
func parseMemory(b []byte) (total, used uint64, err error) {
	v := map[string]uint64{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, e := strconv.ParseUint(f[1], 10, 64)
		if e != nil {
			return 0, 0, e
		}
		v[strings.TrimSuffix(f[0], ":")] = n * 1024
	}
	total = v["MemTotal"]
	available, ok := v["MemAvailable"]
	if !ok {
		return 0, 0, fmt.Errorf("MemAvailable is missing")
	}
	if total == 0 || available > total {
		return 0, 0, fmt.Errorf("invalid /proc/meminfo")
	}
	return total, total - available, nil
}
func (c *Collector) host(now time.Time) (Host, counters, error) {
	var h Host
	next := counters{time: now}
	b, err := c.read("stat")
	if err != nil {
		return h, next, err
	}
	next.total, next.idle, err = parseCPU(b)
	if err != nil {
		return h, next, err
	}
	b, err = c.read("meminfo")
	if err != nil {
		return h, next, err
	}
	h.MemoryTotalBytes, h.MemoryUsedBytes, err = parseMemory(b)
	if err != nil {
		return h, next, err
	}
	h.MemoryPercent = Percent(h.MemoryUsedBytes, h.MemoryTotalBytes)
	if b, e := c.read("uptime"); e == nil {
		f := strings.Fields(string(b))
		if len(f) > 0 {
			h.UptimeSeconds, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	if b, e := c.read("net/dev"); e == nil {
		next.networkValid = true
		for _, line := range strings.Split(string(b), "\n") {
			name, data, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			name = strings.TrimSpace(name)
			if name == "lo" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "virbr") {
				continue
			}
			if _, e := os.Stat(filepath.Join(c.SysRoot, "class/net", name, "master")); e == nil {
				continue
			}
			f := strings.Fields(data)
			if len(f) < 9 {
				continue
			}
			r, e := strconv.ParseUint(f[0], 10, 64)
			if e != nil {
				continue
			}
			w, e := strconv.ParseUint(f[8], 10, 64)
			if e != nil {
				continue
			}
			next.network.ReadBytes += r
			next.network.WriteBytes += w
		}
	}
	if b, e := c.read("diskstats"); e == nil {
		next.blockValid = true
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) < 14 {
				continue
			}
			name := f[2]
			if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "dm-") || strings.HasPrefix(name, "md") {
				continue
			}
			if _, e := os.Stat(filepath.Join(c.SysRoot, "block", name)); e != nil {
				continue
			}
			r, e := strconv.ParseUint(f[5], 10, 64)
			if e != nil {
				continue
			}
			w, e := strconv.ParseUint(f[9], 10, 64)
			if e != nil {
				continue
			}
			next.block.ReadBytes += r * 512
			next.block.WriteBytes += w * 512
		}
	}
	devices := map[uint64]bool{}
	for _, path := range c.DiskPaths {
		d, e := diskUsage(path)
		if e != nil {
			return h, next, fmt.Errorf("disk path %s unavailable", path)
		}
		if d.device != 0 && devices[d.device] {
			continue
		}
		devices[d.device] = true
		h.Disks = append(h.Disks, d)
	}
	if !c.previous.time.IsZero() {
		seconds := now.Sub(c.previous.time).Seconds()
		if seconds > 0 && next.total > c.previous.total && next.idle >= c.previous.idle {
			dt, di := next.total-c.previous.total, next.idle-c.previous.idle
			if di <= dt {
				v := float64(dt-di) / float64(dt) * 100
				h.CPUPercent = &v
			}
		}
		h.Network = next.network
		h.BlockIO = next.block
		if next.networkValid && c.previous.networkValid {
			h.Network = WithRates(next.network, c.previous.network, seconds)
		}
		if next.blockValid && c.previous.blockValid {
			h.BlockIO = WithRates(next.block, c.previous.block, seconds)
		}
	} else {
		h.Network = next.network
		h.BlockIO = next.block
	}
	return h, next, nil
}

type DockerStats struct {
	CPU struct {
		Usage struct {
			Total  uint64   `json:"total_usage"`
			PerCPU []uint64 `json:"percpu_usage"`
		} `json:"cpu_usage"`
		System uint64 `json:"system_cpu_usage"`
		Online uint64 `json:"online_cpus"`
	} `json:"cpu_stats"`
	Memory struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RX uint64 `json:"rx_bytes"`
		TX uint64 `json:"tx_bytes"`
	} `json:"networks"`
	Block struct {
		Entries []struct {
			Op    string
			Value uint64
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
}

func containerSample(id, name string, raw DockerStats, prev containerCounters, now time.Time) (Container, containerCounters) {
	next := containerCounters{cpu: raw.CPU.Usage.Total, system: raw.CPU.System, time: now}
	v := Container{ID: id, Name: name, MemoryUsedBytes: raw.Memory.Usage, MemoryLimitBytes: raw.Memory.Limit}
	cache, ok := raw.Memory.Stats["total_inactive_file"]
	if !ok {
		cache = raw.Memory.Stats["inactive_file"]
	}
	if cache <= v.MemoryUsedBytes {
		v.MemoryUsedBytes -= cache
	}
	v.MemoryPercent = Percent(v.MemoryUsedBytes, v.MemoryLimitBytes)
	for _, n := range raw.Networks {
		next.network.ReadBytes += n.RX
		next.network.WriteBytes += n.TX
	}
	for _, n := range raw.Block.Entries {
		switch strings.ToLower(n.Op) {
		case "read":
			next.block.ReadBytes += n.Value
		case "write":
			next.block.WriteBytes += n.Value
		}
	}
	v.Network = next.network
	v.BlockIO = next.block
	if !prev.time.IsZero() {
		seconds := now.Sub(prev.time).Seconds()
		cpus := raw.CPU.Online
		if cpus == 0 {
			cpus = uint64(len(raw.CPU.Usage.PerCPU))
		}
		if seconds > 0 && next.cpu >= prev.cpu && next.system > prev.system && cpus > 0 {
			p := float64(next.cpu-prev.cpu) / float64(next.system-prev.system) * float64(cpus) * 100
			v.CPUPercent = &p
		}
		v.Network = WithRates(next.network, prev.network, seconds)
		v.BlockIO = WithRates(next.block, prev.block, seconds)
	}
	return v, next
}
