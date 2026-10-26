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

// TestGUIKeysConfig: [keys] rebinds and disables actions, disabled,
// rebound-away and unbound keys reach the terminal (the bytes the pane
// reads are checked),
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
			keylogSteps(0, "ctrl+shift+o 0f", "f2 1b4f51", "alt+a 1b61", "ctrl+j 0a", "ctrl+b 02", "ctrl+space 00", "ctrl+shift+t 14")...),
			"kbd f9",
			"wait last_toast=\"No agent needs you\" input",
			"snap keylog",
			"kbd ctrl+alt+n",
			"wait tabs.#=2",
			"snap rebound",
			"kbd alt+1",
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
		keys(s, "quit") != "ctrl+q,ctrl+shift+q" || s.ConfigError != "" {
		t.Errorf("keys from the config: %v; config error %q", s.Keys, s.ConfigError)
	}
	// Disabled (switcher: Ctrl+Shift+O), rebound-away (F2, Alt+A) and
	// unbound (Ctrl+J, B, Space, Shift+T) keys go to the terminal; F9 runs
	// next_needs_you.
	lines := keylogLines(g.snap("keylog").pane(t, 0))
	want := []string{"0f", "1b4f51", "1b61", "0a", "02", "00", "14"}
	t.Logf("bytes the pane read: %q", lines)
	if !slices.Equal(lines, want) {
		t.Errorf("the pane read %q, want %q (Ctrl+Shift+O, F2, Alt+A, Ctrl+J, Ctrl+B, Ctrl+Space, Ctrl+Shift+T)", lines, want)
	}
	if s := g.snap("keylog"); len(s.Tabs) != 1 || s.Switcher.Open || s.LastToast != "No agent needs you" {
		t.Errorf("after the keys: %d tabs, switcher open %v, toast %q (F9 is next_needs_you)", len(s.Tabs), s.Switcher.Open, s.LastToast)
	}
	if s := g.snap("rebound"); len(s.Tabs) != 2 {
		t.Errorf("Ctrl+Alt+N (new_tab): %d tabs", len(s.Tabs))
	}
	r := g.snap("reloaded")
	if g.Reloads < 1 || keys(r, "switcher") != "ctrl+alt+s" || keys(r, "zoom") != "" || keys(r, "new_tab") != "f2" || r.ConfigError != "" {
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

// passKeys are default bindings, each with the bytes it sends when it
// reaches the terminal.
var passKeys = []string{
	"f2 1b4f51", "ctrl+right 1b5b313b3543", "ctrl+left 1b5b313b3544", "alt+1 1b31", "alt+0 1b30",
	"ctrl+shift+pgup 1b5b353b367e", "ctrl+shift+pgdn 1b5b363b367e", "ctrl+shift+o 0f", "alt+a 1b61",
	"ctrl+f 06", "ctrl+shift+f 06", "ctrl+n 0e", "ctrl+p 10", "ctrl+c 03", "ctrl+shift+c 03",
	"ctrl+v 16", "ctrl+shift+v 16", "ctrl+shift+a 01", "ctrl+q 11", "ctrl+shift+q 11",
	"f10 1b5b32317e", "f11 1b5b32337e", "f12 1b5b32347e", "ctrl+shift+question 7f",
}

// TestGUIPassthrough: Ctrl+F12 sends every other key to the terminal for
// the focused pane, with a PASS badge on the pane and its tab (pixels and
// report); the default app shortcuts then reach the PTY and none runs; the
// options menu's check item does the same.
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
			keylogSteps(0, passKeys...)...),
			"wait input",
			"snap keys",
			"kbd ctrl+f12",
			"wait pane.0.passthrough=false tab.1.pass=false menu_pass_check=false",
			"snap off",
			"shot off.png",
			"kbd f2",
			"wait tabs.#=3",
			"snap new-tab",
			"kbd alt+1",
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
	// Every default key reached the PTY.
	lines := keylogLines(g.snap("keys").pane(t, 0))
	t.Logf("bytes the pane read: %q", lines)
	var want []string
	for _, k := range passKeys {
		_, hex, _ := strings.Cut(k, " ")
		want = append(want, hex)
	}
	if !slices.Equal(lines, want) {
		t.Errorf("with passthrough the pane read %q, want %q", lines, want)
	}
	if s := g.snap("keys"); len(s.Tabs) != 2 || s.SelectedTab != 1 || s.tab(t, 1).ID != 0 || s.Quit != nil ||
		s.Switcher.Open || s.MenuOpen || s.Find != nil || s.Window.Fullscreen || s.Shortcuts != nil ||
		s.pane(t, 0).Selection != "" || s.pane(t, 0).Scroll != 0 {
		t.Errorf("with passthrough an app shortcut ran: %d tabs, selected %d, tab 1 id %d, quit dialog %v, switcher %v, "+
			"menu %v, find %v, fullscreen %v, shortcuts %v, selection %q", len(s.Tabs), s.SelectedTab, s.tab(t, 1).ID,
			s.Quit, s.Switcher.Open, s.MenuOpen, s.Find, s.Window.Fullscreen, s.Shortcuts, s.pane(t, 0).Selection)
	}
	if s := g.snap("off"); s.pane(t, 0).Pass || s.tab(t, 1).Pass || s.MenuPassCheck {
		t.Errorf("passthrough off: still on")
	}
	if s := g.snap("new-tab"); len(s.Tabs) != 3 {
		t.Errorf("F2 after passthrough: %d tabs", len(s.Tabs))
	}
	if s := g.snap("menu"); !s.pane(t, 0).Pass || !s.MenuPassCheck || s.MenuOpen {
		t.Errorf("menu check item: pane 0 pass %v, check %v, menu open %v", s.pane(t, 0).Pass, s.MenuPassCheck, s.MenuOpen)
	}
	if g.Reason != "script" {
		t.Errorf("run ended by %q", g.Reason)
	}
}

