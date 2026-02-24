// render.go – screen painting.
//
// The render loop waits for redraw signals, then:
//  1. Iterates every leaf's vt10x virtual grid and copies cells to tcell.
//  2. Draws separator borders (gray for inactive, green for active).
//  3. Positions the terminal cursor inside the active pane.
//
// VT100 parsing bridge (read side):
//
//	pane.term (vt10x.Terminal) is the virtual terminal updated by readPTY.
//	renderPane reads pane.term.Cell(col, row) for every cell in the pane,
//	converts the vt10x Glyph colour/mode to tcell equivalents, and calls
//	screen.SetContent to stage the glyph for display.
//	pane.mu is held during the scan to prevent races with readPTY.
//
// vt10x attribute bit-mask values (unexported in vt10x; replicated here):
//
//	attrReverse   = 1
//	attrUnderline = 2
//	attrBold      = 4
//	attrItalic    = 16
//	attrBlink     = 32
package main

import (
	"fmt"
	"os"

	"github.com/gdamore/tcell/v2"
	"github.com/hinshun/vt10x"
)

// vt10x Glyph.Mode bitmask constants (mirrored from vt10x/state.go).
const (
	vtAttrReverse   int16 = 1
	vtAttrUnderline int16 = 2
	vtAttrBold      int16 = 4
	vtAttrItalic    int16 = 16
	vtAttrBlink     int16 = 32
)

// ---------------------------------------------------------------------------
// Render loop
// ---------------------------------------------------------------------------

// renderLoop drains the redraw channel and repaints the screen.
func (app *App) renderLoop() {
	defer app.renderWg.Done()
	for {
		select {
		case <-app.redraw:
			app.render()
		case <-app.done:
			return
		}
	}
}

// render performs a full repaint of the tcell screen from the current state.
//
// app.mu is held for the entire render pass.  This prevents concurrent
// layout mutations (split, remove, resize) from modifying BSP-tree node
// pointers or Pane x/y/w/h fields while we are traversing them, which would
// be a data race.  The lock ordering throughout the codebase is always
// app.mu → Pane.mu, so acquiring Pane.mu inside renderPane is safe.
func (app *App) render() {
	app.mu.Lock()
	defer app.mu.Unlock()

	root := app.root
	active := app.active

	app.screen.Clear()
	if root == nil {
		app.screen.Show()
		return
	}

	rt := app.theme

	// Step 1 – draw pane contents.
	drawPaneContents(app.screen, root, rt)

	// Step 2 – draw inter-pane separator lines.
	drawBorders(app.screen, root, rt)

	// Step 3 – re-draw borders adjacent to the active pane in accent color.
	if active != nil {
		activeStyle := tcell.StyleDefault.
			Foreground(rt.activeBorder).
			Background(rt.bg)
		paintActiveBorders(app.screen, root, active, activeStyle)
	}

	// Step 3.5 – scrollbars.
	drawScrollbars(app.screen, root, rt)

	// Step 3.6 – status badges.
	drawAllPaneStatus(app.screen, root, active)

	// Step 4 – place the hardware cursor inside the active pane.
	// Hidden when the pane is in scrollback mode (cursor is in live view,
	// not in the scrolled-back view the user is reading).
	if active != nil {
		active.mu.Lock()
		dead := active.dead
		sbOff := active.sbOff
		cur := active.term.Cursor()
		visible := active.term.CursorVisible()
		active.mu.Unlock()
		if !dead && visible && sbOff == 0 {
			app.screen.ShowCursor(active.x+cur.X, active.y+cur.Y)
		} else {
			app.screen.HideCursor()
		}
	} else {
		app.screen.HideCursor()
	}

	// Step 4.5 – search bar overlay (drawn on top of pane content).
	if app.searchMode && app.searchPane != nil {
		drawSearchBar(app.screen, app.searchPane, app.searchQuery,
			app.searchIdx, len(app.searchMatches))
	}

	// Step 5 – drain OSC passthrough sequences.
	// Written directly to os.Stdout BEFORE tcell.Show() so the host terminal
	// receives OSC 7 (CWD), OSC 8 (hyperlinks), and OSC 52 (clipboard) in
	// the correct order relative to screen content.  Safe because the render
	// goroutine is the only writer to os.Stdout.
drainOSC:
	for {
		select {
		case seq := <-app.oscCh:
			os.Stdout.Write(seq) //nolint:errcheck
		default:
			break drainOSC
		}
	}

	app.screen.Show()
}

