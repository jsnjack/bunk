package main

import (
	"unsafe"

	"bunk/cmd/bunk-gui/gl"
)

// Instance is the per-cell GPU record: 12 bytes, one per grid cell.
type Instance struct {
	Glyph uint32 // atlas slot plus flags (see atlas.go)
	FG    uint32 // RGBA8, R in the low byte
	BG    uint32
}

const instanceSize = int(unsafe.Sizeof(Instance{}))

const vertexCommon = `#version 330 core
layout(location = 0) in uint a_glyph;
layout(location = 1) in vec4 a_fg;
layout(location = 2) in vec4 a_bg;
uniform ivec2 u_grid;     // cols, rows
uniform vec2  u_cell;     // cell size in device px
uniform vec2  u_viewport; // framebuffer size in device px
uniform float u_yflip;    // -1: texture read top-down (GdkGLTexture)
const uint WIDE = 65536u;
vec2 corner() { return vec2(float(gl_VertexID & 1), float(gl_VertexID >> 1)); }
vec2 cellOrigin() {
	return vec2(float(gl_InstanceID % u_grid.x), float(gl_InstanceID / u_grid.x)) * u_cell;
}
vec4 toClip(vec2 px) {
	vec2 ndc = px / u_viewport * 2.0 - 1.0;
	return vec4(ndc.x, -ndc.y * u_yflip, 0.0, 1.0);
}
`

// The background pass also draws decorations and the bar/underline cursor,
// so they appear under spaces too.
const bgVertex = vertexCommon + `
flat out vec4 v_bg;
flat out vec4 v_fg;
flat out uint v_flags;
out vec2 v_local;
void main() {
	vec2 c = corner();
	gl_Position = toClip(cellOrigin() + c * u_cell);
	v_local = c * u_cell;
	v_bg = a_bg;
	v_fg = a_fg;
	v_flags = a_glyph;
}
`

const bgFragment = `#version 330 core
uniform vec2 u_cell;
uniform vec4 u_lines; // underline top, line thickness, strikethrough top, bar width
uniform vec4 u_dim;   // rgb: colour to fade towards, a: amount (inactive panes)
flat in vec4 v_bg;
flat in vec4 v_fg;
flat in uint v_flags;
in vec2 v_local;
out vec4 o_color;
bool band(float y, float top, float h) { return y >= top && y < top + h; }
void main() {
	vec4 c = v_bg;
	float x = v_local.x, y = v_local.y, t = u_lines.y;
	if ((v_flags & 4194304u) != 0u && y < floor(u_cell.y * 0.5)) c = v_fg;      // image half block
	if ((v_flags & 131072u) != 0u && band(y, u_lines.x, t)) c = v_fg;          // underline
	if ((v_flags & 262144u) != 0u && band(y, u_lines.z, t)) c = v_fg;          // strikethrough
	if ((v_flags & 524288u) != 0u && y < t) c = v_fg;                          // overline
	if ((v_flags & 1048576u) != 0u && x < u_lines.w) c = v_fg;                 // bar cursor
	if ((v_flags & 2097152u) != 0u && y >= u_cell.y - 2.0 * t) c = v_fg;       // underline cursor
	o_color = vec4(mix(c.rgb, u_dim.rgb, u_dim.a), c.a);
}
`

const textVertex = vertexCommon + `
uniform int u_slotsPerRow;
out vec2 v_uv;
flat out vec4 v_fg;
void main() {
	v_fg = a_fg;
	int slot = int(a_glyph & 65535u);
	if (slot == 0) {
		// Nothing to draw: collapse the quad outside the clip volume.
		gl_Position = vec4(2.0, 2.0, 2.0, 1.0);
		v_uv = vec2(0.0);
		return;
	}
	vec2 size = u_cell * vec2((a_glyph & WIDE) != 0u ? 2.0 : 1.0, 1.0);
	vec2 c = corner();
	gl_Position = toClip(cellOrigin() + c * size);
	vec2 slotOrigin = vec2(float(slot % u_slotsPerRow), float(slot / u_slotsPerRow)) * u_cell;
	v_uv = slotOrigin + c * size; // texel units
}
`

