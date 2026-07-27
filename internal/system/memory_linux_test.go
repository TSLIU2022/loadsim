package system

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestReadSelfCgroupV2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cgroup")
	if err := os.WriteFile(path, []byte("0::/workload/demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := readSelfCgroup(path)
	if err != nil {
		t.Fatalf("readSelfCgroup: %v", err)
	}
	if got := entries[""]; got != "/workload/demo" {
		t.Fatalf("unified path = %q, want /workload/demo", got)
	}
}

func TestReadSelfCgroupV1Controllers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cgroup")
	content := "5:memory,cpu:/legacy/demo\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := readSelfCgroup(path)
	if err != nil {
		t.Fatalf("readSelfCgroup: %v", err)
	}
	if entries["memory"] != "/legacy/demo" || entries["cpu"] != "/legacy/demo" {
		t.Fatalf("unexpected entries: %#v", entries)
	}
}

func TestReadSelfCgroupRejectsMalformedMembership(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "missing fields",
			content: "not-cgroup-data\n",
		},
		{
			name:    "relative membership",
			content: "0::relative/path\n",
		},
		{
			name:    "empty legacy controller",
			content: "5:memory,:/legacy/demo\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cgroup")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readSelfCgroup(path); err == nil {
				t.Fatal("readSelfCgroup succeeded, want error")
			}
		})
	}
}

func TestParseCgroupMounts(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []cgroupMount
		wantErr bool
	}{
		{
			name:  "unified bind mount",
			input: "29 23 0:26 /workload /run/cgroup rw - cgroup2 cgroup rw\n",
			want: []cgroupMount{{
				version:    cgroupV2,
				root:       "/workload",
				mountPoint: "/run/cgroup",
			}},
		},
		{
			name:  "legacy memory controller in super options",
			input: "30 23 0:27 /legacy /run/memory rw - cgroup cgroup rw,cpu,memory\n",
			want: []cgroupMount{{
				version:    cgroupV1,
				root:       "/legacy",
				mountPoint: "/run/memory",
			}},
		},
		{
			name:  "legacy non-memory mount ignored",
			input: "31 23 0:28 / /run/cpu rw - cgroup cgroup rw,cpu,cpuacct\n",
		},
		{
			name:  "escaped paths",
			input: "32 23 0:29 /team\\040one /run/cgroup\\040memory rw - cgroup2 cgroup rw\n",
			want: []cgroupMount{{
				version:    cgroupV2,
				root:       "/team one",
				mountPoint: "/run/cgroup memory",
			}},
		},
		{
			name:    "malformed mountinfo",
			input:   "not mountinfo\n",
			wantErr: true,
		},
		{
			name:    "invalid path escape",
			input:   "33 23 0:30 /bad\\09x /run/cgroup rw - cgroup2 cgroup rw\n",
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCgroupMounts(strings.NewReader(test.input))
			if test.wantErr {
				if err == nil {
					t.Fatal("parseCgroupMounts succeeded, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCgroupMounts: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("mounts=%#v want=%#v", got, test.want)
			}
		})
	}
}

func TestCgroupGroupCandidates(t *testing.T) {
	tests := []struct {
		name       string
		mountRoot  string
		membership string
		want       []string
	}{
		{
			name:       "standard hierarchy",
			mountRoot:  "/",
			membership: "/team/job",
			want:       []string{"team/job"},
		},
		{
			name:       "bind mounted subtree",
			mountRoot:  "/team",
			membership: "/team/job",
			want:       []string{"job", "team/job"},
		},
		{
			name:       "cgroup namespace",
			mountRoot:  "/host/team",
			membership: "/job",
			want:       []string{"job"},
		},
		{
			name:       "cgroup namespace root",
			mountRoot:  "/host/team",
			membership: "/",
			want:       []string{""},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := cgroupGroupCandidates(test.mountRoot, test.membership)
			if err != nil {
				t.Fatalf("cgroupGroupCandidates: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("candidates=%#v want=%#v", got, test.want)
			}
		})
	}
}

