//go:build linux

package stress

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSampleVisibleSystemCPUFromCgroupV2NamespaceRoot(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "unified")
	usagePath := filepath.Join(mountPoint, "cpu.stat")
	writeVisibleCPUTestFile(t, filepath.Join(mountPoint, "cpu.max"), "200000 100000\n")
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cpuset.cpus.effective"),
		"0-3\n",
	)

	sample, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		time.Second,
		"0::/\n",
		visibleCPUTestMountInfoLine(31, linuxCPUCgroupV2, "/tenant.slice", mountPoint, ""),
		8,
		visibleCPUTestDependencies(
			t,
			usagePath,
			[]string{
				"usage_usec 1000000\nuser_usec 900000\n",
				"usage_usec 1400000\nuser_usec 1200000\n",
			},
			time.Second,
		),
	)
	if err != nil {
		t.Fatalf("sampleVisibleSystemCPUFrom: %v", err)
	}
	if sample.Source != visibleSystemCPUSourceV2 {
		t.Fatalf("source = %q want %q", sample.Source, visibleSystemCPUSourceV2)
	}
	if sample.CPUs != 2 {
		t.Fatalf("CPUs = %v want 2", sample.CPUs)
	}
	if math.Abs(sample.Percent-20) > 0.000001 {
		t.Fatalf("percent = %v want 20", sample.Percent)
	}
	if sample.Busy != 400*time.Millisecond || sample.Elapsed != time.Second {
		t.Fatalf(
			"busy/elapsed = %s/%s want 400ms/1s",
			sample.Busy,
			sample.Elapsed,
		)
	}
	if sample.BoundaryID == "" {
		t.Fatal("boundary ID is empty")
	}
	if sample.BoundaryKind != VisibleSystemCPUBoundaryNamespaceRoot {
		t.Fatalf(
			"boundary kind = %q want %q",
			sample.BoundaryKind,
			VisibleSystemCPUBoundaryNamespaceRoot,
		)
	}
}

func TestInspectVisibleSystemCPUFromDoesNotWait(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "unified")
	usagePath := filepath.Join(mountPoint, "cpu.stat")
	writeVisibleCPUTestFile(t, usagePath, "usage_usec 1234\n")
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cpu.max"),
		"300000 100000\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cpuset.cpus.effective"),
		"0-1\n",
	)

	inspection, err := inspectVisibleSystemCPUFrom(
		"0::/tenant/job\n",
		visibleCPUTestMountInfoLine(
			36,
			linuxCPUCgroupV2,
			"/tenant",
			mountPoint,
			"",
		),
		8,
		os.ReadFile,
	)
	if err != nil {
		t.Fatalf("inspectVisibleSystemCPUFrom: %v", err)
	}
	if inspection.boundary.source != visibleSystemCPUSourceV2 {
		t.Fatalf(
			"source = %q want %q",
			inspection.boundary.source,
			visibleSystemCPUSourceV2,
		)
	}
	if inspection.cpus != 2 {
		t.Fatalf("CPUs = %v want 2", inspection.cpus)
	}
	if inspection.usage != 1234 {
		t.Fatalf("usage = %d want 1234", inspection.usage)
	}
	if inspection.boundary.boundaryID == "" {
		t.Fatal("boundary ID is empty")
	}
	if strings.Contains(inspection.boundary.boundaryID, "/tenant") ||
		strings.Contains(inspection.boundary.boundaryID, mountPoint) {
		t.Fatalf("boundary ID leaks a path: %q", inspection.boundary.boundaryID)
	}
	if inspection.boundary.boundaryKind != VisibleSystemCPUBoundaryAncestor {
		t.Fatalf(
			"boundary kind = %q want %q",
			inspection.boundary.boundaryKind,
			VisibleSystemCPUBoundaryAncestor,
		)
	}
}

func TestInspectVisibleSystemCPUGlobalV2RootAllowsMissingCPUMax(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "unified")
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cpu.stat"),
		"usage_usec 1234\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cgroup.controllers"),
		"cpuset cpu io memory\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cpuset.cpus.effective"),
		"0-3\n",
	)

	inspection, err := inspectVisibleSystemCPUFrom(
		"0::/system.slice/job\n",
		visibleCPUTestMountInfoLine(
			37,
			linuxCPUCgroupV2,
			"/",
			mountPoint,
			"",
		),
		8,
		os.ReadFile,
	)
	if err != nil {
		t.Fatalf("inspectVisibleSystemCPUFrom: %v", err)
	}
	if inspection.cpus != 4 {
		t.Fatalf("CPUs = %v want 4", inspection.cpus)
	}
	if inspection.boundary.boundaryKind != VisibleSystemCPUBoundaryAncestor {
		t.Fatalf(
			"boundary kind = %q want %q",
			inspection.boundary.boundaryKind,
			VisibleSystemCPUBoundaryAncestor,
		)
	}
}

