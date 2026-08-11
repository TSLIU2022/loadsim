//go:build linux

package stress

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseLinuxSchedstat(t *testing.T) {
	data := []byte(strings.Join([]string{
		"version 17",
		"timestamp 12345",
		"cpu2 0 0 0 0 0 0 300 40 5",
		"domain0 MC 00000004 0 0 0",
		"cpu0 0 0 0 0 0 0 100 20 3",
		"",
	}, "\n"))

	snapshot, err := parseLinuxSchedstat(data)
	if err != nil {
		t.Fatalf("parseLinuxSchedstat: %v", err)
	}
	if snapshot.version != 17 {
		t.Fatalf("version = %d want 17", snapshot.version)
	}
	if !reflect.DeepEqual(snapshot.cpuIDs, []int{0, 2}) {
		t.Fatalf("CPU IDs = %v want [0 2]", snapshot.cpuIDs)
	}
	if snapshot.runtimes[0] != 100 || snapshot.runtimes[2] != 300 {
		t.Fatalf("runtimes = %v want cpu0=100 cpu2=300", snapshot.runtimes)
	}
}

func TestParseLinuxSchedstatRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "missing version",
			data: "cpu0 0 0 0 0 0 0 1 0 0\n",
			want: "version line is missing",
		},
		{
			name: "duplicate version",
			data: "version 17\nversion 17\ncpu0 0 0 0 0 0 0 1 0 0\n",
			want: "duplicate",
		},
		{
			name: "invalid version",
			data: "version zero\ncpu0 0 0 0 0 0 0 1 0 0\n",
			want: "invalid /proc/schedstat version",
		},
		{
			name: "old version",
			data: "version 9\ncpu0 0 0 0 0 0 0 1 0 0\n",
			want: "unsupported /proc/schedstat version",
		},
		{
			name: "future version",
			data: "version 18\ncpu0 0 0 0 0 0 0 1 0 0\n",
			want: "unsupported /proc/schedstat version",
		},
		{
			name: "missing CPUs",
			data: "version 17\ntimestamp 1\n",
			want: "contains no CPU runtime counters",
		},
		{
			name: "invalid CPU label",
			data: "version 17\ncpuX 0 0 0 0 0 0 1 0 0\n",
			want: "invalid /proc/schedstat CPU label",
		},
		{
			name: "short CPU line",
			data: "version 17\ncpu0 0 0\n",
			want: "want at least 10",
		},
		{
			name: "invalid runtime",
			data: "version 17\ncpu0 0 0 0 0 0 0 nope 0 0\n",
			want: "scheduler runtime",
		},
		{
			name: "duplicate CPU",
			data: "version 17\ncpu0 0 0 0 0 0 0 1 0 0\ncpu0 0 0 0 0 0 0 2 0 0\n",
			want: "duplicate /proc/schedstat CPU",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseLinuxSchedstat([]byte(test.data))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v want substring %q", err, test.want)
			}
		})
	}
}

func TestParseLinuxProcStat(t *testing.T) {
	snapshot, err := parseLinuxProcStat([]byte(strings.Join([]string{
		"cpu 100 20 30 400 50 10 5 2 80 9",
		"cpu2 50 10 15 200 25 5 3 1 40 4",
		"cpu0 50 10 15 200 25 5 2 1 40 5",
		"intr 123",
		"",
	}, "\n")))
	if err != nil {
		t.Fatalf("parseLinuxProcStat: %v", err)
	}
	if !reflect.DeepEqual(snapshot.cpuIDs, []int{0, 2}) {
		t.Fatalf("CPU IDs = %v want [0 2]", snapshot.cpuIDs)
	}
	if snapshot.total != 567 || snapshot.busy != 167 {
		t.Fatalf(
			"total/busy = %d/%d want 567/167; guest and iowait must be excluded",
			snapshot.total,
			snapshot.busy,
		)
	}
}

func TestParseLinuxProcStatRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "missing aggregate",
			data: "cpu0 1 0 0 9\n",
			want: "aggregate CPU line is missing",
		},
		{
			name: "duplicate aggregate",
			data: "cpu 1 0 0 9\ncpu 2 0 0 8\ncpu0 1 0 0 9\n",
			want: "duplicate",
		},
		{
			name: "missing per CPU",
			data: "cpu 1 0 0 9\nintr 1\n",
			want: "contains no per-CPU",
		},
		{
			name: "invalid CPU label",
			data: "cpu 1 0 0 9\ncpuX 1 0 0 9\n",
			want: "invalid /proc/stat CPU label",
		},
		{
			name: "short CPU line",
			data: "cpu 1 0 0\ncpu0 1 0 0 9\n",
			want: "want at least 5",
		},
		{
			name: "invalid counter",
			data: "cpu nope 0 0 9\ncpu0 1 0 0 9\n",
			want: "CPU counter",
		},
		{
			name: "duplicate CPU",
			data: "cpu 1 0 0 9\ncpu0 1 0 0 9\ncpu0 1 0 0 9\n",
			want: "duplicate /proc/stat CPU",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseLinuxProcStat([]byte(test.data))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v want substring %q", err, test.want)
			}
		})
	}
}

func TestParseLinuxSystemCPUSnapshotRejectsMismatchedCPUSet(t *testing.T) {
	_, err := parseLinuxSystemCPUSnapshot(
		[]byte("version 17\ncpu0 0 0 0 0 0 0 1 0 0\n"),
		[]byte("cpu 1 0 0 9\ncpu0 1 0 0 9\ncpu1 0 0 0 10\n"),
	)
	if err == nil || !strings.Contains(err.Error(), "CPU sets differ") {
		t.Fatalf("error = %v want CPU set mismatch", err)
	}
}

func TestInspectVisibleSystemCPUFromSchedstat(t *testing.T) {
	first, err := inspectVisibleSystemCPUFrom([]byte(
		"version 17\ncpu1 0 0 0 0 0 0 200 0 0\ncpu0 0 0 0 0 0 0 100 0 0\n",
	), []byte(
		"cpu 10 0 0 90 0 0 0 0 0 0\ncpu0 5 0 0 45 0 0 0 0 0 0\ncpu1 5 0 0 45 0 0 0 0 0 0\n",
	))
	if err != nil {
		t.Fatalf("inspect first snapshot: %v", err)
	}
	second, err := inspectVisibleSystemCPUFrom([]byte(
		"version 17\ncpu0 0 0 0 0 0 0 900 0 0\ncpu1 0 0 0 0 0 0 800 0 0\n",
	), []byte(
		"cpu 20 0 0 180 0 0 0 0 0 0\ncpu0 10 0 0 90 0 0 0 0 0 0\ncpu1 10 0 0 90 0 0 0 0 0 0\n",
	))
	if err != nil {
		t.Fatalf("inspect second snapshot: %v", err)
	}
	if first.Source != visibleSystemCPUSourceProcCounters {
		t.Fatalf("source = %q want %q", first.Source, visibleSystemCPUSourceProcCounters)
	}
	if first.BoundaryKind != VisibleSystemCPUBoundaryHost {
		t.Fatalf("boundary kind = %q want host", first.BoundaryKind)
	}
	if first.CPUs != 2 {
		t.Fatalf("CPUs = %.0f want 2", first.CPUs)
	}
	if !strings.HasPrefix(first.BoundaryID, "schedcpu-") {
		t.Fatalf("boundary ID = %q want schedcpu prefix", first.BoundaryID)
	}
	if first.BoundaryID != second.BoundaryID {
		t.Fatalf("runtime changes altered boundary ID: %q != %q", first.BoundaryID, second.BoundaryID)
	}
	if strings.Contains(first.BoundaryID, "/proc") {
		t.Fatalf("boundary ID leaks a path: %q", first.BoundaryID)
	}

	changed, err := inspectVisibleSystemCPUFrom([]byte(
		"version 17\ncpu0 0 0 0 0 0 0 100 0 0\n",
	), []byte(
		"cpu 10 0 0 90 0 0 0 0 0 0\ncpu0 10 0 0 90 0 0 0 0 0 0\n",
	))
	if err != nil {
		t.Fatalf("inspect changed snapshot: %v", err)
	}
	if first.BoundaryID == changed.BoundaryID {
		t.Fatal("CPU set change did not alter boundary ID")
	}
}

