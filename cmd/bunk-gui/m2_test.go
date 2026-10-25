package main

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"

	"bunk/internal/input"
	"bunk/internal/session"
)

func (f *frameFixture) slot(r rune) uint32 { return f.r.atlas.Lookup(r, "", false, false) & slotMask }

// rowText reads a row back from the instances through the atlas slots.
func (f *frameFixture) rowText(y, n int) string {
	bySlot := map[uint32]rune{}
	for r := rune(0x20); r < 0x7f; r++ {
		bySlot[f.slot(r)] = r
	}
	b := make([]rune, 0, n)
	for x := 0; x < n; x++ {
		r, ok := bySlot[f.cell(x, y).Glyph&slotMask]
		if !ok || f.cell(x, y).Glyph&slotMask == 0 {
			r = ' '
		}
		b = append(b, r)
	}
	return string(b)
}

func TestScrollbackView(t *testing.T) {
	f := newFixture(20, 10)
	for i := 1; i <= 50; i++ {
		f.write(fmt.Sprintf("line %02d\r\n", i))
	}
	f.frame()
	if got := f.rowText(0, 7); got != "line 42" {
		t.Fatalf("live row 0 = %q, want line 42", got)
	}
	f.s.ScrollUp(20)
	ok, rows := f.frame()
	if !ok || rows != 10 {
		t.Fatalf("scrolled frame: ok=%v rows=%d, want all 10 rows", ok, rows)
	}
	if got := f.rowText(0, 7); got != "line 22" {
		t.Errorf("scrolled row 0 = %q, want line 22", got)
	}
	// Stored rows drop trailing blanks: the rest of the row is default.
	if got := f.cell(15, 0); got.Glyph != 0 || got.BG != f.tv.defBG {
		t.Errorf("cell past a stored row's end: %+v", got)
	}
	if f.tv.curVisible {
		t.Error("cursor drawn while scrolled back")
	}
	// Output while scrolled keeps the view on the same lines.
	f.write("line 51\r\n")
	f.frame()
	if got := f.rowText(0, 7); got != "line 22" {
		t.Errorf("after output while scrolled, row 0 = %q, want line 22", got)
	}
	f.s.ScrollReset()
	f.frame()
	if got := f.rowText(0, 7); got != "line 43" {
		t.Errorf("back to live, row 0 = %q, want line 43", got)
	}
}

func TestSelectionHighlight(t *testing.T) {
	f := newFixture(20, 3)
	f.write("hello world")
	f.frame()
	plain := f.cell(1, 0)
	f.s.SelActive, f.s.SelAnchor, f.s.SelCursor = true, session.SelPos{Row: 0, Col: 4}, session.SelPos{Row: 0, Col: 0}
	f.frame()
	for x := 0; x < 7; x++ {
		got := f.cell(x, 0)
		selected := got.FG == plain.BG && got.BG == plain.FG
		if selected != (x <= 4) {
			t.Errorf("cell %d selected=%v", x, selected)
		}
	}
	f.s.SelActive = false
	f.frame()
	if got := f.cell(1, 0); got != plain {
		t.Errorf("cleared selection left cell 1 as %+v", got)
	}
}

func TestPreeditDrawnAtCursor(t *testing.T) {
	f := newFixture(20, 3)
	f.write("ab")
	f.frame()
	f.tv.preedit = []preeditCell{{'x', 1}, {'y', 1}}
	f.tv.preeditCursor = 2
	f.tv.preeditDirty = true
	f.frame()
	for i, r := range "xy" {
		got := f.cell(2+i, 0)
		if got.Glyph&slotMask != f.slot(r) || got.Glyph&flagUnderline == 0 {
			t.Errorf("preedit cell %d: %+v", 2+i, got)
		}
	}
	if got := f.cell(4, 0); got.BG != f.tv.defFG { // block cursor after the preedit
		t.Errorf("cursor cell after preedit: %+v", got)
	}
	f.tv.preedit = f.tv.preedit[:0]
	f.tv.preeditDirty = true
	f.frame()
	if got := f.cell(2, 0); got.Glyph&flagUnderline != 0 {
		t.Errorf("preedit left behind: %+v", got)
	}
}

func TestPasteData(t *testing.T) {
	if got := string(pasteData("a\nb\r\nc", false)); got != "a\rb\rc" {
		t.Errorf("plain paste = %q", got)
	}
	got := string(pasteData("x\x1b[201~y", true))
	if want := input.PasteStart + "xy" + input.PasteEnd; got != want {
		t.Errorf("bracketed paste = %q, want %q", got, want)
	}
}

func TestOSC52ClipboardText(t *testing.T) {
	for _, tc := range []struct {
		seq  string
		want string
		ok   bool
	}{
		{"\x1b]52;c;aGVsbG8=\x07", "hello", true},
		{"\x1b]52;;aGVsbG8=\x1b\\", "hello", true},
		{"\x1b]52;c;?\x07", "", false},        // query: ignored
		{"\x1b]52;p;aGVsbG8=\x07", "", false}, // primary only
		{"\x1b]52;c;AA==\x07", "", false},     // NUL byte
		{"\x1b]7;file:///tmp\x07", "", false},
	} {
		got, ok := osc52ClipboardText([]byte(tc.seq))
		if got != tc.want || ok != tc.ok {
			t.Errorf("%q: %q %v, want %q %v", tc.seq, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseKeySpec(t *testing.T) {
	if kv, st := parseKeySpec("ctrl+shift+c"); kv != gdk.KEY_C || st != gdk.ControlMask|gdk.ShiftMask {
		t.Errorf("ctrl+shift+c = %#x %v", kv, st)
	}
	if kv, st := parseKeySpec("Return"); kv != gdk.KEY_Return || st != 0 {
		t.Errorf("Return = %#x %v", kv, st)
	}
}

// TestScrolledFrameAllocs: frames in the scrollback view with a selection
// do not allocate either.
func TestScrolledFrameAllocs(t *testing.T) {
	f := newFixture(120, 36)
	for i := 0; i < 500; i++ {
		f.write(fmt.Sprintf("\x1b[32m%04d\x1b[0m the quick brown fox ─│ 漢字\r\n", i))
	}
	f.s.ScrollUp(100)
	f.s.SelActive, f.s.SelAnchor, f.s.SelCursor = true, session.SelPos{Row: 400, Col: 3}, session.SelPos{Row: 410, Col: 9}
	f.frame()
	line := []byte("more output while scrolled back\r\n")
	var before, after runtime.MemStats
	var mallocs uint64
	for i := 0; i < 100; i++ {
		f.term.Write(line) //nolint:errcheck
		runtime.ReadMemStats(&before)
		ok, rows := f.frame()
		runtime.ReadMemStats(&after)
		if !ok || rows != 36 {
			t.Fatalf("frame %d: ok=%v rows=%d", i, ok, rows)
		}
		mallocs += after.Mallocs - before.Mallocs
	}
	if mallocs != 0 {
		t.Errorf("100 scrolled frames allocated %d times", mallocs)
	}
}
