//go:build !linux

package telemetry

import "fmt"

func diskUsage(path string) (Disk, error) {
	return Disk{}, fmt.Errorf("host metrics require a Linux worker")
}
