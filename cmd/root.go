package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:           "loadsim",
	Short:         "LoadSim resource occupancy tool for CPU, RAM, and combo scenarios",
	Long:          "LoadSim is a resource occupancy CLI for creating controllable CPU, RAM, and combined resource scenarios.",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
