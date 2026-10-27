// search.go - case-insensitive text search over scrollback and the live grid.
package session

import (
	"bunk/internal/vt10x"
	"slices"
	"strings"
	"unicode"
)

// Match identifies one occurrence of the search query inside a pane's
// virtual grid (scrollback ring + live terminal rows).
type Match struct {
	VRow, Col, Length int
}

// Span is a highlighted column range within one virtual row [col, end).
type Span struct{ Col, End int }

// Highlight holds span-based match positions for front ends.
// regular contains all matches; current contains only the selected match.
// Both map virtual row → spans for that row.
type Highlight struct {
	Regular map[int][]Span
	Current map[int][]Span
}

// BuildHighlight maps match positions to per-row spans for front ends.
// idx is the currently selected match; it is highlighted via current spans.
func BuildHighlight(matches []Match, idx int) *Highlight {
	if len(matches) == 0 {
		return nil
	}
	hl := &Highlight{
		Regular: make(map[int][]Span),
		Current: make(map[int][]Span),
	}
	for i, m := range matches {
		span := Span{Col: m.Col, End: m.Col + m.Length}
		if i == idx {
			hl.Current[m.VRow] = append(hl.Current[m.VRow], span)
		} else {
			hl.Regular[m.VRow] = append(hl.Regular[m.VRow], span)
		}
	}
	return hl
}

// SpanContains reports whether any span in spans covers col.
func SpanContains(spans []Span, col int) bool {
	for _, sp := range spans {
		if col >= sp.Col && col < sp.End {
			return true
		}
	}
	return false
}

// FindAll returns every case-insensitive occurrence of query in the
// scrollback and live grid, in virtual-row order. It reads the stored cells
// under the session lock, one row at a time, without building Glyphs. An
// empty query matches nothing.
func (s *Session) FindAll(query string) []Match {
	if query == "" {
		return nil
	}
	needle := []rune(strings.ToLower(query))
	// Past a row's last character every cell is blank, so only a query
	// that ends in a space can match there.
	blanks := needle[len(needle)-1] == ' '
	var (
		matches []Match
		text    rowText
		live    vt10x.Row
	)
	s.Lock()
	defer s.Unlock()
	cols, rows := s.Term.Size()
	sbCount := s.Scrollback.Count
	for vRow := 0; vRow < sbCount+rows; vRow++ {
		var row vt10x.Row
		if vRow < sbCount {
			row = s.Scrollback.Row(vRow)
		} else {
			s.Term.CopyRow(vRow-sbCount, &live)
			row = live
		}
		n := cols
		if !blanks {
			n = min(cols, row.TrimmedLen())
		}
		text.load(row, n)
		matches = text.find(matches, vRow, needle)
	}
	return matches
}

// rowText is the text of one row as search sees it: lower-case runes, each
// with the columns its cell covers. Continuations of wide characters are
// left out, and a cell's combining marks follow its character.
type rowText struct {
	runes      []rune
	start, end []int
}

// load reads the first n columns of row into t, reusing t's storage.
// Columns past the row's end are blanks.
func (t *rowText) load(row vt10x.Row, n int) {
	t.runes, t.start, t.end = t.runes[:0], t.start[:0], t.end[:0]
	for x := 0; x < n; x++ {
		if x >= row.Len() {
			t.add(' ', x, x+1)
			continue
		}
		ch, _, _, _, width, extra := row.CellAt(x)
		if width < 0 {
			continue
		}
		if ch == 0 {
			ch = ' '
		}
		end := x + max(1, int(width))
		t.add(unicode.ToLower(ch), x, end)
		if extra {
			for _, c := range row.Combining(x) {
				t.add(unicode.ToLower(c), x, end)
			}
		}
	}
}

func (t *rowText) add(r rune, start, end int) {
	t.runes = append(t.runes, r)
	t.start = append(t.start, start)
	t.end = append(t.end, end)
}

// find appends the occurrences of needle in t to matches, left to right;
// a match starts after the end of the one before it.
func (t *rowText) find(matches []Match, vRow int, needle []rune) []Match {
	for i := 0; i+len(needle) <= len(t.runes); i++ {
		if t.runes[i] != needle[0] || !slices.Equal(t.runes[i:i+len(needle)], needle) {
			continue
		}
		matches = append(matches, Match{VRow: vRow, Col: t.start[i], Length: t.end[i+len(needle)-1] - t.start[i]})
		i += len(needle) - 1
	}
	return matches
}