const textFragment = `#version 330 core
uniform sampler2D u_atlas;
uniform vec4 u_dim;
in vec2 v_uv;
flat in vec4 v_fg;
out vec4 o_color;
void main() {
	float a = texelFetch(u_atlas, ivec2(v_uv), 0).r * v_fg.a;
	o_color = vec4(mix(v_fg.rgb, u_dim.rgb, u_dim.a) * a, a); // premultiplied
}
`

// The overlay pass draws one flat rectangle (the scrollbar thumb).
const rectVertex = `#version 330 core
uniform vec4  u_rect;     // x, y, w, h in device px
uniform vec2  u_viewport;
uniform float u_yflip;
void main() {
	vec2 c = vec2(float(gl_VertexID & 1), float(gl_VertexID >> 1));
	vec2 ndc = (u_rect.xy + c * u_rect.zw) / u_viewport * 2.0 - 1.0;
	gl_Position = vec4(ndc.x, -ndc.y * u_yflip, 0.0, 1.0);
}
`

const rectFragment = `#version 330 core
uniform vec4 u_color; // premultiplied
out vec4 o_color;
void main() { o_color = u_color; }
`

const checkerVertex = `#version 330 core
void main() {
	vec2 p = vec2(float((gl_VertexID << 1) & 2), float(gl_VertexID & 2));
	gl_Position = vec4(p * 2.0 - 1.0, 0.0, 1.0); // one full-screen triangle
}
`

// checkerFragment draws a 1-px black/white checkerboard (-verify only).
const checkerFragment = `#version 330 core
out vec4 o_color;
void main() {
	ivec2 c = ivec2(gl_FragCoord.xy);
	float v = float((c.x + c.y) & 1);
	o_color = vec4(v, v, v, 1.0);
}
`

// Renderer owns the GL objects. All methods run on the GTK main thread with
// the view's GL context current.
type Renderer struct {
	cols, rows int
	atlas      *Atlas
	inst       []Instance
	rowDirty   []bool // rows whose instances changed since the last upload

	bgProg, textProg uint32
	vao, vbo, tex    uint32
	bgLoc, textLoc   uniformLocs
	linesLoc         int32
	slotsLoc         int32
	atlasLoc         int32
	checkerProg      uint32
	rectProg         uint32
	rectLoc          struct{ rect, viewport, yflip, color int32 }

	fbW, fbH int
	yflip    float32
	// Atlas upload state: the texture holds slots [1, atlasNext) of an
	// atlas texH pixels tall.
	atlasNext, texH int
	clearRGB        [3]float32
	// Inactive panes fade towards the background by dim (0: off); a pane
	// that needs the user gets an orange inner ring of ringPx pixels.
	dim, ringPx float32
	dimLoc      [2]int32 // bg, text programs

	img imageLayer // image cells at full resolution (images.go)

	queries     [8]uint32
	qHead       int // next query to issue
	qTail       int // oldest query not yet read
	qSkipped    int
	timerOK     bool
	uploadCalls int
}

type uniformLocs struct{ grid, cell, viewport, yflip int32 }

