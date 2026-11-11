package notify

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"bunk/internal/notify/wire"
	"bunk/internal/session"
)

// presented is one call to a recording presenter.
type presented struct {
	token, path string
	auto        bool
}

// startPresenter starts a server whose presenter records each request and
// answers show.
func startPresenter(t *testing.T, show bool) (*Server, string, fakeTarget, chan presented) {
	t.Helper()
	srv, token, target := startServer(t)
	got := make(chan presented, 16)
	srv.SetPresenter(func(token, path string, auto bool, check func([]string)) bool {
		got <- presented{token, path, auto}
		if check != nil {
			check(nil)
		}
		return show
	})
	return srv, token, target, got
}

// nextPresented returns the next request a presenter got, failing after 5 s.
func nextPresented(t *testing.T, ch chan presented) presented {
	t.Helper()
	select {
	case p := <-ch:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no present request arrived")
		return presented{}
	}
}

// noPresented fails when a present request is waiting.
func noPresented(t *testing.T, ch chan presented) {
	t.Helper()
	select {
	case p := <-ch:
		t.Fatalf("unexpected present request %+v", p)
	default:
	}
}

// TestPresentReplies: a present request reaches the presenter with its
// token, path and auto flag and gets its reply; a request for a token that
// is not registered, or to a server without a presenter, does not reach
// one. Reports keep their one-way path beside it.
func TestPresentReplies(t *testing.T) {
	srv, token, target, got := startPresenter(t, true)
	if reply, err := Present(srv.Path(), token, "/home/u/docs/plan.md", false); err != nil || reply != PresentShown {
		t.Fatalf("Present = %q, %v; want %q", reply, err, PresentShown)
	}
	if p := nextPresented(t, got); p != (presented{token, "/home/u/docs/plan.md", false}) {
		t.Fatalf("presenter got %+v", p)
	}
	if reply, err := Present(srv.Path(), token, "/home/u/docs/page.html", true); err != nil || reply != PresentShown {
		t.Fatalf("auto: %q, %v", reply, err)
	}
	if p := nextPresented(t, got); !p.auto || p.path != "/home/u/docs/page.html" {
		t.Fatalf("auto: presenter got %+v", p)
	}

	other, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if reply, err := Present(srv.Path(), other, "/home/u/docs/plan.md", false); err != nil || reply != PresentUnknown {
		t.Fatalf("unknown token: %q, %v", reply, err)
	}
	noPresented(t, got)

	if err := Send(srv.Path(), Message{Session: token, Event: session.EventDone, Text: "after present"}); err != nil {
		t.Fatal(err)
	}
	if r := next(t, target); r != (report{session.EventDone, "after present"}) {
		t.Fatalf("report after present: %+v", r)
	}

	refusing, rtoken, _, rgot := startPresenter(t, false)
	if reply, err := Present(refusing.Path(), rtoken, "/x.md", false); err != nil || reply != PresentUnsupported {
		t.Fatalf("presenter refused: %q, %v", reply, err)
	}
	nextPresented(t, rgot)

	tui, ttoken, _ := startServer(t)
	if reply, err := Present(tui.Path(), ttoken, "/x.md", false); err != nil || reply != PresentUnsupported {
		t.Fatalf("no presenter: %q, %v", reply, err)
	}
	if _, err := Present(tui.Path(), "", "/x.md", false); err == nil {
		t.Fatal("Present without a token succeeded")
	}
}

// TestPresentOlderServer: a front end from before present requests reads
// the line, drops it as an unknown event and closes the connection without
// a reply, which counts as unsupported at once, not after its read timeout.
func TestPresentOlderServer(t *testing.T) {
	path := filepath.Join(runtimeDir(t), "old.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := ln.Close(); err != nil {
			t.Error(err)
		}
	}()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		if _, err := io.Copy(io.Discard, c); err != nil {
			t.Errorf("old server read: %v", err)
		}
		if err := c.Close(); err != nil {
			t.Errorf("old server close: %v", err)
		}
	}()
	start := time.Now()
	reply, err := Present(path, "tok", "/x.md", false)
	if err != nil || reply != PresentUnsupported {
		t.Fatalf("Present = %q, %v; want %q", reply, err, PresentUnsupported)
	}
	if d := time.Since(start); d >= readTimeout {
		t.Fatalf("the reply took %v, as long as an old server's read timeout", d)
	}
}

