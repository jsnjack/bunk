package vt10x

// TDD tests for terminal state that bunk previously tracked manually in pane.go.
// These tests are written BEFORE the implementation so they fail first, then
// pass once the features are added to vt10x.
//
// Features covered:
//   - DECSET 2004 (bracketed paste)      → ModeSetPaste
//   - DECSET 2026 (synchronized update)  → ModeSync
//   - DECSCUSR (cursor shape)            → Cursor.Shape
//   - QueryPrivateMode()                 → centralized DECRQM responses

import "testing"

// newTestTerm creates a fresh terminal with the given dimensions using the
// internal newTerminal constructor so Write() is available in package tests.
func newTestTerm(cols, rows int) *terminal {
	return newTerminal(TerminalInfo{w: nil, cols: cols, rows: rows})
}

// ---------------------------------------------------------------------------
// DECSET 2004 — Bracketed paste mode
// ---------------------------------------------------------------------------

func TestMode2004_BracketedPaste_Default(t *testing.T) {
	term := newTestTerm(80, 24)
	if term.Mode()&ModeSetPaste != 0 {
		t.Fatal("bracketed paste must be off by default")
	}
}

func TestMode2004_BracketedPaste_EnableDisable(t *testing.T) {
	term := newTestTerm(80, 24)

	term.Write([]byte("\x1b[?2004h")) //nolint:errcheck
	if term.Mode()&ModeSetPaste == 0 {
		t.Fatal("ModeSetPaste should be set after \\x1b[?2004h")
	}

	term.Write([]byte("\x1b[?2004l")) //nolint:errcheck
	if term.Mode()&ModeSetPaste != 0 {
		t.Fatal("ModeSetPaste should be clear after \\x1b[?2004l")
	}
}

func TestMode2004_BracketedPaste_LastWins(t *testing.T) {
	// When enable and disable appear in the same chunk, last one wins.
	term := newTestTerm(80, 24)

	// enable then disable — disable wins
	term.Write([]byte("\x1b[?2004h\x1b[?2004l")) //nolint:errcheck
	if term.Mode()&ModeSetPaste != 0 {
		t.Fatal("disable after enable in same write: ModeSetPaste should be clear")
	}

	// disable then enable — enable wins
	term.Write([]byte("\x1b[?2004l\x1b[?2004h")) //nolint:errcheck
	if term.Mode()&ModeSetPaste == 0 {
		t.Fatal("enable after disable in same write: ModeSetPaste should be set")
	}
}

// ---------------------------------------------------------------------------
// DECSET 2026 — Synchronized update mode
// ---------------------------------------------------------------------------

func TestMode2026_SyncUpdate_Default(t *testing.T) {
	term := newTestTerm(80, 24)
	if term.Mode()&ModeSync != 0 {
		t.Fatal("sync update must be off by default")
	}
}

func TestMode2026_SyncUpdate_EnableDisable(t *testing.T) {
	term := newTestTerm(80, 24)

	term.Write([]byte("\x1b[?2026h")) //nolint:errcheck
	if term.Mode()&ModeSync == 0 {
		t.Fatal("ModeSync should be set after \\x1b[?2026h")
	}

	term.Write([]byte("\x1b[?2026l")) //nolint:errcheck
	if term.Mode()&ModeSync != 0 {
		t.Fatal("ModeSync should be clear after \\x1b[?2026l")
	}
}

// ---------------------------------------------------------------------------
// DECSCUSR — Set cursor style
// ESC [ Ps SP q   (SP = 0x20, final byte = 'q')
// ---------------------------------------------------------------------------

func TestDECSCUSR_CursorShape_Default(t *testing.T) {
	term := newTestTerm(80, 24)
	if term.Cursor().Shape != 0 {
		t.Fatalf("initial cursor shape = %d, want 0", term.Cursor().Shape)
	}
}

func TestDECSCUSR_CursorShape_AllValues(t *testing.T) {
	for ps := 0; ps <= 6; ps++ {
		term := newTestTerm(80, 24)
		seq := []byte{0x1b, '[', byte('0' + ps), ' ', 'q'}
		term.Write(seq) //nolint:errcheck
		if got := term.Cursor().Shape; got != ps {
			t.Errorf("DECSCUSR %d: Cursor.Shape = %d, want %d", ps, got, ps)
		}
	}
}

func TestDECSCUSR_CursorShape_DefaultReset(t *testing.T) {
	// ESC [ SP q (no explicit digit → Ps=0) should reset to 0 (default).
	term := newTestTerm(80, 24)
	term.Write([]byte{0x1b, '[', '5', ' ', 'q'}) //nolint:errcheck — set blinking bar
	if got := term.Cursor().Shape; got != 5 {
		t.Fatalf("prerequisite failed: shape = %d after ESC[5 q, want 5", got)
	}
	term.Write([]byte{0x1b, '[', ' ', 'q'}) //nolint:errcheck — ESC[ SP q = reset
	if got := term.Cursor().Shape; got != 0 {
		t.Fatalf("shape = %d after ESC[ q, want 0 (default)", got)
	}
}

