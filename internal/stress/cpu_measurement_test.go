package stress

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCPUConsistencyMonitorAcceptsConsistentAccounting(t *testing.T) {
	var monitor cpuConsistencyMonitor
	err := monitor.observe(testCPUMeasurement(12.5, 8, 400*time.Millisecond, 400*time.Millisecond))
	if err != nil {
		t.Fatalf("consistent accounting failed: %v", err)
	}
	if monitor.suspectWindows != 0 {
		t.Fatalf("suspect windows = %d want 0", monitor.suspectWindows)
	}
}

func TestCPUConsistencyMonitorRequiresConsecutiveContradictions(t *testing.T) {
	var monitor cpuConsistencyMonitor
	bad := testCPUMeasurement(1, 8, 400*time.Millisecond, 400*time.Millisecond)

	if err := monitor.observe(bad); err != nil {
		t.Fatalf("first contradictory window should be tolerated: %v", err)
	}
	if monitor.suspectWindows != 1 {
		t.Fatalf("suspect windows = %d want 1", monitor.suspectWindows)
	}

	err := monitor.observe(bad)
	if err == nil || !strings.Contains(err.Error(), "CPU accounting mismatch") {
		t.Fatalf("second contradictory window error = %v", err)
	}
}

func TestCPUConsistencyMonitorReportsSystemScope(t *testing.T) {
	var monitor cpuConsistencyMonitor
	bad := testCPUMeasurement(1, 8, 400*time.Millisecond, 400*time.Millisecond)
	bad.scope = ScopeSystem
	bad.maxPercent = 100

	if err := monitor.observe(bad); err != nil {
		t.Fatalf("first contradictory window should be tolerated: %v", err)
	}
	err := monitor.observe(bad)
	if err == nil || !strings.Contains(err.Error(), "refusing system-scope control") {
		t.Fatalf("second contradictory window error = %v", err)
	}
}

func TestCPUConsistencyMonitorAcceptsBoundedSystemQuotaBurst(t *testing.T) {
	var monitor cpuConsistencyMonitor
	sample := testCPUMeasurement(150, 1, 600*time.Millisecond, 400*time.Millisecond)
	sample.scope = ScopeSystem
	sample.maxPercent = 200
	sample.scopeBusy = 600 * time.Millisecond
	sample.scopeElapsed = 400 * time.Millisecond
	sample.hasScopeRaw = true

	if err := monitor.observe(sample); err != nil {
		t.Fatalf("bounded system quota burst failed: %v", err)
	}
	if monitor.suspectWindows != 0 {
		t.Fatalf("suspect windows = %d want 0", monitor.suspectWindows)
	}
}

func TestNormalizeCPUMeasurementUsesRawSystemBusyTime(t *testing.T) {
	sample := testCPUMeasurement(150, 1, 600*time.Millisecond, 500*time.Millisecond)
	sample.scope = ScopeSystem
	sample.maxPercent = 200
	sample.scopeBusy = 600 * time.Millisecond
	sample.scopeElapsed = 400 * time.Millisecond
	sample.hasScopeRaw = true

	wall, _, busy, err := normalizeCPUMeasurement(sample)
	if err != nil {
		t.Fatalf("normalize raw system sample: %v", err)
	}
	if wall != 500*time.Millisecond || busy != 600*time.Millisecond {
		t.Fatalf("normalized wall/busy=%s/%s want 500ms/600ms", wall, busy)
	}
}

func TestCPUConsistencyMonitorClearsSuspicionAfterConsistentWindow(t *testing.T) {
	var monitor cpuConsistencyMonitor
	bad := testCPUMeasurement(1, 8, 400*time.Millisecond, 400*time.Millisecond)
	good := testCPUMeasurement(12.5, 8, 400*time.Millisecond, 400*time.Millisecond)

	if err := monitor.observe(bad); err != nil {
		t.Fatalf("first contradictory window: %v", err)
	}
	if err := monitor.observe(good); err != nil {
		t.Fatalf("consistent window: %v", err)
	}
	if monitor.suspectWindows != 0 {
		t.Fatalf("suspect windows after recovery = %d want 0", monitor.suspectWindows)
	}
	if err := monitor.observe(bad); err != nil {
		t.Fatalf("new first contradictory window should be tolerated: %v", err)
	}
}

func TestCPUConsistencyMonitorDiscardsInactiveWindow(t *testing.T) {
	var monitor cpuConsistencyMonitor
	bad := testCPUMeasurement(1, 8, 400*time.Millisecond, 400*time.Millisecond)
	if err := monitor.observe(bad); err != nil {
		t.Fatalf("first contradictory window: %v", err)
	}

	inactive := testCPUMeasurement(0, 8, 50*time.Millisecond, 2*time.Second)
	if err := monitor.observe(inactive); err != nil {
		t.Fatalf("inactive window: %v", err)
	}
	if monitor.suspectWindows != 0 {
		t.Fatalf("inactive window did not clear suspicion: %d", monitor.suspectWindows)
	}
}

