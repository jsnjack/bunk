package main

import (
	"io"
	"os"
	"runtime"
	"testing"

	"bunk/internal/vt10x"
)

// ---------------------------------------------------------------------------
// scanDECRQM
//
// scanDECRQM replaced the old per-mode DECRQM list with a generic scanner
// that finds all \x1b[?N$p queries in a chunk and calls a callback for each.
// ---------------------------------------------------------------------------

func TestScanDECRQM_Single(t *testing.T) {
	var modes []int
	scanDECRQM([]byte("\x1b[?2026$p"), func(n int) { modes = append(modes, n) })
	if len(modes) != 1 || modes[0] != 2026 {
		t.Errorf("got %v, want [2026]", modes)
	}
}

func TestScanDECRQM_Multiple(t *testing.T) {
	var modes []int
	scanDECRQM([]byte("\x1b[?2004$p some junk \x1b[?1049$p"), func(n int) { modes = append(modes, n) })
	if len(modes) != 2 || modes[0] != 2004 || modes[1] != 1049 {
		t.Errorf("got %v, want [2004 1049]", modes)
	}
}

func TestScanDECRQM_NotPresent(t *testing.T) {
	var modes []int
	scanDECRQM([]byte("hello world"), func(n int) { modes = append(modes, n) })
	if len(modes) != 0 {
		t.Errorf("expected no modes, got %v", modes)
	}
}

func TestScanDECRQM_MultiDigitMode(t *testing.T) {
	var modes []int
	scanDECRQM([]byte("\x1b[?1000$p\x1b[?2026$p"), func(n int) { modes = append(modes, n) })
	if len(modes) != 2 || modes[0] != 1000 || modes[1] != 2026 {
		t.Errorf("got %v, want [1000 2026]", modes)
	}
}

func feedSplitPTYChunks(t *testing.T, p *Pane, chunks ...[]byte) {
	t.Helper()
	var carry []byte
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, chunk := range chunks {
		combined := append(append([]byte(nil), carry...), chunk...)
		complete, nextCarry := splitPTYChunk(combined)
		if len(complete) > 0 {
			p.captureAndWrite(complete)
		}
		carry = nextCarry
	}

	if len(carry) != 0 {
		t.Fatalf("splitPTYChunk left %q buffered after final chunk", carry)
	}
}

func TestSplitPTYChunk_PartialControlCarry(t *testing.T) {
	tests := []struct {
		name         string
		input        []byte
		wantComplete []byte
		wantCarry    []byte
	}{
		{
			name:         "plain text",
			input:        []byte("hello"),
			wantComplete: []byte("hello"),
		},
		{
			name:         "partial csi",
			input:        []byte("abc\x1b[>0"),
			wantComplete: []byte("abc"),
			wantCarry:    []byte("\x1b[>0"),
		},
		{
			name:         "partial osc",
			input:        []byte("abc\x1b]10"),
			wantComplete: []byte("abc"),
			wantCarry:    []byte("\x1b]10"),
		},
		{
			name:      "partial dcs",
			input:     []byte("\x1bP+q536d"),
			wantCarry: []byte("\x1bP+q536d"),
		},
		{
			name:      "partial utf8",
			input:     []byte{0xe2, 0x96},
			wantCarry: []byte{0xe2, 0x96},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			complete, carry := splitPTYChunk(tc.input)
			if string(complete) != string(tc.wantComplete) {
				t.Fatalf("complete = %q, want %q", complete, tc.wantComplete)
			}
			if string(carry) != string(tc.wantCarry) {
				t.Fatalf("carry = %q, want %q", carry, tc.wantCarry)
			}
		})
	}
}

