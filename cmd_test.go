package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// executeRoot runs the root command in-process with args and returns what
// it printed. Its RunE fails the test: these args must never start the TUI.
func executeRoot(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	runE := rootCmd.RunE
	rootCmd.RunE = func(*cobra.Command, []string) error {
		t.Errorf("bunk %q started the TUI", args)
		return nil
	}
	var out, errOut bytes.Buffer
	rootCmd.SetArgs(args)
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	t.Cleanup(func() {
		rootCmd.RunE = runE
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		if err := rootCmd.Flags().Set("version", "false"); err != nil {
			t.Error(err)
		}
	})
	err = rootCmd.Execute()
	return out.String(), errOut.String(), err
}

// shorthands lists every flag shorthand of cmd and its subcommands, the
// help and version flags cobra adds included, as "command -x".
func shorthands(cmd *cobra.Command) []string {
	cmd.InitDefaultHelpFlag()
	cmd.InitDefaultVersionFlag()
	var got []string
	seen := map[*pflag.Flag]bool{}
	add := func(f *pflag.Flag) {
		if f.Shorthand != "" && !seen[f] {
			seen[f] = true
			got = append(got, cmd.Name()+" -"+f.Shorthand+" (--"+f.Name+")")
		}
	}
	cmd.Flags().VisitAll(add)
	cmd.PersistentFlags().VisitAll(add)
	for _, sub := range cmd.Commands() {
		got = append(got, shorthands(sub)...)
	}
	return got
}

// TestRootCommandConventions checks bunk's command line against
// AGENTS.go.md: --version without a short alias, persistent --debug/-d,
// --trace and --config/-c, and no other short alias besides cobra's -h.
func TestRootCommandConventions(t *testing.T) {
	t.Run("--version prints the version", func(t *testing.T) {
		out, _, err := executeRoot(t, "--version")
		if err != nil || out != "bunk version "+Version+"\n" {
			t.Fatalf("output %q, err %v; want %q", out, err, "bunk version "+Version+"\n")
		}
	})
	t.Run("-v is not a flag", func(t *testing.T) {
		_, stderr, err := executeRoot(t, "-v")
		if err == nil || !strings.Contains(stderr, "unknown shorthand flag: 'v'") {
			t.Fatalf("err %v, stderr %q; want an unknown shorthand flag error", err, stderr)
		}
	})
	for _, tc := range []struct{ name, shorthand string }{
		{"debug", "d"},
		{"trace", ""},
		{"config", "c"},
	} {
		t.Run("persistent --"+tc.name, func(t *testing.T) {
			f := rootCmd.PersistentFlags().Lookup(tc.name)
			if f == nil {
				t.Fatalf("no persistent flag --%s", tc.name)
			}
			if f.Shorthand != tc.shorthand {
				t.Fatalf("--%s shorthand %q, want %q", tc.name, f.Shorthand, tc.shorthand)
			}
		})
	}
	t.Run("short aliases only for -d, -c and -h", func(t *testing.T) {
		allowed := map[string]bool{"d": true, "c": true, "h": true}
		for _, s := range shorthands(rootCmd) {
			if !allowed[strings.Fields(s)[1][1:]] {
				t.Errorf("short alias %s", s)
			}
		}
	})
	t.Run("-h is help", func(t *testing.T) {
		if f := rootCmd.Flags().ShorthandLookup("h"); f == nil || f.Name != "help" {
			t.Fatalf("-h is %v, want --help", f)
		}
	})
	t.Run("notify passes its arguments through", func(t *testing.T) {
		if !notifyCmd.DisableFlagParsing || !notifyCmd.SilenceErrors || notifyCmd.RunE == nil {
			t.Fatal("notify must disable flag parsing, leave error messages to RunCLI and use RunE")
		}
	})
}

// TestLogAlwaysIgnoresLevel checks that the listening line reaches the log
// file at a level that hides Info.
func TestLogAlwaysIgnoresLevel(t *testing.T) {
	prev := L
	t.Cleanup(func() { L = prev })
	path := filepath.Join(t.TempDir(), "bunk.log")
	cleanup := initLogger(path, "warn")
	L.Info("hidden at warn")
	logAlways("Listening on /run/x.sock")
	cleanup()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `msg="Listening on /run/x.sock"`) || strings.Contains(string(data), "hidden at warn") {
		t.Fatalf("log file:\n%s", data)
	}
}

// TestTUIListeningLogsToTraceOnly runs bunk on a PTY and checks where the
// notify socket's "Listening on" line goes: into the log file with
// --trace, never to stderr, and nowhere without a flag (bunk opens no log
// file then).
func TestTUIListeningLogsToTraceOnly(t *testing.T) {
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	bunk := filepath.Join(bunkOnPath(t), "bunk")
	for _, tc := range []struct {
		name    string
		args    []string
		wantLog bool
	}{
		{"trace", []string{"--trace"}, true},
		{"no flags", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			// Socket paths must stay under 108 bytes.
			rt, err := os.MkdirTemp("", "bn")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(rt); err != nil {
					t.Error(err)
				}
			})
			logPath := filepath.Join(dir, "bunk.log")
			cfg := filepath.Join(dir, "config.toml")
			if err := os.WriteFile(cfg, []byte("log_file = "+strconv.Quote(logPath)+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			ptmx, tty, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
				t.Fatal(err)
			}
			// bunk blocks once the PTY buffer fills, so drain it.
			drained := make(chan error, 1)
			go func() {
				_, err := io.Copy(io.Discard, ptmx)
				drained <- err
			}()
			cmd := exec.Command(bunk, append([]string{"--config", cfg}, tc.args...)...)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TERM=xterm-256color",
				"SHELL=/bin/sh", "XDG_RUNTIME_DIR=" + rt}
			var stderr bytes.Buffer
			cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, &stderr
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			socket := filepath.Join(rt, "bunk", "notify-"+strconv.Itoa(cmd.Process.Pid)+".sock")
			deadline := time.Now().Add(15 * time.Second)
			for {
				if _, err := os.Stat(socket); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Errorf("bunk did not listen on %s within 15 s", socket)
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil {
				t.Error("bunk exited cleanly after SIGKILL")
			}
			for _, f := range []io.Closer{tty, ptmx} {
				if err := f.Close(); err != nil {
					t.Error(err)
				}
			}
			// EIO: the last slave descriptor closed; ErrClosed: the master did.
			if err := <-drained; err != nil && !errors.Is(err, syscall.EIO) && !errors.Is(err, os.ErrClosed) {
				t.Errorf("read PTY: %v", err)
			}
			if stderr.Len() != 0 {
				t.Errorf("bunk wrote to stderr: %q", stderr.String())
			}
			data, err := os.ReadFile(logPath)
			switch {
			case !tc.wantLog && err == nil:
				t.Errorf("bunk wrote %s without --trace:\n%s", logPath, data)
			case !tc.wantLog:
			case err != nil:
				t.Fatal(err)
			default:
				want := regexp.MustCompile(`level=INFO msg="Listening on ` + regexp.QuoteMeta(socket) + `"`)
				if !want.Match(data) {
					t.Errorf("log file has no %q line:\n%s", want, data)
				}
			}
		})
	}
}
