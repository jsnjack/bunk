package main

// What an edit changed in a document (docs/present.md): the panel reads
// the page's sections (present.SectionsScript) before it loads again or
// swaps its content and after, and compares their text. A header button
// lists the sections that changed with a word diff of each.

import (
	"html"
	"strings"
)

// docSection is a part of a page from one heading to the next.
type docSection struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

// sectionChange is what an edit did to a section: changed, new or removed.
type sectionChange struct {
	key, title string
	kind       string
	old, new   string
}

// compareSections lists the sections of after that differ from before, in
// the order of after, then those removed.
func compareSections(before, after []docSection) []sectionChange {
	old := make(map[string]docSection, len(before))
	for _, s := range before {
		old[s.Key] = s
	}
	seen := map[string]bool{}
	var out []sectionChange
	for _, s := range after {
		seen[s.Key] = true
		o, ok := old[s.Key]
		switch {
		case !ok && (s.Key != "" || s.Text != ""):
			out = append(out, sectionChange{key: s.Key, title: s.Title, kind: "new", new: s.Text})
		case ok && o.Text != s.Text:
			out = append(out, sectionChange{key: s.Key, title: s.Title, kind: "changed", old: o.Text, new: s.Text})
		}
	}
	for _, s := range before {
		if !seen[s.Key] && (s.Key != "" || s.Text != "") {
			out = append(out, sectionChange{key: s.Key, title: s.Title, kind: "removed", old: s.Text})
		}
	}
	return out
}

// diffOp is a run of words a diff keeps ('='), drops ('-') or adds ('+').
type diffOp struct {
	kind byte
	text string
}

// maxDiffCells bounds the table of a word diff: two sections of about
// 1,400 words each.
const maxDiffCells = 2_000_000

// wordDiff returns the words of b against a, runs of the same kind joined,
// or nil when the sections are too long to compare.
func wordDiff(a, b string) []diffOp {
	x, y := strings.Fields(a), strings.Fields(b)
	if len(x)*len(y) > maxDiffCells {
		return nil
	}
	// lcs[i][j] is the longest common subsequence of x[i:] and y[j:].
	lcs := make([][]int32, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int32, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []diffOp
	add := func(kind byte, word string) {
		if n := len(ops); n > 0 && ops[n-1].kind == kind {
			ops[n-1].text += " " + word
			return
		}
		ops = append(ops, diffOp{kind, word})
	}
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			add('=', x[i])
			i, j = i+1, j+1
		case j < len(y) && (i == len(x) || lcs[i][j+1] >= lcs[i+1][j]):
			add('+', y[j])
			j++
		default:
			add('-', x[i])
			i++
		}
	}
	return ops
}

// Pango markup of a diff: dropped words struck through in red, added
// words underlined in green.
const (
	dropMarkup = `<span foreground="#e5534b" strikethrough="true">%s</span>`
	addMarkup  = `<span foreground="#57ab5a" underline="single">%s</span>`
)

// keptWords is how many words of an unchanged run a diff shows beside a
// change; the rest becomes "…".
const keptWords = 6

// diffMarkup writes ops as Pango markup, unchanged runs cut to keptWords
// words at each side of a change.
func diffMarkup(ops []diffOp) string {
	var b strings.Builder
	for k, op := range ops {
		if k > 0 {
			b.WriteByte(' ')
		}
		text := html.EscapeString(op.text)
		switch op.kind {
		case '-':
			b.WriteString(strings.Replace(dropMarkup, "%s", text, 1))
		case '+':
			b.WriteString(strings.Replace(addMarkup, "%s", text, 1))
		default:
			words := strings.Fields(op.text)
			first, last := k == 0, k == len(ops)-1
			switch {
			case len(words) <= 2*keptWords+1:
			case first:
				words = append([]string{"…"}, words[len(words)-keptWords:]...)
			case last:
				words = append(words[:keptWords:keptWords], "…")
			default:
				words = append(append(words[:keptWords:keptWords], "…"), words[len(words)-keptWords:]...)
			}
			b.WriteString(html.EscapeString(strings.Join(words, " ")))
		}
	}
	return b.String()
}

// changeMarkup is the Pango markup of what a change did to its section.
func changeMarkup(c sectionChange) string {
	excerpt := func(s string, n int) string {
		words := strings.Fields(s)
		if len(words) > n {
			words = append(words[:n:n], "…")
		}
		return html.EscapeString(strings.Join(words, " "))
	}
	switch c.kind {
	case "new":
		return strings.Replace(addMarkup, "%s", excerpt(c.new, 40), 1)
	case "removed":
		return strings.Replace(dropMarkup, "%s", excerpt(c.old, 20), 1)
	}
	ops := wordDiff(c.old, c.new)
	if ops == nil {
		return "<i>The section changed; it is too long to compare word by word.</i>"
	}
	return diffMarkup(ops)
}
