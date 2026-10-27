package session

import (
	"bunk/internal/termcorpus"
	"bytes"
	"slices"
	"testing"
)

// TestFindAllMatchesOracle compares FindAll with the search it replaced on
// every kind of scrollback reflow builds and on the realistic streams, at
// two widths, with queries that end in a space, span wide characters or
// hold a combining mark.
func TestFindAllMatchesOracle(t *testing.T) {
	t.Parallel()
	queries := []string{"the", "E", "a ", "  ", "1 ", "link-", "日本", "😀🎉", "é", "́", "✻ thinking", "⎿", "w0000"}
	feeds := map[string][]byte{}
	for _, kind := range reflowKinds {
		var buf bytes.Buffer
		for i := range 400 {
			reflowLine(&buf, kind, i)
		}
		feeds[kind] = buf.Bytes()
	}
	for _, kind := range termcorpus.Kinds {
		feeds[kind] = termcorpus.Generate(kind, 64<<10)
	}
	feeds["combining"] = bytes.Repeat([]byte("é É́ café \x1b[1mTHE\x1b[m end   \r\n"), 50)
	for kind, data := range feeds {
		for _, cols := range []int{80, 200} {
			s := newTestSession(cols, 24)
			s.ScrollbackLines, s.Scrollback = 300, Ring{MaxLines: 300}
			s.Feed(data)
			found := 0
			for _, q := range queries {
				got, want := s.FindAll(q), s.oracleFindAll(q)
				if !slices.Equal(got, want) {
					t.Errorf("%s at %d columns, %q: %d matches, oracle %d\n got  %v\nwant %v", kind, cols, q, len(got), len(want), got, want)
				}
				found += len(got)
			}
			if found == 0 {
				t.Errorf("%s at %d columns: no query matched, so nothing was compared", kind, cols)
			}
			// After a resize, scrollback rows were laid out at another
			// width.
			s.ResizeAndReflow(cols/2+3, 20)
			for _, q := range queries {
				if got, want := s.FindAll(q), s.oracleFindAll(q); !slices.Equal(got, want) {
					t.Errorf("%s resized from %d columns, %q: %d matches, oracle %d", kind, cols, q, len(got), len(want))
				}
			}
		}
	}
}

// BenchmarkFind searches a coding-agent transcript in 10 000 rows of
// scrollback and a 200×50 screen. "word" is a typed word; "space" ends in a
// space, so the blanks past each row's text are searched too.
func BenchmarkFind(b *testing.B) {
	s := newTestSession(200, 50)
	s.ScrollbackLines, s.Scrollback = 10000, Ring{MaxLines: 10000}
	data := termcorpus.Generate("claude", 1<<20)
	for s.Scrollback.Count < s.ScrollbackLines {
		s.Feed(data)
	}
	for _, c := range []struct{ name, query string }{{"word", "grapheme"}, {"space", "rune "}} {
		b.Run(c.name, func(b *testing.B) {
			if len(s.FindAll(c.query)) == 0 {
				b.Fatalf("%q matches nothing", c.query)
			}
			b.ReportAllocs()
			for b.Loop() {
				s.FindAll(c.query)
			}
		})
	}
}
