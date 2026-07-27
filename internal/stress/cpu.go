package stress

import (
	"context"
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
)

const (
	dutyScale                  = 10000
	cpuCapacityRecheckInterval = time.Second
)

const defaultWorkerNice = 19

// WorkerNiceInherit explicitly keeps the scheduling priority inherited by a
// worker thread. A nil CPUConfig.WorkerNice uses the safe default nice value 19.
const WorkerNiceInherit = 20

type CPUWorkerScheduler string

const (
	WorkerSchedulerNormal CPUWorkerScheduler = "normal"
	WorkerSchedulerIdle   CPUWorkerScheduler = "idle"
)

type CPUScope string

const (
	ScopeWorkers CPUScope = "workers"
	ScopeHost    CPUScope = "host"
	ScopeSystem  CPUScope = "system"
)

type CPUIdleMode string

const (
	IdleModePark CPUIdleMode = "park"
	IdleModeTrim CPUIdleMode = "trim"
)

type CPUConfig struct {
	Mode            Mode
	Scope           CPUScope
	IdleMode        CPUIdleMode
	Percent         float64
	MinPercent      float64
	MaxPercent      float64
	Period          time.Duration
	Cores           int
	Cycle           time.Duration
	ControlInterval time.Duration
	SampleDuration  time.Duration
	DeadbandPercent float64
	MaxStepPercent  float64
	WorkerScheduler CPUWorkerScheduler
	WorkerNice      *int // nil defaults to 19; point to WorkerNiceInherit to disable adjustment
}

type CPUStatus struct {
	Mode                        Mode
	Scope                       CPUScope
	IdleMode                    CPUIdleMode
	WorkerScheduler             CPUWorkerScheduler
	WorkerNice                  int
	ActiveWorkers               int
	MaxWorkers                  int
	HostCPUs                    int
	ProcessCPUs                 float64
	ScopeCPUs                   float64
	MaxReachableHostPercent     float64
	MaxScopeContributionPercent float64
	AccountingSource            string
	AccountingBoundaryID        string
	AccountingBoundaryKind      VisibleSystemCPUBoundaryKind
	RequestedPercent            float64
	RequestedBandLowPercent     float64
	RequestedBandHighPercent    float64
	AppliedPercent              float64
	LastHostPercent             float64
	LastScopePercent            float64
	HasHostSample               bool
	HasScopeSample              bool
}

type cpuLifecycle uint8

const (
	cpuLifecycleNew cpuLifecycle = iota
	cpuLifecycleRunning
	cpuLifecycleFailed
	cpuLifecycleStopped
)

type cpuCapacity struct {
	hostCPUs     int
	affinityCPUs int
	gomaxprocs   int
	quotaCPUs    float64
	quotaLimited bool
	processCPUs  float64
	maxWorkers   int
}

type cpuCapacityDetector func() (cpuCapacity, error)
type hostCPUSampler func(context.Context, time.Duration) (float64, error)
type visibleSystemCPUInspector func() (VisibleSystemCPUInfo, error)
type visibleSystemCPUSampler func(
	context.Context,
	time.Duration,
) (VisibleSystemCPUSample, error)
type workerPrioritySetup func(int) error
type workerSchedulerSetup func(CPUWorkerScheduler) error

type CPUStressor struct {
	config CPUConfig

	lock             sync.RWMutex
	stopOnce         sync.Once
	stopCh           chan struct{}
	context          context.Context
	cancel           context.CancelFunc
	errors           chan error
	workerWG         sync.WaitGroup
	controllerWG     sync.WaitGroup
	workers          []*cpuWorker
	lifecycle        cpuLifecycle
	capacity         cpuCapacity
	detectCapacity   cpuCapacityDetector
	capacityInterval time.Duration
	sampleHostCPU    hostCPUSampler
	sampleProcessCPU processCPUSampler
	inspectSystemCPU visibleSystemCPUInspector
	sampleSystemCPU  visibleSystemCPUSampler
	scopeCPUs        float64
	accountingSource string
	boundaryID       string
	boundaryKind     VisibleSystemCPUBoundaryKind
	cpuConsistency   cpuConsistencyMonitor
	workerScheduler  CPUWorkerScheduler
	workerNice       int
	setupScheduler   workerSchedulerSetup
	setupWorkerNice  workerPrioritySetup
	startedAt        time.Time
	requestedPercent float64
	appliedPercent   float64
	lastHostPercent  float64
	hasHostSample    bool
}

type cpuWorker struct {
	duty atomic.Uint32
	stop chan struct{}
}

func NewCPUStressor(config CPUConfig) (*CPUStressor, error) {
	return newCPUStressor(config, detectCPUCapacity, sampleHostCPUPercent)
}

// ProbeCPUWorkerScheduler applies and verifies a scheduling policy on a
// disposable locked OS thread. The thread exits after the probe so a
// low-priority policy cannot leak back into the Go runtime thread pool.
func ProbeCPUWorkerScheduler(scheduler CPUWorkerScheduler) error {
	if scheduler != WorkerSchedulerNormal &&
		scheduler != WorkerSchedulerIdle {
		return fmt.Errorf("CPU worker scheduler must be normal or idle")
	}
	result := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		result <- platformWorkerSchedulerSetup(scheduler)
	}()
	if err := <-result; err != nil {
		return fmt.Errorf("probe CPU worker scheduler %s: %w", scheduler, err)
	}
	return nil
}

