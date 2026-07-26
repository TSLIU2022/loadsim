package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fanderchan/loadsim/internal/stress"
	"github.com/fanderchan/loadsim/internal/system"

	"github.com/spf13/cobra"
)

var (
	cpuMode           string
	cpuScope          string
	cpuIdleMode       string
	cpuPercent        float64
	cpuMinPercent     float64
	cpuMaxPercent     float64
	cpuWavePeriodSec  int
	cpuCores          int
	cpuControlMS      int
	cpuSampleMS       int
	cpuDeadband       float64
	cpuMaxStep        float64
	cpuWorkerNice     string
	cpuRunTimeSec     int
	cpuStatusEverySec int
)

var cpuCmd = &cobra.Command{
	Use:   "cpu",
	Short: "Occupy CPU with fixed or wave patterns",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		mode, err := stress.ParseMode(cpuMode)
		if err != nil {
			return err
		}
		period, err := seconds(cpuWavePeriodSec, "CPU wave period", false)
		if err != nil {
			return err
		}
		controlInterval, err := milliseconds(cpuControlMS, "CPU control interval", false)
		if err != nil {
			return err
		}
		sampleDuration, err := milliseconds(cpuSampleMS, "CPU sample duration", false)
		if err != nil {
			return err
		}
		runDuration, err := seconds(cpuRunTimeSec, "run time", true)
		if err != nil {
			return err
		}
		statusInterval, err := seconds(cpuStatusEverySec, "status interval", false)
		if err != nil {
			return err
		}
		if err := validateCLIControllerTuning(cpuDeadband, cpuMaxStep); err != nil {
			return err
		}
		workerNice, err := parseWorkerNice(cpuWorkerNice)
		if err != nil {
			return err
		}

		cfg := stress.CPUConfig{
			Mode:            mode,
			Scope:           stress.CPUScope(cpuScope),
			IdleMode:        stress.CPUIdleMode(cpuIdleMode),
			Percent:         cpuPercent,
			MinPercent:      cpuMinPercent,
			MaxPercent:      cpuMaxPercent,
			Period:          period,
			Cores:           cpuCores,
			ControlInterval: controlInterval,
			SampleDuration:  sampleDuration,
			DeadbandPercent: cpuDeadband,
			MaxStepPercent:  cpuMaxStep,
			WorkerNice:      workerNice,
		}

		stressor, err := stress.NewCPUStressor(cfg)
		if err != nil {
			return err
		}

		if err := stressor.Start(); err != nil {
			return err
		}

		reason, runErr := watchLoop(
			runDuration,
			statusInterval,
			func() { printCPUStatus(stressor) },
			stressor.Errors(),
			nil,
			nil,
			nil,
		)

		stopErr := stressor.Stop()
		pendingErr := drainClosedErrors(stressor.Errors())
		if err := joinErrors(runErr, stopErr, pendingErr); err != nil {
			return err
		}

		fmt.Printf("stopped: %s\n", reason)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(cpuCmd)

	cpuCmd.Flags().StringVar(&cpuMode, "mode", "fixed", "fixed or wave")
	cpuCmd.Flags().StringVar(&cpuScope, "scope", "workers", "CPU target scope: workers, host (/proc/stat), or system (visible cgroup root)")
	cpuCmd.Flags().StringVar(&cpuIdleMode, "idle-mode", "park", "idle worker behavior: park or trim")
	cpuCmd.Flags().Float64Var(&cpuPercent, "percent", 50, "fixed CPU target percent")
	cpuCmd.Flags().Float64Var(&cpuMinPercent, "min", 20, "wave mode minimum CPU percent")
	cpuCmd.Flags().Float64Var(&cpuMaxPercent, "max", 80, "wave mode maximum CPU percent")
	cpuCmd.Flags().IntVar(&cpuWavePeriodSec, "period", 60, "wave mode period in seconds")
	cpuCmd.Flags().IntVar(&cpuCores, "cores", 0, "worker count, 0 uses process-available CPUs")
	cpuCmd.Flags().IntVar(&cpuControlMS, "control-ms", 250, "controller adjustment interval in milliseconds")
	cpuCmd.Flags().IntVar(&cpuSampleMS, "sample-ms", 200, "host/system CPU sample duration in milliseconds")
	cpuCmd.Flags().Float64Var(&cpuDeadband, "deadband", 1.0, "host/system CPU deadband percent before adjusting")
	cpuCmd.Flags().Float64Var(&cpuMaxStep, "max-step", 10.0, "maximum worker drive change per control step in percent")
	cpuCmd.Flags().StringVar(&cpuWorkerNice, "worker-nice", "19", "CPU worker nice from 0 to 19, or inherit; 19 yields most to normal-priority work")
	cpuCmd.Flags().IntVar(&cpuRunTimeSec, "time", 60, "run time in seconds, 0 means no limit")
	cpuCmd.Flags().IntVar(&cpuStatusEverySec, "status-interval", 2, "status print interval in seconds")
}

