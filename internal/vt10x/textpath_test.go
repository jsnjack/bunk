package vt10x

import (
	"bytes"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

func TestSimpleGraphemes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		runes  string
		simple bool
	}{
		{"latin", "aZ~ é€", true},
		{"box drawing", "─│┌┐└┘╭╮█▒⣿", true},
		{"wide", "日本語テキスト\U00020000", true},
		{"hangul syllables", "가각한국어", true},
		{"emoji", "👍✅🚀\U0001f600", true},
		{"controls", "\u0085\u200b\ufffd", true},
		{"extend", "\u0301\ufe0f\U0001f3fd\u20e3\u094d", false},
		{"zwj", "\u200d", false},
		{"spacing mark", "\u0903", false},
		{"prepend", "\u0600", false},
		{"regional indicator", "\U0001f1e9\U0001f1ea", false},
		{"hangul jamo", "\u1100\u1161\u11a8\ua960\ud7b0\ud7cb", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, c := range tc.runes {
				if got := runeInfo(c)&infoSimple != 0; got != tc.simple {
					t.Errorf("%U: simple = %v, want %v", c, got, tc.simple)
				}
			}
		})
	}
	t.Run("printable ASCII", func(t *testing.T) {
		for c := rune(0x20); c < 0x7f; c++ {
			if !simpleGrapheme(c) {
				t.Errorf("%U is not simple", c)
			}
		}
	})
	t.Run("width", func(t *testing.T) {
		for c := rune(0x80); c < cachedWidthRunes; c++ {
			want := min(2, max(1, uniseg.StringWidth(string(c))))
			if got := runeCellWidth(c); got != want {
				t.Fatalf("%U: width %d, want %d", c, got, want)
			}
		}
	})
}

