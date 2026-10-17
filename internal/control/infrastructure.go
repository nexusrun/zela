package control

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// These DTOs intentionally exclude worker transports, SSH configuration, errors,
// credentials, application names, labels and per-container telemetry.
type InfrastructureView struct {
	HighAvailability *HAStatus              `json:"highAvailability,omitempty"`
	ObservedAt       time.Time              `json:"observedAt"`
	Demo             bool                   `json:"demo"`
	Capabilities     map[string]any         `json:"capabilities"`
	Workers          []InfrastructureWorker `json:"workers"`
}
type InfrastructureCapacity struct {
	TotalCPUs            float64              `json:"totalCPUs"`
	ReservedCPUs         float64              `json:"reservedCPUs"`
	AvailableCPUs        float64              `json:"availableCPUs"`
	TotalMemoryBytes     int64                `json:"totalMemoryBytes"`
	ReservedMemoryBytes  int64                `json:"reservedMemoryBytes"`
	AvailableMemoryBytes int64                `json:"availableMemoryBytes"`
	UnavailableHostPorts []InfrastructurePort `json:"unavailableHostPorts"`
}
type InfrastructurePort struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}
type InfrastructureDisk struct {
	TotalBytes     uint64  `json:"totalBytes"`
	UsedBytes      uint64  `json:"usedBytes"`
	AvailableBytes uint64  `json:"availableBytes"`
	UsedPercent    float64 `json:"usedPercent"`
}
type InfrastructureUsage struct {
	Disks            []InfrastructureDisk `json:"disks"`
	SampledAt        time.Time            `json:"sampledAt"`
	CPUPercent       *float64             `json:"cpuPercent"`
	MemoryUsedBytes  uint64               `json:"memoryUsedBytes"`
	MemoryTotalBytes uint64               `json:"memoryTotalBytes"`
}
type InfrastructureWorker struct {
	ID                  string                 `json:"id"`
	Name                string                 `json:"name"`
	Region              string                 `json:"region"`
	Mode                string                 `json:"mode"`
	Status              string                 `json:"status"`
	ObservedAt          time.Time              `json:"observedAt"`
	LastSeen            time.Time              `json:"lastSeen"`
	InventoryFresh      bool                   `json:"inventoryFresh"`
	AcceptingContainers bool                   `json:"acceptingContainers"`
	DockerVersion       string                 `json:"dockerVersion"`
	OperatingSystem     string                 `json:"operatingSystem"`
	Architecture        string                 `json:"architecture"`
	Capacity            InfrastructureCapacity `json:"capacity"`
	UsageFresh          bool                   `json:"usageFresh"`
	Usage               *InfrastructureUsage   `json:"usage,omitempty"`
}

