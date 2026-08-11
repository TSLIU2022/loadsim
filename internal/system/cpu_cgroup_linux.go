//go:build linux

package system

import (
	"bufio"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	cpuCgroupLevelSelf     = "self"
	cpuCgroupLevelAncestor = "ancestor"
)

type cpuCgroupMembership struct {
	v2Path    string
	hasV2     bool
	v1CPUPath string
	hasV1CPU  bool
}

type cpuCgroupMount struct {
	version    cgroupVersion
	root       string
	mountPoint string
}

type cpuCgroupDependencies struct {
	readFile func(string) ([]byte, error)
	stat     func(string) (fs.FileInfo, error)
}

func inspectCPUCgroup() (CPUCgroupInfo, error) {
	membership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return CPUCgroupInfo{}, fmt.Errorf("read CPU cgroup membership: %w", err)
	}
	mountInfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return CPUCgroupInfo{}, fmt.Errorf("read CPU cgroup mounts: %w", err)
	}
	return inspectCPUCgroupFrom(
		string(membership),
		string(mountInfo),
		cpuCgroupDependencies{
			readFile: os.ReadFile,
			stat:     os.Stat,
		},
	)
}

func inspectCPUCgroupFrom(
	membershipText string,
	mountInfoText string,
	dependencies cpuCgroupDependencies,
) (CPUCgroupInfo, error) {
	if dependencies.readFile == nil || dependencies.stat == nil {
		return CPUCgroupInfo{}, fmt.Errorf(
			"CPU cgroup inspector dependencies are incomplete",
		)
	}
	membership, err := parseCPUCgroupMembership(membershipText)
	if err != nil {
		return CPUCgroupInfo{}, err
	}
	mounts, err := parseCPUCgroupMounts(mountInfoText)
	if err != nil {
		return CPUCgroupInfo{}, err
	}

	// An explicit v1 CPU membership is authoritative on a hybrid host. The
	// unified hierarchy can exist without owning the CPU controller there.
	if membership.hasV1CPU {
		return inspectCPUCgroupMounts(
			mounts,
			membership.v1CPUPath,
			cgroupV1,
			dependencies,
		)
	}
	if membership.hasV2 {
		return inspectCPUCgroupMounts(
			mounts,
			membership.v2Path,
			cgroupV2,
			dependencies,
		)
	}
	return CPUCgroupInfo{Version: "none", QuotaKnown: true}, nil
}

func parseCPUCgroupMembership(value string) (cpuCgroupMembership, error) {
	var result cpuCgroupMembership
	for lineNumber, rawLine := range strings.Split(value, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 {
			return cpuCgroupMembership{}, fmt.Errorf(
				"parse CPU cgroup membership line %d: expected three fields",
				lineNumber+1,
			)
		}
		group := filepath.Clean(fields[2])
		if !filepath.IsAbs(group) {
			return cpuCgroupMembership{}, fmt.Errorf(
				"parse CPU cgroup membership line %d: path is not absolute",
				lineNumber+1,
			)
		}
		if fields[1] == "" {
			if result.hasV2 && result.v2Path != group {
				return cpuCgroupMembership{}, fmt.Errorf(
					"conflicting cgroup v2 CPU memberships",
				)
			}
			result.v2Path = group
			result.hasV2 = true
			continue
		}
		for _, controller := range strings.Split(fields[1], ",") {
			if controller != "cpu" {
				continue
			}
			if result.hasV1CPU && result.v1CPUPath != group {
				return cpuCgroupMembership{}, fmt.Errorf(
					"conflicting cgroup v1 CPU memberships",
				)
			}
			result.v1CPUPath = group
			result.hasV1CPU = true
			break
		}
	}
	return result, nil
}

func parseCPUCgroupMounts(value string) ([]cpuCgroupMount, error) {
	scanner := bufio.NewScanner(strings.NewReader(value))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var mounts []cpuCgroupMount
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
				"parse CPU cgroup mountinfo line %d: malformed entry",
				lineNumber,
			)
		}

		var version cgroupVersion
		switch fields[separator+1] {
		case "cgroup2":
			version = cgroupV2
		case "cgroup":
			if !mountInfoHasController(fields, separator, "cpu") {
				continue
			}
			version = cgroupV1
		default:
			continue
		}
		root, err := unescapeMountInfoPath(fields[3])
		if err != nil {
			return nil, fmt.Errorf(
				"parse CPU cgroup mountinfo line %d root: %w",
				lineNumber,
				err,
			)
		}
		mountPoint, err := unescapeMountInfoPath(fields[4])
		if err != nil {
			return nil, fmt.Errorf(
				"parse CPU cgroup mountinfo line %d mount point: %w",
				lineNumber,
				err,
			)
		}
		if !filepath.IsAbs(root) || !filepath.IsAbs(mountPoint) {
			return nil, fmt.Errorf(
				"parse CPU cgroup mountinfo line %d: paths must be absolute",
				lineNumber,
			)
		}
		mounts = append(mounts, cpuCgroupMount{
			version:    version,
			root:       filepath.Clean(root),
			mountPoint: filepath.Clean(mountPoint),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan CPU cgroup mountinfo: %w", err)
	}
	return mounts, nil
}

