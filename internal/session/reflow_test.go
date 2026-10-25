package session

import (
	"bytes"
	"fmt"
	"image/color"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"bunk/internal/vt10x"
)

// reflowState is everything a width change can alter that a front end or
// later output can observe.
type reflowState struct {
	Cols, Rows           int
	Cursor               vt10x.Cursor
	Screen               [][]vt10x.Glyph
	Scrollback           [][]vt10x.Glyph
	MaxLines, Count      int
	ScrollOffset         int
	SelActive            bool
	SelAnchor, SelCursor SelPos
	Mode                 vt10x.ModeFlag
	Margins, SGR, Shape  string
	Links                []string
}

func captureReflowState(s *Session) reflowState {
	st := reflowState{
		Cursor: s.Term.Cursor(), MaxLines: s.Scrollback.MaxLines, Count: s.Scrollback.Count,
		ScrollOffset: s.ScrollOffset, SelActive: s.SelActive, SelAnchor: s.SelAnchor, SelCursor: s.SelCursor,
		Mode: s.Term.Mode(), Margins: s.Term.StatusString("r"), SGR: s.Term.StatusString("m"), Shape: s.Term.StatusString(" q"),
	}
	st.Cols, st.Rows = s.Term.Size()
	link := func(g vt10x.Glyph) {
		if g.Link != 0 {
			st.Links = append(st.Links, s.Term.Link(g.Link))
		}
	}
	for y := 0; y < st.Rows; y++ {
		row := CaptureRow(s.Term, y, st.Cols)
		for _, g := range row {
			link(g)
		}
		st.Screen = append(st.Screen, row)
	}
	for i := 0; i < s.Scrollback.Count; i++ {
		row := s.Scrollback.Get(i)
		for _, g := range row {
			link(g)
		}
		st.Scrollback = append(st.Scrollback, row)
	}
	return st
}

// diffReflowState describes the first difference between got and want.
func diffReflowState(got, want reflowState) string {
	if reflect.DeepEqual(got, want) {
		return ""
	}
	rows := func(name string, g, w [][]vt10x.Glyph) string {
		for i := 0; i < max(len(g), len(w)); i++ {
			if i >= len(g) || i >= len(w) {
				return fmt.Sprintf("%s: %d rows, want %d", name, len(g), len(w))
			}
			if !reflect.DeepEqual(g[i], w[i]) {
				for x := 0; x < max(len(g[i]), len(w[i])); x++ {
					if x >= len(g[i]) || x >= len(w[i]) {
						return fmt.Sprintf("%s row %d: %d cells, want %d", name, i, len(g[i]), len(w[i]))
					}
					if g[i][x] != w[i][x] {
						return fmt.Sprintf("%s row %d cell %d: %+v, want %+v", name, i, x, g[i][x], w[i][x])
					}
				}
			}
		}
		return ""
	}
	if d := rows("scrollback", got.Scrollback, want.Scrollback); d != "" {
		return d
	}
	if d := rows("screen", got.Screen, want.Screen); d != "" {
		return d
	}
	got.Screen, want.Screen, got.Scrollback, want.Scrollback = nil, nil, nil, nil
	return fmt.Sprintf("state\n got: %+v\nwant: %+v", got, want)
}

// reflowTwins holds two sessions fed the same output: got resizes with
// ResizeAndReflow, want with the copy of the old implementation.
type reflowTwins struct{ got, want *Session }

func newReflowTwins(cols, rows, scrollback int) *reflowTwins {
	tw := &reflowTwins{newTestSession(cols, rows), newTestSession(cols, rows)}
	for _, s := range []*Session{tw.got, tw.want} {
		s.ScrollbackLines, s.Scrollback = scrollback, Ring{MaxLines: scrollback}
	}
	return tw
}

func (tw *reflowTwins) feed(data []byte) {
	tw.got.Feed(data)
	tw.want.Feed(data)
}

