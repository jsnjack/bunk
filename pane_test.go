package main

import (
	"testing"

	"github.com/hinshun/vt10x"
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
