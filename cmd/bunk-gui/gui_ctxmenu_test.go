package main

import (
	"slices"
	"strings"
	"testing"
)

// ctxLabels lists a context menu's items as "label" for enabled ones,
// "label (off)" for disabled ones and "[x] label" for checked ones.
func ctxLabels(m *ctxMenuReport) []string {
	if m == nil {
		return nil
	}
	var out []string
	for _, it := range m.Items {
		l := it.Label
		if it.Checked {
			l = "[x] " + l
		}
		if !it.Enabled {
			l += " (off)"
		}
		out = append(out, l)
	}
	return out
}

// TestGUITabMenu: a right-click on a tab's ring in the collapsed rail
// opens the tab's menu without selecting the tab. Mute notifications
// keeps a muted tab's done from notifying; Mark as seen clears a done and
// withdraws its notification; Copy directory copies the pane's directory;
// Rename… opens the rename dialog for that tab. Close other tabs asks
// while one of them runs a command: Cancel keeps every tab, Close leaves
// the tab alone and selected.
func TestGUITabMenu(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost: 3,
		args: []string{"--size", "100x30", "--tab", "", "--tab", ""},
		script: []string{
			"maximize", "wait window.maximized=true window.w>1800",
			"wait tabs.#=3 selected_tab=1 focused.line=$ tab.3.status.process=sh",
			"ptr-at tab 2", "ptr-click 3",
			"wait ctx_menu.kind=tab ctx_menu.tab=2 selected_tab=1",
			"snap menu",
			"ctx-item Mute notifications",
			"wait ctx_menu=null tab.2.muted=true",
			"status tab=2 agent=claude state=done",
			"wait tab.2.state=done",
			"status tab=3 agent=claude state=done",
			"wait tab.3.state=done tab.3.notified=true",
			"snap muted",
			"ctx-tab 2",
			"wait ctx_menu.tab=2",
			"snap muted-menu",
			"ctx-item Mute notifications",
			"wait ctx_menu=null tab.2.muted=false",
			"ctx-tab 3",
			"wait ctx_menu.tab=3",
			"snap seen-menu",
			"ctx-item Mark as seen",
			"wait ctx_menu=null tab.3.state=idle tab.3.notified=false selected_tab=1",
			"snap seen",
			"ctx-tab 2",
			"ctx-item Copy directory",
			"wait last_toast~Copied",
			"snap copied",
			"ctx-tab 2",
			"ctx-item Rename…",
			"wait rename.tab=2 rename.ready=true selected_tab=1",
			"kbd escape",
			"wait rename=null",
			"status-clear tab=3",
			"select-tab 3",
			"wait selected_tab=3 focused.line=$ tab.3.status.process=sh",
			"type sleep 100\\r",
			"wait tab.3.status.process=sleep",
			"select-tab 1",
			"wait selected_tab=1",
			"ctx-tab 1",
			"ctx-item Close other tabs",
			"wait close_confirm.#=1",
			"snap confirm",
			"close-dialog cancel",
			"wait close_confirm.#=0 tabs.#=3",
			"ctx-tab 1",
			"ctx-item Close other tabs",
			"wait close_confirm.#=1",
			"close-dialog close",
			"wait close_confirm.#=0 tabs.#=1 selected_tab=1",
			"snap closed",
			"ctx-tab 1",
			"wait ctx_menu.tab=1",
			"snap alone",
			"ctx-close",
			"wait ctx_menu=null",
			"quit",
		},
	})
	want := []string{"Rename…", "Copy directory", "Mark as seen (off)", "Mute notifications", "Show documents (off)", "Close tab", "Close other tabs"}
	if got := ctxLabels(g.snap("menu").CtxMenu); !slices.Equal(got, want) {
		t.Errorf("tab menu %q, want %q", got, want)
	}
	if got := ctxLabels(g.snap("muted-menu").CtxMenu); !slices.Contains(got, "[x] Mute notifications") || !slices.Contains(got, "Mark as seen") {
		t.Errorf("muted tab's menu %q, want Mute checked and Mark as seen enabled", got)
	}
	s := g.snap("muted")
	if !s.tab(t, 2).Muted || !strings.Contains(s.tab(t, 2).Tooltip, "notifications muted") {
		t.Errorf("muted tab 2: muted %v, tooltip %q", s.tab(t, 2).Muted, s.tab(t, 2).Tooltip)
	}
	muted := s.tab(t, 2).Panes[0]
	for _, r := range g.Notifications {
		if r.Pane == muted && r.Op == "send" {
			t.Errorf("muted tab's pane %d sent %+v", muted, r)
		}
	}
	if got := ctxLabels(g.snap("seen-menu").CtxMenu); !slices.Contains(got, "Mark as seen") {
		t.Errorf("done tab's menu %q, want Mark as seen enabled", got)
	}
	if s := g.snap("copied"); !strings.HasPrefix(s.LastToast, "Copied /") {
		t.Errorf("copy directory toast %q", s.LastToast)
	}
	if s := g.snap("confirm"); len(s.CloseConfirm) != 1 || s.CloseConfirm[0] != "Tab 3: sleep" {
		t.Errorf("close dialog lists %q, want Tab 3: sleep", s.CloseConfirm)
	}
	if got := ctxLabels(g.snap("alone").CtxMenu); !slices.Contains(got, "Close other tabs (off)") {
		t.Errorf("only tab's menu %q, want Close other tabs disabled", got)
	}
}

