package main

import (
	"fmt"
	"image"
	"image/png"
	"os"

	"bunk/cmd/bunk-gui/gl"
)

// writePNG writes tightly packed RGBA bytes as an opaque PNG.
func writePNG(path string, rgba []byte, w, h int) error {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	copy(img.Pix, rgba)
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 0xff
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// verify checks that GSK maps the view's texture 1:1 onto device pixels: the
// window's own renderer composites the last frame and a 1-px checkerboard
// offscreen at the surface scale, and the result must equal the texture.
// The real compositor output is not captured.
func (a *App) verify() {
	g := viewGeomC()
	fmt.Printf("\n## verify: widget %dx%d logical at surface (%.2f, %.2f), scale %.4f -> device rect (%d, %d) %dx%d\n",
		g.lw, g.lh, g.ox, g.oy, g.scale, g.x0, g.y0, g.dw, g.dh)
	tex, _, _ := downloadLastC()
	fmt.Printf("- last frame through GSK: %s\n", diffSummary(tex, proxyLastC()))

	viewMakeCurrentC()
	var s viewSlot
	allocSlot(&s, g.dw, g.dh)
	gl.BindFramebuffer(gl.FRAMEBUFFER, s.fbo)
	fw, fh := a.r.fbW, a.r.fbH
	a.r.fbW, a.r.fbH = g.dw, g.dh
	err := a.r.DrawChecker()
	a.r.fbW, a.r.fbH = fw, fh
	s.sync = gl.FenceSync()
	gl.Flush()
	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
	if err != nil {
		fmt.Printf("- checkerboard: %v\n", err)
		return
	}
	out, texpix := proxyGLC(s.tex, s.sync, g.dw, g.dh)
	fmt.Printf("- 1-px checkerboard through GSK: %s; pixels not pure black/white: %d\n", diffSummary(texpix, out), nonBinary(out))
	viewMakeCurrentC()
	gl.DeleteSync(s.sync)
	gl.DeleteFramebuffer(s.fbo)
	gl.DeleteTexture(s.tex)
}

func diffSummary(want, got []byte) string {
	if want == nil || got == nil {
		return "no data"
	}
	if len(want) != len(got) {
		return fmt.Sprintf("size mismatch %d vs %d bytes", len(want), len(got))
	}
	diff, maxd := 0, 0
	for i := 0; i < len(want); i += 4 {
		px := 0
		for c := 0; c < 3; c++ {
			d := int(want[i+c]) - int(got[i+c])
			px = max(px, d, -d)
		}
		if px != 0 {
			diff++
		}
		maxd = max(maxd, px)
	}
	return fmt.Sprintf("%d of %d pixels differ (max channel delta %d)", diff, len(want)/4, maxd)
}

func nonBinary(pix []byte) int {
	n := 0
	for i := 0; i+3 < len(pix); i += 4 {
		for c := 0; c < 3; c++ {
			if v := pix[i+c]; v != 0 && v != 255 {
				n++
				break
			}
		}
	}
	return n
}
