package vt10x

import (
	"bunk/internal/graphics"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/rivo/uniseg"
)

const (
	tabspaces = 8
)

const (
	attrReverse = 1 << iota
	attrUnderline
	attrBold
	attrGfx
	attrItalic
	attrBlink
	attrWrap
	attrDim           // SGR 2  — faint/dim intensity
	attrStrikethrough // SGR 9  — crossed-out
	attrInvisible     // SGR 8  — conceal/invisible
	// Three bits encode the underline style (SGR 4:N).  They are only
	// meaningful when attrUnderline is also set.
	// Bit layout: bits 10-12 of Mode (values 1024/2048/4096).
	// style=0 → solid; 1 → double; 2 → curly; 3 → dotted; 4 → dashed.
	attrUnderlineStyleBit0 // 1024
	attrUnderlineStyleBit1 // 2048
	attrUnderlineStyleBit2 // 4096
	attrHasULColor         // 8192: SGR 58 was explicitly set; UL field is valid
	attrOverline           // 16384: SGR 53 — overline
)

// attrUnderlineStyleMask covers all three underline-style bits.
const attrUnderlineStyleMask = attrUnderlineStyleBit0 | attrUnderlineStyleBit1 | attrUnderlineStyleBit2

// Exported aliases so callers outside this package can reference the
// attribute bitmasks without duplicating magic numbers.
const (
	AttrReverse            = attrReverse
	AttrUnderline          = attrUnderline
	AttrBold               = attrBold
	AttrGfx                = attrGfx
	AttrItalic             = attrItalic
	AttrBlink              = attrBlink
	AttrWrap               = attrWrap
	AttrDim                = attrDim
	AttrStrikethrough      = attrStrikethrough
	AttrInvisible          = attrInvisible
	AttrUnderlineStyleBit0 = attrUnderlineStyleBit0
	AttrUnderlineStyleBit1 = attrUnderlineStyleBit1
	AttrUnderlineStyleBit2 = attrUnderlineStyleBit2
	AttrUnderlineStyleMask = attrUnderlineStyleMask
	AttrHasULColor         = attrHasULColor
	AttrOverline           = attrOverline
)

const (
	cursorDefault = 1 << iota
	cursorWrapNext
	cursorOrigin
)

// CursorWrapNext marks a cursor waiting to wrap before the next printable glyph.
const CursorWrapNext = cursorWrapNext

// ModeFlag represents various terminal mode states.
type ModeFlag uint32

// Terminal modes
const (
	ModeWrap ModeFlag = 1 << iota
	ModeInsert
	ModeAppKeypad
	ModeAltScreen
	ModeCRLF
	ModeMouseButton
	ModeMouseMotion
	ModeReverse
	ModeKeyboardLock
	ModeHide
	ModeEcho
	ModeAppCursor
	ModeMouseSgr
	Mode8bit
	ModeBlink
	ModeFBlink
	ModeFocus
	ModeMouseX10
	ModeMouseMany
	ModeSetPaste  // DECSET 2004 — bracketed paste mode
	ModeSync      // DECSET 2026 — synchronized update
	ModeGrapheme  // DECSET 2027 — extended grapheme cluster widths
	ModeMouseMask = ModeMouseButton | ModeMouseMotion | ModeMouseX10 | ModeMouseMany
)

// ChangeFlag represents possible state changes of the terminal.
type ChangeFlag uint32

// Terminal changes to occur in VT.ReadState
const (
	ChangedScreen ChangeFlag = 1 << iota
	ChangedTitle
)

type Glyph struct {
	Char      rune
	Combining string // immutable suffix of the grapheme, shared safely by snapshots
	Width     int8   // 2: wide, -1: continuation, -2: wrap padding, 0/1: narrow
	Mode      int16
	FG, BG    Color
	UL        Color      // SGR 58 underline color; DefaultUL means inherit from FG
	Link      uint16     // OSC 8 hyperlink ID; 0 = no link, otherwise index into State.links (1-based)
	Image     *ImageCell // immutable cell-local raster data, shared by scrollback snapshots
}

type Cursor struct {
	Attr  Glyph
	X, Y  int
	State uint8
	Shape int // DECSCUSR cursor shape (0-6): 0=default, 1=blinking block, 2=steady block,
	// 3=blinking underline, 4=steady underline, 5=blinking bar, 6=steady bar.
}

// parseState selects the handler for the next rune. A small enum keeps state
// changes free of the closure allocation a bound method value needs.
type parseState uint8

const (
	stateGround parseState = iota
	stateEsc
	stateEscCSI
	stateEscStr
	stateEscStrEnd
	stateEscAltCharset
	stateEscTest
)