// TestPresentDropsBadRequests: a request without a token, with a path that
// is relative, too long or holds control characters gets no reply and
// never reaches the presenter.
func TestPresentDropsBadRequests(t *testing.T) {
	srv, token, _, got := startPresenter(t, true)
	// Encoded without Report.Encode's checks, as another client might.
	raw := func(r wire.Report) []byte {
		r.V, r.Event = wire.Version, wire.EventPresent
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return append(line, '\n')
	}
	for _, tc := range []struct {
		name string
		line []byte
	}{
		{"no token", raw(wire.Report{Path: "/x.md"})},
		{"relative path", raw(wire.Report{Session: token, Path: "docs/x.md"})},
		{"no path", raw(wire.Report{Session: token})},
		{"control character", raw(wire.Report{Session: token, Path: "/x\x1b[31m.md"})},
		{"path over MaxPath", raw(wire.Report{Session: token, Path: "/" + strings.Repeat("a", wire.MaxPath) + ".md"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.Dial("unix", srv.Path())
			if err != nil {
				t.Fatal(err)
			}
			c := conn.(*net.UnixConn)
			defer func() {
				if err := c.Close(); err != nil {
					t.Error(err)
				}
			}()
			if _, err := c.Write(tc.line); err != nil {
				t.Fatal(err)
			}
			if err := c.CloseWrite(); err != nil {
				t.Fatal(err)
			}
			if err := c.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if reply, err := io.ReadAll(c); err != nil || len(reply) > 0 {
				t.Fatalf("reply %q, %v; want none", reply, err)
			}
			noPresented(t, got)
		})
	}
}

// quote writes s as a JSON string.
func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// TestPresentPeerUID: the client refuses a server of another user.
func TestPresentPeerUID(t *testing.T) {
	srv, token, _, got := startPresenter(t, true)
	_, err := presentAs(srv.Path(), token, "/x.md", false, os.Getuid()+1)
	if err == nil || !strings.Contains(err.Error(), "server runs as uid") {
		t.Fatalf("client accepted a server of another uid: %v", err)
	}
	noPresented(t, got)
}

// TestPresentInBandIgnored: a present request written into a pane's output
// as an in-band report, with a token the session knows, changes nothing
// and does not reach the screen. Requests travel over the socket only.
func TestPresentInBandIgnored(t *testing.T) {
	if _, ok := session.ParseEvent(wire.EventPresent); ok {
		t.Fatal("present is a session event")
	}
	line, err := wire.Report{Session: "tok", Event: wire.EventPresent, Path: "/home/u/docs/plan.md"}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(line); err == nil {
		t.Fatal("Decode took a present request for a report")
	}
	s, changed := runSession(t, session.Options{
		Cols: 80, Rows: 5, Scrollback: 100,
		Env:           []string{"SEQ=" + string(wire.InBand(line))},
		Args:          []string{"/bin/sh", "-c", `printf '%s' "$SEQ"; echo after; sleep 30`},
		NotifySession: "tok",
		KnownToken:    func(token string) bool { return token == "tok" },
	})
	waitFor(t, s, changed, "output after the request", func(session.Status) bool {
		return strings.Contains(screenText(s, 80, 5), "after")
	})
	if st := s.Status(); st.Reported || st.State != session.AgentNone {
		t.Fatalf("an in-band present request changed the status: %+v", st)
	}
	if text := screenText(s, 80, 5); strings.Contains(text, wire.UserVar) {
		t.Fatalf("the request reached the screen:\n%s", text)
	}
}

// stubOpen makes openFile record its path and return err. Tests that call
// it must not run in parallel.
func stubOpen(t *testing.T, err error) *[]string {
	t.Helper()
	var opened []string
	prev := openFile
	t.Cleanup(func() { openFile = prev })
	openFile = func(path string, _ []string) error {
		opened = append(opened, path)
		return err
	}
	return &opened
}

// TestPresentCLI runs "present FILE" in a bunk-gui pane, in bunk's
// terminal UI, outside bunk, over ssh and without a display.
func TestPresentCLI(t *testing.T) {
	gui, guiToken, _, shown := startPresenter(t, true)
	tui, tuiToken, _ := startServer(t)
	dir := t.TempDir()
	md, html := filepath.Join(dir, "plan.md"), filepath.Join(dir, "page.HTML")
	writeFile(t, md, "# Plan\n\nText.\n")
	writeFile(t, html, "<!doctype html><p>Hi</p>\n")
	notes, shot := filepath.Join(dir, "notes.txt"), filepath.Join(dir, "shot.png")
	writeFile(t, notes, "notes\n")
	writeFile(t, shot, "\x89PNG\r\n")
	writeFile(t, filepath.Join(dir, "data.bin"), "\x00\x01\x02")
	if err := os.Mkdir(filepath.Join(dir, "folder.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	desktop := []string{"HOME=/home/u", "DISPLAY=:0", "XDG_CACHE_HOME=" + cache}
	inGUI := append(slices.Clone(desktop), EnvSocket+"="+gui.Path(), EnvSession+"="+guiToken)
	inTUI := append(slices.Clone(desktop), EnvSocket+"="+tui.Path(), EnvSession+"="+tuiToken)
	gone := append(slices.Clone(desktop), EnvSocket+"="+filepath.Join(runtimeDir(t), "gone.sock"), EnvSession+"="+guiToken)
	stale := append(slices.Clone(desktop), EnvSocket+"="+gui.Path(), EnvSession+"=0123")

	for _, tc := range []struct {
		name      string
		args, env []string
		openErr   error
		code      int
		stdout    string // substring; "" means no output at all
		stderr    string // substring; "" means no output at all
		shown     string // the file the presenter got, or ""
		opened    string // the file openFile got, "page" for a rendered page
	}{
		{name: "help", args: []string{"--help"}, stdout: "usage: bunk present FILE"},
		{name: "no arguments", code: 1, stderr: "usage: bunk present FILE"},
		{name: "two files", args: []string{md, html}, env: inGUI, code: 1, stderr: "unexpected arguments"},
		{name: "unknown option", args: []string{"--open"}, env: inGUI, code: 1, stderr: "unexpected arguments"},
		{name: "in bunk-gui", args: []string{md}, env: inGUI, stdout: "Showing " + md + " in bunk-gui's side panel.\n", shown: md},
		{name: "text in bunk-gui", args: []string{notes}, env: inGUI, stdout: "Showing " + notes + " in bunk-gui's side panel.\n", shown: notes},
		{name: "text in bunk's terminal UI", args: []string{notes}, env: inTUI, stdout: "Opened " + notes + " in the web browser.\n", opened: "page"},
		{name: "image in bunk's terminal UI", args: []string{shot}, env: inTUI, stdout: "Opened " + shot + " in its default application.\n", opened: shot},
		{name: "in bunk's terminal UI", args: []string{md}, env: inTUI, stdout: "Opened " + md + " in the web browser.\n", opened: "page"},
		{name: "HTML in bunk's terminal UI", args: []string{html}, env: inTUI, stdout: "Opened " + html + " in the web browser.\n", opened: html},
		{name: "outside bunk", args: []string{md}, env: desktop, stdout: "Opened", opened: "page"},
		{name: "on Wayland", args: []string{html}, env: []string{"WAYLAND_DISPLAY=wayland-0"}, stdout: "Opened", opened: html},
		{name: "front end exited", args: []string{html}, env: gone, stdout: "Opened", opened: html},
		{name: "token not registered", args: []string{html}, env: stale, stdout: "Opened", opened: html},
		{name: "over ssh", args: []string{md}, env: append(slices.Clone(desktop), envSSH+"=10.0.0.2 50000 10.0.0.1 22"),
			stdout: "Cannot show " + md + " here: this host is reached over ssh"},
		{name: "over ssh in bunk-gui", args: []string{md}, env: append(slices.Clone(inGUI), envSSH+"=x"), stdout: "Showing", shown: md},
		{name: "no display", args: []string{md}, env: []string{EnvSocket + "=" + tui.Path(), EnvSession + "=" + tuiToken},
			stdout: "Cannot show " + md + " here: there is no graphical display"},
		{name: "no opener", args: []string{html}, env: desktop, openErr: errNoOpener, opened: html,
			stdout: "Cannot show " + html + " here: neither gio nor xdg-open is on PATH.\n"},
		{name: "opener fails", args: []string{html}, env: desktop, openErr: errors.New("exec format error"), opened: html, code: 1, stderr: "exec format error"},
		{name: "missing file", args: []string{filepath.Join(dir, "gone.md")}, env: inGUI, code: 1, stderr: "cannot read " + filepath.Join(dir, "gone.md") + ": no such file"},
		{name: "directory", args: []string{filepath.Join(dir, "folder.md")}, env: inGUI, code: 1, stderr: "not a regular file"},
		{name: "binary", args: []string{filepath.Join(dir, "data.bin")}, env: inGUI, code: 1,
			stderr: "cannot show " + filepath.Join(dir, "data.bin") + ": not a document, image, PDF, video, audio or text file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opened := stubOpen(t, tc.openErr)
			var stdout, stderr bytes.Buffer
			code := runPresent("bunk", tc.args, tc.env, nil, &stdout, &stderr)
			if code != tc.code {
				t.Fatalf("exit %d, want %d (stdout %q, stderr %q)", code, tc.code, stdout.String(), stderr.String())
			}
			for _, out := range []struct {
				name, got, want string
			}{{"stdout", stdout.String(), tc.stdout}, {"stderr", stderr.String(), tc.stderr}} {
				if (out.want == "") != (out.got == "") || !strings.Contains(out.got, out.want) {
					t.Fatalf("%s = %q, want %q", out.name, out.got, out.want)
				}
			}
			if tc.shown != "" {
				if p := nextPresented(t, shown); p != (presented{guiToken, tc.shown, false}) {
					t.Fatalf("presenter got %+v", p)
				}
			}
			noPresented(t, shown)
			switch {
			case tc.opened == "" && len(*opened) > 0:
				t.Fatalf("opened %q", *opened)
			case tc.opened == "page":
				if len(*opened) != 1 || !strings.HasPrefix((*opened)[0], filepath.Join(cache, "bunk", "present")+"/") || filepath.Base((*opened)[0]) != "index.html" {
					t.Fatalf("opened %q, want a page in the cache", *opened)
				}
				if page, err := os.ReadFile((*opened)[0]); err != nil || !bytes.Contains(page, []byte(strings.TrimSuffix(filepath.Base(tc.args[0]), filepath.Ext(tc.args[0])))) {
					t.Fatalf("page: %v\n%s", err, page)
				}
			case tc.opened != "" && !slices.Equal(*opened, []string{tc.opened}):
				t.Fatalf("opened %q, want %q", *opened, tc.opened)
			}
		})
	}
}

// TestPresentRelativePath: a path relative to the working directory is
// sent absolute and named as given.
func TestPresentRelativePath(t *testing.T) {
	srv, token, _, got := startPresenter(t, true)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "docs", "plan.md"), "# Plan\n")
	t.Chdir(dir)
	var stdout bytes.Buffer
	code := runPresent("bunk-gui", []string{"docs/plan.md"}, []string{EnvSocket + "=" + srv.Path(), EnvSession + "=" + token}, nil, &stdout, io.Discard)
	if code != 0 || stdout.String() != "Showing docs/plan.md in bunk-gui's side panel.\n" {
		t.Fatalf("exit %d, %q", code, stdout.String())
	}
	want, err := filepath.EvalSymlinks(filepath.Join(dir, "docs", "plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	if p := nextPresented(t, got); p.path != filepath.Join(dir, "docs", "plan.md") && p.path != want {
		t.Fatalf("presenter got %+v", p)
	}
}

// TestOpenFile runs the real opener with fake gio and xdg-open: gio open
// comes first, xdg-open serves without it, and neither is errNoOpener.
func TestOpenFile(t *testing.T) {
	both, xdgOnly, out := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "calls")
	fake := "#!/bin/sh\nprintf '%s %s\\n' \"${0##*/}\" \"$*\" >> '" + out + "'\n"
	for _, f := range []string{filepath.Join(both, "gio"), filepath.Join(both, "xdg-open"), filepath.Join(xdgOnly, "xdg-open")} {
		if err := os.WriteFile(f, []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, path, want string
	}{
		{"gio", both, "gio open /d/plan.html\n"},
		{"xdg-open", xdgOnly, "xdg-open /d/plan.html\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(out, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := openFile("/d/plan.html", []string{"PATH=" + tc.path}); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				got, err := os.ReadFile(out)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) == tc.want {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("opener ran %q, want %q", got, tc.want)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
	if err := openFile("/d/plan.html", []string{"PATH=" + t.TempDir()}); !errors.Is(err, errNoOpener) {
		t.Fatalf("without an opener: %v", err)
	}
}

// TestHookFiles reads the PostToolUse inputs of each agent's file-writing
// tools, as their hooks receive them: files written whole count, edits do
// not.
func TestHookFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, f := range []string{"plan.md", "docs/design.md", "docs/page.html", "docs/new.md", "main.go", "notes.txt"} {
		writeFile(t, filepath.Join(dir, f), "x\n")
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	abs := func(rel string) string { return filepath.Join(dir, rel) }
	patch := "*** Begin Patch\n*** Add File: docs/new.md\n+x\n*** Add File: main.go\n+package main\n*** Update File: plan.md\n@@\n-a\n+b\n" +
		"*** Update File: old.md\n*** Move to: docs/design.md\n@@\n*** Delete File: notes.md\n*** Add File: docs/page.html\n+<p>x</p>\n*** End Patch\n"
	edit := "*** Begin Patch\n*** Update File: plan.md\n@@\n-a\n+b\n*** End Patch\n"
	for _, tc := range []struct {
		name, agent, input string
		want               []string
	}{
		{"claude Write", "claude", `{"hook_event_name":"PostToolUse","cwd":"/elsewhere","tool_name":"Write","tool_input":{"file_path":"` + abs("plan.md") + `","content":"x"},"tool_response":{}}`,
			[]string{abs("plan.md")}},
		{"claude Write, relative to cwd", "claude", `{"cwd":"` + dir + `","tool_name":"Write","tool_input":{"file_path":"docs/page.html","content":"x"}}`,
			[]string{abs("docs/page.html")}},
		{"claude Edit", "claude", `{"cwd":"` + dir + `","tool_name":"Edit","tool_input":{"file_path":"` + abs("plan.md") + `","old_string":"a","new_string":"b"}}`, nil},
		{"claude MultiEdit", "claude", `{"cwd":"` + dir + `","tool_name":"MultiEdit","tool_input":{"file_path":"` + abs("plan.md") + `","edits":[]}}`, nil},
		{"claude Read", "claude", `{"tool_name":"Read","tool_input":{"file_path":"` + abs("plan.md") + `"}}`, nil},
		{"claude Write of code", "claude", `{"tool_name":"Write","tool_input":{"file_path":"` + abs("main.go") + `"}}`, nil},
		{"claude Write of a deleted file", "claude", `{"tool_name":"Write","tool_input":{"file_path":"` + abs("gone.md") + `"}}`, nil},
		{"claude Write of a directory", "claude", `{"tool_name":"Write","tool_input":{"file_path":"` + abs("dir.md") + `"}}`, nil},
		{"claude relative without cwd", "claude", `{"tool_name":"Write","tool_input":{"file_path":"plan.md"}}`, nil},
		{"codex apply_patch", "codex", `{"hook_event_name":"PostToolUse","cwd":"` + dir + `","tool_name":"apply_patch","tool_input":{"command":` + quote(patch) + `},"tool_use_id":"c1","turn_id":"t1"}`,
			[]string{abs("docs/new.md"), abs("docs/page.html")}},
		{"codex apply_patch as argv", "codex", `{"cwd":"` + dir + `","tool_name":"apply_patch","tool_input":{"command":["apply_patch",` + quote(patch) + `]}}`,
			[]string{abs("docs/new.md"), abs("docs/page.html")}},
		{"codex edit", "codex", `{"cwd":"` + dir + `","tool_name":"apply_patch","tool_input":{"command":` + quote(edit) + `}}`, nil},
		{"codex Bash", "codex", `{"cwd":"` + dir + `","tool_name":"Bash","tool_input":{"command":` + quote(patch) + `}}`, nil},
		{"copilot create", "copilot", `{"sessionId":"s","timestamp":1,"cwd":"/elsewhere","toolName":"create","toolArgs":{"path":"` + abs("plan.md") + `","file_text":"x"},"toolResult":{"resultType":"success"}}`,
			[]string{abs("plan.md")}},
		{"copilot create with JSON text arguments", "copilot", `{"cwd":"` + dir + `","toolName":"create","toolArgs":` + quote(`{"path":"docs/design.md","file_text":"x"}`) + `}`,
			[]string{abs("docs/design.md")}},
		{"copilot edit", "copilot", `{"cwd":"` + dir + `","toolName":"edit","toolArgs":{"path":"` + abs("plan.md") + `","old_str":"a","new_str":"b"}}`, nil},
		{"copilot str_replace_editor", "copilot", `{"cwd":"` + dir + `","toolName":"str_replace_editor","toolArgs":{"command":"str_replace","path":"` + abs("plan.md") + `"}}`, nil},
		{"copilot apply_patch", "copilot", `{"cwd":"` + dir + `","toolName":"apply_patch","toolArgs":` + quote(patch) + `}`,
			[]string{abs("docs/new.md"), abs("docs/page.html")}},
		{"copilot workingDirectory", "copilot", `{"workingDirectory":"` + dir + `","toolName":"create","toolArgs":{"path":"plan.md"}}`,
			[]string{abs("plan.md")}},
		{"another agent's input", "codex", `{"tool_name":"Write","tool_input":{"file_path":"` + abs("plan.md") + `"}}`, nil},
		{"same file twice", "codex", `{"cwd":"` + dir + `","tool_name":"apply_patch","tool_input":{"command":"*** Add File: plan.md\n*** Add File: ./plan.md\n"}}`,
			[]string{abs("plan.md")}},
		{"not JSON", "claude", `Write plan.md`, nil},
		{"cut-off JSON", "claude", `{"tool_name":"Write","tool_input":{"file_path":"`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, show := hookFiles(tc.agent, tc.input, Dirs{}); !slices.Equal(got, tc.want) || show {
				t.Fatalf("hookFiles = %q, %v; want %q", got, show, tc.want)
			}
		})
	}
}

// TestHookPlans: the plan an agent asks the user to approve is shown, from
// Claude Code's ExitPlanMode input or Copilot CLI's plan.md in the state
// directory of the session.
func TestHookPlans(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	const session = "58e6b599-e886-4339-ab00-200003507f62"
	claudePlan := filepath.Join(home, ".claude", "plans", "quiet-river.md")
	copilotPlan := filepath.Join(home, ".copilot", "session-state", session, "plan.md")
	moved := filepath.Join(home, "moved")
	movedPlan := filepath.Join(moved, "session-state", "s1", "plan.md")
	for _, f := range []string{claudePlan, copilotPlan, movedPlan} {
		writeFile(t, f, "# Plan\n")
	}
	d := Dirs{Home: home}
	dm := Dirs{Home: home, Getenv: func(k string) string {
		if k == "COPILOT_HOME" {
			return moved
		}
		return ""
	}}
	for _, tc := range []struct {
		name, agent, input string
		d                  Dirs
		want               []string
		show               bool
	}{
		{"claude ExitPlanMode", "claude", `{"hook_event_name":"PreToolUse","cwd":"/elsewhere","tool_name":"ExitPlanMode","tool_input":{"plan":"# Plan","planFilePath":"` + claudePlan + `"}}`,
			d, []string{claudePlan}, true},
		{"claude ExitPlanMode without a file", "claude", `{"tool_name":"ExitPlanMode","tool_input":{"plan":"# Plan"}}`, d, nil, true},
		{"copilot exit_plan_mode", "copilot", `{"sessionId":"` + session + `","timestamp":1,"cwd":"/elsewhere","toolName":"exit_plan_mode","toolArgs":{"summary":"Add a cache"}}`,
			d, []string{copilotPlan}, true},
		{"copilot with COPILOT_HOME", "copilot", `{"sessionId":"s1","toolName":"exit_plan_mode","toolArgs":{}}`, dm, []string{movedPlan}, true},
		{"copilot without a plan yet", "copilot", `{"sessionId":"none","toolName":"exit_plan_mode","toolArgs":{}}`, d, nil, true},
		{"copilot session ID that leaves the directory", "copilot", `{"sessionId":"../` + session + `","toolName":"exit_plan_mode"}`, d, nil, true},
		{"copilot session ID ..", "copilot", `{"sessionId":"..","toolName":"exit_plan_mode"}`, d, nil, true},
		{"codex has no plan tool", "codex", `{"tool_name":"ExitPlanMode","tool_input":{"planFilePath":"` + claudePlan + `"}}`, d, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, show := hookFiles(tc.agent, tc.input, tc.d); !slices.Equal(got, tc.want) || show != tc.show {
				t.Fatalf("hookFiles = %q, %v; want %q, %v", got, show, tc.want, tc.show)
			}
		})
	}
}

// TestPresentHook: "--hook AGENT -" sends each document with auto set, and
// a plan to approve without it, prints nothing and exits 0, also outside
// bunk and when misused.
func TestPresentHook(t *testing.T) {
	srv, token, _, got := startPresenter(t, true)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "# A\n")
	writeFile(t, filepath.Join(dir, "b.html"), "<p>B</p>\n")
	input := `{"cwd":"` + dir + `","tool_name":"apply_patch","tool_input":{"command":"*** Add File: a.md\n*** Add File: b.html\n"}}`
	plan := `{"tool_name":"ExitPlanMode","tool_input":{"plan":"# A","planFilePath":"` + filepath.Join(dir, "a.md") + `"}}`
	inside := []string{EnvSocket + "=" + srv.Path(), EnvSession + "=" + token, "DISPLAY=:0"}
	for _, tc := range []struct {
		name  string
		args  []string
		env   []string
		input string // "" is input
		want  []presented
	}{
		{"inside bunk", []string{"--hook", "codex", "-"}, inside, "",
			[]presented{{token, filepath.Join(dir, "a.md"), true}, {token, filepath.Join(dir, "b.html"), true}}},
		{"a plan to approve", []string{"--hook", "claude", "-"}, inside, plan,
			[]presented{{token, filepath.Join(dir, "a.md"), false}}},
		{"outside bunk", []string{"--hook", "codex", "-"}, []string{"DISPLAY=:0"}, "", nil},
		{"over ssh", []string{"--hook", "codex", "-"}, []string{envSSH + "=x", EnvSession + "=" + token}, "", nil},
		{"unknown agent", []string{"--hook", "emacs", "-"}, inside, "", nil},
		{"no stdin argument", []string{"--hook", "codex"}, inside, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opened := stubOpen(t, nil)
			var stdout, stderr bytes.Buffer
			if code := runPresent("bunk", tc.args, tc.env, strings.NewReader(cmp.Or(tc.input, input)), &stdout, &stderr); code != 0 || stdout.Len() > 0 || stderr.Len() > 0 {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
			}
			for _, want := range tc.want {
				if p := nextPresented(t, got); p != want {
					t.Fatalf("presenter got %+v, want %+v", p, want)
				}
			}
			noPresented(t, got)
			if len(*opened) > 0 {
				t.Fatalf("a hook opened %q", *opened)
			}
		})
	}
}

