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

// TestConsumeDirtyInto_MatchesConsumeDirty checks the allocation-free variant:
// same flags, cleared afterwards, and no allocation once dst is large enough.
func TestConsumeDirtyInto_MatchesConsumeDirty(t *testing.T) {
	const cols, rows = 10, 5
	term := newTestTerm(cols, rows)
	buf := make([]bool, 0, rows)
	dirty, any := term.ConsumeDirtyInto(buf)
	if !any || len(dirty) != rows {
		t.Fatalf("initial: any=%v len=%d, want true %d", any, len(dirty), rows)
	}
	term.Write([]byte("\x1b[3;1HX")) //nolint:errcheck
	dirty, any = term.ConsumeDirtyInto(dirty)
	if !any {
		t.Fatal("expected any=true after write")
	}
	for i, d := range dirty {
		if d != (i == 2) {
			t.Errorf("row %d dirty=%v", i, d)
		}
	}
	if dirty, any = term.ConsumeDirtyInto(dirty); any || dirty[2] {
		t.Errorf("second call: any=%v row2=%v, want clean", any, dirty[2])
	}
	msg := []byte("\x1b[2;1HY")
	allocs := testing.AllocsPerRun(100, func() {
		term.Write(msg) //nolint:errcheck
		dirty, _ = term.ConsumeDirtyInto(dirty)
	})
	if allocs != 0 {
		t.Errorf("ConsumeDirtyInto allocated %.1f times per call", allocs)
	}
}

// TestCopyRow copies a live row, including side-table data, without
// allocating once dst has capacity.
func TestCopyRow(t *testing.T) {
	term := newTestTerm(10, 3)
	term.Write([]byte("\x1b[2;1Hab́\x1b[1;31mc")) //nolint:errcheck
	var row Row
	term.CopyRow(1, &row)
	if row.Len() != 10 {
		t.Fatalf("len = %d, want 10", row.Len())
	}
	if g := row.Glyph(1); g.Char != 'b' || g.Combining != "́" {
		t.Errorf("cell 1 = %q+%q, want b with combining acute", g.Char, g.Combining)
	}
	if g := row.Glyph(2); g.Char != 'c' || g.FG != LightRed || g.Mode&AttrBold == 0 {
		t.Errorf("cell 2 = %+v, want bold c in bright red (bold brightens 0-7)", g)
	}
	if ch, fg, _, mode, width, extra := row.CellAt(2); ch != 'c' || fg != LightRed || mode&AttrBold == 0 || width != 1 || extra {
		t.Errorf("CellAt(2) = %q %v %v %v %v", ch, fg, mode, width, extra)
	}
	if _, _, _, _, _, extra := row.CellAt(1); !extra || row.Combining(1) != "\u0301" || row.Combining(2) != "" {
		t.Errorf("CellAt(1) extra=%v Combining(1)=%q Combining(2)=%q", extra, row.Combining(1), row.Combining(2))
	}
	if allocs := testing.AllocsPerRun(100, func() { term.CopyRow(1, &row) }); allocs != 0 {
		t.Errorf("CopyRow allocated %.1f times per call", allocs)
	}
}
