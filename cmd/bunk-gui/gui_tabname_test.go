package main

import (
	"strings"
	"testing"
)

// TestGUIRenameTab: a double-click on a tab that is not selected selects
// it and opens the rename dialog for it, with the automatic name as the
// placeholder; Enter saves the name, which replaces the process name in
// the tab and leads its tooltip. A bound rename_tab key opens the dialog
// with the name, and Esc keeps it. "Rename tab…" in the options menu
// renames the selected tab, and an empty name gives the automatic one
// back. A status change keeps the name.
func TestGUIRenameTab(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost:   3,
		config: "[keys]\nrename_tab = \"ctrl+shift+e\"\n",
		args:   []string{"--size", "100x30", "--tab", ""},
		script: []string{
			"maximize", "wait window.maximized=true window.w>1800",
			"wait tabs.#=2 selected_tab=1 focused.line=$ tab.2.status.process=sh",
			"ptr-at tab 2", "ptr-click 1 2",
			"wait rename.tab=2 rename.ready=true selected_tab=2 keyboard_focus=rename",
			"snap dialog",
			"kbd-type backend\\r",
			"wait rename=null tab.2.name=backend",
			"snap named",
			"type sleep 1\\r",
			"wait tab.2.status.process=sleep",
			"wait tab.2.status.process=sh",
			"snap after-command",
			"kbd ctrl+shift+e",
			"wait rename.tab=2 rename.ready=true rename.text=backend keyboard_focus=rename",
			"kbd ctrl+a", "kbd-type x",
			"wait rename.text=x",
			"kbd escape",
			"wait rename=null",
			"snap cancelled",
			"menu open",
			"wait menu_open=true",
			"menu-item Rename tab…",
			"wait rename.tab=2 rename.ready=true menu_open=false keyboard_focus=rename",
			"kbd ctrl+a", "kbd backspace", "kbd return",
			`wait rename=null tab.2.name=""`,
			"snap reset",
			"quit",
		},
	})
	if r := g.snap("dialog").Rename; r == nil || r.Text != "" || r.Placeholder != "sh" {
		t.Errorf("rename dialog %+v, want it empty with the placeholder sh", r)
	}
	for _, name := range []string{"named", "after-command", "cancelled"} {
		tab := g.snap(name).tab(t, 2)
		if tab.Name != "backend" || tab.Agent != "sh" || !strings.HasPrefix(tab.Tooltip, "backend · sh · ") {
			t.Errorf("%s: tab 2 name %q, agent %q, tooltip %q; want backend, sh and a tooltip led by both",
				name, tab.Name, tab.Agent, tab.Tooltip)
		}
	}
	if tab := g.snap("reset").tab(t, 2); tab.Name != "" || strings.HasPrefix(tab.Tooltip, "backend") {
		t.Errorf("reset: tab 2 name %q, tooltip %q", tab.Name, tab.Tooltip)
	}
	if tab := g.snap("named").tab(t, 1); tab.Name != "" {
		t.Errorf("tab 1 got the name %q", tab.Name)
	}
}
