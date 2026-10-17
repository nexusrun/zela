// nexus-agent runs on each worker Docker host. Every interval it pushes host
// metrics to the NEXUS AI backend, and it pushes container die / OOM /
// unhealthy events as soon as Docker reports them.
//
// Configuration (environment, usually /etc/nexus-agent.env):
//
//	NEXUS_API_URL        backend base URL on the private network, e.g. http://10.0.0.5:3001
//	NEXUS_AGENT_HOST     this host's name in the backend's DOCKER_HOSTS
//	NEXUS_AGENT_TOKEN    hex(HMAC-SHA256(HOST_AGENT_SECRET, NEXUS_AGENT_HOST))
//	NEXUS_AGENT_INTERVAL heartbeat interval (default 10s)
//	DOCKER_SOCKET        default /var/run/docker.sock
//	DISK_PATH            filesystem to measure (default /var/lib/docker)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const version = "0.1.0"

// Events are kept across failed pushes, up to this many.
const maxBufferedEvents = 100

type config struct {
	apiURL   string
	host     string
	token    string
	interval time.Duration
	socket   string
	diskPath string
}

func loadConfig() (config, error) {
	c := config{
		apiURL:   strings.TrimRight(os.Getenv("NEXUS_API_URL"), "/"),
		host:     os.Getenv("NEXUS_AGENT_HOST"),
		token:    os.Getenv("NEXUS_AGENT_TOKEN"),
		interval: 10 * time.Second,
		socket:   envOr("DOCKER_SOCKET", "/var/run/docker.sock"),
		diskPath: envOr("DISK_PATH", "/var/lib/docker"),
	}
	if v := os.Getenv("NEXUS_AGENT_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second {
			return c, fmt.Errorf("invalid NEXUS_AGENT_INTERVAL %q", v)
		}
		c.interval = d
	}
	if c.apiURL == "" || c.host == "" || c.token == "" {
		return c, fmt.Errorf("NEXUS_API_URL, NEXUS_AGENT_HOST and NEXUS_AGENT_TOKEN are required")
	}
	return c, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type heartbeat struct {
	Host              string           `json:"host"`
	AgentVersion      string           `json:"agentVersion"`
	CPUCount          int              `json:"cpuCount"`
	MemTotalMb        int              `json:"memTotalMb,omitempty"`
	MemUsedMb         int              `json:"memUsedMb,omitempty"`
	DiskFreePct       *float64         `json:"diskFreePct,omitempty"`
	ContainersRunning *int             `json:"containersRunning,omitempty"`
	Events            []containerEvent `json:"events,omitempty"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println(version)
		return
	}
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("nexus-agent %s reporting %s to %s every %s", version, cfg.host, cfg.apiURL, cfg.interval)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	docker := newDockerClient(cfg.socket)
	events := make(chan containerEvent, 64)
	go watchEvents(ctx, docker, events)

	httpClient := &http.Client{Timeout: 5 * time.Second}
	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()

	var pending []containerEvent
	send := func() {
		hb := collect(ctx, cfg, docker)
		hb.Events = pending
		if err := push(ctx, httpClient, cfg, hb); err != nil {
			log.Printf("heartbeat failed: %v", err)
			return
		}
		pending = nil
	}

	send()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send()
		case ev := <-events:
			pending = append(pending, ev)
			if len(pending) > maxBufferedEvents {
				pending = pending[len(pending)-maxBufferedEvents:]
			}
			send() // report container trouble right away
		}
	}
}

func collect(ctx context.Context, cfg config, docker *dockerClient) heartbeat {
	hb := heartbeat{Host: cfg.host, AgentVersion: version, CPUCount: cpuCount()}
	if total, used, err := memoryMb(); err == nil {
		hb.MemTotalMb, hb.MemUsedMb = total, used
	} else {
		log.Printf("memory: %v", err)
	}
	if pct, err := diskFreePct(cfg.diskPath); err == nil {
		hb.DiskFreePct = &pct
	} else {
		log.Printf("disk: %v", err)
	}
	if n, err := docker.containersRunning(ctx); err == nil {
		hb.ContainersRunning = &n
	} else {
		log.Printf("docker info: %v", err)
	}
	return hb
}

func push(ctx context.Context, client *http.Client, cfg config, hb heartbeat) error {
	body, err := json.Marshal(hb)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.apiURL+"/api/agent/heartbeat", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.token)
	req.Header.Set("User-Agent", "nexus-agent/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("backend answered %s", resp.Status)
	}
	return nil
}
