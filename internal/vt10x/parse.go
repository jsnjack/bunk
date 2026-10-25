package vt10x

import (
	"encoding/binary"
	"unicode/utf8"
)

func isControlCode(c rune) bool {
	return c < 0x20 || c == 0177
}

func (t *State) parse(c rune) {
	t.logRune(c)
	if isControlCode(c) {
		if t.handleControlCodes(c) || t.cur.Attr.Mode&attrGfx == 0 {
			return
		}
	}
	// TODO: update selection; see st.c:2450
	if t.extendGrapheme(c) {
		return
	}
	t.print(c, runeCellWidth(c))
}

// print writes c, of the given cell width, as a new grapheme cluster at the
// cursor.
func (t *State) print(c rune, width int) {
	if width > t.cols {
		c, width = '\uFFFD', 1
	}
	if t.mode&ModeWrap != 0 && (t.cur.State&cursorWrapNext != 0 || t.cur.X+width > t.cols) {
		if t.cur.State&cursorWrapNext == 0 {
			for x := t.cur.X; x < t.cols; x++ {
				t.eraseWideAt(x, t.cur.Y)
				t.eraseCell(x, t.cur.Y)
				t.lines[t.cur.Y].cells[x].setWidth(-2)
			}
			t.markDirty(t.cur.Y)
		}
		t.lines[t.cur.Y].setWrap(t.cols - 1)
		t.newline(true)
	}
	if t.cur.X+width > t.cols {
		c, width = '\uFFFD', 1
	}

	if t.mode&ModeInsert != 0 && t.cur.X+1 < t.cols {
		// TODO: move shiz, look at st.c:2458
		t.logln("insert mode not implemented")
	}

	t.setChar(c, &t.cur.Attr, t.cur.X, t.cur.Y)
	t.clusterX, t.clusterY = t.cur.X, t.cur.Y
	if t.cur.X+width < t.cols {
		t.moveTo(t.cur.X+width, t.cur.Y)
	} else {
		t.cur.X = t.cols - 1
		t.cur.State |= cursorWrapNext
	}
	t.clusterOpen = true
}

// scan handles the leading bytes of p that need no rune-by-rune parsing
// and returns how many it consumed: text through putText, C0 controls, and
// control sequences through putCSI. It returns 0 when the next rune needs
// put.
func (t *State) scan(p []byte) int {
	if t.DebugLogger != nil {
		return 0
	}
	switch t.state {
	case stateGround:
		b := p[0]
		if b >= 0x20 && b != 0x7f {
			return t.putText(p)
		}
		if b == '\x1b' && len(p) > 1 && p[1] == '[' {
			// ESC closes the cluster and resets the sequence; '[' then
			// starts a control sequence.
			t.clusterOpen = false
			t.csi.reset()
			t.state = stateEscCSI
			return 2 + t.putCSI(p[2:])
		}
		// As in parse: in the line-drawing set, a control without an
		// action is printed.
		if t.handleControlCodes(rune(b)) || t.cur.Attr.Mode&attrGfx == 0 {
			return 1
		}
	case stateEsc:
		if p[0] == '[' {
			t.state = stateEscCSI
			return 1 + t.putCSI(p[1:])
		}
	case stateEscCSI:
		return t.putCSI(p)
	}
	return 0
}

// putCSI adds the leading bytes of p to the open control sequence and runs
// the sequence when its final byte arrives, as parseEscCSI does byte by
// byte. It stops before a control, DEL or non-ASCII byte, which put
// handles.
func (t *State) putCSI(p []byte) int {
	c := &t.csi
	// Parameter and intermediate bytes, up to the length limit.
	n := 0
	for limit := min(len(p), maxCSIBytes-len(c.buf)); n < limit && p[n] >= 0x20 && p[n] < 0x40; n++ {
	}
	c.buf = append(c.buf, p[:n]...)
	if len(c.buf) < maxCSIBytes {
		if n == len(p) || p[n] < 0x40 || p[n] > 0x7e {
			return n
		}
		c.buf = append(c.buf, p[n])
		n++
	}
	c.parse()
	t.state = stateGround
	t.handleCSI()
	return n
}

