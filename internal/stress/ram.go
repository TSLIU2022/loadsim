package stress

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const bytesPerMB = 1024 * 1024

var (
	errRAMStopped       = errors.New("RAM stressor stopped")
	errRAMTargetChanged = errors.New("RAM target changed")
)

type RAMConfig struct {
	Mode              Mode
	SizeMB            int
	MinSizeMB         int
	MaxSizeMB         int
	Period            time.Duration
	BlockMB           int
	ControlInterval   time.Duration
	RateLimitMBPerSec int
	// ImmediateShrink bypasses RateLimitMBPerSec only when releasing RAM.
	// Growth remains rate limited.
	ImmediateShrink bool
}

type RAMStatus struct {
	Mode            Mode
	RequestedMB     int
	TargetMB        int
	AppliedMB       int
	CurrentMB       int
	BlockMB         int
	RateLimitMB     int
	ImmediateShrink bool
}

type ramBlock struct {
	sizeMB int
	data   []byte
}

type ramBlockAllocator func(
	sizeMB int,
	stopCh <-chan struct{},
	targetCancelCh <-chan struct{},
) ([]byte, error)

type RAMStressor struct {
	config RAMConfig

	allocateBlock  ramBlockAllocator
	lock           sync.RWMutex
	resizeLock     sync.Mutex
	stopOnce       sync.Once
	stopCh         chan struct{}
	targetWakeCh   chan struct{}
	targetCancelCh chan struct{}
	errorsCh       chan error
	wg             sync.WaitGroup
	started        bool
	stopped        bool
	running        bool
	stopErr        error
	startedAt      time.Time
	targetEpoch    uint64
	requestedMB    int
	targetMB       int
	currentMB      int
	blocks         []ramBlock

	rateLastAt   time.Time
	rateCreditMB float64
}

