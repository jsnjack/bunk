package main

import (
	"image/color"

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
