package vt10x

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"
	"math/rand/v2"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Fuzz inputs are cut to these sizes so seed runs in go test stay fast.
const (
	maxFuzzInput = 16 << 10
	maxFuzzCols  = 128
	maxFuzzRows  = 48
	// maxCellText is the grapheme bound extendGrapheme keeps per cell.
	maxCellText = 1024
)

// fuzzTerm is a terminal that records every reply and callback, so two
// terminals can be compared after the same input. Scrolled rows are only
// hashed, so its link sweeps need to mark nothing outside the screens, and
// they are due after almost every new URL.
type fuzzTerm struct {
	*terminal
	replies       bytes.Buffer
	scrolled      hash.Hash64 // rows passed to the scroll callback
	scrolledRows  int
	bells, clears int
	carry         []byte // bytes Write left unconsumed: an incomplete UTF-8 sequence
}

func newFuzzTerm(cols, rows int) *fuzzTerm {
	f := &fuzzTerm{scrolled: fnv.New64a()}
	f.terminal = New(WithSize(cols, rows), WithWriter(&f.replies),
		WithScrollCallback(func(r Row) {
			f.scrolledRows++
			hashGlyphs(f.scrolled, r.AppendGlyphs(nil))
		}),
		WithBellCallback(func() { f.bells++ }),
		WithScrollbackClearCallback(func() { f.clears++ }),
		WithLinkReferences(func(func(uint16)) {}),
	).(*terminal)
	l := &f.links
	l.sweepMin, l.sweepAt, l.sweepMinBytes, l.sweepBytes, l.sweepGap = 2, 2, 64, 64, 1
	return f
}

// write passes p after the bytes the previous call left unconsumed, as a
// caller that honours Write's count does. runes selects the rune-at-a-time
// parser without the ASCII fast path.
func (f *fuzzTerm) write(p []byte, runes bool) {
	buf := append(append([]byte(nil), f.carry...), p...)
	var n int
	if runes {
		f.lock()
		n = writeOneRuneAtATime(f.terminal, buf)
		f.unlock()
	} else {
		n, _ = f.Write(buf)
	}
	f.carry = buf[n:]
}

// hashGlyphs adds every field of glyphs to h.
func hashGlyphs(h hash.Hash64, glyphs []Glyph) {
	var b []byte
	for _, g := range glyphs {
		b = binary.LittleEndian.AppendUint32(b, uint32(g.Char))
		b = binary.LittleEndian.AppendUint32(b, uint32(len(g.Combining)))
		b = append(b, g.Combining...)
		b = append(b, byte(g.Width))
		b = binary.LittleEndian.AppendUint16(b, uint16(g.Mode))
		b = binary.LittleEndian.AppendUint32(b, uint32(g.FG))
		b = binary.LittleEndian.AppendUint32(b, uint32(g.BG))
		b = binary.LittleEndian.AppendUint32(b, uint32(g.UL))
		b = binary.LittleEndian.AppendUint16(b, g.Link)
		if g.Image != nil {
			b = fmt.Appendf(b, "%v%v%v", g.Image.Top, g.Image.Bottom, *g.Image.Placement)
		}
	}
	b = append(b, '\n')
	h.Write(b) //nolint:errcheck // hash writes never fail
}

// termSnapshot is everything that later input or a front end can observe.
type termSnapshot struct {
	Lines, AltLines    [][]Glyph
	Cur, Saved         Cursor
	Mode               ModeFlag
	State              parseState
	Top, Bottom        int
	Title              string
	Tabs               []bool
	ClusterOpen        bool
	ClusterX, ClusterY int
	Dirty              []bool
	AnyDirty, AllDirty bool
	Changed            ChangeFlag
	Links              []string
	Colors             map[Color]Color
	ColorGen           uint64
	CSI, Str           string
	Graphics           bool
	Replies            string
	Scrolled           uint64
	ScrolledRows       int
	Bells, Clears      int
	Carry              string
}

