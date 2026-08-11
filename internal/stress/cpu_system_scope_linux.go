//go:build linux

package stress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	visibleSystemCPUSourceSchedstat  = "proc:schedstat"
	linuxSchedstatPath               = "/proc/schedstat"
	linuxSchedstatCPUFieldCount      = 10
	linuxSchedstatMinimumVersion     = 10
	linuxSchedstatMaximumVersion     = 17
	linuxSchedstatBusyOvershootRatio = 0.005
	linuxSchedstatReadMaxAttempts    = 3
)

type linuxSchedstatSnapshot struct {
	version  uint64
	cpuIDs   []int
	runtimes map[int]uint64
}

type linuxSchedstatReadDelayError struct {
	delay time.Duration
	limit time.Duration
}

func (e *linuxSchedstatReadDelayError) Error() string {
	return fmt.Sprintf(
		"whole-machine CPU scheduler counter read took %s, exceeding the safe limit %s",
		e.delay,
		e.limit,
	)
}

type linuxVisibleCPUSampleDependencies struct {
	readFile func(string) ([]byte, error)
	now      func() time.Time
	wait     func(context.Context, time.Duration) error
}

func sampleVisibleSystemCPU(
	ctx context.Context,
	sampleDuration time.Duration,
) (VisibleSystemCPUSample, error) {
	return sampleVisibleSystemCPUFrom(
		ctx,
		sampleDuration,
		linuxVisibleCPUSampleDependencies{
			readFile: os.ReadFile,
			now:      time.Now,
			wait:     waitVisibleSystemCPUSample,
		},
	)
}

func inspectVisibleSystemCPU() (VisibleSystemCPUInfo, error) {
	data, err := os.ReadFile(linuxSchedstatPath)
	if err != nil {
		return VisibleSystemCPUInfo{}, fmt.Errorf(
			"read whole-machine CPU scheduler statistics: %w",
			err,
		)
	}
	return inspectVisibleSystemCPUFrom(data)
}

func inspectVisibleSystemCPUFrom(data []byte) (VisibleSystemCPUInfo, error) {
	snapshot, err := parseLinuxSchedstat(data)
	if err != nil {
		return VisibleSystemCPUInfo{}, err
	}
	return visibleSystemCPUInfoFromSchedstat(snapshot), nil
}

func sampleVisibleSystemCPUFrom(
	ctx context.Context,
	sampleDuration time.Duration,
	dependencies linuxVisibleCPUSampleDependencies,
) (VisibleSystemCPUSample, error) {
	if ctx == nil {
		return VisibleSystemCPUSample{}, fmt.Errorf(
			"whole-machine CPU sample context must not be nil",
		)
	}
	if sampleDuration <= 0 {
		return VisibleSystemCPUSample{}, fmt.Errorf(
			"whole-machine CPU sample duration must be greater than zero",
		)
	}
	if dependencies.readFile == nil ||
		dependencies.now == nil ||
		dependencies.wait == nil {
		return VisibleSystemCPUSample{}, fmt.Errorf(
			"whole-machine CPU sampler dependencies are incomplete",
		)
	}
	if err := ctx.Err(); err != nil {
		return VisibleSystemCPUSample{}, err
	}

	readDelayLimit := visibleSystemCPUReadDelayLimit(sampleDuration)
	start, startedAt, err := readLinuxSchedstatAtMidpointWithRetry(
		ctx,
		dependencies,
		readDelayLimit,
	)
	if err != nil {
		return VisibleSystemCPUSample{}, err
	}
	if err := ctx.Err(); err != nil {
		return VisibleSystemCPUSample{}, err
	}
	if err := dependencies.wait(ctx, sampleDuration); err != nil {
		return VisibleSystemCPUSample{}, err
	}
	if err := ctx.Err(); err != nil {
		return VisibleSystemCPUSample{}, err
	}
	end, finishedAt, err := readLinuxSchedstatAtMidpointWithRetry(
		ctx,
		dependencies,
		readDelayLimit,
	)
	if err != nil {
		return VisibleSystemCPUSample{}, err
	}
	if err := ctx.Err(); err != nil {
		return VisibleSystemCPUSample{}, err
	}

	elapsed := finishedAt.Sub(startedAt)
	if elapsed <= 0 {
		return VisibleSystemCPUSample{}, fmt.Errorf(
			"whole-machine CPU sample interval must be greater than zero",
		)
	}
	if elapsed < sampleDuration {
		return VisibleSystemCPUSample{}, fmt.Errorf(
			"whole-machine CPU sample interval %s was shorter than requested %s",
			elapsed,
			sampleDuration,
		)
	}
	if !sameLinuxSchedstatBoundary(start, end) {
		return VisibleSystemCPUSample{}, fmt.Errorf(
			"whole-machine CPU scheduler boundary changed during sampling",
		)
	}

	busy, err := linuxSchedstatBusyDelta(start, end)
	if err != nil {
		return VisibleSystemCPUSample{}, err
	}
	busy, percent, err := normalizeLinuxSchedstatCPUUsage(
		busy,
		elapsed,
		float64(len(start.cpuIDs)),
	)
	if err != nil {
		return VisibleSystemCPUSample{}, err
	}
	info := visibleSystemCPUInfoFromSchedstat(start)
	return VisibleSystemCPUSample{
		Source:       info.Source,
		BoundaryID:   info.BoundaryID,
		BoundaryKind: info.BoundaryKind,
		CPUs:         info.CPUs,
		Percent:      percent,
		Busy:         busy,
		Elapsed:      elapsed,
	}, nil
}

