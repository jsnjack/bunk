package main

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// TestGUIFocusReports: a program that asked for focus events (DECSET
// 1004) learns when the window loses and regains the focus and when the
// user moves to another pane and back, as in bunk's TUI.
func TestGUIFocusReports(t *testing.T) {
	t.Parallel()
	self, err := os.Executable()
	must(t, err)
	g := runGUI(t, scenario{
		args: []string{"--size", "80x24", "--command", "BUNK_GUI_HELPER=focus exec " + self},
		script: []string{
			"wait pane.0.text~ready window.active=true",
			"decoy open", "wait window.active=false",
			"wait pane.0.text~outs=1",
			"decoy close", "wait window.active=true",
			"wait pane.0.text~\"focus in (outs=1\"",
			"kbd F1",
			"wait focused_pane=1",
			"wait pane.0.text~outs=2",
			"kbd alt+left",
			"wait focused_pane=0",
			"wait pane.0.text~\"focus in (outs=2\"",
			"snap end",
			"quit",
		},
	})
	var events []string
	for _, line := range g.snap("end").pane(t, 0).Text {
		if ev, ok := strings.CutPrefix(line, "focus "); ok {
			events = append(events, strings.Fields(ev)[0])
		}
	}
	// The window may report focus in before the program asked for events.
	if len(events) > 0 && events[0] == "in" && len(events) == 5 {
		events = events[1:]
	}
	if want := []string{"out", "in", "out", "in"}; !slices.Equal(events, want) {
		t.Errorf("focus events %q, want %q", events, want)
	}
}

// TestGUISelectionDrag: a selection dragged to the pane's top row keeps
// scrolling back and selecting, the wheel during a drag moves the
// selection's end with the view, and typing ends the selection, as in
// bunk's TUI. Shift+PgUp scrolls half a page.
func TestGUISelectionDrag(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		args: []string{"--size", "80x24"},
		script: []string{
			"maximize",
			"wait window.maximized=true window.w>1800 focused.line=$",
			"type seq 400\\r",
			"wait pane.0.text~\"400\" pane.0.line=$",
			"ptr-at cell 0 3 10",
			"ptr-press",
			"ptr-at cell 0 3 0",
			"wait pane.0.scroll>10",
			"ptr-at cell 0 3 10",
			"wait idle 300ms",
			"snap edge",
			"ptr-scroll -2",
			"wait pane.0.scroll>16 frames 1",
			"snap wheel",
			"ptr-release",
			"wait input",
			"kbd x",
			"wait pane.0.selection=\"\"",
			"snap typed",
			"kbd BackSpace",
			"kbd shift+pgup",
			"wait pane.0.scroll>0 frames 1",
			"snap page",
			"quit",
		},
	})
	edge := g.snap("edge").pane(t, 0)
	if edge.Scroll <= 10 || edge.Selection == "" {
		t.Fatalf("dragging to the top row: scroll %d, selection %q", edge.Scroll, edge.Selection)
	}
	wheel := g.snap("wheel").pane(t, 0)
	if wheel.Scroll <= edge.Scroll || len(wheel.Selection) <= len(edge.Selection) {
		t.Errorf("the wheel during a drag: scroll %d→%d, selection %d→%d bytes; want both to grow",
			edge.Scroll, wheel.Scroll, len(edge.Selection), len(wheel.Selection))
	}
	if s := g.snap("typed").pane(t, 0).Selection; s != "" {
		t.Errorf("typing left the selection %q", s)
	}
	if p := g.snap("page").pane(t, 0); p.Scroll != p.Rows/2 {
		t.Errorf("Shift+PgUp scrolled %d lines, want half of %d rows", p.Scroll, p.Rows)
	}
}