func (f *fuzzTerm) snapshot() termSnapshot {
	s := f.State
	return termSnapshot{
		Lines: decodeRows(s.lines), AltLines: decodeRows(s.altLines),
		Cur: s.cur, Saved: s.curSaved, Mode: s.mode, State: s.state,
		Top: s.top, Bottom: s.bottom, Title: s.title, Tabs: s.tabs,
		ClusterOpen: s.clusterOpen, ClusterX: s.clusterX, ClusterY: s.clusterY,
		Dirty: s.dirty, AnyDirty: s.anydirty, AllDirty: s.allDirty, Changed: s.changed,
		Links: s.links.urls, Colors: s.colorOverride, ColorGen: s.colorGen,
		CSI:      fmt.Sprintf("%q %v %q %v", s.csi.buf, s.csi.args, s.csi.mode, s.csi.priv),
		Str:      fmt.Sprintf("%q %q %v %v", s.str.typ, s.str.buf, s.str.graphics, s.str.discard),
		Graphics: s.graphics != nil,
		Replies:  f.replies.String(), Scrolled: f.scrolled.Sum64(), ScrolledRows: f.scrolledRows,
		Bells: f.bells, Clears: f.clears, Carry: string(f.carry),
	}
}

// snapshotDiff names the first field that differs between a and b.
func snapshotDiff(a, b termSnapshot) string {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	for i := 0; i < va.NumField(); i++ {
		fa, fb := va.Field(i).Interface(), vb.Field(i).Interface()
		if reflect.DeepEqual(fa, fb) {
			continue
		}
		name := va.Type().Field(i).Name
		if ga, ok := fa.([][]Glyph); ok {
			return name + ": " + gridDiff(ga, fb.([][]Glyph))
		}
		return fmt.Sprintf("%s: %+v\nvs %+v", name, fa, fb)
	}
	return ""
}

// gridDiff describes the first cell that differs between two grids.
func gridDiff(a, b [][]Glyph) string {
	for y := range min(len(a), len(b)) {
		for x := range min(len(a[y]), len(b[y])) {
			if !reflect.DeepEqual(a[y][x], b[y][x]) {
				return fmt.Sprintf("cell (%d,%d) %+v vs %+v", x, y, a[y][x], b[y][x])
			}
		}
	}
	return "grid sizes differ"
}

// checkInvariants returns the first structural invariant s breaks, or nil.
// maxExtras bounds each row's side table.
func checkInvariants(s *State, maxSide int) error {
	if len(s.lines) != s.rows || len(s.altLines) != s.rows || len(s.dirty) != s.rows || len(s.tabs) != s.cols {
		return fmt.Errorf("%dx%d grid holds %d rows, %d alternate rows, %d dirty flags and %d tab stops",
			s.cols, s.rows, len(s.lines), len(s.altLines), len(s.dirty), len(s.tabs))
	}
	if s.cur.X < 0 || s.cur.X >= s.cols || s.cur.Y < 0 || s.cur.Y >= s.rows {
		return fmt.Errorf("cursor (%d,%d) outside the %dx%d grid", s.cur.X, s.cur.Y, s.cols, s.rows)
	}
	if s.top < 0 || s.top > s.bottom || s.bottom >= s.rows {
		return fmt.Errorf("scroll region %d-%d outside %d rows", s.top, s.bottom, s.rows)
	}
	// Cell colours go through the override table, so a key outside the
	// palette and the defaults would recolour truecolour cells.
	for k, v := range s.colorOverride {
		if k >= 256 && k != DefaultFG && k != DefaultBG && k != DefaultCursor {
			return fmt.Errorf("colour override for %#x, which is neither a palette index nor a default", k)
		}
		if v < rgbFlag || v >= DefaultFG {
			return fmt.Errorf("colour override %#x for %#x is not a truecolour value", v, k)
		}
	}
	for _, c := range []struct {
		name string
		link uint16
	}{{"cursor", s.cur.Attr.Link}, {"saved cursor", s.curSaved.Attr.Link}} {
		if c.link != 0 && s.Link(c.link) == "" {
			return fmt.Errorf("%s links to ID %d, which names no URL", c.name, c.link)
		}
	}
	if err := checkLinkTable(&s.links); err != nil {
		return fmt.Errorf("link table: %w", err)
	}
	for _, screen := range []struct {
		name string
		rows []*Row
	}{{"primary", s.lines}, {"alternate", s.altLines}} {
		for y, r := range screen.rows {
			if err := checkRow(r, s.cols, s.Link, maxSide); err != nil {
				return fmt.Errorf("%s row %d: %w", screen.name, y, err)
			}
		}
	}
	return nil
}

