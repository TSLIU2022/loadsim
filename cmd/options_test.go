package cmd

import (
	"strings"
	"testing"

	"github.com/fanderchan/loadsim/internal/stress"
)

func TestParsePercentBand(t *testing.T) {
	band, err := parsePercentBand(" 30 : 50 ", "CPU band", 100, 1)
	if err != nil {
		t.Fatalf("parse valid band: %v", err)
	}
	if band.low != 30 || band.high != 50 {
		t.Fatalf("band=%+v want 30:50", band)
	}

	for _, value := range []string{
		"",
		"30",
		"30:50:60",
		"-1:50",
		"50:50",
		"60:50",
		"30:101",
		"NaN:50",
	} {
		if _, err := parsePercentBand(
			value,
			"CPU band",
			100,
			1,
		); err == nil {
			t.Fatalf("invalid band %q was accepted", value)
		}
	}
	if _, err := parsePercentBand(
		"30:34",
		"memory band",
		80,
		5,
	); err == nil {
		t.Fatal("narrow memory band was accepted")
	}
}

func TestSelectMemoryMaximumSupportsMiBAndGiB(t *testing.T) {
	if got, err := selectMemoryMaximum(2330, 0); err != nil || got != 2330 {
		t.Fatalf("MiB maximum=%d err=%v", got, err)
	}
	if got, err := selectMemoryMaximum(0, 64); err != nil || got != 65536 {
		t.Fatalf("GiB maximum=%d err=%v", got, err)
	}
	for _, values := range [][2]int{{0, 0}, {1, 1}, {-1, 0}} {
		if _, err := selectMemoryMaximum(
			values[0],
			values[1],
		); err == nil {
			t.Fatalf("maximum pair %v was accepted", values)
		}
	}
}

func TestParseMemoryMaximumSupportsAutomaticMode(t *testing.T) {
	maximum, automatic, err := parseMemoryMaximum(" auto ", 0)
	if err != nil || !automatic || maximum != 0 {
		t.Fatalf("automatic maximum=%d automatic=%t err=%v", maximum, automatic, err)
	}

	if _, _, err := parseMemoryMaximum("auto", 2); err == nil {
		t.Fatal("automatic maximum accepted with a GiB maximum")
	}
	if _, _, err := parseMemoryMaximum("invalid", 0); err == nil {
		t.Fatal("invalid memory maximum was accepted")
	}
}

func TestParseWorkerScheduler(t *testing.T) {
	for input, want := range map[string]stress.CPUWorkerScheduler{
		"idle":     stress.WorkerSchedulerIdle,
		" NORMAL ": stress.WorkerSchedulerNormal,
	} {
		got, err := parseWorkerScheduler(input)
		if err != nil || got != want {
			t.Fatalf("parse %q=%q err=%v want=%q", input, got, err, want)
		}
	}
	if _, err := parseWorkerScheduler("realtime"); err == nil {
		t.Fatal("unsupported scheduler was accepted")
	}
}

func TestParseYieldPolicy(t *testing.T) {
	for input, want := range map[string]stress.YieldPolicy{
		"gradual": stress.YieldPolicyGradual,
		" ZERO ":  stress.YieldPolicyZero,
	} {
		got, err := parseYieldPolicy(input)
		if err != nil || got != want {
			t.Fatalf("parse %q=%q err=%v want=%q", input, got, err, want)
		}
	}
	if _, err := parseYieldPolicy("pause"); err == nil {
		t.Fatal("unsupported yield policy was accepted")
	}
}

func TestPublicCommandsUseIntentModel(t *testing.T) {
	for _, name := range []string{"fill", "stress", "check", "version"} {
		if command, _, err := rootCmd.Find([]string{name}); err != nil ||
			command == rootCmd ||
			command.Name() != name {
			t.Fatalf("missing public command %q: command=%v err=%v", name, command, err)
		}
	}
	for _, legacy := range []string{"cpu", "ram", "combo"} {
		command, _, err := rootCmd.Find([]string{legacy})
		if err == nil && command != rootCmd && command.Name() == legacy {
			t.Fatalf("legacy command %q is still exposed", legacy)
		}
	}
}

func TestRootRejectsUnknownCommand(t *testing.T) {
	command := newRootCommand()
	command.SetArgs([]string{"cpu"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("legacy command error=%v want unknown command", err)
	}
}

func TestFillMemoryRequiresExactlyOneAbsoluteCap(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing cap",
			args: []string{"--memory", "30:50"},
			want: "requires exactly one",
		},
		{
			name: "two caps",
			args: []string{
				"--memory", "30:50",
				"--memory-max-mib", "2048",
				"--memory-max-gib", "2",
			},
			want: "exactly one",
		},
		{
			name: "nice with default idle scheduler",
			args: []string{
				"--cpu", "30:50",
				"--cpu-nice", "19",
			},
			want: "applies only",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var options fillOptions
			command := newFillCommand(&options)
			command.SetArgs(test.args)
			err := command.Execute()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want substring %q", err, test.want)
			}
		})
	}
}
