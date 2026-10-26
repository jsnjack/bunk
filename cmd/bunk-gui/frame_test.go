package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"

	"bunk/internal/input"
	"bunk/internal/session"
	"bunk/internal/vt10x"
)

type fakeRaster struct{ calls int }

func (f *fakeRaster) draw(string, bool, int, []byte, int) { f.calls++ }

type frameFixture struct {
	s    *session.Session
	term vt10x.Terminal
	tv   *termView
	r    *Renderer
	fr   *fakeRaster
}

// testTheme is the default theme, which bunk-gui shows without a theme
// setting.
var testTheme = defaultTheme()

// newFixture builds the main-thread frame path without GTK, GL or a PTY: a
// session around a vt10x terminal (with scrollback), the termView copy and
// a Renderer that only holds instances.
func newFixture(cols, rows int) *frameFixture {
	fr := &fakeRaster{}
	a := newAtlas(12, 27, fr)
	r := &Renderer{atlas: a, cols: cols, rows: rows, inst: make([]Instance, cols*rows), rowDirty: make([]bool, rows)}
	s := &session.Session{ScrollbackLines: 1000, Scrollback: session.Ring{MaxLines: 1000}}
	s.Term = vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollCallback(s.OnScrollRow), vt10x.WithLinkReferences(s.MarkLinks))
	return &frameFixture{s: s, term: s.Term, tv: newTermView(&testTheme), r: r, fr: fr}
}

func (f *frameFixture) frame() (bool, int) {
	f.s.Lock()
	ok := f.tv.pull(f.s)
	f.s.Unlock()
	if !ok {
		return false, 0
	}
	return true, f.tv.convert(f.r)
}

func (f *frameFixture) write(s string) { f.term.Write([]byte(s)) } //nolint:errcheck

func (f *frameFixture) cell(x, y int) Instance { return f.r.inst[y*f.r.cols+x] }

func TestConvertColoursAndAttributes(t *testing.T) {
	f := newFixture(20, 4)
	f.write("\x1b[?25l") // hide the cursor so it does not change the cells
	f.write("\x1b[31mR\x1b[0m\x1b[48;2;1;2;3mT\x1b[0m\x1b[1mB\x1b[0mb\x1b[4mU\x1b[0m\x1b[7mV\x1b[0m\x1b[38;5;196mP\x1b[0m\x1b[2mD\x1b[0m")
	f.frame()
	tv := f.tv
	if got := f.cell(0, 0); got.FG != rgba(testTheme.ANSI[1].Hex()) || got.BG != rgba(testTheme.Background.Hex()) {
		t.Errorf("red R: fg %08x bg %08x", got.FG, got.BG)
	}
	if got := f.cell(1, 0); got.BG != rgba(0x010203) || got.FG != tv.defFG {
		t.Errorf("truecolor bg T: fg %08x bg %08x", got.FG, got.BG)
	}
	bold, plain := f.cell(2, 0).Glyph&slotMask, f.r.atlas.Lookup('B', "", false, false)&slotMask
	if bold == 0 || bold == plain {
		t.Errorf("bold B uses slot %d, regular B slot %d; want a separate bold slot", bold, plain)
	}
	if f.cell(4, 0).Glyph&flagUnderline == 0 {
		t.Error("underlined U has no underline flag")
	}
	if got := f.cell(5, 0); got.FG != rgba(testTheme.Background.Hex()) || got.BG != rgba(testTheme.Foreground.Hex()) {
		t.Errorf("reverse V: fg %08x bg %08x, want swapped defaults", got.FG, got.BG)
	}
	if got := f.cell(6, 0); got.FG != rgba(paletteRGB(&testTheme, 196)) {
		t.Errorf("256-colour P: fg %08x want %08x", got.FG, rgba(paletteRGB(&testTheme, 196)))
	}
	if got := f.cell(7, 0); got.FG == tv.defFG {
		t.Error("dim D kept the full foreground")
	}
	if got := f.cell(8, 0); got.Glyph != 0 || got.BG != tv.defBG {
		t.Errorf("blank cell: %+v", got)
	}
}

