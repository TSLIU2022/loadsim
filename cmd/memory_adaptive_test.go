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
		maxLoadMB:                300,
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
		{name: "negative allocation cap", change: func(c *adaptiveMemoryConfig) { c.maxLoadMB = -1 }},
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

func TestAutomaticMemoryMaximumUsesSmallestMidpointTarget(t *testing.T) {
	maximum, err := automaticMemoryMaximum(
		[]memoryCapacity{
			{totalMB: 1000, usedMB: 200, availableMB: 800, source: "host"},
			{totalMB: 800, usedMB: 100, availableMB: 700, source: "cgroup"},
		},
		percentBand{low: 65, high: 70},
		16,
		0,
	)
	if err != nil {
		t.Fatalf("select automatic maximum: %v", err)
	}
	if maximum != 440 {
		t.Fatalf("automatic maximum=%d want 440", maximum)
	}
}

func TestAutomaticMemoryMaximumRejectsUnsafeMidpointTarget(t *testing.T) {
	_, err := automaticMemoryMaximum(
		[]memoryCapacity{{
			totalMB:     1000,
			usedMB:      200,
			availableMB: 500,
			source:      "host",
		}},
		percentBand{low: 65, high: 70},
		16,
		0,
	)
	if err == nil || !strings.Contains(err.Error(), "startup budget") {
		t.Fatalf("unsafe automatic maximum error=%v", err)
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

func TestAdaptiveMemoryZeroYieldReleasesAllAboveHigh(t *testing.T) {
	config := testAdaptiveMemoryConfig()
	config.yieldPolicy = stress.YieldPolicyZero

	decision, err := nextAdaptiveMemoryTarget(
		config,
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
	if decision.action != adaptiveMemoryShrink || decision.targetMB != 0 {
		t.Fatalf("decision=%+v want zero-yield release", decision)
	}
}

func TestAdaptiveMemoryZeroYieldRequiresTwoLowSamplesBeforeGrowth(t *testing.T) {
	config := testAdaptiveMemoryConfig()
	config.yieldPolicy = stress.YieldPolicyZero
	overloaded := []memoryCapacity{{
		totalMB: 1000,
		usedMB:  600,
		source:  "host",
	}}
	released, err := nextAdaptiveMemoryTarget(config, overloaded, 300, 300, 2)
	if err != nil {
		t.Fatalf("overloaded decision: %v", err)
	}
	if released.targetMB != 0 || released.lowSamples != 0 {
		t.Fatalf("overloaded decision=%+v want zero release", released)
	}

	belowLow := []memoryCapacity{{
		totalMB: 1000,
		usedMB:  200,
		source:  "host",
	}}
	confirmed, err := nextAdaptiveMemoryTarget(
		config,
		belowLow,
		0,
		0,
		released.lowSamples,
	)
	if err != nil {
		t.Fatalf("first recovery decision: %v", err)
	}
	if confirmed.action != adaptiveMemoryConfirm || confirmed.targetMB != 0 {
		t.Fatalf("first recovery decision=%+v want confirmation", confirmed)
	}
	recovered, err := nextAdaptiveMemoryTarget(
		config,
		belowLow,
		0,
		0,
		confirmed.lowSamples,
	)
	if err != nil {
		t.Fatalf("second recovery decision: %v", err)
	}
	if recovered.action != adaptiveMemoryGrow || recovered.targetMB != 200 {
		t.Fatalf("second recovery decision=%+v want 200MB growth", recovered)
	}
}

func TestAdaptiveMemoryControllerZeroYieldReleasesImmediately(t *testing.T) {
	stressor, err := stress.NewRAMStressor(stress.RAMConfig{
		Mode:                     stress.ModeFixed,
		SizeMB:                   2,
		BlockMB:                  1,
		ControlInterval:          time.Millisecond,
		GrowthRateLimitMBPerSec:  100,
		ReleaseRateLimitMBPerSec: 100,
	})
	if err != nil {
		t.Fatalf("NewRAMStressor: %v", err)
	}
	t.Cleanup(func() {
		if err := stressor.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	if err := stressor.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitAdaptiveRAMCurrentMB(t, stressor, 2)

	config := testAdaptiveMemoryConfig()
	config.yieldPolicy = stress.YieldPolicyZero
	controller, err := newAdaptiveMemoryController(config, stressor, stressor.Stop)
	if err != nil {
		t.Fatalf("new controller: %v", err)
	}
	controller.probe = func() ([]memoryCapacity, error) {
		return []memoryCapacity{{
			totalMB: 100,
			usedMB:  60,
			source:  "host",
		}}, nil
	}

	if err := controller.adjust(); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	status := stressor.Status()
	if status.RequestedMB != 0 || status.TargetMB != 0 || status.CurrentMB != 0 {
		t.Fatalf(
			"requested/target/current=%d/%d/%d want 0/0/0",
			status.RequestedMB,
			status.TargetMB,
			status.CurrentMB,
		)
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
		growth.hardCapMB != 300 {
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
		Mode:                     stress.ModeFixed,
		SizeMB:                   1,
		GrowthRateLimitMBPerSec:  1,
		ReleaseRateLimitMBPerSec: 1,
	})
	if err != nil {
		t.Fatalf("NewRAMStressor: %v", err)
	}
	config := testAdaptiveMemoryConfig()
	config.maxLoadMB = 30
	controller, err := newAdaptiveMemoryController(
		config,
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
		Mode:                     stress.ModeFixed,
		SizeMB:                   1,
		BlockMB:                  1,
		ControlInterval:          time.Hour,
		GrowthRateLimitMBPerSec:  1,
		ReleaseRateLimitMBPerSec: 1,
	})
	if err != nil {
		t.Fatalf("NewRAMStressor: %v", err)
	}
	config := testAdaptiveMemoryConfig()
	config.maxLoadMB = 30
	controller, err := newAdaptiveMemoryController(
		config,
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
	if _, err := nextAdaptiveMemoryTarget(
		testAdaptiveMemoryConfig(),
		nil,
		0,
		0,
		0,
	); err == nil {
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
		Mode:                     stress.ModeFixed,
		SizeMB:                   1,
		BlockMB:                  1,
		ControlInterval:          time.Hour,
		GrowthRateLimitMBPerSec:  100,
		ReleaseRateLimitMBPerSec: 100,
	})
	if err != nil {
		t.Fatalf("NewRAMStressor: %v", err)
	}
	controller, err := newAdaptiveMemoryController(
		adaptiveMemoryConfig{
			lowPercent:               30,
			highPercent:              50,
			maxLoadMB:                30,
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
		Mode:                     stress.ModeFixed,
		SizeMB:                   1,
		GrowthRateLimitMBPerSec:  1,
		ReleaseRateLimitMBPerSec: 1,
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
				Mode:                     stress.ModeWave,
				MinSizeMB:                0,
				MaxSizeMB:                1,
				Period:                   time.Second,
				GrowthRateLimitMBPerSec:  1,
				ReleaseRateLimitMBPerSec: 1,
			},
			wantErr: "fixed-mode",
		},
		{
			name: "positive growth limit",
			config: stress.RAMConfig{
				Mode:                     stress.ModeFixed,
				SizeMB:                   1,
				ReleaseRateLimitMBPerSec: 1,
			},
			wantErr: "positive RAM growth rate limit",
		},
		{
			name: "positive release limit",
			config: stress.RAMConfig{
				Mode:                    stress.ModeFixed,
				SizeMB:                  1,
				GrowthRateLimitMBPerSec: 1,
			},
			wantErr: "positive RAM release rate limit",
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
		Mode:                     stress.ModeFixed,
		SizeMB:                   1,
		BlockMB:                  1,
		ControlInterval:          time.Hour,
		GrowthRateLimitMBPerSec:  1,
		ReleaseRateLimitMBPerSec: 1,
	})
	if err != nil {
		t.Fatalf("NewRAMStressor: %v", err)
	}
	config := testAdaptiveMemoryConfig()
	config.blockMB = 1
	config.interval = minimumAdaptiveMemoryInterval
	config.maxLoadMB = 30
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

func waitAdaptiveRAMCurrentMB(t *testing.T, stressor *stress.RAMStressor, want int) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := stressor.Status()
		if status.CurrentMB == want && status.TargetMB == want {
			return
		}
		select {
		case err, ok := <-stressor.Errors():
			if ok {
				t.Fatalf("RAM controller failed while waiting for %dMB: %v", want, err)
			}
			t.Fatalf("RAM controller stopped while waiting for %dMB", want)
		default:
		}
		time.Sleep(time.Millisecond)
	}
	status := stressor.Status()
	t.Fatalf(
		"timed out waiting for %dMB; requested/target/current=%d/%d/%d",
		want,
		status.RequestedMB,
		status.TargetMB,
		status.CurrentMB,
	)
}
