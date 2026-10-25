package main

import (
	"fmt"
	"strings"
	"testing"
)

// openedInTests records links unit tests open: the test binary never
// launches one (the window tests' bunk-gui guards with launchRefused).
var openedInTests []string

func init() {
	openURL = func(_ *App, uri string) { openedInTests = append(openedInTests, uri) }
}

func TestFindURL(t *testing.T) {
	for _, tc := range []struct{ text, at, want string }{
		{"see https://example.com/a?b=1 now", "example", "https://example.com/a?b=1"},
		{"(https://example.com/x_(y))", "example", "https://example.com/x_(y)"},
		{"(see https://example.com/x)", "example", "https://example.com/x"},
		{"end https://example.com/path.", "path", "https://example.com/path"},
		{"open file:///tmp/a%20b.txt, then", "tmp", "file:///tmp/a%20b.txt"},
		{"<http://h.example/q>", "h.ex", "http://h.example/q"},
		{"xhttps://not.a.link", "not", ""},
		{"https:// nothing", "https", ""},
		{"two http://a.example and http://b.example", "b.ex", "http://b.example"},
		{"no link here", "link", ""},
	} {
		text := []rune(tc.text)
		at := len([]rune(tc.text[:strings.Index(tc.text, tc.at)]))
		s, e, ok := findURL(text, at)
		got := ""
		if ok {
			got = string(text[s:e])
		}
		if got != tc.want {
			t.Errorf("%q at %q: %q, want %q", tc.text, tc.at, got, tc.want)
		}
	}
}

func TestAllowedLink(t *testing.T) {
	for u, want := range map[string]bool{
		"https://example.com": true, "HTTP://example.com": true, "file:///etc/hosts": true, "mailto:a@b.example": true,
		"javascript:alert(1)": false, "ssh://host": false, "": false, "relative/path": false,
	} {
		if got := allowedLink(u); got != want {
			t.Errorf("allowedLink(%q) = %v", u, got)
		}
	}
}

// TestTextLinkAcrossWrappedRows: a URL that soft-wraps is one link; its
// cells on both rows are underlined.
func TestTextLinkAcrossWrappedRows(t *testing.T) {
	f := newFixture(20, 4)
	f.write("\x1b[?25lgo https://example.com/long/path ok")
	f.frame()
	for _, at := range [][2]int{{5, 0}, {3, 1}} {
		hit, ok := f.tv.linkAt(at[0], at[1], func(uint16) string { return "" })
		if !ok || hit.url != "https://example.com/long/path" {
			t.Fatalf("at %v: %q %v", at, hit.url, ok)
		}
		want := []linkSpan{{0, 3, 19}, {1, 0, 11}}
		if !sameSpans(hit.spans, want) {
			t.Errorf("at %v: spans %v, want %v", at, hit.spans, want)
		}
	}
	if _, ok := f.tv.linkAt(14, 1, func(uint16) string { return "" }); ok {
		t.Error("a link after the URL")
	}
}

// TestOSC8LinkAndHover: an OSC 8 link resolves through the session and
// spans its cells; hovering underlines them until new output arrives.
func TestOSC8LinkAndHover(t *testing.T) {
	f := newFixture(30, 4)
	f.write("\x1b[?25lsee \x1b]8;;https://osc8.example/doc\x1b\\the docs\x1b]8;;\x1b\\ now")
	f.frame()
	resolve := func(id uint16) string {
		f.s.Lock()
		defer f.s.Unlock()
		return f.s.Term.Link(id)
	}
	hit, ok := f.tv.linkAt(6, 0, resolve)
	if !ok || hit.url != "https://osc8.example/doc" || !sameSpans(hit.spans, []linkSpan{{0, 4, 11}}) {
		t.Fatalf("OSC 8 link: %+v %v", hit, ok)
	}
	f.tv.setHover(hit)
	f.frame()
	for x := 0; x < 16; x++ {
		under := f.cell(x, 0).Glyph&flagUnderline != 0
		if under != (x >= 4 && x <= 11) {
			t.Errorf("cell %d underlined %v", x, under)
		}
	}
	f.write("!")
	f.frame()
	if f.tv.links.url != "" || f.cell(6, 0).Glyph&flagUnderline != 0 {
		t.Errorf("after output: hover %q, cell 6 %+v", f.tv.links.url, f.cell(6, 0))
	}
	f.write("\r\n\x1b]8;;javascript:alert(1)\x1b\\bad\x1b]8;;\x1b\\")
	f.frame()
	if hit, ok := f.tv.linkAt(1, 1, resolve); ok {
		t.Errorf("a javascript: link opens: %+v", hit)
	}
}

