package system

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/mem"
)

const bytesPerMB = 1024 * 1024

// MemoryCapacity describes the memory currently available to this process.
type MemoryCapacity struct {
	AvailableMB uint64
	TotalMB     uint64
	UsedMB      uint64
	Source      string
}

// MemoryCapacities returns host memory and every visible finite cgroup memory
// constraint that applies to this process. Callers that enforce safety limits
// must evaluate every entry independently: the smallest absolute free-memory
// value is not necessarily the entry closest to its percentage threshold.
func MemoryCapacities() ([]MemoryCapacity, error) {
	host, err := mem.VirtualMemory()
	if err != nil {
		return nil, fmt.Errorf("read host memory: %w", err)
	}

	capacities := []MemoryCapacity{{
		AvailableMB: host.Available / bytesPerMB,
		TotalMB:     host.Total / bytesPerMB,
		UsedMB:      host.Used / bytesPerMB,
		Source:      "host",
	}}

	constraints, err := currentCgroupMemoryConstraints()
	if err != nil {
		return nil, fmt.Errorf("read cgroup memory: %w", err)
	}
	for _, constraint := range constraints {
		if !constraint.valid || !constraint.finite {
			continue
		}

		available := uint64(0)
		if constraint.current < constraint.limit {
			available = (constraint.limit - constraint.current) / bytesPerMB
		}
		capacities = append(capacities, MemoryCapacity{
			AvailableMB: available,
			TotalMB:     constraint.limit / bytesPerMB,
			UsedMB:      constraint.current / bytesPerMB,
			Source:      memoryConstraintSource(constraint),
		})
	}
	return capacities, nil
}

// EffectiveMemoryCapacity keeps the original single-capacity status view. It
// must not be used for safety decisions because it intentionally collapses the
// independently enforced host and cgroup constraints.
func EffectiveMemoryCapacity() (MemoryCapacity, error) {
	capacities, err := MemoryCapacities()
	if err != nil {
		return MemoryCapacity{}, err
	}
	if len(capacities) == 0 {
		return MemoryCapacity{}, fmt.Errorf("no memory capacity is available")
	}

	selected := capacities[0]
	for _, capacity := range capacities[1:] {
		if capacity.AvailableMB < selected.AvailableMB {
			selected = capacity
		}
	}
	return selected, nil
}

func currentCgroupMemory() (limit uint64, current uint64, ok bool, err error) {
	constraints, err := currentCgroupMemoryConstraints()
	if err != nil {
		return 0, 0, false, err
	}
	selected := memoryConstraint{}
	for _, constraint := range constraints {
		selected = tighterMemoryConstraint(selected, constraint)
	}
	return selected.limit, selected.current, selected.finite, nil
}

func currentCgroupMemoryConstraints() ([]memoryConstraint, error) {
	return currentCgroupMemoryConstraintsFrom(
		"/proc/self/cgroup",
		"/proc/self/mountinfo",
	)
}

type cgroupVersion uint8

const (
	cgroupV1 cgroupVersion = 1
	cgroupV2 cgroupVersion = 2
)

type cgroupMount struct {
	version    cgroupVersion
	root       string
	mountPoint string
}

type memoryConstraint struct {
	limit   uint64
	current uint64
	finite  bool
	valid   bool
	kind    string
}

func memoryConstraintSource(constraint memoryConstraint) string {
	if constraint.kind == "" {
		return "cgroup"
	}
	return "cgroup(" + constraint.kind + ")"
}

func currentCgroupMemoryFrom(
	cgroupPath string,
	mountInfoPath string,
) (limit uint64, current uint64, ok bool, err error) {
	constraints, err := currentCgroupMemoryConstraintsFrom(
		cgroupPath,
		mountInfoPath,
	)
	if err != nil {
		return 0, 0, false, err
	}
	selected := memoryConstraint{}
	for _, constraint := range constraints {
		selected = tighterMemoryConstraint(selected, constraint)
	}
	return selected.limit, selected.current, selected.finite, nil
}