func TestCPUConsistencyMonitorRejectsInvalidCounters(t *testing.T) {
	base := time.Unix(0, 0)
	tests := []struct {
		name   string
		sample cpuMeasurementSample
	}{
		{
			name: "process CPU moved backwards",
			sample: cpuMeasurementSample{
				hostCPUs:     8,
				processStart: processCPUSnapshot{totalCPU: time.Second, observedAt: base},
				processEnd:   processCPUSnapshot{totalCPU: 500 * time.Millisecond, observedAt: base.Add(time.Second)},
			},
		},
		{
			name: "wall clock moved backwards",
			sample: cpuMeasurementSample{
				hostCPUs:     8,
				processStart: processCPUSnapshot{observedAt: base.Add(time.Second)},
				processEnd:   processCPUSnapshot{observedAt: base},
			},
		},
		{
			name: "invalid host count",
			sample: cpuMeasurementSample{
				processStart: processCPUSnapshot{observedAt: base},
				processEnd:   processCPUSnapshot{observedAt: base.Add(time.Second)},
			},
		},
		{
			name: "host sample returned too quickly",
			sample: cpuMeasurementSample{
				hostCPUs:     8,
				minimumWall:  time.Second,
				processStart: processCPUSnapshot{observedAt: base},
				processEnd:   processCPUSnapshot{observedAt: base.Add(500 * time.Millisecond)},
			},
		},
		{
			name: "system quota burst exceeds physical ceiling",
			sample: cpuMeasurementSample{
				scope:        ScopeSystem,
				hostCPUs:     1,
				hostPercent:  201,
				maxPercent:   200,
				processStart: processCPUSnapshot{observedAt: base},
				processEnd:   processCPUSnapshot{observedAt: base.Add(time.Second)},
			},
		},
		{
			name: "raw system sample contradicts reported percent",
			sample: cpuMeasurementSample{
				scope:        ScopeSystem,
				hostCPUs:     1,
				hostPercent:  100,
				maxPercent:   200,
				scopeBusy:    500 * time.Millisecond,
				scopeElapsed: time.Second,
				hasScopeRaw:  true,
				processStart: processCPUSnapshot{observedAt: base},
				processEnd:   processCPUSnapshot{observedAt: base.Add(time.Second)},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var monitor cpuConsistencyMonitor
			if err := monitor.observe(tt.sample); err == nil {
				t.Fatal("expected invalid accounting sample to fail")
			}
		})
	}
}

func TestCPUStressorFailsClosedOnAccountingMismatch(t *testing.T) {
	stressor := newTestCPUStressor(t, CPUConfig{
		Mode:     ModeFixed,
		Scope:    ScopeHost,
		IdleMode: IdleModePark,
		Percent:  0,
		Cores:    1,
	}, defaultTestCPUCapacity)
	defer stressor.Stop()

	base := time.Unix(0, 0)
	snapshots := []processCPUSnapshot{
		{totalCPU: 0, observedAt: base},
		{totalCPU: 400 * time.Millisecond, observedAt: base.Add(400 * time.Millisecond)},
		{totalCPU: 400 * time.Millisecond, observedAt: base.Add(400 * time.Millisecond)},
		{totalCPU: 800 * time.Millisecond, observedAt: base.Add(800 * time.Millisecond)},
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
	stressor.ensureWorkersLocked(1)
	stressor.lock.Unlock()

	if !stressor.controlTick() {
		t.Fatal("first contradictory window should not stop the controller")
	}
	if stressor.controlTick() {
		t.Fatal("second contradictory window should stop the controller")
	}

	select {
	case err := <-stressor.Errors():
		if err == nil || !strings.Contains(err.Error(), "CPU accounting mismatch") {
			t.Fatalf("unexpected controller error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("accounting mismatch was not reported")
	}

	stressor.lock.RLock()
	workerCount := len(stressor.workers)
	lifecycle := stressor.lifecycle
	stressor.lock.RUnlock()
	if workerCount != 0 {
		t.Fatalf("workers after accounting mismatch = %d want 0", workerCount)
	}
	if lifecycle != cpuLifecycleFailed {
		t.Fatalf("lifecycle after accounting mismatch = %v want failed", lifecycle)
	}
	if got := stressor.Status().AppliedPercent; got != 0 {
		t.Fatalf("drive after accounting mismatch = %.1f want 0", got)
	}
}

func testCPUMeasurement(
	hostPercent float64,
	hostCPUs int,
	processCPU time.Duration,
	wall time.Duration,
) cpuMeasurementSample {
	base := time.Unix(0, 0)
	return cpuMeasurementSample{
		hostPercent: hostPercent,
		hostCPUs:    float64(hostCPUs),
		processStart: processCPUSnapshot{
			observedAt: base,
		},
		processEnd: processCPUSnapshot{
			totalCPU:   processCPU,
			observedAt: base.Add(wall),
		},
	}
}
