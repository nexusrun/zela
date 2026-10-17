package main

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func cpuCount() int { return runtime.NumCPU() }

// memoryMb returns total and in-use memory from /proc/meminfo. "In use" is
// MemTotal - MemAvailable, so page cache the kernel can reclaim is not counted.
func memoryMb() (total, used int, err error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	return parseMeminfo(f)
}

func parseMeminfo(r io.Reader) (total, used int, err error) {
	var totalKb, availKb int64 = -1, -1
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		v, convErr := strconv.ParseInt(fields[1], 10, 64)
		if convErr != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			totalKb = v
		case "MemAvailable:":
			availKb = v
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	if totalKb < 0 || availKb < 0 {
		return 0, 0, fmt.Errorf("MemTotal or MemAvailable missing from /proc/meminfo")
	}
	return int(totalKb / 1024), int((totalKb - availKb) / 1024), nil
}

// diskFreePct is the share of the filesystem at path available to unprivileged
// writers, rounded to one decimal.
func diskFreePct(path string) (float64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	if st.Blocks == 0 {
		return 0, fmt.Errorf("%s reports zero blocks", path)
	}
	return math.Round(float64(st.Bavail)/float64(st.Blocks)*1000) / 10, nil
}
