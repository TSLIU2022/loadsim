//go:build linux

package stress

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
)

const (
	visibleSystemCPUSourceV2 = "cgroup2:cpu.stat"
	visibleSystemCPUSourceV1 = "cgroup1:cpuacct.usage"
)

type linuxVisibleCPUMembership struct {
	v2Path     string
	hasV2      bool
	controller map[string]string
}

type linuxVisibleCPUMount struct {
	id          uint64
	version     linuxCPUCgroupVersion
	root        string
	mountPoint  string
	controllers map[string]bool
}

type linuxVisibleCPUBoundary struct {
	source       string
	boundaryID   string
	boundaryKind VisibleSystemCPUBoundaryKind
	usageMount   linuxVisibleCPUMount
	usagePath    string
	usageUnit    time.Duration
	quotaMount   *linuxVisibleCPUMount
	cpusetMount  *linuxVisibleCPUMount
}

type linuxVisibleCPUSampleDependencies struct {
	readFile func(string) ([]byte, error)
	now      func() time.Time
	wait     func(context.Context, time.Duration) error
}

type linuxVisibleCPUInputReader func(
	context.Context,
) (string, string, int, error)

func sampleVisibleSystemCPU(
	ctx context.Context,
	sampleDuration time.Duration,
) (VisibleSystemCPUSample, error) {
	if ctx == nil {
		return VisibleSystemCPUSample{}, fmt.Errorf("visible system CPU sample context must not be nil")
	}
	return sampleVisibleSystemCPUWithReinspection(
		ctx,
		sampleDuration,
		readLinuxVisibleCPUInputs,
		linuxVisibleCPUSampleDependencies{
			readFile: os.ReadFile,
			now:      time.Now,
			wait:     waitVisibleSystemCPUSample,
		},
	)
}

func sampleVisibleSystemCPUWithReinspection(
	ctx context.Context,
	sampleDuration time.Duration,
	readInputs linuxVisibleCPUInputReader,
	dependencies linuxVisibleCPUSampleDependencies,
) (VisibleSystemCPUSample, error) {
	if ctx == nil {
		return VisibleSystemCPUSample{}, fmt.Errorf(
			"visible system CPU sample context must not be nil",
		)
	}
	if readInputs == nil {
		return VisibleSystemCPUSample{}, fmt.Errorf(
			"visible system CPU input reader must not be nil",
		)
	}
	membership, mountInfo, hostCPUs, err := readInputs(ctx)
	if err != nil {
		return VisibleSystemCPUSample{}, err
	}
	sample, err := sampleVisibleSystemCPUFrom(
		ctx,
		sampleDuration,
		membership,
		mountInfo,
		hostCPUs,
		dependencies,
	)
	if err != nil {
		return VisibleSystemCPUSample{}, err
	}
	currentMembership, currentMountInfo, currentHostCPUs, err := readInputs(ctx)
	if err != nil {
		return VisibleSystemCPUSample{}, fmt.Errorf(
			"reinspect visible system CPU boundary after sample: %w",
			err,
		)
	}
	current, err := inspectVisibleSystemCPUFrom(
		currentMembership,
		currentMountInfo,
		currentHostCPUs,
		dependencies.readFile,
	)
	if err != nil {
		return VisibleSystemCPUSample{}, fmt.Errorf(
			"reinspect visible system CPU boundary after sample: %w",
			err,
		)
	}
	if sample.Source != current.boundary.source ||
		sample.BoundaryID != current.boundary.boundaryID ||
		sample.BoundaryKind != current.boundary.boundaryKind ||
		sample.CPUs != current.cpus {
		return VisibleSystemCPUSample{}, fmt.Errorf(
			"visible system CPU boundary changed during sampling",
		)
	}
	return sample, nil
}

func inspectVisibleSystemCPU() (VisibleSystemCPUInfo, error) {
	membership, mountInfo, hostCPUs, err := readLinuxVisibleCPUInputs(
		context.Background(),
	)
	if err != nil {
		return VisibleSystemCPUInfo{}, err
	}
	inspection, err := inspectVisibleSystemCPUFrom(
		membership,
		mountInfo,
		hostCPUs,
		os.ReadFile,
	)
	if err != nil {
		return VisibleSystemCPUInfo{}, err
	}
	return VisibleSystemCPUInfo{
		Source:       inspection.boundary.source,
		BoundaryID:   inspection.boundary.boundaryID,
		BoundaryKind: inspection.boundary.boundaryKind,
		CPUs:         inspection.cpus,
	}, nil
}

