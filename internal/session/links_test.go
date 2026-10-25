package session

import (
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"bunk/internal/leakcheck"
)

// TestLinkPastIDLimitClickable: a pane that has seen more unique URLs than
// there are link IDs keeps new links clickable. Before link IDs were
// reclaimed, the table was full after 65,535 URLs and link 65,536 had none.
func TestLinkPastIDLimitClickable(t *testing.T) {
	s := newTestSession(40, 5)
	s.Lock()
	defer s.Unlock()
	const n = 0xFFFF + 1
	var b []byte
	for i := 1; i <= n; i++ {
		b = fmt.Appendf(b[:0], "\x1b]8;;https://example.com/%d\x1b\\x\x1b]8;;\x1b\\\r\n", i)
		s.Feed(b)
	}
	g := s.Term.Cell(0, s.Term.Cursor().Y-1)
	if got, want := s.Term.Link(g.Link), fmt.Sprintf("https://example.com/%d", n); got != want {
		t.Fatalf("link %d (ID %d) resolves to %q, want %q", n, g.Link, got, want)
	}
}

// lineURL is the URL the link tests print on line n.
func lineURL(n int) string { return "https://line.example/" + strconv.Itoa(n) }

// appendLinkLine appends line n: its number, a link to lineURL(n), pad
// more columns and CR LF.
func appendLinkLine(b []byte, n, pad int) []byte {
	b = fmt.Appendf(b, "L%d \x1b]8;;%s\x1b\\link %d\x1b]8;;\x1b\\", n, lineURL(n), n)
	for range pad {
		b = append(b, '-')
	}
	return append(b, "\r\n"...)
}

// homeURL is a URL the soak repeats on many lines.
const homeURL = "https://line.example/home"

// checkLineLinks checks every logical line of the scrollback and the
// screen: each linked cell of a line that starts with a line number names
// that line's URL, and of a line that starts with "home", homeURL. The
// first line may have lost its start to the ring. It returns how many
// lines had links.
func checkLineLinks(s *Session) (int, error) {
	linked := 0
	for i, line := range logicalLines(s) {
		var text strings.Builder
		for _, g := range line {
			text.WriteRune(g.Char)
		}
		want := homeURL
		if !strings.HasPrefix(text.String(), "home ") {
			num, _, _ := strings.Cut(strings.TrimPrefix(text.String(), "L"), " ")
			n, err := strconv.Atoi(num)
			want = lineURL(n)
			if err != nil && i == 0 {
				continue // the ring evicted the line's first rows
			} else if err != nil {
				want = "no link"
			}
		}
		has := false
		for x, g := range line {
			if g.Link == 0 {
				continue
			}
			if got := s.Term.Link(g.Link); got != want {
				return linked, fmt.Errorf("logical line %d %q cell %d: ID %d names %q", i, text.String(), x, g.Link, got)
			}
			has = true
		}
		if has {
			linked++
		}
	}
	return linked, nil
}

// TestSessionLinkSweep: a sweep keeps the links of the scrollback, the
// screen and the alternate screen, and frees those of rows the ring
// evicted. ED 3 frees the scrollback's links at once.
func TestSessionLinkSweep(t *testing.T) {
	s := newTestSession(30, 4)
	s.ScrollbackLines, s.Scrollback = 5, Ring{MaxLines: 5}
	s.Lock()
	defer s.Unlock()
	var b []byte
	for n := 0; n < 8; n++ {
		b = appendLinkLine(b, n, 0)
	}
	s.Feed(b)
	s.Term.SweepLinks()
	if st := s.Term.LinkStats(); st.URLs != 8 || st.Freed != 0 {
		t.Fatalf("5 lines in the ring and 3 on screen: %+v", st)
	}
	s.Feed(appendLinkLine(appendLinkLine(appendLinkLine(nil, 8, 0), 9, 0), 10, 0))
	s.Term.SweepLinks()
	if st := s.Term.LinkStats(); st.URLs != 8 || st.Freed != 3 {
		t.Fatalf("after the ring evicted 3 lines: %+v", st)
	}
	s.Feed([]byte("\x1b[?1049h\x1b[H\x1b]8;;https://alt.example\x1b\\alt\x1b]8;;\x1b\\"))
	s.Term.SweepLinks()
	if st := s.Term.LinkStats(); st.URLs != 9 || st.Freed != 3 {
		t.Fatalf("with the alternate screen: %+v", st)
	}
	if got := s.Term.Link(s.Term.Cell(0, 0).Link); got != "https://alt.example" {
		t.Fatalf("alternate screen link: %q", got)
	}
	s.Feed([]byte("\x1b[?1049l"))
	if n, err := checkLineLinks(s); err != nil || n != 8 {
		t.Fatalf("%d linked lines: %v", n, err)
	}
	s.Feed([]byte("\x1b[3J"))
	if st := s.Term.LinkStats(); st.URLs != 3 || st.Freed != 9 {
		t.Fatalf("after ED 3: %+v", st)
	}
	if n, err := checkLineLinks(s); err != nil || n != 3 {
		t.Fatalf("%d linked lines: %v", n, err)
	}
}

