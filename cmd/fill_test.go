package cmd

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/fanderchan/loadsim/internal/system"
)

func TestFormatCPUCgroupStatusFields(t *testing.T) {
	tests := []struct {
		name string
		info system.CPUCgroupInfo
		err  error
		want []string
	}{
		{
			name: "limited v2 ancestor",
			info: system.CPUCgroupInfo{
				Version:          "v2",
				QuotaKnown:       true,
				QuotaLimited:     true,
				QuotaCPUs:        24,
				QuotaLevel:       "ancestor",
				WeightKnown:      true,
				WeightKind:       "weight",
				Weight:           1,
				WeightLevel:      "ancestor",
				ThrottlingKnown:  true,
				Periods:          20,
				ThrottledPeriods: 7,
				ThrottledTime:    125 * time.Millisecond,
				ThrottlingLevel:  "ancestor",
			},
			want: []string{
				"cpu_cgroup=v2",
				"cpu_cgroup_probe=ok",
				"cpu_cgroup_quota=24.00CPU",
				"cpu_cgroup_quota_level=ancestor",
				"cpu_cgroup_weight_min=1",
				"cpu_cgroup_weight_level=ancestor",
				"cpu_cgroup_periods=20",
				"cpu_cgroup_throttled_periods=7",
				"cpu_cgroup_throttled_time=125ms",
				"cpu_cgroup_throttling_level=ancestor",
			},
		},
		{
			name: "unlimited v1",
			info: system.CPUCgroupInfo{
				Version:     "v1",
				QuotaKnown:  true,
				WeightKnown: true,
				WeightKind:  "shares",
				Weight:      1024,
				WeightLevel: "self",
			},
			want: []string{
				"cpu_cgroup=v1",
				"cpu_cgroup_probe=ok",
				"cpu_cgroup_quota=unlimited",
				"cpu_cgroup_shares_min=1024",
				"cpu_cgroup_shares_level=self",
				"cpu_cgroup_throttling=unknown",
			},
		},
		{
			name: "probe failure hides details",
			err:  errors.New("read /private/customer/path: permission denied"),
			want: []string{
				"cpu_cgroup=unknown",
				"cpu_cgroup_probe=failed",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := formatCPUCgroupStatusFields(test.info, test.err)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("fields = %#v want %#v", got, test.want)
			}
		})
	}
}