// checkRow checks the used/tail invariant from row.go, the side table and
// the cell widths of one row.
func checkRow(r *Row, cols int, link func(uint16) string, maxSide int) error {
	if len(r.cells) != cols {
		return fmt.Errorf("%d cells in a %d-column grid", len(r.cells), cols)
	}
	if r.used < 0 || r.used > cols {
		return fmt.Errorf("used = %d outside 0-%d", r.used, cols)
	}
	if r.tail.extra() != 0 {
		return fmt.Errorf("tail refers to the side table")
	}
	for x := r.used; x < cols; x++ {
		if r.cells[x] != r.tail {
			return fmt.Errorf("cell %d at or past used = %d differs from the tail", x, r.used)
		}
	}
	if len(r.extras) > maxSide {
		return fmt.Errorf("side table holds %d entries, more than %d", len(r.extras), maxSide)
	}
	for x, c := range r.cells {
		if c.extra() > len(r.extras) {
			return fmt.Errorf("cell %d refers to side-table entry %d of %d", x, c.extra(), len(r.extras))
		}
		g := r.Glyph(x)
		if len(g.Text()) > maxCellText {
			return fmt.Errorf("cell %d holds %d bytes of text", x, len(g.Text()))
		}
		if g.Link != 0 && link(g.Link) == "" {
			return fmt.Errorf("cell %d links to ID %d, which names no URL", x, g.Link)
		}
		switch g.Width {
		case 0, 1:
		case 2:
			if x+1 == cols || r.cells[x+1].width() != -1 {
				return fmt.Errorf("wide lead at %d without a continuation", x)
			}
			// A continuation is written with its lead's attributes; a
			// different set means it belongs to another character.
			c := r.Glyph(x + 1)
			if c.FG != g.FG || c.BG != g.BG || c.UL != g.UL || c.Link != g.Link ||
				c.Mode&^attrWrap != g.Mode&^attrWrap {
				return fmt.Errorf("wide lead at %d paired with another character's continuation", x)
			}
		case -1:
			if x == 0 || r.cells[x-1].width() != 2 {
				return fmt.Errorf("continuation at %d without a wide lead", x)
			}
		case -2:
			for k := x + 1; k < cols; k++ {
				if r.cells[k].width() != -2 {
					return fmt.Errorf("wrap padding at %d before a width-%d cell at %d", x, r.cells[k].width(), k)
				}
			}
			if r.cells[cols-1].Mode&attrWrap == 0 {
				return fmt.Errorf("wrap padding at %d in a row that does not wrap", x)
			}
		default:
			return fmt.Errorf("cell %d has width %d", x, g.Width)
		}
	}
	return nil
}

// checkSweptLinks reports an error unless the link table holds exactly the
// IDs that cells and cursors refer to, as right after a sweep.
func checkSweptLinks(s *State) error {
	refs := map[uint16]bool{}
	s.markLinks(func(id uint16) {
		if id != 0 {
			refs[id] = true
		}
	})
	if len(refs) != len(s.links.ids) {
		return fmt.Errorf("after a sweep the link table holds %d URLs, cells and cursors refer to %d", len(s.links.ids), len(refs))
	}
	return nil
}

// sideTableBound is the side-table size extraIndex keeps a row of cols cells
// within.
func sideTableBound(cols int) int { return 2*cols + 32 }

// checkReplies returns an error unless replies is a run of complete escape
// sequences: CSI, or OSC, DCS and APC strings of printable ASCII.
func checkReplies(replies []byte) error {
	for i := 0; i < len(replies); {
		if replies[i] != 0x1b || i+1 == len(replies) {
			return fmt.Errorf("byte %d (%q) does not start an escape sequence", i, replies[i])
		}
		start := i
		i += 2
		switch replies[start+1] {
		case '[':
			for i < len(replies) && replies[i] >= 0x20 && replies[i] <= 0x3f {
				i++
			}
			if i == len(replies) || replies[i] < 0x40 || replies[i] > 0x7e {
				return fmt.Errorf("unterminated CSI reply %q", replies[start:])
			}
			i++
		case ']', 'P', '_':
			for i < len(replies) && replies[i] >= 0x20 && replies[i] <= 0x7e {
				i++
			}
			body := string(replies[start+2 : i])
			switch {
			case i < len(replies) && replies[i] == 0x07 && replies[start+1] == ']':
				i++
			case i+1 < len(replies) && replies[i] == 0x1b && replies[i+1] == '\\':
				i += 2
			default:
				return fmt.Errorf("string reply %q holds a control byte or no terminator", replies[start:min(len(replies), i+1)])
			}
			if replies[start+1] == ']' {
				if err := checkColorReply(body); err != nil {
					return fmt.Errorf("OSC reply %q: %w", body, err)
				}
			}
		default:
			return fmt.Errorf("unknown reply introducer %q", replies[start:start+2])
		}
	}
	return nil
}

