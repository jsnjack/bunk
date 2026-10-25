package main

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"bunk/internal/ptychild"
)

// shellQuote quotes s for sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// refusedToast is the toast of input a pane's program has no room for.
const refusedToast = "Input not sent: the program is not reading its input"

// TestGUINeverReadingPane: pane 0 runs a raw-mode child that floods 30000
// queries and never reads its input. The user types into it and pastes
// more than its input cap (-input-bytes), then types into the shell beside
// it and into a second tab. The window keeps drawing frames and running
// the script on time, key echo latency in the other panes stays normal,
// and the refused paste shows a toast.
func TestGUINeverReadingPane(t *testing.T) {
	t.Parallel()
	self, err := os.Executable()
	must(t, err)
	const inputCap = 16 << 10
	var child []string
	for _, kv := range ptychild.Environ(30000, "", "") {
		child = append(child, shellQuote(kv))
	}
	g := runGUI(t, scenario{
		args: []string{"-size", "100x30", "-input-bytes", strconv.Itoa(inputCap),
			"-e", "exec env " + strings.Join(child, " ") + " " + shellQuote(self), "-tab", ""},
		script: []string{
			"300ms split",
			"700ms focus left",
			"2500ms snap flooded",
			"2600ms type " + strings.Repeat("k", 2000),
			"2700ms clip " + strings.Repeat("p", inputCap+1),
			"2800ms paste",
			"3300ms snap refused",
			"3400ms focus right",
			"3500ms mark type-start",
			"3550ms typing start 25ms",
			"5550ms typing stop",
			"5600ms mark type-end",
			"5650ms key ctrl+u", // the typed x's
			"5700ms type echo pane-ok\\r",
			"6200ms snap pane-echo",
			"6300ms select-tab 2",
			"6500ms type echo tab-ok\\r",
			"7000ms snap tab-echo",
			"7100ms quit",
		},
		limit: 20 * time.Second,
	})

	flooded := g.snap("flooded")
	stuck := flooded.pane(t, 0)
	if !strings.Contains(stuck.text(), ptychild.FloodDone+" 30000") {
		t.Errorf("the flooding pane's output did not reach its end: %q", stuck.Text)
	}
	if flooded.FocusedPane != 0 || stuck.Writer.DroppedReplies == 0 || stuck.Writer.PendingReplies == 0 {
		t.Errorf("after the flood: focused pane %d, writer %+v; want pane 0 with replies queued and dropped",
			flooded.FocusedPane, stuck.Writer)
	}

	refused := g.snap("refused").pane(t, 0)
	t.Logf("the flooding pane after typing and pasting: %+v", refused.Writer)
	if refused.Writer.RefusedInput != 1 || refused.Writer.PendingInput != 2000 {
		t.Errorf("writer %+v: want the 2000 typed bytes queued and the paste refused", refused.Writer)
	}
	if !slices.Contains(g.Toasts, refusedToast) {
		t.Errorf("no toast %q in %q", refusedToast, g.Toasts)
	}

	typing := g.between("type-start", "type-end")
	t.Logf("typing into pane 2 for %d ms: %d frames, %d key echoes", typing.TMs, typing.Frames, typing.KeyRenders)
	if typing.Frames < 20 || typing.KeyRenders < 20 {
		t.Errorf("typing beside the flooding pane: %d frames, %d key echoes drawn; want at least 20", typing.Frames, typing.KeyRenders)
	}
	// Script actions run on the main thread: a late mark means it was blocked.
	if late := g.mark("type-end").TMs - (g.ActiveMs + 5600); late > int64(500*slack()) {
		t.Errorf("the type-end mark ran %d ms late: the main thread was blocked", late)
	}
	kr := g.Latency["key_render"]
	t.Logf("key write -> render p50 %.0f µs p99 %.0f µs (n %d)", kr.P50, kr.P99, kr.N)
	if kr.N < 20 || kr.P50 > 10000*slack() || kr.P99 > 50000*slack() {
		t.Errorf("key write -> render: p50 %.0f µs, p99 %.0f µs over %d keys", kr.P50, kr.P99, kr.N)
	}

	if p := g.snap("pane-echo").pane(t, 2); !slices.Contains(p.Text, "pane-ok") {
		t.Errorf("the shell beside the flooding pane did not echo: %q", p.Text)
	}
	if p := g.snap("tab-echo").pane(t, 1); !slices.Contains(p.Text, "tab-ok") {
		t.Errorf("the second tab did not echo: %q", p.Text)
	}
}
