package vt10x

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"
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
// terminals can be compared after the same input.
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
	).(*terminal)
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
		Links: s.links, Colors: s.colorOverride, ColorGen: s.colorGen,
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
	for _, screen := range []struct {
		name string
		rows []*Row
	}{{"primary", s.lines}, {"alternate", s.altLines}} {
		for y, r := range screen.rows {
			if err := checkRow(r, s.cols, len(s.links), maxSide); err != nil {
				return fmt.Errorf("%s row %d: %w", screen.name, y, err)
			}
		}
	}
	return nil
}

// checkRow checks the used/tail invariant from row.go, the side table and
// the cell widths of one row.
func checkRow(r *Row, cols, links, maxSide int) error {
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
		if int(g.Link) > links {
			return fmt.Errorf("cell %d links to %d of %d URLs", x, g.Link, links)
		}
		switch g.Width {
		case 0, 1:
		case 2:
			if x+1 == cols || r.cells[x+1].width() != -1 {
				return fmt.Errorf("wide lead at %d without a continuation", x)
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
			switch {
			case i < len(replies) && replies[i] == 0x07 && replies[start+1] == ']':
				i++
			case i+1 < len(replies) && replies[i] == 0x1b && replies[i+1] == '\\':
				i += 2
			default:
				return fmt.Errorf("string reply %q holds a control byte or no terminator", replies[start:min(len(replies), i+1)])
			}
		default:
			return fmt.Errorf("unknown reply introducer %q", replies[start:start+2])
		}
	}
	return nil
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
			t.Fatalf("%dx%d: ASCII fast path differs from rune parser in %s", c, r, d)
		}
		replies := whole.replies.Bytes()
		if err := checkReplies(replies); err != nil {
			t.Fatalf("malformed reply: %v", err)
		}
		if len(replies) > maxReplyBytes(len(data)) {
			t.Fatalf("%d bytes of replies to %d bytes of output", len(replies), len(data))
		}
	})
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
		{"padding before text", func(s *State) { s.lines[0].set(1, Glyph{Char: ' ', Width: -2}) }, "wrap padding"},
		{"padding without wrap", func(s *State) { s.lines[0].set(s.cols-1, Glyph{Char: ' ', Width: -2}) }, "does not wrap"},
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