func NewRenderer(cols, rows int, atlas *Atlas) (*Renderer, error) {
	r := &Renderer{atlas: atlas, yflip: -1}
	var err error
	if r.bgProg, err = gl.CompileProgram(bgVertex, bgFragment); err != nil {
		return nil, err
	}
	if r.textProg, err = gl.CompileProgram(textVertex, textFragment); err != nil {
		return nil, err
	}
	locs := func(p uint32) uniformLocs {
		return uniformLocs{gl.UniformLocation(p, "u_grid"), gl.UniformLocation(p, "u_cell"),
			gl.UniformLocation(p, "u_viewport"), gl.UniformLocation(p, "u_yflip")}
	}
	r.bgLoc, r.textLoc = locs(r.bgProg), locs(r.textProg)
	if r.rectProg, err = gl.CompileProgram(rectVertex, rectFragment); err != nil {
		return nil, err
	}
	r.rectLoc.rect = gl.UniformLocation(r.rectProg, "u_rect")
	r.rectLoc.viewport = gl.UniformLocation(r.rectProg, "u_viewport")
	r.rectLoc.yflip = gl.UniformLocation(r.rectProg, "u_yflip")
	r.rectLoc.color = gl.UniformLocation(r.rectProg, "u_color")
	r.linesLoc = gl.UniformLocation(r.bgProg, "u_lines")
	r.dimLoc = [2]int32{gl.UniformLocation(r.bgProg, "u_dim"), gl.UniformLocation(r.textProg, "u_dim")}
	r.slotsLoc = gl.UniformLocation(r.textProg, "u_slotsPerRow")
	r.atlasLoc = gl.UniformLocation(r.textProg, "u_atlas")

	r.vao = gl.GenVertexArray()
	gl.BindVertexArray(r.vao)
	r.vbo = gl.GenBuffer()
	gl.BindBuffer(gl.ARRAY_BUFFER, r.vbo)
	r.Resize(cols, rows)
	gl.VertexAttribIPointer(0, 1, gl.UNSIGNED_INT, int32(instanceSize), 0)
	gl.VertexAttribPointer(1, 4, gl.UNSIGNED_BYTE, true, int32(instanceSize), 4)
	gl.VertexAttribPointer(2, 4, gl.UNSIGNED_BYTE, true, int32(instanceSize), 8)
	for i := uint32(0); i < 3; i++ {
		gl.EnableVertexAttribArray(i)
		gl.VertexAttribDivisor(i, 1)
	}

	r.tex = gl.GenTexture()
	gl.BindTexture(gl.TEXTURE_2D, r.tex)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)
	r.syncAtlas()
	if err := r.img.init(); err != nil {
		return nil, err
	}

	for i := range r.queries {
		r.queries[i] = gl.GenQuery()
	}
	r.timerOK = gl.GetError() == 0
	return r, nil
}

// Resize reallocates the instance buffer for a new grid size.
func (r *Renderer) Resize(cols, rows int) {
	r.cols, r.rows = cols, rows
	r.inst = make([]Instance, cols*rows)
	r.rowDirty = make([]bool, rows)
	r.img.resize(cols, rows)
	gl.BindBuffer(gl.ARRAY_BUFFER, r.vbo)
	gl.BufferData(gl.ARRAY_BUFFER, len(r.inst)*instanceSize, nil, gl.DYNAMIC_DRAW)
}

// SetAtlas switches to an atlas built for another scale.
func (r *Renderer) SetAtlas(a *Atlas) {
	r.atlas = a
	r.texH = 0
	r.syncAtlas()
}

// syncAtlas uploads glyphs rasterized since the last call: the whole texture
// after the atlas grew, otherwise only the slot rows holding new slots.
func (r *Renderer) syncAtlas() {
	a := r.atlas
	if r.texH == a.H && r.atlasNext == a.next {
		return
	}
	gl.ActiveTexture(gl.TEXTURE0)
	gl.BindTexture(gl.TEXTURE_2D, r.tex)
	gl.PixelStorei(gl.UNPACK_ALIGNMENT, 1)
	if r.texH != a.H {
		gl.TexImage2D(gl.TEXTURE_2D, 0, gl.R8, int32(a.W), int32(a.H), gl.RED, gl.UNSIGNED_BYTE, unsafe.Pointer(&a.Pixels[0]))
	} else {
		y0 := r.atlasNext / a.SlotsPerRow * a.CellH
		y1 := ((a.next-1)/a.SlotsPerRow + 1) * a.CellH
		gl.TexSubImage2D(gl.TEXTURE_2D, 0, 0, int32(y0), int32(a.W), int32(y1-y0), gl.RED, gl.UNSIGNED_BYTE, unsafe.Pointer(&a.Pixels[y0*a.W]))
	}
	r.texH, r.atlasNext = a.H, a.next
}