// State represents the terminal emulation state. Use Lock/Unlock
// methods to synchronize data access with VT.
type State struct {
	DebugLogger *log.Logger

	w                  io.Writer
	mu                 sync.Mutex
	changed            ChangeFlag
	cols, rows         int
	lines              []*Row // pointers, so scrolling moves 8 bytes per row
	altLines           []*Row
	dirty              []bool // line dirtiness
	anydirty           bool
	allDirty           bool // every row is dirty; dirty is not updated
	cur, curSaved      Cursor
	top, bottom        int // scroll limits
	mode               ModeFlag
	state              parseState
	clusterOpen        bool
	clusterX, clusterY int
	str                strEscape
	csi                csiEscape
	numlock            bool
	tabs               []bool
	title              string
	colorOverride      map[Color]Color
	// colorGen bumps every time a dynamic-colour override changes (OSC
	// 10/11/12/4 set or reset).  Because Cell() resolves DefaultBG/FG through
	// colorOverride, a change repaints the appearance of EVERY cell — including
	// blank ones the renderer would otherwise skip via dirty tracking.  The
	// renderer watches this counter to force a full repaint when it changes.
	colorGen uint64
	// links / linkIDs together form the OSC 8 hyperlink intern table.
	//
	// links[id-1] = URL gives the slice for ID→URL lookup (used by render).
	// linkIDs[url] = id gives O(1) dedup when the same URL is seen multiple
	// times — without it, ls --hyperlink in a 5k-file directory does
	// quadratic string compares.
	//
	// Index 0 is reserved for "no link" so a zero-valued Glyph.Link means
	// "this cell isn't part of a hyperlink". Slots are append-only —
	// scrollback rows keep their IDs valid for the lifetime of the State.
	// Capped at 0xFFFF (uint16 max) per pane; past that new URLs degrade
	// silently to "no link", which is the right failure mode (rendering
	// stays correct, just no clickable link for the overflow URLs).
	links     []string
	linkIDs   map[string]uint16
	linkBytes int
	// scrollRowCb, if non-nil, is called once per row that scrolls off the
	// top of the primary screen (orig == 0 in scrollUp).  It fires before
	// the row's backing storage is cleared, so the content is still intact.
	scrollRowCb func(row Row)
	// sbClearCb, if non-nil, is called when the application requests
	// scrollback erasure via ED 3 (CSI 3 J) or RIS (ESC c).  Scrollback
	// lives outside this State (which only holds the visible grid), so
	// erasure is delegated to the owner.  Not fired by init-time reset().
	sbClearCb             func()
	bellCb                func() // BEL outside escape strings
	graphics              *graphics.Decoder
	graphicsReply         func([]byte)
	cellWidth, cellHeight int
	graphicsRows          int // replay viewport height; zero uses the live grid
}

func newState(w io.Writer) *State {
	return &State{
		w:             w,
		colorOverride: make(map[Color]Color),
		linkIDs:       make(map[string]uint16),
	}
}

func (t *State) logf(format string, args ...interface{}) {
	if t.DebugLogger != nil {
		t.DebugLogger.Printf(format, args...)
	}
}

// logRune logs one parsed rune. The nil check comes before string(c), so the
// parser allocates nothing when debug logging is off.
func (t *State) logRune(c rune) {
	if t.DebugLogger != nil {
		t.DebugLogger.Printf("%q", string(c))
	}
}

func (t *State) logln(s string) {
	if t.DebugLogger != nil {
		t.DebugLogger.Println(s)
	}
}

func (t *State) lock() {
	t.mu.Lock()
}

func (t *State) unlock() {
	t.mu.Unlock()
}

// Lock locks the state object's mutex.
func (t *State) Lock() {
	t.mu.Lock()
}

// Unlock resets change flags and unlocks the state object's mutex.
func (t *State) Unlock() {
	t.resetChanges()
	t.mu.Unlock()
}

// Cell returns the glyph containing the character code, foreground color, and
// background color at position (x, y) relative to the top left of the terminal.
func (t *State) Cell(x, y int) Glyph {
	cell := t.lines[y].Glyph(x)
	fg, ok := t.colorOverride[cell.FG]
	if ok {
		cell.FG = fg
	}
	bg, ok := t.colorOverride[cell.BG]
	if ok {
		cell.BG = bg
	}
	return cell
}

// RawCell returns a glyph without resolving dynamic colour overrides.
func (t *State) RawCell(x, y int) Glyph { return t.lines[y].Glyph(x) }

// ImportGlyph translates hyperlink identities. The caller must synchronize
// access to both terminals, as for Cell and Link.
func (t *State) ImportGlyph(g Glyph, source View) Glyph {
	if g.Link != 0 && source != nil {
		g.Link = t.internLink(source.Link(g.Link))
	}
	return g
}

// ReplaceScreen installs reflowed cells without resetting terminal state.
// source supplies the hyperlink table used by cells. The caller must prevent
// concurrent access to source, just as for Cell and Link.
func (t *State) ReplaceScreen(cols, rows int, cells [][]Glyph, cursor Cursor, source View) {
	if cols < 1 || rows < 1 {
		return
	}
	t.lock()
	defer t.unlock()
	top, bottom, oldRows := t.top, t.bottom, t.rows
	t.resize(cols, rows)
	if bottom != oldRows-1 || top != 0 {
		t.setScroll(clamp(top, 0, rows-1), clamp(bottom, 0, rows-1))
	}
	blank := t.defaultCursor().Attr
	blank.Char = ' '
	for y := range t.lines {
		t.lines[y].clearExtras() // every cell is rewritten below
		for x := 0; x < cols; x++ {
			g := blank
			if y < len(cells) && x < len(cells[y]) {
				g = cells[y][x]
				if g.Char == 0 && g.Width >= 0 {
					g = blank
				}
				g = t.ImportGlyph(g, source)
			}
			t.lines[y].set(x, g)
		}
		t.repairWideRow(y)
	}
	t.cur.X, t.cur.Y = clamp(cursor.X, 0, cols-1), clamp(cursor.Y, 0, rows-1)
	t.cur.State = t.cur.State&^cursorWrapNext | cursor.State&cursorWrapNext
	t.dirtyAll()
}

// Cursor returns the current position of the cursor.
func (t *State) Cursor() Cursor {
	return t.cur
}

// CursorPosition returns the cursor position relative to the active origin.
func (t *State) CursorPosition() (x, y int) {
	x, y = t.cur.X, t.cur.Y
	if t.cur.State&cursorOrigin != 0 {
		y -= t.top
	}
	return x, y
}

