package cmd

import (
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fanderchan/loadsim/internal/stress"
)

func testAdaptiveMemoryConfig() adaptiveMemoryConfig {
	return adaptiveMemoryConfig{
		lowPercent:               30,
		highPercent:              50,
		maxLoadPercent:           30,
		interval:                 time.Second,
		blockMB:                  16,
		configuredMinAvailableMB: 0,
	}
}

func TestValidateAdaptiveMemoryConfig(t *testing.T) {
	valid := testAdaptiveMemoryConfig()
	if err := validateAdaptiveMemoryConfig(valid); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	tests := []struct {
		name   string
		change func(*adaptiveMemoryConfig)
	}{
		{name: "NaN minimum", change: func(c *adaptiveMemoryConfig) { c.lowPercent = math.NaN() }},
		{name: "infinite maximum", change: func(c *adaptiveMemoryConfig) { c.highPercent = math.Inf(1) }},
		{name: "negative minimum", change: func(c *adaptiveMemoryConfig) { c.lowPercent = -1 }},
		{name: "reversed range", change: func(c *adaptiveMemoryConfig) { c.lowPercent = 50 }},
		{name: "maximum above safety ceiling", change: func(c *adaptiveMemoryConfig) { c.highPercent = 81 }},
		{name: "narrow range", change: func(c *adaptiveMemoryConfig) { c.highPercent = 34 }},
		{name: "load share below minimum", change: func(c *adaptiveMemoryConfig) { c.maxLoadPercent = 29 }},
		{name: "zero load share", change: func(c *adaptiveMemoryConfig) { c.maxLoadPercent = 0 }},
		{name: "fast interval", change: func(c *adaptiveMemoryConfig) { c.interval = 100 * time.Millisecond }},
		{name: "zero block", change: func(c *adaptiveMemoryConfig) { c.blockMB = 0 }},
		{name: "large block", change: func(c *adaptiveMemoryConfig) { c.blockMB = maximumAdaptiveMemoryBlockMB + 1 }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.change(&config)
			if err := validateAdaptiveMemoryConfig(config); err == nil {
				t.Fatal("invalid config was accepted")
			}
		})
	}
}

func TestParseRAMMode(t *testing.T) {
	mode, adaptive, err := parseRAMMode(adaptiveRAMMode)
	if err != nil || mode != stress.ModeFixed || !adaptive {
		t.Fatalf("adaptive parse mode/adaptive/error=%q/%v/%v", mode, adaptive, err)
	}
	mode, adaptive, err = parseRAMMode("wave")
	if err != nil || mode != stress.ModeWave || adaptive {
		t.Fatalf("wave parse mode/adaptive/error=%q/%v/%v", mode, adaptive, err)
	}
	if _, _, err := parseRAMMode("unknown"); err == nil ||
		!strings.Contains(err.Error(), "adaptive") {
		t.Fatalf("invalid mode error=%v", err)
	}
}

func TestAdaptiveMemoryWaitsForTwoLowSamplesThenGrowsToCap(t *testing.T) {
	config := testAdaptiveMemoryConfig()
	capacities := []memoryCapacity{{
		totalMB: 1000,
		usedMB:  200,
		source:  "host",
	}}

	first, err := nextAdaptiveMemoryTarget(config, capacities, 0, 0, 0)
	if err != nil {
		t.Fatalf("first decision: %v", err)
	}
	if first.action != adaptiveMemoryConfirm ||
		first.targetMB != 0 ||
		first.lowSamples != 1 {
		t.Fatalf("first low decision=%+v", first)
	}

	second, err := nextAdaptiveMemoryTarget(
		config,
		capacities,
		0,
		0,
		first.lowSamples,
	)
	if err != nil {
		t.Fatalf("second decision: %v", err)
	}
	if second.action != adaptiveMemoryGrow ||
		second.targetMB != 200 ||
		second.hardCapMB != 300 {
		t.Fatalf("second low decision=%+v want grow to 200MB within 300MB cap", second)
	}
}