// Upload sends each contiguous run of changed rows with one glBufferSubData
// and returns the number of bytes sent.
func (r *Renderer) Upload() int {
	r.syncAtlas()
	gl.BindBuffer(gl.ARRAY_BUFFER, r.vbo)
	bytes := 0
	rowBytes := r.cols * instanceSize
	r.uploadCalls = 0
	for row := 0; row < r.rows; {
		if !r.rowDirty[row] {
			row++
			continue
		}
		start := row
		for row < r.rows && r.rowDirty[row] {
			r.rowDirty[row] = false
			row++
		}
		n := (row - start) * rowBytes
		gl.BufferSubData(gl.ARRAY_BUFFER, start*rowBytes, n, unsafe.Pointer(&r.inst[start*r.cols]))
		bytes += n
		r.uploadCalls++
	}
	return bytes
}

// Draw clears to the default background and issues the background and text
// passes: two instanced draws.
func (r *Renderer) Draw() {
	a := r.atlas
	gl.Viewport(0, 0, int32(r.fbW), int32(r.fbH))
	gl.ClearColor(r.clearRGB[0], r.clearRGB[1], r.clearRGB[2], 1) // the background fades into itself
	gl.Clear(gl.COLOR_BUFFER_BIT)
	gl.BindVertexArray(r.vao)
	n := int32(r.cols * r.rows)
	cw, ch := float32(a.CellW), float32(a.CellH)
	r.setDim()

	gl.UseProgram(r.bgProg)
	gl.Uniform2i(r.bgLoc.grid, int32(r.cols), int32(r.rows))
	gl.Uniform2f(r.bgLoc.cell, cw, ch)
	gl.Uniform2f(r.bgLoc.viewport, float32(r.fbW), float32(r.fbH))
	gl.Uniform1f(r.bgLoc.yflip, r.yflip)
	gl.Uniform4f(r.linesLoc, float32(a.ULPos), float32(a.Thick), float32(a.StrikePos), float32(max(1, a.Thick)))
	gl.DrawArraysInstanced(gl.TRIANGLE_STRIP, 0, 4, n)

	r.img.draw(r)

	gl.UseProgram(r.textProg)
	gl.Uniform2i(r.textLoc.grid, int32(r.cols), int32(r.rows))
	gl.Uniform2f(r.textLoc.cell, cw, ch)
	gl.Uniform2f(r.textLoc.viewport, float32(r.fbW), float32(r.fbH))
	gl.Uniform1f(r.textLoc.yflip, r.yflip)
	gl.Uniform1i(r.slotsLoc, int32(a.SlotsPerRow))
	gl.Uniform1i(r.atlasLoc, 0)
	gl.ActiveTexture(gl.TEXTURE0)
	gl.BindTexture(gl.TEXTURE_2D, r.tex)
	gl.Enable(gl.BLEND)
	gl.BlendFunc(gl.ONE, gl.ONE_MINUS_SRC_ALPHA)
	gl.DrawArraysInstanced(gl.TRIANGLE_STRIP, 0, 4, n)
	gl.Disable(gl.BLEND)
}

// DrawScrollbar draws the scrollback position as a thin thumb at the right
// edge: one extra draw, only while the view is scrolled back.
func (r *Renderer) setDim() {
	gl.UseProgram(r.bgProg)
	gl.Uniform4f(r.dimLoc[0], r.clearRGB[0], r.clearRGB[1], r.clearRGB[2], r.dim)
	gl.UseProgram(r.textProg)
	gl.Uniform4f(r.dimLoc[1], r.clearRGB[0], r.clearRGB[1], r.clearRGB[2], r.dim)
}

