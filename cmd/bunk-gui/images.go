package main

// Image cells at full resolution. The session keeps each placement's
// bitmap in its image store (graphics.Store); a cell names the placement
// (ImageCell.Placement.Bitmap) and its own column and row in it. Per pane:
//
//   - sync (session locked, each frame): copies the store records of
//     placements that rows rebuilt this frame refer to, keeps placements on
//     screen recent in the store's LRU order, and notices placements the
//     store dropped (evicted, or swept once no cell refers to them).
//   - convert (termView.convert): an image cell whose placement is known
//     gets no glyph and one entry in its row's image list; otherwise it
//     draws its two colour samples as a half block (flagHalf).
//   - draw (between the background and text passes): uploads a
//     placement's bitmap to a texture the first time it is drawn, then
//     draws every image cell as a textured quad of its own sub-rectangle,
//     one instanced draw per placement. Cells drawn at the size the image
//     was placed with use texelFetch (exact pixels); others are scaled
//     with linear filtering.
//
// Textures of placements the store dropped are freed at the next frame;
// textures not drawn in a frame are freed oldest first while the pane
// holds more than its GPU budget. Steady frames allocate nothing: the row
// lists, the sorted list and the GPU records reuse their storage.

import (
	"cmp"
	"image"
	"image/color"
	"slices"
	"unsafe"

	"bunk/cmd/bunk-gui/gl"
	"bunk/internal/graphics"
	"bunk/internal/vt10x"
)

// DefaultImageGPUBytes is a pane's texture budget for images.
const DefaultImageGPUBytes = 256 << 20

// imgInst is the GPU record of one image cell: 8 bytes.
type imgInst struct {
	Cell uint32 // grid column | row<<16
	Src  uint32 // column | row<<16 within the placement
}

const imgInstSize = int(unsafe.Sizeof(imgInst{}))

// imgCell is an image cell of a row and its placement.
type imgCell struct {
	inst imgInst
	id   uint64
}

// imgRun is a run of the sorted cells that share a placement.
type imgRun struct {
	id       uint64
	start, n int
}

// imgTex is a placement's store record and, once drawn, its texture. The
// record's bitmap is dropped after the upload: the store keeps its own, and
// a texture freed for the budget is dropped with its entry, so the next
// frame showing the placement looks it up again.
type imgTex struct {
	st     graphics.Stored
	tex    uint32 // 0 until drawn
	w, h   int    // texture size: the bitmap's source rectangle
	bytes  int
	drawn  uint64 // the frame it was last drawn in
	gone   bool   // the store dropped it: free the texture
	failed bool   // larger than GL_MAX_TEXTURE_SIZE: its cells draw half blocks
}

const imageVertex = `#version 330 core
layout(location = 0) in uint a_cell;
layout(location = 1) in uint a_src;
uniform vec2  u_cell;     // grid cell size in device px
uniform vec2  u_viewport; // framebuffer size in device px
uniform float u_yflip;
uniform vec2  u_pcell;    // the placement's cell size in its own pixels
out vec2 v_place;         // position in the placement, its pixels
void main() {
	vec2 c = vec2(float(gl_VertexID & 1), float(gl_VertexID >> 1));
	vec2 cell = vec2(float(a_cell & 65535u), float(a_cell >> 16));
	vec2 src = vec2(float(a_src & 65535u), float(a_src >> 16));
	vec2 ndc = (cell + c) * u_cell / u_viewport * 2.0 - 1.0;
	gl_Position = vec4(ndc.x, -ndc.y * u_yflip, 0.0, 1.0);
	v_place = (src + c) * u_pcell;
}
`

const imageFragment = `#version 330 core
uniform sampler2D u_img;
uniform vec4 u_area;  // image rectangle in the placement: x, y, w, h
uniform vec2 u_tex;   // texture size
uniform int  u_exact; // 1: one texel per device pixel
uniform vec4 u_dim;
in vec2 v_place;
out vec4 o_color;
void main() {
	vec2 q = (v_place - u_area.xy) / u_area.zw;
	if (q.x < 0.0 || q.y < 0.0 || q.x >= 1.0 || q.y >= 1.0) discard;
	vec4 t = u_exact != 0 ? texelFetch(u_img, ivec2(q * u_tex), 0) : texture(u_img, q);
	o_color = vec4(mix(t.rgb, u_dim.rgb * t.a, u_dim.a), t.a); // premultiplied
}
`

