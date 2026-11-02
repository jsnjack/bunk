package main

import (
	"strings"
	"testing"
)

// TestGUIProgress: a program's OSC 9;4 progress shows under its tab in
// the collapsed rail, the open rail and the bar, in its state's colour;
// each state reaches the report, and a progress ends with 9;4;0 or when
// its program leaves the foreground.
func TestGUIProgress(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		args: []string{"--size", "100x24"},
		// Sourced, so the shell itself reports; "sh -c" reports as a program.
		files: map[string]string{"p.sh": `printf '\033]9;4;%s;%s\007' "$1" "$2"` + "\n"},
		script: []string{
			"wait focused.line=$",
			"shot plain.png", "snap plain",
			"type . ./p.sh 1 60\\r",
			"wait tab.1.progress=60% idle 100ms",
			"shot rail.png", "snap rail",
			"rail open",
			"wait idle 100ms",
			"shot open.png", "snap open",
			"layout bar",
			"wait idle 100ms",
			"shot bar.png", "snap bar",
			"type . ./p.sh 2\\r",
			"wait tab.1.progress=\"failed at 60%\" idle 100ms",
			"shot failed.png", "snap failed",
			"type . ./p.sh 4 20\\r",
			"wait tab.1.progress=\"paused at 20%\"",
			"type . ./p.sh 3\\r",
			"wait tab.1.progress=busy",
			"type . ./p.sh 0\\r",
			"wait tab.1.progress=\"\"",
			"type sh -c '. ./p.sh 1 30; sleep 2'\\r",
			"wait tab.1.progress=30%",
			"wait tab.1.progress=\"\" focused.line=$ timeout=10s",
			"quit",
		},
	})
	blue, red := rgb{0x5a, 0xa9, 0xff}, rgb{0xff, 0x8a, 0x8a}
	bar := func(label string, c rgb) int {
		img := loadPNG(t, g.file(label+".png"))
		it := g.snap(label).tab(t, 1).Item
		return count(img, it.DX, it.DY, it.DW, it.DH, c, 3)
	}
	base := bar("plain", blue)
	for _, label := range []string{"rail", "open", "bar"} {
		if n := bar(label, blue); n < base+20 {
			t.Errorf("%s: %d blue pixels in the tab (%d without progress), want a bar", label, n, base)
		}
		if tip := g.snap(label).tab(t, 1).Tooltip; !strings.Contains(tip, "progress 60%") {
			t.Errorf("%s: tooltip %q does not name the progress", label, tip)
		}
	}
	if it := g.snap("bar").tab(t, 1).Item; it == nil || it.H > 40 {
		t.Errorf("the bar's tab with a progress: %+v, want at most 40 px high", it)
	}
	if n := bar("failed", red); n < 20 {
		t.Errorf("failed: %d red pixels in the tab, want a red bar", n)
	}
}