func newCPUStressor(
	config CPUConfig,
	detectCapacity cpuCapacityDetector,
	sampleHostCPU hostCPUSampler,
) (*CPUStressor, error) {
	return newCPUStressorWithSystemAccounting(
		config,
		detectCapacity,
		sampleHostCPU,
		InspectVisibleSystemCPU,
		SampleVisibleSystemCPU,
	)
}

func newCPUStressorWithSystemAccounting(
	config CPUConfig,
	detectCapacity cpuCapacityDetector,
	sampleHostCPU hostCPUSampler,
	inspectSystemCPU visibleSystemCPUInspector,
	sampleSystemCPU visibleSystemCPUSampler,
) (*CPUStressor, error) {
	if config.Cores < 0 {
		return nil, fmt.Errorf("worker core count must not be negative")
	}
	if config.Cycle < 0 {
		return nil, fmt.Errorf("CPU cycle must not be negative")
	}
	if config.ControlInterval < 0 {
		return nil, fmt.Errorf("CPU control interval must not be negative")
	}
	if config.SampleDuration < 0 {
		return nil, fmt.Errorf("CPU sample duration must not be negative")
	}
	if !isFinite(config.DeadbandPercent) || config.DeadbandPercent < 0 || config.DeadbandPercent > 100 {
		return nil, fmt.Errorf("CPU deadband percent must be finite and between 0 and 100")
	}
	if !isFinite(config.MaxStepPercent) || config.MaxStepPercent < 0 || config.MaxStepPercent > 100 {
		return nil, fmt.Errorf("CPU max step percent must be finite and between 0 and 100")
	}
	workerNice, err := normalizeWorkerNice(config.WorkerNice)
	if err != nil {
		return nil, err
	}
	if config.WorkerScheduler == "" {
		config.WorkerScheduler = WorkerSchedulerNormal
	}
	if config.WorkerScheduler != WorkerSchedulerNormal &&
		config.WorkerScheduler != WorkerSchedulerIdle {
		return nil, fmt.Errorf("CPU worker scheduler must be normal or idle")
	}

	if config.Cycle == 0 {
		config.Cycle = 100 * time.Millisecond
	}
	if config.ControlInterval == 0 {
		config.ControlInterval = 250 * time.Millisecond
	}
	if config.SampleDuration == 0 {
		config.SampleDuration = 200 * time.Millisecond
	}
	if config.DeadbandPercent == 0 {
		config.DeadbandPercent = 1.0
	}
	if config.MaxStepPercent == 0 {
		config.MaxStepPercent = 10.0
	}
	if config.Scope == "" {
		config.Scope = ScopeWorkers
	}
	if config.IdleMode == "" {
		config.IdleMode = IdleModePark
	}
	if config.Scope != ScopeWorkers &&
		config.Scope != ScopeHost &&
		config.Scope != ScopeSystem {
		return nil, fmt.Errorf("CPU scope must be workers, host, or system")
	}
	if config.IdleMode != IdleModePark && config.IdleMode != IdleModeTrim {
		return nil, fmt.Errorf("CPU idle mode must be park or trim")
	}

	switch config.Mode {
	case ModeFixed:
		if !isFinite(config.Percent) || config.Percent < 0 || config.Percent > 100 {
			return nil, fmt.Errorf("CPU percent must be finite and between 0 and 100")
		}
	case ModeWave:
		if !isFinite(config.MinPercent) || !isFinite(config.MaxPercent) ||
			config.MinPercent < 0 || config.MaxPercent > 100 {
			return nil, fmt.Errorf("CPU wave percent must be finite and stay between 0 and 100")
		}
		if config.MinPercent > config.MaxPercent {
			return nil, fmt.Errorf("CPU min percent must be less than or equal to max percent")
		}
		if config.Period <= 0 {
			return nil, fmt.Errorf("CPU wave period must be greater than zero")
		}
	default:
		return nil, fmt.Errorf("unsupported CPU mode %q", config.Mode)
	}

	if detectCapacity == nil {
		return nil, fmt.Errorf("CPU capacity detector is not configured")
	}
	capacity, err := detectCapacity()
	if err != nil {
		return nil, fmt.Errorf("detect CPU capacity: %w", err)
	}
	if err := validateCPUCapacity(capacity); err != nil {
		return nil, err
	}

	if config.Cores == 0 {
		config.Cores = capacity.maxWorkers
	}
	if config.Cores > capacity.maxWorkers {
		return nil, fmt.Errorf(
			"worker core count %d exceeds the process scheduling limit %d",
			config.Cores,
			capacity.maxWorkers,
		)
	}
	if config.Cores <= 0 {
		return nil, fmt.Errorf("worker core count must be greater than zero")
	}

	scopeCPUs := float64(capacity.hostCPUs)
	accountingSource := "proc-stat"
	var boundaryID string
	var boundaryKind VisibleSystemCPUBoundaryKind
	if config.Scope == ScopeWorkers {
		scopeCPUs = effectiveWorkerCapacity(config.Cores, capacity)
		accountingSource = "workers"
	}
	if config.Scope == ScopeSystem {
		if inspectSystemCPU == nil || sampleSystemCPU == nil {
			return nil, fmt.Errorf("visible system CPU accounting is not configured")
		}
		info, err := inspectSystemCPU()
		if err != nil {
			return nil, fmt.Errorf("inspect visible system CPU boundary: %w", err)
		}
		if info.Source == "" ||
			info.BoundaryID == "" ||
			!isFinite(info.CPUs) ||
			info.CPUs <= 0 {
			return nil, fmt.Errorf("visible system CPU boundary is invalid")
		}
		scopeCPUs = info.CPUs
		accountingSource = info.Source
		boundaryID = info.BoundaryID
		boundaryKind = info.BoundaryKind
	}

	if config.Scope != ScopeWorkers {
		maxScopeContribution := maxReachableScopePercent(
			config.Cores,
			capacity,
			scopeCPUs,
		)
		if maxScopeContribution <= 0 && maximumRequestedPercent(config) > config.DeadbandPercent {
			return nil, fmt.Errorf(
				"%s CPU controller cannot contribute measurable load toward the configured target",
				config.Scope,
			)
		}
	}

	if config.Scope == ScopeHost && sampleHostCPU == nil {
		return nil, fmt.Errorf("host CPU sampler is not configured")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &CPUStressor{
		config:           config,
		stopCh:           make(chan struct{}),
		context:          ctx,
		cancel:           cancel,
		errors:           make(chan error, 1),
		capacity:         capacity,
		detectCapacity:   detectCapacity,
		capacityInterval: cpuCapacityRecheckInterval,
		sampleHostCPU:    sampleHostCPU,
		sampleProcessCPU: platformProcessCPUSampler(),
		inspectSystemCPU: inspectSystemCPU,
		sampleSystemCPU:  sampleSystemCPU,
		scopeCPUs:        scopeCPUs,
		accountingSource: accountingSource,
		boundaryID:       boundaryID,
		boundaryKind:     boundaryKind,
		workerScheduler:  config.WorkerScheduler,
		workerNice:       workerNice,
		setupScheduler:   platformWorkerSchedulerSetup,
		setupWorkerNice:  platformWorkerPrioritySetup,
	}, nil
}

func (s *CPUStressor) Start() error {
	s.lock.Lock()
	defer s.lock.Unlock()

	switch s.lifecycle {
	case cpuLifecycleRunning:
		return fmt.Errorf("CPU stressor is already running")
	case cpuLifecycleFailed:
		return fmt.Errorf("CPU stressor has failed and cannot be restarted")
	case cpuLifecycleStopped:
		return fmt.Errorf("CPU stressor has been stopped and cannot be restarted")
	}

	currentCapacity, err := s.detectCapacity()
	if err != nil {
		s.failStartLocked()
		return fmt.Errorf("recheck CPU capacity before start: %w", err)
	}
	if err := validateCPUCapacity(currentCapacity); err != nil {
		s.failStartLocked()
		return fmt.Errorf("recheck CPU capacity before start: %w", err)
	}
	if !sameCPUCapacity(s.capacity, currentCapacity) {
		s.failStartLocked()
		return fmt.Errorf(
			"CPU capacity changed before start: configured %s; current %s",
			formatCPUCapacity(s.capacity),
			formatCPUCapacity(currentCapacity),
		)
	}
	if err := s.verifySystemCPUBoundary(); err != nil {
		s.failStartLocked()
		return fmt.Errorf("recheck CPU accounting boundary before start: %w", err)
	}

	s.lifecycle = cpuLifecycleRunning
	s.startedAt = time.Now()
	s.workers = make([]*cpuWorker, 0, s.config.Cores)
	if s.config.IdleMode == IdleModePark {
		if err := s.ensureWorkersLocked(s.config.Cores); err != nil {
			s.failStartLocked()
			return err
		}
	}

	switch s.config.Mode {
	case ModeFixed:
		s.requestedPercent = s.config.Percent
		if s.config.Scope == ScopeWorkers {
			if err := s.applyWorkerCapacityTargetLocked(s.config.Percent); err != nil {
				s.failStartLocked()
				return err
			}
		}
	case ModeWave:
		s.requestedPercent = s.config.MinPercent
		if s.config.Scope == ScopeWorkers {
			if err := s.applyWorkerCapacityTargetLocked(s.config.MinPercent); err != nil {
				s.failStartLocked()
				return err
			}
		}
	}

	s.controllerWG.Add(2)
	go s.controlLoop()
	go s.capacityLoop()
	return nil
}

func (s *CPUStressor) failStartLocked() {
	s.appliedPercent = 0
	s.trimWorkersLocked(0)
	s.lifecycle = cpuLifecycleFailed
}

func (s *CPUStressor) Stop() error {
	s.stopOnce.Do(func() {
		s.lock.Lock()
		s.lifecycle = cpuLifecycleStopped
		close(s.stopCh)
		s.cancel()
		s.lock.Unlock()

		s.controllerWG.Wait()
		s.workerWG.Wait()

		s.lock.Lock()
		defer s.lock.Unlock()

		s.requestedPercent = 0
		s.appliedPercent = 0
		s.lastHostPercent = 0
		s.hasHostSample = false
		s.workers = nil
		close(s.errors)
	})
	return nil
}

// Errors reports fatal asynchronous controller failures. Stop closes the
// channel after the controller and all workers have exited.
func (s *CPUStressor) Errors() <-chan error {
	return s.errors
}

func (s *CPUStressor) Status() CPUStatus {
	s.lock.RLock()
	defer s.lock.RUnlock()

	return CPUStatus{
		Mode:                    s.config.Mode,
		Scope:                   s.config.Scope,
		IdleMode:                s.config.IdleMode,
		WorkerScheduler:         s.workerScheduler,
		WorkerNice:              s.workerNice,
		ActiveWorkers:           activeWorkerCount(s.appliedPercent, s.config.Cores),
		MaxWorkers:              s.config.Cores,
		HostCPUs:                s.capacity.hostCPUs,
		ProcessCPUs:             s.capacity.processCPUs,
		ScopeCPUs:               s.scopeCPUs,
		MaxReachableHostPercent: maxReachableHostPercent(s.config.Cores, s.capacity),
		MaxScopeContributionPercent: maxReachableScopePercent(
			s.config.Cores,
			s.capacity,
			s.scopeCPUs,
		),
		AccountingSource:       s.accountingSource,
		AccountingBoundaryID:   s.boundaryID,
		AccountingBoundaryKind: s.boundaryKind,
		RequestedPercent:       s.requestedPercent,
		RequestedBandLowPercent: clampFloat(
			s.requestedPercent-s.config.DeadbandPercent,
			0,
			100,
		),
		RequestedBandHighPercent: clampFloat(
			s.requestedPercent+s.config.DeadbandPercent,
			0,
			100,
		),
		AppliedPercent:   s.appliedPercent,
		LastHostPercent:  s.lastHostPercent,
		LastScopePercent: s.lastHostPercent,
		HasHostSample:    s.hasHostSample,
		HasScopeSample:   s.hasHostSample,
	}
}

func (s *CPUStressor) controlLoop() {
	defer s.controllerWG.Done()

	if s.config.Scope != ScopeWorkers && !s.controlTick() {
		return
	}

	tick := s.config.ControlInterval
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-s.context.Done():
			return
		case <-ticker.C:
			if !s.controlTick() {
				return
			}
		}
	}
}