// view sets the scroll offset and selection on both sessions.
func (tw *reflowTwins) view(offset int, selection bool) {
	for _, s := range []*Session{tw.got, tw.want} {
		s.ScrollOffset = min(offset, s.Scrollback.Count)
		s.SelActive, s.SelAnchor, s.SelCursor = selection, SelPos{1, 2}, SelPos{3, 4}
	}
}

// resize resizes both sessions and reports the first difference.
func (tw *reflowTwins) resize(cols, rows int) string {
	got := tw.got.ResizeAndReflow(cols, rows)
	want := tw.want.oracleResizeAndReflow(cols, rows)
	if got != want {
		return fmt.Sprintf("ResizeAndReflow(%d, %d) = %v, want %v", cols, rows, got, want)
	}
	return tw.diff()
}

func (tw *reflowTwins) diff() string {
	return diffReflowState(captureReflowState(tw.got), captureReflowState(tw.want))
}

// check resizes through sizes, comparing after each step and again after
// more output, which shows differences in state the capture cannot see.
func (tw *reflowTwins) check(t *testing.T, sizes [][2]int) {
	t.Helper()
	for i, size := range sizes {
		if d := tw.resize(size[0], size[1]); d != "" {
			t.Fatalf("step %d, %dx%d: %s", i, size[0], size[1], d)
		}
	}
	tw.feed([]byte("\x1b[0mafter\r\n\t1\t22\t333 wrap " + strings.Repeat("~", 90) + "\r\nlast\x1b[1;31m!"))
	if d := tw.diff(); d != "" {
		t.Fatalf("after more output: %s", d)
	}
}

// widths returns sizes of the given widths, all rows high.
func widths(rows int, cols ...int) [][2]int {
	sizes := make([][2]int, len(cols))
	for i, c := range cols {
		sizes[i] = [2]int{c, rows}
	}
	return sizes
}

// TestReflowMatchesOracleContent reflows the benchmark's content through
// narrowing, widening, single-column drags and height changes.
func TestReflowMatchesOracleContent(t *testing.T) {
	sequences := map[string][][2]int{
		"narrow-widen": widths(12, 120, 200),
		"narrow":       widths(12, 80),
		"drag":         widths(12, 199, 198, 197, 196, 195, 194, 193),
		"extremes":     widths(12, 37, 1, 2, 3, 250, 7),
		"heights":      {{150, 20}, {150, 5}, {90, 30}, {201, 3}, {200, 12}},
	}
	for _, kind := range reflowKinds {
		for name, sizes := range sequences {
			t.Run(kind+"/"+name, func(t *testing.T) {
				t.Parallel()
				tw := newReflowTwins(200, 12, 200)
				var buf bytes.Buffer
				for i := 0; i < 120; i++ {
					reflowLine(&buf, kind, i)
				}
				buf.WriteString("$ prompt")
				tw.feed(buf.Bytes())
				tw.view(tw.got.Scrollback.Count/2, true)
				tw.check(t, sizes)
			})
		}
	}
}

