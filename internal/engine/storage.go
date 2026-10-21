package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
)

type SharedVolumeProbe struct {
	Targets  []string         `json:"targets"`
	Volume   SharedVolumeSpec `json:"volume"`
	Identity string           `json:"identity"`
	Image    string           `json:"image"`
	ReadOnly bool             `json:"readOnly"`
}

type SharedVolumeVerifier interface {
	VerifySharedVolume(context.Context, SharedVolumeProbe) error
}

func (c *Client) VerifySharedVolume(ctx context.Context, probe SharedVolumeProbe) error {
	if c.URL == "http://docker" {
		return fmt.Errorf("shared storage access verification requires an updated worker agent")
	}
	response, err := c.request(ctx, "POST", "/zela/storage/verify", probe)
	if err != nil {
		return fmt.Errorf("worker shared storage verification: %w", err)
	}
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return err
}

type ImageMetadata struct {
	ID           string
	Architecture string
	OS           string
	Config       struct {
		User        string
		Volumes     map[string]json.RawMessage
		Healthcheck *struct{ Test []string }
	}
}

func (c *Client) ImageMetadata(ctx context.Context, image string) (ImageMetadata, error) {
	var result ImageMetadata
	// Image inspections use the full Docker gateway on remote agents.
	path := "/images/" + url.PathEscape(image) + "/json"
	if c.URL != "http://docker" {
		path = "/docker" + path
	}
	err := c.decode(ctx, path, &result)
	return result, err
}