// checkColorReply checks the body of an OSC reply: "4;<index>;rgb:..."
// with a palette index, or "10", "11" or "12" then ";rgb:...". Each
// component repeats one byte, as XParseColor expects of an 8-bit colour.
func checkColorReply(body string) error {
	num, rest, _ := strings.Cut(body, ";")
	switch num {
	case "10", "11", "12":
	case "4":
		var index string
		index, rest, _ = strings.Cut(rest, ";")
		if n, err := strconv.Atoi(index); err != nil || n < 0 || n > 255 || strconv.Itoa(n) != index {
			return fmt.Errorf("palette index %q outside 0-255", index)
		}
	default:
		return fmt.Errorf("OSC %q is not a colour reply", num)
	}
	spec, ok := strings.CutPrefix(rest, "rgb:")
	parts := strings.Split(spec, "/")
	if !ok || len(parts) != 3 {
		return fmt.Errorf("colour %q is not rgb:rrrr/gggg/bbbb", rest)
	}
	for _, part := range parts {
		if len(part) != 4 || part[:2] != part[2:] || strings.Trim(part, "0123456789abcdef") != "" {
			return fmt.Errorf("colour component %q is not one repeated hex byte", part)
		}
	}
	return nil
}

// cappedCSIParams returns data with every CSI parameter above maxParam
// rewritten as maxParam, or nil when there is none. A probe terminal marks
// the digit runs the parser reads as parameters of a CSI sequence that ends
// with a final byte. A run's digits may join others across a skipped byte,
// but a capped value stays capped whatever digits come before it.
func cappedCSIParams(cols, rows int, data []byte) []byte {
	probe := newFuzzTerm(cols, rows)
	probe.lock()
	defer probe.unlock()
	type span struct{ start, end int }
	var pending, runs []span // digit runs of the open CSI, and of finished ones
	start := -1
	for i := 0; i < len(data); {
		if !utf8.FullRune(data[i:]) {
			break
		}
		c, sz := utf8.DecodeRune(data[i:])
		if c == utf8.RuneError && sz == 1 {
			if start >= 0 {
				pending, start = append(pending, span{start, i}), -1
			}
			i++
			continue
		}
		before := probe.state
		probe.put(c)
		digit := before == stateEscCSI && c >= '0' && c <= '9'
		switch {
		case digit && start < 0:
			start = i
		case !digit && start >= 0:
			pending, start = append(pending, span{start, i}), -1
		}
		i += sz
		if before == stateEscCSI && probe.state != stateEscCSI {
			if start >= 0 {
				pending, start = append(pending, span{start, i}), -1
			}
			buf := probe.csi.buf
			if probe.state == stateGround && c != 0x18 && c != 0x1a && len(buf) > 0 && buf[len(buf)-1] >= 0x40 && buf[len(buf)-1] <= 0x7e {
				runs = append(runs, pending...)
			}
			pending = pending[:0]
		}
	}
	var out []byte
	last := 0
	for _, r := range runs {
		digits := strings.TrimLeft(string(data[r.start:r.end]), "0")
		if len(digits) < 5 || len(digits) == 5 && digits <= "65535" {
			continue
		}
		out = append(append(out, data[last:r.start]...), "65535"...)
		last = r.end
	}
	if out == nil {
		return nil
	}
	return append(out, data[last:]...)
}

// maxReplyBytes bounds the replies to n bytes of output. Every query is a
// few bytes long and the longest reply, a Kitty graphics error, is under 100.
func maxReplyBytes(n int) int { return 16*n + 64 }

// maxCheckedCells bounds the cells FuzzParse checks between chunks.
const maxCheckedCells = 1 << 20

// fuzzTimeLimit is how long one fuzz input may run. The fuzzer does not
// time inputs out itself; a stuck worker only shows as zero execs per second.
const fuzzTimeLimit = 10 * time.Second

