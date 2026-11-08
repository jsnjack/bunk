// cmd_present.go - "bunk present", which shows a Markdown or HTML document
// in bunk-gui's side panel, else in a web browser (docs/present.md).
package main

import (
	"bunk/internal/notify"
	"os"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(presentCmd)
}

var presentCmd = &cobra.Command{
	Use:   "present FILE",
	Short: "Show a Markdown or HTML file in bunk-gui's side panel, else in a browser",
	Long: `Shows a Markdown or HTML file to the user: in a bunk-gui pane in the side
panel of the pane's tab, in bunk's terminal UI and elsewhere in the web
browser. "bunk present --help" describes the formats, the agent hook
(--hook) and the skill for agents (skill, status, install).`,
	// FILE, --hook and its "-" pass through untouched.
	DisableFlagParsing: true,
	// RunPresent prints its own errors, on stderr.
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if code := notify.RunPresent(args, os.Environ(), os.Stdin); code != 0 {
			return exitStatus(code)
		}
		return nil
	},
}
