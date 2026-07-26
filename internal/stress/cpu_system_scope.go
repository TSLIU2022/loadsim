package stress

import (
	"context"
	"time"
)

// VisibleSystemCPUBoundaryKind describes how the visible accounting root
// relates to the current cgroup membership.
type VisibleSystemCPUBoundaryKind string

const (
	VisibleSystemCPUBoundaryUnknown       VisibleSystemCPUBoundaryKind = "unknown"
	VisibleSystemCPUBoundarySelf          VisibleSystemCPUBoundaryKind = "self"
	VisibleSystemCPUBoundaryAncestor      VisibleSystemCPUBoundaryKind = "ancestor"
	VisibleSystemCPUBoundaryNamespaceRoot VisibleSystemCPUBoundaryKind = "namespace-root"
)

// VisibleSystemCPUSample describes aggregate CPU use at the current visible
// cgroup filesystem root.
type VisibleSystemCPUSample struct {
	Source       string
	BoundaryID   string
	BoundaryKind VisibleSystemCPUBoundaryKind
	CPUs         float64
	Percent      float64
	Busy         time.Duration
	Elapsed      time.Duration
}

// VisibleSystemCPUInfo describes the current aggregate CPU accounting
// boundary without waiting for a utilization sample.
type VisibleSystemCPUInfo struct {
	Source       string
	BoundaryID   string
	BoundaryKind VisibleSystemCPUBoundaryKind
	CPUs         float64
}

// InspectVisibleSystemCPU validates and describes the current visible cgroup
// CPU accounting boundary without waiting for a utilization sample.
func InspectVisibleSystemCPU() (VisibleSystemCPUInfo, error) {
	return inspectVisibleSystemCPU()
}

// SampleVisibleSystemCPU samples aggregate CPU use at the current visible
// cgroup filesystem root for the requested interval.
func SampleVisibleSystemCPU(
	ctx context.Context,
	sampleDuration time.Duration,
) (VisibleSystemCPUSample, error) {
	return sampleVisibleSystemCPU(ctx, sampleDuration)
}