// TestSimpleGraphemesNeverJoin checks the claim putText relies on over
// random pairs of simple runes, and that the rune parser keeps each in its
// own cell.
func TestSimpleGraphemesNeverJoin(t *testing.T) {
	var simple []rune
	for c := rune(0x20); c < cachedWidthRunes; c++ {
		if c < 0x7f || c >= 0x80 && runeInfo(c)&infoSimple != 0 {
			simple = append(simple, c)
		}
	}
	if len(simple) < 100000 {
		t.Fatalf("only %d simple runes", len(simple))
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 200000 {
		a, b := simple[rng.IntN(len(simple))], simple[rng.IntN(len(simple))]
		if joins(a, b) {
			t.Fatalf("simple runes %U and %U join", a, b)
		}
	}
	term := New(WithSize(8, 2)).(*terminal)
	for range 2000 {
		a, b := simple[rng.IntN(len(simple))], simple[rng.IntN(len(simple))]
		term.lock()
		term.clear(0, 0, 7, 1)
		term.moveTo(0, 0)
		term.clusterOpen = false
		term.put(a)
		x := term.cur.X
		term.put(b)
		got := term.lines[0].Glyph(0)
		term.unlock()
		if got.Combining != "" || term.clusterX != x {
			t.Fatalf("%U %U: rune parser joined them: %+v", a, b, got)
		}
	}
}

// clusterRunes mixes code points of every grapheme break property.
var clusterRunes = []rune("a \u0085é日\uac00\uac01\U0001f600👍✅" +
	"\u0301\ufe0f\U0001f3fd\u20e3\u094d\u200d\u0903\u0600\U000110bd" +
	"\U0001f1e9\U0001f1ea\u1100\u1161\u11a8\U000e0001")

func TestEndsCluster(t *testing.T) {
	for _, tc := range []struct {
		runes string
		ends  bool
	}{
		{"aé日\uac00👍\u0301\ufe0f\U0001f3fd\u20e3\U0001f1e9\u1161\u11a8\u0903", true},
		{"\u200d\u0600\U000110bd\u1100\ua960", false},
	} {
		for _, c := range tc.runes {
			if got := runeInfo(c)&infoEndsCluster != 0; got != tc.ends {
				t.Errorf("%U: endsCluster = %v, want %v", c, got, tc.ends)
			}
		}
	}
	// Random clusters: when the last code point ends clusters, no simple
	// rune joins; otherwise some must, or the test checks nothing.
	var simple []rune
	for c := rune(0x80); c < 0x3000; c++ {
		if runeInfo(c)&infoSimple != 0 {
			simple = append(simple, c)
		}
	}
	simple = append(simple, 'a', '\uac00', '\uac01', '\U0001f600')
	rng := rand.New(rand.NewPCG(7, 8))
	joined := 0
	for range 20000 {
		var text []byte
		for range 1 + rng.IntN(5) {
			text = utf8.AppendRune(text, clusterRunes[rng.IntN(len(clusterRunes))])
		}
		cluster, _, _, _ := uniseg.FirstGraphemeCluster(text, -1)
		last, _ := utf8.DecodeLastRune(cluster)
		ends := endsCluster(last)
		for range 8 {
			c := simple[rng.IntN(len(simple))]
			if rng.IntN(2) == 0 {
				c = simple[len(simple)-1-rng.IntN(4)]
			}
			next := utf8.AppendRune(append([]byte(nil), cluster...), c)
			got, _, _, _ := uniseg.FirstGraphemeCluster(next, -1)
			if len(got) != len(cluster) {
				if ends {
					t.Fatalf("%+q joins the cluster %+q", c, cluster)
				}
				joined++
			}
		}
	}
	if joined == 0 {
		t.Fatal("no simple rune joined any cluster")
	}
}

// TestClusterCache checks cached segmentations against fresh ones while
// many texts compete for the slots.
func TestClusterCache(t *testing.T) {
	var cc clusterCache
	rng := rand.New(rand.NewPCG(9, 10))
	texts := make([][]byte, 300)
	for i := range texts {
		for range 2 + rng.IntN(4) {
			texts[i] = utf8.AppendRune(texts[i], clusterRunes[rng.IntN(len(clusterRunes))])
		}
	}
	for range 100000 {
		text := texts[rng.IntN(len(texts))]
		if rng.IntN(2) == 0 {
			text = texts[rng.IntN(8)] // a few hot texts
		}
		ch, lead := utf8.DecodeRune(text)
		c, _ := utf8.DecodeLastRune(text)
		var want clusterEntry
		want.segment(text, lead, ch, c)
		got := cc.lookup(text, lead, ch, c)
		if *got != want {
			t.Fatalf("%+q: cached %+v, segmented %+v", text, *got, want)
		}
		if again := cc.lookup(text, lead, ch, c); again != got {
			t.Fatalf("%+q: second lookup moved the entry", text)
		}
	}
	want := clusterEntry{text: "e\u0301", suffix: "\u0301", width: 1, joins: true, zeroWidth: true}
	if got := cc.lookup([]byte(want.text), 1, 'e', '\u0301'); *got != want {
		t.Fatalf("e + U+0301: got %+v, want %+v", *got, want)
	}
}

// fastPieces are the units FuzzFastPaths builds streams from: text the fast
// paths take, runes that send them back to parse, and the controls, control
// sequences and modes that change how text is written.
var fastPieces = []string{
	// Simple text.
	"a", "hello ", "x\x1b[1my", "─", "│┌┐└┘", "█▒⣿", "é", "€", "日本", "한국어", "テ",
	"👍", "✅", "\U0001f600", "\ufffd", "\u0085", "\u200b", "\U00020000", "\U0002ffff", "\u2e3a", "\u2e3b",
	// Runes that join, are uncached, or need parse.
	"\u0301", "\u200d", "\ufe0f", "\U0001f3fd", "\u20e3", "\u0600", "\u0903",
	"\U0001f1e9", "\U0001f1ea", "\u1100", "\u1161", "\u11a8", "क\u094d",
	"\U00030000", "\U000e0001", "\U0010fffd",
	// Controls and invalid UTF-8.
	"\r", "\n", "\r\n", "\b", "\t", "\x0e", "\x07", "\x7f", "\x00", "\x01",
	"\xff", "\xe6\x97", "\xc3", "\x80",
	// SGR, including forms that put state into the side table.
	"\x1b[m", "\x1b[0m", "\x1b[1;31m", "\x1b[7m", "\x1b[38;2;1;2;3m", "\x1b[38:2::1:2:3m",
	"\x1b[48;5;236m", "\x1b[4:3m", "\x1b[58;5;3m", "\x1b[59m", "\x1b[?5m", "\x1b[1;;4m", "\x1b[;m",
	"\x1b]8;;https://example.com\x1b\\", "\x1b]8;;\x1b\\",
	// Sequences split or interrupted: control, DEL and non-ASCII bytes
	// inside, a restart, and one past the length limit.
	"\x1b", "[", "\x1b[", "\x1b[1;", "\x1b[3\x072m", "\x1b[3\x7f1m", "\x1b[3é1m", "\x1b[\x1b[31m",
	"\x1b[3\x18m", "\x1b[" + strings.Repeat("1;", 140) + "m", "\x1b[" + strings.Repeat("9", 300),
	// Cursor, editing, scrolling and modes.
	"\x1b[H", "\x1b[3;2H", "\x1b[2J", "\x1b[K", "\x1b[2@", "\x1b[3P", "\x1b[1L", "\x1b[2M", "\x1b[3X",
	"\x1b[2;3r", "\x1b[r", "\x1b[?7l", "\x1b[?7h", "\x1b[?6h", "\x1b[?6l", "\x1b[4h", "\x1b[4l",
	"\x1b[?2027l", "\x1b[?2027h", "\x1b[?1049h", "\x1b[?1049l", "\x1b[6n", "\x1b[2 q", "\x1b[s", "\x1b[u",
	"\x1b(0", "\x1b(B", "\x1b7", "\x1b8", "\x1bM", "\x1bD", "\x1bc",
}

// FuzzFastPaths builds streams from fastPieces, one piece per input byte, so
// the fuzzer combines interesting runes and sequences instead of searching
// for valid UTF-8. Write must match the rune parser.
func FuzzFastPaths(f *testing.F) {
	f.Add(uint8(7), uint8(3), []byte{5, 1}, []byte{0, 3, 1, 38, 9, 4, 20, 22, 21})
	f.Add(uint8(1), uint8(1), []byte{}, []byte{9, 4, 18, 5, 56})
	f.Add(uint8(11), uint8(4), []byte{2}, []byte("the parser writes text"))
	f.Fuzz(func(t *testing.T, cols, rows uint8, cuts, picks []byte) {
		if len(picks) > 2048 {
			return
		}
		defer watchdog()()
		var data []byte
		for _, p := range picks {
			data = append(data, fastPieces[int(p)%len(fastPieces)]...)
		}
		c, r := 1+int(cols)%24, 1+int(rows)%8
		checkWrites(t, c, r, cuts, data)
	})
}

// TestFastPathsMatchRuneParser runs FuzzFastPaths on random streams, so
// plain go test covers more than the seeds.
func TestFastPathsMatchRuneParser(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for range 300 {
		picks := make([]byte, 1+rng.IntN(400))
		for i := range picks {
			picks[i] = byte(rng.IntN(256))
		}
		var data []byte
		for _, p := range picks {
			data = append(data, fastPieces[int(p)%len(fastPieces)]...)
		}
		checkWrites(t, 1+rng.IntN(24), 1+rng.IntN(8), []byte{byte(rng.IntN(64))}, data)
	}
}

// referenceCSIParse is csiEscape.parse before its one-pass loop for
// decimal parameters.
func referenceCSIParse(buf []byte) (args []int, mode byte, priv bool) {
	mode = buf[len(buf)-1]
	if len(buf) == 1 {
		return nil, mode, false
	}
	s := buf
	if s[0] == '?' {
		priv = true
		s = s[1:]
	}
	s = s[:len(s)-1]
	for {
		p, rest, more := cutByte(s, ';')
		if idx := bytes.IndexByte(p, ':'); idx >= 0 {
			main, err := atoi(p[:idx])
			if err != nil {
				return args, mode, priv
			}
			first, subRest, subMore := cutByte(p[idx+1:], ':')
			sub, err := atoi(first)
			if err != nil {
				sub = 0
			}
			args = append(args, main*10000+sub+1)
			for subMore {
				var sp []byte
				sp, subRest, subMore = cutByte(subRest, ':')
				v, err := atoi(sp)
				if err != nil {
					break
				}
				args = append(args, v)
			}
		} else {
			i, err := atoi(p)
			if err != nil {
				return args, mode, priv
			}
			args = append(args, i)
		}
		if !more {
			return args, mode, priv
		}
		s = rest
	}
}

func FuzzCSIParams(f *testing.F) {
	for _, seed := range []string{"m", "0m", "1;31m", "38;2;1;2;3m", "38:2::1:2:3m", "4:3m", "?25h", "1;;2m",
		";m", "1;m", "99999999;7H", "1:m", "1:2:x;3m", "1>2:3m", "12:3>4;5m", "2 q", "?", "?m", "1;2"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, buf []byte) {
		if len(buf) == 0 || len(buf) > maxCSIBytes {
			return
		}
		var c csiEscape
		c.buf = append(c.buf, buf...)
		c.parse()
		args, mode, priv := referenceCSIParse(buf)
		if len(c.args) == 0 {
			c.args = nil
		}
		if !reflect.DeepEqual(c.args, args) || c.mode != mode || c.priv != priv {
			t.Fatalf("%q: parse gives %v %q %v, reference %v %q %v", buf, c.args, c.mode, c.priv, args, mode, priv)
		}
	})
}

