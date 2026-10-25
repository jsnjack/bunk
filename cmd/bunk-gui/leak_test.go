package main

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// TestGUITabLeaks opens and closes tabs and splits through -do, then
// compares the resource counters of two marks: descriptors, goroutines,
// child processes and zombies, panes not yet freed and their GL textures
// must be back where they were with only the first tab open. 20 tabs here,
// 200 with SOAK=1 (make soak).
func TestGUITabLeaks(t *testing.T) {
	t.Parallel()
	tabs := 20
	if os.Getenv("SOAK") == "1" {
		tabs = 200
	}
	at := 1500 * time.Millisecond
	var script []string
	step := func(action string, gap time.Duration) {
		script = append(script, fmt.Sprintf("%dms %s", at.Milliseconds(), action))
		at += gap
	}
	step("mark start", 100*time.Millisecond)
	for i := range tabs {
		step("new-tab", 80*time.Millisecond)
		step("split", 80*time.Millisecond)
		if i%2 == 1 {
			step("close-pane", 80*time.Millisecond) // the split pane alone
		}
		step("close-tab", 80*time.Millisecond)
	}
	at += 2 * time.Second // the shells exit and their views unrealize
	step("mark end", 100*time.Millisecond)
	step("snap end", 100*time.Millisecond)
	step("quit", 0)
	g := runGUI(t, scenario{args: []string{"-size", "100x30"}, script: script, limit: at + 30*time.Second})

	start, end := g.mark("start"), g.mark("end")
	t.Logf("%d tabs: fds %d → %d, goroutines %d → %d, children %d → %d (zombies %d), panes %d → %d, textures %d → %d",
		tabs, start.FDs, end.FDs, start.Goroutines, end.Goroutines, start.Children, end.Children, end.Zombies,
		start.Panes, end.Panes, start.Textures, end.Textures)
	if s := g.snap("end"); len(s.Tabs) != 1 {
		t.Fatalf("%d tabs open at the end, want 1", len(s.Tabs))
	}
	if end.FDs > start.FDs+2 {
		t.Errorf("%d file descriptors open, %d before the tabs", end.FDs, start.FDs)
	}
	if end.Goroutines > start.Goroutines+2 {
		t.Errorf("%d goroutines, %d before the tabs", end.Goroutines, start.Goroutines)
	}
	if end.Children > start.Children || end.Zombies > 0 {
		t.Errorf("%d child processes (%d zombies), %d before the tabs", end.Children, end.Zombies, start.Children)
	}
	if end.Panes != start.Panes {
		t.Errorf("%d panes not freed, %d before the tabs", end.Panes, start.Panes)
	}
	// Only live panes hold textures: a glyph atlas and the view's slots.
	if limit := end.Panes * (viewSlots + 1); end.Textures > limit {
		t.Errorf("%d GL textures held by %d panes (limit %d)", end.Textures, end.Panes, limit)
	}
}
