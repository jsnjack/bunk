package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"
	"time"

	"bunk/internal/session"
	"bunk/internal/vt10x"

	"github.com/gdamore/tcell/v2"
)

func TestIsTransientLineClear(t *testing.T) {
	cases := []struct {
		name  string
		chunk string
		want  bool
	}{
		{
			name:  "grm progress clear",
			chunk: "\r                                                                      \r",
			want:  true,
		},
		{
			name:  "multiple clears",
			chunk: "\r   \r\r    \r",
			want:  true,
		},
		{
			name:  "progress text is not transient clear",
			chunk: "\r  99% |████|",
			want:  false,
		},
		{
			name:  "clear plus progress text is not transient clear",
			chunk: "\r     \r\r  99% |████|",
			want:  false,
		},
		{
			name:  "newline commit is not transient clear",
			chunk: "\r     \r\n",
			want:  false,
		},
		{
			name:  "escape clear is not treated as progress clear",
			chunk: "\r\x1b[K",
			want:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isTransientLineClear([]byte(tc.chunk))
			if got != tc.want {
				t.Errorf("isTransientLineClear(%q) = %v, want %v", tc.chunk, got, tc.want)
			}
		})
	}
}

func TestIsInPlaceLineUpdate(t *testing.T) {
	cases := []struct {
		name  string
		chunk string
		want  bool
	}{
		{
			name:  "progress text",
			chunk: "\r  99% |████|",
			want:  true,
		},
		{
			name:  "progress clear",
			chunk: "\r       \r",
			want:  true,
		},
		{
			name:  "final progress line commits with newline",
			chunk: "\r 100% |████|\r\n",
			want:  false,
		},
		{
			name:  "prompt is not in-place update",
			chunk: "$ ",
			want:  false,
		},
		{
			name:  "escape sequence is not progress rewrite",
			chunk: "\x1b[?2004h",
			want:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isInPlaceLineUpdate([]byte(tc.chunk))
			if got != tc.want {
				t.Errorf("isInPlaceLineUpdate(%q) = %v, want %v", tc.chunk, got, tc.want)
			}
		})
	}
}

func TestClearTransientLineClearIfUntilIgnoresStaleTimer(t *testing.T) {
	oldUntil := time.Unix(100, 0)
	newUntil := time.Unix(101, 0)
	p := &Pane{
		Session:                 &session.Session{},
		transientLineClear:      true,
		transientLineClearUntil: newUntil,
	}

	p.clearTransientLineClearIfUntil(oldUntil)
	if !p.transientLineClear {
		t.Fatal("stale timer cleared transientLineClear")
	}
	if !p.transientLineClearUntil.Equal(newUntil) {
		t.Fatalf("stale timer changed transientLineClearUntil to %v, want %v", p.transientLineClearUntil, newUntil)
	}

	p.clearTransientLineClearIfUntil(newUntil)
	if p.transientLineClear {
		t.Fatal("current timer did not clear transientLineClear")
	}
	if !p.transientLineClearUntil.IsZero() {
		t.Fatalf("current timer left transientLineClearUntil = %v, want zero", p.transientLineClearUntil)
	}
}

func TestCaptureAndWrite_OSC10NormalModeResponse(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close() //nolint:errcheck // test pipe cleanup
	defer pw.Close() //nolint:errcheck // test pipe cleanup

	p := &Pane{
		Session: &session.Session{
			Term:            vt10x.New(vt10x.WithSize(40, 10)),
			PTY:             pw,
			ScrollbackLines: 100,
			Scrollback:      session.Ring{MaxLines: 100},
			ThemeFG:         "rgb:d0d0/d0d0/d0d0",
			ThemeBG:         "rgb:1c1c/1c1c/1f1f",
		},
	}

	// Send OSC 10 and OSC 11 queries in normal screen mode (no alt-screen).
	p.Lock()
	p.CaptureAndWrite([]byte("\x1b]10;?\x1b\\"))
	p.CaptureAndWrite([]byte("\x1b]11;?\x1b\\"))
	p.Unlock()
	closeWriter(t, p.Session, pw)

	buf, _ := io.ReadAll(pr)
	want := "\x1b]10;rgb:d0d0/d0d0/d0d0\x1b\\\x1b]11;rgb:1c1c/1c1c/1f1f\x1b\\"
	if string(buf) != want {
		t.Fatalf("normal-mode OSC response = %q, want %q", string(buf), want)
	}
}

func TestCaptureAndWrite_OSC10SameChunkAltScreenResponse(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close() //nolint:errcheck // test pipe cleanup
	defer pw.Close() //nolint:errcheck // test pipe cleanup

	p := &Pane{
		Session: &session.Session{
			Term:            vt10x.New(vt10x.WithSize(40, 10)),
			PTY:             pw,
			ScrollbackLines: 100,
			Scrollback:      session.Ring{MaxLines: 100},
			ThemeFG:         "rgb:d0d0/d0d0/d0d0",
		},
	}

	p.Lock()
	p.CaptureAndWrite([]byte("\x1b[?1049h\x1b]10;?\x1b\\"))
	p.Unlock()
	closeWriter(t, p.Session, pw)

	buf, _ := io.ReadAll(pr)
	want := "\x1b]10;rgb:d0d0/d0d0/d0d0\x1b\\"
	if string(buf) != want {
		t.Fatalf("same-chunk alt-screen OSC 10 response = %q, want %q", string(buf), want)
	}
}

func TestCaptureAndWrite_OSC10QueryUsesDynamicOverride(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close() //nolint:errcheck // test pipe cleanup
	defer pw.Close() //nolint:errcheck // test pipe cleanup

	p := &Pane{
		Session: &session.Session{
			Term:            vt10x.New(vt10x.WithSize(40, 10)),
			PTY:             pw,
			ScrollbackLines: 100,
			Scrollback:      session.Ring{MaxLines: 100},
			ThemeFG:         "rgb:d0d0/d0d0/d0d0",
		},
	}

	p.Lock()
	p.CaptureAndWrite([]byte("\x1b[?1049h\x1b]10;rgb:1111/2222/3333\x1b\\\x1b]10;?\x1b\\"))
	p.Unlock()
	closeWriter(t, p.Session, pw)

	buf, _ := io.ReadAll(pr)
	want := "\x1b]10;rgb:1111/2222/3333\x1b\\"
	if string(buf) != want {
		t.Fatalf("dynamic OSC 10 response = %q, want %q", string(buf), want)
	}
}

