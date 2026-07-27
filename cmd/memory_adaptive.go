package cmd

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/fanderchan/loadsim/internal/stress"
)

const (
	defaultAdaptiveMemoryInterval  = 3 * time.Second
	minimumAdaptiveMemoryInterval  = 500 * time.Millisecond
	minimumAdaptiveMemoryBandWidth = 5.0
	maximumAdaptiveMemoryPercent   = 80.0
	maximumAdaptiveMemoryBlockMB   = 64
	requiredLowSamplesBeforeGrowth = 2
)

type adaptiveMemoryConfig struct {
	lowPercent               float64
	highPercent              float64
	maxLoadMB                int
	interval                 time.Duration
	blockMB                  int
	configuredMinAvailableMB uint64
}

type adaptiveMemoryAction string

const (
	adaptiveMemoryHold    adaptiveMemoryAction = "hold"
	adaptiveMemoryConfirm adaptiveMemoryAction = "confirm"
	adaptiveMemoryGrow    adaptiveMemoryAction = "grow"
	adaptiveMemoryShrink  adaptiveMemoryAction = "shrink"
)

type adaptiveMemoryStatus struct {
	lowPercent  float64
	highPercent float64
	observed    float64
	scope       string
	action      adaptiveMemoryAction
	targetMB    int
	hardCapMB   int
	hasSample   bool
}

type adaptiveMemoryDecision struct {
	targetMB   int
	hardCapMB  int
	lowSamples int
	observed   float64
	scope      string
	action     adaptiveMemoryAction
}

type adaptiveMemoryController struct {
	config        adaptiveMemoryConfig
	probe         func() ([]memoryCapacity, error)
	stressor      *stress.RAMStressor
	emergencyStop func() error

	lock            sync.RWMutex
	observationLock sync.RWMutex
	stopOnce        sync.Once
	stopCh          chan struct{}
	errorsCh        chan error
	startupWG       sync.WaitGroup
	wg              sync.WaitGroup
	starting        bool
	started         bool
	stopped         bool
	lowSamples      int
	status          adaptiveMemoryStatus
}

func newAdaptiveMemoryController(
	config adaptiveMemoryConfig,
	stressor *stress.RAMStressor,
	emergencyStop func() error,
) (*adaptiveMemoryController, error) {
	if err := validateAdaptiveMemoryConfig(config); err != nil {
		return nil, err
	}
	if stressor == nil {
		return nil, fmt.Errorf("adaptive memory stressor is not configured")
	}
	ramStatus := stressor.Status()
	if ramStatus.Mode != stress.ModeFixed {
		return nil, fmt.Errorf("adaptive memory requires a fixed-mode RAM stressor")
	}
	if ramStatus.GrowthRateLimitMB <= 0 {
		return nil, fmt.Errorf("adaptive memory requires a positive RAM growth rate limit")
	}
	if ramStatus.ReleaseRateLimitMB <= 0 {
		return nil, fmt.Errorf("adaptive memory requires a positive RAM release rate limit")
	}
	if emergencyStop == nil {
		return nil, fmt.Errorf("adaptive memory emergency stop is not configured")
	}

	return &adaptiveMemoryController{
		config:        config,
		probe:         systemMemoryCapacities,
		stressor:      stressor,
		emergencyStop: emergencyStop,
		stopCh:        make(chan struct{}),
		errorsCh:      make(chan error, 1),
		status: adaptiveMemoryStatus{
			lowPercent:  config.lowPercent,
			highPercent: config.highPercent,
			scope:       "pending",
			action:      adaptiveMemoryHold,
		},
	}, nil
}