// TestOSC8LinkAcrossSweeps: a link that stays on screen while link sweeps
// free and reuse the IDs around it still resolves. A link in rows copied
// before a sweep does not resolve until the next frame copies them again,
// so an ID the sweep gave to another URL never opens that URL.
func TestOSC8LinkAcrossSweeps(t *testing.T) {
	f := newFixture(40, 4)
	f.write("\x1b[?25l\x1b]8;;https://keep.example/doc\x1b\\the keeper\x1b]8;;\x1b\\\r\n")
	churn := func(from, to int) {
		var b strings.Builder
		for i := from; i < to; i++ {
			fmt.Fprintf(&b, "\r\x1b]8;;https://churn.example/%d\x1b\\churn %d\x1b]8;;\x1b\\\x1b[K", i, i)
		}
		f.write(b.String())
	}
	resolve := func(id uint16) string { return f.tv.linkURL(f.s, id) }
	churn(0, 5000)
	f.frame()
	if hit, ok := f.tv.linkAt(2, 1, resolve); !ok || hit.url != "https://churn.example/4999" {
		t.Fatalf("churned link: %+v %v", hit, ok)
	}
	sweeps := f.term.LinkStats().Sweeps
	churn(5000, 15000)
	if f.term.LinkStats().Sweeps == sweeps {
		t.Fatal("no link sweep ran")
	}
	if id := f.tv.lines[1].Link(2); f.term.Link(id) == "https://churn.example/4999" {
		t.Fatalf("ID %d of the copied row still names its URL; the test needs it freed", id)
	}
	if hit, ok := f.tv.linkAt(2, 1, resolve); ok {
		t.Errorf("a link of rows copied before the sweep resolved: %+v", hit)
	}
	f.frame()
	for _, c := range []struct {
		col, row int
		url      string
	}{{4, 0, "https://keep.example/doc"}, {2, 1, "https://churn.example/14999"}} {
		if hit, ok := f.tv.linkAt(c.col, c.row, resolve); !ok || hit.url != c.url {
			t.Errorf("cell (%d,%d) after the next frame: %+v %v, want %s", c.col, c.row, hit, ok, c.url)
		}
	}
}

// TestOSC8HoverAcrossSweepInSyncUpdate: while an application holds a
// synchronized update open, the view keeps its last frame and the
// underline under the pointer. A link of that frame does not resolve once
// link sweeps ran, since its ID may name another URL by then. The end of
// the update redraws, clears the underline, and the link resolves again.
func TestOSC8HoverAcrossSweepInSyncUpdate(t *testing.T) {
	const keep = "https://keep.example/doc"
	f := newFixture(40, 4)
	f.write("\x1b[?25l\x1b]8;;" + keep + "\x1b\\the keeper\x1b]8;;\x1b\\\r\n")
	f.frame()
	resolve := func(id uint16) string { return f.tv.linkURL(f.s, id) }
	hit, ok := f.tv.linkAt(4, 0, resolve)
	if !ok || hit.url != keep {
		t.Fatalf("keeper: %+v %v", hit, ok)
	}
	f.tv.setHover(hit)
	f.frame()
	var b strings.Builder
	b.WriteString("\x1b[?2026h")
	for i := range 10000 {
		fmt.Fprintf(&b, "\r\x1b]8;;https://churn.example/%d\x1b\\churn %d\x1b]8;;\x1b\\\x1b[K", i, i)
	}
	f.write(b.String())
	if f.term.LinkStats().Sweeps == 0 {
		t.Fatal("no link sweep ran")
	}
	if drawn, _ := f.frame(); drawn {
		t.Error("a frame was drawn during the synchronized update")
	}
	if f.tv.links.url != keep {
		t.Errorf("underline during the update: %q", f.tv.links.url)
	}
	if hit, ok := f.tv.linkAt(4, 0, resolve); ok {
		t.Errorf("a link of the frame before the sweeps resolved: %+v", hit)
	}
	f.write("\x1b[?2026l")
	f.frame()
	if f.tv.links.url != "" {
		t.Errorf("underline after the update: %q", f.tv.links.url)
	}
	if hit, ok := f.tv.linkAt(4, 0, resolve); !ok || hit.url != keep {
		t.Errorf("keeper after the update: %+v %v", hit, ok)
	}
}

// TestLaunchRefusedOnTestCompositor: the real launcher never runs on the
// headless compositor.
func TestLaunchRefusedOnTestCompositor(t *testing.T) {
	t.Setenv("BUNK_HEADLESS", "1")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	if launchRefused() == nil {
		t.Error("BUNK_HEADLESS set: launch allowed")
	}
	t.Setenv("BUNK_HEADLESS", "")
	t.Setenv("WAYLAND_DISPLAY", "bunk-headless-42")
	if launchRefused() == nil {
		t.Error("headless display: launch allowed")
	}
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	if err := launchRefused(); err != nil {
		t.Errorf("desktop: %v", err)
	}
}
