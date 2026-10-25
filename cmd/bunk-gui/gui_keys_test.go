package main

// Window scenarios for key bindings, passthrough, the shortcuts window,
// quitting and agent hook reports.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// keylog prints, in hex, every chunk the terminal sends to its stdin: the
// bytes a key produced, one line per key.
const keylog = `import os, tty
tty.setraw(0)
os.write(1, b"keylog ready\r\n")
while True:
    b = os.read(0, 64)
    if not b:
        break
    os.write(1, (b.hex() + "\r\n").encode())
`

// keylogLines returns the hex lines a keylog pane printed.
func keylogLines(p paneReport) []string {
	var out []string
	started := false
	for _, l := range p.Text {
		switch {
		case strings.Contains(l, "keylog ready"):
			started = true
		case started && l != "":
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}

func keys(s *uiSnapshot, action string) string { return strings.Join(s.Keys[action], ",") }

// TestGUIKeysConfig: [keys] rebinds and disables actions, disabled and
// freed keys reach the terminal (the bytes the pane reads are checked),
// edits to config.toml apply live, and bad bindings produce the Settings
// toast naming them.
func TestGUIKeysConfig(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		config: "# test config\n[keys]\nnew_tab = \"ctrl+alt+n\"   # rebound\nswitcher = \"none\"\nnext_needs_you = [\"ctrl+shift+j\", \"f9\"]\n",
		args:   []string{"-size", "100x30", "-e", "python3 keylog.py"},
		files: map[string]string{
			"keylog.py": keylog,
			"next.toml": "tabs = \"bar\"\nfont_size = 12\n\n[keys]\nswitcher = \"ctrl+alt+s\"\nzoom = \"none\"\n",
			"bad.toml":  "[keys]\nfrobnicate = \"f3\"\nsplit = \"ctrl+banana\"\nswitcher = \"ctrl+alt+s\"\ncopy = \"ctrl+alt+s\"\n",
		},
		script: []string{
			"500ms snap start",
			"700ms kbd ctrl+shift+space",
			"850ms kbd ctrl+j",
			"1000ms kbd ctrl+b",
			"1150ms kbd ctrl+space",
			"1300ms kbd ctrl+q",
			"1450ms kbd ctrl+shift+t",
			"1600ms kbd f9",
			"1900ms snap keylog",
			"2000ms kbd ctrl+alt+n",
			"2400ms snap rebound",
			"2500ms kbd ctrl+1",
			"2600ms run cp next.toml config.toml",
			"3200ms snap reloaded",
			"3300ms kbd ctrl+alt+s",
			"3600ms snap switcher",
			"3700ms switcher close",
			"3800ms run cp bad.toml config.toml",
			"4400ms snap bad",
			"4500ms quit",
		},
	})
	s := g.snap("start")
	if keys(s, "new_tab") != "ctrl+alt+n" || keys(s, "switcher") != "" || keys(s, "next_needs_you") != "ctrl+shift+j,f9" ||
		keys(s, "quit") != "ctrl+shift+q" || s.ConfigError != "" {
		t.Errorf("keys from the config: %v; config error %q", s.Keys, s.ConfigError)
	}
	// Disabled (switcher), freed (Ctrl+J, B, Space, Q) and rebound-away
	// (Ctrl+Shift+T) keys go to the terminal; F9 runs next_needs_you.
	lines := keylogLines(g.snap("keylog").pane(t, 0))
	want := []string{"00", "0a", "02", "00", "11", "14"}
	t.Logf("bytes the pane read: %q", lines)
	if !slices.Equal(lines, want) {
		t.Errorf("the pane read %q, want %q (Ctrl+Shift+Space, Ctrl+J, Ctrl+B, Ctrl+Space, Ctrl+Q, Ctrl+Shift+T)", lines, want)
	}
	if s := g.snap("keylog"); len(s.Tabs) != 1 || s.Switcher.Open || s.LastToast != "No agent needs you" {
		t.Errorf("after the keys: %d tabs, switcher open %v, toast %q (F9 is next_needs_you)", len(s.Tabs), s.Switcher.Open, s.LastToast)
	}
	if s := g.snap("rebound"); len(s.Tabs) != 2 {
		t.Errorf("Ctrl+Alt+N (new_tab): %d tabs", len(s.Tabs))
	}
	r := g.snap("reloaded")
	if g.Reloads < 1 || keys(r, "switcher") != "ctrl+alt+s" || keys(r, "zoom") != "" || keys(r, "new_tab") != "ctrl+shift+t" || r.ConfigError != "" {
		t.Errorf("after editing config.toml: %d reloads, keys %v, error %q", g.Reloads, r.Keys, r.ConfigError)
	}
	if r.Layout != "bar" || r.Font != "Noto Sans Mono 12" || g.snap("start").Layout != "rail" {
		t.Errorf("layout and font after the reload: %q, %q", r.Layout, r.Font)
	}
	if s := g.snap("switcher"); !s.Switcher.Open {
		t.Error("Ctrl+Alt+S after the reload: switcher not open")
	}
	b := g.snap("bad")
	for _, w := range []string{"Settings: ", "frobnicate", "ctrl+banana", "ctrl+alt+s is bound to both switcher and copy"} {
		if !strings.Contains(b.LastToast, w) {
			t.Errorf("toast %q lacks %q", b.LastToast, w)
		}
	}
	if keys(b, "switcher") != "ctrl+alt+s" || keys(b, "copy") != "" || keys(b, "split") != "" {
		t.Errorf("after the bad file: switcher %q, copy %q, split %q", keys(b, "switcher"), keys(b, "copy"), keys(b, "split"))
	}
}

