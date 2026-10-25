package vt10x

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

	width := runeCellWidth(c)
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

// putASCII writes the leading run of printable ASCII in p and returns its
// length. Each byte gets the same result as parse, without grapheme
// segmentation: an ASCII character never joins a cluster whose lead is a
// single ASCII character. It returns 0 when the next byte needs parse.
func (t *State) putASCII(p []byte) int {
	if t.state != stateGround || t.DebugLogger != nil ||
		t.mode&ModeInsert != 0 || t.cur.Attr.Mode&attrGfx != 0 {
		return 0
	}
	// parse moves the cursor into the scroll region after the first
	// character; leave that case to it.
	if t.cur.State&cursorOrigin != 0 && (t.cur.Y < t.top || t.cur.Y > t.bottom) {
		return 0
	}
	if t.clusterOpen && t.clusterX < t.cols && t.clusterY < t.rows {
		row := t.lines[t.clusterY]
		if c := row.cells[t.clusterX]; c.width() < 0 || c.Char >= 0x80 || row.hasCombining(t.clusterX) {
			return 0
		}
	}
	n := 0
	for n < len(p) && p[n] >= 0x20 && p[n] < 0x7f {
		n++
	}
	if n == 0 {
		return 0
	}

	// The cell template matches what setChar stores for a narrow rune.
	attr := &t.cur.Attr
	tmpl := *attr
	tmpl.Combining, tmpl.Width = "", 1
	if attr.Mode&attrBold != 0 && attr.FG < 8 {
		tmpl.FG = attr.FG + 8
	}
	if attr.Mode&attrReverse != 0 {
		tmpl.FG, tmpl.BG = attr.BG, attr.FG
	}
	// Without side-table data the stored cell is the same on every row.
	var plain cell
	isPlain := tmpl.UL == DefaultUL && tmpl.Link == 0 && tmpl.Image == nil
	if isPlain {
		plain = cell{Char: tmpl.Char, FG: tmpl.FG, BG: tmpl.BG, Mode: tmpl.Mode}
		plain.setWidth(1)
	}
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
		c := plain
		if !isPlain {
			c = row.encode(tmpl)
		}
		row.writeASCII(x, p[i:i+k], c)
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
	return n
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
