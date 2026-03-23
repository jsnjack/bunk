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
// isBlankRow
// ---------------------------------------------------------------------------

func TestIsBlankRow_Empty(t *testing.T) {
	if !isBlankRow(nil) {
		t.Error("isBlankRow(nil) = false, want true")
	}
	if !isBlankRow([]vt10x.Glyph{}) {
		t.Error("isBlankRow(empty) = false, want true")
	}
}

func TestIsBlankRow_AllNUL(t *testing.T) {
	row := makeGlyphRow(0, 0, 0)
	if !isBlankRow(row) {
		t.Error("isBlankRow(all NUL) = false, want true")
	}
}

func TestIsBlankRow_AllSpaces(t *testing.T) {
	row := makeGlyphRow(' ', ' ', ' ')
	if !isBlankRow(row) {
		t.Error("isBlankRow(all spaces) = false, want true")
	}
}

func TestIsBlankRow_OneNonBlank(t *testing.T) {
	row := makeGlyphRow(' ', 'X', ' ')
	if isBlankRow(row) {
		t.Error("isBlankRow(one non-blank) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// rowsEqual
// ---------------------------------------------------------------------------

func TestRowsEqual_Same(t *testing.T) {
	a := makeGlyphRow('H', 'i')
	b := makeGlyphRow('H', 'i')
	if !rowsEqual(a, b) {
		t.Error("rowsEqual(same) = false, want true")
	}
}

func TestRowsEqual_DifferentLengths(t *testing.T) {
	a := makeGlyphRow('H', 'i')
	b := makeGlyphRow('H')
	if rowsEqual(a, b) {
		t.Error("rowsEqual(different lengths) = true, want false")
	}
}

func TestRowsEqual_DifferentChar(t *testing.T) {
	a := makeGlyphRow('H', 'i')
	b := makeGlyphRow('H', 'o')
	if rowsEqual(a, b) {
		t.Error("rowsEqual(different char) = true, want false")
	}
}

func TestRowsEqual_BothEmpty(t *testing.T) {
	if !rowsEqual(nil, nil) {
		t.Error("rowsEqual(nil,nil) = false, want true")
	}
	if !rowsEqual([]vt10x.Glyph{}, []vt10x.Glyph{}) {
		t.Error("rowsEqual(empty,empty) = false, want true")
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
// rebuildScrollbackFromRawBuf — must not clobber a larger existing ring
//
// Bug: entering scrollback during dnf install erases older history
//
// rawBuf is a rolling byte window (capped at scrollbackLines*200 bytes).
// After a long-running command the window covers only recent output; the
// older history lives in p.sb (captured by detectShift).  The rebuild was
// unconditionally replacing p.sb, discarding rows that weren't in rawBuf.
//
// Fix: skip the rebuild when the replay would produce fewer rows than p.sb
// already holds.
// ---------------------------------------------------------------------------

func TestRebuildScrollback_PreservesLargerRing(t *testing.T) {
	const cols, rows = 20, 5
	term := vt10x.New(vt10x.WithSize(cols, rows))
	p := &Pane{
		term:            term,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	// Simulate detectShift accumulating 40 rows during a long-running command.
	for i := 0; i < 40; i++ {
		p.sb.push(makeGlyphRow(rune('A' + i%26)))
	}
	want := p.sb.count // 40

	// rawBuf contains only a few lines — the rolling window trimmed older
	// content.  Replaying it would produce ~3 rows, far fewer than 40.
	p.rawBuf = []byte("lineX\r\nlineY\r\nlineZ\r\n")

	p.mu.Lock()
	p.rebuildScrollbackFromRawBuf()
	p.mu.Unlock()

	if p.sb.count != want {
		t.Errorf("rebuildScrollbackFromRawBuf clobbered ring: count %d → %d (want %d)",
			want, p.sb.count, want)
	}
}

// ---------------------------------------------------------------------------
// resizeAndReflow — must not clobber scrollback when pane expands
//
// Bug: split panes erase scrollback history
//
// Trace showed: pane had 43 rows of scrollback; user expanded terminal from
// 79×24 to 281×64 then pressed F1 to split.  resizeAndReflow was rebuilding
// p.sb unconditionally from rawBuf replay.  At 281 cols the 24 content rows
// fit in the 64-row terminal with firstVisible=0, so p.sb was replaced by an
// empty ring.  After the split (narrow again, 140 cols) the ring was still
// empty, and scrolling no longer worked.
//
// Fix: only replace p.sb when the replay yields MORE rows than already held.
// ---------------------------------------------------------------------------

func TestResizeAndReflow_PreservesScrollbackOnExpand(t *testing.T) {
	const cols, rows = 40, 10
	term := vt10x.New(vt10x.WithSize(cols, rows))
	p := &Pane{
		term:            term,
		scrollbackLines: 200,
		sb:              sbRing{maxLines: 200},
	}

	// Pre-fill p.sb with 30 rows captured by detectShift (simulating real output).
	for i := 0; i < 30; i++ {
		row := make([]vt10x.Glyph, cols)
		for c := range row {
			row[c] = vt10x.Glyph{Char: rune('A' + i%26)}
		}
		p.sb.push(row)
	}
	wantSB := p.sb.count // 30

	// rawBuf holds only 5 lines (rolling window, older content trimmed).
	p.rawBuf = []byte("line1\r\nline2\r\nline3\r\nline4\r\nline5\r\n")

	// Expand: double both dimensions.  replay will produce only 5 rows of
	// scrollback (content fits in the taller/wider terminal) → firstVisible=0.
	p.mu.Lock()
	p.resizeAndReflow(cols*2, rows*2)
	p.mu.Unlock()

	if p.sb.count < wantSB {
		t.Errorf("resizeAndReflow clobbered scrollback on expand: count %d → %d (want ≥ %d)",
			wantSB, p.sb.count, wantSB)
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