func TestPrintableASCII(t *testing.T) {
	testByteRun(t, printableASCII, func(b byte) bool { return b >= 0x20 && b < 0x7f })
}

// testByteRun checks run, which returns the length of the leading run of
// bytes in p that match in, against a byte-by-byte loop.
func testByteRun(t *testing.T, run func(p []byte) int, in func(b byte) bool) {
	t.Helper()
	naive := func(p []byte) int {
		n := 0
		for n < len(p) && in(p[n]) {
			n++
		}
		return n
	}
	// Every byte value at every position of a 17-byte run of each byte in
	// the class, so each word lane and the tail see each value.
	for fill := 0; fill < 256; fill++ {
		if !in(byte(fill)) {
			continue
		}
		for pos := 0; pos < 17; pos++ {
			for b := 0; b < 256; b++ {
				p := bytes.Repeat([]byte{byte(fill)}, 17)
				p[pos] = byte(b)
				if got, want := run(p), naive(p); got != want {
					t.Fatalf("%q: got %d, want %d", p, got, want)
				}
			}
		}
	}
	rng := rand.New(rand.NewPCG(5, 6))
	for range 10000 {
		p := make([]byte, rng.IntN(40))
		for i := range p {
			p[i] = byte(0x1e + rng.IntN(0x64))
			if rng.IntN(50) == 0 {
				p[i] = byte(rng.IntN(256))
			}
		}
		if got, want := run(p), naive(p); got != want {
			t.Fatalf("%q: got %d, want %d", p, got, want)
		}
	}
}