// TestGUIPaneMenu: a right-click in a pane opens its menu, with Copy
// disabled until text is selected and the link under the pointer first.
// Clear scrollback drops the scrollback; Send all keys to terminal turns
// passthrough on; Split pane splits and Close pane closes. While the
// program reports the mouse, a right-click goes to it and
// Shift+right-click opens the menu.
func TestGUIPaneMenu(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost: 3,
		args: []string{"--size", "80x24"},
		script: []string{
			"wait tabs.#=1 focused.line=$",
			"type seq 100; echo see https://example.com/x here\\r",
			"wait focused.screen~example.com focused.scrollback>0 focused.line=$",
			"click 0 0 3",
			"wait ctx_menu.kind=pane ctx_menu.pane=0",
			"snap plain",
			"ctx-item Clear scrollback",
			"wait ctx_menu=null focused.scrollback=0",
			"click 8 22 3", // the link, on the row above the prompt
			"wait ctx_menu.kind=pane",
			"snap link",
			"ctx-item Open link",
			"wait run.opened.#=1 ctx_menu=null",
			"click 0 0 3",
			"ctx-item Send all keys to terminal",
			"wait ctx_menu=null focused.passthrough=true",
			"click 0 0 3",
			"snap pass-menu",
			"ctx-item Send all keys to terminal",
			"wait focused.passthrough=false",
			"click 0 0 3",
			"ctx-item Split pane",
			"wait panes.#=2",
			"type printf '\\\\033[?1000h'\\r",
			"wait focused.line=$",
			"click 0 0 3",
			"snap mouse-app",
			"click 0 0 3 shift",
			"wait ctx_menu.kind=pane",
			"ctx-item Close pane",
			"wait panes.#=1 ctx_menu=null",
			"quit",
		},
	})
	want := []string{"Copy (off)", "Paste", "Select all", "Find…", "Split pane", "Zoom pane (off)",
		"Send all keys to terminal", "Copy directory", "Open directory", "Clear scrollback", "Close pane"}
	if got := ctxLabels(g.snap("plain").CtxMenu); !slices.Equal(got, want) {
		t.Errorf("pane menu %q, want %q", got, want)
	}
	if got := ctxLabels(g.snap("link").CtxMenu); len(got) < 2 || got[0] != "Open link" || got[1] != "Copy link address" {
		t.Errorf("menu over a link %q, want the link's items first", got)
	}
	if len(g.Opened) != 1 || g.Opened[0] != "https://example.com/x" {
		t.Errorf("opened %q, want the link", g.Opened)
	}
	if got := ctxLabels(g.snap("pass-menu").CtxMenu); !slices.Contains(got, "[x] Send all keys to terminal") {
		t.Errorf("menu while passing keys %q, want the item checked", got)
	}
	if m := g.snap("mouse-app").CtxMenu; m != nil {
		t.Errorf("right-click while the program reports the mouse opened %+v", m)
	}
}

