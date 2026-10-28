package session

import (
	"bunk/internal/input"
	"bunk/internal/vt10x"
	"io"
	"os"
	"strings"
	"testing"
	"time"
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
func TestParseDECRQM_OverflowRejected(t *testing.T) {
	// A mode number exceeding 65535 must be rejected to prevent int overflow.
	data := []byte("\x1b[?99999999999999999$p")
	_, _, ok := parseDECRQM(data, 0)
	if ok {
		t.Error("parseDECRQM accepted overflowing mode number, want rejection")
	}
}
func feedSplitPTYChunks(t *testing.T, p *Session, chunks ...[]byte) {
	t.Helper()
	var stream ptyStream
	p.Lock()
	defer p.Unlock()

	for _, chunk := range chunks {
		complete := stream.scan(chunk)
		if len(complete) > 0 {
			p.CaptureAndWrite(complete)
		}
	}

	if len(stream.pending)+len(stream.utf8) != 0 {
		t.Fatalf("PTY stream left buffered control or UTF-8 data")
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
			var stream ptyStream
			complete := stream.scan(tc.input)
			carry := append(stream.pending, stream.utf8...)
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
	defer pr.Close() //nolint:errcheck // test pipe cleanup
	defer pw.Close() //nolint:errcheck // test pipe cleanup

	p := &Session{
		Term:            vt10x.New(vt10x.WithSize(40, 10)),
		PTY:             pw,
		ScrollbackLines: 100,
		Scrollback:      Ring{MaxLines: 100},
	}

	feedSplitPTYChunks(t, p, []byte("\x1b[>"), []byte("0q"))
	closeWriter(t, p, pw)

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
	defer pr.Close() //nolint:errcheck // test pipe cleanup
	defer pw.Close() //nolint:errcheck // test pipe cleanup

	p := &Session{
		Term:            vt10x.New(vt10x.WithSize(40, 10)),
		PTY:             pw,
		ScrollbackLines: 100,
		Scrollback:      Ring{MaxLines: 100},
	}

	feedSplitPTYChunks(t, p, []byte("\x1bP+q536d"), []byte("756c78\x1b\\"))
	closeWriter(t, p, pw)

	buf, _ := io.ReadAll(pr)
	wantHexVal := "1b5b343a25703125646d"
	want := "\x1bP1+r536d756c78=" + wantHexVal + "\x1b\\"
	if string(buf) != want {
		t.Fatalf("split XTGETTCAP response = %q, want %q", string(buf), want)
	}
}

// TestCaptureAndWrite_SplitOSC10Response verifies that a split OSC 10 query
// (arriving in two chunks) receives a response in alt-screen mode.
// OSC 10/11 responses are only sent in alt-screen mode — full-screen TUI apps
// (neovim, helix) own their event loop and safely consume terminal responses.
func TestCaptureAndWrite_SplitOSC10Response(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close() //nolint:errcheck // test pipe cleanup
	defer pw.Close() //nolint:errcheck // test pipe cleanup

	p := &Session{
		Term:            vt10x.New(vt10x.WithSize(40, 10)),
		PTY:             pw,
		ScrollbackLines: 100,
		Scrollback:      Ring{MaxLines: 100},
		ThemeFG:         "rgb:d0d0/d0d0/d0d0",
	}

	// Enter alt-screen before the OSC 10 query; the DECSET 1049 and the start
	// of the OSC are in the first chunk so splitPTYChunk carries the partial
	// OSC into the second chunk — this exercises the carry/split path.
	feedSplitPTYChunks(t, p, []byte("\x1b[?1049h\x1b]10"), []byte(";?\x1b\\"))
	closeWriter(t, p, pw)

	buf, _ := io.ReadAll(pr)
	want := "\x1b]10;rgb:d0d0/d0d0/d0d0\x1b\\"
	if string(buf) != want {
		t.Fatalf("split OSC 10 response = %q, want %q", string(buf), want)
	}
}

// TestCaptureAndWrite_AltScreenRoundTrips: a program that switches to the
// alternate screen and back twice in quick succession, as the feature
// scripts' colour queries do, leaves its text on the primary screen where
// it wrote it, however the PTY reads split the output. A read that holds
// one switch's exit and the next one's entry used to keep the first entry's
// cursor, so the second exit moved the cursor back over the earlier rows.
func TestCaptureAndWrite_AltScreenRoundTrips(t *testing.T) {
	stream := []byte("Q10: \x1b[?1049hA\x1b[?1049lR10\r\nQ11: \x1b[?1049hB\x1b[?1049lR11\r\n")
	want := []string{"Q10: R10", "Q11: R11", ""}
	splits := [][][]byte{{stream}}
	for i := 1; i < len(stream); i++ {
		splits = append(splits, [][]byte{stream[:i], stream[i:]})
	}
	for _, chunks := range splits {
		s := newTestSession(20, 3)
		feedSplitPTYChunks(t, s, chunks...)
		for y, w := range want {
			var row []rune
			for x := range 20 {
				row = append(row, s.Term.Cell(x, y).Char)
			}
			if got := strings.TrimRight(string(row), " \x00"); got != w {
				t.Fatalf("chunks %q: row %d = %q, want %q", chunks, y, got, w)
			}
		}
	}
}

func TestSbRing_PushNotFull(t *testing.T) {
	r := Ring{MaxLines: 4}
	r.Push(makeGlyphRow('A'))
	r.Push(makeGlyphRow('B'))
	if r.Count != 2 {
		t.Errorf("count after 2 pushes = %d, want 2", r.Count)
	}
	if r.head != 0 {
		t.Errorf("head after 2 pushes = %d, want 0", r.head)
	}
}
func TestSbRing_PushWhenFull(t *testing.T) {
	r := Ring{MaxLines: 3}
	r.Push(makeGlyphRow('A'))
	r.Push(makeGlyphRow('B'))
	r.Push(makeGlyphRow('C'))
	// Ring is now full. Push a 4th element; head should advance.
	r.Push(makeGlyphRow('D'))
	if r.Count != 3 {
		t.Errorf("count after overflow = %d, want 3", r.Count)
	}
	if r.head != 1 {
		t.Errorf("head after overflow = %d, want 1", r.head)
	}
	// Oldest surviving element should be 'B' (index 0), then 'C', then 'D'.
	if got := r.Get(0); got == nil || got[0].Char != 'B' {
		t.Errorf("get(0) after wrap: got %v, want 'B'", got)
	}
	if got := r.Get(2); got == nil || got[0].Char != 'D' {
		t.Errorf("get(2) after wrap: got %v, want 'D'", got)
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
	p := &Session{
		Scrollback:   Ring{MaxLines: 100},
		ScrollOffset: 3,
	}
	// Pre-fill 10 rows.
	for i := 0; i < 10; i++ {
		p.Scrollback.Push(makeGlyphRow(rune('A' + i)))
	}
	p.SelActive = true
	p.SelAnchor = SelPos{Row: 7, Col: 0}
	p.SelCursor = SelPos{Row: 9, Col: 5}

	oldCount := p.Scrollback.Count // 10
	oldSbOff := p.ScrollOffset     // 3
	// Push 2 more rows (ring not full, count goes 10→12).
	p.Scrollback.Push(makeGlyphRow('K'))
	p.Scrollback.Push(makeGlyphRow('L'))
	p.adjustAfterScrollbackPush(2, oldCount, oldSbOff)

	// sbOff should advance: 3+2=5
	if p.ScrollOffset != 5 {
		t.Errorf("sbOff = %d, want 5", p.ScrollOffset)
	}
	// Virtual-top: old = 10-3=7, new = 12-5=7.  Delta = 0.
	// Selection should be unchanged.
	if p.SelAnchor.Row != 7 || p.SelCursor.Row != 9 {
		t.Errorf("selection shifted when it shouldn't: anchor.row=%d cursor.row=%d",
			p.SelAnchor.Row, p.SelCursor.Row)
	}
}
func TestAdjustAfterScrollbackPush_RingFull(t *testing.T) {
	// Ring capacity 5, already full.  Pushing more rows causes count to stay
	// constant while sbOff grows → virtual-top decreases.
	p := &Session{
		Scrollback:   Ring{MaxLines: 5},
		ScrollOffset: 2,
	}
	for i := 0; i < 5; i++ {
		p.Scrollback.Push(makeGlyphRow(rune('A' + i)))
	}
	// count=5, sbOff=2 → virtual-top = 5-2 = 3
	p.SelActive = true
	p.SelAnchor = SelPos{Row: 4, Col: 0}
	p.SelCursor = SelPos{Row: 4, Col: 10}

	oldCount := p.Scrollback.Count // 5
	oldSbOff := p.ScrollOffset     // 2
	// Push 1 row.  Ring is full so count stays 5; sbOff → 3.
	p.Scrollback.Push(makeGlyphRow('F'))
	p.adjustAfterScrollbackPush(1, oldCount, oldSbOff)

	// sbOff: 2+1=3 (clamped to count=5 → 3)
	if p.ScrollOffset != 3 {
		t.Errorf("sbOff = %d, want 3", p.ScrollOffset)
	}
	// Virtual-top: old = 5-2=3, new = 5-3=2.  Delta = -1.
	// Selection rows should have decreased by 1.
	if p.SelAnchor.Row != 3 {
		t.Errorf("selAnchor.row = %d, want 3", p.SelAnchor.Row)
	}
	if p.SelCursor.Row != 3 {
		t.Errorf("selCursor.row = %d, want 3", p.SelCursor.Row)
	}
}
func TestAdjustAfterScrollbackPush_NoScrollback(t *testing.T) {
	// sbOff=0 (user looking at live view) → sbOff should not change.
	p := &Session{
		Scrollback:   Ring{MaxLines: 100},
		ScrollOffset: 0,
	}
	for i := 0; i < 3; i++ {
		p.Scrollback.Push(makeGlyphRow(rune('A' + i)))
	}
	p.SelActive = true
	p.SelAnchor = SelPos{Row: 2, Col: 0}
	p.SelCursor = SelPos{Row: 2, Col: 5}

	oldCount := p.Scrollback.Count
	oldSbOff := p.ScrollOffset
	p.Scrollback.Push(makeGlyphRow('D'))
	p.adjustAfterScrollbackPush(1, oldCount, oldSbOff)

	if p.ScrollOffset != 0 {
		t.Errorf("sbOff = %d, want 0", p.ScrollOffset)
	}
	// count: 3→4, sbOff stays 0 → virtual-top: 3-0=3 → 4-0=4, delta=+1
	if p.SelAnchor.Row != 3 {
		t.Errorf("selAnchor.row = %d, want 3 (shifted by +1)", p.SelAnchor.Row)
	}
}
func TestAdjustAfterScrollbackPush_NoSelection(t *testing.T) {
	// selActive=false → should not panic or modify anything.
	p := &Session{
		Scrollback:   Ring{MaxLines: 100},
		ScrollOffset: 2,
	}
	for i := 0; i < 5; i++ {
		p.Scrollback.Push(makeGlyphRow(rune('A' + i)))
	}
	p.SelActive = false
	p.SelAnchor = SelPos{Row: 3, Col: 0}
	p.SelCursor = SelPos{Row: 4, Col: 0}

	oldCount := p.Scrollback.Count
	oldSbOff := p.ScrollOffset
	p.Scrollback.Push(makeGlyphRow('F'))
	p.adjustAfterScrollbackPush(1, oldCount, oldSbOff)

	// Selection coordinates should be untouched.
	if p.SelAnchor.Row != 3 || p.SelCursor.Row != 4 {
		t.Errorf("selection modified when selActive=false: anchor=%d cursor=%d",
			p.SelAnchor.Row, p.SelCursor.Row)
	}
}

// (rows added) keeps the user's scroll position, adjusted for the pulled rows.
func TestResizeHeightOnly_GrowPreservesScrollOffset(t *testing.T) {
	const cols, oldRows, newRows = 10, 4, 7
	// pull = extra = 3; scrollback needs ≥ 3 rows.
	p := &Session{ScrollbackLines: 100, Scrollback: Ring{MaxLines: 100}}
	p.Term = vt10x.New(vt10x.WithSize(cols, oldRows), vt10x.WithScrollCallback(p.OnScrollRow))
	for i := 0; i < 5; i++ {
		row := make([]vt10x.Glyph, cols)
		row[0] = vt10x.Glyph{Char: rune('A' + i)}
		p.Scrollback.Push(row)
	}
	p.ScrollOffset = 5 // scrolled to very top (sb.count = 5)

	p.Lock()
	p.resizeHeightOnly(cols, oldRows, newRows) // extra=3, pull=3
	p.Unlock()

	// Pulled 3 rows from scrollback into live terminal.
	// new sbOff = max(0, 5 - 3) = 2.
	if p.ScrollOffset != 2 {
		t.Errorf("grow: sbOff = %d, want 2 (old 5 - pull 3)", p.ScrollOffset)
	}
}

// TestResizeHeightOnly_GrowClampsScrollOffsetToZero verifies that when the
// user was looking at rows that are now in the live terminal, sbOff becomes 0.
func TestResizeHeightOnly_GrowClampsScrollOffsetToZero(t *testing.T) {
	const cols, oldRows, newRows = 10, 4, 10
	// extra = 6, but only 2 rows in scrollback → pull = 2.
	p := &Session{ScrollbackLines: 100, Scrollback: Ring{MaxLines: 100}}
	p.Term = vt10x.New(vt10x.WithSize(cols, oldRows), vt10x.WithScrollCallback(p.OnScrollRow))
	for i := 0; i < 2; i++ {
		row := make([]vt10x.Glyph, cols)
		p.Scrollback.Push(row)
	}
	p.ScrollOffset = 1 // 1 row above live view, but pull=2 absorbs it

	p.Lock()
	p.resizeHeightOnly(cols, oldRows, newRows)
	p.Unlock()

	if p.ScrollOffset != 0 {
		t.Errorf("grow (pull >= sbOff): sbOff = %d, want 0", p.ScrollOffset)
	}
}

// TestResizeHeightOnly_ShrinkPreservesScrollOffset verifies that shrinking a
// pane (rows removed → excess pushed into scrollback) keeps the scroll offset
// adjusted so the user is still viewing the same content.
func TestResizeHeightOnly_ShrinkPreservesScrollOffset(t *testing.T) {
	const cols, oldRows, newRows = 10, 8, 5
	// 6 content rows; excess = 6 - 5 = 1.
	p := &Session{ScrollbackLines: 100, Scrollback: Ring{MaxLines: 100}}
	p.Term = vt10x.New(vt10x.WithSize(cols, oldRows), vt10x.WithScrollCallback(p.OnScrollRow))
	// Write 6 lines of content so findContentRows returns 6.
	p.Lock()
	p.CaptureAndWrite([]byte("AAAAAAAAAA\r\nBBBBBBBBBB\r\nCCCCCCCCCC\r\nDDDDDDDDDD\r\nEEEEEEEEEE\r\nFFFFFFFFFF"))
	p.Unlock()
	// Set sbOff to 2 (scrolled back 2 rows).
	p.Lock()
	p.ScrollOffset = 2
	oldSbCount := p.Scrollback.Count

	p.resizeHeightOnly(cols, oldRows, newRows)
	p.Unlock()

	// excess rows were pushed into scrollback; sbOff must increase by excess.
	excess := p.Scrollback.Count - oldSbCount
	want := 2 + excess
	if want > p.Scrollback.Count {
		want = p.Scrollback.Count
	}
	if p.ScrollOffset != want {
		t.Errorf("shrink: sbOff = %d, want %d (old 2 + excess %d)", p.ScrollOffset, want, excess)
	}
}

// TestResizeHeightOnly_ShrinkLiveViewStaysLive verifies that a pane in live
// view (sbOff=0) stays at sbOff=0 after a height shrink.
func TestResizeHeightOnly_ShrinkLiveViewStaysLive(t *testing.T) {
	const cols, oldRows, newRows = 10, 8, 5
	p := &Session{ScrollbackLines: 100, Scrollback: Ring{MaxLines: 100}}
	p.Term = vt10x.New(vt10x.WithSize(cols, oldRows), vt10x.WithScrollCallback(p.OnScrollRow))
	p.Lock()
	p.CaptureAndWrite([]byte("AAAAAAAAAA\r\nBBBBBBBBBB\r\nCCCCCCCCCC\r\nDDDDDDDDDD\r\nEEEEEEEEEE\r\nFFFFFFFFFF"))
	p.ScrollOffset = 0
	p.resizeHeightOnly(cols, oldRows, newRows)
	p.Unlock()

	if p.ScrollOffset != 0 {
		t.Errorf("shrink from live view: sbOff = %d, want 0", p.ScrollOffset)
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
func TestXTGETTCAPResponse_Smulx(t *testing.T) {
	// Smulx hex = 536d756c78; value = "\x1b[4:%p1%dm"
	got := xtgettcapResponse("536d756c78", 0, 0)
	wantHexVal := "1b5b343a25703125646d"
	want := "\x1bP1+r536d756c78=" + wantHexVal + "\x1b\\"
	if got != want {
		t.Errorf("xtgettcapResponse(Smulx) = %q, want %q", got, want)
	}
}
func TestXTGETTCAPResponse_Setulc(t *testing.T) {
	// Setulc hex = 5365 74756c63
	got := xtgettcapResponse("5365 74756c63", 0, 0)
	// invalid hex (space) — should return empty string (no response)
	if got != "" {
		t.Errorf("xtgettcapResponse(invalid hex) = %q, want \"\"", got)
	}
}
func TestXTGETTCAPResponse_Setulc_Valid(t *testing.T) {
	// "Setulc" hex-encoded = 5365 74756c63 without space = 536574756c63
	got := xtgettcapResponse("536574756c63", 0, 0)
	if got == "" || got[2] != '1' {
		t.Errorf("xtgettcapResponse(Setulc) should be found, got %q", got)
	}
}

// TestKittyStack_StaleAfterExit is the regression test for the bug where a
// non-alt-screen app (e.g. Claude Code) enables KKP but exits without sending
// \x1b[<u.  The stale kittyStack causes bunk to encode all subsequent
// keystrokes as CSI-u sequences; the shell echoes them as garbage text.
//
// trackFgProcess clears kittyStack on foreground PGID change.  This test
// verifies that a cleared stack restores legacy key encoding.
func TestKittyStack_StaleAfterExit(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close() //nolint:errcheck // test pipe cleanup
	pw.Close()       //nolint:errcheck // no responses expected

	term := vt10x.New(vt10x.WithSize(40, 10))
	p := &Session{Term: term, PTY: pw, ScrollbackLines: 100, Scrollback: Ring{MaxLines: 100}}

	// Simulate app enabling KKP but exiting without cleanup.
	p.CaptureAndWrite([]byte("\x1b[>1u"))

	p.Lock()
	staleFlags := 0
	if len(p.KittyStack) > 0 {
		staleFlags = p.KittyStack[len(p.KittyStack)-1]
	}
	p.Unlock()

	if staleFlags == 0 {
		t.Fatal("precondition: expected non-zero kittyFlags after push")
	}

	// With stale stack, Enter encodes as CSI-u — the broken state.
	gotStale := input.EncodeKey(input.Event{Key: input.KeyEnter}, input.Modes{KittyFlags: staleFlags})
	if string(gotStale) != "\x1b[13u" {
		t.Errorf("stale KKP: Enter = %q, want \\x1b[13u", gotStale)
	}

	// The negotiation belongs to the exited foreground process.
	p.Lock()
	p.KittyOwnerPGID = 11
	p.clearStaleKittyMode(12)
	p.Unlock()

	p.Lock()
	clearedFlags := 0
	if len(p.KittyStack) > 0 {
		clearedFlags = p.KittyStack[len(p.KittyStack)-1]
	}
	p.Unlock()

	// After clear, Enter must encode as a plain carriage return.
	gotCleared := input.EncodeKey(input.Event{Key: input.KeyEnter}, input.Modes{KittyFlags: clearedFlags})
	if string(gotCleared) != "\r" {
		t.Errorf("after clear: Enter = %q, want \\r", gotCleared)
	}
}

// TestRegressionSyncTimeout: a synchronized update the application never
// ends is released after Timing.SyncRelease, with a redraw.
func TestRegressionSyncTimeout(t *testing.T) {
	t.Parallel()
	for _, tc := range timings(fastTiming) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			testSyncTimeout(t, tc.timing)
		})
	}
}

func testSyncTimeout(t *testing.T, tm Timing) {
	p := newTestSession(10, 3)
	p.timing = tm
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p.PTY = r
	redraw := make(chan struct{}, 1)
	readDone := make(chan struct{})
	go func() {
		p.ReadLoop(Hooks{Redraw: func() {
			select {
			case redraw <- struct{}{}:
			default:
			}
		}})
		close(readDone)
	}()
	defer func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
		<-readDone
	}()
	written := time.Now()
	if _, err := w.Write([]byte("\x1b[?2026hheld")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-redraw:
		took := time.Since(written)
		if took < tm.syncRelease() {
			t.Errorf("released after %v, before Timing.SyncRelease (%v)", took, tm.syncRelease())
		}
		if tm.SyncRelease > 0 && took >= SyncUpdateTimeout {
			t.Errorf("released after %v: Timing.SyncRelease (%v) was not used", took, tm.SyncRelease)
		}
		p.Lock()
		defer p.Unlock()
		if p.Term.Mode()&vt10x.ModeSync != 0 || !p.syncDeadline.IsZero() {
			t.Fatal("sync timeout did not release rendering")
		}
	case <-time.After(max(3*tm.syncRelease(), 3*time.Second)):
		t.Fatal("missing redraw after abandoned synchronized update")
	}
}
