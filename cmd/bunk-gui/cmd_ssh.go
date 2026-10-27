package main

import (
	"os"

	"github.com/spf13/cobra"

	"bunk/internal/notify"
)

func init() {
	rootCmd.AddCommand(sshCmd)
}

// sshCmd is "bunk-gui ssh", ssh that lets agent hooks on the remote host
// report to the bunk-gui tab it runs in (docs/remote-agent-status.md). It
// opens no window.
var sshCmd = &cobra.Command{
	Use:   "ssh [ssh options] destination [command]",
	Short: "Run ssh so agent hooks on the remote host report to this tab",
	Long: `Runs ssh. In a bunk-gui tab, an interactive login also gets the tab's
BUNK_NOTIFY_SESSION in the remote shell, so agent hooks there report to
this tab. With a command, or options such as -N, -W or -T, ssh runs
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
