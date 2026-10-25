package vt10x

import (
	"bytes"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// Text returns the complete grapheme stored in a lead cell.
func (g Glyph) Text() string {
	if g.Width < 0 {
		return ""
	}
	if g.Char == 0 {
		return " "
	}
	return string(g.Char) + g.Combining
}

// extendGrapheme is incremental across Write calls. Controls close the current
// cluster, so moving the cursor cannot accidentally extend an unrelated cell.
func (t *State) extendGrapheme(c rune) bool {
	if !t.clusterOpen || t.clusterX >= t.cols || t.clusterY >= t.rows {
		return false
	}
	x, y := t.clusterX, t.clusterY
	g := t.lines[y].Glyph(x)
	// Build the candidate cluster in a stack buffer; most runes start a new
	// cluster, and that check should not allocate.
	var buf [64]byte
	text := buf[:0]
	lead := 0 // bytes of g.Char in text
	if g.Width >= 0 {
		if g.Char == 0 {
			text = append(text, ' ')
		} else {
			text = utf8.AppendRune(text, g.Char)
			lead = len(text)
			text = append(text, g.Combining...)
		}
	}
	text = utf8.AppendRune(text, c)
	// The text of a lead cell decides the segmentation, so a repeated
	// sequence is looked up instead of segmented again.
	var seg *clusterEntry
	if lead > 0 && len(text) <= len(buf) {
		seg = t.clusters.lookup(text, lead, g.Char, c)
	} else {
		seg = new(clusterEntry)
		seg.segment(text, lead, g.Char, c)
	}
	width := seg.width
	legacy := t.mode&ModeGrapheme == 0
	if !seg.joins || (legacy && !seg.zeroWidth) {
		return false
	}
	// A pathological combining sequence must not grow per-cell storage forever.
	if len(text) > 1024 {
		return true
	}
	// Per-codepoint widths (DECRST 2027): a zero-width code point such as
	// VS16 or a keycap joins the cell without widening it.
	if legacy && g.Width > 0 {
		width = int(g.Width)
	}
	// Cells hold at most two columns. uniseg adds up Prepend widths, so
	// "\u0600\u0600a" reports 3.
	width = min(min(t.cols, 2), max(int(g.Width), width))
	if lead > 0 {
		g.Combining = seg.suffix
	} else {
		g.Combining += string(c)
	}
	if x+width > t.cols {
		if t.mode&ModeWrap == 0 {
			return true
		}
		t.eraseCell(x, y)
		t.lines[y].cells[x].setWidth(-2)
		t.lines[y].setWrap(x)
		t.markDirty(y)
		t.newline(true)
		x, y = t.cur.X, t.cur.Y
	}
	if width == 2 {
		t.eraseWideAt(x+1, y)
	}
	g.Width = int8(width)
	t.lines[y].set(x, g)
	if width == 2 {
		t.lines[y].set(x+1, Glyph{Width: -1, Mode: g.Mode, FG: g.FG, BG: g.BG, UL: g.UL, Link: g.Link})
	}
	t.markDirty(y)
	t.clusterX, t.clusterY = x, y
	t.cur.X, t.cur.Y = min(x+width, t.cols-1), y
	t.cur.State &^= cursorWrapNext
	if x+width == t.cols {
		t.cur.State |= cursorWrapNext
	}
	return true
}

// clusterCache keeps recent segmentations, two for each hash of the cell's
// lead rune, the next rune and the text length. Each pair holds the most
// recently used entry first.
type clusterCache [64]clusterEntry

// lookup returns the segmentation of text, a cell's text extended by c,
// whose lead rune ch takes the first lead bytes.
func (cc *clusterCache) lookup(text []byte, lead int, ch, c rune) *clusterEntry {
	h := uint32(c)*0x9e3779b1 ^ uint32(ch)*0x85ebca6b ^ uint32(len(text))*0xc2b2ae35
	set := cc[h>>27*2:][:2]
	if set[0].text == string(text) {
		return &set[0]
	}
	if set[1].text == string(text) {
		set[0], set[1] = set[1], set[0]
	} else {
		set[1] = set[0]
		set[0].segment(text, lead, ch, c)
	}
	return &set[0]
}

// clusterEntry is the segmentation of a candidate cluster: the text of a
// cell followed by the next rune.
type clusterEntry struct {
	text      string // the candidate cluster; "" in an empty slot
	suffix    string // text after the cell's lead rune: Combining once joined
	width     int    // uniseg's width of text, with keycaps widened
	joins     bool   // text is one grapheme cluster
	zeroWidth bool   // the last rune alone is zero columns wide
}

// segment fills e for text, whose lead rune, ch, takes the first lead
// bytes, and whose last rune is c.
func (e *clusterEntry) segment(text []byte, lead int, ch, c rune) {
	_, rest, width, _ := uniseg.FirstGraphemeCluster(text, -1)
	// uniseg 0.4.7 does not widen emoji keycap sequences.
	if bytes.ContainsRune(text, '\u20e3') && (ch >= '0' && ch <= '9' || ch == '#' || ch == '*') {
		width = 2
	}
	e.text = string(text)
	e.suffix = e.text[lead:]
	e.width = width
	e.joins = len(rest) == 0
	e.zeroWidth = uniseg.StringWidth(string(c)) == 0
}

// graphemeProbes holds one code point for each grapheme break property a
// simple grapheme can have: Other, Control, Hangul LV and LVT syllables, and
// Extended_Pictographic.
var graphemeProbes = [...]rune{'a', '\u0085', '\uac00', '\uac01', '\U0001f600'}

// simpleGrapheme reports whether c never joins a neighbouring simple
// grapheme into one cluster, so text made of simple graphemes needs no
// segmentation.
//
// uniseg decides the boundary between two code points from their break
// properties alone. A rune that joins 'a' on neither side is not Extend,
// ZWJ, SpacingMark (GB9, GB9a) or Prepend (GB9b). A rune that does not
// join itself is not a regional indicator or a Hangul L, V or T jamo
// (GB6-8, GB12). The properties left are Other, Control, LV, LVT and
// Extended_Pictographic (CR and LF are ASCII controls that never become
// text), and graphemeProbes holds one rune of each. Two simple runes
// therefore never join.
func simpleGrapheme(c rune) bool {
	if joins(c, c) {
		return false
	}
	for _, p := range graphemeProbes {
		if joins(p, c) || joins(c, p) {
			return false
		}
	}
	return true
}

// endsCluster reports whether no simple grapheme joins a cluster whose last
// code point is c, whatever comes before c.
//
// In uniseg's rules a simple rune joins only three parser states: after a
// Prepend (GB9b), after a Hangul L jamo (GB6, for LV and LVT), and after
// Extended_Pictographic Extend* ZWJ (GB11, for Extended_Pictographic). Each
// state is entered only on its own property, so the last code point decides.
// A Prepend joins 'a', an L jamo joins an LV syllable, and a ZWJ joins two
// pictographs.
func endsCluster(c rune) bool {
	const emoji = '\U0001f600'
	if joins(c, 'a') || joins(c, '\uac00') {
		return false
	}
	var buf [3 * utf8.UTFMax]byte
	text := utf8.AppendRune(utf8.AppendRune(utf8.AppendRune(buf[:0], emoji), c), emoji)
	_, rest, _, _ := uniseg.FirstGraphemeCluster(text, -1)
	return len(rest) != 0
}

// joins reports whether a and b form one grapheme cluster.
func joins(a, b rune) bool {
	var buf [2 * utf8.UTFMax]byte
	text := utf8.AppendRune(utf8.AppendRune(buf[:0], a), b)
	_, rest, _, _ := uniseg.FirstGraphemeCluster(text, -1)
	return len(rest) == 0
}

// closedCluster reports whether no simple grapheme extends the cluster in
// cell x: its last code point ends clusters (endsCluster). Char 0 is a
// blank. Printable ASCII is Other; other ASCII controls stay with parse.
func (r *Row) closedCluster(x int) bool {
	last := r.cells[x].Char
	if r.hasCombining(x) {
		last, _ = utf8.DecodeLastRuneInString(r.Combining(x))
	}
	if last < 0x80 {
		return last == 0 || last >= 0x20 && last < 0x7f
	}
	return last < cachedWidthRunes && runeInfo(last)&infoEndsCluster != 0
}