func (s *CPUStressor) capacityLoop() {
	defer s.controllerWG.Done()

	ticker := time.NewTicker(s.capacityInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-s.context.Done():
			return
		case <-ticker.C:
			if err := s.verifyCPUCapacity(); err != nil {
				if s.context.Err() != nil {
					return
				}
				s.fail(err)
				return
			}
		}
	}
}

func (s *CPUStressor) verifyCPUCapacity() error {
	if s.detectCapacity == nil {
		return fmt.Errorf("CPU capacity detector is not configured")
	}
	current, err := s.detectCapacity()
	if err != nil {
		return fmt.Errorf("recheck CPU capacity: %w", err)
	}
	if err := validateCPUCapacity(current); err != nil {
		return fmt.Errorf("recheck CPU capacity: %w", err)
	}

	s.lock.RLock()
	if s.lifecycle != cpuLifecycleRunning {
		s.lock.RUnlock()
		return nil
	}
	startup := s.capacity
	s.lock.RUnlock()

	if sameCPUCapacity(startup, current) {
		return s.verifySystemCPUBoundary()
	}
	return fmt.Errorf(
		"CPU capacity changed during run: startup %s; current %s",
		formatCPUCapacity(startup),
		formatCPUCapacity(current),
	)
}

func (s *CPUStressor) verifySystemCPUBoundary() error {
	if s.config.Scope != ScopeSystem {
		return nil
	}
	if s.inspectSystemCPU == nil {
		return fmt.Errorf("visible system CPU inspector is not configured")
	}
	info, err := s.inspectSystemCPU()
	if err != nil {
		return fmt.Errorf("inspect visible system CPU boundary: %w", err)
	}
	if info.Source != s.accountingSource ||
		info.BoundaryID != s.boundaryID ||
		info.BoundaryKind != s.boundaryKind ||
		info.CPUs != s.scopeCPUs {
		return fmt.Errorf(
			"visible system CPU boundary changed: startup source=%s id=%s kind=%s cpus=%.3f; current source=%s id=%s kind=%s cpus=%.3f",
			s.accountingSource,
			s.boundaryID,
			s.boundaryKind,
			s.scopeCPUs,
			info.Source,
			info.BoundaryID,
			info.BoundaryKind,
			info.CPUs,
		)
	}
	return nil
}

