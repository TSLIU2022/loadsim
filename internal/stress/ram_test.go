package stress

import (
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestNewRAMStressorValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     RAMConfig
		wantErr bool
	}{
		{
			name: "fixed ok",
			cfg: RAMConfig{
				Mode:   ModeFixed,
				SizeMB: 64,
			},
		},
		{
			name: "fixed invalid size",
			cfg: RAMConfig{
				Mode:   ModeFixed,
				SizeMB: 0,
			},
			wantErr: true,
		},
		{
			name: "wave invalid bounds",
			cfg: RAMConfig{
				Mode:      ModeWave,
				MinSizeMB: 128,
				MaxSizeMB: 64,
				Period:    60 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "wave invalid period",
			cfg: RAMConfig{
				Mode:      ModeWave,
				MinSizeMB: 64,
				MaxSizeMB: 128,
			},
			wantErr: true,
		},
		{
			name: "negative block size",
			cfg: RAMConfig{
				Mode:    ModeFixed,
				SizeMB:  1,
				BlockMB: -1,
			},
			wantErr: true,
		},
		{
			name: "negative control interval",
			cfg: RAMConfig{
				Mode:            ModeFixed,
				SizeMB:          1,
				ControlInterval: -time.Millisecond,
			},
			wantErr: true,
		},
		{
			name: "negative growth rate limit",
			cfg: RAMConfig{
				Mode:                    ModeFixed,
				SizeMB:                  1,
				GrowthRateLimitMBPerSec: -1,
			},
			wantErr: true,
		},
		{
			name: "negative release rate limit",
			cfg: RAMConfig{
				Mode:                     ModeFixed,
				SizeMB:                   1,
				ReleaseRateLimitMBPerSec: -1,
			},
			wantErr: true,
		},
		{
			name: "size conversion overflow",
			cfg: RAMConfig{
				Mode:   ModeFixed,
				SizeMB: math.MaxInt,
			},
			wantErr: true,
		},
		{
			name: "block conversion overflow",
			cfg: RAMConfig{
				Mode:    ModeFixed,
				SizeMB:  1,
				BlockMB: math.MaxInt,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRAMStressor(tt.cfg)
			if tt.wantErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestRAMWaveTarget(t *testing.T) {
	stressor, err := NewRAMStressor(RAMConfig{
		Mode:      ModeWave,
		MinSizeMB: 64,
		MaxSizeMB: 256,
		Period:    60 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cases := []struct {
		elapsed time.Duration
		want    int
	}{
		{elapsed: 0, want: 64},
		{elapsed: 15 * time.Second, want: 160},
		{elapsed: 30 * time.Second, want: 256},
		{elapsed: 45 * time.Second, want: 160},
	}

	for _, tc := range cases {
		got := stressor.waveTarget(tc.elapsed)
		if got != tc.want {
			t.Fatalf("elapsed=%v got=%d want=%d", tc.elapsed, got, tc.want)
		}
	}
}

func TestRAMResizeToExactTargetAndUnmapsReleasedBlocks(t *testing.T) {
	stressor, err := NewRAMStressor(RAMConfig{
		Mode:    ModeFixed,
		SizeMB:  1,
		BlockMB: 16,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() {
		if err := stressor.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	})

	if err := stressor.resizeTo(20); err != nil {
		t.Fatalf("grow: %v", err)
	}
	if stressor.currentMB != 20 {
		t.Fatalf("currentMB=%d want=20 after grow", stressor.currentMB)
	}
	firstBlock := stressor.blocks[0].data
	firstAddress := unsafe.Pointer(unsafe.SliceData(firstBlock))
	firstBlock[0] = 0x7f
	releasedTailAddress := unsafe.Add(firstAddress, 5*bytesPerMB)
	releasedBlockAddress := unsafe.Pointer(
		unsafe.SliceData(stressor.blocks[len(stressor.blocks)-1].data),
	)

	if err := stressor.resizeTo(5); err != nil {
		t.Fatalf("shrink: %v", err)
	}
	if stressor.currentMB != 5 {
		t.Fatalf("currentMB=%d want=5 after shrink", stressor.currentMB)
	}
	if len(stressor.blocks) != 1 {
		t.Fatalf("blocks=%d want=1 after shrink", len(stressor.blocks))
	}
	retained := stressor.blocks[0]
	if retained.sizeMB != 5 || len(retained.data) != 5*bytesPerMB {
		t.Fatalf(
			"retained block size=%dMB bytes=%d want=5MB/%d",
			retained.sizeMB,
			len(retained.data),
			5*bytesPerMB,
		)
	}
	if got := unsafe.Pointer(unsafe.SliceData(retained.data)); got != firstAddress {
		t.Fatalf("retained block moved from %p to %p", firstAddress, got)
	}
	if retained.data[0] != 0x7f {
		t.Fatalf("retained block was rebuilt: marker=%x want=7f", retained.data[0])
	}
	if !ramAddressMapped(t, firstAddress) {
		t.Fatal("retained mapping prefix is not mapped")
	}
	if ramAddressMapped(t, releasedTailAddress) {
		t.Fatal("released mapping tail is still mapped")
	}
	if ramAddressMapped(t, releasedBlockAddress) {
		t.Fatal("released complete block is still mapped")
	}

	if err := stressor.resizeTo(0); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if stressor.currentMB != 0 {
		t.Fatalf("currentMB=%d want=0 after clear", stressor.currentMB)
	}
	if len(stressor.blocks) != 0 {
		t.Fatalf("blocks=%d want=0 after clear", len(stressor.blocks))
	}
	if ramAddressMapped(t, firstAddress) {
		t.Fatal("mapping prefix is still mapped after clear")
	}
}

func TestRAMTailShrinkKeepsPrefixInPlace(t *testing.T) {
	stressor, err := NewRAMStressor(RAMConfig{
		Mode:    ModeFixed,
		SizeMB:  16,
		BlockMB: 16,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() {
		if err := stressor.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	})

	if err := stressor.resizeTo(16); err != nil {
		t.Fatalf("grow: %v", err)
	}
	original := stressor.blocks[0].data
	base := unsafe.Pointer(unsafe.SliceData(original))
	lastRetainedPage := unsafe.Add(base, 14*bytesPerMB)
	releasedPage := unsafe.Add(base, 15*bytesPerMB)
	original[0] = 0xa5

	if err := stressor.resizeTo(15); err != nil {
		t.Fatalf("shrink: %v", err)
	}
	if stressor.currentMB != 15 || len(stressor.blocks) != 1 {
		t.Fatalf(
			"currentMB/blocks=%d/%d want=15/1",
			stressor.currentMB,
			len(stressor.blocks),
		)
	}
	block := stressor.blocks[0]
	if block.sizeMB != 15 || len(block.data) != 15*bytesPerMB || cap(block.data) != 15*bytesPerMB {
		t.Fatalf(
			"block size/len/cap=%d/%d/%d want=15/%d/%d",
			block.sizeMB,
			len(block.data),
			cap(block.data),
			15*bytesPerMB,
			15*bytesPerMB,
		)
	}
	if got := unsafe.Pointer(unsafe.SliceData(block.data)); got != base {
		t.Fatalf("block moved from %p to %p", base, got)
	}
	if block.data[0] != 0xa5 {
		t.Fatalf("retained prefix was retouched: marker=%x want=a5", block.data[0])
	}
	if !ramAddressMapped(t, lastRetainedPage) {
		t.Fatal("last retained MB is not mapped")
	}
	if ramAddressMapped(t, releasedPage) {
		t.Fatal("released 1MB tail remains mapped")
	}
}

func TestRAMRateLimitUsesElapsedTimeAndFractionalCredit(t *testing.T) {
	stressor, err := NewRAMStressor(RAMConfig{
		Mode:                    ModeWave,
		MinSizeMB:               0,
		MaxSizeMB:               256,
		Period:                  60 * time.Second,
		ControlInterval:         250 * time.Millisecond,
		GrowthRateLimitMBPerSec: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	base := time.Now()
	stressor.rateLastAt = base
	stressor.rateDirection = 1
	for step := 1; step <= 3; step++ {
		if got := stressor.limitTargetChangeAt(100, base.Add(time.Duration(step)*250*time.Millisecond)); got != 0 {
			t.Fatalf("step %d target=%d want=0 before one full MB accrues", step, got)
		}
	}
	if got := stressor.limitTargetChangeAt(100, base.Add(time.Second)); got != 1 {
		t.Fatalf("target=%d want=1 after one second", got)
	}

	stressor.currentMB = 1
	if got := stressor.limitTargetChangeAt(100, base.Add(1500*time.Millisecond)); got != 1 {
		t.Fatalf("target=%d want=1 with only half an MB of new credit", got)
	}
	if got := stressor.limitTargetChangeAt(100, base.Add(2*time.Second)); got != 2 {
		t.Fatalf("target=%d want=2 after two seconds", got)
	}
}

func TestRAMRateLimitDoesNotBankCreditAtTarget(t *testing.T) {
	stressor, err := NewRAMStressor(RAMConfig{
		Mode:                    ModeFixed,
		SizeMB:                  100,
		GrowthRateLimitMBPerSec: 4,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	base := time.Now()
	stressor.currentMB = 10
	stressor.rateLastAt = base
	if got := stressor.limitTargetChangeAt(10, base.Add(10*time.Second)); got != 10 {
		t.Fatalf("target=%d want=10 while already at target", got)
	}
	if got := stressor.limitTargetChangeAt(100, base.Add(10*time.Second)); got != 10 {
		t.Fatalf("target=%d want=10 while initializing a new direction", got)
	}
	if got := stressor.limitTargetChangeAt(100, base.Add(10250*time.Millisecond)); got != 11 {
		t.Fatalf("target=%d want=11 after 250ms, idle time must not create a burst", got)
	}
}

func TestRAMRateLimitThrottlesShrinkByDefault(t *testing.T) {
	stressor, err := NewRAMStressor(RAMConfig{
		Mode:                     ModeFixed,
		SizeMB:                   100,
		ReleaseRateLimitMBPerSec: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	base := time.Now()
	stressor.currentMB = 100
	stressor.rateLastAt = base
	stressor.rateDirection = -1
	if got := stressor.limitTargetChangeAt(10, base.Add(500*time.Millisecond)); got != 100 {
		t.Fatalf("target=%d want=100 before one full MB accrues", got)
	}
	if got := stressor.limitTargetChangeAt(10, base.Add(time.Second)); got != 99 {
		t.Fatalf("target=%d want=99 after one second", got)
	}
}

func TestRAMGrowthAndReleaseUseIndependentRates(t *testing.T) {
	stressor, err := NewRAMStressor(RAMConfig{
		Mode:                     ModeFixed,
		SizeMB:                   100,
		GrowthRateLimitMBPerSec:  1,
		ReleaseRateLimitMBPerSec: 10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	base := time.Now()
	stressor.currentMB = 100
	stressor.rateLastAt = base
	stressor.rateDirection = -1
	if got := stressor.limitTargetChangeAt(10, base.Add(500*time.Millisecond)); got != 95 {
		t.Fatalf("release target=%d want=95 after half a second", got)
	}
	stressor.currentMB = 95
	if got := stressor.limitTargetChangeAt(100, base.Add(time.Second)); got != 95 {
		t.Fatalf("direction change target=%d want=95 with reset credit", got)
	}
	if got := stressor.limitTargetChangeAt(100, base.Add(2*time.Second)); got != 96 {
		t.Fatalf("growth target=%d want=96 after one second", got)
	}
}

func TestRAMUnlimitedRateAppliesTargetImmediately(t *testing.T) {
	stressor, err := NewRAMStressor(RAMConfig{
		Mode:   ModeFixed,
		SizeMB: 3,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() {
		if err := stressor.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	})

	if err := stressor.applyDesiredTarget(); err != nil {
		t.Fatalf("applyDesiredTarget: %v", err)
	}
	if stressor.currentMB != 3 {
		t.Fatalf("currentMB=%d want=3", stressor.currentMB)
	}
	status := stressor.Status()
	if status.RequestedMB != 3 || status.TargetMB != 3 || status.CurrentMB != 3 {
		t.Fatalf(
			"status requested/target/current=%d/%d/%d want=3/3/3",
			status.RequestedMB,
			status.TargetMB,
			status.CurrentMB,
		)
	}
}

func TestRAMStatusSeparatesRequestedLimitedAndCurrentTargets(t *testing.T) {
	stressor, err := NewRAMStressor(RAMConfig{
		Mode:                    ModeFixed,
		SizeMB:                  100,
		GrowthRateLimitMBPerSec: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := stressor.applyDesiredTarget(); err != nil {
		t.Fatalf("applyDesiredTarget: %v", err)
	}
	status := stressor.Status()
	if status.RequestedMB != 100 || status.TargetMB != 0 || status.CurrentMB != 0 {
		t.Fatalf(
			"status requested/target/current=%d/%d/%d want=100/0/0",
			status.RequestedMB,
			status.TargetMB,
			status.CurrentMB,
		)
	}
}

func TestRAMUpdateTargetGrowsAndShrinksRunningFixedStressor(t *testing.T) {
	stressor, err := NewRAMStressor(RAMConfig{
		Mode:            ModeFixed,
		SizeMB:          2,
		BlockMB:         2,
		ControlInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewRAMStressor: %v", err)
	}
	t.Cleanup(func() {
		if err := stressor.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	})

	if err := stressor.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitRAMCurrentMB(t, stressor, 2)

	stressor.lock.RLock()
	base := unsafe.Pointer(unsafe.SliceData(stressor.blocks[0].data))
	releasedSecondMB := unsafe.Add(base, bytesPerMB)
	stressor.lock.RUnlock()

	if err := stressor.UpdateTargetMB(4); err != nil {
		t.Fatalf("grow target: %v", err)
	}
	if status := stressor.Status(); status.RequestedMB != 4 {
		t.Fatalf("requested=%d want=4 immediately after update", status.RequestedMB)
	}
	waitRAMCurrentMB(t, stressor, 4)

	if err := stressor.UpdateTargetMB(1); err != nil {
		t.Fatalf("shrink target: %v", err)
	}
	waitRAMCurrentMB(t, stressor, 1)
	if !ramAddressMapped(t, base) {
		t.Fatal("retained first MB was unmapped")
	}
	if ramAddressMapped(t, releasedSecondMB) {
		t.Fatal("released second MB remains mapped")
	}

	if err := stressor.UpdateTargetMB(0); err != nil {
		t.Fatalf("zero target: %v", err)
	}
	waitRAMCurrentMB(t, stressor, 0)
	if ramAddressMapped(t, base) {
		t.Fatal("RAM mapping remains after zero target")
	}
}

func TestRAMReleaseImmediatelyBypassesReleaseRate(t *testing.T) {
	stressor, err := NewRAMStressor(RAMConfig{
		Mode:                     ModeFixed,
		SizeMB:                   2,
		BlockMB:                  1,
		ControlInterval:          time.Hour,
		ReleaseRateLimitMBPerSec: 1,
	})
	if err != nil {
		t.Fatalf("NewRAMStressor: %v", err)
	}
	t.Cleanup(func() {
		if err := stressor.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	})

	if err := stressor.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitRAMCurrentMB(t, stressor, 2)

	if err := stressor.ReleaseImmediately(); err != nil {
		t.Fatalf("ReleaseImmediately: %v", err)
	}
	status := stressor.Status()
	if status.RequestedMB != 0 || status.TargetMB != 0 || status.CurrentMB != 0 {
		t.Fatalf(
			"requested/target/current=%d/%d/%d want 0/0/0",
			status.RequestedMB,
			status.TargetMB,
			status.CurrentMB,
		)
	}
}

func TestRAMUpdateTargetPreservesRateLimit(t *testing.T) {
	stressor, err := NewRAMStressor(RAMConfig{
		Mode:                    ModeFixed,
		SizeMB:                  1,
		BlockMB:                 1,
		ControlInterval:         10 * time.Millisecond,
		GrowthRateLimitMBPerSec: 10,
	})
	if err != nil {
		t.Fatalf("NewRAMStressor: %v", err)
	}
	t.Cleanup(func() {
		if err := stressor.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	})

	if err := stressor.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitRAMCurrentMB(t, stressor, 1)

	if err := stressor.UpdateTargetMB(5); err != nil {
		t.Fatalf("UpdateTargetMB: %v", err)
	}
	status := stressor.Status()
	if status.RequestedMB != 5 {
		t.Fatalf("requested=%d want=5", status.RequestedMB)
	}
	if status.CurrentMB >= 5 {
		t.Fatalf("current=%d reached target without rate limiting", status.CurrentMB)
	}
	waitRAMCurrentMB(t, stressor, 5)
}

func TestRAMUpdateTargetInterruptsGrowthBeforeFollowingOldTarget(t *testing.T) {
	stressor, err := NewRAMStressor(RAMConfig{
		Mode:            ModeFixed,
		SizeMB:          1,
		BlockMB:         64,
		ControlInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewRAMStressor: %v", err)
	}
	if err := stressor.UpdateTargetMB(0); err != nil {
		t.Fatalf("preset zero target: %v", err)
	}

	allocationStarted := make(chan int, 1)
	var allocationCalls atomic.Int32
	stressor.allocateBlock = func(
		sizeMB int,
		stopCh <-chan struct{},
		targetCancelCh <-chan struct{},
	) ([]byte, error) {
		allocationCalls.Add(1)
		select {
		case allocationStarted <- sizeMB:
		default:
		}
		select {
		case <-stopCh:
			return nil, errRAMStopped
		case <-targetCancelCh:
			return nil, errRAMTargetChanged
		}
	}
	t.Cleanup(func() {
		if err := stressor.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	})

	if err := stressor.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := stressor.UpdateTargetMB(512); err != nil {
		t.Fatalf("start growth: %v", err)
	}
	select {
	case sizeMB := <-allocationStarted:
		if sizeMB != 64 {
			t.Fatalf("first allocation=%dMB want=64MB", sizeMB)
		}
	case <-time.After(time.Second):
		t.Fatal("growth allocation did not start")
	}

	if err := stressor.UpdateTargetMB(0); err != nil {
		t.Fatalf("interrupt growth: %v", err)
	}
	waitRAMCurrentMB(t, stressor, 0)
	if calls := allocationCalls.Load(); calls != 1 {
		t.Fatalf("allocation calls=%d want=1; controller continued toward stale target", calls)
	}
	if status := stressor.Status(); status.RequestedMB != 0 || status.CurrentMB != 0 {
		t.Fatalf(
			"requested/current=%d/%d want=0/0",
			status.RequestedMB,
			status.CurrentMB,
		)
	}
}

func TestRAMUpdateTargetRejectsInvalidLifecycleAndWaveMode(t *testing.T) {
	fixed, err := NewRAMStressor(RAMConfig{
		Mode:   ModeFixed,
		SizeMB: 1,
	})
	if err != nil {
		t.Fatalf("NewRAMStressor fixed: %v", err)
	}
	if err := fixed.UpdateTargetMB(0); err != nil {
		t.Fatalf("preset target before Start: %v", err)
	}
	if status := fixed.Status(); status.RequestedMB != 0 {
		t.Fatalf("preset requested=%d want=0", status.RequestedMB)
	}
	if err := fixed.UpdateTargetMB(2); err != nil {
		t.Fatalf("replace preset target before Start: %v", err)
	}
	if err := fixed.UpdateTargetMB(-1); err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("negative target error=%v", err)
	}
	if err := fixed.UpdateTargetMB(math.MaxInt); err == nil || !strings.Contains(err.Error(), "exceeds addressable memory") {
		t.Fatalf("overflow target error=%v", err)
	}
	if err := fixed.Start(); err != nil {
		t.Fatalf("Start fixed: %v", err)
	}
	waitRAMCurrentMB(t, fixed, 2)
	if err := fixed.Stop(); err != nil {
		t.Fatalf("Stop fixed: %v", err)
	}
	if err := fixed.UpdateTargetMB(2); err == nil || !strings.Contains(err.Error(), "after Stop") {
		t.Fatalf("update after Stop error=%v", err)
	}

	wave, err := NewRAMStressor(RAMConfig{
		Mode:      ModeWave,
		MinSizeMB: 0,
		MaxSizeMB: 2,
		Period:    time.Minute,
	})
	if err != nil {
		t.Fatalf("NewRAMStressor wave: %v", err)
	}
	if err := wave.Start(); err != nil {
		t.Fatalf("Start wave: %v", err)
	}
	if err := wave.UpdateTargetMB(1); err == nil || !strings.Contains(err.Error(), "only in fixed mode") {
		t.Fatalf("wave update error=%v", err)
	}
	if err := wave.Stop(); err != nil {
		t.Fatalf("Stop wave: %v", err)
	}
}

func TestRAMUpdateTargetIsConcurrentSafe(t *testing.T) {
	stressor, err := NewRAMStressor(RAMConfig{
		Mode:            ModeFixed,
		SizeMB:          1,
		BlockMB:         1,
		ControlInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRAMStressor: %v", err)
	}
	if err := stressor.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitRAMCurrentMB(t, stressor, 1)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for update := 0; update < 50; update++ {
				if err := stressor.UpdateTargetMB((offset + update) % 4); err != nil {
					errs <- err
					return
				}
				_ = stressor.Status()
			}
		}(worker)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent target update: %v", err)
	}

	if err := stressor.UpdateTargetMB(2); err != nil {
		t.Fatalf("final UpdateTargetMB: %v", err)
	}
	waitRAMCurrentMB(t, stressor, 2)
	if err := stressor.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestRAMUpdateTargetConcurrentWithStop(t *testing.T) {
	stressor, err := NewRAMStressor(RAMConfig{
		Mode:            ModeFixed,
		SizeMB:          1,
		BlockMB:         1,
		ControlInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRAMStressor: %v", err)
	}
	if err := stressor.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitRAMCurrentMB(t, stressor, 1)

	updateStopped := make(chan error, 1)
	go func() {
		for target := 0; ; target = (target + 1) % 3 {
			if err := stressor.UpdateTargetMB(target); err != nil {
				updateStopped <- err
				return
			}
		}
	}()

	time.Sleep(5 * time.Millisecond)
	if err := stressor.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case err := <-updateStopped:
		if !strings.Contains(err.Error(), "after Stop") &&
			!strings.Contains(err.Error(), "not running") {
			t.Fatalf("concurrent update error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("target updater did not observe Stop")
	}
	if status := stressor.Status(); status.CurrentMB != 0 {
		t.Fatalf("current=%d want=0 after Stop", status.CurrentMB)
	}
}

func TestRAMAllocationCanBeCanceled(t *testing.T) {
	stopCh := make(chan struct{})
	close(stopCh)

	startedAt := time.Now()
	block, err := allocateRAMBlock(256, stopCh)
	if !errors.Is(err, errRAMStopped) {
		t.Fatalf("error=%v want errRAMStopped", err)
	}
	if block != nil {
		t.Fatalf("block length=%d want nil", len(block))
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("canceled allocation took %v", elapsed)
	}
}

func TestRAMAllocationTouchesDistinctPages(t *testing.T) {
	stopCh := make(chan struct{})
	block, err := allocateRAMBlock(2, stopCh)
	if err != nil {
		t.Fatalf("allocateRAMBlock: %v", err)
	}
	t.Cleanup(func() {
		if err := unmapRAM(block); err != nil {
			t.Fatalf("unmapRAM: %v", err)
		}
	})

	pageSize := unix.Getpagesize()
	first := binary.LittleEndian.Uint64(block[:8])
	second := binary.LittleEndian.Uint64(block[pageSize : pageSize+8])
	if second != first+1 {
		t.Fatalf(
			"page tokens first=%#x second=%#x must be sequential and distinct",
			first,
			second,
		)
	}
}

func TestRAMRuntimeErrorIsReported(t *testing.T) {
	stressor, err := NewRAMStressor(RAMConfig{
		Mode:   ModeFixed,
		SizeMB: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Simulate a configuration becoming invalid after construction so the
	// asynchronous controller takes a deterministic runtime error path.
	stressor.config.SizeMB = math.MaxInt

	if err := stressor.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case runtimeErr := <-stressor.Errors():
		if runtimeErr == nil || !strings.Contains(runtimeErr.Error(), "exceeds addressable memory") {
			t.Fatalf("runtime error=%v want overflow error", runtimeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runtime error")
	}
	deadline := time.Now().Add(time.Second)
	for {
		stressor.lock.RLock()
		running := stressor.running
		stressor.lock.RUnlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("RAM stressor remained running after runtime failure")
		}
		time.Sleep(time.Millisecond)
	}
	if err := stressor.UpdateTargetMB(1); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("update after runtime failure error=%v", err)
	}
	if err := stressor.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestRAMLifecycleIsOneShot(t *testing.T) {
	stressor, err := NewRAMStressor(RAMConfig{
		Mode:   ModeFixed,
		SizeMB: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := stressor.Start(); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := stressor.Start(); err == nil {
		t.Fatal("second Start while running succeeded")
	}
	if err := stressor.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := stressor.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if err := stressor.Start(); err == nil {
		t.Fatal("Start after Stop succeeded")
	}
	if runtimeErr, ok := <-stressor.Errors(); ok || runtimeErr != nil {
		t.Fatalf("normal Stop produced error=%v ok=%v", runtimeErr, ok)
	}
}

func TestRAMStopBeforeStartPreventsStart(t *testing.T) {
	stressor, err := NewRAMStressor(RAMConfig{
		Mode:   ModeFixed,
		SizeMB: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := stressor.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := stressor.Start(); err == nil {
		t.Fatal("Start after an initial Stop succeeded")
	}
}

func TestMBToBytesRejectsOverflow(t *testing.T) {
	if _, err := mbToBytes(math.MaxInt); err == nil {
		t.Fatal("mbToBytes accepted an overflowing value")
	}
	if got, err := mbToBytes(2); err != nil || got != 2*bytesPerMB {
		t.Fatalf("mbToBytes(2)=(%d, %v)", got, err)
	}
}

func ramAddressMapped(t *testing.T, address unsafe.Pointer) bool {
	t.Helper()

	var residency byte
	_, _, errno := unix.Syscall(
		unix.SYS_MINCORE,
		uintptr(address),
		uintptr(unix.Getpagesize()),
		uintptr(unsafe.Pointer(&residency)),
	)
	switch errno {
	case 0:
		return true
	case unix.ENOMEM:
		return false
	default:
		t.Fatalf("mincore(%p): %v", address, errno)
		return false
	}
}

func waitRAMCurrentMB(t *testing.T, stressor *RAMStressor, want int) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := stressor.Status()
		if status.CurrentMB == want && status.TargetMB == want {
			return
		}
		select {
		case err, ok := <-stressor.Errors():
			if ok {
				t.Fatalf("RAM controller failed while waiting for %dMB: %v", want, err)
			}
			t.Fatalf("RAM controller stopped while waiting for %dMB", want)
		default:
		}
		time.Sleep(time.Millisecond)
	}
	status := stressor.Status()
	t.Fatalf(
		"timed out waiting for %dMB; requested/target/current=%d/%d/%d",
		want,
		status.RequestedMB,
		status.TargetMB,
		status.CurrentMB,
	)
}