// presentBin is a directory with a "bunk-gui" that runs this test binary
// as the present CLI, for hook snippets to run.
func presentBin(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	wrapper := "#!/bin/sh\n[ \"$1\" = present ] && shift\nGORACE=\"$GORACE atexit_sleep_ms=0\" " + presentHelperEnv + "=1 exec '" + exe + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "bunk-gui"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestPresentHookSnippets runs the present hooks of each agent's snippet
// with sh and the real CLI (this test binary as bunk-gui) on the agent's
// hook input: the file the tool wrote reaches the presenter with auto set,
// the plan to approve without it, and the hook prints nothing.
func TestPresentHookSnippets(t *testing.T) {
	srv, token, _, got := startPresenter(t, true)
	bin := presentBin(t)
	dir := t.TempDir()
	doc := filepath.Join(dir, "docs", "plan.md")
	writeFile(t, doc, "# Plan\n")
	copilotHome := filepath.Join(dir, "copilot")
	copilotPlan := filepath.Join(copilotHome, "session-state", "s1", "plan.md")
	writeFile(t, copilotPlan, "# Plan\n")
	for _, tc := range []struct {
		name, agent, event, matcher, input string
		want                               presented
	}{
		{"claude Write", "claude", "PostToolUse", claudeWriteTools, `{"hook_event_name":"PostToolUse","cwd":"` + dir + `","tool_name":"Write","tool_input":{"file_path":"` + doc + `"}}`,
			presented{token, doc, true}},
		{"claude ExitPlanMode", "claude", "PreToolUse", exitPlanMode, `{"hook_event_name":"PreToolUse","cwd":"` + dir + `","tool_name":"ExitPlanMode","tool_input":{"plan":"# Plan","planFilePath":"` + doc + `"}}`,
			presented{token, doc, false}},
		{"codex apply_patch", "codex", "PostToolUse", codexWriteTools, `{"hook_event_name":"PostToolUse","cwd":"` + dir + `","tool_name":"apply_patch","tool_input":{"command":"*** Begin Patch\n*** Add File: docs/plan.md\n+# Plan\n*** End Patch\n"}}`,
			presented{token, doc, true}},
		{"copilot create", "copilot", "postToolUse", copilotWriteTools, `{"sessionId":"s","cwd":"` + dir + `","toolName":"create","toolArgs":` + quote(`{"path":"`+doc+`","file_text":"# Plan"}`) + `}`,
			presented{token, doc, true}},
		{"copilot exit_plan_mode", "copilot", "preToolUse", copilotPlanTool, `{"sessionId":"s1","cwd":"` + dir + `","toolName":"exit_plan_mode","toolArgs":{"summary":"Plan"}}`,
			presented{token, copilotPlan, false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snippet, _ := HookSnippet(tc.agent)
			cmd, ok := handlerCommands(t, tc.agent, snippet)[hookKey(tc.event, tc.matcher)]
			if !ok || !strings.Contains(cmd, "present --hook "+tc.agent+" -") {
				t.Fatalf("no present hook for %s %q: %q", tc.event, tc.matcher, cmd)
			}
			c := exec.Command("/bin/sh", "-c", cmd)
			c.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", EnvSocket + "=" + srv.Path(), EnvSession + "=" + token, "COPILOT_HOME=" + copilotHome}
			c.Stdin = strings.NewReader(tc.input)
			if out, err := c.CombinedOutput(); err != nil || len(out) > 0 {
				t.Fatalf("hook: %v, output %q", err, out)
			}
			if p := nextPresented(t, got); p != tc.want {
				t.Fatalf("presenter got %+v, want %+v", p, tc.want)
			}
		})
	}
}

// TestSessionStart: "--session-start AGENT -" tells the agent about the
// panel in a bunk-gui pane, in the form each agent reads, and prints
// nothing in bunk's terminal UI, outside bunk, over ssh or when misused.
func TestSessionStart(t *testing.T) {
	t.Parallel()
	pane := []string{EnvSocket + "=/run/bunk.sock", EnvSession + "=tok", "TERM_PROGRAM=bunk-gui"}
	input := `{"session_id":"s","hook_event_name":"SessionStart","source":"startup","cwd":"/src"}`
	for _, tc := range []struct {
		name string
		args []string
		env  []string
		want string // the JSON key that holds the hint; "" for no output
	}{
		{"claude in bunk-gui", []string{"--session-start", "claude", "-"}, pane, "hookSpecificOutput"},
		{"codex in bunk-gui", []string{"--session-start", "codex", "-"}, pane, "hookSpecificOutput"},
		{"copilot in bunk-gui", []string{"--session-start", "copilot", "-"}, pane, "additionalContext"},
		{"bunk's terminal UI", []string{"--session-start", "claude", "-"}, []string{EnvSocket + "=/run/bunk.sock", EnvSession + "=tok", "TERM_PROGRAM=ptyxis"}, ""},
		{"outside bunk", []string{"--session-start", "claude", "-"}, []string{"TERM_PROGRAM=bunk-gui"}, ""},
		{"over ssh", []string{"--session-start", "claude", "-"}, []string{envSSH + "=x", EnvSession + "=tok", "TERM_PROGRAM=bunk-gui"}, ""},
		{"unknown agent", []string{"--session-start", "gemini", "-"}, pane, ""},
		{"no stdin argument", []string{"--session-start", "claude"}, pane, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runPresent("bunk-gui", tc.args, tc.env, strings.NewReader(input), &stdout, &stderr); code != 0 || stderr.Len() > 0 {
				t.Fatalf("exit %d, stderr %q", code, stderr.String())
			}
			if tc.want == "" {
				if stdout.Len() > 0 {
					t.Fatalf("printed %q", stdout.String())
				}
				return
			}
			hint := sessionHintOf(t, tc.args[1], stdout.String())
			for _, want := range []string{"bunk-gui", "`bunk-gui present FILE`", "self-contained HTML page", "Markdown", "About FILE › Section:", "bunk-present skill"} {
				if !strings.Contains(hint, want) {
					t.Errorf("hint lacks %q: %s", want, hint)
				}
			}
		})
	}
}

