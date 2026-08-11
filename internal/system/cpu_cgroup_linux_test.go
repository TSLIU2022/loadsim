//go:build linux

package system

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInspectCPUCgroupV2IncludesAncestorControls(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "cgroup")
	leaf := filepath.Join(mountPoint, "system.slice", "loadsim.service")
	parent := filepath.Dir(leaf)
	writeCPUCgroupTestFiles(t, leaf, map[string]string{
		"cpu.max":    "2400000 100000\n",
		"cpu.weight": "100\n",
		"cpu.stat": strings.Join([]string{
			"usage_usec 1000000",
			"nr_periods 100",
			"nr_throttled 25",
			"throttled_usec 120000",
			"",
		}, "\n"),
	})
	writeCPUCgroupTestFiles(t, parent, map[string]string{
		"cpu.max":    "1200000 100000\n",
		"cpu.weight": "1\n",
		"cpu.stat": strings.Join([]string{
			"nr_periods 200",
			"nr_throttled 50",
			"throttled_usec 250000",
			"",
		}, "\n"),
	})

	info, err := inspectCPUCgroupFrom(
		"0::/system.slice/loadsim.service\n",
		cpuCgroupTestMountInfo(cgroupV2, "/", mountPoint),
		cpuCgroupDependencies{readFile: os.ReadFile, stat: os.Stat},
	)
	if err != nil {
		t.Fatalf("inspectCPUCgroupFrom: %v", err)
	}
	if info.Version != "v2" ||
		!info.QuotaKnown ||
		!info.QuotaLimited ||
		math.Abs(info.QuotaCPUs-12) > 0.000001 ||
		info.QuotaLevel != cpuCgroupLevelAncestor {
		t.Fatalf("unexpected quota info: %+v", info)
	}
	if !info.WeightKnown ||
		info.WeightKind != "weight" ||
		info.Weight != 1 ||
		info.WeightLevel != cpuCgroupLevelAncestor {
		t.Fatalf("unexpected weight info: %+v", info)
	}
	if !info.ThrottlingKnown ||
		info.Periods != 200 ||
		info.ThrottledPeriods != 50 ||
		info.ThrottledTime != 250*time.Millisecond ||
		info.ThrottlingLevel != cpuCgroupLevelAncestor {
		t.Fatalf("unexpected throttling info: %+v", info)
	}
}

func TestInspectCPUCgroupV2FindsControlsAtVisibleAncestor(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "cgroup")
	leaf := filepath.Join(
		mountPoint,
		"user.slice",
		"user-1000.slice",
		"session-2.scope",
	)
	if err := os.MkdirAll(leaf, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCPUCgroupTestFiles(t, filepath.Join(mountPoint, "user.slice"), map[string]string{
		"cpu.max":    "max 100000\n",
		"cpu.weight": "100\n",
		"cpu.stat": strings.Join([]string{
			"nr_periods 0",
			"nr_throttled 0",
			"throttled_usec 0",
			"",
		}, "\n"),
	})

	info, err := inspectCPUCgroupFrom(
		"0::/user.slice/user-1000.slice/session-2.scope\n",
		cpuCgroupTestMountInfo(cgroupV2, "/", mountPoint),
		cpuCgroupDependencies{readFile: os.ReadFile, stat: os.Stat},
	)
	if err != nil {
		t.Fatalf("inspectCPUCgroupFrom: %v", err)
	}
	if info.Version != "v2" ||
		!info.QuotaKnown ||
		info.QuotaLimited ||
		!info.WeightKnown ||
		info.Weight != 100 ||
		info.WeightLevel != cpuCgroupLevelAncestor ||
		!info.ThrottlingKnown ||
		info.Periods != 0 ||
		info.ThrottledPeriods != 0 ||
		info.ThrottledTime != 0 {
		t.Fatalf("unexpected cgroup info: %+v", info)
	}
}

func TestInspectCPUCgroupV2NamespaceRoot(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "cgroup")
	writeCPUCgroupTestFiles(t, mountPoint, map[string]string{
		"cpu.max":    "50000 100000\n",
		"cpu.weight": "50\n",
		"cpu.stat": strings.Join([]string{
			"nr_periods 12",
			"nr_throttled 4",
			"throttled_usec 3000",
			"",
		}, "\n"),
	})

	info, err := inspectCPUCgroupFrom(
		"0::/\n",
		cpuCgroupTestMountInfo(cgroupV2, "/host/job", mountPoint),
		cpuCgroupDependencies{readFile: os.ReadFile, stat: os.Stat},
	)
	if err != nil {
		t.Fatalf("inspectCPUCgroupFrom: %v", err)
	}
	if info.Version != "v2" ||
		!info.QuotaLimited ||
		info.QuotaCPUs != 0.5 ||
		info.QuotaLevel != cpuCgroupLevelSelf ||
		info.Weight != 50 ||
		info.WeightLevel != cpuCgroupLevelSelf ||
		info.Periods != 12 ||
		info.ThrottledPeriods != 4 ||
		info.ThrottledTime != 3*time.Millisecond {
		t.Fatalf("unexpected namespace-root cgroup info: %+v", info)
	}
}

