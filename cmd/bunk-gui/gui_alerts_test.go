package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// alertAgent writes a fake claude that reports through "bunk-gui notify":
// each line of events runs once the script creates go0, go1, …. It
// returns the PATH for the scenario.
func alertAgent(t *testing.T, events ...string) string {
	t.Helper()
	bin := t.TempDir()
	writeAgent(t, bin, "claude", "go", events...)
	return agentPath(t, bin)
}

// writeAgent writes a fake agent called name into bin that reports
// through "bunk-gui notify": each line of events runs once the script
// creates <prefix>0, <prefix>1, ….
func writeAgent(t *testing.T, bin, name, prefix string, events ...string) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "#!/bin/sh\necho fake %s\n", name)
	for i, ev := range events {
		fmt.Fprintf(&b, "until [ -e \"$BUNK_TEST_DIR/%s%d\" ]; do sleep 0.05; done\nbunk-gui notify %s\n", prefix, i, ev)
	}
	b.WriteString("sleep 60\n")
	must(t, os.WriteFile(filepath.Join(bin, name), []byte(b.String()), 0o755))
}

// agentPath is the scenario's PATH: the fake agents in bin, then bunk-gui.
func agentPath(t *testing.T, bin string) string {
	return "PATH=" + bin + ":" + filepath.Dir(guiBinary(t)) + ":" + os.Getenv("PATH")
}

// alertRepo makes the scenario directory a git work tree on branch
// feature-x: ".git" names the directory itself as the git directory.
var alertRepo = map[string]string{".git": "gitdir: .\n", "HEAD": "ref: refs/heads/feature-x\n"}

// startAlertAgent runs the fake claude in tab 2 and selects tab 1.
var startAlertAgent = []string{
	"kbd alt+2",
	"wait selected_tab=2 focused.line=$ tab.2.status.branch=feature-x",
	"type claude\\r",
	"wait tab.2.status.agent=claude",
	"kbd alt+1",
	"wait selected_tab=1",
}

// checkRecord compares a notification call in --report with want, for
// the pane with the ID pane; TMs is not compared.
func checkRecord(t *testing.T, g *guiRun, i int, pane int, want alertRecord) {
	t.Helper()
	if i >= len(g.Notifications) {
		t.Errorf("notification %d: none (%d calls: %+v)", i, len(g.Notifications), g.Notifications)
		return
	}
	got := g.Notifications[i]
	want.Pane, want.TMs = pane, got.TMs
	if got != want {
		t.Errorf("notification %d: %+v\nwant %+v", i, got, want)
	}
}

// TestGUINotifications: a fake claude in tab 2 reports through "bunk-gui
// notify" while tab 1 is selected. needs-you sends one notification for
// its pane, with the agent, notice, repo and branch, and app.show-pane
// with the pane's ID as its action; done replaces it; selecting the tab
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
		"wait tab.2.state=needs run.notifications.#=1 tab.2.notified=true pane.1.notified=true",
		"snap needs",
		"run touch go1",
		"wait tab.2.state=done run.notifications.#=2",
		"snap done",
		"kbd alt+2",
		"wait selected_tab=2 tab.2.state=idle run.notifications.#=3 tab.2.notified=false",
		"kbd alt+1",
		"wait selected_tab=1",
		"run touch go2",
		"wait tab.2.state=needs run.notifications.#=4",
		"alert-click 1",
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
		"alert-click 1",
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
	if panes := g.snap("needs").tab(t, 2).Panes; !slices.Equal(panes, []int{1}) {
		t.Fatalf("tab 2 has panes %v, want pane 1, which the script clicks", panes)
	}
	const pane = 1
	where := filepath.Base(g.dir) + " ⎇ feature-x"
	checkRecord(t, g, 0, pane, alertRecord{Op: "send", Title: "claude needs you", Body: "Allow npm install?\n" + where,
		State: "needs-you", Action: "app.show-pane"})
	checkRecord(t, g, 1, pane, alertRecord{Op: "send", Title: "claude is done", Body: "Finished the refactor\n" + where,
		State: "done", Action: "app.show-pane"})
	checkRecord(t, g, 2, pane, alertRecord{Op: "withdraw"})
	checkRecord(t, g, 3, pane, alertRecord{Op: "send", Title: "claude needs you", Body: "Run the tests?\n" + where,
		State: "needs-you", Action: "app.show-pane"})
	checkRecord(t, g, 4, pane, alertRecord{Op: "activate"})
	checkRecord(t, g, 5, pane, alertRecord{Op: "withdraw"})
	checkRecord(t, g, 6, pane, alertRecord{Op: "send", Title: "claude failed", Body: "Out of credits\n" + where,
		State: "failed", Action: "app.show-pane"})
	checkRecord(t, g, 7, pane, alertRecord{Op: "activate"})
	checkRecord(t, g, 8, pane, alertRecord{Op: "withdraw"})
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

