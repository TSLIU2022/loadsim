package cmd

import (
	"errors"
	"fmt"
	"time"

	"github.com/fanderchan/loadsim/internal/stress"
)

func stopResourcesImmediately(
	ram *stress.RAMStressor,
	cpu *stress.CPUStressor,
) error {
	var ramErr error
	if ram != nil {
		ramErr = ram.Stop()
	}
	var cpuErr error
	if cpu != nil {
		cpuErr = cpu.Stop()
	}
	return joinErrors(ramErr, cpuErr)
}

func releaseRAMGradually(
	stressor *stress.RAMStressor,
	statusInterval time.Duration,
	printStatus func(),
	ramErrors <-chan error,
	safetyErrors <-chan error,
) error {
	if stressor == nil {
		return nil
	}
	status := stressor.Status()
	if status.CurrentMB == 0 {
		return nil
	}
	if err := stressor.UpdateTargetMB(0); err != nil {
		return fmt.Errorf("request graceful RAM release: %w", err)
	}

	releaseRate := status.ReleaseRateLimitMB
	timeout := 30 * time.Second
	if releaseRate > 0 {
		secondsNeeded := int64(status.CurrentMB/releaseRate) + 1
		if secondsNeeded <= (int64(^uint64(0)>>1)-30) &&
			secondsNeeded+30 <= int64((24*time.Hour)/time.Second) {
			timeout = time.Duration(secondsNeeded+30) * time.Second
		} else {
			timeout = 24 * time.Hour
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	statusTicker := time.NewTicker(statusInterval)
	defer statusTicker.Stop()

	for {
		select {
		case <-ticker.C:
			if stressor.Status().CurrentMB == 0 {
				return nil
			}
		case <-statusTicker.C:
			printStatus()
		case err, ok := <-ramErrors:
			if !ok {
				ramErrors = nil
				continue
			}
			if err != nil {
				return err
			}
		case err, ok := <-safetyErrors:
			if !ok {
				safetyErrors = nil
				continue
			}
			if err != nil {
				return err
			}
		case <-timer.C:
			return errors.New("timed out while releasing RAM gradually")
		}
	}
}