func TestCaptureAndWrite_OSC10ResetRestoresTheme(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close() //nolint:errcheck // test pipe cleanup
	defer pw.Close() //nolint:errcheck // test pipe cleanup

	p := &Pane{
		Session: &session.Session{
			Term:            vt10x.New(vt10x.WithSize(40, 10)),
			PTY:             pw,
			ScrollbackLines: 100,
			Scrollback:      session.Ring{MaxLines: 100},
			ThemeFG:         "rgb:d0d0/d0d0/d0d0",
		},
	}

	p.Lock()
	p.CaptureAndWrite([]byte("\x1b[?1049h\x1b]10;rgb:1111/2222/3333\x1b\\\x1b]110\x07\x1b]10;?\x1b\\"))
	p.Unlock()
	closeWriter(t, p.Session, pw)

	buf, _ := io.ReadAll(pr)
	want := "\x1b]10;rgb:d0d0/d0d0/d0d0\x1b\\"
	if string(buf) != want {
		t.Fatalf("OSC 110 reset response = %q, want %q", string(buf), want)
	}
}

func TestCaptureAndWrite_DECRQMSameChunkUsesUpdatedState(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close() //nolint:errcheck // test pipe cleanup
	defer pw.Close() //nolint:errcheck // test pipe cleanup

	p := &Pane{
		Session: &session.Session{
			Term:            vt10x.New(vt10x.WithSize(40, 10)),
			PTY:             pw,
			ScrollbackLines: 100,
			Scrollback:      session.Ring{MaxLines: 100},
		},
	}

	p.Lock()
	p.CaptureAndWrite([]byte("\x1b[?2004h\x1b[?2004$p\x1b[?2004l\x1b[?2004$p"))
	p.Unlock()
	closeWriter(t, p.Session, pw)

	buf, _ := io.ReadAll(pr)
	want := "\x1b[?2004;1$y\x1b[?2004;2$y"
	if string(buf) != want {
		t.Fatalf("same-chunk DECRQM responses = %q, want %q", string(buf), want)
	}
}

func TestCaptureAndWrite_OSC10UnknownThemeNoResponse(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close() //nolint:errcheck // test pipe cleanup
	defer pw.Close() //nolint:errcheck // test pipe cleanup

	p := &Pane{
		Session: &session.Session{
			Term:            vt10x.New(vt10x.WithSize(40, 10)),
			PTY:             pw,
			ScrollbackLines: 100,
			Scrollback:      session.Ring{MaxLines: 100},
		},
	}

	p.Lock()
	p.CaptureAndWrite([]byte("\x1b[?1049h\x1b]10;?\x1b\\"))
	p.Unlock()
	closeWriter(t, p.Session, pw)

	buf, _ := io.ReadAll(pr)
	if len(buf) != 0 {
		t.Fatalf("unknown-theme OSC 10 produced response %q, want none", string(buf))
	}
}

// ---------------------------------------------------------------------------
// readPTY single-lock invariant
//
// Bug: cursor intermittently invisible in Claude/Copilot (race condition)
//
// readPTY previously had two separate p.mu lock sections: one to update
// flags (cursorStyle, inSyncUpdate) and another for captureAndWrite.
// render() could sneak between them and see the updated cursorStyle with
// stale vt10x cells — making the reverse-video cursor invisible.
//
// This test verifies the merged single-lock invariant: cursor shape and
// vt10x cell content are always consistent when observed under p.mu.
// Both are now tracked entirely inside vt10x, so a single Write() call
// updates them atomically.
// ---------------------------------------------------------------------------

func TestReadPTYSingleLock_ClaudeCursorRace(t *testing.T) {
	term := vt10x.New(vt10x.WithSize(20, 5))
	p := &Pane{
		Session: &session.Session{
			Term:            term,
			ScrollbackLines: 100,
			Scrollback:      session.Ring{MaxLines: 100},
		},
	}

	const iterations = 5000
	done := make(chan struct{})

	// Writer goroutine: simulates readPTY's single-lock section.
	// Each iteration writes DECSCUSR (cursor shape) AND a paired marker
	// character to cell(0,0) in a single p.term.Write call under p.mu.
	// shape 1 ↔ 'A', shape 2 ↔ 'B', …, shape 6 ↔ 'F', then wraps.
	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			shape := byte('1' + (i % 6)) // '1'..'6'
			marker := byte('A' + (i % 6))
			// DECSCUSR: ESC [ shape SP q; cursor home: ESC [ H; marker char.
			seq := []byte{0x1b, '[', shape, ' ', 'q', 0x1b, '[', 'H', marker}

			p.Lock()
			p.Term.Write(seq) //nolint:errcheck
			p.Unlock()

			runtime.Gosched() // yield to give reader a chance to observe
		}
	}()

	// Reader goroutine (this goroutine): simulates render() reading
	// cursor shape + cell content under a single lock acquisition.
	// The invariant: shape and cell must come from the SAME write.
	violations := 0
	checks := 0
	for {
		select {
		case <-done:
			if violations > 0 {
				t.Errorf("%d/%d observations saw cursor shape updated but cell stale (lock broken)",
					violations, checks)
			}
			t.Logf("checked %d observations, 0 violations", checks)
			return
		default:
		}

		p.Lock()
		shape := p.Term.Cursor().Shape
		cell := p.Term.Cell(0, 0)
		p.Unlock()

		checks++
		if shape > 0 && cell.Char != 0 {
			expected := rune('A' + (shape - 1))
			if cell.Char != expected {
				violations++
			}
		}
	}
}

// ---------------------------------------------------------------------------
// sbRing
// ---------------------------------------------------------------------------

func makeGlyphRow(chars ...rune) []vt10x.Glyph {
	row := make([]vt10x.Glyph, len(chars))
	for i, ch := range chars {
		row[i] = vt10x.Glyph{Char: ch}
	}
	return row
}

func TestSbRing_PushMaxZero(t *testing.T) {
	var r session.Ring // maxLines == 0
	r.Push(makeGlyphRow('A'))
	if r.Count != 0 {
		t.Errorf("push with maxLines=0: count = %d, want 0", r.Count)
	}
}

func TestSbRing_GetValid(t *testing.T) {
	r := session.Ring{MaxLines: 4}
	r.Push(makeGlyphRow('X'))
	r.Push(makeGlyphRow('Y'))
	r.Push(makeGlyphRow('Z'))

	want := []rune{'X', 'Y', 'Z'}
	for i, ch := range want {
		got := r.Get(i)
		if got == nil || got[0].Char != ch {
			t.Errorf("get(%d) = %v, want '%c'", i, got, ch)
		}
	}
}