func TestAdaptiveMemoryClosedBandHoldsAndCancelsPendingGrowth(t *testing.T) {
	config := testAdaptiveMemoryConfig()
	for _, usedMB := range []uint64{300, 500} {
		decision, err := nextAdaptiveMemoryTarget(
			config,
			[]memoryCapacity{{
				totalMB: 1000,
				usedMB:  usedMB,
				source:  "host",
			}},
			100,
			250,
			1,
		)
		if err != nil {
			t.Fatalf("used=%d decision: %v", usedMB, err)
		}
		if decision.action != adaptiveMemoryHold ||
			decision.targetMB != 100 ||
			decision.lowSamples != 0 {
			t.Fatalf("used=%d decision=%+v want closed-band hold", usedMB, decision)
		}
	}
}

func TestAdaptiveMemoryAboveHighShrinksToMidpointImmediately(t *testing.T) {
	decision, err := nextAdaptiveMemoryTarget(
		testAdaptiveMemoryConfig(),
		[]memoryCapacity{{
			totalMB: 1000,
			usedMB:  510,
			source:  "host",
		}},
		300,
		300,
		2,
	)
	if err != nil {
		t.Fatalf("decision: %v", err)
	}
	if decision.action != adaptiveMemoryShrink ||
		decision.targetMB != 190 ||
		decision.lowSamples != 0 {
		t.Fatalf("decision=%+v want immediate shrink to 190MB", decision)
	}
}

func TestAdaptiveMemoryUsesMostConservativeConstraint(t *testing.T) {
	config := testAdaptiveMemoryConfig()
	growth, err := nextAdaptiveMemoryTarget(
		config,
		[]memoryCapacity{
			{totalMB: 1000, usedMB: 100, source: "host"},
			{totalMB: 500, usedMB: 100, source: "cgroup(memory.max)"},
		},
		0,
		0,
		1,
	)
	if err != nil {
		t.Fatalf("growth decision: %v", err)
	}
	if growth.action != adaptiveMemoryGrow ||
		growth.targetMB != 100 ||
		growth.hardCapMB != 150 {
		t.Fatalf("growth decision=%+v want cgroup-limited 100MB", growth)
	}

	shrink, err := nextAdaptiveMemoryTarget(
		config,
		[]memoryCapacity{
			{totalMB: 1000, usedMB: 600, source: "host"},
			{totalMB: 500, usedMB: 300, source: "cgroup(memory.max)"},
		},
		300,
		300,
		0,
	)
	if err != nil {
		t.Fatalf("shrink decision: %v", err)
	}
	if shrink.action != adaptiveMemoryShrink || shrink.targetMB != 100 {
		t.Fatalf("shrink decision=%+v want host-limited 100MB", shrink)
	}
}

func TestAdaptiveMemoryIgnoresUnlimitedV1SentinelForGrowth(t *testing.T) {
	config := testAdaptiveMemoryConfig()
	capacities := []memoryCapacity{
		{totalMB: 1000, usedMB: 200, source: "host"},
		{
			totalMB: uint64(math.MaxInt64 / (1024 * 1024)),
			usedMB:  1,
			source:  "cgroup(memory.limit_in_bytes)",
		},
	}

	decision, err := nextAdaptiveMemoryTarget(
		config,
		capacities,
		0,
		0,
		requiredLowSamplesBeforeGrowth-1,
	)
	if err != nil {
		t.Fatalf("decision: %v", err)
	}
	if decision.action != adaptiveMemoryGrow ||
		decision.targetMB != 200 ||
		decision.hardCapMB != 300 {
		t.Fatalf("decision=%+v want host-limited 200MB growth and 300MB cap", decision)
	}
}

func TestAdaptiveMemoryShrinksOnOvercommittedConstraint(t *testing.T) {
	decision, err := nextAdaptiveMemoryTarget(
		testAdaptiveMemoryConfig(),
		[]memoryCapacity{
			{totalMB: 1000, usedMB: 200, source: "host"},
			{
				totalMB: 100,
				usedMB:  101,
				source:  "cgroup(memory.limit_in_bytes)",
			},
		},
		50,
		50,
		0,
	)
	if err != nil {
		t.Fatalf("decision: %v", err)
	}
	if decision.action != adaptiveMemoryShrink || decision.targetMB != 0 {
		t.Fatalf("decision=%+v want fail-safe shrink to 0MB", decision)
	}
}