// TestGUISplitAgents: tab 2 runs a fake claude and, split beside it, a
// fake codex, while tab 1 is selected. The tab keeps claude's name with
// "+1"; its state is the most urgent of the two, with codex's notice when
// codex is that agent. Each pane sends its own notification. Alt+A
// focuses codex, which withdraws only its notification; focusing claude
// withdraws the other. The tab's map in the collapsed rail shows each
// agent's dot where its pane is.
func TestGUISplitAgents(t *testing.T) {
	t.Parallel()
	bin := t.TempDir()
	writeAgent(t, bin, "claude", "c", `done "Finished the refactor"`)
	writeAgent(t, bin, "codex", "x", `needs-you "Allow npm install?"`)
	g := runGUI(t, scenario{
		cost:  3,
		args:  []string{"--size", "100x30", "--tab", ""},
		env:   []string{agentPath(t, bin)},
		files: alertRepo,
		script: []string{
			"kbd alt+2",
			"wait selected_tab=2 focused.line=$ tab.2.status.branch=feature-x",
			"type claude\\r",
			"wait pane.1.status.agent=claude",
			"split",
			"wait tab.2.panes.#=2 focused_pane=2 pane.2.line=$",
			"type codex\\r",
			"wait pane.2.status.agent=codex tab.2.more=1",
			"kbd alt+1",
			"wait selected_tab=1",
			"run touch x0",
			"wait tab.2.state=needs pane.2.notified=true run.notifications.#=1",
			"snap codex-needs",
			"run touch c0",
			"wait pane.1.status.state=done pane.1.notified=true run.notifications.#=2",
			"snap both",
			"kbd alt+a",
			"wait selected_tab=2 focused_pane=2 pane.2.notified=false run.notifications.#=3",
			"snap jumped",
			"focus left",
			"wait focused_pane=1 pane.1.notified=false run.notifications.#=4",
			"snap seen",
			"quit",
		},
		limit: 25e9,
	})
	where := filepath.Base(g.dir) + " ⎇ feature-x"
	s := g.snap("codex-needs")
	tab := s.tab(t, 2)
	if tab.Agent != "claude" || tab.More != 1 || tab.State != "needs" || !tab.Needs || tab.Notice != "codex: Allow npm install?" {
		t.Errorf("codex needs you: tab 2 %q +%d, state %q, needs %v, notice %q", tab.Agent, tab.More, tab.State, tab.Needs, tab.Notice)
	}
	if !strings.Contains(tab.Tooltip, "\ncodex · needs you · Allow npm install?") {
		t.Errorf("codex needs you: tooltip %q", tab.Tooltip)
	}
	if !tab.Notified || s.pane(t, 1).Notified || !s.pane(t, 2).Notified {
		t.Errorf("codex needs you: tab notified %v, claude %v, codex %v", tab.Notified, s.pane(t, 1).Notified, s.pane(t, 2).Notified)
	}
	if tab := g.snap("both").tab(t, 2); tab.State != "needs" || tab.Agent != "claude" || tab.Map != "(✓|◐)" {
		t.Errorf("claude done, codex needs you: tab 2 %q, state %q, map %q", tab.Agent, tab.State, tab.Map)
	}
	if m := s.tab(t, 1).Map; m != "" {
		t.Errorf("tab 1 has one pane but a map %q", m)
	}
	if tab := g.snap("jumped").tab(t, 2); tab.State != "done" || tab.Notice != "Finished the refactor" {
		t.Errorf("codex seen, claude done: tab 2 state %q, notice %q", tab.State, tab.Notice)
	}
	// Both agents are idle, or working for 2 s after their last output.
	if tab := g.snap("seen").tab(t, 2); (tab.State != "idle" && tab.State != "working") || tab.Notified {
		t.Errorf("both seen: tab 2 state %q, notified %v", tab.State, tab.Notified)
	}
	checkRecord(t, g, 0, 2, alertRecord{Op: "send", Title: "codex needs you", Body: "Allow npm install?\n" + where,
		State: "needs-you", Action: "app.show-pane"})
	checkRecord(t, g, 1, 1, alertRecord{Op: "send", Title: "claude is done", Body: "Finished the refactor\n" + where,
		State: "done", Action: "app.show-pane"})
	checkRecord(t, g, 2, 2, alertRecord{Op: "withdraw"})
	checkRecord(t, g, 3, 1, alertRecord{Op: "withdraw"})
	if len(g.Notifications) != 4 {
		t.Errorf("%d notification calls, want 4: %+v", len(g.Notifications), g.Notifications)
	}
}

