package system

import "time"

// CPUCgroupInfo describes CPU controls visible from the current process
// without exposing cgroup paths.
type CPUCgroupInfo struct {
	Version string

	QuotaKnown   bool
	QuotaLimited bool
	QuotaCPUs    float64
	QuotaLevel   string

	WeightKnown bool
	WeightKind  string
	Weight      uint64
	WeightLevel string

	ThrottlingKnown  bool
	Periods          uint64
	ThrottledPeriods uint64
	ThrottledTime    time.Duration
	ThrottlingLevel  string
}

// InspectCPUCgroup reports CPU quota, relative weight, and throttling counters
// for the current process and its visible ancestors.
func InspectCPUCgroup() (CPUCgroupInfo, error) {
	return inspectCPUCgroup()
}
