package vt10x

import (
	"bytes"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Regressions found by FuzzParse and FuzzResize. Each input is also kept in
// testdata/fuzz.

// TestWriteKeepsSplitRune: a chunk that ends inside a multi-byte character
// must leave the whole prefix unconsumed. Write used to drop its lead byte
// and return the rest, so the character was lost.
func TestWriteKeepsSplitRune(t *testing.T) {
	for _, tc := range []struct{ name, first, second string }{
		{"two of three bytes", "a\xe6\x97", "\xa5b"},
		{"one of three bytes", "a\xe6", "\x97\xa5b"},
		{"three of four bytes", "a\xf0\x9f\x91", "\x8db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := New(WithSize(10, 1))
			n, err := term.Write([]byte(tc.first))
			if err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Fatalf("Write consumed %d bytes of %q, want 1", n, tc.first)
			}
			if _, err := term.Write([]byte(tc.first[n:] + tc.second)); err != nil {
				t.Fatal(err)
			}
			want := []rune(tc.first + tc.second)[1]
			if got := term.Cell(1, 0).Char; got != want {
				t.Fatalf("cell 1 = %q, want %q", got, want)
			}
		})
	}
}

// TestTabCountBounded: CHT and CBT with a huge count used to step through
// every tab stop once per count, holding the terminal lock for seconds (or
// forever for counts near 2^63).
func TestTabCountBounded(t *testing.T) {
	for _, tc := range []struct {
		name, seq string
		x         int
	}{
		{"forward", "\x1b[9223372036854775807I", 79},
		{"backward", "\x1b[40G\x1b[9223372036854775807Z", 0},
		{"forward two", "\x1b[2I", 16},
		{"backward two", "\x1b[40G\x1b[2Z", 24},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := New(WithSize(80, 2))
			if _, err := term.Write([]byte(tc.seq)); err != nil {
				t.Fatal(err)
			}
			if x := term.Cursor().X; x != tc.x {
				t.Fatalf("cursor x = %d, want %d", x, tc.x)
			}
		})
	}
}

// TestWrapPaddingOnlyBeforeWrap: a wide character that does not fit leaves
// a width -2 padding cell at the end of the row and wraps. Copy, search and
// reflow skip padding, so it must stay the last cell of a wrapped row. DCH
// and widening the grid used to move it in front of ordinary cells.
func TestWrapPaddingOnlyBeforeWrap(t *testing.T) {
	// Column 7 of an 8-column row becomes padding.
	const padded = "abcdefg界"
	for _, tc := range []struct {
		name, seq    string
		cols, resize int
	}{
		{"fuzz input", "000000ｗｉｄａ\x1b80\x1b[P", 13, 0},
		{"delete character", padded + "\x1b[1;2H\x1b[P", 8, 0},
		{"delete characters", padded + "\x1b[1;2H\x1b[3P", 8, 0},
		{"insert character", padded + "\x1b[1;2H\x1b[@", 8, 0},
		{"widen", padded, 8, 12},
		{"narrow", padded, 8, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := newFuzzTerm(tc.cols, 3)
			term.write([]byte(tc.seq), false)
			if tc.resize > 0 {
				term.Resize(tc.resize, 3)
			}
			cols, _ := term.Size()
			for y, row := range append(term.lines, term.altLines...) {
				if err := checkRow(row, cols, len(term.links), sideTableBound(cols)); err != nil {
					t.Fatalf("row %d: %v", y, err)
				}
			}
		})
	}
}

// TestOSCColorReplyUsesIndex: the OSC 4 reply printed the colour value
// where the palette index belongs ("4;16711680;..."), and colour queries
// without an override answered with bits of the index or of DefaultFG.
// A reply now names the requested index and an override's low 24 bits; an
// unknown colour gets no reply.
func TestOSCColorReplyUsesIndex(t *testing.T) {
	for _, tc := range []struct{ name, seq, want string }{
		{"palette override", "\x1b]4;1;#ff8000\x07\x1b]4;1;?\x07", "\x1b]4;1;rgb:ffff/8080/0000\x07"},
		{"palette unknown", "\x1b]4;7;?\x07", ""},
		{"palette reset", "\x1b]4;1;#ff8000\x07\x1b]104;1\x07\x1b]4;1;?\x07", ""},
		{"foreground override", "\x1b]10;rgb:12/34/56\x07\x1b]10;?\x07", "\x1b]10;rgb:1212/3434/5656\x07"},
		{"foreground unknown", "\x1b]10;?\x07\x1b]11;?\x07\x1b]12;?\x07", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var replies bytes.Buffer
			term := New(WithSize(10, 2), WithWriter(&replies))
			if _, err := term.Write([]byte(tc.seq)); err != nil {
				t.Fatal(err)
			}
			if replies.String() != tc.want {
				t.Fatalf("reply %q, want %q", replies.String(), tc.want)
			}
		})
	}
}

