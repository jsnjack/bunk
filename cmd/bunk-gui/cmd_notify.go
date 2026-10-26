package main

import (
	"os"

	"github.com/spf13/cobra"

	"bunk/internal/notify"
)

func init() {
	rootCmd.AddCommand(notifyCmd)
}

// notifyCmd is the CLI agent hooks run to report their state to the
// bunk-gui pane they run in (docs/agent-hooks.md). It opens no window.
var notifyCmd = &cobra.Command{
	Use:   "notify <event> [message | -]",
	Short: "Report an agent's state to the bunk-gui pane it runs in (for agent hooks)",
	Long: `Reports an agent's state to the bunk-gui pane it runs in, from an agent's hooks.
Events: working, idle, needs-you, done, failed, clear.
"bunk-gui notify hooks <agent>" prints hook snippets for claude, codex, copilot
and gemini. Outside bunk-gui it does nothing and exits 0. It never exits 2,
which agents read as "block this action".`,
	// Messages, "-" and Codex's JSON payload pass through untouched.
	DisableFlagParsing: true,
	// RunCLI prints its own errors, on stderr.
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if code := notify.RunCLI(args, os.Environ(), os.Stdin); code != 0 {
			return exitStatus(code)
		}
		return nil
	},
}
