package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/fanderchan/loadsim/internal/stress"
	"github.com/fanderchan/loadsim/internal/system"

	"github.com/spf13/cobra"
)

type fillOptions struct {
	cpuBand                string
	memoryBand             string
	memoryMaxMiB           int
	memoryMaxGiB           int
	cpuCores               int
	cpuControlMS           int
	cpuSampleMS            int
	cpuMaxStep             float64
	cpuScheduler           string
	cpuNice                string
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
	flags.IntVar(
		&options.memoryMaxMiB,
		"memory-max-mib",
		0,
		"maximum memory allocated by LoadSim in MiB",
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
		maxMemoryMiB, err := selectMemoryMaximum(
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

	printStatus := func() {
		printFillStatus(
			cpuStressor,
			ramStressor,
			guard,
			memoryController,
			options.oomScoreAdj,
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
) {
	fields := []string{"mode=fill"}
	if cpuStressor != nil {
		status := cpuStressor.Status()
		fields = append(
			fields,
			fmt.Sprintf(
				"cpu_band=%.1f:%.1f%%",
				status.RequestedBandLowPercent,
				status.RequestedBandHighPercent,
			),
			"cpu_scope="+string(status.Scope),
			"cpu_scheduler="+string(status.WorkerScheduler),
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
	}

	if ramStressor != nil && controller != nil {
		ramStatus, adaptiveStatus := controller.Snapshot()
		fields = append(
			fields,
			fmt.Sprintf(
				"memory_band=%.1f:%.1f%%",
				adaptiveStatus.lowPercent,
				adaptiveStatus.highPercent,
			),
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
		fields = append(
			fields,
			"memory_scope="+stats.MemoryScope,
			fmt.Sprintf("memory_total=%.1f%%", stats.MemoryPercent),
			fmt.Sprintf("process_rss=%dMiB", stats.ProcessRSSMB),
		)
	}
	if guard != nil {
		if snapshot, err := guard.safetySnapshot(); err == nil {
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
