package vt10x

import "strings"

// Bounds of the OSC 8 hyperlink table. Glyph.Link is a uint16, so at most
// maxLinkIDs URLs are held at once; maxLinkBytes bounds their text.
const (
	maxLinkIDs   = 0xFFFF
	maxLinkBytes = 16 << 20
	// A sweep is due once the table holds linkSweepMin URLs or
	// linkSweepMinBytes bytes, or twice what the last sweep kept.
	linkSweepMin      = 4096
	linkSweepMinBytes = 1 << 20
	// linkSweepGap new URLs must arrive between two sweeps. When live
	// links alone fill the table, it is swept once per gap, not per URL.
	linkSweepGap = 1024
)

// linkTable interns the URLs of OSC 8 hyperlinks: a cell stores its URL's
// ID. A sweep frees the IDs that no cell or cursor refers to, and new URLs
// reuse them, lowest first. Cells never change their ID, and an ID is freed
// only when nothing refers to it, so every cell keeps its URL.
//
// When live links alone exceed maxLinkIDs URLs or maxLinkBytes bytes, new
// URLs get no ID: their cells keep text and style but no link. Once rows
// that hold links are erased or leave the scrollback, a later sweep frees
// their IDs and new links resolve again.
type linkTable struct {
	urls  []string          // urls[id-1]; "" marks a free ID
	ids   map[string]uint16 // the ID of each URL held
	free  []uint16          // free IDs below len(urls), highest first
	bytes int               // URL bytes held

	// A sweep is due when the table reaches sweepAt URLs or sweepBytes
	// bytes and at least sweepGap URLs missed the table since the last
	// one. The minimums are fields so tests can sweep often.
	sweepAt, sweepBytes, misses       int
	sweepMin, sweepMinBytes, sweepGap int

	gen           uint64   // sweeps that freed IDs
	sweeps, freed int      // sweeps so far and the IDs they freed
	marks         []uint64 // the IDs a sweep found, one bit each
	// referenced marks the IDs of rows kept outside the terminal
	// (WithLinkReferences).
	referenced func(mark func(id uint16))
}

func newLinkTable() linkTable {
	return linkTable{
		ids:     map[string]uint16{},
		sweepAt: linkSweepMin, sweepBytes: linkSweepMinBytes,
		sweepMin: linkSweepMin, sweepMinBytes: linkSweepMinBytes, sweepGap: linkSweepGap,
	}
}

// LinkStats describes a terminal's hyperlink table.
type LinkStats struct {
	URLs   int // URLs held
	IDs    int // highest ID in use; the free IDs below it are reused first
	Bytes  int // bytes of the URLs held
	Sweeps int // sweeps so far
	Freed  int // IDs the sweeps freed
}

// Link returns the OSC 8 hyperlink URL for the given ID, or "" if id is 0
// (meaning "no link"), free or out of range. Callers must hold the State
// lock.
func (t *State) Link(id uint16) string {
	if id == 0 || int(id) > len(t.links.urls) {
		return ""
	}
	return t.links.urls[id-1]
}

// LinkGen returns a counter that changes whenever a sweep frees link IDs.
// A freed ID may then name another URL, so a front end that resolves IDs
// of cells it copied earlier must copy them again first.
func (t *State) LinkGen() uint64 { return t.links.gen }

// LinkStats reports the size of the hyperlink table.
func (t *State) LinkStats() LinkStats {
	l := &t.links
	return LinkStats{URLs: len(l.ids), IDs: len(l.urls), Bytes: l.bytes, Sweeps: l.sweeps, Freed: l.freed}
}

// SweepLinks frees the hyperlink IDs that no cell refers to: the cells of
// both screens, the cursor's attributes and the rows the WithLinkReferences
// callback marks. The terminal also sweeps by itself as the table grows,
// and after ED 3 and RIS. Callers must hold the State lock.
func (t *State) SweepLinks() { t.sweepLinks() }

// internLink returns the ID of url, adding it when the table lacks it. It
// returns 0 for "" and when the table is full.
func (t *State) internLink(url string) uint16 {
	if url == "" {
		return 0
	}
	l := &t.links
	if id, ok := l.ids[url]; ok {
		return id
	}
	l.misses++
	if (len(l.ids) >= l.sweepAt || l.bytes+len(url) > l.sweepBytes) && l.misses >= l.sweepGap {
		t.sweepLinks()
	}
	if len(l.ids) >= maxLinkIDs || len(url) > maxLinkBytes-l.bytes {
		return 0
	}
	url = strings.Clone(url) // do not retain a larger OSC header backing string
	var id uint16
	if n := len(l.free); n > 0 {
		id = l.free[n-1]
		l.free = l.free[:n-1]
		l.urls[id-1] = url
	} else {
		l.urls = append(l.urls, url)
		id = uint16(len(l.urls))
	}
	l.ids[url] = id
	l.bytes += len(url)
	return id
}

// sweepLinks frees every ID that no cell, cursor or kept row refers to.
// A terminal whose scrolled rows go to a callback sweeps only when
// WithLinkReferences also marks the rows the callback keeps.
func (t *State) sweepLinks() {
	l := &t.links
	l.misses = 0
	if len(l.ids) == 0 || l.referenced == nil && t.scrollRowCb != nil {
		return
	}
	words := len(l.urls)/64 + 1
	if cap(l.marks) < words {
		l.marks = make([]uint64, words)
	}
	marks := l.marks[:words]
	clear(marks)
	mark := func(id uint16) {
		if int(id) <= len(l.urls) {
			marks[id>>6] |= 1 << (id & 63)
		}
	}
	t.markLinks(mark)
	if l.referenced != nil {
		l.referenced(mark)
	}
	l.sweeps++
	freed := 0
	for i, u := range l.urls {
		if id := i + 1; u != "" && marks[id>>6]&(1<<(id&63)) == 0 {
			l.urls[i] = ""
			l.bytes -= len(u)
			freed++
		}
	}
	live := len(l.ids) - freed
	l.sweepAt = min(maxLinkIDs, max(l.sweepMin, 2*live))
	l.sweepBytes = min(maxLinkBytes, max(l.sweepMinBytes, 2*l.bytes))
	if freed > 0 {
		l.compact(live)
		l.freed += freed
		l.gen++
	}
}

// compact rebuilds the table after a sweep freed IDs: it drops the free
// IDs past the last one in use, rebuilds the free list and the URL map, and
// releases storage the table outgrew. Go maps never shrink, so the map is
// rebuilt rather than pruned, with room for as many new URLs as live ones.
func (l *linkTable) compact(live int) {
	n := len(l.urls)
	for n > 0 && l.urls[n-1] == "" {
		n--
	}
	if cap(l.urls) > 2*n+64 {
		l.urls = append(make([]string, 0, n), l.urls[:n]...)
	} else {
		l.urls = l.urls[:n]
	}
	ids := make(map[string]uint16, 2*live)
	free := make([]uint16, 0, n-live)
	for i := n - 1; i >= 0; i-- {
		if u := l.urls[i]; u != "" {
			ids[u] = uint16(i + 1)
		} else {
			free = append(free, uint16(i+1))
		}
	}
	l.ids, l.free = ids, free
}

// markLinks marks the link IDs of both screens and of the cursor
// attributes, current and saved: text printed later takes their link.
func (t *State) markLinks(mark func(id uint16)) {
	for _, screen := range [2][]*Row{t.lines, t.altLines} {
		for _, row := range screen {
			row.MarkLinks(mark)
		}
	}
	mark(t.cur.Attr.Link)
	mark(t.curSaved.Attr.Link)
}
