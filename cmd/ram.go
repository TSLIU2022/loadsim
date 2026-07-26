package cmd

import (
	"fmt"
	"time"

	"github.com/fanderchan/loadsim/internal/stress"
	"github.com/fanderchan/loadsim/internal/system"

	"github.com/spf13/cobra"
)

var (
	ramMode           string
	ramSizeMB         int
	ramMinSizeMB      int
	ramMaxSizeMB      int
	ramWavePeriodSec  int
	ramBlockMB        int
	ramControlMS      int
	ramRateLimitMB    int
	ramRunTimeSec     int
	ramStatusEverySec int
	ramForce          bool
	ramMinAvailableMB int
	ramMemoryCheckMS  int
	ramOOMScoreAdj    int
	ramMinPercent     float64
	ramMaxPercent     float64
	ramMaxLoadPercent float64
	ramAdaptiveMS     int
)

var ramCmd = &cobra.Command{
	Use:   "ram",
	Short: "Occupy RAM with fixed, wave, or adaptive patterns",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		mode, adaptive, err := parseRAMMode(ramMode)
		if err != nil {
			return err
		}
		period, err := seconds(ramWavePeriodSec, "RAM wave period", false)
		if err != nil {
			return err
		}
		controlInterval, err := milliseconds(ramControlMS, "RAM control interval", false)
		if err != nil {
			return err
		}
		runDuration, err := seconds(ramRunTimeSec, "run time", true)
		if err != nil {
			return err
		}
		statusInterval, err := seconds(ramStatusEverySec, "status interval", false)
		if err != nil {
			return err
		}
		memoryCheckInterval, err := milliseconds(
			ramMemoryCheckMS,
			"memory check interval",
			false,
		)
		if err != nil {
			return err
		}
		if err := validateOOMScoreAdj(ramOOMScoreAdj); err != nil {
			return err
		}
		adaptiveInterval, err := milliseconds(
			ramAdaptiveMS,
			"adaptive RAM interval",
			false,
		)
		if err != nil {
			return err
		}
		if adaptive && ramForce {
			return fmt.Errorf(
				"adaptive RAM mode does not allow --force",
			)
		}
		if adaptive && ramMinAvailableMB < 0 {
			return fmt.Errorf("minimum available memory must not be negative")
		}

		cfg := stress.RAMConfig{
			Mode:              mode,
			SizeMB:            ramSizeMB,
			MinSizeMB:         ramMinSizeMB,
			MaxSizeMB:         ramMaxSizeMB,
			Period:            period,
			BlockMB:           ramBlockMB,
			ControlInterval:   controlInterval,
			RateLimitMBPerSec: ramRateLimitMB,
			ImmediateShrink:   adaptive,
		}
		if adaptive {
			if ramRateLimitMB <= 0 {
				return fmt.Errorf(
					"adaptive RAM mode requires a positive --rate-limit",
				)
			}
			// Adaptive mode always starts empty. The slow controller raises
			// this fixed target only after confirming spare capacity.
			cfg.SizeMB = 1
		}

		stressor, err := stress.NewRAMStressor(cfg)
		if err != nil {
			return err
		}

		var adaptiveController *adaptiveMemoryController
		maxTargetMB := ramSizeMB
		if adaptive {
			adaptiveController, err = newAdaptiveMemoryController(
				adaptiveMemoryConfig{
					lowPercent:               ramMinPercent,
					highPercent:              ramMaxPercent,
					maxLoadPercent:           ramMaxLoadPercent,
					interval:                 adaptiveInterval,
					blockMB:                  ramBlockMB,
					configuredMinAvailableMB: uint64(ramMinAvailableMB),
				},
				stressor,
				stressor.Stop,
			)
			if err != nil {
				return err
			}
			maxTargetMB, err = adaptiveController.Preflight()
			if err != nil {
				adaptiveController.Stop()
				return err
			}
		} else if mode == stress.ModeWave {
			maxTargetMB = ramMaxSizeMB
		}
		memoryGuard, err := newMemoryGuard(
			maxTargetMB,
			ramBlockMB,
			ramMinAvailableMB,
			memoryCheckInterval,
			ramForce,
			stressor.Stop,
		)
		if err != nil {
			stopAdaptiveMemoryController(adaptiveController)
			return err
		}
		if err := applyOOMScoreAdj(ramOOMScoreAdj); err != nil {
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

		if err := memoryGuard.Start(); err != nil {
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
		if err := stressor.Start(); err != nil {
			stopAdaptiveMemoryController(adaptiveController)
			memoryGuard.Stop()
			return joinErrors(
				err,
				drainClosedErrors(
					stressor.Errors(),
					memoryGuard.Errors(),
					adaptiveMemoryErrors(adaptiveController),
				),
			)
		}
		if adaptiveController != nil {
			if err := adaptiveController.Start(); err != nil {
				adaptiveController.Stop()
				stopErr := stressor.Stop()
				memoryGuard.Stop()
				return joinErrors(
					err,
					stopErr,
					drainClosedErrors(
						stressor.Errors(),
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
				printRAMStatus(
					stressor,
					memoryGuard,
					adaptiveController,
					ramOOMScoreAdj,
				)
			},
			stressor.Errors(),
			nil,
			memoryGuard.Errors(),
			adaptiveMemoryErrors(adaptiveController),
		)

		stopAdaptiveMemoryController(adaptiveController)
		stopErr := stressor.Stop()
		memoryGuard.Stop()
		pendingErr := drainClosedErrors(
			stressor.Errors(),
			memoryGuard.Errors(),
			adaptiveMemoryErrors(adaptiveController),
		)
		if err := joinErrors(runErr, stopErr, pendingErr); err != nil {
			return err
		}

		fmt.Printf("stopped: %s\n", reason)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(ramCmd)

	ramCmd.Flags().StringVar(&ramMode, "mode", "fixed", "fixed, wave, or adaptive")
	ramCmd.Flags().IntVar(&ramSizeMB, "size", 256, "fixed RAM target in MB")
	ramCmd.Flags().IntVar(&ramMinSizeMB, "min-size", 64, "wave mode minimum RAM in MB")
	ramCmd.Flags().IntVar(&ramMaxSizeMB, "max-size", 256, "wave mode maximum RAM in MB")
	ramCmd.Flags().IntVar(&ramWavePeriodSec, "period", 60, "wave mode period in seconds")
	ramCmd.Flags().IntVar(&ramBlockMB, "block-size", 16, "RAM allocation block size in MB; adaptive mode allows at most 64")
	ramCmd.Flags().IntVar(&ramControlMS, "control-ms", 250, "RAM control interval in milliseconds")
	ramCmd.Flags().IntVar(&ramRateLimitMB, "rate-limit", 0, "RAM change rate limit in MB per second; adaptive mode requires a positive growth limit and releases immediately")
	ramCmd.Flags().IntVar(&ramRunTimeSec, "time", 60, "run time in seconds, 0 means no limit")
	ramCmd.Flags().IntVar(&ramStatusEverySec, "status-interval", 2, "status print interval in seconds")
	ramCmd.Flags().BoolVar(&ramForce, "force", false, "allow a fixed or wave RAM target above the safe startup budget; adaptive mode rejects this flag")
	ramCmd.Flags().IntVar(&ramMinAvailableMB, "memory-min-available", 0, "minimum effective available memory in MB, 0 selects a safe automatic threshold")
	ramCmd.Flags().IntVar(&ramMemoryCheckMS, "memory-check-ms", int(defaultMemoryCheckInterval/time.Millisecond), "runtime memory safety check interval in milliseconds, minimum 10")
	ramCmd.Flags().IntVar(&ramOOMScoreAdj, "oom-score-adj", 1000, "Linux OOM score adjustment from 0 to 1000; -1 inherits the current value")
	ramCmd.Flags().Float64Var(&ramMinPercent, "min-percent", 30, "adaptive mode lower total memory utilization bound")
	ramCmd.Flags().Float64Var(&ramMaxPercent, "max-percent", 50, "adaptive mode upper total memory utilization bound")
	ramCmd.Flags().Float64Var(&ramMaxLoadPercent, "max-load-percent", 30, "adaptive mode maximum memory share allocated by LoadSim")
	ramCmd.Flags().IntVar(&ramAdaptiveMS, "adaptive-ms", int(defaultAdaptiveMemoryInterval/time.Millisecond), "adaptive memory utilization sampling interval in milliseconds")
}

func printRAMStatus(
	stressor *stress.RAMStressor,
	guard *memoryGuard,
	adaptive *adaptiveMemoryController,
	oomScoreAdj int,
) {
	var (
		status         stress.RAMStatus
		adaptiveStatus adaptiveMemoryStatus
	)
	if adaptive != nil {
		status, adaptiveStatus = adaptive.Snapshot()
	} else {
		status = stressor.Status()
	}
	mode := string(status.Mode)
	if adaptive != nil {
		mode = adaptiveRAMMode
	}
	stats, err := system.Snapshot()
	memorySafety, safetyErr := guard.safetySnapshot()
	if err != nil || safetyErr != nil {
		if adaptive != nil {
			fmt.Printf(
				"[%s] ram mode=%s band=%.1f-%.1f%% band_action=%s band_scope=%s band_memory=%.1f%% band_target=%dMB hard_cap=%dMB max_load=%.1f%% desired=%dMB target=%dMB current=%dMB block=%dMB rate_limit=%dMB/s memory_check=%dms oom_score_adj=%s\n",
				time.Now().Format("15:04:05"),
				mode,
				adaptiveStatus.lowPercent,
				adaptiveStatus.highPercent,
				adaptiveStatus.action,
				adaptiveStatus.scope,
				adaptiveStatus.observed,
				adaptiveStatus.targetMB,
				adaptiveStatus.hardCapMB,
				adaptiveStatus.maxLoadPercent,
				status.RequestedMB,
				status.TargetMB,
				status.CurrentMB,
				status.BlockMB,
				status.RateLimitMB,
				guard.checkInterval/time.Millisecond,
				formatOOMScoreAdj(oomScoreAdj),
			)
			return
		}
		fmt.Printf(
			"[%s] ram mode=%s desired=%dMB target=%dMB current=%dMB block=%dMB rate_limit=%dMB/s memory_check=%dms oom_score_adj=%s\n",
			time.Now().Format("15:04:05"),
			mode,
			status.RequestedMB,
			status.TargetMB,
			status.CurrentMB,
			status.BlockMB,
			status.RateLimitMB,
			guard.checkInterval/time.Millisecond,
			formatOOMScoreAdj(oomScoreAdj),
		)
		return
	}

	if adaptive != nil {
		fmt.Printf(
			"[%s] ram mode=%s band=%.1f-%.1f%% band_action=%s band_scope=%s band_memory=%.1f%% band_target=%dMB hard_cap=%dMB max_load=%.1f%% desired=%dMB target=%dMB current=%dMB block=%dMB rate_limit=%dMB/s memory_scope=%s memory=%.1f%% memory_guard_scope=%s memory_available=%dMB memory_min_available=%dMB memory_check=%dms oom_score_adj=%s process_rss=%dMB\n",
			time.Now().Format("15:04:05"),
			mode,
			adaptiveStatus.lowPercent,
			adaptiveStatus.highPercent,
			adaptiveStatus.action,
			adaptiveStatus.scope,
			adaptiveStatus.observed,
			adaptiveStatus.targetMB,
			adaptiveStatus.hardCapMB,
			adaptiveStatus.maxLoadPercent,
			status.RequestedMB,
			status.TargetMB,
			status.CurrentMB,
			status.BlockMB,
			status.RateLimitMB,
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
		"[%s] ram mode=%s desired=%dMB target=%dMB current=%dMB block=%dMB rate_limit=%dMB/s memory_scope=%s memory=%.1f%% memory_guard_scope=%s memory_available=%dMB memory_min_available=%dMB memory_check=%dms oom_score_adj=%s process_rss=%dMB\n",
		time.Now().Format("15:04:05"),
		mode,
		status.RequestedMB,
		status.TargetMB,
		status.CurrentMB,
		status.BlockMB,
		status.RateLimitMB,
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
