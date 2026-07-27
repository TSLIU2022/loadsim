package cmd

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/fanderchan/loadsim/internal/stress"

	"github.com/spf13/cobra"
)

func TestCheckedDurationValidation(t *testing.T) {
	if _, err := seconds(-1, "run time", true); err == nil {
		t.Fatal("negative duration must be rejected")
	}
	if _, err := seconds(0, "period", false); err == nil {
		t.Fatal("zero duration must be rejected when zero is not allowed")
	}
	if got, err := milliseconds(250, "control interval", false); err != nil || got != 250*time.Millisecond {
		t.Fatalf("milliseconds got=%v err=%v", got, err)
	}

	tooLarge := int(math.MaxInt64/int64(time.Second) + 1)
	if _, err := seconds(tooLarge, "run time", true); err == nil {
		t.Fatal("overflowing duration must be rejected")
	}
}

func TestCLIControllerTuningRejectsAmbiguousZeroValues(t *testing.T) {
	if err := validateCLIControllerTuning(0, 10); err == nil {
		t.Fatal("zero deadband must be rejected instead of silently using a default")
	}
	if err := validateCLIControllerTuning(1, 0); err == nil {
		t.Fatal("zero max step must be rejected instead of silently using a default")
	}
	if err := validateCLIControllerTuning(1, 10); err != nil {
		t.Fatalf("valid tuning rejected: %v", err)
	}
}

func TestValidateRAMCapacity(t *testing.T) {
	original := systemMemoryCapacities
	t.Cleanup(func() { systemMemoryCapacities = original })
	systemMemoryCapacities = func() ([]memoryCapacity, error) {
		return []memoryCapacity{{
			availableMB: 1000,
			totalMB:     1000,
			source:      "cgroup",
		}}, nil
	}

	if err := validateRAMCapacity(840, false); err != nil {
		t.Fatalf("safe target rejected: %v", err)
	}
	if err := validateRAMCapacity(841, false); err == nil || !strings.Contains(err.Error(), "cgroup") {
		t.Fatalf("unsafe target error=%v", err)
	}
	if err := validateRAMCapacity(10000, true); err != nil {
		t.Fatalf("force must bypass the safety budget: %v", err)
	}
}

func TestValidateRAMCapacityForceDoesNotBypassEmergencyThreshold(t *testing.T) {
	original := systemMemoryCapacities
	t.Cleanup(func() { systemMemoryCapacities = original })
	systemMemoryCapacities = func() ([]memoryCapacity, error) {
		return []memoryCapacity{{
			availableMB: 128,
			totalMB:     1000,
			source:      "cgroup",
		}}, nil
	}

	err := validateRAMCapacity(1, true)
	if err == nil || !strings.Contains(err.Error(), "safety threshold") {
		t.Fatalf("force bypassed emergency threshold: %v", err)
	}
}