func readLinuxVisibleCPUInputs(
	ctx context.Context,
) (string, string, int, error) {
	if err := ctx.Err(); err != nil {
		return "", "", 0, err
	}
	membership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", "", 0, fmt.Errorf("read cgroup membership: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", "", 0, err
	}
	mountInfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", "", 0, fmt.Errorf("read cgroup mounts: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", "", 0, err
	}
	hostCPUs, err := cpu.Counts(true)
	if err != nil {
		return "", "", 0, fmt.Errorf("count host logical CPUs: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", "", 0, err
	}
	return string(membership), string(mountInfo), hostCPUs, nil
}

type linuxVisibleCPUInspection struct {
	boundary linuxVisibleCPUBoundary
	usage    uint64
	cpus     float64
}

func inspectVisibleSystemCPUFrom(
	membership string,
	mountInfo string,
	hostCPUs int,
	readFile func(string) ([]byte, error),
) (linuxVisibleCPUInspection, error) {
	if hostCPUs <= 0 {
		return linuxVisibleCPUInspection{}, fmt.Errorf(
			"visible system CPU host count must be greater than zero",
		)
	}
	if readFile == nil {
		return linuxVisibleCPUInspection{}, fmt.Errorf(
			"visible system CPU file reader must not be nil",
		)
	}
	memberships, err := parseLinuxVisibleCPUMembership(membership)
	if err != nil {
		return linuxVisibleCPUInspection{}, err
	}
	mounts, err := parseLinuxVisibleCPUMounts(mountInfo)
	if err != nil {
		return linuxVisibleCPUInspection{}, err
	}
	boundaries, err := resolveLinuxVisibleCPUBoundaries(memberships, mounts)
	if err != nil {
		return linuxVisibleCPUInspection{}, err
	}
	for _, boundary := range boundaries {
		usage, err := readLinuxVisibleCPUUsage(boundary, readFile)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return linuxVisibleCPUInspection{}, err
		}
		cpus, err := readLinuxVisibleCPUCapacity(
			boundary,
			hostCPUs,
			readFile,
		)
		if err != nil {
			return linuxVisibleCPUInspection{}, err
		}
		return linuxVisibleCPUInspection{
			boundary: boundary,
			usage:    usage,
			cpus:     cpus,
		}, nil
	}
	return linuxVisibleCPUInspection{}, fmt.Errorf(
		"no readable CPU usage counter exists at the visible cgroup root",
	)
}

func sampleVisibleSystemCPUFrom(
	ctx context.Context,
	sampleDuration time.Duration,
	membership string,
	mountInfo string,
	hostCPUs int,
	dependencies linuxVisibleCPUSampleDependencies,
) (VisibleSystemCPUSample, error) {
	if ctx == nil {
		return VisibleSystemCPUSample{}, fmt.Errorf("visible system CPU sample context must not be nil")
	}
	if sampleDuration <= 0 {
		return VisibleSystemCPUSample{}, fmt.Errorf(
			"visible system CPU sample duration must be greater than zero",
		)
	}
	if dependencies.readFile == nil ||
		dependencies.now == nil ||
		dependencies.wait == nil {
		return VisibleSystemCPUSample{}, fmt.Errorf(
			"visible system CPU sampler dependencies are incomplete",
		)
	}
	if err := ctx.Err(); err != nil {
		return VisibleSystemCPUSample{}, err
	}

	inspection, err := inspectVisibleSystemCPUFrom(
		membership,
		mountInfo,
		hostCPUs,
		dependencies.readFile,
	)
	if err != nil {
		return VisibleSystemCPUSample{}, err
	}
	if err := ctx.Err(); err != nil {
		return VisibleSystemCPUSample{}, err
	}
	readDelayLimit := visibleSystemCPUReadDelayLimit(sampleDuration)
	startUsage, startedAt, err := readLinuxVisibleCPUUsageAtMidpoint(
		inspection.boundary,
		dependencies,
		readDelayLimit,
	)
	if err != nil {
		return VisibleSystemCPUSample{}, err
	}
	if err := ctx.Err(); err != nil {
		return VisibleSystemCPUSample{}, err
	}
	if err := dependencies.wait(ctx, sampleDuration); err != nil {
		return VisibleSystemCPUSample{}, err
	}
	if err := ctx.Err(); err != nil {
		return VisibleSystemCPUSample{}, err
	}
	endUsage, finishedAt, err := readLinuxVisibleCPUUsageAtMidpoint(
		inspection.boundary,
		dependencies,
		readDelayLimit,
	)
	if err != nil {
		return VisibleSystemCPUSample{}, err
	}
	if err := ctx.Err(); err != nil {
		return VisibleSystemCPUSample{}, err
	}
	elapsed := finishedAt.Sub(startedAt)
	if elapsed <= 0 {
		return VisibleSystemCPUSample{}, fmt.Errorf(
			"visible system CPU sample interval must be greater than zero",
		)
	}
	if elapsed < sampleDuration {
		return VisibleSystemCPUSample{}, fmt.Errorf(
			"visible system CPU sample interval %s was shorter than requested %s",
			elapsed,
			sampleDuration,
		)
	}
	if endUsage < startUsage {
		return VisibleSystemCPUSample{}, fmt.Errorf(
			"visible system CPU usage counter moved backwards",
		)
	}
	endCPUs, err := readLinuxVisibleCPUCapacity(
		inspection.boundary,
		hostCPUs,
		dependencies.readFile,
	)
	if err != nil {
		return VisibleSystemCPUSample{}, err
	}
	if endCPUs != inspection.cpus {
		return VisibleSystemCPUSample{}, fmt.Errorf(
			"visible system CPU capacity changed during sampling from %.6f to %.6f CPUs",
			inspection.cpus,
			endCPUs,
		)
	}
	percent, busy, err := calculateLinuxVisibleCPUPercent(
		endUsage-startUsage,
		inspection.boundary.usageUnit,
		elapsed,
		inspection.cpus,
	)
	if err != nil {
		return VisibleSystemCPUSample{}, err
	}
	return VisibleSystemCPUSample{
		Source:       inspection.boundary.source,
		BoundaryID:   inspection.boundary.boundaryID,
		BoundaryKind: inspection.boundary.boundaryKind,
		CPUs:         inspection.cpus,
		Percent:      percent,
		Busy:         busy,
		Elapsed:      elapsed,
	}, nil
}