func parseLinuxSchedstat(data []byte) (linuxSchedstatSnapshot, error) {
	snapshot := linuxSchedstatSnapshot{
		runtimes: make(map[int]uint64),
	}
	var hasVersion bool
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		switch {
		case fields[0] == "version":
			if hasVersion {
				return linuxSchedstatSnapshot{}, fmt.Errorf(
					"duplicate /proc/schedstat version line",
				)
			}
			if len(fields) != 2 {
				return linuxSchedstatSnapshot{}, fmt.Errorf(
					"invalid /proc/schedstat version line",
				)
			}
			version, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return linuxSchedstatSnapshot{}, fmt.Errorf(
					"invalid /proc/schedstat version %q",
					fields[1],
				)
			}
			if version < linuxSchedstatMinimumVersion ||
				version > linuxSchedstatMaximumVersion {
				return linuxSchedstatSnapshot{}, fmt.Errorf(
					"unsupported /proc/schedstat version %d; supported versions are %d through %d",
					version,
					linuxSchedstatMinimumVersion,
					linuxSchedstatMaximumVersion,
				)
			}
			snapshot.version = version
			hasVersion = true
		case strings.HasPrefix(fields[0], "cpu"):
			cpuID, err := parseLinuxSchedstatCPUID(fields[0])
			if err != nil {
				return linuxSchedstatSnapshot{}, err
			}
			if len(fields) < linuxSchedstatCPUFieldCount {
				return linuxSchedstatSnapshot{}, fmt.Errorf(
					"%s contains %d fields, want at least %d",
					fields[0],
					len(fields),
					linuxSchedstatCPUFieldCount,
				)
			}
			if _, exists := snapshot.runtimes[cpuID]; exists {
				return linuxSchedstatSnapshot{}, fmt.Errorf(
					"duplicate /proc/schedstat CPU %d",
					cpuID,
				)
			}
			runtimeNS, err := strconv.ParseUint(fields[7], 10, 64)
			if err != nil {
				return linuxSchedstatSnapshot{}, fmt.Errorf(
					"parse %s scheduler runtime %q: %w",
					fields[0],
					fields[7],
					err,
				)
			}
			snapshot.runtimes[cpuID] = runtimeNS
			snapshot.cpuIDs = append(snapshot.cpuIDs, cpuID)
		}
	}
	if err := scanner.Err(); err != nil {
		return linuxSchedstatSnapshot{}, fmt.Errorf(
			"scan /proc/schedstat: %w",
			err,
		)
	}
	if !hasVersion {
		return linuxSchedstatSnapshot{}, fmt.Errorf(
			"/proc/schedstat version line is missing",
		)
	}
	if len(snapshot.cpuIDs) == 0 {
		return linuxSchedstatSnapshot{}, fmt.Errorf(
			"/proc/schedstat contains no CPU runtime counters",
		)
	}
	sort.Ints(snapshot.cpuIDs)
	return snapshot, nil
}

