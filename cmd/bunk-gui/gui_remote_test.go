package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGUIRemoteAgent plays an agent on a host reached over ssh
// (docs/remote-agent-status.md). A fake ssh runs a fake claude whose hook
// runs "bunk-gui notify" without the socket or the token, as on the remote
// host: the tab shows the report with the remote host, repo and branch.
// Once ssh exits the report goes. A report without a token from a plain
// shell is dropped; one with the tab's token is shown.
func TestGUIRemoteAgent(t *testing.T) {
	t.Parallel()
	bin, proj := t.TempDir(), filepath.Join(t.TempDir(), "proj")
	must(t, os.MkdirAll(filepath.Join(proj, ".git"), 0o755))
	must(t, os.WriteFile(filepath.Join(proj, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\n\"$(dirname \"$0\")/claude\"\n"), 0o755))
	must(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(`#!/bin/sh
echo claude ready
until [ -e "$BUNK_TEST_DIR/go1" ]; do sleep 0.05; done
cd "$PROJ" && env -u BUNK_NOTIFY_SOCKET -u BUNK_NOTIFY_SESSION SSH_CONNECTION='10.0.0.2 50000 10.0.0.1 22' \
	bunk-gui notify needs-you 'Allow rm?' </dev/null
sleep 30
`), 0o755))
	gui := filepath.Dir(guiBinary(t))
	g := runGUI(t, scenario{
		cost: 2,
		args: []string{"--size", "100x24"},
		env:  []string{"PATH=" + bin + ":" + gui + ":" + os.Getenv("PATH"), "PROJ=" + proj},
		script: []string{
			"wait focused.line=$",
			"type ssh build01\\r",
			"wait focused.status.process=ssh focused.screen~\"claude ready\"",
			"run touch go1",
			"wait tab.1.state=needs",
			"snap remote",
			"kbd ctrl+c",
			"wait focused.status.process=sh focused.line=$ tab.1.state=none",
			"snap left",
			// The marker is split in the typed line, so only the output matches.
			"type env -u BUNK_NOTIFY_SOCKET -u BUNK_NOTIFY_SESSION SSH_CONNECTION=x sh -c 'bunk-gui notify needs-you spoof; echo sp\"\"oofed; sleep 30'\\r",
			"wait focused.screen~spoofed idle 200ms",
			"snap spoof",
			"kbd ctrl+c",
			"wait focused.status.process=sh focused.line=$",
			"type env -u BUNK_NOTIFY_SOCKET sh -c 'bunk-gui notify done Finished; sleep 30'\\r",
			"wait tab.1.state=done",
			"snap token",
			"kbd ctrl+c",
			"quit",
		},
		limit: 30e9,
	})
	host, err := os.Hostname()
	must(t, err)
	r := g.snap("remote").tab(t, 1)
	if r.Agent != "claude" || r.Notice != "Allow rm?" || !r.Status.Reported || r.Status.Host != host ||
		r.Status.Repo != "proj" || r.Status.Branch != "main" || !strings.HasPrefix(r.Loc, "⇄ ") || !strings.Contains(r.Tooltip, "Allow rm?") {
		t.Errorf("remote agent: tab %+v", r)
	}
	if l := g.snap("left").tab(t, 1); l.Status.Reported || l.Status.Host != "" || l.Notice != "" {
		t.Errorf("after ssh exited: tab %+v", l)
	}
	if s := g.snap("spoof").tab(t, 1); s.State != "none" || s.Notice != "" || s.Status.Reported {
		t.Errorf("a report without a token outside ssh was shown: tab %+v", s)
	}
	if k := g.snap("token").tab(t, 1); k.Notice != "Finished" || !k.Status.Reported {
		t.Errorf("a report with the tab's token: tab %+v", k)
	}
}

// TestGUITabHosts: a tab split into panes on three ssh hosts names all
// three in pane order, wherever the focus is, and an agent's host first.
// The switcher cuts the list instead of growing wider.
func TestGUITabHosts(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost:   2,
		config: "tabs = \"rail\"\nrail_open = true\n",
		args:   []string{"--size", "100x30"},
		script: []string{
			"wait focused.line=$",
			"kbd ctrl+shift+o",
			"wait switcher.open=true",
			"snap plain",
			"kbd Escape",
			"wait switcher.open=false",
			"split",
			"wait tab.1.panes.#=2",
			"split",
			"wait tab.1.panes.#=3",
			"status pane=0 ssh=ti-ci",
			"wait tab.1.loc=\"⇄ ti-ci\"",
			"snap one-host",
			"status pane=1 ssh=int-build",
			"status pane=2 ssh=int-devops",
			"wait tab.1.loc=\"⇄ ti-ci, int-build, int-devops\"",
			"snap hosts",
			"status pane=1 agent=claude state=working",
			"status pane=2 ssh=ti-ci-long-host-name-for-the-switcher.example.com",
			"wait tab.1.agent=claude",
			"kbd ctrl+shift+o",
			"wait switcher.open=true",
			"snap agent",
			"quit",
		},
	})
	one, three := g.snap("one-host").tab(t, 1), g.snap("hosts").tab(t, 1)
	if !strings.Contains(three.Tooltip, "⇄ ti-ci, int-build, int-devops") {
		t.Errorf("three hosts: tab %+v", three)
	}
	// The open rail wraps the hosts onto a second line.
	if one.Item == nil || three.Item == nil || three.Item.H <= one.Item.H {
		t.Errorf("rail item %+v with one host, %+v with three; want it a line taller", one.Item, three.Item)
	}
	a := g.snap("agent")
	if l := a.tab(t, 1).Loc; l != "⇄ int-build, ti-ci, ti-ci-long-host-name-for-the-switcher.example.com" {
		t.Errorf("with an agent in pane 1: loc %q; want its host first", l)
	}
	plain, wide := g.snap("plain").Switcher, a.Switcher
	if len(plain.Rects) != 1 || len(wide.Rects) != 1 {
		t.Fatalf("switcher rows %d and %d; want 1", len(plain.Rects), len(wide.Rects))
	}
	if w0, w1 := plain.Rects[0].W, wide.Rects[0].W; w1 != w0 {
		t.Errorf("switcher row %v px wide with the hosts, %v without; want the same width", w1, w0)
	}
}