func TestWideGlyphsAndContinuation(t *testing.T) {
	f := newFixture(10, 2)
	f.write("\x1b[?25l漢x")
	f.frame()
	lead, cont := f.cell(0, 0), f.cell(1, 0)
	if lead.Glyph&flagWide == 0 || lead.Glyph&slotMask == 0 {
		t.Errorf("wide lead: glyph %x", lead.Glyph)
	}
	if cont.Glyph != 0 {
		t.Errorf("continuation cell draws glyph %x", cont.Glyph)
	}
	if x := f.cell(2, 0).Glyph & slotMask; x == 0 {
		t.Error("x after the wide glyph is missing")
	}
	slot := int(lead.Glyph & slotMask)
	if slot%f.r.atlas.SlotsPerRow == f.r.atlas.SlotsPerRow-1 {
		t.Errorf("wide glyph slot %d straddles an atlas row", slot)
	}
}

func TestEmojiDrawnAsReplacement(t *testing.T) {
	f := newFixture(10, 2)
	f.write("\U0001F600")
	f.frame()
	if f.r.atlas.Replaced != 1 {
		t.Errorf("replaced = %d, want 1", f.r.atlas.Replaced)
	}
}

func TestCursorShapes(t *testing.T) {
	f := newFixture(10, 3)
	f.write("ab")
	f.frame()
	under := f.cell(2, 0)
	if under.BG != f.tv.defFG || under.FG != f.tv.defBG {
		t.Errorf("block cursor cell: fg %08x bg %08x, want inverted", under.FG, under.BG)
	}
	f.write("\x1b[6 q") // steady bar
	f.frame()
	if got := f.cell(2, 0); got.Glyph&flagCursorBar == 0 || got.BG != f.tv.defBG {
		t.Errorf("bar cursor cell: %+v", got)
	}
	f.write("\x1b[2;5H") // move: the old row must be redrawn without the cursor
	f.frame()
	if got := f.cell(2, 0); got.Glyph&(flagCursorBar|flagCursorLine) != 0 {
		t.Errorf("old cursor cell keeps the cursor: %+v", got)
	}
	if got := f.cell(4, 1); got.Glyph&flagCursorBar == 0 {
		t.Errorf("new cursor cell: %+v", got)
	}
	f.write("\x1b[?25l")
	f.frame()
	if got := f.cell(4, 1); got.Glyph&flagCursorBar != 0 {
		t.Errorf("hidden cursor still drawn: %+v", got)
	}
}

func TestOnlyDirtyRowsRebuilt(t *testing.T) {
	f := newFixture(20, 10)
	f.frame()
	if _, rows := f.frame(); rows != 0 {
		t.Fatalf("idle frame rebuilt %d rows", rows)
	}
	f.write("\x1b[5;1Hhello")
	if _, rows := f.frame(); rows > 2 {
		t.Errorf("one changed line rebuilt %d rows (want the line and the cursor's old row)", rows)
	}
}

func TestSynchronizedUpdateHoldsFrame(t *testing.T) {
	f := newFixture(10, 3)
	f.frame()
	f.write("\x1b[?2026hpartial")
	if ok, _ := f.frame(); ok {
		t.Error("pull rendered inside a synchronized update")
	}
	f.write("\x1b[?2026l")
	if ok, rows := f.frame(); !ok || rows == 0 {
		t.Errorf("after the update: ok=%v rows=%d", ok, rows)
	}
}