func infrastructureSnapshot(s State, now time.Time) InfrastructureView {
	view := InfrastructureView{ObservedAt: now, Demo: s.Demo, Workers: []InfrastructureWorker{}, Capabilities: map[string]any{
		"workerDiscovery": "/api/cluster/workers", "workerHeader": "X-Zela-Worker", "workerPath": "/docker/workers/{id}/",
		"infrastructureTokenRequiresWorkerSelection": true, "selectedWorkerFallback": false,
		"containerOwnershipRouting": true, "scopedContainerInventory": true,
		"defaultMemoryBytes": int64(128 << 20), "defaultNanoCPUs": int64(250000000),
		"scopedHostAccess": false, "scopedBuilds": false, "scopedNetworkAdministration": false, "scopedVolumeAdministration": false,
		"inventoryFreshnessSeconds": 45, "applicationScheduling": "client", "capacityAccounting": "persisted reservations; measured usage is separate; unmanaged workload limits are not reserved",
	}}
	for _, worker := range s.Workers {
		view.Workers = append(view.Workers, infrastructureWorker(s, worker, now))
	}
	sort.Slice(view.Workers, func(i, j int) bool { return view.Workers[i].ID < view.Workers[j].ID })
	return view
}
func infrastructureWorker(s State, w Worker, now time.Time) InfrastructureWorker {
	capacity := InfrastructureCapacity{TotalCPUs: float64(w.Info.NCPU), TotalMemoryBytes: w.Info.MemTotal, UnavailableHostPorts: []InfrastructurePort{}}
	ports := map[string]InfrastructurePort{}
	addPort := func(port int, proto string) {
		if port == 0 {
			return
		}
		if proto == "" {
			proto = "tcp"
		}
		ports[fmt.Sprintf("%d/%s", port, proto)] = InfrastructurePort{port, proto}
	}
	for _, ct := range s.DockerContainers {
		if ct.WorkerID != w.ID {
			continue
		}
		capacity.ReservedMemoryBytes += ((ct.MemoryBytes + (1 << 20) - 1) / (1 << 20)) * (1 << 20)
		capacity.ReservedCPUs += float64(ct.NanoCPUs) / 1e9
		for _, port := range ct.Ports {
			addPort(port.Host, port.Protocol)
		}
	}
	for _, d := range s.Deployments {
		for _, task := range d.Tasks {
			if task.WorkerID != w.ID {
				continue
			}
			capacity.ReservedMemoryBytes += d.MemoryMB * (1 << 20)
			capacity.ReservedCPUs += d.CPUs
			for _, port := range d.Ports {
				addPort(port.Host, port.Protocol)
			}
		}
	}
	for _, ct := range w.Containers {
		for _, port := range ct.Ports {
			addPort(port.PublicPort, port.Type)
		}
	}
	for _, port := range ports {
		capacity.UnavailableHostPorts = append(capacity.UnavailableHostPorts, port)
	}
	sort.Slice(capacity.UnavailableHostPorts, func(i, j int) bool {
		a, b := capacity.UnavailableHostPorts[i], capacity.UnavailableHostPorts[j]
		if a.Port == b.Port {
			return a.Protocol < b.Protocol
		}
		return a.Port < b.Port
	})
	capacity.AvailableCPUs = math.Max(0, capacity.TotalCPUs-capacity.ReservedCPUs)
	// Match the MiB granularity used by admission checks.
	capacity.AvailableMemoryBytes = max(int64(0), (capacity.TotalMemoryBytes/(1<<20))*(1<<20)-capacity.ReservedMemoryBytes)
	fresh := !w.LastSeen.IsZero() && w.Status == "online" && now.Sub(w.LastSeen) <= 45*time.Second && w.LastSeen.Sub(now) <= 30*time.Second
	out := InfrastructureWorker{ID: w.ID, Name: w.Name, Region: w.Region, Mode: w.Mode, Status: w.Status, ObservedAt: now, LastSeen: w.LastSeen, InventoryFresh: fresh,
		AcceptingContainers: !s.Demo && fresh && w.Mode == "active" && capacity.AvailableCPUs >= 0.001 && capacity.AvailableMemoryBytes >= 6<<20,
		DockerVersion:       w.Info.ServerVersion, OperatingSystem: w.Info.OperatingSystem, Architecture: w.Info.Architecture, Capacity: capacity, UsageFresh: freshMetrics(w, now)}
	if out.UsageFresh {
		out.Usage = &InfrastructureUsage{SampledAt: w.Metrics.Time, CPUPercent: w.Metrics.Host.CPUPercent, MemoryUsedBytes: w.Metrics.Host.MemoryUsedBytes, MemoryTotalBytes: w.Metrics.Host.MemoryTotalBytes}
		out.Usage.Disks = []InfrastructureDisk{}
		for _, disk := range w.Metrics.Host.Disks {
			out.Usage.Disks = append(out.Usage.Disks, InfrastructureDisk{TotalBytes: disk.TotalBytes, UsedBytes: disk.UsedBytes, AvailableBytes: disk.AvailableBytes, UsedPercent: disk.UsedPercent})
		}
	}
	return out
}