func (s *CPUStressor) controlTick() bool {
	requested := s.currentRequestedPercent()

	if s.config.Scope == ScopeWorkers {
		s.lock.Lock()
		if s.lifecycle != cpuLifecycleRunning {
			s.lock.Unlock()
			return false
		}
		s.requestedPercent = requested
		err := s.applyWorkerCapacityTargetLocked(requested)
		s.lock.Unlock()
		if err != nil {
			s.fail(err)
			return false
		}
		return true
	}

	hostUsage, err := s.sampleHostCPUWithConsistency()
	if err != nil {
		if s.context.Err() != nil {
			return false
		}
		s.fail(err)
		return false
	}

	s.lock.Lock()
	if s.lifecycle != cpuLifecycleRunning {
		s.lock.Unlock()
		return false
	}

	s.requestedPercent = requested
	s.lastHostPercent = hostUsage
	s.hasHostSample = true

	nextAppliedPercent := nextScopeAdaptiveAppliedPercent(
		requested,
		hostUsage,
		s.appliedPercent,
		s.config.Cores,
		s.capacity,
		s.scopeCPUs,
		s.config.DeadbandPercent,
		s.config.MaxStepPercent,
	)
	err = s.applyTargetLocked(nextAppliedPercent)
	s.lock.Unlock()
	if err != nil {
		s.fail(err)
		return false
	}
	return true
}