func visibleSystemCPUReadDelayLimit(
	sampleDuration time.Duration,
) time.Duration {
	const (
		minimum = 10 * time.Millisecond
		maximum = 100 * time.Millisecond
	)
	limit := sampleDuration / 10
	if limit < minimum {
		return minimum
	}
	if limit > maximum {
		return maximum
	}
	return limit
}

func readLinuxVisibleCPUUsageAtMidpoint(
	boundary linuxVisibleCPUBoundary,
	dependencies linuxVisibleCPUSampleDependencies,
	delayLimit time.Duration,
) (uint64, time.Time, error) {
	before := dependencies.now()
	usage, err := readLinuxVisibleCPUUsage(
		boundary,
		dependencies.readFile,
	)
	after := dependencies.now()
	if err != nil {
		return 0, time.Time{}, err
	}
	delay := after.Sub(before)
	if delay < 0 {
		return 0, time.Time{}, fmt.Errorf(
			"visible system CPU counter read time moved backwards",
		)
	}
	if delay > delayLimit {
		return 0, time.Time{}, fmt.Errorf(
			"visible system CPU counter read took %s, exceeding the safe limit %s",
			delay,
			delayLimit,
		)
	}
	return usage, before.Add(delay / 2), nil
}

func parseLinuxVisibleCPUMembership(
	value string,
) (linuxVisibleCPUMembership, error) {
	result := linuxVisibleCPUMembership{
		controller: make(map[string]string),
	}
	for lineNumber, rawLine := range strings.Split(value, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 {
			return linuxVisibleCPUMembership{}, fmt.Errorf(
				"parse cgroup membership line %d: expected three fields",
				lineNumber+1,
			)
		}
		group := filepath.Clean(fields[2])
		if !filepath.IsAbs(group) {
			return linuxVisibleCPUMembership{}, fmt.Errorf(
				"parse cgroup membership line %d: path %q is not absolute",
				lineNumber+1,
				fields[2],
			)
		}
		if fields[1] == "" {
			if result.hasV2 && result.v2Path != group {
				return linuxVisibleCPUMembership{}, fmt.Errorf(
					"conflicting cgroup v2 memberships %q and %q",
					result.v2Path,
					group,
				)
			}
			result.v2Path = group
			result.hasV2 = true
			continue
		}
		for _, controller := range strings.Split(fields[1], ",") {
			if controller == "" || strings.HasPrefix(controller, "name=") {
				continue
			}
			if previous, ok := result.controller[controller]; ok &&
				previous != group {
				return linuxVisibleCPUMembership{}, fmt.Errorf(
					"conflicting cgroup v1 %s memberships %q and %q",
					controller,
					previous,
					group,
				)
			}
			result.controller[controller] = group
		}
	}
	return result, nil
}