func TestInspectVisibleSystemCPUNamespaceV2RootRejectsMissingCPUMax(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "unified")
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cpu.stat"),
		"usage_usec 1234\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cgroup.controllers"),
		"cpuset cpu io memory\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cpuset.cpus.effective"),
		"0-3\n",
	)

	_, err := inspectVisibleSystemCPUFrom(
		"0::/\n",
		visibleCPUTestMountInfoLine(
			38,
			linuxCPUCgroupV2,
			"/tenant.slice",
			mountPoint,
			"",
		),
		8,
		os.ReadFile,
	)
	if err == nil || !strings.Contains(err.Error(), "quota interface is missing") {
		t.Fatalf("error = %v want missing-quota failure", err)
	}
}

func TestSampleVisibleSystemCPUFromCgroupV1CombinedMount(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "cpu,cpuacct")
	usagePath := filepath.Join(mountPoint, "cpuacct.usage")
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cpu.cfs_quota_us"),
		"150000\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cpu.cfs_period_us"),
		"100000\n",
	)

	sample, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		time.Second,
		"2:cpu,cpuacct:/tenant/job\n",
		visibleCPUTestMountInfoLine(
			41,
			linuxCPUCgroupV1,
			"/tenant",
			mountPoint,
			"cpu,cpuacct",
		),
		8,
		visibleCPUTestDependencies(
			t,
			usagePath,
			[]string{"1000000000\n", "1750000000\n"},
			time.Second,
		),
	)
	if err != nil {
		t.Fatalf("sampleVisibleSystemCPUFrom: %v", err)
	}
	if sample.Source != visibleSystemCPUSourceV1 {
		t.Fatalf("source = %q want %q", sample.Source, visibleSystemCPUSourceV1)
	}
	if sample.CPUs != 1.5 {
		t.Fatalf("CPUs = %v want 1.5", sample.CPUs)
	}
	if math.Abs(sample.Percent-50) > 0.000001 {
		t.Fatalf("percent = %v want 50", sample.Percent)
	}
}

func TestV1GlobalRootIgnoresIndependentCPUSet(t *testing.T) {
	base := t.TempDir()
	cpuMount := filepath.Join(base, "cpu,cpuacct")
	cpusetMount := filepath.Join(base, "cpuset")
	usagePath := filepath.Join(cpuMount, "cpuacct.usage")
	writeVisibleCPUTestFile(
		t,
		filepath.Join(cpuMount, "cpu.cfs_quota_us"),
		"400000\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(cpuMount, "cpu.cfs_period_us"),
		"100000\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(cpusetMount, "cpuset.effective_cpus"),
		"0\n",
	)
	mountInfo := visibleCPUTestMountInfoLine(
		46,
		linuxCPUCgroupV1,
		"/",
		cpuMount,
		"cpu,cpuacct",
	) + visibleCPUTestMountInfoLine(
		47,
		linuxCPUCgroupV1,
		"/",
		cpusetMount,
		"cpuset",
	)
	sample, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		time.Second,
		"2:cpu,cpuacct:/system/job\n3:cpuset:/different/job\n",
		mountInfo,
		8,
		visibleCPUTestDependencies(
			t,
			usagePath,
			[]string{"1000000000\n", "3000000000\n"},
			time.Second,
		),
	)
	if err != nil {
		t.Fatalf("sampleVisibleSystemCPUFrom: %v", err)
	}
	if sample.CPUs != 4 {
		t.Fatalf("CPUs = %v want 4 with independent cpuset ignored", sample.CPUs)
	}
	if math.Abs(sample.Percent-50) > 0.000001 {
		t.Fatalf("percent = %v want 50", sample.Percent)
	}
}

func TestV1SubtreeRootRejectsIndependentCPUSet(t *testing.T) {
	base := t.TempDir()
	cpuMount := filepath.Join(base, "cpu,cpuacct")
	cpusetMount := filepath.Join(base, "cpuset")
	mountInfo := visibleCPUTestMountInfoLine(
		48,
		linuxCPUCgroupV1,
		"/tenant",
		cpuMount,
		"cpu,cpuacct",
	) + visibleCPUTestMountInfoLine(
		49,
		linuxCPUCgroupV1,
		"/tenant",
		cpusetMount,
		"cpuset",
	)
	_, err := inspectVisibleSystemCPUFrom(
		"2:cpu,cpuacct:/tenant/job\n3:cpuset:/tenant/job\n",
		mountInfo,
		8,
		os.ReadFile,
	)
	if err == nil || !strings.Contains(err.Error(), "independent cpuset") {
		t.Fatalf("error = %v want independent-cpuset failure", err)
	}
}

