package main

import (
	"slices"
	"strings"
	"testing"
)

// TestCompareSections: a section whose text changed, a new one and a
// removed one are listed, in the order of the new page.
func TestCompareSections(t *testing.T) {
	t.Parallel()
	before := []docSection{{"", "", "Intro"}, {"a", "A", "one two"}, {"b", "B", "same"}, {"c", "C", "gone"}}
	after := []docSection{{"", "", "Intro"}, {"a", "A", "one three"}, {"b", "B", "same"}, {"d", "D", "fresh"}}
	var got []string
	for _, c := range compareSections(before, after) {
		got = append(got, c.kind+" "+c.title)
	}
	if want := []string{"changed A", "new D", "removed C"}; !slices.Equal(got, want) {
		t.Errorf("changes %q, want %q", got, want)
	}
	if c := compareSections(after, after); len(c) != 0 {
		t.Errorf("an unchanged page has changes: %+v", c)
	}
}

// TestWordDiff: a diff keeps the common words, strikes the dropped ones
// and marks the added ones; long unchanged runs shrink to their ends.
func TestWordDiff(t *testing.T) {
	t.Parallel()
	ops := wordDiff("Sessions live in Redis keyed by a random ID", "Sessions live in Redis keyed by a random 256-bit ID")
	var kinds []string
	for _, op := range ops {
		kinds = append(kinds, string(op.kind)+op.text)
	}
	if want := []string{"=Sessions live in Redis keyed by a random", "+256-bit", "=ID"}; !slices.Equal(kinds, want) {
		t.Errorf("ops %q, want %q", kinds, want)
	}
	m := diffMarkup(ops)
	if !strings.Contains(m, `underline="single">256-bit</span>`) || !strings.HasPrefix(m, "Sessions live in Redis keyed by a random") {
		t.Errorf("markup %q", m)
	}
	words := strings.Repeat("word ", 20)
	if m := diffMarkup(wordDiff(words+"old", words+"new")); !strings.HasPrefix(m, "… word word word word word word <span") {
		t.Errorf("a long unchanged run kept: %q", m)
	}
	m = changeMarkup(sectionChange{kind: "changed", old: "a <b> c", new: "a c"})
	if !strings.Contains(m, `strikethrough="true">&lt;b&gt;</span>`) {
		t.Errorf("markup of a dropped word %q", m)
	}
	if long := strings.Repeat("w ", 1500); wordDiff(long, long+"x") != nil {
		t.Error("two long sections were compared word by word")
	}
}
