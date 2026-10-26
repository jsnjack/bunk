package vt10x

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
)

// keptRows is a scrollback for link tests: it keeps the last max rows that
// scroll off the screen and marks their links for sweeps.
type keptRows struct {
	rows []Row
	max  int
}

func (k *keptRows) push(r Row) {
	if k.max <= 0 {
		return
	}
	if len(k.rows) == k.max {
		k.rows[0] = Row{}
		k.rows = k.rows[1:]
	}
	var c Row
	c.CopyFrom(r, r.Len())
	k.rows = append(k.rows, c)
}

func (k *keptRows) mark(mark func(id uint16)) {
	for _, r := range k.rows {
		r.MarkLinks(mark)
	}
}

// newLinkTerm returns a terminal whose scrolled rows go to a keptRows of
// at most keep rows, which ED 3 and RIS clear.
func newLinkTerm(cols, rows, keep int) (*terminal, *keptRows) {
	k := &keptRows{max: keep}
	term := New(WithSize(cols, rows), WithScrollCallback(k.push), WithLinkReferences(k.mark),
		WithScrollbackClearCallback(func() { k.rows = nil })).(*terminal)
	return term, k
}

func osc8(url, text string) string { return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\" }

// linkAt returns the URL of cell (x, y).
func linkAt(term *terminal, x, y int) string { return term.Link(term.Cell(x, y).Link) }

// TestLinkSweepFreesOnlyUnreferenced: a sweep keeps the links of the
// primary and alternate screens, of kept scrollback rows, of the open link
// and of the saved cursor. It frees only an overwritten link, and a new URL
// reuses that ID.
func TestLinkSweepFreesOnlyUnreferenced(t *testing.T) {
	term, kept := newLinkTerm(20, 3, 100)
	write := func(s string) { term.Write([]byte(s)) } //nolint:errcheck
	// c scrolls into the kept rows, f is overwritten, a stays on screen,
	// e is saved with the cursor, b is on the alternate screen and d is
	// open with nothing printed yet.
	write(osc8("https://c.example", "c") + "\r\n\r\n\r\n")
	write("\x1b[H" + osc8("https://f.example", "f") + "\rX")
	write("\x1b[2;1H" + osc8("https://a.example", "a"))
	write("\x1b]8;;https://e.example\x1b\\\x1b7\x1b]8;;\x1b\\")
	write("\x1b[?1047h\x1b[H" + osc8("https://b.example", "b"))
	write("\x1b]8;;https://d.example\x1b\\")
	if len(kept.rows) != 1 || term.Link(kept.rows[0].Link(0)) != "https://c.example" {
		t.Fatalf("kept rows: %d", len(kept.rows))
	}
	ids := map[string]uint16{}
	for id := uint16(1); id <= 6; id++ {
		ids[term.Link(id)] = id
	}
	term.SweepLinks()
	st := term.LinkStats()
	if st.URLs != 5 || st.Freed != 1 || st.Sweeps != 1 || term.LinkGen() != 1 {
		t.Fatalf("after the sweep: %+v, gen %d", st, term.LinkGen())
	}
	if got := term.Link(ids["https://f.example"]); got != "" {
		t.Fatalf("the overwritten link's ID still names %q", got)
	}
	for url, id := range ids {
		if url != "https://f.example" && term.Link(id) != url {
			t.Errorf("ID %d names %q, want %q", id, term.Link(id), url)
		}
	}
	if got := linkAt(term, 0, 0); got != "https://b.example" {
		t.Errorf("alternate screen cell: %q", got)
	}
	write("\x1b]8;;https://g.example\x1b\\g")
	if g := term.Cell(1, 0).Link; g != ids["https://f.example"] || term.Link(g) != "https://g.example" {
		t.Errorf("new URL took ID %d (%q), want the freed ID %d", g, term.Link(g), ids["https://f.example"])
	}
	// Leaving the alternate screen erases it, and DECRC restores the
	// cursor saved with e open, which closes d: b, g and d go.
	write("\x1b[?1047l\x1b8e\x1b]8;;\x1b\\")
	if got := linkAt(term, 1, 1); got != "https://e.example" {
		t.Errorf("text after DECRC: %q", got)
	}
	term.SweepLinks()
	if st := term.LinkStats(); st.URLs != 3 || st.Freed != 4 {
		t.Errorf("after leaving the alternate screen: %+v", st)
	}
	if linkAt(term, 0, 1) != "https://a.example" || linkAt(term, 1, 1) != "https://e.example" || term.Link(kept.rows[0].Link(0)) != "https://c.example" {
		t.Error("a live link lost its URL")
	}
}

// TestLinkSweepNeedsReferences: a terminal that hands scrolled rows to a
// callback but cannot mark them never frees an ID; one without a
// scroll callback sweeps its own cells.
func TestLinkSweepNeedsReferences(t *testing.T) {
	for _, tc := range []struct {
		name      string
		opts      []TerminalOption
		wantFreed bool
	}{
		{"scroll callback without references", []TerminalOption{WithScrollCallback(func(Row) {})}, false},
		{"scroll callback with references", []TerminalOption{WithScrollCallback(func(Row) {}), WithLinkReferences(func(func(uint16)) {})}, true},
		{"no scroll callback", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := New(append(tc.opts, WithSize(10, 2))...).(*terminal)
			term.links.sweepMin, term.links.sweepAt, term.links.sweepGap = 4, 4, 1
			for i := 0; i < 100; i++ {
				term.Write([]byte("\r" + osc8("https://x.example/"+strconv.Itoa(i), "x"))) //nolint:errcheck
			}
			st := term.LinkStats()
			if freed := st.Freed > 0; freed != tc.wantFreed {
				t.Fatalf("%+v", st)
			}
			if got := linkAt(term, 0, 0); got != "https://x.example/99" {
				t.Fatalf("last link: %q", got)
			}
		})
	}
}