func parseLinuxVisibleCPUMounts(
	mountInfo string,
) ([]linuxVisibleCPUMount, error) {
	scanner := bufio.NewScanner(strings.NewReader(mountInfo))
	scanner.Buffer(make([]byte, 4096), 1024*1024)

	var mounts []linuxVisibleCPUMount
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		fields := strings.Fields(scanner.Text())
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if separator < 6 || len(fields) <= separator+3 {
			return nil, fmt.Errorf(
				"parse mountinfo line %d: malformed entry",
				lineNumber,
			)
		}

		var version linuxCPUCgroupVersion
		switch fields[separator+1] {
		case "cgroup2":
			version = linuxCPUCgroupV2
		case "cgroup":
			version = linuxCPUCgroupV1
		default:
			continue
		}
		root, err := unescapeLinuxMountInfoPath(fields[3])
		if err != nil {
			return nil, fmt.Errorf(
				"parse mountinfo line %d root: %w",
				lineNumber,
				err,
			)
		}
		mountPoint, err := unescapeLinuxMountInfoPath(fields[4])
		if err != nil {
			return nil, fmt.Errorf(
				"parse mountinfo line %d mount point: %w",
				lineNumber,
				err,
			)
		}
		if !filepath.IsAbs(root) || !filepath.IsAbs(mountPoint) {
			return nil, fmt.Errorf(
				"parse mountinfo line %d: cgroup paths must be absolute",
				lineNumber,
			)
		}
		mountID, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil || mountID == 0 {
			return nil, fmt.Errorf(
				"parse mountinfo line %d: invalid mount ID %q",
				lineNumber,
				fields[0],
			)
		}

		controllers := make(map[string]bool)
		if version == linuxCPUCgroupV1 {
			for _, controller := range []string{"cpu", "cpuacct", "cpuset"} {
				if linuxMountInfoHasController(fields, separator, controller) {
					controllers[controller] = true
				}
			}
			if len(controllers) == 0 {
				continue
			}
		}
		mounts = append(mounts, linuxVisibleCPUMount{
			id:          mountID,
			version:     version,
			root:        filepath.Clean(root),
			mountPoint:  filepath.Clean(mountPoint),
			controllers: controllers,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse mountinfo: %w", err)
	}
	return mounts, nil
}

func resolveLinuxVisibleCPUBoundaries(
	membership linuxVisibleCPUMembership,
	mounts []linuxVisibleCPUMount,
) ([]linuxVisibleCPUBoundary, error) {
	var (
		v2Mount       linuxVisibleCPUMount
		hasV2Mount    bool
		v1UsageMount  linuxVisibleCPUMount
		hasV1Usage    bool
		v1CPUMount    linuxVisibleCPUMount
		hasV1CPUMount bool
		v1CpusetMount linuxVisibleCPUMount
		hasV1Cpuset   bool
		err           error
	)
	if membership.hasV2 {
		v2Mount, hasV2Mount, err = selectLinuxVisibleCPUMount(
			mounts,
			linuxCPUCgroupV2,
			"",
			membership.v2Path,
		)
		if err != nil {
			return nil, err
		}
	}

	cpuacctPath, hasCPUAcct := membership.controller["cpuacct"]
	if hasCPUAcct {
		v1UsageMount, hasV1Usage, err = selectLinuxVisibleCPUMount(
			mounts,
			linuxCPUCgroupV1,
			"cpuacct",
			cpuacctPath,
		)
		if err != nil {
			return nil, err
		}
	}

	cpuPath, hasV1CPU := membership.controller["cpu"]
	if hasV1CPU {
		v1CPUMount, hasV1CPUMount, err = selectLinuxVisibleCPUMount(
			mounts,
			linuxCPUCgroupV1,
			"cpu",
			cpuPath,
		)
		if err != nil {
			return nil, err
		}
		if !hasV1CPUMount {
			return nil, fmt.Errorf(
				"cgroup v1 CPU membership has no visible CPU controller mount",
			)
		}
	}

	cpusetPath, hasV1CpusetMembership := membership.controller["cpuset"]
	if hasV1CpusetMembership {
		v1CpusetMount, hasV1Cpuset, err = selectLinuxVisibleCPUMount(
			mounts,
			linuxCPUCgroupV1,
			"cpuset",
			cpusetPath,
		)
		if err != nil {
			return nil, err
		}
		if !hasV1Cpuset {
			return nil, fmt.Errorf(
				"cgroup v1 cpuset membership has no visible cpuset controller mount",
			)
		}
	}

	// A v1 CPU membership proves that the actual CPU controller is attached to
	// v1. Never prefer a readable unified accounting file in that hybrid case.
	if hasV1CPU {
		if !hasCPUAcct || !hasV1Usage {
			return nil, fmt.Errorf(
				"cgroup v1 CPU control has no matching visible cpuacct accounting root",
			)
		}
		if !sameLinuxVisibleCPUMount(v1CPUMount, v1UsageMount) {
			return nil, fmt.Errorf(
				"cgroup v1 CPU control and cpuacct accounting must share one mount hierarchy",
			)
		}
		cpusetMount, cpusetMembership := chooseLinuxVisibleCPUSetMount(
			hasV1Cpuset,
			v1CpusetMount,
			cpusetPath,
			hasV2Mount,
			v2Mount,
			membership.v2Path,
		)
		boundary, err := newLinuxVisibleCPUBoundary(
			visibleSystemCPUSourceV1,
			v1UsageMount,
			cpuacctPath,
			&v1CPUMount,
			cpuPath,
			cpusetMount,
			cpusetMembership,
		)
		if err != nil {
			return nil, err
		}
		return []linuxVisibleCPUBoundary{boundary}, nil
	}

	var boundaries []linuxVisibleCPUBoundary
	if hasV2Mount {
		cpusetMount, cpusetMembership := chooseLinuxVisibleCPUSetMount(
			hasV1Cpuset,
			v1CpusetMount,
			cpusetPath,
			true,
			v2Mount,
			membership.v2Path,
		)
		boundary, err := newLinuxVisibleCPUBoundary(
			visibleSystemCPUSourceV2,
			v2Mount,
			membership.v2Path,
			&v2Mount,
			membership.v2Path,
			cpusetMount,
			cpusetMembership,
		)
		if err != nil {
			return nil, err
		}
		boundaries = append(boundaries, boundary)
	}

	return boundaries, nil
}

func sameLinuxVisibleCPUMount(
	first linuxVisibleCPUMount,
	second linuxVisibleCPUMount,
) bool {
	return first.id == second.id &&
		first.version == second.version &&
		first.root == second.root &&
		first.mountPoint == second.mountPoint
}

func chooseLinuxVisibleCPUSetMount(
	hasV1 bool,
	v1Mount linuxVisibleCPUMount,
	v1Membership string,
	hasV2 bool,
	v2Mount linuxVisibleCPUMount,
	v2Membership string,
) (*linuxVisibleCPUMount, string) {
	if hasV1 {
		return &v1Mount, v1Membership
	}
	if hasV2 {
		return &v2Mount, v2Membership
	}
	return nil, ""
}

func newLinuxVisibleCPUBoundary(
	source string,
	usageMount linuxVisibleCPUMount,
	usageMembership string,
	quotaMount *linuxVisibleCPUMount,
	quotaMembership string,
	cpusetMount *linuxVisibleCPUMount,
	cpusetMembership string,
) (linuxVisibleCPUBoundary, error) {
	if cpusetMount != nil &&
		!sameLinuxVisibleCPUMount(*cpusetMount, usageMount) {
		if usageMount.root != string(os.PathSeparator) {
			return linuxVisibleCPUBoundary{}, fmt.Errorf(
				"cannot combine an independent cpuset with a non-global CPU accounting root",
			)
		}
		cpusetMount = nil
		cpusetMembership = ""
	}
	if quotaMount != nil && quotaMount.root != usageMount.root {
		return linuxVisibleCPUBoundary{}, fmt.Errorf(
			"cannot prove the CPU quota and usage counters share one visible cgroup root",
		)
	}
	if quotaMount != nil &&
		filepath.Clean(quotaMembership) != filepath.Clean(usageMembership) {
		return linuxVisibleCPUBoundary{}, fmt.Errorf(
			"cannot prove the CPU quota and usage counters share one cgroup membership",
		)
	}
	if cpusetMount != nil && cpusetMount.root != usageMount.root {
		return linuxVisibleCPUBoundary{}, fmt.Errorf(
			"cannot prove the cpuset and CPU usage counters share one visible cgroup root",
		)
	}
	if cpusetMount != nil &&
		filepath.Clean(cpusetMembership) != filepath.Clean(usageMembership) {
		return linuxVisibleCPUBoundary{}, fmt.Errorf(
			"cannot prove the cpuset and CPU usage counters share one cgroup membership",
		)
	}

	var usageFile string
	var usageUnit time.Duration
	switch source {
	case visibleSystemCPUSourceV2:
		usageFile = "cpu.stat"
		usageUnit = time.Microsecond
	case visibleSystemCPUSourceV1:
		usageFile = "cpuacct.usage"
		usageUnit = time.Nanosecond
	default:
		return linuxVisibleCPUBoundary{}, fmt.Errorf(
			"unsupported visible CPU source %q",
			source,
		)
	}
	boundary := linuxVisibleCPUBoundary{
		source: source,
		boundaryKind: classifyLinuxVisibleCPUBoundary(
			usageMount.root,
			usageMembership,
		),
		usageMount:  usageMount,
		usagePath:   filepath.Join(usageMount.mountPoint, usageFile),
		usageUnit:   usageUnit,
		quotaMount:  quotaMount,
		cpusetMount: cpusetMount,
	}
	boundary.boundaryID = hashLinuxVisibleCPUBoundary(
		boundary,
		usageMembership,
		quotaMembership,
		cpusetMembership,
	)
	return boundary, nil
}

func classifyLinuxVisibleCPUBoundary(
	root string,
	membership string,
) VisibleSystemCPUBoundaryKind {
	root = filepath.Clean(root)
	membership = filepath.Clean(membership)
	if root == membership {
		return VisibleSystemCPUBoundarySelf
	}
	if linuxPathWithinRoot(membership, root) {
		return VisibleSystemCPUBoundaryAncestor
	}
	if membership == string(os.PathSeparator) &&
		root != string(os.PathSeparator) {
		return VisibleSystemCPUBoundaryNamespaceRoot
	}
	return VisibleSystemCPUBoundaryUnknown
}

func hashLinuxVisibleCPUBoundary(
	boundary linuxVisibleCPUBoundary,
	usageMembership string,
	quotaMembership string,
	cpusetMembership string,
) string {
	hash := sha256.New()
	writePart := func(value string) {
		length := strconv.Itoa(len(value))
		_, _ = hash.Write([]byte(length))
		_, _ = hash.Write([]byte{':'})
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{'\n'})
	}
	writeMount := func(role string, mount *linuxVisibleCPUMount, member string) {
		writePart(role)
		if mount == nil {
			writePart("none")
			return
		}
		writePart(strconv.FormatUint(mount.id, 10))
		writePart(strconv.Itoa(int(mount.version)))
		writePart(mount.root)
		writePart(mount.mountPoint)
		writePart(member)
	}
	writePart(boundary.source)
	writeMount("usage", &boundary.usageMount, usageMembership)
	writeMount("quota", boundary.quotaMount, quotaMembership)
	writeMount("cpuset", boundary.cpusetMount, cpusetMembership)
	sum := hash.Sum(nil)
	return "cgcpu-" + hex.EncodeToString(sum[:16])
}

