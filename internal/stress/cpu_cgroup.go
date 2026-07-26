package stress

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type linuxCPUCgroupVersion uint8

const (
	linuxCPUCgroupV1 linuxCPUCgroupVersion = 1
	linuxCPUCgroupV2 linuxCPUCgroupVersion = 2
)

type linuxCPUCgroupMount struct {
	version    linuxCPUCgroupVersion
	root       string
	mountPoint string
}

type cpuQuotaConstraint struct {
	quota   float64
	limited bool
	valid   bool
}

func linuxCgroupCPUQuota() (float64, bool, error) {
	membership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return 0, false, fmt.Errorf("read cgroup membership: %w", err)
	}
	mountInfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return 0, false, fmt.Errorf("read cgroup mounts: %w", err)
	}
	return linuxCgroupCPUQuotaFrom(string(membership), string(mountInfo))
}

func linuxCgroupCPUQuotaFrom(
	membership string,
	mountInfo string,
) (float64, bool, error) {
	v2Path, hasV2, v1CPUPath, hasV1, err := parseLinuxCgroupPaths(membership)
	if err != nil {
		return 0, false, err
	}
	if !hasV2 && !hasV1 {
		return 0, false, nil
	}

	mounts, err := parseLinuxCPUCgroupMounts(mountInfo)
	if err != nil {
		return 0, false, err
	}

	var v2Constraint cpuQuotaConstraint
	if hasV2 {
		v2Constraint, err = readCPUCgroupMounts(
			mounts,
			v2Path,
			linuxCPUCgroupV2,
		)
		if err != nil {
			return 0, false, err
		}
	}

	var v1Constraint cpuQuotaConstraint
	if hasV1 {
		v1Constraint, err = readCPUCgroupMounts(
			mounts,
			v1CPUPath,
			linuxCPUCgroupV1,
		)
		if err != nil {
			return 0, false, err
		}
		if !v1Constraint.valid {
			return 0, false, fmt.Errorf(
				"cgroup v1 CPU membership %q has no readable CPU controller mount",
				v1CPUPath,
			)
		}
	}

	// A hybrid host always has a unified membership, even when the CPU
	// controller remains attached to v1. In that case v2 legitimately has no
	// cpu.max files, while the explicit v1 CPU membership remains authoritative.
	if hasV2 && !v2Constraint.valid && !hasV1 {
		return 0, false, fmt.Errorf(
			"cgroup v2 membership %q has no readable CPU controller mount",
			v2Path,
		)
	}

	selected := tighterCPUQuotaConstraint(v2Constraint, v1Constraint)
	return selected.quota, selected.limited, nil
}

func parseLinuxCgroupPaths(
	membership string,
) (v2Path string, hasV2 bool, v1CPUPath string, hasV1 bool, err error) {
	for lineNumber, rawLine := range strings.Split(membership, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 {
			return "", false, "", false, fmt.Errorf(
				"parse cgroup membership line %d: expected three fields",
				lineNumber+1,
			)
		}
		group := filepath.Clean(fields[2])
		if !filepath.IsAbs(group) {
			return "", false, "", false, fmt.Errorf(
				"parse cgroup membership line %d: path %q is not absolute",
				lineNumber+1,
				fields[2],
			)
		}

		if fields[1] == "" {
			if hasV2 && v2Path != group {
				return "", false, "", false, fmt.Errorf(
					"conflicting cgroup v2 memberships %q and %q",
					v2Path,
					group,
				)
			}
			v2Path = group
			hasV2 = true
			continue
		}
		for _, controller := range strings.Split(fields[1], ",") {
			if controller != "cpu" {
				continue
			}
			if hasV1 && v1CPUPath != group {
				return "", false, "", false, fmt.Errorf(
					"conflicting cgroup v1 CPU memberships %q and %q",
					v1CPUPath,
					group,
				)
			}
			v1CPUPath = group
			hasV1 = true
			break
		}
	}
	return v2Path, hasV2, v1CPUPath, hasV1, nil
}