func TestSampleVisibleSystemCPUFromSchedstat(t *testing.T) {
	dependencies := schedstatSampleDependencies(
		t,
		[]string{
			"version 17\ncpu0 0 0 0 0 0 0 1000000000 0 0\ncpu1 0 0 0 0 0 0 2000000000 0 0\n",
			"version 17\ncpu0 0 0 0 0 0 0 1400000000 0 0\ncpu1 0 0 0 0 0 0 2600000000 0 0\n",
		},
		0,
	)

	sample, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		time.Second,
		dependencies,
	)
	if err != nil {
		t.Fatalf("sampleVisibleSystemCPUFrom: %v", err)
	}
	if sample.Source != visibleSystemCPUSourceProcCounters ||
		sample.BoundaryKind != VisibleSystemCPUBoundaryHost ||
		sample.CPUs != 2 {
		t.Fatalf("unexpected sample metadata: %+v", sample)
	}
	if sample.Busy != time.Second || sample.Elapsed != time.Second {
		t.Fatalf("busy/elapsed = %s/%s want 1s/1s", sample.Busy, sample.Elapsed)
	}
	if math.Abs(sample.Percent-50) > 0.000001 {
		t.Fatalf("percent = %.9f want 50", sample.Percent)
	}
}

func TestSampleVisibleSystemCPUUsesProcStatAsConservativeFloor(t *testing.T) {
	dependencies := systemCPUSampleDependencies(
		t,
		[]string{
			"version 17\ncpu0 0 0 0 0 0 0 1000000000 0 0\ncpu1 0 0 0 0 0 0 2000000000 0 0\n",
			"version 17\ncpu0 0 0 0 0 0 0 1200000000 0 0\ncpu1 0 0 0 0 0 0 2200000000 0 0\n",
		},
		[]string{
			"cpu 0 0 0 100 0 0 0 0 0 0\ncpu0 0 0 0 50 0 0 0 0 0 0\ncpu1 0 0 0 50 0 0 0 0 0 0\n",
			"cpu 200 0 0 100 0 0 0 0 0 0\ncpu0 100 0 0 50 0 0 0 0 0 0\ncpu1 100 0 0 50 0 0 0 0 0 0\n",
		},
		0,
	)

	sample, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		time.Second,
		dependencies,
	)
	if err != nil {
		t.Fatalf("sampleVisibleSystemCPUFrom: %v", err)
	}
	if sample.Percent != 100 || sample.Busy != 2*time.Second {
		t.Fatalf("sample = %+v want /proc/stat saturation floor", sample)
	}
}

func TestSampleVisibleSystemCPUPreservesSchedstatIdleClassAccounting(t *testing.T) {
	dependencies := systemCPUSampleDependencies(
		t,
		[]string{
			"version 17\ncpu0 0 0 0 0 0 0 1000000000 0 0\ncpu1 0 0 0 0 0 0 2000000000 0 0\n",
			"version 17\ncpu0 0 0 0 0 0 0 1400000000 0 0\ncpu1 0 0 0 0 0 0 2600000000 0 0\n",
		},
		[]string{
			"cpu 0 0 0 100 0 0 0 0 0 0\ncpu0 0 0 0 50 0 0 0 0 0 0\ncpu1 0 0 0 50 0 0 0 0 0 0\n",
			"cpu 30 0 0 270 0 0 0 0 0 0\ncpu0 15 0 0 135 0 0 0 0 0 0\ncpu1 15 0 0 135 0 0 0 0 0 0\n",
		},
		0,
	)

	sample, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		time.Second,
		dependencies,
	)
	if err != nil {
		t.Fatalf("sampleVisibleSystemCPUFrom: %v", err)
	}
	if math.Abs(sample.Percent-50) > 0.000001 || sample.Busy != time.Second {
		t.Fatalf("sample = %+v want schedstat 50%%", sample)
	}
}

func TestSampleVisibleSystemCPUClampsMinorSaturationOvershoot(t *testing.T) {
	dependencies := schedstatSampleDependencies(
		t,
		[]string{
			"version 16\ncpu0 0 0 0 0 0 0 1000000000 0 0\n",
			"version 16\ncpu0 0 0 0 0 0 0 2000185924 0 0\n",
		},
		0,
	)

	sample, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		time.Second,
		dependencies,
	)
	if err != nil {
		t.Fatalf("sampleVisibleSystemCPUFrom: %v", err)
	}
	if sample.Percent != 100 {
		t.Fatalf("percent = %.9f want 100", sample.Percent)
	}
	if sample.Busy != time.Second {
		t.Fatalf("busy = %s want 1s", sample.Busy)
	}
}