// textCell returns what setChar stores for a character with the current
// attributes, without character and width, and whether the attributes need
// no side-table data: no underline colour, link or image.
func (t *State) textCell() (cell, bool) {
	attr := &t.cur.Attr
	c := cell{FG: attr.FG, BG: attr.BG, Mode: attr.Mode}
	if attr.Mode&attrBold != 0 && attr.FG < 8 {
		c.FG = attr.FG + 8
	}
	if attr.Mode&attrReverse != 0 {
		c.FG, c.BG = attr.BG, attr.FG
	}
	return c, attr.UL == DefaultUL && attr.Link == 0 && attr.Image == nil
}

// putText writes the leading run of text in p that needs no grapheme
// segmentation and returns its length: printable ASCII, and runes that
// cannot join their neighbours (simpleGrapheme). Each rune gets the same
// result as parse. It returns 0 when the next rune needs parse.
func (t *State) putText(p []byte) int {
	if t.state != stateGround || t.DebugLogger != nil ||
		t.mode&ModeInsert != 0 || t.cur.Attr.Mode&attrGfx != 0 {
		return 0
	}
	// parse moves the cursor into the scroll region after the first
	// character; leave that case to it.
	if t.cur.State&cursorOrigin != 0 && (t.cur.Y < t.top || t.cur.Y > t.bottom) {
		return 0
	}
	// The open cluster must be one that no simple rune extends.
	if t.clusterOpen && t.clusterX < t.cols && t.clusterY < t.rows {
		row := t.lines[t.clusterY]
		if row.cells[t.clusterX].width() < 0 || !row.closedCluster(t.clusterX) {
			return 0
		}
	}
	tmpl, plain := t.textCell()
	narrow, wide, cont := tmpl, tmpl, tmpl
	narrow.setWidth(1)
	wide.setWidth(2)
	cont.setWidth(-1)
	i := 0
	for i < len(p) {
		if b := p[i]; b >= 0x20 && b < 0x7f {
			n := i + 1 + printableASCII(p[i+1:])
			t.putASCII(p[i:n], tmpl, plain)
			i = n
			continue
		} else if b < 0x80 {
			break
		}
		c, size := utf8.DecodeRune(p[i:])
		if size == 1 || c >= cachedWidthRunes {
			break // invalid or incomplete UTF-8, or an uncached rune
		}
		info := runeInfo(c)
		if info&infoSimple == 0 {
			break
		}
		width := int(info & infoWidth)
		x, y := t.cur.X, t.cur.Y
		if !plain || t.cur.State&cursorWrapNext != 0 || x+width > t.cols {
			// Wrapping, wrap padding and side-table data.
			t.print(c, width)
			i += size
			continue
		}
		// What setChar and print do, for a character that fits.
		row := t.lines[y]
		last := x + width - 1
		if row.cells[x].width() == -1 {
			t.eraseWideAt(x, y)
		}
		if row.cells[last].width() == 2 {
			t.eraseWideAt(last, y)
		}
		// Store whole cells, then the character: building a cell in a
		// local and copying it stalls on the partly written local.
		if width == 1 {
			row.cells[x] = narrow
		} else {
			row.cells[x] = wide
			row.cells[last] = cont
		}
		row.cells[x].Char = c
		row.touch(last)
		t.markDirty(y)
		t.changed |= ChangedScreen
		t.clusterX, t.clusterY, t.clusterOpen = x, y, true
		if last+1 < t.cols {
			t.cur.X = last + 1
			t.cur.State &^= cursorWrapNext
		} else {
			t.cur.X = t.cols - 1
			t.cur.State |= cursorWrapNext
		}
		i += size
	}
	return i
}

