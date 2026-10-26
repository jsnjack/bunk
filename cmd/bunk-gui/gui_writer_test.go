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
// more than its input cap (--input-bytes), then types into the shell beside
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
		cost: 3,
		args: []string{"--size", "100x30", "--input-bytes", strconv.Itoa(inputCap),
			"--command", "exec env " + strings.Join(child, " ") + " " + shellQuote(self), "--tab", ""},
		script: []string{
			"split",
			"wait tab.1.panes.#=2 focused.settled=true",
			"focus left",
			"wait focused_pane=0",
			"wait pane.0.text~\"" + ptychild.FloodDone + " 30000\" pane.0.writer.dropped_replies>0 pane.0.writer.pending_replies>0",
			"snap flooded",
			"type " + strings.Repeat("k", 2000),
			"clip " + strings.Repeat("p", inputCap+1),
			"wait clipboard " + strings.Repeat("p", inputCap+1),
			"paste",
			"wait pane.0.writer.refused_input=1 pane.0.writer.pending_input=2000 run.toasts=\"" + refusedToast + "\"",
			"snap refused",
			"focus right",
			"wait focused_pane=2 focused.line=$",
			// Real time: the window must keep drawing and run the script
			// on time while typing.
			"mark type-start",
			"typing start 25ms",
			"sleep 2s",
			"typing stop",
			"mark type-end",
			"key ctrl+u", // the typed x's
			"type echo pane-ok\\r",
			"wait pane.2.text=pane-ok",
			"snap pane-echo",
			"select-tab 2",
			"wait selected_tab=2 focused.line=$",
			"type echo tab-ok\\r",
			"wait pane.1.text=tab-ok",
			"snap tab-echo",
			"quit",
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
	if late := typing.TMs - 2000; late > int64(500*slack()) {
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
