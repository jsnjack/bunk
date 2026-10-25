// cmd_notify.go - "bunk notify", the CLI agent hooks run to report their
// state to the bunk session they run in (docs/agent-hooks.md).
package main

import (
	"bunk/internal/notify"
	"os"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(notifyCmd)
}

var notifyCmd = &cobra.Command{
	Use:   "notify <event> [message | -]",
	Short: "Report an agent's state to the bunk pane it runs in (for agent hooks)",
	Long: `Reports an agent's state to the bunk pane it runs in, from an agent's hooks.
Events: working, idle, needs-you, done, failed, clear.
"bunk notify hooks <agent>" prints hook snippets for claude, codex, copilot
and gemini. Outside bunk it does nothing and exits 0. It never exits 2,
which agents read as "block this action".`,
	// Messages, "-" and Codex's JSON payload pass through untouched.
	DisableFlagParsing: true,
	Run: func(cmd *cobra.Command, args []string) {
		if code := notify.RunCLI(args, os.Environ(), os.Stdin); code != 0 {
			os.Exit(code)
		}
	},
}
