package stress

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

var defaultTestCPUCapacity = cpuCapacity{
	hostCPUs:    8,
	processCPUs: 8,
	maxWorkers:  8,
}

const (
	testVisibleSystemSource = "test-visible-root"
	testVisibleSystemID     = "test-visible-boundary"
)

func fixedCPUCapacity(capacity cpuCapacity) cpuCapacityDetector {
	return func() (cpuCapacity, error) {
		return capacity, nil
	}
}

func zeroHostCPUSampler(context.Context, time.Duration) (float64, error) {
	return 0, nil
}

func newIdleProcessCPUSampler() processCPUSampler {
	observedAt := time.Unix(0, 0)
	return func(context.Context) (processCPUSnapshot, error) {
		observedAt = observedAt.Add(250 * time.Millisecond)
		return processCPUSnapshot{observedAt: observedAt}, nil
	}
}

func newTestCPUStressor(t *testing.T, config CPUConfig, capacity cpuCapacity) *CPUStressor {
	t.Helper()
	stressor, err := newCPUStressor(
		config,
		fixedCPUCapacity(capacity),
		zeroHostCPUSampler,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stressor.sampleProcessCPU = newIdleProcessCPUSampler()
	stressor.setupScheduler = func(CPUWorkerScheduler) error { return nil }
	stressor.setupWorkerNice = func(int) error { return nil }
	return stressor
}

func TestNewCPUStressorValidation(t *testing.T) {
	tests := []struct {
		name     string
		cfg      CPUConfig
		capacity cpuCapacity
		wantErr  bool
	}{
		{
			name: "fixed ok",
			cfg: CPUConfig{
				Mode:    ModeFixed,
				Scope:   ScopeWorkers,
				Percent: 50,
				Cores:   2,
			},
			capacity: defaultTestCPUCapacity,
		},
		{
			name: "invalid worker scheduler",
			cfg: CPUConfig{
				Mode:            ModeFixed,
				Percent:         50,
				WorkerScheduler: "realtime",
			},
			capacity: defaultTestCPUCapacity,
			wantErr:  true,
		},
		{
			name: "fixed invalid percent",
			cfg: CPUConfig{
				Mode:    ModeFixed,
				Percent: 120,
			},
			capacity: defaultTestCPUCapacity,
			wantErr:  true,
		},
		{
			name: "fixed NaN percent",
			cfg: CPUConfig{
				Mode:    ModeFixed,
				Percent: math.NaN(),
			},
			capacity: defaultTestCPUCapacity,
			wantErr:  true,
		},
		{
			name: "fixed infinite percent",
			cfg: CPUConfig{
				Mode:    ModeFixed,
				Percent: math.Inf(1),
			},
			capacity: defaultTestCPUCapacity,
			wantErr:  true,
		},
		{
			name: "wave invalid bounds",
			cfg: CPUConfig{
				Mode:       ModeWave,
				Scope:      ScopeWorkers,
				MinPercent: 80,
				MaxPercent: 20,
				Period:     60 * time.Second,
			},
			capacity: defaultTestCPUCapacity,
			wantErr:  true,
		},
		{
			name: "wave non-finite bound",
			cfg: CPUConfig{
				Mode:       ModeWave,
				Scope:      ScopeWorkers,
				MinPercent: 20,
				MaxPercent: math.Inf(1),
				Period:     60 * time.Second,
			},
			capacity: defaultTestCPUCapacity,
			wantErr:  true,
		},
		{
			name: "wave invalid period",
			cfg: CPUConfig{
				Mode:       ModeWave,
				Scope:      ScopeWorkers,
				MinPercent: 20,
				MaxPercent: 80,
			},
			capacity: defaultTestCPUCapacity,
			wantErr:  true,
		},
		{
			name: "host target may be reached with background CPU use",
			cfg: CPUConfig{
				Mode:     ModeFixed,
				Scope:    ScopeHost,
				IdleMode: IdleModePark,
				Percent:  50,
			},
			capacity: cpuCapacity{
				hostCPUs:    8,
				processCPUs: 1,
				maxWorkers:  1,
			},
		},
		{
			name: "invalid idle mode",
			cfg: CPUConfig{
				Mode:     ModeFixed,
				Scope:    ScopeWorkers,
				IdleMode: CPUIdleMode("drop"),
				Percent:  10,
				Cores:    1,
			},
			capacity: defaultTestCPUCapacity,
			wantErr:  true,
		},
		{
			name: "negative cores",
			cfg: CPUConfig{
				Mode:    ModeFixed,
				Percent: 10,
				Cores:   -1,
			},
			capacity: defaultTestCPUCapacity,
			wantErr:  true,
		},
		{
			name: "negative cycle",
			cfg: CPUConfig{
				Mode:    ModeFixed,
				Percent: 10,
				Cycle:   -time.Millisecond,
			},
			capacity: defaultTestCPUCapacity,
			wantErr:  true,
		},
		{
			name: "negative control interval",
			cfg: CPUConfig{
				Mode:            ModeFixed,
				Percent:         10,
				ControlInterval: -time.Millisecond,
			},
			capacity: defaultTestCPUCapacity,
			wantErr:  true,
		},
		{
			name: "negative sample duration",
			cfg: CPUConfig{
				Mode:           ModeFixed,
				Percent:        10,
				SampleDuration: -time.Millisecond,
			},
			capacity: defaultTestCPUCapacity,
			wantErr:  true,
		},
		{
			name: "negative deadband",
			cfg: CPUConfig{
				Mode:            ModeFixed,
				Percent:         10,
				DeadbandPercent: -1,
			},
			capacity: defaultTestCPUCapacity,
			wantErr:  true,
		},
		{
			name: "non-finite deadband",
			cfg: CPUConfig{
				Mode:            ModeFixed,
				Percent:         10,
				DeadbandPercent: math.NaN(),
			},
			capacity: defaultTestCPUCapacity,
			wantErr:  true,
		},
		{
			name: "negative max step",
			cfg: CPUConfig{
				Mode:           ModeFixed,
				Percent:        10,
				MaxStepPercent: -1,
			},
			capacity: defaultTestCPUCapacity,
			wantErr:  true,
		},
		{
			name: "invalid detected capacity",
			cfg: CPUConfig{
				Mode:    ModeFixed,
				Percent: 10,
			},
			capacity: cpuCapacity{},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newCPUStressor(
				tt.cfg,
				fixedCPUCapacity(tt.capacity),
				zeroHostCPUSampler,
			)
			if tt.wantErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestNewCPUStressorSystemScopeUsesVisibleBoundary(t *testing.T) {
	capacity := cpuCapacity{
		hostCPUs:    8,
		processCPUs: 2,
		maxWorkers:  2,
	}
	inspect := func() (VisibleSystemCPUInfo, error) {
		return VisibleSystemCPUInfo{
			Source:       testVisibleSystemSource,
			BoundaryID:   testVisibleSystemID,
			BoundaryKind: VisibleSystemCPUBoundaryNamespaceRoot,
			CPUs:         2,
		}, nil
	}
	sample := func(
		context.Context,
		time.Duration,
	) (VisibleSystemCPUSample, error) {
		return VisibleSystemCPUSample{
			Source:       testVisibleSystemSource,
			BoundaryID:   testVisibleSystemID,
			BoundaryKind: VisibleSystemCPUBoundaryNamespaceRoot,
			CPUs:         2,
			Percent:      20,
		}, nil
	}

	stressor, err := newCPUStressorWithSystemAccounting(
		CPUConfig{
			Mode:            ModeFixed,
			Scope:           ScopeSystem,
			Percent:         40,
			DeadbandPercent: 10,
			Cores:           2,
		},
		fixedCPUCapacity(capacity),
		nil,
		inspect,
		sample,
	)
	if err != nil {
		t.Fatalf("new system stressor: %v", err)
	}
	if err := stressor.Start(); err != nil {
		t.Fatalf("start system stressor: %v", err)
	}
	defer stressor.Stop()

	status := stressor.Status()
	if status.ScopeCPUs != 2 ||
		status.MaxScopeContributionPercent != 100 ||
		status.RequestedBandLowPercent != 30 ||
		status.RequestedBandHighPercent != 50 ||
		status.AccountingSource != testVisibleSystemSource ||
		status.AccountingBoundaryID != testVisibleSystemID ||
		status.AccountingBoundaryKind != VisibleSystemCPUBoundaryNamespaceRoot {
		t.Fatalf("system status=%+v", status)
	}
}

func TestNewCPUStressorAllowsBackgroundToHelpReachSystemTarget(t *testing.T) {
	stressor, err := newCPUStressorWithSystemAccounting(
		CPUConfig{
			Mode:            ModeFixed,
			Scope:           ScopeSystem,
			Percent:         60,
			Cores:           1,
			DeadbandPercent: 1,
			MaxStepPercent:  100,
		},
		fixedCPUCapacity(cpuCapacity{
			hostCPUs:    8,
			processCPUs: 1,
			maxWorkers:  1,
		}),
		nil,
		func() (VisibleSystemCPUInfo, error) {
			return VisibleSystemCPUInfo{
				Source:       testVisibleSystemSource,
				BoundaryID:   testVisibleSystemID,
				BoundaryKind: VisibleSystemCPUBoundaryNamespaceRoot,
				CPUs:         2,
			}, nil
		},
		func(
			context.Context,
			time.Duration,
		) (VisibleSystemCPUSample, error) {
			return VisibleSystemCPUSample{
				Source:       testVisibleSystemSource,
				BoundaryID:   testVisibleSystemID,
				BoundaryKind: VisibleSystemCPUBoundaryNamespaceRoot,
				CPUs:         2,
				Percent:      20,
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("new system stressor: %v", err)
	}
	defer stressor.Stop()
	stressor.sampleProcessCPU = nil
	stressor.setupWorkerNice = func(int) error { return nil }

	stressor.lock.Lock()
	stressor.lifecycle = cpuLifecycleRunning
	stressor.startedAt = time.Now()
	if err := stressor.ensureWorkersLocked(1); err != nil {
		stressor.lock.Unlock()
		t.Fatalf("ensure workers: %v", err)
	}
	stressor.lock.Unlock()

	if !stressor.controlTick() {
		t.Fatal("system control tick failed")
	}
	status := stressor.Status()
	if status.MaxScopeContributionPercent != 50 ||
		status.LastScopePercent != 20 ||
		status.AppliedPercent != 80 {
		t.Fatalf("background-assisted status=%+v", status)
	}
}

func TestNewCPUStressorRejectsSystemControllerWithNoMeasurableContribution(t *testing.T) {
	_, err := newCPUStressorWithSystemAccounting(
		CPUConfig{
			Mode:            ModeFixed,
			Scope:           ScopeSystem,
			Percent:         50,
			DeadbandPercent: 1,
			Cores:           1,
		},
		fixedCPUCapacity(cpuCapacity{
			hostCPUs:    1,
			processCPUs: math.SmallestNonzeroFloat64,
			maxWorkers:  1,
		}),
		nil,
		func() (VisibleSystemCPUInfo, error) {
			return VisibleSystemCPUInfo{
				Source:       testVisibleSystemSource,
				BoundaryID:   testVisibleSystemID,
				BoundaryKind: VisibleSystemCPUBoundaryNamespaceRoot,
				CPUs:         math.MaxFloat64,
			}, nil
		},
		func(
			context.Context,
			time.Duration,
		) (VisibleSystemCPUSample, error) {
			return VisibleSystemCPUSample{}, nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "cannot contribute measurable load") {
		t.Fatalf("error=%v want no measurable contribution", err)
	}
}

func TestCPUStressorSystemScopeControlsVisibleUtilization(t *testing.T) {
	capacity := cpuCapacity{
		hostCPUs:    8,
		processCPUs: 2,
		maxWorkers:  2,
	}
	stressor, err := newCPUStressorWithSystemAccounting(
		CPUConfig{
			Mode:            ModeFixed,
			Scope:           ScopeSystem,
			IdleMode:        IdleModePark,
			Percent:         40,
			Cores:           2,
			DeadbandPercent: 10,
			MaxStepPercent:  100,
		},
		fixedCPUCapacity(capacity),
		nil,
		func() (VisibleSystemCPUInfo, error) {
			return VisibleSystemCPUInfo{
				Source:       testVisibleSystemSource,
				BoundaryID:   testVisibleSystemID,
				BoundaryKind: VisibleSystemCPUBoundaryNamespaceRoot,
				CPUs:         2,
			}, nil
		},
		func(
			context.Context,
			time.Duration,
		) (VisibleSystemCPUSample, error) {
			return VisibleSystemCPUSample{
				Source:       testVisibleSystemSource,
				BoundaryID:   testVisibleSystemID,
				BoundaryKind: VisibleSystemCPUBoundaryNamespaceRoot,
				CPUs:         2,
				Percent:      20,
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("new system stressor: %v", err)
	}
	defer stressor.Stop()
	stressor.sampleProcessCPU = nil
	stressor.setupWorkerNice = func(int) error { return nil }

	stressor.lock.Lock()
	stressor.lifecycle = cpuLifecycleRunning
	stressor.startedAt = time.Now()
	if err := stressor.ensureWorkersLocked(2); err != nil {
		stressor.lock.Unlock()
		t.Fatalf("ensure workers: %v", err)
	}
	stressor.lock.Unlock()

	if !stressor.controlTick() {
		t.Fatal("system control tick failed")
	}
	status := stressor.Status()
	if status.AppliedPercent != 20 ||
		status.LastScopePercent != 20 ||
		!status.HasScopeSample {
		t.Fatalf("system control status=%+v", status)
	}
}

func TestCPUStressorSystemScopeAcceptsRawQuotaBurstAndBacksOff(t *testing.T) {
	stressor, err := newCPUStressorWithSystemAccounting(
		CPUConfig{
			Mode:            ModeFixed,
			Scope:           ScopeSystem,
			IdleMode:        IdleModePark,
			Percent:         40,
			Cores:           1,
			SampleDuration:  400 * time.Millisecond,
			DeadbandPercent: 1,
			MaxStepPercent:  100,
		},
		fixedCPUCapacity(cpuCapacity{
			hostCPUs:     2,
			affinityCPUs: 2,
			gomaxprocs:   1,
			quotaCPUs:    0.5,
			quotaLimited: true,
			processCPUs:  0.5,
			maxWorkers:   1,
		}),
		nil,
		func() (VisibleSystemCPUInfo, error) {
			return VisibleSystemCPUInfo{
				Source:       testVisibleSystemSource,
				BoundaryID:   testVisibleSystemID,
				BoundaryKind: VisibleSystemCPUBoundaryNamespaceRoot,
				CPUs:         0.5,
			}, nil
		},
		func(context.Context, time.Duration) (VisibleSystemCPUSample, error) {
			return VisibleSystemCPUSample{
				Source:       testVisibleSystemSource,
				BoundaryID:   testVisibleSystemID,
				BoundaryKind: VisibleSystemCPUBoundaryNamespaceRoot,
				CPUs:         0.5,
				Percent:      150,
				Busy:         300 * time.Millisecond,
				Elapsed:      400 * time.Millisecond,
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("new system stressor: %v", err)
	}
	defer stressor.Stop()
	stressor.setupWorkerNice = func(int) error { return nil }

	base := time.Unix(0, 0)
	snapshots := []processCPUSnapshot{
		{observedAt: base},
		{totalCPU: 300 * time.Millisecond, observedAt: base.Add(500 * time.Millisecond)},
	}
	nextSnapshot := 0
	stressor.sampleProcessCPU = func(context.Context) (processCPUSnapshot, error) {
		snapshot := snapshots[nextSnapshot]
		nextSnapshot++
		return snapshot, nil
	}

	stressor.lock.Lock()
	stressor.lifecycle = cpuLifecycleRunning
	stressor.startedAt = time.Now()
	stressor.appliedPercent = 50
	stressor.lock.Unlock()

	if !stressor.controlTick() {
		t.Fatal("system controller rejected a bounded raw quota burst")
	}
	status := stressor.Status()
	if status.LastScopePercent != 150 || status.AppliedPercent != 0 {
		t.Fatalf("quota-burst status=%+v", status)
	}
}

func TestCPUStressorRejectsChangedSystemBoundary(t *testing.T) {
	capacity := cpuCapacity{
		hostCPUs:    8,
		processCPUs: 2,
		maxWorkers:  2,
	}
	inspections := 0
	stressor, err := newCPUStressorWithSystemAccounting(
		CPUConfig{
			Mode:    ModeFixed,
			Scope:   ScopeSystem,
			Percent: 40,
			Cores:   2,
		},
		fixedCPUCapacity(capacity),
		nil,
		func() (VisibleSystemCPUInfo, error) {
			inspections++
			boundaryID := testVisibleSystemID
			if inspections > 1 {
				boundaryID += "-changed"
			}
			return VisibleSystemCPUInfo{
				Source:       testVisibleSystemSource,
				BoundaryID:   boundaryID,
				BoundaryKind: VisibleSystemCPUBoundaryNamespaceRoot,
				CPUs:         2,
			}, nil
		},
		func(
			context.Context,
			time.Duration,
		) (VisibleSystemCPUSample, error) {
			return VisibleSystemCPUSample{}, nil
		},
	)
	if err != nil {
		t.Fatalf("new system stressor: %v", err)
	}
	defer stressor.Stop()

	stressor.lock.Lock()
	stressor.lifecycle = cpuLifecycleRunning
	stressor.lock.Unlock()
	if err := stressor.verifyCPUCapacity(); err == nil ||
		!strings.Contains(err.Error(), "boundary changed") {
		t.Fatalf("verify error=%v want boundary change", err)
	}
}

func TestCPUStressorRejectsChangedSystemBoundaryBeforeStart(t *testing.T) {
	inspections := 0
	stressor, err := newCPUStressorWithSystemAccounting(
		CPUConfig{
			Mode:    ModeFixed,
			Scope:   ScopeSystem,
			Percent: 40,
			Cores:   2,
		},
		fixedCPUCapacity(defaultTestCPUCapacity),
		nil,
		func() (VisibleSystemCPUInfo, error) {
			inspections++
			boundaryID := testVisibleSystemID
			if inspections > 1 {
				boundaryID += "-changed"
			}
			return VisibleSystemCPUInfo{
				Source:       testVisibleSystemSource,
				BoundaryID:   boundaryID,
				BoundaryKind: VisibleSystemCPUBoundaryNamespaceRoot,
				CPUs:         8,
			}, nil
		},
		func(context.Context, time.Duration) (VisibleSystemCPUSample, error) {
			return VisibleSystemCPUSample{}, nil
		},
	)
	if err != nil {
		t.Fatalf("new system stressor: %v", err)
	}
	defer stressor.Stop()
	stressor.setupWorkerNice = func(int) error { return nil }

	err = stressor.Start()
	if err == nil || !strings.Contains(err.Error(), "boundary changed") {
		t.Fatalf("Start error=%v want boundary change", err)
	}
}

func TestCPUStressorRejectsChangedSystemBoundaryDuringSample(t *testing.T) {
	stressor, err := newCPUStressorWithSystemAccounting(
		CPUConfig{
			Mode:    ModeFixed,
			Scope:   ScopeSystem,
			Percent: 40,
			Cores:   2,
		},
		fixedCPUCapacity(defaultTestCPUCapacity),
		nil,
		func() (VisibleSystemCPUInfo, error) {
			return VisibleSystemCPUInfo{
				Source:       testVisibleSystemSource,
				BoundaryID:   testVisibleSystemID,
				BoundaryKind: VisibleSystemCPUBoundaryNamespaceRoot,
				CPUs:         8,
			}, nil
		},
		func(context.Context, time.Duration) (VisibleSystemCPUSample, error) {
			return VisibleSystemCPUSample{
				Source:       testVisibleSystemSource,
				BoundaryID:   testVisibleSystemID + "-changed",
				BoundaryKind: VisibleSystemCPUBoundaryNamespaceRoot,
				CPUs:         8,
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("new system stressor: %v", err)
	}
	defer stressor.Stop()
	stressor.sampleProcessCPU = nil

	stressor.lock.Lock()
	stressor.lifecycle = cpuLifecycleRunning
	stressor.startedAt = time.Now()
	stressor.lock.Unlock()
	if stressor.controlTick() {
		t.Fatal("controller continued after sampled boundary changed")
	}
	select {
	case runtimeErr := <-stressor.Errors():
		if runtimeErr == nil || !strings.Contains(runtimeErr.Error(), "boundary changed") {
			t.Fatalf("runtime error=%v want boundary change", runtimeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for sampled boundary failure")
	}
}

func TestNewCPUStressorUsesSchedulingCapacityForDefaultWorkers(t *testing.T) {
	capacity := cpuCapacity{
		hostCPUs:    16,
		processCPUs: 1.5,
		maxWorkers:  2,
	}

	stressor := newTestCPUStressor(t, CPUConfig{
		Mode:    ModeFixed,
		Percent: 10,
	}, capacity)

	if stressor.config.Cores != 2 {
		t.Fatalf("default worker count = %d want 2", stressor.config.Cores)
	}
}

func TestNewCPUStressorRejectsExplicitWorkersAboveSchedulingCapacity(t *testing.T) {
	capacity := cpuCapacity{
		hostCPUs:    16,
		processCPUs: 1.5,
		maxWorkers:  2,
	}

	_, err := newCPUStressor(
		CPUConfig{
			Mode:    ModeFixed,
			Percent: 10,
			Cores:   16,
		},
		fixedCPUCapacity(capacity),
		zeroHostCPUSampler,
	)
	if err == nil {
		t.Fatal("expected explicit worker count above scheduling capacity to fail")
	}
}

func TestSameCPUCapacityChecksEverySafetyInput(t *testing.T) {
	baseline := cpuCapacity{
		hostCPUs:     8,
		affinityCPUs: 4,
		gomaxprocs:   3,
		quotaCPUs:    1.5,
		quotaLimited: true,
		processCPUs:  1.5,
		maxWorkers:   2,
	}
	tests := []struct {
		name   string
		change func(*cpuCapacity)
	}{
		{name: "host CPUs", change: func(value *cpuCapacity) { value.hostCPUs-- }},
		{name: "affinity", change: func(value *cpuCapacity) { value.affinityCPUs-- }},
		{name: "GOMAXPROCS", change: func(value *cpuCapacity) { value.gomaxprocs-- }},
		{name: "quota mode", change: func(value *cpuCapacity) { value.quotaLimited = false }},
		{name: "quota amount", change: func(value *cpuCapacity) { value.quotaCPUs = 1 }},
		{name: "effective CPUs", change: func(value *cpuCapacity) { value.processCPUs = 1 }},
		{name: "worker ceiling", change: func(value *cpuCapacity) { value.maxWorkers = 1 }},
	}

	if !sameCPUCapacity(baseline, baseline) {
		t.Fatal("identical capacities were reported as changed")
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := baseline
			test.change(&changed)
			if sameCPUCapacity(baseline, changed) {
				t.Fatalf("change in %s was not detected", test.name)
			}
		})
	}
}

func TestCPUCapacityMonitorFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		afterStart func() (cpuCapacity, error)
		want       string
	}{
		{
			name: "capacity changed",
			afterStart: func() (cpuCapacity, error) {
				changed := defaultTestCPUCapacity
				changed.hostCPUs = 4
				return changed, nil
			},
			want: "CPU capacity changed during run",
		},
		{
			name: "capacity probe failed",
			afterStart: func() (cpuCapacity, error) {
				return cpuCapacity{}, errors.New("capacity probe failed")
			},
			want: "capacity probe failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			detector := func() (cpuCapacity, error) {
				calls++
				if calls <= 2 {
					return defaultTestCPUCapacity, nil
				}
				return test.afterStart()
			}
			stressor, err := newCPUStressor(
				CPUConfig{
					Mode:            ModeFixed,
					Scope:           ScopeWorkers,
					IdleMode:        IdleModePark,
					Percent:         0,
					Cores:           1,
					ControlInterval: time.Hour,
				},
				detector,
				zeroHostCPUSampler,
			)
			if err != nil {
				t.Fatalf("newCPUStressor: %v", err)
			}
			stressor.sampleProcessCPU = newIdleProcessCPUSampler()
			stressor.setupWorkerNice = func(int) error { return nil }
			stressor.capacityInterval = time.Millisecond
			t.Cleanup(func() {
				if err := stressor.Stop(); err != nil {
					t.Errorf("Stop: %v", err)
				}
			})

			if err := stressor.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			select {
			case runtimeErr := <-stressor.Errors():
				if runtimeErr == nil || !strings.Contains(runtimeErr.Error(), test.want) {
					t.Fatalf("runtime error = %v want substring %q", runtimeErr, test.want)
				}
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for capacity monitor failure")
			}

			status := stressor.Status()
			if status.AppliedPercent != 0 || status.ActiveWorkers != 0 {
				t.Fatalf(
					"capacity failure left drive active: applied=%v workers=%d",
					status.AppliedPercent,
					status.ActiveWorkers,
				)
			}
			stressor.lock.RLock()
			workerCount := len(stressor.workers)
			lifecycle := stressor.lifecycle
			stressor.lock.RUnlock()
			if workerCount != 0 || lifecycle != cpuLifecycleFailed {
				t.Fatalf(
					"capacity failure state workers/lifecycle=%d/%v",
					workerCount,
					lifecycle,
				)
			}
		})
	}
}

func TestCPUStartRechecksCapacityBeforeCreatingWorkers(t *testing.T) {
	tests := []struct {
		name       string
		secondCall func() (cpuCapacity, error)
		want       string
	}{
		{
			name: "capacity changed",
			secondCall: func() (cpuCapacity, error) {
				changed := defaultTestCPUCapacity
				changed.processCPUs = 0.5
				changed.maxWorkers = 1
				return changed, nil
			},
			want: "CPU capacity changed before start",
		},
		{
			name: "capacity probe failed",
			secondCall: func() (cpuCapacity, error) {
				return cpuCapacity{}, errors.New("pre-start probe failed")
			},
			want: "pre-start probe failed",
		},
		{
			name: "capacity became invalid",
			secondCall: func() (cpuCapacity, error) {
				return cpuCapacity{}, nil
			},
			want: "recheck CPU capacity before start",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			detector := func() (cpuCapacity, error) {
				calls++
				if calls == 1 {
					return defaultTestCPUCapacity, nil
				}
				return test.secondCall()
			}
			stressor, err := newCPUStressor(
				CPUConfig{
					Mode:     ModeFixed,
					Scope:    ScopeWorkers,
					IdleMode: IdleModePark,
					Percent:  100,
					Cores:    1,
				},
				detector,
				zeroHostCPUSampler,
			)
			if err != nil {
				t.Fatalf("newCPUStressor: %v", err)
			}
			stressor.setupWorkerNice = func(int) error { return nil }

			err = stressor.Start()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Start error = %v want substring %q", err, test.want)
			}
			if calls != 2 {
				t.Fatalf("capacity detector calls = %d want 2", calls)
			}

			stressor.lock.RLock()
			workerCount := len(stressor.workers)
			lifecycle := stressor.lifecycle
			stressor.lock.RUnlock()
			if workerCount != 0 || lifecycle != cpuLifecycleFailed {
				t.Fatalf(
					"pre-start failure state workers/lifecycle=%d/%v",
					workerCount,
					lifecycle,
				)
			}
			if status := stressor.Status(); status.AppliedPercent != 0 || status.ActiveWorkers != 0 {
				t.Fatalf(
					"pre-start failure left drive active: applied=%v workers=%d",
					status.AppliedPercent,
					status.ActiveWorkers,
				)
			}
			if err := stressor.Stop(); err != nil {
				t.Fatalf("Stop: %v", err)
			}
		})
	}
}

func TestCPUStatusIncludesEffectiveCapacity(t *testing.T) {
	capacity := cpuCapacity{
		hostCPUs:    8,
		processCPUs: 1.5,
		maxWorkers:  2,
	}
	stressor := newTestCPUStressor(t, CPUConfig{
		Mode:    ModeFixed,
		Scope:   ScopeHost,
		Percent: 10,
		Cores:   2,
	}, capacity)

	status := stressor.Status()
	if status.HostCPUs != 8 || status.ProcessCPUs != 1.5 {
		t.Fatalf(
			"status host/process CPUs=%d/%.2f want 8/1.5",
			status.HostCPUs,
			status.ProcessCPUs,
		)
	}
	if status.MaxReachableHostPercent != 18.75 {
		t.Fatalf(
			"status max reachable host percent=%.2f want 18.75",
			status.MaxReachableHostPercent,
		)
	}
}

func TestNormalizeWorkerNice(t *testing.T) {
	tests := []struct {
		name       string
		configured *int
		want       int
		wantErr    bool
	}{
		{name: "nil uses safe default", want: defaultWorkerNice},
		{name: "inherit sentinel", configured: intPointer(WorkerNiceInherit), want: WorkerNiceInherit},
		{name: "explicit zero", configured: intPointer(0), want: 0},
		{name: "minimum", configured: intPointer(0), want: 0},
		{name: "maximum", configured: intPointer(19), want: 19},
		{name: "negative value", configured: intPointer(-1), wantErr: true},
		{name: "above inherit sentinel", configured: intPointer(21), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeWorkerNice(tt.configured)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v wantErr=%v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("worker nice = %d want %d", got, tt.want)
			}
		})
	}
}

func TestCPUWorkerPrioritySetupSemantics(t *testing.T) {
	tests := []struct {
		name       string
		configured *int
		wantNice   int
		wantCalls  int
	}{
		{
			name:      "default lowers worker priority",
			wantNice:  defaultWorkerNice,
			wantCalls: 1,
		},
		{
			name:       "explicit zero is not inherit",
			configured: intPointer(0),
			wantNice:   0,
			wantCalls:  1,
		},
		{
			name:       "inherit skips priority syscall",
			configured: intPointer(WorkerNiceInherit),
			wantNice:   WorkerNiceInherit,
			wantCalls:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stressor := newTestCPUStressor(t, CPUConfig{
				Mode:       ModeFixed,
				Scope:      ScopeWorkers,
				IdleMode:   IdleModePark,
				Percent:    0,
				Cores:      1,
				WorkerNice: tt.configured,
			}, defaultTestCPUCapacity)

			calls := 0
			gotNice := 0
			stressor.setupWorkerNice = func(nice int) error {
				calls++
				gotNice = nice
				return nil
			}
			if err := stressor.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if err := stressor.Stop(); err != nil {
				t.Fatalf("Stop: %v", err)
			}

			if calls != tt.wantCalls {
				t.Fatalf("priority setup calls = %d want %d", calls, tt.wantCalls)
			}
			if calls > 0 && gotNice != tt.wantNice {
				t.Fatalf("priority setup nice = %d want %d", gotNice, tt.wantNice)
			}
		})
	}
}

func TestCPUWorkerPrioritySetupFailureFailsStartClosed(t *testing.T) {
	stressor := newTestCPUStressor(t, CPUConfig{
		Mode:     ModeFixed,
		Scope:    ScopeWorkers,
		IdleMode: IdleModePark,
		Percent:  0,
		Cores:    2,
	}, defaultTestCPUCapacity)
	stressor.setupWorkerNice = func(int) error {
		return errors.New("setpriority denied")
	}

	err := stressor.Start()
	if err == nil || !strings.Contains(err.Error(), "setpriority denied") {
		t.Fatalf("Start error = %v", err)
	}
	stressor.lock.RLock()
	workerCount := len(stressor.workers)
	lifecycle := stressor.lifecycle
	stressor.lock.RUnlock()
	if workerCount != 0 {
		t.Fatalf("workers after setup failure = %d want 0", workerCount)
	}
	if lifecycle != cpuLifecycleFailed {
		t.Fatalf("lifecycle after setup failure = %v want failed", lifecycle)
	}
	if err := stressor.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestCPUIdleSchedulerSkipsNiceSetup(t *testing.T) {
	stressor := newTestCPUStressor(t, CPUConfig{
		Mode:            ModeFixed,
		Scope:           ScopeWorkers,
		IdleMode:        IdleModePark,
		Percent:         0,
		Cores:           1,
		WorkerScheduler: WorkerSchedulerIdle,
	}, defaultTestCPUCapacity)

	schedulerCalls := 0
	stressor.setupScheduler = func(scheduler CPUWorkerScheduler) error {
		schedulerCalls++
		if scheduler != WorkerSchedulerIdle {
			t.Fatalf("scheduler=%q want idle", scheduler)
		}
		return nil
	}
	stressor.setupWorkerNice = func(int) error {
		t.Fatal("idle scheduler unexpectedly applied nice")
		return nil
	}
	if err := stressor.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := stressor.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if schedulerCalls != 1 {
		t.Fatalf("scheduler setup calls=%d want 1", schedulerCalls)
	}
	if status := stressor.Status(); status.WorkerScheduler != WorkerSchedulerIdle {
		t.Fatalf("status scheduler=%q want idle", status.WorkerScheduler)
	}
}

func TestCPUWorkerSchedulerFailureFailsStartClosed(t *testing.T) {
	stressor := newTestCPUStressor(t, CPUConfig{
		Mode:            ModeFixed,
		Scope:           ScopeWorkers,
		IdleMode:        IdleModePark,
		Percent:         0,
		Cores:           2,
		WorkerScheduler: WorkerSchedulerIdle,
	}, defaultTestCPUCapacity)
	stressor.setupScheduler = func(CPUWorkerScheduler) error {
		return errors.New("sched idle denied")
	}

	err := stressor.Start()
	if err == nil || !strings.Contains(err.Error(), "sched idle denied") {
		t.Fatalf("Start error=%v", err)
	}
	stressor.lock.RLock()
	workerCount := len(stressor.workers)
	lifecycle := stressor.lifecycle
	stressor.lock.RUnlock()
	if workerCount != 0 {
		t.Fatalf("workers after scheduler failure=%d want 0", workerCount)
	}
	if lifecycle != cpuLifecycleFailed {
		t.Fatalf("lifecycle=%v want failed", lifecycle)
	}
	if err := stressor.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestCPUWorkerPrioritySetupFailureStopsController(t *testing.T) {
	stressor := newTestCPUStressor(t, CPUConfig{
		Mode:     ModeFixed,
		Scope:    ScopeWorkers,
		IdleMode: IdleModeTrim,
		Percent:  50,
		Cores:    1,
	}, defaultTestCPUCapacity)
	defer stressor.Stop()
	stressor.setupWorkerNice = func(int) error {
		return errors.New("priority verification failed")
	}

	stressor.lock.Lock()
	stressor.lifecycle = cpuLifecycleRunning
	stressor.startedAt = time.Now()
	stressor.lock.Unlock()

	if stressor.controlTick() {
		t.Fatal("controller continued after worker priority setup failure")
	}
	select {
	case err := <-stressor.Errors():
		if err == nil || !strings.Contains(err.Error(), "priority verification failed") {
			t.Fatalf("unexpected controller error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker priority setup failure was not reported")
	}
	if got := stressor.Status().AppliedPercent; got != 0 {
		t.Fatalf("drive after worker setup failure = %.1f want 0", got)
	}
	stressor.lock.RLock()
	workerCount := len(stressor.workers)
	stressor.lock.RUnlock()
	if workerCount != 0 {
		t.Fatalf("workers after controller setup failure = %d want 0", workerCount)
	}
}

func intPointer(value int) *int {
	return &value
}

func TestCPUWavePercent(t *testing.T) {
	stressor := newTestCPUStressor(t, CPUConfig{
		Mode:       ModeWave,
		Scope:      ScopeWorkers,
		MinPercent: 20,
		MaxPercent: 80,
		Period:     60 * time.Second,
		Cores:      1,
	}, defaultTestCPUCapacity)

	cases := []struct {
		elapsed time.Duration
		want    float64
	}{
		{elapsed: 0, want: 20},
		{elapsed: 15 * time.Second, want: 50},
		{elapsed: 30 * time.Second, want: 80},
		{elapsed: 45 * time.Second, want: 50},
	}

	for _, tc := range cases {
		got := stressor.wavePercent(tc.elapsed)
		if got != tc.want {
			t.Fatalf("elapsed=%v got=%.1f want=%.1f", tc.elapsed, got, tc.want)
		}
	}
}

func TestCPUPercentConversionHelpersUseEffectiveCapacity(t *testing.T) {
	capacity := cpuCapacity{
		hostCPUs:    160,
		processCPUs: 2,
		maxWorkers:  2,
	}

	if got := maxReachableHostPercent(4, capacity); got != 1.25 {
		t.Fatalf("maxReachableHostPercent got %.2f want 1.25", got)
	}

	if got := workerPercentToHostPercent(25, 4, capacity); got != 0.625 {
		t.Fatalf("workerPercentToHostPercent got %.3f want 0.625", got)
	}

	if got := hostPercentToWorkerPercent(0.625, 4, capacity); got != 25 {
		t.Fatalf("hostPercentToWorkerPercent got %.2f want 25.00", got)
	}

	if got := workerPercentToHostPercent(100, 4, capacity); got != 1.25 {
		t.Fatalf("quota-capped worker percent got %.2f want 1.25", got)
	}

	if got := workerCapacityPercentToDrivePercent(50, 4, capacity); got != 25 {
		t.Fatalf("worker capacity drive got %.2f want 25.00", got)
	}
}

func TestWorkersScopeScalesFractionalQuotaTarget(t *testing.T) {
	capacity := cpuCapacity{
		hostCPUs:     8,
		affinityCPUs: 8,
		gomaxprocs:   1,
		quotaCPUs:    0.5,
		quotaLimited: true,
		processCPUs:  0.5,
		maxWorkers:   1,
	}
	stressor := newTestCPUStressor(t, CPUConfig{
		Mode:     ModeFixed,
		Scope:    ScopeWorkers,
		IdleMode: IdleModePark,
		Percent:  50,
	}, capacity)
	t.Cleanup(func() {
		if err := stressor.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	if err := stressor.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := stressor.Status()
	if status.RequestedPercent != 50 || status.AppliedPercent != 25 {
		t.Fatalf(
			"requested/applied=%.1f/%.1f want 50.0/25.0",
			status.RequestedPercent,
			status.AppliedPercent,
		)
	}
	if len(stressor.workers) != 1 {
		t.Fatalf("workers=%d want 1", len(stressor.workers))
	}
	if got := stressor.workers[0].duty.Load(); got != dutyScale/4 {
		t.Fatalf("worker duty=%d want %d", got, dutyScale/4)
	}
}

func TestNextHostAdaptiveAppliedPercent(t *testing.T) {
	capacity := cpuCapacity{
		hostCPUs:    160,
		processCPUs: 160,
		maxWorkers:  160,
	}
	tests := []struct {
		name     string
		target   float64
		observed float64
		current  float64
		deadband float64
		maxStep  float64
		want     float64
	}{
		{
			name:     "reduces drive from actual overload error",
			target:   50,
			observed: 70,
			current:  40,
			deadband: 1,
			maxStep:  100,
			want:     20,
		},
		{
			name:     "drops to zero when baseline host load already exceeds target",
			target:   50,
			observed: 70,
			current:  10,
			deadband: 1,
			maxStep:  100,
			want:     0,
		},
		{
			name:     "holds within deadband",
			target:   50,
			observed: 50.5,
			current:  40,
			deadband: 1,
			maxStep:  100,
			want:     40,
		},
		{
			name:     "limits adjustment step",
			target:   50,
			observed: 10,
			current:  0,
			deadband: 1,
			maxStep:  10,
			want:     10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextHostAdaptiveAppliedPercent(
				tt.target,
				tt.observed,
				tt.current,
				160,
				capacity,
				tt.deadband,
				tt.maxStep,
			)
			if got != tt.want {
				t.Fatalf("got %.2f want %.2f", got, tt.want)
			}
		})
	}
}

func TestNextHostAdaptiveAppliedPercentIntegratesActualDeliveryError(t *testing.T) {
	capacity := cpuCapacity{
		hostCPUs:    8,
		processCPUs: 1,
		maxWorkers:  1,
	}

	first := nextHostAdaptiveAppliedPercent(10, 0, 0, 1, capacity, 0, 100)
	if first != 80 {
		t.Fatalf("initial feed-forward drive = %.1f want 80.0", first)
	}

	second := nextHostAdaptiveAppliedPercent(10, 6, first, 1, capacity, 0, 100)
	if second != 100 {
		t.Fatalf("drive after under-delivery = %.1f want 100.0", second)
	}

	backoff := nextHostAdaptiveAppliedPercent(10, 12, second, 1, capacity, 0, 100)
	if backoff != 84 {
		t.Fatalf("drive after overload = %.1f want 84.0", backoff)
	}

	backgroundAware := nextHostAdaptiveAppliedPercent(10, 4, 0, 1, capacity, 0, 100)
	if backgroundAware != 48 {
		t.Fatalf("drive with 4%% background load = %.1f want 48.0", backgroundAware)
	}
}

func TestNextScopeAdaptiveAppliedPercentHonorsFractionalQuotaCeiling(t *testing.T) {
	capacity := cpuCapacity{
		hostCPUs:     8,
		affinityCPUs: 8,
		gomaxprocs:   1,
		quotaCPUs:    0.5,
		quotaLimited: true,
		processCPUs:  0.5,
		maxWorkers:   1,
	}

	saturated := nextScopeAdaptiveAppliedPercent(
		40,
		0,
		0,
		1,
		capacity,
		2,
		0,
		100,
	)
	if saturated != 50 {
		t.Fatalf("fractional quota saturation = %.1f want 50.0", saturated)
	}

	backoff := nextScopeAdaptiveAppliedPercent(
		40,
		60,
		100,
		1,
		capacity,
		2,
		0,
		5,
	)
	if backoff != 45 {
		t.Fatalf("business-load backoff = %.1f want 45.0", backoff)
	}

	burstBackoff := nextScopeAdaptiveAppliedPercent(
		40,
		150,
		50,
		1,
		capacity,
		0.5,
		0,
		100,
	)
	if burstBackoff != 0 {
		t.Fatalf("quota-burst backoff = %.1f want 0.0", burstBackoff)
	}
}

func TestHostControllerAppliesFractionalQuotaCeilingAndBacksOff(t *testing.T) {
	samples := []float64{0, 60}
	sampleIndex := 0
	stressor, err := newCPUStressor(
		CPUConfig{
			Mode:            ModeFixed,
			Scope:           ScopeHost,
			IdleMode:        IdleModePark,
			Percent:         40,
			Cores:           1,
			DeadbandPercent: 1,
			MaxStepPercent:  100,
		},
		fixedCPUCapacity(cpuCapacity{
			hostCPUs:     2,
			affinityCPUs: 2,
			gomaxprocs:   1,
			quotaCPUs:    0.5,
			quotaLimited: true,
			processCPUs:  0.5,
			maxWorkers:   1,
		}),
		func(context.Context, time.Duration) (float64, error) {
			value := samples[sampleIndex]
			sampleIndex++
			return value, nil
		},
	)
	if err != nil {
		t.Fatalf("new host stressor: %v", err)
	}
	defer stressor.Stop()
	stressor.sampleProcessCPU = nil
	stressor.setupWorkerNice = func(int) error { return nil }

	stressor.lock.Lock()
	stressor.lifecycle = cpuLifecycleRunning
	stressor.startedAt = time.Now()
	if err := stressor.ensureWorkersLocked(1); err != nil {
		stressor.lock.Unlock()
		t.Fatalf("ensure workers: %v", err)
	}
	stressor.lock.Unlock()

	if !stressor.controlTick() {
		t.Fatal("initial host control tick failed")
	}
	if got := stressor.Status().AppliedPercent; got != 50 {
		t.Fatalf("saturated drive = %.1f want 50.0", got)
	}

	stressor.config.MaxStepPercent = 5
	if !stressor.controlTick() {
		t.Fatal("business-load host control tick failed")
	}
	if got := stressor.Status().AppliedPercent; got != 45 {
		t.Fatalf("drive after business load = %.1f want 45.0", got)
	}
}

func TestLinuxCPUListParser(t *testing.T) {
	got, err := parseLinuxCPUList("0-3,8,10-11")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 7 {
		t.Fatalf("CPU count = %d want 7", got)
	}

	for _, value := range []string{"", "3-1", "0,,2", "a"} {
		t.Run(value, func(t *testing.T) {
			if _, err := parseLinuxCPUList(value); err == nil {
				t.Fatalf("expected %q to fail", value)
			}
		})
	}
}

func TestCgroupV2CPUMaxParser(t *testing.T) {
	tests := []struct {
		value       string
		wantQuota   float64
		wantLimited bool
		wantErr     bool
	}{
		{value: "max 100000", wantLimited: false},
		{value: "150000 100000", wantQuota: 1.5, wantLimited: true},
		{value: "0 100000", wantErr: true},
		{value: "garbage", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			gotQuota, gotLimited, err := parseCgroupV2CPUMax(tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v wantErr=%v", err, tt.wantErr)
			}
			if gotQuota != tt.wantQuota || gotLimited != tt.wantLimited {
				t.Fatalf(
					"quota/limited = %.2f/%v want %.2f/%v",
					gotQuota,
					gotLimited,
					tt.wantQuota,
					tt.wantLimited,
				)
			}
		})
	}
}

func TestCPUStressorReportsFatalSamplingErrorAndZerosDrive(t *testing.T) {
	firstSample := make(chan struct{})
	allowFailure := make(chan struct{})
	sampleCount := 0
	sampler := func(ctx context.Context, _ time.Duration) (float64, error) {
		sampleCount++
		if sampleCount == 1 {
			close(firstSample)
			return 0, nil
		}
		select {
		case <-allowFailure:
			return 0, errors.New("sample failed")
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	stressor, err := newCPUStressor(
		CPUConfig{
			Mode:            ModeFixed,
			Scope:           ScopeHost,
			Percent:         25,
			Cores:           2,
			ControlInterval: time.Millisecond,
			MaxStepPercent:  100,
		},
		fixedCPUCapacity(defaultTestCPUCapacity),
		sampler,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stressor.sampleProcessCPU = newIdleProcessCPUSampler()
	stressor.setupWorkerNice = func(int) error { return nil }
	defer stressor.Stop()

	if err := stressor.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	select {
	case <-firstSample:
	case <-time.After(time.Second):
		t.Fatal("first host sample did not run")
	}
	deadline := time.Now().Add(time.Second)
	for stressor.Status().AppliedPercent != 100 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	status := stressor.Status()
	if status.AppliedPercent != 100 {
		t.Fatalf("drive before sample failure = %.1f want 100", status.AppliedPercent)
	}
	if !status.HasHostSample {
		t.Fatal("successful host sample was not marked valid")
	}

	close(allowFailure)
	select {
	case got := <-stressor.Errors():
		if !strings.Contains(got.Error(), "sample failed") {
			t.Fatalf("unexpected fatal error: %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("sampling error was not reported")
	}

	if got := stressor.Status().AppliedPercent; got != 0 {
		t.Fatalf("drive after sample failure = %.1f want 0", got)
	}
	if err := stressor.Start(); err == nil {
		t.Fatal("expected failed stressor restart to be rejected")
	}
}

func TestCPUStressorStopCancelsHostSampleAndPreventsRestart(t *testing.T) {
	sampleStarted := make(chan struct{})
	sampler := func(ctx context.Context, _ time.Duration) (float64, error) {
		close(sampleStarted)
		<-ctx.Done()
		return 0, ctx.Err()
	}
	stressor, err := newCPUStressor(
		CPUConfig{
			Mode:           ModeFixed,
			Scope:          ScopeHost,
			Percent:        0,
			SampleDuration: time.Hour,
		},
		fixedCPUCapacity(defaultTestCPUCapacity),
		sampler,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stressor.sampleProcessCPU = newIdleProcessCPUSampler()
	stressor.setupWorkerNice = func(int) error { return nil }
	if err := stressor.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	select {
	case <-sampleStarted:
	case <-time.After(time.Second):
		t.Fatal("host sample did not start")
	}

	stopped := make(chan struct{})
	go func() {
		_ = stressor.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel host sample")
	}

	if err := stressor.Start(); err == nil {
		t.Fatal("expected stopped stressor restart to be rejected")
	}
	if stressor.Status().HasHostSample {
		t.Fatal("canceled host sample was marked valid")
	}
	select {
	case err, ok := <-stressor.Errors():
		if ok {
			t.Fatalf("canceled sample was reported as an error: %v", err)
		}
	default:
		t.Fatal("Errors channel was not closed by Stop")
	}
}

func TestCPUStressorRejectsRepeatedStart(t *testing.T) {
	stressor := newTestCPUStressor(t, CPUConfig{
		Mode:    ModeFixed,
		Scope:   ScopeWorkers,
		Percent: 0,
		Cores:   1,
	}, defaultTestCPUCapacity)
	defer stressor.Stop()

	if err := stressor.Start(); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := stressor.Start(); err == nil {
		t.Fatal("expected repeated Start to fail")
	}
}

func TestTrimIdleModeShrinksWorkerPool(t *testing.T) {
	stressor := newTestCPUStressor(t, CPUConfig{
		Mode:     ModeFixed,
		Scope:    ScopeWorkers,
		IdleMode: IdleModeTrim,
		Percent:  0,
		Cores:    4,
	}, defaultTestCPUCapacity)
	defer stressor.Stop()

	stressor.lock.Lock()
	stressor.applyTargetLocked(50)
	activeAfterGrow := len(stressor.workers)
	stressor.applyTargetLocked(0)
	activeAfterTrim := len(stressor.workers)
	stressor.lock.Unlock()

	if activeAfterGrow != 2 {
		t.Fatalf("active workers after grow = %d want 2", activeAfterGrow)
	}
	if activeAfterTrim != 0 {
		t.Fatalf("active workers after trim = %d want 0", activeAfterTrim)
	}
}

func TestCPUStatusCountsOnlyActiveWorkers(t *testing.T) {
	stressor := newTestCPUStressor(t, CPUConfig{
		Mode:     ModeFixed,
		Scope:    ScopeWorkers,
		IdleMode: IdleModePark,
		Percent:  0,
		Cores:    4,
	}, defaultTestCPUCapacity)
	defer stressor.Stop()

	stressor.lock.Lock()
	stressor.applyTargetLocked(50)
	stressor.lock.Unlock()

	status := stressor.Status()
	if status.ActiveWorkers != 2 {
		t.Fatalf("status active workers = %d want 2", status.ActiveWorkers)
	}
	if status.MaxWorkers != 4 {
		t.Fatalf("status max workers = %d want 4", status.MaxWorkers)
	}
}