// sessionHintOf returns the context that a SessionStart hook's output out
// gives agent: hookSpecificOutput.additionalContext for Claude Code and
// Codex, additionalContext for Copilot CLI.
func sessionHintOf(t *testing.T, agent, out string) string {
	t.Helper()
	var got struct {
		Context  string `json:"additionalContext"`
		Specific *struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || !strings.HasSuffix(out, "}\n") {
		t.Fatalf("output %q: %v", out, err)
	}
	if agent == "copilot" {
		if got.Specific != nil || got.Context == "" {
			t.Fatalf("copilot output %q", out)
		}
		return got.Context
	}
	if got.Context != "" || got.Specific == nil || got.Specific.Event != "SessionStart" || got.Specific.Context == "" {
		t.Fatalf("%s output %q", agent, out)
	}
	return got.Specific.Context
}

// TestSessionStartSnippets runs each agent's SessionStart hook with sh and
// the real CLI: in a bunk-gui pane it prints the hint for the agent, and
// in bunk's terminal UI nothing.
func TestSessionStartSnippets(t *testing.T) {
	t.Parallel()
	bin := presentBin(t)
	for _, tc := range []struct{ agent, event string }{
		{"claude", "SessionStart"}, {"codex", "SessionStart"}, {"copilot", "sessionStart"},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			snippet, _ := HookSnippet(tc.agent)
			cmd, ok := handlerCommands(t, tc.agent, snippet)[hookKey(tc.event, "")]
			if !ok || !strings.Contains(cmd, "present --session-start "+tc.agent+" -") {
				t.Fatalf("no session-start hook for %s: %q", tc.event, cmd)
			}
			for _, term := range []string{"bunk-gui", ""} {
				c := exec.Command("/bin/sh", "-c", cmd)
				c.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", EnvSocket + "=/run/bunk.sock", EnvSession + "=tok", "TERM_PROGRAM=" + term}
				c.Stdin = strings.NewReader(`{"source":"startup"}`)
				out, err := c.Output()
				if err != nil {
					t.Fatalf("hook in %q: %v", term, err)
				}
				if term == "" {
					if len(out) > 0 {
						t.Fatalf("hook outside bunk-gui printed %q", out)
					}
					continue
				}
				if hint := sessionHintOf(t, tc.agent, string(out)); !strings.Contains(hint, " present FILE`") {
					t.Fatalf("hint %q", hint)
				}
			}
		})
	}
}

