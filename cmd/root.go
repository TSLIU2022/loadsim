package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newRootCommand() *cobra.Command {
	command := &cobra.Command{
		Use:           "loadsim",
		Short:         "LoadSim production resource filler and test load generator",
		Long:          "LoadSim keeps visible CPU and memory use inside production bands, or generates explicit test load in isolated environments.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(command *cobra.Command, args []string) error {
			return command.Help()
		},
	}
	return command
}

var rootCmd = newRootCommand()

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
