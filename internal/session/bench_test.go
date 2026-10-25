package session

import (
	"bytes"
	"fmt"
	"testing"
)

// BenchmarkOutput measures the per-read work ReadLoop does with PTY output:
// framing, OSC scanning, query replies, vt10x parsing and scrollback capture.
// Front-end hooks are left out.
func BenchmarkOutput(b *testing.B) {
	for _, kind := range []string{"ascii", "full-rows", "sgr"} {
		var buf bytes.Buffer
		for i := 0; buf.Len() < 1<<20; i++ {
			switch kind {
			case "ascii":
				fmt.Fprintf(&buf, "%06d the quick brown fox jumps over the lazy dog 0123456789 abcdefghijklmnopqrstuvwxyz\r\n", i)
			case "full-rows":
				fmt.Fprintf(&buf, "%06d %s\r\n", i, bytes.Repeat([]byte{'a' + byte(i%26)}, 193))
			default:
				fmt.Fprintf(&buf, "\x1b[38;2;%d;%d;%dm%06d\x1b[0m \x1b[1;34msrc/file_%d.go\x1b[0m:\x1b[33m%d\x1b[0m func main() {}\r\n",
					i%256, (i*7)%256, (i*13)%256, i, i%97, i%400)
			}
		}
		data := buf.Bytes()
		b.Run(kind, func(b *testing.B) {
			s := newTestSession(200, 50)
			s.ScrollbackLines, s.Scrollback = 10000, Ring{MaxLines: 10000}
			var stream ptyStream
			emit := func([]byte) {}
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for b.Loop() {
				for off := 0; off < len(data); off += 32768 {
					chunk := stream.scan(data[off:min(off+32768, len(data))])
					s.oscScan.Scan(chunk, emit)
					s.Lock()
					s.CaptureAndWrite(chunk)
					s.Unlock()
				}
			}
		})
	}
}