func selectLinuxVisibleCPUMount(
	mounts []linuxVisibleCPUMount,
	version linuxCPUCgroupVersion,
	controller string,
	membership string,
) (linuxVisibleCPUMount, bool, error) {
	var eligible []linuxVisibleCPUMount
	for _, mount := range mounts {
		if mount.version != version {
			continue
		}
		if version == linuxCPUCgroupV1 && !mount.controllers[controller] {
			continue
		}
		eligible = append(eligible, mount)
	}
	if len(eligible) == 0 {
		return linuxVisibleCPUMount{}, false, nil
	}

	var direct []linuxVisibleCPUMount
	maximumRootLength := -1
	for _, mount := range eligible {
		if !linuxPathWithinRoot(membership, mount.root) {
			continue
		}
		rootLength := len(mount.root)
		if rootLength > maximumRootLength {
			maximumRootLength = rootLength
			direct = direct[:0]
		}
		if rootLength == maximumRootLength {
			direct = append(direct, mount)
		}
	}
	if len(direct) > 0 {
		sortLinuxVisibleCPUMounts(direct)
		return direct[0], true, nil
	}

	roots := make(map[string]bool)
	for _, mount := range eligible {
		roots[mount.root] = true
	}
	if len(roots) != 1 {
		return linuxVisibleCPUMount{}, false, fmt.Errorf(
			"cgroup v%d %s membership %q has ambiguous visible mount roots",
			version,
			visibleCPUControllerName(controller),
			membership,
		)
	}
	sortLinuxVisibleCPUMounts(eligible)
	return eligible[0], true, nil
}