func TestV1SubtreeRootUsesSameMountCPUSet(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "cpu,cpuacct,cpuset")
	usagePath := filepath.Join(mountPoint, "cpuacct.usage")
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cpu.cfs_quota_us"),
		"400000\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cpu.cfs_period_us"),
		"100000\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cpuset.effective_cpus"),
		"0-1\n",
	)
	sample, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		time.Second,
		"2:cpu,cpuacct,cpuset:/tenant/job\n",
		visibleCPUTestMountInfoLine(
			50,
			linuxCPUCgroupV1,
			"/tenant",
			mountPoint,
			"cpu,cpuacct,cpuset",
		),
		8,
		visibleCPUTestDependencies(
			t,
			usagePath,
			[]string{"1000000000\n", "1500000000\n"},
			time.Second,
		),
	)
	if err != nil {
		t.Fatalf("sampleVisibleSystemCPUFrom: %v", err)
	}
	if sample.CPUs != 2 || math.Abs(sample.Percent-25) > 0.000001 {
		t.Fatalf("CPUs/percent = %v/%v want 2/25", sample.CPUs, sample.Percent)
	}
}

func TestV1VisibleSystemCPURejectsSplitCPUAndCPUAcctWithMatchingPaths(t *testing.T) {
	base := t.TempDir()
	cpuMount := filepath.Join(base, "cpu")
	cpuacctMount := filepath.Join(base, "cpuacct")
	cpusetMount := filepath.Join(base, "cpuset")
	writeVisibleCPUTestFile(
		t,
		filepath.Join(cpuMount, "cpu.cfs_quota_us"),
		"400000\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(cpuMount, "cpu.cfs_period_us"),
		"100000\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(cpusetMount, "cpuset.effective_cpus"),
		"0-1\n",
	)

	mountInfo := visibleCPUTestMountInfoLine(
		51,
		linuxCPUCgroupV1,
		"/tenant",
		cpuMount,
		"cpu",
	) + visibleCPUTestMountInfoLine(
		52,
		linuxCPUCgroupV1,
		"/tenant",
		cpuacctMount,
		"cpuacct",
	) + visibleCPUTestMountInfoLine(
		53,
		linuxCPUCgroupV1,
		"/tenant",
		cpusetMount,
		"cpuset",
	)
	_, err := inspectVisibleSystemCPUFrom(
		"2:cpu:/tenant/job\n3:cpuacct:/tenant/job\n4:cpuset:/tenant/job\n",
		mountInfo,
		8,
		os.ReadFile,
	)
	if err == nil || !strings.Contains(err.Error(), "one mount hierarchy") {
		t.Fatalf("error = %v want split-hierarchy failure", err)
	}
}

func TestHybridVisibleSystemCPUUsesActualV1CPUController(t *testing.T) {
	base := t.TempDir()
	v2Mount := filepath.Join(base, "unified")
	v1CPUMount := filepath.Join(base, "cpu,cpuacct")
	v1CpusetMount := filepath.Join(base, "cpuset")
	usagePath := filepath.Join(v1CPUMount, "cpuacct.usage")
	writeVisibleCPUTestFile(
		t,
		filepath.Join(v2Mount, "cpu.stat"),
		"usage_usec 999999\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(v2Mount, "cpu.max"),
		"800000 100000\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(v1CPUMount, "cpu.cfs_quota_us"),
		"100000\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(v1CPUMount, "cpu.cfs_period_us"),
		"100000\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(v1CpusetMount, "cpuset.effective_cpus"),
		"0-3\n",
	)
	mountInfo := visibleCPUTestMountInfoLine(
		81,
		linuxCPUCgroupV2,
		"/",
		v2Mount,
		"",
	) + visibleCPUTestMountInfoLine(
		82,
		linuxCPUCgroupV1,
		"/",
		v1CPUMount,
		"cpu,cpuacct",
	) + visibleCPUTestMountInfoLine(
		83,
		linuxCPUCgroupV1,
		"/",
		v1CpusetMount,
		"cpuset",
	)

	sample, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		time.Second,
		"0::/unified/job\n2:cpu,cpuacct:/legacy/job\n3:cpuset:/legacy/job\n",
		mountInfo,
		8,
		visibleCPUTestDependencies(
			t,
			usagePath,
			[]string{"1000000000\n", "1500000000\n"},
			time.Second,
		),
	)
	if err != nil {
		t.Fatalf("sampleVisibleSystemCPUFrom: %v", err)
	}
	if sample.Source != visibleSystemCPUSourceV1 {
		t.Fatalf("source = %q want %q", sample.Source, visibleSystemCPUSourceV1)
	}
	if sample.CPUs != 1 || math.Abs(sample.Percent-50) > 0.000001 {
		t.Fatalf("CPUs/percent = %v/%v want 1/50", sample.CPUs, sample.Percent)
	}
}

