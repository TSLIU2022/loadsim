//go:build linux

package stress

import (
	"context"
	"errors"
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

func TestInspectVisibleSystemCPUFromSchedstat(t *testing.T) {
	first, err := inspectVisibleSystemCPUFrom([]byte(
		"version 17\ncpu1 0 0 0 0 0 0 200 0 0\ncpu0 0 0 0 0 0 0 100 0 0\n",
	))
	if err != nil {
		t.Fatalf("inspect first snapshot: %v", err)
	}
	second, err := inspectVisibleSystemCPUFrom([]byte(
		"version 17\ncpu0 0 0 0 0 0 0 900 0 0\ncpu1 0 0 0 0 0 0 800 0 0\n",
	))
	if err != nil {
		t.Fatalf("inspect second snapshot: %v", err)
	}
	if first.Source != visibleSystemCPUSourceSchedstat {
		t.Fatalf("source = %q want %q", first.Source, visibleSystemCPUSourceSchedstat)
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
	if sample.Source != visibleSystemCPUSourceSchedstat ||
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

func TestSampleVisibleSystemCPURejectsMaterialSaturationOvershoot(t *testing.T) {
	dependencies := schedstatSampleDependencies(
		t,
		[]string{
			"version 16\ncpu0 0 0 0 0 0 0 1000000000 0 0\n",
			"version 16\ncpu0 0 0 0 0 0 0 2010000001 0 0\n",
		},
		0,
	)

	_, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		time.Second,
		dependencies,
	)
	if err == nil || !strings.Contains(err.Error(), "beyond 0.500% tolerance") {
		t.Fatalf("error = %v want material saturation overshoot", err)
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

func TestSampleVisibleSystemCPURejectsSlowRead(t *testing.T) {
	base := time.Unix(100, 0)
	nowValues := []time.Time{base, base.Add(20 * time.Millisecond)}
	nowIndex := 0
	dependencies := linuxVisibleCPUSampleDependencies{
		readFile: func(path string) ([]byte, error) {
			return []byte("version 17\ncpu0 0 0 0 0 0 0 100 0 0\n"), nil
		},
		now: func() time.Time {
			value := nowValues[nowIndex]
			nowIndex++
			return value
		},
		wait: func(context.Context, time.Duration) error { return nil },
	}
	_, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		100*time.Millisecond,
		dependencies,
	)
	if err == nil || !strings.Contains(err.Error(), "exceeding the safe limit") {
		t.Fatalf("error = %v want slow read rejection", err)
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

func schedstatSampleDependencies(
	t *testing.T,
	readings []string,
	readAdvance time.Duration,
) linuxVisibleCPUSampleDependencies {
	t.Helper()
	current := time.Unix(100, 0)
	readIndex := 0
	return linuxVisibleCPUSampleDependencies{
		readFile: func(path string) ([]byte, error) {
			if path != linuxSchedstatPath {
				t.Fatalf("read path = %q want %q", path, linuxSchedstatPath)
			}
			if readIndex >= len(readings) {
				t.Fatalf("unexpected scheduler statistics read %d", readIndex+1)
			}
			data := []byte(readings[readIndex])
			readIndex++
			current = current.Add(readAdvance)
			return data, nil
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