func TestValidateRAMCapacityPropagatesProbeError(t *testing.T) {
	original := systemMemoryCapacities
	t.Cleanup(func() { systemMemoryCapacities = original })
	systemMemoryCapacities = func() ([]memoryCapacity, error) {
		return nil, errors.New("probe failed")
	}

	if err := validateRAMCapacity(1, false); err == nil || !strings.Contains(err.Error(), "probe failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWatchLoopReturnsRuntimeError(t *testing.T) {
	runtimeErrors := make(chan error, 1)
	runtimeErrors <- errors.New("runtime failed")

	reason, err := watchLoop(
		0,
		time.Hour,
		func() {},
		runtimeErrors,
		nil,
		nil,
		nil,
	)
	if reason != "" {
		t.Fatalf("reason=%q, want empty", reason)
	}
	if err == nil || err.Error() != "runtime failed" {
		t.Fatalf("error=%v", err)
	}
}

func TestWatchLoopReturnsSafetyError(t *testing.T) {
	safetyErrors := make(chan error, 1)
	safetyErrors <- errors.New("memory safety failed")

	reason, err := watchLoop(
		0,
		time.Hour,
		func() {},
		nil,
		nil,
		safetyErrors,
		nil,
	)
	if reason != "" {
		t.Fatalf("reason=%q, want empty", reason)
	}
	if err == nil || err.Error() != "memory safety failed" {
		t.Fatalf("error=%v", err)
	}
}

func TestMinimumAvailableMemoryMBAutomaticThreshold(t *testing.T) {
	tests := []struct {
		totalMB uint64
		wantMB  uint64
	}{
		{totalMB: 128, wantMB: 32},
		{totalMB: 512, wantMB: 128},
		{totalMB: 4096, wantMB: 410},
		{totalMB: 65536, wantMB: 6554},
	}
	for _, tt := range tests {
		if got := minimumAvailableMemoryMB(tt.totalMB, 0); got != tt.wantMB {
			t.Fatalf("total=%d threshold=%d want=%d", tt.totalMB, got, tt.wantMB)
		}
	}
	if got := minimumAvailableMemoryMB(65536, 64); got != 64 {
		t.Fatalf("explicit threshold=%d want=64", got)
	}
}

func TestStartupBudgetLeavesRuntimeGuardHeadroom(t *testing.T) {
	initial := memoryCapacity{
		availableMB: 1000,
		totalMB:     1000,
		source:      "cgroup",
	}
	if err := validateRAMCapacitySnapshot(initial, 840, 16, 0, false); err != nil {
		t.Fatalf("safe startup target rejected: %v", err)
	}

	original := systemMemoryCapacities
	t.Cleanup(func() { systemMemoryCapacities = original })
	systemMemoryCapacities = func() ([]memoryCapacity, error) {
		return []memoryCapacity{{
			availableMB: 160,
			totalMB:     1000,
			source:      "cgroup",
		}}, nil
	}
	guard := &memoryGuard{}
	if err := guard.check(); err != nil {
		t.Fatalf("normal target consumption triggered runtime guard: %v", err)
	}
}

func TestMemorySafetyChecksHostAndCgroupAutomaticThresholdsIndependently(t *testing.T) {
	capacities := []memoryCapacity{
		{
			availableMB: 8000,
			totalMB:     128 * 1024,
			source:      "host",
		},
		{
			availableMB: 7000,
			totalMB:     8 * 1024,
			source:      "cgroup",
		},
	}

	err := validateRAMCapacitySnapshots(capacities, 1, 16, 0, false)
	if err == nil || !strings.Contains(err.Error(), "host") ||
		!strings.Contains(err.Error(), "safety threshold") {
		t.Fatalf("low-percentage host capacity was ignored: %v", err)
	}
}

func TestStartupBudgetChecksEveryAutomaticConstraint(t *testing.T) {
	tests := []struct {
		name         string
		capacities   []memoryCapacity
		rejectedFrom string
	}{
		{
			name: "host budget tighter despite more absolute availability",
			capacities: []memoryCapacity{
				{
					availableMB: 20 * 1024,
					totalMB:     128 * 1024,
					source:      "host",
				},
				{
					availableMB: 7 * 1024,
					totalMB:     8 * 1024,
					source:      "cgroup",
				},
			},
			rejectedFrom: "host",
		},
		{
			name: "ancestor budget tighter despite more absolute availability",
			capacities: []memoryCapacity{
				{
					availableMB: 20 * 1024,
					totalMB:     128 * 1024,
					source:      "cgroup-ancestor",
				},
				{
					availableMB: 7 * 1024,
					totalMB:     8 * 1024,
					source:      "cgroup-child",
				},
			},
			rejectedFrom: "cgroup-ancestor",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRAMCapacitySnapshots(
				tt.capacities,
				5*1024,
				16,
				0,
				false,
			)
			if err == nil || !strings.Contains(err.Error(), tt.rejectedFrom) {
				t.Fatalf("tighter startup budget was ignored: %v", err)
			}
		})
	}
}

func TestExplicitMemoryThresholdChecksEveryConstraint(t *testing.T) {
	capacities := []memoryCapacity{
		{
			availableMB: 900,
			totalMB:     1000,
			source:      "first",
		},
		{
			availableMB: 500,
			totalMB:     10000,
			source:      "second",
		},
	}

	err := validateRAMCapacitySnapshots(capacities, 1, 16, 600, true)
	if err == nil || !strings.Contains(err.Error(), "second") {
		t.Fatalf("explicit threshold did not check the later constraint: %v", err)
	}
}

func TestSelectMemorySafetySnapshotUsesNearestRuntimeBoundary(t *testing.T) {
	snapshot, err := selectMemorySafetySnapshot(
		[]memoryCapacity{
			{
				availableMB: 8000,
				totalMB:     128 * 1024,
				source:      "host",
			},
			{
				availableMB: 7000,
				totalMB:     8 * 1024,
				source:      "cgroup",
			},
		},
		0,
	)
	if err != nil {
		t.Fatalf("select snapshot: %v", err)
	}
	if snapshot.source != "host" {
		t.Fatalf("source=%q want host", snapshot.source)
	}
	if snapshot.minimumAvailableMB != 13108 {
		t.Fatalf(
			"minimum=%d want 13108",
			snapshot.minimumAvailableMB,
		)
	}
}