// printableASCII returns the length of the leading run of printable ASCII
// in p. It tests eight bytes at a time while it can.
func printableASCII(p []byte) int {
	const ones, highs = 0x0101010101010101, 0x8080808080808080
	n := 0
	for ; n+8 <= len(p); n += 8 {
		x := binary.LittleEndian.Uint64(p[n:])
		// A byte below 0x20 borrows into its high bit; one at 0x7f or above
		// has it set, before or after adding 1.
		if (x-0x20*ones)&^x&highs != 0 || (x|(x+ones))&highs != 0 {
			break
		}
	}
	for n < len(p) && p[n] >= 0x20 && p[n] < 0x7f {
		n++
	}
	return n
}

// putASCII writes text, a run of printable ASCII, as cells like tmpl, from
// textCell. Each byte gets the same result as parse, without grapheme
// segmentation: an ASCII character never joins a cluster whose lead is a
// simple grapheme.
func (t *State) putASCII(text []byte, tmpl cell, plain bool) {
	n := len(text)
	tmpl.setWidth(1)
	t.changed |= ChangedScreen
	for i := 0; i < n; {
		if t.cur.State&cursorWrapNext != 0 && t.mode&ModeWrap != 0 {
			t.lines[t.cur.Y].setWrap(t.cols - 1)
			t.newline(true)
		}
		// Write the part of the run that fits on this row. A wide character
		// inside the segment is overwritten whole, so only the segment's
		// edges can split one.
		x, y := t.cur.X, t.cur.Y
		row := t.lines[y]
		k := min(n-i, t.cols-x)
		if row.cells[x].width() == -1 {
			t.eraseWideAt(x, y)
		}
		if row.cells[x+k-1].width() == 2 {
			t.eraseWideAt(x+k-1, y)
		}
		c := tmpl
		if !plain {
			g := t.cur.Attr
			g.FG, g.BG, g.Combining, g.Width = c.FG, c.BG, "", 1
			c = row.encode(g)
		}
		row.writeASCII(x, text[i:i+k], c)
		t.markDirty(y)
		i += k
		last := x + k - 1
		t.clusterX, t.clusterY = last, y
		if last+1 < t.cols {
			t.moveTo(last+1, y)
		} else {
			t.cur.X = last
			t.cur.State |= cursorWrapNext
		}
	}
	t.clusterOpen = true
}

func (t *State) parseEsc(c rune) {
	if t.handleControlCodes(c) {
		return
	}
	next := stateGround
	t.logRune(c)
	switch c {
	case '[':
		next = stateEscCSI
	case '#':
		next = stateEscTest
	case 'P', // DCS - Device Control String
		'_', // APC - Application Program Command
		'^', // PM - Privacy Message
		']', // OSC - Operating System Command
		'k': // old title set compatibility
		t.str.reset()
		t.str.typ = c
		next = stateEscStr
	case '(': // set primary charset G0
		next = stateEscAltCharset
	case ')', // set secondary charset G1 (ignored)
		'*', // set tertiary charset G2 (ignored)
		'+': // set quaternary charset G3 (ignored)
	case 'D': // IND - linefeed
		if t.cur.Y == t.bottom {
			t.scrollUp(t.top, 1)
		} else {
			t.moveTo(t.cur.X, t.cur.Y+1)
		}
	case 'E': // NEL - next line
		t.newline(true)
	case 'H': // HTS - horizontal tab stop
		t.tabs[t.cur.X] = true
	case 'M': // RI - reverse index
		if t.cur.Y == t.top {
			t.scrollDown(t.top, 1)
		} else {
			t.moveTo(t.cur.X, t.cur.Y-1)
		}
	case 'Z': // DECID - identify terminal
		// TODO: write to our writer our id
	case 'c': // RIS - reset to initial state
		t.reset()
		if t.sbClearCb != nil {
			t.sbClearCb()
		}
		t.sweepImages()
	case '=': // DECPAM - application keypad
		t.mode |= ModeAppKeypad
	case '>': // DECPNM - normal keypad
		t.mode &^= ModeAppKeypad
	case '7': // DECSC - save cursor
		t.saveCursor()
	case '8': // DECRC - restore cursor
		t.restoreCursor()
	case '\\': // ST - stop
	default:
		t.logf("unknown ESC sequence '%c'\n", c)
	}
	t.state = next
}