// TestOSC4IndexRange: OSC 4 accepted indices up to 2^24 and stored them in
// the override table that every cell colour goes through, so an index equal
// to a truecolour value recoloured those cells. Only 0-255 are palette
// indices; anything else must leave the terminal unchanged.
func TestOSC4IndexRange(t *testing.T) {
	red := New(WithSize(4, 1))
	if _, err := red.Write([]byte("\x1b[38;2;255;0;0mR")); err != nil {
		t.Fatal(err)
	}
	truecolour := red.Cell(0, 0).FG
	for _, index := range []string{"256", "16711680", strconv.Itoa(int(truecolour)), "-1", "x", ""} {
		t.Run(index, func(t *testing.T) {
			term := New(WithSize(4, 1))
			if _, err := term.Write([]byte("\x1b[38;2;255;0;0mR\x1b]4;" + index + ";#00ff00\x07")); err != nil {
				t.Fatal(err)
			}
			if fg := term.Cell(0, 0).FG; fg != truecolour {
				t.Fatalf("truecolour red became %#x", fg)
			}
			if term.ColorGen() != 0 {
				t.Fatalf("rejected index %q changed the colour generation", index)
			}
			if _, ok := term.ColorOverride(Color(255)); ok {
				t.Fatal("override stored for palette 255")
			}
		})
	}
}

// TestCSICountCap: CSI parameters near 2^63 wrapped negative when added to
// the cursor position, so CUF moved to column 0 and ECH erased to the left;
// longer ones failed to parse and fell back to the default. Signed ones
// erased to the left too. Parameters are now digits only and capped at
// 65535, as xterm caps them (MAX_I_PARAM).
func TestCSICountCap(t *testing.T) {
	for _, tc := range []struct {
		name, seq string
		x         int
		row       string
	}{
		{"forward", "\x1b[1;5H\x1b[9223372036854775807C", 9, "abcdefghij"},
		{"forward beyond int64", "\x1b[1;5H\x1b[99999999999999999999C", 9, "abcdefghij"},
		{"down then forward", "\x1b[1;5H\x1b[9223372036854775807B\x1b[9223372036854775807a", 9, "abcdefghij"},
		{"erase characters", "\x1b[1;5H\x1b[9223372036854775807X", 4, "abcd      "},
		{"signed erase", "\x1b[1;5H\x1b[-5X", 4, "abcd fghij"},
		{"signed forward", "\x1b[1;5H\x1b[+3C", 5, "abcdefghij"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := New(WithSize(10, 3))
			if _, err := term.Write([]byte("abcdefghij" + tc.seq)); err != nil {
				t.Fatal(err)
			}
			if x := term.Cursor().X; x != tc.x {
				t.Fatalf("cursor x = %d, want %d", x, tc.x)
			}
			if row := strings.SplitN(term.String(), "\n", 2)[0]; row != tc.row {
				t.Fatalf("row 0 = %q, want %q", row, tc.row)
			}
		})
	}
	var c csiEscape
	c.buf = append(c.buf, "99999999999;65536;65535;070000;1:99999m"...)
	c.parse()
	if want := []int{65535, 65535, 65535, 65535, 10000 + 65535 + 1}; !slices.Equal(c.args, want) {
		t.Fatalf("args %v, want %v", c.args, want)
	}
	term := New(WithSize(10, 1))
	if _, err := term.Write([]byte("\x1b[99999999999999999999999 q")); err != nil {
		t.Fatal(err)
	}
	if shape := term.Cursor().Shape; shape != 65535 {
		t.Fatalf("DECSCUSR shape %d, want 65535", shape)
	}
}
