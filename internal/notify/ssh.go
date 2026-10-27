package notify

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// sshUsage is the help text of "bunk ssh"; %[1]s is the program name.
const sshUsage = `usage: %[1]s ssh [ssh options] destination [command]

Runs ssh. In a bunk pane, an interactive login gets the pane's
BUNK_NOTIFY_SESSION in the remote shell, so agent hooks there report to
this pane (docs/remote-agent-status.md). With a command, or options
such as -N, -W or -T, ssh runs unchanged.
`

// sshValueFlags are ssh's options that take a value (ssh.c's getopt
// string), as in "-p 22" or "-p22".
const sshValueFlags = "BDEFIJLOPQRSWbceilmopw"

// sshAsIs are ssh options after which bunk ssh adds nothing: no session
// (-N, -W, -O, -G, -V, -Q), no terminal (-T), a subsystem (-s) or a
// background session (-f).
const sshAsIs = "NWOGVQTsf"

// RunSSH runs "bunk ssh": ssh with args, replacing this process. Inside a
// bunk pane an interactive login also exports the pane's token in the
// remote shell (sshArgs). It returns only when ssh cannot start, with exit
// status 1.
func RunSSH(args, env []string, stderr io.Writer) int {
	prog := filepath.Base(os.Args[0])
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		write(os.Stdout, fmt.Sprintf(sshUsage, prog))
		return 0
	}
	path, err := exec.LookPath("ssh")
	if err != nil {
		write(stderr, prog+" ssh: "+err.Error()+"\n")
		return exitError
	}
	argv := sshArgs(args, lookupEnv(env, EnvSession), func(opts []string, dest string) bool {
		return sshConfigRunsCommand(path, opts, dest)
	})
	slog.Debug("ssh: run", "argv", argv)
	err = syscall.Exec(path, append([]string{"ssh"}, argv...), env)
	write(stderr, prog+" ssh: "+err.Error()+"\n")
	return exitError
}

// sshArgs returns ssh's arguments for "bunk ssh args". With a token, an
// interactive login (a destination, no command, none of sshAsIs) gets -t
// and a remote command that exports the token and starts the login shell.
// Options after the destination move before it, which ssh reads the same.
// configured reports whether the ssh config already sets RemoteCommand or
// a session type for the destination; then args stay as they are.
func sshArgs(args []string, token string, configured func(opts []string, dest string) bool) []string {
	if token == "" || !isHex(token) {
		return args
	}
	opts, dest, command, ok := splitSSHArgs(args)
	if !ok || dest == "" || len(command) > 0 || hasSSHFlag(opts, sshAsIs) || configured(opts, dest) {
		return args
	}
	// sh runs the export whatever the login shell is (fish and csh have no
	// export), then starts the login shell as a login shell.
	remote := "env " + EnvSession + "=" + token + ` sh -c 'exec "${SHELL:-/bin/sh}" -l'`
	out := make([]string, 0, len(opts)+3)
	if !hasSSHFlag(opts, "t") {
		out = append(out, "-t")
	}
	out = append(out, opts...)
	return append(out, dest, remote)
}

// splitSSHArgs splits ssh's arguments into options, the destination and the
// remote command. ssh reads options after the destination too, until the
// first word that is not one. ok is false when an option lacks its value.
func splitSSHArgs(args []string) (opts []string, dest string, command []string, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if dest == "" && i+1 < len(args) {
				dest, i = args[i+1], i+1
			}
			return opts, dest, args[i+1:], true
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			if dest != "" {
				return opts, dest, args[i:], true
			}
			dest = a
			continue
		}
		opts = append(opts, a)
		for j := 1; j < len(a); j++ {
			if strings.IndexByte(sshValueFlags, a[j]) < 0 {
				continue
			}
			if j == len(a)-1 { // the value is the next argument
				if i+1 >= len(args) {
					return nil, "", nil, false
				}
				i++
				opts = append(opts, args[i])
			}
			break
		}
	}
	return opts, dest, nil, true
}

// hasSSHFlag reports whether opts, as splitSSHArgs returns them, set any
// of the flags in flags.
func hasSSHFlag(opts []string, flags string) bool {
	for i := 0; i < len(opts); i++ {
		a := opts[i]
		if !strings.HasPrefix(a, "-") {
			continue
		}
		for j := 1; j < len(a); j++ {
			if strings.IndexByte(flags, a[j]) >= 0 {
				return true
			}
			if strings.IndexByte(sshValueFlags, a[j]) >= 0 {
				if j == len(a)-1 {
					i++ // skip the value
				}
				break
			}
		}
	}
	return false
}

// sshConfigRunsCommand asks ssh (ssh -G) whether the configuration sets a
// RemoteCommand or a session type other than the default for dest. When
// ssh -G fails it reports true, so bunk ssh changes nothing.
func sshConfigRunsCommand(path string, opts []string, dest string) bool {
	out, err := exec.Command(path, append(append([]string{"-G"}, opts...), dest)...).Output()
	if err != nil {
		slog.Debug("ssh: ssh -G", "err", err)
		return true
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		key, value, _ := strings.Cut(sc.Text(), " ")
		switch {
		case key == "remotecommand" && value != "" && value != "none":
			return true
		case key == "sessiontype" && value != "default":
			return true
		}
	}
	return false
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return s != ""
}
