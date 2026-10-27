package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"bunk/internal/notify"
	"bunk/internal/session"
)

// executeRoot runs the root command in-process with args and returns what
// it printed. Its RunE fails the test: these args must never open GTK.
func executeRoot(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	runE := rootCmd.RunE
	rootCmd.RunE = func(*cobra.Command, []string) error {
		t.Errorf("bunk-gui %q started the GUI", args)
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
		for _, name := range []string{"version", "help"} {
			if err := rootCmd.Flags().Set(name, "false"); err != nil {
				t.Error(err)
			}
		}
	})
	err = rootCmd.Execute()
	return out.String(), errOut.String(), err
}

// shorthands lists every flag shorthand of cmd and its subcommands, the
// help and version flags cobra adds included, as "command -x (--name)".
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

// TestRootCommandConventions checks bunk-gui's command line against
// AGENTS.go.md without starting GTK: --version without a short alias,
// persistent --debug/-d, --trace and --config/-c, no other short alias
// besides cobra's -h, test flags hidden, and notify's arguments untouched.
func TestRootCommandConventions(t *testing.T) {
	t.Run("--version prints the version", func(t *testing.T) {
		out, _, err := executeRoot(t, "--version")
		if want := "bunk-gui version " + Version + "\n"; err != nil || out != want {
			t.Fatalf("output %q, err %v; want %q", out, err, want)
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
	t.Run("--config defaults to the user config dir", func(t *testing.T) {
		dir, err := os.UserConfigDir()
		if err != nil {
			t.Skip(err)
		}
		want := filepath.Join(dir, "bunk-gui", "config.toml")
		if got := rootCmd.PersistentFlags().Lookup("config").DefValue; got != want {
			t.Fatalf("--config default %q, want %q", got, want)
		}
	})
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
	t.Run("--command replaces -e", func(t *testing.T) {
		if rootCmd.Flags().Lookup("command") == nil || rootCmd.Flags().Lookup("e") != nil {
			t.Fatal("want --command and no -e")
		}
	})
	t.Run("test flags are hidden", func(t *testing.T) {
		want := []string{"agent-home", "alloc-probe", "do", "dump", "dump-size", "maximize-after", "report", "verify", "wait-focus"}
		var hidden []string
		rootCmd.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Hidden {
				hidden = append(hidden, f.Name)
			}
		})
		slices.Sort(hidden)
		if !slices.Equal(hidden, want) {
			t.Fatalf("hidden flags %q, want %q", hidden, want)
		}
		help, _, err := executeRoot(t, "--help")
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range want {
			if strings.Contains(help, "--"+name+" ") {
				t.Errorf("--help lists --%s:\n%s", name, help)
			}
		}
		if !strings.Contains(help, "--command CMD") || !strings.Contains(help, "--trace") {
			t.Errorf("--help misses --command or --trace:\n%s", help)
		}
	})
	t.Run("notify passes its arguments through", func(t *testing.T) {
		if !notifyCmd.DisableFlagParsing || !notifyCmd.SilenceErrors || notifyCmd.RunE == nil {
			t.Fatal("notify must disable flag parsing, leave error messages to RunCLI and use RunE")
		}
	})
}

// TestNotifyCLI runs "bunk-gui notify" as the agent hooks do and checks
// its exit codes, output and the report that reaches the server.
func TestNotifyCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("builds bunk-gui; skipped with -short")
	}
	bin := guiBinary(t)
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
	t.Setenv("XDG_RUNTIME_DIR", rt)
	srv, err := notify.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Error(err)
		}
	})
	got := make(chan report, 4)
	srv.Register("tok", reportTarget(got))
	inside := []string{session.EnvNotifySocket + "=" + srv.Path(), session.EnvNotifySession + "=tok"}
	for _, tc := range []struct {
		name   string
		args   []string
		env    []string
		code   int
		stdout string // substring; "" means none
		stderr string // substring; "" means none
		want   *report
	}{
		{name: "outside bunk", args: []string{"notify", "done", "hi"}},
		{name: "report", args: []string{"notify", "needs-you", "Allow", "npm", "install?"}, env: inside,
			want: &report{session.EventNeedsYou, "Allow npm install?"}},
		{name: "words that look like flags", args: []string{"notify", "failed", "--config", "-d", "--trace"}, env: inside,
			want: &report{session.EventFailed, "--config -d --trace"}},
		{name: "unknown event", args: []string{"notify", "waiting"}, env: inside, code: 1, stderr: `bunk-gui notify: unknown event "waiting"`},
		{name: "no event", args: []string{"notify"}, code: 1, stderr: "usage: bunk-gui notify"},
		{name: "help", args: []string{"notify", "--help"}, stdout: "usage: bunk-gui notify"},
		{name: "hook snippet", args: []string{"notify", "hooks", "claude"}, stdout: `notify needs-you -`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bin, tc.args...)
			// A -race build otherwise sleeps 1 s at exit.
			cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GORACE=atexit_sleep_ms=0"}, tc.env...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := cmd.ProcessState.ExitCode()
			if code != tc.code || (err != nil && code == 0) {
				t.Fatalf("exit %d (%v), want %d; stderr %q", code, err, tc.code, stderr.String())
			}
			for _, out := range []struct{ name, got, want string }{
				{"stdout", stdout.String(), tc.stdout}, {"stderr", stderr.String(), tc.stderr},
			} {
				if (out.want == "") != (out.got == "") || !strings.Contains(out.got, out.want) {
					t.Fatalf("%s = %q, want %q", out.name, out.got, out.want)
				}
			}
			if tc.want == nil {
				return
			}
			select {
			case r := <-got:
				if r != *tc.want {
					t.Fatalf("report %+v, want %+v", r, *tc.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no report")
			}
		})
	}
}