// ---------------------------------------------------------------------------
// Pane content rendering
// ---------------------------------------------------------------------------

// drawPaneContents recursively renders every leaf's virtual terminal grid.
func drawPaneContents(scr tcell.Screen, n *Node, rt resolvedTheme) {
	if n.isLeaf() {
		renderPane(scr, n.pane, rt)
		return
	}
	drawPaneContents(scr, n.left, rt)
	drawPaneContents(scr, n.right, rt)
}

// renderPane paints a single pane's vt10x Glyph grid onto the tcell screen,
// then overlays the scrollbar on the rightmost column.
//
// When p.sbOff == 0 the live vt10x grid is rendered directly.
// When p.sbOff > 0 we display a "virtual" view that combines scrollback lines
// (from p.sb) above the live grid:
//
//	Virtual line 0                = oldest captured scrollback line
//	…
//	Virtual line p.sb.count-1    = most recently scrolled-off line
//	Virtual line p.sb.count      = live term row 0  (current top)
//	…
//	Virtual line p.sb.count+h-1  = live term row h-1 (current bottom)
//
// With sbOff = N we display virtual lines [p.sb.count-N, p.sb.count-N+h).
func renderPane(scr tcell.Screen, p *Pane, rt resolvedTheme) {
	p.mu.Lock()
	defer p.mu.Unlock()

	cols, rows := p.term.Size()
	sbOff := p.sbOff
	sbCount := p.sb.count

	for row := 0; row < rows; row++ {
		// Unified virtual row: stable across scrolls (see selPos in pane.go).
		vRow := (sbCount - sbOff) + row

		var cells []vt10x.Glyph
		if sbOff > 0 {
			switch {
			case vRow < 0:
				// Before the oldest captured line – render blank.
			case vRow < sbCount:
				cells = p.sb.get(vRow)
			default:
				// In the live grid.
				termRow := vRow - sbCount
				if termRow < rows {
					cells = captureRow(p.term, termRow, cols)
				}
			}
		}

		for col := 0; col < cols; col++ {
			var cell vt10x.Glyph
			if cells != nil && col < len(cells) {
				cell = cells[col]
			} else if sbOff == 0 {
				cell = p.term.Cell(col, row)
			}

			ch := cell.Char
			if ch == 0 {
				ch = ' '
			}

			style := tcell.StyleDefault.
				Foreground(vtColor(cell.FG, rt.fg, rt)).
				Background(vtColor(cell.BG, rt.bg, rt))

			if cell.Mode&vtAttrBold != 0 {
				style = style.Bold(true)
			}
			if cell.Mode&vtAttrUnderline != 0 {
				style = style.Underline(true)
			}
			if cell.Mode&vtAttrBlink != 0 {
				style = style.Blink(true)
			}
			if cell.Mode&vtAttrReverse != 0 {
				style = style.Reverse(true)
			}
			if cell.Mode&vtAttrItalic != 0 {
				style = style.Italic(true)
			}

			// Selection highlight: toggle reverse video so selected text is
			// always visually distinct regardless of the cell's original style.
			if p.selContainsUnlocked(vRow, col) {
				if cell.Mode&vtAttrReverse != 0 {
					style = style.Reverse(false)
				} else {
					style = style.Reverse(true)
				}
			}

			// Search highlight: amber for regular matches, orange for current.
			if p.searchHL != nil {
				key := int64(vRow)<<16 | int64(col)
				switch p.searchHL[key] {
				case 1:
					style = style.Background(tcell.NewRGBColor(0x80, 0x60, 0x00)).
						Foreground(tcell.NewRGBColor(0xff, 0xe0, 0x80))
				case 2:
					style = style.Background(tcell.NewRGBColor(0xff, 0xa5, 0x00)).
						Foreground(tcell.ColorBlack)
				}
			}

			scr.SetContent(p.x+col, p.y+row, ch, nil, style)
		}
	}

}