func (s *CPUStressor) sampleHostCPUWithConsistency() (float64, error) {
	if s.sampleProcessCPU == nil {
		scopeSample, err := s.sampleScopeCPU(
			s.context,
			s.config.SampleDuration,
		)
		if err != nil {
			return 0, fmt.Errorf("sample %s CPU: %w", s.config.Scope, err)
		}
		return scopeSample.percent, nil
	}

	processStart, err := s.sampleProcessCPU(s.context)
	if err != nil {
		return 0, fmt.Errorf("sample process CPU time before host sample: %w", err)
	}
	scopeSample, err := s.sampleScopeCPU(s.context, s.config.SampleDuration)
	if err != nil {
		return 0, fmt.Errorf("sample %s CPU: %w", s.config.Scope, err)
	}
	processEnd, err := s.sampleProcessCPU(s.context)
	if err != nil {
		return 0, fmt.Errorf("sample process CPU time after host sample: %w", err)
	}

	if err := s.cpuConsistency.observe(cpuMeasurementSample{
		scope:        s.config.Scope,
		hostPercent:  scopeSample.percent,
		hostCPUs:     s.scopeCPUs,
		maxPercent:   s.measurementPercentCeiling(),
		scopeBusy:    scopeSample.busy,
		scopeElapsed: scopeSample.elapsed,
		hasScopeRaw:  scopeSample.hasRaw,
		minimumWall:  s.config.SampleDuration - s.config.SampleDuration/5,
		processStart: processStart,
		processEnd:   processEnd,
	}); err != nil {
		return 0, err
	}
	return scopeSample.percent, nil
}

func (s *CPUStressor) measurementPercentCeiling() float64 {
	if s.config.Scope != ScopeSystem {
		return 100
	}
	return math.Max(
		100,
		float64(s.capacity.hostCPUs)*100/s.scopeCPUs,
	)
}

type cpuScopeSample struct {
	percent float64
	busy    time.Duration
	elapsed time.Duration
	hasRaw  bool
}

func (s *CPUStressor) sampleScopeCPU(
	ctx context.Context,
	sampleDuration time.Duration,
) (cpuScopeSample, error) {
	if s.config.Scope != ScopeSystem {
		if s.sampleHostCPU == nil {
			return cpuScopeSample{}, fmt.Errorf("host CPU sampler is not configured")
		}
		percent, err := s.sampleHostCPU(ctx, sampleDuration)
		return cpuScopeSample{percent: percent}, err
	}
	if s.sampleSystemCPU == nil {
		return cpuScopeSample{}, fmt.Errorf("visible system CPU sampler is not configured")
	}
	sample, err := s.sampleSystemCPU(ctx, sampleDuration)
	if err != nil {
		return cpuScopeSample{}, err
	}
	if sample.Source != s.accountingSource ||
		sample.BoundaryID != s.boundaryID ||
		sample.BoundaryKind != s.boundaryKind ||
		sample.CPUs != s.scopeCPUs {
		return cpuScopeSample{}, fmt.Errorf(
			"visible system CPU boundary changed during sample: startup source=%s id=%s kind=%s cpus=%.3f; sample source=%s id=%s kind=%s cpus=%.3f",
			s.accountingSource,
			s.boundaryID,
			s.boundaryKind,
			s.scopeCPUs,
			sample.Source,
			sample.BoundaryID,
			sample.BoundaryKind,
			sample.CPUs,
		)
	}
	hasRaw := sample.Elapsed != 0 || sample.Busy != 0
	return cpuScopeSample{
		percent: sample.Percent,
		busy:    sample.Busy,
		elapsed: sample.Elapsed,
		hasRaw:  hasRaw,
	}, nil
}

func (s *CPUStressor) fail(err error) {
	s.lock.Lock()
	if s.lifecycle != cpuLifecycleRunning {
		s.lock.Unlock()
		return
	}
	s.appliedPercent = 0
	s.trimWorkersLocked(0)
	s.lifecycle = cpuLifecycleFailed
	s.lock.Unlock()

	s.cancel()
	select {
	case s.errors <- err:
	default:
	}
}

func (s *CPUStressor) wavePercent(elapsed time.Duration) float64 {
	phase := math.Mod(elapsed.Seconds(), s.config.Period.Seconds()) / s.config.Period.Seconds()
	span := s.config.MaxPercent - s.config.MinPercent
	if phase < 0.5 {
		return s.config.MinPercent + span*phase*2
	}
	return s.config.MaxPercent - span*(phase-0.5)*2
}

func (s *CPUStressor) applyTargetLocked(percent float64) error {
	percent = clampFloat(
		percent,
		0,
		effectiveWorkerDriveCeilingPercent(s.config.Cores, s.capacity),
	)
	s.appliedPercent = percent

	activeUnits := float64(s.config.Cores) * percent / 100
	fullWorkers := int(math.Floor(activeUnits))
	partial := activeUnits - float64(fullWorkers)
	requiredWorkers := fullWorkers
	if partial > 0 {
		requiredWorkers++
	}

	switch s.config.IdleMode {
	case IdleModeTrim:
		if err := s.ensureWorkersLocked(requiredWorkers); err != nil {
			return err
		}
		s.trimWorkersLocked(requiredWorkers)
	default:
		if err := s.ensureWorkersLocked(s.config.Cores); err != nil {
			return err
		}
	}

	for idx, worker := range s.workers {
		switch {
		case idx < fullWorkers:
			worker.duty.Store(dutyScale)
		case idx == fullWorkers && partial > 0 && fullWorkers < len(s.workers):
			worker.duty.Store(uint32(math.Round(partial * dutyScale)))
		default:
			worker.duty.Store(0)
		}
	}
	return nil
}

