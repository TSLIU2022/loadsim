package cmd

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/fanderchan/loadsim/internal/system"
)

type memoryCapacity struct {
	availableMB uint64
	totalMB     uint64
	usedMB      uint64
	source      string
}

type memorySafetySnapshot struct {
	availableMB        uint64
	minimumAvailableMB uint64
	source             string
}

var systemMemoryCapacities = func() ([]memoryCapacity, error) {
	capacities, err := system.MemoryCapacities()
	if err != nil {
		return nil, err
	}
	result := make([]memoryCapacity, 0, len(capacities))
	for _, capacity := range capacities {
		result = append(result, memoryCapacity{
			availableMB: capacity.AvailableMB,
			totalMB:     capacity.TotalMB,
			usedMB:      capacity.UsedMB,
			source:      capacity.Source,
		})
	}
	return result, nil
}

var setOOMScoreAdjustment = system.SetOOMScoreAdj

const (
	defaultMemoryCheckInterval = 100 * time.Millisecond
	minimumMemoryCheckInterval = 10 * time.Millisecond
	autoMemoryFloorMB          = uint64(128)
)

type memoryGuard struct {
	targetMB                 int
	blockMB                  int
	configuredMinAvailableMB uint64
	checkInterval            time.Duration
	force                    bool
	emergencyStop            func() error

	lock     sync.Mutex
	started  bool
	stopped  bool
	stopOnce sync.Once
	stopCh   chan struct{}
	errorsCh chan error
	wg       sync.WaitGroup
}

func watchLoop(
	duration time.Duration,
	statusInterval time.Duration,
	printStatus func(),
	primaryErrors <-chan error,
	secondaryErrors <-chan error,
	safetyErrors <-chan error,
	controllerErrors <-chan error,
) (string, error) {
	if statusInterval <= 0 {
		statusInterval = 2 * time.Second
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	ticker := time.NewTicker(statusInterval)
	defer ticker.Stop()

	var timerC <-chan time.Time
	var timer *time.Timer
	if duration > 0 {
		timer = time.NewTimer(duration)
		defer timer.Stop()
		timerC = timer.C
	}

	printStatus()

	for {
		select {
		case <-ticker.C:
			printStatus()
		case sig := <-signals:
			return sig.String(), nil
		case <-timerC:
			return "time limit reached", nil
		case err, ok := <-primaryErrors:
			if !ok {
				primaryErrors = nil
				continue
			}
			if err != nil {
				return "", err
			}
		case err, ok := <-secondaryErrors:
			if !ok {
				secondaryErrors = nil
				continue
			}
			if err != nil {
				return "", err
			}
		case err, ok := <-safetyErrors:
			if !ok {
				safetyErrors = nil
				continue
			}
			if err != nil {
				return "", err
			}
		case err, ok := <-controllerErrors:
			if !ok {
				controllerErrors = nil
				continue
			}
			if err != nil {
				return "", err
			}
		}
	}
}

func seconds(value int, name string, allowZero bool) (time.Duration, error) {
	return checkedDuration(value, int64(time.Second), name, allowZero)
}

func milliseconds(value int, name string, allowZero bool) (time.Duration, error) {
	return checkedDuration(value, int64(time.Millisecond), name, allowZero)
}

func checkedDuration(value int, unit int64, name string, allowZero bool) (time.Duration, error) {
	if value < 0 || (!allowZero && value == 0) {
		if allowZero {
			return 0, fmt.Errorf("%s must be greater than or equal to zero", name)
		}
		return 0, fmt.Errorf("%s must be greater than zero", name)
	}
	if int64(value) > math.MaxInt64/unit {
		return 0, fmt.Errorf("%s is too large", name)
	}
	return time.Duration(int64(value) * unit), nil
}

func validateCLIControllerTuning(deadbandPercent, maxStepPercent float64) error {
	if deadbandPercent == 0 {
		return fmt.Errorf("CPU deadband percent must be greater than zero")
	}
	if maxStepPercent == 0 {
		return fmt.Errorf("CPU max step percent must be greater than zero")
	}
	return nil
}

func validateRAMCapacity(targetMB int, force bool) error {
	capacities, err := systemMemoryCapacities()
	if err != nil {
		return fmt.Errorf("check available memory: %w", err)
	}
	return validateRAMCapacitySnapshots(
		capacities,
		targetMB,
		16,
		0,
		force,
	)
}

func newMemoryGuard(
	targetMB int,
	blockMB int,
	configuredMinAvailableMB int,
	checkInterval time.Duration,
	force bool,
	emergencyStop func() error,
) (*memoryGuard, error) {
	if configuredMinAvailableMB < 0 {
		return nil, fmt.Errorf("minimum available memory must not be negative")
	}
	if checkInterval < minimumMemoryCheckInterval {
		return nil, fmt.Errorf(
			"memory check interval must be at least %s",
			minimumMemoryCheckInterval,
		)
	}

	guard := &memoryGuard{
		targetMB:                 targetMB,
		blockMB:                  blockMB,
		configuredMinAvailableMB: uint64(configuredMinAvailableMB),
		checkInterval:            checkInterval,
		force:                    force,
		emergencyStop:            emergencyStop,
		stopCh:                   make(chan struct{}),
		errorsCh:                 make(chan error, 1),
	}
	if err := guard.preflight(); err != nil {
		return nil, err
	}
	return guard, nil
}

func (g *memoryGuard) Start() error {
	g.lock.Lock()
	defer g.lock.Unlock()

	if g.started {
		return fmt.Errorf("memory guard is already started")
	}
	if g.stopped {
		return fmt.Errorf("memory guard cannot start after Stop")
	}
	if err := g.preflight(); err != nil {
		return err
	}
	g.started = true
	g.wg.Add(1)
	go g.run()
	return nil
}

func (g *memoryGuard) Errors() <-chan error {
	return g.errorsCh
}

func (g *memoryGuard) Stop() {
	g.stopOnce.Do(func() {
		g.lock.Lock()
		g.stopped = true
		close(g.stopCh)
		if !g.started {
			close(g.errorsCh)
			g.lock.Unlock()
			return
		}
		g.lock.Unlock()
		g.wg.Wait()
	})
}

func (g *memoryGuard) preflight() error {
	capacities, err := systemMemoryCapacities()
	if err != nil {
		return fmt.Errorf("check available memory: %w", err)
	}
	return validateRAMCapacitySnapshots(
		capacities,
		g.targetMB,
		g.blockMB,
		g.configuredMinAvailableMB,
		g.force,
	)
}

func (g *memoryGuard) run() {
	defer g.wg.Done()
	defer close(g.errorsCh)

	ticker := time.NewTicker(g.checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-g.stopCh:
			return
		case <-ticker.C:
			if err := g.check(); err != nil {
				g.fail(err)
				return
			}
		}
	}
}

