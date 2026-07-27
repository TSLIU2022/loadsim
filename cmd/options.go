package cmd

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/fanderchan/loadsim/internal/stress"

	"github.com/spf13/cobra"
)

type percentBand struct {
	low  float64
	high float64
}

func parsePercentBand(
	value string,
	name string,
	maximum float64,
	minimumWidth float64,
) (percentBand, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return percentBand{}, fmt.Errorf(
			"%s must use LOW:HIGH, for example 30:50",
			name,
		)
	}
	low, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return percentBand{}, fmt.Errorf("%s lower bound is invalid", name)
	}
	high, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return percentBand{}, fmt.Errorf("%s upper bound is invalid", name)
	}
	if math.IsNaN(low) || math.IsInf(low, 0) ||
		math.IsNaN(high) || math.IsInf(high, 0) {
		return percentBand{}, fmt.Errorf("%s bounds must be finite", name)
	}
	if low < 0 || high > maximum || low >= high {
		return percentBand{}, fmt.Errorf(
			"%s must satisfy 0 <= LOW < HIGH <= %.0f",
			name,
			maximum,
		)
	}
	if high-low < minimumWidth {
		return percentBand{}, fmt.Errorf(
			"%s must be at least %.0f percentage points wide",
			name,
			minimumWidth,
		)
	}
	return percentBand{low: low, high: high}, nil
}

func parsePositiveIntegerRange(value string, name string) (int, int, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf(
			"%s must use LOW:HIGH, for example 1024:4096",
			name,
		)
	}
	low, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("%s lower bound is invalid", name)
	}
	high, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("%s upper bound is invalid", name)
	}
	if low < 0 || high <= 0 || low >= high {
		return 0, 0, fmt.Errorf(
			"%s must satisfy 0 <= LOW < HIGH",
			name,
		)
	}
	return low, high, nil
}

func gibToMiB(value int, name string) (int, error) {
	if value <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", name)
	}
	if value > math.MaxInt/1024 {
		return 0, fmt.Errorf("%s is too large", name)
	}
	return value * 1024, nil
}

func selectMemoryMaximum(mib int, gib int) (int, error) {
	var selected int
	switch {
	case mib > 0 && gib > 0:
		return 0, fmt.Errorf(
			"use exactly one of --memory-max-mib or --memory-max-gib",
		)
	case mib <= 0 && gib <= 0:
		return 0, fmt.Errorf(
			"memory fill requires exactly one of --memory-max-mib or --memory-max-gib",
		)
	case mib > 0:
		selected = mib
	default:
		converted, err := gibToMiB(gib, "memory maximum GiB")
		if err != nil {
			return 0, err
		}
		selected = converted
	}
	if selected > math.MaxInt/(1024*1024) {
		return 0, fmt.Errorf("memory maximum exceeds addressable memory")
	}
	return selected, nil
}

func parseWorkerScheduler(value string) (stress.CPUWorkerScheduler, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(stress.WorkerSchedulerIdle):
		return stress.WorkerSchedulerIdle, nil
	case string(stress.WorkerSchedulerNormal):
		return stress.WorkerSchedulerNormal, nil
	default:
		return "", fmt.Errorf("CPU worker scheduler must be idle or normal")
	}
}

func parseWorkerNice(value string) (*int, error) {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "inherit") {
		workerNice := stress.WorkerNiceInherit
		return &workerNice, nil
	}

	workerNice, err := strconv.Atoi(value)
	if err != nil || workerNice < 0 || workerNice > 19 {
		return nil, fmt.Errorf(
			"CPU worker nice must be an integer from 0 to 19, or inherit",
		)
	}
	return &workerNice, nil
}

func formatWorkerNice(value int) string {
	if value == stress.WorkerNiceInherit {
		return "inherit"
	}
	return strconv.Itoa(value)
}

func formatCPUBoundary(
	kind stress.VisibleSystemCPUBoundaryKind,
	id string,
) string {
	if id == "" {
		return "none"
	}
	if kind == "" {
		kind = stress.VisibleSystemCPUBoundaryUnknown
	}
	return fmt.Sprintf("%s:%s", kind, id)
}

func anyFlagChanged(command *cobra.Command, names ...string) bool {
	for _, name := range names {
		if command.Flags().Changed(name) {
			return true
		}
	}
	return false
}