// TestLinkSweepOnClear: ED 3 and RIS sweep at once, after the scrollback
// is cleared.
func TestLinkSweepOnClear(t *testing.T) {
	term, kept := newLinkTerm(10, 2, 100)
	for i := 0; i < 20; i++ {
		term.Write([]byte(osc8("https://x.example/"+strconv.Itoa(i), "x") + "\r\n")) //nolint:errcheck
	}
	if len(kept.rows) != 19 || term.LinkStats().URLs != 20 {
		t.Fatalf("%d kept rows, %+v", len(kept.rows), term.LinkStats())
	}
	term.Write([]byte("\x1b[3J")) //nolint:errcheck
	if st := term.LinkStats(); st.URLs != 1 || st.Freed != 19 || linkAt(term, 0, 0) != "https://x.example/19" {
		t.Fatalf("after ED 3: %+v", st)
	}
	term.Write([]byte("\x1bc")) //nolint:errcheck
	if st := term.LinkStats(); st.URLs != 0 || st.IDs != 0 || st.Bytes != 0 {
		t.Fatalf("after RIS: %+v", st)
	}
	term.Write([]byte(osc8("https://y.example", "y"))) //nolint:errcheck
	if g := term.Cell(0, 0).Link; g != 1 || term.Link(g) != "https://y.example" {
		t.Fatalf("first link after RIS: ID %d %q", g, term.Link(g))
	}
}

// linkGlyph is the character the link workload prints for URL number n:
// a wide CJK ideograph, so the URL of every cell can be checked.
func linkGlyph(n int) rune { return rune(0x4E00 + n%20000) }

func linkURL(n int) string { return "https://w.example/" + strconv.Itoa(n) }

// checkLinkCells checks every cell of both screens and of the kept rows:
// a linked ideograph names its own URL and a continuation its lead's.
func checkLinkCells(term *terminal, kept *keptRows) error {
	check := func(where string, r Row) error {
		for x := 0; x < r.Len(); x++ {
			g := r.Glyph(x)
			if g.Link == 0 {
				continue
			}
			url := term.Link(g.Link)
			if g.Width == -1 {
				if x == 0 || r.Glyph(x-1).Link != g.Link {
					return fmt.Errorf("%s cell %d: continuation links to ID %d, not its lead's", where, x, g.Link)
				}
				continue
			}
			n, err := strconv.Atoi(strings.TrimPrefix(url, "https://w.example/"))
			if err != nil || linkGlyph(n) != g.Char {
				return fmt.Errorf("%s cell %d: %q links to ID %d, %q", where, x, g.Char, g.Link, url)
			}
		}
		return nil
	}
	for i, r := range kept.rows {
		if err := check(fmt.Sprintf("kept row %d", i), r); err != nil {
			return err
		}
	}
	for s, screen := range [2][]*Row{term.lines, term.altLines} {
		for y, r := range screen {
			if err := check(fmt.Sprintf("screen %d row %d", s, y), *r); err != nil {
				return err
			}
		}
	}
	return nil
}

