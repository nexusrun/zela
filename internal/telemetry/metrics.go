// Package telemetry collects host and Docker usage without exposing arbitrary host access.
package telemetry

import "time"

type IO struct {
	ReadBytes  uint64   `json:"readBytes"`
	WriteBytes uint64   `json:"writeBytes"`
	ReadBPS    *float64 `json:"readBps"`
	WriteBPS   *float64 `json:"writeBps"`
}
type Disk struct {
	device         uint64
	Path           string  `json:"path"`
	TotalBytes     uint64  `json:"totalBytes"`
	UsedBytes      uint64  `json:"usedBytes"`
	AvailableBytes uint64  `json:"availableBytes"`
	UsedPercent    float64 `json:"usedPercent"`
}
type Host struct {
	CPUPercent       *float64 `json:"cpuPercent"`
	MemoryTotalBytes uint64   `json:"memoryTotalBytes"`
	MemoryUsedBytes  uint64   `json:"memoryUsedBytes"`
	MemoryPercent    float64  `json:"memoryPercent"`
	UptimeSeconds    float64  `json:"uptimeSeconds"`
	Disks            []Disk   `json:"disks"`
	Network          IO       `json:"network"`
	BlockIO          IO       `json:"blockIO"`
}
type Container struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	CPUPercent       *float64 `json:"cpuPercent"`
	MemoryUsedBytes  uint64   `json:"memoryUsedBytes"`
	MemoryLimitBytes uint64   `json:"memoryLimitBytes"`
	MemoryPercent    float64  `json:"memoryPercent"`
	Network          IO       `json:"network"`
	BlockIO          IO       `json:"blockIO"`
}
type Sample struct {
	Time       time.Time   `json:"time"`
	Host       *Host       `json:"host"`
	Containers []Container `json:"containers"`
	Errors     []string    `json:"errors,omitempty"`
	Truncated  int         `json:"truncated,omitempty"`
}

func Percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	p := float64(used) / float64(total) * 100
	if p > 100 {
		return 100
	}
	return p
}
func Rate(current, previous uint64, seconds float64) *float64 {
	if seconds <= 0 || current < previous {
		return nil
	}
	v := float64(current-previous) / seconds
	return &v
}
func WithRates(now, before IO, seconds float64) IO {
	now.ReadBPS = Rate(now.ReadBytes, before.ReadBytes, seconds)
	now.WriteBPS = Rate(now.WriteBytes, before.WriteBytes, seconds)
	return now
}
