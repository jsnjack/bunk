// osc.go – OSC (Operating System Command) sequence pre-scanner.
//
// vt10x handles OSC 0/1/2 (title), 4 (colour), 10/11 (fg/bg colour query),
// and 104 (colour reset).  Everything else is silently dropped.
//
// Modern terminal emulators (foot, kitty, iTerm2, Ptyxis/VTE, …) understand
// these sequences, which must reach the host terminal directly:
//
//	OSC 7   – shell CWD notification; lets the terminal open new tabs in the
//	           same directory ("open here"), update tab titles with the path, etc.
//	OSC 8   – inline hyperlinks; terminals can open URLs on Ctrl+click.
//	OSC 52  – clipboard read/write; lets programs access the system clipboard
//	           without needing xclip/xdotool.
//
// The oscScanner runs over each raw PTY chunk BEFORE it is fed to vt10x.
// When it finds a complete OSC in the forward-set it sends a copy to a
// per-App channel; the render loop drains that channel and writes the bytes to
// os.Stdout just before tcell.Show().  Writing from the single render goroutine
// serialises our writes with tcell's own writes, preventing interleaved escapes.
package main

import (
	"bytes"
	"strconv"
)

// oscForwardNums is the set of OSC command numbers forwarded to the host
// terminal verbatim.  Sequences not in this set are consumed by vt10x or
// silently discarded.
var oscForwardNums = map[int]bool{
	7:  true, // CWD notification  (shell integration, widely supported)
	8:  true, // Hyperlinks        (Ctrl+click in modern terminals)
	52: true, // Clipboard access  (OSC 52, supported by most terminals)
}

// oscMaxBuf is the maximum bytes we accumulate for a single OSC sequence.
// OSC 7 (CWD) is typically <300 bytes; OSC 8 (hyperlink) rarely exceeds 2 KB.
// OSC 52 clipboard can be large – we cap at 64 KB and let the host handle it.
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
// One instance lives per Pane; keep it as a value inside Pane (no allocation).
type oscScanner struct {
	state   oscParseState
	buf     []byte // accumulates bytes of the current in-progress OSC
	overrun bool   // set when buf was capped; discard this OSC silently
}

// Scan processes chunk and sends any complete, forward-eligible OSC sequences
// to oscCh.  It never blocks; if oscCh is full the sequence is dropped (the
// next one from the same shell will arrive shortly).
//
// All bytes in chunk are also fed to vt10x by the caller regardless of what
// Scan finds – vt10x must see the full stream to keep its state consistent.
func (s *oscScanner) Scan(chunk []byte, oscCh chan<- []byte) {
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
				s.dispatch(oscCh)
				s.state = oscIdle
			case 0x1b:
				s.state = oscContentESC
			}

		case oscContentESC:
			if len(s.buf) < oscMaxBuf {
				s.buf = append(s.buf, b)
			}
			if b == '\\' { // ST = ESC \ terminator
				s.dispatch(oscCh)
				s.state = oscIdle
			} else {
				s.state = oscInContent // spurious ESC inside OSC, keep going
			}
		}
	}
}

// dispatch sends s.buf to oscCh if the OSC number is in oscForwardNums.
func (s *oscScanner) dispatch(oscCh chan<- []byte) {
	if s.overrun || len(s.buf) < 3 {
		return
	}
	// buf = \x1b ] <numBytes> ; <rest> <terminator>
	body := s.buf[2:]
	semi := bytes.IndexByte(body, ';')
	var numBytes []byte
	if semi >= 0 {
		numBytes = body[:semi]
	} else {
		// Bare OSC with no semicolon (unusual but valid) – strip terminator.
		numBytes = bytes.TrimRight(body, "\x07\x1b\\")
	}
	n, err := strconv.Atoi(string(numBytes))
	if err != nil || !oscForwardNums[n] {
		return
	}
	out := make([]byte, len(s.buf))
	copy(out, s.buf)
	select {
	case oscCh <- out:
	default: // channel full – this frame's OSC is dropped, shell will re-emit
	}
}
