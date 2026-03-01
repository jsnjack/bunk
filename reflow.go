// reflow.go – terminal content reflow on pane resize and scrollback rebuild.
//
// On any pane resize (split or close), the full terminal history — scrollback
// ring plus the current visible vt10x grid — is collected, the scrollback is
// rebuilt, and the portion that fits in the new terminal is re-injected so
// content reflows naturally at the new column width.
//
// The same rawBuf replay is also used when the user first enters scrollback
// mode (Shift+PgUp).  detectShift can only record rows that were visible
// *before* each PTY chunk arrived; a single large TCP burst (common over SSH)
// can scroll through many screenfuls in one read, silently dropping every
// intermediate line.  Replaying rawBuf into a tall scratch terminal captures
// all of them in one shot.
//
// The key insight: each captured row is a []vt10x.Glyph whose length is the
// column count at capture time.  When a row is wider than the new terminal,
// vt10x auto-wraps it; when narrower, it fits as a short line.  No additional
// bookkeeping is needed.
//
// Limitations:
//   - Rows in the scrollback and visible grid represent physical (already
//     wrapped) lines, not logical lines.  A line that was split across two
//     rows by auto-wrap at the OLD width will not be re-joined before being
//     re-wrapped at the new width.  This is the same behaviour as tmux/screen.
//   - The scrollback is rebuilt from the combined history, so the user's
//     scroll position is reset to the live view after a resize.
package main

import (
	"bytes"
	"strconv"

	"github.com/hinshun/vt10x"
)

// stripAltScreen removes alt-screen content from buf, keeping pre-entry and
// post-exit bytes.  This prevents the scratch terminal from allocating a huge
// alt-screen buffer during replay, and ensures pre-vim shell history is
// preserved across resize (the previous approach of discarding everything
// before the last exit sequence lost all pre-vim content).
func stripAltScreen(buf []byte) []byte {
	entrySeqs := [][]byte{
		[]byte("\x1b[?1049h"),
		[]byte("\x1b[?1047h"),
		[]byte("\x1b[?47h"),
	}
	exitSeqs := [][]byte{
		[]byte("\x1b[?1049l"),
		[]byte("\x1b[?1047l"),
		[]byte("\x1b[?47l"),
	}

	var result []byte
	pos := 0
	found := false
	for pos < len(buf) {
		entryPos := -1
		for _, seq := range entrySeqs {
			if p := bytes.Index(buf[pos:], seq); p >= 0 {
				absPos := pos + p
				if entryPos < 0 || absPos < entryPos {
					entryPos = absPos
				}
			}
		}
		if entryPos < 0 {
			if found {
				result = append(result, buf[pos:]...)
			}
			break
		}
		found = true
		result = append(result, buf[pos:entryPos]...)

		exitEnd := -1
		for _, seq := range exitSeqs {
			if p := bytes.Index(buf[entryPos:], seq); p >= 0 {
				absEnd := entryPos + p + len(seq)
				if exitEnd < 0 || absEnd < exitEnd {
					exitEnd = absEnd
				}
			}
		}
		if exitEnd < 0 {
			break // entry with no exit — discard from entry onward
		}
		pos = exitEnd
	}
	if !found {
		return buf
	}
	return result
}

// rowContentEnd returns the index one past the last non-blank cell in row.
// Only cells with an actual visible character (non-NUL, non-space) are
// considered content.  Trailing spaces are ignored regardless of their
// background colour — shells commonly use \x1b[K (erase-to-EOL) to fill
// the rest of a prompt line with a coloured background; we must not replay
// those filled cells or they tint the entire pane.
func rowContentEnd(row []vt10x.Glyph) int {
	end := len(row)
	for end > 0 {
		g := row[end-1]
		if g.Char != 0 && g.Char != ' ' {
			break
		}
		end--
	}
	return end
}

// rowVisualHeight returns how many terminal rows a Glyph row will occupy when
// rendered in a terminal that is cols columns wide.
func rowVisualHeight(row []vt10x.Glyph, cols int) int {
	end := rowContentEnd(row)
	if end == 0 || cols <= 0 {
		return 1
	}
	return (end + cols - 1) / cols
}