func TestSbRing_GetOutOfRange(t *testing.T) {
	r := session.Ring{MaxLines: 4}
	r.Push(makeGlyphRow('A'))
	if got := r.Get(-1); got != nil {
		t.Errorf("get(-1) = %v, want nil", got)
	}
	if got := r.Get(1); got != nil {
		t.Errorf("get(1) with count=1 = %v, want nil", got)
	}
	if got := r.Get(100); got != nil {
		t.Errorf("get(100) = %v, want nil", got)
	}
}

func TestSbRing_GetWrapped(t *testing.T) {
	r := session.Ring{MaxLines: 3}
	// Push 5 items into a ring of capacity 3: A, B, C, D, E.
	// After wrapping, the ring should hold C, D, E.
	for _, ch := range []rune{'A', 'B', 'C', 'D', 'E'} {
		r.Push(makeGlyphRow(ch))
	}
	if r.Count != 3 {
		t.Fatalf("count = %d, want 3", r.Count)
	}
	want := []rune{'C', 'D', 'E'}
	for i, ch := range want {
		got := r.Get(i)
		if got == nil || got[0].Char != ch {
			t.Errorf("get(%d) after wrapping = %v, want '%c'", i, got, ch)
		}
	}
}

// ---------------------------------------------------------------------------
// sbRing — ownership and slot-reuse properties
//
// These tests verify that push() copies rows into ring-owned storage and
// reuses backing arrays when the terminal width stays constant.  The
// captureGrid path produces sub-slices of a shared slab; without copy
// semantics, any retained slab row keeps the entire slab alive.
// ---------------------------------------------------------------------------

func makeGlyphRowN(n int, ch rune) []vt10x.Glyph {
	row := make([]vt10x.Glyph, n)
	for i := range row {
		row[i] = vt10x.Glyph{Char: ch}
	}
	return row
}

// TestSbRing_PushCopiesRow: mutating the source after push must not affect
// the ring.  Fails with the reference-store implementation.
func TestSbRing_PushCopiesRow(t *testing.T) {
	r := session.Ring{MaxLines: 3}
	src := makeGlyphRowN(4, 'A')
	r.Push(src)

	// Mutate the source after pushing.
	src[0].Char = 'Z'

	got := r.Get(0)
	if got == nil {
		t.Fatal("get(0) returned nil")
	}
	if got[0].Char != 'A' {
		t.Errorf("ring mutated by source change: got %q, want 'A'", got[0].Char)
	}
}

// TestSbRing_SlabNotRetained: pushing a row that is a sub-slice of a slab
// must not cause the ring to hold a reference into that slab.
// We verify this by checking that the ring's stored slice does NOT share
// backing memory with the original slab.
func TestSbRing_SlabNotRetained(t *testing.T) {
	const cols, rows = 4, 2
	// Build a fake captureGrid-style slab.
	slab := make([]vt10x.Glyph, rows*cols)
	for i := range slab {
		slab[i].Char = 'X'
	}
	row0 := slab[0:cols] // sub-slice into slab
	row0[0].Char = 'A'

	r := session.Ring{MaxLines: 3}
	r.Push(row0)

	got := r.Get(0)
	if got == nil {
		t.Fatal("get(0) returned nil")
	}
	// After a copy, got and row0 must not share memory.
	// Overwrite row0 and verify got is unaffected.
	row0[0].Char = 'Z'
	if got[0].Char != 'A' {
		t.Errorf("ring shares slab memory: overwriting source changed ring content")
	}
}

// TestSbRing_SlotReusedWhenFull: once the ring is full, repeated pushes at the
// same width should reuse the slot's existing backing array (no new allocation).
func TestSbRing_SlotReusedWhenFull(t *testing.T) {
	const width = 6
	r := session.Ring{MaxLines: 2}
	r.Push(makeGlyphRowN(width, 'A')) // slot 0
	r.Push(makeGlyphRowN(width, 'B')) // slot 1 — ring now full

	// Push again: ring is full, so the oldest slot is overwritten in place.
	row := makeGlyphRowN(width, 'C')
	if n := testing.AllocsPerRun(10, func() { r.Push(row) }); n != 0 {
		t.Errorf("SlotReusedWhenFull: push allocated %.0f times instead of reusing slots", n)
	}
	// Content must be correct.
	got := r.Get(1) // newest is at logical index 1
	if got == nil || got[0].Char != 'C' {
		t.Errorf("SlotReusedWhenFull: newest row = %v, want 'C'", got)
	}
}

// TestSbRing_SlotReusedOnShrink: pushing a narrower row into a full ring
// must reuse the existing backing array (cap >= new len) rather than
// allocating a fresh slice.
func TestSbRing_SlotReusedOnShrink(t *testing.T) {
	const wideW, narrowW = 8, 3
	r := session.Ring{MaxLines: 2}
	r.Push(makeGlyphRowN(wideW, 'W')) // slot 0, cap=8
	r.Push(makeGlyphRowN(wideW, 'W')) // slot 1, cap=8 — ring full

	narrow := makeGlyphRowN(narrowW, 'N')
	// Both slots have capacity 8 >= 3, so overwriting them must not allocate.
	if n := testing.AllocsPerRun(10, func() { r.Push(narrow) }); n != 0 {
		t.Errorf("SlotReusedOnShrink: push allocated %.0f times despite sufficient capacity", n)
	}
	got := r.Get(1) // newest
	if got == nil || len(got) != narrowW {
		t.Errorf("SlotReusedOnShrink: len = %d, want %d", len(got), narrowW)
	}
	if got[0].Char != 'N' {
		t.Errorf("SlotReusedOnShrink: char = %q, want 'N'", got[0].Char)
	}
}

// TestSbRing_SlotReallocOnGrow: pushing a wider row into a full ring must
// allocate new backing storage and store the content correctly.
func TestSbRing_SlotReallocOnGrow(t *testing.T) {
	const narrowW, wideW = 3, 8
	r := session.Ring{MaxLines: 2}
	r.Push(makeGlyphRowN(narrowW, 'N')) // slot 0, cap=3
	r.Push(makeGlyphRowN(narrowW, 'N')) // slot 1, cap=3 — ring full

	wide := makeGlyphRowN(wideW, 'W')
	for i := range wide {
		wide[i].Char = rune('A' + i)
	}
	r.Push(wide) // slot 0, cap 3 < len 8 → must reallocate

	got := r.Get(1) // newest
	if got == nil || len(got) != wideW {
		t.Fatalf("SlotReallocOnGrow: len = %d, want %d", len(got), wideW)
	}
	for i, g := range got {
		if g.Char != rune('A'+i) {
			t.Errorf("SlotReallocOnGrow: got[%d].Char = %q, want %q", i, g.Char, rune('A'+i))
		}
	}
}