func parseLinuxSchedstatCPUID(value string) (int, error) {
	suffix := strings.TrimPrefix(value, "cpu")
	if suffix == "" {
		return 0, fmt.Errorf("invalid /proc/schedstat CPU label %q", value)
	}
	cpuID, err := strconv.Atoi(suffix)
	if err != nil || cpuID < 0 {
		return 0, fmt.Errorf("invalid /proc/schedstat CPU label %q", value)
	}
	return cpuID, nil
}

func visibleSystemCPUInfoFromSchedstat(
	snapshot linuxSchedstatSnapshot,
) VisibleSystemCPUInfo {
	return VisibleSystemCPUInfo{
		Source:       visibleSystemCPUSourceSchedstat,
		BoundaryID:   linuxSchedstatBoundaryID(snapshot),
		BoundaryKind: VisibleSystemCPUBoundaryHost,
		CPUs:         float64(len(snapshot.cpuIDs)),
	}
}

func linuxSchedstatBoundaryID(snapshot linuxSchedstatSnapshot) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "version=%d\n", snapshot.version)
	for _, cpuID := range snapshot.cpuIDs {
		_, _ = fmt.Fprintf(hash, "cpu=%d\n", cpuID)
	}
	sum := hash.Sum(nil)
	return "schedcpu-" + hex.EncodeToString(sum[:8])
}

func sameLinuxSchedstatBoundary(
	first linuxSchedstatSnapshot,
	second linuxSchedstatSnapshot,
) bool {
	if first.version != second.version || len(first.cpuIDs) != len(second.cpuIDs) {
		return false
	}
	for index := range first.cpuIDs {
		if first.cpuIDs[index] != second.cpuIDs[index] {
			return false
		}
	}
	return true
}

func linuxSchedstatBusyDelta(
	start linuxSchedstatSnapshot,
	end linuxSchedstatSnapshot,
) (time.Duration, error) {
	var total uint64
	for _, cpuID := range start.cpuIDs {
		startRuntime := start.runtimes[cpuID]
		endRuntime := end.runtimes[cpuID]
		if endRuntime < startRuntime {
			return 0, fmt.Errorf(
				"CPU %d scheduler runtime counter moved backwards",
				cpuID,
			)
		}
		delta := endRuntime - startRuntime
		if total > math.MaxUint64-delta {
			return 0, fmt.Errorf(
				"whole-machine CPU scheduler runtime overflow",
			)
		}
		total += delta
	}
	if total > uint64(math.MaxInt64) {
		return 0, fmt.Errorf(
			"whole-machine CPU busy duration is out of range",
		)
	}
	return time.Duration(total), nil
}

func calculateLinuxSchedstatCPUPercent(
	busy time.Duration,
	elapsed time.Duration,
	cpus float64,
) (float64, error) {
	_, percent, err := normalizeLinuxSchedstatCPUUsage(busy, elapsed, cpus)
	return percent, err
}