func currentCgroupMemoryConstraintsFrom(
	cgroupPath string,
	mountInfoPath string,
) ([]memoryConstraint, error) {
	entries, err := readSelfCgroup(cgroupPath)
	if err != nil {
		return nil, err
	}

	unifiedPath, hasUnified := entries[""]
	memoryPath, hasLegacyMemory := entries["memory"]
	if !hasUnified && !hasLegacyMemory {
		return nil, nil
	}

	mounts, err := readCgroupMounts(mountInfoPath)
	if err != nil {
		return nil, err
	}

	var constraints []memoryConstraint
	unifiedValid := false
	if hasUnified {
		var unifiedConstraints []memoryConstraint
		unifiedConstraints, unifiedValid, err = readCgroupMemoryMounts(
			selectCgroupMounts(mounts, cgroupV2),
			unifiedPath,
			cgroupV2,
		)
		if err != nil {
			return nil, err
		}
		constraints = append(constraints, unifiedConstraints...)
	}

	legacyValid := false
	if hasLegacyMemory {
		var legacyConstraints []memoryConstraint
		legacyConstraints, legacyValid, err = readCgroupMemoryMounts(
			selectCgroupMounts(mounts, cgroupV1),
			memoryPath,
			cgroupV1,
		)
		if err != nil {
			return nil, err
		}
		if !legacyValid {
			return nil, fmt.Errorf(
				"cgroup v1 memory membership %q has no readable memory controller mount",
				memoryPath,
			)
		}
		constraints = append(constraints, legacyConstraints...)
	}

	if hasUnified && !unifiedValid && !hasLegacyMemory {
		return nil, fmt.Errorf(
			"cgroup v2 membership %q has no readable memory controller mount",
			unifiedPath,
		)
	}

	return constraints, nil
}

func readCgroupMounts(path string) ([]cgroupMount, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	mounts, err := parseCgroupMounts(file)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return mounts, nil
}

