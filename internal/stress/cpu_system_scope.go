package stress

import (
	"context"
	"time"
)

// VisibleSystemCPUBoundaryKind describes the aggregate CPU accounting boundary.
type VisibleSystemCPUBoundaryKind string

const (
	VisibleSystemCPUBoundaryUnknown       VisibleSystemCPUBoundaryKind = "unknown"
	VisibleSystemCPUBoundaryHost          VisibleSystemCPUBoundaryKind = "host"
	VisibleSystemCPUBoundarySelf          VisibleSystemCPUBoundaryKind = "self"
	VisibleSystemCPUBoundaryAncestor      VisibleSystemCPUBoundaryKind = "ancestor"
	VisibleSystemCPUBoundaryNamespaceRoot VisibleSystemCPUBoundaryKind = "namespace-root"
)

// VisibleSystemCPUSample describes aggregate whole-machine CPU use.
type VisibleSystemCPUSample struct {
	Source       string
	BoundaryID   string
	BoundaryKind VisibleSystemCPUBoundaryKind
	CPUs         float64
	Percent      float64
	Busy         time.Duration
	Elapsed      time.Duration
}

// VisibleSystemCPUInfo describes the whole-machine CPU accounting boundary
// without waiting for a utilization sample.
type VisibleSystemCPUInfo struct {
	Source       string
	BoundaryID   string
	BoundaryKind VisibleSystemCPUBoundaryKind
	CPUs         float64
}

// InspectVisibleSystemCPU validates and describes whole-machine CPU accounting
// without waiting for a utilization sample.
func InspectVisibleSystemCPU() (VisibleSystemCPUInfo, error) {
	return inspectVisibleSystemCPU()
}

// SampleVisibleSystemCPU samples aggregate whole-machine CPU use for the
// requested interval.
func SampleVisibleSystemCPU(
	ctx context.Context,
	sampleDuration time.Duration,
) (VisibleSystemCPUSample, error) {
	return sampleVisibleSystemCPU(ctx, sampleDuration)
}
