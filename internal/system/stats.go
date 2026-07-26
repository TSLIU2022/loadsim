package system

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/mem"
)

type Stats struct {
	MemoryPercent float64
	MemoryUsedMB  uint64
	MemoryTotalMB uint64
	MemoryScope   string
	ProcessRSSMB  uint64
}

// Snapshot returns instantaneous memory and process metrics. Host CPU is
// intentionally omitted: host-scope CPU control owns the validated sampler,
// while status printing must never block signal handling on a sample window.
func Snapshot() (Stats, error) {
	memStat, err := mem.VirtualMemory()
	if err != nil {
		return Stats{}, err
	}

	stats := Stats{
		MemoryPercent: memStat.UsedPercent,
		MemoryUsedMB:  memStat.Used / (1024 * 1024),
		MemoryTotalMB: memStat.Total / (1024 * 1024),
		MemoryScope:   "host",
	}
	if capacity, capacityErr := EffectiveMemoryCapacity(); capacityErr == nil {
		stats.MemoryScope = capacity.Source
		stats.MemoryUsedMB = capacity.UsedMB
		stats.MemoryTotalMB = capacity.TotalMB
		if capacity.TotalMB > 0 {
			stats.MemoryPercent = float64(capacity.UsedMB) * 100 / float64(capacity.TotalMB)
		}
	}
	if rssMB, rssErr := ProcessRSSMB(); rssErr == nil {
		stats.ProcessRSSMB = rssMB
	}

	return stats, nil
}

func ProcessRSSMB() (uint64, error) {
	raw, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return 0, fmt.Errorf("invalid /proc/self/statm")
	}
	residentPages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, err
	}
	return residentPages * uint64(os.Getpagesize()) / (1024 * 1024), nil
}