// CursorVisible returns the visible state of the cursor.
func (t *State) CursorVisible() bool {
	return t.mode&ModeHide == 0
}

// Mode returns the current terminal mode.
func (t *State) Mode() ModeFlag {
	return t.mode
}

// QueryPrivateMode returns the DECRQM status byte for a DEC private mode number.
// Return values follow DECRQM: '0' = unknown, '1' = set, '2' = reset.
// This is used to generate \x1b[?N;S$y responses without hardcoding them in the host.
func (t *State) QueryPrivateMode(mode int) byte {
	if mode == 12 {
		if t.cur.Shape == 0 || t.cur.Shape%2 == 1 {
			return '1'
		}
		return '2'
	}
	if mode == 6 {
		if t.cur.State&cursorOrigin != 0 {
			return '1'
		}
		return '2'
	}
	// Mode 25 (DECTCEM) is inverted: "mode 25 set" means cursor visible, but
	// internally we track ModeHide (cursor hidden). Invert before returning.
	if mode == 25 {
		if t.mode&ModeHide == 0 {
			return '1' // cursor visible → DECTCEM is "set"
		}
		return '2' // cursor hidden → DECTCEM is "reset"
	}
	flag, ok := t.privateModeFlag(mode)
	if !ok {
		return '0' // not recognized
	}
	if t.mode&flag != 0 {
		return '1' // set
	}
	return '2' // reset
}

// ColorOverride returns the active dynamic-colour override for c, if present.
func (t *State) ColorOverride(c Color) (Color, bool) {
	v, ok := t.colorOverride[c]
	return v, ok
}

// ColorGen returns a counter that increments every time a dynamic-colour
// override changes.  The renderer compares it against the value seen on the
// previous frame to decide whether a full repaint is needed (a colour change
// affects every DefaultBG/FG cell, not just dirty rows).
func (t *State) ColorGen() uint64 {
	return t.colorGen
}

// privateModeFlag maps a DEC private mode number to its ModeFlag.
// Returns (0, false) for untracked modes.
func (t *State) privateModeFlag(mode int) (ModeFlag, bool) {
	switch mode {
	case 1:
		return ModeAppCursor, true
	case 5:
		return ModeReverse, true
	case 7:
		return ModeWrap, true
	case 1000:
		return ModeMouseButton, true
	case 1002:
		return ModeMouseMotion, true
	case 1003:
		return ModeMouseMany, true
	case 1004:
		return ModeFocus, true
	case 1006:
		return ModeMouseSgr, true
	case 47, 1047, 1049:
		return ModeAltScreen, true
	case 2004:
		return ModeSetPaste, true
	case 2026:
		return ModeSync, true
	case 2027:
		return ModeGrapheme, true
	case 9:
		return ModeMouseX10, true
	case 66:
		return ModeAppKeypad, true
	default:
		return 0, false
	}
}

// Title returns the current title set via the tty.
func (t *State) Title() string {
	return t.title
}

// Link returns the OSC 8 hyperlink URL for the given ID, or "" if id is 0
// (meaning "no link") or out of range. Callers must hold the State lock.
func (t *State) Link(id uint16) string {
	if id == 0 || int(id) > len(t.links) {
		return ""
	}
	return t.links[id-1]
}

// internLink interns url and returns its 1-based ID. Returns 0 for "" or when
// the per-pane table is full (uint16 max ≈ 65k unique URLs). The table is
// append-only so existing IDs remain valid for scrollback rows.
//
// Lookup is O(1) via linkIDs; ls --hyperlink in a large directory used to
// be quadratic before this map was added.
func (t *State) internLink(url string) uint16 {
	if url == "" {
		return 0
	}
	if id, ok := t.linkIDs[url]; ok {
		return id
	}
	// The graphics-capable string parser also accepts longer OSC strings.
	// Bound total URL bytes independently of the number of interned IDs.
	if len(t.links) >= 0xFFFF || len(url) > maxLinkBytes-t.linkBytes {
		return 0
	}
	url = strings.Clone(url) // do not retain a larger OSC header backing string
	t.linkBytes += len(url)
	t.links = append(t.links, url)
	id := uint16(len(t.links))
	t.linkIDs[url] = id
	return id
}

const maxLinkBytes = 16 << 20

/*
// ChangeMask returns a bitfield of changes that have occured by VT.
func (t *State) ChangeMask() ChangeFlag {
	return t.changed
}
*/

// Changed returns true if change has occured.
func (t *State) Changed(change ChangeFlag) bool {
	return t.changed&change != 0
}

// ConsumeDirty returns which rows have been written to since the last call,
// then clears the dirty state.  Returns nil, false when nothing is dirty
// (zero allocation — the common case for idle panes).
func (t *State) ConsumeDirty() ([]bool, bool) {
	if !t.anydirty {
		return nil, false
	}
	out := make([]bool, len(t.dirty))
	t.takeDirty(out)
	return out, true
}

// takeDirty moves the dirty flags into dst, which has one entry per row.
func (t *State) takeDirty(dst []bool) {
	if t.allDirty {
		for i := range dst {
			dst[i] = true
		}
	} else {
		copy(dst, t.dirty)
	}
	clear(t.dirty)
	t.anydirty, t.allDirty = false, false
}

// ConsumeDirtyInto copies the dirty flags into dst, clears them, and returns
// dst resliced to the row count. It allocates only when dst is too short.
func (t *State) ConsumeDirtyInto(dst []bool) ([]bool, bool) {
	if cap(dst) < len(t.dirty) {
		dst = make([]bool, len(t.dirty))
	}
	dst = dst[:len(t.dirty)]
	if !t.anydirty {
		clear(dst)
		return dst, false
	}
	t.takeDirty(dst)
	return dst, true
}

