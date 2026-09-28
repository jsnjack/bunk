package main

import (
	"image/color"
	"math"

	"github.com/creack/pty"
	"github.com/gdamore/tcell/v2"
)

func imageColor(pixel color.NRGBA, background tcell.Color) tcell.Color {
	r, g, b := background.RGB()
	if r < 0 {
		r, g, b = 0, 0, 0
	}
	a := int32(pixel.A)
	return tcell.NewRGBColor((int32(pixel.R)*a+r*(255-a)+127)/255, (int32(pixel.G)*a+g*(255-a)+127)/255, (int32(pixel.B)*a+b*(255-a)+127)/255)
}

func virtualCellPixels(aspect []float64) (int, int) {
	width, height := 8, 16
	if len(aspect) != 0 && !math.IsNaN(aspect[0]) && !math.IsInf(aspect[0], 0) && aspect[0] > 0 {
		height = int(math.Round(8 * min(aspect[0], 8)))
	}
	return width, max(2, height)
}

func paneWinsize(cols, rows, cellWidth, cellHeight int) *pty.Winsize {
	return &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows), X: uint16(min(65535, cols*cellWidth)), Y: uint16(min(65535, rows*cellHeight))}
}
