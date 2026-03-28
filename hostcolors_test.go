package main

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestParseOSCColorReply_ST(t *testing.T) {
	got, ok := parseOSCColorReply([]byte("\x1b]10;rgb:aaaa/bbbb/cccc\x1b\\"), 10)
	if !ok {
		t.Fatal("parseOSCColorReply did not detect complete ST-terminated reply")
	}
	if got != "rgb:aaaa/bbbb/cccc" {
		t.Fatalf("parseOSCColorReply(ST) = %q, want %q", got, "rgb:aaaa/bbbb/cccc")
	}
}

func TestParseOSCColorReply_BEL(t *testing.T) {
	got, ok := parseOSCColorReply([]byte("\x1b]11;rgb:1111/2222/3333\x07"), 11)
	if !ok {
		t.Fatal("parseOSCColorReply did not detect complete BEL-terminated reply")
	}
	if got != "rgb:1111/2222/3333" {
		t.Fatalf("parseOSCColorReply(BEL) = %q, want %q", got, "rgb:1111/2222/3333")
	}
}

func TestParseOSCColorReply_HashColor(t *testing.T) {
	got, ok := parseOSCColorReply([]byte("\x1b]12;#1a2b3c\x1b\\"), 12)
	if !ok {
		t.Fatal("parseOSCColorReply did not detect complete hash-colour reply")
	}
	if got != "rgb:1a1a/2b2b/3c3c" {
		t.Fatalf("parseOSCColorReply(hash) = %q, want %q", got, "rgb:1a1a/2b2b/3c3c")
	}
}

func TestParseOSCColorReply_Incomplete(t *testing.T) {
	if got, ok := parseOSCColorReply([]byte("\x1b]10;rgb:aaaa/bbbb/cccc"), 10); ok || got != "" {
		t.Fatalf("parseOSCColorReply(incomplete) = (%q, %t), want (\"\", false)", got, ok)
	}
}

func TestParseOSCColorReply_IgnoresDifferentOSCNumber(t *testing.T) {
	if got, ok := parseOSCColorReply([]byte("\x1b]11;rgb:aaaa/bbbb/cccc\x1b\\"), 10); ok || got != "" {
		t.Fatalf("parseOSCColorReply(other num) = (%q, %t), want (\"\", false)", got, ok)
	}
}

func TestDefaultOSCColors_ExplicitThemeWins(t *testing.T) {
	theme := resolvedTheme{
		fg: tcell.NewRGBColor(0xd0, 0xd0, 0xd0),
		bg: tcell.NewRGBColor(0x1a, 0x1a, 0x2e),
	}
	host := hostOSCColors{
		fg:     "rgb:1111/2222/3333",
		bg:     "rgb:4444/5555/6666",
		cursor: "rgb:7777/8888/9999",
	}

	fg, bg, cursor := defaultOSCColors(theme, host)
	if fg != "rgb:d0d0/d0d0/d0d0" {
		t.Fatalf("fg = %q, want theme fg", fg)
	}
	if bg != "rgb:1a1a/1a1a/2e2e" {
		t.Fatalf("bg = %q, want theme bg", bg)
	}
	if cursor != "rgb:d0d0/d0d0/d0d0" {
		t.Fatalf("cursor = %q, want theme fg fallback", cursor)
	}
}

func TestDefaultOSCColors_TerminalThemeUsesHostProbe(t *testing.T) {
	theme := resolvedTheme{
		fg: tcell.ColorDefault,
		bg: tcell.ColorDefault,
	}
	host := hostOSCColors{
		fg:     "rgb:1111/2222/3333",
		bg:     "rgb:4444/5555/6666",
		cursor: "rgb:7777/8888/9999",
	}

	fg, bg, cursor := defaultOSCColors(theme, host)
	if fg != host.fg {
		t.Fatalf("fg = %q, want host fg %q", fg, host.fg)
	}
	if bg != host.bg {
		t.Fatalf("bg = %q, want host bg %q", bg, host.bg)
	}
	if cursor != host.cursor {
		t.Fatalf("cursor = %q, want host cursor %q", cursor, host.cursor)
	}
}

func TestDefaultOSCColors_TerminalThemeUnknownCursorStaysUnknown(t *testing.T) {
	theme := resolvedTheme{
		fg: tcell.ColorDefault,
		bg: tcell.ColorDefault,
	}
	host := hostOSCColors{
		fg: "rgb:1111/2222/3333",
		bg: "rgb:4444/5555/6666",
	}

	fg, bg, cursor := defaultOSCColors(theme, host)
	if fg != host.fg || bg != host.bg {
		t.Fatalf("fg/bg = %q/%q, want %q/%q", fg, bg, host.fg, host.bg)
	}
	if cursor != "" {
		t.Fatalf("cursor = %q, want empty when host cursor is unknown", cursor)
	}
}
