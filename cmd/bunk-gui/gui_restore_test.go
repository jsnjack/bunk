package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"bunk/internal/restore"
)

// fakeClaude is a "claude" on PATH: it logs its arguments, reports a
// session ID through bunk-gui's hook CLI as Claude Code's hooks would,
// and waits for input.
const fakeClaude = `#!/bin/sh
echo "$*" >> "$CLAUDE_LOG"
printf '{"session_id":"sess-%s","prompt":"hi"}' "${CLAUDE_ID:-resumed}" | "$BUNK_GUI_BIN" notify working -
echo "fake claude $*"
while read -r line; do :; done
`

// restoreEnv returns the environment of a scenario whose windows are
// saved in state, with the fake claude on PATH logging to the file it
// returns.
func restoreEnv(t *testing.T, state string) (env []string, claudeLog string) {
	bin := t.TempDir()
	must(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeClaude), 0o755))
	claudeLog = filepath.Join(bin, "claude.log")
	return []string{"XDG_STATE_HOME_TEST=" + state, "PATH=" + bin + ":" + os.Getenv("PATH"),
		"CLAUDE_LOG=" + claudeLog, "BUNK_GUI_BIN=" + guiBinary(t)}, claudeLog
}

// savedWindows lists the snapshots in state, as a later start sees them.
func savedWindows(t *testing.T, state string) []restore.Saved {
	st, err := restore.Open(restore.DefaultDir(func(k string) string {
		if k == "XDG_STATE_HOME" {
			return state
		}
		return ""
	}))
	must(t, err)
	return st.Saved()
}

// TestGUIRestoreSession: a window with a split running an agent and a
// second, named tab quits; the next start offers it in a banner, and
// Restore brings back the tabs, the split, the directory, the scrollback
// and the agent's conversation (claude --resume with the session ID its
// hook reported), in place of the fresh tab.
func TestGUIRestoreSession(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	env, claudeLog := restoreEnv(t, state)
	runGUI(t, scenario{
		cost:   3,
		env:    env,
		config: "[keys]\nrename_tab = \"ctrl+shift+e\"\n",
		args:   []string{"--size", "100x30"},
		script: []string{
			"wait tabs.#=1 focused.line=$ restore.slot!=\"\" restore.banner=false",
			`type cd /tmp && echo first-pane-marker\r`,
			"wait focused.status.cwd=/tmp focused.screen~first-pane-marker",
			"split",
			"wait panes.#=2 focused.line=$",
			`type CLAUDE_ID=one claude\r`,
			"wait focused.status.agent=claude focused.status.session=sess-one",
			"new-tab",
			"wait tabs.#=2 selected_tab=2 focused.line=$",
			"kbd ctrl+shift+e",
			"wait rename.ready=true keyboard_focus=rename",
			`kbd-type logs\r`,
			"wait rename=null tab.2.name=logs",
			"restore save",
			"wait restore.saves>0",
			"quit",
		},
	})
	saved := savedWindows(t, state)
	if len(saved) != 1 {
		t.Fatalf("saved %d windows, want 1", len(saved))
	}
	first, w := saved[0].ID, saved[0].Window
	if len(w.Tabs) != 2 || w.Selected != 1 || w.Tabs[1].Name != "logs" {
		t.Fatalf("snapshot %+v", w)
	}
	root := w.Tabs[0].Root
	if root.Pane != nil || root.A.Pane == nil || root.B.Pane == nil || root.Ratio < 0.3 || root.Ratio > 0.7 || w.Tabs[0].Focus != 1 {
		t.Fatalf("tab 1: %+v, focus %d; want a split near half", root, w.Tabs[0].Focus)
	}
	if a, b := root.A.Pane, root.B.Pane; a.Cwd != "/tmp" || a.History == "" || b.Agent != "claude" || b.AgentSession != "sess-one" {
		t.Errorf("tab 1 panes %+v and %+v", *a, *b)
	}

	g := runGUI(t, scenario{
		cost: 3,
		env:  env,
		args: []string{"--size", "100x30"},
		script: []string{
			"wait tabs.#=1 focused.line=$ restore.banner=true",
			"snap offered",
			"restore accept",
			"wait tabs.#=2 restore.banner=false restore.answer=restored selected_tab=2 tab.2.name=logs",
			"select-tab 1",
			"wait tab.1.panes.#=2 tab.1.agent=claude restore.typed.#=1",
			"wait focused.status.session=sess-resumed",
			"snap restored",
			"quit",
		},
	})
	if offer := g.snap("offered").Restore.Offer; !strings.Contains(offer, "2 tabs, 1 agent") {
		t.Errorf("banner offers %q", offer)
	}
	s := g.snap("restored")
	if typed := s.Restore.Typed; len(typed) != 1 || typed[0].Text != "claude --resume sess-one\r" {
		t.Errorf("typed %+v, want claude --resume sess-one", typed)
	}
	log, err := os.ReadFile(claudeLog)
	must(t, err)
	if lines := strings.Split(strings.TrimSpace(string(log)), "\n"); lines[len(lines)-1] != "--resume sess-one" {
		t.Errorf("claude ran with %q, want --resume sess-one last", lines)
	}
	if s.Tree == nil || s.Tree.Split != "h" || s.Tree.A == nil || s.Tree.B == nil {
		t.Fatalf("tab 1 tree %+v, want two panes side by side", s.Tree)
	}
	shell := s.pane(t, *s.Tree.A.Pane)
	screen := strings.Join(shell.Text, "\n")
	if shell.Status.Cwd != "/tmp" || !strings.Contains(screen, "first-pane-marker") || !strings.Contains(screen, "── restored from") {
		t.Errorf("restored shell in %q shows:\n%s", shell.Status.Cwd, screen)
	}
	if agent := s.pane(t, *s.Tree.B.Pane); !agent.Focused || strings.Contains(strings.Join(agent.Text, "\n"), "restored from") {
		t.Errorf("agent pane: focused %v, text %q; want focused, without the old scrollback", agent.Focused, agent.Text)
	}
	if slices.Contains(s.Tabs[0].Panes, 0) || slices.Contains(s.Tabs[1].Panes, 0) {
		t.Errorf("the fresh tab's pane 0 stayed: %+v", s.Tabs)
	}
	// The restored window's own snapshot replaced the one it came from.
	own := g.snap("offered").Restore.Slot
	if saved := savedWindows(t, state); len(saved) != 1 || saved[0].ID != own || len(saved[0].Window.Tabs) != 2 {
		t.Errorf("after the restored run: %+v, want only %s (was %s)", saved, own, first)
	}
}

