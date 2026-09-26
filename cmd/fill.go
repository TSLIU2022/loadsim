package cmd

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fanderchan/loadsim/internal/stress"
	"github.com/fanderchan/loadsim/internal/system"

	"github.com/spf13/cobra"
)

type fillOptions struct {
	cpuBand                string
	memoryBand             string
	memoryMaxMiB           string
	memoryMaxGiB           int
	cpuCores               int
	cpuControlMS           int
	cpuSampleMS            int
	cpuMaxStep             float64
	cpuScheduler           string
	cpuNice                string
	yieldPolicy            string
	memoryControlMS        int
	memoryGrowMiBPerSec    int
	memoryReleaseMiBPerSec int
	memoryBlockMiB         int
	memoryCheckMS          int
	memoryMinAvailableMiB  int
	oomScoreAdj            int
	durationSec            int
	statusIntervalSec      int
}

var fillConfig fillOptions

var fillCmd = newFillCommand(&fillConfig)

func newFillCommand(options *fillOptions) *cobra.Command {
	command := &cobra.Command{
		Use:   "fill",
		Short: "Keep total resource use inside closed target bands",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runFill(command, *options)
		},
	}

	flags := command.Flags()
	flags.StringVar(
		&options.cpuBand,
		"cpu",
		"",
		"total CPU utilization band as LOW:HIGH percent",
	)
	flags.StringVar(
		&options.memoryBand,
		"memory",
		"",
		"total memory utilization band as LOW:HIGH percent",
	)
	flags.StringVar(
		&options.memoryMaxMiB,
		"memory-max-mib",
		"",
		"maximum memory allocated by LoadSim in MiB, or auto",
	)
	flags.IntVar(
		&options.memoryMaxGiB,
		"memory-max-gib",
		0,
		"maximum memory allocated by LoadSim in GiB",
	)
	flags.IntVar(
		&options.cpuCores,
		"cpu-cores",
		0,
		"CPU worker count; 0 uses the process scheduling capacity",
	)
	flags.IntVar(
		&options.cpuControlMS,
		"cpu-control-ms",
		3000,
		"CPU controller interval in milliseconds",
	)
	flags.IntVar(
		&options.cpuSampleMS,
		"cpu-sample-ms",
		1000,
		"CPU accounting sample duration in milliseconds",
	)
	flags.Float64Var(
		&options.cpuMaxStep,
		"cpu-max-step",
		5,
		"maximum CPU drive adjustment per control step in percent",
	)
	flags.StringVar(
		&options.cpuScheduler,
		"cpu-scheduler",
		"idle",
		"CPU worker scheduler: idle or normal",
	)
	flags.StringVar(
		&options.cpuNice,
		"cpu-nice",
		"19",
		"nice value for the explicit normal scheduler, 0 to 19 or inherit",
	)
	flags.StringVar(
		&options.yieldPolicy,
		"yield-policy",
		"gradual",
		"resource withdrawal above HIGH: gradual or zero",
	)
	flags.IntVar(
		&options.memoryControlMS,
		"memory-control-ms",
		3000,
		"memory band controller interval in milliseconds",
	)
	flags.IntVar(
		&options.memoryGrowMiBPerSec,
		"memory-grow-mib-per-sec",
		64,
		"maximum normal memory growth rate in MiB per second",
	)
	flags.IntVar(
		&options.memoryReleaseMiBPerSec,
		"memory-release-mib-per-sec",
		256,
		"maximum normal memory release rate in MiB per second",
	)
	flags.IntVar(
		&options.memoryBlockMiB,
		"memory-block-mib",
		16,
		"memory allocation block size in MiB",
	)
	flags.IntVar(
		&options.memoryCheckMS,
		"memory-check-ms",
		int(defaultMemoryCheckInterval/time.Millisecond),
		"emergency memory safety check interval in milliseconds",
	)
	flags.IntVar(
		&options.memoryMinAvailableMiB,
		"memory-min-available-mib",
		0,
		"minimum available memory in MiB; 0 selects the automatic threshold",
	)
	flags.IntVar(
		&options.oomScoreAdj,
		"oom-score-adj",
		1000,
		"Linux OOM score adjustment from 0 to 1000; -1 inherits",
	)
	flags.IntVar(
		&options.durationSec,
		"duration-sec",
		60,
		"run duration in seconds; 0 means no limit",
	)
	flags.IntVar(
		&options.statusIntervalSec,
		"status-interval-sec",
		5,
		"status output interval in seconds",
	)
	return command
}

