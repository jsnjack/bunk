package notify

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"bunk/internal/notify/wire"
	"bunk/internal/session"
)

// Hook inputs as the agents' documentation shows them.
const (
	claudeNotification = `{"session_id":"abc123","transcript_path":"/x.jsonl","cwd":"/w","hook_event_name":"Notification",` +
		`"message":"Claude needs your permission to use Bash","title":"Permission needed","notification_type":"permission_prompt"}`
	claudeStop        = `{"session_id":"abc123","hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"Fixed the race; all tests pass.","background_tasks":[]}`
	claudeStopFailure = `{"session_id":"abc123","hook_event_name":"StopFailure","error":"rate_limit","error_details":"429 Too Many Requests","last_assistant_message":"API Error: Rate limit reached"}`
	codexNotify       = `{"type":"agent-turn-complete","thread-id":"b5f6c1c2","turn-id":"12345","cwd":"/w","input-messages":["Rename foo to bar"],"last-assistant-message":"Rename complete."}`
	geminiAfterAgent  = `{"session_id":"s","hook_event_name":"AfterAgent","prompt":"fix it","prompt_response":"Done: fixed it.","stop_hook_active":false}`
	copilotError      = `{"sessionId":"s","timestamp":1,"cwd":"/w","error":{"message":"Network timeout","name":"TimeoutError"},"recoverable":false}`
	codexPermission   = `{"session_id":"s","hook_event_name":"PermissionRequest","turn_id":"t","tool_name":"Bash","tool_input":{"command":"rm -rf build"}}`
)

