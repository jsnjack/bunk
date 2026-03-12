package main

import (
	"bytes"
	"testing"
)

// OSC scanner tests: clipboard paste (OSC 52) and directory tracking (OSC 7)
//
// Bug: clipboard paste not working, CWD not updating in pane title
//
// These sequences must be intercepted from the PTY stream and forwarded
// to the host terminal or bunk's own handlers.

// helper: collect all sequences emitted by scanning chunk(s).
func scanAll(chunks ...[]byte) [][]byte {
	ch := make(chan []byte, 16)
	var s oscScanner
	for _, c := range chunks {
		s.Scan(c, ch)
	}
	close(ch)
	var out [][]byte
	for seq := range ch {
		out = append(out, seq)
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
// Channel full — sequence dropped (no panic)
// ---------------------------------------------------------------------------

func TestOSC_ChannelFull(t *testing.T) {
	// Use a channel with zero capacity so it is always full.
	ch := make(chan []byte)
	var s oscScanner
	data := []byte("\x1b]7;file:///tmp\x07")
	// This must not panic or block.
	s.Scan(data, ch)
	// Nothing should be in the channel (it has no buffer).
	select {
	case <-ch:
		t.Error("expected channel to have no sequence (dropped), but got one")
	default:
		// OK — sequence was dropped as expected.
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
