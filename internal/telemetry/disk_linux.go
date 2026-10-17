package telemetry

import "syscall"

func diskUsage(path string) (Disk, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return Disk{}, err
	}
	size := uint64(s.Bsize)
	d := Disk{Path: path, TotalBytes: s.Blocks * size, UsedBytes: (s.Blocks - s.Bfree) * size, AvailableBytes: s.Bavail * size}
	d.UsedPercent = Percent(d.UsedBytes, d.UsedBytes+d.AvailableBytes)
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err == nil {
		d.device = uint64(st.Dev)
	}
	return d, nil
}