// TestGUIPassthrough: Ctrl+F12 sends every other key to the terminal for
// the focused pane, with a PASS badge on the pane and its tab (pixels and
// report); app shortcuts then reach the PTY; the options menu's check item
// does the same.
func TestGUIPassthrough(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		args:  []string{"-size", "100x30", "-e", "python3 keylog.py", "-tab", ""},
		files: map[string]string{"keylog.py": keylog},
		script: []string{
			"500ms kbd ctrl+f12",
			"800ms snap on",
			"850ms shot on.png",
			"950ms kbd ctrl+shift+t",
			"1100ms kbd ctrl+shift+q",
			"1250ms kbd f12",
			"1400ms kbd ctrl+shift+space",
			"1700ms snap keys",
			"1800ms kbd ctrl+f12",
			"2100ms snap off",
			"2150ms shot off.png",
			"2250ms kbd ctrl+shift+t",
			"2700ms snap new-tab",
			"2800ms kbd ctrl+1",
			"2900ms menu open",
			"3200ms menu-check",
			"3500ms snap menu",
			"3600ms quit",
		},
	})
	on := g.snap("on")
	p := on.pane(t, 0)
	if !p.Pass || p.Badge == nil || !on.tab(t, 1).Pass || on.tab(t, 2).Pass || !on.MenuPassCheck {
		t.Fatalf("passthrough on: pane %+v, tab 1 pass %v, tab 2 pass %v, menu check %v", p, on.tab(t, 1).Pass, on.tab(t, 2).Pass, on.MenuPassCheck)
	}
	badge := hex(0xe6e8eb)
	img := loadPNG(t, g.file("on.png"))
	x, y, w, h := inner(p.Badge, 2)
	if n := count(img, x, y, w, h, badge, 2); n < w*h/3 {
		t.Errorf("PASS badge on the pane: %d of %d pixels %v", n, w*h, badge)
	}
	it := on.tab(t, 1).Item
	if n := count(img, it.DX, it.DY, it.DW, it.DH, badge, 2); n < 20 {
		t.Errorf("PASS tag on the tab: %d pixels %v", n, badge)
	}
	if n := count(loadPNG(t, g.file("off.png")), x, y, w, h, badge, 2); n != 0 {
		t.Errorf("badge after passthrough off: %d pixels %v", n, badge)
	}
	// Ctrl+Shift+T, Ctrl+Shift+Q, F12 and Ctrl+Shift+Space reached the PTY.
	lines := keylogLines(g.snap("keys").pane(t, 0))
	t.Logf("bytes the pane read: %q", lines)
	if !slices.Equal(lines, []string{"14", "11", "1b5b32347e", "00"}) {
		t.Errorf("with passthrough the pane read %q", lines)
	}
	if s := g.snap("keys"); len(s.Tabs) != 2 || s.Quit != nil || s.Switcher.Open {
		t.Errorf("with passthrough an app shortcut ran: %d tabs, quit dialog %v, switcher %v", len(s.Tabs), s.Quit, s.Switcher.Open)
	}
	if s := g.snap("off"); s.pane(t, 0).Pass || s.tab(t, 1).Pass || s.MenuPassCheck {
		t.Errorf("passthrough off: still on")
	}
	if s := g.snap("new-tab"); len(s.Tabs) != 3 {
		t.Errorf("Ctrl+Shift+T after passthrough: %d tabs", len(s.Tabs))
	}
	if s := g.snap("menu"); !s.pane(t, 0).Pass || !s.MenuPassCheck || s.MenuOpen {
		t.Errorf("menu check item: pane 0 pass %v, check %v, menu open %v", s.pane(t, 0).Pass, s.MenuPassCheck, s.MenuOpen)
	}
	if g.Reason != "script" {
		t.Errorf("run ended by %q", g.Reason)
	}
}