// TestGUIRailMap: with rail_style = "map", the collapsed rail draws a
// tab of 2 to 4 panes as a map of their dots, placed like the panes, in
// the rail's 48 px. A fifth pane turns the map back into the most urgent
// dot.
func TestGUIRailMap(t *testing.T) {
	t.Parallel()
	bin := t.TempDir()
	writeAgent(t, bin, "claude", "c", `needs-you "Allow edit?"`)
	g := runGUI(t, scenario{
		cost:   3,
		config: "tabs = \"rail\"\nrail_style = \"map\"\n",
		args:   []string{"--size", "100x30"},
		env:    []string{agentPath(t, bin)},
		script: []string{
			"wait focused.line=$",
			"type claude\\r",
			"wait tab.1.status.agent=claude",
			"run touch c0",
			"wait tab.1.state=needs",
			"split",
			"wait tab.1.panes.#=2 focused.line=$",
			"split",
			"wait tab.1.panes.#=3 focused.line=$ chrome.w=48",
			"snap three",
			"shot rail-map.png",
			"split",
			"wait tab.1.panes.#=4 focused.line=$",
			"split",
			"wait tab.1.panes.#=5 focused.line=$ chrome.w=48",
			"snap five",
			"quit",
		},
		limit: 25e9,
	})
	three := g.snap("three")
	if tab := three.tab(t, 1); tab.Map != "(◐|(○/○))" || tab.Dot != "◐" {
		t.Errorf("three panes: map %q, dot %q; want (◐|(○/○)) and ◐", tab.Map, tab.Dot)
	}
	if it := three.tab(t, 1).Item; it == nil || it.W > 40 || it.H > 40 {
		t.Errorf("three panes: rail item %+v, want within 40x40", it)
	}
	if tab := g.snap("five").tab(t, 1); tab.Map != "" || tab.Dot != "◐" {
		t.Errorf("five panes: map %q, dot %q; want none and ◐", tab.Map, tab.Dot)
	}
}