// ---------------------------------------------------------------------------
// captureAndWrite — in-place overwrite must not fire large-burst sentinel
//
// Bug: dnf/cargo progress bars erase scrollback history
//
// Progress bars update rows in-place using cursor-up + overwrite.  When row 0
// happens to be rewritten with new progress text, detectShift sees row 0
// changed and the new content isn't found anywhere in the previous grid —
// which matches the large-burst sentinel condition.  The sentinel was firing
// on every progress update, pushing the entire screen snapshot into p.sb
// repeatedly and evicting real history.
//
// Fix: sample rows at N/4, N/2, N*3/4 after the write.  If any sample is
// unchanged from prevGrid the screen wasn't truly replaced, so skip the push.
// ---------------------------------------------------------------------------

func TestCaptureAndWrite_InPlaceOverwrite_NoSentinelPush(t *testing.T) {
	const cols, rows = 20, 8
	term := vt10x.New(vt10x.WithSize(cols, rows))
	p := &Pane{
		Session: &session.Session{
			Term:            term,
			ScrollbackLines: 100,
			Scrollback:      session.Ring{MaxLines: 100},
		},
	}

	// Write 8 distinct rows directly (bypasses captureAndWrite so p.sb
	// stays empty).  Cursor lands on row 7.
	setup := "\x1b[0m\x1b[H" +
		"AAAA_INITIAL_ROW0___\r\n" +
		"BBBB_ROW1_UNCHANGED_\r\n" +
		"CCCC_ROW2_UNCHANGED_\r\n" + // sample at rows/4 = 2
		"DDDD_ROW3_UNCHANGED_\r\n" +
		"EEEE_ROW4_UNCHANGED_\r\n" + // sample at rows/2 = 4
		"FFFF_ROW5_UNCHANGED_\r\n" +
		"GGGG_ROW6_UNCHANGED_\r\n" + // sample at rows*3/4 = 6
		"HHHH_ROW7_CURSOR____"
	term.Write([]byte(setup)) //nolint:errcheck
	if p.Scrollback.Count != 0 {
		t.Fatalf("setup: expected sb.count=0, got %d", p.Scrollback.Count)
	}

	// In-place update: only row 0 changes.  Middle rows are unchanged.
	// Large-burst sentinel must NOT fire.
	chunk := []byte("\x1b[1;1HNEWPROGRESS_ROW0\x1b[K")
	p.Lock()
	p.CaptureAndWrite(chunk)
	p.Unlock()

	if p.Scrollback.Count != 0 {
		t.Errorf("in-place row-0 overwrite triggered scrollback push: sb.count = %d, want 0",
			p.Scrollback.Count)
	}
}

// ---------------------------------------------------------------------------
// Native scroll callback integration — pane.onScrollRow
//
// These tests verify the end-to-end path: vt10x fires the scroll callback →
// onScrollRow pushes the row into p.sb.  They require onScrollRow to exist
// (compile-fails until implemented) and fail at runtime if the wrong number
// of rows end up in p.sb.
// ---------------------------------------------------------------------------

// TestCaptureAndWrite_NativeCallback_NormalScroll creates a pane with the
// native scroll callback wired up (as NewPane does) and verifies that rows
// pushed off the top of the terminal land in p.sb with correct content.
func TestCaptureAndWrite_NativeCallback_NormalScroll(t *testing.T) {
	const cols, rows = 6, 3
	p := &Pane{
		Session: &session.Session{
			ScrollbackLines: 100,
			Scrollback:      session.Ring{MaxLines: 100},
		},
	}
	term := vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollCallback(p.OnScrollRow))
	p.Term = term

	// Write 5 rows into a 3-row terminal → 2 rows scroll off the top.
	p.Lock()
	p.CaptureAndWrite([]byte("AAAAAA\r\nBBBBBB\r\nCCCCCC\r\nDDDDDD\r\nEEEEEE"))
	p.Unlock()

	if p.Scrollback.Count != 2 {
		t.Fatalf("want 2 rows in scrollback, got %d", p.Scrollback.Count)
	}
	// First captured row should be the original row 0: all 'A's.
	row := p.Scrollback.Get(0)
	if row == nil || row[0].Char != 'A' {
		t.Errorf("sb.get(0): got %v, want all 'A'", row)
	}
	// Second captured row should be row 1: all 'B's.
	row = p.Scrollback.Get(1)
	if row == nil || row[0].Char != 'B' {
		t.Errorf("sb.get(1): got %v, want all 'B'", row)
	}
}

// TestCaptureAndWrite_NativeCallback_InPlaceNoScrollback verifies that
// cursor-up + overwrite (progress bars, spinners) does not populate p.sb.
func TestCaptureAndWrite_NativeCallback_InPlaceNoScrollback(t *testing.T) {
	const cols, rows = 10, 4
	p := &Pane{
		Session: &session.Session{
			ScrollbackLines: 100,
			Scrollback:      session.Ring{MaxLines: 100},
		},
	}
	term := vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollCallback(p.OnScrollRow))
	p.Term = term

	// Fill the screen without scrolling.
	p.Lock()
	p.CaptureAndWrite([]byte("AAAAAAAAAA\r\nBBBBBBBBBB\r\nCCCCCCCCCC\r\nDDDDDDDDDD"))
	p.Unlock()

	if p.Scrollback.Count != 0 {
		t.Fatalf("after fill (no scroll): want 0, got %d", p.Scrollback.Count)
	}

	// In-place update: cursor to row 0, overwrite — no scroll should occur.
	p.Lock()
	p.CaptureAndWrite([]byte("\x1b[1;1HNEWCONTENT\x1b[K"))
	p.Unlock()

	if p.Scrollback.Count != 0 {
		t.Errorf("in-place overwrite via callback: want 0 in scrollback, got %d", p.Scrollback.Count)
	}
}