func NewRAMStressor(config RAMConfig) (*RAMStressor, error) {
	if config.BlockMB < 0 {
		return nil, fmt.Errorf("RAM block size must not be negative")
	}
	if config.BlockMB == 0 {
		config.BlockMB = 16
	}
	if config.ControlInterval < 0 {
		return nil, fmt.Errorf("RAM control interval must not be negative")
	}
	if config.ControlInterval == 0 {
		config.ControlInterval = 250 * time.Millisecond
	}
	if config.RateLimitMBPerSec < 0 {
		return nil, fmt.Errorf("RAM rate limit must not be negative")
	}

	switch config.Mode {
	case ModeFixed:
		if config.SizeMB <= 0 {
			return nil, fmt.Errorf("RAM size must be greater than zero")
		}
		if _, err := mbToBytes(config.SizeMB); err != nil {
			return nil, fmt.Errorf("invalid RAM size: %w", err)
		}
	case ModeWave:
		if config.MinSizeMB < 0 || config.MaxSizeMB <= 0 {
			return nil, fmt.Errorf("RAM wave bounds must be non-negative and max must be greater than zero")
		}
		if config.MinSizeMB > config.MaxSizeMB {
			return nil, fmt.Errorf("RAM min size must be less than or equal to max size")
		}
		if config.Period <= 0 {
			return nil, fmt.Errorf("RAM wave period must be greater than zero")
		}
		if _, err := mbToBytes(config.MinSizeMB); err != nil {
			return nil, fmt.Errorf("invalid RAM minimum size: %w", err)
		}
		if _, err := mbToBytes(config.MaxSizeMB); err != nil {
			return nil, fmt.Errorf("invalid RAM maximum size: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported RAM mode %q", config.Mode)
	}

	if _, err := mbToBytes(config.BlockMB); err != nil {
		return nil, fmt.Errorf("invalid RAM block size: %w", err)
	}
	if config.RateLimitMBPerSec > 0 {
		if _, err := mbToBytes(config.RateLimitMBPerSec); err != nil {
			return nil, fmt.Errorf("invalid RAM rate limit: %w", err)
		}
	}

	requestedMB := config.SizeMB
	if config.Mode == ModeWave {
		requestedMB = config.MinSizeMB
	}
	return &RAMStressor{
		config:         config,
		allocateBlock:  allocateRAMBlockUntilTargetChange,
		stopCh:         make(chan struct{}),
		targetWakeCh:   make(chan struct{}, 1),
		targetCancelCh: make(chan struct{}),
		errorsCh:       make(chan error, 1),
		requestedMB:    requestedMB,
	}, nil
}

func (s *RAMStressor) Start() error {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.started {
		if s.running {
			return fmt.Errorf("RAM stressor is already running")
		}
		return fmt.Errorf("RAM stressor cannot be restarted")
	}
	if s.stopped {
		return fmt.Errorf("RAM stressor cannot be started after Stop")
	}

	now := time.Now()
	s.started = true
	s.running = true
	s.startedAt = now
	s.rateLastAt = now
	s.rateCreditMB = 0
	s.wg.Add(1)
	go s.controlLoop()
	return nil
}

func (s *RAMStressor) Stop() error {
	s.stopOnce.Do(func() {
		s.lock.Lock()
		s.stopped = true
		s.running = false
		close(s.stopCh)
		s.lock.Unlock()

		s.wg.Wait()

		err := s.releaseAll()

		s.lock.Lock()
		s.stopErr = err
		s.lock.Unlock()
		close(s.errorsCh)
	})

	s.lock.RLock()
	defer s.lock.RUnlock()
	return s.stopErr
}

// Errors reports asynchronous allocation failures. The channel is buffered so
// the control loop never depends on an active receiver. Stop returns release
// failures directly and closes the channel after all mappings have been handled.
func (s *RAMStressor) Errors() <-chan error {
	return s.errorsCh
}

// UpdateTargetMB changes the requested allocation of a fixed-mode stressor.
// It may be used before Start to preset the initial target, or while the
// stressor is running. The control loop applies running updates using the
// configured rate limit; a zero target is valid and releases all simulated RAM.
//
// Wave mode deliberately rejects external target changes so its configured
// min/max/period semantics remain authoritative.
func (s *RAMStressor) UpdateTargetMB(targetMB int) error {
	if targetMB < 0 {
		return fmt.Errorf("RAM target must not be negative")
	}
	if _, err := mbToBytes(targetMB); err != nil {
		return fmt.Errorf("invalid RAM target: %w", err)
	}

	s.lock.Lock()
	switch {
	case s.config.Mode != ModeFixed:
		s.lock.Unlock()
		return fmt.Errorf("RAM target updates are supported only in fixed mode")
	case s.stopped:
		s.lock.Unlock()
		return fmt.Errorf("RAM target cannot be updated after Stop")
	case s.started && !s.running:
		s.lock.Unlock()
		return fmt.Errorf("RAM target cannot be updated because the stressor is not running")
	}
	if s.config.SizeMB == targetMB {
		s.lock.Unlock()
		return nil
	}
	close(s.targetCancelCh)
	s.targetCancelCh = make(chan struct{})
	s.config.SizeMB = targetMB
	s.targetEpoch++
	s.requestedMB = targetMB
	running := s.running
	s.lock.Unlock()

	if !running {
		return nil
	}

	// Coalesce bursts: the control loop always reads the latest requested
	// target, so one pending notification is sufficient.
	select {
	case s.targetWakeCh <- struct{}{}:
	default:
	}
	return nil
}

func (s *RAMStressor) Status() RAMStatus {
	s.lock.RLock()
	defer s.lock.RUnlock()

	return RAMStatus{
		Mode:            s.config.Mode,
		RequestedMB:     s.requestedMB,
		TargetMB:        s.targetMB,
		AppliedMB:       s.currentMB,
		CurrentMB:       s.currentMB,
		BlockMB:         s.config.BlockMB,
		RateLimitMB:     s.config.RateLimitMBPerSec,
		ImmediateShrink: s.config.ImmediateShrink,
	}
}

func (s *RAMStressor) controlLoop() {
	defer s.wg.Done()
	defer func() {
		s.lock.Lock()
		s.running = false
		s.lock.Unlock()
	}()

	ticker := time.NewTicker(s.config.ControlInterval)
	defer ticker.Stop()

	if err := s.applyDesiredTarget(); err != nil {
		if !errors.Is(err, errRAMStopped) {
			s.reportError(err)
		}
		return
	}

	for {
		select {
		case <-s.stopCh:
			return
		case <-s.targetWakeCh:
			if err := s.applyDesiredTarget(); err != nil {
				if !errors.Is(err, errRAMStopped) {
					s.reportError(err)
				}
				return
			}
		case <-ticker.C:
			if err := s.applyDesiredTarget(); err != nil {
				if !errors.Is(err, errRAMStopped) {
					s.reportError(err)
				}
				return
			}
		}
	}
}

func (s *RAMStressor) desiredTarget() (int, uint64, bool) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	if s.config.Mode == ModeWave {
		return s.config.MinSizeMB, 0, false
	}
	return s.config.SizeMB, s.targetEpoch, true
}

