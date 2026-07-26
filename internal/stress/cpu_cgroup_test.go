package stress

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxCgroupCPUQuotaFromMountInfo(t *testing.T) {
	t.Run("v2 translates mount root and checks ancestors", func(t *testing.T) {
		mountPoint := filepath.Join(t.TempDir(), "cpu mount")
		writeTestCPUFile(t, filepath.Join(mountPoint, "cpu.max"), "150000 100000\n")
		writeTestCPUFile(
			t,
			filepath.Join(mountPoint, "job", "cpu.max"),
			"200000 100000\n",
		)

		quota, limited, err := linuxCgroupCPUQuotaFrom(
			"0::/tenant.slice/job\n",
			testCPUMountInfoLine(29, linuxCPUCgroupV2, "/tenant.slice", mountPoint),
		)
		if err != nil {
			t.Fatalf("linuxCgroupCPUQuotaFrom: %v", err)
		}
		if !limited || quota != 1.5 {
			t.Fatalf("quota/limited = %v/%v want 1.5/true", quota, limited)
		}
	})

	t.Run("hybrid selects tighter v1 quota", func(t *testing.T) {
		base := t.TempDir()
		v2Mount := filepath.Join(base, "unified")
		v1Mount := filepath.Join(base, "legacy")
		writeTestCPUFile(t, filepath.Join(v2Mount, "cpu.max"), "max 100000\n")
		writeTestCPUFile(
			t,
			filepath.Join(v2Mount, "unified", "job", "cpu.max"),
			"200000 100000\n",
		)
		writeTestCPUFile(
			t,
			filepath.Join(v1Mount, "cpu.cfs_quota_us"),
			"-1\n",
		)
		writeTestCPUFile(
			t,
			filepath.Join(v1Mount, "legacy", "job", "cpu.cfs_quota_us"),
			"100000\n",
		)
		writeTestCPUFile(
			t,
			filepath.Join(v1Mount, "legacy", "job", "cpu.cfs_period_us"),
			"100000\n",
		)

		mountInfo := testCPUMountInfoLine(
			29,
			linuxCPUCgroupV2,
			"/",
			v2Mount,
		) + testCPUMountInfoLine(
			30,
			linuxCPUCgroupV1,
			"/",
			v1Mount,
		)
		quota, limited, err := linuxCgroupCPUQuotaFrom(
			"0::/unified/job\n2:cpu,cpuacct:/legacy/job\n",
			mountInfo,
		)
		if err != nil {
			t.Fatalf("linuxCgroupCPUQuotaFrom: %v", err)
		}
		if !limited || quota != 1 {
			t.Fatalf("quota/limited = %v/%v want 1/true", quota, limited)
		}
	})

	t.Run("hybrid permits CPU controller only on v1", func(t *testing.T) {
		base := t.TempDir()
		v2Mount := filepath.Join(base, "unified")
		v1Mount := filepath.Join(base, "legacy")
		if err := os.MkdirAll(filepath.Join(v2Mount, "job"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeTestCPUFile(
			t,
			filepath.Join(v1Mount, "job", "cpu.cfs_quota_us"),
			"50000\n",
		)
		writeTestCPUFile(
			t,
			filepath.Join(v1Mount, "job", "cpu.cfs_period_us"),
			"100000\n",
		)

		mountInfo := testCPUMountInfoLine(
			29,
			linuxCPUCgroupV2,
			"/",
			v2Mount,
		) + testCPUMountInfoLine(
			30,
			linuxCPUCgroupV1,
			"/",
			v1Mount,
		)
		quota, limited, err := linuxCgroupCPUQuotaFrom(
			"0::/job\n2:cpu:/job\n",
			mountInfo,
		)
		if err != nil {
			t.Fatalf("linuxCgroupCPUQuotaFrom: %v", err)
		}
		if !limited || quota != 0.5 {
			t.Fatalf("quota/limited = %v/%v want 0.5/true", quota, limited)
		}
	})

	t.Run("v2 unlimited is a valid readable constraint", func(t *testing.T) {
		mountPoint := filepath.Join(t.TempDir(), "unified")
		writeTestCPUFile(
			t,
			filepath.Join(mountPoint, "job", "cpu.max"),
			"max 100000\n",
		)

		quota, limited, err := linuxCgroupCPUQuotaFrom(
			"0::/job\n",
			testCPUMountInfoLine(29, linuxCPUCgroupV2, "/", mountPoint),
		)
		if err != nil {
			t.Fatalf("linuxCgroupCPUQuotaFrom: %v", err)
		}
		if limited || quota != 0 {
			t.Fatalf("quota/limited = %v/%v want 0/false", quota, limited)
		}
	})

	t.Run("direct mount excludes unrelated bind mount", func(t *testing.T) {
		base := t.TempDir()
		directMount := filepath.Join(base, "direct")
		unrelatedMount := filepath.Join(base, "unrelated")
		writeTestCPUFile(
			t,
			filepath.Join(directMount, "job", "cpu.max"),
			"200000 100000\n",
		)
		writeTestCPUFile(
			t,
			filepath.Join(unrelatedMount, "job", "cpu.max"),
			"10000 100000\n",
		)

		mountInfo := testCPUMountInfoLine(
			29,
			linuxCPUCgroupV2,
			"/",
			directMount,
		) + testCPUMountInfoLine(
			30,
			linuxCPUCgroupV2,
			"/other",
			unrelatedMount,
		)
		quota, limited, err := linuxCgroupCPUQuotaFrom(
			"0::/job\n",
			mountInfo,
		)
		if err != nil {
			t.Fatalf("linuxCgroupCPUQuotaFrom: %v", err)
		}
		if !limited || quota != 2 {
			t.Fatalf("quota/limited = %v/%v want 2/true", quota, limited)
		}
	})
}

func TestLinuxCgroupCPUQuotaFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		membership string
		setup      func(t *testing.T) string
	}{
		{
			name:       "membership has no matching mount",
			membership: "0::/job\n",
			setup: func(t *testing.T) string {
				return "20 10 0:2 / /proc rw - proc proc rw\n"
			},
		},
		{
			name:       "matched v2 mount has no CPU control file",
			membership: "0::/job\n",
			setup: func(t *testing.T) string {
				mountPoint := filepath.Join(t.TempDir(), "unified")
				if err := os.MkdirAll(filepath.Join(mountPoint, "job"), 0o700); err != nil {
					t.Fatal(err)
				}
				return testCPUMountInfoLine(
					29,
					linuxCPUCgroupV2,
					"/",
					mountPoint,
				)
			},
		},
		{
			name:       "quota file cannot be read",
			membership: "0::/job\n",
			setup: func(t *testing.T) string {
				mountPoint := filepath.Join(t.TempDir(), "unified")
				quotaPath := filepath.Join(mountPoint, "job", "cpu.max")
				if err := os.MkdirAll(quotaPath, 0o700); err != nil {
					t.Fatal(err)
				}
				return testCPUMountInfoLine(
					29,
					linuxCPUCgroupV2,
					"/",
					mountPoint,
				)
			},
		},
		{
			name:       "malformed mountinfo",
			membership: "0::/job\n",
			setup: func(t *testing.T) string {
				return "not mountinfo\n"
			},
		},
		{
			name:       "malformed membership",
			membership: "not-membership\n",
			setup: func(t *testing.T) string {
				return ""
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := linuxCgroupCPUQuotaFrom(
				test.membership,
				test.setup(t),
			)
			if err == nil {
				t.Fatal("expected fail-closed error")
			}
		})
	}
}

func testCPUMountInfoLine(
	id int,
	version linuxCPUCgroupVersion,
	root string,
	mountPoint string,
) string {
	root = escapeTestCPUMountInfoPath(root)
	mountPoint = escapeTestCPUMountInfoPath(mountPoint)
	if version == linuxCPUCgroupV2 {
		return fmt.Sprintf(
			"%d 23 0:26 %s %s rw,nosuid,nodev - cgroup2 cgroup rw\n",
			id,
			root,
			mountPoint,
		)
	}
	return fmt.Sprintf(
		"%d 23 0:27 %s %s rw,nosuid,nodev - cgroup cgroup rw,cpu,cpuacct\n",
		id,
		root,
		mountPoint,
	)
}

func escapeTestCPUMountInfoPath(path string) string {
	return strings.NewReplacer(
		`\`, `\134`,
		" ", `\040`,
		"\t", `\011`,
		"\n", `\012`,
	).Replace(path)
}

func writeTestCPUFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
