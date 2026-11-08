package notify

import (
	"bytes"
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
	srv.SetPresenter(func(token, path string, auto bool) bool {
		got <- presented{token, path, auto}
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
	writeFile(t, filepath.Join(dir, "notes.txt"), "notes\n")
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
		shown     bool   // the presenter got md
		opened    string // the file openFile got, "page" for md's rendered page
	}{
		{name: "help", args: []string{"--help"}, stdout: "usage: bunk present FILE"},
		{name: "no arguments", code: 1, stderr: "usage: bunk present FILE"},
		{name: "two files", args: []string{md, html}, env: inGUI, code: 1, stderr: "unexpected arguments"},
		{name: "unknown option", args: []string{"--open"}, env: inGUI, code: 1, stderr: "unexpected arguments"},
		{name: "in bunk-gui", args: []string{md}, env: inGUI, stdout: "Showing " + md + " in bunk-gui's side panel.\n", shown: true},
		{name: "in bunk's terminal UI", args: []string{md}, env: inTUI, stdout: "Opened " + md + " in the web browser.\n", opened: "page"},
		{name: "HTML in bunk's terminal UI", args: []string{html}, env: inTUI, stdout: "Opened " + html + " in the web browser.\n", opened: html},
		{name: "outside bunk", args: []string{md}, env: desktop, stdout: "Opened", opened: "page"},
		{name: "on Wayland", args: []string{html}, env: []string{"WAYLAND_DISPLAY=wayland-0"}, stdout: "Opened", opened: html},
		{name: "front end exited", args: []string{html}, env: gone, stdout: "Opened", opened: html},
		{name: "token not registered", args: []string{html}, env: stale, stdout: "Opened", opened: html},
		{name: "over ssh", args: []string{md}, env: append(slices.Clone(desktop), envSSH+"=10.0.0.2 50000 10.0.0.1 22"),
			stdout: "Cannot show " + md + " here: this host is reached over ssh"},
		{name: "over ssh in bunk-gui", args: []string{md}, env: append(slices.Clone(inGUI), envSSH+"=x"), stdout: "Showing", shown: true},
		{name: "no display", args: []string{md}, env: []string{EnvSocket + "=" + tui.Path(), EnvSession + "=" + tuiToken},
			stdout: "Cannot show " + md + " here: there is no graphical display"},
		{name: "no opener", args: []string{html}, env: desktop, openErr: errNoOpener, opened: html,
			stdout: "Cannot show " + html + " here: neither gio nor xdg-open is on PATH.\n"},
		{name: "opener fails", args: []string{html}, env: desktop, openErr: errors.New("exec format error"), opened: html, code: 1, stderr: "exec format error"},
		{name: "missing file", args: []string{filepath.Join(dir, "gone.md")}, env: inGUI, code: 1, stderr: "cannot read " + filepath.Join(dir, "gone.md") + ": no such file"},
		{name: "directory", args: []string{filepath.Join(dir, "folder.md")}, env: inGUI, code: 1, stderr: "not a regular file"},
		{name: "other kind", args: []string{filepath.Join(dir, "notes.txt")}, env: inGUI, code: 1, stderr: "neither Markdown (.md) nor HTML (.html)"},
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
			if tc.shown {
				if p := nextPresented(t, shown); p != (presented{guiToken, md, false}) {
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
				if page, err := os.ReadFile((*opened)[0]); err != nil || !bytes.Contains(page, []byte("Plan")) {
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
			if got := hookFiles(tc.agent, tc.input); !slices.Equal(got, tc.want) {
				t.Fatalf("hookFiles = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPresentHook: "--hook AGENT -" sends each document with auto set,
// prints nothing and exits 0, also outside bunk and when misused.
func TestPresentHook(t *testing.T) {
	srv, token, _, got := startPresenter(t, true)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "# A\n")
	writeFile(t, filepath.Join(dir, "b.html"), "<p>B</p>\n")
	input := `{"cwd":"` + dir + `","tool_name":"apply_patch","tool_input":{"command":"*** Add File: a.md\n*** Add File: b.html\n"}}`
	inside := []string{EnvSocket + "=" + srv.Path(), EnvSession + "=" + token, "DISPLAY=:0"}
	for _, tc := range []struct {
		name string
		args []string
		env  []string
		want []presented
	}{
		{"inside bunk", []string{"--hook", "codex", "-"}, inside,
			[]presented{{token, filepath.Join(dir, "a.md"), true}, {token, filepath.Join(dir, "b.html"), true}}},
		{"outside bunk", []string{"--hook", "codex", "-"}, []string{"DISPLAY=:0"}, nil},
		{"over ssh", []string{"--hook", "codex", "-"}, []string{envSSH + "=x", EnvSession + "=" + token}, nil},
		{"unknown agent", []string{"--hook", "emacs", "-"}, inside, nil},
		{"no stdin argument", []string{"--hook", "codex"}, inside, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opened := stubOpen(t, nil)
			var stdout, stderr bytes.Buffer
			if code := runPresent("bunk", tc.args, tc.env, strings.NewReader(input), &stdout, &stderr); code != 0 || stdout.Len() > 0 || stderr.Len() > 0 {
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

// TestPresentHookSnippets runs the present hook of each agent's snippet
// with sh and the real CLI (this test binary as bunk-gui) on the agent's
// PostToolUse input: the file the tool wrote reaches the presenter with
// auto set, and the hook prints nothing.
func TestPresentHookSnippets(t *testing.T) {
	srv, token, _, got := startPresenter(t, true)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	wrapper := "#!/bin/sh\n[ \"$1\" = present ] && shift\nGORACE=\"$GORACE atexit_sleep_ms=0\" " + presentHelperEnv + "=1 exec '" + exe + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "bunk-gui"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	doc := filepath.Join(dir, "docs", "plan.md")
	writeFile(t, doc, "# Plan\n")
	for _, tc := range []struct {
		agent, event, matcher, input string
	}{
		{"claude", "PostToolUse", claudeWriteTools, `{"hook_event_name":"PostToolUse","cwd":"` + dir + `","tool_name":"Write","tool_input":{"file_path":"` + doc + `"}}`},
		{"codex", "PostToolUse", codexWriteTools, `{"hook_event_name":"PostToolUse","cwd":"` + dir + `","tool_name":"apply_patch","tool_input":{"command":"*** Begin Patch\n*** Add File: docs/plan.md\n+# Plan\n*** End Patch\n"}}`},
		{"copilot", "postToolUse", copilotWriteTools, `{"sessionId":"s","cwd":"` + dir + `","toolName":"create","toolArgs":` + quote(`{"path":"`+doc+`","file_text":"# Plan"}`) + `}`},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			snippet, _ := HookSnippet(tc.agent)
			cmd, ok := handlerCommands(t, tc.agent, snippet)[hookKey(tc.event, tc.matcher)]
			if !ok || !strings.Contains(cmd, "present --hook "+tc.agent+" -") {
				t.Fatalf("no present hook for %s %q: %q", tc.event, tc.matcher, cmd)
			}
			c := exec.Command("/bin/sh", "-c", cmd)
			c.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", EnvSocket + "=" + srv.Path(), EnvSession + "=" + token}
			c.Stdin = strings.NewReader(tc.input)
			if out, err := c.CombinedOutput(); err != nil || len(out) > 0 {
				t.Fatalf("hook: %v, output %q", err, out)
			}
			if p := nextPresented(t, got); p != (presented{token, doc, true}) {
				t.Fatalf("presenter got %+v", p)
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
	if n := strings.Count(skillText, "\n"); n >= 120 {
		t.Errorf("%d lines, want under 120", n)
	}
	for _, want := range []string{"bunk-gui present PATH", "```mermaid", "$$display$$", "About docs/plan.md › Session storage:"} {
		if !strings.Contains(body, want) {
			t.Errorf("the skill lacks %q", want)
		}
	}
}
