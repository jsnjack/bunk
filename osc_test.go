package main

import (
	"bytes"
	"testing"
)

func TestOSC52ClipboardText_DecodesClipboardWrite(t *testing.T) {
	data := []byte("\x1b]52;c;d29ya3NwYWNlL2J1bms=\x07")
	got, ok := osc52ClipboardText(data)
	if !ok {
		t.Fatal("expected OSC 52 clipboard write to decode")
	}
	if got != "workspace/bunk" {
		t.Errorf("decoded text = %q, want %q", got, "workspace/bunk")
	}
}

func TestOSC52ClipboardText_STTerminator(t *testing.T) {
	data := []byte("\x1b]52;c;SGVsbG8=\x1b\\")
	got, ok := osc52ClipboardText(data)
	if !ok {
		t.Fatal("expected ST-terminated OSC 52 clipboard write to decode")
	}
	if got != "Hello" {
		t.Errorf("decoded text = %q, want %q", got, "Hello")
	}
}

func TestOSC52ClipboardText_UnpaddedBase64(t *testing.T) {
	data := []byte("\x1b]52;c;SGVsbG8\x07")
	got, ok := osc52ClipboardText(data)
	if !ok {
		t.Fatal("expected unpadded OSC 52 clipboard write to decode")
	}
	if got != "Hello" {
		t.Errorf("decoded text = %q, want %q", got, "Hello")
	}
}

func TestOSC52ClipboardText_EmptySelectionTargetsClipboard(t *testing.T) {
	data := []byte("\x1b]52;;SGVsbG8=\x07")
	got, ok := osc52ClipboardText(data)
	if !ok {
		t.Fatal("expected empty OSC 52 selection to target clipboard")
	}
	if got != "Hello" {
		t.Errorf("decoded text = %q, want %q", got, "Hello")
	}
}

func TestOSC52ClipboardText_IgnoresQueriesAndPrimary(t *testing.T) {
	for _, data := range [][]byte{
		[]byte("\x1b]52;c;?\x07"),
		[]byte("\x1b]52;p;SGVsbG8=\x07"),
	} {
		if got, ok := osc52ClipboardText(data); ok {
			t.Errorf("osc52ClipboardText(%q) = %q, true; want ignored", data, got)
		}
	}
}

func TestOSC52ClipboardText_IgnoresClearClipboard(t *testing.T) {
	data := []byte("\x1b]52;c;\x07")
	if got, ok := osc52ClipboardText(data); ok {
		t.Errorf("osc52ClipboardText(%q) = %q, true; want clear request ignored", data, got)
	}
}

func TestOSC52ClipboardText_IgnoresBinaryClipboardData(t *testing.T) {
	data := []byte("\x1b]52;c;//4=\x07")
	if got, ok := osc52ClipboardText(data); ok {
		t.Errorf("osc52ClipboardText(%q) = %q, true; want binary data ignored", data, got)
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
	const n = 5000
	one := []byte("\x1b]133;A\x07")
	for i := 0; i < n; i++ {
		buf.append(one)
	}

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