func (s *RAMStressor) applyDesiredTarget() error {
	for {
		target, epoch, trackChanges := s.desiredTarget()
		if !trackChanges {
			target = s.waveTarget(time.Since(s.startedAt))
		}

		s.lock.Lock()
		if trackChanges && s.targetEpoch != epoch {
			s.lock.Unlock()
			continue
		}
		s.requestedMB = target
		s.lock.Unlock()

		target = s.limitTargetChange(target)
		err := s.resizeToEpoch(target, epoch, trackChanges)
		if errors.Is(err, errRAMTargetChanged) {
			continue
		}
		return err
	}
}

func (s *RAMStressor) waveTarget(elapsed time.Duration) int {
	phase := math.Mod(elapsed.Seconds(), s.config.Period.Seconds()) / s.config.Period.Seconds()
	span := float64(s.config.MaxSizeMB - s.config.MinSizeMB)
	if phase < 0.5 {
		return s.config.MinSizeMB + int(math.Round(span*phase*2))
	}
	return s.config.MaxSizeMB - int(math.Round(span*(phase-0.5)*2))
}

func (s *RAMStressor) resizeTo(targetMB int) error {
	return s.resizeToEpoch(targetMB, 0, false)
}

func (s *RAMStressor) resizeToEpoch(targetMB int, epoch uint64, trackChanges bool) error {
	if targetMB < 0 {
		return fmt.Errorf("RAM target must not be negative")
	}
	if _, err := mbToBytes(targetMB); err != nil {
		return fmt.Errorf("invalid RAM target: %w", err)
	}

	s.resizeLock.Lock()
	defer s.resizeLock.Unlock()

	s.lock.Lock()
	if trackChanges && s.targetEpoch != epoch {
		s.lock.Unlock()
		return errRAMTargetChanged
	}
	var targetCancelCh <-chan struct{}
	if trackChanges {
		targetCancelCh = s.targetCancelCh
	}
	s.targetMB = targetMB
	s.lock.Unlock()

	for {
		if s.isStopped() {
			return errRAMStopped
		}

		s.lock.RLock()
		targetChanged := trackChanges && s.targetEpoch != epoch
		currentMB := s.currentMB
		s.lock.RUnlock()
		if targetChanged {
			return errRAMTargetChanged
		}

		switch {
		case currentMB < targetMB:
			chunkMB := s.config.BlockMB
			if remaining := targetMB - currentMB; remaining < chunkMB {
				chunkMB = remaining
			}

			data, err := s.allocateBlock(chunkMB, s.stopCh, targetCancelCh)
			if err != nil {
				return fmt.Errorf("allocate %dMB RAM block: %w", chunkMB, err)
			}
			if s.isStopped() {
				if err := unmapRAM(data); err != nil {
					return fmt.Errorf("release newly allocated %dMB RAM block after stop: %w", chunkMB, err)
				}
				return errRAMStopped
			}

			s.lock.Lock()
			if trackChanges && s.targetEpoch != epoch {
				s.lock.Unlock()
				if err := unmapRAM(data); err != nil {
					return fmt.Errorf(
						"release newly allocated %dMB RAM block after target change: %w",
						chunkMB,
						err,
					)
				}
				return errRAMTargetChanged
			}
			s.blocks = append(s.blocks, ramBlock{sizeMB: chunkMB, data: data})
			s.currentMB += chunkMB
			s.lock.Unlock()

		case currentMB > targetMB:
			excess := currentMB - targetMB

			s.lock.Lock()
			if trackChanges && s.targetEpoch != epoch {
				s.lock.Unlock()
				return errRAMTargetChanged
			}
			lastIdx := len(s.blocks) - 1
			last := &s.blocks[lastIdx]
			if excess >= last.sizeMB {
				lastSizeMB := last.sizeMB
				if err := unmapRAM(last.data); err != nil {
					s.lock.Unlock()
					return fmt.Errorf("release %dMB RAM block: %w", lastSizeMB, err)
				}
				s.blocks = s.blocks[:lastIdx]
				s.currentMB -= lastSizeMB
				s.lock.Unlock()
				continue
			}

			newSizeMB := last.sizeMB - excess
			newSizeBytes, err := mbToBytes(newSizeMB)
			if err != nil {
				s.lock.Unlock()
				return fmt.Errorf("calculate shrunken RAM block size: %w", err)
			}
			tail := last.data[newSizeBytes:]
			if err := unmapRAM(tail); err != nil {
				s.lock.Unlock()
				return fmt.Errorf(
					"release %dMB RAM block tail: %w",
					last.sizeMB-newSizeMB,
					err,
				)
			}
			last.data = last.data[:newSizeBytes:newSizeBytes]
			last.sizeMB = newSizeMB
			s.currentMB -= excess
			s.lock.Unlock()

		default:
			return nil
		}
	}
}