func TestHybridVisibleSystemCPUUsesV2WithoutV1CPUController(t *testing.T) {
	base := t.TempDir()
	v2Mount := filepath.Join(base, "unified")
	v1CPUAcctMount := filepath.Join(base, "cpuacct")
	usagePath := filepath.Join(v2Mount, "cpu.stat")
	writeVisibleCPUTestFile(
		t,
		filepath.Join(v2Mount, "cpu.max"),
		"200000 100000\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(v1CPUAcctMount, "cpuacct.usage"),
		"9000000000\n",
	)
	mountInfo := visibleCPUTestMountInfoLine(
		86,
		linuxCPUCgroupV2,
		"/",
		v2Mount,
		"",
	) + visibleCPUTestMountInfoLine(
		87,
		linuxCPUCgroupV1,
		"/",
		v1CPUAcctMount,
		"cpuacct",
	)

	sample, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		time.Second,
		"0::/unified/job\n2:cpuacct:/legacy/job\n",
		mountInfo,
		8,
		visibleCPUTestDependencies(
			t,
			usagePath,
			[]string{"usage_usec 1000000\n", "usage_usec 1400000\n"},
			time.Second,
		),
	)
	if err != nil {
		t.Fatalf("sampleVisibleSystemCPUFrom: %v", err)
	}
	if sample.Source != visibleSystemCPUSourceV2 {
		t.Fatalf("source = %q want %q", sample.Source, visibleSystemCPUSourceV2)
	}
	if sample.CPUs != 2 || math.Abs(sample.Percent-20) > 0.000001 {
		t.Fatalf("CPUs/percent = %v/%v want 2/20", sample.CPUs, sample.Percent)
	}
}

func TestHybridVisibleSystemCPUDoesNotFallbackAcrossV2AndV1(t *testing.T) {
	base := t.TempDir()
	v2Mount := filepath.Join(base, "unified")
	v1CPUAcctMount := filepath.Join(base, "cpuacct")
	writeVisibleCPUTestFile(
		t,
		filepath.Join(v2Mount, "cpu.max"),
		"200000 100000\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(v1CPUAcctMount, "cpuacct.usage"),
		"9000000000\n",
	)
	mountInfo := visibleCPUTestMountInfoLine(
		88,
		linuxCPUCgroupV2,
		"/",
		v2Mount,
		"",
	) + visibleCPUTestMountInfoLine(
		89,
		linuxCPUCgroupV1,
		"/",
		v1CPUAcctMount,
		"cpuacct",
	)

	_, err := inspectVisibleSystemCPUFrom(
		"0::/job\n2:cpuacct:/job\n",
		mountInfo,
		8,
		os.ReadFile,
	)
	if err == nil || !strings.Contains(err.Error(), "no readable CPU usage") {
		t.Fatalf("error = %v want v2-source failure without v1 fallback", err)
	}
}

func TestVisibleSystemCPURejectsStandaloneV1CPUAcct(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "cpuacct")
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cpuacct.usage"),
		"9000000000\n",
	)
	_, err := inspectVisibleSystemCPUFrom(
		"2:cpuacct:/job\n",
		visibleCPUTestMountInfoLine(
			90,
			linuxCPUCgroupV1,
			"/",
			mountPoint,
			"cpuacct",
		),
		8,
		os.ReadFile,
	)
	if err == nil || !strings.Contains(err.Error(), "no readable CPU usage") {
		t.Fatalf("error = %v want standalone-cpuacct failure", err)
	}
}

func TestV1VisibleSystemCPURejectsUnpairedControllerRoots(t *testing.T) {
	base := t.TempDir()
	cpuMount := filepath.Join(base, "cpu")
	cpuacctMount := filepath.Join(base, "cpuacct")
	writeVisibleCPUTestFile(
		t,
		filepath.Join(cpuMount, "cpu.cfs_quota_us"),
		"100000\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(cpuMount, "cpu.cfs_period_us"),
		"100000\n",
	)
	writeVisibleCPUTestFile(
		t,
		filepath.Join(cpuacctMount, "cpuacct.usage"),
		"1000000000\n",
	)
	mountInfo := visibleCPUTestMountInfoLine(
		91,
		linuxCPUCgroupV1,
		"/cpu-root",
		cpuMount,
		"cpu",
	) + visibleCPUTestMountInfoLine(
		92,
		linuxCPUCgroupV1,
		"/accounting-root",
		cpuacctMount,
		"cpuacct",
	)

	_, err := inspectVisibleSystemCPUFrom(
		"2:cpu:/cpu-root/job\n3:cpuacct:/accounting-root/job\n",
		mountInfo,
		8,
		os.ReadFile,
	)
	if err == nil || !strings.Contains(err.Error(), "one mount hierarchy") {
		t.Fatalf("error = %v want split-hierarchy failure", err)
	}
}