func TestCLI(t *testing.T) {
	srv, token, target := startServer(t)
	inside := []string{"HOME=/home/u", EnvSocket + "=" + srv.Path(), EnvSession + "=" + token}

	gone := filepath.Join(runtimeDir(t), "gone.sock")
	refused := filepath.Join(runtimeDir(t), "refused.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: refused, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	denied := filepath.Join(runtimeDir(t), "denied.sock")
	dl, err := net.ListenUnix("unix", &net.UnixAddr{Name: denied, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := dl.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := os.Chmod(denied, 0); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		args       []string
		env        []string
		stdin      string
		code       int
		stdout     string // substring; "" means no output at all
		stderr     string // substring; "" means no output at all
		want       *report
		otherToken bool
		inBand     *wire.Report // the report written to the terminal
		noTTY      bool         // no terminal leads to the agent
	}{
		{name: "no arguments", code: 1, stderr: "usage: bunk notify <event>"},
		{name: "help", args: []string{"help"}, stdout: "usage: bunk notify <event>"},
		{name: "unknown event", args: []string{"waiting"}, env: inside, code: 1, stderr: `bunk notify: unknown event "waiting"`},
		{name: "unknown event outside bunk", args: []string{"waiting"}, code: 1, stderr: "unknown event"},
		{name: "outside bunk", args: []string{"needs-you", "-"}, env: []string{"HOME=/home/u"}, stdin: claudeNotification},
		{name: "socket without session", args: []string{"working"}, env: []string{EnvSocket + "=" + srv.Path()}},
		{name: "session without socket", args: []string{"working"}, env: []string{EnvSession + "=" + token},
			inBand: &wire.Report{V: 1, Session: token, Event: "working", Agent: "claude", Repo: "bunk", Branch: "main", Cwd: "/home/u/bunk"}},
		{name: "empty values", args: []string{"working"}, env: []string{EnvSocket + "=", EnvSession + "="}},
		{name: "front end exited", args: []string{"done"}, env: []string{EnvSocket + "=" + gone, EnvSession + "=" + token},
			inBand: &wire.Report{V: 1, Session: token, Event: "done", Agent: "claude", Repo: "bunk", Branch: "main", Cwd: "/home/u/bunk"}},
		{name: "front end crashed", args: []string{"done"}, env: []string{EnvSocket + "=" + refused, EnvSession + "=" + token},
			inBand: &wire.Report{V: 1, Session: token, Event: "done", Agent: "claude", Repo: "bunk", Branch: "main", Cwd: "/home/u/bunk"}},
		{name: "over ssh", args: []string{"needs-you", "-"}, env: []string{envSSH + "=10.0.0.2 50000 10.0.0.1 22"}, stdin: claudeNotification,
			inBand: &wire.Report{V: 1, Event: "needs-you", Message: "Claude needs your permission to use Bash", Agent: "claude",
				Host: "build01", Repo: "bunk", Branch: "main", Cwd: "/home/u/bunk"}},
		{name: "over ssh in tmux", args: []string{"clear"}, env: []string{envSSH + "=x", "TMUX=/tmp/tmux-1000/default,1,0"},
			inBand: &wire.Report{V: 1, Event: "clear", Agent: "claude", Host: "build01", Repo: "bunk", Branch: "main", Cwd: "/home/u/bunk"}},
		{name: "over ssh with a token", args: []string{"idle"}, env: []string{envSSH + "=x", EnvSession + "=" + token},
			inBand: &wire.Report{V: 1, Session: token, Event: "idle", Agent: "claude", Host: "build01", Repo: "bunk", Branch: "main", Cwd: "/home/u/bunk"}},
		{name: "over ssh without a terminal", args: []string{"done"}, env: []string{envSSH + "=x"}, noTTY: true},
		{name: "unknown event over ssh", args: []string{"waiting"}, env: []string{envSSH + "=x"}, code: 1, stderr: "unknown event"},
		{name: "socket not writable", args: []string{"done"}, env: []string{EnvSocket + "=" + denied, EnvSession + "=" + token}, code: 1, stderr: "permission denied"},
		{name: "oversized token", args: []string{"done"}, env: []string{EnvSocket + "=" + srv.Path(), EnvSession + "=" + strings.Repeat("a", wire.MaxToken+1)}, code: 1, stderr: "token"},
		{name: "working", args: []string{"working"}, env: inside, want: &report{session.EventWorking, ""}},
		{name: "idle", args: []string{"idle"}, env: inside, want: &report{session.EventIdle, ""}},
		{name: "clear", args: []string{"clear"}, env: inside, want: &report{session.EventClear, ""}},
		{name: "message words", args: []string{"needs-you", "Allow", "npm", "install?"}, env: inside, want: &report{session.EventNeedsYou, "Allow npm install?"}},
		{name: "last env value wins", args: []string{"idle"}, env: append([]string{EnvSession + "=stale"}, inside...), want: &report{session.EventIdle, ""}},
		{name: "Claude Notification on stdin", args: []string{"needs-you", "-"}, env: inside, stdin: claudeNotification,
			want: &report{session.EventNeedsYou, "Claude needs your permission to use Bash"}},
		{name: "Claude Stop on stdin", args: []string{"done", "-"}, env: inside, stdin: claudeStop, want: &report{session.EventDone, "Fixed the race; all tests pass."}},
		{name: "Claude StopFailure on stdin", args: []string{"failed", "-"}, env: inside, stdin: claudeStopFailure, want: &report{session.EventFailed, "API Error: Rate limit reached"}},
		{name: "Codex notify argument", args: []string{"done", codexNotify}, env: inside, want: &report{session.EventDone, "Rename complete."}},
		{name: "Gemini AfterAgent on stdin", args: []string{"done", "-"}, env: inside, stdin: geminiAfterAgent, want: &report{session.EventDone, "Done: fixed it."}},
		{name: "error object on stdin", args: []string{"failed", "-"}, env: inside, stdin: copilotError, want: &report{session.EventFailed, "Network timeout"}},
		{name: "hook JSON without text", args: []string{"needs-you", "-"}, env: inside, stdin: codexPermission, want: &report{session.EventNeedsYou, ""}},
		{name: "cut-off hook JSON", args: []string{"done", "-"}, env: inside, stdin: claudeStop[:40], want: &report{session.EventDone, ""}},
		{name: "plain text on stdin", args: []string{"failed", "-"}, env: inside, stdin: "make: *** [test] Error 1\n", want: &report{session.EventFailed, "make: *** [test] Error 1"}},
		{name: "list hook agents", args: []string{"hooks"}, stdout: "claude"},
		{name: "hooks for claude", args: []string{"hooks", "claude"}, stdout: `"UserPromptSubmit"`},
		{name: "hooks for an unknown agent", args: []string{"hooks", "emacs"}, code: 1, stderr: "copilot"},
		{name: "hooks with two agents", args: []string{"hooks", "claude", "codex"}, code: 1, stderr: "gemini"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tty := stubInBand(t, !tc.noTTY)
			var stdout, stderr bytes.Buffer
			code := runCLI("bunk", tc.args, tc.env, strings.NewReader(tc.stdin), &stdout, &stderr)
			if code != tc.code {
				t.Fatalf("exit %d, want %d (stderr %q)", code, tc.code, stderr.String())
			}
			for _, out := range []struct {
				name, got, want string
			}{{"stdout", stdout.String(), tc.stdout}, {"stderr", stderr.String(), tc.stderr}} {
				if (out.want == "") != (out.got == "") || !strings.Contains(out.got, out.want) {
					t.Fatalf("%s = %q, want %q", out.name, out.got, out.want)
				}
			}
			if tc.want != nil {
				if got := next(t, target); got != *tc.want {
					t.Fatalf("report %+v, want %+v", got, *tc.want)
				}
			}
			got, ok := decodeInBand(t, tty.Bytes())
			switch {
			case tc.inBand == nil && ok:
				t.Fatalf("wrote an in-band report %+v", got)
			case tc.inBand != nil && got != *tc.inBand:
				t.Fatalf("in-band report %+v, want %+v", got, *tc.inBand)
			}
			if wrapped := bytes.HasPrefix(tty.Bytes(), []byte("\x1bPtmux;")); wrapped != strings.Contains(strings.Join(tc.env, " "), "TMUX=") {
				t.Fatalf("tmux passthrough %v for %q", wrapped, tty.Bytes())
			}
			// Nothing else arrived: the next report is this sentinel.
			if err := Send(srv.Path(), Message{Session: token, Event: session.EventClear, Text: "sentinel"}); err != nil {
				t.Fatal(err)
			}
			if got := next(t, target); got != (report{session.EventClear, "sentinel"}) {
				t.Fatalf("unexpected report %+v", got)
			}
		})
	}
}

// TestCLIStdinStaysOpen: a hook whose stdin never closes still reports, with
// an empty message, after stdinTimeout.
func TestCLIStdinStaysOpen(t *testing.T) {
	srv, token, target := startServer(t)
	saved := stdinTimeout
	stdinTimeout = 100 * time.Millisecond
	defer func() { stdinTimeout = saved }()
	r, w := io.Pipe()
	defer func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
	}()
	start := time.Now()
	code := runCLI("bunk", []string{"needs-you", "-"}, []string{EnvSocket + "=" + srv.Path(), EnvSession + "=" + token}, r, io.Discard, io.Discard)
	if code != 0 || time.Since(start) > 2*time.Second {
		t.Fatalf("exit %d after %v", code, time.Since(start))
	}
	if got := next(t, target); got != (report{session.EventNeedsYou, ""}) {
		t.Fatalf("report %+v", got)
	}
}

