package session

import (
	"bunk/internal/graphics"
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// The byte-at-a-time framing, OSC scanning and query matching, as they were
// before CSI parameters were framed in bulk, the scanners skipped to the
// next ESC and queries were picked by their third byte. FuzzFraming compares
// the current code against them.

// referenceScan is ptyStream.scan before the bulk CSI path.
func referenceScan(s *ptyStream, chunk []byte) []byte {
	out := append(s.out[:0], s.utf8...)
	s.utf8 = s.utf8[:0]
	for i := 0; i < len(chunk); i++ {
		b := chunk[i]
		if s.state == ptyText {
			if b != 0x1b {
				// Copy the text up to the next ESC in one append.
				end := len(chunk)
				if j := bytes.IndexByte(chunk[i:], 0x1b); j >= 0 {
					end = i + j
				}
				out = append(out, chunk[i:end]...)
				i = end - 1
				continue
			}
			s.state = ptyEscape
			s.pending = append(s.pending[:0], b)
			continue
		}
		if b == 0x18 || b == 0x1a { // CAN/SUB cancel an unfinished control.
			if s.graphics {
				out = append(out, b)
			} // abort multipart uploads in vt10x too
			s.reset()
			continue
		}
		if !s.discard {
			limit := oscMaxBuf
			if s.graphics {
				limit = graphics.MaxSequenceBytes + 4 // introducer and ST
			}
			if len(s.pending) >= limit {
				s.pending = s.pending[:0]
				s.discard = true
			} else {
				s.pending = append(s.pending, b)
				if !s.graphics && len(s.pending) >= 3 && len(s.pending) <= 82 {
					s.graphics = graphics.IsCommand(rune(s.pending[1]), s.pending[2:])
				}
			}
		}
		finished := false
		switch s.state {
		case ptyEscape:
			switch b {
			case '[':
				s.state = ptyCSI
			case ']', 'P', '_', '^', 'k':
				s.state = ptyString
			case '(', ')', '*', '+', '#':
				s.state = ptyCharset
			case 0x1b:
				s.pending = append(s.pending[:0], b)
			default:
				finished = true
			}
		case ptyCSI:
			if b == 0x1b {
				s.reset()
				s.state = ptyEscape
				s.pending = append(s.pending, b)
			} else {
				finished = b >= 0x40 && b <= 0x7e
			}
		case ptyString:
			if b == 0x1b {
				s.state = ptyStringEscape
			}
			finished = b == 0x07
		case ptyStringEscape:
			finished = b == '\\' || b == 0x07
			if b != 0x1b {
				s.state = ptyString
			}
		case ptyCharset:
			if b == 0x1b { // vt10x starts a new control here too
				s.pending = append(s.pending[:0], b)
				s.state = ptyEscape
			} else {
				finished = true
			}
		}
		if finished {
			if !s.discard {
				out = append(out, s.pending...)
			} else if s.graphics {
				out = append(out, 0x18)
			}
			s.reset()
		}
	}
	split := utf8Boundary(out)
	s.utf8 = append(s.utf8, out[split:]...)
	s.out = out
	if cap(out) > maxReusedOutput {
		s.out = nil
	}
	return out[:split]
}

// referenceParseCSIQuery is parseCSIQuery before it switched on the byte
// after "\x1b[".
func referenceParseCSIQuery(data []byte, start int) (terminalQuery, bool) {
	rest := data[start:]
	switch {
	case bytes.HasPrefix(rest, []byte("\x1b[14t")):
		return terminalQuery{start: start, end: start + 5, kind: terminalQuerySize, mode: 14}, true
	case bytes.HasPrefix(rest, []byte("\x1b[16t")):
		return terminalQuery{start: start, end: start + 5, kind: terminalQuerySize, mode: 16}, true
	case bytes.HasPrefix(rest, []byte("\x1b[18t")):
		return terminalQuery{start: start, end: start + 5, kind: terminalQuerySize, mode: 18}, true
	case bytes.HasPrefix(rest, []byte("\x1b[=c")):
		return terminalQuery{start: start, end: start + 4, kind: terminalQueryDA3}, true
	case bytes.HasPrefix(rest, []byte("\x1b[=0c")):
		return terminalQuery{start: start, end: start + 5, kind: terminalQueryDA3}, true
	case bytes.HasPrefix(rest, []byte("\x1b[5n")):
		return terminalQuery{start: start, end: start + 4, kind: terminalQueryDSR}, true
	case bytes.HasPrefix(rest, []byte("\x1b[>0q")):
		return terminalQuery{start: start, end: start + len("\x1b[>0q"), kind: terminalQueryXTVERSION}, true
	case bytes.HasPrefix(rest, []byte("\x1b[>q")):
		return terminalQuery{start: start, end: start + len("\x1b[>q"), kind: terminalQueryXTVERSION}, true
	case bytes.HasPrefix(rest, []byte("\x1b[>0c")):
		return terminalQuery{start: start, end: start + len("\x1b[>0c"), kind: terminalQueryDA2}, true
	case bytes.HasPrefix(rest, []byte("\x1b[>c")):
		return terminalQuery{start: start, end: start + len("\x1b[>c"), kind: terminalQueryDA2}, true
	case bytes.HasPrefix(rest, []byte("\x1b[0c")):
		return terminalQuery{start: start, end: start + len("\x1b[0c"), kind: terminalQueryDA}, true
	case bytes.HasPrefix(rest, []byte("\x1b[c")):
		return terminalQuery{start: start, end: start + len("\x1b[c"), kind: terminalQueryDA}, true
	case bytes.HasPrefix(rest, []byte("\x1b[6n")):
		return terminalQuery{start: start, end: start + len("\x1b[6n"), kind: terminalQueryCPR}, true
	case bytes.HasPrefix(rest, []byte("\x1b[?6n")):
		return terminalQuery{start: start, end: start + 5, kind: terminalQueryCPR, mode: 1}, true
	case bytes.HasPrefix(rest, []byte("\x1b[?")):
		mode, end, ok := parseDECRQM(data, start)
		if ok {
			return terminalQuery{start: start, end: end, kind: terminalQueryDECRQM, mode: mode}, true
		}
	}
	return terminalQuery{}, false
}

// referenceOSCScan is oscScanner.Scan before it skipped to the next ESC.
func referenceOSCScan(s *oscScanner, chunk []byte, emit func([]byte)) {
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

// referenceNextTerminalQuery is nextTerminalQuery before it skipped to the
// next ESC.
func referenceNextTerminalQuery(data []byte, from int) (terminalQuery, bool) {
	for i := from; i+1 < len(data); i++ {
		if data[i] != 0x1b {
			continue
		}
		switch data[i+1] {
		case '[':
			if q, ok := parseCSIQuery(data, i); ok {
				return q, true
			}
		case ']':
			end := oscSequenceEnd(data, i)
			if end <= i {
				continue
			}
			if q, ok := parseOSCQuery(data, i, end); ok {
				return q, true
			}
			i = end - 1
		case 'P':
			end := dcsSequenceEnd(data, i)
			if end <= i {
				continue
			}
			if bytes.HasPrefix(data[i:end], []byte("\x1bP+q")) {
				return terminalQuery{
					start:   i,
					end:     end,
					kind:    terminalQueryXTGETTCAP,
					payload: string(data[i+4 : end-2]),
				}, true
			}
			if bytes.HasPrefix(data[i:end], []byte("\x1bP$q")) {
				return terminalQuery{start: i, end: end, kind: terminalQueryDECRQSS, payload: string(data[i+4 : end-2])}, true
			}
			i = end - 1
		case '_', '^', 'X':
			if end := dcsSequenceEnd(data, i); end > i {
				i = end - 1
			}
		}
	}
	return terminalQuery{}, false
}

// fastFramingSeeds are streams heavy in the sequences the fast paths take.
var fastFramingSeeds = []string{
	"\x1b[38;2;1;2;3mrgb\x1b[0m \x1b[1;34mbold\x1b[22m\x1b[48;5;236mbg\x1b[49m",
	"\x1b[14t\x1b[16t\x1b[18t\x1b[=c\x1b[=0c\x1b[5n\x1b[>0q\x1b[>q\x1b[>0c\x1b[>c\x1b[0c\x1b[c\x1b[6n\x1b[?6n\x1b[?2026$p",
	"\x1b[1x\x1b[=x\x1b[>x\x1b[0x\x1b[?x\x1b[?1$x\x1b[",
	"\x1b[" + strings.Repeat("1;", 40000) + "m",
	"\x1b[12\x1b[3m\x1b[4\x18m\x1b[5\x1aX\x1b[6\xe6\x97\xa5m",
	"\x1b_Ga=T;AAAA\x1b\\\x1b[31m\x1bPq#0\x1b\\",
}

func FuzzFraming(f *testing.F) {
	for i, seed := range append(fastFramingSeeds, fuzzStreamSeeds...) {
		f.Add([]byte{byte(i % 5), 3}, []byte(seed))
	}
	f.Fuzz(func(t *testing.T, cuts, data []byte) {
		if len(data) > 4*oscMaxBuf {
			return
		}
		var got, want ptyStream
		var gotOSC, wantOSC oscScanner
		var gotSeqs, wantSeqs [][]byte
		for _, chunk := range fuzzChunks(data, cuts) {
			a := append([]byte(nil), got.scan(chunk)...)
			b := referenceScan(&want, chunk)
			if !bytes.Equal(a, b) {
				t.Fatalf("chunk %q framed as %q, reference %q", chunk, a, b)
			}
			gotOSC.Scan(a, func(seq []byte) { gotSeqs = append(gotSeqs, bytes.Clone(seq)) })
			referenceOSCScan(&wantOSC, a, func(seq []byte) { wantSeqs = append(wantSeqs, bytes.Clone(seq)) })
			if !reflect.DeepEqual(gotSeqs, wantSeqs) || gotOSC.state != wantOSC.state ||
				!bytes.Equal(gotOSC.buf, wantOSC.buf) || gotOSC.overrun != wantOSC.overrun {
				t.Fatalf("after %q: OSC scanner emitted %q in state %v %q, reference %q in %v %q",
					a, gotSeqs, gotOSC.state, gotOSC.buf, wantSeqs, wantOSC.state, wantOSC.buf)
			}
			if got.state != want.state || !bytes.Equal(got.pending, want.pending) || !bytes.Equal(got.utf8, want.utf8) ||
				got.discard != want.discard || got.graphics != want.graphics {
				t.Fatalf("after %q: state %v %q %v %v, reference %v %q %v %v", chunk,
					got.state, got.pending, got.discard, got.graphics, want.state, want.pending, want.discard, want.graphics)
			}
		}
		// Results change only at ESC bytes, so start at each one and the
		// byte after it; every start would make long inputs quadratic.
		for start := range data {
			if start > 0 && data[start] != 0x1b && data[start-1] != 0x1b {
				continue
			}
			q, ok := parseCSIQuery(data, start)
			rq, rok := referenceParseCSIQuery(data, start)
			if q != rq || ok != rok {
				t.Fatalf("%q at %d: query %+v %v, reference %+v %v", data, start, q, ok, rq, rok)
			}
			q, ok = nextTerminalQuery(data, start)
			rq, rok = referenceNextTerminalQuery(data, start)
			if q != rq || ok != rok {
				t.Fatalf("%q from %d: next query %+v %v, reference %+v %v", data, start, q, ok, rq, rok)
			}
		}
	})
}