func TestV1VisibleSystemCPURejectsUnpairedControllerMemberships(t *testing.T) {
	t.Run("CPU and cpuacct differ", func(t *testing.T) {
		base := t.TempDir()
		cpuMount := filepath.Join(base, "cpu")
		cpuacctMount := filepath.Join(base, "cpuacct")
		writeVisibleCPUTestFile(
			t,
			filepath.Join(cpuMount, "cpu.cfs_quota_us"),
			"100000\n",
		)
		writeVisibleCPUTestFile(
			t,
			filepath.Join(cpuMount, "cpu.cfs_period_us"),
			"100000\n",
		)
		writeVisibleCPUTestFile(
			t,
			filepath.Join(cpuacctMount, "cpuacct.usage"),
			"1000000000\n",
		)
		mountInfo := visibleCPUTestMountInfoLine(
			111,
			linuxCPUCgroupV1,
			"/",
			cpuMount,
			"cpu",
		) + visibleCPUTestMountInfoLine(
			112,
			linuxCPUCgroupV1,
			"/",
			cpuacctMount,
			"cpuacct",
		)
		_, err := inspectVisibleSystemCPUFrom(
			"2:cpu:/cpu-job\n3:cpuacct:/accounting-job\n",
			mountInfo,
			8,
			os.ReadFile,
		)
		if err == nil || !strings.Contains(err.Error(), "one mount hierarchy") {
			t.Fatalf("error = %v want split-hierarchy failure", err)
		}
	})
}

func TestSelectLinuxVisibleCPUMountPrefersSpecificBindRoot(t *testing.T) {
	mounts := []linuxVisibleCPUMount{
		{
			version:    linuxCPUCgroupV2,
			root:       "/",
			mountPoint: "/sys/fs/cgroup",
		},
		{
			version:    linuxCPUCgroupV2,
			root:       "/tenant",
			mountPoint: "/run/container/cgroup",
		},
	}
	selected, found, err := selectLinuxVisibleCPUMount(
		mounts,
		linuxCPUCgroupV2,
		"",
		"/tenant/job",
	)
	if err != nil {
		t.Fatalf("selectLinuxVisibleCPUMount: %v", err)
	}
	if !found || selected.root != "/tenant" {
		t.Fatalf("selected = %+v/%v want /tenant/true", selected, found)
	}
}

func TestSelectLinuxVisibleCPUMountRejectsAmbiguousNamespaceRoots(t *testing.T) {
	mounts := []linuxVisibleCPUMount{
		{
			version:    linuxCPUCgroupV2,
			root:       "/tenant-a",
			mountPoint: "/one",
		},
		{
			version:    linuxCPUCgroupV2,
			root:       "/tenant-b",
			mountPoint: "/two",
		},
	}
	_, _, err := selectLinuxVisibleCPUMount(
		mounts,
		linuxCPUCgroupV2,
		"",
		"/",
	)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("error = %v want ambiguous-root failure", err)
	}
}

func TestVisibleSystemCPUCounterValidation(t *testing.T) {
	t.Run("v2 parser rejects malformed counters", func(t *testing.T) {
		tests := []string{
			"user_usec 1\n",
			"usage_usec\n",
			"usage_usec -1\n",
			"usage_usec 1\nusage_usec 2\n",
		}
		for _, value := range tests {
			if _, err := parseLinuxCgroupV2CPUUsage([]byte(value)); err == nil {
				t.Fatalf("parseLinuxCgroupV2CPUUsage(%q) succeeded", value)
			}
		}
	})

	t.Run("v1 parser rejects malformed counters", func(t *testing.T) {
		for _, value := range []string{"", "-1", "1 2", "invalid"} {
			if _, err := parseLinuxCgroupV1CPUUsage([]byte(value)); err == nil {
				t.Fatalf("parseLinuxCgroupV1CPUUsage(%q) succeeded", value)
			}
		}
	})

	t.Run("sampler rejects a backwards counter", func(t *testing.T) {
		mountPoint := filepath.Join(t.TempDir(), "unified")
		usagePath := filepath.Join(mountPoint, "cpu.stat")
		writeVisibleCPUTestFile(
			t,
			filepath.Join(mountPoint, "cpu.max"),
			"max 100000\n",
		)
		_, err := sampleVisibleSystemCPUFrom(
			context.Background(),
			time.Second,
			"0::/\n",
			visibleCPUTestMountInfoLine(
				61,
				linuxCPUCgroupV2,
				"/",
				mountPoint,
				"",
			),
			4,
			visibleCPUTestDependencies(
				t,
				usagePath,
				[]string{"usage_usec 200\n", "usage_usec 100\n"},
				time.Second,
			),
		)
		if err == nil || !strings.Contains(err.Error(), "moved backwards") {
			t.Fatalf("error = %v want backwards-counter failure", err)
		}
	})
}

func TestVisibleSystemCPUSampleCancellation(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "unified")
	usagePath := filepath.Join(mountPoint, "cpu.stat")
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cpu.max"),
		"max 100000\n",
	)
	dependencies := visibleCPUTestDependencies(
		t,
		usagePath,
		[]string{"usage_usec 100\n", "usage_usec 200\n"},
		time.Hour,
	)
	ctx, cancel := context.WithCancel(context.Background())
	dependencies.wait = func(ctx context.Context, _ time.Duration) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}

	_, err := sampleVisibleSystemCPUFrom(
		ctx,
		time.Hour,
		"0::/\n",
		visibleCPUTestMountInfoLine(
			71,
			linuxCPUCgroupV2,
			"/",
			mountPoint,
			"",
		),
		4,
		dependencies,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v want context.Canceled", err)
	}
}

