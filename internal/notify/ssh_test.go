package notify

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSSHArgs(t *testing.T) {
	t.Parallel()
	const token = "0123456789abcdef0123456789abcdef"
	login := "env " + EnvSession + "=" + token + ` sh -c 'exec "${SHELL:-/bin/sh}" -l'`
	for _, tc := range []struct {
		name       string
		args       []string
		token      string
		configured bool
		want       []string
	}{
		{"outside bunk", []string{"build01"}, "", false, []string{"build01"}},
		{"login", []string{"build01"}, token, false, []string{"-t", "build01", login}},
		{"user and port", []string{"-p", "2222", "me@build01"}, token, false, []string{"-t", "-p", "2222", "me@build01", login}},
		{"joined value", []string{"-p2222", "-oServerAliveInterval=30", "build01"}, token, false,
			[]string{"-t", "-p2222", "-oServerAliveInterval=30", "build01", login}},
		{"options after the destination", []string{"build01", "-v", "-L", "8080:localhost:80"}, token, false,
			[]string{"-t", "-v", "-L", "8080:localhost:80", "build01", login}},
		{"-t already", []string{"-t", "build01"}, token, false, []string{"-t", "build01", login}},
		{"flags together", []string{"-At", "build01"}, token, false, []string{"-At", "build01", login}},
		{"jump host", []string{"-J", "bastion", "build01"}, token, false, []string{"-t", "-J", "bastion", "build01", login}},
		{"a command", []string{"build01", "claude"}, token, false, []string{"build01", "claude"}},
		{"a command after --", []string{"--", "build01", "ls", "-l"}, token, false, []string{"--", "build01", "ls", "-l"}},
		{"no session", []string{"-N", "-L", "5432:db:5432", "build01"}, token, false, []string{"-N", "-L", "5432:db:5432", "build01"}},
		{"no terminal", []string{"-T", "build01"}, token, false, []string{"-T", "build01"}},
		{"stdio forward", []string{"-W", "db:22", "bastion"}, token, false, []string{"-W", "db:22", "bastion"}},
		{"version", []string{"-V"}, token, false, []string{"-V"}},
		{"flag inside a value", []string{"-o", "-N", "build01"}, token, false, []string{"-t", "-o", "-N", "build01", login}},
		{"no destination", []string{"-v"}, token, false, []string{"-v"}},
		{"missing value", []string{"build01", "-p"}, token, false, []string{"build01", "-p"}},
		{"RemoteCommand in the config", []string{"build01"}, token, true, []string{"build01"}},
		{"token that needs quoting", []string{"build01"}, "x'; rm -rf ~", false, []string{"build01"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sshArgs(tc.args, tc.token, func([]string, string) bool { return tc.configured })
			if !slices.Equal(got, tc.want) {
				t.Fatalf("sshArgs(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// TestSSHConfigRunsCommand reads ssh -G output from a fake ssh. It does
// not run in parallel: a fork elsewhere while a fake is open for writing
// makes running it fail with ETXTBSY.
func TestSSHConfigRunsCommand(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		want   bool
	}{
		{"plain host", "printf 'user me\\nsessiontype default\\n'", false},
		{"RemoteCommand", "printf 'remotecommand tmux attach\\nsessiontype default\\n'", true},
		{"SessionType none", "printf 'sessiontype none\\n'", true},
		{"ssh -G fails", "exit 255", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := filepath.Join(t.TempDir(), "ssh")
			if err := os.WriteFile(fake, []byte("#!/bin/sh\n"+tc.script+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			if got := sshConfigRunsCommand(fake, []string{"-p", "22"}, "build01"); got != tc.want {
				t.Fatalf("sshConfigRunsCommand = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAncestors walks up from this test process through /proc.
func TestAncestors(t *testing.T) {
	t.Parallel()
	got := ancestors(os.Getpid())
	if len(got) == 0 || got[0] != os.Getpid() || (len(got) > 1 && got[1] != os.Getppid()) || len(got) > maxAncestors {
		t.Fatalf("ancestors(self) = %v, want %d then %d", got, os.Getpid(), os.Getppid())
	}
	if got := parentPID(os.Getpid()); got != os.Getppid() {
		t.Fatalf("parentPID = %d, want %d", got, os.Getppid())
	}
	if got := parentPID(-5); got != 0 {
		t.Fatalf("parentPID(-5) = %d", got)
	}
}

func TestIsTerminalPath(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/dev/null", false},
		{"/dev/ptmx", false},
		{"/dev/pts/999999", false}, // no such device
		{"/tmp/pts/1", false},
		{"pipe:[1234]", false},
	} {
		if got := isTerminalPath(tc.path); got != tc.want {
			t.Errorf("isTerminalPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
