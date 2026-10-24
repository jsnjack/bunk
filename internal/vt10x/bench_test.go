package vt10x

import (
	"bytes"
	"fmt"
	"testing"
)

// benchStream builds about 1 MiB of typical terminal output of one kind.
func benchStream(kind string) []byte {
	var b bytes.Buffer
	for i := 0; b.Len() < 1<<20; i++ {
		switch kind {
		case "ascii":
			fmt.Fprintf(&b, "%06d the quick brown fox jumps over the lazy dog 0123456789 abcdefghijklmnopqrstuvwxyz\r\n", i)
		case "sgr":
			fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm%06d\x1b[0m \x1b[1;34msrc/file_%d.go\x1b[0m:\x1b[33m%d\x1b[0m \x1b[48;5;236m func main() { fmt.Println(\"hi\") }\x1b[0m\r\n",
				i%256, (i*7)%256, (i*13)%256, i, i%97, i%400)
		case "unicode":
			fmt.Fprintf(&b, "%06d ─│┌┐└┘ héllo wörld 日本語テキスト 👍🏽 ünïcödé →←↑↓ ✓✗\r\n", i)
		case "tui":
			fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K\x1b[38;5;%dm%-80s\x1b[0m", i%50+1, i%256, fmt.Sprintf("row %d status ok", i))
		}
	}
	return b.Bytes()
}

func BenchmarkWrite(b *testing.B) {
	for _, kind := range []string{"ascii", "sgr", "unicode", "tui"} {
		data := benchStream(kind)
		b.Run(kind, func(b *testing.B) {
			t := New(WithSize(200, 50))
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for b.Loop() {
				for off := 0; off < len(data); off += 32768 {
					t.Write(data[off:min(off+32768, len(data))]) //nolint:errcheck
				}
			}
		})
	}
}