func TestSampleVisibleSystemCPUConservativelyClampsMaterialSaturationOvershoot(t *testing.T) {
	dependencies := schedstatSampleDependencies(
		t,
		[]string{
			"version 16\ncpu0 0 0 0 0 0 0 1000000000 0 0\n",
			"version 16\ncpu0 0 0 0 0 0 0 2010000001 0 0\n",
		},
		0,
	)

	sample, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		time.Second,
		dependencies,
	)
	if err != nil {
		t.Fatalf("sampleVisibleSystemCPUFrom: %v", err)
	}
	if sample.Percent != 100 || sample.Busy != time.Second {
		t.Fatalf("sample = %+v want saturation", sample)
	}
}

func TestSampleVisibleSystemCPUBracketsCounterReads(t *testing.T) {
	dependencies := schedstatSampleDependencies(
		t,
		[]string{
			"version 17\ncpu0 0 0 0 0 0 0 100 0 0\n",
			"version 17\ncpu0 0 0 0 0 0 0 1000000100 0 0\n",
		},
		4*time.Millisecond,
	)

	sample, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		time.Second,
		dependencies,
	)
	if err != nil {
		t.Fatalf("sampleVisibleSystemCPUFrom: %v", err)
	}
	if sample.Elapsed != time.Second+4*time.Millisecond {
		t.Fatalf("elapsed = %s want 1.004s", sample.Elapsed)
	}
}

func TestSampleVisibleSystemCPURejectsBoundaryChange(t *testing.T) {
	tests := []struct {
		name string
		end  string
	}{
		{
			name: "version",
			end:  "version 16\ncpu0 0 0 0 0 0 0 200 0 0\n",
		},
		{
			name: "CPU set",
			end:  "version 17\ncpu0 0 0 0 0 0 0 200 0 0\ncpu1 0 0 0 0 0 0 1 0 0\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies := schedstatSampleDependencies(
				t,
				[]string{
					"version 17\ncpu0 0 0 0 0 0 0 100 0 0\n",
					test.end,
				},
				0,
			)
			_, err := sampleVisibleSystemCPUFrom(
				context.Background(),
				time.Second,
				dependencies,
			)
			if err == nil || !strings.Contains(err.Error(), "boundary changed") {
				t.Fatalf("error = %v want boundary change", err)
			}
		})
	}
}

func TestSampleVisibleSystemCPURejectsCounterRegression(t *testing.T) {
	dependencies := schedstatSampleDependencies(
		t,
		[]string{
			"version 17\ncpu0 0 0 0 0 0 0 200 0 0\n",
			"version 17\ncpu0 0 0 0 0 0 0 100 0 0\n",
		},
		0,
	)
	_, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		time.Second,
		dependencies,
	)
	if err == nil || !strings.Contains(err.Error(), "moved backwards") {
		t.Fatalf("error = %v want counter regression", err)
	}
}

func TestSampleVisibleSystemCPUCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := sampleVisibleSystemCPUFrom(
		ctx,
		time.Second,
		linuxVisibleCPUSampleDependencies{
			readFile: os.ReadFile,
			now:      time.Now,
			wait:     waitVisibleSystemCPUSample,
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v want context cancellation", err)
	}

	dependencies := schedstatSampleDependencies(
		t,
		[]string{"version 17\ncpu0 0 0 0 0 0 0 100 0 0\n"},
		0,
	)
	dependencies.wait = func(context.Context, time.Duration) error {
		return context.Canceled
	}
	_, err = sampleVisibleSystemCPUFrom(
		context.Background(),
		time.Second,
		dependencies,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v want context cancellation", err)
	}
}