// CopyRow copies live row y into dst, reusing dst's storage.
func (t *State) CopyRow(y int, dst *Row) {
	src := t.lines[y]
	dst.CopyFrom(*src, src.Len())
}

// markDirty marks row y as dirty and sets the anydirty flag.
func (t *State) markDirty(y int) {
	t.dirty[y] = true
	t.anydirty = true
}

// markDirtyRange marks rows y0 through y1. A full-screen range sets one flag,
// so scrolling the whole screen costs the same for any height.
func (t *State) markDirtyRange(y0, y1 int) {
	t.anydirty = true
	if y0 == 0 && y1 == len(t.dirty)-1 {
		t.allDirty = true
		return
	}
	for y := y0; y <= y1; y++ {
		t.dirty[y] = true
	}
}

// resetChanges resets the change mask and dirtiness.
func (t *State) resetChanges() {
	clear(t.dirty)
	t.anydirty, t.allDirty = false, false
	t.changed = 0
}

func (t *State) saveCursor() {
	t.curSaved = t.cur
}

func (t *State) restoreCursor() {
	t.cur = t.curSaved
	t.moveTo(t.cur.X, t.cur.Y)
}

func (t *State) put(c rune) {
	switch t.state {
	case stateGround:
		t.parse(c)
	case stateEsc:
		t.parseEsc(c)
	case stateEscCSI:
		t.parseEscCSI(c)
	case stateEscStr:
		t.parseEscStr(c)
	case stateEscStrEnd:
		t.parseEscStrEnd(c)
	case stateEscAltCharset:
		t.parseEscAltCharset(c)
	case stateEscTest:
		t.parseEscTest(c)
	}
}

func (t *State) putTab(forward bool) {
	x := t.cur.X
	if forward {
		if x == t.cols {
			return
		}
		for x++; x < t.cols && !t.tabs[x]; x++ {
		}
	} else {
		if x == 0 {
			return
		}
		for x--; x > 0 && !t.tabs[x]; x-- {
		}
	}
	t.moveTo(x, t.cur.Y)
}

func (t *State) newline(firstCol bool) {
	y := t.cur.Y
	if y == t.bottom {
		cur := t.cur
		t.cur = t.defaultCursor()
		t.scrollUp(t.top, 1)
		t.cur = cur
	} else {
		y++
	}
	if firstCol {
		t.moveTo(0, y)
	} else {
		t.moveTo(t.cur.X, y)
	}
}

// table from st, which in turn is from rxvt :)
var gfxCharTable = [62]rune{
	'↑', '↓', '→', '←', '█', '▚', '☃', // A - G
	0, 0, 0, 0, 0, 0, 0, 0, // H - O
	0, 0, 0, 0, 0, 0, 0, 0, // P - W
	0, 0, 0, 0, 0, 0, 0, ' ', // X - _
	'◆', '▒', '␉', '␌', '␍', '␊', '°', '±', // ` - g
	'␤', '␋', '┘', '┐', '┌', '└', '┼', '⎺', // h - o
	'⎻', '─', '⎼', '⎽', '├', '┤', '┴', '┬', // p - w
	'│', '≤', '≥', 'π', '≠', '£', '·', // x - ~
}

func (t *State) setChar(c rune, attr *Glyph, x, y int) {
	if attr.Mode&attrGfx != 0 {
		if c >= 0x41 && c <= 0x7e && gfxCharTable[c-0x41] != 0 {
			c = gfxCharTable[c-0x41]
		}
	}
	t.changed |= ChangedScreen
	t.markDirty(y)
	width := runeCellWidth(c)
	if x+width > t.cols {
		c, width = '\uFFFD', 1
	}
	t.eraseWideAt(x, y)
	if width == 2 {
		t.eraseWideAt(x+1, y)
	}
	fg, bg := attr.FG, attr.BG
	//if t.options.BrightBold && attr.Mode&attrBold != 0 && attr.FG < 8 {
	if attr.Mode&attrBold != 0 && attr.FG < 8 {
		fg = attr.FG + 8
	}
	if attr.Mode&attrReverse != 0 {
		fg, bg = attr.BG, attr.FG
	}
	if attr.UL == DefaultUL && attr.Link == 0 && attr.Image == nil {
		// No side-table data: store the cell directly.
		cl := cell{Char: c, FG: fg, BG: bg, Mode: attr.Mode}
		cl.setWidth(int8(width))
		t.lines[y].cells[x] = cl
		t.lines[y].touch(x)
		if width == 2 {
			cl.Char = 0
			cl.setWidth(-1)
			t.lines[y].cells[x+1] = cl
			t.lines[y].touch(x + 1)
		}
		return
	}
	g := *attr
	g.Char = c
	g.Combining = ""
	g.Width = int8(width)
	g.FG, g.BG = fg, bg
	t.lines[y].set(x, g)
	if width == 2 {
		g.Char = 0
		g.Width = -1
		t.lines[y].set(x+1, g)
	}
}

func runeCellWidth(c rune) int {
	if c < 0x80 {
		return 1
	}
	if c >= cachedWidthRunes {
		return max(1, uniseg.StringWidth(string(c)))
	}
	// Each uint32 holds sixteen 2-bit entries: 0 = not cached, else the width.
	word, shift := &runeWidths[c>>4], uint(c&15)*2
	if w := word.Load() >> shift & 3; w != 0 {
		return int(w)
	}
	w := max(1, uniseg.StringWidth(string(c)))
	for {
		old := word.Load()
		if word.CompareAndSwap(old, old|uint32(w)<<shift) {
			return w
		}
	}
}

