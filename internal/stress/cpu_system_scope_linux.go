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
	visibleSystemCPUSourceProcCounters = "proc:schedstat+stat"
	linuxSchedstatPath                 = "/proc/schedstat"
	linuxProcStatPath                  = "/proc/stat"
	linuxSchedstatCPUFieldCount        = 10
	linuxSchedstatMinimumVersion       = 10
	linuxSchedstatMaximumVersion       = 17
	linuxSchedstatBusyOvershootRatio   = 0.005
	linuxCPUCounterReadMaxAttempts     = 3
)

type linuxSchedstatSnapshot struct {
	version  uint64
	cpuIDs   []int
	runtimes map[int]uint64
}

type linuxProcStatSnapshot struct {
	cpuIDs []int
	total  uint64
	busy   uint64
}

type linuxSystemCPUSnapshot struct {
	schedstat linuxSchedstatSnapshot
	procStat  linuxProcStatSnapshot
}

type linuxCPUCounterReadDelayError struct {
	delay time.Duration
	limit time.Duration
}

func (e *linuxCPUCounterReadDelayError) Error() string {
	return fmt.Sprintf(
		"whole-machine CPU counter read took %s, exceeding the safe limit %s",
		e.delay,
		e.limit,
	)
}

type linuxSchedstatCapacityError struct {
	busy     time.Duration
	capacity time.Duration
}