// writeSnapshot saves w in state as a window that has quit.
func writeSnapshot(t *testing.T, state string, w restore.Window) string {
	st, err := restore.Open(filepath.Join(state, "bunk-gui", "windows"))
	must(t, err)
	sl, err := st.New()
	must(t, err)
	must(t, sl.Save(w))
	sl.Release()
	return sl.ID()
}

func onePane(cwd string) restore.Tab {
	return restore.Tab{Root: &restore.Node{Pane: &restore.Pane{Cwd: cwd}}}
}

// TestGUIRestoreWindows: with two saved windows, Restore builds the newer
// one here and opens the other in a window of its own, which --restore
// builds without a banner; Dismiss deletes what was offered.
func TestGUIRestoreWindows(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	env, _ := restoreEnv(t, state)
	older := writeSnapshot(t, state, restore.Window{Tabs: []restore.Tab{onePane("/"), onePane("/tmp")}, Selected: 1})
	newer := writeSnapshot(t, state, restore.Window{Tabs: []restore.Tab{
		{Name: "ssh", Root: &restore.Node{Pane: &restore.Pane{Cwd: "/tmp", Remote: []string{"ssh", "-p", "2222", "gpu 01"}}}},
	}})
	g := runGUI(t, scenario{
		env:  env,
		args: []string{"--size", "100x30"},
		script: []string{
			"wait tabs.#=1 restore.banner=true",
			"snap offered",
			"restore accept",
			"wait tabs.#=1 tab.1.name=ssh restore.typed.#=1",
			`wait focused.line~"ssh -p 2222 'gpu 01'"`,
			"snap restored",
			"quit",
		},
	})
	if offer := g.snap("offered").Restore.Offer; !strings.Contains(offer, "2 windows, 3 tabs") {
		t.Errorf("banner offers %q", offer)
	}
	if s := g.snap("restored").Restore; !slices.Equal(s.Spawned, []string{older}) {
		t.Errorf("opened %v in new windows, want %s (and %s here)", s.Spawned, older, newer)
	}

	g = runGUI(t, scenario{
		env:  env,
		args: []string{"--size", "100x30", "--restore", older},
		script: []string{
			"wait tabs.#=2 selected_tab=2 focused.status.cwd=/tmp",
			"snap restored",
			"quit",
		},
	})
	if s := g.snap("restored").Restore; s.Banner || s.Slot != older {
		t.Errorf("--restore: %+v, want slot %s and no banner", s, older)
	}

	// Two windows have quit since: the one --restore ran in, and the
	// first run's own. Dismiss deletes both.
	if n := len(savedWindows(t, state)); n != 2 {
		t.Fatalf("%d saved windows, want 2", n)
	}
	runGUI(t, scenario{
		env:    env,
		args:   []string{"--size", "100x30"},
		script: []string{"wait restore.banner=true", "restore dismiss", "wait restore.banner=false restore.answer=dismissed", "quit"},
	})
	if saved := savedWindows(t, state); len(saved) != 1 {
		t.Errorf("after Dismiss: %d saved windows, want only the last run's", len(saved))
	}
}

// TestGUIRestoreOff: restore = false neither offers nor saves.
func TestGUIRestoreOff(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	env, _ := restoreEnv(t, state)
	writeSnapshot(t, state, restore.Window{Tabs: []restore.Tab{onePane("/")}})
	g := runGUI(t, scenario{
		env:    env,
		config: "restore = false\n",
		script: []string{"wait tabs.#=1 focused.line=$", "snap s", "quit"},
	})
	if r := g.snap("s").Restore; r.Banner || r.Slot != "" {
		t.Errorf("restore off: %+v", r)
	}
	if n := len(savedWindows(t, state)); n != 1 {
		t.Errorf("%d saved windows, want the one written before", n)
	}
}
