package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGUIQuestionStaysNeedsYou: Claude Code asks a question (the
// PreToolUse hook of AskUserQuestion). The window losing and getting back
// the focus, keys that move through the choices, and Enter, which may only
// move on to the next question, leave the tab and its rail ring at
// needs-you; Esc cancels it, and the PostToolUse hook's working ends it.
func TestGUIQuestionStaysNeedsYou(t *testing.T) {
	t.Parallel()
	bin := t.TempDir()
	claude := `#!/bin/sh
echo fake claude
printf '{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which DB?"}]}}' | bunk-gui notify needs-you -
until [ -e "$BUNK_TEST_DIR/answered" ]; do sleep 0.05; done
bunk-gui notify working
while read -r line; do :; done
`
	must(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(claude), 0o755))
	g := runGUI(t, scenario{
		env:  []string{agentPath(t, bin)},
		args: []string{"--size", "100x30"},
		script: []string{
			"wait focused.line=$",
			"type claude\\r",
			"wait tab.1.state=needs tab.1.notice=\"Which DB?\"",
			"snap asked",
			"decoy open", "wait window.active=false",
			"decoy close", "wait window.active=true",
			"kbd down", "kbd down", "wait input",
			"snap browsing",
			"kbd Return", "wait input",
			"snap next-question",
			"kbd Escape",
			"wait tab.1.state!=needs",
			"run touch answered",
			"wait tab.1.state=working",
			"quit",
		},
	})
	for _, name := range []string{"asked", "browsing", "next-question"} {
		if tab := g.snap(name).tab(t, 1); tab.State != "needs" || tab.Ring != "◐@0" {
			t.Errorf("%s: state %q, ring %q; want needs and ◐", name, tab.State, tab.Ring)
		}
	}
}
