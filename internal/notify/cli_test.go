package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	claudeQuestion    = `{"session_id":"abc123","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[` +
		`{"question":"Which framework?","header":"Framework","options":[{"label":"React","description":"Component library"},` +
		`{"label":"Vue","description":"Progressive framework"}],"multiSelect":false},` +
		`{"question":"Which bundler?","header":"Bundler","options":[{"label":"Vite"},{"label":"esbuild"}]}]},"tool_use_id":"toolu_01"}`
	claudePlan = `{"session_id":"abc123","hook_event_name":"PreToolUse","tool_name":"ExitPlanMode",` +
		`"tool_input":{"plan":"\n# Plan: Infer done from output\n\n1. Track the working stretch.\n","planFilePath":"/home/u/.claude/plans/p.md"},"tool_use_id":"toolu_02"}`
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
	// Codex transcripts whose last turn routes approvals to the automatic
	// reviewer, to the user, or names no reviewer.
	transcripts := t.TempDir()
	autoReview := codexPermissionIn(t, transcripts, "user", "auto_review")
	userReview := codexPermissionIn(t, transcripts, "auto_review", "user")
	noReviewer := codexPermissionIn(t, transcripts)
	missing := strings.Replace(codexPermission, `{`, `{"transcript_path":"`+filepath.Join(transcripts, "missing.jsonl")+`",`, 1)

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
				Host: "build01", Repo: "bunk", Branch: "main", Cwd: "/home/u/bunk", AgentSession: "abc123"}},
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
		{name: "Claude Stop with a background shell", args: []string{"done", "-"}, env: inside,
			stdin: `{"hook_event_name":"Stop","last_assistant_message":"Waiting for the tests.","background_tasks":[{"id":"b1","type":"shell","status":"running","description":"make check-all","command":"make check-all"}],"session_crons":[]}`,
			want:  &report{session.EventWorking, "Waiting for the tests."}},
		{name: "Claude Stop with only an artifact watch", args: []string{"done", "-"}, env: inside,
			stdin: `{"hook_event_name":"Stop","last_assistant_message":"The page is published.","background_tasks":[{"id":"w1","type":"monitor","status":"running","description":"live updates for artifact https://claude.ai/artifact/7rhYRjnfJTG7JfFHZXhNJb (watch requested)"}],"session_crons":[]}`,
			want:  &report{session.EventDone, "The page is published."}},
		{name: "Claude Stop with an artifact watch and a monitor", args: []string{"done", "-"}, env: inside,
			stdin: `{"hook_event_name":"Stop","last_assistant_message":"Waiting for CI.","background_tasks":[{"id":"w1","type":"monitor","status":"running","description":"live updates for artifact https://claude.ai/artifact/7rhYRjnfJTG7JfFHZXhNJb (watch requested)"},{"id":"m1","type":"monitor","status":"running","description":"CI on PR 42"}],"session_crons":[]}`,
			want:  &report{session.EventWorking, "Waiting for CI."}},
		{name: "Claude Stop with only a scheduled wakeup", args: []string{"done", "-"}, env: inside,
			stdin: `{"hook_event_name":"Stop","last_assistant_message":"Checking again in 20 min.","background_tasks":[],"session_crons":[{"id":"c1","schedule":"*/20 * * * *"}]}`,
			want:  &report{session.EventDone, "Checking again in 20 min."}},
		{name: "Claude StopFailure on stdin", args: []string{"failed", "-"}, env: inside, stdin: claudeStopFailure, want: &report{session.EventFailed, "API Error: Rate limit reached"}},
		{name: "Codex notify argument", args: []string{"done", codexNotify}, env: inside, want: &report{session.EventDone, "Rename complete."}},
		{name: "Gemini AfterAgent on stdin", args: []string{"done", "-"}, env: inside, stdin: geminiAfterAgent, want: &report{session.EventDone, "Done: fixed it."}},
		{name: "error object on stdin", args: []string{"failed", "-"}, env: inside, stdin: copilotError, want: &report{session.EventFailed, "Network timeout"}},
		{name: "hook JSON without text", args: []string{"needs-you", "-"}, env: inside,
			stdin: `{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`,
			want:  &report{session.EventNeedsYou, ""}},
		{name: "Codex PermissionRequest on stdin", args: []string{"needs-you", "-"}, env: inside, stdin: codexPermission,
			want: &report{session.EventNeedsYou, "Approval requested: rm -rf build"}},
		{name: "Codex approval of another tool", args: []string{"needs-you", "-"}, env: inside,
			stdin: `{"session_id":"s","hook_event_name":"PermissionRequest","turn_id":"t","tool_name":"apply_patch","tool_input":{"command":"*** Begin Patch\n"}}`,
			want:  &report{session.EventNeedsYou, "Approval requested"}},
		{name: "Codex approval for the automatic reviewer", args: []string{"needs-you", "-"}, env: inside, stdin: autoReview,
			want: &report{session.EventWorking, "Approval requested: rm -rf build"}},
		{name: "Codex approval for the user", args: []string{"needs-you", "-"}, env: inside, stdin: userReview,
			want: &report{session.EventNeedsYou, "Approval requested: rm -rf build"}},
		{name: "Codex transcript without a reviewer", args: []string{"needs-you", "-"}, env: inside, stdin: noReviewer,
			want: &report{session.EventNeedsYou, "Approval requested: rm -rf build"}},
		{name: "Codex transcript missing", args: []string{"needs-you", "-"}, env: inside, stdin: missing,
			want: &report{session.EventNeedsYou, "Approval requested: rm -rf build"}},
		{name: "Claude question on stdin", args: []string{"needs-you", "-"}, env: inside, stdin: claudeQuestion,
			want: &report{session.EventNeedsYou, "Which framework?"}},
		{name: "Claude plan on stdin", args: []string{"needs-you", "-"}, env: inside, stdin: claudePlan,
			want: &report{session.EventNeedsYou, "Approve the plan: Infer done from output"}},
		{name: "Claude plan without text", args: []string{"needs-you", "-"}, env: inside,
			stdin: `{"hook_event_name":"PreToolUse","tool_name":"ExitPlanMode","tool_input":{"plan":"# Plan:\n"}}`,
			want:  &report{session.EventNeedsYou, "Approve the plan"}},
		{name: "question of another tool", args: []string{"needs-you", "-"}, env: inside,
			stdin: `{"hook_event_name":"PreToolUse","tool_name":"mcp__survey__ask","tool_input":{"questions":[{"question":"Rate us?"}]}}`,
			want:  &report{session.EventNeedsYou, ""}},
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