// TestReflowMatchesOracleCases covers cursor, anchor, limit and storage
// cases one at a time.
func TestReflowMatchesOracleCases(t *testing.T) {
	long := strings.Repeat("0123456789", 9)
	cases := []struct {
		name             string
		cols, rows, sb   int
		input            string
		offset           int
		selection        bool
		sizes            [][2]int
		afterFirstResize string
	}{
		{name: "cursor_mid_line", cols: 20, rows: 5, sb: 50, input: long + "\r\n" + long + "\x1b[2;7H", sizes: widths(5, 7, 33, 3, 20)},
		{name: "cursor_on_continuation", cols: 10, rows: 4, sb: 20, input: "ab界cd界\x1b[1;4H", sizes: widths(4, 3, 5, 1, 2, 10)},
		{name: "cursor_on_wide_lead", cols: 10, rows: 4, sb: 20, input: "ab界cd界\x1b[1;3H", sizes: widths(4, 3, 4, 1, 10)},
		{name: "pending_wrap", cols: 10, rows: 4, sb: 20, input: "abcdefghij", sizes: widths(4, 5, 10, 3, 20)},
		{name: "pending_wide_wrap", cols: 6, rows: 4, sb: 20, input: "abcd界", sizes: widths(4, 3, 5, 6, 9)},
		{name: "wide_padding", cols: 5, rows: 4, sb: 20, input: "abcd界ef界ghi界界界", sizes: widths(4, 4, 6, 3, 2, 1, 5)},
		{name: "cursor_top_content_below", cols: 20, rows: 4, sb: 20, input: long + "\r\n" + long + "\r\nthird\r\nfourth\x1b[H", sizes: widths(4, 6, 3, 40, 20)},
		{name: "cursor_padding", cols: 5, rows: 3, sb: 20, input: "abcd界\x1b[1;5H", sizes: widths(3, 4, 7, 5)},
		{name: "scrollback_zero", cols: 20, rows: 3, sb: 0, input: long + "\r\n" + long + "\r\nend", sizes: widths(3, 7, 40, 3)},
		{name: "scrollback_tiny", cols: 20, rows: 3, sb: 3, input: long + "\r\n" + long + "\r\n" + long, offset: 2, sizes: widths(3, 4, 9, 20, 1)},
		{name: "scrollback_negative", cols: 20, rows: 4, sb: -2, input: long + "\r\n" + long + "\r\nend", sizes: widths(4, 6, 40, 15)},
		{name: "anchor_far_up", cols: 30, rows: 5, sb: 100, input: strings.Repeat(long+"\r\nshort\r\n", 20), offset: 999, selection: true, sizes: widths(5, 11, 30, 70)},
		{name: "anchor_on_live_rows", cols: 30, rows: 8, sb: 100, input: strings.Repeat("line\r\n", 12) + long, offset: 1, sizes: widths(8, 11, 30, 4)},
		{name: "styled_blanks", cols: 12, rows: 4, sb: 20, input: "\x1b[41mred\x1b[K\r\n\x1b[0mx\x1b[44m   \x1b[0m\r\n\x1b[4:3;58;2;1;2;3m   \x1b[0m\x1b[4;1H\x1b[48;2;10;20;30m  \x1b[0m\x1b[H", sizes: widths(4, 5, 2, 12, 30)},
		{name: "erased_text", cols: 30, rows: 5, sb: 20, input: "discard me\x1b[H\x1b[2Jkeep me", sizes: widths(5, 4, 40, 30)},
		{name: "graphemes_one_column", cols: 8, rows: 6, sb: 20, input: "a界b👩‍👩‍👧é🇳🇱❤️x", sizes: widths(6, 1, 3, 8, 2, 1, 9)},
		{name: "links", cols: 16, rows: 4, sb: 20, input: "\x1b]8;;https://a.example\x1b\\link a\x1b]8;;\x1b\\ plain \x1b]8;;https://b.example\x1b\\界b\x1b]8;;\x1b\\" + long, sizes: widths(4, 5, 9, 16, 40)},
		{name: "image", cols: 20, rows: 6, sb: 20, input: "\x1b_Ga=T,f=24,s=2,v=1,c=12,r=2;/wAAAP8A\x1b\\text\r\n\r\n\r\n", sizes: widths(6, 7, 3, 20, 1, 30)},
		{name: "alt_screen", cols: 20, rows: 4, sb: 20, input: long + "\x1b[?1049h\x1b[41malt", sizes: widths(4, 9, 30), afterFirstResize: "\x1b[?1049l"},
		{name: "stale_width_scrollback", cols: 30, rows: 3, sb: 20, input: long + "\r\n" + long + "\x1b[?1049h", sizes: widths(3, 12, 17, 5), afterFirstResize: "\x1b[?1049l"},
		{name: "no_wrap_mode", cols: 10, rows: 4, sb: 20, input: "\x1b[?7l" + long + "\r\n" + long, sizes: widths(4, 3, 30)},
		{name: "scroll_region", cols: 20, rows: 6, sb: 20, input: long + "\x1b[2;4r\x1b[3;1Hregion\x1b[2S", sizes: widths(6, 9, 20, 30)},
		{name: "tabs", cols: 30, rows: 4, sb: 20, input: "a\tb\tc\td\te\x1b[3g\tf", sizes: widths(4, 7, 60, 30)},
		{name: "grapheme_mode_off", cols: 10, rows: 4, sb: 20, input: "\x1b[?2027l👩‍👩‍👧é🇳🇱x", sizes: widths(4, 3, 10, 1)},
		{name: "rows_and_cols", cols: 20, rows: 6, sb: 30, input: strings.Repeat(long+"\r\n", 8) + "tail", offset: 3, sizes: [][2]int{{9, 3}, {9, 10}, {40, 2}, {20, 6}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tw := newReflowTwins(tc.cols, tc.rows, tc.sb)
			tw.feed([]byte(tc.input))
			tw.view(tc.offset, tc.selection)
			if tc.afterFirstResize != "" {
				tw.check(t, tc.sizes[:1])
				tw.feed([]byte(tc.afterFirstResize))
				tc.sizes = tc.sizes[1:]
			}
			tw.check(t, tc.sizes)
		})
	}
}