func TestSampleVisibleSystemCPURetriesTransientSlowRead(t *testing.T) {
	current := time.Unix(100, 0)
	readIndex := 0
	readAdvances := []time.Duration{
		20 * time.Millisecond,
		0,
		0,
	}
	readings := []string{
		"version 17\ncpu0 0 0 0 0 0 0 50 0 0\n",
		"version 17\ncpu0 0 0 0 0 0 0 100 0 0\n",
		"version 17\ncpu0 0 0 0 0 0 0 100000100 0 0\n",
	}
	lastSchedstat := ""
	procReadCount := 0
	dependencies := linuxVisibleCPUSampleDependencies{
		readFile: func(path string) ([]byte, error) {
			switch path {
			case linuxSchedstatPath:
				if readIndex >= len(readings) {
					t.Fatalf("unexpected scheduler statistics read %d", readIndex+1)
				}
				lastSchedstat = readings[readIndex]
				data := []byte(lastSchedstat)
				current = current.Add(readAdvances[readIndex])
				readIndex++
				return data, nil
			case linuxProcStatPath:
				procReadCount++
				return testProcStatForSchedstat(
					t,
					lastSchedstat,
					uint64(procReadCount*100),
					0,
				), nil
			default:
				t.Fatalf("unexpected read path %q", path)
				return nil, nil
			}
		},
		now: func() time.Time {
			return current
		},
		wait: func(_ context.Context, duration time.Duration) error {
			current = current.Add(duration)
			return nil
		},
	}
	sample, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		100*time.Millisecond,
		dependencies,
	)
	if err != nil {
		t.Fatalf("sample after transient slow read: %v", err)
	}
	if readIndex != 3 {
		t.Fatalf("scheduler statistics reads = %d want 3", readIndex)
	}
	if sample.Elapsed != 100*time.Millisecond || sample.Percent != 100 {
		t.Fatalf("sample = %+v want 100ms at 100%%", sample)
	}
}

func TestSampleVisibleSystemCPURetriesTransientSlowEndRead(t *testing.T) {
	current := time.Unix(100, 0)
	readIndex := 0
	readAdvances := []time.Duration{
		0,
		20 * time.Millisecond,
		0,
	}
	readings := []string{
		"version 17\ncpu0 0 0 0 0 0 0 100 0 0\n",
		"version 17\ncpu0 0 0 0 0 0 0 100000100 0 0\n",
		"version 17\ncpu0 0 0 0 0 0 0 120000100 0 0\n",
	}
	lastSchedstat := ""
	procReadCount := 0
	dependencies := linuxVisibleCPUSampleDependencies{
		readFile: func(path string) ([]byte, error) {
			switch path {
			case linuxSchedstatPath:
				if readIndex >= len(readings) {
					t.Fatalf("unexpected scheduler statistics read %d", readIndex+1)
				}
				lastSchedstat = readings[readIndex]
				data := []byte(lastSchedstat)
				current = current.Add(readAdvances[readIndex])
				readIndex++
				return data, nil
			case linuxProcStatPath:
				procReadCount++
				return testProcStatForSchedstat(
					t,
					lastSchedstat,
					uint64(procReadCount*100),
					0,
				), nil
			default:
				t.Fatalf("unexpected read path %q", path)
				return nil, nil
			}
		},
		now: func() time.Time { return current },
		wait: func(_ context.Context, duration time.Duration) error {
			current = current.Add(duration)
			return nil
		},
	}
	sample, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		100*time.Millisecond,
		dependencies,
	)
	if err != nil {
		t.Fatalf("sample after transient slow end read: %v", err)
	}
	if readIndex != 3 {
		t.Fatalf("scheduler statistics reads = %d want 3", readIndex)
	}
	if sample.Elapsed != 120*time.Millisecond || sample.Percent != 100 {
		t.Fatalf("sample = %+v want 120ms at 100%%", sample)
	}
}

func TestSampleVisibleSystemCPURejectsRepeatedSlowReads(t *testing.T) {
	current := time.Unix(100, 0)
	readCount := 0
	lastSchedstat := ""
	procReadCount := 0
	dependencies := linuxVisibleCPUSampleDependencies{
		readFile: func(path string) ([]byte, error) {
			switch path {
			case linuxSchedstatPath:
				readCount++
				current = current.Add(20 * time.Millisecond)
				lastSchedstat = "version 17\ncpu0 0 0 0 0 0 0 100 0 0\n"
				return []byte(lastSchedstat), nil
			case linuxProcStatPath:
				procReadCount++
				return testProcStatForSchedstat(
					t,
					lastSchedstat,
					uint64(procReadCount*100),
					0,
				), nil
			default:
				t.Fatalf("unexpected read path %q", path)
				return nil, nil
			}
		},
		now:  func() time.Time { return current },
		wait: func(context.Context, time.Duration) error { return nil },
	}
	_, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		100*time.Millisecond,
		dependencies,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "exceeding the safe limit") ||
		!strings.Contains(err.Error(), "after 3 consecutive attempts") {
		t.Fatalf("error = %v want repeated slow read rejection", err)
	}
	if readCount != linuxCPUCounterReadMaxAttempts {
		t.Fatalf(
			"scheduler statistics reads = %d want %d",
			readCount,
			linuxCPUCounterReadMaxAttempts,
		)
	}
}