// TestGUISwitcherCurrent: the switcher marks the selected tab's row as
// current. When that tab sorts first (it needs the user), the cursor
// starts on the next row, so the switcher's key and Enter go to another
// tab; a filter that matches only the selected tab puts the cursor on it.
func TestGUISwitcherCurrent(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost:   2,
		config: "tabs = \"rail\"\nrail_open = true\n",
		args:   []string{"--size", "100x30", "--tab", "", "--tab", ""},
		script: []string{
			"wait tabs.#=3 selected_tab=1",
			"status tab=1 agent=claude state=needs repo=api branch=main",
			"status tab=2 agent=codex state=working repo=web branch=dev",
			"wait tab.1.state=needs tab.2.state=working",
			"kbd ctrl+shift+o",
			"wait switcher.open=true keyboard_focus=switcher",
			"snap open",
			"shot switcher.png",
			"kbd-type api",
			"wait switcher.filter=api switcher.rows.#=1",
			"snap filtered",
			"kbd BackSpace BackSpace BackSpace",
			"wait switcher.rows.#=3",
			"kbd Return",
			"wait switcher.open=false selected_tab=2",
			"quit",
		},
	})
	if s := g.snap("open").Switcher; !slices.Equal(s.Rows, []int{1, 2, 3}) || s.Current != 1 || s.Cursor != 2 {
		t.Errorf("switcher %+v; want rows 1 2 3, tab 1 current, cursor on tab 2", s)
	}
	if s := g.snap("filtered").Switcher; s.Current != 1 || s.Cursor != 1 {
		t.Errorf("filtered to the selected tab: %+v; want the cursor on it", s)
	}
}

// TestGUISwitcherClose: a row's × closes its tab and leaves the switcher
// open, with the filter field keeping the keyboard. Closing the tab under
// the cursor puts the cursor on the row that took its place; closing the
// selected tab selects another, which the switcher marks as current.
func TestGUISwitcherClose(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost:   2,
		config: "tabs = \"rail\"\nrail_open = true\n",
		args:   []string{"--size", "100x30", "--tab", "", "--tab", "", "--tab", ""},
		script: []string{
			"maximize", "wait window.maximized=true window.w>1800",
			"wait tabs.#=4 selected_tab=1 focused.line=$",
			"kbd ctrl+shift+o",
			"wait switcher.open=true keyboard_focus=switcher",
			"snap open",
			"ptr-at swclose 2", "ptr-click",
			"wait tabs.#=3 switcher.rows.#=3",
			"snap closed-cursor",
			"ptr-at swclose 1", "ptr-click",
			"wait tabs.#=2 switcher.rows.#=2 keyboard_focus=switcher",
			"snap closed-selected",
			"kbd-type x",
			"wait switcher.filter=x",
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
	open := g.snap("open")
	if s := open.Switcher; s.Cursor != 2 || len(s.Close) != 4 || slices.Contains(s.Close, nil) {
		t.Fatalf("open: %+v; want the cursor on tab 2 and a close button on every row", s)
	}
	c := g.snap("closed-cursor")
	if s := c.Switcher; !slices.Equal(ids(c), []int{0, 2, 3}) || s.Cursor != 2 || s.Current != 1 || c.KeyboardFocus != "switcher" {
		t.Errorf("after closing the cursor's tab: tabs %v, %+v, focus %q; want tab 2 gone, the cursor on the next tab",
			ids(c), s, c.KeyboardFocus)
	}
	sel := g.snap("closed-selected")
	if s := sel.Switcher; !slices.Equal(ids(sel), []int{2, 3}) || sel.SelectedTab != 1 || s.Current != 1 || !slices.Equal(s.Rows, []int{1, 2}) {
		t.Errorf("after closing the selected tab: tabs %v, selected %d, %+v; want the next tab selected and marked current",
			ids(sel), sel.SelectedTab, s)
	}
}
