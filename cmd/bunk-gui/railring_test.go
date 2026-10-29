package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestStateRGBMatchesCSS: the ring draws each state in the colour its
// dot has in the CSS.
func TestStateRGBMatchesCSS(t *testing.T) {
	for cls, c := range stateRGB {
		rule := fmt.Sprintf("\n.%s { color: #%06x; }", cls, c)
		if !strings.Contains(chromeCSS, rule) {
			t.Errorf("chromeCSS has no %q", strings.TrimSpace(rule))
		}
	}
}

func TestLoadSettingsRailStyle(t *testing.T) {
	for _, tc := range []struct{ file, want, err string }{
		{"", "ring", ""},
		{`rail_style = "map"`, "map", ""},
		{`rail_style = "ring"`, "ring", ""},
		{`rail_style = "pie"`, "ring", `rail_style = "pie"`},
	} {
		st, err := loadSettings(writeConfig(t, tc.file+"\n"))
		if st.RailStyle != tc.want || (err == nil) != (tc.err == "") || err != nil && !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%q: style %q, error %v; want %q, %q", tc.file, st.RailStyle, err, tc.want, tc.err)
		}
	}
}

// TestRingArcs: each pane's arc faces the pane, so the ring reads like
// the tab: a pane on the left has the left arc, a pane on top the top one.
func TestRingArcs(t *testing.T) {
	names := map[*Pane]string{}
	leaf := func(name string) *node {
		p := &Pane{}
		names[p] = name
		return &node{pane: p}
	}
	side := func(a, b *node) *node { return &node{a: a, b: b} }
	stack := func(a, b *node) *node { return &node{a: a, b: b, vertical: true} }
	dot := func(p *Pane) look { return look{dot: names[p]} }
	for _, tc := range []struct {
		tree *node
		want string
	}{
		{leaf("a"), "a@0"},
		{side(leaf("a"), leaf("b")), "b@90 a@270"},
		{stack(leaf("a"), leaf("b")), "a@0 b@180"},
		{side(leaf("a"), stack(leaf("b"), leaf("c"))), "b@30 c@150 a@270"},
		{side(stack(leaf("a"), leaf("b")), stack(leaf("c"), leaf("d"))), "c@45 d@135 b@225 a@315"},
	} {
		if got := ringDots(ringArcs(tc.tree, dot)); got != tc.want {
			t.Errorf("ring = %q, want %q", got, tc.want)
		}
	}
}