func TestSelectMemorySafetySnapshotUsesSmallestSafeHeadroom(t *testing.T) {
	snapshot, err := selectMemorySafetySnapshot(
		[]memoryCapacity{
			{
				availableMB: 1000,
				totalMB:     4000,
				source:      "host",
			},
			{
				availableMB: 600,
				totalMB:     1000,
				source:      "cgroup",
			},
		},
		0,
	)
	if err != nil {
		t.Fatalf("select snapshot: %v", err)
	}
	if snapshot.source != "cgroup" ||
		snapshot.availableMB != 600 ||
		snapshot.minimumAvailableMB != 128 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

func TestSelectMemorySafetySnapshotRejectsEmptyProbe(t *testing.T) {
	if _, err := selectMemorySafetySnapshot(nil, 0); err == nil {
		t.Fatal("empty memory safety probe must be rejected")
	}
}

func TestMemoryGuardStartRevalidatesCompleteStartupBudget(t *testing.T) {
	original := systemMemoryCapacities
	t.Cleanup(func() { systemMemoryCapacities = original })

	call := 0
	systemMemoryCapacities = func() ([]memoryCapacity, error) {
		call++
		availableMB := uint64(1000)
		if call > 1 {
			availableMB = 500
		}
		return []memoryCapacity{{
			availableMB: availableMB,
			totalMB:     1000,
			source:      "cgroup",
		}}, nil
	}

	emergencyStopped := false
	guard, err := newMemoryGuard(
		400,
		16,
		100,
		minimumMemoryCheckInterval,
		false,
		func() error {
			emergencyStopped = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("newMemoryGuard: %v", err)
	}
	t.Cleanup(guard.Stop)

	err = guard.Start()
	if err == nil || !strings.Contains(err.Error(), "startup budget") {
		t.Fatalf("Start accepted a stale startup budget: %v", err)
	}
	if emergencyStopped {
		t.Fatal("failed Start invoked emergency cleanup before the guard ran")
	}
}

func TestMemoryGuardDoesNotStopStressorBeforeGuardStart(t *testing.T) {
	original := systemMemoryCapacities
	t.Cleanup(func() { systemMemoryCapacities = original })

	call := 0
	systemMemoryCapacities = func() ([]memoryCapacity, error) {
		call++
		if call == 1 {
			return []memoryCapacity{{
				availableMB: 500,
				totalMB:     1000,
				source:      "cgroup",
			}}, nil
		}
		return []memoryCapacity{{
			availableMB: 128,
			totalMB:     1000,
			source:      "cgroup",
		}}, nil
	}

	emergencyStopped := false
	guard, err := newMemoryGuard(
		100,
		16,
		128,
		minimumMemoryCheckInterval,
		false,
		func() error {
			emergencyStopped = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("newMemoryGuard: %v", err)
	}
	if err := guard.preflight(); err == nil {
		t.Fatal("second preflight accepted a crossed threshold")
	}
	if emergencyStopped {
		t.Fatal("unstarted guard called emergency Stop")
	}
	guard.Stop()
	if _, ok := <-guard.Errors(); ok {
		t.Fatal("Stop before Start did not close the error channel")
	}
}

func TestMemoryGuardTriggersWhenAvailableMemoryCrossesThreshold(t *testing.T) {
	original := systemMemoryCapacities
	t.Cleanup(func() { systemMemoryCapacities = original })

	call := 0
	systemMemoryCapacities = func() ([]memoryCapacity, error) {
		call++
		if call <= 2 {
			return []memoryCapacity{{
				availableMB: 500,
				totalMB:     1000,
				source:      "cgroup",
			}}, nil
		}
		return []memoryCapacity{{
			availableMB: 128,
			totalMB:     1000,
			source:      "cgroup",
		}}, nil
	}

	emergencyStopped := false
	guard, err := newMemoryGuard(
		100,
		16,
		128,
		minimumMemoryCheckInterval,
		false,
		func() error {
			emergencyStopped = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("newMemoryGuard: %v", err)
	}
	t.Cleanup(guard.Stop)
	if err := guard.Start(); err != nil {
		t.Fatalf("guard Start: %v", err)
	}

	select {
	case guardErr := <-guard.Errors():
		if guardErr == nil || !strings.Contains(guardErr.Error(), "cgroup") {
			t.Fatalf("guard error=%v", guardErr)
		}
		if !emergencyStopped {
			t.Fatal("guard reported failure before emergency cleanup completed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for memory guard")
	}
}

func TestMemoryGuardFailsClosedOnProbeError(t *testing.T) {
	original := systemMemoryCapacities
	t.Cleanup(func() { systemMemoryCapacities = original })

	call := 0
	systemMemoryCapacities = func() ([]memoryCapacity, error) {
		call++
		if call <= 2 {
			return []memoryCapacity{{
				availableMB: 500,
				totalMB:     1000,
				source:      "host",
			}}, nil
		}
		return nil, errors.New("probe failed")
	}

	guard, err := newMemoryGuard(
		100,
		16,
		128,
		minimumMemoryCheckInterval,
		false,
		nil,
	)
	if err != nil {
		t.Fatalf("newMemoryGuard: %v", err)
	}
	t.Cleanup(guard.Stop)
	if err := guard.Start(); err != nil {
		t.Fatalf("guard Start: %v", err)
	}

	select {
	case guardErr := <-guard.Errors():
		if guardErr == nil || !strings.Contains(guardErr.Error(), "probe failed") {
			t.Fatalf("guard error=%v", guardErr)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for memory guard")
	}
}

func TestMemoryGuardRejectsOverlyAggressiveCheckInterval(t *testing.T) {
	original := systemMemoryCapacities
	t.Cleanup(func() { systemMemoryCapacities = original })
	systemMemoryCapacities = func() ([]memoryCapacity, error) {
		return []memoryCapacity{{
			availableMB: 500,
			totalMB:     1000,
			source:      "host",
		}}, nil
	}

	if _, err := newMemoryGuard(
		100,
		16,
		128,
		minimumMemoryCheckInterval-time.Millisecond,
		false,
		nil,
	); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("overly aggressive check interval error=%v", err)
	}
}

func TestStopRAMBeforeCPUReleasesMemoryFirst(t *testing.T) {
	ramReleased := false
	var order []string

	ramErr, cpuErr := stopRAMBeforeCPU(
		func() error {
			order = append(order, "ram")
			ramReleased = true
			return errors.New("RAM stop failed")
		},
		func() error {
			if !ramReleased {
				t.Fatal("CPU stop began before RAM was released")
			}
			order = append(order, "cpu")
			return errors.New("CPU stop failed")
		},
	)
	if strings.Join(order, ",") != "ram,cpu" {
		t.Fatalf("stop order=%v want [ram cpu]", order)
	}
	if ramErr == nil || cpuErr == nil {
		t.Fatalf("stop errors=%v/%v want both preserved", ramErr, cpuErr)
	}
}

func TestStopRAMBeforeCPUDoesNotDelayRAMForSlowCPUStop(t *testing.T) {
	ramStopped := make(chan struct{})
	allowCPUStop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		_, _ = stopRAMBeforeCPU(
			func() error {
				close(ramStopped)
				return nil
			},
			func() error {
				<-allowCPUStop
				return nil
			},
		)
	}()

	select {
	case <-ramStopped:
	case <-time.After(time.Second):
		t.Fatal("RAM stop was delayed by CPU stop")
	}
	select {
	case <-done:
		t.Fatal("cleanup completed before the simulated CPU stop was released")
	default:
	}

	close(allowCPUStop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not finish after CPU stop was released")
	}
}

func TestValidateOOMScoreAdj(t *testing.T) {
	for _, value := range []int{-1, 0, 500, 1000} {
		if err := validateOOMScoreAdj(value); err != nil {
			t.Fatalf("value %d rejected: %v", value, err)
		}
	}
	for _, value := range []int{-2, 1001} {
		if err := validateOOMScoreAdj(value); err == nil {
			t.Fatalf("value %d accepted", value)
		}
	}
}

func TestApplyOOMScoreAdjSupportsExplicitInheritance(t *testing.T) {
	original := setOOMScoreAdjustment
	t.Cleanup(func() { setOOMScoreAdjustment = original })

	var applied []int
	setOOMScoreAdjustment = func(value int) error {
		applied = append(applied, value)
		return nil
	}

	if err := applyOOMScoreAdj(-1); err != nil {
		t.Fatalf("inherit OOM score adjustment: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("inherit mode unexpectedly wrote values: %v", applied)
	}
	if err := applyOOMScoreAdj(1000); err != nil {
		t.Fatalf("apply OOM score adjustment: %v", err)
	}
	if len(applied) != 1 || applied[0] != 1000 {
		t.Fatalf("applied values=%v want [1000]", applied)
	}
}

func TestDrainClosedErrorsPreservesPendingRuntimeFailure(t *testing.T) {
	runtimeErrors := make(chan error, 1)
	runtimeErrors <- errors.New("runtime failed at shutdown")
	close(runtimeErrors)

	err := drainClosedErrors(runtimeErrors, nil)
	if err == nil || !strings.Contains(err.Error(), "runtime failed at shutdown") {
		t.Fatalf("pending error was lost: %v", err)
	}
}

func TestCommandsRejectPositionalArguments(t *testing.T) {
	for _, command := range []*cobra.Command{
		fillCmd,
		stressCmd,
		checkCmd,
		versionCmd,
	} {
		if err := command.Args(command, []string{"unexpected"}); err == nil {
			t.Fatalf("%s accepted a positional argument", command.Name())
		}
	}
}

func TestSafeDefaultsAreBounded(t *testing.T) {
	if fillConfig.durationSec <= 0 || stressConfig.durationSec <= 0 {
		t.Fatalf(
			"run time defaults must be bounded: fill=%d stress=%d",
			fillConfig.durationSec,
			stressConfig.durationSec,
		)
	}
	if fillConfig.memoryMaxMiB != 0 || fillConfig.memoryMaxGiB != 0 {
		t.Fatalf(
			"fill memory maximum must be explicit: mib=%d gib=%d",
			fillConfig.memoryMaxMiB,
			fillConfig.memoryMaxGiB,
		)
	}
	if fillConfig.memoryCheckMS != 100 ||
		stressConfig.memoryCheckMS != 100 {
		t.Fatalf(
			"memory check defaults must be 100ms: fill=%d stress=%d",
			fillConfig.memoryCheckMS,
			stressConfig.memoryCheckMS,
		)
	}
	if fillConfig.memoryMinAvailableMiB != 0 ||
		stressConfig.memoryMinAvailableMiB != 0 {
		t.Fatalf(
			"memory threshold defaults must be automatic: fill=%d stress=%d",
			fillConfig.memoryMinAvailableMiB,
			stressConfig.memoryMinAvailableMiB,
		)
	}
	if fillConfig.oomScoreAdj != 1000 ||
		stressConfig.oomScoreAdj != 1000 {
		t.Fatalf(
			"OOM score defaults must prefer LoadSim: fill=%d stress=%d",
			fillConfig.oomScoreAdj,
			stressConfig.oomScoreAdj,
		)
	}
	if fillConfig.cpuScheduler != "idle" ||
		stressConfig.cpuScheduler != "normal" {
		t.Fatalf(
			"unexpected scheduler defaults: fill=%q stress=%q",
			fillConfig.cpuScheduler,
			stressConfig.cpuScheduler,
		)
	}
	if fillConfig.memoryGrowMiBPerSec <= 0 ||
		fillConfig.memoryReleaseMiBPerSec <= 0 {
		t.Fatal("fill memory growth and release defaults must be rate limited")
	}
}

func TestRAMCommandsExposeRuntimeMemorySafetyFlags(t *testing.T) {
	for _, command := range []*cobra.Command{fillCmd, stressCmd} {
		for _, name := range []string{
			"memory-min-available-mib",
			"memory-check-ms",
			"oom-score-adj",
		} {
			if command.Flags().Lookup(name) == nil {
				t.Fatalf("%s is missing --%s", command.Name(), name)
			}
		}
	}
}

func TestCPUCommandsExposeWorkerNiceFlags(t *testing.T) {
	for _, command := range []*cobra.Command{fillCmd, stressCmd} {
		if command.Flags().Lookup("cpu-scheduler") == nil {
			t.Fatalf("%s is missing --cpu-scheduler", command.Name())
		}
		if command.Flags().Lookup("cpu-nice") == nil {
			t.Fatalf("%s is missing --cpu-nice", command.Name())
		}
	}
}

func TestParseWorkerNice(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{input: "0", want: 0},
		{input: "19", want: 19},
		{input: " inherit ", want: stress.WorkerNiceInherit},
		{input: "INHERIT", want: stress.WorkerNiceInherit},
	}
	for _, tt := range tests {
		value, err := parseWorkerNice(tt.input)
		if err != nil {
			t.Fatalf("parseWorkerNice(%q): %v", tt.input, err)
		}
		if value == nil || *value != tt.want {
			t.Fatalf("parseWorkerNice(%q)=%v want=%d", tt.input, value, tt.want)
		}
	}

	for _, input := range []string{"-1", "20", "1.5", "", "nice"} {
		if _, err := parseWorkerNice(input); err == nil {
			t.Fatalf("parseWorkerNice(%q) unexpectedly succeeded", input)
		}
	}
}