// watchdog crashes the fuzz worker when an input runs longer than
// fuzzTimeLimit, so the fuzzer reports and keeps it. Call the result to stop
// the watchdog.
func watchdog() (stop func()) {
	timer := time.AfterFunc(fuzzTimeLimit, func() {
		panic(fmt.Sprintf("fuzz input ran for over %v", fuzzTimeLimit))
	})
	return func() { timer.Stop() }
}

// fuzzSize maps fuzzer bytes to a grid size.
func fuzzSize(cols, rows uint8) (int, int) {
	return 1 + int(cols)%maxFuzzCols, 1 + int(rows)%maxFuzzRows
}

// fuzzChunks splits data at lengths taken from cuts, cycling through them.
// Without cuts it returns data whole.
func fuzzChunks(data, cuts []byte) [][]byte {
	var chunks [][]byte
	for off, i := 0, 0; off < len(data); i++ {
		n := len(data) - off
		if len(cuts) > 0 {
			n = min(n, 1+int(cuts[i%len(cuts)]))
		}
		chunks = append(chunks, data[off:off+n])
		off += n
	}
	return chunks
}

// parseSeeds are streams that reach every parser state and the cell-width
// edge cases.
var parseSeeds = []string{
	"hello, world\r\n\x1b[1;31mred\x1b[0m \x1b[38;2;1;2;3mrgb\x1b[m",
	"\x1b[?1049h\x1b[2J\x1b[H\x1b[5;10rscroll\x1bM\x1bD\x1b[S\x1b[T\x1b[?1049l",
	"\u754c\u754cAB\x1b[2G\x1b[@\x1b[P\x1b[X\x1b[K\x1b[1K\x1b[2K",
	"abcd\u754c\x1b[1;4H\x1b[3PZ\x1b[1;5HYY",
	"e\u0301\u0301\u0301 \U0001f468\u200d\U0001f469\u200d\U0001f467 1\ufe0f\u20e3 \U0001f1e9\U0001f1ea \u0600\u0600a",
	"\x1b[?2027l\U0001f44d\U0001f3fd\x1b[?2027h\U0001f44d\U0001f3fd",
	"\x1b]0;title\x07\x1b]2;title\x1b\\\x1bkold\x1b\\\x1b]8;id=1;https://example.com\x1b\\link\x1b]8;;\x1b\\",
	"\x1b]10;?\x07\x1b]11;?\x1b\\\x1b]12;#ff0000\x07\x1b]4;1;rgb:ff/00/00\x07\x1b]4;1;?\x07\x1b]104;1\x07\x1b]110\x07",
	"\x1b[6n\x1b[5n\x1b[?6h\x1b[3;5r\x1b[6n\x1b[?6l\x1b[c\x1b[>c",
	"\x1b[4:3m\x1b[58:2::1:2:3m\x1b[21m\x1b[53m\x1b[2;8;9mX\x1b[?4m\x1b[4h\x1b[4l",
	"\x1b[2 q\x1b[?12l\x1b[?12h\x1b[?25l\x1b[?2004h\x1b[?2026h\x1b[?1000h\x1b[?1006h",
	"\x1b#8\x1b(0lqqk\x1b(B\x0e\x0f\x1b7\x1b[10;10H\x1b8\x1b[s\x1b[u",
	"\x1bP$qm\x1b\\\x1bP+q544e\x1b\\\x1b_Ga=q,i=1\x1b\\\x1b^pm\x1b\\",
	"\x1b_Ga=T,i=1,f=24,s=1,v=1,c=2,r=1;/wAA\x1b\\\x1b_Ga=d,d=i,i=1\x1b\\",
	"\x1bPq#0;2;100;0;0#0!8~-~~\x1b\\\x1b]1337;File=inline=1:AAAA\x07",
	"\x1b]0;unterminated\x18text\x1b[12\x1aafter\x1b[3J\x1bc",
	"\t\tx\x1b[3g\x1b[g\x1bH\x1b[2I\x1b[2Z\x1b[5`\x1b[3d\x1b[2E\x1b[F\x1b[2a\x1b[e",
	"\xe6\x97\xa5\xe6\x97\xff\xc3(\xf0\x9f\x98\x80\xed\xa0\x80\xf4\x90\x80\x80",
	"abcdefgh\x1b[1;5H\x1b[9223372036854775807C\x1b[2;5H\x1b[9223372036854775807X\x1b[099999999999999999999B\x1b[-5X\x1b[+3C\x1b[1;9223372036854775807H\x1b[0000070000 q",
	"\x1b[38;2;255;0;0mR\x1b]4;16711680;#00ff00\x07\x1b]4;33488896;#00ff00\x07\x1b]4;256;#fff\x07\x1b]4;x;#fff\x07\x1b]4;7;?\x07\x1b]4;3;rgb:12/34/56\x07\x1b]4;3;?\x07\x1b]104;3\x07\x1b]4;3;?\x07",
}

