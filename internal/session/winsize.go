// winsize.go - PTY window size in cells and virtual pixels.
package session

import (
	"math"

	"github.com/creack/pty"
)

// VirtualCellPixels returns the virtual cell size bunk's TUI reports: 8
// pixels wide and 8 x aspect[0] high (aspect capped at 8, height at least
// 2), or 8x16 without a usable aspect.
func VirtualCellPixels(aspect []float64) (int, int) {
	width, height := 8, 16
	if len(aspect) != 0 && !math.IsNaN(aspect[0]) && !math.IsInf(aspect[0], 0) && aspect[0] > 0 {
		height = int(math.Round(8 * min(aspect[0], 8)))
	}
	return width, max(2, height)
}

// Winsize returns the PTY window size for a grid of cols x rows cells of
// cellWidth x cellHeight pixels, the pixel sizes capped at 65535.
func Winsize(cols, rows, cellWidth, cellHeight int) *pty.Winsize {
	return &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows), X: uint16(min(65535, cols*cellWidth)), Y: uint16(min(65535, rows*cellHeight))}
}