func parseCgroupMounts(reader io.Reader) ([]cgroupMount, error) {
	var mounts []cgroupMount
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
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
			return nil, fmt.Errorf("line %d is malformed", lineNumber)
		}

		fileSystemType := fields[separator+1]
		version := cgroupVersion(0)
		switch fileSystemType {
		case "cgroup2":
			version = cgroupV2
		case "cgroup":
			if !mountInfoHasController(fields, separator, "memory") {
				continue
			}
			version = cgroupV1
		default:
			continue
		}

		root, err := unescapeMountInfoPath(fields[3])
		if err != nil {
			return nil, fmt.Errorf("line %d root: %w", lineNumber, err)
		}
		mountPoint, err := unescapeMountInfoPath(fields[4])
		if err != nil {
			return nil, fmt.Errorf("line %d mount point: %w", lineNumber, err)
		}
		if !filepath.IsAbs(root) || !filepath.IsAbs(mountPoint) {
			return nil, fmt.Errorf("line %d has a non-absolute cgroup path", lineNumber)
		}

		mounts = append(mounts, cgroupMount{
			version:    version,
			root:       filepath.Clean(root),
			mountPoint: filepath.Clean(mountPoint),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return mounts, nil
}

func mountInfoHasController(fields []string, separator int, controller string) bool {
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

func unescapeMountInfoPath(value string) (string, error) {
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

func selectCgroupMounts(
	discovered []cgroupMount,
	version cgroupVersion,
) []cgroupMount {
	var selected []cgroupMount
	for _, mount := range discovered {
		if mount.version == version {
			selected = append(selected, mount)
		}
	}
	return selected
}

func readCgroupMemoryMounts(
	mounts []cgroupMount,
	membership string,
	version cgroupVersion,
) ([]memoryConstraint, bool, error) {
	var constraints []memoryConstraint
	valid := false
	for _, mount := range mounts {
		mountConstraints, mountValid, err := readCgroupMemoryMount(
			mount,
			membership,
			version,
		)
		if err != nil {
			return nil, false, fmt.Errorf(
				"read cgroup v%d mount %s: %w",
				version,
				mount.mountPoint,
				err,
			)
		}
		valid = valid || mountValid
		constraints = append(constraints, mountConstraints...)
	}
	return constraints, valid, nil
}

func readCgroupMemoryMount(
	mount cgroupMount,
	membership string,
	version cgroupVersion,
) ([]memoryConstraint, bool, error) {
	groups, err := cgroupGroupCandidates(mount.root, membership)
	if err != nil {
		return nil, false, err
	}

	var constraints []memoryConstraint
	valid := false
	for _, group := range groups {
		groupPath := filepath.Join(mount.mountPoint, group)
		groupInfo, err := os.Stat(groupPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, false, fmt.Errorf("stat %s: %w", groupPath, err)
		}
		if !groupInfo.IsDir() {
			return nil, false, fmt.Errorf("%s is not a cgroup directory", groupPath)
		}

		switch version {
		case cgroupV2:
			maximum, err := readCgroupMemoryHierarchyConstraints(
				mount.mountPoint,
				group,
				"memory.max",
				"memory.current",
			)
			if err != nil {
				return nil, false, err
			}
			high, err := readCgroupMemoryHierarchyConstraints(
				mount.mountPoint,
				group,
				"memory.high",
				"memory.current",
			)
			if err != nil {
				return nil, false, err
			}
			for _, constraint := range append(maximum, high...) {
				valid = valid || constraint.valid
				if constraint.finite {
					constraints = append(constraints, constraint)
				}
			}
		case cgroupV1:
			legacy, err := readCgroupMemoryHierarchyConstraints(
				mount.mountPoint,
				group,
				"memory.limit_in_bytes",
				"memory.usage_in_bytes",
			)
			if err != nil {
				return nil, false, err
			}
			memsw, err := readCgroupMemoryHierarchyConstraints(
				mount.mountPoint,
				group,
				"memory.memsw.limit_in_bytes",
				"memory.memsw.usage_in_bytes",
			)
			if err != nil {
				return nil, false, err
			}
			for _, constraint := range append(legacy, memsw...) {
				valid = valid || constraint.valid
				if constraint.finite {
					constraints = append(constraints, constraint)
				}
			}
		default:
			return nil, false, fmt.Errorf("unsupported cgroup version %d", version)
		}
	}
	return constraints, valid, nil
}

func cgroupGroupCandidates(mountRoot, membership string) ([]string, error) {
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

	if pathWithinCgroupRoot(membership, mountRoot) {
		relative, err := filepath.Rel(mountRoot, membership)
		if err != nil {
			return nil, err
		}
		appendCandidate(relative)
	}

	// A cgroup namespace reports membership relative to its namespace root,
	// while mountinfo can retain the underlying filesystem root.
	appendCandidate(strings.TrimPrefix(membership, string(os.PathSeparator)))
	return candidates, nil
}

func pathWithinCgroupRoot(path, root string) bool {
	if root == string(os.PathSeparator) {
		return true
	}
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}

func tighterMemoryConstraint(first, second memoryConstraint) memoryConstraint {
	if !first.valid {
		return second
	}
	if !second.valid {
		return first
	}
	if !first.finite {
		return second
	}
	if !second.finite {
		return first
	}

	firstAvailable := uint64(0)
	if first.current < first.limit {
		firstAvailable = first.limit - first.current
	}
	secondAvailable := uint64(0)
	if second.current < second.limit {
		secondAvailable = second.limit - second.current
	}
	if secondAvailable < firstAvailable {
		return second
	}
	return first
}

func tighterMemoryLimit(
	firstLimit uint64,
	firstCurrent uint64,
	firstOK bool,
	secondLimit uint64,
	secondCurrent uint64,
	secondOK bool,
) (limit uint64, current uint64, ok bool, err error) {
	if !firstOK {
		return secondLimit, secondCurrent, secondOK, nil
	}
	if !secondOK {
		return firstLimit, firstCurrent, true, nil
	}

	firstAvailable := uint64(0)
	if firstCurrent < firstLimit {
		firstAvailable = firstLimit - firstCurrent
	}
	secondAvailable := uint64(0)
	if secondCurrent < secondLimit {
		secondAvailable = secondLimit - secondCurrent
	}
	if secondAvailable < firstAvailable {
		return secondLimit, secondCurrent, true, nil
	}
	return firstLimit, firstCurrent, true, nil
}

func readSelfCgroup(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	entries := make(map[string]string)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("line %d is malformed", lineNumber)
		}
		if !filepath.IsAbs(parts[2]) {
			return nil, fmt.Errorf(
				"line %d has non-absolute membership %q",
				lineNumber,
				parts[2],
			)
		}
		if parts[1] == "" {
			entries[""] = parts[2]
			continue
		}
		for _, controller := range strings.Split(parts[1], ",") {
			if controller == "" {
				return nil, fmt.Errorf("line %d has an empty controller", lineNumber)
			}
			entries[controller] = parts[2]
		}
	}
	return entries, scanner.Err()
}

func readMemoryPair(limitPath, currentPath string) (uint64, uint64, bool, error) {
	constraint, err := readMemoryPairConstraint(limitPath, currentPath)
	if err != nil {
		return 0, 0, false, err
	}
	return constraint.limit, constraint.current, constraint.finite, nil
}

func readMemoryPairConstraint(limitPath, currentPath string) (memoryConstraint, error) {
	limitRaw, err := os.ReadFile(limitPath)
	if err != nil {
		if os.IsNotExist(err) {
			return memoryConstraint{}, nil
		}
		return memoryConstraint{}, fmt.Errorf("read %s: %w", limitPath, err)
	}

	limitText := strings.TrimSpace(string(limitRaw))
	if limitText == "max" {
		return memoryConstraint{valid: true}, nil
	}
	limit, err := strconv.ParseUint(limitText, 10, 64)
	if err != nil {
		return memoryConstraint{}, fmt.Errorf("parse %s: %w", limitPath, err)
	}

	currentRaw, err := os.ReadFile(currentPath)
	if err != nil {
		return memoryConstraint{}, fmt.Errorf("read %s: %w", currentPath, err)
	}
	current, err := strconv.ParseUint(strings.TrimSpace(string(currentRaw)), 10, 64)
	if err != nil {
		return memoryConstraint{}, fmt.Errorf("parse %s: %w", currentPath, err)
	}
	return memoryConstraint{
		limit:   limit,
		current: current,
		finite:  true,
		valid:   true,
	}, nil
}

func readCgroupMemoryHierarchy(
	root string,
	group string,
	limitName string,
	currentName string,
) (uint64, uint64, bool, error) {
	constraint, err := readCgroupMemoryHierarchyConstraint(
		root,
		group,
		limitName,
		currentName,
	)
	if err != nil {
		return 0, 0, false, err
	}
	return constraint.limit, constraint.current, constraint.finite, nil
}

func readCgroupMemoryHierarchyConstraint(
	root string,
	group string,
	limitName string,
	currentName string,
) (memoryConstraint, error) {
	constraints, err := readCgroupMemoryHierarchyConstraints(
		root,
		group,
		limitName,
		currentName,
	)
	if err != nil {
		return memoryConstraint{}, err
	}
	var selected memoryConstraint
	for _, constraint := range constraints {
		selected = tighterMemoryConstraint(selected, constraint)
	}
	return selected, nil
}

func readCgroupMemoryHierarchyConstraints(
	root string,
	group string,
	limitName string,
	currentName string,
) ([]memoryConstraint, error) {
	var constraints []memoryConstraint
	for _, directory := range cgroupDirectories(root, group) {
		constraint, err := readMemoryPairConstraint(
			filepath.Join(directory, limitName),
			filepath.Join(directory, currentName),
		)
		if err != nil {
			return nil, err
		}
		if !constraint.valid {
			continue
		}
		constraint.kind = limitName
		constraints = append(constraints, constraint)
	}
	return constraints, nil
}

func cgroupDirectories(root, group string) []string {
	root = filepath.Clean(root)
	group = strings.TrimPrefix(filepath.Clean("/"+group), "/")
	current := filepath.Join(root, group)

	var directories []string
	for {
		directories = append(directories, current)
		if current == root {
			break
		}
		parent := filepath.Dir(current)
		if parent == current || (parent != root && !strings.HasPrefix(parent, root+string(os.PathSeparator))) {
			break
		}
		current = parent
	}
	return directories
}

// SetOOMScoreAdj makes this process a preferred OOM victim.
func SetOOMScoreAdj(value int) error {
	return setOOMScoreAdjAt("/proc/self/oom_score_adj", value)
}

func setOOMScoreAdjAt(path string, value int) error {
	if value < -1000 || value > 1000 {
		return fmt.Errorf("OOM score adjustment must be between -1000 and 1000")
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(value)+"\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("verify %s: %w", path, err)
	}
	applied, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return fmt.Errorf("verify %s: %w", path, err)
	}
	if applied != value {
		return fmt.Errorf(
			"verify %s: applied value %d does not match requested value %d",
			path,
			applied,
			value,
		)
	}
	return nil
}
