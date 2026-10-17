package telemetry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func writeFixture(t *testing.T, root, name, data string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func fixtureCollector(t *testing.T) (*Collector, *time.Time, *int) {
	t.Helper()
	proc, sys := t.TempDir(), t.TempDir()
	now := time.Now().UTC()
	round := 0
	writeFixture(t, proc, "stat", "cpu 100 0 0 100 0 0 0 0 50 0\n")
	writeFixture(t, proc, "meminfo", "MemTotal: 1000 kB\nMemAvailable: 400 kB\n")
	writeFixture(t, proc, "uptime", "1000 0\n")
	writeFixture(t, proc, "net/dev", "eth0: 1000 0 0 0 0 0 0 0 2000 0\nlo: 99999 0 0 0 0 0 0 0 99999\nveth123: 99999 0 0 0 0 0 0 0 99999\n")
	writeFixture(t, proc, "diskstats", "8 0 sda 1 0 100 0 1 0 200 0 0 0 0\n8 1 sda1 1 0 999 0 1 0 999 0 0 0 0\n")
	if err := os.MkdirAll(filepath.Join(sys, "block/sda"), 0700); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body := `[{"Id":"one","Names":["/web"]}]`
		if r.URL.Path == "/containers/one/stats" {
			if r.URL.Query().Get("stream") != "false" || r.URL.Query().Get("one-shot") != "true" {
				t.Error("stats must be bounded one-shot requests")
			}
			body = fmt.Sprintf(`{"cpu_stats":{"cpu_usage":{"total_usage":%d},"system_cpu_usage":%d,"online_cpus":2},"memory_stats":{"usage":1000,"limit":2000,"stats":{"inactive_file":200}},"networks":{"eth0":{"rx_bytes":%d,"tx_bytes":%d}},"blkio_stats":{"io_service_bytes_recursive":[{"Op":"Read","Value":%d},{"Op":"Write","Value":%d}]}}`, 100+round*50, 1000+round*200, 1000+round*1000, 2000+round*500, 100+round*100, 200+round*200)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	c := NewCollector(client, "http://docker", nil)
	c.ProcRoot = proc
	c.SysRoot = sys
	c.Now = func() time.Time { return now }
	return c, &now, &round
}
func TestCollectorMeasuresUsageAndRatesWithoutDoubleCountingGuestCPU(t *testing.T) {
	c, now, round := fixtureCollector(t)
	first := c.Collect(context.Background())
	if first.Host == nil || first.Host.CPUPercent != nil || first.Containers[0].CPUPercent != nil || first.Host.Network.ReadBPS != nil {
		t.Fatalf("first sample should warm up rates: %+v", first)
	}
	*now = now.Add(10 * time.Second)
	*round = 1
	writeFixture(t, c.ProcRoot, "stat", "cpu 180 0 0 120 0 0 0 0 500 0\n")
	writeFixture(t, c.ProcRoot, "net/dev", "eth0: 3000 0 0 0 0 0 0 0 2500 0\n")
	writeFixture(t, c.ProcRoot, "diskstats", "8 0 sda 1 0 120 0 1 0 230 0 0 0 0\n")
	v := c.Collect(context.Background())
	if v.Host == nil || *v.Host.CPUPercent != 80 || v.Host.MemoryPercent != 60 || v.Host.MemoryUsedBytes != 600*1024 {
		t.Fatalf("wrong host usage: %+v", v.Host)
	}
	if *v.Host.Network.ReadBPS != 200 || *v.Host.Network.WriteBPS != 50 || *v.Host.BlockIO.ReadBPS != 1024 {
		t.Fatalf("wrong host rates: %+v", v.Host)
	}
	m := v.Containers[0]
	if *m.CPUPercent != 50 || m.MemoryUsedBytes != 800 || m.MemoryPercent != 40 || *m.Network.ReadBPS != 100 || *m.BlockIO.WriteBPS != 20 {
		t.Fatalf("wrong Docker usage: %+v", m)
	}
}
func TestCounterResetAndNonIncreasingTimeDoNotProduceSpikes(t *testing.T) {
	if Rate(1, 100, 10) != nil || Rate(100, 0, 0) != nil {
		t.Fatal("reset counters or zero intervals generated rates")
	}
	raw := DockerStats{}
	raw.CPU.System = 1
	raw.CPU.Usage.Total = 1
	raw.CPU.Online = 4
	v, _ := containerSample("c", "c", raw, containerCounters{cpu: 100, system: 100, time: time.Now().Add(-time.Second)}, time.Now())
	if v.CPUPercent != nil {
		t.Fatal("container restart produced CPU spike")
	}
	if _, _, err := parseMemory([]byte("MemTotal: 100 kB\n")); err == nil {
		t.Fatal("missing availability treated as zero usage")
	}
}
func TestCgroupV1CacheAndCPUFallback(t *testing.T) {
	raw := DockerStats{}
	raw.CPU.System = 200
	raw.CPU.Usage.Total = 120
	raw.CPU.Usage.PerCPU = []uint64{1, 1, 1, 1}
	raw.Memory.Usage = 1000
	raw.Memory.Limit = 2000
	raw.Memory.Stats = map[string]uint64{"total_inactive_file": 300, "inactive_file": 100}
	v, _ := containerSample("c", "c", raw, containerCounters{cpu: 100, system: 100, time: time.Now().Add(-time.Second)}, time.Now())
	if *v.CPUPercent != 80 || v.MemoryUsedBytes != 700 {
		t.Fatalf("wrong v1 stats: %+v", v)
	}
}
func TestSamplerReturnsUnavailableBeforeFirstSampleAndCollectorIsRaceSafe(t *testing.T) {
	s := &Sampler{}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "http://worker/zela/metrics", nil))
	if w.Code != 503 {
		t.Fatal("warming sampler claimed data")
	}
	c, _, _ := fixtureCollector(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Collect(context.Background()) }()
	}
	wg.Wait()
}
func TestPartialDockerFailureDoesNotEraseHostMonitoring(t *testing.T) {
	c, _, _ := fixtureCollector(t)
	c.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("failure"))}, nil
	})
	v := c.Collect(context.Background())
	if v.Host == nil || len(v.Errors) == 0 {
		t.Fatal("Docker error should be reported alongside available host metrics")
	}
}
func TestMissingIOCountersDoNotFabricateZeroUsageOrRecoverySpikes(t *testing.T) {
	c, now, _ := fixtureCollector(t)
	c.Collect(context.Background())
	if err := os.Remove(filepath.Join(c.ProcRoot, "net/dev")); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(10 * time.Second)
	v := c.Collect(context.Background())
	if v.Host.Network.ReadBPS != nil {
		t.Fatal("missing network counters were treated as zero traffic")
	}
	writeFixture(t, c.ProcRoot, "net/dev", "eth0: 500000 0 0 0 0 0 0 0 900000 0\n")
	*now = now.Add(10 * time.Second)
	v = c.Collect(context.Background())
	if v.Host.Network.ReadBPS != nil {
		t.Fatal("restored counters produced a false recovery spike")
	}
}