// TestSkills: each agent's skill is missing in an empty home, installed
// once written, outdated with other text; the variables that move an
// agent's directory are followed, and a SKILL.md that is a symbolic link
// stays one.
func TestSkills(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	moved := filepath.Join(home, "conf", "codex")
	d := Dirs{Home: home, Getenv: func(k string) string {
		if k == "CODEX_HOME" {
			return moved
		}
		return ""
	}}
	paths := map[string]string{
		"claude":  filepath.Join(home, ".claude", "skills", SkillName, "SKILL.md"),
		"codex":   filepath.Join(moved, "skills", SkillName, "SKILL.md"),
		"copilot": filepath.Join(home, ".copilot", "skills", SkillName, "SKILL.md"),
	}
	states := func() map[string]HookState {
		t.Helper()
		out := map[string]HookState{}
		for _, s := range CheckSkills(d) {
			if s.Path != paths[s.Agent] || s.Problem != "" {
				t.Fatalf("%+v, want path %s", s, paths[s.Agent])
			}
			out[s.Agent] = s.State
		}
		return out
	}
	if got := states(); len(got) != 3 || got["claude"] != HooksMissing || got["codex"] != HooksMissing || got["copilot"] != HooksMissing {
		t.Fatalf("empty home: %v", got)
	}
	for _, agent := range skillAgents {
		path, err := InstallSkill(d, agent)
		if err != nil || path != paths[agent] {
			t.Fatalf("install %s: %s, %v", agent, path, err)
		}
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o644 {
			t.Fatalf("%s: %v, mode %v", path, err, fi.Mode())
		}
	}
	if got := states(); got["claude"] != HooksInstalled || got["codex"] != HooksInstalled || got["copilot"] != HooksInstalled {
		t.Fatalf("after install: %v", got)
	}
	writeFile(t, paths["copilot"], "---\nname: bunk-present\n---\nolder\n")
	if got := states(); got["copilot"] != HooksOutdated {
		t.Fatalf("other text: %v", got)
	}

	target := filepath.Join(t.TempDir(), "SKILL.md")
	writeFile(t, target, "older\n")
	if err := os.Remove(paths["claude"]); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, paths["claude"]); err != nil {
		t.Fatal(err)
	}
	if got := states(); got["claude"] != HooksOutdated {
		t.Fatalf("linked older skill: %v", got)
	}
	if _, err := InstallSkill(d, "claude"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(paths["claude"]); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != skillText {
		t.Fatalf("link target: %v\n%s", err, data)
	}
	for _, agent := range []string{"gemini", "emacs"} {
		if _, err := InstallSkill(d, agent); err == nil {
			t.Errorf("installed a skill for %s", agent)
		}
	}
}