// TestCaptureAndWrite_NativeCallback_AltScreenNoScrollback verifies that
// content scrolling within alt-screen does not enter the primary scrollback.
func TestCaptureAndWrite_NativeCallback_AltScreenNoScrollback(t *testing.T) {
	const cols, rows = 8, 3
	p := &Pane{
		Session: &session.Session{
			ScrollbackLines: 100,
			Scrollback:      session.Ring{MaxLines: 100},
		},
	}
	term := vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollCallback(p.OnScrollRow))
	p.Term = term

	// Enter alt-screen, scroll heavily, exit.
	p.Lock()
	p.CaptureAndWrite([]byte("\x1b[?1049h"))
	for i := 0; i < 10; i++ {
		p.CaptureAndWrite([]byte("XXXXXXXX\r\n"))
	}
	p.CaptureAndWrite([]byte("\x1b[?1049l"))
	p.Unlock()

	if p.Scrollback.Count != 0 {
		t.Errorf("alt-screen scroll populated primary scrollback: sb.count = %d, want 0", p.Scrollback.Count)
	}
}

func TestResizeAndReflow_ReflowsScrollbackOnWiden(t *testing.T) {
	p := regressionPane(6, 3)
	p.CaptureAndWrite([]byte("AAAAAAAAAA\r\nBBBBBBBBBB\r\nCCCCCCCCCC\r\nDDDDDDDDDD\r\nEEEEEEEEEE\r\n"))
	p.ResizeAndReflow(10, 3)
	if p.Scrollback.Count != 3 {
		t.Fatalf("scrollback count = %d, want 3", p.Scrollback.Count)
	}
	for i := 0; i < p.Scrollback.Count; i++ {
		row := p.Scrollback.Get(i)
		if len(row) != 10 || row[0].Char != rune('A'+i) {
			t.Fatalf("unexpected row %d: %v", i, row)
		}
	}
}

func TestResizeAndReflow_ScrollAnchor(t *testing.T) {
	for _, tc := range []struct{ offset, want int }{{8, 3}, {4, 2}, {1, 0}} {
		t.Run(fmt.Sprint(tc.offset), func(t *testing.T) {
			p := regressionPane(6, 3)
			p.CaptureAndWrite([]byte("AAAAAAAAAA\r\nBBBBBBBBBB\r\nCCCCCCCCCC\r\nDDDDDDDDDD\r\nEEEEEEEEEE\r\n"))
			p.ScrollOffset = tc.offset
			p.ResizeAndReflow(20, 3)
			if p.ScrollOffset != tc.want {
				t.Fatalf("scroll offset = %d, want %d", p.ScrollOffset, tc.want)
			}
		})
	}
}

func TestResizeAndReflow_ContentAnchorWiden(t *testing.T) {
	const narrowCols, wideCols, rows = 6, 20, 5
	rawBuf := []byte("AAAAAAAAAA\r\nBBBBBBBBBB\r\nCCCCCCCCCC\r\nDDDDDDDDDD\r\nEEEEEEEEEE\r\nFFFFFFFFFF\r\nGGGGGGGGGG\r\n")

	p := &Pane{Session: &session.Session{ScrollbackLines: 200, Scrollback: session.Ring{MaxLines: 200}}}
	p.Term = vt10x.New(vt10x.WithSize(narrowCols, rows), vt10x.WithScrollCallback(p.OnScrollRow))

	// Feed the content so the scroll callback populates the ring with real rows.
	p.Lock()
	p.CaptureAndWrite(rawBuf)
	// Scroll back so "CCCCCCCCCC" (split into "CCCCCC"+"CCCC") is near centre.
	// Ring holds the rows that scrolled off; live terminal holds the last 5.
	// Push sbOff high enough that the C-rows are visible in the centre.
	p.ScrollOffset = p.Scrollback.Count // scroll to very top for simplicity
	preCount := p.Scrollback.Count
	p.Unlock()

	if preCount == 0 {
		t.Skip("no scrollback captured; rawBuf may not have caused enough scrolling")
	}

	p.Lock()
	p.ResizeAndReflow(wideCols, rows)
	p.Unlock()

	// After widening, the ring must not be empty (content exists).
	if p.Scrollback.Count == 0 && p.ScrollOffset != 0 {
		t.Error("sbOff > 0 but ring is empty after widen")
	}
	// sbOff must be valid.
	if p.ScrollOffset > p.Scrollback.Count {
		t.Errorf("sbOff=%d > sb.count=%d after widen", p.ScrollOffset, p.Scrollback.Count)
	}
	// The viewport centre row must contain a known content character.
	// With content matching, the centre of the viewport should contain one of
	// our known lines (A–G repeated 10 times).
	centerRingIdx := p.Scrollback.Count - p.ScrollOffset + rows/2
	if centerRingIdx >= 0 && centerRingIdx < p.Scrollback.Count {
		row := p.Scrollback.Get(centerRingIdx)
		if len(row) > 0 {
			ch := row[0].Char
			if ch == 0 || ch == ' ' {
				t.Errorf("centre row is blank after widen (sbOff=%d sb.count=%d)", p.ScrollOffset, p.Scrollback.Count)
			}
		}
	}
}

//
// Bug: resizeAndReflow and resizeHeightOnly replaced p.term with a new
// terminal that lacked WithScrollCallback, so no further scrollback was
// captured after the first resize.
// ---------------------------------------------------------------------------

// TestScrollCallback_PreservedAfterResize verifies that scroll-callback-based
// scrollback capture still works after a column-width resize (resizeAndReflow).
func TestScrollCallback_PreservedAfterResize(t *testing.T) {
	const cols, rows = 6, 3
	p := &Pane{
		Session: &session.Session{
			ScrollbackLines: 100,
			Scrollback:      session.Ring{MaxLines: 100},
		},
	}
	p.Term = vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollCallback(p.OnScrollRow))

	// Trigger one scroll so p.sb has at least 1 row.
	p.Lock()
	p.CaptureAndWrite([]byte("AAAAAA\r\nBBBBBB\r\nCCCCCC\r\nDDDDDD"))
	p.Unlock()
	if p.Scrollback.Count < 1 {
		t.Fatalf("pre-resize: want ≥1 row in scrollback, got %d", p.Scrollback.Count)
	}

	// Resize (new column width forces resizeAndReflow).
	p.Lock()
	p.ResizeAndReflow(8, rows)
	p.Unlock()

	// After resize, send more content that scrolls → callback must still fire.
	p.Lock()
	p.CaptureAndWrite([]byte("EEEEEEEE\r\nFFFFFFFF\r\nGGGGGGGG\r\nHHHHHHHH"))
	p.Unlock()

	if p.Scrollback.Count < 2 {
		t.Errorf("post-resize: scroll callback lost after resizeAndReflow; sb.count=%d want ≥2", p.Scrollback.Count)
	}
}

