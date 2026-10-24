package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"testing"

	"bunk/internal/vt10x"
)

// BenchmarkPaneOutput measures the per-read work readPTY does with PTY
// output: framing, OSC scanning, query replies, vt10x parsing and scrollback
// capture. Redraw signalling is left out.
func BenchmarkPaneOutput(b *testing.B) {
	for _, kind := range []string{"ascii", "sgr"} {
		var buf bytes.Buffer
		for i := 0; buf.Len() < 1<<20; i++ {
			if kind == "ascii" {
				fmt.Fprintf(&buf, "%06d the quick brown fox jumps over the lazy dog 0123456789 abcdefghijklmnopqrstuvwxyz\r\n", i)
			} else {
				fmt.Fprintf(&buf, "\x1b[38;2;%d;%d;%dm%06d\x1b[0m \x1b[1;34msrc/file_%d.go\x1b[0m:\x1b[33m%d\x1b[0m func main() {}\r\n",
					i%256, (i*7)%256, (i*13)%256, i, i%97, i%400)
			}
		}
		data := buf.Bytes()
		b.Run(kind, func(b *testing.B) {
			p := &Pane{cmd: &exec.Cmd{}, w: 201, h: 50, scrollbackLines: 10000, sb: sbRing{maxLines: 10000}}
			p.term = vt10x.New(vt10x.WithSize(200, 50), vt10x.WithScrollCallback(p.onScrollRow))
			osc := newOSCBuffer()
			var stream ptyStream
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for b.Loop() {
				for off := 0; off < len(data); off += 32768 {
					chunk := stream.scan(data[off:min(off+32768, len(data))])
					p.oscScan.Scan(chunk, osc.append)
					_ = isTransientLineClear(chunk)
					_ = isInPlaceLineUpdate(chunk)
					p.mu.Lock()
					p.captureAndWrite(chunk)
					p.mu.Unlock()
				}
			}
		})
	}
}