func (g *memoryGuard) fail(guardErr error) {
	var stopErr error
	if g.emergencyStop != nil {
		stopErr = g.emergencyStop()
	}
	g.errorsCh <- joinErrors(guardErr, stopErr)
}

func (g *memoryGuard) check() error {
	capacities, err := systemMemoryCapacities()
	if err != nil {
		return fmt.Errorf("memory safety probe failed: %w", err)
	}
	if len(capacities) == 0 {
		return fmt.Errorf("memory safety probe returned no constraints")
	}
	for _, capacity := range capacities {
		thresholdMB := minimumAvailableMemoryMB(
			capacity.totalMB,
			g.configuredMinAvailableMB,
		)
		if capacity.availableMB > thresholdMB {
			continue
		}
		return fmt.Errorf(
			"memory safety guard triggered: %s available memory %dMB is at or below the %dMB threshold",
			capacity.source,
			capacity.availableMB,
			thresholdMB,
		)
	}
	return nil
}

func (g *memoryGuard) safetySnapshot() (memorySafetySnapshot, error) {
	capacities, err := systemMemoryCapacities()
	if err != nil {
		return memorySafetySnapshot{}, fmt.Errorf("memory safety probe failed: %w", err)
	}
	return selectMemorySafetySnapshot(
		capacities,
		g.configuredMinAvailableMB,
	)
}

func selectMemorySafetySnapshot(
	capacities []memoryCapacity,
	configuredMinAvailableMB uint64,
) (memorySafetySnapshot, error) {
	if len(capacities) == 0 {
		return memorySafetySnapshot{}, fmt.Errorf(
			"memory safety probe returned no constraints",
		)
	}

	snapshotFor := func(capacity memoryCapacity) memorySafetySnapshot {
		return memorySafetySnapshot{
			availableMB: capacity.availableMB,
			minimumAvailableMB: minimumAvailableMemoryMB(
				capacity.totalMB,
				configuredMinAvailableMB,
			),
			source: capacity.source,
		}
	}
	selected := snapshotFor(capacities[0])
	for _, capacity := range capacities[1:] {
		candidate := snapshotFor(capacity)
		if memorySnapshotIsMoreConstrained(candidate, selected) {
			selected = candidate
		}
	}
	return selected, nil
}

func memorySnapshotIsMoreConstrained(
	candidate memorySafetySnapshot,
	selected memorySafetySnapshot,
) bool {
	candidateUnsafe := candidate.availableMB <= candidate.minimumAvailableMB
	selectedUnsafe := selected.availableMB <= selected.minimumAvailableMB
	if candidateUnsafe != selectedUnsafe {
		return candidateUnsafe
	}
	if candidateUnsafe {
		candidateDeficit := candidate.minimumAvailableMB - candidate.availableMB
		selectedDeficit := selected.minimumAvailableMB - selected.availableMB
		return candidateDeficit > selectedDeficit
	}
	candidateHeadroom := candidate.availableMB - candidate.minimumAvailableMB
	selectedHeadroom := selected.availableMB - selected.minimumAvailableMB
	return candidateHeadroom < selectedHeadroom
}