func TestDECSCUSR_CursorShape_LastOccurrence(t *testing.T) {
	term := newTestTerm(80, 24)
	term.Write([]byte{
		0x1b, '[', '2', ' ', 'q', // steady underline
		0x1b, '[', '4', ' ', 'q', // blinking underline
	}) //nolint:errcheck
	if got := term.Cursor().Shape; got != 4 {
		t.Errorf("last DECSCUSR wins: shape = %d, want 4", got)
	}
}

func TestDECSCUSR_CursorShape_PlainText_Unaffected(t *testing.T) {
	term := newTestTerm(80, 24)
	term.Write([]byte("hello q world")) //nolint:errcheck
	if got := term.Cursor().Shape; got != 0 {
		t.Errorf("plain 'q' in text changed cursor shape to %d", got)
	}
}

// ---------------------------------------------------------------------------
// QueryPrivateMode — centralized DECRQM answers
// ---------------------------------------------------------------------------

func TestQueryPrivateMode_UnknownMode(t *testing.T) {
	term := newTestTerm(80, 24)
	for _, m := range []int{0, 99, 9999} {
		got := term.QueryPrivateMode(m)
		if got != '4' {
			t.Errorf("QueryPrivateMode(%d) = %c, want '4' (not recognized)", m, got)
		}
	}
}

func TestQueryPrivateMode_KnownModes_DefaultReset(t *testing.T) {
	term := newTestTerm(80, 24)
	// These are all reset (2) by default.
	for _, m := range []int{1, 1000, 1002, 1003, 1004, 1006, 1049, 2004, 2026} {
		got := term.QueryPrivateMode(m)
		if got != '2' {
			t.Errorf("QueryPrivateMode(%d) default = %c, want '2' (reset)", m, got)
		}
	}
	// Mode 7 (DECAWM, auto-wrap) is ON by default.
	if got := term.QueryPrivateMode(7); got != '1' {
		t.Errorf("QueryPrivateMode(7) default = %c, want '1' (DECAWM set by default)", got)
	}
}

func TestQueryPrivateMode_Mode25_CursorVisible_Default(t *testing.T) {
	// Mode 25 = DECTCEM. Cursor is visible by default → ModeHide is off → return '1'.
	term := newTestTerm(80, 24)
	if got := term.QueryPrivateMode(25); got != '1' {
		t.Errorf("QueryPrivateMode(25) default = %c, want '1' (cursor visible)", got)
	}
	// Hide cursor, then query
	term.Write([]byte("\x1b[?25l")) //nolint:errcheck
	if got := term.QueryPrivateMode(25); got != '2' {
		t.Errorf("QueryPrivateMode(25) after hide = %c, want '2'", got)
	}
}

func TestQueryPrivateMode_Mode2004_ReflectsState(t *testing.T) {
	term := newTestTerm(80, 24)

	if got := term.QueryPrivateMode(2004); got != '2' {
		t.Fatalf("2004 before enable = %c, want '2'", got)
	}
	term.Write([]byte("\x1b[?2004h")) //nolint:errcheck
	if got := term.QueryPrivateMode(2004); got != '1' {
		t.Fatalf("2004 after enable = %c, want '1'", got)
	}
	term.Write([]byte("\x1b[?2004l")) //nolint:errcheck
	if got := term.QueryPrivateMode(2004); got != '2' {
		t.Fatalf("2004 after disable = %c, want '2'", got)
	}
}

func TestQueryPrivateMode_Mode2026_ReflectsState(t *testing.T) {
	term := newTestTerm(80, 24)

	if got := term.QueryPrivateMode(2026); got != '2' {
		t.Fatalf("2026 before enable = %c, want '2'", got)
	}
	term.Write([]byte("\x1b[?2026h")) //nolint:errcheck
	if got := term.QueryPrivateMode(2026); got != '1' {
		t.Fatalf("2026 after enable = %c, want '1'", got)
	}
}

func TestQueryPrivateMode_Mode1049_AltScreen(t *testing.T) {
	term := newTestTerm(80, 24)

	if got := term.QueryPrivateMode(1049); got != '2' {
		t.Fatalf("1049 before alt screen = %c, want '2'", got)
	}
	term.Write([]byte("\x1b[?1049h")) //nolint:errcheck
	if got := term.QueryPrivateMode(1049); got != '1' {
		t.Fatalf("1049 after entering alt screen = %c, want '1'", got)
	}
}

// ---------------------------------------------------------------------------
// ESC c (RIS — Reset to Initial State)
// ---------------------------------------------------------------------------

func TestRIS_ClearsFullScreen(t *testing.T) {
	// Bug: reset() called clear(0, 0, rows-1, cols-1) — swapped axes.
	// On a wide terminal (cols >> rows) only the first `rows` columns were
	// blanked, leaving the rest of each line intact. ESC c should blank every
	// cell in the grid.
	const cols, rows = 220, 24
	term := newTestTerm(cols, rows)

	// Fill every cell with 'X'.
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			term.Write([]byte("X")) //nolint:errcheck
		}
	}

	// ESC c = RIS
	term.Write([]byte("\x1bc")) //nolint:errcheck

	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			g := term.Cell(c, r)
			if g.Char != 0 && g.Char != ' ' {
				t.Errorf("cell(%d,%d) = %q after RIS, want blank", c, r, g.Char)
				return
			}
		}
	}
}