func TestCaptureAndWrite_SplitXTVersionResponse(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close()
	defer pw.Close()

	p := &Pane{
		term:            vt10x.New(vt10x.WithSize(40, 10)),
		ptmx:            pw,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	feedSplitPTYChunks(t, p, []byte("\x1b[>"), []byte("0q"))
	pw.Close()

	buf, _ := io.ReadAll(pr)
	want := "\x1bP>|VTE(8203)\x1b\\"
	if string(buf) != want {
		t.Fatalf("split XTVERSION response = %q, want %q", string(buf), want)
	}
}

func TestCaptureAndWrite_SplitXTGETTCAPResponse(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close()
	defer pw.Close()

	p := &Pane{
		term:            vt10x.New(vt10x.WithSize(40, 10)),
		ptmx:            pw,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	feedSplitPTYChunks(t, p, []byte("\x1bP+q536d"), []byte("756c78\x1b\\"))
	pw.Close()

	buf, _ := io.ReadAll(pr)
	wantHexVal := "1b5b343a25703125646d"
	want := "\x1bP1+r536d756c78=" + wantHexVal + "\x1b\\"
	if string(buf) != want {
		t.Fatalf("split XTGETTCAP response = %q, want %q", string(buf), want)
	}
}

func TestCaptureAndWrite_SplitOSC10Response(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close()
	defer pw.Close()

	p := &Pane{
		term:            vt10x.New(vt10x.WithSize(40, 10)),
		ptmx:            pw,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
		themeFGColor:    "rgb:d0d0/d0d0/d0d0",
	}

	feedSplitPTYChunks(t, p, []byte("\x1b]10"), []byte(";?\x1b\\"))
	pw.Close()

	buf, _ := io.ReadAll(pr)
	want := "\x1b]10;rgb:d0d0/d0d0/d0d0\x1b\\"
	if string(buf) != want {
		t.Fatalf("split OSC 10 response = %q, want %q", string(buf), want)
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
		term:            term,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
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

			p.mu.Lock()
			p.term.Write(seq) //nolint:errcheck
			p.mu.Unlock()

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

		p.mu.Lock()
		shape := p.term.Cursor().Shape
		cell := p.term.Cell(0, 0)
		p.mu.Unlock()

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
	var r sbRing // maxLines == 0
	r.push(makeGlyphRow('A'))
	if r.count != 0 {
		t.Errorf("push with maxLines=0: count = %d, want 0", r.count)
	}
}

func TestSbRing_PushNotFull(t *testing.T) {
	r := sbRing{maxLines: 4}
	r.push(makeGlyphRow('A'))
	r.push(makeGlyphRow('B'))
	if r.count != 2 {
		t.Errorf("count after 2 pushes = %d, want 2", r.count)
	}
	if r.head != 0 {
		t.Errorf("head after 2 pushes = %d, want 0", r.head)
	}
}

func TestSbRing_PushWhenFull(t *testing.T) {
	r := sbRing{maxLines: 3}
	r.push(makeGlyphRow('A'))
	r.push(makeGlyphRow('B'))
	r.push(makeGlyphRow('C'))
	// Ring is now full. Push a 4th element; head should advance.
	r.push(makeGlyphRow('D'))
	if r.count != 3 {
		t.Errorf("count after overflow = %d, want 3", r.count)
	}
	if r.head != 1 {
		t.Errorf("head after overflow = %d, want 1", r.head)
	}
	// Oldest surviving element should be 'B' (index 0), then 'C', then 'D'.
	if got := r.get(0); got == nil || got[0].Char != 'B' {
		t.Errorf("get(0) after wrap: got %v, want 'B'", got)
	}
	if got := r.get(2); got == nil || got[0].Char != 'D' {
		t.Errorf("get(2) after wrap: got %v, want 'D'", got)
	}
}

func TestSbRing_GetValid(t *testing.T) {
	r := sbRing{maxLines: 4}
	r.push(makeGlyphRow('X'))
	r.push(makeGlyphRow('Y'))
	r.push(makeGlyphRow('Z'))

	want := []rune{'X', 'Y', 'Z'}
	for i, ch := range want {
		got := r.get(i)
		if got == nil || got[0].Char != ch {
			t.Errorf("get(%d) = %v, want '%c'", i, got, ch)
		}
	}
}

func TestSbRing_GetOutOfRange(t *testing.T) {
	r := sbRing{maxLines: 4}
	r.push(makeGlyphRow('A'))
	if got := r.get(-1); got != nil {
		t.Errorf("get(-1) = %v, want nil", got)
	}
	if got := r.get(1); got != nil {
		t.Errorf("get(1) with count=1 = %v, want nil", got)
	}
	if got := r.get(100); got != nil {
		t.Errorf("get(100) = %v, want nil", got)
	}
}

func TestSbRing_GetWrapped(t *testing.T) {
	r := sbRing{maxLines: 3}
	// Push 5 items into a ring of capacity 3: A, B, C, D, E.
	// After wrapping, the ring should hold C, D, E.
	for _, ch := range []rune{'A', 'B', 'C', 'D', 'E'} {
		r.push(makeGlyphRow(ch))
	}
	if r.count != 3 {
		t.Fatalf("count = %d, want 3", r.count)
	}
	want := []rune{'C', 'D', 'E'}
	for i, ch := range want {
		got := r.get(i)
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
	r := sbRing{maxLines: 3}
	src := makeGlyphRowN(4, 'A')
	r.push(src)

	// Mutate the source after pushing.
	src[0].Char = 'Z'

	got := r.get(0)
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

	r := sbRing{maxLines: 3}
	r.push(row0)

	got := r.get(0)
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
	r := sbRing{maxLines: 2}
	r.push(makeGlyphRowN(width, 'A')) // slot 0
	r.push(makeGlyphRowN(width, 'B')) // slot 1 — ring now full

	// Record the backing-array pointer of physical slot 0.
	ptr0 := &r.lines[0][0]

	// Push again: ring is full, head=0, so slot 0 is overwritten.
	r.push(makeGlyphRowN(width, 'C'))

	// Slot 0 must have been reused (same backing array).
	if &r.lines[0][0] != ptr0 {
		t.Error("SlotReusedWhenFull: push allocated new backing array instead of reusing slot")
	}
	// Content must be correct.
	got := r.get(1) // newest is at logical index 1
	if got == nil || got[0].Char != 'C' {
		t.Errorf("SlotReusedWhenFull: newest row = %v, want 'C'", got)
	}
}

// TestSbRing_SlotReusedOnShrink: pushing a narrower row into a full ring
// must reuse the existing backing array (cap >= new len) rather than
// allocating a fresh slice.
func TestSbRing_SlotReusedOnShrink(t *testing.T) {
	const wideW, narrowW = 8, 3
	r := sbRing{maxLines: 2}
	r.push(makeGlyphRowN(wideW, 'W')) // slot 0, cap=8
	r.push(makeGlyphRowN(wideW, 'W')) // slot 1, cap=8 — ring full

	ptr0 := &r.lines[0][0]

	narrow := makeGlyphRowN(narrowW, 'N')
	r.push(narrow) // slot 0 overwritten, cap 8 >= len 3 → should reuse

	if &r.lines[0][0] != ptr0 {
		t.Error("SlotReusedOnShrink: push allocated new backing array despite sufficient capacity")
	}
	got := r.get(1) // newest
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
	r := sbRing{maxLines: 2}
	r.push(makeGlyphRowN(narrowW, 'N')) // slot 0, cap=3
	r.push(makeGlyphRowN(narrowW, 'N')) // slot 1, cap=3 — ring full

	wide := makeGlyphRowN(wideW, 'W')
	for i := range wide {
		wide[i].Char = rune('A' + i)
	}
	r.push(wide) // slot 0, cap 3 < len 8 → must reallocate

	got := r.get(1) // newest
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
// adjustAfterScrollbackPush — selection stability during scrollback
//
// Bug: selecting text while logs scroll causes selection to jump
//
// When new rows are pushed to the scrollback ring, selection virtual-row
// coordinates must be adjusted so they keep pointing at the same content.
// The tricky case is when the ring is FULL: sb.count stays constant but
// sbOff rises, changing the virtual-top (sbCount - sbOff).
// ---------------------------------------------------------------------------

func TestAdjustAfterScrollbackPush_RingNotFull(t *testing.T) {
	// Ring capacity 100, 5 rows pushed, ring not yet full.
	// sbOff=3 means user is reading scrollback.
	p := &Pane{
		sb:    sbRing{maxLines: 100},
		sbOff: 3,
	}
	// Pre-fill 10 rows.
	for i := 0; i < 10; i++ {
		p.sb.push(makeGlyphRow(rune('A' + i)))
	}
	p.selActive = true
	p.selAnchor = selPos{row: 7, col: 0}
	p.selCursor = selPos{row: 9, col: 5}

	oldCount := p.sb.count // 10
	oldSbOff := p.sbOff    // 3
	// Push 2 more rows (ring not full, count goes 10→12).
	p.sb.push(makeGlyphRow('K'))
	p.sb.push(makeGlyphRow('L'))
	p.adjustAfterScrollbackPush(2, oldCount, oldSbOff)

	// sbOff should advance: 3+2=5
	if p.sbOff != 5 {
		t.Errorf("sbOff = %d, want 5", p.sbOff)
	}
	// Virtual-top: old = 10-3=7, new = 12-5=7.  Delta = 0.
	// Selection should be unchanged.
	if p.selAnchor.row != 7 || p.selCursor.row != 9 {
		t.Errorf("selection shifted when it shouldn't: anchor.row=%d cursor.row=%d",
			p.selAnchor.row, p.selCursor.row)
	}
}

func TestAdjustAfterScrollbackPush_RingFull(t *testing.T) {
	// Ring capacity 5, already full.  Pushing more rows causes count to stay
	// constant while sbOff grows → virtual-top decreases.
	p := &Pane{
		sb:    sbRing{maxLines: 5},
		sbOff: 2,
	}
	for i := 0; i < 5; i++ {
		p.sb.push(makeGlyphRow(rune('A' + i)))
	}
	// count=5, sbOff=2 → virtual-top = 5-2 = 3
	p.selActive = true
	p.selAnchor = selPos{row: 4, col: 0}
	p.selCursor = selPos{row: 4, col: 10}

	oldCount := p.sb.count // 5
	oldSbOff := p.sbOff    // 2
	// Push 1 row.  Ring is full so count stays 5; sbOff → 3.
	p.sb.push(makeGlyphRow('F'))
	p.adjustAfterScrollbackPush(1, oldCount, oldSbOff)

	// sbOff: 2+1=3 (clamped to count=5 → 3)
	if p.sbOff != 3 {
		t.Errorf("sbOff = %d, want 3", p.sbOff)
	}
	// Virtual-top: old = 5-2=3, new = 5-3=2.  Delta = -1.
	// Selection rows should have decreased by 1.
	if p.selAnchor.row != 3 {
		t.Errorf("selAnchor.row = %d, want 3", p.selAnchor.row)
	}
	if p.selCursor.row != 3 {
		t.Errorf("selCursor.row = %d, want 3", p.selCursor.row)
	}
}

func TestAdjustAfterScrollbackPush_NoScrollback(t *testing.T) {
	// sbOff=0 (user looking at live view) → sbOff should not change.
	p := &Pane{
		sb:    sbRing{maxLines: 100},
		sbOff: 0,
	}
	for i := 0; i < 3; i++ {
		p.sb.push(makeGlyphRow(rune('A' + i)))
	}
	p.selActive = true
	p.selAnchor = selPos{row: 2, col: 0}
	p.selCursor = selPos{row: 2, col: 5}

	oldCount := p.sb.count
	oldSbOff := p.sbOff
	p.sb.push(makeGlyphRow('D'))
	p.adjustAfterScrollbackPush(1, oldCount, oldSbOff)

	if p.sbOff != 0 {
		t.Errorf("sbOff = %d, want 0", p.sbOff)
	}
	// count: 3→4, sbOff stays 0 → virtual-top: 3-0=3 → 4-0=4, delta=+1
	if p.selAnchor.row != 3 {
		t.Errorf("selAnchor.row = %d, want 3 (shifted by +1)", p.selAnchor.row)
	}
}

func TestAdjustAfterScrollbackPush_NoSelection(t *testing.T) {
	// selActive=false → should not panic or modify anything.
	p := &Pane{
		sb:    sbRing{maxLines: 100},
		sbOff: 2,
	}
	for i := 0; i < 5; i++ {
		p.sb.push(makeGlyphRow(rune('A' + i)))
	}
	p.selActive = false
	p.selAnchor = selPos{row: 3, col: 0}
	p.selCursor = selPos{row: 4, col: 0}

	oldCount := p.sb.count
	oldSbOff := p.sbOff
	p.sb.push(makeGlyphRow('F'))
	p.adjustAfterScrollbackPush(1, oldCount, oldSbOff)

	// Selection coordinates should be untouched.
	if p.selAnchor.row != 3 || p.selCursor.row != 4 {
		t.Errorf("selection modified when selActive=false: anchor=%d cursor=%d",
			p.selAnchor.row, p.selCursor.row)
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
		term:            term,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
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
	if p.sb.count != 0 {
		t.Fatalf("setup: expected sb.count=0, got %d", p.sb.count)
	}

	// In-place update: only row 0 changes.  Middle rows are unchanged.
	// Large-burst sentinel must NOT fire.
	chunk := []byte("\x1b[1;1HNEWPROGRESS_ROW0\x1b[K")
	p.mu.Lock()
	p.captureAndWrite(chunk)
	p.mu.Unlock()

	if p.sb.count != 0 {
		t.Errorf("in-place row-0 overwrite triggered scrollback push: sb.count = %d, want 0",
			p.sb.count)
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
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}
	term := vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollCallback(p.onScrollRow))
	p.term = term

	// Write 5 rows into a 3-row terminal → 2 rows scroll off the top.
	p.mu.Lock()
	p.captureAndWrite([]byte("AAAAAA\r\nBBBBBB\r\nCCCCCC\r\nDDDDDD\r\nEEEEEE"))
	p.mu.Unlock()

	if p.sb.count != 2 {
		t.Fatalf("want 2 rows in scrollback, got %d", p.sb.count)
	}
	// First captured row should be the original row 0: all 'A's.
	row := p.sb.get(0)
	if row == nil || row[0].Char != 'A' {
		t.Errorf("sb.get(0): got %v, want all 'A'", row)
	}
	// Second captured row should be row 1: all 'B's.
	row = p.sb.get(1)
	if row == nil || row[0].Char != 'B' {
		t.Errorf("sb.get(1): got %v, want all 'B'", row)
	}
}

// TestCaptureAndWrite_NativeCallback_InPlaceNoScrollback verifies that
// cursor-up + overwrite (progress bars, spinners) does not populate p.sb.
func TestCaptureAndWrite_NativeCallback_InPlaceNoScrollback(t *testing.T) {
	const cols, rows = 10, 4
	p := &Pane{
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}
	term := vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollCallback(p.onScrollRow))
	p.term = term

	// Fill the screen without scrolling.
	p.mu.Lock()
	p.captureAndWrite([]byte("AAAAAAAAAA\r\nBBBBBBBBBB\r\nCCCCCCCCCC\r\nDDDDDDDDDD"))
	p.mu.Unlock()

	if p.sb.count != 0 {
		t.Fatalf("after fill (no scroll): want 0, got %d", p.sb.count)
	}

	// In-place update: cursor to row 0, overwrite — no scroll should occur.
	p.mu.Lock()
	p.captureAndWrite([]byte("\x1b[1;1HNEWCONTENT\x1b[K"))
	p.mu.Unlock()

	if p.sb.count != 0 {
		t.Errorf("in-place overwrite via callback: want 0 in scrollback, got %d", p.sb.count)
	}
}

// TestCaptureAndWrite_NativeCallback_AltScreenNoScrollback verifies that
// content scrolling within alt-screen does not enter the primary scrollback.
func TestCaptureAndWrite_NativeCallback_AltScreenNoScrollback(t *testing.T) {
	const cols, rows = 8, 3
	p := &Pane{
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}
	term := vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollCallback(p.onScrollRow))
	p.term = term

	// Enter alt-screen, scroll heavily, exit.
	p.mu.Lock()
	p.captureAndWrite([]byte("\x1b[?1049h"))
	for i := 0; i < 10; i++ {
		p.captureAndWrite([]byte("XXXXXXXX\r\n"))
	}
	p.captureAndWrite([]byte("\x1b[?1049l"))
	p.mu.Unlock()

	if p.sb.count != 0 {
		t.Errorf("alt-screen scroll populated primary scrollback: sb.count = %d, want 0", p.sb.count)
	}
}

// ---------------------------------------------------------------------------
// resizeAndReflow — column-width change always resets the scrollback ring
//
// Any rows in the ring are at oldCols width.  Keeping them at newCols would
// produce garbled wrapping when the user scrolls (wide→narrow→wide shows
// narrow wrapped rows above wide live rows).
//
// When firstVisible==0 (rawBuf content fits in the new terminal), the ring
// is reset to empty — the live terminal holds everything rawBuf covers.
// Pre-rawBuf history that rawBuf's rolling window no longer covers is
// discarded; that is an accepted limitation of the rawBuf replay approach.
// ---------------------------------------------------------------------------

func TestResizeAndReflow_ResetsScrollbackOnExpand(t *testing.T) {
	const cols, rows = 40, 10
	term := vt10x.New(vt10x.WithSize(cols, rows))
	p := &Pane{
		term:            term,
		scrollbackLines: 200,
		sb:              sbRing{maxLines: 200},
	}

	// Pre-fill p.sb with 30 rows at cols width (simulating real output).
	for i := 0; i < 30; i++ {
		row := make([]vt10x.Glyph, cols)
		for c := range row {
			row[c] = vt10x.Glyph{Char: rune('A' + i%26)}
		}
		p.sb.push(row)
	}

	// rawBuf holds only 5 lines (rolling window, older content trimmed).
	p.rawBuf = []byte("line1\r\nline2\r\nline3\r\nline4\r\nline5\r\n")

	// Expand: double both dimensions.  replay will produce only 5 rows of
	// content that fit in the taller/wider terminal → firstVisible=0.
	// The ring must be reset: the old cols-wide rows would show at the wrong
	// column width (garbled wrapping) and the live terminal covers rawBuf.
	p.mu.Lock()
	p.resizeAndReflow(cols*2, rows*2)
	p.mu.Unlock()

	if p.sb.count != 0 {
		t.Errorf("resizeAndReflow kept stale-width rows in ring on expand: count = %d, want 0",
			p.sb.count)
	}
}

// ---------------------------------------------------------------------------
// resizeAndReflow must reflow scrollback when going from narrow → wide
//
// Bug: wide→narrow→wide leaves scrollback rows at the narrow width.
//
// After a narrow resize, the ring holds N rows at narrow width.  When
// resizing back to wide, the replay at wide width produces M < N rows
// (lines don't wrap).  The guard `firstVisible > p.sb.count` prevented
// the rebuild because M < N, leaving the ring full of narrow-width rows.
// Scrollback content then appeared garbled (wrong wrapping) after widening.
//
// Fix: rebuild whenever firstVisible > 0 (any scrollback exists at new
// width), regardless of whether it is more or less than the current ring.
// ---------------------------------------------------------------------------

func TestResizeAndReflow_ReflowsScrollbackOnWiden(t *testing.T) {
	const narrowCols, wideCols, rows = 6, 10, 3

	// rawBuf holds 5 lines of 10 chars each.
	// At wideCols=10: each fits in 1 row → 5 content rows.
	//   firstVisible = 5 - rows = 2 (2 rows go to scrollback).
	// At narrowCols=6: each wraps to 2 rows → 10 content rows.
	//   firstVisible = 10 - rows = 7.
	rawBuf := []byte("AAAAAAAAAA\r\nBBBBBBBBBB\r\nCCCCCCCCCC\r\nDDDDDDDDDD\r\nEEEEEEEEEE\r\n")

	term := vt10x.New(vt10x.WithSize(narrowCols, rows))
	p := &Pane{
		term:            term,
		scrollbackLines: 200,
		sb:              sbRing{maxLines: 200},
		rawBuf:          rawBuf,
	}

	// Simulate the ring state AFTER a narrow resize: 7 rows of width narrowCols.
	for i := 0; i < 7; i++ {
		row := make([]vt10x.Glyph, narrowCols)
		for c := range row {
			row[c] = vt10x.Glyph{Char: rune('a' + i%26)}
		}
		p.sb.push(row)
	}
	if p.sb.count != 7 {
		t.Fatalf("pre-condition: sb.count = %d, want 7", p.sb.count)
	}

	// Widen: resize from narrowCols to wideCols.
	p.mu.Lock()
	p.resizeAndReflow(wideCols, rows)
	p.mu.Unlock()

	// The replay at wideCols produces 3 scrollback rows (trailing \r\n leaves
	// cursor one row below content, so findContentRows returns 6;
	// firstVisible = 6 - rows = 3).  The ring must be rebuilt to 3 wide rows.
	if p.sb.count != 3 {
		t.Fatalf("after widen: sb.count = %d, want 3 (ring not reflowed at new width)", p.sb.count)
	}
	row0 := p.sb.get(0)
	if len(row0) != wideCols {
		t.Errorf("after widen: sb.get(0) width = %d, want %d (still narrow!)", len(row0), wideCols)
	}
	if row0[0].Char != 'A' {
		t.Errorf("after widen: sb.get(0)[0] = %q, want 'A'", row0[0].Char)
	}
}

// ---------------------------------------------------------------------------
// resizeAndReflow — wide → narrow → wide must clear stale narrow rows
//
// Bug: after going wide(W) → narrow(N) → wide(W), the ring retained N-width
// rows from the narrow phase.  At the final wide resize firstVisible==0 (the
// rawBuf content that was in the narrow ring now fits in the wider terminal),
// and the old `if firstVisible > 0` guard left those N-width rows in place.
// Scrolling then showed narrow-wrapped lines above wide-wrapped live rows.
// ---------------------------------------------------------------------------

func TestResizeAndReflow_ClearsStaleNarrowRowsOnWiden(t *testing.T) {
	// Use content that fits the wide terminal so firstVisible==0 on the final
	// wide resize, exercising the guard that used to skip the ring reset.
	const narrowCols, wideCols, rows = 6, 20, 10

	// rawBuf: 3 short lines that fit easily at wideCols.
	rawBuf := []byte("ABC\r\nDEF\r\nGHI\r\n")

	term := vt10x.New(vt10x.WithSize(narrowCols, rows))
	p := &Pane{
		term:            term,
		scrollbackLines: 200,
		sb:              sbRing{maxLines: 200},
		rawBuf:          rawBuf,
	}

	// Simulate the ring state AFTER a narrow resize: fill with narrow-width rows.
	for i := 0; i < 5; i++ {
		row := make([]vt10x.Glyph, narrowCols)
		for c := range row {
			row[c] = vt10x.Glyph{Char: rune('a' + i)}
		}
		p.sb.push(row)
	}
	if p.sb.count != 5 {
		t.Fatalf("pre-condition: sb.count = %d, want 5", p.sb.count)
	}

	// Widen: rawBuf content (3 short lines) fits in the new terminal (10 rows)
	// → firstVisible=0. The ring must be reset: no narrow rows may survive.
	p.mu.Lock()
	p.resizeAndReflow(wideCols, rows)
	p.mu.Unlock()

	if p.sb.count != 0 {
		t.Errorf("stale narrow-width rows persisted after widen: sb.count = %d, want 0", p.sb.count)
	}
}

// ---------------------------------------------------------------------------
// Scroll position is preserved across resize
// ---------------------------------------------------------------------------

// TestResizeAndReflow_PreservesScrollOffset verifies that sbOff is mapped
// proportionally (not simply reset) when a column-width change rebuilds
// the ring.  No wrap change here — content fits in one row at both widths.
// rawBuf holds 5 lines; at cols=6,rows=3 the replay produces contentRows=6
// (cursorY=5 after trailing \r\n), firstVisible=3, newSbCount=3.
// The pre-pushed 2-row ring has single-char anchor content ('A','B') which
// is below anchorMinLen=6, so proportional mapping kicks in.
// sbOff=2 → oldTopRow=0 → newTopRow=0 → sbOff=3 (top of new ring).
func TestResizeAndReflow_PreservesScrollOffset(t *testing.T) {
	// 5 lines of 6 chars each (no wrapping at either width).
	// At cols=6  : contentRows=6 (cursorY=5), firstVisible=3, ring=3.
	// At cols=10 : contentRows=6 (cursorY=5), firstVisible=3, ring=3.
	// oldSbCount=2 (pre-pushed), oldRows=3; sbOff=2 → oldTopRow=0.
	rawBuf := []byte("AAAAAA\r\nBBBBBB\r\nCCCCCC\r\nDDDDDD\r\nEEEEEE\r\n")
	const cols, rows = 6, 3

	term := vt10x.New(vt10x.WithSize(cols, rows))
	p := &Pane{
		term:            term,
		scrollbackLines: 200,
		sb:              sbRing{maxLines: 200},
		rawBuf:          rawBuf,
	}
	for i := 0; i < 2; i++ {
		row := make([]vt10x.Glyph, cols)
		row[0] = vt10x.Glyph{Char: rune('A' + i)}
		p.sb.push(row)
	}
	p.sbOff = 2 // user scrolled to the very top of the 2-row ring

	p.mu.Lock()
	p.resizeAndReflow(10, rows)
	p.mu.Unlock()

	// User was at the very top; must stay at the very top (sbOff==sb.count).
	if p.sbOff != p.sb.count {
		t.Errorf("resizeAndReflow changed sbOff: got %d, want %d (sb.count)", p.sbOff, p.sb.count)
	}
	if p.sbOff == 0 && p.sb.count > 0 {
		t.Errorf("resizeAndReflow dropped sbOff to 0 despite ring holding %d rows", p.sb.count)
	}
}

// TestResizeAndReflow_ProportionalScrollOnWrap verifies the proportional mapping
// when widening causes lines to unwrap (ring shrinks).
//
// 5 lines of 10 chars each.
// At cols=6  : each wraps to 2 rows → contentRows=11 (cursorY=10), firstVisible=8.
//
//	Test pre-fills ring with 7 rows; oldSbCount=7, oldRows=3, oldTotal=10.
//
// At cols=20 : each fits in 1 row  → contentRows=6  (cursorY=5),  firstVisible=3.
//
//	newSbCount=3, newRows=3, newTotal=6.
//
// Proportional mapping: newTopRow = oldTopRow * newTotal / oldTotal = oldTopRow*6/10
//
//	sbOff=7 (top): oldTopRow=0 → newTopRow=0 → sbOff=3 (top of new ring).
//	sbOff=4 (mid): oldTopRow=3 → newTopRow=1 → sbOff=2 (BBBBBBBBBB at top).
//	sbOff=1 (near bottom): oldTopRow=6 → newTopRow=3 → in live terminal → sbOff=0.
func TestResizeAndReflow_ProportionalScrollOnWrap(t *testing.T) {
	rawBuf := []byte("AAAAAAAAAA\r\nBBBBBBBBBB\r\nCCCCCCCCCC\r\nDDDDDDDDDD\r\nEEEEEEEEEE\r\n")
	const narrowCols, wideCols, rows = 6, 20, 3

	cases := []struct {
		oldSbOff  int
		wantSbOff int
	}{
		{7, 3}, // top of old ring → top of new ring
		{4, 2}, // mid → BBBBBBBBBB row at top of viewport
		{1, 0}, // near-bottom content now in live terminal
	}

	for _, tc := range cases {
		term := vt10x.New(vt10x.WithSize(narrowCols, rows))
		p := &Pane{
			term:            term,
			scrollbackLines: 200,
			sb:              sbRing{maxLines: 200},
			rawBuf:          rawBuf,
		}
		for i := 0; i < 7; i++ {
			p.sb.push(make([]vt10x.Glyph, narrowCols))
		}
		p.sbOff = tc.oldSbOff

		p.mu.Lock()
		p.resizeAndReflow(wideCols, rows)
		p.mu.Unlock()

		if p.sbOff != tc.wantSbOff {
			t.Errorf("sbOff=%d → after widen got %d, want %d (sb.count=%d)",
				tc.oldSbOff, p.sbOff, tc.wantSbOff, p.sb.count)
		}
		if p.sbOff > p.sb.count {
			t.Errorf("sbOff=%d exceeds sb.count=%d", p.sbOff, p.sb.count)
		}
	}
}

// TestResizeAndReflow_ContentAnchorWiden verifies that content matching
// correctly repositions the viewport when widening causes lines to unwrap.
//
// The pane has real scrollback content (captured via the scroll callback), so
// the ring rows have actual character content that reflowSbOff can match.
// The user is looking at "CCCCCCCCCC" in the centre of their viewport.  After
// widening, that line unwraps and should still appear at the centre.
func TestResizeAndReflow_ContentAnchorWiden(t *testing.T) {
	const narrowCols, wideCols, rows = 6, 20, 5
	rawBuf := []byte("AAAAAAAAAA\r\nBBBBBBBBBB\r\nCCCCCCCCCC\r\nDDDDDDDDDD\r\nEEEEEEEEEE\r\nFFFFFFFFFF\r\nGGGGGGGGGG\r\n")

	p := &Pane{scrollbackLines: 200, sb: sbRing{maxLines: 200}}
	p.term = vt10x.New(vt10x.WithSize(narrowCols, rows), vt10x.WithScrollCallback(p.onScrollRow))

	// Feed the content so the scroll callback populates the ring with real rows.
	p.mu.Lock()
	p.rawBuf = rawBuf
	p.captureAndWrite(rawBuf)
	// Scroll back so "CCCCCCCCCC" (split into "CCCCCC"+"CCCC") is near centre.
	// Ring holds the rows that scrolled off; live terminal holds the last 5.
	// Push sbOff high enough that the C-rows are visible in the centre.
	p.sbOff = p.sb.count // scroll to very top for simplicity
	preCount := p.sb.count
	p.mu.Unlock()

	if preCount == 0 {
		t.Skip("no scrollback captured; rawBuf may not have caused enough scrolling")
	}

	p.mu.Lock()
	p.resizeAndReflow(wideCols, rows)
	p.mu.Unlock()

	// After widening, the ring must not be empty (content exists).
	if p.sb.count == 0 && p.sbOff != 0 {
		t.Error("sbOff > 0 but ring is empty after widen")
	}
	// sbOff must be valid.
	if p.sbOff > p.sb.count {
		t.Errorf("sbOff=%d > sb.count=%d after widen", p.sbOff, p.sb.count)
	}
	// The viewport centre row must contain a known content character.
	// With content matching, the centre of the viewport should contain one of
	// our known lines (A–G repeated 10 times).
	centerRingIdx := p.sb.count - p.sbOff + rows/2
	if centerRingIdx >= 0 && centerRingIdx < p.sb.count {
		row := p.sb.get(centerRingIdx)
		if len(row) > 0 {
			ch := row[0].Char
			if ch == 0 || ch == ' ' {
				t.Errorf("centre row is blank after widen (sbOff=%d sb.count=%d)", p.sbOff, p.sb.count)
			}
		}
	}
}

// (rows added) keeps the user's scroll position, adjusted for the pulled rows.
func TestResizeHeightOnly_GrowPreservesScrollOffset(t *testing.T) {
	const cols, oldRows, newRows = 10, 4, 7
	// pull = extra = 3; scrollback needs ≥ 3 rows.
	p := &Pane{scrollbackLines: 100, sb: sbRing{maxLines: 100}}
	p.term = vt10x.New(vt10x.WithSize(cols, oldRows), vt10x.WithScrollCallback(p.onScrollRow))
	for i := 0; i < 5; i++ {
		row := make([]vt10x.Glyph, cols)
		row[0] = vt10x.Glyph{Char: rune('A' + i)}
		p.sb.push(row)
	}
	p.sbOff = 5 // scrolled to very top (sb.count = 5)

	p.mu.Lock()
	p.resizeHeightOnly(cols, oldRows, newRows) // extra=3, pull=3
	p.mu.Unlock()

	// Pulled 3 rows from scrollback into live terminal.
	// new sbOff = max(0, 5 - 3) = 2.
	if p.sbOff != 2 {
		t.Errorf("grow: sbOff = %d, want 2 (old 5 - pull 3)", p.sbOff)
	}
}

// TestResizeHeightOnly_GrowClampsScrollOffsetToZero verifies that when the
// user was looking at rows that are now in the live terminal, sbOff becomes 0.
func TestResizeHeightOnly_GrowClampsScrollOffsetToZero(t *testing.T) {
	const cols, oldRows, newRows = 10, 4, 10
	// extra = 6, but only 2 rows in scrollback → pull = 2.
	p := &Pane{scrollbackLines: 100, sb: sbRing{maxLines: 100}}
	p.term = vt10x.New(vt10x.WithSize(cols, oldRows), vt10x.WithScrollCallback(p.onScrollRow))
	for i := 0; i < 2; i++ {
		row := make([]vt10x.Glyph, cols)
		p.sb.push(row)
	}
	p.sbOff = 1 // 1 row above live view, but pull=2 absorbs it

	p.mu.Lock()
	p.resizeHeightOnly(cols, oldRows, newRows)
	p.mu.Unlock()

	if p.sbOff != 0 {
		t.Errorf("grow (pull >= sbOff): sbOff = %d, want 0", p.sbOff)
	}
}

// TestResizeHeightOnly_ShrinkPreservesScrollOffset verifies that shrinking a
// pane (rows removed → excess pushed into scrollback) keeps the scroll offset
// adjusted so the user is still viewing the same content.
func TestResizeHeightOnly_ShrinkPreservesScrollOffset(t *testing.T) {
	const cols, oldRows, newRows = 10, 8, 5
	// 6 content rows; excess = 6 - 5 = 1.
	p := &Pane{scrollbackLines: 100, sb: sbRing{maxLines: 100}}
	p.term = vt10x.New(vt10x.WithSize(cols, oldRows), vt10x.WithScrollCallback(p.onScrollRow))
	// Write 6 lines of content so findContentRows returns 6.
	p.mu.Lock()
	p.captureAndWrite([]byte("AAAAAAAAAA\r\nBBBBBBBBBB\r\nCCCCCCCCCC\r\nDDDDDDDDDD\r\nEEEEEEEEEE\r\nFFFFFFFFFF"))
	p.mu.Unlock()
	// Set sbOff to 2 (scrolled back 2 rows).
	p.mu.Lock()
	p.sbOff = 2
	oldSbCount := p.sb.count

	p.resizeHeightOnly(cols, oldRows, newRows)
	p.mu.Unlock()

	// excess rows were pushed into scrollback; sbOff must increase by excess.
	excess := p.sb.count - oldSbCount
	want := 2 + excess
	if want > p.sb.count {
		want = p.sb.count
	}
	if p.sbOff != want {
		t.Errorf("shrink: sbOff = %d, want %d (old 2 + excess %d)", p.sbOff, want, excess)
	}
}

// TestResizeHeightOnly_ShrinkLiveViewStaysLive verifies that a pane in live
// view (sbOff=0) stays at sbOff=0 after a height shrink.
func TestResizeHeightOnly_ShrinkLiveViewStaysLive(t *testing.T) {
	const cols, oldRows, newRows = 10, 8, 5
	p := &Pane{scrollbackLines: 100, sb: sbRing{maxLines: 100}}
	p.term = vt10x.New(vt10x.WithSize(cols, oldRows), vt10x.WithScrollCallback(p.onScrollRow))
	p.mu.Lock()
	p.captureAndWrite([]byte("AAAAAAAAAA\r\nBBBBBBBBBB\r\nCCCCCCCCCC\r\nDDDDDDDDDD\r\nEEEEEEEEEE\r\nFFFFFFFFFF"))
	p.sbOff = 0
	p.resizeHeightOnly(cols, oldRows, newRows)
	p.mu.Unlock()

	if p.sbOff != 0 {
		t.Errorf("shrink from live view: sbOff = %d, want 0", p.sbOff)
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
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}
	p.term = vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollCallback(p.onScrollRow))

	// Trigger one scroll so p.sb has at least 1 row.
	p.mu.Lock()
	p.captureAndWrite([]byte("AAAAAA\r\nBBBBBB\r\nCCCCCC\r\nDDDDDD"))
	p.mu.Unlock()
	if p.sb.count < 1 {
		t.Fatalf("pre-resize: want ≥1 row in scrollback, got %d", p.sb.count)
	}

	// Resize (new column width forces resizeAndReflow).
	p.rawBuf = []byte("AAAAAA\r\nBBBBBB\r\nCCCCCC\r\nDDDDDD")
	p.mu.Lock()
	p.resizeAndReflow(8, rows)
	p.mu.Unlock()

	// After resize, send more content that scrolls → callback must still fire.
	p.mu.Lock()
	p.captureAndWrite([]byte("EEEEEEEE\r\nFFFFFFFF\r\nGGGGGGGG\r\nHHHHHHHH"))
	p.mu.Unlock()

	if p.sb.count < 2 {
		t.Errorf("post-resize: scroll callback lost after resizeAndReflow; sb.count=%d want ≥2", p.sb.count)
	}
}

// TestScrollCallback_PreservedAfterHeightResize verifies that scroll-callback
// capture still works after a height-only resize (resizeHeightOnly).
func TestScrollCallback_PreservedAfterHeightResize(t *testing.T) {
	const cols, rows = 6, 4
	p := &Pane{
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}
	p.term = vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollCallback(p.onScrollRow))

	// rawBuf required by resizeHeightOnly's path detection.
	p.rawBuf = []byte("AAAAAA\r\nBBBBBB\r\n")

	// Shrink height → resizeHeightOnly (same cols, fewer rows).
	p.mu.Lock()
	p.resizeAndReflow(cols, 2)
	p.mu.Unlock()

	// Now send content that scrolls in the 2-row terminal.
	p.mu.Lock()
	p.captureAndWrite([]byte("CCCCCC\r\nDDDDDD\r\nEEEEEE"))
	p.mu.Unlock()

	if p.sb.count < 1 {
		t.Errorf("post-height-resize: scroll callback lost; sb.count=%d want ≥1", p.sb.count)
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
		term:            term,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	// Write a line then enter alt-screen.
	p.captureAndWrite([]byte("hello\r\n\x1b[?1049h"))
	if p.needsSync {
		t.Error("needsSync should be false after alt-screen entry")
	}

	// Exit alt-screen.
	p.captureAndWrite([]byte("\x1b[?1049l"))
	if !p.needsSync {
		t.Error("needsSync should be true after alt-screen exit")
	}

	// Clearing it manually (simulating render consuming it).
	p.needsSync = false
	if p.needsSync {
		t.Error("needsSync should be false after manual clear")
	}
}

// ---------------------------------------------------------------------------
// findContentRows
// ---------------------------------------------------------------------------

func TestFindContentRows_CursorBelowContent(t *testing.T) {
	// Simulate a terminal where content is on rows 0-2, cursor on row 3
	// (empty row after a trailing \r\n), and rows 3-9 are blank.
	term := vt10x.New(vt10x.WithSize(20, 10))
	term.Write([]byte("line1\r\nline2\r\nline3\r\n")) //nolint:errcheck

	got := findContentRows(term, 20, 10)
	// Cursor is on row 3 (the blank row after "line3\r\n").
	// findContentRows starts from max(cursor.Y+1, ...) and scans backward,
	// so it should return at least 4 (rows 0-3), but row 3 is blank so
	// content is rows 0-2 → the function returns cursor.Y+1 = 4 as minimum.
	if got < 3 {
		t.Errorf("findContentRows = %d, want >= 3", got)
	}
}

func TestFindContentRows_CursorOnContent(t *testing.T) {
	term := vt10x.New(vt10x.WithSize(20, 10))
	term.Write([]byte("prompt$ ")) //nolint:errcheck

	got := findContentRows(term, 20, 10)
	// Cursor is on row 0, which has content → should return 1.
	if got != 1 {
		t.Errorf("findContentRows = %d, want 1", got)
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
		got := xParseColor(tc.input)
		if got != tc.want {
			t.Errorf("xParseColor(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestXParseColor_InvalidLength(t *testing.T) {
	got := xParseColor("ff00")
	if got != "rgb:0000/0000/0000" {
		t.Errorf("xParseColor invalid length: got %q, want fallback", got)
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
	defer pr.Close()
	defer pw.Close()

	term := vt10x.New(vt10x.WithSize(40, 10))
	p := &Pane{term: term, ptmx: pw, scrollbackLines: 100, sb: sbRing{maxLines: 100}}

	p.captureAndWrite([]byte("\x1b[>0q"))
	pw.Close()

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
	defer pr.Close()
	defer pw.Close()

	term := vt10x.New(vt10x.WithSize(40, 10))
	p := &Pane{term: term, ptmx: pw, scrollbackLines: 100, sb: sbRing{maxLines: 100}}

	p.captureAndWrite([]byte("\x1b[>q"))
	pw.Close()

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
	defer pr.Close()
	defer pw.Close()

	term := vt10x.New(vt10x.WithSize(40, 10))
	p := &Pane{term: term, ptmx: pw, scrollbackLines: 100, sb: sbRing{maxLines: 100}}

	// "Smulx" hex-encoded = 536d756c78
	p.captureAndWrite([]byte("\x1bP+q536d756c78\x1b\\"))
	pw.Close()

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
	defer pr.Close()
	defer pw.Close()

	term := vt10x.New(vt10x.WithSize(40, 10))
	p := &Pane{term: term, ptmx: pw, scrollbackLines: 100, sb: sbRing{maxLines: 100}}

	// "colors" hex-encoded = 636f6c6f7273
	p.captureAndWrite([]byte("\x1bP+q636f6c6f7273\x1b\\"))
	pw.Close()

	buf, _ := io.ReadAll(pr)
	want := "\x1bP0+r636f6c6f7273\x1b\\"
	if string(buf) != want {
		t.Errorf("XTGETTCAP unknown cap = %q, want %q", string(buf), want)
	}
}

func TestXTGETTCAPResponse_Smulx(t *testing.T) {
	// Smulx hex = 536d756c78; value = "\x1b[4:%p1%dm"
	got := xtgettcapResponse("536d756c78")
	wantHexVal := "1b5b343a25703125646d"
	want := "\x1bP1+r536d756c78=" + wantHexVal + "\x1b\\"
	if got != want {
		t.Errorf("xtgettcapResponse(Smulx) = %q, want %q", got, want)
	}
}

func TestXTGETTCAPResponse_Setulc(t *testing.T) {
	// Setulc hex = 5365 74756c63
	got := xtgettcapResponse("5365 74756c63")
	// invalid hex (space) — should return empty string (no response)
	if got != "" {
		t.Errorf("xtgettcapResponse(invalid hex) = %q, want \"\"", got)
	}
}

func TestXTGETTCAPResponse_Setulc_Valid(t *testing.T) {
	// "Setulc" hex-encoded = 5365 74756c63 without space = 536574756c63
	got := xtgettcapResponse("536574756c63")
	if got == "" || got[2] != '1' {
		t.Errorf("xtgettcapResponse(Setulc) should be found, got %q", got)
	}
}

func TestSGR53_Overline(t *testing.T) {
	term := vt10x.New(vt10x.WithSize(10, 5))
	// SGR 53 = overline on, SGR 55 = overline off
	term.Write([]byte("\x1b[53mA\x1b[55mB"))
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
	term.Write([]byte("\x1b[53mA\x1b[0mB"))
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
	term.Write([]byte("A\x1b[?4mB"))
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
	term.Write([]byte("\x1b[4mU\x1b[0mN"))
	cellU := term.Cell(0, 0)
	cellN := term.Cell(1, 0)
	if cellU.Mode&vt10x.AttrUnderline == 0 {
		t.Errorf("cell U (after real \\x1b[4m): expected underline, Mode=0x%x", cellU.Mode)
	}
	if cellN.Mode&vt10x.AttrUnderline != 0 {
		t.Errorf("cell N (after \\x1b[0m): expected no underline, Mode=0x%x", cellN.Mode)
	}
}
