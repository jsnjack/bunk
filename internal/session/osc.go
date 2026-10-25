// osc.go - OSC (Operating System Command) sequence pre-scanner.
//
// The scanner runs over each raw PTY chunk before it reaches vt10x and hands
// complete OSC 7, 52 and 133 sequences to Hooks.OSC, so a front end can pass
// them on (bunk forwards them to the host terminal).
package session

import (
	"bytes"
	"strconv"
	"unicode/utf8"
)

// oscForwardNums is the set of OSC command numbers forwarded to the host
// terminal verbatim.  Sequences not in this set are consumed by vt10x or
// silently discarded.
//
// OSC 8 (hyperlinks) is intentionally NOT forwarded here. vt10x parses OSC 8
// and stores the URL on each glyph; the renderer applies tcell.Style.Url so
// tcell emits OSC 8 inline with the cell paint, preserving the open/close
// pairing relative to the characters they annotate. Forwarding raw OSC 8
// from the PTY would be wrong (the opens and closes would arrive separately
// from the glyph paint, attributing links to the wrong text).
var oscForwardNums = map[int]bool{
	7:   true, // CWD notification       (shell integration, widely supported)
	52:  true, // Clipboard access       (OSC 52, supported by most terminals)
	133: true, // Shell prompt markers   (semantic shell integration)
}

// statusOSCNums are the OSC numbers that report attention requests (9:
// iTerm2/Windows Terminal, 99: kitty, 777: urxvt/VTE notify) and command
// results (133;D).
var statusOSCNums = map[int]bool{9: true, 99: true, 133: true, 777: true}

// oscMaxBuf is the maximum bytes we accumulate for a single OSC sequence.
// OSC 7 (CWD) is typically <300 bytes; OSC 8 (hyperlink) rarely exceeds 2 KB.
// OSC 52 clipboard can be large - we cap at 64 KB and let the host handle it.
const oscMaxBuf = 65536

// oscParseState is the state of the oscScanner FSM.
type oscParseState int

const (
	oscIdle       oscParseState = iota // normal content
	oscSeenESC                         // just saw 0x1b
	oscInContent                       // inside \x1b] … content
	oscContentESC                      // inside OSC, just saw 0x1b (possible ST)
)

// oscScanner is a minimal state machine that extracts complete OSC sequences
// from a raw PTY byte stream that may arrive in arbitrary chunk sizes.
// One instance lives per Session; keep it as a value inside Session (no allocation).
type oscScanner struct {
	// status, when set, receives the payload of each OSC 9, 99, 777 and 133
	// sequence (terminator removed) for agent status, forwarded or not.
	status func(num int, payload []byte)

	state   oscParseState
	buf     []byte // accumulates bytes of the current in-progress OSC
	overrun bool   // set when buf was capped; discard this OSC silently
}

// Scan processes chunk and calls emit for each complete, forward-eligible
// OSC sequence.  emit must not retain the slice past its own call: the
// scanner reuses its internal buffer between sequences.
//
// All bytes in chunk are also fed to vt10x by the caller regardless of what
// Scan finds - vt10x must see the full stream to keep its state consistent.
func (s *oscScanner) Scan(chunk []byte, emit func([]byte)) {
	for _, b := range chunk {
		switch s.state {

		case oscIdle:
			if b == 0x1b {
				s.state = oscSeenESC
			}

		case oscSeenESC:
			if b == ']' { // ESC ] = start of OSC
				s.buf = append(s.buf[:0], 0x1b, ']')
				s.overrun = false
				s.state = oscInContent
			} else {
				s.state = oscIdle
			}

		case oscInContent:
			if len(s.buf) < oscMaxBuf {
				s.buf = append(s.buf, b)
			} else {
				s.overrun = true // cap reached; mark and stop accumulating
			}
			switch b {
			case 0x07: // BEL terminator
				s.dispatch(emit)
				s.state = oscIdle
			case 0x1b:
				s.state = oscContentESC
			}

		case oscContentESC:
			if len(s.buf) < oscMaxBuf {
				s.buf = append(s.buf, b)
			}
			// End where ptyStream framing ends the string: ST (ESC \) or
			// ESC BEL.
			switch b {
			case '\\', 0x07:
				s.dispatch(emit)
				s.state = oscIdle
			case 0x1b: // this ESC may start the ST
			default:
				s.state = oscInContent // spurious ESC inside OSC, keep going
			}
		}
	}
}