// TestReflowMatchesOracleStoredRows reflows scrollback rows vt10x does not
// write itself, which reflow must still treat as before.
func TestReflowMatchesOracleStoredRows(t *testing.T) {
	g := func(ch rune, width int8, mode int16) vt10x.Glyph {
		return vt10x.Glyph{Char: ch, Width: width, Mode: mode, FG: vt10x.DefaultFG, BG: vt10x.DefaultBG, UL: vt10x.DefaultUL}
	}
	a, wide, cont, pad := g('a', 1, 0), g('界', 2, 0), g(0, -1, 0), g(' ', -2, 0)
	colouredEmpty, underlinedBlank := g(0, 0, 0), g(' ', 1, 0)
	colouredEmpty.BG = 3
	underlinedBlank.UL = 7
	oddCont := cont
	oddCont.FG = 5
	rows := map[string][]vt10x.Glyph{
		"wrap_bit_mid_row":      {a, g('b', 1, vt10x.AttrWrap), a},
		"trailing_empty_colour": {a, a, colouredEmpty, colouredEmpty},
		"trailing_ul_blank":     {a, underlinedBlank},
		"orphan_lead":           {a, wide, a},
		"orphan_lead_at_end":    {a, a, wide},
		"orphan_continuation":   {a, cont, a},
		"odd_continuation":      {wide, oddCont, a},
		"padding_mid_row":       {a, pad, a},
		"padding_at_end":        {a, a, pad},
		"width_zero":            {g('a', 0, 0), g('b', 0, 0)},
		"width_three":           {g('a', 3, 0), a, a},
		"empty":                 {},
		"blank_only":            {g(' ', 1, 0)},
	}
	for name, row := range rows {
		t.Run(name, func(t *testing.T) {
			tw := newReflowTwins(8, 3, 20)
			for _, s := range []*Session{tw.got, tw.want} {
				s.Scrollback.Push(makeGlyphRow('x'))
				s.Scrollback.Push(row)
				s.Scrollback.Push(row)
			}
			tw.feed([]byte("prompt"))
			tw.check(t, widths(3, 9, 2, 1, 5, 8, 40))
		})
	}
}