// TestGUIShortcutsWindow drives the shortcuts window with real pointer
// clicks and keys: turn an action off and on again, rebind by key capture,
// cancel with Esc, clear with Backspace, refuse a key in use; config.toml
// is saved with everything else kept byte for byte.
func TestGUIShortcutsWindow(t *testing.T) {
	t.Parallel()
	const cfg = `# my settings: keep this comment
tabs = "rail"   # side

[keys]
# my bindings
new_tab = "ctrl+shift+t"   # muscle memory

[[agents]]
name = "aider"
processes = ["aider"]
`
	g := runGUI(t, scenario{
		config: cfg,
		args:   []string{"-size", "100x30"},
		script: []string{
			"300ms maximize",
			"1200ms kbd ctrl+shift+question",
			"1500ms snap open",
			"1600ms shortcut-scroll switcher",
			"1900ms ptr-at shortcut-switch switcher",
			"2000ms ptr-click",
			"2400ms snap off",
			"2500ms shortcut-scroll new_tab",
			"2800ms ptr-at shortcut-keys new_tab",
			"2900ms ptr-click",
			"3100ms snap capturing",
			"3200ms kbd ctrl+alt+t",
			"3600ms snap rebound",
			"3700ms shortcut-scroll zoom",
			"4000ms ptr-at shortcut-keys zoom",
			"4100ms ptr-click",
			"4300ms kbd Escape",
			"4600ms snap escaped",
			"4700ms ptr-click",
			"4900ms kbd BackSpace",
			"5300ms snap cleared",
			"5400ms shortcut-scroll copy",
			"5700ms ptr-at shortcut-keys copy",
			"5800ms ptr-click",
			"6000ms kbd ctrl+alt+t",
			"6300ms snap conflict",
			"6400ms shortcut-scroll switcher",
			"6700ms ptr-at shortcut-switch switcher",
			"6800ms ptr-click",
			"7200ms snap on-again",
			"7300ms dialog-close",
			"7600ms kbd ctrl+alt+t",
			"8000ms snap after",
			"8100ms quit",
		},
	})
	row := func(s *uiSnapshot, action string) shortcutsRowReport {
		t.Helper()
		if s.Shortcuts == nil {
			t.Fatalf("%s: the shortcuts window is not open", s.Label)
		}
		for _, r := range s.Shortcuts.Rows {
			if r.Action == action {
				return r
			}
		}
		t.Fatalf("%s: no row %s", s.Label, action)
		return shortcutsRowReport{}
	}
	open := g.snap("open")
	if len(open.Shortcuts.Rows) != len(keyActions) {
		t.Errorf("%d rows, want %d", len(open.Shortcuts.Rows), len(keyActions))
	}
	for action, want := range map[string]bool{"zoom": true, "split": true, "select_tab_1": true, "nav_left": true,
		"new_tab": false, "switcher": false, "passthrough": false, "scroll_up": false} {
		if r := row(open, action); r.Collides != want {
			t.Errorf("%s: collides %v, want %v", action, r.Collides, want)
		}
	}
	if r := row(g.snap("off"), "switcher"); r.Enabled || r.Keys != "Disabled" || keys(g.snap("off"), "switcher") != "" {
		t.Errorf("switch off: %+v", r)
	}
	if s := g.snap("capturing"); s.Shortcuts.Capturing != "new_tab" {
		t.Errorf("click on the keys: capturing %q", s.Shortcuts.Capturing)
	}
	if s := g.snap("rebound"); keys(s, "new_tab") != "ctrl+alt+t" || row(s, "new_tab").Keys != "Ctrl+Alt+T" || s.Shortcuts.Capturing != "" {
		t.Errorf("rebound: %q, row %+v", keys(s, "new_tab"), row(s, "new_tab"))
	}
	if s := g.snap("escaped"); keys(s, "zoom") != "f12" || s.Shortcuts.Capturing != "" {
		t.Errorf("Esc: zoom %q, capturing %q", keys(s, "zoom"), s.Shortcuts.Capturing)
	}
	if s := g.snap("cleared"); keys(s, "zoom") != "" || row(s, "zoom").Enabled {
		t.Errorf("Backspace: zoom %q", keys(s, "zoom"))
	}
	if s := g.snap("conflict"); keys(s, "copy") != "ctrl+shift+c" || !strings.Contains(s.Shortcuts.Message, "already used by New tab") {
		t.Errorf("a key in use: copy %q, message %q", keys(s, "copy"), s.Shortcuts.Message)
	}
	if s := g.snap("on-again"); keys(s, "switcher") != "ctrl+shift+space" || !row(s, "switcher").Enabled {
		t.Errorf("switch on again: %q", keys(s, "switcher"))
	}
	if s := g.snap("after"); len(s.Tabs) != 2 || s.Shortcuts != nil || s.ConfigError != "" {
		t.Errorf("Ctrl+Alt+T after closing the window: %d tabs; config error %q", len(s.Tabs), s.ConfigError)
	}
	got, err := os.ReadFile(filepath.Join(g.dir, "config.toml"))
	must(t, err)
	want := `# my settings: keep this comment
tabs = "rail"   # side

[keys]
# my bindings
new_tab = "ctrl+alt+t"  # muscle memory
switcher = "ctrl+shift+space"
zoom = "none"  # was "f12"

[[agents]]
name = "aider"
processes = ["aider"]
`
	if string(got) != want {
		t.Errorf("config.toml:\n%s\nwant:\n%s", got, want)
	}
	if g.Reloads < 1 {
		t.Errorf("the saved file was not reloaded (%d reloads)", g.Reloads)
	}
}

