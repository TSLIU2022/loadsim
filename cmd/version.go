package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var version = "0.5.0"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the current version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("LoadSim %s\n", version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