// reflowInject writes rows into term (already resized) as ANSI-coded text.
// Each row ends with \r\n except the last content row; long rows auto-wrap at
// the new terminal width.  Trailing blank rows are skipped so the cursor lands
// right after the last visible content, not at the bottom of the terminal.
// Must be called with p.mu held.
func reflowInject(term vt10x.Terminal, rows [][]vt10x.Glyph) {
	if len(rows) == 0 {
		return
	}

	// Find the last row that has any visible content so we don't emit
	// trailing \r\n sequences that would push the cursor to the bottom.
	lastContent := -1
	for r := len(rows) - 1; r >= 0; r-- {
		if rowContentEnd(rows[r]) > 0 {
			lastContent = r
			break
		}
	}
	if lastContent < 0 {
		return // nothing to inject
	}

	var buf bytes.Buffer
	buf.WriteString("\x1b[0m\x1b[2J\x1b[H") // reset attrs, clear, cursor home

	var prevFG, prevBG vt10x.Color = vt10x.DefaultFG, vt10x.DefaultBG
	var prevMode int16

	for r := 0; r <= lastContent; r++ {
		row := rows[r]
		end := rowContentEnd(row)
		for c := 0; c < end; c++ {
			g := row[c]
			if g.FG != prevFG || g.BG != prevBG || g.Mode != prevMode {
				emitSGR(&buf, g)
				prevFG, prevBG, prevMode = g.FG, g.BG, g.Mode
			}
			ch := g.Char
			if ch == 0 {
				ch = ' '
			}
			buf.WriteRune(ch)
		}
		if r < lastContent {
			buf.WriteString("\x1b[0m\r\n")
			prevFG, prevBG, prevMode = vt10x.DefaultFG, vt10x.DefaultBG, 0
		}
	}
	buf.WriteString("\x1b[0m")
	term.Write(buf.Bytes()) //nolint:errcheck
}

// emitSGR writes a complete SGR escape sequence for the given glyph's
// attributes and colours into buf.  Always starts with \x1b[0 (full reset).
func emitSGR(buf *bytes.Buffer, g vt10x.Glyph) {
	buf.WriteString("\x1b[0")
	if g.Mode&vtAttrBold != 0 {
		buf.WriteString(";1")
	}
	if g.Mode&vtAttrItalic != 0 {
		buf.WriteString(";3")
	}
	if g.Mode&vtAttrUnderline != 0 {
		buf.WriteString(";4")
	}
	if g.Mode&vtAttrBlink != 0 {
		buf.WriteString(";5")
	}
	if g.Mode&vtAttrReverse != 0 {
		buf.WriteString(";7")
	}
	emitColorCode(buf, g.FG, true)
	emitColorCode(buf, g.BG, false)
	buf.WriteByte('m')
}

// emitColorCode appends SGR colour sub-parameters for c.
// Default colours are skipped (the leading \x1b[0 already resets them).
func emitColorCode(buf *bytes.Buffer, c vt10x.Color, isFG bool) {
	if c >= vt10x.DefaultFG {
		return
	}
	switch {
	case c < 8:
		buf.WriteByte(';')
		if isFG {
			buf.Write(strconv.AppendInt(nil, int64(30+c), 10))
		} else {
			buf.Write(strconv.AppendInt(nil, int64(40+c), 10))
		}
	case c < 16:
		buf.WriteByte(';')
		if isFG {
			buf.Write(strconv.AppendInt(nil, int64(90+c-8), 10))
		} else {
			buf.Write(strconv.AppendInt(nil, int64(100+c-8), 10))
		}
	case c < 256:
		if isFG {
			buf.WriteString(";38;5;")
		} else {
			buf.WriteString(";48;5;")
		}
		buf.Write(strconv.AppendInt(nil, int64(c), 10))
	default: // truecolor: r<<16|g<<8|b
		r := (c >> 16) & 0xff
		g := (c >> 8) & 0xff
		b := c & 0xff
		if isFG {
			buf.WriteString(";38;2;")
		} else {
			buf.WriteString(";48;2;")
		}
		buf.Write(strconv.AppendInt(nil, int64(r), 10))
		buf.WriteByte(';')
		buf.Write(strconv.AppendInt(nil, int64(g), 10))
		buf.WriteByte(';')
		buf.Write(strconv.AppendInt(nil, int64(b), 10))
	}
}