func (t *State) parseEscCSI(c rune) {
	if t.handleControlCodes(c) {
		return
	}
	t.logRune(c)
	if t.csi.put(byte(c)) {
		t.state = stateGround
		t.handleCSI()
	}
}

func (t *State) parseEscStr(c rune) {
	t.logRune(c)
	switch c {
	case 0x18, 0x1a:
		t.state = stateGround
		t.str.reset()
		if t.graphics != nil {
			t.graphics.Abort()
		}
	case '\033':
		t.state = stateEscStrEnd
	case '\a': // backwards compatiblity to xterm
		t.state = stateGround
		t.handleSTR()
	default:
		t.str.put(c)
	}
}

func (t *State) parseEscStrEnd(c rune) {
	if t.str.graphics {
		// Graphics payloads are opaque until ST/BEL. In particular, an
		// embedded ESC [ must not turn corrupt image data into pane text.
		switch c {
		case '\\', '\a':
			t.state = stateGround
			t.handleSTR()
		case 0x18, 0x1a:
			t.handleControlCodes(c)
		case '\x1b':
			t.str.put('\x1b')
		default:
			t.str.put('\x1b')
			t.str.put(c)
			t.state = stateEscStr
		}
		return
	}
	if t.handleControlCodes(c) {
		return
	}
	t.logRune(c)
	t.state = stateGround
	if c == '\\' {
		t.handleSTR()
	}
}

func (t *State) parseEscAltCharset(c rune) {
	if t.handleControlCodes(c) {
		return
	}
	t.logRune(c)
	switch c {
	case '0': // line drawing set
		t.cur.Attr.Mode |= attrGfx
	case 'B': // USASCII
		t.cur.Attr.Mode &^= attrGfx
	case 'A', // UK (ignored)
		'<', // multinational (ignored)
		'5', // Finnish (ignored)
		'C', // Finnish (ignored)
		'K': // German (ignored)
	default:
		t.logf("unknown alt. charset '%c'\n", c)
	}
	t.state = stateGround
}

func (t *State) parseEscTest(c rune) {
	if t.handleControlCodes(c) {
		return
	}
	// DEC screen alignment test
	if c == '8' {
		for y := 0; y < t.rows; y++ {
			for x := 0; x < t.cols; x++ {
				t.setChar('E', &t.cur.Attr, x, y)
			}
		}
	}
	t.state = stateGround
}

func (t *State) handleControlCodes(c rune) bool {
	if !isControlCode(c) {
		return false
	}
	t.clusterOpen = false
	switch c {
	// HT
	case '\t':
		t.putTab(true)
	// BS
	case '\b':
		t.moveTo(t.cur.X-1, t.cur.Y)
	// CR
	case '\r':
		t.moveTo(0, t.cur.Y)
	// LF, VT, LF
	case '\f', '\v', '\n':
		// go to first col if mode is set
		t.newline(t.mode&ModeCRLF != 0)
	// BEL
	case '\a':
		if t.bellCb != nil {
			t.bellCb()
		}
	// ESC
	case 033:
		t.csi.reset()
		t.state = stateEsc
	// SO, SI
	case 016, 017:
		// different charsets not supported. apps should use the correct
		// alt charset escapes, probably for line drawing
	// SUB, CAN
	case 032, 030:
		t.csi.reset()
		t.str.reset()
		t.state = stateGround
		if t.graphics != nil {
			t.graphics.Abort()
		}
	// ignore ENQ, NUL, XON, XOFF, DEL
	case 005, 000, 021, 023, 0177:
	default:
		return false
	}
	return true
}