// DrawRing draws the needs-you ring (#ffb454) inside the framebuffer edges.
func (r *Renderer) DrawRing() {
	if r.ringPx <= 0 {
		return
	}
	w, h, t := float32(r.fbW), float32(r.fbH), r.ringPx
	gl.UseProgram(r.rectProg)
	gl.Uniform2f(r.rectLoc.viewport, w, h)
	gl.Uniform1f(r.rectLoc.yflip, r.yflip)
	gl.Uniform4f(r.rectLoc.color, 1, 0xb4/255.0, 0x54/255.0, 1)
	for _, rc := range [4][4]float32{{0, 0, w, t}, {0, h - t, w, t}, {0, 0, t, h}, {w - t, 0, t, h}} {
		gl.Uniform4f(r.rectLoc.rect, rc[0], rc[1], rc[2], rc[3])
		gl.DrawArrays(gl.TRIANGLE_STRIP, 0, 4)
	}
}

func (r *Renderer) DrawScrollbar(off, count int) {
	if off <= 0 || count <= 0 {
		return
	}
	h := float32(r.fbH)
	total := float32(count + r.rows)
	thumb := max(float32(r.atlas.CellH), h*float32(r.rows)/total)
	y := (h - thumb) * (1 - float32(off)/float32(count))
	w := float32(max(3, r.atlas.CellW/3))
	gl.UseProgram(r.rectProg)
	gl.Uniform4f(r.rectLoc.rect, float32(r.fbW)-w-2, y, w, thumb)
	gl.Uniform2f(r.rectLoc.viewport, float32(r.fbW), float32(r.fbH))
	gl.Uniform1f(r.rectLoc.yflip, r.yflip)
	gl.Uniform4f(r.rectLoc.color, 0.5, 0.5, 0.5, 0.6) // premultiplied grey
	gl.Enable(gl.BLEND)
	gl.BlendFunc(gl.ONE, gl.ONE_MINUS_SRC_ALPHA)
	gl.DrawArrays(gl.TRIANGLE_STRIP, 0, 4)
	gl.Disable(gl.BLEND)
}

// Delete frees the renderer's GL objects (the view's context is current).
func (r *Renderer) Delete() {
	r.img.delete()
	gl.DeleteTexture(r.tex)
	gl.DeleteBuffer(r.vbo)
	gl.DeleteVertexArray(r.vao)
	for _, p := range []uint32{r.bgProg, r.textProg, r.rectProg, r.checkerProg} {
		if p != 0 {
			gl.DeleteProgram(p)
		}
	}
	for _, q := range r.queries {
		gl.DeleteQuery(q)
	}
}

// DrawChecker fills the viewport with a 1-px checkerboard (-verify only).
func (r *Renderer) DrawChecker() error {
	if r.checkerProg == 0 {
		p, err := gl.CompileProgram(checkerVertex, checkerFragment)
		if err != nil {
			return err
		}
		r.checkerProg = p
	}
	gl.Viewport(0, 0, int32(r.fbW), int32(r.fbH))
	gl.BindVertexArray(r.vao)
	gl.UseProgram(r.checkerProg)
	gl.DrawArrays(gl.TRIANGLES, 0, 3)
	return nil
}

// BeginTimer starts a GL_TIME_ELAPSED query if a slot is free.
func (r *Renderer) BeginTimer() bool {
	if !r.timerOK || r.qHead-r.qTail >= len(r.queries) {
		r.qSkipped++
		return false
	}
	gl.BeginQuery(gl.TIME_ELAPSED, r.queries[r.qHead%len(r.queries)])
	return true
}

func (r *Renderer) EndTimer() {
	gl.EndQuery(gl.TIME_ELAPSED)
	r.qHead++
}

// PollTimers reads finished queries without stalling and appends each GPU
// time (µs) to dst when it is non-nil.
func (r *Renderer) PollTimers(dst *series) {
	for r.qTail < r.qHead {
		q := r.queries[r.qTail%len(r.queries)]
		if gl.GetQueryObjecti(q, gl.QUERY_RESULT_AVAILABLE) == 0 {
			return
		}
		ns := gl.GetQueryObjectui64(q, gl.QUERY_RESULT)
		if dst != nil {
			dst.Add(float64(ns) / 1e3)
		}
		r.qTail++
	}
}
