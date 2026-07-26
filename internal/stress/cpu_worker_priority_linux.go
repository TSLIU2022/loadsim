//go:build linux

package stress

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func platformWorkerPrioritySetup(nice int) error {
	threadID := unix.Gettid()
	if err := unix.Setpriority(unix.PRIO_PROCESS, threadID, nice); err != nil {
		return fmt.Errorf("set worker thread %d nice to %d: %w", threadID, nice, err)
	}

	kernelPriority, err := unix.Getpriority(unix.PRIO_PROCESS, threadID)
	if err != nil {
		return fmt.Errorf("read worker thread %d nice after setting %d: %w", threadID, nice, err)
	}
	actualNice, err := linuxNiceFromKernelPriority(kernelPriority)
	if err != nil {
		return err
	}
	if actualNice != nice {
		return fmt.Errorf("worker nice verification returned %d, want %d", actualNice, nice)
	}
	return nil
}

func linuxNiceFromKernelPriority(kernelPriority int) (int, error) {
	if kernelPriority < 1 || kernelPriority > 40 {
		return 0, fmt.Errorf("Linux getpriority returned out-of-range value %d", kernelPriority)
	}
	return 20 - kernelPriority, nil
}