// TestGUITabKeys presses the default tab and window keys: Alt+1…0 select
// tabs, Ctrl+Left/Right switch with wrap-around, F2 opens a tab,
// Ctrl+Shift+PgUp/PgDn move the selected tab (the report lists the new
// order), Ctrl+Shift+O opens and closes the switcher, Alt+A jumps to a tab
// needing you, F10 opens the options menu and F11 toggles fullscreen.
func TestGUITabKeys(t *testing.T) {
	t.Parallel()
	args := []string{"--size", "100x30"}
	for range 9 {
		args = append(args, "--tab", "")
	}
	g := runGUI(t, scenario{
		cost: 2,
		args: args,
		script: []string{
			"wait tabs.#=10 tab.10.panes.#=1",
			"kbd alt+0",
			"wait selected_tab=10",
			"snap alt0",
			"kbd alt+1",
			"wait selected_tab=1",
			"snap alt1",
			"kbd ctrl+right",
			"wait selected_tab=2",
			"snap next",
			"kbd ctrl+left",
			"wait selected_tab=1",
			"kbd ctrl+left",
			"wait selected_tab=10",
			"snap prev-wrap",
			"kbd f2",
			"wait tabs.#=11 selected_tab=11",
			"snap new-tab",
			"kbd alt+2",
			"wait selected_tab=2",
			"kbd ctrl+shift+pgdn",
			"wait selected_tab=3 tab.3.id=1",
			"snap moved-right",
			"kbd ctrl+shift+pgup ctrl+shift+pgup",
			"wait selected_tab=1 tab.1.id=1",
			"snap moved-left",
			"kbd ctrl+shift+pgup",
			"wait input",
			"snap moved-edge",
			"kbd ctrl+shift+o",
			"wait switcher.open=true keyboard_focus=switcher",
			"snap switcher",
			"kbd ctrl+shift+o",
			"wait switcher.open=false",
			"snap switcher-closed",
			"status tab=5 agent=claude state=needs notice=Approve?",
			"wait tab.5.state=needs",
			"kbd alt+a",
			"wait selected_tab=5 tab.5.state!=needs",
			"snap needs-you",
			"kbd f10",
			"wait menu_open=true",
			"snap menu",
			"kbd Escape",
			"wait menu_open=false",
			"kbd f11",
			"wait window.fullscreen=true",
			"snap fullscreen",
			"kbd f11",
			"wait window.fullscreen=false",
			"snap windowed",
			"quit",
		},
	})
	ids := func(s *uiSnapshot) []int {
		var out []int
		for _, tb := range s.Tabs {
			out = append(out, tb.ID)
		}
		return out
	}
	for label, want := range map[string]int{"alt0": 10, "alt1": 1, "next": 2, "prev-wrap": 10, "new-tab": 11, "needs-you": 5} {
		if s := g.snap(label); s.SelectedTab != want {
			t.Errorf("%s: selected tab %d, want %d", label, s.SelectedTab, want)
		}
	}
	if s := g.snap("new-tab"); len(s.Tabs) != 11 || s.tab(t, 11).ID != 10 {
		t.Errorf("F2: %d tabs, order %v", len(s.Tabs), ids(s))
	}
	for label, want := range map[string][]int{
		"moved-right": {0, 2, 1, 3, 4, 5, 6, 7, 8, 9, 10},
		"moved-left":  {1, 0, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		"moved-edge":  {1, 0, 2, 3, 4, 5, 6, 7, 8, 9, 10},
	} {
		s := g.snap(label)
		if got := ids(s); !slices.Equal(got, want) || s.tab(t, s.SelectedTab).ID != 1 {
			t.Errorf("%s: tab order %v, selected %d; want %v with tab id 1 selected", label, got, s.SelectedTab, want)
		}
	}
	if s := g.snap("switcher"); !s.Switcher.Open || len(s.Switcher.Rows) != 11 {
		t.Errorf("Ctrl+Shift+O: switcher %+v", s.Switcher)
	}
	if s := g.snap("switcher-closed"); s.Switcher.Open || s.SelectedTab != 1 {
		t.Errorf("Ctrl+Shift+O again: switcher open %v, selected %d", s.Switcher.Open, s.SelectedTab)
	}
	if s := g.snap("needs-you"); s.tab(t, 5).State == "needs" {
		t.Errorf("Alt+A: tab 5 still %q", s.tab(t, 5).State)
	}
	if s := g.snap("menu"); !s.MenuOpen {
		t.Error("F10: the options menu is closed")
	}
	if s := g.snap("fullscreen"); !s.Window.Fullscreen {
		t.Errorf("F11: window %+v", s.Window)
	}
	if s := g.snap("windowed"); s.Window.Fullscreen || s.MenuOpen {
		t.Errorf("F11 again: window %+v, menu open %v", s.Window, s.MenuOpen)
	}
}

// TestGUITextKeys presses the default text keys on a pane that prints the
// bytes it reads: Ctrl+C with a selection copies and ends it without
// reaching the program, Ctrl+C without one sends ^C, Ctrl+Shift+C without
// one sends nothing, Ctrl+V pastes, Ctrl+Shift+A selects all text onto the
// primary selection, Ctrl+F and Ctrl+Shift+F open find, Ctrl+N and Ctrl+P
// step through matches while the bar has the keyboard and reach the
// program once it is closed, and Ctrl+V in the bar pastes into the bar.
func TestGUITextKeys(t *testing.T) {
	t.Parallel()
	const pasted = "7061737465642d74657874" // "pasted-text"
	g := runGUI(t, scenario{
		args:  []string{"--size", "100x30", "--command", "python3 keylog.py"},
		files: map[string]string{"keylog.py": keylog},
		script: []string{
			"wait pane.0.text~\"keylog ready\"",
			"dclick 0 0",
			"wait primary keylog pane.0.selection=keylog",
			"kbd ctrl+c",
			"wait clipboard keylog pane.0.selection=\"\" pane.0.badges.0.text=\" COPIED \"",
			"snap copied",
			"kbd ctrl+c",
			"wait pane.0.screen~\"keylog ready\\n03\"",
			"kbd ctrl+shift+c",
			"clip pasted-text",
			"wait clipboard pasted-text",
			"kbd ctrl+v",
			"wait pane.0.screen~\"keylog ready\\n03\\n" + pasted + "\"",
			"snap pasted",
			"kbd ctrl+shift+a",
			"wait primary \"keylog ready\\n03\\n" + pasted + "\"",
			"snap all",
			"kbd ctrl+f",
			"wait find.focused=true",
			"kbd-type 7",
			"wait find.query=7 find.busy=false find.total=6",
			"snap find",
			"kbd ctrl+n",
			"wait find.index=0",
			"snap find-next",
			"kbd ctrl+p",
			"wait find.index=5",
			"snap find-prev",
			"clip needle",
			"wait clipboard needle",
			"kbd ctrl+v",
			"wait find.query=7needle find.busy=false",
			"snap find-paste",
			"kbd Escape",
			"wait find=null keyboard_focus=\"pane 0\"",
			"kbd ctrl+shift+f",
			"wait find.focused=true",
			"snap find-shift",
			"kbd Escape",
			"wait find=null keyboard_focus=\"pane 0\"",
			"kbd ctrl+n",
			"wait pane.0.screen~\"" + pasted + "\\n0e\"",
			"kbd ctrl+p",
			"wait pane.0.screen~\"" + pasted + "\\n0e\\n10\"",
			"snap keys",
			"quit",
		},
	})
	if s := g.snap("copied"); s.Clipboard == nil || *s.Clipboard != "keylog" || s.pane(t, 0).Selection != "" {
		t.Errorf("Ctrl+C with a selection: clipboard %q, selection %q", strp(s.Clipboard), s.pane(t, 0).Selection)
	}
	// Only the second Ctrl+C (no selection) and the keys pressed with the
	// find bar closed reached the program; Ctrl+V sent the clipboard.
	if lines := keylogLines(g.snap("keys").pane(t, 0)); !slices.Equal(lines, []string{"03", pasted, "0e", "10"}) {
		t.Errorf("the pane read %q, want ^C, the paste, ^N and ^P", lines)
	}
	all := "keylog ready\n03\n" + pasted
	if s := g.snap("all"); s.Primary == nil || *s.Primary != all || s.pane(t, 0).Selection != all {
		t.Errorf("Ctrl+Shift+A: primary %q, selection %q; want %q", strp(s.Primary), s.pane(t, 0).Selection, all)
	}
	if f := g.snap("find").Find; f == nil || !f.Focused || f.Index != 5 {
		t.Errorf("Ctrl+F: find bar %+v; want it focused on the last of 6 matches", f)
	}
	if f := g.snap("find-next").Find; f == nil || f.Index != 0 {
		t.Errorf("Ctrl+N: find bar %+v", f)
	}
	if f := g.snap("find-prev").Find; f == nil || f.Index != 5 {
		t.Errorf("Ctrl+P: find bar %+v", f)
	}
	if f := g.snap("find-paste").Find; f == nil || f.Query != "7needle" || f.Total != 0 {
		t.Errorf("Ctrl+V in the find bar: %+v", f)
	}
	if f := g.snap("find-shift").Find; f == nil || !f.Focused || f.Query != "" {
		t.Errorf("Ctrl+Shift+F: find bar %+v", f)
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
			"wait shortcuts.rows.switcher.enabled=true keys.switcher=ctrl+shift+o",
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
	// new_tab is Ctrl+Shift+T in this config.
	for action, want := range map[string]bool{"zoom": true, "split": true, "select_tab_1": true, "nav_left": true,
		"select_tab_10": true, "next_tab": true, "prev_tab": true, "next_needs_you": true, "search": true,
		"search_next": true, "search_prev": true, "copy": true, "paste": true, "quit": true, "menu": true,
		"toggle_fullscreen": true, "shortcuts": true,
		"new_tab": false, "switcher": false, "passthrough": false, "scroll_up": false, "move_tab_left": false,
		"move_tab_right": false, "select_all": false, "close_tab": false, "search_exit": false} {
		if r := row(open, action); r.Collides != want {
			t.Errorf("%s: collides %v, want %v", action, r.Collides, want)
		}
	}
	for action, want := range map[string]string{"new_tab": "Ctrl+Shift+T", "next_tab": "Ctrl+Right",
		"move_tab_left": "Ctrl+Shift+PgUp", "select_tab_10": "Alt+0", "switcher": "Ctrl+Shift+O",
		"next_needs_you": "Alt+A", "search": "Ctrl+F / Ctrl+Shift+F", "search_next": "Ctrl+N / Enter",
		"copy": "Ctrl+Shift+C / Ctrl+C", "paste": "Ctrl+Shift+V / Ctrl+V", "select_all": "Ctrl+Shift+A",
		"toggle_fullscreen": "F11", "menu": "F10", "shortcuts": "Ctrl+Shift+? / Ctrl+?", "quit": "Ctrl+Q / Ctrl+Shift+Q"} {
		if r := row(open, action); r.Keys != want {
			t.Errorf("%s: row shows %q, want %q", action, r.Keys, want)
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
	if s := g.snap("conflict"); keys(s, "copy") != "ctrl+shift+c,ctrl+c" || !strings.Contains(s.Shortcuts.Message, "already used by New tab") {
		t.Errorf("a key in use: copy %q, message %q", keys(s, "copy"), s.Shortcuts.Message)
	}
	if s := g.snap("on-again"); keys(s, "switcher") != "ctrl+shift+o" || !row(s, "switcher").Enabled {
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
switcher = "ctrl+shift+o"
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

// TestGUIQuitIdle: Ctrl+Q with nothing running quits at once.
func TestGUIQuitIdle(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		args:   []string{"--size", "80x24", "--tab", ""},
		script: []string{"kbd ctrl+q", "sleep 1500ms", "snap still-running"},
	})
	if g.Reason != "quit" || len(g.Snapshots) != 0 {
		t.Errorf("run ended by %q with %d snapshots; want an immediate quit", g.Reason, len(g.Snapshots))
	}
	if end := g.mark("end"); end.TMs-g.ActiveMs > 1500 {
		t.Errorf("quit took until %d ms after the script started", end.TMs-g.ActiveMs)
	}
}

// TestGUIQuitBusy: with a command running, Ctrl+Q, Ctrl+Shift+Q and the
// window's × ask first, listing the tab and its process; Return picks
// Cancel, the default; clicking Quit quits.
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
			"kbd ctrl+q",
			"wait quit_dialog!=null keyboard_focus=other",
			"snap asked",
			"kbd Return",
			"wait quit_dialog=null",
			"snap cancelled",
			"kbd ctrl+shift+q",
			"wait quit_dialog!=null keyboard_focus=other",
			"snap asked-shift",
			"kbd Return",
			"wait quit_dialog=null",
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
	for _, label := range []string{"asked", "asked-shift", "asked-again"} {
		if s := g.snap(label); s.Quit == nil || !slices.Equal(s.Quit.Body, []string{"Tab 1: sleep"}) {
			t.Errorf("%s: quit dialog %+v, want it to list Tab 1: sleep", label, s.Quit)
		}
	}
	if s := g.snap("cancelled"); s.Quit != nil {
		t.Errorf("Return did not pick Cancel: %+v", s.Quit)
	}
	if g.Reason != "quit" || len(g.Snapshots) != 4 {
		t.Errorf("run ended by %q after %d snapshots; want Quit after the fourth", g.Reason, len(g.Snapshots))
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
			"kbd alt+2",
			"wait selected_tab=2 focused.line=$",
			"type sh agent.sh\\r",
			"kbd alt+1",
			"wait selected_tab=1 tab.2.state=needs",
			"snap needs",
			"shot needs.png",
			"kbd alt+a",
			"wait selected_tab=2 tab.2.state!=needs",
			"snap acked",
			"type yes\\r",
			"wait tab.2.state=done",
			"kbd alt+1",
			"wait selected_tab=1",
			"snap done",
			"shot done.png",
			"kbd alt+a",
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
