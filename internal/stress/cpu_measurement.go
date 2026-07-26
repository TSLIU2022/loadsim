package stress

import (
	"context"
	"fmt"
	"math"
	"time"
)

const (
	cpuConsistencyMinWall         = 400 * time.Millisecond
	cpuConsistencyMinProcessCPU   = 100 * time.Millisecond
	cpuConsistencyMaxWindow       = 2 * time.Second
	cpuConsistencyAbsoluteSlack   = 40 * time.Millisecond
	cpuConsistencyRelativeSlack   = 0.25
	cpuConsistencyRequiredWindows = 2
)

type processCPUSnapshot struct {
	totalCPU   time.Duration
	observedAt time.Time
}

type processCPUSampler func(context.Context) (processCPUSnapshot, error)

type cpuMeasurementSample struct {
	scope        CPUScope
	hostPercent  float64
	hostCPUs     float64
	maxPercent   float64
	scopeBusy    time.Duration
	scopeElapsed time.Duration
	hasScopeRaw  bool
	minimumWall  time.Duration
	processStart processCPUSnapshot
	processEnd   processCPUSnapshot
}

type cpuConsistencyMonitor struct {
	windowWall       time.Duration
	windowProcessCPU time.Duration
	windowHostCPU    time.Duration
	suspectWindows   int
}

func (m *cpuConsistencyMonitor) observe(sample cpuMeasurementSample) error {
	wall, processCPU, hostCPU, err := normalizeCPUMeasurement(sample)
	if err != nil {
		return err
	}

	if !addDuration(&m.windowWall, wall) ||
		!addDuration(&m.windowProcessCPU, processCPU) ||
		!addDuration(&m.windowHostCPU, hostCPU) {
		return fmt.Errorf("CPU accounting window overflow")
	}

	if m.windowWall < cpuConsistencyMinWall ||
		m.windowProcessCPU < cpuConsistencyMinProcessCPU {
		if m.windowWall >= cpuConsistencyMaxWindow {
			m.resetWindow()
			m.suspectWindows = 0
		}
		return nil
	}

	windowWall := m.windowWall
	windowProcessCPU := m.windowProcessCPU
	windowHostCPU := m.windowHostCPU
	m.resetWindow()

	excess := windowProcessCPU - windowHostCPU
	allowedExcess := cpuConsistencyAbsoluteSlack +
		time.Duration(float64(windowProcessCPU)*cpuConsistencyRelativeSlack)
	if excess <= allowedExcess {
		m.suspectWindows = 0
		return nil
	}

	m.suspectWindows++
	if m.suspectWindows < cpuConsistencyRequiredWindows {
		return nil
	}

	scope := sample.scope
	if scope == "" {
		scope = ScopeHost
	}
	return fmt.Errorf(
		"CPU accounting mismatch: process used %.3fs CPU but %s samples accounted for only %.3fs busy CPU over %.3fs; refusing %s-scope control",
		windowProcessCPU.Seconds(),
		scope,
		windowHostCPU.Seconds(),
		windowWall.Seconds(),
		scope,
	)
}

func normalizeCPUMeasurement(sample cpuMeasurementSample) (time.Duration, time.Duration, time.Duration, error) {
	if !isFinite(sample.hostCPUs) || sample.hostCPUs <= 0 {
		return 0, 0, 0, fmt.Errorf("CPU accounting scope capacity must be greater than zero")
	}
	maxPercent := 100.0
	if sample.scope == ScopeSystem && sample.maxPercent > 100 {
		maxPercent = sample.maxPercent
	}
	if !isFinite(maxPercent) || maxPercent < 100 {
		return 0, 0, 0, fmt.Errorf("CPU accounting percent ceiling is invalid")
	}
	if !isFinite(sample.hostPercent) ||
		sample.hostPercent < 0 ||
		sample.hostPercent > maxPercent {
		return 0, 0, 0, fmt.Errorf(
			"CPU accounting received invalid scope percent %v (maximum %.3f)",
			sample.hostPercent,
			maxPercent,
		)
	}
	if sample.processStart.totalCPU < 0 || sample.processEnd.totalCPU < 0 {
		return 0, 0, 0, fmt.Errorf("CPU accounting received negative process CPU time")
	}
	if sample.processEnd.totalCPU < sample.processStart.totalCPU {
		return 0, 0, 0, fmt.Errorf("process CPU time moved backwards")
	}

	wall := sample.processEnd.observedAt.Sub(sample.processStart.observedAt)
	if wall <= 0 {
		return 0, 0, 0, fmt.Errorf("CPU accounting interval must be greater than zero")
	}
	if sample.minimumWall < 0 {
		return 0, 0, 0, fmt.Errorf("CPU accounting minimum interval must not be negative")
	}
	if wall < sample.minimumWall {
		return 0, 0, 0, fmt.Errorf(
			"CPU accounting sample interval %s was shorter than the safe minimum %s",
			wall,
			sample.minimumWall,
		)
	}
	processCPU := sample.processEnd.totalCPU - sample.processStart.totalCPU
	if sample.hasScopeRaw {
		if sample.scope != ScopeSystem {
			return 0, 0, 0, fmt.Errorf(
				"raw CPU accounting is only valid for system scope",
			)
		}
		if sample.scopeBusy < 0 || sample.scopeElapsed <= 0 {
			return 0, 0, 0, fmt.Errorf(
				"raw system CPU accounting durations are invalid",
			)
		}
		if sample.scopeElapsed < sample.minimumWall {
			return 0, 0, 0, fmt.Errorf(
				"raw system CPU accounting interval %s was shorter than the safe minimum %s",
				sample.scopeElapsed,
				sample.minimumWall,
			)
		}
		if sample.scopeElapsed > wall {
			return 0, 0, 0, fmt.Errorf(
				"raw system CPU accounting interval %s exceeded the enclosing process interval %s",
				sample.scopeElapsed,
				wall,
			)
		}
		rawPercent := sample.scopeBusy.Seconds() /
			(sample.scopeElapsed.Seconds() * sample.hostCPUs) *
			100
		tolerance := math.Max(0.000001, math.Abs(sample.hostPercent)*0.000000001)
		if !isFinite(rawPercent) ||
			math.Abs(rawPercent-sample.hostPercent) > tolerance {
			return 0, 0, 0, fmt.Errorf(
				"raw system CPU accounting percent %.6f contradicts reported percent %.6f",
				rawPercent,
				sample.hostPercent,
			)
		}
		return wall, processCPU, sample.scopeBusy, nil
	}

	hostCPUSeconds := wall.Seconds() * float64(sample.hostCPUs) * sample.hostPercent / 100
	if !isFinite(hostCPUSeconds) || hostCPUSeconds < 0 ||
		hostCPUSeconds > float64(math.MaxInt64)/float64(time.Second) {
		return 0, 0, 0, fmt.Errorf("CPU accounting busy time is out of range")
	}
	hostCPU := time.Duration(math.Round(hostCPUSeconds * float64(time.Second)))
	return wall, processCPU, hostCPU, nil
}

func addDuration(total *time.Duration, value time.Duration) bool {
	if value < 0 || *total > time.Duration(math.MaxInt64)-value {
		return false
	}
	*total += value
	return true
}

func (m *cpuConsistencyMonitor) resetWindow() {
	m.windowWall = 0
	m.windowProcessCPU = 0
	m.windowHostCPU = 0
}