// TestLinksSurviveReflowAndSweeps: reflow moves linked rows between the
// scrollback and the screen and rewraps them; sweeps between resizes keep
// every link, and the new links that reuse freed IDs name their own URLs.
func TestLinksSurviveReflowAndSweeps(t *testing.T) {
	s := newTestSession(60, 8)
	s.ScrollbackLines, s.Scrollback = 50, Ring{MaxLines: 50}
	s.Lock()
	defer s.Unlock()
	var b []byte
	n := 0
	for round, cols := range []int{23, 60, 7, 41, 90, 13, 60} {
		b = b[:0]
		for range 30 {
			b = appendLinkLine(b, n, n%70)
			n++
		}
		s.Feed(b)
		s.Term.SweepLinks()
		s.ResizeAndReflow(cols, 8+round%3)
		if _, err := checkLineLinks(s); err != nil {
			t.Fatalf("round %d at %d columns: %v", round, cols, err)
		}
		s.Term.SweepLinks()
		if _, err := checkLineLinks(s); err != nil {
			t.Fatalf("round %d at %d columns, swept: %v", round, cols, err)
		}
	}
	if st := s.Term.LinkStats(); st.Freed == 0 || st.URLs > s.ScrollbackLines+8+2 {
		t.Fatalf("%d lines: %+v", n, st)
	}
}

// TestLinkSoak streams unique links through a session: lines that wrap,
// lines with two links, a URL repeated on many lines, links on the
// alternate screen, resizes and ED 3. The link table stays bounded and the
// live heap stops growing; every link on screen and in the scrollback
// names its own URL, and the newest link is always clickable. SOAK=1
// (make soak) streams 1,000,000 links through a 10,000-line scrollback;
// check-all streams linkSoakLines through 2,000 lines.
func TestLinkSoak(t *testing.T) {
	total, scrollback := linkSoakLines, 2_000
	if leakcheck.Soak() {
		total, scrollback = 1_000_000, 10_000
	}
	heap := func() uint64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	base := heap()
	s := newTestSession(100, 30)
	s.ScrollbackLines, s.Scrollback = scrollback, Ring{MaxLines: scrollback}
	start := time.Now()
	var b []byte
	var samples []uint64
	maxURLs, maxIDs, checks, sweeps := 0, 0, 0, 0
	check := func(n int) {
		t.Helper()
		if _, err := checkLineLinks(s); err != nil {
			t.Fatalf("after %d lines: %v", n, err)
		}
		checks++
	}
	for n := 0; n < total; n++ {
		b = b[:0]
		switch {
		case n%5000 == 4999: // a full-screen app with links of its own
			b = append(b, "\x1b[?1049h\x1b[H"...)
			for k := range 5 {
				b = fmt.Appendf(b, "\x1b]8;;https://alt.example/%d/%d\x1b\\alt\x1b]8;;\x1b\\\r\n", n, k)
			}
			b = append(b, "\x1b[?1049l"...)
		case n%(total*3/10) == total*3/10-1:
			b = append(b, "\x1b[3J"...)
		}
		b = appendLinkLine(b, n, n%7*40) // every seventh line has no padding; others wrap
		if n%3 == 0 {
			b = fmt.Appendf(b[:len(b)-2], " \x1b]8;;%s\x1b\\again\x1b]8;;\x1b\\\r\n", lineURL(n))
		}
		if n%10 == 0 {
			b = append(b, "home \x1b]8;;"+homeURL+"\x1b\\home\x1b]8;;\x1b\\\r\n"...)
		}
		s.Lock()
		s.Feed(b)
		if n%(total/10) == total/20 {
			s.ResizeAndReflow(77+n%3*10, 30)
		}
		st := s.Term.LinkStats()
		maxURLs, maxIDs = max(maxURLs, st.URLs), max(maxIDs, st.IDs)
		if st.Sweeps != sweeps {
			sweeps = st.Sweeps
			check(n)
		}
		if n%97 == 0 {
			if got := newestLink(s, n); got != lineURL(n) {
				t.Fatalf("line %d: its link names %q", n, got)
			}
		}
		s.Unlock()
		if n >= total/10 && n%(total/40) == 0 {
			samples = append(samples, heap()-base)
		}
	}
	s.Lock()
	check(total)
	st := s.Term.LinkStats()
	s.Unlock()
	samples = append(samples, heap()-base)
	// The ring fills within the first tenth; from then on the heap holds
	// the ring, the screens and the table, none of which grows.
	half := len(samples) / 2
	early, late := slices.Max(samples[:half]), slices.Max(samples[half:])
	t.Logf("%d lines, %d-line scrollback, in %v: %d checks; table at most %d URLs / %d IDs, at the end %+v; "+
		"live heap at most %s in the first half, %s in the second, %s at the end",
		total, scrollback, time.Since(start).Round(time.Millisecond), checks, maxURLs, maxIDs, st,
		leakcheck.MiB(early), leakcheck.MiB(late), leakcheck.MiB(samples[len(samples)-1]))
	// Live links: at most two per scrollback row, plus the screens.
	if bound := 4*scrollback + 4096; maxURLs > bound || maxIDs > bound {
		t.Errorf("table reached %d URLs and %d IDs, bound %d", maxURLs, maxIDs, bound)
	}
	if st.Freed < total-4*scrollback || sweeps < 10 {
		t.Errorf("%d sweeps freed %d IDs for %d unique URLs", sweeps, st.Freed, total)
	}
	if late > early+3<<20 {
		t.Errorf("live heap grew from %s in the first half to %s in the second", leakcheck.MiB(early), leakcheck.MiB(late))
	}
	runtime.KeepAlive(s)
}

// newestLink returns the URL of the link of line n, which starts a row of
// the screen.
func newestLink(s *Session, n int) string {
	prefix := "L" + strconv.Itoa(n) + " "
	cols, _ := s.Term.Size()
	for y := s.Term.Cursor().Y; y >= 0; y-- {
		if len(prefix) >= cols {
			return ""
		}
		var text []rune
		for x := range prefix {
			text = append(text, s.Term.RawCell(x, y).Char)
		}
		if string(text) == prefix {
			return s.Term.Link(s.Term.RawCell(len(prefix), y).Link)
		}
	}
	return ""
}
