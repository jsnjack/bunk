package session

import (
	"bytes"
	"fmt"
	"testing"
)

// BenchmarkOutputUniqueLinks feeds lines that each carry a new OSC 8 URL
// into a session with a 10,000-line scrollback, so link sweeps run along.
// Each op is 60,000 lines in a fresh session: below the 65,535-URL table
// limit, where a table that is never swept does the same work.
func BenchmarkOutputUniqueLinks(b *testing.B) {
	var buf bytes.Buffer
	for i := 0; i < 60000; i++ {
		fmt.Fprintf(&buf, "%06d \x1b]8;;https://example.com/file/%d\x1b\\link\x1b]8;;\x1b\\ tail\r\n", i, i)
	}
	data := buf.Bytes()
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		s := newTestSession(200, 50)
		s.ScrollbackLines, s.Scrollback = 10000, Ring{MaxLines: 10000}
		var stream ptyStream
		b.StartTimer()
		for off := 0; off < len(data); off += 32768 {
			chunk := stream.scan(data[off:min(off+32768, len(data))])
			s.Lock()
			s.CaptureAndWrite(chunk)
			s.Unlock()
		}
	}
}

// BenchmarkLinkSweep measures one link sweep over a full 10,000-line
// scrollback of 100-column rows with two links each, plus the screen. The
// table holds 20,000 live URLs, so the next sweep is due after 20,000 new
// ones.
func BenchmarkLinkSweep(b *testing.B) {
	s := newTestSession(100, 30)
	s.ScrollbackLines, s.Scrollback = 10000, Ring{MaxLines: 10000}
	var buf bytes.Buffer
	for i := 0; i < 10030; i++ {
		fmt.Fprintf(&buf, "%06d \x1b]8;;https://example.com/a/%d\x1b\\first\x1b]8;;\x1b\\ %s \x1b]8;;https://example.com/b/%d\x1b\\second\x1b]8;;\x1b\\\r\n",
			i, i, bytes.Repeat([]byte{'x'}, 60), i)
	}
	s.Lock()
	defer s.Unlock()
	s.Feed(buf.Bytes())
	s.Term.SweepLinks()
	b.ReportAllocs()
	for b.Loop() {
		s.Term.SweepLinks()
	}
	b.ReportMetric(float64(s.Term.LinkStats().URLs), "urls")
}
