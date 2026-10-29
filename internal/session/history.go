package session

// history.go - the text of the primary screen and its scrollback as a
// stream of escape sequences, which a new session replays (Feed) to show
// what an earlier one held.

import (
	"bytes"
	"strconv"
	"strings"

	"bunk/internal/vt10x"
)

// History returns the scrollback and the live primary screen, oldest line
// first, as text with SGR colours and attributes and OSC 8 links. Each
// logical line ends in CRLF; a soft-wrapped row runs on into the next, so
// a replay rewraps it at its own width. Trailing blanks and blank lines
// after the last text are left out, and so are images, which replay as
// their cells' characters. At most maxLines rows are kept, the newest.
// It takes the session lock.
func (s *Session) History(maxLines int) []byte {
	s.Lock()
	defer s.Unlock()
	_, rows := s.Term.Size()
	sb := s.Scrollback.Count
	total := sb + rows
	var live vt10x.Row
	row := func(i int) vt10x.Row {
		if i < sb {
			return s.Scrollback.Row(i)
		}
		s.Term.CopyPrimaryRow(i-sb, &live)
		return live
	}
	// The last row with text: the rows below the prompt are blank.
	last := total - 1
	for ; last >= 0; last-- {
		if row(last).TrimmedLen() > 0 {
			break
		}
	}
	first := 0
	if maxLines > 0 && last+1 > maxLines {
		first = last + 1 - maxLines
	}
	var w historyWriter
	for i := first; i <= last; i++ {
		w.row(row(i), s.Term.Link, i == last)
	}
	return w.buf.Bytes()
}

// historyWriter encodes rows, emitting SGR and OSC 8 only where a cell's
// style or link differs from the cell before it.
type historyWriter struct {
	buf   bytes.Buffer
	style vt10x.Glyph // the style in effect; its Char is unused
	link  uint16
	sgr   []byte
}

// plain is the style of a reset terminal.
var plain = vt10x.Glyph{FG: vt10x.DefaultFG, BG: vt10x.DefaultBG, UL: vt10x.DefaultUL}

func (w *historyWriter) row(r vt10x.Row, url func(uint16) string, last bool) {
	if w.buf.Len() == 0 {
		w.style = plain
	}
	n := r.TrimmedLen()
	wrapped := n > 0 && rowWrapped(r, n-1)
	for x := range n {
		g := r.Glyph(x)
		if g.Width < 0 {
			continue // a wide character's continuation or wrap padding
		}
		if g.Link != w.link {
			w.setLink(g.Link, url)
		}
		w.setStyle(g)
		ch := g.Char
		if ch == 0 || ch < ' ' || ch == 0x7f {
			ch = ' '
		}
		w.buf.WriteRune(ch)
		w.buf.WriteString(g.Combining)
	}
	if wrapped && !last {
		return
	}
	// Close the line: a style left on would colour the next line's start
	// and the shell's prompt after the replay.
	if w.link != 0 {
		w.setLink(0, url)
	}
	if w.style != plain {
		w.buf.WriteString("\x1b[0m")
		w.style = plain
	}
	w.buf.WriteString("\r\n")
}

func (w *historyWriter) setLink(id uint16, url func(uint16) string) {
	w.link = id
	target := ""
	if id != 0 {
		target = strings.Map(func(r rune) rune {
			if r < ' ' || r == 0x7f {
				return -1
			}
			return r
		}, url(id))
	}
	w.buf.WriteString("\x1b]8;;" + target + "\x1b\\")
}

// styleModes are the attribute bits History keeps and their SGR numbers;
// the underline style comes from the style bits.
var styleModes = []struct {
	bit int16
	sgr string
}{
	{vt10x.AttrBold, "1"}, {vt10x.AttrDim, "2"}, {vt10x.AttrItalic, "3"}, {vt10x.AttrBlink, "5"},
	{vt10x.AttrReverse, "7"}, {vt10x.AttrInvisible, "8"}, {vt10x.AttrStrikethrough, "9"}, {vt10x.AttrOverline, "53"},
}

const keptModes = vt10x.AttrBold | vt10x.AttrDim | vt10x.AttrItalic | vt10x.AttrBlink | vt10x.AttrReverse |
	vt10x.AttrInvisible | vt10x.AttrStrikethrough | vt10x.AttrOverline | vt10x.AttrUnderline |
	vt10x.AttrUnderlineStyleMask | vt10x.AttrHasULColor

// setStyle switches to g's colours and attributes, from a reset.
func (w *historyWriter) setStyle(g vt10x.Glyph) {
	want := vt10x.Glyph{FG: g.FG, BG: g.BG, UL: vt10x.DefaultUL, Mode: g.Mode & keptModes}
	if want.Mode&vt10x.AttrHasULColor != 0 {
		want.UL = g.UL
	}
	if want == w.style {
		return
	}
	w.style = want
	b := append(w.sgr[:0], "\x1b[0"...)
	for _, m := range styleModes {
		if want.Mode&m.bit != 0 {
			b = append(b, ';')
			b = append(b, m.sgr...)
		}
	}
	if want.Mode&vt10x.AttrUnderline != 0 {
		// Style 0 is solid (SGR 4); the others are 4:2 double to 4:5 dashed.
		b = append(b, ";4"...)
		if style := (want.Mode & vt10x.AttrUnderlineStyleMask) / vt10x.AttrUnderlineStyleBit0; style != 0 {
			b = strconv.AppendInt(append(b, ':'), int64(style)+1, 10)
		}
	}
	b = appendColor(b, want.FG, "3", "9", "38")
	b = appendColor(b, want.BG, "4", "10", "48")
	if want.Mode&vt10x.AttrHasULColor != 0 {
		b = appendColor(b, want.UL, "", "", "58")
	}
	b = append(b, 'm')
	w.sgr = b
	w.buf.Write(b)
}

// appendColor appends c as an SGR parameter: base+N for the first eight
// colours, bright+N for the next eight (both "" for the underline colour,
// which has only the extended form), else ext;5;N or ext;2;R;G;B. A
// default colour appends nothing.
func appendColor(b []byte, c vt10x.Color, base, bright, ext string) []byte {
	switch {
	case c < 8 && base != "":
		return strconv.AppendInt(append(append(b, ';'), base...), int64(c), 10)
	case c < 16 && bright != "":
		return strconv.AppendInt(append(append(b, ';'), bright...), int64(c-8), 10)
	case c < 256:
		return strconv.AppendInt(append(append(b, ';'), ext+";5;"...), int64(c), 10)
	case c < vt10x.DefaultFG:
		b = append(append(b, ';'), ext+";2;"...)
		b = strconv.AppendInt(b, int64(c>>16&0xff), 10)
		b = strconv.AppendInt(append(b, ';'), int64(c>>8&0xff), 10)
		return strconv.AppendInt(append(b, ';'), int64(c&0xff), 10)
	}
	return b
}