// TestGUIRailRing: by default the collapsed rail draws every tab as one
// ring: closed for one pane, an arc per pane in its state's colour for
// several, around an empty centre, within the rail button. Preferences switch it to the
// map at once and save rail_style.
func TestGUIRailRing(t *testing.T) {
	t.Parallel()
	bin := t.TempDir()
	writeAgent(t, bin, "claude", "c", `needs-you "Allow edit?"`)
	g := runGUI(t, scenario{
		cost:   3,
		config: "tabs = \"rail\"\n",
		args:   []string{"--size", "100x30"},
		env:    []string{agentPath(t, bin)},
		script: []string{
			"wait focused.line=$",
			"type claude\\r",
			"wait tab.1.status.agent=claude",
			"run touch c0",
			"wait tab.1.state=needs",
			"wait tab.1.ring=◐@0",
			"snap one",
			"split",
			"wait tab.1.panes.#=2 focused.line=$",
			"split",
			"wait tab.1.panes.#=3 focused.line=$ chrome.w=48 tab.1.ring=\"○@30 ○@150 ◐@270\"",
			"snap three",
			"shot ring.png",
			"split",
			"wait tab.1.panes.#=4 focused.line=$",
			"split",
			"wait tab.1.panes.#=5 focused.line=$ chrome.w=48",
			"snap five",
			"maximize",
			"wait window.maximized=true window.w>1800",
			"kbd ctrl+comma",
			"wait prefs!=null",
			"ptr-at pref rail-map", "ptr-click",
			"wait rail_style=map tab.1.ring=\"\"",
			"snap map",
			"quit",
		},
		limit: 30e9,
	})
	if tab := g.snap("one").tab(t, 1); tab.Ring != "◐@0" {
		t.Errorf("one pane: ring %q, want ◐@0", tab.Ring)
	}
	three := g.snap("three")
	tab := three.tab(t, 1)
	// The agent's pane is on the left, the shells stacked on the right.
	if tab.Ring != "○@30 ○@150 ◐@270" || tab.Dot != "◐" || three.RailStyle != "ring" {
		t.Errorf("three panes: ring %q, dot %q, style %q; want ○@30 ○@150 ◐@270, ◐ and ring", tab.Ring, tab.Dot, three.RailStyle)
	}
	if it := tab.Item; it == nil || it.W > 40 || it.H > 40 {
		t.Errorf("three panes: rail item %+v, want within 40x40", it)
	} else {
		// The agent's arc is amber, the shells' arcs grey, the centre empty.
		img := loadPNG(t, g.file("ring.png"))
		amber, grey := 0, 0
		cx, cy := it.DX+it.DW/2, it.DY+it.DH/2
		for y := cy - 3; y <= cy+3; y++ {
			for x := cx - 3; x <= cx+3; x++ {
				if c := at(img, x, y); c.dist(hex(stateRGB["needs-fg"])) < 40 || c.dist(hex(0x5c5b58)) < 24 {
					t.Errorf("ring centre (%d,%d) is %v, want the background", x, y, c)
				}
			}
		}
		for y := it.DY; y < it.DY+it.DH; y++ {
			for x := it.DX; x < it.DX+it.DW; x++ {
				switch c := at(img, x, y); {
				case c.dist(hex(stateRGB["needs-fg"])) < 40:
					amber++
				case c.dist(hex(0x5c5b58)) < 24: // idle grey at 45 % over the needs-you background
					grey++
				}
			}
		}
		if amber < 40 || grey < 40 {
			t.Errorf("ring pixels: %d amber, %d grey; want an amber arc and grey arcs", amber, grey)
		}
	}
	if tab := g.snap("five").tab(t, 1); tab.Ring != "○@62 ○@134 ○@206 ◐@278 ○@350" || tab.Map != "" {
		t.Errorf("five panes: ring %q, map %q; want ○@62 ○@134 ○@206 ◐@278 ○@350 and none", tab.Ring, tab.Map)
	}
	if s := g.snap("map"); s.RailStyle != "map" || s.tab(t, 1).Ring != "" || s.Prefs == nil || s.Prefs.RailRing {
		t.Errorf("map chosen: style %q, ring %q, prefs ring %v", s.RailStyle, s.tab(t, 1).Ring, s.Prefs.RailRing)
	}
	if got := readFile(t, filepath.Join(g.dir, "config.toml")); !strings.Contains(got, `rail_style = "map"`) {
		t.Errorf("config.toml does not save rail_style:\n%s", got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return string(b)
}

// TestGUICommandNotifications: a command that ran command_after seconds
// or longer in a tab the user is not looking at sends one notification
// when it ends, titled after the command, with its line as the body;
// selecting the tab withdraws it. The same command in the selected tab
// sends none. The preferences window saves command_after.
func TestGUICommandNotifications(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost:   8,
		config: "[notifications]\ncommand_after = 1\n",
		args:   []string{"--size", "100x30", "--tab", ""},
		script: []string{
			"maximize", "wait window.maximized=true window.w>1800",
			"kbd alt+2",
			"wait selected_tab=2 focused.line=$",
			"type sleep 3\\r",
			"wait tab.2.status.process=sleep",
			"kbd alt+1",
			"wait selected_tab=1 run.notifications.#=1 tab.2.notified=true",
			"snap sent",
			"kbd alt+2",
			"wait selected_tab=2 run.notifications.#=2 tab.2.notified=false",
			"type sleep 3\\r",
			"wait tab.2.status.process=sleep",
			"wait tab.2.status.process=sh tab.2.status.command=sleep",
			"kbd ctrl+comma",
			"wait prefs!=null",
			"snap prefs",
			"pref-scroll notify-command-after",
			"ptr-at pref notify-command-after -30 0", "ptr-click", "kbd ctrl+a", "kbd-type 30\\r",
			"wait prefs.notify_command_after=30 run.reloads>=1",
			"dialog-close",
			"wait prefs=null",
			"quit",
		},
		limit: 30e9,
	})
	if p := g.snap("prefs").Prefs; p == nil || p.NotifyCommandAfter != 1 {
		t.Fatalf("preferences opened with %+v", p)
	}
	pane := g.snap("sent").tab(t, 2).Panes[0]
	if len(g.Notifications) != 2 {
		t.Fatalf("%d notification calls, want 2: %+v", len(g.Notifications), g.Notifications)
	}
	sent := g.Notifications[0]
	if sent.Op != "send" || sent.Pane != pane || !strings.HasPrefix(sent.Title, "sleep finished after ") ||
		sent.Body != "sleep 3" || sent.State != "command" || sent.Action != "app.show-pane" {
		t.Errorf("notification 0: %+v, want sleep 3 in pane %d", sent, pane)
	}
	checkRecord(t, g, 1, pane, alertRecord{Op: "withdraw"})
	if n, _ := g.Settings["notifications"].(map[string]any); n == nil || n["command_after"] != 30.0 {
		t.Errorf("settings at exit: %+v", g.Settings["notifications"])
	}
	if got, want := readFile(t, g.file("config.toml")), "[notifications]\ncommand_after = 30\n"; got != want {
		t.Errorf("config.toml:\n%s\nwant:\n%s", got, want)
	}
}