func TestCurrentCgroupMemoryFromSelectsTightestHybridController(t *testing.T) {
	tests := []struct {
		name        string
		v2Maximum   string
		v2High      string
		v2Current   string
		v1Limit     string
		v1Current   string
		wantLimit   uint64
		wantCurrent uint64
	}{
		{
			name:        "legacy controller is tighter",
			v2Maximum:   "1000",
			v2High:      "max",
			v2Current:   "800",
			v1Limit:     "1000",
			v1Current:   "900",
			wantLimit:   1000,
			wantCurrent: 900,
		},
		{
			name:        "unified high limit is tighter",
			v2Maximum:   "1000",
			v2High:      "750",
			v2Current:   "600",
			v1Limit:     "1000",
			v1Current:   "800",
			wantLimit:   750,
			wantCurrent: 600,
		},
		{
			name:        "unified memory controller is unavailable",
			v1Limit:     "800",
			v1Current:   "600",
			wantLimit:   800,
			wantCurrent: 600,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			v2Mount := filepath.Join(base, "unified")
			v1Mount := filepath.Join(base, "legacy")
			if err := os.MkdirAll(v2Mount, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(v1Mount, 0o700); err != nil {
				t.Fatal(err)
			}

			if test.v2Maximum != "" {
				writeTestCgroupV2Files(
					t,
					filepath.Join(v2Mount, "unified", "job"),
					test.v2Maximum,
					test.v2High,
					test.v2Current,
				)
			}
			writeTestCgroupV1Files(
				t,
				filepath.Join(v1Mount, "legacy", "job"),
				test.v1Limit,
				test.v1Current,
			)

			cgroupPath := filepath.Join(base, "cgroup")
			if err := os.WriteFile(
				cgroupPath,
				[]byte("0::/unified/job\n5:memory:/legacy/job\n"),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			mountInfoPath := filepath.Join(base, "mountinfo")
			mountInfo := testMountInfoLine(cgroupV2, "/", v2Mount) +
				testMountInfoLine(cgroupV1, "/", v1Mount)
			if err := os.WriteFile(mountInfoPath, []byte(mountInfo), 0o600); err != nil {
				t.Fatal(err)
			}

			limit, current, ok, err := currentCgroupMemoryFrom(
				cgroupPath,
				mountInfoPath,
			)
			if err != nil {
				t.Fatalf("currentCgroupMemoryFrom: %v", err)
			}
			if !ok || limit != test.wantLimit || current != test.wantCurrent {
				t.Fatalf(
					"limit/current/ok=%d/%d/%v want=%d/%d/true",
					limit,
					current,
					ok,
					test.wantLimit,
					test.wantCurrent,
				)
			}
		})
	}
}

func TestCurrentCgroupMemoryConstraintsFromKeepsHybridConstraints(t *testing.T) {
	base := t.TempDir()
	v2Mount := filepath.Join(base, "unified")
	v1Mount := filepath.Join(base, "legacy")
	writeTestCgroupV2Files(
		t,
		filepath.Join(v2Mount, "unified", "job"),
		"1000",
		"max",
		"100",
	)
	writeTestCgroupV1Files(
		t,
		filepath.Join(v1Mount, "legacy", "job"),
		"800",
		"200",
	)
	cgroupPath, mountInfoPath := writeTestCgroupInputs(
		t,
		base,
		"0::/unified/job\n5:memory:/legacy/job\n",
		testMountInfoLine(cgroupV2, "/", v2Mount)+
			testMountInfoLine(cgroupV1, "/", v1Mount),
	)

	constraints, err := currentCgroupMemoryConstraintsFrom(
		cgroupPath,
		mountInfoPath,
	)
	if err != nil {
		t.Fatalf("currentCgroupMemoryConstraintsFrom: %v", err)
	}
	if len(constraints) != 2 {
		t.Fatalf("constraints=%#v want both v1 and v2 finite constraints", constraints)
	}
}

func TestCurrentCgroupMemoryConstraintsFromKeepsV1MemswConstraint(t *testing.T) {
	base := t.TempDir()
	mountPoint := filepath.Join(base, "legacy")
	groupPath := filepath.Join(mountPoint, "job")
	writeTestCgroupV1Files(t, groupPath, "1000", "100")
	writeTestCgroupV1MemswFiles(t, groupPath, "1200", "1150")
	cgroupPath, mountInfoPath := writeTestCgroupInputs(
		t,
		base,
		"5:memory:/job\n",
		testMountInfoLine(cgroupV1, "/", mountPoint),
	)

	constraints, err := currentCgroupMemoryConstraintsFrom(
		cgroupPath,
		mountInfoPath,
	)
	if err != nil {
		t.Fatalf("currentCgroupMemoryConstraintsFrom: %v", err)
	}
	if len(constraints) != 2 {
		t.Fatalf(
			"constraints=%#v want separate memory and memsw constraints",
			constraints,
		)
	}
	kinds := map[string]bool{}
	for _, constraint := range constraints {
		kinds[constraint.kind] = true
	}
	if !kinds["memory.limit_in_bytes"] ||
		!kinds["memory.memsw.limit_in_bytes"] {
		t.Fatalf("constraint kinds=%#v want memory and memsw", kinds)
	}

	limit, current, ok, err := currentCgroupMemoryFrom(
		cgroupPath,
		mountInfoPath,
	)
	if err != nil {
		t.Fatalf("currentCgroupMemoryFrom: %v", err)
	}
	if !ok || limit != 1200 || current != 1150 {
		t.Fatalf(
			"limit/current/ok=%d/%d/%v want=1200/1150/true",
			limit,
			current,
			ok,
		)
	}
}

func TestCurrentCgroupMemoryFromRejectsMalformedV1Memsw(t *testing.T) {
	base := t.TempDir()
	mountPoint := filepath.Join(base, "legacy")
	groupPath := filepath.Join(mountPoint, "job")
	writeTestCgroupV1Files(t, groupPath, "1000", "100")
	writeTestCgroupV1MemswFiles(t, groupPath, "broken", "100")
	cgroupPath, mountInfoPath := writeTestCgroupInputs(
		t,
		base,
		"5:memory:/job\n",
		testMountInfoLine(cgroupV1, "/", mountPoint),
	)

	if _, _, _, err := currentCgroupMemoryFrom(
		cgroupPath,
		mountInfoPath,
	); err == nil {
		t.Fatal("malformed v1 memsw constraint did not fail closed")
	}
}

func TestCurrentCgroupMemoryConstraintsFromKeepsEveryVisibleAncestor(t *testing.T) {
	base := t.TempDir()
	mountPoint := filepath.Join(base, "unified")
	writeTestCgroupV2Files(t, mountPoint, "10000", "max", "9000")
	writeTestCgroupV2Files(
		t,
		filepath.Join(mountPoint, "parent"),
		"4000",
		"max",
		"1000",
	)
	writeTestCgroupV2Files(
		t,
		filepath.Join(mountPoint, "parent", "child"),
		"2000",
		"max",
		"500",
	)
	cgroupPath, mountInfoPath := writeTestCgroupInputs(
		t,
		base,
		"0::/parent/child\n",
		testMountInfoLine(cgroupV2, "/", mountPoint),
	)

	constraints, err := currentCgroupMemoryConstraintsFrom(
		cgroupPath,
		mountInfoPath,
	)
	if err != nil {
		t.Fatalf("currentCgroupMemoryConstraintsFrom: %v", err)
	}
	if len(constraints) != 3 {
		t.Fatalf("constraints=%#v want child, parent, and root constraints", constraints)
	}
	for _, constraint := range constraints {
		if constraint.kind != "memory.max" {
			t.Fatalf("constraint kind=%q want memory.max", constraint.kind)
		}
	}
}

func TestMemoryConstraintSourceIncludesKindWithoutPath(t *testing.T) {
	if got := memoryConstraintSource(memoryConstraint{kind: "memory.high"}); got != "cgroup(memory.high)" {
		t.Fatalf("source=%q want cgroup(memory.high)", got)
	}
	if got := memoryConstraintSource(memoryConstraint{}); got != "cgroup" {
		t.Fatalf("fallback source=%q want cgroup", got)
	}
}

func TestCurrentCgroupMemoryFromConservativelyIncludesAllNamespaceCandidates(t *testing.T) {
	base := t.TempDir()
	mountPoint := filepath.Join(base, "unified")
	writeTestCgroupV2Files(
		t,
		filepath.Join(mountPoint, "job"),
		"1000",
		"max",
		"100",
	)
	writeTestCgroupV2Files(
		t,
		filepath.Join(mountPoint, "host", "job"),
		"100",
		"max",
		"90",
	)
	cgroupPath, mountInfoPath := writeTestCgroupInputs(
		t,
		base,
		"0::/host/job\n",
		testMountInfoLine(cgroupV2, "/host", mountPoint),
	)

	constraints, err := currentCgroupMemoryConstraintsFrom(
		cgroupPath,
		mountInfoPath,
	)
	if err != nil {
		t.Fatalf("currentCgroupMemoryConstraintsFrom: %v", err)
	}
	if len(constraints) != 2 {
		t.Fatalf("constraints=%#v want both namespace interpretations", constraints)
	}
	limit, current, ok, err := currentCgroupMemoryFrom(cgroupPath, mountInfoPath)
	if err != nil {
		t.Fatalf("currentCgroupMemoryFrom: %v", err)
	}
	if !ok || limit != 100 || current != 90 {
		t.Fatalf("limit/current/ok=%d/%d/%v want=100/90/true", limit, current, ok)
	}
}

func TestCurrentCgroupMemoryFromMapsMembershipToMountRoot(t *testing.T) {
	tests := []struct {
		name       string
		version    cgroupVersion
		mountRoot  string
		membership string
		group      string
	}{
		{
			name:       "unified bind mounted subtree",
			version:    cgroupV2,
			mountRoot:  "/host/team",
			membership: "/host/team/job",
			group:      "job",
		},
		{
			name:       "unified cgroup namespace",
			version:    cgroupV2,
			mountRoot:  "/host/team",
			membership: "/job",
			group:      "job",
		},
		{
			name:       "unified namespace path overlaps mount root",
			version:    cgroupV2,
			mountRoot:  "/host",
			membership: "/host/job",
			group:      filepath.Join("host", "job"),
		},
		{
			name:       "unified cgroup namespace root",
			version:    cgroupV2,
			mountRoot:  "/host/team",
			membership: "/",
		},
		{
			name:       "legacy nonstandard bind mount",
			version:    cgroupV1,
			mountRoot:  "/legacy/root",
			membership: "/legacy/root/job",
			group:      "job",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			mountPoint := filepath.Join(base, "mounted")
			groupPath := filepath.Join(mountPoint, test.group)
			switch test.version {
			case cgroupV2:
				writeTestCgroupV2Files(t, groupPath, "1024", "max", "24")
			case cgroupV1:
				writeTestCgroupV1Files(t, groupPath, "1024", "24")
			default:
				t.Fatalf("unsupported cgroup version %d", test.version)
			}

			cgroupContent := "0::" + test.membership + "\n"
			if test.version == cgroupV1 {
				cgroupContent = "5:memory:" + test.membership + "\n"
			}
			cgroupPath := filepath.Join(base, "cgroup")
			if err := os.WriteFile(cgroupPath, []byte(cgroupContent), 0o600); err != nil {
				t.Fatal(err)
			}
			mountInfoPath := filepath.Join(base, "mountinfo")
			if err := os.WriteFile(
				mountInfoPath,
				[]byte(testMountInfoLine(test.version, test.mountRoot, mountPoint)),
				0o600,
			); err != nil {
				t.Fatal(err)
			}

			limit, current, ok, err := currentCgroupMemoryFrom(
				cgroupPath,
				mountInfoPath,
			)
			if err != nil {
				t.Fatalf("currentCgroupMemoryFrom: %v", err)
			}
			if !ok || limit != 1024 || current != 24 {
				t.Fatalf(
					"limit/current/ok=%d/%d/%v want=1024/24/true",
					limit,
					current,
					ok,
				)
			}
		})
	}
}