// codexPermissionIn writes a Codex rollout transcript in dir with one turn
// per reviewer, each turn_context followed by the turn's items, and
// returns codexPermission naming it. A reviewer "" leaves the field out.
func codexPermissionIn(t *testing.T, dir string, reviewers ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"timestamp":"2026-10-28T09:14:11.314Z","ordinal":0,"type":"session_meta","payload":{"id":"s","cwd":"/w"}}` + "\n")
	for i, reviewer := range reviewers {
		field := ""
		if reviewer != "" {
			field = `,"approvals_reviewer":"` + reviewer + `"`
		}
		fmt.Fprintf(&b, `{"timestamp":"2026-10-28T09:14:11.690Z","ordinal":%d,"type":"turn_context","payload":{"turn_id":"t%d","cwd":"/w","approval_policy":"on-request"%s,"sandbox_policy":{"type":"workspace-write"}}}`+"\n", 2*i+1, i, field)
		// A tool output that quotes a turn_context line, escaped as JSON.
		fmt.Fprintf(&b, `{"timestamp":"2026-10-28T09:14:12.000Z","ordinal":%d,"type":"response_item","payload":{"type":"function_call_output","output":"{\"type\":\"turn_context\",\"payload\":{\"approvals_reviewer\":\"user\"}}"}}`+"\n", 2*i+2)
	}
	path := filepath.Join(dir, fmt.Sprintf("rollout-%d.jsonl", len(reviewers))+strings.Join(reviewers, "-"))
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := json.Marshal(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Replace(codexPermission, `{`, `{"transcript_path":`+string(in)+`,`, 1)
}

// TestCodexReviewer: the last turn_context gives the reviewer wherever it
// falls among the chunks read, and none past the scan limit.
func TestCodexReviewer(t *testing.T) {
	t.Parallel()
	sep := []byte(turnContextMarker)
	size := int64(transcriptChunk + 4096)
	// Around the boundary of the first two chunks, and at the edges.
	var offsets []int64
	for k := -1; k <= len(sep); k++ {
		offsets = append(offsets, size-transcriptChunk-int64(k))
	}
	offsets = append(offsets, 0, size-int64(len(sep)))
	for _, at := range offsets {
		data := bytes.Repeat([]byte{'x'}, int(size))
		copy(data[at:], sep)
		if got := lastIndex(bytes.NewReader(data), size, size, sep); got != at {
			t.Errorf("sep at %d: found at %d", at, got)
		}
		if got := lastIndex(bytes.NewReader(data), size, size-at-1, sep); got != -1 {
			t.Errorf("sep at %d, limit %d: found at %d", at, size-at-1, got)
		}
	}
	if got := lastIndex(bytes.NewReader(nil), 0, transcriptScan, sep); got != -1 {
		t.Errorf("empty transcript: found at %d", got)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	turn := func(reviewer string) string {
		return `{"timestamp":"t","type":"turn_context","payload":{"turn_id":"t","approvals_reviewer":"` + reviewer + `"}}` + "\n"
	}
	output := `{"timestamp":"t","type":"response_item","payload":{"output":"` + strings.Repeat("y", 3*transcriptChunk) + `"}}` + "\n"
	for _, tc := range []struct {
		name, transcript, want string
	}{
		{"last turn", turn("user") + turn("auto_review"), "auto_review"},
		{"old alias", turn("guardian_subagent"), "guardian_subagent"},
		{"long turn", turn("user") + turn("auto_review") + output + output, "auto_review"},
		{"cut-off line", turn("user") + `{"timestamp":"t","type":"turn_context","payload":{"turn_id":"t","approvals_rev`, ""},
		{"no turn", output, ""},
	} {
		if err := os.WriteFile(path, []byte(tc.transcript), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := codexReviewer(path); got != tc.want {
			t.Errorf("%s: reviewer %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := codexReviewer(filepath.Join(dir, "missing.jsonl")); got != "" {
		t.Errorf("missing transcript: reviewer %q", got)
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
	case "claude", "codex", "gemini":
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
					// Codex allows these two 3 s at most.
					if agent == "codex" && (ev == "Interrupt" || ev == "SessionEnd") && h.Timeout > 3 {
						t.Errorf("codex %s: timeout %d s, Codex allows 3", ev, h.Timeout)
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
	}
	return cmds
}

// codexNotifyArgv returns the notify program of codexNotifyLine, the
// config.toml key for Codex releases without hooks.
func codexNotifyArgv(t *testing.T) []string {
	t.Helper()
	var cfg struct{ Notify []string }
	if _, err := toml.Decode(codexNotifyLine(), &cfg); err != nil || len(cfg.Notify) == 0 {
		t.Fatalf("codex notify line %q: %v, %q", codexNotifyLine(), err, cfg.Notify)
	}
	return cfg.Notify
}

// TestHookSnippets parses each agent's snippet and runs every hook command
// with sh. Outside bunk it succeeds without running anything. Inside it runs
// "bunk-gui notify" with the recipe's arguments, or "bunk notify" when
// bunk-gui is not on PATH, as on a server reached over ssh. The Codex
// notify program passes Codex's JSON payload on.
func TestHookSnippets(t *testing.T) {
	t.Parallel()
	both, guiOnly, tuiOnly := t.TempDir(), t.TempDir(), t.TempDir()
	out := filepath.Join(t.TempDir(), "calls")
	fake := "#!/bin/sh\nprintf '%s %s\\n' \"${0##*/}\" \"$*\" >> \"$OUT\"\n"
	for _, f := range []string{filepath.Join(both, "bunk"), filepath.Join(both, "bunk-gui"), filepath.Join(guiOnly, "bunk-gui"), filepath.Join(tuiOnly, "bunk")} {
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
					{"inside bunk with both binaries", []string{"PATH=" + both, EnvSession + "=tok"}, "bunk-gui notify " + h.args + "\n"},
					{"inside bunk-gui without bunk", []string{"PATH=" + guiOnly, EnvSession + "=tok"}, "bunk-gui notify " + h.args + "\n"},
					{"inside bunk without bunk-gui", []string{"PATH=" + tuiOnly, EnvSession + "=tok"}, "bunk notify " + h.args + "\n"},
					{"inside bunk without either binary", []string{"PATH=" + empty, EnvSession + "=tok"}, ""},
					{"over ssh", []string{"PATH=" + tuiOnly, envSSH + "=10.0.0.2 50000 10.0.0.1 22"}, "bunk notify " + h.args + "\n"},
					{"over ssh with both binaries", []string{"PATH=" + both, envSSH + "=10.0.0.2 50000 10.0.0.1 22"}, "bunk-gui notify " + h.args + "\n"},
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
			argv := append(codexNotifyArgv(t), codexNotify)
			for _, tc := range []struct {
				name string
				env  []string
				want string
			}{
				{"outside bunk", []string{"PATH=" + empty}, ""},
				{"over ssh without bunk", []string{"PATH=" + empty, envSSH + "=x"}, ""},
				{"over ssh", []string{"PATH=" + tuiOnly, envSSH + "=x"}, "bunk notify done " + codexNotify + "\n"},
				{"inside bunk with both binaries", []string{"PATH=" + both, EnvSession + "=tok"}, "bunk-gui notify done " + codexNotify + "\n"},
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

// TestClaudeQuestionHooks runs the Claude snippet's PreToolUse and
// PostToolUse commands with the real CLI (this test binary as bunk-gui) and
// Claude Code's hook input: a question or a plan reports needs-you with the
// question as the notice, and the answer reports work again. The commands
// print nothing, so Claude Code reads no permission decision from them.
func TestClaudeQuestionHooks(t *testing.T) {
	srv, token, target := startServer(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	wrapper := "#!/bin/sh\n[ \"$1\" = notify ] && shift\nGORACE=\"$GORACE atexit_sleep_ms=0\" " + cliHelperEnv + "=1 exec '" + exe + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "bunk-gui"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	snippet, _ := HookSnippet("claude")
	var cfg struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(snippet), &cfg); err != nil {
		t.Fatal(err)
	}
	command := func(event string) string {
		t.Helper()
		groups := cfg.Hooks[event]
		if len(groups) != 1 || groups[0].Matcher != "AskUserQuestion|ExitPlanMode" || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s hooks %+v, want one group matching AskUserQuestion and ExitPlanMode", event, groups)
		}
		return groups[0].Hooks[0].Command
	}
	postAnswer := `{"session_id":"abc123","hook_event_name":"PostToolUse","tool_name":"AskUserQuestion",` +
		`"tool_input":{"questions":[{"question":"Which framework?"}]},"tool_response":{"answers":{"Which framework?":"React"}}}`
	for _, tc := range []struct {
		name, event, input string
		want               report
	}{
		{"question shown", "PreToolUse", claudeQuestion, report{session.EventNeedsYou, "Which framework?"}},
		{"question answered", "PostToolUse", postAnswer, report{session.EventWorking, ""}},
		{"plan shown", "PreToolUse", claudePlan, report{session.EventNeedsYou, "Approve the plan: Infer done from output"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := exec.Command("/bin/sh", "-c", command(tc.event))
			c.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", EnvSocket + "=" + srv.Path(), EnvSession + "=" + token}
			c.Stdin = strings.NewReader(tc.input)
			if out, err := c.CombinedOutput(); err != nil || len(out) > 0 {
				t.Fatalf("hook: %v, output %q", err, out)
			}
			if got := next(t, target); got != tc.want {
				t.Fatalf("report %+v, want %+v", got, tc.want)
			}
		})
	}
	expectEmpty(t, target)
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
	if !strings.Contains(string(doc), codexNotifyLine()) {
		t.Errorf("docs/agent-hooks.md lacks the current Codex notify line:\n%s", codexNotifyLine())
	}
}