func normalizeLinuxSchedstatCPUUsage(
	busy time.Duration,
	elapsed time.Duration,
	cpus float64,
) (time.Duration, float64, error) {
	if busy < 0 {
		return 0, 0, fmt.Errorf(
			"whole-machine CPU busy duration must not be negative",
		)
	}
	if elapsed <= 0 {
		return 0, 0, fmt.Errorf(
			"whole-machine CPU sample interval must be greater than zero",
		)
	}
	if !isFinite(cpus) || cpus <= 0 {
		return 0, 0, fmt.Errorf("whole-machine CPU capacity is invalid")
	}

	capacityNanoseconds := float64(elapsed) * cpus
	if !isFinite(capacityNanoseconds) ||
		capacityNanoseconds <= 0 ||
		capacityNanoseconds >= float64(math.MaxInt64) {
		return 0, 0, fmt.Errorf(
			"whole-machine CPU utilization calculation is out of range",
		)
	}

	// Scheduler runtime and userspace monotonic time can differ slightly at
	// saturation. Accept only a narrow skew, clamp it to physical capacity,
	// and continue to fail closed for materially impossible counter deltas.
	maximumBusyNanoseconds := capacityNanoseconds *
		(1 + linuxSchedstatBusyOvershootRatio)
	if float64(busy) > maximumBusyNanoseconds {
		return 0, 0, fmt.Errorf(
			"whole-machine CPU busy duration %s exceeds physical capacity %s beyond %.3f%% tolerance",
			busy,
			time.Duration(capacityNanoseconds),
			linuxSchedstatBusyOvershootRatio*100,
		)
	}
	if float64(busy) > capacityNanoseconds {
		busy = time.Duration(capacityNanoseconds)
	}

	percent := float64(busy) / capacityNanoseconds * 100
	if !isFinite(percent) || percent < 0 || percent > 100 {
		return 0, 0, fmt.Errorf(
			"whole-machine CPU utilization calculation is out of range",
		)
	}
	return busy, percent, nil
}

func visibleSystemCPUReadDelayLimit(
	sampleDuration time.Duration,
) time.Duration {
	const (
		minimum = 10 * time.Millisecond
		maximum = 100 * time.Millisecond
	)
	limit := sampleDuration / 10
	if limit < minimum {
		return minimum
	}
	if limit > maximum {
		return maximum
	}
	return limit
}

func readLinuxSchedstatAtMidpoint(
	dependencies linuxVisibleCPUSampleDependencies,
	delayLimit time.Duration,
) (linuxSchedstatSnapshot, time.Time, error) {
	before := dependencies.now()
	data, err := dependencies.readFile(linuxSchedstatPath)
	after := dependencies.now()
	if err != nil {
		return linuxSchedstatSnapshot{}, time.Time{}, fmt.Errorf(
			"read whole-machine CPU scheduler statistics: %w",
			err,
		)
	}
	delay := after.Sub(before)
	if delay < 0 {
		return linuxSchedstatSnapshot{}, time.Time{}, fmt.Errorf(
			"whole-machine CPU scheduler counter read time moved backwards",
		)
	}
	if delay > delayLimit {
		return linuxSchedstatSnapshot{}, time.Time{}, &linuxSchedstatReadDelayError{
			delay: delay,
			limit: delayLimit,
		}
	}
	snapshot, err := parseLinuxSchedstat(data)
	if err != nil {
		return linuxSchedstatSnapshot{}, time.Time{}, err
	}
	return snapshot, before.Add(delay / 2), nil
}

func readLinuxSchedstatAtMidpointWithRetry(
	ctx context.Context,
	dependencies linuxVisibleCPUSampleDependencies,
	delayLimit time.Duration,
) (linuxSchedstatSnapshot, time.Time, error) {
	var lastDelayErr *linuxSchedstatReadDelayError
	for attempt := 1; attempt <= linuxSchedstatReadMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return linuxSchedstatSnapshot{}, time.Time{}, err
		}
		snapshot, observedAt, err := readLinuxSchedstatAtMidpoint(
			dependencies,
			delayLimit,
		)
		if err == nil {
			return snapshot, observedAt, nil
		}
		if !errors.As(err, &lastDelayErr) {
			return linuxSchedstatSnapshot{}, time.Time{}, err
		}
	}
	return linuxSchedstatSnapshot{}, time.Time{}, fmt.Errorf(
		"%w (after %d consecutive attempts)",
		lastDelayErr,
		linuxSchedstatReadMaxAttempts,
	)
}

func waitVisibleSystemCPUSample(
	ctx context.Context,
	duration time.Duration,
) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