func inspectCPUCgroupMounts(
	mounts []cpuCgroupMount,
	membership string,
	version cgroupVersion,
	dependencies cpuCgroupDependencies,
) (CPUCgroupInfo, error) {
	var matching []cpuCgroupMount
	var direct []cpuCgroupMount
	for _, mount := range mounts {
		if mount.version != version {
			continue
		}
		matching = append(matching, mount)
		if pathWithinCgroupRoot(membership, mount.root) {
			direct = append(direct, mount)
		}
	}
	if len(direct) > 0 {
		matching = direct
	}
	if len(matching) == 0 {
		return CPUCgroupInfo{}, fmt.Errorf(
			"cgroup v%d CPU membership has no matching mount",
			version,
		)
	}

	for _, mount := range matching {
		groups, err := cgroupGroupCandidates(mount.root, membership)
		if err != nil {
			return CPUCgroupInfo{}, err
		}
		for _, group := range groups {
			groupPath := filepath.Join(mount.mountPoint, group)
			info, err := dependencies.stat(groupPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return CPUCgroupInfo{}, fmt.Errorf(
					"stat CPU cgroup directory: %w",
					err,
				)
			}
			if !info.IsDir() {
				return CPUCgroupInfo{}, fmt.Errorf(
					"CPU cgroup membership is not a directory",
				)
			}
			return inspectCPUCgroupHierarchy(
				mount.mountPoint,
				group,
				version,
				dependencies.readFile,
			)
		}
	}
	return CPUCgroupInfo{}, fmt.Errorf(
		"cgroup v%d CPU membership has no visible directory",
		version,
	)
}

func inspectCPUCgroupHierarchy(
	root string,
	group string,
	version cgroupVersion,
	readFile func(string) ([]byte, error),
) (CPUCgroupInfo, error) {
	info := CPUCgroupInfo{
		Version:    fmt.Sprintf("v%d", version),
		QuotaKnown: true,
	}
	minimumQuota := math.Inf(1)
	for index, directory := range cgroupDirectories(root, group) {
		level := cpuCgroupLevelAncestor
		if index == 0 {
			level = cpuCgroupLevelSelf
		}
		var err error
		switch version {
		case cgroupV2:
			err = inspectCPUCgroupV2Directory(
				directory,
				level,
				readFile,
				&info,
				&minimumQuota,
			)
		case cgroupV1:
			err = inspectCPUCgroupV1Directory(
				directory,
				level,
				readFile,
				&info,
				&minimumQuota,
			)
		default:
			err = fmt.Errorf("unsupported CPU cgroup version %d", version)
		}
		if err != nil {
			return CPUCgroupInfo{}, err
		}
	}
	if info.QuotaLimited {
		info.QuotaCPUs = minimumQuota
	}
	return info, nil
}

func inspectCPUCgroupV2Directory(
	directory string,
	level string,
	readFile func(string) ([]byte, error),
	info *CPUCgroupInfo,
	minimumQuota *float64,
) error {
	if raw, ok, err := readOptionalCPUCgroupFile(
		filepath.Join(directory, "cpu.max"),
		readFile,
	); err != nil {
		return err
	} else if ok {
		quota, limited, err := parseCPUCgroupV2Max(string(raw))
		if err != nil {
			return fmt.Errorf("parse cgroup v2 cpu.max: %w", err)
		}
		if limited && quota < *minimumQuota {
			*minimumQuota = quota
			info.QuotaLimited = true
			info.QuotaLevel = level
		}
	}
	if raw, ok, err := readOptionalCPUCgroupFile(
		filepath.Join(directory, "cpu.weight"),
		readFile,
	); err != nil {
		return err
	} else if ok {
		weight, err := parsePositiveCPUCgroupUint(raw, "cpu.weight")
		if err != nil {
			return err
		}
		observeCPUCgroupWeight(info, "weight", weight, level)
	}
	return inspectCPUCgroupStatFile(
		filepath.Join(directory, "cpu.stat"),
		cgroupV2,
		level,
		readFile,
		info,
	)
}