func sortLinuxVisibleCPUMounts(mounts []linuxVisibleCPUMount) {
	sort.Slice(mounts, func(left, right int) bool {
		if len(mounts[left].mountPoint) != len(mounts[right].mountPoint) {
			return len(mounts[left].mountPoint) < len(mounts[right].mountPoint)
		}
		return mounts[left].mountPoint < mounts[right].mountPoint
	})
}

func visibleCPUControllerName(controller string) string {
	if controller == "" {
		return "unified"
	}
	return controller
}

func readLinuxVisibleCPUUsage(
	boundary linuxVisibleCPUBoundary,
	readFile func(string) ([]byte, error),
) (uint64, error) {
	data, err := readFile(boundary.usagePath)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", boundary.usagePath, err)
	}
	var value uint64
	switch boundary.source {
	case visibleSystemCPUSourceV2:
		value, err = parseLinuxCgroupV2CPUUsage(data)
	case visibleSystemCPUSourceV1:
		value, err = parseLinuxCgroupV1CPUUsage(data)
	default:
		err = fmt.Errorf("unsupported visible CPU source %q", boundary.source)
	}
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", boundary.usagePath, err)
	}
	return value, nil
}

func parseLinuxCgroupV2CPUUsage(data []byte) (uint64, error) {
	var (
		usage uint64
		found bool
	)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "usage_usec" {
			continue
		}
		if found {
			return 0, fmt.Errorf("duplicate usage_usec field")
		}
		if len(fields) != 2 {
			return 0, fmt.Errorf("usage_usec must contain one value")
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid usage_usec value %q", fields[1])
		}
		usage = value
		found = true
	}
	if !found {
		return 0, fmt.Errorf("usage_usec field is missing")
	}
	return usage, nil
}

