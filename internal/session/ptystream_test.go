package session

import (
	"bunk/internal/graphics"
	"bytes"
	"strings"
	"testing"
)

func TestPTYStreamBoundedRecovery(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, start, end string }{
		{"OSC BEL", "\x1b]0;", "\x07"},
		{"OSC ST", "\x1b]0;", "\x1b\\"},
		{"DCS", "\x1bP", "\x1b\\"},
		{"APC", "\x1b_", "\x1b\\"},
		{"CSI", "\x1b[", "m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, step := range []int{1, 4096, 2 * oscMaxBuf} {
				var stream ptyStream
				input := []byte("before" + tc.start + strings.Repeat("1", oscMaxBuf+1) + tc.end + "after界")
				var got []byte
				for start := 0; start < len(input); start += step {
					got = append(got, stream.scan(input[start:min(start+step, len(input))])...)
					if len(stream.pending) > oscMaxBuf || len(stream.utf8) > 3 {
						t.Fatal("unbounded pending input")
					}
				}
				if string(got) != "beforeafter界" {
					t.Fatalf("chunk size %d: got %q", step, got)
				}
			}
		})
	}
}

func TestPTYStreamEveryBoundary(t *testing.T) {
	for _, input := range []string{"abc界😀", "A\x1b]8;;https://example.org/界\x1b\\B", "\x1b[?1049h\x1b[?2004h", "\x1bP+q536d756c78\x1b\\", "\x1b(0q\x1b(B"} {
		t.Run(input, func(t *testing.T) {
			for split := 0; split <= len(input); split++ {
				var stream ptyStream
				got := append([]byte(nil), stream.scan([]byte(input[:split]))...)
				got = append(got, stream.scan([]byte(input[split:]))...)
				if !bytes.Equal(got, []byte(input)) {
					t.Fatalf("split %d: got %q", split, got)
				}
			}
		})
	}
}

func TestPTYStreamCancellation(t *testing.T) {
	for _, cancel := range []byte{0x18, 0x1a} {
		var stream ptyStream
		stream.scan([]byte("\x1b]0;unfinished"))
		got := stream.scan(append([]byte{cancel}, []byte("recovered")...))
		if string(got) != "recovered" {
			t.Fatalf("cancel %x: got %q", cancel, got)
		}
	}
}

func TestGraphicsFramingBounds(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"\x1b_G", "\x1b]1337;File=inline=1:", "\x1bPq"} {
		var stream ptyStream
		sequence := []byte(prefix + strings.Repeat("A", oscMaxBuf+1) + "\x1b\\")
		if got := stream.scan(sequence); !bytes.Equal(got, sequence) {
			t.Fatal("graphics control truncated to text limit")
		}
		if got := stream.scan([]byte(prefix + strings.Repeat("A", graphics.MaxSequenceBytes+1) + "\x1b\\recovered")); string(got) != "\x18recovered" {
			t.Fatal("oversized graphics payload leaked into text")
		}
		if len(stream.pending) != 0 || cap(stream.pending) > oscMaxBuf {
			t.Fatal("graphics buffer retained after completion")
		}
	}

}

func TestRegressionUnterminatedOSCBounded(t *testing.T) {
	data := append([]byte("\x1b]0;"), bytes.Repeat([]byte{'x'}, 2*oscMaxBuf)...)
	var stream ptyStream
	complete := stream.scan(data)
	if len(stream.pending) > oscMaxBuf {
		t.Fatalf("unterminated OSC bypasses scanner cap: complete=%d carry=%d", len(complete), len(stream.pending))
	}
	if got := string(stream.scan([]byte("\x1b\\recovered"))); got != "recovered" {
		t.Fatalf("oversized OSC recovery = %q", got)
	}
}