func init() {
	rootCmd.AddCommand(fillCmd)
}

// fillRunSummary aggregates status samples so a normal run can end with one
// human-readable summary line describing how well the band targets were met.
type fillRunSummary struct {
	lock sync.Mutex

	cpuSamples   int
	cpuInBand    int
	cpuHasBand   bool
	cpuBandLow   float64
	cpuBandHigh  float64
	cpuHasSample bool
	cpuMin       float64
	cpuMax       float64
	cpuSum       float64

	memorySamples   int
	memoryInBand    int
	memoryHasBand   bool
	memoryBandLow   float64
	memoryBandHigh  float64
	memoryHasSample bool
	memoryMin       float64
	memoryMax       float64
	memorySum       float64

	rssMaxMB      uint64
	availableMin  uint64
	availableSeen bool
}

func (s *fillRunSummary) observeCPU(status stress.CPUStatus) {
	if !status.HasScopeSample {
		return
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	if !s.cpuHasBand {
		s.cpuBandLow = status.RequestedBandLowPercent
		s.cpuBandHigh = status.RequestedBandHighPercent
		s.cpuHasBand = true
	}
	if !s.cpuHasSample {
		s.cpuMin = status.LastScopePercent
		s.cpuHasSample = true
	}
	if status.LastScopePercent < s.cpuMin {
		s.cpuMin = status.LastScopePercent
	}
	if status.LastScopePercent > s.cpuMax {
		s.cpuMax = status.LastScopePercent
	}
	s.cpuSum += status.LastScopePercent
	s.cpuSamples++
	if status.LastScopePercent >= s.cpuBandLow &&
		status.LastScopePercent <= s.cpuBandHigh {
		s.cpuInBand++
	}
}

func (s *fillRunSummary) observeMemory(status stress.RAMStatus, adaptive adaptiveMemoryStatus) {
	// 释放阶段（requested=0）的观测值不代表填充状态，不计入达标统计。
	if !adaptive.hasSample || status.RequestedMB <= 0 {
		return
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	if !s.memoryHasBand {
		s.memoryBandLow = adaptive.lowPercent
		s.memoryBandHigh = adaptive.highPercent
		s.memoryHasBand = true
	}
	if !s.memoryHasSample {
		s.memoryMin = adaptive.observed
		s.memoryHasSample = true
	}
	if adaptive.observed < s.memoryMin {
		s.memoryMin = adaptive.observed
	}
	if adaptive.observed > s.memoryMax {
		s.memoryMax = adaptive.observed
	}
	s.memorySum += adaptive.observed
	s.memorySamples++
	if adaptive.observed >= s.memoryBandLow &&
		adaptive.observed <= s.memoryBandHigh {
		s.memoryInBand++
	}
}

func (s *fillRunSummary) observeRSS(rssMB uint64) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if rssMB > s.rssMaxMB {
		s.rssMaxMB = rssMB
	}
}

func (s *fillRunSummary) observeAvailable(availableMB uint64) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if !s.availableSeen || availableMB < s.availableMin {
		s.availableMin = availableMB
		s.availableSeen = true
	}
}