// TestSkillCLI runs "present skill", "status" and "install" against a home
// directory of their own.
func TestSkillCLI(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	env := []string{"HOME=" + home, "PATH=" + t.TempDir()}
	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := runPresent("bunk", args, env, nil, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	if code, out, _ := run("skill"); code != 0 || out != skillText {
		t.Errorf("skill: exit %d, %q", code, out)
	}
	code, out, _ := run("status")
	for _, want := range []string{
		"claude   missing    ~/.claude/skills/bunk-present/SKILL.md (claude is not on PATH)\n",
		"run bunk present install codex\n",
		"docs/present.md",
	} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("status: exit %d, %q lacks %q", code, out, want)
		}
	}
	if code, out, _ := run("install", "codex"); code != 0 || out != "Installed ~/.codex/skills/bunk-present/SKILL.md.\n" {
		t.Errorf("install codex: exit %d, %q", code, out)
	}
	if _, out, _ := run("status"); !strings.Contains(out, "codex    installed  ~/.codex/skills/bunk-present/SKILL.md") {
		t.Errorf("after install: %q", out)
	}
	for _, args := range [][]string{{"install", "gemini"}, {"install"}, {"install", "claude", "codex"}} {
		if code, _, errOut := run(args...); code != 1 || !strings.Contains(errOut, "claude, codex, copilot") {
			t.Errorf("%q: exit %d, %q", args, code, errOut)
		}
	}
}