// reflowOps turns fuzz input into terminal output and resizes, so the fuzz
// target explores content, cursor positions and widths together.
func reflowOps(tw *reflowTwins, data []byte, fail func(string)) {
	next := func() byte {
		if len(data) == 0 {
			return 0
		}
		b := data[0]
		data = data[1:]
		return b
	}
	texts := []string{"界", "😀", "👩‍👩‍👧", "é", "🇳🇱", "❤️", "\u0301", "\u200d", " ", "  ", "\t", "\b", "x", "AB"}
	sgrs := []string{"\x1b[0m", "\x1b[1;31m", "\x1b[41m", "\x1b[7m", "\x1b[4:3;58;2;9;8;7m", "\x1b[38;2;1;2;3;48;5;200m", "\x1b[9;53m", "\x1b[2;8m"}
	controls := []string{"\r\n", "\n", "\r", "\x1b[K", "\x1b[1K", "\x1b[2K", "\x1b[J", "\x1b[2J", "\x1b[3J", "\x1b[S", "\x1b[T", "\x1b[L", "\x1b[M", "\x1b[P", "\x1b[@", "\x1b[X", "\x1b[?7l", "\x1b[?7h", "\x1b[?2027l", "\x1b[?2027h", "\x1b[?1049h", "\x1b[?1049l", "\x1b[4h", "\x1b[4l", "\x1b7", "\x1b8", "\x1bD", "\x1bM", "\x1b[r"}
	for len(data) > 0 {
		op := next()
		var out string
		switch op % 13 {
		case 0, 1, 2:
			n := int(next()%40) + 1
			out = strings.Repeat(string(rune('a'+op%26)), n)
		case 3:
			out = texts[int(next())%len(texts)]
		case 4:
			out = sgrs[int(next())%len(sgrs)]
		case 5:
			out = controls[int(next())%len(controls)]
		case 6:
			if b := next(); b%2 == 0 {
				out = fmt.Sprintf("\x1b]8;;https://example.org/%d\x1b\\", b%5)
			} else {
				out = "\x1b]8;;\x1b\\"
			}
		case 7:
			out = fmt.Sprintf("\x1b[%d;%dH", next()%14+1, next()%44+1)
		case 8:
			out = fmt.Sprintf("\x1b_Ga=T,f=24,s=2,v=1,c=%d,r=%d;/wAAAP8A\x1b\\", next()%6+1, next()%3+1)
		case 9:
			out = fmt.Sprintf("\x1b[%d;%dr", next()%6+1, next()%14+1)
		case 10:
			offset := int(next())
			for _, s := range []*Session{tw.got, tw.want} {
				s.ScrollOffset = min(offset, s.Scrollback.Count)
				s.SelActive = true
			}
			continue
		case 11:
			row := make([]vt10x.Glyph, next()%14)
			for x := range row {
				row[x] = syntheticGlyph(next(), next())
			}
			tw.got.Scrollback.Push(row)
			tw.want.Scrollback.Push(row)
			continue
		default:
			cols, rows := int(next()%44)+1, int(next()%14)+1
			if d := tw.resize(cols, rows); d != "" {
				fail(fmt.Sprintf("resize to %dx%d: %s", cols, rows, d))
				return
			}
			continue
		}
		tw.feed([]byte(out))
	}
}

var syntheticImage = &vt10x.ImageCell{Top: color.NRGBA{R: 255, A: 255}}

// syntheticGlyph returns a glyph chosen by a and b from cells vt10x
// rarely or never stores itself: orphan wide cells, padding mid-row, wrap
// bits mid-row, widths up to 3, empty cells with colours or side-table data.
func syntheticGlyph(a, b byte) vt10x.Glyph {
	g := vt10x.Glyph{FG: vt10x.DefaultFG, BG: vt10x.DefaultBG, UL: vt10x.DefaultUL}
	g.Char = []rune{'a', ' ', 0, '界', 'z'}[a%5]
	g.Width = int8(a/5%6) - 2
	if a/30%2 == 1 {
		g.BG = 4
	}
	g.Mode = []int16{0, 0, vt10x.AttrWrap, vt10x.AttrBold, vt10x.AttrWrap | vt10x.AttrUnderline}[b%5]
	switch b / 5 % 6 {
	case 0:
		g.Link = 1
	case 1:
		g.Image = syntheticImage
	case 2:
		g.Combining = "\u0301"
	case 3:
		g.UL = 9
	}
	if b/30%2 == 1 {
		g.FG = 2
	}
	return g
}