// handlerCommands returns every hook command in a snippet, keyed by event.
func handlerCommands(t *testing.T, agent, snippet string) map[string]string {
	t.Helper()
	cmds := map[string]string{}
	switch agent {
	case "claude", "gemini":
		var cfg struct {
			Hooks map[string][]struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Name    string `json:"name"`
					Type    string `json:"type"`
					Command string `json:"command"`
					Timeout int    `json:"timeout"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal([]byte(snippet), &cfg); err != nil {
			t.Fatalf("%s snippet is not JSON: %v", agent, err)
		}
		for ev, groups := range cfg.Hooks {
			for _, g := range groups {
				for _, h := range g.Hooks {
					if h.Type != "command" || h.Timeout <= 0 || (agent == "gemini" && h.Name == "") {
						t.Errorf("%s %s: handler %+v", agent, ev, h)
					}
					cmds[ev] = h.Command
				}
			}
		}
	case "copilot":
		var cfg struct {
			Version int `json:"version"`
			Hooks   map[string][]struct {
				Type       string `json:"type"`
				Bash       string `json:"bash"`
				TimeoutSec int    `json:"timeoutSec"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal([]byte(snippet), &cfg); err != nil {
			t.Fatalf("copilot snippet is not JSON: %v", err)
		}
		if cfg.Version != 1 {
			t.Errorf("copilot snippet version %d", cfg.Version)
		}
		for ev, hs := range cfg.Hooks {
			for _, h := range hs {
				if h.Type != "command" || h.TimeoutSec <= 0 {
					t.Errorf("copilot %s: handler %+v", ev, h)
				}
				cmds[ev] = h.Bash
			}
		}
	case "codex":
		var cfg struct {
			Hooks map[string][]struct {
				Matcher string
				Hooks   []struct {
					Type    string
					Command string
					Timeout int
				}
			}
		}
		if _, err := toml.Decode(snippet, &cfg); err != nil {
			t.Fatalf("codex snippet is not TOML: %v", err)
		}
		for ev, groups := range cfg.Hooks {
			for _, g := range groups {
				for _, h := range g.Hooks {
					if h.Type != "command" || h.Timeout <= 0 {
						t.Errorf("codex %s: handler %+v", ev, h)
					}
					cmds[ev] = h.Command
				}
			}
		}
	}
	return cmds
}

// codexNotifyArgv returns the notify program in the Codex snippet's
// commented line, which is valid TOML once uncommented.
func codexNotifyArgv(t *testing.T, snippet string) []string {
	t.Helper()
	var cfg struct{ Notify []string }
	uncommented := strings.TrimPrefix(strings.Split(snippet, "\n")[3], "# ")
	if _, err := toml.Decode(uncommented, &cfg); err != nil || len(cfg.Notify) == 0 {
		t.Fatalf("codex notify line %q: %v, %q", uncommented, err, cfg.Notify)
	}
	return cfg.Notify
}

// TestHookSnippets parses each agent's snippet and runs every hook command
// with sh. Outside bunk it succeeds without running anything. Inside it runs
// "bunk notify" with the recipe's arguments, or "bunk-gui notify" when bunk
// is not on PATH. The Codex notify program passes Codex's JSON payload on.
func TestHookSnippets(t *testing.T) {
	t.Parallel()
	both, guiOnly := t.TempDir(), t.TempDir()
	out := filepath.Join(t.TempDir(), "calls")
	fake := "#!/bin/sh\nprintf '%s %s\\n' \"${0##*/}\" \"$*\" >> \"$OUT\"\n"
	for _, f := range []string{filepath.Join(both, "bunk"), filepath.Join(both, "bunk-gui"), filepath.Join(guiOnly, "bunk-gui")} {
		if err := os.WriteFile(f, []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	empty := t.TempDir()
	// run runs argv with env and returns what the fake binaries recorded.
	run := func(t *testing.T, argv, env []string) string {
		t.Helper()
		if err := os.WriteFile(out, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		c := exec.Command(argv[0], argv[1:]...)
		c.Env = append(env, "OUT="+out)
		c.Stdin = strings.NewReader(claudeStop)
		if b, err := c.CombinedOutput(); err != nil || len(b) > 0 {
			t.Fatalf("%q: %v, output %q", argv, err, b)
		}
		got, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return string(got)
	}
	for _, r := range recipes {
		t.Run(r.name, func(t *testing.T) {
			snippet, ok := HookSnippet(r.name)
			if !ok {
				t.Fatal("no snippet")
			}
			cmds := handlerCommands(t, r.name, snippet)
			if len(cmds) != len(r.hooks) {
				t.Fatalf("%d hook events in the snippet, want %d: %v", len(cmds), len(r.hooks), cmds)
			}
			for _, h := range r.hooks {
				cmd, ok := cmds[h.event]
				if !ok {
					t.Fatalf("no %s hook", h.event)
				}
				for _, tc := range []struct {
					name string
					env  []string
					want string
				}{
					{"outside bunk", []string{"PATH=" + empty}, ""},
					{"outside bunk with bunk installed", []string{"PATH=" + both}, ""},
					{"inside bunk", []string{"PATH=" + both, EnvSession + "=tok"}, "bunk notify " + h.args + "\n"},
					{"inside bunk-gui without bunk", []string{"PATH=" + guiOnly, EnvSession + "=tok"}, "bunk-gui notify " + h.args + "\n"},
					{"inside bunk without either binary", []string{"PATH=" + empty, EnvSession + "=tok"}, ""},
					{"over ssh", []string{"PATH=" + both, envSSH + "=10.0.0.2 50000 10.0.0.1 22"}, "bunk notify " + h.args + "\n"},
					{"over ssh without bunk", []string{"PATH=" + empty, envSSH + "=10.0.0.2 50000 10.0.0.1 22"}, ""},
				} {
					if got := run(t, []string{"/bin/sh", "-c", cmd}, tc.env); got != tc.want {
						t.Fatalf("%s %s: %q ran %q, want %q", h.event, tc.name, cmd, got, tc.want)
					}
				}
			}
			if r.name != "codex" {
				return
			}
			argv := append(codexNotifyArgv(t, snippet), codexNotify)
			for _, tc := range []struct {
				name string
				env  []string
				want string
			}{
				{"outside bunk", []string{"PATH=" + empty}, ""},
				{"over ssh without bunk", []string{"PATH=" + empty, envSSH + "=x"}, ""},
				{"over ssh", []string{"PATH=" + guiOnly, envSSH + "=x"}, "bunk-gui notify done " + codexNotify + "\n"},
				{"inside bunk", []string{"PATH=" + both, EnvSession + "=tok"}, "bunk notify done " + codexNotify + "\n"},
				{"inside bunk-gui without bunk", []string{"PATH=" + guiOnly, EnvSession + "=tok"}, "bunk-gui notify done " + codexNotify + "\n"},
			} {
				if got := run(t, argv, tc.env); got != tc.want {
					t.Fatalf("notify program %s: %q ran %q, want %q", tc.name, argv, got, tc.want)
				}
			}
		})
	}
	if _, ok := HookSnippet("emacs"); ok {
		t.Fatal("snippet for an unknown agent")
	}
}

// TestHookDocs: docs/agent-hooks.md shows exactly what "notify hooks" prints.
func TestHookDocs(t *testing.T) {
	t.Parallel()
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "agent-hooks.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recipes {
		snippet, ok := HookSnippet(r.name)
		if !ok || !strings.Contains(string(doc), snippet) {
			t.Errorf("docs/agent-hooks.md lacks the current %s snippet; paste the output of `bunk notify hooks %s`", r.name, r.name)
		}
	}
}