func (s *CPUStressor) applyWorkerCapacityTargetLocked(percent float64) error {
	return s.applyTargetLocked(
		workerCapacityPercentToDrivePercent(
			percent,
			s.config.Cores,
			s.capacity,
		),
	)
}

func (s *CPUStressor) ensureWorkersLocked(count int) error {
	if count < 0 {
		count = 0
	}
	if count > s.config.Cores {
		count = s.config.Cores
	}
	for len(s.workers) < count {
		worker := &cpuWorker{stop: make(chan struct{})}
		ready := make(chan error, 1)
		s.workers = append(s.workers, worker)
		s.workerWG.Add(1)
		go func(w *cpuWorker, initialized chan<- error) {
			defer s.workerWG.Done()
			runCPUWorker(
				s.stopCh,
				w,
				s.config.Cycle,
				s.workerScheduler,
				s.setupScheduler,
				s.workerNice,
				s.setupWorkerNice,
				initialized,
			)
		}(worker, ready)
		if err := <-ready; err != nil {
			s.trimWorkersLocked(0)
			return fmt.Errorf("initialize CPU worker scheduling: %w", err)
		}
	}
	return nil
}

func (s *CPUStressor) trimWorkersLocked(count int) {
	if count < 0 {
		count = 0
	}
	for len(s.workers) > count {
		last := s.workers[len(s.workers)-1]
		close(last.stop)
		s.workers = s.workers[:len(s.workers)-1]
	}
}

func runCPUWorker(
	stop <-chan struct{},
	worker *cpuWorker,
	cycle time.Duration,
	workerScheduler CPUWorkerScheduler,
	setupScheduler workerSchedulerSetup,
	workerNice int,
	setupPriority workerPrioritySetup,
	ready chan<- error,
) {
	runtime.LockOSThread()
	// Do not call UnlockOSThread. A worker may lower only this Linux thread's
	// priority, so the thread must be terminated with the goroutine instead of
	// returning to the runtime thread pool.

	if setupScheduler == nil {
		ready <- fmt.Errorf("worker scheduler setup is not available")
		return
	}
	if err := setupScheduler(workerScheduler); err != nil {
		ready <- err
		return
	}
	if workerScheduler == WorkerSchedulerNormal &&
		workerNice != WorkerNiceInherit {
		if setupPriority == nil {
			ready <- fmt.Errorf("worker priority setup is not available")
			return
		}
		if err := setupPriority(workerNice); err != nil {
			ready <- err
			return
		}
	}
	ready <- nil

	for {
		select {
		case <-stop:
			return
		case <-worker.stop:
			return
		default:
		}

		duty := worker.duty.Load()
		switch {
		case duty == 0:
			if !sleepOrStop(stop, worker.stop, cycle) {
				return
			}
		case duty >= dutyScale:
			if !busyUntil(stop, worker.stop, time.Now().Add(cycle)) {
				return
			}
		default:
			busyFor := time.Duration(int64(cycle) * int64(duty) / dutyScale)
			if busyFor > 0 && !busyUntil(stop, worker.stop, time.Now().Add(busyFor)) {
				return
			}
			if rest := cycle - busyFor; rest > 0 && !sleepOrStop(stop, worker.stop, rest) {
				return
			}
		}
	}
}

func busyUntil(stop <-chan struct{}, workerStop <-chan struct{}, deadline time.Time) bool {
	var sink float64
	for time.Now().Before(deadline) {
		select {
		case <-stop:
			return false
		case <-workerStop:
			return false
		default:
		}

		sink += math.Sqrt(12345.6789)
		sink += math.Sin(sink)
	}
	runtime.KeepAlive(sink)
	return true
}

func sleepOrStop(stop <-chan struct{}, workerStop <-chan struct{}, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-stop:
		return false
	case <-workerStop:
		return false
	case <-timer.C:
		return true
	}
}

func clampFloat(value, minValue, maxValue float64) float64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func normalizeWorkerNice(configured *int) (int, error) {
	if configured == nil {
		return defaultWorkerNice, nil
	}
	if *configured == WorkerNiceInherit {
		return WorkerNiceInherit, nil
	}
	if *configured < 0 || *configured > 19 {
		return 0, fmt.Errorf(
			"CPU worker nice must be between 0 and 19, or WorkerNiceInherit",
		)
	}
	return *configured, nil
}

func validateCPUCapacity(capacity cpuCapacity) error {
	if capacity.hostCPUs <= 0 {
		return fmt.Errorf("host CPU count must be greater than zero")
	}
	if !isFinite(capacity.processCPUs) || capacity.processCPUs <= 0 {
		return fmt.Errorf("process CPU capacity must be finite and greater than zero")
	}
	if capacity.maxWorkers <= 0 {
		return fmt.Errorf("available CPU worker count must be greater than zero")
	}
	return nil
}