func parseLinuxCgroupV1CPUUsage(data []byte) (uint64, error) {
	value := strings.TrimSpace(string(data))
	if value == "" {
		return 0, fmt.Errorf("cpuacct.usage value is empty")
	}
	usage, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid cpuacct.usage value %q", value)
	}
	return usage, nil
}

func readLinuxVisibleCPUCapacity(
	boundary linuxVisibleCPUBoundary,
	hostCPUs int,
	readFile func(string) ([]byte, error),
) (float64, error) {
	capacity := float64(hostCPUs)
	if boundary.quotaMount != nil {
		quota, limited, valid, err := readLinuxVisibleCPUQuota(
			*boundary.quotaMount,
			readFile,
		)
		if err != nil {
			return 0, err
		}
		if valid && limited {
			capacity = math.Min(capacity, quota)
		}
	}
	if boundary.cpusetMount != nil {
		cpusetCPUs, valid, err := readLinuxVisibleCPUSet(
			*boundary.cpusetMount,
			readFile,
		)
		if err != nil {
			return 0, err
		}
		if valid {
			capacity = math.Min(capacity, float64(cpusetCPUs))
		}
	}
	if !isFinite(capacity) || capacity <= 0 {
		return 0, fmt.Errorf(
			"visible cgroup root leaves no finite CPU capacity",
		)
	}
	return capacity, nil
}

func readLinuxVisibleCPUQuota(
	mount linuxVisibleCPUMount,
	readFile func(string) ([]byte, error),
) (float64, bool, bool, error) {
	switch mount.version {
	case linuxCPUCgroupV2:
		path := filepath.Join(mount.mountPoint, "cpu.max")
		data, err := readFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			globalRoot, controllerErr := linuxVisibleV2GlobalRootHasCPUController(
				mount,
				readFile,
			)
			if controllerErr != nil {
				return 0, false, false, controllerErr
			}
			if globalRoot {
				return 0, false, true, nil
			}
			return 0, false, false, fmt.Errorf(
				"visible cgroup v2 CPU quota interface is missing",
			)
		}
		if err != nil {
			return 0, false, false, fmt.Errorf("read %s: %w", path, err)
		}
		quota, limited, err := parseCgroupV2CPUMax(string(data))
		if err != nil {
			return 0, false, false, fmt.Errorf("parse %s: %w", path, err)
		}
		return quota, limited, true, nil
	case linuxCPUCgroupV1:
		quotaPath := filepath.Join(mount.mountPoint, "cpu.cfs_quota_us")
		quotaData, err := readFile(quotaPath)
		if errors.Is(err, fs.ErrNotExist) {
			return 0, false, false, fmt.Errorf(
				"visible cgroup v1 CPU quota interface is missing",
			)
		}
		if err != nil {
			return 0, false, false, fmt.Errorf("read %s: %w", quotaPath, err)
		}
		quota, err := strconv.ParseInt(strings.TrimSpace(string(quotaData)), 10, 64)
		if err != nil {
			return 0, false, false, fmt.Errorf("parse %s: %w", quotaPath, err)
		}
		if quota == -1 {
			return 0, false, true, nil
		}
		if quota <= 0 {
			return 0, false, false, fmt.Errorf(
				"invalid cgroup v1 CPU quota %d in %s",
				quota,
				quotaPath,
			)
		}
		periodPath := filepath.Join(mount.mountPoint, "cpu.cfs_period_us")
		periodData, err := readFile(periodPath)
		if err != nil {
			return 0, false, false, fmt.Errorf("read %s: %w", periodPath, err)
		}
		period, err := strconv.ParseInt(strings.TrimSpace(string(periodData)), 10, 64)
		if err != nil || period <= 0 {
			return 0, false, false, fmt.Errorf(
				"invalid cgroup v1 CPU period in %s",
				periodPath,
			)
		}
		result := float64(quota) / float64(period)
		if !isFinite(result) || result <= 0 {
			return 0, false, false, fmt.Errorf(
				"invalid cgroup v1 CPU capacity in %s",
				mount.mountPoint,
			)
		}
		return result, true, true, nil
	default:
		return 0, false, false, fmt.Errorf(
			"unsupported CPU cgroup version %d",
			mount.version,
		)
	}
}

