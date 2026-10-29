package session

import (
	"strings"
	"testing"

	"bunk/internal/vt10x"
)

// historyCell is what a replay must reproduce of one cell.
type historyCell struct {
	ch        rune
	comb      string
	fg, bg    vt10x.Color
	ul        vt10x.Color
	mode      int16
	width     int8
	url       string
	wrapAfter bool
}

// historyRows returns every row of s with text, scrollback first, trimmed
// of trailing blanks and blank rows at the end.
func historyRows(s *Session) [][]historyCell {
	s.Lock()
	defer s.Unlock()
	_, rows := s.Term.Size()
	var out [][]historyCell
	var live vt10x.Row
	for i := 0; i < s.Scrollback.Count+rows; i++ {
		var r vt10x.Row
		if i < s.Scrollback.Count {
			r = s.Scrollback.Row(i)
		} else {
			s.Term.CopyPrimaryRow(i-s.Scrollback.Count, &live)
			r = live
		}
		var cells []historyCell
		for x := range r.TrimmedLen() {
			g := r.Glyph(x)
			c := historyCell{ch: g.Char, comb: g.Combining, fg: g.FG, bg: g.BG, mode: g.Mode & keptModes, width: g.Width}
			if c.ch == 0 {
				c.ch = ' '
			}
			if g.Mode&vt10x.AttrHasULColor != 0 {
				c.ul = g.UL
			}
			if g.Link != 0 {
				c.url = s.Term.Link(g.Link)
			}
			c.mode &^= vt10x.AttrWrap
			c.wrapAfter = g.Mode&vt10x.AttrWrap != 0
			cells = append(cells, c)
		}
		out = append(out, cells)
	}
	for len(out) > 0 && len(out[len(out)-1]) == 0 {
		out = out[:len(out)-1]
	}
	return out
}

func historyText(rows [][]historyCell) string {
	var b strings.Builder
	for _, r := range rows {
		for _, c := range r {
			if c.width >= 0 {
				b.WriteRune(c.ch)
				b.WriteString(c.comb)
			}
		}
		if len(r) == 0 || !r[len(r)-1].wrapAfter {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

const historySample = "plain line\r\n" +
	"\x1b[1;31mbold red\x1b[0m \x1b[3;4:3;58;2;1;2;3mcurly\x1b[0m \x1b[38;5;200;48;2;10;20;30mext\x1b[0m\r\n" +
	"\x1b[7;9;53mrev strike over\x1b[0m \x1b[2;92;104mdim bright\x1b[0m\r\n" +
	"\x1b]8;;https://example.com/a\x1b\\link\x1b]8;;\x1b\\ after\r\n" +
	"wide 世界 and e\u0301 and 👍🏽\r\n" +
	"a line long enough to wrap twice across a twenty column pane\r\n" +
	"\x1b[44m   \x1b[0m blue blanks\r\n"

func TestHistoryReplaysCells(t *testing.T) {
	src := newTestSession(20, 5)
	for range 3 {
		src.Feed([]byte(historySample))
	}
	src.Feed([]byte("$ prompt"))
	want := historyRows(src)

	dst := newTestSession(20, 5)
	dst.Feed(src.History(0))
	got := historyRows(dst)
	if len(got) != len(want) {
		t.Fatalf("replay has %d rows, want %d:\n%s\nwant:\n%s", len(got), len(want), historyText(got), historyText(want))
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("row %d: %d cells, want %d\n got %q\nwant %q", i, len(got[i]), len(want[i]),
				historyText(got[i:i+1]), historyText(want[i:i+1]))
		}
		for x := range want[i] {
			if got[i][x] != want[i][x] {
				t.Errorf("row %d cell %d: %+v, want %+v", i, x, got[i][x], want[i][x])
			}
		}
	}
}

// A replay at another width rewraps soft-wrapped lines, as reflow does.
func TestHistoryRewrapsAtNewWidth(t *testing.T) {
	src := newTestSession(20, 5)
	src.Feed([]byte(historySample))
	dst := newTestSession(33, 5)
	dst.Feed(src.History(0))
	if got, want := historyText(historyRows(dst)), historyText(historyRows(src)); got != want {
		t.Errorf("text after replay at 33 columns:\n%s\nwant:\n%s", got, want)
	}
}

// The primary screen is kept while a full-screen program shows the
// alternate one, and maxLines keeps the newest rows.
func TestHistoryPrimaryScreenAndCap(t *testing.T) {
	s := newTestSession(20, 5)
	for i := range 30 {
		s.Feed([]byte("line " + string(rune('A'+i%26)) + "\r\n"))
	}
	s.Feed([]byte("\x1b[?1049h\x1b[2Jfull screen"))
	h := string(s.History(4))
	if strings.Contains(h, "full screen") {
		t.Errorf("history holds the alternate screen: %q", h)
	}
	if want := "line A\r\nline B\r\nline C\r\nline D\r\n"; h != want {
		t.Errorf("History(4) = %q, want %q", h, want)
	}
	if h := s.History(0); len(h) == 0 || !strings.HasPrefix(string(h), "line A") {
		t.Errorf("History(0) starts %q", h[:min(len(h), 20)])
	}
}

func TestHistoryEmpty(t *testing.T) {
	if h := newTestSession(20, 5).History(0); len(h) != 0 {
		t.Errorf("History of an empty session = %q", h)
	}
}
