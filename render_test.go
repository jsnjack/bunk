package main

import (
	"fmt"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/hinshun/vt10x"
)

// testTheme returns a resolvedTheme with distinct, recognisable colours for
// fg, bg, and a partial ANSI palette.  Slots not explicitly set are left as
// tcell.ColorDefault so the "no palette override" path is exercised.
func testTheme() resolvedTheme {
	rt := resolvedTheme{
		fg:             tcell.NewRGBColor(0xd0, 0xd0, 0xd0), // light grey
		bg:             tcell.NewRGBColor(0x1a, 0x1a, 0x2e), // dark blue
		activeBorder:   tcell.ColorGreen,
		inactiveBorder: tcell.ColorGray,
		scrollThumb:    tcell.ColorWhite,
		scrollTrack:    tcell.ColorDarkGray,
	}
	// Override only a few palette slots so we can test both paths.
	rt.palette[0] = tcell.NewRGBColor(0x00, 0x00, 0x00) // black override
	rt.palette[1] = tcell.NewRGBColor(0xcc, 0x00, 0x00) // red override
	rt.palette[7] = tcell.NewRGBColor(0xaa, 0xaa, 0xaa) // light grey override
	// Slots 2-6, 8-15 remain tcell.ColorDefault (no override).
	return rt
}

// ---------------------------------------------------------------------------
// 1. DefaultFG returns rt.fg regardless of the def parameter
//
// Bug: btop background leak after resize+exit
//
// When btop exits, cells are cleared with the active cursor attributes.
// vtColor must map DefaultFG → rt.fg (not the `def` fallback) so that
// cleared cells pick up the theme foreground, not the app's colours.
// ---------------------------------------------------------------------------

func TestVtColor_DefaultFG_BtopBackgroundLeak(t *testing.T) {
	rt := testTheme()

	// Pass a totally different colour as `def` to prove it is ignored.
	def := tcell.ColorRed
	got := vtColor(vt10x.DefaultFG, def, rt)
	if got != rt.fg {
		t.Errorf("vtColor(DefaultFG, def, rt) = %v; want rt.fg = %v", got, rt.fg)
	}
}

func TestVtColor_DefaultFG_BtopBackgroundLeak_IgnoresDef(t *testing.T) {
	rt := testTheme()

	// Even when def == rt.bg, the result must be rt.fg.
	got := vtColor(vt10x.DefaultFG, rt.bg, rt)
	if got != rt.fg {
		t.Errorf("vtColor(DefaultFG, rt.bg, rt) = %v; want rt.fg = %v", got, rt.fg)
	}
}

// ---------------------------------------------------------------------------
// 2. DefaultBG returns rt.bg regardless of the def parameter
//
// Bug: btop background leak after resize+exit
//
// Same as DefaultFG above — DefaultBG must always → rt.bg so that
// background colours in cleared cells use the theme, not the app's palette.
// ---------------------------------------------------------------------------

func TestVtColor_DefaultBG_BtopBackgroundLeak(t *testing.T) {
	rt := testTheme()

	def := tcell.ColorBlue
	got := vtColor(vt10x.DefaultBG, def, rt)
	if got != rt.bg {
		t.Errorf("vtColor(DefaultBG, def, rt) = %v; want rt.bg = %v", got, rt.bg)
	}
}

func TestVtColor_DefaultBG_BtopBackgroundLeak_IgnoresDef(t *testing.T) {
	rt := testTheme()

	got := vtColor(vt10x.DefaultBG, rt.fg, rt)
	if got != rt.bg {
		t.Errorf("vtColor(DefaultBG, rt.fg, rt) = %v; want rt.bg = %v", got, rt.bg)
	}
}

// ---------------------------------------------------------------------------
// 3. DefaultCursor returns def
// ---------------------------------------------------------------------------

func TestVtColor_DefaultCursor_ReturnsDef(t *testing.T) {
	rt := testTheme()

	defs := []tcell.Color{
		tcell.ColorRed,
		tcell.ColorWhite,
		rt.fg,
		rt.bg,
		tcell.NewRGBColor(0x12, 0x34, 0x56),
	}
	for _, def := range defs {
		got := vtColor(vt10x.DefaultCursor, def, rt)
		if got != def {
			t.Errorf("vtColor(DefaultCursor, %v, rt) = %v; want %v", def, got, def)
		}
	}
}

