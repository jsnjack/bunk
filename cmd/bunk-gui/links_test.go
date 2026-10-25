package main

import (
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
