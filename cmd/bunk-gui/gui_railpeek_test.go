package main

// The where badge and the rail peek (railpeek.go) in a window: an agent's
// pane names its repo and branch beside the agent badge, and holding Alt
// alone over the collapsed rail labels each tab's ring until Alt is
// released or the window goes inactive.

import (
	"math"
	"testing"
)

// TestGUIRailPeek: tab 1 runs claude in api on main and tab 2 codex in
// web on dev, in the collapsed rail. Claude's pane shows " api ⎇ main "
// left of its agent badge. Alt+B, a shell's key, shows no labels after
// the peek's delay. Alt held alone labels both rings, level with them
// right of the rail, and a scrim dims the window right of the rail but
// not the rail; Alt+2 selects tab 2 and the labels stay; releasing Alt
// hides them, and so does another window taking the focus.
func TestGUIRailPeek(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost:   2,
		config: "tabs = \"rail\"\n",
		args:   []string{"--size", "100x30", "--tab", ""},
		script: []string{
			"wait tabs.#=2 selected_tab=1 focused.line=$",
			"status tab=1 agent=claude state=working repo=api branch=main",
			"status tab=2 agent=codex state=needs repo=web branch=dev",
			"wait tab.1.state=working tab.2.state=needs focused.badges.1.role=where idle 100ms",
			"snap where", "shot where.png",
			"kbd alt+b",
			"sleep 700ms", // past railPeekDelay: the labels must not come
			"snap combo",
			"kbd-down alt",
			"wait rail_peek.#=2 idle 100ms",
			"snap peek", "shot peek.png",
			"kbd 2",
			"wait selected_tab=2 rail_peek.#=2 idle 100ms",
			"snap switched",
			"kbd-up alt",
			"wait rail_peek.#=0",
			"snap released",
			"kbd-down alt",
			"wait rail_peek.#=2",
			"decoy open",
			"wait window.active=false rail_peek.#=0",
			"snap inactive",
			"kbd-up alt",
			"decoy close",
			"wait window.active=true",
			"quit",
		},
	})
	for _, s := range g.Snapshots {
		checkBadgeRows(t, s)
	}
	b := focusedBadges(t, g.snap("where"))
	if len(b) != 2 || b[0].Text != " ● claude working " || b[1].Text != " api ⎇ main " || b[1].Role != "where" {
		t.Fatalf("where: badges %+v, want the agent, then where it works", b)
	}
	img := loadPNG(t, g.file("where.png"))
	for _, x := range b {
		checkBadgePixels(t, img, "where", x)
	}

	for _, label := range []string{"combo", "released", "inactive"} {
		if s := g.snap(label); s.RailPeek != nil || s.RailPeekScrim != nil {
			t.Errorf("%s: rail peek %+v, scrim %+v, want none", label, s.RailPeek, s.RailPeekScrim)
		}
	}

	want := []string{"1 ● claude api ⎇ main", "2 ◐ codex web ⎇ dev"}
	for _, label := range []string{"peek", "switched"} {
		s := g.snap(label)
		if len(s.RailPeek) != len(want) || s.Chrome == nil {
			t.Fatalf("%s: rail peek %+v, chrome %+v", label, s.RailPeek, s.Chrome)
		}
		railRight := s.Chrome.X + s.Chrome.W
		if sc := s.RailPeekScrim; sc == nil || math.Abs(sc.X-railRight) > 1 || sc.Y > 0.5 ||
			sc.X+sc.W < float64(s.Window.W)-0.5 || sc.Y+sc.H < float64(s.Window.H)-0.5 {
			t.Errorf("%s: scrim %+v, want it from the rail (%.0f) to the window's edges (%dx%d)",
				label, sc, railRight, s.Window.W, s.Window.H)
		}
		for i, r := range s.RailPeek {
			if r.Tab != i+1 || r.Text != want[i] || r.Cut {
				t.Errorf("%s: label %d is tab %d %q (cut %v), want tab %d %q in full", label, i, r.Tab, r.Text, r.Cut, i+1, want[i])
			}
			if r.Rect == nil || r.Item == nil {
				t.Errorf("%s: label %d not drawn: %+v", label, i, r)
				continue
			}
			mid, itemMid := r.Rect.Y+r.Rect.H/2, r.Item.Y+r.Item.H/2
			if r.Rect.X < railRight || r.Rect.X > railRight+12 || math.Abs(mid-itemMid) > 1 || r.Rect.W < 80 {
				t.Errorf("%s: label %d at %+v; want it right of the rail (%.0f), level with its ring %+v",
					label, i, *r.Rect, railRight, *r.Item)
			}
		}
	}
	if s := g.snap("switched"); s.SelectedTab != 2 {
		t.Errorf("Alt+2 under the labels: tab %d selected, want 2", s.SelectedTab)
	}

	// The selected tab's label is lighter than the others, and both are
	// lighter than the panes.
	before, img := img, loadPNG(t, g.file("peek.png"))
	for i, c := range []rgb{hex(0x383c44), hex(0x2e3138)} {
		r := g.snap("peek").RailPeek[i].Rect
		x, y, w, h := inner(r, 3)
		if n := count(img, x, y, w, h, c, 3); n < w*h/3 {
			t.Errorf("peek: label %d: %d of %d pixels %v", i, n, w*h, c)
		}
	}

	// The scrim darkens the empty pane at the window's lower right as the
	// switcher's backdrop does, and leaves the rail below its rings.
	peek := g.snap("peek")
	sc := peek.RailPeekScrim
	x, y := sc.DX+sc.DW-60, sc.DY+sc.DH-60
	dim := dimmed(at(before, x, y))
	if n := count(img, x, y, 40, 40, dim, 3); n < 40*40*9/10 {
		t.Errorf("peek: pane at %d,%d: %d of %d pixels %v, dimmed from %v", x, y, n, 40*40, dim, at(before, x, y))
	}
	item := peek.RailPeek[len(peek.RailPeek)-1].Item
	x, y = peek.Chrome.DX+4, item.DY+item.DH+8
	if n := count(img, x, y, peek.Chrome.DW-10, 30, colChrome, 3); n < (peek.Chrome.DW-10)*30*9/10 {
		t.Errorf("peek: rail below the rings at %d,%d: %d of %d pixels %v", x, y, n, (peek.Chrome.DW-10)*30, colChrome)
	}
}

// dimmed is c under the switcher's backdrop, rgba(8, 9, 11, 0.62), which
// the rail peek's scrim shares.
func dimmed(c rgb) rgb {
	mix := func(v, s int) int { return int(math.Round(float64(v)*0.38 + float64(s)*0.62)) }
	return rgb{mix(c.r, 8), mix(c.g, 9), mix(c.b, 11)}
}