// TestGUICommandMarks: a command that has run command_after seconds marks
// its tab and its pane's badge running; when it fails while its tab is
// not selected, the tab shows failed and how it went until the user
// selects the tab. Notifications off leave the marks on. The test's sh
// has no shell integration, so the job marks itself with OSC 133.
func TestGUICommandMarks(t *testing.T) {
	t.Parallel()
	job := "printf '\\033]133;C\\007'\nsleep 3\nfalse\nprintf '\\033]133;D;%d\\007' $?\n"
	g := runGUI(t, scenario{
		cost:   3,
		config: "[notifications]\nenabled = false\ncommand_after = 1\n",
		args:   []string{"--size", "100x30", "--tab", ""},
		files:  map[string]string{"job.sh": job},
		script: []string{
			"kbd alt+2",
			"wait selected_tab=2 focused.line=$",
			"type sh job.sh\\r",
			"wait tab.2.state=working tab.2.label=running focused.command_mark.state=working",
			"snap running",
			"kbd alt+1",
			"wait selected_tab=1 tab.2.state=failed",
			"snap failed",
			"kbd alt+2",
			"wait selected_tab=2 tab.2.state=none focused.command_mark.state=none",
			"snap seen",
			"quit",
		},
	})
	running := g.snap("running")
	if tab := running.tab(t, 2); tab.Agent != "sh" || tab.Dot != "●" || !strings.Contains(tab.Tooltip, "sh job.sh · running since ") {
		t.Errorf("running: tab 2 %+v", tab)
	}
	rows := checkBadgeRows(t, running)
	if row := rows[running.FocusedPane]; len(row.Badges) == 0 || row.Badges[0].Text != " ● sh running " {
		t.Errorf("running: badges %q", row.Badges)
	}
	tab := g.snap("failed").tab(t, 2)
	if tab.Label != "failed" || tab.Dot != "✕" || !strings.HasPrefix(tab.Notice, "failed after ") ||
		!strings.HasSuffix(tab.Notice, " (exit status 1)") || !strings.Contains(tab.Tooltip, "sh job.sh · failed after ") {
		t.Errorf("failed: tab 2 %+v", tab)
	}
	if tab := g.snap("seen").tab(t, 2); tab.Label != "" || tab.Notice != "" {
		t.Errorf("seen: tab 2 %+v", tab)
	}
	if len(g.Notifications) != 0 {
		t.Errorf("notifications off sent %+v", g.Notifications)
	}
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
	pane := g.snap("done").tab(t, 2).Panes[0]
	checkRecord(t, g, 0, pane, alertRecord{Op: "send", Title: "claude failed",
		Body: "Crashed\n" + filepath.Base(g.dir) + " ⎇ feature-x", State: "failed", Action: "app.show-pane"})
	checkRecord(t, g, 1, pane, alertRecord{Op: "withdraw"})
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