func TestVisibleSystemCPUSampleRejectsCapacityChange(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "unified")
	usagePath := filepath.Join(mountPoint, "cpu.stat")
	quotaPath := filepath.Join(mountPoint, "cpu.max")
	dependencies := visibleCPUTestDependencies(
		t,
		usagePath,
		[]string{"usage_usec 100\n", "usage_usec 200\n"},
		time.Second,
	)
	baseReadFile := dependencies.readFile
	quotaRead := 0
	dependencies.readFile = func(path string) ([]byte, error) {
		if path != quotaPath {
			return baseReadFile(path)
		}
		values := []string{"200000 100000\n", "100000 100000\n"}
		if quotaRead >= len(values) {
			return nil, fmt.Errorf("unexpected quota read %d", quotaRead+1)
		}
		value := values[quotaRead]
		quotaRead++
		return []byte(value), nil
	}

	_, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		time.Second,
		"0::/\n",
		visibleCPUTestMountInfoLine(
			76,
			linuxCPUCgroupV2,
			"/",
			mountPoint,
			"",
		),
		4,
		dependencies,
	)
	if err == nil || !strings.Contains(err.Error(), "capacity changed") {
		t.Fatalf("error = %v want capacity-change failure", err)
	}
}

func TestVisibleSystemCPUSampleBracketsCounterReads(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "unified")
	usagePath := filepath.Join(mountPoint, "cpu.stat")
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cpu.max"),
		"max 100000\n",
	)
	clock := time.Unix(500, 0)
	usageRead := 0
	dependencies := linuxVisibleCPUSampleDependencies{
		readFile: func(path string) ([]byte, error) {
			if path != usagePath {
				return os.ReadFile(path)
			}
			usageRead++
			switch usageRead {
			case 1:
				return []byte("usage_usec 900000\n"), nil
			case 2:
				clock = clock.Add(20 * time.Millisecond)
				return []byte("usage_usec 1000000\n"), nil
			case 3:
				clock = clock.Add(80 * time.Millisecond)
				return []byte("usage_usec 1525000\n"), nil
			default:
				return nil, fmt.Errorf("unexpected usage read %d", usageRead)
			}
		},
		now: func() time.Time {
			return clock
		},
		wait: func(context.Context, time.Duration) error {
			clock = clock.Add(time.Second)
			return nil
		},
	}
	sample, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		time.Second,
		"0::/\n",
		visibleCPUTestMountInfoLine(
			96,
			linuxCPUCgroupV2,
			"/",
			mountPoint,
			"",
		),
		1,
		dependencies,
	)
	if err != nil {
		t.Fatalf("sampleVisibleSystemCPUFrom: %v", err)
	}
	if sample.Busy != 525*time.Millisecond ||
		sample.Elapsed != 1050*time.Millisecond {
		t.Fatalf(
			"busy/elapsed = %s/%s want 525ms/1.05s",
			sample.Busy,
			sample.Elapsed,
		)
	}
	if math.Abs(sample.Percent-50) > 0.000001 {
		t.Fatalf("percent = %v want 50", sample.Percent)
	}
}

func TestVisibleSystemCPUSampleRejectsSlowCounterRead(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "unified")
	usagePath := filepath.Join(mountPoint, "cpu.stat")
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cpu.max"),
		"max 100000\n",
	)
	clock := time.Unix(600, 0)
	usageRead := 0
	dependencies := linuxVisibleCPUSampleDependencies{
		readFile: func(path string) ([]byte, error) {
			if path != usagePath {
				return os.ReadFile(path)
			}
			usageRead++
			if usageRead == 2 {
				clock = clock.Add(100 * time.Millisecond)
			}
			return []byte("usage_usec 1000000\n"), nil
		},
		now: func() time.Time {
			return clock
		},
		wait: func(context.Context, time.Duration) error {
			clock = clock.Add(time.Second)
			return nil
		},
	}
	sample, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		500*time.Millisecond,
		"0::/\n",
		visibleCPUTestMountInfoLine(
			97,
			linuxCPUCgroupV2,
			"/",
			mountPoint,
			"",
		),
		1,
		dependencies,
	)
	if err == nil || !strings.Contains(err.Error(), "safe limit") {
		t.Fatalf("sample/error = %+v/%v want slow-read failure", sample, err)
	}
}

