package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/fanderchan/loadsim/internal/stress"
	"github.com/fanderchan/loadsim/internal/system"

	"github.com/spf13/cobra"
)

type checkOptions struct {
	active   bool
	json     bool
	sampleMS int
}

type checkCPUReport struct {
	Source        string  `json:"source"`
	BoundaryKind  string  `json:"boundary_kind"`
	BoundaryID    string  `json:"boundary_id"`
	CapacityCPUs  float64 `json:"capacity_cpus"`
	Observed      float64 `json:"observed_percent"`
	SampleMS      int     `json:"sample_ms"`
	IdleScheduler string  `json:"idle_scheduler"`
}

type checkMemoryReport struct {
	Source       string `json:"source"`
	TotalMiB     uint64 `json:"total_mib"`
	UsedMiB      uint64 `json:"used_mib"`
	AvailableMiB uint64 `json:"available_mib"`
}

type checkReport struct {
	CPU    checkCPUReport      `json:"cpu"`
	Memory []checkMemoryReport `json:"memory"`
}

var checkConfig checkOptions

var checkCmd = newCheckCommand(&checkConfig)

func newCheckCommand(options *checkOptions) *cobra.Command {
	command := &cobra.Command{
		Use:   "check",
		Short: "Inspect resource accounting and production prerequisites",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runCheck(*options)
		},
	}
	command.Flags().BoolVar(
		&options.active,
		"active",
		false,
		"actively apply SCHED_IDLE on one disposable thread",
	)
	command.Flags().BoolVar(
		&options.json,
		"json",
		false,
		"print machine-readable JSON",
	)
	command.Flags().IntVar(
		&options.sampleMS,
		"sample-ms",
		1000,
		"CPU accounting sample duration in milliseconds",
	)
	return command
}

func init() {
	rootCmd.AddCommand(checkCmd)
}

func runCheck(options checkOptions) error {
	sampleDuration, err := milliseconds(
		options.sampleMS,
		"CPU sample duration",
		false,
	)
	if err != nil {
		return err
	}
	cpuInfo, err := stress.InspectVisibleSystemCPU()
	if err != nil {
		return fmt.Errorf("inspect visible system CPU: %w", err)
	}
	sample, err := stress.SampleVisibleSystemCPU(
		context.Background(),
		sampleDuration,
	)
	if err != nil {
		return fmt.Errorf("sample visible system CPU: %w", err)
	}
	if sample.Source != cpuInfo.Source ||
		sample.BoundaryID != cpuInfo.BoundaryID ||
		sample.BoundaryKind != cpuInfo.BoundaryKind ||
		sample.CPUs != cpuInfo.CPUs {
		return fmt.Errorf("visible system CPU boundary changed during check")
	}

	idleScheduler := "not-tested"
	if options.active {
		if err := stress.ProbeCPUWorkerScheduler(
			stress.WorkerSchedulerIdle,
		); err != nil {
			return err
		}
		idleScheduler = "supported"
	}
	capacities, err := system.MemoryCapacities()
	if err != nil {
		return fmt.Errorf("inspect memory capacity: %w", err)
	}
	report := checkReport{
		CPU: checkCPUReport{
			Source:        cpuInfo.Source,
			BoundaryKind:  string(cpuInfo.BoundaryKind),
			BoundaryID:    cpuInfo.BoundaryID,
			CapacityCPUs:  cpuInfo.CPUs,
			Observed:      sample.Percent,
			SampleMS:      options.sampleMS,
			IdleScheduler: idleScheduler,
		},
		Memory: make([]checkMemoryReport, 0, len(capacities)),
	}
	for _, capacity := range capacities {
		report.Memory = append(report.Memory, checkMemoryReport{
			Source:       capacity.Source,
			TotalMiB:     capacity.TotalMB,
			UsedMiB:      capacity.UsedMB,
			AvailableMiB: capacity.AvailableMB,
		})
	}

	if options.json {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}

	fmt.Printf(
		"cpu source=%s boundary=%s capacity=%.2fCPU observed=%.1f%% sample=%s sched_idle=%s\n",
		report.CPU.Source,
		formatCPUBoundary(
			stress.VisibleSystemCPUBoundaryKind(report.CPU.BoundaryKind),
			report.CPU.BoundaryID,
		),
		report.CPU.CapacityCPUs,
		report.CPU.Observed,
		time.Duration(report.CPU.SampleMS)*time.Millisecond,
		report.CPU.IdleScheduler,
	)
	for _, memory := range report.Memory {
		fmt.Printf(
			"memory source=%s total=%dMiB used=%dMiB available=%dMiB\n",
			memory.Source,
			memory.TotalMiB,
			memory.UsedMiB,
			memory.AvailableMiB,
		)
	}
	if !options.active {
		fmt.Println(
			"note: run \"loadsim check --active\" to verify SCHED_IDLE support",
		)
	}
	return nil
}
