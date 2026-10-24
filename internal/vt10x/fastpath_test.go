package vt10x

import (
	"bytes"
	"fmt"
	"io"
	"math/rand/v2"
	"reflect"
	"testing"
	"unicode"
)

// writeOneRuneAtATime is the parser loop as it was before the ASCII fast
// path: every rune goes through put.
func writeOneRuneAtATime(t *terminal, p []byte) int {
	var written int
	r := bytes.NewReader(p)
	for {
		c, sz, err := r.ReadRune()
		if err == io.EOF {
			return written
		}
		written += sz
		if c == unicode.ReplacementChar && sz == 1 {
			if r.Len() == 0 {
				return written - 1
			}
			continue
		}
		t.put(c)
	}
}

var fastPathPieces = []string{
	"\r", "\n", "\r\n", "\b", "\t", "\x0e", "\x0f", "\x1b(0", "\x1b(B",
	"\x1b[1m", "\x1b[7m", "\x1b[0m", "\x1b[31m", "\x1b[38;5;200m", "\x1b[1;7;33;44m",
	"\x1b[4h", "\x1b[4l", "\x1b[?7l", "\x1b[?7h", "\x1b[?6h", "\x1b[?6l",
	"\x1b[?2027l", "\x1b[?2027h", "\x1b[?1049h", "\x1b[?1049l",
	"\x1b[2@", "\x1b[3P", "\x1b[2L", "\x1b[1M", "\x1b[K", "\x1b[1K", "\x1b[2J", "\x1b[J",
	"\x1b7", "\x1b8", "\x1bM", "\x1bD", "\x1bE", "\x1b]8;;https://example.com\x1b\\", "\x1b]8;;\x1b\\",
	"\u00e9", "e\u0301", "\u0301", "\u65e5\u672c", "\U0001f44d\U0001f3fd", "\U0001f468\u200d\U0001f469\u200d\U0001f467", "\u200d", "\u0600", "\u0600a",
	"1\ufe0f\u20e3", "#\u20e3", "\U0001f1e9\U0001f1ea", "\uff57\uff49\uff44\uff45",
}

func randomStream(rng *rand.Rand, cols, rows int) []byte {
	var b bytes.Buffer
	for b.Len() < 4096 {
		switch rng.IntN(5) {
		case 0, 1:
			n := 1 + rng.IntN(2*cols)
			for i := 0; i < n; i++ {
				b.WriteByte(byte(0x20 + rng.IntN(0x5f)))
			}
		case 2:
			fmt.Fprintf(&b, "\x1b[%d;%dH", 1+rng.IntN(rows+2), 1+rng.IntN(cols+2))
		case 3:
			fmt.Fprintf(&b, "\x1b[%d;%dr", 1+rng.IntN(rows), 1+rng.IntN(rows))
		default:
			b.WriteString(fastPathPieces[rng.IntN(len(fastPathPieces))])
		}
	}
	return b.Bytes()
}

// wideCellError reports a wide lead without its continuation, or the reverse.
func wideCellError(s *State) string {
	for y, l := range s.lines {
		for x, g := range l {
			if g.Width == -1 && (x == 0 || l[x-1].Width != 2) {
				return fmt.Sprintf("orphan continuation at (%d,%d)", x, y)
			}
			if g.Width == 2 && (x+1 == len(l) || l[x+1].Width != -1) {
				return fmt.Sprintf("wide lead without continuation at (%d,%d)", x, y)
			}
		}
	}
	return ""
}

func TestASCIIFastPathMatchesRuneParser(t *testing.T) {
	for seed := uint64(1); seed <= 300; seed++ {
		rng := rand.New(rand.NewPCG(seed, 0))
		cols, rows := 1+rng.IntN(30), 1+rng.IntN(12)
		data := randomStream(rng, cols, rows)

		var fastRows, slowRows [][]Glyph
		record := func(dst *[][]Glyph) func([]Glyph) []Glyph {
			return func(row []Glyph) []Glyph { *dst = append(*dst, append([]Glyph(nil), row...)); return nil }
		}
		fast := New(WithSize(cols, rows), WithScrollCallback(record(&fastRows))).(*terminal)
		slow := New(WithSize(cols, rows), WithScrollCallback(record(&slowRows))).(*terminal)

		for off := 0; off < len(data); {
			end := min(len(data), off+1+rng.IntN(64))
			n, err := fast.Write(data[off:end])
			if err != nil {
				t.Fatal(err)
			}
			slow.lock()
			m := writeOneRuneAtATime(slow, data[off:end])
			slow.unlock()
			if n != m {
				t.Fatalf("seed %d: Write consumed %d bytes, rune loop %d", seed, n, m)
			}
			off += max(n, 1)
			if msg := wideCellError(fast.State); msg != "" {
				t.Fatalf("seed %d (%dx%d): %s\nstream: %q", seed, cols, rows, msg, data[:off])
			}
		}

		fs, ss := fast.State, slow.State
		for _, f := range []struct {
			name       string
			fast, slow any
		}{
			{"lines", fs.lines, ss.lines},
			{"altLines", fs.altLines, ss.altLines},
			{"cursor", fs.cur, ss.cur},
			{"saved cursor", fs.curSaved, ss.curSaved},
			{"mode", fs.mode, ss.mode},
			{"state", fs.state, ss.state},
			{"cluster", [3]any{fs.clusterOpen, fs.clusterX, fs.clusterY}, [3]any{ss.clusterOpen, ss.clusterX, ss.clusterY}},
			{"dirty", fs.dirty, ss.dirty},
			{"changed", fs.changed, ss.changed},
			{"scrolled rows", fastRows, slowRows},
		} {
			if !reflect.DeepEqual(f.fast, f.slow) {
				t.Fatalf("seed %d (%dx%d): %s differ\nfast: %v\nslow: %v\nstream: %q", seed, cols, rows, f.name, f.fast, f.slow, data)
			}
		}
	}
}
