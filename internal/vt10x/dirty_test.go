package vt10x

// Tests for ConsumeDirty() — the dirty-row tracking API used by bunk's
// renderPane to skip rows that haven't changed since the last render tick.

import "testing"

// TestConsumeDirty_InitiallyAllDirty verifies that a freshly created terminal
// has all rows marked dirty (resize calls dirtyAll).
func TestConsumeDirty_InitiallyAllDirty(t *testing.T) {
	const cols, rows = 10, 5
	term := newTestTerm(cols, rows)

	dirty, any := term.ConsumeDirty()
	if !any {
		t.Fatal("expected any=true on a fresh terminal")
	}
	if len(dirty) != rows {
		t.Fatalf("dirty len = %d, want %d", len(dirty), rows)
	}
	for i, d := range dirty {
		if !d {
			t.Errorf("row %d not dirty on fresh terminal", i)
		}
	}
}

// TestConsumeDirty_ClearsAfterConsume verifies that a second ConsumeDirty
// call immediately after the first returns nil, false (nothing new).
func TestConsumeDirty_ClearsAfterConsume(t *testing.T) {
	term := newTestTerm(10, 5)
	term.ConsumeDirty() // consume initial state

	dirty, any := term.ConsumeDirty()
	if any {
		t.Error("expected any=false after consuming with no writes")
	}
	if dirty != nil {
		t.Errorf("expected nil dirty slice, got len %d", len(dirty))
	}
}

// TestConsumeDirty_MarksCorrectRow verifies that writing a character to row 2
// marks only row 2 dirty (other rows stay clean after initial consume).
func TestConsumeDirty_MarksCorrectRow(t *testing.T) {
	const cols, rows = 10, 5
	term := newTestTerm(cols, rows)
	term.ConsumeDirty() // drain initial all-dirty

	// Move cursor to row 2 and write a character.
	term.Write([]byte("\x1b[3;1H" + "X")) //nolint:errcheck // CSI 3;1H = row 3, col 1 (1-based)

	dirty, any := term.ConsumeDirty()
	if !any {
		t.Fatal("expected any=true after write")
	}
	if len(dirty) != rows {
		t.Fatalf("dirty len = %d, want %d", len(dirty), rows)
	}
	for i, d := range dirty {
		wantDirty := i == 2 // row 2 (0-based) = CSI row 3
		if d != wantDirty {
			t.Errorf("row %d dirty=%v, want %v", i, d, wantDirty)
		}
	}
}

// TestConsumeDirty_NothingDirtyReturnsNil verifies the zero-allocation fast
// path: when no rows are dirty, ConsumeDirty returns nil and false.
func TestConsumeDirty_NothingDirtyReturnsNil(t *testing.T) {
	term := newTestTerm(10, 5)
	term.ConsumeDirty() // drain initial state — now clean

	rows, any := term.ConsumeDirty()
	if rows != nil || any {
		t.Errorf("ConsumeDirty on clean terminal: got (%v, %v), want (nil, false)", rows, any)
	}
}

// TestConsumeDirty_ResizeMarksAllDirty verifies that Resize marks all rows
// dirty so the first render after a resize performs a full repaint.
func TestConsumeDirty_ResizeMarksAllDirty(t *testing.T) {
	const cols = 10
	term := newTestTerm(cols, 5)
	term.ConsumeDirty() // drain initial

	term.Resize(cols, 8) // height-only resize

	dirty, any := term.ConsumeDirty()
	if !any {
		t.Fatal("expected any=true after resize")
	}
	if len(dirty) != 8 {
		t.Fatalf("dirty len after resize = %d, want 8", len(dirty))
	}
	for i, d := range dirty {
		if !d {
			t.Errorf("row %d not dirty after resize", i)
		}
	}
}