// imageLayer draws one pane's image cells. sync runs with the session
// locked; everything else on the main thread without it.
type imageLayer struct {
	cache map[uint64]*imgTex
	// rows holds the image cells of each grid row, each a window of cols
	// cells into backing, so rows never grow while scrolling. Allocated
	// when the first image cell shows.
	rows        [][]imgCell
	backing     []imgCell
	cols, nrows int
	dirty       bool      // a row's list changed since flat was built
	redo        bool      // an upload failed: rebuild every row with half blocks
	flat        []imgCell // every row's cells, sorted by placement
	gpu         []imgInst
	runs        []imgRun
	frame       uint64

	budget             int // texture bytes held before textures not on screen are freed
	texBytes, textures int
	uploads, freed     int

	prog, vao, vbo, vboCap uint32
	maxTex                 int32
	loc                    struct{ cell, viewport, yflip, pcell, img, area, tex, exact, dim int32 }
}

func (l *imageLayer) init() error {
	l.cache = map[uint64]*imgTex{}
	if l.budget == 0 {
		l.budget = DefaultImageGPUBytes
	}
	p, err := gl.CompileProgram(imageVertex, imageFragment)
	if err != nil {
		return err
	}
	l.prog = p
	for name, dst := range map[string]*int32{
		"u_cell": &l.loc.cell, "u_viewport": &l.loc.viewport, "u_yflip": &l.loc.yflip, "u_pcell": &l.loc.pcell,
		"u_img": &l.loc.img, "u_area": &l.loc.area, "u_tex": &l.loc.tex, "u_exact": &l.loc.exact, "u_dim": &l.loc.dim,
	} {
		*dst = gl.UniformLocation(p, name)
	}
	l.vao = gl.GenVertexArray()
	gl.BindVertexArray(l.vao)
	l.vbo = gl.GenBuffer()
	gl.BindBuffer(gl.ARRAY_BUFFER, l.vbo)
	for i := uint32(0); i < 2; i++ {
		gl.EnableVertexAttribArray(i)
		gl.VertexAttribDivisor(i, 1)
	}
	var max [1]int32
	gl.GetIntegerv(gl.MAX_TEXTURE_SIZE, max[:])
	l.maxTex = max[0]
	return nil
}

// resize drops the row lists for a new grid size.
func (l *imageLayer) resize(cols, rows int) {
	l.cols, l.nrows = cols, rows
	l.rows, l.backing = nil, nil
	l.dirty = true
}

// rowCells returns row y's image list emptied, or nil before any image.
func (l *imageLayer) rowCells(y int) []imgCell {
	if l.rows == nil {
		return nil
	}
	return l.rows[y][:0]
}

// firstCells allocates the row lists when the first image cell shows and
// returns row y's empty list.
func (l *imageLayer) firstCells(y int) []imgCell {
	if l.rows == nil {
		l.backing = make([]imgCell, l.cols*l.nrows)
		l.rows = make([][]imgCell, l.nrows)
		for i := range l.rows {
			l.rows[i] = l.backing[i*l.cols : i*l.cols : (i+1)*l.cols]
		}
	}
	return l.rows[y][:0]
}

// sync runs with the session locked, after termView.pull. It looks up the
// placements that rows to rebuild refer to, keeps those drawn in the last
// frame recent in the store, and marks entries the store no longer has.
// It reports whether a placement on screen is gone, so every row must be
// rebuilt with half blocks.
func (l *imageLayer) sync(store *graphics.Store, v *termView) (redraw bool) {
	if l.cache == nil {
		l.cache = map[uint64]*imgTex{}
	}
	redraw, l.redo = l.redo, false
	for id, e := range l.cache {
		if e.gone {
			continue
		}
		var ok bool
		if e.drawn == l.frame && e.drawn != 0 {
			ok = store.Touch(id)
		} else {
			ok = store.Has(id)
		}
		if !ok {
			e.gone = true
			redraw = redraw || e.drawn == l.frame
		}
	}
	if store == nil || store.Len() == 0 {
		return redraw
	}
	for y := 0; y < v.rows; y++ {
		if !v.dirty[y] || !v.lines[y].HasImages() {
			continue
		}
		line := &v.lines[y]
		var last uint64
		for x := 0; x < line.Len(); x++ {
			img := line.Image(x)
			if img == nil || img.Composite || img.Placement == nil || img.Placement.Bitmap == 0 || img.Placement.Bitmap == last {
				continue
			}
			id := img.Placement.Bitmap
			last = id
			if e := l.cache[id]; e != nil && !e.gone {
				continue
			}
			if st, ok := store.Get(id); ok {
				l.cache[id] = &imgTex{st: st}
			}
		}
	}
	return redraw
}

