package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestGUILinksAfterSweeps: a pane prints more unique OSC 8 URLs than link
// IDs exist, so link sweeps free and reuse IDs. Ctrl+click opens the link
// that stayed on screen through every sweep, the link printed last (on a
// reused ID; before IDs were reclaimed it had none) and a link in the
// scrollback.
func TestGUILinksAfterSweeps(t *testing.T) {
	t.Parallel()
	const churn = 66000
	var b strings.Builder
	b.WriteString("\x1b]8;;https://scrolled.example/doc\x1b\\scrolled link\x1b]8;;\x1b\\")
	b.WriteString(strings.Repeat("\r\n", 100))
	b.WriteString("\x1b[H\x1b[2Jkeep: \x1b]8;;https://keep.example/doc\x1b\\the keeper\x1b]8;;\x1b\\\x1b[3;1H")
	for i := range churn {
		fmt.Fprintf(&b, "\r\x1b]8;;c:%d\x1b\\c%d\x1b]8;;\x1b\\", i, i)
	}
	b.WriteString("\x1b[2;1Hfresh: \x1b]8;;https://fresh.example/new\x1b\\the fresh one\x1b]8;;\x1b\\\x1b[5;1H")
	g := runGUI(t, scenario{
		cost:  3,
		args:  []string{"-size", "100x30"},
		files: map[string]string{"sweep.txt": b.String()},
		script: []string{
			"maximize",
			"wait window.maximized=true window.w>1800 focused.line=$",
			"type cat sweep.txt\\r",
			"wait pane.0.text~\"the fresh one\" pane.0.line=$ timeout=20s",
			"snap printed",
			"ptr-at cell 0 8 0",
			"kbd-down ctrl",
			"wait pane.0.hover=https://keep.example/doc",
			"snap hover-keep",
			"ptr-click",
			"wait run.opened.#=1",
			"ptr-at cell 0 10 1",
			"wait pane.0.hover=https://fresh.example/new",
			"snap hover-fresh",
			"ptr-click",
			"wait run.opened.#=2",
			"kbd-up ctrl",
			"wait input pane.0.hover=\"\"",
			"scroll 1000",
			"wait pane.0.scroll>0 frames 1",
			"ptr-at cell 0 3 1",
			"kbd-down ctrl",
			"wait pane.0.hover=https://scrolled.example/doc",
			"snap hover-scrolled",
			"ptr-click",
			"wait run.opened.#=3",
			"kbd-up ctrl",
			"wait input",
			"quit",
		},
		limit: 25e9,
	})
	p := g.snap("printed").pane(t, 0)
	if len(p.Text) < 3 || !strings.Contains(p.Text[1], "the fresh one") {
		t.Fatalf("output not printed: %q", p.Text)
	}
	if p.Links.Sweeps == 0 || p.Links.Freed < churn-linkSweepLive || p.Links.URLs > linkSweepLive {
		t.Errorf("link table after %d unique URLs: %+v", churn+3, p.Links)
	}
	for label, want := range map[string]string{
		"hover-keep": "https://keep.example/doc", "hover-fresh": "https://fresh.example/new", "hover-scrolled": "https://scrolled.example/doc",
	} {
		if got := g.snap(label).pane(t, 0).Hover; got != want {
			t.Errorf("%s: hover %q, want %q", label, got, want)
		}
	}
	want := []string{"https://keep.example/doc", "https://fresh.example/new", "https://scrolled.example/doc"}
	if !slices.Equal(g.Opened, want) {
		t.Errorf("opened %q, want %q", g.Opened, want)
	}
	t.Logf("link table: %+v", p.Links)
}

// linkSweepLive bounds the URLs a pane's link table holds when few links
// are live: a sweep is due at 4,096.
const linkSweepLive = 4096