// dispatch hands s.buf to emit if the OSC number is in oscForwardNums and
// the payload passes sanitizeOSC.
func (s *oscScanner) dispatch(emit func([]byte)) {
	if s.overrun || len(s.buf) < 3 {
		return
	}
	// buf = \x1b ] <numBytes> [; <payload>] <terminator>. A bare OSC has no
	// semicolon and an empty payload; its number must be all digits, or
	// stray ESC bytes before the terminator would reach the host.
	numBytes, payload, _ := bytes.Cut(StripOSCTerminator(s.buf[2:]), []byte(";"))
	n, err := strconv.Atoi(string(numBytes))
	if err != nil {
		return
	}
	if s.status != nil && statusOSCNums[n] {
		s.status(n, payload)
	}
	if !oscForwardNums[n] {
		return
	}
	if !sanitizeOSC(n, payload) {
		L().Debug("osc: dropping malformed/binary sequence", "osc_num", n, "len", len(s.buf))
		return
	}
	L().Debug("osc: forwarding sequence", "osc_num", n, "len", len(s.buf))
	emit(s.buf)
}

// StripOSCTerminator removes a trailing OSC terminator (BEL or ESC \) from
// payload so validators see only the meaningful body.
func StripOSCTerminator(payload []byte) []byte {
	switch {
	case len(payload) >= 1 && payload[len(payload)-1] == 0x07:
		return payload[:len(payload)-1]
	case len(payload) >= 2 && payload[len(payload)-2] == 0x1b && payload[len(payload)-1] == '\\':
		return payload[:len(payload)-2]
	}
	return payload
}

// sanitizeOSC validates an OSC payload against the rules for its number.
// Returns true if the sequence is safe to forward verbatim to the host
// terminal, false if it should be dropped.
//
// Why this exists: a pane displaying binary content (e.g. cat /bin/ls)
// streams arbitrary bytes through the OSC scanner. Without validation those
// bytes get re-emitted to the host terminal, corrupting state across every
// pane and the borders. Each forwarded OSC has a known structure and we
// reject anything that doesn't match it.
//
// Validation philosophy:
//   - OSC 7 carries an IRI (RFC 3987) so non-ASCII Unicode must pass; we
//     require valid UTF-8 and reject C0/C1 control bytes.
//   - OSC 52 (clipboard) carries base64; we accept only the base64 alphabet
//     after the selection prefix, plus "?" for query.
//   - OSC 133 (prompt markers) is a small grammar of printable ASCII.
func sanitizeOSC(num int, payload []byte) bool {
	switch num {
	case 7:
		return isSafeUTF8Text(payload)
	case 52:
		return IsSafeOSC52(payload)
	case 133:
		return isPrintableASCII(payload)
	}
	return false
}

// isSafeUTF8Text returns true if b is valid UTF-8 and contains no C0 (U+0000-
// U+001F, U+007F) or C1 (U+0080-U+009F) control codepoints. Tab (U+0009) is
// allowed since some hyperlink ID schemes use it as a separator.
func isSafeUTF8Text(b []byte) bool {
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size < 2 {
			return false
		}
		switch {
		case r == 0x09:
		case r < 0x20:
			return false
		case r >= 0x7F && r <= 0x9F:
			return false
		}
		i += size
	}
	return true
}

// isPrintableASCII returns true if every byte of b is in 0x20-0x7E.
func isPrintableASCII(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c > 0x7E {
			return false
		}
	}
	return true
}

// IsSafeOSC52 validates an OSC 52 payload of the form:
//
//	<selection> ; <data>
//
// where <selection> ⊆ {c,p,q,s,0..7} (may be empty) and <data> is either "?"
// (query) or a base64-encoded string drawn from [A-Za-z0-9+/=] (may be
// empty, meaning "clear clipboard").
func IsSafeOSC52(b []byte) bool {
	semi := bytes.IndexByte(b, ';')
	if semi < 0 {
		return false
	}
	sel, data := b[:semi], b[semi+1:]
	for _, c := range sel {
		switch {
		case c >= '0' && c <= '7':
		case c == 'c' || c == 'p' || c == 'q' || c == 's':
		default:
			return false
		}
	}
	if len(data) == 1 && data[0] == '?' {
		return true
	}
	for _, c := range data {
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '+' || c == '/' || c == '=':
		default:
			return false
		}
	}
	return true
}