func parseLinuxCPUCgroupMounts(mountInfo string) ([]linuxCPUCgroupMount, error) {
	scanner := bufio.NewScanner(strings.NewReader(mountInfo))
	scanner.Buffer(make([]byte, 4096), 1024*1024)

	var mounts []linuxCPUCgroupMount
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
			return nil, fmt.Errorf("parse mountinfo line %d: malformed entry", lineNumber)
		}

		var version linuxCPUCgroupVersion
		switch fields[separator+1] {
		case "cgroup2":
			version = linuxCPUCgroupV2
		case "cgroup":
			if !linuxMountInfoHasController(fields, separator, "cpu") {
				continue
			}
			version = linuxCPUCgroupV1
		default:
			continue
		}

		root, err := unescapeLinuxMountInfoPath(fields[3])
		if err != nil {
			return nil, fmt.Errorf("parse mountinfo line %d root: %w", lineNumber, err)
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
		mounts = append(mounts, linuxCPUCgroupMount{
			version:    version,
			root:       filepath.Clean(root),
			mountPoint: filepath.Clean(mountPoint),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse mountinfo: %w", err)
	}
	return mounts, nil
}

func linuxMountInfoHasController(
	fields []string,
	separator int,
	controller string,
) bool {
	for _, field := range []string{
		fields[5],
		fields[separator+2],
		fields[separator+3],
	} {
		for _, option := range strings.Split(field, ",") {
			if option == controller {
				return true
			}
		}
	}
	return false
}

func unescapeLinuxMountInfoPath(value string) (string, error) {
	var decoded strings.Builder
	decoded.Grow(len(value))
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' {
			decoded.WriteByte(value[index])
			continue
		}
		if index+3 >= len(value) {
			return "", fmt.Errorf("invalid escape in %q", value)
		}
		var escaped uint16
		for offset := 1; offset <= 3; offset++ {
			digit := value[index+offset]
			if digit < '0' || digit > '7' {
				return "", fmt.Errorf("invalid escape in %q", value)
			}
			escaped = escaped*8 + uint16(digit-'0')
		}
		if escaped > 255 {
			return "", fmt.Errorf("invalid escape in %q", value)
		}
		decoded.WriteByte(byte(escaped))
		index += 3
	}
	return decoded.String(), nil
}

func readCPUCgroupMounts(
	mounts []linuxCPUCgroupMount,
	membership string,
	version linuxCPUCgroupVersion,
) (cpuQuotaConstraint, error) {
	versionMounts := make([]linuxCPUCgroupMount, 0, len(mounts))
	directMounts := make([]linuxCPUCgroupMount, 0, len(mounts))
	for _, mount := range mounts {
		if mount.version != version {
			continue
		}
		versionMounts = append(versionMounts, mount)
		if linuxPathWithinRoot(
			filepath.Clean(membership),
			filepath.Clean(mount.root),
		) {
			directMounts = append(directMounts, mount)
		}
	}
	if len(directMounts) > 0 {
		versionMounts = directMounts
	}
	if len(versionMounts) == 0 {
		return cpuQuotaConstraint{}, fmt.Errorf(
			"cgroup v%d CPU membership %q has no matching mountinfo entry",
			version,
			membership,
		)
	}

	var selected cpuQuotaConstraint
	for _, mount := range versionMounts {
		groups, err := linuxCgroupGroupCandidates(mount.root, membership)
		if err != nil {
			return cpuQuotaConstraint{}, err
		}
		for _, group := range groups {
			groupPath := filepath.Join(mount.mountPoint, group)
			info, err := os.Stat(groupPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return cpuQuotaConstraint{}, fmt.Errorf("stat %s: %w", groupPath, err)
			}
			if !info.IsDir() {
				return cpuQuotaConstraint{}, fmt.Errorf(
					"%s is not a cgroup directory",
					groupPath,
				)
			}

			var constraint cpuQuotaConstraint
			switch version {
			case linuxCPUCgroupV2:
				constraint, err = readCgroupV2CPUQuota(
					mount.mountPoint,
					group,
				)
			case linuxCPUCgroupV1:
				constraint, err = readCgroupV1CPUQuota(
					mount.mountPoint,
					group,
				)
			default:
				err = fmt.Errorf("unsupported CPU cgroup version %d", version)
			}
			if err != nil {
				return cpuQuotaConstraint{}, fmt.Errorf(
					"read cgroup v%d CPU mount %s: %w",
					version,
					mount.mountPoint,
					err,
				)
			}
			selected = tighterCPUQuotaConstraint(selected, constraint)
			if constraint.valid {
				break
			}
		}
	}
	return selected, nil
}

