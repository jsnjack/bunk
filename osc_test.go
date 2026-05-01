package main

import (
	"bytes"
	"testing"
)

// OSC scanner tests: clipboard paste, directory tracking, and shell prompt
// markers that must be forwarded to the host terminal.
//
// Bug: clipboard paste not working, CWD not updating in pane title
//
// These sequences must be intercepted from the PTY stream and forwarded
// to the host terminal or bunk's own handlers.

// helper: collect all sequences emitted by scanning chunk(s).
func scanAll(chunks ...[]byte) [][]byte {
	var out [][]byte
	var s oscScanner
	emit := func(seq []byte) {
		out = append(out, append([]byte(nil), seq...))
	}
	for _, c := range chunks {
		s.Scan(c, emit)
	}
	return out
}

// ---------------------------------------------------------------------------
// OSC 7 with BEL terminator
// ---------------------------------------------------------------------------

func TestOSC7_BEL(t *testing.T) {
	// OSC 7 ; file:///home/user BEL
	data := []byte("\x1b]7;file:///home/user\x07")
	seqs := scanAll(data)
	if len(seqs) != 1 {
		t.Fatalf("expected 1 forwarded sequence, got %d", len(seqs))
	}
	if !bytes.Equal(seqs[0], data) {
		t.Errorf("forwarded sequence = %q, want %q", seqs[0], data)
	}
}

// ---------------------------------------------------------------------------
// OSC 7 with ST terminator (ESC \)
// ---------------------------------------------------------------------------

func TestOSC7_ST(t *testing.T) {
	data := []byte("\x1b]7;file:///tmp\x1b\\")
	seqs := scanAll(data)
	if len(seqs) != 1 {
		t.Fatalf("expected 1 forwarded sequence, got %d", len(seqs))
	}
	if !bytes.Equal(seqs[0], data) {
		t.Errorf("forwarded sequence = %q, want %q", seqs[0], data)
	}
}

// ---------------------------------------------------------------------------
// OSC 52 (clipboard)
// ---------------------------------------------------------------------------

func TestOSC52_Forwarded(t *testing.T) {
	data := []byte("\x1b]52;c;SGVsbG8=\x07")
	seqs := scanAll(data)
	if len(seqs) != 1 {
		t.Fatalf("expected 1 forwarded sequence, got %d", len(seqs))
	}
	if !bytes.Equal(seqs[0], data) {
		t.Errorf("forwarded sequence = %q, want %q", seqs[0], data)
	}
}

// ---------------------------------------------------------------------------
// OSC 133 (shell prompt markers)
// ---------------------------------------------------------------------------

func TestOSC133_BEL(t *testing.T) {
	data := []byte("\x1b]133;A\x07")
	seqs := scanAll(data)
	if len(seqs) != 1 {
		t.Fatalf("expected 1 forwarded sequence, got %d", len(seqs))
	}
	if !bytes.Equal(seqs[0], data) {
		t.Errorf("forwarded sequence = %q, want %q", seqs[0], data)
	}
}

func TestOSC133_ST(t *testing.T) {
	data := []byte("\x1b]133;D;0\x1b\\")
	seqs := scanAll(data)
	if len(seqs) != 1 {
		t.Fatalf("expected 1 forwarded sequence, got %d", len(seqs))
	}
	if !bytes.Equal(seqs[0], data) {
		t.Errorf("forwarded sequence = %q, want %q", seqs[0], data)
	}
}

// ---------------------------------------------------------------------------
// OSC 0 (title) — NOT forwarded (vt10x handles it)
// ---------------------------------------------------------------------------

func TestOSC0_NotForwarded(t *testing.T) {
	data := []byte("\x1b]0;my window title\x07")
	seqs := scanAll(data)
	if len(seqs) != 0 {
		t.Errorf("OSC 0 should not be forwarded, got %d sequences", len(seqs))
	}
}

// ---------------------------------------------------------------------------
// Multi-chunk OSC (split across two Scan calls)
// ---------------------------------------------------------------------------

func TestOSC_MultiChunk(t *testing.T) {
	// Split an OSC 7 sequence in the middle.
	full := []byte("\x1b]7;file:///home/user\x07")
	mid := len(full) / 2
	chunk1 := full[:mid]
	chunk2 := full[mid:]

	seqs := scanAll(chunk1, chunk2)
	if len(seqs) != 1 {
		t.Fatalf("multi-chunk: expected 1 sequence, got %d", len(seqs))
	}
	if !bytes.Equal(seqs[0], full) {
		t.Errorf("multi-chunk: forwarded = %q, want %q", seqs[0], full)
	}
}

// ---------------------------------------------------------------------------
// oscBuffer: bursty hyperlink output (ls --hyperlink=auto on a large dir)
// must never drop OSC 8 closes — that was the chan<-based bug.
// ---------------------------------------------------------------------------

func TestOSCBuffer_NoDropsUnderBurst(t *testing.T) {
	// Regression test for the chan<-based design that dropped OSC sequences
	// under burst load.  Use OSC 133 prompt markers as a stand-in for any
	// forwarded sequence; the buffer must accumulate every one.
	buf := newOSCBuffer()
	var s oscScanner
	const n = 5000
	one := []byte("\x1b]133;A\x07")
	var stream []byte
	for i := 0; i < n; i++ {
		stream = append(stream, one...)
	}
	s.Scan(stream, buf.append)

	var sink bytes.Buffer
	buf.flush(&sink)

	got := bytes.Count(sink.Bytes(), one)
	if got != n {
		t.Errorf("forwarded %d/%d OSC 133 markers under burst (was the dropped-OSC bug)", got, n)
	}
}

