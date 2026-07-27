package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/fanderchan/loadsim/internal/stress"
	"github.com/fanderchan/loadsim/internal/system"

	"github.com/spf13/cobra"
)

type stressOptions struct {
	cpuPercent             float64
	cpuWave                string
	cpuCores               int
	cpuScheduler           string
	cpuNice                string
	memoryMiB              int
	memoryGiB              int
	memoryWaveMiB          string
	memoryWaveGiB          string
	periodSec              int
	memoryGrowMiBPerSec    int
	memoryReleaseMiBPerSec int
	memoryBlockMiB         int
	memoryCheckMS          int
	memoryMinAvailableMiB  int
	oomScoreAdj            int
	force                  bool
	durationSec            int
	statusIntervalSec      int
}

var stressConfig stressOptions

var stressCmd = newStressCommand(&stressConfig)

func newStressCommand(options *stressOptions) *cobra.Command {
	command := &cobra.Command{
		Use:   "stress",
		Short: "Generate explicit fixed or wave-shaped test load",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runStress(command, *options)
		},
	}

	flags := command.Flags()
	flags.Float64Var(
		&options.cpuPercent,
		"cpu-percent",
		0,
		"fixed CPU load as a percent of selected worker capacity",
	)
	flags.StringVar(
		&options.cpuWave,
		"cpu-wave",
		"",
		"CPU wave bounds as LOW:HIGH percent",
	)
	flags.IntVar(
		&options.cpuCores,
		"cpu-cores",
		0,
		"CPU worker count; 0 uses the process scheduling capacity",
	)
	flags.StringVar(
		&options.cpuScheduler,
		"cpu-scheduler",
		"normal",
		"CPU worker scheduler: normal or idle",
	)
	flags.StringVar(
		&options.cpuNice,
		"cpu-nice",
		"0",
		"nice value for the normal scheduler, 0 to 19 or inherit",
	)
	flags.IntVar(
		&options.memoryMiB,
		"memory-mib",
		0,
		"fixed memory load in MiB",
	)
	flags.IntVar(
		&options.memoryGiB,
		"memory-gib",
		0,
		"fixed memory load in GiB",
	)
	flags.StringVar(
		&options.memoryWaveMiB,
		"memory-wave-mib",
		"",
		"memory wave bounds as LOW:HIGH MiB",
	)
	flags.StringVar(
		&options.memoryWaveGiB,
		"memory-wave-gib",
		"",
		"memory wave bounds as LOW:HIGH GiB",
	)
	flags.IntVar(
		&options.periodSec,
		"period-sec",
		60,
		"period in seconds for every enabled wave",
	)
	flags.IntVar(
		&options.memoryGrowMiBPerSec,
		"memory-grow-mib-per-sec",
		64,
		"maximum memory growth rate in MiB per second; 0 is unlimited",
	)
	flags.IntVar(
		&options.memoryReleaseMiBPerSec,
		"memory-release-mib-per-sec",
		256,
		"maximum normal memory release rate in MiB per second; 0 is unlimited",
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
	flags.BoolVar(
		&options.force,
		"force",
		false,
		"bypass only the startup target budget; runtime safety remains enabled",
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
	rootCmd.AddCommand(stressCmd)
}

func runStress(command *cobra.Command, options stressOptions) error {
	hasCPUFixed := command.Flags().Changed("cpu-percent")
	hasCPUWave := strings.TrimSpace(options.cpuWave) != ""
	if hasCPUFixed && hasCPUWave {
		return fmt.Errorf("use only one of --cpu-percent or --cpu-wave")
	}
	hasMemoryFixed := command.Flags().Changed("memory-mib") ||
		command.Flags().Changed("memory-gib")
	hasMemoryWave := strings.TrimSpace(options.memoryWaveMiB) != "" ||
		strings.TrimSpace(options.memoryWaveGiB) != ""
	if hasMemoryFixed && hasMemoryWave {
		return fmt.Errorf(
			"use fixed memory flags or wave memory flags, not both",
		)
	}
	if !hasCPUFixed && !hasCPUWave && !hasMemoryFixed && !hasMemoryWave {
		return fmt.Errorf("stress requires a CPU or memory target")
	}
	hasCPU := hasCPUFixed || hasCPUWave
	hasMemory := hasMemoryFixed || hasMemoryWave
	if !hasCPU && anyFlagChanged(
		command,
		"cpu-cores",
		"cpu-scheduler",
		"cpu-nice",
	) {
		return fmt.Errorf("CPU tuning flags require a CPU target")
	}
	if !hasMemory && anyFlagChanged(
		command,
		"memory-grow-mib-per-sec",
		"memory-release-mib-per-sec",
		"memory-block-mib",
		"memory-check-ms",
		"memory-min-available-mib",
		"oom-score-adj",
		"force",
	) {
		return fmt.Errorf("memory tuning flags require a memory target")
	}
	if hasCPUFixed && options.cpuPercent <= 0 {
		return fmt.Errorf("--cpu-percent must be greater than zero")
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
	if hasCPUFixed || hasCPUWave {
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

		cpuConfig := stress.CPUConfig{
			Mode:            stress.ModeFixed,
			Scope:           stress.ScopeWorkers,
			IdleMode:        stress.IdleModePark,
			Percent:         options.cpuPercent,
			Cores:           options.cpuCores,
			WorkerScheduler: scheduler,
			WorkerNice:      workerNice,
		}
		if hasCPUWave {
			band, err := parsePercentBand(
				options.cpuWave,
				"CPU wave",
				100,
				1,
			)
			if err != nil {
				return err
			}
			period, err := seconds(options.periodSec, "wave period", false)
			if err != nil {
				return err
			}
			cpuConfig.Mode = stress.ModeWave
			cpuConfig.MinPercent = band.low
			cpuConfig.MaxPercent = band.high
			cpuConfig.Period = period
		}
		cpuStressor, err = stress.NewCPUStressor(cpuConfig)
		if err != nil {
			return err
		}
	}

	var (
		ramStressor *stress.RAMStressor
		guard       *memoryGuard
	)
	if hasMemoryFixed || hasMemoryWave {
		ramConfig := stress.RAMConfig{
			Mode:                     stress.ModeFixed,
			BlockMB:                  options.memoryBlockMiB,
			ControlInterval:          100 * time.Millisecond,
			GrowthRateLimitMBPerSec:  options.memoryGrowMiBPerSec,
			ReleaseRateLimitMBPerSec: options.memoryReleaseMiBPerSec,
		}
		maxMemoryMiB := 0
		if hasMemoryFixed {
			switch {
			case options.memoryMiB > 0 && options.memoryGiB > 0:
				return fmt.Errorf(
					"use only one of --memory-mib or --memory-gib",
				)
			case options.memoryMiB > 0:
				ramConfig.SizeMB = options.memoryMiB
			case options.memoryGiB > 0:
				value, err := gibToMiB(options.memoryGiB, "memory GiB")
				if err != nil {
					return err
				}
				ramConfig.SizeMB = value
			default:
				return fmt.Errorf(
					"fixed memory stress requires a positive --memory-mib or --memory-gib",
				)
			}
			maxMemoryMiB = ramConfig.SizeMB
		} else {
			if options.memoryWaveMiB != "" &&
				options.memoryWaveGiB != "" {
				return fmt.Errorf(
					"use only one of --memory-wave-mib or --memory-wave-gib",
				)
			}
			value := options.memoryWaveMiB
			name := "memory wave MiB"
			scale := 1
			if options.memoryWaveGiB != "" {
				value = options.memoryWaveGiB
				name = "memory wave GiB"
				scale = 1024
			}
			minimum, maximum, err := parsePositiveIntegerRange(value, name)
			if err != nil {
				return err
			}
			if scale != 1 {
				minimum, err = gibToMiB(minimum, "memory wave minimum GiB")
				if err != nil {
					return err
				}
				maximum, err = gibToMiB(maximum, "memory wave maximum GiB")
				if err != nil {
					return err
				}
			}
			period, err := seconds(options.periodSec, "wave period", false)
			if err != nil {
				return err
			}
			ramConfig.Mode = stress.ModeWave
			ramConfig.MinSizeMB = minimum
			ramConfig.MaxSizeMB = maximum
			ramConfig.Period = period
			maxMemoryMiB = maximum
		}
		if options.memoryMinAvailableMiB < 0 {
			return fmt.Errorf("minimum available memory must not be negative")
		}
		if err := validateOOMScoreAdj(options.oomScoreAdj); err != nil {
			return err
		}

		ramStressor, err = stress.NewRAMStressor(ramConfig)
		if err != nil {
			return err
		}
		memoryCheckInterval, err := milliseconds(
			options.memoryCheckMS,
			"memory check interval",
			false,
		)
		if err != nil {
			_ = stopResourcesImmediately(ramStressor, cpuStressor)
			return err
		}
		guard, err = newMemoryGuard(
			maxMemoryMiB,
			options.memoryBlockMiB,
			options.memoryMinAvailableMiB,
			memoryCheckInterval,
			options.force,
			func() error {
				return stopResourcesImmediately(ramStressor, cpuStressor)
			},
		)
		if err != nil {
			_ = stopResourcesImmediately(ramStressor, cpuStressor)
			return err
		}
		if err := applyOOMScoreAdj(options.oomScoreAdj); err != nil {
			guard.Stop()
			_ = stopResourcesImmediately(ramStressor, cpuStressor)
			return err
		}
	}

	cleanupStartFailure := func(startErr error) error {
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
	if cpuStressor != nil {
		if err := cpuStressor.Start(); err != nil {
			return cleanupStartFailure(err)
		}
	}

	printStatus := func() {
		printStressStatus(cpuStressor, ramStressor, guard, options.oomScoreAdj)
	}
	reason, runErr := watchLoop(
		runDuration,
		statusInterval,
		printStatus,
		stressorErrors(cpuStressor),
		ramStressorErrors(ramStressor),
		memoryGuardErrors(guard),
		nil,
	)

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
				printStressStatus(
					nil,
					ramStressor,
					guard,
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
	)
	if err := joinErrors(runErr, stopErr, pendingErr); err != nil {
		return err
	}

	fmt.Printf("stopped: %s\n", reason)
	return nil
}

func printStressStatus(
	cpuStressor *stress.CPUStressor,
	ramStressor *stress.RAMStressor,
	guard *memoryGuard,
	oomScoreAdj int,
) {
	fields := []string{"mode=stress"}
	if cpuStressor != nil {
		status := cpuStressor.Status()
		fields = append(
			fields,
			"cpu_mode="+string(status.Mode),
			fmt.Sprintf("cpu_target=%.1f%%", status.RequestedPercent),
			fmt.Sprintf("cpu_drive=%.1f%%", status.AppliedPercent),
			fmt.Sprintf(
				"cpu_workers=%d/%d",
				status.ActiveWorkers,
				status.MaxWorkers,
			),
			"cpu_scheduler="+string(status.WorkerScheduler),
		)
		if status.WorkerScheduler == stress.WorkerSchedulerNormal {
			fields = append(
				fields,
				"cpu_nice="+formatWorkerNice(status.WorkerNice),
			)
		}
	}
	if ramStressor != nil {
		status := ramStressor.Status()
		fields = append(
			fields,
			"memory_mode="+string(status.Mode),
			fmt.Sprintf("memory_requested=%dMiB", status.RequestedMB),
			fmt.Sprintf("memory_target=%dMiB", status.TargetMB),
			fmt.Sprintf("memory_current=%dMiB", status.CurrentMB),
			fmt.Sprintf(
				"memory_grow_rate=%dMiB/s",
				status.GrowthRateLimitMB,
			),
			fmt.Sprintf(
				"memory_release_rate=%dMiB/s",
				status.ReleaseRateLimitMB,
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