// TestFrameAllocs: once glyphs are in the atlas, a frame (pull + convert)
// does not allocate. Only the frame is measured; vt10x.Write is not.
func TestFrameAllocs(t *testing.T) {
	f := newFixture(240, 70)
	line := []byte("\x1b[1;32mfunc\x1b[0m main() { fmt.Println(\"héllo 漢字\") } // ─│┌┐ \x1b[4munder\x1b[0m\r\n")
	for i := 0; i < 100; i++ {
		f.term.Write(line) //nolint:errcheck
		f.frame()
	}
	const n = 200
	mallocs := frameMallocs(t, func(i int) {
		f.term.Write(line) //nolint:errcheck
		if ok, rows := f.frame(); !ok || rows != 70 {
			t.Fatalf("frame %d: ok=%v rows=%d, want all 70 rows after a scroll", i, ok, rows)
		}
	}, n)
	if mallocs != 0 {
		t.Errorf("%d frames allocated %d times", n, mallocs)
	}
}

// frameMallocs runs step n times and counts the objects allocated by the
// frame path (termView.pull and termView.convert) only: other goroutines
// and the test's own writes do not count.
func frameMallocs(t *testing.T, step func(int), n int) int64 {
	t.Helper()
	frame := []string{"(*termView).pull", "(*termView).convert", "(*imageLayer).sync"}
	before := mallocsUnder(frame...)
	if before == 0 {
		// The first frames allocate their rows: zero means the names match
		// nothing and the count below would pass vacuously.
		t.Fatalf("no allocation attributed to %v so far", frame)
	}
	for i := 0; i < n; i++ {
		step(i)
	}
	return mallocsUnder(frame...) - before
}

func TestKeyEvents(t *testing.T) {
	for _, tc := range []struct {
		keyval uint
		state  gdk.ModifierType
		want   string
	}{
		{gdk.KEY_Return, 0, "\r"},
		{gdk.KEY_BackSpace, 0, "\x7f"},
		{gdk.KEY_ISO_Left_Tab, gdk.ShiftMask, "\x1b[Z"},
		{gdk.KEY_Up, 0, "\x1b[A"},
		{gdk.KEY_Up, gdk.ControlMask, "\x1b[1;5A"},
		{gdk.KEY_F5, 0, "\x1b[15~"},
		{'a', gdk.ControlMask, "\x01"},
		{'x', gdk.AltMask, "\x1bx"},
		{'A', gdk.ShiftMask, "A"},
		{0xe9, 0, "é"}, // GDK_KEY_eacute
		{gdk.KEY_KP_Enter, 0, "\r"},
	} {
		ev, ok := keyEvent(tc.keyval, tc.state)
		if !ok {
			t.Errorf("keyval %#x: not mapped", tc.keyval)
			continue
		}
		got := input.EncodeKey(ev, input.Modes{})
		if b, ok := ctrlByte(ev); ok {
			got = []byte{b}
		}
		if !bytes.Equal(got, []byte(tc.want)) {
			t.Errorf("keyval %#x state %v: %q, want %q", tc.keyval, tc.state, got, tc.want)
		}
	}
	if ev, _ := keyEvent(' ', gdk.ControlMask); !strings.Contains(string(mustCtrl(t, ev)), "\x00") {
		t.Error("Ctrl+Space does not send NUL")
	}
	if _, ok := keyEvent(gdk.KEY_Shift_L, gdk.ShiftMask); ok {
		t.Error("a bare modifier produced input")
	}
}

func mustCtrl(t *testing.T, ev input.Event) []byte {
	t.Helper()
	b, ok := ctrlByte(ev)
	if !ok {
		t.Fatalf("no control byte for %+v", ev)
	}
	return []byte{b}
}

// BenchmarkFullScreenFrame: every row changes (scrolling output) and is
// pulled and converted.
func BenchmarkFullScreenFrame(b *testing.B) {
	f := newFixture(240, 70)
	line := []byte(strings.Repeat("The quick brown fox jumps over the lazy dog. ", 6)[:239] + "\r\n")
	for i := 0; i < 80; i++ {
		f.term.Write(line) //nolint:errcheck
	}
	f.frame()
	b.ReportAllocs()
	for b.Loop() {
		f.term.Write(line) //nolint:errcheck
		f.frame()
	}
}