// cachedWidthRunes covers the BMP, emoji and CJK extension planes.
const cachedWidthRunes = 0x30000

// runeWidths caches runeCellWidth, filled on first use. Panes parse
// concurrently, so entries are read and set atomically.
var runeWidths [cachedWidthRunes / 16]atomic.Uint32

// blankCell returns the glyph that erase operations write: the current
// colours without character attributes, link or image.
func (t *State) blankCell() Glyph {
	g := t.cur.Attr
	g.Char, g.Width, g.Link = ' ', 1, 0
	g.Mode, g.Combining, g.UL = 0, "", DefaultUL
	g.Image = nil
	return g
}

// blankStored is blankCell as a stored cell. A blank has no side-table
// data, so it needs no encoding.
func (t *State) blankStored() cell {
	c := cell{Char: ' ', FG: t.cur.Attr.FG, BG: t.cur.Attr.BG}
	c.setWidth(1)
	return c
}

func (t *State) eraseCell(x, y int) {
	t.lines[y].set(x, t.blankCell())
}

func (t *State) eraseWideAt(x, y int) {
	switch t.lines[y].cells[x].width() {
	case -1:
		if x > 0 {
			t.eraseCell(x-1, y)
		}
	case 2:
		if x+1 < t.cols {
			t.eraseCell(x+1, y)
		}
	}
}

func (t *State) repairWideRow(y int) {
	for x := 0; x < t.cols; x++ {
		switch t.lines[y].cells[x].width() {
		case 2:
			if x+1 < t.cols && t.lines[y].cells[x+1].width() == -1 {
				x++
			} else {
				t.eraseCell(x, y)
			}
		case -1:
			t.eraseCell(x, y)
		}
	}
}

func (t *State) defaultCursor() Cursor {
	c := Cursor{}
	c.Attr.FG = DefaultFG
	c.Attr.BG = DefaultBG
	c.Attr.UL = DefaultUL
	return c
}

func (t *State) reset() {
	t.graphics = nil
	t.cur = t.defaultCursor()
	t.saveCursor()
	for i := range t.tabs {
		t.tabs[i] = false
	}
	for i := tabspaces; i < len(t.tabs); i += tabspaces {
		t.tabs[i] = true
	}
	t.top = 0
	t.bottom = t.rows - 1
	t.mode = ModeWrap | ModeGrapheme
	t.clusterOpen = false
	t.clear(0, 0, t.cols-1, t.rows-1)
	t.moveTo(0, 0)
}

// TODO: definitely can improve allocs
func (t *State) resize(cols, rows int) bool {
	t.clusterOpen = false
	if cols == t.cols && rows == t.rows {
		return false
	}
	if cols < 1 || rows < 1 {
		return false
	}
	slide := t.cur.Y - rows + 1
	if slide > 0 {
		copy(t.lines, t.lines[slide:slide+rows])
		copy(t.altLines, t.altLines[slide:slide+rows])
	}

	lines, altLines, tabs := t.lines, t.altLines, t.tabs
	t.lines = make([]*Row, rows)
	t.altLines = make([]*Row, rows)
	t.dirty = make([]bool, rows)
	t.tabs = make([]bool, cols)

	minrows := min(rows, t.rows)
	mincols := min(cols, t.cols)
	t.changed |= ChangedScreen
	for i := 0; i < rows; i++ {
		t.markDirty(i)
		t.lines[i] = newRow(cols)
		t.altLines[i] = newRow(cols)
	}
	for i := 0; i < minrows; i++ {
		t.lines[i].copyCells(*lines[i])
		t.altLines[i].copyCells(*altLines[i])
	}
	copy(t.tabs, tabs)
	if cols > t.cols {
		i := t.cols - 1
		for i > 0 && !tabs[i] {
			i--
		}
		for i += tabspaces; i < len(t.tabs); i += tabspaces {
			t.tabs[i] = true
		}
	}

	t.cols = cols
	t.rows = rows
	t.setScroll(0, rows-1)
	t.moveTo(t.cur.X, t.cur.Y)
	for i := 0; i < 2; i++ {
		if mincols < cols && minrows > 0 {
			t.clear(mincols, 0, cols-1, minrows-1)
		}
		if cols > 0 && minrows < rows {
			t.clear(0, minrows, cols-1, rows-1)
		}
		for y := range t.lines {
			t.repairWideRow(y)
		}
		t.swapScreen()
	}
	return slide > 0
}

func (t *State) clear(x0, y0, x1, y1 int) {
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	x0 = clamp(x0, 0, t.cols-1)
	x1 = clamp(x1, 0, t.cols-1)
	y0 = clamp(y0, 0, t.rows-1)
	y1 = clamp(y1, 0, t.rows-1)
	t.changed |= ChangedScreen
	blank := t.blankStored()
	for y := y0; y <= y1; y++ {
		t.markDirty(y)
		start, end := x0, x1
		if t.lines[y].cells[start].width() == -1 && start > 0 {
			start--
		}
		if t.lines[y].cells[end].width() == 2 && end+1 < t.cols {
			end++
		}
		t.lines[y].fill(start, end+1, blank)
	}
}

func (t *State) moveAbsTo(x, y int) {
	if t.cur.State&cursorOrigin != 0 {
		y += t.top
	}
	t.moveTo(x, y)
}

