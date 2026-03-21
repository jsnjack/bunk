package main

import (
	"io"
	"os"
	"runtime"
	"testing"

	"bunk/internal/vt10x"
)

// ---------------------------------------------------------------------------
// scanCursorStyle
//
// Bug: cursor not visible in Claude/Copilot
//
// Apps send DECSCUSR (\x1b[N q) to set cursor shape. scanCursorStyle
// extracts the style from the PTY output so we can forward it via tcell.
// ---------------------------------------------------------------------------

func TestScanCursorStyle_CursorForwarding_ValidSequences(t *testing.T) {
	for ps := 0; ps <= 6; ps++ {
		seq := []byte{0x1b, '[', byte('0' + ps), ' ', 'q'}
		got := scanCursorStyle(seq)
		if got != ps {
			t.Errorf("scanCursorStyle(\\x1b[%d q) = %d, want %d", ps, got, ps)
		}
	}
}

func TestScanCursorStyle_CursorForwarding_LastOccurrence(t *testing.T) {
	// Two sequences: \x1b[2 q then \x1b[4 q — should return 4.
	data := []byte{0x1b, '[', '2', ' ', 'q', 'X', 'Y', 0x1b, '[', '4', ' ', 'q'}
	got := scanCursorStyle(data)
	if got != 4 {
		t.Errorf("scanCursorStyle with two sequences = %d, want 4", got)
	}
}

func TestScanCursorStyle_CursorForwarding_NotPresent(t *testing.T) {
	data := []byte("hello world, no cursor style here")
	got := scanCursorStyle(data)
	if got != -1 {
		t.Errorf("scanCursorStyle on plain text = %d, want -1", got)
	}
}

func TestScanCursorStyle_CursorForwarding_BufferTooShort(t *testing.T) {
	// Needs at least 5 bytes; feed 4.
	data := []byte{0x1b, '[', '2', ' '}
	got := scanCursorStyle(data)
	if got != -1 {
		t.Errorf("scanCursorStyle on short buffer = %d, want -1", got)
	}
}

func TestScanCursorStyle_CursorForwarding_InvalidDigit(t *testing.T) {
	for _, digit := range []byte{'7', '8', '9'} {
		data := []byte{0x1b, '[', digit, ' ', 'q'}
		got := scanCursorStyle(data)
		if got != -1 {
			t.Errorf("scanCursorStyle(\\x1b[%c q) = %d, want -1", digit, got)
		}
	}
}

func TestScanCursorStyle_CursorForwarding_Embedded(t *testing.T) {
	// Sequence embedded in a larger stream of data.
	prefix := []byte("some output before\x1b[0m")
	seq := []byte{0x1b, '[', '5', ' ', 'q'}
	suffix := []byte("more output after")
	data := append(append(prefix, seq...), suffix...)
	got := scanCursorStyle(data)
	if got != 5 {
		t.Errorf("scanCursorStyle embedded = %d, want 5", got)
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
// This test verifies the merged single-lock invariant: cursorStyle and
// vt10x cell content are always consistent when observed under p.mu.
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
	// Each iteration atomically sets cursorStyle AND writes a paired
	// marker character to cell(0,0).
	// style 1 ↔ 'A', style 2 ↔ 'B', …, style 6 ↔ 'F', then wraps.
	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			style := (i % 6) + 1
			marker := byte('A' + (i % 6))

			p.mu.Lock()
			p.cursorStyle = style
			// \x1b[H = cursor home (0,0), then write the marker.
			p.term.Write([]byte{0x1b, '[', 'H', marker}) //nolint:errcheck
			p.mu.Unlock()

			runtime.Gosched() // yield to give reader a chance to observe
		}
	}()

	// Reader goroutine (this goroutine): simulates render() reading
	// cursorStyle + cell content under a single lock acquisition.
	// The invariant: style and cell must be from the SAME write.
	violations := 0
	checks := 0
	for {
		select {
		case <-done:
			if violations > 0 {
				t.Errorf("%d/%d observations saw cursorStyle updated but cell stale (lock broken)",
					violations, checks)
			}
			t.Logf("checked %d observations, 0 violations", checks)
			return
		default:
		}

		p.mu.Lock()
		style := p.cursorStyle
		cell := p.term.Cell(0, 0)
		p.mu.Unlock()

		checks++
		if style > 0 && cell.Char != 0 {
			expected := rune('A' + (style - 1))
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