func FuzzParse(f *testing.F) {
	for i, seed := range parseSeeds {
		f.Add(uint8(9+i), uint8(i%5), []byte{byte(i % 4), 2, 7}, []byte(seed))
	}
	for seed := uint64(1); seed <= 8; seed++ {
		rng := rand.New(rand.NewPCG(seed, 0))
		cols, rows := 1+rng.IntN(30), 1+rng.IntN(12)
		f.Add(uint8(cols-1), uint8(rows-1), []byte{byte(rng.IntN(64))}, randomStream(rng, cols, rows))
	}
	f.Fuzz(func(t *testing.T, cols, rows uint8, cuts, data []byte) {
		if len(data) > maxFuzzInput {
			return
		}
		defer watchdog()()
		c, r := fuzzSize(cols, rows)
		whole := checkWrites(t, c, r, cuts, data)
		replies := whole.replies.Bytes()
		if err := checkReplies(replies); err != nil {
			t.Fatalf("malformed reply: %v", err)
		}
		if len(replies) > maxReplyBytes(len(data)) {
			t.Fatalf("%d bytes of replies to %d bytes of output", len(replies), len(data))
		}
		// A CSI parameter above maxParam must act exactly like maxParam:
		// counts near 2^63 used to wrap negative.
		if capped := cappedCSIParams(c, r, data); capped != nil {
			variant := newFuzzTerm(c, r)
			variant.write(capped, false)
			a, b := whole.snapshot(), variant.snapshot()
			a.CSI, b.CSI = "", "" // the raw text of the last sequence differs
			if d := snapshotDiff(a, b); d != "" {
				t.Fatalf("%dx%d: CSI parameters capped at %d differ in %s\ncapped: %q", c, r, maxParam, d, capped)
			}
		}
		// A sweep keeps exactly the IDs that cells and cursors refer to.
		whole.SweepLinks()
		if err := checkInvariants(whole.State, sideTableBound(c)); err != nil {
			t.Fatalf("%dx%d after a link sweep: %v", c, r, err)
		}
		if err := checkSweptLinks(whole.State); err != nil {
			t.Fatalf("%dx%d: %v", c, r, err)
		}
	})
}

// checkWrites writes data to three c x r terminals: whole, in chunks cut
// at cuts, and rune by rune without the fast paths. All three must end in
// the same state and keep the grid invariants. It returns the first.
func checkWrites(t *testing.T, c, r int, cuts, data []byte) *fuzzTerm {
	t.Helper()
	whole, chunked, runes := newFuzzTerm(c, r), newFuzzTerm(c, r), newFuzzTerm(c, r)
	whole.write(data, false)
	if err := checkInvariants(whole.State, sideTableBound(c)); err != nil {
		t.Fatalf("%dx%d after %q: %v", c, r, data, err)
	}
	chunks := fuzzChunks(data, cuts)
	// Checking every chunk boundary of a long input in a large grid is
	// slow; check as many as a fixed budget of cells allows.
	every := max(1, len(chunks)*c*r/maxCheckedCells)
	for i, chunk := range chunks {
		chunked.write(chunk, false)
		runes.write(chunk, true)
		if i%every != 0 {
			continue
		}
		if err := checkInvariants(chunked.State, sideTableBound(c)); err != nil {
			t.Fatalf("%dx%d after chunk %d %q: %v", c, r, i, chunk, err)
		}
	}
	if len(chunked.carry) > 3 {
		t.Fatalf("Write left %d bytes unconsumed", len(chunked.carry))
	}
	if d := snapshotDiff(whole.snapshot(), chunked.snapshot()); d != "" {
		t.Fatalf("%dx%d: chunked write differs from whole write in %s", c, r, d)
	}
	if d := snapshotDiff(chunked.snapshot(), runes.snapshot()); d != "" {
		t.Fatalf("%dx%d: fast paths differ from rune parser in %s", c, r, d)
	}
	return whole
}

