package vt10x

import (
	"bufio"
	"bunk/internal/graphics"
	"fmt"
	"io"
)

// Terminal represents the virtual terminal emulator.
type Terminal interface {
	// View displays the virtual terminal.
	View

	// Write parses input and writes terminal changes to state.
	io.Writer

	// Parse blocks on read on pty or io.Reader, then parses sequences until
	// buffer empties. State is locked as soon as first rune is read, and unlocked
	// when buffer is empty.
	Parse(bf *bufio.Reader) error
}

// View represents the view of the virtual terminal emulator.
type View interface {
	// String dumps the virtual terminal contents.
	fmt.Stringer

	// Size returns the size of the virtual terminal.
	Size() (cols, rows int)
	// CellPixels reports the raster resolution of each character cell.
	CellPixels() (width, height int)
	// SetCellPixels changes it (see WithCellPixels).
	SetCellPixels(width, height int)
	// GraphicsState clones reusable images for replay without duplicating pixels.
	GraphicsState() *graphics.Decoder

	// MarkImages reports the image store ID of every placement a cell of
	// either screen refers to. Callers must serialize access, as for Cell.
	MarkImages(mark func(id uint64))

	// Resize changes the size of the virtual terminal.
	Resize(cols, rows int)

	// Mode returns the current terminal mode.//
	Mode() ModeFlag

	// Title represents the title of the console window.
	Title() string

	// Link returns the OSC 8 hyperlink URL for the given Glyph.Link ID, or
	// "" if id is 0 (no link) or free. Callers must hold Lock().
	Link(id uint16) string

	// LinkGen changes whenever a sweep frees link IDs. Callers must hold
	// Lock().
	LinkGen() uint64

	// LinkStats reports the size of the hyperlink table. Callers must hold
	// Lock().
	LinkStats() LinkStats

	// SweepLinks frees the link IDs that no cell refers to. Callers must
	// hold Lock().
	SweepLinks()

	// Cell returns the glyph containing the character code, foreground color, and
	// background color at position (x, y) relative to the top left of the terminal.
	Cell(x, y int) Glyph

	// RawCell returns a glyph without resolving dynamic colour overrides.
	RawCell(x, y int) Glyph

	// ImportGlyph translates a glyph's hyperlink ID from source to this view.
	ImportGlyph(g Glyph, source View) Glyph

	// ReplaceScreen installs reflowed cells while retaining modes, attributes,
	// callbacks, colours, and saved state. source supplies hyperlink identities.
	ReplaceScreen(cols, rows int, cells [][]Glyph, cursor Cursor, source View)

	// Cursor returns the current position of the cursor.
	Cursor() Cursor

	// CursorPosition returns the cursor relative to the active origin for CPR.
	CursorPosition() (x, y int)

	// CursorVisible returns the visible state of the cursor.
	CursorVisible() bool

	// Lock locks the state object's mutex.
	Lock()

	// Unlock resets change flags and unlocks the state object's mutex.
	Unlock()

	// QueryPrivateMode returns the DECRQM status byte for a DEC private mode.
	// '1' = set, '2' = reset, '0' = not recognized.
	QueryPrivateMode(mode int) byte

	// StatusString returns a DECRQSS response for an implemented setting.
	StatusString(setting string) string

	// ColorOverride returns the current dynamic-colour override for c, if one
	// has been set via OSC 10/11/12 or similar colour-control sequences.
	ColorOverride(c Color) (Color, bool)

	// ColorGen returns a monotonically increasing counter that changes whenever
	// a dynamic-colour override is set or reset.  The renderer uses it to force
	// a full repaint when colours change (a change affects every DefaultBG/FG
	// cell, including blank rows that dirty tracking would otherwise skip).
	ColorGen() uint64

	// ConsumeDirty returns which rows have been written to since the last
	// ConsumeDirty call, then clears the dirty state.  Returns nil, false
	// when nothing is dirty (zero allocation).  The caller must not retain
	// the returned slice past the next Write call.
	ConsumeDirty() (rows []bool, any bool)

	// ConsumeDirtyInto is ConsumeDirty without the allocation: it copies the
	// dirty flags into dst (grown only when it is shorter than the screen),
	// clears them, and returns dst resliced to the row count.
	ConsumeDirtyInto(dst []bool) (rows []bool, any bool)

	// CopyRow copies live row y into dst, reusing dst's storage. Decode cells
	// with dst.Glyph; dynamic colour overrides are not applied.
	CopyRow(y int, dst *Row)

	// CopyPrimaryRow is CopyRow for the primary screen, which stays put
	// while the alternate screen is shown.
	CopyPrimaryRow(y int, dst *Row)
}

type TerminalOption func(*TerminalInfo)

