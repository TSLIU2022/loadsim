//go:build !linux

package stress

import "fmt"

func platformWorkerPrioritySetup(nice int) error {
	return fmt.Errorf("worker nice %d is only supported on Linux", nice)
}
