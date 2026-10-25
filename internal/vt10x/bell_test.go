package vt10x

import "testing"

// TestBellCallback counts only bells outside escape strings: a BEL that
// terminates an OSC title is not a bell.
func TestBellCallback(t *testing.T) {
	bells := 0
	term := New(WithSize(10, 2), WithBellCallback(func() { bells++ }))
	if _, err := term.Write([]byte("a\ab\x1b]0;title\x07c\x1b]2;x\x1b\\\a")); err != nil {
		t.Fatal(err)
	}
	if bells != 2 {
		t.Fatalf("bells = %d, want 2", bells)
	}
	if got := term.Title(); got != "x" {
		t.Fatalf("title = %q, want %q", got, "x")
	}
}