// drawScrollbars walks the BSP tree and draws a scrollbar for every leaf that
// has scrollback history (sbCount > 0).  The bar always occupies the last
// column of the pane's node region (p.x+p.w-1), which is permanently reserved
// — the PTY terminal is created one column narrower (w-1) so content never
// reaches that column.
func drawScrollbars(scr tcell.Screen, n *Node, rt resolvedTheme) {
	if n.isLeaf() {
		p := n.pane
		p.mu.Lock()
		sbCount := p.sb.count
		sbOff := p.sbOff
		_, rows := p.term.Size()
		p.mu.Unlock()
		if sbCount > 0 {
			drawScrollbar(scr, p.x+p.w-1, p.y, rows, sbCount, sbOff, rt)
		}
		return
	}
	drawScrollbars(scr, n.left, rt)
	drawScrollbars(scr, n.right, rt)
}

// drawScrollbar draws a narrow one-column scrollbar at screen column bx.
//
// Characters (thin, minimal):
//
//	'▕'  U+2595  RIGHT ONE EIGHTH BLOCK – empty track
//	'▐'  U+2590  RIGHT HALF BLOCK       – scrollbar thumb
func drawScrollbar(scr tcell.Screen, bx, by, h, sbCount, sbOff int, rt resolvedTheme) {
	total := sbCount + h // total virtual lines

	// Handle height: proportional to the visible fraction, minimum 1 row.
	handleH := max(1, h*h/total)

	// Handle top: where in [0, h) the visible window starts.
	// visibleStart = sbCount - sbOff  (0 = top of scrollback, sbCount = live top)
	visibleStart := sbCount - sbOff
	handleTop := visibleStart * h / total

	// Clamp so the handle never overflows the track.
	if handleTop+handleH > h {
		handleTop = h - handleH
	}
	if handleTop < 0 {
		handleTop = 0
	}

	trackStyle := tcell.StyleDefault.
		Foreground(rt.scrollTrack).
		Background(rt.bg)
	thumbStyle := tcell.StyleDefault.
		Foreground(rt.scrollThumb).
		Background(rt.bg)

	for row := 0; row < h; row++ {
		ch := '▕'
		style := trackStyle
		if row >= handleTop && row < handleTop+handleH {
			ch = '▐'
			style = thumbStyle
		}
		scr.SetContent(bx, by+row, ch, nil, style)
	}

	// When scrolled back, show a compact line-count badge at the top.
	if sbOff > 0 {
		label := fmt.Sprintf("-%d", sbOff)
		labelStyle := tcell.StyleDefault.
			Foreground(tcell.ColorYellow).
			Background(tcell.ColorBlack).
			Bold(true)
		col := bx
		for i := len(label) - 1; i >= 0 && col >= 0; i-- {
			scr.SetContent(col, by, rune(label[i]), nil, labelStyle)
			col--
		}
	}
}

// ---------------------------------------------------------------------------
// Search bar overlay
// ---------------------------------------------------------------------------

// drawSearchBar renders a one-row search overlay at the bottom of pane p.
// Called from render() (under app.mu) when search mode is active.
func drawSearchBar(scr tcell.Screen, p *Pane, query string, matchIdx, matchCount int) {
	y := p.y + p.h - 1

	var label string
	switch {
	case query == "":
		label = " Search: "
	case matchCount == 0:
		label = fmt.Sprintf(" Search: %s  (no matches) ", query)
	default:
		label = fmt.Sprintf(" Search: %s  %d/%d ", query, matchIdx+1, matchCount)
	}

	barStyle := tcell.StyleDefault.
		Background(tcell.NewRGBColor(0x1a, 0x1a, 0x44)).
		Foreground(tcell.ColorWhite)
	noMatchStyle := barStyle.Foreground(tcell.NewRGBColor(0xff, 0x66, 0x66))

	s := barStyle
	if matchCount == 0 && query != "" {
		s = noMatchStyle
	}

	col := p.x
	for _, ch := range label {
		if col >= p.x+p.w {
			break
		}
		scr.SetContent(col, y, ch, nil, s)
		col++
	}
	// Blinking-cursor indicator.
	if col < p.x+p.w {
		scr.SetContent(col, y, '█', nil, s)
		col++
	}
	// Pad to end of pane width.
	for ; col < p.x+p.w; col++ {
		scr.SetContent(col, y, ' ', nil, barStyle)
	}
}

