// Package storageprobe verifies access from an isolated application container.
package storageprobe

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Run is invoked only by the worker executable's fixed storage-probe mode.
// The worker mounts the registered volume at /zela-volume. It never follows a
// marker or health-directory symlink to another application directory.
func Run(root, identity string, readOnly bool) error {
	marker := filepath.Join(root, ".zela-identity")
	info, err := os.Lstat(marker)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("identity marker must be a regular file")
	}
	f, err := os.Open(marker)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, 1025))
	f.Close()
	if err != nil {
		return err
	}
	if len(data) > 1024 || strings.TrimSpace(string(data)) != identity {
		return fmt.Errorf("shared volume identity does not match")
	}
	if readOnly {
		return nil
	}
	health := filepath.Join(root, ".zela-health")
	info, err = os.Lstat(health)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("health directory must be a real directory")
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	name := filepath.Join(health, "probe-"+hex.EncodeToString(nonce[:]))
	file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	defer os.Remove(name)
	if _, err = file.Write(nonce[:]); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if _, err = file.Seek(0, 0); err != nil {
		return err
	}
	got, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	if string(got) != string(nonce[:]) {
		return fmt.Errorf("shared storage read/write verification failed")
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Remove(name)
}