func sameCPUCapacity(first, second cpuCapacity) bool {
	return first.hostCPUs == second.hostCPUs &&
		first.affinityCPUs == second.affinityCPUs &&
		first.gomaxprocs == second.gomaxprocs &&
		first.quotaLimited == second.quotaLimited &&
		(!first.quotaLimited || first.quotaCPUs == second.quotaCPUs) &&
		first.processCPUs == second.processCPUs &&
		first.maxWorkers == second.maxWorkers
}

func formatCPUCapacity(capacity cpuCapacity) string {
	quota := "unlimited"
	if capacity.quotaLimited {
		quota = fmt.Sprintf("%.3f", capacity.quotaCPUs)
	}
	return fmt.Sprintf(
		"host_cpus=%d affinity_cpus=%d gomaxprocs=%d cgroup_quota=%s process_cpus=%.3f max_workers=%d",
		capacity.hostCPUs,
		capacity.affinityCPUs,
		capacity.gomaxprocs,
		quota,
		capacity.processCPUs,
		capacity.maxWorkers,
	)
}

func detectCPUCapacity() (cpuCapacity, error) {
	hostCPUs, err := cpu.Counts(true)
	if err != nil {
		return cpuCapacity{}, fmt.Errorf("count host logical CPUs: %w", err)
	}
	if hostCPUs <= 0 {
		return cpuCapacity{}, fmt.Errorf("count host logical CPUs: no CPUs reported")
	}

	runtimeCPUs := runtime.NumCPU()
	affinityCPUs := runtimeCPUs
	if runtime.GOOS == "linux" {
		affinityCPUs, err = linuxAffinityCPUCount()
		if err != nil {
			return cpuCapacity{}, fmt.Errorf("read process CPU affinity: %w", err)
		}
	}
	gomaxprocs := runtime.GOMAXPROCS(0)
	availableCPUs := minInt(runtimeCPUs, affinityCPUs)
	availableCPUs = minInt(availableCPUs, gomaxprocs)
	if availableCPUs <= 0 {
		return cpuCapacity{}, fmt.Errorf("no CPUs are available to the process scheduler")
	}

	processCPUs := float64(availableCPUs)
	var quotaCPUs float64
	var quotaLimited bool
	if runtime.GOOS == "linux" {
		quotaCPUs, quotaLimited, err = linuxCgroupCPUQuota()
		if err != nil {
			return cpuCapacity{}, err
		}
		if quotaLimited {
			processCPUs = math.Min(processCPUs, quotaCPUs)
		}
	}
	if !isFinite(processCPUs) || processCPUs <= 0 {
		return cpuCapacity{}, fmt.Errorf("cgroup CPU quota leaves no usable CPU capacity")
	}

	return cpuCapacity{
		hostCPUs:     hostCPUs,
		affinityCPUs: affinityCPUs,
		gomaxprocs:   gomaxprocs,
		quotaCPUs:    quotaCPUs,
		quotaLimited: quotaLimited,
		processCPUs:  processCPUs,
		maxWorkers:   maxInt(1, int(math.Ceil(processCPUs))),
	}, nil
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func linuxAffinityCPUCount() (int, error) {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(status), "\n") {
		key, value, found := strings.Cut(line, ":")
		if found && key == "Cpus_allowed_list" {
			return parseLinuxCPUList(value)
		}
	}
	return 0, fmt.Errorf("cpus_allowed_list missing from /proc/self/status")
}

func parseLinuxCPUList(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("CPU list is empty")
	}

	count := 0
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0, fmt.Errorf("CPU list contains an empty range")
		}
		bounds := strings.Split(part, "-")
		switch len(bounds) {
		case 1:
			if _, err := strconv.Atoi(bounds[0]); err != nil {
				return 0, fmt.Errorf("invalid CPU number %q", bounds[0])
			}
			count++
		case 2:
			first, err := strconv.Atoi(bounds[0])
			if err != nil {
				return 0, fmt.Errorf("invalid CPU range start %q", bounds[0])
			}
			last, err := strconv.Atoi(bounds[1])
			if err != nil || first < 0 || last < first {
				return 0, fmt.Errorf("invalid CPU range %q", part)
			}
			count += last - first + 1
		default:
			return 0, fmt.Errorf("invalid CPU range %q", part)
		}
	}
	return count, nil
}

func effectiveWorkerCapacity(workers int, capacity cpuCapacity) float64 {
	if workers <= 0 || capacity.processCPUs <= 0 {
		return 0
	}
	return math.Min(float64(workers), capacity.processCPUs)
}

func effectiveWorkerDriveCeilingPercent(workers int, capacity cpuCapacity) float64 {
	if workers <= 0 {
		return 0
	}
	return clampFloat(
		effectiveWorkerCapacity(workers, capacity)*100/float64(workers),
		0,
		100,
	)
}

func maxReachableHostPercent(workers int, capacity cpuCapacity) float64 {
	return maxReachableScopePercent(
		workers,
		capacity,
		float64(capacity.hostCPUs),
	)
}

func maxReachableScopePercent(
	workers int,
	capacity cpuCapacity,
	scopeCPUs float64,
) float64 {
	if !isFinite(scopeCPUs) || scopeCPUs <= 0 {
		return 0
	}
	return clampFloat(
		effectiveWorkerCapacity(workers, capacity)*100/scopeCPUs,
		0,
		100,
	)
}