func (s *fillRunSummary) String() string {
	s.lock.Lock()
	defer s.lock.Unlock()

	fields := []string{fmt.Sprintf("samples=%d", maxInt(s.cpuSamples, s.memorySamples))}
	if s.cpuHasBand {
		fields = append(
			fields,
			fmt.Sprintf("cpu_band=%.1f:%.1f%%", s.cpuBandLow, s.cpuBandHigh),
			fmt.Sprintf("cpu_in_band=%d/%d", s.cpuInBand, s.cpuSamples),
			fmt.Sprintf("cpu_observed_min=%.1f%%", s.cpuMin),
			fmt.Sprintf("cpu_observed_avg=%.1f%%", s.cpuSum/float64(s.cpuSamples)),
			fmt.Sprintf("cpu_observed_max=%.1f%%", s.cpuMax),
		)
	}
	if s.memoryHasBand {
		fields = append(
			fields,
			fmt.Sprintf("memory_band=%.1f:%.1f%%", s.memoryBandLow, s.memoryBandHigh),
			fmt.Sprintf("memory_in_band=%d/%d", s.memoryInBand, s.memorySamples),
			fmt.Sprintf("memory_observed_min=%.1f%%", s.memoryMin),
			fmt.Sprintf("memory_observed_avg=%.1f%%", s.memorySum/float64(s.memorySamples)),
			fmt.Sprintf("memory_observed_max=%.1f%%", s.memoryMax),
		)
	}
	if s.rssMaxMB > 0 {
		fields = append(fields, fmt.Sprintf("process_rss_max=%dMiB", s.rssMaxMB))
	}
	if s.availableSeen {
		fields = append(fields, fmt.Sprintf("memory_available_min=%dMiB", s.availableMin))
	}
	return "summary: " + strings.Join(fields, " ")
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func runFill(command *cobra.Command, options fillOptions) error {
	hasCPU := strings.TrimSpace(options.cpuBand) != ""
	hasMemory := strings.TrimSpace(options.memoryBand) != ""
	if !hasCPU && !hasMemory {
		return fmt.Errorf("fill requires --cpu, --memory, or both")
	}
	if !hasCPU && anyFlagChanged(
		command,
		"cpu-cores",
		"cpu-control-ms",
		"cpu-sample-ms",
		"cpu-max-step",
		"cpu-scheduler",
		"cpu-nice",
	) {
		return fmt.Errorf("CPU tuning flags require --cpu")
	}
	if !hasMemory && anyFlagChanged(
		command,
		"memory-max-mib",
		"memory-max-gib",
		"memory-control-ms",
		"memory-grow-mib-per-sec",
		"memory-release-mib-per-sec",
		"memory-block-mib",
		"memory-check-ms",
		"memory-min-available-mib",
		"oom-score-adj",
	) {
		return fmt.Errorf("memory tuning flags require --memory")
	}

	runDuration, err := seconds(options.durationSec, "run duration", true)
	if err != nil {
		return err
	}
	statusInterval, err := seconds(
		options.statusIntervalSec,
		"status interval",
		false,
	)
	if err != nil {
		return err
	}

	yieldPolicy, err := parseYieldPolicy(options.yieldPolicy)
	if err != nil {
		return err
	}

	var cpuStressor *stress.CPUStressor
	if hasCPU {
		cpuBand, err := parsePercentBand(options.cpuBand, "CPU band", 100, 1)
		if err != nil {
			return err
		}
		controlInterval, err := milliseconds(
			options.cpuControlMS,
			"CPU control interval",
			false,
		)
		if err != nil {
			return err
		}
		sampleDuration, err := milliseconds(
			options.cpuSampleMS,
			"CPU sample duration",
			false,
		)
		if err != nil {
			return err
		}
		if err := validateCLIControllerTuning(
			(cpuBand.high-cpuBand.low)/2,
			options.cpuMaxStep,
		); err != nil {
			return err
		}
		scheduler, err := parseWorkerScheduler(options.cpuScheduler)
		if err != nil {
			return err
		}
		if scheduler == stress.WorkerSchedulerIdle &&
			command.Flags().Changed("cpu-nice") {
			return fmt.Errorf(
				"--cpu-nice applies only with --cpu-scheduler normal",
			)
		}
		workerNice, err := parseWorkerNice(options.cpuNice)
		if err != nil {
			return err
		}
		if scheduler == stress.WorkerSchedulerIdle {
			inherit := stress.WorkerNiceInherit
			workerNice = &inherit
		}

		cpuStressor, err = stress.NewCPUStressor(stress.CPUConfig{
			Mode:            stress.ModeFixed,
			Scope:           stress.ScopeSystem,
			IdleMode:        stress.IdleModePark,
			YieldPolicy:     yieldPolicy,
			Percent:         (cpuBand.low + cpuBand.high) / 2,
			Cores:           options.cpuCores,
			ControlInterval: controlInterval,
			SampleDuration:  sampleDuration,
			DeadbandPercent: (cpuBand.high - cpuBand.low) / 2,
			MaxStepPercent:  options.cpuMaxStep,
			WorkerScheduler: scheduler,
			WorkerNice:      workerNice,
		})
		if err != nil {
			return err
		}
	}

	var (
		ramStressor      *stress.RAMStressor
		memoryController *adaptiveMemoryController
		guard            *memoryGuard
	)
	if hasMemory {
		memoryBand, err := parsePercentBand(
			options.memoryBand,
			"memory band",
			maximumAdaptiveMemoryPercent,
			minimumAdaptiveMemoryBandWidth,
		)
		if err != nil {
			return err
		}
		maxMemoryMiB, automaticMaximum, err := parseMemoryMaximum(
			options.memoryMaxMiB,
			options.memoryMaxGiB,
		)
		if err != nil {
			return err
		}
		controllerInterval, err := milliseconds(
			options.memoryControlMS,
			"memory control interval",
			false,
		)
		if err != nil {
			return err
		}
		memoryCheckInterval, err := milliseconds(
			options.memoryCheckMS,
			"memory check interval",
			false,
		)
		if err != nil {
			return err
		}
		if options.memoryMinAvailableMiB < 0 {
			return fmt.Errorf("minimum available memory must not be negative")
		}
		if err := validateOOMScoreAdj(options.oomScoreAdj); err != nil {
			return err
		}
		if automaticMaximum {
			capacities, err := systemMemoryCapacities()
			if err != nil {
				return fmt.Errorf("probe automatic RAM maximum: %w", err)
			}
			maxMemoryMiB, err = automaticMemoryMaximum(
				capacities,
				memoryBand,
				options.memoryBlockMiB,
				uint64(options.memoryMinAvailableMiB),
			)
			if err != nil {
				return err
			}
		}

		ramStressor, err = stress.NewRAMStressor(stress.RAMConfig{
			Mode:                     stress.ModeFixed,
			SizeMB:                   1,
			BlockMB:                  options.memoryBlockMiB,
			ControlInterval:          100 * time.Millisecond,
			GrowthRateLimitMBPerSec:  options.memoryGrowMiBPerSec,
			ReleaseRateLimitMBPerSec: options.memoryReleaseMiBPerSec,
		})
		if err != nil {
			return err
		}

		emergencyStop := func() error {
			return stopResourcesImmediately(ramStressor, cpuStressor)
		}
		memoryController, err = newAdaptiveMemoryController(
			adaptiveMemoryConfig{
				lowPercent:               memoryBand.low,
				highPercent:              memoryBand.high,
				maxLoadMB:                maxMemoryMiB,
				interval:                 controllerInterval,
				blockMB:                  options.memoryBlockMiB,
				configuredMinAvailableMB: uint64(options.memoryMinAvailableMiB),
				yieldPolicy:              yieldPolicy,
			},
			ramStressor,
			emergencyStop,
		)
		if err != nil {
			return err
		}
		if _, err := memoryController.Preflight(); err != nil {
			memoryController.Stop()
			_ = stopResourcesImmediately(ramStressor, cpuStressor)
			return err
		}
		guard, err = newMemoryGuard(
			maxMemoryMiB,
			options.memoryBlockMiB,
			options.memoryMinAvailableMiB,
			memoryCheckInterval,
			false,
			emergencyStop,
		)
		if err != nil {
			memoryController.Stop()
			_ = stopResourcesImmediately(ramStressor, cpuStressor)
			return err
		}
		if err := applyOOMScoreAdj(options.oomScoreAdj); err != nil {
			memoryController.Stop()
			guard.Stop()
			_ = stopResourcesImmediately(ramStressor, cpuStressor)
			return err
		}
	}

	cleanupStartFailure := func(startErr error) error {
		stopAdaptiveMemoryController(memoryController)
		stopErr := stopResourcesImmediately(ramStressor, cpuStressor)
		if guard != nil {
			guard.Stop()
		}
		return joinErrors(
			startErr,
			stopErr,
			drainClosedErrors(
				stressorErrors(cpuStressor),
				ramStressorErrors(ramStressor),
				memoryGuardErrors(guard),
				adaptiveMemoryErrors(memoryController),
			),
		)
	}

	if guard != nil {
		if err := guard.Start(); err != nil {
			return cleanupStartFailure(err)
		}
	}
	if ramStressor != nil {
		if err := ramStressor.Start(); err != nil {
			return cleanupStartFailure(err)
		}
	}
	if memoryController != nil {
		if err := memoryController.Start(); err != nil {
			return cleanupStartFailure(err)
		}
	}
	if cpuStressor != nil {
		if err := cpuStressor.Start(); err != nil {
			return cleanupStartFailure(err)
		}
	}

	var initialCPUFields []string
	if cpuStressor != nil {
		cpuCgroupInfo, cpuCgroupErr := system.InspectCPUCgroup()
		initialCPUFields = formatCPUCgroupStatusFields(
			cpuCgroupInfo,
			cpuCgroupErr,
		)
	}
	firstStatus := true
	summary := &fillRunSummary{}
	printStatus := func() {
		var diagnosticFields []string
		if firstStatus {
			diagnosticFields = initialCPUFields
			firstStatus = false
		}
		printFillStatus(
			cpuStressor,
			ramStressor,
			guard,
			memoryController,
			options.oomScoreAdj,
			diagnosticFields,
			summary,
		)
	}
	reason, runErr := watchLoop(
		runDuration,
		statusInterval,
		printStatus,
		stressorErrors(cpuStressor),
		ramStressorErrors(ramStressor),
		memoryGuardErrors(guard),
		adaptiveMemoryErrors(memoryController),
	)

	stopAdaptiveMemoryController(memoryController)
	var stopErr error
	if runErr != nil {
		stopErr = stopResourcesImmediately(ramStressor, cpuStressor)
	} else {
		if cpuStressor != nil {
			stopErr = cpuStressor.Stop()
		}
		if ramStressor != nil && ramStressor.Status().CurrentMB > 0 {
			fmt.Printf(
				"stopping: releasing RAM at up to %dMiB/s\n",
				ramStressor.Status().ReleaseRateLimitMB,
			)
		}
		releaseErr := releaseRAMGradually(
			ramStressor,
			statusInterval,
			func() {
				printFillStatus(
					nil,
					ramStressor,
					guard,
					memoryController,
					options.oomScoreAdj,
					nil,
					summary,
				)
			},
			ramStressorErrors(ramStressor),
			memoryGuardErrors(guard),
		)
		if releaseErr != nil {
			runErr = releaseErr
			stopErr = joinErrors(
				stopErr,
				stopResourcesImmediately(ramStressor, cpuStressor),
			)
		} else if ramStressor != nil {
			stopErr = joinErrors(stopErr, ramStressor.Stop())
		}
	}
	if guard != nil {
		guard.Stop()
	}
	pendingErr := drainClosedErrors(
		stressorErrors(cpuStressor),
		ramStressorErrors(ramStressor),
		memoryGuardErrors(guard),
		adaptiveMemoryErrors(memoryController),
	)
	if err := joinErrors(runErr, stopErr, pendingErr); err != nil {
		return err
	}

	if line := summary.String(); line != "summary: samples=0" {
		fmt.Println(line)
	}
	fmt.Printf("stopped: %s\n", reason)
	return nil
}

func stressorErrors(stressor *stress.CPUStressor) <-chan error {
	if stressor == nil {
		return nil
	}
	return stressor.Errors()
}

func ramStressorErrors(stressor *stress.RAMStressor) <-chan error {
	if stressor == nil {
		return nil
	}
	return stressor.Errors()
}

func memoryGuardErrors(guard *memoryGuard) <-chan error {
	if guard == nil {
		return nil
	}
	return guard.Errors()
}

func printFillStatus(
	cpuStressor *stress.CPUStressor,
	ramStressor *stress.RAMStressor,
	guard *memoryGuard,
	controller *adaptiveMemoryController,
	oomScoreAdj int,
	diagnosticFields []string,
	summary *fillRunSummary,
) {
	fields := []string{"mode=fill"}
	if cpuStressor != nil {
		status := cpuStressor.Status()
		if summary != nil {
			summary.observeCPU(status)
		}
		fields = append(
			fields,
			fmt.Sprintf(
				"cpu_band=%.1f:%.1f%%",
				status.RequestedBandLowPercent,
				status.RequestedBandHighPercent,
			),
			"cpu_scope="+string(status.Scope),
			"cpu_scheduler="+string(status.WorkerScheduler),
			"cpu_yield_policy="+string(status.YieldPolicy),
		)
		if status.WorkerScheduler == stress.WorkerSchedulerNormal {
			fields = append(
				fields,
				"cpu_nice="+formatWorkerNice(status.WorkerNice),
			)
		}
		fields = append(
			fields,
			fmt.Sprintf("cpu_drive=%.1f%%", status.AppliedPercent),
			fmt.Sprintf(
				"cpu_workers=%d/%d",
				status.ActiveWorkers,
				status.MaxWorkers,
			),
			fmt.Sprintf("cpu_scope_cpus=%.2f", status.ScopeCPUs),
			"cpu_source="+status.AccountingSource,
			"cpu_boundary="+formatCPUBoundary(
				status.AccountingBoundaryKind,
				status.AccountingBoundaryID,
			),
		)
		if status.HasScopeSample {
			fields = append(
				fields,
				fmt.Sprintf("cpu_observed=%.1f%%", status.LastScopePercent),
			)
		}
		fields = append(fields, diagnosticFields...)
	}

	if ramStressor != nil && controller != nil {
		ramStatus, adaptiveStatus := controller.Snapshot()
		if summary != nil {
			summary.observeMemory(ramStatus, adaptiveStatus)
		}
		fields = append(
			fields,
			fmt.Sprintf(
				"memory_band=%.1f:%.1f%%",
				adaptiveStatus.lowPercent,
				adaptiveStatus.highPercent,
			),
			"memory_yield_policy="+string(controller.config.yieldPolicy),
			"memory_action="+string(adaptiveStatus.action),
			"memory_observed_scope="+adaptiveStatus.scope,
			fmt.Sprintf(
				"memory_observed=%.1f%%",
				adaptiveStatus.observed,
			),
			fmt.Sprintf("memory_cap=%dMiB", adaptiveStatus.hardCapMB),
			fmt.Sprintf("memory_requested=%dMiB", ramStatus.RequestedMB),
			fmt.Sprintf("memory_target=%dMiB", ramStatus.TargetMB),
			fmt.Sprintf("memory_current=%dMiB", ramStatus.CurrentMB),
			fmt.Sprintf(
				"memory_grow_rate=%dMiB/s",
				ramStatus.GrowthRateLimitMB,
			),
			fmt.Sprintf(
				"memory_release_rate=%dMiB/s",
				ramStatus.ReleaseRateLimitMB,
			),
			"oom_score_adj="+formatOOMScoreAdj(oomScoreAdj),
		)
	}

	if stats, err := system.Snapshot(); err == nil {
		if summary != nil {
			summary.observeRSS(stats.ProcessRSSMB)
		}
		fields = append(
			fields,
			"memory_scope="+stats.MemoryScope,
			fmt.Sprintf("memory_total=%.1f%%", stats.MemoryPercent),
			fmt.Sprintf("process_rss=%dMiB", stats.ProcessRSSMB),
		)
	}
	if guard != nil {
		if snapshot, err := guard.safetySnapshot(); err == nil {
			if summary != nil {
				summary.observeAvailable(snapshot.availableMB)
			}
			fields = append(
				fields,
				"memory_guard_scope="+snapshot.source,
				fmt.Sprintf(
					"memory_available=%dMiB",
					snapshot.availableMB,
				),
				fmt.Sprintf(
					"memory_min_available=%dMiB",
					snapshot.minimumAvailableMB,
				),
			)
		}
	}
	fmt.Printf(
		"[%s] %s\n",
		time.Now().Format("15:04:05"),
		strings.Join(fields, " "),
	)
}

func formatCPUCgroupStatusFields(
	info system.CPUCgroupInfo,
	probeErr error,
) []string {
	if probeErr != nil {
		return []string{
			"cpu_cgroup=unknown",
			"cpu_cgroup_probe=failed",
		}
	}
	fields := []string{
		"cpu_cgroup=" + info.Version,
		"cpu_cgroup_probe=ok",
	}
	switch {
	case !info.QuotaKnown:
		fields = append(fields, "cpu_cgroup_quota=unknown")
	case !info.QuotaLimited:
		fields = append(fields, "cpu_cgroup_quota=unlimited")
	default:
		fields = append(
			fields,
			fmt.Sprintf("cpu_cgroup_quota=%.2fCPU", info.QuotaCPUs),
			"cpu_cgroup_quota_level="+info.QuotaLevel,
		)
	}
	if info.WeightKnown {
		fieldPrefix := "cpu_cgroup_" + info.WeightKind
		fields = append(
			fields,
			fmt.Sprintf(
				"%s_min=%d",
				fieldPrefix,
				info.Weight,
			),
			fieldPrefix+"_level="+info.WeightLevel,
		)
	} else {
		fields = append(fields, "cpu_cgroup_weight_min=unknown")
	}
	if info.ThrottlingKnown {
		fields = append(
			fields,
			fmt.Sprintf("cpu_cgroup_periods=%d", info.Periods),
			fmt.Sprintf(
				"cpu_cgroup_throttled_periods=%d",
				info.ThrottledPeriods,
			),
			"cpu_cgroup_throttled_time="+info.ThrottledTime.String(),
			"cpu_cgroup_throttling_level="+info.ThrottlingLevel,
		)
	} else {
		fields = append(fields, "cpu_cgroup_throttling=unknown")
	}
	return fields
}