func (e *linuxSchedstatCapacityError) Error() string {
	return fmt.Sprintf(
		"whole-machine CPU busy duration %s exceeds physical capacity %s beyond %.3f%% tolerance",
		e.busy,
		e.capacity,
		linuxSchedstatBusyOvershootRatio*100,
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
	schedstatData, err := os.ReadFile(linuxSchedstatPath)
	if err != nil {
		return VisibleSystemCPUInfo{}, fmt.Errorf(
			"read whole-machine CPU scheduler statistics: %w",
			err,
		)
	}
	procStatData, err := os.ReadFile(linuxProcStatPath)
	if err != nil {
		return VisibleSystemCPUInfo{}, fmt.Errorf(
			"read whole-machine CPU statistics: %w",
			err,
		)
	}
	return inspectVisibleSystemCPUFrom(schedstatData, procStatData)
}

func inspectVisibleSystemCPUFrom(
	schedstatData []byte,
	procStatData []byte,
) (VisibleSystemCPUInfo, error) {
	snapshot, err := parseLinuxSystemCPUSnapshot(schedstatData, procStatData)
	if err != nil {
		return VisibleSystemCPUInfo{}, err
	}
	return visibleSystemCPUInfoFromSnapshot(snapshot), nil
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
	start, startedAt, err := readLinuxSystemCPUAtMidpointWithRetry(
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
	end, finishedAt, err := readLinuxSystemCPUAtMidpointWithRetry(
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
	if !sameLinuxSystemCPUBoundary(start, end) {
		return VisibleSystemCPUSample{}, fmt.Errorf(
			"whole-machine CPU scheduler boundary changed during sampling",
		)
	}

	busy, err := linuxSchedstatBusyDelta(start.schedstat, end.schedstat)
	if err != nil {
		return VisibleSystemCPUSample{}, err
	}
	busy, percent, err := conservativeLinuxSchedstatCPUUsage(
		busy,
		elapsed,
		float64(len(start.schedstat.cpuIDs)),
	)
	if err != nil {
		return VisibleSystemCPUSample{}, err
	}
	procStatPercent, err := calculateLinuxProcStatCPUPercent(
		start.procStat,
		end.procStat,
	)
	if err != nil {
		return VisibleSystemCPUSample{}, err
	}
	if procStatPercent > percent {
		percent = procStatPercent
		busy, err = linuxCPUUsageDuration(
			percent,
			elapsed,
			float64(len(start.schedstat.cpuIDs)),
		)
		if err != nil {
			return VisibleSystemCPUSample{}, err
		}
	}
	info := visibleSystemCPUInfoFromSnapshot(start)
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

func parseLinuxSystemCPUSnapshot(
	schedstatData []byte,
	procStatData []byte,
) (linuxSystemCPUSnapshot, error) {
	schedstat, err := parseLinuxSchedstat(schedstatData)
	if err != nil {
		return linuxSystemCPUSnapshot{}, err
	}
	procStat, err := parseLinuxProcStat(procStatData)
	if err != nil {
		return linuxSystemCPUSnapshot{}, err
	}
	if !sameLinuxCPUSet(schedstat.cpuIDs, procStat.cpuIDs) {
		return linuxSystemCPUSnapshot{}, fmt.Errorf(
			"/proc/schedstat and /proc/stat CPU sets differ",
		)
	}
	return linuxSystemCPUSnapshot{
		schedstat: schedstat,
		procStat:  procStat,
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

func parseLinuxProcStat(data []byte) (linuxProcStatSnapshot, error) {
	var snapshot linuxProcStatSnapshot
	seenCPUs := make(map[int]struct{})
	var hasAggregate bool
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "cpu" {
			if hasAggregate {
				return linuxProcStatSnapshot{}, fmt.Errorf(
					"duplicate /proc/stat aggregate CPU line",
				)
			}
			total, busy, err := parseLinuxProcStatCPUFields(fields)
			if err != nil {
				return linuxProcStatSnapshot{}, err
			}
			snapshot.total = total
			snapshot.busy = busy
			hasAggregate = true
			continue
		}
		if !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		cpuID, err := parseLinuxProcStatCPUID(fields[0])
		if err != nil {
			return linuxProcStatSnapshot{}, err
		}
		if _, exists := seenCPUs[cpuID]; exists {
			return linuxProcStatSnapshot{}, fmt.Errorf(
				"duplicate /proc/stat CPU %d",
				cpuID,
			)
		}
		if _, _, err := parseLinuxProcStatCPUFields(fields); err != nil {
			return linuxProcStatSnapshot{}, err
		}
		seenCPUs[cpuID] = struct{}{}
		snapshot.cpuIDs = append(snapshot.cpuIDs, cpuID)
	}
	if err := scanner.Err(); err != nil {
		return linuxProcStatSnapshot{}, fmt.Errorf("scan /proc/stat: %w", err)
	}
	if !hasAggregate {
		return linuxProcStatSnapshot{}, fmt.Errorf(
			"/proc/stat aggregate CPU line is missing",
		)
	}
	if len(snapshot.cpuIDs) == 0 {
		return linuxProcStatSnapshot{}, fmt.Errorf(
			"/proc/stat contains no per-CPU counters",
		)
	}
	sort.Ints(snapshot.cpuIDs)
	return snapshot, nil
}

func parseLinuxProcStatCPUFields(fields []string) (uint64, uint64, error) {
	if len(fields) < 5 {
		return 0, 0, fmt.Errorf(
			"%s contains %d fields, want at least 5",
			fields[0],
			len(fields),
		)
	}
	counters := make([]uint64, 0, len(fields)-1)
	for _, value := range fields[1:] {
		counter, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf(
				"parse %s CPU counter %q: %w",
				fields[0],
				value,
				err,
			)
		}
		counters = append(counters, counter)
	}

	// guest and guest_nice are already included in user and nice. Limit the
	// calculation to the first eight fields and exclude iowait as well: Linux
	// documents that iowait can decrease, so it is unsuitable for a monotonic
	// safety signal.
	fieldCount := len(counters)
	if fieldCount > 8 {
		fieldCount = 8
	}
	var total uint64
	var busy uint64
	for index := 0; index < fieldCount; index++ {
		counter := counters[index]
		if index == 4 {
			continue
		}
		if total > math.MaxUint64-counter {
			return 0, 0, fmt.Errorf("%s total CPU counter overflow", fields[0])
		}
		total += counter
		if index == 3 {
			continue
		}
		if busy > math.MaxUint64-counter {
			return 0, 0, fmt.Errorf("%s busy CPU counter overflow", fields[0])
		}
		busy += counter
	}
	if busy > total {
		return 0, 0, fmt.Errorf("%s busy CPU counter exceeds total", fields[0])
	}
	return total, busy, nil
}

func parseLinuxProcStatCPUID(value string) (int, error) {
	suffix := strings.TrimPrefix(value, "cpu")
	if suffix == "" {
		return 0, fmt.Errorf("invalid /proc/stat CPU label %q", value)
	}
	cpuID, err := strconv.Atoi(suffix)
	if err != nil || cpuID < 0 {
		return 0, fmt.Errorf("invalid /proc/stat CPU label %q", value)
	}
	return cpuID, nil
}

func visibleSystemCPUInfoFromSnapshot(
	snapshot linuxSystemCPUSnapshot,
) VisibleSystemCPUInfo {
	return VisibleSystemCPUInfo{
		Source:       visibleSystemCPUSourceProcCounters,
		BoundaryID:   linuxSchedstatBoundaryID(snapshot.schedstat),
		BoundaryKind: VisibleSystemCPUBoundaryHost,
		CPUs:         float64(len(snapshot.schedstat.cpuIDs)),
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

func sameLinuxSystemCPUBoundary(
	first linuxSystemCPUSnapshot,
	second linuxSystemCPUSnapshot,
) bool {
	return sameLinuxSchedstatBoundary(first.schedstat, second.schedstat) &&
		sameLinuxCPUSet(first.procStat.cpuIDs, second.procStat.cpuIDs)
}

func sameLinuxCPUSet(first []int, second []int) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
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

func calculateLinuxProcStatCPUPercent(
	start linuxProcStatSnapshot,
	end linuxProcStatSnapshot,
) (float64, error) {
	if end.total < start.total {
		return 0, fmt.Errorf("whole-machine total CPU counter moved backwards")
	}
	if end.busy < start.busy {
		return 0, fmt.Errorf("whole-machine busy CPU counter moved backwards")
	}
	totalDelta := end.total - start.total
	busyDelta := end.busy - start.busy
	if totalDelta == 0 {
		return 0, nil
	}
	// If aggregate counter skew makes the non-idle delta exceed the total
	// delta, conservatively report saturation instead of understating load.
	if busyDelta >= totalDelta {
		return 100, nil
	}
	percent := float64(busyDelta) / float64(totalDelta) * 100
	if !isFinite(percent) || percent < 0 || percent > 100 {
		return 0, fmt.Errorf(
			"whole-machine /proc/stat CPU utilization calculation is out of range",
		)
	}
	return percent, nil
}

func conservativeLinuxSchedstatCPUUsage(
	busy time.Duration,
	elapsed time.Duration,
	cpus float64,
) (time.Duration, float64, error) {
	normalizedBusy, percent, err := normalizeLinuxSchedstatCPUUsage(
		busy,
		elapsed,
		cpus,
	)
	if err == nil {
		return normalizedBusy, percent, nil
	}
	var capacityErr *linuxSchedstatCapacityError
	if !errors.As(err, &capacityErr) {
		return 0, 0, err
	}
	// Some vendor kernels publish schedstat runtime lazily. A short sample can
	// therefore contain runtime accumulated before the sample began. Treat the
	// impossible high value as saturation so LoadSim yields instead of exiting.
	return capacityErr.capacity, 100, nil
}

func linuxCPUUsageDuration(
	percent float64,
	elapsed time.Duration,
	cpus float64,
) (time.Duration, error) {
	if !isFinite(percent) || percent < 0 || percent > 100 {
		return 0, fmt.Errorf(
			"whole-machine CPU utilization calculation is out of range",
		)
	}
	capacityNanoseconds := float64(elapsed) * cpus
	if !isFinite(capacityNanoseconds) ||
		capacityNanoseconds <= 0 ||
		capacityNanoseconds >= float64(math.MaxInt64) {
		return 0, fmt.Errorf(
			"whole-machine CPU utilization calculation is out of range",
		)
	}
	busyNanoseconds := capacityNanoseconds * percent / 100
	if !isFinite(busyNanoseconds) || busyNanoseconds < 0 {
		return 0, fmt.Errorf(
			"whole-machine CPU utilization calculation is out of range",
		)
	}
	if busyNanoseconds > capacityNanoseconds {
		busyNanoseconds = capacityNanoseconds
	}
	return time.Duration(busyNanoseconds), nil
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
		return 0, 0, &linuxSchedstatCapacityError{
			busy:     busy,
			capacity: time.Duration(capacityNanoseconds),
		}
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

func readLinuxSystemCPUAtMidpoint(
	dependencies linuxVisibleCPUSampleDependencies,
	delayLimit time.Duration,
) (linuxSystemCPUSnapshot, time.Time, error) {
	before := dependencies.now()
	schedstatData, err := dependencies.readFile(linuxSchedstatPath)
	if err != nil {
		return linuxSystemCPUSnapshot{}, time.Time{}, fmt.Errorf(
			"read whole-machine CPU scheduler statistics: %w",
			err,
		)
	}
	procStatData, err := dependencies.readFile(linuxProcStatPath)
	after := dependencies.now()
	if err != nil {
		return linuxSystemCPUSnapshot{}, time.Time{}, fmt.Errorf(
			"read whole-machine CPU statistics: %w",
			err,
		)
	}
	delay := after.Sub(before)
	if delay < 0 {
		return linuxSystemCPUSnapshot{}, time.Time{}, fmt.Errorf(
			"whole-machine CPU counter read time moved backwards",
		)
	}
	if delay > delayLimit {
		return linuxSystemCPUSnapshot{}, time.Time{}, &linuxCPUCounterReadDelayError{
			delay: delay,
			limit: delayLimit,
		}
	}
	snapshot, err := parseLinuxSystemCPUSnapshot(schedstatData, procStatData)
	if err != nil {
		return linuxSystemCPUSnapshot{}, time.Time{}, err
	}
	return snapshot, before.Add(delay / 2), nil
}

func readLinuxSystemCPUAtMidpointWithRetry(
	ctx context.Context,
	dependencies linuxVisibleCPUSampleDependencies,
	delayLimit time.Duration,
) (linuxSystemCPUSnapshot, time.Time, error) {
	var lastDelayErr *linuxCPUCounterReadDelayError
	for attempt := 1; attempt <= linuxCPUCounterReadMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return linuxSystemCPUSnapshot{}, time.Time{}, err
		}
		snapshot, observedAt, err := readLinuxSystemCPUAtMidpoint(
			dependencies,
			delayLimit,
		)
		if err == nil {
			return snapshot, observedAt, nil
		}
		if !errors.As(err, &lastDelayErr) {
			return linuxSystemCPUSnapshot{}, time.Time{}, err
		}
	}
	return linuxSystemCPUSnapshot{}, time.Time{}, fmt.Errorf(
		"%w (after %d consecutive attempts)",
		lastDelayErr,
		linuxCPUCounterReadMaxAttempts,
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
