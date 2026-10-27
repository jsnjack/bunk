// cmd_ssh.go - "bunk ssh", ssh that lets agent hooks on the remote host
// report to the pane it runs in (docs/remote-agent-status.md).
package main

import (
	"bunk/internal/notify"
	"os"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(sshCmd)
}

var sshCmd = &cobra.Command{
	Use:   "ssh [ssh options] destination [command]",
	Short: "Run ssh so agent hooks on the remote host report to this pane",
	Long: `Runs ssh. In a bunk pane, an interactive login also gets the pane's
BUNK_NOTIFY_SESSION in the remote shell, so agent hooks there report to
this pane. With a command, or options such as -N, -W or -T, ssh runs
unchanged.`,
	// ssh's own options pass through untouched.
	DisableFlagParsing: true,
	SilenceErrors:      true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if code := notify.RunSSH(args, os.Environ(), os.Stderr); code != 0 {
			return exitStatus(code)
		}
		return nil
	},
}
