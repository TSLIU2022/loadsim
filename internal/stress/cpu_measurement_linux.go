//go:build linux

package stress

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
)

func platformProcessCPUSampler() processCPUSampler {
	return sampleLinuxProcessCPU
}

func sampleLinuxProcessCPU(ctx context.Context) (processCPUSnapshot, error) {
	select {
	case <-ctx.Done():
		return processCPUSnapshot{}, ctx.Err()
	default:
	}

	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return processCPUSnapshot{}, fmt.Errorf("read /proc/self/stat: %w", err)
	}
	totalCPU, err := parseLinuxProcessCPUTime(stat, cpu.ClocksPerSec)
	if err != nil {
		return processCPUSnapshot{}, err
	}

	select {
	case <-ctx.Done():
		return processCPUSnapshot{}, ctx.Err()
	default:
	}
	return processCPUSnapshot{
		totalCPU:   totalCPU,
		observedAt: time.Now(),
	}, nil
}

func parseLinuxProcessCPUTime(stat []byte, clocksPerSecond float64) (time.Duration, error) {
	if !isFinite(clocksPerSecond) || clocksPerSecond <= 0 {
		return 0, fmt.Errorf("invalid Linux clock tick rate %v", clocksPerSecond)
	}

	closeParen := strings.LastIndexByte(string(stat), ')')
	if closeParen < 0 || closeParen+1 >= len(stat) {
		return 0, fmt.Errorf("invalid /proc/self/stat process name")
	}
	fields := strings.Fields(string(stat[closeParen+1:]))
	const (
		utimeIndex = 11
		stimeIndex = 12
	)
	if len(fields) <= stimeIndex {
		return 0, fmt.Errorf("invalid /proc/self/stat field count")
	}

	userTicks, err := strconv.ParseUint(fields[utimeIndex], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse /proc/self/stat user CPU time: %w", err)
	}
	systemTicks, err := strconv.ParseUint(fields[stimeIndex], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse /proc/self/stat system CPU time: %w", err)
	}
	if userTicks > math.MaxUint64-systemTicks {
		return 0, fmt.Errorf("process CPU tick count overflow")
	}

	totalNanoseconds := float64(userTicks+systemTicks) *
		float64(time.Second) / clocksPerSecond
	if !isFinite(totalNanoseconds) || totalNanoseconds < 0 ||
		totalNanoseconds > float64(math.MaxInt64) {
		return 0, fmt.Errorf("process CPU time is out of range")
	}
	return time.Duration(math.Round(totalNanoseconds)), nil
}
