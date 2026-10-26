package main

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"log/slog"
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
		return fmt.Errorf("create PNG: %w", err)
	}
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(f, img); err != nil {
		if cerr := f.Close(); cerr != nil {
			slog.Log(context.Background(), LevelTrace, "close PNG", "path", path, "err", cerr)
		}
		return fmt.Errorf("encode PNG: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close PNG: %w", err)
	}
	return nil
}

// savePNG converts and writes the top-left cw x ch pixels of a BGRA image
// w pixels wide on a goroutine, so a test's dump or shot does not stall
// the main loop (PNG encoding takes long in -race builds). finish waits.
func (a *App) savePNG(path string, bgra []byte, w, cw, ch int) {
	a.pngJobs.Add(1)
	go func() {
		defer a.pngJobs.Done()
		if err := writePNG(path, cropBGRA(bgra, w, cw, ch), cw, ch); err != nil {
			a.pngMu.Lock()
			a.pngErrs = append(a.pngErrs, path+": "+err.Error())
			a.pngMu.Unlock()
		}
	}()
}

// waitPNG waits for savePNG writes and reports their errors.
func (a *App) waitPNG() {
	a.pngJobs.Wait()
	a.pngMu.Lock()
	for _, e := range a.pngErrs {
		a.reportError("png: %s", e)
	}
	a.pngErrs = nil
	a.pngMu.Unlock()
}

// verify checks that GSK maps p's texture 1:1 onto device pixels: the
// window's own renderer composites the last frame and a 1-px checkerboard
// offscreen at the surface scale, and the result must equal the texture.
// The real compositor output is not captured.
func (a *App) verify(p *Pane) {
	g := viewGeomC(p.view)
	fmt.Printf("\n## verify: widget %dx%d logical at surface (%.2f, %.2f), scale %.4f -> device rect (%d, %d) %dx%d\n",
		g.lw, g.lh, g.ox, g.oy, g.scale, g.x0, g.y0, g.dw, g.dh)
	tex, w, h := downloadLastC(p.view)
	got := proxyLastC(p.view, w, h)
	fmt.Printf("- last frame through GSK: %s\n", diffSummary(tex, got))
	vr := &verifyReport{FrameDiffer: -1, CheckerDiffer: -1}
	a.rep.Verify = vr
	vr.FramePixels, vr.FrameDiffer, vr.FrameMaxDelta = diffCount(tex, got)
	if p.r == nil {
		return
	}
	viewMakeCurrentC(p.view)
	var s viewSlot
	allocSlot(&s, g.dw, g.dh)
	gl.BindFramebuffer(gl.FRAMEBUFFER, s.fbo)
	fw, fh := p.r.fbW, p.r.fbH
	p.r.fbW, p.r.fbH = g.dw, g.dh
	err := p.r.DrawChecker()
	p.r.fbW, p.r.fbH = fw, fh
	s.sync = gl.FenceSync()
	gl.Flush()
	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
	if err != nil {
		fmt.Printf("- checkerboard: %v\n", err)
		return
	}
	out, texpix := proxyGLC(p.view, s.tex, s.sync, g.dw, g.dh)
	fmt.Printf("- 1-px checkerboard through GSK: %s; pixels not pure black/white: %d\n", diffSummary(texpix, out), nonBinary(out))
	vr.CheckerPixels, vr.CheckerDiffer, vr.CheckerMaxDelta = diffCount(texpix, out)
	vr.CheckerNonBinary = nonBinary(out)
	viewMakeCurrentC(p.view)
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
	total, diff, maxd := diffCount(want, got)
	return fmt.Sprintf("%d of %d pixels differ (max channel delta %d)", diff, total, maxd)
}

// diffCount compares two BGRA images; differ is -1 when they cannot be
// compared.
func diffCount(want, got []byte) (total, differ, maxDelta int) {
	if want == nil || got == nil || len(want) != len(got) {
		return 0, -1, 0
	}
	for i := 0; i < len(want); i += 4 {
		px := 0
		for c := 0; c < 3; c++ {
			d := int(want[i+c]) - int(got[i+c])
			px = max(px, d, -d)
		}
		if px != 0 {
			differ++
		}
		maxDelta = max(maxDelta, px)
	}
	return len(want) / 4, differ, maxDelta
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