func validateAdaptiveMemoryConfig(config adaptiveMemoryConfig) error {
	for name, value := range map[string]float64{
		"minimum": config.lowPercent,
		"maximum": config.highPercent,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("adaptive RAM %s percent must be finite", name)
		}
	}
	if config.lowPercent < 0 ||
		config.highPercent > maximumAdaptiveMemoryPercent ||
		config.lowPercent >= config.highPercent {
		return fmt.Errorf(
			"adaptive RAM range must satisfy 0 <= min < max <= %.0f percent",
			maximumAdaptiveMemoryPercent,
		)
	}
	if config.highPercent-config.lowPercent < minimumAdaptiveMemoryBandWidth {
		return fmt.Errorf(
			"adaptive RAM range must be at least %.0f percentage points wide",
			minimumAdaptiveMemoryBandWidth,
		)
	}
	if config.maxLoadMB <= 0 {
		return fmt.Errorf("adaptive RAM maximum allocation must be greater than zero")
	}
	if config.interval < minimumAdaptiveMemoryInterval {
		return fmt.Errorf(
			"adaptive RAM interval must be at least %s",
			minimumAdaptiveMemoryInterval,
		)
	}
	if config.blockMB <= 0 || config.blockMB > maximumAdaptiveMemoryBlockMB {
		return fmt.Errorf(
			"adaptive RAM block size must be between 1MB and %dMB",
			maximumAdaptiveMemoryBlockMB,
		)
	}
	return nil
}

// Preflight starts adaptive mode from zero, verifies the worst permitted
// LoadSim allocation against every current memory constraint, and returns that
// allocation cap for the independent runtime guard.
func (c *adaptiveMemoryController) Preflight() (int, error) {
	capacities, err := c.probe()
	if err != nil {
		return 0, fmt.Errorf("probe adaptive RAM capacity: %w", err)
	}
	hardCapMB := c.config.maxLoadMB
	if err := validateRAMCapacitySnapshots(
		capacities,
		hardCapMB,
		c.config.blockMB,
		c.config.configuredMinAvailableMB,
		false,
	); err != nil {
		return 0, fmt.Errorf("adaptive RAM cap is unsafe: %w", err)
	}

	c.observationLock.Lock()
	defer c.observationLock.Unlock()
	if err := c.stressor.UpdateTargetMB(0); err != nil {
		return 0, fmt.Errorf("set adaptive RAM startup target: %w", err)
	}

	c.lock.Lock()
	c.status.hardCapMB = hardCapMB
	c.status.targetMB = 0
	c.lock.Unlock()
	return hardCapMB, nil
}

func (c *adaptiveMemoryController) Start() error {
	c.lock.Lock()
	if c.started || c.starting {
		c.lock.Unlock()
		return fmt.Errorf("adaptive memory controller is already started")
	}
	if c.stopped {
		c.lock.Unlock()
		return fmt.Errorf("adaptive memory controller cannot start after Stop")
	}
	c.starting = true
	c.startupWG.Add(1)
	c.lock.Unlock()
	defer func() {
		c.lock.Lock()
		c.starting = false
		c.lock.Unlock()
		c.startupWG.Done()
	}()

	if err := c.adjust(); err != nil {
		return err
	}

	c.lock.Lock()
	if c.stopped {
		c.lock.Unlock()
		stopErr := c.emergencyStop()
		return errors.Join(
			fmt.Errorf("adaptive memory controller stopped during startup"),
			stopErr,
		)
	}
	c.started = true
	c.wg.Add(1)
	c.lock.Unlock()
	go c.run()
	return nil
}

func (c *adaptiveMemoryController) Stop() {
	c.stopOnce.Do(func() {
		c.lock.Lock()
		c.stopped = true
		close(c.stopCh)
		starting := c.starting
		started := c.started
		c.lock.Unlock()

		if starting {
			c.startupWG.Wait()
			c.lock.RLock()
			started = c.started
			c.lock.RUnlock()
		}
		if !started {
			close(c.errorsCh)
			return
		}
		c.wg.Wait()
	})
}

func (c *adaptiveMemoryController) Errors() <-chan error {
	return c.errorsCh
}