// collect frees the textures of placements the store dropped. Main thread,
// GL context current, session not locked.
func (l *imageLayer) collect() {
	for id, e := range l.cache {
		if e.gone {
			l.freeTex(e)
			delete(l.cache, id)
		}
	}
}

// entry returns the drawable placement of an image cell, or nil when its
// cell draws as a half block.
func (l *imageLayer) entry(img *vt10x.ImageCell) *imgTex {
	if img.Composite || img.Placement == nil || img.Placement.Bitmap == 0 {
		return nil
	}
	e := l.cache[img.Placement.Bitmap]
	if e == nil || e.gone || e.failed || int(img.Col) >= e.st.Cols || int(img.Row) >= e.st.Rows {
		return nil
	}
	return e
}

// draw draws the image cells of the grid. The background pass ran; the
// text pass follows.
func (l *imageLayer) draw(r *Renderer) {
	l.frame++
	if l.dirty {
		l.rebuild()
	}
	if len(l.runs) == 0 {
		l.trim()
		return
	}
	a := r.atlas
	gl.UseProgram(l.prog)
	gl.Uniform2f(l.loc.cell, float32(a.CellW), float32(a.CellH))
	gl.Uniform2f(l.loc.viewport, float32(r.fbW), float32(r.fbH))
	gl.Uniform1f(l.loc.yflip, r.yflip)
	gl.Uniform1i(l.loc.img, 1)
	gl.Uniform4f(l.loc.dim, r.clearRGB[0], r.clearRGB[1], r.clearRGB[2], r.dim)
	gl.ActiveTexture(gl.TEXTURE1)
	gl.BindVertexArray(l.vao)
	gl.BindBuffer(gl.ARRAY_BUFFER, l.vbo)
	gl.Enable(gl.BLEND)
	gl.BlendFunc(gl.ONE, gl.ONE_MINUS_SRC_ALPHA)
	for _, run := range l.runs {
		e := l.cache[run.id]
		if e == nil || e.gone || e.failed {
			continue
		}
		if e.tex == 0 && !l.upload(e) {
			continue
		}
		e.drawn = l.frame
		st := &e.st
		gl.BindTexture(gl.TEXTURE_2D, e.tex)
		gl.Uniform2f(l.loc.pcell, float32(st.CellWidth), float32(st.CellHeight))
		gl.Uniform4f(l.loc.area, float32(st.OffsetX), float32(st.OffsetY), float32(st.PixelWidth), float32(st.PixelHeight))
		gl.Uniform2f(l.loc.tex, float32(e.w), float32(e.h))
		exact := int32(0)
		if st.CellWidth == a.CellW && st.CellHeight == a.CellH && st.PixelWidth == e.w && st.PixelHeight == e.h {
			exact = 1
		}
		gl.Uniform1i(l.loc.exact, exact)
		off := uintptr(run.start * imgInstSize)
		gl.VertexAttribIPointer(0, 1, gl.UNSIGNED_INT, int32(imgInstSize), off)
		gl.VertexAttribIPointer(1, 1, gl.UNSIGNED_INT, int32(imgInstSize), off+4)
		gl.DrawArraysInstanced(gl.TRIANGLE_STRIP, 0, 4, int32(run.n))
	}
	gl.Disable(gl.BLEND)
	gl.ActiveTexture(gl.TEXTURE0)
	gl.BindVertexArray(r.vao)
	l.trim()
}

// rebuild sorts every row's image cells by placement and uploads them.
func (l *imageLayer) rebuild() {
	l.rebuildLists()
	if len(l.gpu) == 0 {
		return
	}
	gl.BindBuffer(gl.ARRAY_BUFFER, l.vbo)
	n := len(l.gpu) * imgInstSize
	if uint32(n) > l.vboCap {
		l.vboCap = uint32(cap(l.gpu) * imgInstSize)
		gl.BufferData(gl.ARRAY_BUFFER, int(l.vboCap), nil, gl.DYNAMIC_DRAW)
	}
	gl.BufferSubData(gl.ARRAY_BUFFER, 0, n, unsafe.Pointer(&l.gpu[0]))
}

// rebuildLists builds the sorted cells, their runs and GPU records.
func (l *imageLayer) rebuildLists() {
	l.dirty = false
	l.flat = l.flat[:0]
	for y := range l.rows {
		l.flat = append(l.flat, l.rows[y]...)
	}
	slices.SortFunc(l.flat, func(a, b imgCell) int { return cmp.Compare(a.id, b.id) })
	l.runs, l.gpu = l.runs[:0], l.gpu[:0]
	for i, c := range l.flat {
		if i == 0 || c.id != l.flat[i-1].id {
			l.runs = append(l.runs, imgRun{id: c.id, start: i})
		}
		l.runs[len(l.runs)-1].n++
		l.gpu = append(l.gpu, c.inst)
	}
}

