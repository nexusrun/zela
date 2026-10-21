package control

import (
	"context"
	"fmt"
	"net"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/zela-dev/zela/internal/engine"
)

// StorageBackend describes an administrator-managed NFSv4 export. Zela does
// not install, secure, replicate, or back up the NFS server.
type StorageBackend struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Server    string    `json:"server"`
	Export    string    `json:"export"`
	WorkerIDs []string  `json:"workerIDs"`
	CreatedAt time.Time `json:"createdAt"`
	Revision  int64     `json:"revision"`
}

// StorageAccess records the last application-user access check on a worker.
type StorageAccess struct {
	CheckedAt       time.Time `json:"checkedAt"`
	BackendRevision int64     `json:"backendRevision"`
	Image           string    `json:"image"`
	ReadOnly        bool      `json:"readOnly"`
	Error           string    `json:"error,omitempty"`
}

// SharedVolume exposes one administrator-approved directory from an NFS export.
type SharedVolume struct {
	Identity  string                   `json:"identity"`
	Access    map[string]StorageAccess `json:"access,omitempty"`
	ID        string                   `json:"id"`
	Name      string                   `json:"name"`
	BackendID string                   `json:"backendID"`
	Directory string                   `json:"directory"`
	CreatedAt time.Time                `json:"createdAt"`
}

