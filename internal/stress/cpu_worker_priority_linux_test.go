//go:build linux

package stress

import "testing"

func TestLinuxNiceFromKernelPriority(t *testing.T) {
	tests := []struct {
		kernelPriority int
		wantNice       int
		wantErr        bool
	}{
		{kernelPriority: 40, wantNice: -20},
		{kernelPriority: 20, wantNice: 0},
		{kernelPriority: 1, wantNice: 19},
		{kernelPriority: 0, wantErr: true},
		{kernelPriority: 41, wantErr: true},
	}

	for _, tt := range tests {
		got, err := linuxNiceFromKernelPriority(tt.kernelPriority)
		if (err != nil) != tt.wantErr {
			t.Fatalf(
				"kernel priority %d error = %v wantErr=%v",
				tt.kernelPriority,
				err,
				tt.wantErr,
			)
		}
		if err == nil && got != tt.wantNice {
			t.Fatalf(
				"kernel priority %d nice = %d want %d",
				tt.kernelPriority,
				got,
				tt.wantNice,
			)
		}
	}
}