func TestOSCBuffer_FlushClearsBuffer(t *testing.T) {
	buf := newOSCBuffer()
	buf.append([]byte("\x1b]7;file:///x\x07"))
	var sink bytes.Buffer
	buf.flush(&sink)
	if sink.Len() == 0 {
		t.Fatal("first flush emitted nothing")
	}
	sink.Reset()
	buf.flush(&sink)
	if sink.Len() != 0 {
		t.Errorf("second flush should be empty, got %d bytes", sink.Len())
	}
}

func TestOSCBuffer_CapEnforced(t *testing.T) {
	buf := newOSCBuffer()
	// Fill near the cap, then try to overshoot.
	big := make([]byte, oscBufferMax-10)
	buf.append(big)
	buf.append([]byte("12345678901234567890")) // 20 bytes; would exceed cap, dropped
	var sink bytes.Buffer
	buf.flush(&sink)
	if sink.Len() != len(big) {
		t.Errorf("buffered = %d, want %d (over-cap append should be dropped)", sink.Len(), len(big))
	}
}

// ---------------------------------------------------------------------------
// Sanitization: binary content from `cat /bin/ls` must not reach the host
// terminal. Each forwarded OSC has format-specific validation.
// ---------------------------------------------------------------------------

func TestSanitize_OSC52_BinaryDropped(t *testing.T) {
	// OSC 52 with a payload containing raw control bytes — exactly the kind
	// of garbage `cat /bin/ls` produces. Must be dropped, not forwarded.
	data := []byte("\x1b]52;c;\x01\x02\x03ABC\x07")
	seqs := scanAll(data)
	if len(seqs) != 0 {
		t.Errorf("binary OSC 52 should be dropped, got %d sequences: %q", len(seqs), seqs)
	}
}

func TestSanitize_OSC52_BadBase64Dropped(t *testing.T) {
	// Characters outside the base64 alphabet (e.g. '!' or space) → drop.
	data := []byte("\x1b]52;c;Hello World!\x07")
	seqs := scanAll(data)
	if len(seqs) != 0 {
		t.Errorf("non-base64 OSC 52 payload should be dropped, got %d", len(seqs))
	}
}

func TestSanitize_OSC52_BadSelectionDropped(t *testing.T) {
	// 'X' is not a valid selection char.
	data := []byte("\x1b]52;X;SGVsbG8=\x07")
	seqs := scanAll(data)
	if len(seqs) != 0 {
		t.Errorf("OSC 52 with invalid selection should be dropped, got %d", len(seqs))
	}
}

func TestSanitize_OSC52_QueryAccepted(t *testing.T) {
	data := []byte("\x1b]52;c;?\x07")
	seqs := scanAll(data)
	if len(seqs) != 1 {
		t.Fatalf("OSC 52 query should pass, got %d", len(seqs))
	}
}

func TestSanitize_OSC52_EmptyDataAccepted(t *testing.T) {
	// Empty data = clear clipboard, valid.
	data := []byte("\x1b]52;c;\x07")
	seqs := scanAll(data)
	if len(seqs) != 1 {
		t.Fatalf("OSC 52 empty data (clear) should pass, got %d", len(seqs))
	}
}

func TestOSC8_NotForwarded(t *testing.T) {
	// OSC 8 must not be forwarded by the scanner — vt10x parses it into
	// per-glyph hyperlink IDs and the renderer emits OSC 8 inline with cell
	// paint via tcell.Style.Url. Forwarding here would emit the open/close
	// pair before any characters are drawn, attributing the link to nothing.
	data := []byte("\x1b]8;;https://example.com/\x1b\\")
	seqs := scanAll(data)
	if len(seqs) != 0 {
		t.Errorf("OSC 8 should not be forwarded by the scanner, got %d sequences", len(seqs))
	}
}

func TestSanitize_OSC7_BinaryDropped(t *testing.T) {
	data := []byte("\x1b]7;file:///\x01\x02\x03\x07")
	seqs := scanAll(data)
	if len(seqs) != 0 {
		t.Errorf("OSC 7 with binary content should be dropped, got %d", len(seqs))
	}
}

func TestSanitize_OSC7_UnicodePathPasses(t *testing.T) {
	data := []byte("\x1b]7;file:///home/üser/café\x07")
	seqs := scanAll(data)
	if len(seqs) != 1 {
		t.Fatalf("OSC 7 with Unicode path should pass, got %d", len(seqs))
	}
}

func TestSanitize_OSC133_ASCIIPasses(t *testing.T) {
	data := []byte("\x1b]133;A;cl=cmd\x07")
	seqs := scanAll(data)
	if len(seqs) != 1 {
		t.Fatalf("OSC 133 ASCII should pass, got %d", len(seqs))
	}
}

func TestSanitize_OSC133_BinaryDropped(t *testing.T) {
	data := []byte("\x1b]133;A;\x01\x02\x07")
	seqs := scanAll(data)
	if len(seqs) != 0 {
		t.Errorf("OSC 133 with control bytes should be dropped, got %d", len(seqs))
	}
}

// ---------------------------------------------------------------------------
// Large OSC exceeding oscMaxBuf — dropped
// ---------------------------------------------------------------------------

func TestOSC_OverrunDropped(t *testing.T) {
	// Build an OSC 52 sequence whose body exceeds oscMaxBuf.
	var buf []byte
	buf = append(buf, "\x1b]52;c;"...)
	// Pad with 'A' to exceed the cap.
	pad := make([]byte, oscMaxBuf+100)
	for i := range pad {
		pad[i] = 'A'
	}
	buf = append(buf, pad...)
	buf = append(buf, 0x07) // BEL terminator

	seqs := scanAll(buf)
	if len(seqs) != 0 {
		t.Errorf("oversize OSC should be dropped, got %d sequences", len(seqs))
	}
}