func inspectCPUCgroupV1Directory(
	directory string,
	level string,
	readFile func(string) ([]byte, error),
	info *CPUCgroupInfo,
	minimumQuota *float64,
) error {
	quotaRaw, quotaOK, err := readOptionalCPUCgroupFile(
		filepath.Join(directory, "cpu.cfs_quota_us"),
		readFile,
	)
	if err != nil {
		return err
	}
	if quotaOK {
		quota, err := strconv.ParseInt(strings.TrimSpace(string(quotaRaw)), 10, 64)
		if err != nil {
			return fmt.Errorf("parse cgroup v1 CPU quota: %w", err)
		}
		if quota == 0 || quota < -1 {
			return fmt.Errorf("cgroup v1 CPU quota is invalid")
		}
		if quota > 0 {
			periodRaw, periodOK, err := readOptionalCPUCgroupFile(
				filepath.Join(directory, "cpu.cfs_period_us"),
				readFile,
			)
			if err != nil {
				return err
			}
			if !periodOK {
				return fmt.Errorf("cgroup v1 CPU period is missing")
			}
			period, err := strconv.ParseInt(
				strings.TrimSpace(string(periodRaw)),
				10,
				64,
			)
			if err != nil || period <= 0 {
				return fmt.Errorf("cgroup v1 CPU period is invalid")
			}
			candidate := float64(quota) / float64(period)
			if candidate < *minimumQuota {
				*minimumQuota = candidate
				info.QuotaLimited = true
				info.QuotaLevel = level
			}
		}
	}
	if raw, ok, err := readOptionalCPUCgroupFile(
		filepath.Join(directory, "cpu.shares"),
		readFile,
	); err != nil {
		return err
	} else if ok {
		shares, err := parsePositiveCPUCgroupUint(raw, "cpu.shares")
		if err != nil {
			return err
		}
		observeCPUCgroupWeight(info, "shares", shares, level)
	}
	return inspectCPUCgroupStatFile(
		filepath.Join(directory, "cpu.stat"),
		cgroupV1,
		level,
		readFile,
		info,
	)
}

func parseCPUCgroupV2Max(value string) (float64, bool, error) {
	fields := strings.Fields(value)
	if len(fields) != 2 {
		return 0, false, fmt.Errorf("expected quota and period")
	}
	period, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil || period == 0 {
		return 0, false, fmt.Errorf("period must be greater than zero")
	}
	if fields[0] == "max" {
		return 0, false, nil
	}
	quota, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil || quota == 0 {
		return 0, false, fmt.Errorf("quota must be greater than zero")
	}
	return float64(quota) / float64(period), true, nil
}

func readOptionalCPUCgroupFile(
	path string,
	readFile func(string) ([]byte, error),
) ([]byte, bool, error) {
	raw, err := readFile(path)
	if err == nil {
		return raw, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("read CPU cgroup control file: %w", err)
}

func parsePositiveCPUCgroupUint(raw []byte, name string) (uint64, error) {
	value, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func observeCPUCgroupWeight(
	info *CPUCgroupInfo,
	kind string,
	value uint64,
	level string,
) {
	if !info.WeightKnown || value < info.Weight {
		info.WeightKnown = true
		info.WeightKind = kind
		info.Weight = value
		info.WeightLevel = level
	}
}

func inspectCPUCgroupStatFile(
	path string,
	version cgroupVersion,
	level string,
	readFile func(string) ([]byte, error),
	info *CPUCgroupInfo,
) error {
	raw, ok, err := readOptionalCPUCgroupFile(path, readFile)
	if err != nil || !ok {
		return err
	}
	periods, throttled, duration, known, err := parseCPUCgroupStat(
		string(raw),
		version,
	)
	if err != nil {
		return err
	}
	if !known {
		return nil
	}
	if !info.ThrottlingKnown ||
		duration > info.ThrottledTime ||
		(duration == info.ThrottledTime && throttled > info.ThrottledPeriods) {
		info.ThrottlingKnown = true
		info.Periods = periods
		info.ThrottledPeriods = throttled
		info.ThrottledTime = duration
		info.ThrottlingLevel = level
	}
	return nil
}

func parseCPUCgroupStat(
	value string,
	version cgroupVersion,
) (uint64, uint64, time.Duration, bool, error) {
	var periods uint64
	var throttled uint64
	var rawDuration uint64
	var hasPeriods bool
	var hasThrottled bool
	var hasDuration bool
	durationName := "throttled_time"
	durationUnit := time.Nanosecond
	if version == cgroupV2 {
		durationName = "throttled_usec"
		durationUnit = time.Microsecond
	}
	for _, line := range strings.Split(value, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return 0, 0, 0, false, fmt.Errorf("malformed CPU cgroup stat")
		}
		var target *uint64
		switch fields[0] {
		case "nr_periods":
			target = &periods
			hasPeriods = true
		case "nr_throttled":
			target = &throttled
			hasThrottled = true
		case durationName:
			target = &rawDuration
			hasDuration = true
		default:
			continue
		}
		parsed, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, 0, 0, false, fmt.Errorf(
				"parse CPU cgroup stat %s: %w",
				fields[0],
				err,
			)
		}
		*target = parsed
	}
	known := hasPeriods || hasThrottled || hasDuration
	if rawDuration > uint64(math.MaxInt64)/uint64(durationUnit) {
		return 0, 0, 0, false, fmt.Errorf(
			"CPU cgroup throttled duration is out of range",
		)
	}
	return periods,
		throttled,
		time.Duration(rawDuration) * durationUnit,
		known,
		nil
}
