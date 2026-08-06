//go:build !linux

package stress

import (
	"context"
	"fmt"
	"runtime"
	"time"
)

func inspectVisibleSystemCPU() (VisibleSystemCPUInfo, error) {
	return VisibleSystemCPUInfo{}, fmt.Errorf(
		"whole-machine CPU inspection is unsupported on %s",
		runtime.GOOS,
	)
}

func sampleVisibleSystemCPU(
	_ context.Context,
	_ time.Duration,
) (VisibleSystemCPUSample, error) {
	return VisibleSystemCPUSample{}, fmt.Errorf(
		"whole-machine CPU sampling is unsupported on %s",
		runtime.GOOS,
	)
}
