//go:build linux

package stress

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

type linuxSchedParam struct {
	priority int32
}

func platformWorkerSchedulerSetup(scheduler CPUWorkerScheduler) error {
	policy := 0
	policyName := ""
	switch scheduler {
	case WorkerSchedulerNormal:
		policy = 0 // Linux SCHED_OTHER.
		policyName = "SCHED_OTHER"
	case WorkerSchedulerIdle:
		policy = unix.SCHED_IDLE
		policyName = "SCHED_IDLE"
	default:
		return fmt.Errorf("unsupported CPU worker scheduler %q", scheduler)
	}

	threadID := unix.Gettid()
	parameter := linuxSchedParam{}
	_, _, errno := unix.RawSyscall(
		unix.SYS_SCHED_SETSCHEDULER,
		uintptr(threadID),
		uintptr(policy),
		uintptr(unsafe.Pointer(&parameter)),
	)
	if errno != 0 {
		return fmt.Errorf(
			"set worker thread %d scheduler to %s: %w",
			threadID,
			policyName,
			errno,
		)
	}

	actualPolicy, _, errno := unix.RawSyscall(
		unix.SYS_SCHED_GETSCHEDULER,
		uintptr(threadID),
		0,
		0,
	)
	if errno != 0 {
		return fmt.Errorf(
			"read worker thread %d scheduler after setting %s: %w",
			threadID,
			policyName,
			errno,
		)
	}
	if int(actualPolicy) != policy {
		return fmt.Errorf(
			"worker scheduler verification returned %d, want %s",
			actualPolicy,
			policyName,
		)
	}
	return nil
}

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
