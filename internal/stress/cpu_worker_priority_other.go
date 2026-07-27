//go:build !linux

package stress

import "fmt"

func platformWorkerSchedulerSetup(scheduler CPUWorkerScheduler) error {
	if scheduler == WorkerSchedulerNormal {
		return nil
	}
	return fmt.Errorf("worker scheduler %q is only supported on Linux", scheduler)
}

func platformWorkerPrioritySetup(nice int) error {
	return fmt.Errorf("worker nice %d is only supported on Linux", nice)
}