// TestSkillText: the skill names itself, says when agents load it, and
// stays short enough to load whole.
func TestSkillText(t *testing.T) {
	t.Parallel()
	head, body, ok := strings.Cut(strings.TrimPrefix(skillText, "---\n"), "\n---\n")
	if !strings.HasPrefix(skillText, "---\n") || !ok {
		t.Fatalf("no frontmatter:\n%s", skillText)
	}
	if !strings.Contains(head, "name: "+SkillName+"\n") || !strings.Contains(head, "description: ") || !strings.Contains(head, EnvSession) {
		t.Errorf("frontmatter %q", head)
	}
	// Copilot CLI parses the frontmatter as strict YAML and skips a skill
	// whose plain value holds ": " (a mapping) or " #" (a comment).
	for line := range strings.Lines(head) {
		key, value, ok := strings.Cut(strings.TrimSuffix(line, "\n"), ": ")
		if !ok || strings.ContainsAny(key, " \"'") || strings.Contains(value, ": ") || strings.Contains(value, " #") ||
			strings.ContainsAny(value[:1], "-?:,[]{}#&*!|>'\"%@`") {
			t.Errorf("frontmatter line %q is not a key with a plain YAML value", line)
		}
	}
	if n := strings.Count(skillText, "\n"); n >= 120 {
		t.Errorf("%d lines, want under 120", n)
	}
	for _, want := range []string{"bunk-gui present PATH", "```mermaid", "$$display$$", "About docs/plan.md › Session storage:"} {
		if !strings.Contains(body, want) {
			t.Errorf("the skill lacks %q", want)
		}
	}
}