// FuzzReflowEquivalence feeds random output and resizes to two sessions
// and requires the new reflow to match the old one exactly.
func FuzzReflowEquivalence(f *testing.F) {
	f.Add([]byte{20, 5, 30, 0, 30, 3, 0, 11, 4, 11, 11, 30, 4})
	f.Add([]byte{3, 0, 3, 1, 3, 2, 0, 40, 11, 2, 3, 11, 9, 3, 11, 1, 1})
	f.Add([]byte{30, 4, 10, 1, 11, 0, 5, 7, 2, 2, 8, 3, 1, 11, 7, 2, 11, 40, 9})
	f.Add([]byte{40, 3, 50, 6, 2, 0, 39, 5, 0, 0, 39, 10, 5, 11, 12, 5, 11, 43, 13})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 3 || len(data) > 600 {
			return
		}
		tw := newReflowTwins(int(data[0]%44)+1, int(data[1]%14)+1, int(data[2]%60))
		reflowOps(tw, data[3:], func(d string) { t.Fatal(d) })
		if d := tw.diff(); d != "" {
			t.Fatal(d)
		}
	})
}

// TestReflowMatchesOracleRandom runs the fuzz target's generator on fixed
// pseudo-random inputs, so every test run explores beyond the seeds.
func TestReflowMatchesOracleRandom(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	n := 400
	if testing.Short() {
		n = 50
	}
	for i := 0; i < n; i++ {
		data := make([]byte, 3+rng.IntN(300))
		for j := range data {
			data[j] = byte(rng.Uint32())
		}
		tw := newReflowTwins(int(data[0]%44)+1, int(data[1]%14)+1, int(data[2]%60))
		reflowOps(tw, data[3:], func(d string) { t.Fatalf("input %d %x: %s", i, data, d) })
	}
}

// logicalLines returns the session's primary-screen lines as reflow reads
// them: soft-wrapped rows joined, continuation and padding cells left out,
// the wrap bit cleared and trailing cells without content dropped.
func logicalLines(s *Session) [][]vt10x.Glyph {
	cols, rows := s.Term.Size()
	var lines [][]vt10x.Glyph
	var line []vt10x.Glyph
	total := s.Scrollback.Count + findContentRows(s.Term, cols, rows)
	for r := 0; r < total; r++ {
		row := s.Scrollback.Get(r)
		if r >= s.Scrollback.Count {
			row = CaptureRow(s.Term, r-s.Scrollback.Count, cols)
		}
		for _, g := range row {
			if g.Width >= 0 {
				g.Mode &^= vt10x.AttrWrap
				line = append(line, g)
			}
		}
		if len(row) == 0 || row[len(row)-1].Mode&vt10x.AttrWrap == 0 || r == total-1 {
			lines = append(lines, line[:oracleContentEnd(line)])
			line = nil
		}
	}
	return lines
}

// TestReflowRoundTripRestoresLogicalLines checks that reflowing to any
// width of two or more columns and back keeps every logical line, when the
// scrollback has room for all rows. The first round trip may normalise
// cells (dropping trailing blanks), so comparisons start after it.
func TestReflowRoundTripRestoresLogicalLines(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for _, kind := range append(append([]string(nil), reflowKinds...), "random") {
		// Generated before the parallel subtest starts: rng is shared.
		var buf bytes.Buffer
		for i := 0; i < 30; i++ {
			if kind == "random" {
				randomLine(&buf, rng)
			} else {
				reflowLine(&buf, kind, i)
			}
		}
		buf.WriteString("$ tail")
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s := newTestSession(90, 10)
			s.ScrollbackLines, s.Scrollback = 20000, Ring{MaxLines: 20000}
			s.Feed(buf.Bytes())
			s.ResizeAndReflow(37, 10)
			s.ResizeAndReflow(90, 10)
			want := logicalLines(s)
			for _, cols := range []int{2, 3, 7, 41, 89, 91, 200, 333} {
				s.ResizeAndReflow(cols, 10)
				if got := logicalLines(s); !reflect.DeepEqual(got, want) {
					t.Fatalf("at width %d: logical lines differ", cols)
				}
				s.ResizeAndReflow(90, 10)
				if got := logicalLines(s); !reflect.DeepEqual(got, want) {
					t.Fatalf("back from width %d: logical lines differ", cols)
				}
			}
		})
	}
}