func workerCapacityPercentToDrivePercent(
	capacityPercent float64,
	workers int,
	capacity cpuCapacity,
) float64 {
	return clampFloat(
		clampFloat(capacityPercent, 0, 100)*
			effectiveWorkerDriveCeilingPercent(workers, capacity)/
			100,
		0,
		effectiveWorkerDriveCeilingPercent(workers, capacity),
	)
}

func workerPercentToHostPercent(workerPercent float64, workers int, capacity cpuCapacity) float64 {
	return workerPercentToScopePercent(
		workerPercent,
		workers,
		capacity,
		float64(capacity.hostCPUs),
	)
}

func workerPercentToScopePercent(
	workerPercent float64,
	workers int,
	capacity cpuCapacity,
	scopeCPUs float64,
) float64 {
	if workers <= 0 ||
		!isFinite(scopeCPUs) ||
		scopeCPUs <= 0 ||
		capacity.processCPUs <= 0 {
		return 0
	}
	driveCPUs := clampFloat(workerPercent, 0, 100) * float64(workers) / 100
	driveCPUs = math.Min(driveCPUs, capacity.processCPUs)
	return clampFloat(
		driveCPUs*100/scopeCPUs,
		0,
		100,
	)
}

func hostPercentToWorkerPercent(hostPercent float64, workers int, capacity cpuCapacity) float64 {
	return scopePercentToWorkerPercent(
		hostPercent,
		workers,
		capacity,
		float64(capacity.hostCPUs),
	)
}

func scopePercentToWorkerPercent(
	scopePercent float64,
	workers int,
	capacity cpuCapacity,
	scopeCPUs float64,
) float64 {
	if workers <= 0 ||
		!isFinite(scopeCPUs) ||
		scopeCPUs <= 0 ||
		capacity.processCPUs <= 0 {
		return 0
	}
	driveCPUs := clampFloat(scopePercent, 0, 100) * scopeCPUs / 100
	driveCPUs = math.Min(driveCPUs, effectiveWorkerCapacity(workers, capacity))
	return clampFloat(driveCPUs*100/float64(workers), 0, 100)
}

func sampleHostCPUPercent(ctx context.Context, sampleDuration time.Duration) (float64, error) {
	percentages, err := cpu.PercentWithContext(ctx, sampleDuration, false)
	if err != nil {
		return 0, err
	}
	if len(percentages) == 0 {
		return 0, fmt.Errorf("failed to sample host CPU percent")
	}
	if !isFinite(percentages[0]) || percentages[0] < 0 || percentages[0] > 100 {
		return 0, fmt.Errorf("host CPU sampler returned invalid percent %v", percentages[0])
	}
	return percentages[0], nil
}

func activeWorkerCount(appliedPercent float64, cores int) int {
	if cores <= 0 {
		return 0
	}

	activeUnits := float64(cores) * clampFloat(appliedPercent, 0, 100) / 100
	fullWorkers := int(math.Floor(activeUnits))
	partial := activeUnits - float64(fullWorkers)
	if partial > 0 {
		fullWorkers++
	}
	if fullWorkers > cores {
		return cores
	}
	return fullWorkers
}

func (s *CPUStressor) currentRequestedPercent() float64 {
	if s.config.Mode == ModeWave {
		return s.wavePercent(time.Since(s.startedAt))
	}
	return s.config.Percent
}

func maximumRequestedPercent(config CPUConfig) float64 {
	if config.Mode == ModeWave {
		return config.MaxPercent
	}
	return config.Percent
}

func nextHostAdaptiveAppliedPercent(
	requestedHostPercent float64,
	observedHostPercent float64,
	currentAppliedPercent float64,
	workers int,
	capacity cpuCapacity,
	deadbandPercent float64,
	maxStepPercent float64,
) float64 {
	return nextScopeAdaptiveAppliedPercent(
		requestedHostPercent,
		observedHostPercent,
		currentAppliedPercent,
		workers,
		capacity,
		float64(capacity.hostCPUs),
		deadbandPercent,
		maxStepPercent,
	)
}

func nextScopeAdaptiveAppliedPercent(
	requestedScopePercent float64,
	observedScopePercent float64,
	currentAppliedPercent float64,
	workers int,
	capacity cpuCapacity,
	scopeCPUs float64,
	deadbandPercent float64,
	maxStepPercent float64,
) float64 {
	if !isFinite(requestedScopePercent) ||
		!isFinite(observedScopePercent) ||
		!isFinite(currentAppliedPercent) {
		return 0
	}
	requestedScopePercent = clampFloat(requestedScopePercent, 0, 100)
	observedScopePercent = math.Max(observedScopePercent, 0)
	driveCeiling := effectiveWorkerDriveCeilingPercent(workers, capacity)
	currentAppliedPercent = clampFloat(currentAppliedPercent, 0, driveCeiling)
	maxStepPercent = clampFloat(maxStepPercent, 0, 100)

	scopeErrorPercent := requestedScopePercent - observedScopePercent
	if math.Abs(scopeErrorPercent) <= deadbandPercent {
		return currentAppliedPercent
	}
	if workers <= 0 || !isFinite(scopeCPUs) || scopeCPUs <= 0 {
		return currentAppliedPercent
	}

	delta := scopeErrorPercent * scopeCPUs / float64(workers)
	if delta > maxStepPercent {
		delta = maxStepPercent
	} else if delta < -maxStepPercent {
		delta = -maxStepPercent
	}

	return clampFloat(currentAppliedPercent+delta, 0, driveCeiling)
}