func validateRAMCapacitySnapshot(
	capacity memoryCapacity,
	targetMB int,
	blockMB int,
	configuredMinAvailableMB uint64,
	force bool,
) error {
	return validateRAMCapacitySnapshots(
		[]memoryCapacity{capacity},
		targetMB,
		blockMB,
		configuredMinAvailableMB,
		force,
	)
}

func validateRAMCapacitySnapshots(
	capacities []memoryCapacity,
	targetMB int,
	blockMB int,
	configuredMinAvailableMB uint64,
	force bool,
) error {
	if targetMB < 0 {
		return fmt.Errorf("RAM target must not be negative")
	}
	if blockMB < 0 {
		return fmt.Errorf("RAM block size must not be negative")
	}
	if len(capacities) == 0 {
		return fmt.Errorf("cannot start: memory safety probe returned no constraints")
	}

	for _, capacity := range capacities {
		thresholdMB := minimumAvailableMemoryMB(
			capacity.totalMB,
			configuredMinAvailableMB,
		)
		if capacity.availableMB <= thresholdMB {
			return fmt.Errorf(
				"cannot start: %s available memory %dMB is at or below the %dMB safety threshold",
				capacity.source,
				capacity.availableMB,
				thresholdMB,
			)
		}
	}
	if force {
		return nil
	}

	for _, capacity := range capacities {
		thresholdMB := minimumAvailableMemoryMB(
			capacity.totalMB,
			configuredMinAvailableMB,
		)
		headroomMB := memoryStartupHeadroomMB(thresholdMB, uint64(blockMB))
		reserveMB := saturatingAddUint64(thresholdMB, headroomMB)
		safeBudgetMB := uint64(0)
		if capacity.availableMB > reserveMB {
			safeBudgetMB = capacity.availableMB - reserveMB
		}
		if uint64(targetMB) <= safeBudgetMB {
			continue
		}
		return fmt.Errorf(
			"RAM target %dMB exceeds the safe %s startup budget %dMB "+
				"(available %dMB, runtime threshold %dMB, startup headroom %dMB); "+
				"lower the target or use --force",
			targetMB,
			capacity.source,
			safeBudgetMB,
			capacity.availableMB,
			thresholdMB,
			headroomMB,
		)
	}
	return nil
}

func minimumAvailableMemoryMB(totalMB, configuredMB uint64) uint64 {
	if configuredMB > 0 {
		return configuredMB
	}

	thresholdMB := ceilPercent(totalMB, 10)
	if thresholdMB < autoMemoryFloorMB {
		thresholdMB = autoMemoryFloorMB
	}
	if quarter := totalMB / 4; quarter > 0 && thresholdMB > quarter {
		thresholdMB = quarter
	}
	if thresholdMB == 0 {
		return 1
	}
	return thresholdMB
}

func memoryStartupHeadroomMB(thresholdMB, blockMB uint64) uint64 {
	headroomMB := thresholdMB / 4
	if headroomMB < 16 {
		headroomMB = 16
	}
	if blockMB > headroomMB {
		headroomMB = blockMB
	}
	return headroomMB
}

func ceilPercent(value, percent uint64) uint64 {
	quotient := value / 100
	remainder := value % 100
	result := quotient * percent
	if remainder == 0 {
		return result
	}
	return saturatingAddUint64(result, (remainder*percent+99)/100)
}

func saturatingAddUint64(left, right uint64) uint64 {
	if math.MaxUint64-left < right {
		return math.MaxUint64
	}
	return left + right
}

func validateOOMScoreAdj(value int) error {
	if value == -1 {
		return nil
	}
	if value < 0 || value > 1000 {
		return fmt.Errorf("OOM score adjustment must be -1 (inherit) or between 0 and 1000")
	}
	return nil
}

func applyOOMScoreAdj(value int) error {
	if err := validateOOMScoreAdj(value); err != nil {
		return err
	}
	if value == -1 {
		return nil
	}
	if err := setOOMScoreAdjustment(value); err != nil {
		return fmt.Errorf("set OOM score adjustment: %w", err)
	}
	return nil
}

func formatOOMScoreAdj(value int) string {
	if value == -1 {
		return "inherit"
	}
	return strconv.Itoa(value)
}

// drainClosedErrors must only be called after the channel owners have stopped
// and closed their error channels.
func drainClosedErrors(channels ...<-chan error) error {
	var drained []error
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		for err := range channel {
			if err != nil {
				drained = append(drained, err)
			}
		}
	}
	return joinErrors(drained...)
}

func joinErrors(errs ...error) error {
	filtered := make([]error, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			filtered = append(filtered, err)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return errors.Join(filtered...)
}