func (s *RAMStressor) limitTargetChange(targetMB int) int {
	return s.limitTargetChangeAt(targetMB, time.Now())
}

func (s *RAMStressor) limitTargetChangeAt(targetMB int, now time.Time) int {
	if s.config.RateLimitMBPerSec == 0 {
		return targetMB
	}

	s.lock.Lock()
	defer s.lock.Unlock()

	delta := targetMB - s.currentMB
	if delta < 0 && s.config.ImmediateShrink {
		s.rateLastAt = now
		s.rateCreditMB = 0
		return targetMB
	}

	if s.rateLastAt.IsZero() {
		s.rateLastAt = now
		s.rateCreditMB = 0
		return s.currentMB
	}

	elapsed := now.Sub(s.rateLastAt)
	s.rateLastAt = now
	if elapsed > 0 {
		s.rateCreditMB += elapsed.Seconds() * float64(s.config.RateLimitMBPerSec)
	}

	if delta == 0 {
		s.rateCreditMB = 0
		return targetMB
	}

	var allowedMB int
	if s.rateCreditMB >= float64(math.MaxInt) {
		allowedMB = math.MaxInt
	} else {
		allowedMB = int(math.Floor(s.rateCreditMB))
	}
	if allowedMB <= 0 {
		return s.currentMB
	}

	distanceMB := delta
	if distanceMB < 0 {
		distanceMB = -distanceMB
	}
	if allowedMB >= distanceMB {
		s.rateCreditMB = 0
		return targetMB
	}

	s.rateCreditMB -= float64(allowedMB)
	if delta > 0 {
		return s.currentMB + allowedMB
	}
	return s.currentMB - allowedMB
}

func (s *RAMStressor) releaseAll() error {
	s.resizeLock.Lock()
	defer s.resizeLock.Unlock()

	s.lock.Lock()
	s.requestedMB = 0
	s.targetMB = 0

	var releaseErr error
	var retained []ramBlock
	retainedMB := 0
	for _, block := range s.blocks {
		if err := unmapRAM(block.data); err != nil {
			releaseErr = errors.Join(
				releaseErr,
				fmt.Errorf("release %dMB RAM block: %w", block.sizeMB, err),
			)
			retained = append(retained, block)
			retainedMB += block.sizeMB
		}
	}
	s.blocks = retained
	s.currentMB = retainedMB
	s.lock.Unlock()
	return releaseErr
}