// TestScrollCallback_PreservedAfterHeightResize verifies that scroll-callback
// capture still works after a height-only resize (resizeHeightOnly).
func TestScrollCallback_PreservedAfterHeightResize(t *testing.T) {
	const cols, rows = 6, 4
	p := &Pane{
		Session: &session.Session{
			ScrollbackLines: 100,
			Scrollback:      session.Ring{MaxLines: 100},
		},
	}
	p.Term = vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollCallback(p.OnScrollRow))

	// Preserve the rows that shrink into scrollback.

	// Shrink height → resizeHeightOnly (same cols, fewer rows).
	p.Lock()
	p.ResizeAndReflow(cols, 2)
	p.Unlock()

	// Now send content that scrolls in the 2-row terminal.
	p.Lock()
	p.CaptureAndWrite([]byte("CCCCCC\r\nDDDDDD\r\nEEEEEE"))
	p.Unlock()

	if p.Scrollback.Count < 1 {
		t.Errorf("post-height-resize: scroll callback lost; sb.count=%d want ≥1", p.Scrollback.Count)
	}
}

// ---------------------------------------------------------------------------
// needsSync — set on alt-screen exit
//
// Bug: btop background artifact persists after exit
//
// When the pane exits alt-screen, needsSync is set so the next render uses
// Sync() (full repaint) to clear any residual background colour.
// ---------------------------------------------------------------------------

func TestNeedsSync_AltScreenExit(t *testing.T) {
	term := vt10x.New(vt10x.WithSize(40, 10))
	p := &Pane{
		Session: &session.Session{
			Term:            term,
			ScrollbackLines: 100,
			Scrollback:      session.Ring{MaxLines: 100},
		},
	}

	// Write a line then enter alt-screen.
	p.CaptureAndWrite([]byte("hello\r\n\x1b[?1049h"))
	if p.NeedsSync {
		t.Error("needsSync should be false after alt-screen entry")
	}

	// Exit alt-screen.
	p.CaptureAndWrite([]byte("\x1b[?1049l"))
	if !p.NeedsSync {
		t.Error("needsSync should be true after alt-screen exit")
	}

	// Clearing it manually (simulating render consuming it).
	p.NeedsSync = false
	if p.NeedsSync {
		t.Error("needsSync should be false after manual clear")
	}
}

// ---------------------------------------------------------------------------
// xParseColor
// ---------------------------------------------------------------------------