// randomLine appends a line of random text, styles, links and wide
// characters, then CR LF.
func randomLine(buf *bytes.Buffer, rng *rand.Rand) {
	parts := []string{"word ", "界", "😀", "é", "\x1b[1;32m", "\x1b[0m", "\x1b[44m", "\x1b]8;;https://x.example\x1b\\", "\x1b]8;;\x1b\\", "   ", "\x1b[4:3;58;2;1;2;3m"}
	for n := rng.IntN(60); n > 0; n-- {
		buf.WriteString(parts[rng.IntN(len(parts))])
	}
	buf.WriteString("\x1b[0m\x1b]8;;\x1b\\\r\n")
}

// TestReflowEndMatchesContentRule compares the compact-cell content rule
// with the glyph rule findContentRows and the old reflow use.
func TestReflowEndMatchesContentRule(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	img := &vt10x.ImageCell{}
	for i := 0; i < 5000; i++ {
		glyphs := make([]vt10x.Glyph, rng.IntN(6))
		for x := range glyphs {
			g := vt10x.Glyph{FG: vt10x.DefaultFG, BG: vt10x.DefaultBG, UL: vt10x.DefaultUL, Width: int8(rng.IntN(5) - 2)}
			g.Char = []rune{0, ' ', 'a'}[rng.IntN(3)]
			if rng.IntN(4) == 0 {
				g.BG = 1
			}
			if rng.IntN(4) == 0 {
				g.Mode = []int16{vt10x.AttrWrap, vt10x.AttrBold, vt10x.AttrWrap | vt10x.AttrBold}[rng.IntN(3)]
			}
			switch rng.IntN(8) {
			case 0:
				g.Link = 1
			case 1:
				g.Image = img
			case 2:
				g.Combining = "\u0301"
			case 3:
				g.UL = 5
			}
			glyphs[x] = g
			if reflowCellHasContent(g) != oracleCellHasContent(g) {
				t.Fatalf("content rule differs for %+v", g)
			}
		}
		var row vt10x.Row
		row.SetGlyphs(glyphs)
		if got, want := row.ReflowEnd(), oracleContentEnd(glyphs); got != want {
			t.Fatalf("ReflowEnd(%+v) = %d, want %d", glyphs, got, want)
		}
	}
}

// rowStorage returns the address of row's first stored cell, or 0.
func rowStorage(row vt10x.Row) uintptr {
	return reflect.ValueOf(&row).Elem().FieldByName("cells").Pointer()
}

// TestReflowMovesUnchangedRows checks that rows which fit the new width
// keep their storage, so a resize does not copy them, and that the number
// of allocations does not grow with the number of such rows.
func TestReflowMovesUnchangedRows(t *testing.T) {
	s := newReflowSession("shell", 120, 10, 500)
	s.ResizeAndReflow(110, 10) // learns which rows fit
	before := make([]uintptr, s.Scrollback.Count)
	for i := range before {
		before[i] = rowStorage(s.Scrollback.Row(i))
	}
	s.ResizeAndReflow(100, 10)
	if s.Scrollback.Count != len(before) {
		t.Fatalf("scrollback holds %d rows, want %d", s.Scrollback.Count, len(before))
	}
	for i, p := range before {
		if got := rowStorage(s.Scrollback.Row(i)); got != p {
			t.Fatalf("row %d was copied", i)
		}
	}

	allocs := func(lines int) float64 {
		s := newReflowSession("shell", 120, 10, lines)
		s.ResizeAndReflow(110, 10)
		i := 0
		return testing.AllocsPerRun(10, func() {
			s.ResizeAndReflow([]int{100, 110}[i%2], 10)
			i++
		})
	}
	small, large := allocs(500), allocs(5000)
	if large > small {
		t.Fatalf("reflow made %.0f allocations with 5000 rows, %.0f with 500", large, small)
	}
}
