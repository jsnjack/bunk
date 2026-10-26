package main

// Window scenarios for key bindings, passthrough, the shortcuts window,
// quitting and agent hook reports.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"bunk/internal/badges"
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

// keylogSteps presses each key of "KEY HEX" pairs in turn and waits until
// the keylog pane (pane ID) has printed its bytes after those before.
func keylogSteps(pane int, keys ...string) []string {
	var out []string
	seen := "keylog ready"
	for _, k := range keys {
		key, hex, _ := strings.Cut(k, " ")
		seen += "\\n" + hex
		out = append(out, "kbd "+key, fmt.Sprintf("wait pane.%d.screen~\"%s\"", pane, seen))
	}
	return out
}

// TestGUIKeysConfig: [keys] rebinds and disables actions, disabled and
// freed keys reach the terminal (the bytes the pane reads are checked),
// edits to config.toml apply live, and bad bindings produce the Settings
// toast naming them.
func TestGUIKeysConfig(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		config: "# test config\n[keys]\nnew_tab = \"ctrl+alt+n\"   # rebound\nswitcher = \"none\"\nnext_needs_you = [\"ctrl+shift+j\", \"f9\"]\n",
		args:   []string{"--size", "100x30", "--command", "python3 keylog.py"},
		files: map[string]string{
			"keylog.py": keylog,
			"next.toml": "tabs = \"bar\"\nfont_size = 12\n\n[keys]\nswitcher = \"ctrl+alt+s\"\nzoom = \"none\"\n",
			"bad.toml":  "[keys]\nfrobnicate = \"f3\"\nsplit = \"ctrl+banana\"\nswitcher = \"ctrl+alt+s\"\ncopy = \"ctrl+alt+s\"\n",
		},
		script: append(append([]string{
			"wait pane.0.text~\"keylog ready\"",
			"snap start",
		},
			// One key at a time, so the pane reads each in a chunk of its own.
			keylogSteps(0, "ctrl+shift+space 00", "ctrl+j 0a", "ctrl+b 02", "ctrl+space 00", "ctrl+q 11", "ctrl+shift+t 14")...),
			"kbd f9",
			"wait last_toast=\"No agent needs you\" input",
			"snap keylog",
			"kbd ctrl+alt+n",
			"wait tabs.#=2",
			"snap rebound",
			"kbd ctrl+1",
			"wait selected_tab=1",
			"run cp next.toml config.toml",
			"wait run.reloads>=1 keys.switcher=ctrl+alt+s layout=bar font=\"Noto Sans Mono 12\"",
			"snap reloaded",
			"kbd ctrl+alt+s",
			"wait switcher.open=true",
			"snap switcher",
			"switcher close",
			"wait switcher.open=false",
			"run cp bad.toml config.toml",
			"wait last_toast~frobnicate last_toast~\"ctrl+alt+s is bound to both switcher and copy\"",
			"snap bad",
			"quit",
		),
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
		args:  []string{"--size", "100x30", "--command", "python3 keylog.py", "--tab", ""},
		files: map[string]string{"keylog.py": keylog},
		script: append(append([]string{
			"wait pane.0.text~\"keylog ready\"",
			"kbd ctrl+f12",
			"wait pane.0.passthrough=true tab.1.pass=true menu_pass_check=true",
			"snap on",
			"shot on.png",
		},
			keylogSteps(0, "ctrl+shift+t 14", "ctrl+shift+q 11", "f12 1b5b32347e", "ctrl+shift+space 00")...),
			"wait input",
			"snap keys",
			"kbd ctrl+f12",
			"wait pane.0.passthrough=false tab.1.pass=false menu_pass_check=false",
			"snap off",
			"shot off.png",
			"kbd ctrl+shift+t",
			"wait tabs.#=3",
			"snap new-tab",
			"kbd ctrl+1",
			"wait selected_tab=1",
			"menu open",
			"wait menu_open=true",
			"menu-check",
			"wait pane.0.passthrough=true menu_pass_check=true menu_open=false",
			"snap menu",
			"quit",
		),
	})
	on := g.snap("on")
	p := on.pane(t, 0)
	if !p.Pass || len(p.Badges) == 0 || p.Badges[0].Role != "pass" || p.Badges[0].Rect == nil ||
		!on.tab(t, 1).Pass || on.tab(t, 2).Pass || !on.MenuPassCheck {
		t.Fatalf("passthrough on: pane %+v, tab 1 pass %v, tab 2 pass %v, menu check %v", p, on.tab(t, 1).Pass, on.tab(t, 2).Pass, on.MenuPassCheck)
	}
	// The PASS badge is the first of the pane's badge row (badge.go).
	_, bg, _ := badgeLook(&testTheme, badges.RolePass)
	badge := hex(bg)
	img := loadPNG(t, g.file("on.png"))
	x, y, w, h := inner(p.Badges[0].Rect, 1)
	if n := count(img, x, y, w, h, badge, 2); n < w*h/3 {
		t.Errorf("PASS badge on the pane: %d of %d pixels %v", n, w*h, badge)
	}
	tag := hex(0xe6e8eb)
	it := on.tab(t, 1).Item
	if n := count(img, it.DX, it.DY, it.DW, it.DH, tag, 2); n < 20 {
		t.Errorf("PASS tag on the tab: %d pixels %v", n, tag)
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
		cost:   2,
		config: cfg,
		args:   []string{"--size", "100x30"},
		script: []string{
			"maximize",
			"wait window.maximized=true window.w>1800",
			"kbd ctrl+shift+question",
			"wait shortcuts!=null",
			"snap open",
			"shortcut-scroll switcher",
			"ptr-at shortcut-switch switcher",
			"ptr-click",
			"wait shortcuts.rows.switcher.enabled=false",
			"snap off",
			"shortcut-scroll new_tab",
			"ptr-at shortcut-keys new_tab",
			"ptr-click",
			"wait shortcuts.capturing=new_tab",
			"snap capturing",
			"kbd ctrl+alt+t",
			"wait keys.new_tab=ctrl+alt+t shortcuts.capturing=\"\"",
			"snap rebound",
			"shortcut-scroll zoom",
			"ptr-at shortcut-keys zoom",
			"ptr-click",
			"wait shortcuts.capturing=zoom",
			"kbd Escape",
			"wait shortcuts.capturing=\"\" input",
			"snap escaped",
			"ptr-click",
			"wait shortcuts.capturing=zoom",
			"kbd BackSpace",
			"wait shortcuts.rows.zoom.enabled=false",
			"snap cleared",
			"shortcut-scroll copy",
			"ptr-at shortcut-keys copy",
			"ptr-click",
			"wait shortcuts.capturing=copy",
			"kbd ctrl+alt+t",
			"wait shortcuts.message~\"already used by New tab\"",
			"snap conflict",
			"shortcut-scroll switcher",
			"ptr-at shortcut-switch switcher",
			"ptr-click",
			"wait shortcuts.rows.switcher.enabled=true keys.switcher=ctrl+shift+space",
			"snap on-again",
			"dialog-close",
			"wait shortcuts=null",
			"kbd ctrl+alt+t",
			"wait tabs.#=2",
			"snap after",
			"wait run.reloads>=1",
			"quit",
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
new_tab = "ctrl+alt+t"   # muscle memory
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
		args:   []string{"--size", "80x24", "--tab", ""},
		script: []string{"kbd ctrl+shift+q", "sleep 1500ms", "snap still-running"},
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
		cost: 2,
		args: []string{"--size", "80x24", "--tab", ""},
		script: []string{
			"maximize",
			"wait window.maximized=true window.w>1800 focused.line=$",
			"type sleep 30\\r",
			"wait focused.busy=sleep",
			"kbd ctrl+shift+q",
			"wait quit_dialog!=null keyboard_focus=other",
			"snap asked",
			"kbd Return",
			"wait quit_dialog=null",
			"snap cancelled",
			"ptr-at closewin",
			"ptr-click",
			"wait quit_dialog!=null",
			"snap asked-again",
			"ptr-at button Quit",
			"ptr-click",
			"sleep 3s", // the click quits: no more snapshots
			"snap not-quit",
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
		cost: 2.5,
		args: []string{"--size", "100x30", "--tab", ""},
		env:  []string{"PATH=" + filepath.Dir(bin) + ":" + os.Getenv("PATH")},
		files: map[string]string{"agent.sh": `bunk-gui notify needs-you "Allow npm install?"
read answer
bunk-gui notify done "Finished the refactor"
sleep 30
`},
		script: []string{
			"kbd ctrl+2",
			"wait selected_tab=2 focused.line=$",
			"type sh agent.sh\\r",
			"kbd ctrl+1",
			"wait selected_tab=1 tab.2.state=needs",
			"snap needs",
			"shot needs.png",
			"kbd ctrl+shift+j",
			"wait selected_tab=2 tab.2.state!=needs",
			"snap acked",
			"type yes\\r",
			"wait tab.2.state=done",
			"kbd ctrl+1",
			"wait selected_tab=1",
			"snap done",
			"shot done.png",
			"kbd ctrl+shift+j",
			"wait selected_tab=2 tab.2.state!=done",
			"snap done-acked",
			"quit",
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