func TestCurrentCgroupMemoryFromConservativelyIncludesAmbiguousBindMount(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "primary")
	unrelated := filepath.Join(base, "unrelated")
	writeTestCgroupV2Files(
		t,
		filepath.Join(primary, "target", "job"),
		"1000",
		"max",
		"100",
	)
	writeTestCgroupV2Files(
		t,
		filepath.Join(unrelated, "target", "job"),
		"100",
		"max",
		"90",
	)
	cgroupPath, mountInfoPath := writeTestCgroupInputs(
		t,
		base,
		"0::/target/job\n",
		testMountInfoLine(cgroupV2, "/", primary)+
			testMountInfoLine(cgroupV2, "/unrelated", unrelated),
	)

	limit, current, ok, err := currentCgroupMemoryFrom(
		cgroupPath,
		mountInfoPath,
	)
	if err != nil {
		t.Fatalf("currentCgroupMemoryFrom: %v", err)
	}
	if !ok || limit != 100 || current != 90 {
		t.Fatalf("limit/current/ok=%d/%d/%v want=100/90/true", limit, current, ok)
	}
}

func TestCurrentCgroupMemoryFromFailsClosed(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) (string, string)
	}{
		{
			name: "unified membership without cgroup mount",
			setup: func(t *testing.T) (string, string) {
				base := t.TempDir()
				return writeTestCgroupInputs(
					t,
					base,
					"0::/job\n",
					"1 0 0:1 / /proc rw - proc proc rw\n",
				)
			},
		},
		{
			name: "legacy memory membership without memory mount",
			setup: func(t *testing.T) (string, string) {
				base := t.TempDir()
				return writeTestCgroupInputs(
					t,
					base,
					"5:memory:/job\n",
					"30 23 0:27 / /run/cpu rw - cgroup cgroup rw,cpu\n",
				)
			},
		},
		{
			name: "mountinfo cannot be read",
			setup: func(t *testing.T) (string, string) {
				base := t.TempDir()
				cgroupPath := filepath.Join(base, "cgroup")
				if err := os.WriteFile(cgroupPath, []byte("0::/job\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return cgroupPath, filepath.Join(base, "missing-mountinfo")
			},
		},
		{
			name: "malformed mountinfo",
			setup: func(t *testing.T) (string, string) {
				base := t.TempDir()
				return writeTestCgroupInputs(
					t,
					base,
					"0::/job\n",
					"malformed\n",
				)
			},
		},
		{
			name: "resolved mount without controller files",
			setup: func(t *testing.T) (string, string) {
				base := t.TempDir()
				discovered := filepath.Join(base, "discovered")
				if err := os.MkdirAll(discovered, 0o700); err != nil {
					t.Fatal(err)
				}
				return writeTestCgroupInputs(
					t,
					base,
					"0::/job\n",
					testMountInfoLine(cgroupV2, "/", discovered),
				)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cgroupPath, mountInfoPath := test.setup(t)
			if _, _, _, err := currentCgroupMemoryFrom(
				cgroupPath,
				mountInfoPath,
			); err == nil {
				t.Fatal("currentCgroupMemoryFrom succeeded, want fail-closed error")
			}
		})
	}
}

func TestCurrentCgroupMemoryFromAcceptsUnlimitedV2Controller(t *testing.T) {
	base := t.TempDir()
	mountPoint := filepath.Join(base, "unified")
	writeTestCgroupV2Files(t, filepath.Join(mountPoint, "job"), "max", "max", "")
	cgroupPath, mountInfoPath := writeTestCgroupInputs(
		t,
		base,
		"0::/job\n",
		testMountInfoLine(cgroupV2, "/", mountPoint),
	)

	if _, _, ok, err := currentCgroupMemoryFrom(
		cgroupPath,
		mountInfoPath,
	); err != nil || ok {
		t.Fatalf("unlimited controller ok=%v err=%v", ok, err)
	}
}

func testMountInfoLine(version cgroupVersion, root, mountPoint string) string {
	root = escapeTestMountInfoPath(root)
	mountPoint = escapeTestMountInfoPath(mountPoint)
	if version == cgroupV1 {
		return "30 23 0:27 " + root + " " + mountPoint +
			" rw - cgroup cgroup rw,memory\n"
	}
	return "29 23 0:26 " + root + " " + mountPoint +
		" rw - cgroup2 cgroup rw\n"
}

func escapeTestMountInfoPath(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\134",
		" ", "\\040",
		"\t", "\\011",
		"\n", "\\012",
	)
	return replacer.Replace(value)
}