func TestAdaptiveMemoryHardCapShrinksEvenInsideBand(t *testing.T) {
	for _, test := range []struct {
		name        string
		usedMB      uint64
		requestedMB int
	}{
		{name: "inside band", usedMB: 400, requestedMB: 350},
		{name: "inside band with pending growth", usedMB: 400, requestedMB: 450},
		{name: "below band with pending growth", usedMB: 200, requestedMB: 450},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision, err := nextAdaptiveMemoryTarget(
				testAdaptiveMemoryConfig(),
				[]memoryCapacity{{
					totalMB: 1000,
					usedMB:  test.usedMB,
					source:  "host",
				}},
				350,
				test.requestedMB,
				1,
			)
			if err != nil {
				t.Fatalf("decision: %v", err)
			}
			if decision.action != adaptiveMemoryShrink ||
				decision.targetMB != 300 ||
				decision.lowSamples != 0 {
				t.Fatalf("decision=%+v want immediate hard-cap shrink to 300MB", decision)
			}
		})
	}
}

func TestAdaptiveMemoryControllerRejectsConcurrentStart(t *testing.T) {
	stressor, err := stress.NewRAMStressor(stress.RAMConfig{
		Mode:              stress.ModeFixed,
		SizeMB:            1,
		RateLimitMBPerSec: 1,
		ImmediateShrink:   true,
	})
	if err != nil {
		t.Fatalf("NewRAMStressor: %v", err)
	}
	controller, err := newAdaptiveMemoryController(
		testAdaptiveMemoryConfig(),
		stressor,
		stressor.Stop,
	)
	if err != nil {
		t.Fatalf("new controller: %v", err)
	}

	probeEntered := make(chan struct{})
	releaseProbe := make(chan struct{})
	var probes atomic.Int32
	controller.probe = func() ([]memoryCapacity, error) {
		if probes.Add(1) == 1 {
			close(probeEntered)
			<-releaseProbe
		}
		return []memoryCapacity{{
			availableMB: 80,
			totalMB:     100,
			usedMB:      40,
			source:      "test",
		}}, nil
	}
	t.Cleanup(func() {
		controller.Stop()
		if err := stressor.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- controller.Start()
	}()
	<-probeEntered

	if err := controller.Start(); err == nil ||
		!strings.Contains(err.Error(), "already started") {
		t.Fatalf("concurrent Start error=%v", err)
	}
	close(releaseProbe)
	if err := <-firstResult; err != nil {
		t.Fatalf("first Start: %v", err)
	}
}

func TestAdaptiveMemoryControllerStopWaitsForStartupAndFailsClosed(t *testing.T) {
	stressor, err := stress.NewRAMStressor(stress.RAMConfig{
		Mode:              stress.ModeFixed,
		SizeMB:            1,
		BlockMB:           1,
		ControlInterval:   time.Hour,
		RateLimitMBPerSec: 1,
		ImmediateShrink:   true,
	})
	if err != nil {
		t.Fatalf("NewRAMStressor: %v", err)
	}
	controller, err := newAdaptiveMemoryController(
		testAdaptiveMemoryConfig(),
		stressor,
		stressor.Stop,
	)
	if err != nil {
		t.Fatalf("new controller: %v", err)
	}
	controller.probe = func() ([]memoryCapacity, error) {
		return []memoryCapacity{{
			availableMB: 80,
			totalMB:     100,
			usedMB:      20,
			source:      "test",
		}}, nil
	}
	if _, err := controller.Preflight(); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if err := stressor.Start(); err != nil {
		t.Fatalf("stressor Start: %v", err)
	}
	t.Cleanup(func() {
		controller.Stop()
		if err := stressor.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	probeEntered := make(chan struct{})
	releaseProbe := make(chan struct{})
	controller.probe = func() ([]memoryCapacity, error) {
		close(probeEntered)
		<-releaseProbe
		return []memoryCapacity{{
			availableMB: 80,
			totalMB:     100,
			usedMB:      20,
			source:      "test",
		}}, nil
	}

	startResult := make(chan error, 1)
	go func() {
		startResult <- controller.Start()
	}()
	<-probeEntered

	stopDone := make(chan struct{})
	go func() {
		controller.Stop()
		close(stopDone)
	}()
	for {
		controller.lock.RLock()
		stopped := controller.stopped
		controller.lock.RUnlock()
		if stopped {
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-stopDone:
		t.Fatal("Stop returned before startup completed")
	default:
	}

	close(releaseProbe)
	if err := <-startResult; err == nil ||
		!strings.Contains(err.Error(), "stopped during startup") {
		t.Fatalf("Start error=%v", err)
	}
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("Stop did not finish after startup failed closed")
	}
	status := stressor.Status()
	if status.RequestedMB != 0 || status.CurrentMB != 0 {
		t.Fatalf(
			"RAM after concurrent Stop requested/current=%d/%d want 0/0",
			status.RequestedMB,
			status.CurrentMB,
		)
	}
}

func TestAdaptiveMemoryRejectsInvalidCapacitySnapshots(t *testing.T) {
	if _, err := adaptiveMemoryHardCapMB(nil, 30); err == nil {
		t.Fatal("empty capacity list was accepted")
	}
	if _, err := nextAdaptiveMemoryTarget(
		testAdaptiveMemoryConfig(),
		[]memoryCapacity{{source: "broken"}},
		0,
		0,
		0,
	); err == nil {
		t.Fatal("zero-total capacity was accepted")
	}
}

func TestAdaptiveMemoryControllerPreflightStartsAtZero(t *testing.T) {
	stressor, err := stress.NewRAMStressor(stress.RAMConfig{
		Mode:              stress.ModeFixed,
		SizeMB:            1,
		BlockMB:           1,
		ControlInterval:   time.Hour,
		RateLimitMBPerSec: 100,
		ImmediateShrink:   true,
	})
	if err != nil {
		t.Fatalf("NewRAMStressor: %v", err)
	}
	controller, err := newAdaptiveMemoryController(
		adaptiveMemoryConfig{
			lowPercent:               30,
			highPercent:              50,
			maxLoadPercent:           30,
			interval:                 time.Hour,
			blockMB:                  1,
			configuredMinAvailableMB: 0,
		},
		stressor,
		stressor.Stop,
	)
	if err != nil {
		t.Fatalf("new controller: %v", err)
	}
	controller.probe = func() ([]memoryCapacity, error) {
		return []memoryCapacity{{
			availableMB: 80,
			totalMB:     100,
			usedMB:      20,
			source:      "test",
		}}, nil
	}
	t.Cleanup(func() {
		controller.Stop()
		if err := stressor.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	hardCapMB, err := controller.Preflight()
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if hardCapMB != 30 || stressor.Status().RequestedMB != 0 {
		t.Fatalf(
			"cap/requested=%d/%d want 30/0",
			hardCapMB,
			stressor.Status().RequestedMB,
		)
	}
	if err := stressor.Start(); err != nil {
		t.Fatalf("stressor Start: %v", err)
	}
	if err := controller.Start(); err != nil {
		t.Fatalf("controller Start: %v", err)
	}
	if err := controller.adjust(); err != nil {
		t.Fatalf("second adjustment: %v", err)
	}
	if got := stressor.Status().RequestedMB; got != 20 {
		t.Fatalf("requested=%d want midpoint-limited 20MB", got)
	}
	ramStatus, adaptiveStatus := controller.Snapshot()
	if adaptiveStatus.targetMB != ramStatus.RequestedMB {
		t.Fatalf(
			"coherent snapshot band target/requested=%d/%d",
			adaptiveStatus.targetMB,
			ramStatus.RequestedMB,
		)
	}
}

func TestAdaptiveMemoryControllerPropagatesProbeFailure(t *testing.T) {
	stressor, err := stress.NewRAMStressor(stress.RAMConfig{
		Mode:              stress.ModeFixed,
		SizeMB:            1,
		RateLimitMBPerSec: 1,
		ImmediateShrink:   true,
	})
	if err != nil {
		t.Fatalf("NewRAMStressor: %v", err)
	}
	controller, err := newAdaptiveMemoryController(
		testAdaptiveMemoryConfig(),
		stressor,
		stressor.Stop,
	)
	if err != nil {
		t.Fatalf("new controller: %v", err)
	}
	controller.probe = func() ([]memoryCapacity, error) {
		return nil, errors.New("capacity read failed")
	}
	t.Cleanup(func() {
		controller.Stop()
		if err := stressor.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	if _, err := controller.Preflight(); err == nil ||
		!strings.Contains(err.Error(), "capacity read failed") {
		t.Fatalf("Preflight error=%v", err)
	}
}

func TestAdaptiveMemoryControllerRequiresSafeStressorConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  stress.RAMConfig
		wantErr string
	}{
		{
			name: "fixed mode",
			config: stress.RAMConfig{
				Mode:              stress.ModeWave,
				MinSizeMB:         0,
				MaxSizeMB:         1,
				Period:            time.Second,
				RateLimitMBPerSec: 1,
				ImmediateShrink:   true,
			},
			wantErr: "fixed-mode",
		},
		{
			name: "positive growth limit",
			config: stress.RAMConfig{
				Mode:            stress.ModeFixed,
				SizeMB:          1,
				ImmediateShrink: true,
			},
			wantErr: "positive RAM growth rate limit",
		},
		{
			name: "immediate release",
			config: stress.RAMConfig{
				Mode:              stress.ModeFixed,
				SizeMB:            1,
				RateLimitMBPerSec: 1,
			},
			wantErr: "immediate RAM release",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stressor, err := stress.NewRAMStressor(test.config)
			if err != nil {
				t.Fatalf("NewRAMStressor: %v", err)
			}
			t.Cleanup(func() {
				if err := stressor.Stop(); err != nil {
					t.Errorf("Stop: %v", err)
				}
			})

			if _, err := newAdaptiveMemoryController(
				testAdaptiveMemoryConfig(),
				stressor,
				stressor.Stop,
			); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("constructor error=%v want %q", err, test.wantErr)
			}
		})
	}
}

func TestAdaptiveMemoryRuntimeProbeFailureStopsStressor(t *testing.T) {
	stressor, err := stress.NewRAMStressor(stress.RAMConfig{
		Mode:              stress.ModeFixed,
		SizeMB:            1,
		BlockMB:           1,
		ControlInterval:   time.Hour,
		RateLimitMBPerSec: 1,
		ImmediateShrink:   true,
	})
	if err != nil {
		t.Fatalf("NewRAMStressor: %v", err)
	}
	config := testAdaptiveMemoryConfig()
	config.blockMB = 1
	config.interval = minimumAdaptiveMemoryInterval
	controller, err := newAdaptiveMemoryController(
		config,
		stressor,
		stressor.Stop,
	)
	if err != nil {
		t.Fatalf("new controller: %v", err)
	}

	var probes atomic.Int32
	controller.probe = func() ([]memoryCapacity, error) {
		if probes.Add(1) >= 3 {
			return nil, errors.New("runtime capacity read failed")
		}
		return []memoryCapacity{{
			availableMB: 80,
			totalMB:     100,
			usedMB:      40,
			source:      "test",
		}}, nil
	}
	t.Cleanup(func() {
		controller.Stop()
		if err := stressor.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	if _, err := controller.Preflight(); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if err := stressor.Start(); err != nil {
		t.Fatalf("stressor Start: %v", err)
	}
	if err := controller.Start(); err != nil {
		t.Fatalf("controller Start: %v", err)
	}

	select {
	case runtimeErr := <-controller.Errors():
		if runtimeErr == nil ||
			!strings.Contains(runtimeErr.Error(), "runtime capacity read failed") {
			t.Fatalf("runtime error=%v", runtimeErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for adaptive probe failure")
	}
	if err := stressor.UpdateTargetMB(1); err == nil ||
		!strings.Contains(err.Error(), "after Stop") {
		t.Fatalf("stressor remained updateable after fail-closed stop: %v", err)
	}
}