// TestGUIQuitIdle: Ctrl+Shift+Q with nothing running quits at once.
func TestGUIQuitIdle(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		args:   []string{"-size", "80x24", "-tab", ""},
		script: []string{"600ms kbd ctrl+shift+q", "3000ms snap still-running"},
	})
	if g.Reason != "quit" || len(g.Snapshots) != 0 {
		t.Errorf("run ended by %q with %d snapshots; want an immediate quit", g.Reason, len(g.Snapshots))
	}
	if end := g.mark("end"); end.TMs-g.ActiveMs > 1500 {
		t.Errorf("quit took until %d ms after the script started", end.TMs-g.ActiveMs)
	}
}

// TestGUIQuitBusy: with a command running, Ctrl+Shift+Q and the window's ×
// ask first, listing the tab and its process; Return picks Cancel, the
// default; clicking Quit quits.
func TestGUIQuitBusy(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		args: []string{"-size", "80x24", "-tab", ""},
		script: []string{
			"300ms maximize",
			"800ms type sleep 30\\r",
			"1600ms kbd ctrl+shift+q",
			"1900ms snap asked",
			"2000ms kbd Return",
			"2300ms snap cancelled",
			"2400ms ptr-at closewin",
			"2500ms ptr-click",
			"2900ms snap asked-again",
			"3000ms ptr-at button Quit",
			"3100ms ptr-click",
			"6000ms snap not-quit",
		},
	})
	for _, label := range []string{"asked", "asked-again"} {
		if s := g.snap(label); s.Quit == nil || !slices.Equal(s.Quit.Body, []string{"Tab 1: sleep"}) {
			t.Errorf("%s: quit dialog %+v, want it to list Tab 1: sleep", label, s.Quit)
		}
	}
	if s := g.snap("cancelled"); s.Quit != nil {
		t.Errorf("Return did not pick Cancel: %+v", s.Quit)
	}
	if g.Reason != "quit" || len(g.Snapshots) != 3 {
		t.Errorf("run ended by %q after %d snapshots; want Quit after the third", g.Reason, len(g.Snapshots))
	}
}

