package cmd

import (
	"fmt"
	"time"

	"github.com/fanderchan/loadsim/internal/stress"
	"github.com/fanderchan/loadsim/internal/system"

	"github.com/spf13/cobra"
)

var (
	comboCPUPercent     float64
	comboCPUMinPercent  float64
	comboCPUMaxPercent  float64
	comboCPUPeriodSec   int
	comboCPUCores       int
	comboCPUMode        string
	comboCPUScope       string
	comboCPUIdleMode    string
	comboCPUControlMS   int
	comboCPUSampleMS    int
	comboCPUDeadband    float64
	comboCPUMaxStep     float64
	comboCPUWorkerNice  string
	comboRAMMode        string
	comboRAMSizeMB      int
	comboRAMMinSizeMB   int
	comboRAMMaxSizeMB   int
	comboRAMPeriodSec   int
	comboRAMBlockMB     int
	comboRAMControlMS   int
	comboRAMRateLimitMB int
	comboRunTimeSec     int
	comboStatusEverySec int
	comboForce          bool
	comboMinAvailableMB int
	comboMemoryCheckMS  int
	comboOOMScoreAdj    int
	comboRAMMinPercent  float64
	comboRAMMaxPercent  float64
	comboRAMMaxLoad     float64
	comboRAMAdaptiveMS  int
)