func TestVisibleSystemCPUBoundaryIDIsStableAndOpaque(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "unified")
	usagePath := filepath.Join(mountPoint, "cpu.stat")
	writeVisibleCPUTestFile(t, usagePath, "usage_usec 1000\n")
	writeVisibleCPUTestFile(
		t,
		filepath.Join(mountPoint, "cpu.max"),
		"200000 100000\n",
	)
	membership := "0::/tenant/job\n"
	mountInfo := visibleCPUTestMountInfoLine(
		101,
		linuxCPUCgroupV2,
		"/tenant",
		mountPoint,
		"",
	)
	first, err := inspectVisibleSystemCPUFrom(
		membership,
		mountInfo,
		8,
		os.ReadFile,
	)
	if err != nil {
		t.Fatalf("first inspection: %v", err)
	}
	second, err := inspectVisibleSystemCPUFrom(
		membership,
		mountInfo,
		8,
		os.ReadFile,
	)
	if err != nil {
		t.Fatalf("second inspection: %v", err)
	}
	if first.boundary.boundaryID != second.boundary.boundaryID {
		t.Fatalf(
			"stable boundary IDs differ: %q/%q",
			first.boundary.boundaryID,
			second.boundary.boundaryID,
		)
	}
	if !strings.HasPrefix(first.boundary.boundaryID, "cgcpu-") ||
		strings.Contains(first.boundary.boundaryID, "/tenant") ||
		strings.Contains(first.boundary.boundaryID, mountPoint) {
		t.Fatalf("boundary ID is not opaque: %q", first.boundary.boundaryID)
	}

	changed, err := inspectVisibleSystemCPUFrom(
		membership,
		visibleCPUTestMountInfoLine(
			102,
			linuxCPUCgroupV2,
			"/tenant",
			mountPoint,
			"",
		),
		8,
		os.ReadFile,
	)
	if err != nil {
		t.Fatalf("changed inspection: %v", err)
	}
	if changed.boundary.boundaryID == first.boundary.boundaryID {
		t.Fatal("boundary ID did not change with the mount identity")
	}

	sample, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		time.Second,
		membership,
		mountInfo,
		8,
		visibleCPUTestDependencies(
			t,
			usagePath,
			[]string{"usage_usec 1000\n", "usage_usec 2000\n"},
			time.Second,
		),
	)
	if err != nil {
		t.Fatalf("sampleVisibleSystemCPUFrom: %v", err)
	}
	if sample.BoundaryID != first.boundary.boundaryID ||
		sample.BoundaryKind != first.boundary.boundaryKind {
		t.Fatalf(
			"inspect/sample boundary differs: id=%q/%q kind=%q/%q",
			first.boundary.boundaryID,
			sample.BoundaryID,
			first.boundary.boundaryKind,
			sample.BoundaryKind,
		)
	}
}

func TestVisibleSystemCPUSampleReinspectsBoundary(t *testing.T) {
	tests := []struct {
		name          string
		secondMountID int
		secondReadErr error
		wantErr       bool
	}{
		{name: "unchanged", secondMountID: 121},
		{name: "same capacity remounted", secondMountID: 122, wantErr: true},
		{
			name:          "input reread fails",
			secondMountID: 121,
			secondReadErr: errors.New("input reread failed"),
			wantErr:       true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mountPoint := filepath.Join(t.TempDir(), "unified")
			usagePath := filepath.Join(mountPoint, "cpu.stat")
			writeVisibleCPUTestFile(
				t,
				filepath.Join(mountPoint, "cpu.max"),
				"200000 100000\n",
			)
			usageValues := []string{
				"usage_usec 900\n",
				"usage_usec 1000\n",
				"usage_usec 201000\n",
				"usage_usec 202000\n",
			}
			usageRead := 0
			startedAt := time.Unix(700, 0)
			timeRead := 0
			dependencies := linuxVisibleCPUSampleDependencies{
				readFile: func(path string) ([]byte, error) {
					if path != usagePath {
						return os.ReadFile(path)
					}
					if usageRead >= len(usageValues) {
						return nil, fmt.Errorf(
							"unexpected usage read %d",
							usageRead+1,
						)
					}
					value := usageValues[usageRead]
					usageRead++
					return []byte(value), nil
				},
				now: func() time.Time {
					defer func() {
						timeRead++
					}()
					if timeRead < 2 {
						return startedAt
					}
					return startedAt.Add(time.Second)
				},
				wait: func(context.Context, time.Duration) error {
					return nil
				},
			}
			inputRead := 0
			readInputs := func(
				context.Context,
			) (string, string, int, error) {
				inputRead++
				if inputRead == 2 && test.secondReadErr != nil {
					return "", "", 0, test.secondReadErr
				}
				mountID := 121
				if inputRead == 2 {
					mountID = test.secondMountID
				}
				return "0::/job\n", visibleCPUTestMountInfoLine(
					mountID,
					linuxCPUCgroupV2,
					"/",
					mountPoint,
					"",
				), 4, nil
			}

			sample, err := sampleVisibleSystemCPUWithReinspection(
				context.Background(),
				time.Second,
				readInputs,
				dependencies,
			)
			if test.wantErr {
				if err == nil {
					t.Fatalf("sample = %+v want fail-closed error", sample)
				}
				return
			}
			if err != nil {
				t.Fatalf("sampleVisibleSystemCPUWithReinspection: %v", err)
			}
			if sample.Busy != 200*time.Millisecond ||
				sample.Elapsed != time.Second {
				t.Fatalf(
					"busy/elapsed = %s/%s want 200ms/1s",
					sample.Busy,
					sample.Elapsed,
				)
			}
			if inputRead != 2 || usageRead != 4 {
				t.Fatalf(
					"input/usage reads = %d/%d want 2/4",
					inputRead,
					usageRead,
				)
			}
		})
	}
}