// referencedLinks returns the IDs that cells, kept rows and cursors refer to.
func referencedLinks(term *terminal, kept *keptRows) map[uint16]bool {
	ids := map[uint16]bool{}
	mark := func(id uint16) {
		if id != 0 {
			ids[id] = true
		}
	}
	term.markLinks(mark)
	kept.mark(mark)
	return ids
}

// checkLinkTable checks the table's bookkeeping: map and slice agree, the
// free list holds exactly the free IDs, the byte count is exact, and no
// ID past the last one in use is kept.
func checkLinkTable(l *linkTable) error {
	held, size := 0, 0
	for i, u := range l.urls {
		if u == "" {
			continue
		}
		held++
		size += len(u)
		if l.ids[u] != uint16(i+1) {
			return fmt.Errorf("URL %q at ID %d maps to %d", u, i+1, l.ids[u])
		}
	}
	if held != len(l.ids) || size != l.bytes {
		return fmt.Errorf("%d URLs of %d bytes held, map has %d, count %d bytes", held, size, len(l.ids), l.bytes)
	}
	if len(l.free) != len(l.urls)-held {
		return fmt.Errorf("%d free IDs listed, %d free", len(l.free), len(l.urls)-held)
	}
	for i, id := range l.free {
		if id == 0 || int(id) > len(l.urls) || l.urls[id-1] != "" || i > 0 && id >= l.free[i-1] {
			return fmt.Errorf("free list %v is not the free IDs, highest first", l.free)
		}
	}
	return nil
}

// TestLinkCellsKeepURLsAcrossSweeps runs a random workload with sweeps
// due after almost every new URL: printing, scrolling into kept rows,
// erasing, inserting and deleting, the alternate screen, saved cursors,
// ED 3 and RIS. After every step every cell still names the URL it was
// printed with; after a sweep the table holds exactly the referenced IDs.
func TestLinkCellsKeepURLsAcrossSweeps(t *testing.T) {
	steps := 30000
	if testing.Short() {
		steps = 5000
	}
	term, kept := newLinkTerm(12, 5, 40)
	term.links.sweepMin, term.links.sweepAt, term.links.sweepMinBytes, term.links.sweepGap = 8, 8, 1<<10, 1
	rng := rand.New(rand.NewPCG(7, 11))
	next, open, saved := 0, -1, -1
	ops := []string{"\r\n", "\x1b[K", "\x1b[1K", "\x1b[2J", "\x1b[@", "\x1b[2P", "\x1b[L", "\x1b[M", "\x1b[S", "\x1b[T", "\x1b[2X"}
	for step := 0; step < steps; step++ {
		var out string
		switch r := rng.IntN(100); {
		case r < 25: // open a new URL
			open = next
			next++
			out = "\x1b]8;;" + linkURL(open) + "\x1b\\"
		case r < 30 && next > 0: // reopen a recent URL, which may have been freed
			open = max(0, next-1-rng.IntN(20))
			out = "\x1b]8;;" + linkURL(open) + "\x1b\\"
		case r < 60 && open >= 0:
			out = strings.Repeat(string(linkGlyph(open)), 1+rng.IntN(3))
		case r < 65:
			open = -1
			out = "\x1b]8;;\x1b\\"
		case r < 80:
			out = ops[rng.IntN(len(ops))]
		case r < 88:
			out = fmt.Sprintf("\x1b[%d;%dH", 1+rng.IntN(5), 1+rng.IntN(12))
		case r < 92:
			if rng.IntN(2) == 0 {
				out = "\x1b[?1047h"
			} else {
				out = "\x1b[?1047l"
			}
		case r < 96:
			if rng.IntN(2) == 0 {
				saved, out = open, "\x1b7"
			} else {
				open, out = saved, "\x1b8"
			}
		case r < 98:
			out = "\x1b[3J"
		default:
			open, saved, out = -1, -1, "\x1bc"
		}
		term.Write([]byte(out)) //nolint:errcheck
		if err := checkLinkCells(term, kept); err != nil {
			t.Fatalf("step %d after %q: %v", step, out, err)
		}
		if err := checkLinkTable(&term.links); err != nil {
			t.Fatalf("step %d after %q: %v", step, out, err)
		}
		if step%5 != 0 {
			continue
		}
		term.SweepLinks()
		if err := checkLinkCells(term, kept); err != nil {
			t.Fatalf("step %d, swept: %v", step, err)
		}
		if err := checkLinkTable(&term.links); err != nil {
			t.Fatalf("step %d, swept: %v", step, err)
		}
		refs := referencedLinks(term, kept)
		if st := term.LinkStats(); st.URLs != len(refs) {
			t.Fatalf("step %d: after a sweep the table holds %d URLs, cells refer to %d", step, st.URLs, len(refs))
		}
		for id := range refs {
			if term.Link(id) == "" {
				t.Fatalf("step %d: a cell refers to free ID %d", step, id)
			}
		}
	}
	st := term.LinkStats()
	t.Logf("%d steps, %d URLs: %+v", steps, next, st)
	if st.Sweeps < 100 || st.Freed < next/2 {
		t.Fatalf("the workload swept too little: %+v", st)
	}
}