func linuxCgroupGroupCandidates(mountRoot, membership string) ([]string, error) {
	mountRoot = filepath.Clean(mountRoot)
	membership = filepath.Clean(membership)
	if !filepath.IsAbs(mountRoot) || !filepath.IsAbs(membership) {
		return nil, fmt.Errorf(
			"cgroup mount root %q and membership %q must be absolute",
			mountRoot,
			membership,
		)
	}

	var candidates []string
	appendCandidate := func(group string) {
		group = filepath.Clean(group)
		if group == "." {
			group = ""
		}
		for _, existing := range candidates {
			if existing == group {
				return
			}
		}
		candidates = append(candidates, group)
	}

	if linuxPathWithinRoot(membership, mountRoot) {
		relative, err := filepath.Rel(mountRoot, membership)
		if err != nil {
			return nil, err
		}
		appendCandidate(relative)
	}
	// A cgroup namespace can report membership relative to its namespace root
	// while mountinfo retains the underlying cgroup filesystem root.
	appendCandidate(strings.TrimPrefix(membership, string(os.PathSeparator)))
	return candidates, nil
}

func linuxPathWithinRoot(path, root string) bool {
	if root == string(os.PathSeparator) {
		return true
	}
	return path == root ||
		strings.HasPrefix(path, root+string(os.PathSeparator))
}

func cgroupHierarchy(root, group string) []string {
	root = filepath.Clean(root)
	group = filepath.Clean("/" + group)
	current := filepath.Join(root, strings.TrimPrefix(group, "/"))

	var directories []string
	for {
		directories = append(directories, current)
		if current == root {
			break
		}
		parent := filepath.Dir(current)
		if parent == current ||
			(parent != root &&
				!strings.HasPrefix(parent, root+string(os.PathSeparator))) {
			break
		}
		current = parent
	}
	return directories
}

func readCgroupV2CPUQuota(
	root string,
	group string,
) (cpuQuotaConstraint, error) {
	constraint := cpuQuotaConstraint{}
	minimum := math.Inf(1)
	for _, directory := range cgroupHierarchy(root, group) {
		path := filepath.Join(directory, "cpu.max")
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return cpuQuotaConstraint{}, fmt.Errorf("read %s: %w", path, err)
		}
		constraint.valid = true
		quota, limited, err := parseCgroupV2CPUMax(string(data))
		if err != nil {
			return cpuQuotaConstraint{}, fmt.Errorf("parse %s: %w", path, err)
		}
		if limited {
			minimum = math.Min(minimum, quota)
			constraint.limited = true
		}
	}
	if constraint.limited {
		constraint.quota = minimum
	}
	return constraint, nil
}

func parseCgroupV2CPUMax(value string) (float64, bool, error) {
	fields := strings.Fields(value)
	if len(fields) != 2 {
		return 0, false, fmt.Errorf("expected quota and period")
	}
	if fields[0] == "max" {
		return 0, false, nil
	}
	quota, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("invalid quota %q", fields[0])
	}
	period, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || quota <= 0 || period <= 0 {
		return 0, false, fmt.Errorf("quota and period must be greater than zero")
	}
	return float64(quota) / float64(period), true, nil
}

func readCgroupV1CPUQuota(
	root string,
	group string,
) (cpuQuotaConstraint, error) {
	constraint := cpuQuotaConstraint{}
	minimum := math.Inf(1)
	for _, directory := range cgroupHierarchy(root, group) {
		quotaPath := filepath.Join(directory, "cpu.cfs_quota_us")
		quotaData, err := os.ReadFile(quotaPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return cpuQuotaConstraint{}, fmt.Errorf("read %s: %w", quotaPath, err)
		}
		constraint.valid = true
		quota, err := strconv.ParseInt(strings.TrimSpace(string(quotaData)), 10, 64)
		if err != nil {
			return cpuQuotaConstraint{}, fmt.Errorf("parse %s: %w", quotaPath, err)
		}
		if quota < 0 {
			continue
		}

		periodPath := filepath.Join(directory, "cpu.cfs_period_us")
		periodData, err := os.ReadFile(periodPath)
		if err != nil {
			return cpuQuotaConstraint{}, fmt.Errorf("read %s: %w", periodPath, err)
		}
		period, err := strconv.ParseInt(strings.TrimSpace(string(periodData)), 10, 64)
		if err != nil || quota <= 0 || period <= 0 {
			return cpuQuotaConstraint{}, fmt.Errorf(
				"invalid cgroup v1 quota or period in %s",
				directory,
			)
		}
		minimum = math.Min(minimum, float64(quota)/float64(period))
		constraint.limited = true
	}
	if constraint.limited {
		constraint.quota = minimum
	}
	return constraint, nil
}

func tighterCPUQuotaConstraint(
	first cpuQuotaConstraint,
	second cpuQuotaConstraint,
) cpuQuotaConstraint {
	if !first.valid {
		return second
	}
	if !second.valid {
		return first
	}
	if !first.limited {
		return second
	}
	if !second.limited {
		return first
	}
	if second.quota < first.quota {
		return second
	}
	return first
}