// upload creates a placement's texture from the source rectangle of its
// bitmap, premultiplied. It allocates only for a translucent bitmap.
func (l *imageLayer) upload(e *imgTex) bool {
	img := e.st.Image
	src := e.st.Source.Intersect(img.Bounds())
	w, h := src.Dx(), src.Dy()
	if w <= 0 || h <= 0 || int32(w) > l.maxTex || int32(h) > l.maxTex {
		e.failed, l.redo = true, true
		return false
	}
	pix, rowLen := img.Pix[img.PixOffset(src.Min.X, src.Min.Y):], img.Stride/4
	if !opaque(img, src) {
		pix, rowLen = premultiplied(img, src), w
	}
	e.tex = gl.GenTexture()
	gl.BindTexture(gl.TEXTURE_2D, e.tex)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)
	gl.PixelStorei(gl.UNPACK_ROW_LENGTH, int32(rowLen))
	gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA8, int32(w), int32(h), gl.RGBA, gl.UNSIGNED_BYTE, unsafe.Pointer(&pix[0]))
	gl.PixelStorei(gl.UNPACK_ROW_LENGTH, 0)
	e.st.Image = nil
	e.w, e.h, e.bytes = w, h, w*h*4
	l.texBytes += e.bytes
	l.textures++
	l.uploads++
	return true
}

// opaque reports whether every pixel of r is opaque, so the bitmap is its
// own premultiplied form.
func opaque(img *image.NRGBA, r image.Rectangle) bool {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := img.Pix[img.PixOffset(r.Min.X, y) : img.PixOffset(r.Max.X-1, y)+4]
		for i := 3; i < len(row); i += 4 {
			if row[i] != 0xff {
				return false
			}
		}
	}
	return true
}

// premultiplied returns r of img as tightly packed premultiplied RGBA.
func premultiplied(img *image.NRGBA, r image.Rectangle) []byte {
	out := make([]byte, 0, r.Dx()*r.Dy()*4)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := img.Pix[img.PixOffset(r.Min.X, y) : img.PixOffset(r.Max.X-1, y)+4]
		for i := 0; i < len(row); i += 4 {
			a := uint32(row[i+3])
			pm := func(v byte) byte { return byte((uint32(v)*a + 127) / 255) }
			out = append(out, pm(row[i]), pm(row[i+1]), pm(row[i+2]), row[i+3])
		}
	}
	return out
}

// trim frees textures not drawn this frame, least recently drawn first,
// while the pane holds more than its budget.
func (l *imageLayer) trim() {
	for l.texBytes > l.budget {
		var oldest *imgTex
		var oldestID uint64
		for id, e := range l.cache {
			if e.tex != 0 && e.drawn != l.frame && (oldest == nil || e.drawn < oldest.drawn) {
				oldest, oldestID = e, id
			}
		}
		if oldest == nil {
			return
		}
		l.freeTex(oldest)
		delete(l.cache, oldestID)
	}
}

func (l *imageLayer) freeTex(e *imgTex) {
	if e.tex == 0 {
		return
	}
	gl.DeleteTexture(e.tex)
	e.tex = 0
	l.texBytes -= e.bytes
	l.textures--
	l.freed++
	e.bytes = 0
}

// release frees every texture and forgets every placement (the view is
// hidden or its context goes away); showing it again looks them up.
func (l *imageLayer) release() {
	for id, e := range l.cache {
		l.freeTex(e)
		delete(l.cache, id)
	}
}

// delete frees the layer's GL objects.
func (l *imageLayer) delete() {
	l.release()
	if l.prog != 0 {
		gl.DeleteProgram(l.prog)
		gl.DeleteBuffer(l.vbo)
		gl.DeleteVertexArray(l.vao)
	}
}

// halfColor blends an image sample over the cell background, as bunk's
// TUI draws image cells (imageColor).
func halfColor(c color.NRGBA, bg uint32) uint32 {
	a := uint32(c.A)
	mix := func(v uint8, shift uint) uint32 {
		b := bg >> shift & 0xff
		return (uint32(v)*a + b*(255-a) + 127) / 255 << shift
	}
	return mix(c.R, 0) | mix(c.G, 8) | mix(c.B, 16) | 0xff<<24
}
