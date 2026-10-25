package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestGUITabLeaks opens and closes tabs and splits through -do, then
// compares the resource counters of two marks: descriptors, goroutines,
// child processes and zombies, panes not yet freed and their GL textures
// must be back where they were with only the first tab open. 20 tabs here,
// 200 with SOAK=1 (make soak). Every fifth tab runs a program that floods
// queries and never reads its input, and gets typed into, so it closes
// with the PTY writer blocked and replies and input queued.
func TestGUITabLeaks(t *testing.T) {
	t.Parallel()
	tabs := 20
	if os.Getenv("SOAK") == "1" {
		tabs = 200
	}
	// Every step waits until it took effect: a tab's panes have drawn
	// before it closes, so their textures exist.
	script := []string{"mark start"}
	for i := range tabs {
		if i%5 == 0 {
			script = append(script,
				`new-tab stty raw -echo; yes "$(printf '\\033[5n')" | head -c 300000; sleep 100`,
				"wait tabs.#=2 focused.settled=true focused.writer.dropped_replies>0 focused.writer.pending_replies>0",
				"type "+strings.Repeat("k", 1000),
				"wait focused.writer.pending_input=1000",
				fmt.Sprintf("snap flooding-%d", i),
				"close-tab",
				"wait tabs.#=1")
			continue
		}
		script = append(script, "new-tab", "wait tabs.#=2 focused.settled=true", "split", "wait tab.2.panes.#=2 focused.settled=true")
		if i%2 == 1 {
			script = append(script, "close-pane", "wait tab.2.panes.#=1") // the split pane alone
		}
		script = append(script, "close-tab", "wait tabs.#=1")
	}
	// The shells exit and their views unrealize: wait for the counts the
	// checks below compare.
	script = append(script,
		"wait res.panes=@start.panes res.children<=@start.children res.zombies=0 res.fds<=@start.fds+2 res.goroutines<=@start.goroutines+2 timeout=20s",
		"mark end", "snap end", "quit")
	g := runGUI(t, scenario{cost: 3, args: []string{"-size", "100x30"}, script: script,
		limit: 30*time.Second + time.Duration(tabs)*time.Second})

	start, end := g.mark("start"), g.mark("end")
	t.Logf("%d tabs: fds %d → %d, goroutines %d → %d, children %d → %d (zombies %d), panes %d → %d, textures %d → %d",
		tabs, start.FDs, end.FDs, start.Goroutines, end.Goroutines, start.Children, end.Children, end.Zombies,
		start.Panes, end.Panes, start.Textures, end.Textures)
	if s := g.snap("end"); len(s.Tabs) != 1 {
		t.Fatalf("%d tabs open at the end, want 1", len(s.Tabs))
	}
	for i := 0; i < tabs; i += 5 {
		s := g.snap(fmt.Sprintf("flooding-%d", i))
		if w := s.focused(t).Writer; w.DroppedReplies == 0 || w.PendingReplies == 0 || w.PendingInput != 1000 {
			t.Errorf("tab %d closed without a blocked writer: %+v", i+2, w)
		}
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