func printCPUStatus(stressor *stress.CPUStressor) {
	status := stressor.Status()
	workerNice := formatWorkerNice(status.WorkerNice)
	cpuBoundary := formatCPUBoundary(
		status.AccountingBoundaryKind,
		status.AccountingBoundaryID,
	)
	stats, err := system.Snapshot()
	if err != nil {
		fmt.Printf(
			"[%s] cpu mode=%s scope=%s idle=%s worker_nice=%s target=%.1f%% band=%.1f-%.1f%% drive=%.1f%% workers=%d/%d host_cpus=%d process_cpus=%.2f scope_cpus=%.2f scope_contribution_max=%.1f%% cpu_source=%s cpu_boundary=%s\n",
			time.Now().Format("15:04:05"),
			status.Mode,
			status.Scope,
			status.IdleMode,
			workerNice,
			status.RequestedPercent,
			status.RequestedBandLowPercent,
			status.RequestedBandHighPercent,
			status.AppliedPercent,
			status.ActiveWorkers,
			status.MaxWorkers,
			status.HostCPUs,
			status.ProcessCPUs,
			status.ScopeCPUs,
			status.MaxScopeContributionPercent,
			status.AccountingSource,
			cpuBoundary,
		)
		return
	}

	if status.Scope != stress.ScopeWorkers && status.HasScopeSample {
		fmt.Printf(
			"[%s] cpu mode=%s scope=%s idle=%s worker_nice=%s target=%.1f%% band=%.1f-%.1f%% drive=%.1f%% workers=%d/%d host_cpus=%d process_cpus=%.2f scope_cpus=%.2f scope_contribution_max=%.1f%% cpu_source=%s cpu_boundary=%s scope_cpu=%.1f%% memory_scope=%s memory=%.1f%% process_rss=%dMB\n",
			time.Now().Format("15:04:05"),
			status.Mode,
			status.Scope,
			status.IdleMode,
			workerNice,
			status.RequestedPercent,
			status.RequestedBandLowPercent,
			status.RequestedBandHighPercent,
			status.AppliedPercent,
			status.ActiveWorkers,
			status.MaxWorkers,
			status.HostCPUs,
			status.ProcessCPUs,
			status.ScopeCPUs,
			status.MaxScopeContributionPercent,
			status.AccountingSource,
			cpuBoundary,
			status.LastScopePercent,
			stats.MemoryScope,
			stats.MemoryPercent,
			stats.ProcessRSSMB,
		)
		return
	}

	fmt.Printf(
		"[%s] cpu mode=%s scope=%s idle=%s worker_nice=%s target=%.1f%% band=%.1f-%.1f%% drive=%.1f%% workers=%d/%d host_cpus=%d process_cpus=%.2f scope_cpus=%.2f scope_contribution_max=%.1f%% cpu_source=%s cpu_boundary=%s memory_scope=%s memory=%.1f%% process_rss=%dMB\n",
		time.Now().Format("15:04:05"),
		status.Mode,
		status.Scope,
		status.IdleMode,
		workerNice,
		status.RequestedPercent,
		status.RequestedBandLowPercent,
		status.RequestedBandHighPercent,
		status.AppliedPercent,
		status.ActiveWorkers,
		status.MaxWorkers,
		status.HostCPUs,
		status.ProcessCPUs,
		status.ScopeCPUs,
		status.MaxScopeContributionPercent,
		status.AccountingSource,
		cpuBoundary,
		stats.MemoryScope,
		stats.MemoryPercent,
		stats.ProcessRSSMB,
	)
}

func formatCPUBoundary(
	kind stress.VisibleSystemCPUBoundaryKind,
	id string,
) string {
	if id == "" {
		return "none"
	}
	if kind == "" {
		kind = stress.VisibleSystemCPUBoundaryUnknown
	}
	return fmt.Sprintf("%s:%s", kind, id)
}

func parseWorkerNice(value string) (*int, error) {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "inherit") {
		workerNice := stress.WorkerNiceInherit
		return &workerNice, nil
	}

	workerNice, err := strconv.Atoi(value)
	if err != nil || workerNice < 0 || workerNice > 19 {
		return nil, fmt.Errorf("CPU worker nice must be an integer from 0 to 19, or inherit")
	}
	return &workerNice, nil
}

func formatWorkerNice(value int) string {
	if value == stress.WorkerNiceInherit {
		return "inherit"
	}
	return strconv.Itoa(value)
}