func TestSampleVisibleSystemCPUValidation(t *testing.T) {
	validDependencies := linuxVisibleCPUSampleDependencies{
		readFile: os.ReadFile,
		now:      time.Now,
		wait:     waitVisibleSystemCPUSample,
	}
	tests := []struct {
		name         string
		ctx          context.Context
		duration     time.Duration
		dependencies linuxVisibleCPUSampleDependencies
		want         string
	}{
		{
			name:         "nil context",
			ctx:          nil,
			duration:     time.Second,
			dependencies: validDependencies,
			want:         "context must not be nil",
		},
		{
			name:         "zero duration",
			ctx:          context.Background(),
			duration:     0,
			dependencies: validDependencies,
			want:         "duration must be greater than zero",
		},
		{
			name:         "missing dependencies",
			ctx:          context.Background(),
			duration:     time.Second,
			dependencies: linuxVisibleCPUSampleDependencies{},
			want:         "dependencies are incomplete",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := sampleVisibleSystemCPUFrom(
				test.ctx,
				test.duration,
				test.dependencies,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v want substring %q", err, test.want)
			}
		})
	}
}

func TestLinuxSchedstatBusyDeltaValidation(t *testing.T) {
	start := linuxSchedstatSnapshot{
		version:  17,
		cpuIDs:   []int{0, 1},
		runtimes: map[int]uint64{0: 0, 1: 0},
	}
	end := linuxSchedstatSnapshot{
		version:  17,
		cpuIDs:   []int{0, 1},
		runtimes: map[int]uint64{0: math.MaxUint64, 1: 1},
	}
	if _, err := linuxSchedstatBusyDelta(start, end); err == nil {
		t.Fatal("expected scheduler runtime overflow")
	}
}

func TestCalculateLinuxProcStatCPUPercent(t *testing.T) {
	tests := []struct {
		name  string
		start linuxProcStatSnapshot
		end   linuxProcStatSnapshot
		want  float64
	}{
		{
			name:  "half busy",
			start: linuxProcStatSnapshot{total: 100, busy: 20},
			end:   linuxProcStatSnapshot{total: 300, busy: 120},
			want:  50,
		},
		{
			name:  "no elapsed ticks",
			start: linuxProcStatSnapshot{total: 100, busy: 20},
			end:   linuxProcStatSnapshot{total: 100, busy: 20},
			want:  0,
		},
		{
			name:  "counter skew is saturation",
			start: linuxProcStatSnapshot{total: 100, busy: 20},
			end:   linuxProcStatSnapshot{total: 150, busy: 100},
			want:  100,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := calculateLinuxProcStatCPUPercent(test.start, test.end)
			if err != nil {
				t.Fatalf("calculateLinuxProcStatCPUPercent: %v", err)
			}
			if got != test.want {
				t.Fatalf("percent = %.1f want %.1f", got, test.want)
			}
		})
	}
}

func TestCalculateLinuxProcStatCPUPercentRejectsCounterRegression(t *testing.T) {
	tests := []struct {
		name  string
		start linuxProcStatSnapshot
		end   linuxProcStatSnapshot
		want  string
	}{
		{
			name:  "total",
			start: linuxProcStatSnapshot{total: 100, busy: 20},
			end:   linuxProcStatSnapshot{total: 99, busy: 21},
			want:  "total CPU counter moved backwards",
		},
		{
			name:  "busy",
			start: linuxProcStatSnapshot{total: 100, busy: 20},
			end:   linuxProcStatSnapshot{total: 101, busy: 19},
			want:  "busy CPU counter moved backwards",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := calculateLinuxProcStatCPUPercent(test.start, test.end)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v want substring %q", err, test.want)
			}
		})
	}
}

