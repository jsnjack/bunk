// winsize.go - PTY window size in cells and virtual pixels.
package session

import (
	"math"

	"github.com/creack/pty"
)

func VirtualCellPixels(aspect []float64) (int, int) {
	width, height := 8, 16
	if len(aspect) != 0 && !math.IsNaN(aspect[0]) && !math.IsInf(aspect[0], 0) && aspect[0] > 0 {
		height = int(math.Round(8 * min(aspect[0], 8)))
	}
	return width, max(2, height)
}

func Winsize(cols, rows, cellWidth, cellHeight int) *pty.Winsize {
	return &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows), X: uint16(min(65535, cols*cellWidth)), Y: uint16(min(65535, rows*cellHeight))}
}