func (c *adaptiveMemoryController) Status() adaptiveMemoryStatus {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.status
}

func (c *adaptiveMemoryController) Snapshot() (
	stress.RAMStatus,
	adaptiveMemoryStatus,
) {
	c.observationLock.RLock()
	defer c.observationLock.RUnlock()
	return c.stressor.Status(), c.Status()
}

func stopAdaptiveMemoryController(controller *adaptiveMemoryController) {
	if controller != nil {
		controller.Stop()
	}
}

func adaptiveMemoryErrors(
	controller *adaptiveMemoryController,
) <-chan error {
	if controller == nil {
		return nil
	}
	return controller.Errors()
}

func (c *adaptiveMemoryController) run() {
	defer c.wg.Done()
	defer close(c.errorsCh)

	ticker := time.NewTicker(c.config.interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			if err := c.adjust(); err != nil {
				c.fail(err)
				return
			}
		}
	}
}

func (c *adaptiveMemoryController) fail(controllerErr error) {
	stopErr := c.emergencyStop()
	c.errorsCh <- errors.Join(controllerErr, stopErr)
}

func (c *adaptiveMemoryController) adjust() error {
	capacities, err := c.probe()
	if err != nil {
		return fmt.Errorf("adaptive RAM probe failed: %w", err)
	}

	c.observationLock.Lock()
	defer c.observationLock.Unlock()
	ramStatus := c.stressor.Status()

	c.lock.RLock()
	lowSamples := c.lowSamples
	c.lock.RUnlock()
	decision, err := nextAdaptiveMemoryTarget(
		c.config,
		capacities,
		ramStatus.CurrentMB,
		ramStatus.RequestedMB,
		lowSamples,
	)
	if err != nil {
		return err
	}

	if decision.targetMB > ramStatus.CurrentMB {
		additionalMB := decision.targetMB - ramStatus.CurrentMB
		if err := validateRAMCapacitySnapshots(
			capacities,
			additionalMB,
			c.config.blockMB,
			c.config.configuredMinAvailableMB,
			false,
		); err != nil {
			return fmt.Errorf("adaptive RAM growth is unsafe: %w", err)
		}
	}
	if decision.targetMB != ramStatus.RequestedMB {
		if err := c.stressor.UpdateTargetMB(decision.targetMB); err != nil {
			return fmt.Errorf("update adaptive RAM target: %w", err)
		}
	}

	c.lock.Lock()
	c.lowSamples = decision.lowSamples
	c.status.observed = decision.observed
	c.status.scope = decision.scope
	c.status.action = decision.action
	c.status.targetMB = decision.targetMB
	c.status.hardCapMB = decision.hardCapMB
	c.status.hasSample = true
	c.lock.Unlock()
	return nil
}