func (s *RAMStressor) reportError(err error) {
	select {
	case s.errorsCh <- err:
	default:
	}
}

func (s *RAMStressor) isStopped() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

func allocateRAMBlock(sizeMB int, stopCh <-chan struct{}) ([]byte, error) {
	return allocateRAMBlockUntilTargetChange(sizeMB, stopCh, nil)
}

func allocateRAMBlockUntilTargetChange(
	sizeMB int,
	stopCh <-chan struct{},
	targetCancelCh <-chan struct{},
) ([]byte, error) {
	sizeBytes, err := mbToBytes(sizeMB)
	if err != nil {
		return nil, err
	}
	if sizeBytes == 0 {
		return nil, nil
	}
	if err := ramAllocationCanceled(stopCh, targetCancelCh); err != nil {
		return nil, err
	}

	var saltBytes [8]byte
	if _, err := rand.Read(saltBytes[:]); err != nil {
		return nil, fmt.Errorf("generate RAM page salt: %w", err)
	}
	pageSalt := binary.LittleEndian.Uint64(saltBytes[:])

	if err := ramAllocationCanceled(stopCh, targetCancelCh); err != nil {
		return nil, err
	}

	address, err := unix.MmapPtr(
		-1,
		0,
		nil,
		uintptr(sizeBytes),
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_PRIVATE|unix.MAP_ANON,
	)
	if err != nil {
		return nil, err
	}
	block := unsafe.Slice((*byte)(address), sizeBytes)
	if err := unix.Madvise(block, unix.MADV_DONTDUMP); err != nil {
		if unmapErr := unmapRAM(block); unmapErr != nil {
			return nil, errors.Join(
				fmt.Errorf("exclude RAM mapping from core dumps: %w", err),
				fmt.Errorf("release RAM mapping after madvise failure: %w", unmapErr),
			)
		}
		return nil, fmt.Errorf("exclude RAM mapping from core dumps: %w", err)
	}

	pageSize := unix.Getpagesize()
	for offset := 0; offset < len(block); offset += pageSize {
		if err := ramAllocationCanceled(stopCh, targetCancelCh); err != nil {
			if err := unmapRAM(block); err != nil {
				return nil, fmt.Errorf("release canceled RAM mapping: %w", err)
			}
			return nil, err
		}
		// Keep pages distinct both within this process and across
		// concurrent LoadSim instances so KSM cannot collapse the
		// simulated allocation into shared pages.
		token := pageSalt + uint64(offset/pageSize)
		binary.LittleEndian.PutUint64(block[offset:offset+8], token)
	}
	return block, nil
}

func ramAllocationCanceled(
	stopCh <-chan struct{},
	targetCancelCh <-chan struct{},
) error {
	select {
	case <-stopCh:
		return errRAMStopped
	default:
	}
	if targetCancelCh == nil {
		return nil
	}
	select {
	case <-targetCancelCh:
		return errRAMTargetChanged
	default:
		return nil
	}
}

func unmapRAM(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if uintptr(unsafe.Pointer(unsafe.SliceData(data)))%uintptr(unix.Getpagesize()) != 0 {
		return unix.EINVAL
	}
	if len(data)%unix.Getpagesize() != 0 {
		return unix.EINVAL
	}
	return unix.MunmapPtr(unsafe.Pointer(unsafe.SliceData(data)), uintptr(len(data)))
}

func mbToBytes(sizeMB int) (int, error) {
	if sizeMB < 0 {
		return 0, fmt.Errorf("MB value must not be negative")
	}
	if sizeMB > math.MaxInt/bytesPerMB {
		return 0, fmt.Errorf("%dMB exceeds addressable memory", sizeMB)
	}
	return sizeMB * bytesPerMB, nil
}
