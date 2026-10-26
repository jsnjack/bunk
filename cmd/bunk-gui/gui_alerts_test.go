package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// alertAgent writes a fake claude that reports through "bunk-gui notify":
// each line of events runs once the script creates go0, go1, …. It
// returns the PATH for the scenario.
func alertAgent(t *testing.T, events ...string) string {
	t.Helper()
	bin := t.TempDir()
	var b strings.Builder
	b.WriteString("#!/bin/sh\necho fake claude\n")
	for i, ev := range events {
		fmt.Fprintf(&b, "until [ -e \"$BUNK_TEST_DIR/go%d\" ]; do sleep 0.05; done\nbunk-gui notify %s\n", i, ev)
	}
	b.WriteString("sleep 60\n")
	must(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(b.String()), 0o755))
	return "PATH=" + bin + ":" + filepath.Dir(guiBinary(t)) + ":" + os.Getenv("PATH")
}

// alertRepo makes the scenario directory a git work tree on branch
// feature-x: ".git" names the directory itself as the git directory.
var alertRepo = map[string]string{".git": "gitdir: .\n", "HEAD": "ref: refs/heads/feature-x\n"}

// startAlertAgent runs the fake claude in tab 2 and selects tab 1.
var startAlertAgent = []string{
	"kbd ctrl+2",
	"wait selected_tab=2 focused.line=$ tab.2.status.branch=feature-x",
	"type claude\\r",
	"wait tab.2.status.agent=claude",
	"kbd ctrl+1",
	"wait selected_tab=1",
}

// checkRecord compares a notification call in --report with want; Tab,
// TMs and the fields want leaves empty are not compared.
func checkRecord(t *testing.T, g *guiRun, i int, tab int, want alertRecord) {
	t.Helper()
	if i >= len(g.Notifications) {
		t.Errorf("notification %d: none (%d calls: %+v)", i, len(g.Notifications), g.Notifications)
		return
	}
	got := g.Notifications[i]
	want.Tab, want.TMs = tab, got.TMs
	if got != want {
		t.Errorf("notification %d: %+v\nwant %+v", i, got, want)
	}
}