func nextAdaptiveMemoryTarget(
	config adaptiveMemoryConfig,
	capacities []memoryCapacity,
	currentMB int,
	requestedMB int,
	lowSamples int,
) (adaptiveMemoryDecision, error) {
	if len(capacities) == 0 {
		return adaptiveMemoryDecision{}, fmt.Errorf(
			"adaptive RAM probe returned no constraints",
		)
	}
	hardCapMB := config.maxLoadMB
	if currentMB < 0 || requestedMB < 0 {
		return adaptiveMemoryDecision{}, fmt.Errorf(
			"adaptive RAM state contains a negative target",
		)
	}

	decision := adaptiveMemoryDecision{
		targetMB:   currentMB,
		hardCapMB:  hardCapMB,
		lowSamples: lowSamples,
		action:     adaptiveMemoryHold,
	}

	allBelowLow := true
	aboveHigh := make([]memoryCapacity, 0, len(capacities))
	for _, capacity := range capacities {
		percent, err := memoryCapacityUsedPercent(capacity)
		if err != nil {
			return adaptiveMemoryDecision{}, err
		}
		if decision.scope == "" || percent > decision.observed {
			decision.observed = percent
			decision.scope = capacity.source
		}
		if percent >= config.lowPercent {
			allBelowLow = false
		}
		if percent > config.highPercent {
			aboveHigh = append(aboveHigh, capacity)
		}
	}

	if currentMB > hardCapMB {
		decision.targetMB = hardCapMB
		decision.action = adaptiveMemoryShrink
		decision.lowSamples = 0
	}

	midpoint := (config.lowPercent + config.highPercent) / 2
	if len(aboveHigh) > 0 {
		targetMB := decision.targetMB
		for _, capacity := range aboveHigh {
			candidate, err := adaptiveTargetForCapacity(
				currentMB,
				capacity,
				midpoint,
			)
			if err != nil {
				return adaptiveMemoryDecision{}, err
			}
			if candidate < targetMB {
				targetMB = candidate
			}
		}
		decision.targetMB = targetMB
		decision.action = adaptiveMemoryShrink
		decision.lowSamples = 0
		return decision, nil
	}

	if decision.action == adaptiveMemoryShrink {
		return decision, nil
	}

	if !allBelowLow {
		decision.lowSamples = 0
		// Cancel pending growth as soon as the system enters the band.
		if requestedMB > currentMB {
			decision.targetMB = currentMB
		}
		return decision, nil
	}

	decision.lowSamples++
	if decision.lowSamples < requiredLowSamplesBeforeGrowth {
		decision.action = adaptiveMemoryConfirm
		if requestedMB > currentMB {
			decision.targetMB = currentMB
		}
		return decision, nil
	}

	targetMB := hardCapMB
	for _, capacity := range capacities {
		candidate, err := adaptiveTargetForCapacity(
			currentMB,
			capacity,
			midpoint,
		)
		if err != nil {
			return adaptiveMemoryDecision{}, err
		}
		if candidate < targetMB {
			targetMB = candidate
		}
	}
	if targetMB > currentMB {
		decision.targetMB = targetMB
		decision.action = adaptiveMemoryGrow
	} else if requestedMB > currentMB {
		decision.targetMB = currentMB
	}
	return decision, nil
}

func adaptiveTargetForCapacity(
	currentMB int,
	capacity memoryCapacity,
	midpointPercent float64,
) (int, error) {
	if capacity.totalMB == 0 {
		return 0, fmt.Errorf(
			"adaptive RAM constraint %s has zero total memory",
			capacity.source,
		)
	}
	midpointMB := math.Round(
		float64(capacity.totalMB) * midpointPercent / 100,
	)
	if midpointMB > float64(math.MaxInt) {
		return 0, fmt.Errorf(
			"adaptive RAM midpoint for %s exceeds addressable memory",
			capacity.source,
		)
	}
	if capacity.usedMB > uint64(math.MaxInt) {
		return 0, nil
	}

	midpoint := int(midpointMB)
	used := int(capacity.usedMB)
	if used >= midpoint {
		reduction := used - midpoint
		if reduction >= currentMB {
			return 0, nil
		}
		return currentMB - reduction, nil
	}

	increase := midpoint - used
	if increase > math.MaxInt-currentMB {
		return 0, fmt.Errorf(
			"adaptive RAM target for %s exceeds addressable memory",
			capacity.source,
		)
	}
	return currentMB + increase, nil
}

func memoryCapacityUsedPercent(capacity memoryCapacity) (float64, error) {
	if capacity.totalMB == 0 {
		return 0, fmt.Errorf(
			"adaptive RAM constraint %s has zero total memory",
			capacity.source,
		)
	}
	percent := float64(capacity.usedMB) * 100 / float64(capacity.totalMB)
	if math.IsNaN(percent) || math.IsInf(percent, 0) {
		return 0, fmt.Errorf(
			"adaptive RAM utilization for %s is not finite",
			capacity.source,
		)
	}
	return percent, nil
}