type report struct {
	ev   session.Event
	text string
}

// reportTarget is a notify.Target that sends each report to its channel.
type reportTarget chan report

func (r reportTarget) Notify(ev session.Event, text string) { r <- report{ev, text} }

// useTracePath points --trace at a file of the test's own and restores the
// default logger afterwards.
func useTracePath(t *testing.T) string {
	t.Helper()
	prevPath, prevLogger := tracePath, slog.Default()
	tracePath = filepath.Join(t.TempDir(), "bunk-gui.log")
	t.Cleanup(func() {
		tracePath = prevPath
		slog.SetDefault(prevLogger)
	})
	return tracePath
}

// TestLogLevels checks what each combination of --debug and --trace
// writes to stderr and to the trace log.
func TestLogLevels(t *testing.T) {
	for _, tc := range []struct {
		name         string
		debug, trace bool
		stderr       []string // levels that reach stderr
		traceLog     []string // levels in the trace log; nil: no trace log
	}{
		{"no flags", false, false, []string{"WARN", "ERROR"}, nil},
		{"debug", true, false, []string{"DEBUG", "INFO", "WARN", "ERROR"}, nil},
		{"trace", false, true, []string{"WARN", "ERROR"}, []string{"DEBUG-4", "DEBUG", "INFO", "WARN", "ERROR"}},
		{"debug and trace", true, true, []string{"DEBUG", "INFO", "WARN", "ERROR"}, []string{"DEBUG-4", "DEBUG", "INFO", "WARN", "ERROR"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := useTracePath(t)
			var stderr bytes.Buffer
			logs, err := initLogger(&stderr, tc.debug, tc.trace)
			if err != nil {
				t.Fatal(err)
			}
			slog.Log(context.Background(), LevelTrace, "m")
			slog.Debug("m")
			slog.Info("m")
			slog.Warn("m")
			slog.Error("m")
			logs.close()
			if got := levels(stderr.String()); !slices.Equal(got, tc.stderr) {
				t.Errorf("stderr levels %q, want %q", got, tc.stderr)
			}
			data, err := os.ReadFile(path)
			switch {
			case tc.traceLog == nil && err == nil:
				t.Errorf("trace log written without --trace:\n%s", data)
			case tc.traceLog == nil:
			case err != nil:
				t.Fatal(err)
			default:
				if got := levels(string(data)); !slices.Equal(got, tc.traceLog) {
					t.Errorf("trace log levels %q, want %q", got, tc.traceLog)
				}
			}
		})
	}
}

// levels lists the level of each text-handler line.
func levels(log string) []string {
	var got []string
	for _, m := range regexp.MustCompile(`level=(\S+)`).FindAllStringSubmatch(log, -1) {
		got = append(got, m[1])
	}
	return got
}

// TestTraceLogTruncates checks that --trace starts the trace log afresh.
func TestTraceLogTruncates(t *testing.T) {
	path := useTracePath(t)
	if err := os.WriteFile(path, []byte("an earlier run\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logs, err := initLogger(&bytes.Buffer{}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	slog.Debug("this run")
	logs.close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "an earlier run") || !strings.Contains(string(data), "this run") {
		t.Fatalf("trace log:\n%s", data)
	}
}

// TestLogAlwaysIgnoresLevel checks that the listening line reaches stderr
// at the default level, which hides Info, and the trace log with --trace.
func TestLogAlwaysIgnoresLevel(t *testing.T) {
	for _, trace := range []bool{false, true} {
		t.Run("trace="+map[bool]string{false: "off", true: "on"}[trace], func(t *testing.T) {
			path := useTracePath(t)
			var stderr bytes.Buffer
			logs, err := initLogger(&stderr, false, trace)
			if err != nil {
				t.Fatal(err)
			}
			slog.Info("hidden on stderr")
			logs.always("Listening on /run/x.sock")
			logs.close()
			const line = `level=INFO msg="Listening on /run/x.sock"`
			if !strings.Contains(stderr.String(), line) || strings.Contains(stderr.String(), "hidden") {
				t.Errorf("stderr:\n%s", stderr.String())
			}
			if !trace {
				return
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), line) {
				t.Errorf("trace log:\n%s", data)
			}
		})
	}
}

// TestGUIListeningLine checks the real binary: without --debug, stderr
// holds the notify socket's "Listening on" line and no other Info or
// Debug line.
func TestGUIListeningLine(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{args: []string{"--size", "40x10"}, script: []string{"quit"}})
	listening := regexp.MustCompile(`level=INFO msg="Listening on /\S+/bunk/notify-\d+\.sock"`)
	if !listening.MatchString(g.stderr) {
		t.Errorf("stderr has no %q line:\n%s", listening, g.stderr)
	}
	for _, line := range strings.Split(g.stderr, "\n") {
		if (strings.Contains(line, "level=INFO") || strings.Contains(line, "level=DEBUG")) && !listening.MatchString(line) {
			t.Errorf("stderr line below the default level: %s", line)
		}
	}
}
