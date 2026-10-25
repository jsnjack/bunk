package main

// Window scenarios for real cell pixels and find in pane.

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// lastLine returns the last pane row containing text.
func lastLine(p paneReport, text string) string {
	line := ""
	for _, l := range p.Text {
		if strings.Contains(l, text) {
			line = strings.TrimSpace(l)
		}
	}
	return line
}

// TestGUICellPixels: a program in the pane learns the real device size of
// a cell, from TIOCGWINSZ and from CSI 16 t / 14 t, before and after a
// text zoom (Ctrl+=), so it can size images 1:1.
func TestGUICellPixels(t *testing.T) {
	t.Parallel()
	self, err := os.Executable()
	must(t, err)
	g := runGUI(t, scenario{
		args: []string{"-size", "80x24", "-e", "BUNK_GUI_HELPER=pixels exec " + self},
		script: []string{
			// A resize would report again ("winch") after 100 ms.
			"wait focused.text~\"start: winsize\" focused.settled=true idle 300ms",
			"snap before",
			"kbd ctrl+equal",
			"wait text_percent=110 focused.settled=true",
			"kbd r",
			"wait focused.text~\"key: winsize\"",
			"snap after",
			"quit",
		},
	})
	cells := map[string][2]int{}
	for _, label := range []string{"before", "after"} {
		p := g.snap(label).pane(t, 0)
		cw, ch := p.Cell[0], p.Cell[1]
		cells[label] = p.Cell
		want := fmt.Sprintf("winsize %dx%d pixels %dx%d csi16 6;%d;%d csi14 4;%d;%d",
			p.Cols, p.Rows, p.Cols*cw, p.Rows*ch, ch, cw, p.Rows*ch, p.Cols*cw)
		got := lastLine(p, "winsize")
		t.Logf("%s: cell %dx%d device px; the program saw %q", label, cw, ch, got)
		if !strings.HasSuffix(got, want) {
			t.Errorf("%s: the program saw %q, want %q", label, got, want)
		}
	}
	b, a := cells["before"], cells["after"]
	if b[0] <= 8 || a[0] < b[0] || a[1] <= b[1] {
		t.Errorf("cells before %v and after zoom %v: want real pixels that grow", b, a)
	}
}