// ---------------------------------------------------------------------------
// 4. ANSI colors 0-15 WITH theme palette override
// ---------------------------------------------------------------------------

func TestVtColor_ANSIWithPaletteOverride(t *testing.T) {
	rt := testTheme()
	def := tcell.ColorDefault

	// Slot 0 is overridden in testTheme.
	got := vtColor(0, def, rt)
	if got != rt.palette[0] {
		t.Errorf("vtColor(0, def, rt) = %v; want palette[0] = %v", got, rt.palette[0])
	}

	// Slot 1 is overridden.
	got = vtColor(1, def, rt)
	if got != rt.palette[1] {
		t.Errorf("vtColor(1, def, rt) = %v; want palette[1] = %v", got, rt.palette[1])
	}

	// Slot 7 is overridden.
	got = vtColor(7, def, rt)
	if got != rt.palette[7] {
		t.Errorf("vtColor(7, def, rt) = %v; want palette[7] = %v", got, rt.palette[7])
	}
}

// ---------------------------------------------------------------------------
// 5. ANSI colors 0-15 WITHOUT palette override (tcell.ColorDefault in palette)
// ---------------------------------------------------------------------------

func TestVtColor_ANSIWithoutPaletteOverride(t *testing.T) {
	rt := testTheme()
	def := tcell.ColorDefault

	// Slots 2-6 and 8-15 are NOT overridden in testTheme.
	unoverridden := []vt10x.Color{2, 3, 4, 5, 6, 8, 9, 10, 11, 12, 13, 14, 15}
	for _, c := range unoverridden {
		want := tcell.PaletteColor(int(c))
		got := vtColor(c, def, rt)
		if got != want {
			t.Errorf("vtColor(%d, def, rt) = %v; want PaletteColor(%d) = %v", c, got, c, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 6. xterm-256 colors (16-255)
// ---------------------------------------------------------------------------

func TestVtColor_Xterm256(t *testing.T) {
	rt := testTheme()
	def := tcell.ColorDefault

	// Test boundary and a sample of values.
	cases := []vt10x.Color{16, 17, 100, 200, 231, 255}
	for _, c := range cases {
		want := tcell.PaletteColor(int(c))
		got := vtColor(c, def, rt)
		if got != want {
			t.Errorf("vtColor(%d, def, rt) = %v; want PaletteColor(%d) = %v", c, got, c, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 7. Truecolor RGB
// ---------------------------------------------------------------------------

func TestVtColor_Truecolor(t *testing.T) {
	rt := testTheme()
	def := tcell.ColorDefault

	cases := []struct {
		r, g, b byte
	}{
		{0x00, 0x00, 0x00}, // black
		{0xff, 0xff, 0xff}, // white
		{0xca, 0xfe, 0x42}, // arbitrary
		{0x01, 0x00, 0x00}, // near-zero
		{0x00, 0x01, 0x00},
		{0x00, 0x00, 0x01},
	}
	for _, tc := range cases {
		c := vt10x.Color(uint32(tc.r)<<16 | uint32(tc.g)<<8 | uint32(tc.b))
		// Skip if the packed value happens to collide with a low ANSI or
		// xterm-256 index (values 0-255) since those take a different path.
		if c < 256 {
			continue
		}
		want := tcell.NewRGBColor(int32(tc.r), int32(tc.g), int32(tc.b))
		got := vtColor(c, def, rt)
		if got != want {
			t.Errorf("vtColor(rgb(%02x,%02x,%02x)=%d, def, rt) = %v; want %v",
				tc.r, tc.g, tc.b, c, got, want)
		}
	}
}

func TestVtColor_TruecolorBoundary(t *testing.T) {
	rt := testTheme()
	def := tcell.ColorDefault

	// The highest valid truecolor is 0xffffff = 16777215, which must be
	// less than vt10x.DefaultFG (1<<24 = 16777216).
	c := vt10x.Color(0xff_ff_ff)
	if c >= vt10x.DefaultFG {
		t.Fatalf("0xffffff (%d) >= DefaultFG (%d); test assumption broken", c, vt10x.DefaultFG)
	}
	want := tcell.NewRGBColor(0xff, 0xff, 0xff)
	got := vtColor(c, def, rt)
	if got != want {
		t.Errorf("vtColor(0xffffff, def, rt) = %v; want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// 8. Fallback: unknown sentinel values beyond DefaultCursor return def
// ---------------------------------------------------------------------------

func TestVtColor_UnknownSentinel_ReturnsDef(t *testing.T) {
	rt := testTheme()
	def := tcell.NewRGBColor(0x42, 0x42, 0x42)

	// Values above DefaultCursor are unknown; vtColor should return def.
	unknown := vt10x.DefaultCursor + 1
	got := vtColor(unknown, def, rt)
	if got != def {
		t.Errorf("vtColor(%d, def, rt) = %v; want def = %v", unknown, got, def)
	}
}

// ---------------------------------------------------------------------------
// 9. REVERSE VIDEO SCENARIO
//
// Bug: Claude/Copilot cursor invisible in bunk
//
// Claude Code draws its cursor as a reverse-video space (\x1b[7m \x1b[27m).
// vt10x pre-swaps FG/BG for reverse cells, setting cell.FG=DefaultBG and
// cell.BG=DefaultFG.  The old vtColor treated both DefaultFG and DefaultBG
// as "return def", which undid the swap — making the cursor invisible.
//
// The fix: DefaultFG always → rt.fg, DefaultBG always → rt.bg.
// ---------------------------------------------------------------------------

func TestVtColor_ReverseVideo_ClaudeCopilotCursor(t *testing.T) {
	rt := testTheme()

	// Simulate the render call site for a normal (non-reversed) cell:
	//   fg = vtColor(cell.FG=DefaultFG, def=rt.fg, rt)  → rt.fg
	//   bg = vtColor(cell.BG=DefaultBG, def=rt.bg, rt)  → rt.bg
	normalFG := vtColor(vt10x.DefaultFG, rt.fg, rt)
	normalBG := vtColor(vt10x.DefaultBG, rt.bg, rt)
	if normalFG != rt.fg {
		t.Fatalf("normal cell FG: got %v, want rt.fg=%v", normalFG, rt.fg)
	}
	if normalBG != rt.bg {
		t.Fatalf("normal cell BG: got %v, want rt.bg=%v", normalBG, rt.bg)
	}

	// Simulate the render call site for a REVERSED cell.
	// vt10x has already swapped FG↔BG in the Glyph:
	//   cell.FG = DefaultBG   (was background, now foreground)
	//   cell.BG = DefaultFG   (was foreground, now background)
	//
	// The caller still passes the same `def` values as for a normal cell:
	//   fg = vtColor(cell.FG=DefaultBG, def=rt.fg, rt)
	//   bg = vtColor(cell.BG=DefaultFG, def=rt.bg, rt)
	reversedFG := vtColor(vt10x.DefaultBG, rt.fg, rt)
	reversedBG := vtColor(vt10x.DefaultFG, rt.bg, rt)

	// After reverse-video swap, FG should be the theme background colour
	// and BG should be the theme foreground colour.
	if reversedFG != rt.bg {
		t.Errorf("reversed cell FG: vtColor(DefaultBG, rt.fg, rt) = %v; want rt.bg = %v",
			reversedFG, rt.bg)
	}
	if reversedBG != rt.fg {
		t.Errorf("reversed cell BG: vtColor(DefaultFG, rt.bg, rt) = %v; want rt.fg = %v",
			reversedBG, rt.fg)
	}

	// The critical invariant: reversed colours must be the opposite of normal.
	if normalFG == reversedFG {
		t.Error("reversed cell FG must differ from normal cell FG (swap broken)")
	}
	if normalBG == reversedBG {
		t.Error("reversed cell BG must differ from normal cell BG (swap broken)")
	}
}

// TestVtColor_ReverseVideoWithANSIColors verifies that reverse video also
// works correctly when one side is an ANSI colour and the other is a default.
func TestVtColor_ReverseVideoWithANSIColors(t *testing.T) {
	rt := testTheme()

	// Scenario: cell has ANSI red foreground on default background, then reversed.
	// Before reverse: FG=Red(1), BG=DefaultBG
	// After vt10x reverse swap: FG=DefaultBG, BG=Red(1)
	//
	// Render call: fg = vtColor(DefaultBG, rt.fg, rt), bg = vtColor(1, rt.bg, rt)
	fg := vtColor(vt10x.DefaultBG, rt.fg, rt)
	bg := vtColor(1, rt.bg, rt)

	if fg != rt.bg {
		t.Errorf("reversed ANSI cell FG: vtColor(DefaultBG, rt.fg, rt) = %v; want rt.bg = %v",
			fg, rt.bg)
	}
	// Color 1 is overridden in our test palette.
	if bg != rt.palette[1] {
		t.Errorf("reversed ANSI cell BG: vtColor(1, rt.bg, rt) = %v; want palette[1] = %v",
			bg, rt.palette[1])
	}
}

// ---------------------------------------------------------------------------
// Table-driven comprehensive test
// ---------------------------------------------------------------------------

func TestVtColor_TableDriven(t *testing.T) {
	rt := testTheme()

	tests := []struct {
		name string
		c    vt10x.Color
		def  tcell.Color
		want tcell.Color
	}{
		// Defaults
		{"DefaultFG with fg def", vt10x.DefaultFG, rt.fg, rt.fg},
		{"DefaultFG with bg def", vt10x.DefaultFG, rt.bg, rt.fg},
		{"DefaultFG with red def", vt10x.DefaultFG, tcell.ColorRed, rt.fg},
		{"DefaultBG with bg def", vt10x.DefaultBG, rt.bg, rt.bg},
		{"DefaultBG with fg def", vt10x.DefaultBG, rt.fg, rt.bg},
		{"DefaultBG with red def", vt10x.DefaultBG, tcell.ColorRed, rt.bg},
		{"DefaultCursor with fg", vt10x.DefaultCursor, rt.fg, rt.fg},
		{"DefaultCursor with bg", vt10x.DefaultCursor, rt.bg, rt.bg},
		{"DefaultCursor with red", vt10x.DefaultCursor, tcell.ColorRed, tcell.ColorRed},

		// ANSI palette overrides
		{"ANSI 0 (black, overridden)", 0, tcell.ColorDefault, rt.palette[0]},
		{"ANSI 1 (red, overridden)", 1, tcell.ColorDefault, rt.palette[1]},
		{"ANSI 7 (light grey, overridden)", 7, tcell.ColorDefault, rt.palette[7]},

		// ANSI palette no override
		{"ANSI 2 (green, no override)", 2, tcell.ColorDefault, tcell.PaletteColor(2)},
		{"ANSI 15 (white, no override)", 15, tcell.ColorDefault, tcell.PaletteColor(15)},

		// xterm-256
		{"xterm 16", 16, tcell.ColorDefault, tcell.PaletteColor(16)},
		{"xterm 128", 128, tcell.ColorDefault, tcell.PaletteColor(128)},
		{"xterm 255", 255, tcell.ColorDefault, tcell.PaletteColor(255)},

		// Truecolor
		{"truecolor #010101", vt10x.Color(0x010101), tcell.ColorDefault,
			tcell.NewRGBColor(0x01, 0x01, 0x01)},
		{"truecolor #cafe42", vt10x.Color(0xcafe42), tcell.ColorDefault,
			tcell.NewRGBColor(0xca, 0xfe, 0x42)},
		{"truecolor #ffffff", vt10x.Color(0xffffff), tcell.ColorDefault,
			tcell.NewRGBColor(0xff, 0xff, 0xff)},

		// Unknown sentinel
		{"unknown past DefaultCursor", vt10x.DefaultCursor + 1, tcell.ColorRed, tcell.ColorRed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vtColor(tt.c, tt.def, rt)
			if got != tt.want {
				t.Errorf("vtColor(%d, %v, rt) = %v; want %v", tt.c, tt.def, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

// TestVtColor_Color256Boundary checks the boundary between ANSI (0-15) and
// xterm-256 (16-255).
func TestVtColor_Color256Boundary(t *testing.T) {
	rt := testTheme()
	def := tcell.ColorDefault

	// Color 15 is the last ANSI slot; not overridden in testTheme → PaletteColor.
	got15 := vtColor(15, def, rt)
	want15 := tcell.PaletteColor(15)
	if got15 != want15 {
		t.Errorf("vtColor(15) = %v; want PaletteColor(15) = %v", got15, want15)
	}

	// Color 16 is the first xterm-256 slot.
	got16 := vtColor(16, def, rt)
	want16 := tcell.PaletteColor(16)
	if got16 != want16 {
		t.Errorf("vtColor(16) = %v; want PaletteColor(16) = %v", got16, want16)
	}
}

// TestVtColor_AllANSISlots verifies that every ANSI slot (0-15) returns either
// the palette override or PaletteColor, never anything else.
func TestVtColor_AllANSISlots(t *testing.T) {
	rt := testTheme()
	def := tcell.ColorDefault

	for i := vt10x.Color(0); i < 16; i++ {
		got := vtColor(i, def, rt)
		if rt.palette[i] != tcell.ColorDefault {
			if got != rt.palette[i] {
				t.Errorf("ANSI %d: got %v; want palette override %v", i, got, rt.palette[i])
			}
		} else {
			want := tcell.PaletteColor(int(i))
			if got != want {
				t.Errorf("ANSI %d: got %v; want PaletteColor(%d) = %v", i, got, i, want)
			}
		}
	}
}

// TestVtColor_TruecolorComponents tests that RGB extraction from the packed
// vt10x format is correct for a range of values.
func TestVtColor_TruecolorComponents(t *testing.T) {
	rt := testTheme()
	def := tcell.ColorDefault

	// Generate a spread of RGB values to test component extraction.
	components := []struct{ r, g, b int32 }{
		{0x10, 0x20, 0x30},
		{0xff, 0x00, 0x00},
		{0x00, 0xff, 0x00},
		{0x00, 0x00, 0xff},
		{0x80, 0x80, 0x80},
		{0xde, 0xad, 0xbe},
	}

	for _, comp := range components {
		packed := vt10x.Color(uint32(comp.r)<<16 | uint32(comp.g)<<8 | uint32(comp.b))
		if packed < 256 {
			continue // skip values that fall in the palette range
		}
		want := tcell.NewRGBColor(comp.r, comp.g, comp.b)
		got := vtColor(packed, def, rt)
		if got != want {
			t.Errorf("truecolor #%02x%02x%02x: got %v; want %v",
				comp.r, comp.g, comp.b, got, want)
		}
	}
}

// TestVtColor_EmptyPalette verifies behaviour when the theme has NO palette
// overrides at all (all slots are tcell.ColorDefault).
func TestVtColor_EmptyPalette(t *testing.T) {
	rt := resolvedTheme{
		fg: tcell.NewRGBColor(0xff, 0xff, 0xff),
		bg: tcell.NewRGBColor(0x00, 0x00, 0x00),
	}
	// palette is zero-value: all tcell.ColorDefault (which is 0).
	def := tcell.ColorDefault

	for i := vt10x.Color(0); i < 16; i++ {
		want := tcell.PaletteColor(int(i))
		got := vtColor(i, def, rt)
		if got != want {
			t.Errorf("empty palette ANSI %d: got %v; want PaletteColor(%d) = %v",
				i, got, i, want)
		}
	}
}

// TestVtColor_FullPalette verifies behaviour when every palette slot is overridden.
func TestVtColor_FullPalette(t *testing.T) {
	rt := resolvedTheme{
		fg: tcell.NewRGBColor(0xff, 0xff, 0xff),
		bg: tcell.NewRGBColor(0x00, 0x00, 0x00),
	}
	for i := 0; i < 16; i++ {
		// Each slot gets a unique colour.
		rt.palette[i] = tcell.NewRGBColor(int32(i*16), int32(i*16+1), int32(i*16+2))
	}
	def := tcell.ColorDefault

	for i := vt10x.Color(0); i < 16; i++ {
		got := vtColor(i, def, rt)
		if got != rt.palette[i] {
			t.Errorf("full palette ANSI %d: got %v; want %v", i, got, rt.palette[i])
		}
	}
}

// TestVtColor_DefaultFGBGAreDistinct_ClaudeCopilotCursor confirms that
// DefaultFG and DefaultBG produce different results — the core invariant for
// reverse video.  Without this, the cursor drawn by Claude/Copilot
// (\x1b[7m space) is invisible because the FG/BG swap is undone.
func TestVtColor_DefaultFGBGAreDistinct_ClaudeCopilotCursor(t *testing.T) {
	rt := testTheme()
	if rt.fg == rt.bg {
		t.Skip("test theme fg == bg; pick different colours")
	}

	fgResult := vtColor(vt10x.DefaultFG, tcell.ColorDefault, rt)
	bgResult := vtColor(vt10x.DefaultBG, tcell.ColorDefault, rt)
	if fgResult == bgResult {
		t.Errorf("vtColor(DefaultFG) == vtColor(DefaultBG) == %v; they must differ for reverse video", fgResult)
	}
}

// TestVtColor_DefParameterIrrelevant_BtopBackgroundLeak exhaustively verifies
// that the `def` parameter has zero effect on DefaultFG and DefaultBG results.
// This prevents background colours leaking from alt-screen apps like btop.
func TestVtColor_DefParameterIrrelevant_BtopBackgroundLeak(t *testing.T) {
	rt := testTheme()

	defs := []tcell.Color{
		tcell.ColorDefault,
		tcell.ColorRed,
		tcell.ColorBlue,
		rt.fg,
		rt.bg,
		tcell.NewRGBColor(0x12, 0x34, 0x56),
		tcell.NewRGBColor(0xff, 0x00, 0xff),
	}

	for _, def := range defs {
		t.Run(fmt.Sprintf("DefaultFG_def_%v", def), func(t *testing.T) {
			got := vtColor(vt10x.DefaultFG, def, rt)
			if got != rt.fg {
				t.Errorf("vtColor(DefaultFG, %v, rt) = %v; want rt.fg = %v", def, got, rt.fg)
			}
		})
		t.Run(fmt.Sprintf("DefaultBG_def_%v", def), func(t *testing.T) {
			got := vtColor(vt10x.DefaultBG, def, rt)
			if got != rt.bg {
				t.Errorf("vtColor(DefaultBG, %v, rt) = %v; want rt.bg = %v", def, got, rt.bg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// checkAndClearNeedsSync
//
// Bug: btop background artifact persists after exit
//
// After alt-screen exit, needsSync is set on the pane so that the render
// loop uses Sync() (full repaint).  checkAndClearNeedsSync walks the BSP
// tree, returns true if any pane had it set, and clears them all.
// ---------------------------------------------------------------------------

func TestCheckAndClearNeedsSync_NoPanes(t *testing.T) {
	if checkAndClearNeedsSync(nil) {
		t.Error("checkAndClearNeedsSync(nil) = true, want false")
	}
}

func TestCheckAndClearNeedsSync_SingleLeaf_Clean(t *testing.T) {
	p := &Pane{needsSync: false}
	n := &Node{pane: p}
	if checkAndClearNeedsSync(n) {
		t.Error("checkAndClearNeedsSync(clean pane) = true, want false")
	}
}

func TestCheckAndClearNeedsSync_SingleLeaf_Dirty(t *testing.T) {
	p := &Pane{needsSync: true}
	n := &Node{pane: p}
	if !checkAndClearNeedsSync(n) {
		t.Error("checkAndClearNeedsSync(dirty pane) = false, want true")
	}
	// Should have cleared the flag.
	if p.needsSync {
		t.Error("needsSync not cleared after checkAndClearNeedsSync")
	}
}

func TestCheckAndClearNeedsSync_Tree_OneDirty(t *testing.T) {
	p1 := &Pane{needsSync: false}
	p2 := &Pane{needsSync: true}
	root := &Node{
		left:  &Node{pane: p1},
		right: &Node{pane: p2},
	}
	if !checkAndClearNeedsSync(root) {
		t.Error("checkAndClearNeedsSync with one dirty = false, want true")
	}
	if p2.needsSync {
		t.Error("p2.needsSync not cleared")
	}
}

func TestCheckAndClearNeedsSync_Tree_AllDirty(t *testing.T) {
	p1 := &Pane{needsSync: true}
	p2 := &Pane{needsSync: true}
	root := &Node{
		left:  &Node{pane: p1},
		right: &Node{pane: p2},
	}
	if !checkAndClearNeedsSync(root) {
		t.Error("checkAndClearNeedsSync with both dirty = false, want true")
	}
	if p1.needsSync || p2.needsSync {
		t.Error("not all needsSync flags cleared")
	}
}
