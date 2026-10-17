package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// dockerClient talks to the Docker Engine API over its unix socket.
type dockerClient struct {
	http *http.Client
}

func newDockerClient(socket string) *dockerClient {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}
	return &dockerClient{http: &http.Client{Transport: transport}}
}

func (d *dockerClient) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("docker %s: %s", path, resp.Status)
	}
	return resp, nil
}

func (d *dockerClient) containersRunning(ctx context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := d.get(ctx, "/info")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var info struct{ ContainersRunning int }
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return 0, err
	}
	return info.ContainersRunning, nil
}

// containerEvent matches the backend's AgentContainerEvent.
type containerEvent struct {
	Type          string `json:"type"` // die | oom | health_status
	ContainerName string `json:"containerName,omitempty"`
	DeploymentID  string `json:"deploymentId,omitempty"`
	ExitCode      *int   `json:"exitCode,omitempty"`
	Health        string `json:"health,omitempty"`
	At            string `json:"at,omitempty"`
}

// dockerEvent is the subset of a Docker /events message we read.
type dockerEvent struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	Actor  struct {
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
	Time int64 `json:"time"`
}

// toContainerEvent maps a Docker event to what the backend wants, or false
// for events we do not report (clean exits, healthy transitions, others).
func toContainerEvent(e dockerEvent) (containerEvent, bool) {
	if e.Type != "container" {
		return containerEvent{}, false
	}
	attrs := e.Actor.Attributes
	ev := containerEvent{
		ContainerName: attrs["name"],
		DeploymentID:  attrs["zela.deployment.id"],
	}
	if e.Time > 0 {
		ev.At = time.Unix(e.Time, 0).UTC().Format(time.RFC3339)
	}
	switch {
	case e.Action == "oom":
		ev.Type = "oom"
	case e.Action == "die":
		code, err := strconv.Atoi(attrs["exitCode"])
		if err != nil || code == 0 {
			return containerEvent{}, false
		}
		ev.Type, ev.ExitCode = "die", &code
	case strings.HasPrefix(e.Action, "health_status:"):
		health := strings.TrimSpace(strings.TrimPrefix(e.Action, "health_status:"))
		if health != "unhealthy" {
			return containerEvent{}, false
		}
		ev.Type, ev.Health = "health_status", health
	default:
		return containerEvent{}, false
	}
	return ev, true
}

// watchEvents streams Docker container events into out, reconnecting with
// backoff whenever the stream drops, until ctx is cancelled.
func watchEvents(ctx context.Context, d *dockerClient, out chan<- containerEvent) {
	filters := url.QueryEscape(`{"type":["container"],"event":["die","oom","health_status"]}`)
	backoff := time.Second
	for ctx.Err() == nil {
		err := streamEvents(ctx, d, "/events?filters="+filters, out)
		if ctx.Err() != nil {
			return
		}
		log.Printf("docker event stream ended: %v; reconnecting in %s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

func streamEvents(ctx context.Context, d *dockerClient, path string, out chan<- containerEvent) error {
	resp, err := d.get(ctx, path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var e dockerEvent
		if err := dec.Decode(&e); err != nil {
			if err == io.EOF {
				return fmt.Errorf("stream closed")
			}
			return err
		}
		if ev, ok := toContainerEvent(e); ok {
			select {
			case out <- ev:
			case <-ctx.Done():
				return nil
			}
		}
	}
}