// TestGUIFind drives the find bar with real keys: incremental typing,
// next and previous with wrap-around, scrolling a match in scrollback into
// view, the count, no matches, Esc keeping the scroll position (as bunk's
// TUI), highlight colours in pixels, and no allocations in the frame path
// while matches are shown.
func TestGUIFind(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost: 2,
		args: []string{"-alloc-probe", "-size", "80x24", "-e", "sh fill.sh"},
		files: map[string]string{"fill.sh": `i=1
while [ $i -le 300 ]; do
  if [ $((i % 10)) -eq 0 ]; then echo "row $i Needle here"; else echo "row $i"; fi
  i=$((i+1))
done
exec sh
`},
		script: []string{
			"wait focused.text=\"row 300 Needle here\" focused.line=$",
			"kbd ctrl+shift+f",
			"wait find.focused=true",
			"kbd-type needle",
			"wait find.query=needle find.busy=false find.total=30",
			"snap typed",
			"shot typed.png",
			"kbd Return",
			"wait find.index=0 find.current.row>=0",
			"snap next-wrap",
			"kbd shift+Return",
			"wait find.index=29",
			"snap prev-wrap",
			"kbd Return",
			"wait find.index=0",
			"kbd Return",
			"wait find.index=1",
			"snap second",
			"kbd-type x",
			"wait find.query=needlex find.busy=false",
			"snap none",
			"kbd BackSpace",
			"wait find.query=needle find.busy=false find.total=30",
			"snap back",
			"mark alloc-start",
			"typing start 30ms",
			"wait frames 12",
			"typing stop",
			"scroll 2", "wait frames 1", "scroll 2", "wait frames 1",
			"scroll -2", "wait frames 1", "scroll -2", "wait frames 1",
			"mark alloc-end",
			"kbd Escape",
			"wait find=null keyboard_focus=\"pane 0\"",
			"snap closed",
			"quit",
		},
	})
	const total = 30
	find := func(label string) *findReport {
		t.Helper()
		s := g.snap(label)
		if s.Find == nil {
			t.Fatalf("%s: the find bar is closed", label)
		}
		f := s.Find
		if f.Busy || f.Count != fmt.Sprintf("%d/%d", f.Index+1, f.Total) {
			t.Errorf("%s: count %q for match %d of %d (busy %v)", label, f.Count, f.Index, f.Total, f.Busy)
		}
		return f
	}
	f := find("typed")
	s := g.snap("typed")
	if f.Query != "needle" || f.Total != total || f.Index != total-1 || !f.Focused || s.pane(t, 0).Scroll != 0 {
		t.Errorf("typed: query %q, %d/%d, focused %v, scroll %d; want the newest of 30 matches in the live view",
			f.Query, f.Index+1, f.Total, f.Focused, s.pane(t, 0).Scroll)
	}
	if f.Current == nil || f.Current.Row < 0 || len(f.Visible) < 2 {
		t.Fatalf("typed: current %+v, visible %v", f.Current, f.Visible)
	}
	// Highlights: orange for the current match, amber for the others.
	img := loadPNG(t, g.file("typed.png"))
	p := s.pane(t, 0)
	cellPixel := func(m matchReport) rgb {
		x := p.Texture[0] + m.Col*p.Cell[0] + 1
		y := p.Texture[1] + m.Row*p.Cell[1] + 1
		return at(img, x, y)
	}
	if c := cellPixel(*f.Current); c.dist(hex(0xff8c2a)) > 2 {
		t.Errorf("current match cell %v, want #ff8c2a", c)
	}
	for _, m := range f.Visible {
		if m != *f.Current {
			if c := cellPixel(m); c.dist(hex(0xc9a227)) > 2 {
				t.Errorf("other match at row %d: %v, want #c9a227", m.Row, c)
			}
		}
	}

	if f := find("next-wrap"); f.Index != 0 || f.Current == nil || f.Current.Row < 0 || g.snap("next-wrap").pane(t, 0).Scroll == 0 {
		t.Errorf("Enter after the last match: match %d, current %+v; want the first, scrolled into view", f.Index+1, f.Current)
	}
	if f := find("prev-wrap"); f.Index != total-1 || g.snap("prev-wrap").pane(t, 0).Scroll != 0 {
		t.Errorf("Shift+Enter on the first match: match %d, scroll %d; want the last, live", f.Index+1, g.snap("prev-wrap").pane(t, 0).Scroll)
	}
	if f := find("second"); f.Index != 1 || f.Current == nil || f.Current.Row < 0 {
		t.Errorf("Enter twice: match %d, current %+v", f.Index+1, f.Current)
	}
	if f := find("none"); f.Query != "needlex" || f.Total != 0 || f.Count != "0/0" {
		t.Errorf("no matches: query %q, count %q", f.Query, f.Count)
	}
	back := find("back")
	if back.Query != "needle" || back.Total != total || back.Current == nil || back.Current.Row < 0 {
		t.Errorf("Backspace: query %q, %d matches, current %+v", back.Query, back.Total, back.Current)
	}
	allocs := g.between("alloc-start", "alloc-end")
	t.Logf("with matches shown: %d frames, %d objects allocated by the frame path", allocs.Frames, allocs.RenderMallocs)
	if allocs.Frames < 10 || allocs.RenderMallocs != 0 {
		t.Errorf("with matches shown: %d frames, frame path allocated %d objects; want 0", allocs.Frames, allocs.RenderMallocs)
	}
	c := g.snap("closed")
	if c.Find != nil || c.pane(t, 0).Scroll != g.snap("back").pane(t, 0).Scroll || c.FocusedPane != 0 || c.KeyboardFocus != "pane 0" {
		t.Errorf("Esc: find %+v, scroll %d (was %d), keyboard %q", c.Find, c.pane(t, 0).Scroll, g.snap("back").pane(t, 0).Scroll, c.KeyboardFocus)
	}
}