// TestPresentCheck: a check's reply waits for the page's problems, which
// follow it, while reports keep reaching their targets; a page that never
// finishes counts as a problem after checkWait; a hook's request is never
// checked, and an old server's one-line reply has no problems. The CLI
// prints the problems after its line.
func TestPresentCheck(t *testing.T) {
	srv, token, target := startServer(t)
	checks := make(chan func([]string), 4)
	srv.SetPresenter(func(token, path string, auto bool, check func([]string)) bool {
		checks <- check
		return true
	})
	type result struct {
		reply    string
		problems []string
		err      error
	}
	ask := func() chan result {
		ch := make(chan result, 1)
		go func() {
			reply, problems, err := PresentCheck(srv.Path(), token, "/home/u/docs/plan.md")
			ch <- result{reply, problems, err}
		}()
		return ch
	}
	res := ask()
	check := <-checks
	if check == nil {
		t.Fatal("a check reached the presenter without a check function")
	}
	// The server keeps serving while the check waits.
	if err := Send(srv.Path(), Message{Session: token, Event: session.EventClear, Text: "meanwhile"}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-target:
		if r.text != "meanwhile" {
			t.Fatalf("report %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a waiting check held up a report")
	}
	long := strings.Repeat("x", 2*maxProblemBytes)
	check([]string{"Mermaid diagram 1: Parse error\non line 2", long})
	r := <-res
	if r.err != nil || r.reply != PresentShown || len(r.problems) != 2 || r.problems[0] != "Mermaid diagram 1: Parse error on line 2" {
		t.Fatalf("PresentCheck = %+v", r)
	}
	if !strings.HasSuffix(r.problems[1], "…") || len(r.problems[1]) > maxProblemBytes+len("…") {
		t.Errorf("a long problem came back %d bytes long", len(r.problems[1]))
	}

	srv.mu.Lock()
	srv.checkWait = 50 * time.Millisecond
	srv.mu.Unlock()
	res = ask()
	<-checks // never called
	if r := <-res; r.reply != PresentShown || len(r.problems) != 1 || !strings.Contains(r.problems[0], "had not finished loading") {
		t.Fatalf("a check without an answer: %+v", r)
	}

	if reply, err := Present(srv.Path(), token, "/home/u/docs/plan.md", true); err != nil || reply != PresentShown {
		t.Fatalf("hook: %q, %v", reply, err)
	}
	if check := <-checks; check != nil {
		t.Error("a hook's request was checked")
	}

	// The CLI prints the problems after its line.
	srv.SetPresenter(func(token, path string, auto bool, check func([]string)) bool {
		check([]string{"cannot load img/flow.png: not found"})
		return true
	})
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "plan.md"), "# Plan\n")
	t.Chdir(dir)
	var stdout bytes.Buffer
	code := runPresent("bunk-gui", []string{"plan.md"}, []string{EnvSocket + "=" + srv.Path(), EnvSession + "=" + token}, nil, &stdout, io.Discard)
	want := "Showing plan.md in bunk-gui's side panel.\nThe page has a problem:\n- cannot load img/flow.png: not found\n"
	if code != 0 || !strings.HasPrefix(stdout.String(), want) {
		t.Fatalf("exit %d:\n%s\nwant it to start:\n%s", code, stdout.String(), want)
	}
}