func TestCalculateLinuxSchedstatCPUPercentValidation(t *testing.T) {
	tests := []struct {
		name    string
		busy    time.Duration
		elapsed time.Duration
		cpus    float64
	}{
		{name: "negative busy", busy: -1, elapsed: time.Second, cpus: 1},
		{name: "zero elapsed", busy: 0, elapsed: 0, cpus: 1},
		{name: "zero CPUs", busy: 0, elapsed: time.Second, cpus: 0},
		{name: "NaN CPUs", busy: 0, elapsed: time.Second, cpus: math.NaN()},
		{name: "capacity overflow", busy: 0, elapsed: time.Duration(math.MaxInt64), cpus: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := calculateLinuxSchedstatCPUPercent(
				test.busy,
				test.elapsed,
				test.cpus,
			); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestCalculateLinuxSchedstatCPUPercentRejectsMaterialOvershoot(t *testing.T) {
	_, err := calculateLinuxSchedstatCPUPercent(
		101*time.Millisecond,
		100*time.Millisecond,
		1,
	)
	if err == nil || !strings.Contains(err.Error(), "beyond 0.500% tolerance") {
		t.Fatalf("error = %v want material saturation overshoot", err)
	}
}

func schedstatSampleDependencies(
	t *testing.T,
	readings []string,
	readAdvance time.Duration,
) linuxVisibleCPUSampleDependencies {
	t.Helper()
	current := time.Unix(100, 0)
	readIndex := 0
	lastSchedstat := ""
	procReadCount := 0
	return linuxVisibleCPUSampleDependencies{
		readFile: func(path string) ([]byte, error) {
			switch path {
			case linuxSchedstatPath:
				if readIndex >= len(readings) {
					t.Fatalf("unexpected scheduler statistics read %d", readIndex+1)
				}
				lastSchedstat = readings[readIndex]
				readIndex++
				current = current.Add(readAdvance)
				return []byte(lastSchedstat), nil
			case linuxProcStatPath:
				procReadCount++
				return testProcStatForSchedstat(
					t,
					lastSchedstat,
					uint64(procReadCount*100),
					0,
				), nil
			default:
				t.Fatalf("unexpected read path %q", path)
				return nil, nil
			}
		},
		now: func() time.Time {
			return current
		},
		wait: func(ctx context.Context, duration time.Duration) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			current = current.Add(duration)
			return nil
		},
	}
}

func systemCPUSampleDependencies(
	t *testing.T,
	schedstatReadings []string,
	procStatReadings []string,
	readAdvance time.Duration,
) linuxVisibleCPUSampleDependencies {
	t.Helper()
	current := time.Unix(100, 0)
	schedstatIndex := 0
	procStatIndex := 0
	return linuxVisibleCPUSampleDependencies{
		readFile: func(path string) ([]byte, error) {
			switch path {
			case linuxSchedstatPath:
				if schedstatIndex >= len(schedstatReadings) {
					t.Fatalf(
						"unexpected scheduler statistics read %d",
						schedstatIndex+1,
					)
				}
				data := []byte(schedstatReadings[schedstatIndex])
				schedstatIndex++
				current = current.Add(readAdvance)
				return data, nil
			case linuxProcStatPath:
				if procStatIndex >= len(procStatReadings) {
					t.Fatalf(
						"unexpected /proc/stat read %d",
						procStatIndex+1,
					)
				}
				data := []byte(procStatReadings[procStatIndex])
				procStatIndex++
				return data, nil
			default:
				t.Fatalf("unexpected read path %q", path)
				return nil, nil
			}
		},
		now: func() time.Time {
			return current
		},
		wait: func(ctx context.Context, duration time.Duration) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			current = current.Add(duration)
			return nil
		},
	}
}

func testProcStatForSchedstat(
	t *testing.T,
	schedstat string,
	total uint64,
	busy uint64,
) []byte {
	t.Helper()
	if busy > total {
		t.Fatalf("test /proc/stat busy %d exceeds total %d", busy, total)
	}
	snapshot, err := parseLinuxSchedstat([]byte(schedstat))
	if err != nil {
		t.Fatalf("parse test schedstat: %v", err)
	}
	var output strings.Builder
	_, _ = fmt.Fprintf(&output, "cpu %d 0 0 %d 0 0 0 0 0 0\n", busy, total-busy)
	for _, cpuID := range snapshot.cpuIDs {
		_, _ = fmt.Fprintf(&output, "cpu%d 0 0 0 0 0 0 0 0 0 0\n", cpuID)
	}
	return []byte(output.String())
}