func TestClassifyLinuxVisibleCPUBoundary(t *testing.T) {
	tests := []struct {
		name       string
		root       string
		membership string
		want       VisibleSystemCPUBoundaryKind
	}{
		{name: "self", root: "/tenant", membership: "/tenant", want: VisibleSystemCPUBoundarySelf},
		{name: "ancestor", root: "/tenant", membership: "/tenant/job", want: VisibleSystemCPUBoundaryAncestor},
		{name: "namespace root", root: "/tenant", membership: "/", want: VisibleSystemCPUBoundaryNamespaceRoot},
		{name: "unknown", root: "/tenant", membership: "/other/job", want: VisibleSystemCPUBoundaryUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyLinuxVisibleCPUBoundary(
				test.root,
				test.membership,
			); got != test.want {
				t.Fatalf("kind = %q want %q", got, test.want)
			}
		})
	}
}

func TestVisibleSystemCPUSampleDurationAndFiniteValidation(t *testing.T) {
	dependencies := linuxVisibleCPUSampleDependencies{
		readFile: os.ReadFile,
		now:      time.Now,
		wait:     waitVisibleSystemCPUSample,
	}
	if _, err := sampleVisibleSystemCPUFrom(
		context.Background(),
		0,
		"",
		"",
		1,
		dependencies,
	); err == nil {
		t.Fatal("zero sample duration succeeded")
	}

	for _, test := range []struct {
		name string
		unit time.Duration
		cpus float64
	}{
		{name: "zero unit", unit: 0, cpus: 1},
		{name: "non-finite CPUs", unit: time.Nanosecond, cpus: math.NaN()},
		{name: "zero CPUs", unit: time.Nanosecond, cpus: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := calculateLinuxVisibleCPUPercent(
				1,
				test.unit,
				time.Second,
				test.cpus,
			); err == nil {
				t.Fatal("invalid utilization inputs succeeded")
			}
		})
	}
	if _, _, err := calculateLinuxVisibleCPUPercent(
		^uint64(0),
		time.Microsecond,
		time.Second,
		1,
	); err == nil {
		t.Fatal("overflowing busy duration succeeded")
	}
}

func visibleCPUTestDependencies(
	t *testing.T,
	usagePath string,
	usageValues []string,
	elapsed time.Duration,
) linuxVisibleCPUSampleDependencies {
	t.Helper()
	usageRead := 0
	startedAt := time.Unix(100, 0)
	timeRead := 0
	return linuxVisibleCPUSampleDependencies{
		readFile: func(path string) ([]byte, error) {
			if path != usagePath {
				return os.ReadFile(path)
			}
			valueIndex := usageRead
			if len(usageValues) == 2 {
				if usageRead <= 1 {
					valueIndex = 0
				} else {
					valueIndex = 1
				}
			}
			if valueIndex >= len(usageValues) || usageRead >= 3 {
				return nil, fmt.Errorf("unexpected usage counter read %d", usageRead+1)
			}
			value := usageValues[valueIndex]
			usageRead++
			return []byte(value), nil
		},
		now: func() time.Time {
			defer func() {
				timeRead++
			}()
			if timeRead < 2 {
				return startedAt
			}
			return startedAt.Add(elapsed)
		},
		wait: func(context.Context, time.Duration) error {
			return nil
		},
	}
}

func visibleCPUTestMountInfoLine(
	id int,
	version linuxCPUCgroupVersion,
	root string,
	mountPoint string,
	controllers string,
) string {
	root = escapeVisibleCPUTestMountInfoPath(root)
	mountPoint = escapeVisibleCPUTestMountInfoPath(mountPoint)
	if version == linuxCPUCgroupV2 {
		return fmt.Sprintf(
			"%d 23 0:26 %s %s rw,nosuid,nodev - cgroup2 cgroup rw\n",
			id,
			root,
			mountPoint,
		)
	}
	return fmt.Sprintf(
		"%d 23 0:27 %s %s rw,nosuid,nodev,%s - cgroup cgroup rw,%s\n",
		id,
		root,
		mountPoint,
		controllers,
		controllers,
	)
}

func escapeVisibleCPUTestMountInfoPath(path string) string {
	return strings.NewReplacer(
		`\`, `\134`,
		" ", `\040`,
		"\t", `\011`,
		"\n", `\012`,
	).Replace(path)
}

func writeVisibleCPUTestFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}