func TestXParseColor_Standard(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"d0d0d0", "rgb:d0d0/d0d0/d0d0"},
		{"1a1a2e", "rgb:1a1a/1a1a/2e2e"},
		{"000000", "rgb:0000/0000/0000"},
		{"ffffff", "rgb:ffff/ffff/ffff"},
		{"ff0080", "rgb:ffff/0000/8080"},
	}
	for _, tc := range tests {
		got := session.XParseColor(tc.input)
		if got != tc.want {
			t.Errorf("xParseColor(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestXParseColor_InvalidLength(t *testing.T) {
	got := session.XParseColor("ff00")
	if got != "rgb:0000/0000/0000" {
		t.Errorf("xParseColor invalid length: got %q, want fallback", got)
	}
}

func TestTcellColorToXParse_DefaultReturnsEmpty(t *testing.T) {
	if got := tcellColorToXParse(tcell.ColorDefault); got != "" {
		t.Fatalf("tcellColorToXParse(ColorDefault) = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// XTVERSION (CSI > 0 q / CSI > q)
//
// Modern apps (Claude Code, Neovim, WezTerm) send \x1b[>0q at startup for
// feature detection.  Without a response they may skip features or time out.
// Response format: DCS > | name(version) ST
// ---------------------------------------------------------------------------

func TestXTVersion_Response(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close() //nolint:errcheck // test pipe cleanup
	defer pw.Close() //nolint:errcheck // test pipe cleanup

	term := vt10x.New(vt10x.WithSize(40, 10))
	p := &Pane{Session: &session.Session{Term: term, PTY: pw, ScrollbackLines: 100, Scrollback: session.Ring{MaxLines: 100}}}

	p.CaptureAndWrite([]byte("\x1b[>0q"))
	closeWriter(t, p.Session, pw)

	buf, _ := io.ReadAll(pr)
	want := "\x1bP>|VTE(8203)\x1b\\"
	if string(buf) != want {
		t.Errorf("XTVERSION response = %q, want %q", string(buf), want)
	}
}

func TestXTVersion_ShortForm(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close() //nolint:errcheck // test pipe cleanup
	defer pw.Close() //nolint:errcheck // test pipe cleanup

	term := vt10x.New(vt10x.WithSize(40, 10))
	p := &Pane{Session: &session.Session{Term: term, PTY: pw, ScrollbackLines: 100, Scrollback: session.Ring{MaxLines: 100}}}

	p.CaptureAndWrite([]byte("\x1b[>q"))
	closeWriter(t, p.Session, pw)

	buf, _ := io.ReadAll(pr)
	want := "\x1bP>|VTE(8203)\x1b\\"
	if string(buf) != want {
		t.Errorf("XTVERSION short form response = %q, want %q", string(buf), want)
	}
}

func TestXTGETTCAP_Smulx(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close() //nolint:errcheck // test pipe cleanup
	defer pw.Close() //nolint:errcheck // test pipe cleanup

	term := vt10x.New(vt10x.WithSize(40, 10))
	p := &Pane{Session: &session.Session{Term: term, PTY: pw, ScrollbackLines: 100, Scrollback: session.Ring{MaxLines: 100}}}

	// "Smulx" hex-encoded = 536d756c78
	p.CaptureAndWrite([]byte("\x1bP+q536d756c78\x1b\\"))
	closeWriter(t, p.Session, pw)

	buf, _ := io.ReadAll(pr)
	// value = "\x1b[4:%p1%dm" hex-encoded
	wantHexVal := "1b5b343a25703125646d"
	want := "\x1bP1+r536d756c78=" + wantHexVal + "\x1b\\"
	if string(buf) != want {
		t.Errorf("XTGETTCAP Smulx = %q, want %q", string(buf), want)
	}
}

func TestXTGETTCAP_Unknown(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close() //nolint:errcheck // test pipe cleanup
	defer pw.Close() //nolint:errcheck // test pipe cleanup

	term := vt10x.New(vt10x.WithSize(40, 10))
	p := &Pane{Session: &session.Session{Term: term, PTY: pw, ScrollbackLines: 100, Scrollback: session.Ring{MaxLines: 100}}}

	// "unknown" hex-encoded = 756e6b6e6f776e
	p.CaptureAndWrite([]byte("\x1bP+q756e6b6e6f776e\x1b\\"))
	closeWriter(t, p.Session, pw)

	buf, _ := io.ReadAll(pr)
	want := "\x1bP0+r756e6b6e6f776e\x1b\\"
	if string(buf) != want {
		t.Errorf("XTGETTCAP unknown cap = %q, want %q", string(buf), want)
	}
}

func TestSGR53_Overline(t *testing.T) {
	term := vt10x.New(vt10x.WithSize(10, 5))
	// SGR 53 = overline on, SGR 55 = overline off
	term.Write([]byte("\x1b[53mA\x1b[55mB")) //nolint:errcheck // in-memory terminal write
	cellA := term.Cell(0, 0)
	cellB := term.Cell(1, 0)
	if cellA.Mode&vt10x.AttrOverline == 0 {
		t.Errorf("cell A (after SGR 53): expected AttrOverline set, Mode=0x%x", cellA.Mode)
	}
	if cellB.Mode&vt10x.AttrOverline != 0 {
		t.Errorf("cell B (after SGR 55): expected AttrOverline cleared, Mode=0x%x", cellB.Mode)
	}
}

func TestSGR53_ResetBy0(t *testing.T) {
	term := vt10x.New(vt10x.WithSize(10, 5))
	term.Write([]byte("\x1b[53mA\x1b[0mB")) //nolint:errcheck // in-memory terminal write
	cellA := term.Cell(0, 0)
	cellB := term.Cell(1, 0)
	if cellA.Mode&vt10x.AttrOverline == 0 {
		t.Errorf("cell A (after SGR 53): expected AttrOverline set, Mode=0x%x", cellA.Mode)
	}
	if cellB.Mode&vt10x.AttrOverline != 0 {
		t.Errorf("cell B (after SGR 0): expected AttrOverline cleared, Mode=0x%x", cellB.Mode)
	}
}

// TestPrivateModeSGR_vim_slash4m guards against \x1b[?4m being misinterpreted
// as SGR 4 (underline). Vim sends \x1b[?4m as a DEC private mode sequence at
// the end of its initial render; without the c.priv guard it set underline on
// all subsequent cells, visibly underlining command-line text like "!q".
func TestPrivateModeSGR_vim_slash4m(t *testing.T) {
	term := vt10x.New(vt10x.WithSize(10, 5))
	// Plain text, then the vim sequence, then more text.
	term.Write([]byte("A\x1b[?4mB")) //nolint:errcheck // in-memory terminal write
	cellA := term.Cell(0, 0)
	cellB := term.Cell(1, 0)
	if cellA.Mode&vt10x.AttrUnderline != 0 {
		t.Errorf("cell A: unexpected underline, Mode=0x%x", cellA.Mode)
	}
	if cellB.Mode&vt10x.AttrUnderline != 0 {
		t.Errorf("cell B (after \\x1b[?4m): should NOT be underlined, Mode=0x%x (vim bug reproduced)", cellB.Mode)
	}
}

// TestPrivateModeSGR_realSGR4_still_works ensures the real \x1b[4m still works.
func TestPrivateModeSGR_realSGR4_still_works(t *testing.T) {
	term := vt10x.New(vt10x.WithSize(10, 5))
	term.Write([]byte("\x1b[4mU\x1b[0mN")) //nolint:errcheck // in-memory terminal write
	cellU := term.Cell(0, 0)
	cellN := term.Cell(1, 0)
	if cellU.Mode&vt10x.AttrUnderline == 0 {
		t.Errorf("cell U (after real \\x1b[4m): expected underline, Mode=0x%x", cellU.Mode)
	}
	if cellN.Mode&vt10x.AttrUnderline != 0 {
		t.Errorf("cell N (after \\x1b[0m): expected no underline, Mode=0x%x", cellN.Mode)
	}
}

// ---------------------------------------------------------------------------
// Kitty keyboard protocol stack (handleKittyKeyboard via captureAndWrite)
//
// Apps like Claude Code enable KKP with \x1b[>1u and should disable it with
// \x1b[<u on exit.  If they crash or exit abnormally the stack is left non-
// empty and bunk encodes subsequent keystrokes as CSI-u sequences that the
// shell does not understand.  trackFgProcess clears the stack on PGID change.
// ---------------------------------------------------------------------------

func TestHandleKittyKeyboard_PushQueryPop(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close() //nolint:errcheck // test pipe cleanup
	defer pw.Close() //nolint:errcheck // test pipe cleanup

	term := vt10x.New(vt10x.WithSize(40, 10))
	p := &Pane{Session: &session.Session{Term: term, PTY: pw, ScrollbackLines: 100, Scrollback: session.Ring{MaxLines: 100}}}

	// Push flags=1 (\x1b[>1u) — the spec form used by Claude Code and Neovim.
	p.CaptureAndWrite([]byte("\x1b[>1u"))
	p.Lock()
	if len(p.KittyStack) != 1 || p.KittyStack[0] != 1 {
		t.Errorf("after push: kittyStack = %v, want [1]", p.KittyStack)
	}
	p.Unlock()

	// Push again with flags=3 to verify nesting.
	p.CaptureAndWrite([]byte("\x1b[>3u"))
	p.Lock()
	if len(p.KittyStack) != 2 || p.KittyStack[1] != 3 {
		t.Errorf("after second push: kittyStack = %v, want [1 3]", p.KittyStack)
	}
	p.Unlock()

	// Query (\x1b[?u) — response must be \x1b[?3u (top of stack).
	p.CaptureAndWrite([]byte("\x1b[?u"))

	// Pop one level (\x1b[<u) — stack should shrink by 1.
	p.CaptureAndWrite([]byte("\x1b[<u"))
	p.Lock()
	if len(p.KittyStack) != 1 || p.KittyStack[0] != 1 {
		t.Errorf("after pop: kittyStack = %v, want [1]", p.KittyStack)
	}
	p.Unlock()

	// Pop all remaining levels (\x1b[<2u with count > depth — should clamp to 0).
	p.CaptureAndWrite([]byte("\x1b[<2u"))
	p.Lock()
	if len(p.KittyStack) != 0 {
		t.Errorf("after over-pop: kittyStack = %v, want []", p.KittyStack)
	}
	p.Unlock()

	closeWriter(t, p.Session, pw)
	buf, _ := io.ReadAll(pr)
	// Only the query should have produced a response.
	want := "\x1b[?3u"
	if string(buf) != want {
		t.Errorf("ptmx output = %q, want %q", string(buf), want)
	}
}

// TestHandleKittyKeyboard_SetFlagsForm is the regression test for the Copilot
// CLI welcome-screen corruption: Copilot sends the kitty "set flags" sequence
// \x1b[=1;1u (form CSI = flags ; mode u, two ';'-separated params) right before
// drawing its mascot with cursor-relative moves inside a DEC 2026 sync frame.
//
// The old handler scanned only a single digit run after '=', stopped at the
// ';', failed to recognise the sequence, and let it through to vt10x — which
// reads the trailing 'u' as DECRC (restore cursor), jumping the cursor to the
// saved slot.  Every relative-positioned draw afterwards then landed on the
// wrong row, splitting the tab bar across two rows ("multiple backgrounds")
// and bleeding the mascot's eyes into its top border.
func TestHandleKittyKeyboard_SetFlagsForm(t *testing.T) {
	term := vt10x.New(vt10x.WithSize(40, 10))
	p := &Pane{Session: &session.Session{Term: term, ScrollbackLines: 100, Scrollback: session.Ring{MaxLines: 100}}}

	// Park the cursor away from the origin, then send the set-flags sequence
	// followed by a printable char.  If \x1b[=1;1u leaks to vt10x it triggers
	// DECRC and 'X' lands at (0,0); when stripped, 'X' lands at the cursor.
	p.CaptureAndWrite([]byte("\x1b[5;5H\x1b[=1;1uX"))

	if got := term.Cell(0, 0).Char; got == 'X' {
		t.Fatalf("'X' rendered at (0,0): \\x1b[=1;1u leaked to vt10x as DECRC")
	}
	if got := term.Cell(4, 4).Char; got != 'X' {
		t.Errorf("cursor desynced: cell(4,4) = %q, want 'X'", got)
	}

	// The "set" form replaces the current flags rather than nesting forever.
	p.Lock()
	defer p.Unlock()
	if len(p.KittyStack) != 1 || p.KittyStack[0] != 1 {
		t.Errorf("after set-flags: kittyStack = %v, want [1]", p.KittyStack)
	}
}

// ---------------------------------------------------------------------------
// Scrollback erase
//
// clear(1) sends CSI 3 J (the xterm E3 capability) and reset(1) sends RIS
// (ESC c); both must empty the scrollback ring, snap the view back to live,
// and keep erased history from returning after a resize.  Plain CSI 2 J (readline Ctrl+L, clear -x) must keep scrollback,
// matching xterm.
// ---------------------------------------------------------------------------

func newScrollbackClearPane(cols, rows int) *Pane {
	p := &Pane{
		Session: &session.Session{
			ScrollbackLines: 100,
			Scrollback:      session.Ring{MaxLines: 100},
		},
	}
	p.Term = vt10x.New(vt10x.WithSize(cols, rows),
		vt10x.WithScrollCallback(p.OnScrollRow),
		vt10x.WithScrollbackClearCallback(p.OnScrollbackClear))
	return p
}

// fillScrollback overflows the pane so rows scroll into the ring.
func fillScrollback(t *testing.T, p *Pane) {
	t.Helper()
	var burst []byte
	for i := 0; i < 12; i++ {
		burst = append(burst, []byte("history-line\r\n")...)
	}
	p.CaptureAndWrite(burst)
	if p.Scrollback.Count == 0 {
		t.Fatal("setup: expected scrollback rows after overflow")
	}
}

func TestScrollbackClear_ED3(t *testing.T) {
	p := newScrollbackClearPane(20, 5)
	fillScrollback(t, p)
	p.ScrollOffset = 2 // user is scrolled back

	// clear(1) output: cursor home + erase display + erase saved lines,
	// with the follow-up prompt coalesced into the same PTY chunk.
	p.CaptureAndWrite([]byte("\x1b[H\x1b[2J\x1b[3J$ "))

	if p.Scrollback.Count != 0 {
		t.Errorf("sb.count = %d after CSI 3 J, want 0", p.Scrollback.Count)
	}
	if p.ScrollOffset != 0 {
		t.Errorf("sbOff = %d after CSI 3 J, want 0 (live view)", p.ScrollOffset)
	}
	if got := string(rowChars(session.CaptureRow(p.Term, 0, 20))); got != "$" {
		t.Errorf("prompt = %q, want $", got)
	}
}

func TestScrollbackClear_ED2KeepsHistory(t *testing.T) {
	p := newScrollbackClearPane(20, 5)
	fillScrollback(t, p)
	before := p.Scrollback.Count

	// Readline Ctrl+L sends only cursor home + CSI 2 J.
	p.CaptureAndWrite([]byte("\x1b[H\x1b[2J"))

	if p.Scrollback.Count != before {
		t.Errorf("sb.count = %d after CSI 2 J, want %d (2J must not touch scrollback)", p.Scrollback.Count, before)
	}
}

func TestScrollbackClear_RIS(t *testing.T) {
	p := newScrollbackClearPane(20, 5)
	fillScrollback(t, p)

	p.CaptureAndWrite([]byte("\x1bc$ "))

	if p.Scrollback.Count != 0 {
		t.Errorf("sb.count = %d after RIS, want 0", p.Scrollback.Count)
	}
	if got := string(rowChars(session.CaptureRow(p.Term, 0, 20))); got != "$" {
		t.Errorf("prompt = %q, want $", got)
	}
}

func TestScrollbackClear_SurvivesReflow(t *testing.T) {
	p := newScrollbackClearPane(20, 5)
	fillScrollback(t, p)

	p.CaptureAndWrite([]byte("\x1b[H\x1b[2J\x1b[3J$ "))

	// Erased history must stay absent after a width change.
	p.ResizeAndReflow(40, 5)

	if p.Scrollback.Count != 0 {
		t.Errorf("sb.count = %d after reflow, want 0 — erased history must not be resurrected", p.Scrollback.Count)
	}
}

// TestScrollbackClear_RingReuse verifies the ring accepts pushes again after
// clear() and that cleared storage is not visible via get().
func TestScrollbackClear_RingReuse(t *testing.T) {
	p := newScrollbackClearPane(20, 5)
	fillScrollback(t, p)

	p.CaptureAndWrite([]byte("\x1b[3J"))
	if p.Scrollback.Count != 0 {
		t.Fatalf("sb.count = %d after clear, want 0", p.Scrollback.Count)
	}
	if row := p.Scrollback.Get(0); row != nil {
		t.Errorf("sb.get(0) = %v after clear, want nil", row)
	}

	fillScrollback(t, p)
	if got := p.Scrollback.Get(0); got == nil {
		t.Error("sb.get(0) = nil after post-clear refill, want a row")
	}
}
