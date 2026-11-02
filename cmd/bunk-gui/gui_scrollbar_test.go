package main

import "testing"

// TestGUIScrollbarDrag drags the scrollbar thumb with the pointer, as in
// bunk's TUI: up to the oldest row, then back down to the live view. The
// drag scrolls and selects no text.
func TestGUIScrollbarDrag(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		args: []string{"--size", "80x24"},
		script: []string{
			"maximize",
			"wait window.maximized=true window.w>1800 focused.line=$",
			"type seq 300\\r",
			"wait pane.0.text~\"300\" pane.0.line=$",
			"scroll 100000",
			"wait pane.0.scroll>0 frames 1",
			"snap max",
			"scroll-reset",
			"scroll 50",
			"wait pane.0.scroll=50 frames 1",
			"ptr-at thumb 0",
			"ptr-press",
			"ptr-by 0 -40",
			"wait pane.0.scroll>50 frames 1",
			"snap up",
			"ptr-by 0 -4000",
			"wait frames 1",
			"snap top",
			"ptr-by 0 8000",
			"wait pane.0.scroll=0 frames 1",
			"ptr-release",
			"snap end",
			"quit",
		},
	})
	up := g.snap("up").pane(t, 0)
	top := g.snap("top").pane(t, 0)
	if top.Scroll <= up.Scroll {
		t.Errorf("dragging to the top scrolled to %d, not past %d", top.Scroll, up.Scroll)
	}
	if most := g.snap("max").pane(t, 0).Scroll; top.Scroll != most {
		t.Errorf("dragging to the top stopped at scroll %d, want %d", top.Scroll, most)
	}
	end := g.snap("end").pane(t, 0)
	if end.Scroll != 0 {
		t.Errorf("dragging to the bottom left scroll %d, want 0", end.Scroll)
	}
	for _, p := range []paneReport{up, top, end} {
		if p.Selection != "" {
			t.Errorf("the drag selected %q", p.Selection)
		}
	}
}
