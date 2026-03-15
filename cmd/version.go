package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags.
var Version = "dev"

var versionJSON bool

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version of agent-minder",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if versionJSON {
			info := map[string]string{"version": Version}
			enc := json.NewEncoder(os.Stdout)
			if err := enc.Encode(info); err != nil {
				fmt.Fprintf(os.Stderr, "error encoding JSON: %v\n", err)
				os.Exit(1)
			}
			return
		}
		fmt.Println(Version)
	},
}

func init() {
	versionCmd.Flags().BoolVar(&versionJSON, "json", false, "Output version info as JSON")
	rootCmd.AddCommand(versionCmd)
}