var comboCmd = &cobra.Command{
	Use:   "combo",
	Short: "Occupy CPU and RAM at the same time",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cpuMode, err := stress.ParseMode(comboCPUMode)
		if err != nil {
			return err
		}
		ramMode, adaptiveRAM, err := parseRAMMode(comboRAMMode)
		if err != nil {
			return err
		}
		cpuPeriod, err := seconds(comboCPUPeriodSec, "CPU wave period", false)
		if err != nil {
			return err
		}
		cpuControlInterval, err := milliseconds(comboCPUControlMS, "CPU control interval", false)
		if err != nil {
			return err
		}
		cpuSampleDuration, err := milliseconds(comboCPUSampleMS, "CPU sample duration", false)
		if err != nil {
			return err
		}
		ramPeriod, err := seconds(comboRAMPeriodSec, "RAM wave period", false)
		if err != nil {
			return err
		}
		ramControlInterval, err := milliseconds(comboRAMControlMS, "RAM control interval", false)
		if err != nil {
			return err
		}
		runDuration, err := seconds(comboRunTimeSec, "run time", true)
		if err != nil {
			return err
		}
		statusInterval, err := seconds(comboStatusEverySec, "status interval", false)
		if err != nil {
			return err
		}
		if err := validateCLIControllerTuning(comboCPUDeadband, comboCPUMaxStep); err != nil {
			return err
		}
		workerNice, err := parseWorkerNice(comboCPUWorkerNice)
		if err != nil {
			return err
		}
		memoryCheckInterval, err := milliseconds(
			comboMemoryCheckMS,
			"memory check interval",
			false,
		)
		if err != nil {
			return err
		}
		if err := validateOOMScoreAdj(comboOOMScoreAdj); err != nil {
			return err
		}
		ramAdaptiveInterval, err := milliseconds(
			comboRAMAdaptiveMS,
			"adaptive RAM interval",
			false,
		)
		if err != nil {
			return err
		}
		if adaptiveRAM && comboForce {
			return fmt.Errorf(
				"adaptive RAM mode does not allow --force",
			)
		}
		if adaptiveRAM && comboMinAvailableMB < 0 {
			return fmt.Errorf("minimum available memory must not be negative")
		}

		cpuCfg := stress.CPUConfig{
			Mode:            cpuMode,
			Scope:           stress.CPUScope(comboCPUScope),
			IdleMode:        stress.CPUIdleMode(comboCPUIdleMode),
			Percent:         comboCPUPercent,
			MinPercent:      comboCPUMinPercent,
			MaxPercent:      comboCPUMaxPercent,
			Period:          cpuPeriod,
			Cores:           comboCPUCores,
			ControlInterval: cpuControlInterval,
			SampleDuration:  cpuSampleDuration,
			DeadbandPercent: comboCPUDeadband,
			MaxStepPercent:  comboCPUMaxStep,
			WorkerNice:      workerNice,
		}
		ramCfg := stress.RAMConfig{
			Mode:              ramMode,
			SizeMB:            comboRAMSizeMB,
			MinSizeMB:         comboRAMMinSizeMB,
			MaxSizeMB:         comboRAMMaxSizeMB,
			Period:            ramPeriod,
			BlockMB:           comboRAMBlockMB,
			ControlInterval:   ramControlInterval,
			RateLimitMBPerSec: comboRAMRateLimitMB,
			ImmediateShrink:   adaptiveRAM,
		}
		if adaptiveRAM {
			if comboRAMRateLimitMB <= 0 {
				return fmt.Errorf(
					"adaptive RAM mode requires a positive --ram-rate-limit",
				)
			}
			ramCfg.SizeMB = 1
		}

		cpuStressor, err := stress.NewCPUStressor(cpuCfg)
		if err != nil {
			return err
		}
		ramStressor, err := stress.NewRAMStressor(ramCfg)
		if err != nil {
			return err
		}
		if !cpuConfigActive(cpuCfg) || !ramConfigActive(ramCfg) {
			return fmt.Errorf("combo requires both CPU and RAM targets to be greater than zero")
		}

		var adaptiveController *adaptiveMemoryController
		maxRAMTargetMB := comboRAMSizeMB
		if adaptiveRAM {
			adaptiveController, err = newAdaptiveMemoryController(
				adaptiveMemoryConfig{
					lowPercent:               comboRAMMinPercent,
					highPercent:              comboRAMMaxPercent,
					maxLoadPercent:           comboRAMMaxLoad,
					interval:                 ramAdaptiveInterval,
					blockMB:                  comboRAMBlockMB,
					configuredMinAvailableMB: uint64(comboMinAvailableMB),
				},
				ramStressor,
				func() error {
					ramStopErr, cpuStopErr := stopRAMBeforeCPU(
						ramStressor.Stop,
						cpuStressor.Stop,
					)
					return joinErrors(ramStopErr, cpuStopErr)
				},
			)
			if err != nil {
				return err
			}
			maxRAMTargetMB, err = adaptiveController.Preflight()
			if err != nil {
				adaptiveController.Stop()
				return err
			}
		} else if ramMode == stress.ModeWave {
			maxRAMTargetMB = comboRAMMaxSizeMB
		}
		memoryGuard, err := newMemoryGuard(
			maxRAMTargetMB,
			comboRAMBlockMB,
			comboMinAvailableMB,
			memoryCheckInterval,
			comboForce,
			func() error {
				ramStopErr, cpuStopErr := stopRAMBeforeCPU(
					ramStressor.Stop,
					cpuStressor.Stop,
				)
				return joinErrors(ramStopErr, cpuStopErr)
			},
		)
		if err != nil {
			stopAdaptiveMemoryController(adaptiveController)
			return err
		}
		if err := applyOOMScoreAdj(comboOOMScoreAdj); err != nil {
			stopAdaptiveMemoryController(adaptiveController)
			memoryGuard.Stop()
			return joinErrors(
				err,
				drainClosedErrors(
					memoryGuard.Errors(),
					adaptiveMemoryErrors(adaptiveController),
				),
			)
		}

		if err := cpuStressor.Start(); err != nil {
			stopAdaptiveMemoryController(adaptiveController)
			memoryGuard.Stop()
			return joinErrors(
				err,
				drainClosedErrors(
					memoryGuard.Errors(),
					adaptiveMemoryErrors(adaptiveController),
				),
			)
		}
		if err := memoryGuard.preflight(); err != nil {
			cpuStopErr := cpuStressor.Stop()
			stopAdaptiveMemoryController(adaptiveController)
			memoryGuard.Stop()
			return joinErrors(
				err,
				cpuStopErr,
				drainClosedErrors(
					cpuStressor.Errors(),
					memoryGuard.Errors(),
					adaptiveMemoryErrors(adaptiveController),
				),
			)
		}
		if err := memoryGuard.Start(); err != nil {
			stopAdaptiveMemoryController(adaptiveController)
			ramStopErr, cpuStopErr := stopRAMBeforeCPU(
				ramStressor.Stop,
				cpuStressor.Stop,
			)
			memoryGuard.Stop()
			return joinErrors(
				err,
				ramStopErr,
				cpuStopErr,
				drainClosedErrors(
					cpuStressor.Errors(),
					ramStressor.Errors(),
					memoryGuard.Errors(),
					adaptiveMemoryErrors(adaptiveController),
				),
			)
		}
		if err := ramStressor.Start(); err != nil {
			stopAdaptiveMemoryController(adaptiveController)
			ramStopErr, cpuStopErr := stopRAMBeforeCPU(
				ramStressor.Stop,
				cpuStressor.Stop,
			)
			memoryGuard.Stop()
			return joinErrors(
				err,
				ramStopErr,
				cpuStopErr,
				drainClosedErrors(
					cpuStressor.Errors(),
					ramStressor.Errors(),
					memoryGuard.Errors(),
					adaptiveMemoryErrors(adaptiveController),
				),
			)
		}
		if adaptiveController != nil {
			if err := adaptiveController.Start(); err != nil {
				adaptiveController.Stop()
				ramStopErr, cpuStopErr := stopRAMBeforeCPU(
					ramStressor.Stop,
					cpuStressor.Stop,
				)
				memoryGuard.Stop()
				return joinErrors(
					err,
					ramStopErr,
					cpuStopErr,
					drainClosedErrors(
						cpuStressor.Errors(),
						ramStressor.Errors(),
						memoryGuard.Errors(),
						adaptiveController.Errors(),
					),
				)
			}
		}

		reason, runErr := watchLoop(
			runDuration,
			statusInterval,
			func() {
				printComboStatus(
					cpuStressor,
					ramStressor,
					memoryGuard,
					adaptiveController,
					comboOOMScoreAdj,
				)
			},
			cpuStressor.Errors(),
			ramStressor.Errors(),
			memoryGuard.Errors(),
			adaptiveMemoryErrors(adaptiveController),
		)

		stopAdaptiveMemoryController(adaptiveController)
		ramStopErr, cpuStopErr := stopRAMBeforeCPU(
			ramStressor.Stop,
			cpuStressor.Stop,
		)
		memoryGuard.Stop()
		pendingErr := drainClosedErrors(
			cpuStressor.Errors(),
			ramStressor.Errors(),
			memoryGuard.Errors(),
			adaptiveMemoryErrors(adaptiveController),
		)
		if err := joinErrors(runErr, cpuStopErr, ramStopErr, pendingErr); err != nil {
			return err
		}

		fmt.Printf("stopped: %s\n", reason)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(comboCmd)

	comboCmd.Flags().StringVar(&comboCPUMode, "cpu-mode", "fixed", "CPU mode: fixed or wave")
	comboCmd.Flags().StringVar(&comboCPUScope, "cpu-scope", "workers", "CPU target scope: workers, host (/proc/stat), or system (visible cgroup root)")
	comboCmd.Flags().StringVar(&comboCPUIdleMode, "cpu-idle-mode", "park", "CPU idle worker behavior: park or trim")
	comboCmd.Flags().Float64Var(&comboCPUPercent, "cpu-percent", 50, "fixed CPU target percent")
	comboCmd.Flags().Float64Var(&comboCPUMinPercent, "cpu-min", 20, "wave CPU minimum percent")
	comboCmd.Flags().Float64Var(&comboCPUMaxPercent, "cpu-max", 80, "wave CPU maximum percent")
	comboCmd.Flags().IntVar(&comboCPUPeriodSec, "cpu-period", 60, "wave CPU period in seconds")
	comboCmd.Flags().IntVar(&comboCPUCores, "cpu-cores", 0, "worker count, 0 uses process-available CPUs")
	comboCmd.Flags().IntVar(&comboCPUControlMS, "cpu-control-ms", 250, "CPU controller adjustment interval in milliseconds")
	comboCmd.Flags().IntVar(&comboCPUSampleMS, "cpu-sample-ms", 200, "CPU host/system sample duration in milliseconds")
	comboCmd.Flags().Float64Var(&comboCPUDeadband, "cpu-deadband", 1.0, "CPU host/system deadband percent before adjusting")
	comboCmd.Flags().Float64Var(&comboCPUMaxStep, "cpu-max-step", 10.0, "maximum CPU worker drive change per control step in percent")
	comboCmd.Flags().StringVar(&comboCPUWorkerNice, "cpu-worker-nice", "19", "CPU worker nice from 0 to 19, or inherit; 19 yields most to normal-priority work")

	comboCmd.Flags().StringVar(&comboRAMMode, "ram-mode", "fixed", "RAM mode: fixed, wave, or adaptive")
	comboCmd.Flags().IntVar(&comboRAMSizeMB, "ram-size", 256, "fixed RAM target in MB")
	comboCmd.Flags().IntVar(&comboRAMMinSizeMB, "ram-min-size", 64, "wave RAM minimum in MB")
	comboCmd.Flags().IntVar(&comboRAMMaxSizeMB, "ram-max-size", 256, "wave RAM maximum in MB")
	comboCmd.Flags().IntVar(&comboRAMPeriodSec, "ram-period", 60, "wave RAM period in seconds")
	comboCmd.Flags().IntVar(&comboRAMBlockMB, "ram-block-size", 16, "RAM allocation block size in MB; adaptive mode allows at most 64")
	comboCmd.Flags().IntVar(&comboRAMControlMS, "ram-control-ms", 250, "RAM control interval in milliseconds")
	comboCmd.Flags().IntVar(&comboRAMRateLimitMB, "ram-rate-limit", 0, "RAM change rate limit in MB per second; adaptive mode requires a positive growth limit and releases immediately")

	comboCmd.Flags().IntVar(&comboRunTimeSec, "time", 60, "run time in seconds, 0 means no limit")
	comboCmd.Flags().IntVar(&comboStatusEverySec, "status-interval", 2, "status print interval in seconds")
	comboCmd.Flags().BoolVar(&comboForce, "force", false, "allow a fixed or wave RAM target above the safe startup budget; adaptive mode rejects this flag")
	comboCmd.Flags().IntVar(&comboMinAvailableMB, "memory-min-available", 0, "minimum effective available memory in MB, 0 selects a safe automatic threshold")
	comboCmd.Flags().IntVar(&comboMemoryCheckMS, "memory-check-ms", int(defaultMemoryCheckInterval/time.Millisecond), "runtime memory safety check interval in milliseconds, minimum 10")
	comboCmd.Flags().IntVar(&comboOOMScoreAdj, "oom-score-adj", 1000, "Linux OOM score adjustment from 0 to 1000; -1 inherits the current value")
	comboCmd.Flags().Float64Var(&comboRAMMinPercent, "ram-min-percent", 30, "adaptive RAM lower total utilization bound")
	comboCmd.Flags().Float64Var(&comboRAMMaxPercent, "ram-max-percent", 50, "adaptive RAM upper total utilization bound")
	comboCmd.Flags().Float64Var(&comboRAMMaxLoad, "ram-max-load-percent", 30, "adaptive RAM maximum memory share allocated by LoadSim")
	comboCmd.Flags().IntVar(&comboRAMAdaptiveMS, "ram-adaptive-ms", int(defaultAdaptiveMemoryInterval/time.Millisecond), "adaptive RAM utilization sampling interval in milliseconds")
}