// VolumeAttachment mounts a registered volume at an absolute container path.
type VolumeAttachment struct {
	VolumeID string `json:"volumeID"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

const (
	WorkloadOverlapSafe  = "overlap-safe"
	WorkloadSingleWriter = "single-writer"
)

var storageIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
var storageHostPattern = regexp.MustCompile(`^(?i)[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*\.?$`)

func validateStorageBackend(b StorageBackend) error {
	if !storageIDPattern.MatchString(b.Name) || len(b.Name) > 63 {
		return fmt.Errorf("storage backend name must be 1–63 letters, numbers, dots, underscores or hyphens")
	}
	if b.Server == "" || strings.ContainsAny(b.Server, " /\\\x00\r\n") {
		return fmt.Errorf("NFS server must be a hostname or IP address")
	}
	if ip := net.ParseIP(b.Server); ip == nil && (len(b.Server) > 253 || !storageHostPattern.MatchString(b.Server)) {
		return fmt.Errorf("NFS server must be a hostname or IP address without a port")
	}
	if !path.IsAbs(b.Export) || path.Clean(b.Export) != b.Export || strings.ContainsAny(b.Export, "\x00\r\n") {
		return fmt.Errorf("NFS export must be a clean absolute path")
	}
	if len(b.WorkerIDs) == 0 {
		return fmt.Errorf("select at least one approved worker")
	}
	seen := map[string]bool{}
	for _, id := range b.WorkerIDs {
		if id == "" || seen[id] {
			return fmt.Errorf("worker IDs must be non-empty and unique")
		}
		seen[id] = true
	}
	return nil
}

func validateVolumeDirectory(dir string) error {
	if dir == "." || strings.HasPrefix(dir, "/") || path.Clean(dir) != dir || dir == "" {
		return fmt.Errorf("volume directory must be a relative path inside the approved export")
	}
	for _, segment := range strings.Split(dir, "/") {
		if segment == ".." || segment == "." || segment == "" {
			return fmt.Errorf("volume directory cannot contain dot or empty path segments")
		}
	}
	if strings.ContainsAny(dir, "\x00\r\n") {
		return fmt.Errorf("invalid volume directory")
	}
	return nil
}

func validateAttachments(d *Deployment, s State) error {
	if len(d.StorageAttachments) == 0 {
		if d.WorkloadPolicy != "" && d.WorkloadPolicy != WorkloadOverlapSafe && d.WorkloadPolicy != WorkloadSingleWriter {
			return fmt.Errorf("workloadPolicy must be overlap-safe or single-writer")
		}
		return nil
	}
	if d.WorkloadPolicy != WorkloadOverlapSafe && d.WorkloadPolicy != WorkloadSingleWriter {
		return fmt.Errorf("storage attachments require workloadPolicy overlap-safe or single-writer")
	}
	if len(d.StorageAttachments) > 16 {
		return fmt.Errorf("at most 16 storage attachments are supported")
	}
	seenVolume, seenTarget := map[string]bool{}, map[string]bool{}
	for _, attachment := range d.StorageAttachments {
		volume, ok := s.SharedVolumes[attachment.VolumeID]
		if !ok {
			return fmt.Errorf("shared volume %q is not registered", attachment.VolumeID)
		}
		if !strings.HasPrefix(attachment.Target, "/") || path.Clean(attachment.Target) != attachment.Target || attachment.Target == "/" || strings.ContainsAny(attachment.Target, "\x00\r\n") {
			return fmt.Errorf("volume mount target must be a clean absolute path other than /")
		}
		if seenVolume[attachment.VolumeID] || seenTarget[attachment.Target] {
			return fmt.Errorf("volume IDs and mount targets must be unique")
		}
		seenVolume[attachment.VolumeID], seenTarget[attachment.Target] = true, true
		if !attachment.ReadOnly && d.WorkloadPolicy != WorkloadOverlapSafe && d.WorkloadPolicy != WorkloadSingleWriter {
			return fmt.Errorf("writable shared volumes require an explicit workload policy")
		}

		for _, existing := range s.Deployments {
			if existing.ID == d.ID {
				continue
			}
			for _, mounted := range existing.StorageAttachments {
				if mounted.VolumeID == attachment.VolumeID && !mounted.ReadOnly && !attachment.ReadOnly && (existing.WorkloadPolicy == WorkloadSingleWriter || d.WorkloadPolicy == WorkloadSingleWriter) {
					return fmt.Errorf("volume %s is reserved by an exclusive writer", volume.Name)
				}
			}
		}
	}
	return nil
}

// RegisterStorageBackend is available to the cluster administrator only.
func (c *Controller) RegisterStorageBackend(b StorageBackend) (StorageBackend, error) {
	b.Name = strings.TrimSpace(b.Name)
	b.Server = strings.TrimSpace(b.Server)
	b.Export = strings.TrimSpace(b.Export)
	if err := validateStorageBackend(b); err != nil {
		return StorageBackend{}, err
	}
	b.ID, b.CreatedAt, b.Revision = newID("nfs_"), time.Now().UTC(), 1
	err := c.Store.update(func(s *State) error {
		if s.StorageBackends == nil {
			s.StorageBackends = map[string]StorageBackend{}
		}
		for _, current := range s.StorageBackends {
			if current.Name == b.Name || (current.Server == b.Server && (current.Export == b.Export || strings.HasPrefix(current.Export, b.Export+"/") || strings.HasPrefix(b.Export, current.Export+"/"))) {
				return fmt.Errorf("storage backend name or export is already registered")
			}
		}
		for _, id := range b.WorkerIDs {
			w, ok := s.Workers[id]
			if !ok || w.Status != "online" || w.Mode != "active" {
				return fmt.Errorf("approved workers must be active and online")
			}
		}
		s.StorageBackends[b.ID] = b
		return nil
	})
	return b, err
}

func (c *Controller) RegisterSharedVolume(v SharedVolume) (SharedVolume, error) {
	v.Name = strings.TrimSpace(v.Name)
	v.Directory = strings.TrimSpace(v.Directory)
	if !storageIDPattern.MatchString(v.Name) {
		return SharedVolume{}, fmt.Errorf("volume name must use letters, numbers, dots, underscores or hyphens")
	}
	if err := validateVolumeDirectory(v.Directory); err != nil {
		return SharedVolume{}, err
	}
	v.ID, v.CreatedAt = newID("vol_"), time.Now().UTC()
	if v.Identity == "" {
		v.Identity = v.ID
	}
	if !storageIDPattern.MatchString(v.Identity) {
		return SharedVolume{}, fmt.Errorf("identity must be 1-63 letters, numbers, dots, underscores or hyphens")
	}
	v.Access = nil
	err := c.Store.update(func(s *State) error {
		b, ok := s.StorageBackends[v.BackendID]
		if !ok {
			return fmt.Errorf("storage backend not found")
		}
		for _, current := range s.SharedVolumes {
			if current.Name == v.Name || current.Identity == v.Identity || (current.BackendID == v.BackendID && (current.Directory == v.Directory || strings.HasPrefix(current.Directory, v.Directory+"/") || strings.HasPrefix(v.Directory, current.Directory+"/"))) {
				return fmt.Errorf("volume name or directory is already registered")
			}
		}
		if s.SharedVolumes == nil {
			s.SharedVolumes = map[string]SharedVolume{}
		}
		_ = b
		s.SharedVolumes[v.ID] = v
		return nil
	})
	return v, err
}

func (c *Controller) RemoveSharedVolume(id string) error {
	return c.Store.update(func(s *State) error {
		if _, ok := s.SharedVolumes[id]; !ok {
			return fmt.Errorf("shared volume not found")
		}
		for _, d := range s.Deployments {
			for _, attachment := range d.StorageAttachments {
				if attachment.VolumeID == id {
					return fmt.Errorf("shared volume is still attached to deployment %s", d.Name)
				}
			}
		}
		delete(s.SharedVolumes, id)
		return nil
	})
}

func (c *Controller) RemoveStorageBackend(id string) error {
	return c.Store.update(func(s *State) error {
		if _, ok := s.StorageBackends[id]; !ok {
			return fmt.Errorf("storage backend not found")
		}
		for _, v := range s.SharedVolumes {
			if v.BackendID == id {
				return fmt.Errorf("storage backend still has registered volumes")
			}
		}
		delete(s.StorageBackends, id)
		return nil
	})
}

func managedVolumeName(id string) string { return "zela-" + strings.TrimPrefix(id, "vol_") }

func validateManagedTaskMounts(d Deployment, inspection engine.Inspection) error {
	expected := map[string]VolumeAttachment{}
	targets := map[string]bool{}
	for _, a := range d.StorageAttachments {
		expected[a.Target] = a
		targets[a.Target] = true
	}
	for _, mount := range inspection.Mounts {
		attachment, ok := expected[mount.Destination]
		if !ok || mount.Type != "volume" || mount.Name != managedVolumeName(attachment.VolumeID) || mount.RW == attachment.ReadOnly {
			return fmt.Errorf("has a local, anonymous, image, unregistered, or mismatched mount and cannot move automatically")
		}
		delete(expected, mount.Destination)
	}
	if len(expected) > 0 {
		return fmt.Errorf("registered shared volume attachments are missing from the container")
	}

	for target := range inspection.Config.Volumes {
		if !targets[target] {
			return fmt.Errorf("image declares an unregistered volume at %s", target)
		}
	}
	return nil
}

func (c *Controller) prepareVolumes(ctx context.Context, e engine.Engine, workerID string, attachments []VolumeAttachment) error {
	s := c.Store.snapshot()
	for _, a := range attachments {
		v, ok := s.SharedVolumes[a.VolumeID]
		if !ok {
			return fmt.Errorf("shared volume %q is no longer registered", a.VolumeID)
		}
		b, ok := s.StorageBackends[v.BackendID]
		if !ok {
			return fmt.Errorf("storage backend for volume %q is no longer registered", v.Name)
		}
		approved := false
		for _, id := range b.WorkerIDs {
			if id == workerID {
				approved = true
				break
			}
		}
		if !approved {
			return fmt.Errorf("worker is not approved for NFS backend %q", b.Name)
		}
		preparer, ok := e.(engine.SharedVolumePreparer)
		if !ok {
			return fmt.Errorf("worker does not support managed shared volumes; update its Zela worker agent")
		}
		if err := preparer.EnsureSharedVolume(ctx, engine.SharedVolumeSpec{Name: managedVolumeName(v.ID), Server: b.Server, Export: path.Join(b.Export, v.Directory)}); err != nil {
			return fmt.Errorf("prepare shared volume %q: %w", v.Name, err)
		}
	}
	return nil
}

// verifyVolumeAccess is deliberately not cached for admission: every startup
// checks the same image user, identity, access mode, and backend revision.
func (c *Controller) verifyVolumeAccess(ctx context.Context, d Deployment, e engine.Engine, workerID, image string) error {
	var targets []string
	for _, a := range d.StorageAttachments {
		targets = append(targets, a.Target)
	}
	for _, a := range d.StorageAttachments {
		s := c.Store.snapshot()
		v, ok := s.SharedVolumes[a.VolumeID]
		if !ok {
			return fmt.Errorf("volume is no longer registered")
		}
		b, ok := s.StorageBackends[v.BackendID]
		if !ok {
			return fmt.Errorf("storage backend is no longer registered")
		}
		verifier, ok := e.(engine.SharedVolumeVerifier)
		if !ok {
			return fmt.Errorf("worker does not support bounded shared storage access checks; update its agent")
		}
		probe, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := verifier.VerifySharedVolume(probe, engine.SharedVolumeProbe{Targets: targets, Volume: engine.SharedVolumeSpec{Name: managedVolumeName(v.ID), Server: b.Server, Export: path.Join(b.Export, v.Directory)}, Identity: v.Identity, Image: image, ReadOnly: a.ReadOnly})
		cancel()
		observation := StorageAccess{CheckedAt: time.Now().UTC(), BackendRevision: b.Revision, Image: image, ReadOnly: a.ReadOnly}
		if err != nil {
			observation.Error = err.Error()
		}
		saveErr := c.Store.update(func(s *State) error {
			volume, ok := s.SharedVolumes[v.ID]
			if !ok {
				return fmt.Errorf("volume disappeared during verification")
			}
			if volume.Access == nil {
				volume.Access = map[string]StorageAccess{}
			}
			volume.Access[workerID] = observation
			s.SharedVolumes[v.ID] = volume
			return nil
		})
		if err != nil {
			return fmt.Errorf("verify shared volume %s: %w", v.Name, err)
		}
		if saveErr != nil {
			return saveErr
		}
	}
	return nil
}