// TestGUINotifications: a fake claude in tab 2 reports through "bunk-gui
// notify" while tab 1 is selected. needs-you sends one notification for
// tab 2, with the agent, notice, repo and branch, and app.show-tab with
// the tab's ID as its action; done replaces it; selecting the tab
// acknowledges the agent and withdraws it. A click on the next one raises
// the window and selects the tab, which withdraws it. The selected tab of
// the active window sends none; with another window focused, it does. On
// the test compositor no call reaches a notification server.
func TestGUINotifications(t *testing.T) {
	t.Parallel()
	path := alertAgent(t, `needs-you "Allow npm install?"`, `done "Finished the refactor"`,
		`needs-you "Run the tests?"`, `done "All green"`, `failed "Out of credits"`)
	script := append(append([]string{}, startAlertAgent...),
		"wait run.notifications.#=0",
		"run touch go0",
		"wait tab.2.state=needs run.notifications.#=1 tab.2.notified=true",
		"snap needs",
		"run touch go1",
		"wait tab.2.state=done run.notifications.#=2",
		"snap done",
		"kbd ctrl+2",
		"wait selected_tab=2 tab.2.state=idle run.notifications.#=3 tab.2.notified=false",
		"kbd ctrl+1",
		"wait selected_tab=1",
		"run touch go2",
		"wait tab.2.state=needs run.notifications.#=4",
		"alert-click 2",
		"wait selected_tab=2 window.active=true run.notifications.#=6 tab.2.notified=false",
		"snap clicked",
		"run touch go3",
		"wait tab.2.state=done",
		"snap watched",
		"decoy open",
		"wait window.active=false",
		"run touch go4",
		"wait tab.2.state=failed run.notifications.#=7",
		"snap inactive",
		"alert-click 2",
		"wait window.active=true run.notifications.#=9",
		"decoy close",
		"quit",
	)
	g := runGUI(t, scenario{
		cost:   3,
		args:   []string{"--size", "100x30", "--tab", "", "-d"},
		env:    []string{path},
		files:  alertRepo,
		script: script,
		limit:  25e9,
	})
	tab2 := g.snap("needs").tab(t, 2)
	if tab2.ID == g.snap("needs").tab(t, 1).ID {
		t.Fatalf("tabs 1 and 2 share ID %d", tab2.ID)
	}
	where := filepath.Base(g.dir) + " ⎇ feature-x"
	checkRecord(t, g, 0, tab2.ID, alertRecord{Op: "send", Title: "claude needs you", Body: "Allow npm install?\n" + where,
		State: "needs-you", Action: "app.show-tab"})
	checkRecord(t, g, 1, tab2.ID, alertRecord{Op: "send", Title: "claude is done", Body: "Finished the refactor\n" + where,
		State: "done", Action: "app.show-tab"})
	checkRecord(t, g, 2, tab2.ID, alertRecord{Op: "withdraw"})
	checkRecord(t, g, 3, tab2.ID, alertRecord{Op: "send", Title: "claude needs you", Body: "Run the tests?\n" + where,
		State: "needs-you", Action: "app.show-tab"})
	checkRecord(t, g, 4, tab2.ID, alertRecord{Op: "activate"})
	checkRecord(t, g, 5, tab2.ID, alertRecord{Op: "withdraw"})
	checkRecord(t, g, 6, tab2.ID, alertRecord{Op: "send", Title: "claude failed", Body: "Out of credits\n" + where,
		State: "failed", Action: "app.show-tab"})
	checkRecord(t, g, 7, tab2.ID, alertRecord{Op: "activate"})
	checkRecord(t, g, 8, tab2.ID, alertRecord{Op: "withdraw"})
	if len(g.Notifications) != 9 {
		t.Errorf("%d notification calls, want 9: %+v", len(g.Notifications), g.Notifications)
	}
	if s := g.snap("done"); !s.tab(t, 2).Notified || s.SelectedTab != 1 {
		t.Errorf("after done: tab 2 notified %v, selected %d", s.tab(t, 2).Notified, s.SelectedTab)
	}
	if s := g.snap("clicked"); s.SelectedTab != 2 || !s.Window.Active || s.tab(t, 2).State != "idle" {
		t.Errorf("after the click: selected %d, window active %v, tab 2 %q", s.SelectedTab, s.Window.Active, s.tab(t, 2).State)
	}
	if s := g.snap("watched"); s.tab(t, 2).Notified || s.SelectedTab != 2 {
		t.Errorf("done in the selected tab of the active window: notified %v, selected %d", s.tab(t, 2).Notified, s.SelectedTab)
	}
	if s := g.snap("inactive"); s.Window.Active || !s.tab(t, 2).Notified {
		t.Errorf("failed while another window has the focus: window active %v, notified %v", s.Window.Active, s.tab(t, 2).Notified)
	}
	if !strings.Contains(g.stderr, `level=DEBUG msg="desktop notifications are recorded, not shown"`) {
		t.Errorf("no record that notifications stay off the notification server on the test compositor")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return string(b)
}

// TestGUINotificationSettings: [notifications] enabled = false sends
// nothing. The preferences window turns notifications on and leaves out
// done; both apply at once and are saved to config.toml. A needs-you seen
// while they were off does not notify later; failed does, and turning
// them off in the file withdraws it.
func TestGUINotificationSettings(t *testing.T) {
	t.Parallel()
	path := alertAgent(t, `needs-you "Allow?"`, `done "Finished"`, `failed "Crashed"`)
	cfg := "# mine\n[notifications]\nenabled = false   # quiet\n"
	script := append(append([]string{"maximize", "wait window.maximized=true window.w>1800"}, startAlertAgent...),
		"run touch go0",
		"wait tab.2.state=needs",
		"snap off",
		"kbd ctrl+comma",
		"wait prefs!=null",
		"snap prefs",
		"pref-scroll notify",
		"ptr-at pref notify", "ptr-click",
		"wait prefs.notify=true run.reloads>=1",
		"pref-scroll notify-done",
		"ptr-at pref notify-done", "ptr-click",
		"wait prefs.notify_states.#=2 run.reloads>=2",
		"dialog-close",
		"wait prefs=null",
		"snap on",
		"run touch go1",
		"wait tab.2.state=done",
		"snap done",
		"run touch go2",
		"wait tab.2.state=failed run.notifications.#=1",
		"run sed -i 's/^enabled = true/enabled = false/' config.toml",
		"wait run.notifications.#=2",
		"quit",
	)
	g := runGUI(t, scenario{
		cost:   3,
		config: cfg,
		args:   []string{"--size", "100x30", "--tab", ""},
		env:    []string{path},
		files:  alertRepo,
		script: script,
		limit:  25e9,
	})
	if s := g.snap("off"); s.tab(t, 2).Notified || s.tab(t, 2).State != "needs" {
		t.Errorf("turned off: tab 2 %q, notified %v", s.tab(t, 2).State, s.tab(t, 2).Notified)
	}
	if p := g.snap("prefs").Prefs; p == nil || p.Notify || strings.Join(p.NotifyStates, ",") != "needs-you,done,failed" {
		t.Fatalf("preferences opened with %+v", p)
	}
	if s := g.snap("on"); s.tab(t, 2).Notified {
		t.Errorf("turned on with a needs-you already seen: notified")
	}
	if s := g.snap("done"); s.tab(t, 2).Notified {
		t.Errorf("done, which the preferences left out: notified")
	}
	tab2 := g.snap("done").tab(t, 2).ID
	checkRecord(t, g, 0, tab2, alertRecord{Op: "send", Title: "claude failed",
		Body: "Crashed\n" + filepath.Base(g.dir) + " ⎇ feature-x", State: "failed", Action: "app.show-tab"})
	checkRecord(t, g, 1, tab2, alertRecord{Op: "withdraw"})
	if len(g.Notifications) != 2 {
		t.Errorf("%d notification calls, want 2: %+v", len(g.Notifications), g.Notifications)
	}
	n, _ := g.Settings["notifications"].(map[string]any)
	if n == nil || n["enabled"] != false {
		t.Errorf("settings at exit: %+v", g.Settings["notifications"])
	}
	want := "# mine\n[notifications]\nenabled = false   # quiet\nstates = [\"needs-you\", \"failed\"]\n"
	if got := readFile(t, g.file("config.toml")); got != want {
		t.Errorf("config.toml:\n%s\nwant:\n%s", got, want)
	}
}