func cpuConfigActive(cfg stress.CPUConfig) bool {
	switch cfg.Mode {
	case stress.ModeFixed:
		return cfg.Percent > 0
	case stress.ModeWave:
		return cfg.MaxPercent > 0
	default:
		return false
	}
}

func ramConfigActive(cfg stress.RAMConfig) bool {
	switch cfg.Mode {
	case stress.ModeFixed:
		return cfg.SizeMB > 0
	case stress.ModeWave:
		return cfg.MaxSizeMB > 0
	default:
		return false
	}
}

func printComboStatus(
	cpuStressor *stress.CPUStressor,
	ramStressor *stress.RAMStressor,
	guard *memoryGuard,
	adaptive *adaptiveMemoryController,
	oomScoreAdj int,
) {
	cpuStatus := cpuStressor.Status()
	var (
		ramStatus      stress.RAMStatus
		adaptiveStatus adaptiveMemoryStatus
	)
	if adaptive != nil {
		ramStatus, adaptiveStatus = adaptive.Snapshot()
	} else {
		ramStatus = ramStressor.Status()
	}
	workerNice := formatWorkerNice(cpuStatus.WorkerNice)
	cpuBoundary := formatCPUBoundary(
		cpuStatus.AccountingBoundaryKind,
		cpuStatus.AccountingBoundaryID,
	)
	ramControl := fmt.Sprintf("ram_mode=%s", ramStatus.Mode)
	if adaptive != nil {
		ramControl = fmt.Sprintf(
			"ram_mode=%s ram_band=%.1f-%.1f%% ram_band_action=%s ram_band_scope=%s ram_band_memory=%.1f%% ram_band_target=%dMB ram_hard_cap=%dMB ram_max_load=%.1f%%",
			adaptiveRAMMode,
			adaptiveStatus.lowPercent,
			adaptiveStatus.highPercent,
			adaptiveStatus.action,
			adaptiveStatus.scope,
			adaptiveStatus.observed,
			adaptiveStatus.targetMB,
			adaptiveStatus.hardCapMB,
			adaptiveStatus.maxLoadPercent,
		)
	}
	stats, err := system.Snapshot()
	memorySafety, safetyErr := guard.safetySnapshot()
	if err != nil || safetyErr != nil {
		fmt.Printf(
			"[%s] combo cpu_scope=%s cpu_idle=%s cpu_worker_nice=%s cpu_target=%.1f%% cpu_band=%.1f-%.1f%% cpu_drive=%.1f%% workers=%d/%d host_cpus=%d process_cpus=%.2f scope_cpus=%.2f scope_contribution_max=%.1f%% cpu_source=%s cpu_boundary=%s %s ram_desired=%dMB ram_target=%dMB ram_current=%dMB memory_check=%dms oom_score_adj=%s\n",
			time.Now().Format("15:04:05"),
			cpuStatus.Scope,
			cpuStatus.IdleMode,
			workerNice,
			cpuStatus.RequestedPercent,
			cpuStatus.RequestedBandLowPercent,
			cpuStatus.RequestedBandHighPercent,
			cpuStatus.AppliedPercent,
			cpuStatus.ActiveWorkers,
			cpuStatus.MaxWorkers,
			cpuStatus.HostCPUs,
			cpuStatus.ProcessCPUs,
			cpuStatus.ScopeCPUs,
			cpuStatus.MaxScopeContributionPercent,
			cpuStatus.AccountingSource,
			cpuBoundary,
			ramControl,
			ramStatus.RequestedMB,
			ramStatus.TargetMB,
			ramStatus.CurrentMB,
			guard.checkInterval/time.Millisecond,
			formatOOMScoreAdj(oomScoreAdj),
		)
		return
	}

	if cpuStatus.Scope != stress.ScopeWorkers && cpuStatus.HasScopeSample {
		fmt.Printf(
			"[%s] combo cpu_scope=%s cpu_idle=%s cpu_worker_nice=%s cpu_target=%.1f%% cpu_band=%.1f-%.1f%% cpu_drive=%.1f%% workers=%d/%d host_cpus=%d process_cpus=%.2f scope_cpus=%.2f scope_contribution_max=%.1f%% cpu_source=%s cpu_boundary=%s %s ram_desired=%dMB ram_target=%dMB ram_current=%dMB scope_cpu=%.1f%% memory_scope=%s memory=%.1f%% memory_guard_scope=%s memory_available=%dMB memory_min_available=%dMB memory_check=%dms oom_score_adj=%s process_rss=%dMB\n",
			time.Now().Format("15:04:05"),
			cpuStatus.Scope,
			cpuStatus.IdleMode,
			workerNice,
			cpuStatus.RequestedPercent,
			cpuStatus.RequestedBandLowPercent,
			cpuStatus.RequestedBandHighPercent,
			cpuStatus.AppliedPercent,
			cpuStatus.ActiveWorkers,
			cpuStatus.MaxWorkers,
			cpuStatus.HostCPUs,
			cpuStatus.ProcessCPUs,
			cpuStatus.ScopeCPUs,
			cpuStatus.MaxScopeContributionPercent,
			cpuStatus.AccountingSource,
			cpuBoundary,
			ramControl,
			ramStatus.RequestedMB,
			ramStatus.TargetMB,
			ramStatus.CurrentMB,
			cpuStatus.LastScopePercent,
			stats.MemoryScope,
			stats.MemoryPercent,
			memorySafety.source,
			memorySafety.availableMB,
			memorySafety.minimumAvailableMB,
			guard.checkInterval/time.Millisecond,
			formatOOMScoreAdj(oomScoreAdj),
			stats.ProcessRSSMB,
		)
		return
	}

	fmt.Printf(
		"[%s] combo cpu_scope=%s cpu_idle=%s cpu_worker_nice=%s cpu_target=%.1f%% cpu_band=%.1f-%.1f%% cpu_drive=%.1f%% workers=%d/%d host_cpus=%d process_cpus=%.2f scope_cpus=%.2f scope_contribution_max=%.1f%% cpu_source=%s cpu_boundary=%s %s ram_desired=%dMB ram_target=%dMB ram_current=%dMB memory_scope=%s memory=%.1f%% memory_guard_scope=%s memory_available=%dMB memory_min_available=%dMB memory_check=%dms oom_score_adj=%s process_rss=%dMB\n",
		time.Now().Format("15:04:05"),
		cpuStatus.Scope,
		cpuStatus.IdleMode,
		workerNice,
		cpuStatus.RequestedPercent,
		cpuStatus.RequestedBandLowPercent,
		cpuStatus.RequestedBandHighPercent,
		cpuStatus.AppliedPercent,
		cpuStatus.ActiveWorkers,
		cpuStatus.MaxWorkers,
		cpuStatus.HostCPUs,
		cpuStatus.ProcessCPUs,
		cpuStatus.ScopeCPUs,
		cpuStatus.MaxScopeContributionPercent,
		cpuStatus.AccountingSource,
		cpuBoundary,
		ramControl,
		ramStatus.RequestedMB,
		ramStatus.TargetMB,
		ramStatus.CurrentMB,
		stats.MemoryScope,
		stats.MemoryPercent,
		memorySafety.source,
		memorySafety.availableMB,
		memorySafety.minimumAvailableMB,
		guard.checkInterval/time.Millisecond,
		formatOOMScoreAdj(oomScoreAdj),
		stats.ProcessRSSMB,
	)
}
