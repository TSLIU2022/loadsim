//go:build !linux

package system

import (
	"fmt"
	"runtime"
)

func inspectCPUCgroup() (CPUCgroupInfo, error) {
	return CPUCgroupInfo{}, fmt.Errorf(
		"CPU cgroup inspection is unsupported on %s",
		runtime.GOOS,
	)
}