func (t *State) moveTo(x, y int) {
	var miny, maxy int
	if t.cur.State&cursorOrigin != 0 {
		miny = t.top
		maxy = t.bottom
	} else {
		miny = 0
		maxy = t.rows - 1
	}
	x = clamp(x, 0, t.cols-1)
	y = clamp(y, miny, maxy)
	t.changed |= ChangedScreen
	t.cur.State &^= cursorWrapNext
	t.cur.X = x
	t.cur.Y = y
}

func (t *State) swapScreen() {
	t.lines, t.altLines = t.altLines, t.lines
	t.mode ^= ModeAltScreen
	t.dirtyAll()
}

func (t *State) dirtyAll() {
	t.changed |= ChangedScreen
	t.markDirtyRange(0, t.rows-1)
}

func (t *State) setScroll(top, bottom int) {
	top = clamp(top, 0, t.rows-1)
	bottom = clamp(bottom, 0, t.rows-1)
	if top > bottom {
		top, bottom = bottom, top
	}
	t.top = top
	t.bottom = bottom
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clamp(val, min, max int) int {
	if val < min {
		return min
	} else if val > max {
		return max
	}
	return val
}

func between(val, min, max int) bool {
	if val < min || val > max {
		return false
	}
	return true
}

func (t *State) scrollDown(orig, n int) {
	n = clamp(n, 0, t.bottom-orig+1)
	t.clear(0, t.bottom-n+1, t.cols-1, t.bottom)
	t.changed |= ChangedScreen
	for i := t.bottom; i >= orig+n; i-- {
		t.lines[i], t.lines[i-n] = t.lines[i-n], t.lines[i]
		t.markDirty(i)
		t.markDirty(i - n)
	}

	// TODO: selection scroll
}

func (t *State) scrollUp(orig, n int) {
	n = clamp(n, 0, t.bottom-orig+1)
	// Fire the scroll callback before clearing the departing rows.
	// The callback receives each row while its content is still intact.
	// Only fire when orig == 0 — rows leaving the top of the visible screen.
	if t.scrollRowCb != nil && orig == 0 {
		for i := 0; i < n; i++ {
			t.scrollRowCb(*t.lines[i])
		}
	}
	t.clear(0, orig, t.cols-1, orig+n-1)
	t.changed |= ChangedScreen
	if n == 1 {
		// Rotate the region up by one row, the common newline case.
		first := t.lines[orig]
		copy(t.lines[orig:t.bottom], t.lines[orig+1:t.bottom+1])
		t.lines[t.bottom] = first
		t.markDirtyRange(orig, t.bottom)
		return
	}
	for i := orig; i <= t.bottom-n; i++ {
		t.lines[i], t.lines[i+n] = t.lines[i+n], t.lines[i]
		t.markDirty(i)
		t.markDirty(i + n)
	}

	// TODO: selection scroll
}

func (t *State) modMode(set bool, bit ModeFlag) {
	if set {
		t.mode |= bit
	} else {
		t.mode &^= bit
	}
}

func (t *State) setMode(priv bool, set bool, args []int) {
	if priv {
		for _, a := range args {
			switch a {
			case 1: // DECCKM - cursor key
				t.modMode(set, ModeAppCursor)
			case 5: // DECSCNM - reverse video
				t.modMode(set, ModeReverse)
				// TODO: redraw when reverse-video mode actually changes
			case 6: // DECOM - origin
				if set {
					t.cur.State |= cursorOrigin
				} else {
					t.cur.State &^= cursorOrigin
				}
				t.moveAbsTo(0, 0)
			case 7: // DECAWM - auto wrap
				t.modMode(set, ModeWrap)
			case 12: // cursor blink, preserving the selected shape
				shape := t.cur.Shape
				if shape == 0 {
					shape = 1
				}
				if set {
					shape = (shape-1)/2*2 + 1
				} else {
					shape = (shape-1)/2*2 + 2
				}
				t.cur.Shape = shape
			case 66:
				t.modMode(set, ModeAppKeypad)
			// IGNORED:
			case 0, // error
				2,  // DECANM - ANSI/VT52
				3,  // DECCOLM - column
				4,  // DECSCLM - scroll
				8,  // DECARM - auto repeat
				18, // DECPFF - printer feed
				19, // DECPEX - printer extent
				42: // DECNRCM - national characters
				// modes we intentionally ignore
			case 25: // DECTCEM - text cursor enable mode
				t.modMode(!set, ModeHide)
			case 9: // X10 mouse compatibility mode
				t.modMode(false, ModeMouseMask)
				t.modMode(set, ModeMouseX10)
			case 1000: // report button press
				t.modMode(false, ModeMouseMask)
				t.modMode(set, ModeMouseButton)
			case 1002: // report motion on button press
				t.modMode(false, ModeMouseMask)
				t.modMode(set, ModeMouseMotion)
			case 1003: // enable all mouse motions
				t.modMode(false, ModeMouseMask)
				t.modMode(set, ModeMouseMany)
			case 1004: // send focus events to tty
				t.modMode(set, ModeFocus)
			case 1006: // extended reporting mode
				t.modMode(set, ModeMouseSgr)
			case 1034:
				t.modMode(set, Mode8bit)
			case 1049, // = 1047 and 1048
				47, 1047:
				alt := t.mode&ModeAltScreen != 0
				if alt {
					t.clear(0, 0, t.cols-1, t.rows-1)
				}
				if !set || !alt {
					t.swapScreen()
				}
				if a != 1049 {
					break
				}
				fallthrough
			case 1048:
				if set {
					t.saveCursor()
				} else {
					t.restoreCursor()
				}
			case 1001:
				// mouse highlight mode; can hang the terminal by design when
				// implemented
			case 1005:
				// utf8 mouse mode; will confuse applications not supporting
				// utf8 and luit
			case 1015:
				// urxvt mangled mouse mode; incompatiblt and can be mistaken
				// for other control codes
			case 2004: // bracketed paste
				t.modMode(set, ModeSetPaste)
			case 2026: // synchronized update
				t.modMode(set, ModeSync)
			case 2027:
				t.modMode(set, ModeGrapheme)
			default:
				t.logf("unknown private set/reset mode %d\n", a)
			}
		}
	} else {
		for _, a := range args {
			switch a {
			case 0: // Error (ignored)
			case 2: // KAM - keyboard action
				t.modMode(set, ModeKeyboardLock)
			case 4: // IRM - insertion-replacement
				t.modMode(set, ModeInsert)
				t.logln("insert mode not implemented")
			case 12: // SRM - send/receive
				t.modMode(set, ModeEcho)
			case 20: // LNM - linefeed/newline
				t.modMode(set, ModeCRLF)
			case 34:
				t.logln("right-to-left mode not implemented")
			case 96:
				t.logln("right-to-left copy mode not implemented")
			default:
				t.logf("unknown set/reset mode %d\n", a)
			}
		}
	}
}

func (t *State) setAttr(attr []int) {
	if len(attr) == 0 {
		attr = []int{0}
	}
	for i := 0; i < len(attr); i++ {
		a := attr[i]
		// Decode sub-parameter: params with a ':' sub-param are encoded as
		// main*10000+(sub+1) by csiEscape.parse().  sub == -1 means no colon
		// sub-parameter was present (plain arg like "4" vs "4:3").
		sub := -1
		if a >= 10000 {
			sub = (a % 10000) - 1 // recover original: 40001→0, 40002→1, …
			a = a / 10000
		}
		switch a {
		case 0:
			t.cur.Attr.Mode &^= attrReverse | attrUnderline | attrBold | attrItalic | attrBlink |
				attrDim | attrStrikethrough | attrInvisible | attrUnderlineStyleMask | attrHasULColor | attrOverline
			t.cur.Attr.FG = DefaultFG
			t.cur.Attr.BG = DefaultBG
			t.cur.Attr.UL = DefaultUL
		case 1:
			t.cur.Attr.Mode |= attrBold
		case 2:
			t.cur.Attr.Mode |= attrDim
		case 3:
			t.cur.Attr.Mode |= attrItalic
		case 4:
			// SGR 4[:N] — underline with optional style sub-parameter:
			//   4 or 4:1 → solid/straight
			//   4:2 → double
			//   4:3 → curly/wavy
			//   4:4 → dotted
			//   4:5 → dashed
			//   4:0 → turn off underline
			t.cur.Attr.Mode &^= attrUnderlineStyleMask
			if sub < 0 {
				// plain "4" with no sub-parameter → solid underline
				t.cur.Attr.Mode |= attrUnderline
			} else {
				switch sub {
				case 0: // 4:0 → turn off
					t.cur.Attr.Mode &^= attrUnderline
				case 1: // solid
					t.cur.Attr.Mode |= attrUnderline
				case 2: // double
					t.cur.Attr.Mode |= attrUnderline | attrUnderlineStyleBit0
				case 3: // curly
					t.cur.Attr.Mode |= attrUnderline | attrUnderlineStyleBit1
				case 4: // dotted
					t.cur.Attr.Mode |= attrUnderline | attrUnderlineStyleBit0 | attrUnderlineStyleBit1
				case 5: // dashed
					t.cur.Attr.Mode |= attrUnderline | attrUnderlineStyleBit2
				default:
					t.cur.Attr.Mode &^= attrUnderline
				}
			}
		case 5, 6: // slow, rapid blink
			t.cur.Attr.Mode |= attrBlink
		case 7:
			t.cur.Attr.Mode |= attrReverse
		case 8:
			t.cur.Attr.Mode |= attrInvisible
		case 9:
			t.cur.Attr.Mode |= attrStrikethrough
		case 21:
			t.cur.Attr.Mode &^= attrUnderlineStyleMask
			t.cur.Attr.Mode |= attrUnderline | attrUnderlineStyleBit0
		case 22:
			t.cur.Attr.Mode &^= attrBold | attrDim // SGR 22 resets both bold and dim
		case 23:
			t.cur.Attr.Mode &^= attrItalic
		case 24:
			t.cur.Attr.Mode &^= attrUnderline | attrUnderlineStyleMask
		case 25, 26:
			t.cur.Attr.Mode &^= attrBlink
		case 27:
			t.cur.Attr.Mode &^= attrReverse
		case 28:
			t.cur.Attr.Mode &^= attrInvisible
		case 29:
			t.cur.Attr.Mode &^= attrStrikethrough
		case 53:
			t.cur.Attr.Mode |= attrOverline
		case 55:
			t.cur.Attr.Mode &^= attrOverline
		case 38:
			// Colour mode from sub-param (e.g. 38:2:R:G:B) or next arg (38;2;R;G;B).
			colorMode := sub
			if colorMode < 0 && i+1 < len(attr) {
				colorMode = attr[i+1]
			}
			switch colorMode {
			case 5:
				if sub < 0 {
					i++
				} // skip the "5" arg only for the ';' form
				i++
				if i < len(attr) && between(attr[i], 0, 255) {
					t.cur.Attr.FG = Color(attr[i])
				} else {
					t.logf("bad fgcolor\n")
				}
			case 2:
				if sub < 0 {
					i++
				} // skip the "2" arg only for the ';' form
				i += 3
				if i < len(attr) {
					r, g, b := attr[i-2], attr[i-1], attr[i]
					if !between(r, 0, 255) || !between(g, 0, 255) || !between(b, 0, 255) {
						t.logf("bad fg rgb color (%d,%d,%d)\n", r, g, b)
					} else {
						t.cur.Attr.FG = Color(r<<16 | g<<8 | b)
					}
				}
			default:
				t.logf("gfx attr %d unknown\n", a)
			}
		case 39:
			t.cur.Attr.FG = DefaultFG
		case 48:
			colorMode := sub
			if colorMode < 0 && i+1 < len(attr) {
				colorMode = attr[i+1]
			}
			switch colorMode {
			case 5:
				if sub < 0 {
					i++
				}
				i++
				if i < len(attr) && between(attr[i], 0, 255) {
					t.cur.Attr.BG = Color(attr[i])
				} else {
					t.logf("bad bgcolor\n")
				}
			case 2:
				if sub < 0 {
					i++
				}
				i += 3
				if i < len(attr) {
					r, g, b := attr[i-2], attr[i-1], attr[i]
					if !between(r, 0, 255) || !between(g, 0, 255) || !between(b, 0, 255) {
						t.logf("bad bg rgb color (%d,%d,%d)\n", r, g, b)
					} else {
						t.cur.Attr.BG = Color(r<<16 | g<<8 | b)
					}
				}
			default:
				t.logf("gfx attr %d unknown\n", a)
			}
		case 49:
			t.cur.Attr.BG = DefaultBG
		case 58:
			// SGR 58[:N] — underline colour (same encoding as 38/48).
			colorMode := sub
			if colorMode < 0 && i+1 < len(attr) {
				colorMode = attr[i+1]
			}
			switch colorMode {
			case 5:
				if sub < 0 {
					i++
				}
				i++
				if i < len(attr) && between(attr[i], 0, 255) {
					t.cur.Attr.UL = Color(attr[i])
					t.cur.Attr.Mode |= attrHasULColor
				} else {
					t.logf("bad ulcolor\n")
				}
			case 2:
				if sub < 0 {
					i++
				}
				i += 3
				if i < len(attr) {
					r, g, b := attr[i-2], attr[i-1], attr[i]
					if !between(r, 0, 255) || !between(g, 0, 255) || !between(b, 0, 255) {
						t.logf("bad ul rgb color (%d,%d,%d)\n", r, g, b)
					} else {
						t.cur.Attr.UL = Color(r<<16 | g<<8 | b)
						t.cur.Attr.Mode |= attrHasULColor
					}
				}
			default:
				t.logf("gfx attr %d unknown\n", a)
			}
		case 59:
			t.cur.Attr.UL = DefaultUL
			t.cur.Attr.Mode &^= attrHasULColor
		default:
			if between(a, 30, 37) {
				t.cur.Attr.FG = Color(a - 30)
			} else if between(a, 40, 47) {
				t.cur.Attr.BG = Color(a - 40)
			} else if between(a, 90, 97) {
				t.cur.Attr.FG = Color(a - 90 + 8)
			} else if between(a, 100, 107) {
				t.cur.Attr.BG = Color(a - 100 + 8)
			} else {
				t.logf("gfx attr %d unknown\n", a)
			}
		}
	}
}

func (t *State) insertBlanks(n int) {
	n = clamp(n, 1, t.cols-t.cur.X)
	src := t.cur.X
	dst := src + n
	size := t.cols - dst
	t.changed |= ChangedScreen
	t.markDirty(t.cur.Y)

	if dst >= t.cols {
		t.clear(t.cur.X, t.cur.Y, t.cols-1, t.cur.Y)
	} else {
		copy(t.lines[t.cur.Y].cells[dst:dst+size], t.lines[t.cur.Y].cells[src:src+size])
		t.lines[t.cur.Y].touchAll()
		for x := src; x < dst; x++ {
			t.eraseCell(x, t.cur.Y)
		}
		t.repairWideRow(t.cur.Y)
	}
}

func (t *State) insertBlankLines(n int) {
	if t.cur.Y < t.top || t.cur.Y > t.bottom {
		return
	}
	t.scrollDown(t.cur.Y, n)
}

func (t *State) deleteLines(n int) {
	if t.cur.Y < t.top || t.cur.Y > t.bottom {
		return
	}
	t.scrollUp(t.cur.Y, n)
}

func (t *State) deleteChars(n int) {
	n = clamp(n, 1, t.cols-t.cur.X)
	src := t.cur.X + n
	dst := t.cur.X
	size := t.cols - src
	t.changed |= ChangedScreen
	t.markDirty(t.cur.Y)

	if src >= t.cols {
		t.clear(t.cur.X, t.cur.Y, t.cols-1, t.cur.Y)
	} else {
		copy(t.lines[t.cur.Y].cells[dst:dst+size], t.lines[t.cur.Y].cells[src:src+size])
		t.lines[t.cur.Y].touchAll()
		for x := t.cols - n; x < t.cols; x++ {
			t.eraseCell(x, t.cur.Y)
		}
		t.repairWideRow(t.cur.Y)
	}
}

func (t *State) setTitle(title string) {
	t.changed |= ChangedTitle
	t.title = title
}

func (t *State) Size() (cols, rows int) {
	return t.cols, t.rows
}

func (t *State) String() string {
	t.Lock()
	defer t.Unlock()

	var view []rune
	for y := 0; y < t.rows; y++ {
		for x := 0; x < t.cols; x++ {
			attr := t.Cell(x, y)
			if attr.Width < 0 {
				continue
			}
			view = append(view, attr.Char)
			view = append(view, []rune(attr.Combining)...)
		}
		view = append(view, '\n')
	}

	return string(view)
}