// ---------------------------------------------------------------------------
// Border rendering
// ---------------------------------------------------------------------------

// drawBorders draws a gray separator line at every internal node split point.
func drawBorders(scr tcell.Screen, n *Node, rt resolvedTheme) {
	if n.isLeaf() {
		return
	}
	borderStyle := tcell.StyleDefault.
		Foreground(rt.inactiveBorder).
		Background(rt.bg)

	if n.dir == splitVertical {
		bx := n.left.x + n.left.w
		for y := n.y; y < n.y+n.h; y++ {
			scr.SetContent(bx, y, tcell.RuneVLine, nil, borderStyle)
		}
	} else {
		by := n.left.y + n.left.h
		for x := n.x; x < n.x+n.w; x++ {
			scr.SetContent(x, by, tcell.RuneHLine, nil, borderStyle)
		}
	}

	drawBorders(scr, n.left, rt)
	drawBorders(scr, n.right, rt)
}

// paintActiveBorders re-colours every separator that is directly adjacent to
// the active pane (i.e. both siblings of that split include or are the active
// pane) using the provided style.
func paintActiveBorders(scr tcell.Screen, n *Node, active *Pane, style tcell.Style) {
	if n.isLeaf() {
		return
	}

	// If either child subtree contains the active pane, that split line is
	// adjacent to the active pane and should be highlighted.
	if nodeContains(n.left, active) || nodeContains(n.right, active) {
		if n.dir == splitVertical {
			bx := n.left.x + n.left.w
			for y := n.y; y < n.y+n.h; y++ {
				scr.SetContent(bx, y, tcell.RuneVLine, nil, style)
			}
		} else {
			by := n.left.y + n.left.h
			for x := n.x; x < n.x+n.w; x++ {
				scr.SetContent(x, by, tcell.RuneHLine, nil, style)
			}
		}
	}

	paintActiveBorders(scr, n.left, active, style)
	paintActiveBorders(scr, n.right, active, style)
}

// nodeContains reports whether any leaf in subtree n holds pane p.
func nodeContains(n *Node, p *Pane) bool {
	if n.isLeaf() {
		return n.pane == p
	}
	return nodeContains(n.left, p) || nodeContains(n.right, p)
}

// ---------------------------------------------------------------------------
// Colour conversion
// ---------------------------------------------------------------------------

// vtColor converts a vt10x Color to the nearest tcell Color, applying the
// theme palette for ANSI colors 0–15 and the theme's fg/bg for defaults.
func vtColor(c vt10x.Color, def tcell.Color, rt resolvedTheme) tcell.Color {
	switch c {
	case vt10x.DefaultFG, vt10x.DefaultBG, vt10x.DefaultCursor:
		return def
	}
	if c < 16 {
		// ANSI colors 0–15: use theme palette, or fall through to the
		// terminal's own palette when the theme leaves the slot unset.
		if rt.palette[c] != tcell.ColorDefault {
			return rt.palette[c]
		}
		return tcell.PaletteColor(int(c))
	}
	if c < 256 {
		// xterm-256 colors 16–255: standard palette.
		return tcell.PaletteColor(int(c))
	}
	if c < vt10x.DefaultFG {
		// True-color RGB: packed as r<<16 | g<<8 | b by vt10x.
		return tcell.NewRGBColor(int32(c>>16&0xff), int32(c>>8&0xff), int32(c&0xff))
	}
	return def
}