// FuzzResize writes output, resizes the terminal, then writes more. The
// grid must stay consistent through the resize.
func FuzzResize(f *testing.F) {
	for i, seed := range parseSeeds {
		f.Add(uint8(9+i), uint8(i%5), uint8(i*7), uint8(i*3), uint16(len(seed)/2), []byte(seed))
	}
	f.Fuzz(func(t *testing.T, cols, rows, cols2, rows2 uint8, at uint16, data []byte) {
		if len(data) > maxFuzzInput {
			return
		}
		c, r := fuzzSize(cols, rows)
		c2, r2 := fuzzSize(cols2, rows2)
		split := int(at) % (len(data) + 1)
		defer watchdog()()
		term := newFuzzTerm(c, r)
		term.write(data[:split], false)
		term.Resize(c2, r2)
		// Rows keep the side table of their wider source until it compacts.
		bound := sideTableBound(max(c, c2))
		if err := checkInvariants(term.State, bound); err != nil {
			t.Fatalf("%dx%d resized to %dx%d after %q: %v", c, r, c2, r2, data[:split], err)
		}
		term.write(data[split:], false)
		if err := checkInvariants(term.State, bound); err != nil {
			t.Fatalf("%dx%d resized to %dx%d, then %q: %v", c, r, c2, r2, data[split:], err)
		}
		if err := checkReplies(term.replies.Bytes()); err != nil {
			t.Fatalf("malformed reply: %v", err)
		}
	})
}

// TestCheckInvariantsCatchesBrokenGrids keeps the fuzz checks honest: each
// broken grid must be reported.
func TestCheckInvariantsCatchesBrokenGrids(t *testing.T) {
	for _, tc := range []struct {
		name    string
		corrupt func(s *State)
		want    string
	}{
		{"cursor", func(s *State) { s.cur.X = s.cols }, "cursor"},
		{"used", func(s *State) { s.lines[0].cells[3].Char = 'x'; s.lines[0].used = 2 }, "differs from the tail"},
		{"orphan continuation", func(s *State) { s.lines[1].set(2, Glyph{Width: -1}) }, "without a wide lead"},
		{"lone lead", func(s *State) { s.lines[1].set(2, Glyph{Char: '界', Width: 2}) }, "without a continuation"},
		{"foreign continuation", func(s *State) {
			s.lines[1].set(2, Glyph{Char: '界', Width: 2, FG: 1})
			s.lines[1].set(3, Glyph{Width: -1, FG: 2})
		}, "another character's continuation"},
		{"padding before text", func(s *State) { s.lines[0].set(1, Glyph{Char: ' ', Width: -2}) }, "wrap padding"},
		{"padding without wrap", func(s *State) { s.lines[0].set(s.cols-1, Glyph{Char: ' ', Width: -2}) }, "does not wrap"},
		{"free link", func(s *State) { s.lines[1].set(0, Glyph{Char: 'a', Width: 1, Link: 1}) }, "names no URL"},
		{"link table", func(s *State) { s.links.bytes++ }, "link table"},
		{"long text", func(s *State) {
			s.lines[0].set(0, Glyph{Char: 'e', Width: 1, Combining: strings.Repeat("\u0301", 600)})
		}, "bytes of text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := newFuzzTerm(6, 3)
			term.write([]byte("ab"), false)
			if err := checkInvariants(term.State, sideTableBound(6)); err != nil {
				t.Fatalf("clean grid reported: %v", err)
			}
			tc.corrupt(term.State)
			err := checkInvariants(term.State, sideTableBound(6))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
	for _, bad := range []string{"x", "\x1b[12", "\x1b]10;rgb\x1b", "\x1b]1\x05\x07", "\x1bP1\x1b"} {
		if checkReplies([]byte(bad)) == nil {
			t.Errorf("malformed reply %q accepted", bad)
		}
	}
	if err := checkReplies([]byte("\x1b[0n\x1b]10;rgb:ffff/0000/0000\x07\x1b_Gi=1;OK\x1b\\\x1bP1$r0m\x1b\\")); err != nil {
		t.Errorf("well-formed replies rejected: %v", err)
	}
}