// TestLinkTableBounded: a terminal that sees many more unique URLs than
// IDs exist keeps the table at the size of its live links, and every new
// link resolves.
func TestLinkTableBounded(t *testing.T) {
	n := 100000
	if testing.Short() {
		n = 70000
	}
	term, kept := newLinkTerm(40, 24, 1000)
	maxURLs, maxIDs := 0, 0
	var b []byte
	for i := 0; i < n; i++ {
		b = fmt.Appendf(b[:0], "%d %s\r\n", i, osc8(linkURL(i), "link"))
		term.Write(b) //nolint:errcheck
		st := term.LinkStats()
		maxURLs, maxIDs = max(maxURLs, st.URLs), max(maxIDs, st.IDs)
		if i%997 == 0 {
			if got := term.Link(term.Cell(len(strconv.Itoa(i))+1, min(i, 22)).Link); got != linkURL(i) {
				t.Fatalf("link %d resolves to %q", i, got)
			}
		}
	}
	// 1,000 kept rows and 24 on screen hold the live links; a sweep is due
	// at linkSweepMin URLs.
	if maxURLs > linkSweepMin || maxIDs > linkSweepMin {
		t.Fatalf("table reached %d URLs and %d IDs", maxURLs, maxIDs)
	}
	for i, r := range kept.rows {
		want := linkURL(n - 1 - 23 - (len(kept.rows) - 1 - i))
		if got := term.Link(r.Link(strings.IndexByte(rowText(r), ' ') + 1)); got != want {
			t.Fatalf("kept row %d: %q, want %q", i, got, want)
		}
	}
	t.Logf("%d URLs: %+v, at most %d URLs and %d IDs held", n, term.LinkStats(), maxURLs, maxIDs)
}

func rowText(r Row) string {
	var sb strings.Builder
	for x := 0; x < r.Len(); x++ {
		sb.WriteRune(r.Glyph(x).Char)
	}
	return sb.String()
}

// TestLinkTableFullOfLiveLinks: when live links alone fill every ID, new
// URLs get no link and the table is swept once per linkSweepGap new URLs,
// not for each. Once the rows holding links are gone, new links resolve.
func TestLinkTableFullOfLiveLinks(t *testing.T) {
	term, kept := newLinkTerm(16, 4, 70000)
	var b []byte
	write := func(i int) {
		b = fmt.Appendf(b[:0], "%s\r\n", osc8(linkURL(i), "x"))
		term.Write(b) //nolint:errcheck
	}
	for i := 0; i < maxLinkIDs; i++ {
		write(i)
	}
	if st := term.LinkStats(); st.URLs != maxLinkIDs || st.Freed != 0 {
		t.Fatalf("after %d live links: %+v", maxLinkIDs, st)
	}
	sweeps := term.LinkStats().Sweeps
	extra := 3 * linkSweepGap
	for i := maxLinkIDs; i < maxLinkIDs+extra; i++ {
		write(i)
		if g := term.Cell(0, 2).Link; g != 0 {
			t.Fatalf("link %d got ID %d in a full table", i, g)
		}
	}
	if got := term.LinkStats().Sweeps - sweeps; got > extra/linkSweepGap+1 {
		t.Fatalf("%d sweeps for %d URLs that did not fit", got, extra)
	}
	// The rows leave the scrollback: within linkSweepGap new URLs a sweep
	// frees their IDs.
	kept.rows, kept.max = nil, 0
	last := maxLinkIDs + extra
	for ; last < maxLinkIDs+extra+linkSweepGap; last++ {
		write(last)
		if term.Cell(0, 2).Link != 0 {
			break
		}
	}
	if got := linkAt(term, 0, 2); got != linkURL(last) {
		t.Fatalf("after the rows left and %d more URLs: %q", last-maxLinkIDs-extra+1, got)
	}
	if st := term.LinkStats(); st.URLs > 8 {
		t.Fatalf("after the rows left: %+v", st)
	}
}