// TestGUINotify: an agent in a real pane reports through "bunk-gui notify"
// (the hook CLI and the notify socket): needs-you shows on its tab
// (report and pixels), next_needs_you acknowledges it; done shows a ✓
// done dot, next_needs_you visits it after needs-you tabs and clears it.
func TestGUINotify(t *testing.T) {
	t.Parallel()
	bin := guiBinary(t)
	g := runGUI(t, scenario{
		args: []string{"-size", "100x30", "-tab", ""},
		env:  []string{"PATH=" + filepath.Dir(bin) + ":" + os.Getenv("PATH")},
		files: map[string]string{"agent.sh": `bunk-gui notify needs-you "Allow npm install?"
read answer
bunk-gui notify done "Finished the refactor"
sleep 30
`},
		script: []string{
			"500ms kbd ctrl+2",
			"600ms type sh agent.sh\\r",
			"800ms kbd ctrl+1",
			"1500ms snap needs",
			"1550ms shot needs.png",
			"1650ms kbd ctrl+shift+j",
			"2000ms snap acked",
			"2100ms type yes\\r",
			"2400ms kbd ctrl+1",
			"3000ms snap done",
			"3050ms shot done.png",
			"3150ms kbd ctrl+shift+j",
			"3500ms snap done-acked",
			"3600ms quit",
		},
	})
	s := g.snap("needs")
	tb := s.tab(t, 2)
	if tb.State != "needs" || tb.Notice != "Allow npm install?" || !tb.Status.Reported || !tb.Needs || s.SelectedTab != 1 {
		t.Fatalf("after notify needs-you: tab 2 %+v", tb)
	}
	img := loadPNG(t, g.file("needs.png"))
	x, y, w, h := inner(tb.Item, 3)
	if n := count(img, x, y, w, h, colTint, 1); n < w*h/3 {
		t.Errorf("needs-you tint: %d of %d pixels %v", n, w*h, colTint)
	}
	if n := blendCount(img, tb.Item.DX, tb.Item.DY, tb.Item.DW, tb.Item.DH, colNeeds, colTint); n < 4 {
		t.Errorf("needs-you dot: %d pixels %v", n, colNeeds)
	}
	if s := g.snap("acked"); s.SelectedTab != 2 || s.tab(t, 2).State == "needs" {
		t.Errorf("next_needs_you: selected %d, tab 2 %q", s.SelectedTab, s.tab(t, 2).State)
	}
	d := g.snap("done")
	if tb := d.tab(t, 2); tb.State != "done" || tb.Dot != "✓" || tb.Label != "done" || tb.Notice != "Finished the refactor" || d.SelectedTab != 1 {
		t.Errorf("after notify done: tab 2 %+v, selected %d", tb, d.SelectedTab)
	} else {
		img := loadPNG(t, g.file("done.png"))
		if n := blendCount(img, tb.Item.DX, tb.Item.DY, tb.Item.DW, tb.Item.DH, hex(0x7ee2a8), colChrome); n < 4 {
			t.Errorf("done dot: %d pixels #7ee2a8", n)
		}
	}
	if s := g.snap("done-acked"); s.SelectedTab != 2 || s.tab(t, 2).State == "done" {
		t.Errorf("next_needs_you on a done tab: selected %d, tab 2 %q", s.SelectedTab, s.tab(t, 2).State)
	}
}