func TestInspectCPUCgroupV1(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "cpu")
	leaf := filepath.Join(mountPoint, "batch", "loadsim")
	writeCPUCgroupTestFiles(t, leaf, map[string]string{
		"cpu.cfs_quota_us":  "2400000\n",
		"cpu.cfs_period_us": "100000\n",
		"cpu.shares":        "512\n",
		"cpu.stat": strings.Join([]string{
			"nr_periods 10",
			"nr_throttled 3",
			"throttled_time 25000000",
			"",
		}, "\n"),
	})
	writeCPUCgroupTestFiles(t, filepath.Dir(leaf), map[string]string{
		"cpu.cfs_quota_us": "-1\n",
		"cpu.shares":       "1024\n",
	})

	info, err := inspectCPUCgroupFrom(
		"2:cpu,cpuacct:/batch/loadsim\n",
		cpuCgroupTestMountInfo(cgroupV1, "/", mountPoint),
		cpuCgroupDependencies{readFile: os.ReadFile, stat: os.Stat},
	)
	if err != nil {
		t.Fatalf("inspectCPUCgroupFrom: %v", err)
	}
	if info.Version != "v1" ||
		!info.QuotaLimited ||
		math.Abs(info.QuotaCPUs-24) > 0.000001 ||
		info.QuotaLevel != cpuCgroupLevelSelf ||
		info.WeightKind != "shares" ||
		info.Weight != 512 ||
		info.WeightLevel != cpuCgroupLevelSelf ||
		info.Periods != 10 ||
		info.ThrottledPeriods != 3 ||
		info.ThrottledTime != 25*time.Millisecond {
		t.Fatalf("unexpected cgroup info: %+v", info)
	}
}

func TestInspectCPUCgroupHybridPrefersV1CPUController(t *testing.T) {
	base := t.TempDir()
	v2Mount := filepath.Join(base, "unified")
	v1Mount := filepath.Join(base, "cpu")
	if err := os.MkdirAll(filepath.Join(v2Mount, "unified-job"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeCPUCgroupTestFiles(t, filepath.Join(v1Mount, "legacy-job"), map[string]string{
		"cpu.cfs_quota_us":  "100000\n",
		"cpu.cfs_period_us": "100000\n",
		"cpu.shares":        "1024\n",
	})
	mountInfo := cpuCgroupTestMountInfo(cgroupV2, "/", v2Mount) +
		cpuCgroupTestMountInfo(cgroupV1, "/", v1Mount)

	info, err := inspectCPUCgroupFrom(
		"0::/unified-job\n2:cpu,cpuacct:/legacy-job\n",
		mountInfo,
		cpuCgroupDependencies{readFile: os.ReadFile, stat: os.Stat},
	)
	if err != nil {
		t.Fatalf("inspectCPUCgroupFrom: %v", err)
	}
	if info.Version != "v1" || !info.QuotaLimited || info.QuotaCPUs != 1 {
		t.Fatalf("unexpected hybrid cgroup info: %+v", info)
	}
}

func TestParseCPUCgroupV2Max(t *testing.T) {
	tests := []struct {
		value       string
		wantQuota   float64
		wantLimited bool
		wantErr     bool
	}{
		{value: "max 100000", wantLimited: false},
		{value: "250000 100000", wantQuota: 2.5, wantLimited: true},
		{value: "0 100000", wantErr: true},
		{value: "100000 0", wantErr: true},
		{value: "invalid", wantErr: true},
	}
	for _, test := range tests {
		quota, limited, err := parseCPUCgroupV2Max(test.value)
		if (err != nil) != test.wantErr ||
			limited != test.wantLimited ||
			math.Abs(quota-test.wantQuota) > 0.000001 {
			t.Errorf(
				"parseCPUCgroupV2Max(%q) = %.3f/%v/%v, want %.3f/%v/error=%v",
				test.value,
				quota,
				limited,
				err,
				test.wantQuota,
				test.wantLimited,
				test.wantErr,
			)
		}
	}
}

func writeCPUCgroupTestFiles(
	t *testing.T,
	directory string,
	files map[string]string,
) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range files {
		if err := os.WriteFile(
			filepath.Join(directory, name),
			[]byte(value),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
}

func cpuCgroupTestMountInfo(
	version cgroupVersion,
	root string,
	mountPoint string,
) string {
	root = escapeTestMountInfoPath(root)
	mountPoint = escapeTestMountInfoPath(mountPoint)
	if version == cgroupV1 {
		return "30 23 0:27 " + root + " " + mountPoint +
			" rw - cgroup cgroup rw,cpu,cpuacct\n"
	}
	return "29 23 0:26 " + root + " " + mountPoint +
		" rw - cgroup2 cgroup rw\n"
}