func linuxVisibleV2GlobalRootHasCPUController(
	mount linuxVisibleCPUMount,
	readFile func(string) ([]byte, error),
) (bool, error) {
	if mount.root != string(os.PathSeparator) {
		return false, nil
	}
	path := filepath.Join(mount.mountPoint, "cgroup.controllers")
	data, err := readFile(path)
	if err != nil {
		return false, fmt.Errorf(
			"read visible cgroup v2 controllers: %w",
			err,
		)
	}
	for _, controller := range strings.Fields(string(data)) {
		if controller == "cpu" {
			return true, nil
		}
	}
	return false, nil
}

func readLinuxVisibleCPUSet(
	mount linuxVisibleCPUMount,
	readFile func(string) ([]byte, error),
) (int, bool, error) {
	var names []string
	switch mount.version {
	case linuxCPUCgroupV2:
		names = []string{"cpuset.cpus.effective", "cpuset.cpus"}
	case linuxCPUCgroupV1:
		names = []string{"cpuset.effective_cpus", "cpuset.cpus"}
	default:
		return 0, false, fmt.Errorf(
			"unsupported CPU cgroup version %d",
			mount.version,
		)
	}

	for _, name := range names {
		path := filepath.Join(mount.mountPoint, name)
		data, err := readFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, false, fmt.Errorf("read %s: %w", path, err)
		}
		value := strings.TrimSpace(string(data))
		if value == "" {
			continue
		}
		count, err := parseLinuxCPUList(value)
		if err != nil {
			return 0, false, fmt.Errorf("parse %s: %w", path, err)
		}
		if count <= 0 {
			return 0, false, fmt.Errorf("%s contains no CPUs", path)
		}
		return count, true, nil
	}
	return 0, false, nil
}

func calculateLinuxVisibleCPUPercent(
	usageDelta uint64,
	usageUnit time.Duration,
	elapsed time.Duration,
	cpus float64,
) (float64, time.Duration, error) {
	if usageUnit <= 0 {
		return 0, 0, fmt.Errorf("visible system CPU counter unit is invalid")
	}
	if elapsed <= 0 {
		return 0, 0, fmt.Errorf(
			"visible system CPU sample interval must be greater than zero",
		)
	}
	if !isFinite(cpus) || cpus <= 0 {
		return 0, 0, fmt.Errorf("visible system CPU capacity is invalid")
	}
	unit := uint64(usageUnit)
	if usageDelta > uint64(math.MaxInt64)/unit {
		return 0, 0, fmt.Errorf(
			"visible system CPU busy duration is out of range",
		)
	}
	busy := time.Duration(usageDelta * unit)
	usageSeconds := busy.Seconds()
	denominator := elapsed.Seconds() * cpus
	percent := usageSeconds / denominator * 100
	if !isFinite(usageSeconds) || usageSeconds < 0 ||
		!isFinite(denominator) || denominator <= 0 ||
		!isFinite(percent) || percent < 0 {
		return 0, 0, fmt.Errorf(
			"visible system CPU utilization calculation is out of range",
		)
	}
	return percent, busy, nil
}

func waitVisibleSystemCPUSample(
	ctx context.Context,
	duration time.Duration,
) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
