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
// right of the rail; Alt+2 selects tab 2 and the labels stay; releasing
// Alt hides them, and so does another window taking the focus.
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
		if p := g.snap(label).RailPeek; p != nil {
			t.Errorf("%s: rail peek %+v, want none", label, p)
		}
	}

	want := []string{"1 ● claude api ⎇ main", "2 ◐ codex web ⎇ dev"}
	for _, label := range []string{"peek", "switched"} {
		s := g.snap(label)
		if len(s.RailPeek) != len(want) || s.Chrome == nil {
			t.Fatalf("%s: rail peek %+v, chrome %+v", label, s.RailPeek, s.Chrome)
		}
		railRight := s.Chrome.X + s.Chrome.W
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

	// The selected tab's label is lighter than the others.
	img = loadPNG(t, g.file("peek.png"))
	for i, c := range []rgb{hex(0x26292f), colMenu} {
		r := g.snap("peek").RailPeek[i].Rect
		x, y, w, h := inner(r, 3)
		if n := count(img, x, y, w, h, c, 3); n < w*h/3 {
			t.Errorf("peek: label %d: %d of %d pixels %v", i, n, w*h, c)
		}
	}
}