type TerminalInfo struct {
	w          io.Writer
	cols, rows int
	// scrollCb is called synchronously inside scrollUp() for each row that
	// leaves the top of the primary screen (orig == 0), before that row's
	// backing storage is cleared.  See WithScrollCallback for ownership.
	scrollCb func(row Row)
	// sbClearCb is called when the application requests scrollback erasure:
	// ED 3 (CSI 3 J, the xterm E3 extension sent by clear(1)) or RIS
	// (ESC c, sent by reset(1)).
	sbClearCb             func()
	bellCb                func()
	graphics              *graphics.Decoder
	graphicsReply         func([]byte)
	cellWidth, cellHeight int
	graphicsRows          int
	images                *graphics.Store
	linkRefs              func(mark func(id uint16))
}

// WithGraphicsReply routes graphics acknowledgements independently of ordinary
// terminal queries. The callback runs synchronously while the state is locked.
func WithGraphicsReply(fn func([]byte)) TerminalOption {
	return func(info *TerminalInfo) { info.graphicsReply = fn }
}

// WithGraphicsState installs an isolated decoder snapshot for history replay.
func WithGraphicsState(decoder *graphics.Decoder) TerminalOption {
	return func(info *TerminalInfo) { info.graphics = decoder.Clone() }
}

// WithCellPixels sets the virtual raster size used for image placement.
func WithCellPixels(width, height int) TerminalOption {
	return func(info *TerminalInfo) {
		info.cellWidth, info.cellHeight = max(1, min(width, 256)), max(2, min(height, 256))
	}
}

// WithImageStore keeps the bitmap of each image placement in store, so front
// ends can draw image cells at full resolution (ImageCell). Without it only
// the cells' colour samples remain. The terminal sweeps the store after
// image deletion, ED 3 and RIS; store.Referenced must mark this terminal's
// cells (MarkImages) and any scrollback the caller keeps.
func WithImageStore(store *graphics.Store) TerminalOption {
	return func(info *TerminalInfo) { info.images = store }
}

// WithLinkReferences installs fn, which marks the hyperlink ID of every
// cell kept outside the terminal: the rows the scroll callback stored and
// still keeps. A link sweep keeps those IDs; it runs while the terminal is
// locked, also from inside Write. A terminal with a scroll callback but
// without fn never frees link IDs, so its table fills up for good.
func WithLinkReferences(fn func(mark func(id uint16))) TerminalOption {
	return func(info *TerminalInfo) { info.linkRefs = fn }
}

// WithGraphicsViewportRows keeps percentage-sized images relative to the pane,
// not the much taller scratch grid used to replay history during reflow.
func WithGraphicsViewportRows(rows int) TerminalOption {
	return func(info *TerminalInfo) { info.graphicsRows = max(1, rows) }
}

func WithWriter(w io.Writer) TerminalOption {
	return func(info *TerminalInfo) {
		info.w = w
	}
}

func WithSize(cols, rows int) TerminalOption {
	return func(info *TerminalInfo) {
		info.cols = cols
		info.rows = rows
	}
}

// WithScrollCallback installs a callback that fires once per row scrolled off
// the top of the primary screen.  The callback runs synchronously inside
// terminal mutation code; it must be fast and non-blocking.
//
// The row is only valid for the duration of the call; a callback that keeps
// it must copy it with Row.CopyFrom.
func WithScrollCallback(fn func(row Row)) TerminalOption {
	return func(info *TerminalInfo) {
		info.scrollCb = fn
	}
}

// WithScrollbackClearCallback installs a callback that fires when the
// application requests scrollback erasure: ED 3 (CSI 3 J, the xterm E3
// extension sent by clear(1)) or RIS (ESC c, sent by reset(1)).  The State
// only holds the visible grid — scrollback lives with the caller — so
// erasure is delegated through this callback.  It runs synchronously inside
// terminal mutation code; it must be fast and non-blocking.
func WithScrollbackClearCallback(fn func()) TerminalOption {
	return func(info *TerminalInfo) {
		info.sbClearCb = fn
	}
}

// WithBellCallback installs a callback that fires on each BEL outside an
// escape string. It runs synchronously inside terminal mutation code; it
// must be fast and non-blocking.
func WithBellCallback(fn func()) TerminalOption {
	return func(info *TerminalInfo) {
		info.bellCb = fn
	}
}

// New returns a new virtual terminal emulator.
func New(opts ...TerminalOption) Terminal {
	info := TerminalInfo{
		w:          io.Discard,
		cols:       80,
		rows:       24,
		cellWidth:  8,
		cellHeight: 16,
	}
	for _, opt := range opts {
		opt(&info)
	}
	return newTerminal(info)
}