func writeTestCgroupV2Files(
	t *testing.T,
	directory string,
	maximum string,
	high string,
	current string,
) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"memory.max":     maximum,
		"memory.high":    high,
		"memory.current": current,
	} {
		if value == "" {
			continue
		}
		if err := os.WriteFile(
			filepath.Join(directory, name),
			[]byte(value+"\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
}

func writeTestCgroupV1Files(t *testing.T, directory, limit, current string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"memory.limit_in_bytes": limit,
		"memory.usage_in_bytes": current,
	} {
		if err := os.WriteFile(
			filepath.Join(directory, name),
			[]byte(value+"\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
}

func writeTestCgroupV1MemswFiles(t *testing.T, directory, limit, current string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"memory.memsw.limit_in_bytes": limit,
		"memory.memsw.usage_in_bytes": current,
	} {
		if err := os.WriteFile(
			filepath.Join(directory, name),
			[]byte(value+"\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
}

func writeTestCgroupInputs(
	t *testing.T,
	base string,
	cgroupContent string,
	mountInfoContent string,
) (string, string) {
	t.Helper()
	cgroupPath := filepath.Join(base, "cgroup")
	if err := os.WriteFile(cgroupPath, []byte(cgroupContent), 0o600); err != nil {
		t.Fatal(err)
	}
	mountInfoPath := filepath.Join(base, "mountinfo")
	if err := os.WriteFile(mountInfoPath, []byte(mountInfoContent), 0o600); err != nil {
		t.Fatal(err)
	}
	return cgroupPath, mountInfoPath
}

func TestReadMemoryPair(t *testing.T) {
	dir := t.TempDir()
	limitPath := filepath.Join(dir, "memory.max")
	currentPath := filepath.Join(dir, "memory.current")
	if err := os.WriteFile(limitPath, []byte("1073741824\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(currentPath, []byte("268435456\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	limit, current, ok, err := readMemoryPair(limitPath, currentPath)
	if err != nil {
		t.Fatalf("readMemoryPair: %v", err)
	}
	if !ok {
		t.Fatal("expected cgroup memory pair")
	}
	if limit != 1024*1024*1024 || current != 256*1024*1024 {
		t.Fatalf("limit=%d current=%d", limit, current)
	}
}

func TestReadMemoryPairUnlimited(t *testing.T) {
	dir := t.TempDir()
	limitPath := filepath.Join(dir, "memory.max")
	currentPath := filepath.Join(dir, "memory.current")
	if err := os.WriteFile(limitPath, []byte("max\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(currentPath, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, ok, err := readMemoryPair(limitPath, currentPath); err != nil || ok {
		t.Fatalf("unlimited pair ok=%v err=%v", ok, err)
	}
}

func TestReadMemoryPairRecognizesLegacyNumericUnlimitedSentinel(t *testing.T) {
	dir := t.TempDir()
	pageSize := uint64(os.Getpagesize())
	sentinel := uint64(math.MaxInt64) &^ (pageSize - 1)

	for _, name := range []string{
		"memory.limit_in_bytes",
		"memory.memsw.limit_in_bytes",
	} {
		limitPath := filepath.Join(dir, name)
		if err := os.WriteFile(
			limitPath,
			[]byte(strconv.FormatUint(sentinel, 10)+"\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		constraint, err := readMemoryPairConstraint(
			limitPath,
			filepath.Join(dir, "missing.usage"),
		)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !constraint.valid || constraint.finite {
			t.Fatalf(
				"%s constraint=%+v want valid unlimited",
				name,
				constraint,
			)
		}
	}
}

func TestReadMemoryPairKeepsNumericV2LimitFinite(t *testing.T) {
	dir := t.TempDir()
	limitPath := filepath.Join(dir, "memory.max")
	currentPath := filepath.Join(dir, "memory.current")
	limit := uint64(math.MaxInt64) &^ (uint64(os.Getpagesize()) - 1)
	if err := os.WriteFile(
		limitPath,
		[]byte(strconv.FormatUint(limit, 10)+"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(currentPath, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	constraint, err := readMemoryPairConstraint(limitPath, currentPath)
	if err != nil {
		t.Fatal(err)
	}
	if !constraint.valid || !constraint.finite || constraint.limit != limit {
		t.Fatalf("constraint=%+v want finite numeric v2 limit", constraint)
	}
}

func TestReadMemoryPairRejectsMalformedLimit(t *testing.T) {
	dir := t.TempDir()
	limitPath := filepath.Join(dir, "memory.max")
	currentPath := filepath.Join(dir, "memory.current")
	if err := os.WriteFile(limitPath, []byte("not-a-limit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(currentPath, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := readMemoryPair(limitPath, currentPath); err == nil {
		t.Fatal("malformed cgroup limit must not be treated as unlimited")
	}
}

func TestReadMemoryPairRejectsMissingUsageForFiniteLimit(t *testing.T) {
	dir := t.TempDir()
	limitPath := filepath.Join(dir, "memory.max")
	currentPath := filepath.Join(dir, "memory.current")
	if err := os.WriteFile(limitPath, []byte("1024\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := readMemoryPair(limitPath, currentPath); err == nil {
		t.Fatal("missing usage for a finite cgroup limit must fail closed")
	}
}

func TestReadMemoryPairAllowsMissingControllerFile(t *testing.T) {
	dir := t.TempDir()
	if _, _, ok, err := readMemoryPair(
		filepath.Join(dir, "memory.max"),
		filepath.Join(dir, "memory.current"),
	); err != nil || ok {
		t.Fatalf("missing controller pair ok=%v err=%v", ok, err)
	}
}

func TestReadMemoryPairUnlimitedDoesNotRequireUsage(t *testing.T) {
	dir := t.TempDir()
	limitPath := filepath.Join(dir, "memory.max")
	if err := os.WriteFile(limitPath, []byte("max\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, ok, err := readMemoryPair(
		limitPath,
		filepath.Join(dir, "missing.current"),
	); err != nil || ok {
		t.Fatalf("unlimited pair ok=%v err=%v", ok, err)
	}
}

func TestReadMemoryPairRejectsMalformedUsage(t *testing.T) {
	dir := t.TempDir()
	limitPath := filepath.Join(dir, "memory.max")
	currentPath := filepath.Join(dir, "memory.current")
	if err := os.WriteFile(limitPath, []byte("1024\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(currentPath, []byte("not-usage\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := readMemoryPair(limitPath, currentPath); err == nil {
		t.Fatal("malformed cgroup usage must fail closed")
	}
}

func TestReadMemoryPairRejectsLimitReadFailure(t *testing.T) {
	dir := t.TempDir()
	limitPath := filepath.Join(dir, "memory.max")
	if err := os.Mkdir(limitPath, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := readMemoryPair(
		limitPath,
		filepath.Join(dir, "memory.current"),
	); err == nil {
		t.Fatal("cgroup limit read failure must fail closed")
	}
}

func TestReadCgroupMemoryHierarchyPropagatesAncestorError(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "memory.max"), []byte("1024\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "memory.current"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "memory.max"), []byte("broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := readCgroupMemoryHierarchy(
		root,
		"child",
		"memory.max",
		"memory.current",
	); err == nil {
		t.Fatal("malformed ancestor limit must fail closed")
	}
}

func TestReadCgroupMemoryHierarchyUsesTightestAncestor(t *testing.T) {
	root := t.TempDir()
	group := filepath.Join("parent", "child")
	for _, directory := range []string{
		root,
		filepath.Join(root, "parent"),
		filepath.Join(root, group),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	writePair := func(directory, limit, current string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(directory, "memory.max"), []byte(limit), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "memory.current"), []byte(current), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writePair(root, "max", "1")
	writePair(filepath.Join(root, "parent"), "1000", "600")
	writePair(filepath.Join(root, group), "900", "100")

	limit, current, ok, err := readCgroupMemoryHierarchy(
		root,
		group,
		"memory.max",
		"memory.current",
	)
	if err != nil {
		t.Fatalf("readCgroupMemoryHierarchy: %v", err)
	}
	if !ok {
		t.Fatal("expected a finite ancestor memory limit")
	}
	if limit != 1000 || current != 600 {
		t.Fatalf("selected limit/current=%d/%d want 1000/600", limit, current)
	}
}

func TestTighterMemoryLimitSelectsLowestAvailableCapacity(t *testing.T) {
	limit, current, ok, err := tighterMemoryLimit(
		1000,
		700,
		true,
		800,
		600,
		true,
	)
	if err != nil {
		t.Fatalf("tighterMemoryLimit: %v", err)
	}
	if !ok || limit != 800 || current != 600 {
		t.Fatalf(
			"selected limit/current/ok=%d/%d/%v want=800/600/true",
			limit,
			current,
			ok,
		)
	}
}

func TestTighterMemoryLimitHandlesUnlimitedConstraint(t *testing.T) {
	limit, current, ok, err := tighterMemoryLimit(
		0,
		0,
		false,
		800,
		600,
		true,
	)
	if err != nil {
		t.Fatalf("tighterMemoryLimit: %v", err)
	}
	if !ok || limit != 800 || current != 600 {
		t.Fatalf(
			"selected limit/current/ok=%d/%d/%v want=800/600/true",
			limit,
			current,
			ok,
		)
	}
}

func TestSetOOMScoreAdjAtWritesAndVerifiesValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oom_score_adj")
	if err := os.WriteFile(path, []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := setOOMScoreAdjAt(path, 1000); err != nil {
		t.Fatalf("setOOMScoreAdjAt: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "1000\n" {
		t.Fatalf("oom_score_adj=%q want %q", raw, "1000\\n")
	}
}

func TestSetOOMScoreAdjAtRejectsOutOfRangeValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oom_score_adj")
	if err := setOOMScoreAdjAt(path, 1001); err == nil {
		t.Fatal("out-of-range OOM score adjustment was accepted")
	}
}

func TestProcessRSSMB(t *testing.T) {
	rssMB, err := ProcessRSSMB()
	if err != nil {
		t.Fatalf("ProcessRSSMB: %v", err)
	}
	if rssMB == 0 {
		t.Fatal("process RSS must be greater than zero")
	}
}
