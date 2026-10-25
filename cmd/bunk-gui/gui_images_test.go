package main

// Window scenarios for images and links. Test pictures are generated
// here and printed with cat, so every pixel is known: a picture drawn 1:1
// must appear in the pane's dump exactly, pixel for pixel.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"slices"
	"strings"
	"testing"
)

// picture is a test image: colour a left of x=w/2, b right of it, c in
// the bottom ten rows, and a white line one pixel wide at x=10. Its
// colours use 0 and 255 only, so SIXEL's percent colours are exact.
type picture struct {
	w, h    int
	a, b, c rgb
}

var white = rgb{255, 255, 255}

func (p picture) at(x, y int) rgb {
	switch {
	case y >= p.h-10:
		return p.c
	case x == 10:
		return white
	case x < p.w/2:
		return p.a
	}
	return p.b
}

func (p picture) nrgba() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, p.w, p.h))
	for y := 0; y < p.h; y++ {
		for x := 0; x < p.w; x++ {
			c := p.at(x, y)
			img.SetNRGBA(x, y, color.NRGBA{uint8(c.r), uint8(c.g), uint8(c.b), 255})
		}
	}
	return img
}

// sixel encodes the picture as SIXEL with one register per colour.
func (p picture) sixel() string {
	var colours []rgb
	for y := 0; y < p.h; y++ {
		for x := 0; x < p.w; x++ {
			if c := p.at(x, y); !slices.Contains(colours, c) {
				colours = append(colours, c)
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\x1bP0;1;0q\"1;1;%d;%d", p.w, p.h)
	for i, c := range colours {
		fmt.Fprintf(&b, "#%d;2;%d;%d;%d", i, c.r*100/255, c.g*100/255, c.b*100/255)
	}
	for band := 0; band < p.h; band += 6 {
		for i, c := range colours {
			fmt.Fprintf(&b, "#%d", i)
			for x := 0; x < p.w; x++ {
				bits := 0
				for k := 0; k < 6 && band+k < p.h; k++ {
					if p.at(x, band+k) == c {
						bits |= 1 << k
					}
				}
				b.WriteByte(byte('?' + bits))
			}
			b.WriteByte('$')
		}
		b.WriteByte('-')
	}
	b.WriteString("\x1b\\")
	return b.String()
}

// kitty encodes the picture as Kitty RGBA in chunks of 4096 base64 bytes.
func (p picture) kitty(keys string) string {
	data := base64.StdEncoding.EncodeToString(p.nrgba().Pix)
	var b strings.Builder
	for i := 0; i < len(data); i += 4096 {
		chunk := data[i:min(len(data), i+4096)]
		more := 0
		if i+4096 < len(data) {
			more = 1
		}
		if i == 0 {
			fmt.Fprintf(&b, "\x1b_Ga=T,f=32,s=%d,v=%d,m=%d%s;%s\x1b\\", p.w, p.h, more, keys, chunk)
		} else {
			fmt.Fprintf(&b, "\x1b_Gm=%d;%s\x1b\\", more, chunk)
		}
	}
	return b.String()
}

// iterm encodes the picture as an iTerm2 inline PNG.
func (p picture) iterm() string {
	var buf bytes.Buffer
	if err := png.Encode(&buf, p.nrgba()); err != nil {
		panic(err)
	}
	return fmt.Sprintf("\x1b]1337;File=inline=1;size=%d:%s\a", buf.Len(), base64.StdEncoding.EncodeToString(buf.Bytes()))
}

var (
	sixelPic = picture{60, 60, rgb{255, 0, 0}, rgb{0, 0, 255}, rgb{255, 255, 0}}
	kittyPic = picture{60, 60, rgb{0, 255, 0}, rgb{255, 0, 255}, rgb{0, 255, 255}}
	itermPic = picture{60, 60, rgb{255, 128, 0}, rgb{128, 0, 255}, rgb{0, 128, 255}}
)

// box returns the bounding box of the pixels exactly c in the rectangle r
// of img, and their count.
func box(img image.Image, r image.Rectangle, c rgb) (image.Rectangle, int) {
	var bb image.Rectangle
	n := 0
	r = r.Intersect(img.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if at(img, x, y) == c {
				bb = bb.Union(image.Rect(x, y, x+1, y+1))
				n++
			}
		}
	}
	return bb, n
}

// findPicture locates a picture drawn 1:1 in img by its colour a, and
// returns the top-left corner of the picture.
func findPicture(img image.Image, p picture) (image.Point, bool) {
	bb, n := box(img, img.Bounds(), p.a)
	if n == 0 {
		return image.Point{}, false
	}
	return bb.Min, true
}

// mismatches compares the picture's pixels with img at origin o, within
// cols x rows of the picture (to the image's edge): it returns how many
// differ and the first of them.
func mismatches(img image.Image, p picture, o image.Point, w, h int) (int, string) {
	n, first := 0, ""
	for y := 0; y < min(h, p.h); y++ {
		for x := 0; x < min(w, p.w); x++ {
			if got, want := at(img, o.X+x, o.Y+y), p.at(x, y); got != want {
				if n == 0 {
					first = fmt.Sprintf("picture pixel %d,%d at %d,%d is %v, want %v", x, y, o.X+x, o.Y+y, got, want)
				}
				n++
			}
		}
	}
	return n, first
}

// checkExact requires the whole picture drawn 1:1 at a cell origin.
func checkExact(t *testing.T, name string, img image.Image, p picture, cell [2]int) image.Point {
	t.Helper()
	o, ok := findPicture(img, p)
	if !ok {
		t.Errorf("%s: picture not found", name)
		return o
	}
	if o.X%cell[0] != 0 || o.Y%cell[1] != 0 {
		t.Errorf("%s: picture at %v, not at a cell origin (cells %dx%d)", name, o, cell[0], cell[1])
	}
	if n, first := mismatches(img, p, o, p.w, p.h); n != 0 {
		t.Errorf("%s: %d of %d pixels differ; %s", name, n, p.w*p.h, first)
	}
	// Sharp 1-px edges: the white line between two pixels of colour a,
	// and the a|b boundary between neighbouring pixels.
	if l, m, r := at(img, o.X+9, o.Y+5), at(img, o.X+10, o.Y+5), at(img, o.X+11, o.Y+5); l != p.a || m != white || r != p.a {
		t.Errorf("%s: 1-px line reads %v %v %v", name, l, m, r)
	}
	if l, r := at(img, o.X+p.w/2-1, o.Y+5), at(img, o.X+p.w/2, o.Y+5); l != p.a || r != p.b {
		t.Errorf("%s: edge reads %v | %v, want %v | %v", name, l, r, p.a, p.b)
	}
	return o
}

// TestGUIImages: SIXEL, Kitty and iTerm2 pictures drawn 1:1 with exact
// pixels and sharp edges; scrolled into the scrollback and back; drawn
// scaled after the text size changes; clipped at the pane edge and at a
// split; no frames while idle and no frame-path allocations while shown.
func TestGUIImages(t *testing.T) {
	t.Parallel()
	all := "\x1b[H\x1b[2J" + "\x1b[2;2H" + sixelPic.sixel() + "\x1b[2;12H" + kittyPic.kitty("") + "\x1b[2;22H" + itermPic.iterm() + "\x1b[8;1H"
	edge := "\x1b[H\x1b[2J\x1b[2;999H" + kittyPic.kitty(",C=1") + "\x1b[8;1H"
	// The same scroll steps twice: the first pass lets each row of the
	// view grow its storage for the image rows that land in it.
	scroll := []string{}
	for pass, t0 := range []int{3800, 4500} {
		for i := 0; i < 5; i++ {
			scroll = append(scroll, fmt.Sprintf("%dms scroll -2", t0+i*60), fmt.Sprintf("%dms scroll 2", t0+300+i*60))
		}
		if pass == 0 {
			scroll = append(scroll, "4450ms mark scroll-start")
		}
	}
	script := []string{
		"700ms type cat all.bin\\r",
		"1500ms snap shown", "1550ms dump-pane 0 shown.png",
		"1700ms mark idle-start", "2700ms mark idle-end",
		"2800ms type seq 1 80\\r",
		"3300ms snap off", "3350ms dump-pane 0 off.png",
		"3400ms scroll 1000",
		"3700ms snap back", "3750ms dump-pane 0 back.png",
	}
	script = append(script, scroll...)
	script = append(script,
		"5150ms mark scroll-end",
		"5200ms scroll-reset",
		"5250ms type clear; cat all.bin\\r",
		"5900ms mark type-start",
		"5950ms typing start 25ms",
		"6800ms typing stop",
		"6850ms mark type-end",
		"6900ms key ctrl+u",
		"7000ms font +",
		"7800ms snap zoomed", "7850ms dump-pane 0 zoomed.png",
		"7900ms font 0",
		"8400ms type cat edge.bin\\r",
		"9100ms snap edge", "9150ms dump-pane 0 edge.png",
		"9200ms split",
		"9800ms focus left",
		"9900ms type cat edge.bin\\r",
		"10700ms snap split", "10750ms shot split.png",
		"10800ms quit",
	)
	g := runGUI(t, scenario{
		args:   []string{"-alloc-probe", "-size", "100x30"},
		files:  map[string]string{"all.bin": all, "edge.bin": edge},
		script: script,
		limit:  20e9,
	})
	shown := g.snap("shown").pane(t, 0)
	cell := shown.Cell
	t.Logf("cells %dx%d px; images %+v", cell[0], cell[1], shown.Images)
	img := loadPNG(t, g.file("shown.png"))
	pos := map[string]image.Point{}
	for _, c := range []struct {
		name string
		p    picture
		col  int
	}{{"SIXEL", sixelPic, 1}, {"Kitty", kittyPic, 11}, {"iTerm2", itermPic, 21}} {
		o := checkExact(t, c.name, img, c.p, cell)
		if o != (image.Point{c.col * cell[0], cell[1]}) {
			t.Errorf("%s at %v, want cell %d,1", c.name, o, c.col)
		}
		pos[c.name] = o
	}
	if im := shown.Images; im.Stored != 3 || im.Textures != 3 || im.TextureBytes != 3*60*60*4 || im.HalfBlocks != 0 {
		t.Errorf("three pictures shown: %+v", im)
	}

	idle := g.between("idle-start", "idle-end")
	if idle.Frames != 0 || idle.ClockFrames != 0 {
		t.Errorf("idle with images: %d frames, %d frame-clock frames", idle.Frames, idle.ClockFrames)
	}
	off := loadPNG(t, g.file("off.png"))
	if _, ok := findPicture(off, kittyPic); ok {
		t.Error("the pictures are still on screen after 80 lines")
	}
	back := loadPNG(t, g.file("back.png"))
	if s := g.snap("back").pane(t, 0); s.Scroll == 0 {
		t.Errorf("not scrolled back: %+v", s.Scroll)
	}
	for name, p := range map[string]picture{"SIXEL": sixelPic, "Kitty": kittyPic, "iTerm2": itermPic} {
		if o := checkExact(t, name+" in the scrollback", back, p, cell); o != pos[name] {
			t.Errorf("%s in the scrollback at %v, want %v", name, o, pos[name])
		}
	}
	sc := g.between("scroll-start", "scroll-end")
	ty := g.between("type-start", "type-end")
	t.Logf("scrolling with images: %d frames, %d objects allocated; typing below images: %d frames, %d objects", sc.Frames, sc.RenderMallocs, ty.Frames, ty.RenderMallocs)
	if sc.Frames < 8 || sc.RenderMallocs != 0 {
		t.Errorf("scrolling with images: %d frames allocated %d objects; want 8+ frames, 0 objects", sc.Frames, sc.RenderMallocs)
	}
	if ty.Frames < 10 || ty.RenderMallocs != 0 {
		t.Errorf("typing below images: %d frames allocated %d objects; want 10+ frames, 0 objects", ty.Frames, ty.RenderMallocs)
	}

	// A larger text size: the pictures keep their placement and are scaled.
	zs := g.snap("zoomed").pane(t, 0)
	zoomed := loadPNG(t, g.file("zoomed.png"))
	if zs.Cell == cell {
		t.Fatalf("text size + kept %v cells", cell)
	}
	bb, n := box(zoomed, zoomed.Bounds(), kittyPic.a)
	wantW := 30 * zs.Cell[0] / cell[0]
	t.Logf("zoomed cells %v: Kitty colour a covers %v (%d px), want about %d px wide", zs.Cell, bb, n, wantW)
	if n == 0 || abs(bb.Dx()-wantW) > 2 || bb.Min.X%zs.Cell[0] != 0 {
		t.Errorf("scaled picture: colour a box %v, want %d px wide at a cell edge", bb, wantW)
	}

	// Clipped at the pane's last column: one cell of the picture shows.
	es := g.snap("edge").pane(t, 0)
	edgeImg := loadPNG(t, g.file("edge.png"))
	ebb, en := box(edgeImg, edgeImg.Bounds(), kittyPic.a)
	lastX := (es.Cols - 1) * cell[0]
	if en == 0 || ebb.Min.X != lastX || ebb.Max.X != lastX+cell[0] || ebb.Min.Y != cell[1] {
		t.Errorf("clipped picture: colour a box %v, want x %d-%d from row 1 (%d cols)", ebb, lastX, lastX+cell[0], es.Cols)
	}
	if n, first := mismatches(edgeImg, kittyPic, image.Point{lastX, cell[1]}, cell[0], kittyPic.h); n != 0 {
		t.Errorf("clipped picture: %d pixels differ; %s", n, first)
	}

	// In a split, the left pane clips at its own edge: nothing reaches the
	// right pane.
	sp := g.snap("split")
	left, right := sp.pane(t, 0), sp.pane(t, 1)
	shot := loadPNG(t, g.file("split.png"))
	lt, rt := left.Texture, right.Texture
	lbb, ln := box(shot, image.Rect(lt[0], lt[1], lt[0]+lt[2], lt[1]+lt[3]), kittyPic.a)
	_, rn := box(shot, image.Rect(rt[0], rt[1], rt[0]+rt[2], rt[1]+rt[3]), kittyPic.a)
	edgeX := lt[0] + left.Cols*cell[0]
	t.Logf("split: left texture %v (%d cols), right %v; colour a in left %v (%d px), right %d px", lt, left.Cols, rt, lbb, ln, rn)
	if ln == 0 || lbb.Max.X != edgeX || lbb.Dx() != cell[0] || rn != 0 {
		t.Errorf("split clipping: left box %v (edge at %d), %d px in the right pane", lbb, edgeX, rn)
	}
}

// TestGUIImageReflow: after the window grows, a picture in a soft-wrapped
// line moves with its line, and every cell draws its own part of it.
func TestGUIImageReflow(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		args:  []string{"-size", "40x12"},
		files: map[string]string{"reflow.bin": "\x1b[H\x1b[2J" + strings.Repeat("x", 50) + kittyPic.kitty(",C=1") + "\x1b[6;1H"},
		script: []string{
			"700ms type cat reflow.bin\\r",
			"1500ms snap small", "1550ms dump-pane 0 small.png",
			"1600ms maximize",
			"2800ms snap big", "2850ms dump-pane 0 big.png",
			"2900ms quit",
		},
	})
	small, big := g.snap("small").pane(t, 0), g.snap("big").pane(t, 0)
	cell := small.Cell
	cols0, cols1 := small.Cols, big.Cols
	if cols0 >= 50 || cols0 <= 25 || cols1 < 60 {
		t.Fatalf("grid %d then %d cols: want 26-49, then 60+", cols0, cols1)
	}
	// Cell (c, r) of the picture lies at grid cell at(c, r); compare its part.
	part := func(name, file string, gridCell func(c, r int) (int, int)) {
		img := loadPNG(t, g.file(file))
		for r := 0; r*cell[1] < kittyPic.h; r++ {
			for c := 0; c*cell[0] < kittyPic.w; c++ {
				gx, gy := gridCell(c, r)
				for y := r * cell[1]; y < min(kittyPic.h, (r+1)*cell[1]); y++ {
					for x := c * cell[0]; x < min(kittyPic.w, (c+1)*cell[0]); x++ {
						px, py := gx*cell[0]+x-c*cell[0], gy*cell[1]+y-r*cell[1]
						if got, want := at(img, px, py), kittyPic.at(x, y); got != want {
							t.Errorf("%s: picture cell %d,%d at grid %d,%d: pixel %d,%d is %v, want %v", name, c, r, gx, gy, x, y, got, want)
							return
						}
					}
				}
			}
		}
	}
	x0 := 50 % cols0
	part("before", "small.png", func(c, r int) (int, int) { return x0 + c, 1 + r })
	// The picture's first row joined the line of x's; the others stayed.
	part("after", "big.png", func(c, r int) (int, int) {
		if r == 0 {
			return 50 + c, 0
		}
		return x0 + c, r
	})
}

// TestGUIImageEviction: with room for one bitmap, the second picture
// evicts the first, whose texture is freed and whose cells draw as half
// blocks; clear frees the rest.
func TestGUIImageEviction(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		args: []string{"-size", "100x30", "-image-bytes", "20000"}, // one 60x60 bitmap is 14400 bytes
		files: map[string]string{
			"a.bin": "\x1b[H\x1b[2J\x1b[2;2H" + sixelPic.kitty(",C=1") + "\x1b[8;1H",
			"b.bin": "\x1b[2;20H" + kittyPic.kitty(",C=1") + "\x1b[10;1H",
		},
		script: []string{
			"700ms type cat a.bin\\r",
			"1400ms snap a", "1450ms dump-pane 0 a.png",
			"1500ms type cat b.bin\\r",
			"2200ms snap b", "2250ms dump-pane 0 b.png",
			"2300ms type clear\\r",
			"2900ms snap cleared",
			"3000ms quit",
		},
	})
	a := g.snap("a").pane(t, 0)
	cell := a.Cell
	if im := a.Images; im.Stored != 1 || im.Textures != 1 || im.TextureBytes != 14400 || im.HalfBlocks != 0 {
		t.Errorf("first picture: %+v", im)
	}
	o := checkExact(t, "first", loadPNG(t, g.file("a.png")), sixelPic, cell)
	b := g.snap("b").pane(t, 0)
	t.Logf("after the second picture: %+v", b.Images)
	if im := b.Images; im.Stored != 1 || im.Evicted != 1 || im.Textures != 1 || im.TextureBytes != 14400 || im.Freed < 1 || im.HalfBlocks == 0 {
		t.Errorf("second picture: %+v; want the first evicted, its texture freed, its cells half blocks", im)
	}
	img := loadPNG(t, g.file("b.png"))
	checkExact(t, "second", img, kittyPic, cell)
	if n, _ := mismatches(img, sixelPic, o, sixelPic.w, sixelPic.h); n == 0 {
		t.Error("the evicted picture still shows every pixel")
	}
	// Each cell of the evicted picture: one colour in its top half, one in
	// its bottom half; the cell across the a|b edge blends both.
	for r := 0; r*cell[1] < sixelPic.h; r++ {
		for c := 0; c*cell[0] < sixelPic.w; c++ {
			x0, y0 := o.X+c*cell[0], o.Y+r*cell[1]
			top, bottom := at(img, x0, y0), at(img, x0, y0+cell[1]-1)
			for y := 0; y < cell[1]; y++ {
				want := top
				if y >= cell[1]/2 {
					want = bottom
				}
				for x := 0; x < cell[0]; x++ {
					if got := at(img, x0+x, y0+y); got != want {
						t.Fatalf("half block %d,%d: pixel %d,%d is %v, want %v", c, r, x, y, got, want)
					}
				}
			}
		}
	}
	if c := (sixelPic.w/2 - 1) / cell[0]; c == sixelPic.w/2/cell[0] {
		if mid := at(img, o.X+c*cell[0], o.Y); mid == sixelPic.a || mid == sixelPic.b {
			t.Errorf("the half block across the edge is %v, want a blend", mid)
		}
	}
	if im := g.snap("cleared").pane(t, 0).Images; im.Stored != 0 || im.Textures != 0 || im.TextureBytes != 0 || im.Swept < 1 {
		t.Errorf("after clear: %+v; want nothing stored or uploaded", im)
	}
}

// TestGUIImageTextures: textures of pictures not on screen are freed when
// the pane holds more than its GPU budget, and uploaded again when shown.
func TestGUIImageTextures(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		args: []string{"-size", "100x30", "-image-gpu-bytes", "20000"}, // room for one 14400-byte texture
		files: map[string]string{
			"c.bin": "\x1b[H\x1b[2J\x1b[2;2H" + sixelPic.kitty(",C=1") + "\x1b[8;1H",
			"d.bin": "\x1b[2;30H" + kittyPic.kitty(",C=1") + "\x1b[8;1H",
		},
		script: []string{
			"700ms type cat c.bin\\r",
			"1300ms snap c",
			"1400ms type seq 1 80; cat d.bin\\r", // the scrollback outgrows the screen
			"2100ms snap d",
			"2200ms scroll 1000",
			"2600ms snap back", "2650ms dump-pane 0 back.png",
			"2700ms scroll-reset",
			"3100ms snap live",
			"3200ms quit",
		},
	})
	c, d, back, live := g.snap("c").pane(t, 0).Images, g.snap("d").pane(t, 0).Images, g.snap("back").pane(t, 0).Images, g.snap("live").pane(t, 0).Images
	t.Logf("c %+v\nd %+v\nback %+v\nlive %+v", c, d, back, live)
	if c.Textures != 1 || c.Uploads != 1 {
		t.Errorf("first picture: %+v", c)
	}
	if d.Stored != 2 || d.Textures != 1 || d.Uploads != 2 || d.Freed != 1 {
		t.Errorf("second picture, first scrolled away: %+v; want both stored, one texture", d)
	}
	if back.Textures != 1 || back.Uploads != 3 || back.Freed != 2 {
		t.Errorf("scrolled back to the first: %+v; want it uploaded again, the second freed", back)
	}
	checkExact(t, "uploaded again", loadPNG(t, g.file("back.png")), sixelPic, g.snap("back").pane(t, 0).Cell)
	if live.Textures != 1 || live.Uploads != 4 || live.TextureBytes != 14400 {
		t.Errorf("back to the live view: %+v", live)
	}
}

// TestGUILinks: Ctrl+hover underlines OSC 8 links and plain URLs and shows
// the hand pointer; Ctrl+click opens them. An application that reports the
// mouse gets plain and Ctrl clicks; Ctrl+Shift+click opens there.
func TestGUILinks(t *testing.T) {
	t.Parallel()
	links := "\x1b[H\x1b[2Josc8: \x1b]8;;https://osc8.example/doc\x1b\\the docs\x1b]8;;\x1b\\ end\r\nplain: see https://plain.example/a-(b) now\r\n\r\n"
	mouse := "printf '\\033[?1000h\\033[?1006happ: https://app.example/x\\n'; stty raw -echo; timeout --foreground 5 cat > clicks.bin; stty sane; printf '\\033[?1000l\\033[?1006l'\n"
	g := runGUI(t, scenario{
		args:  []string{"-size", "100x30"},
		files: map[string]string{"links.txt": links, "mouse.sh": mouse},
		script: []string{
			"300ms maximize",
			"1000ms type cat links.txt\\r",
			"1600ms ptr-at cell 0 8 0",
			"1700ms snap no-ctrl",
			"1800ms kbd-down ctrl",
			"2000ms snap hover-osc8", "2050ms dump-pane 0 hover-osc8.png",
			"2100ms ptr-click",
			"2300ms ptr-at cell 0 15 1",
			"2500ms snap hover-plain", "2550ms dump-pane 0 hover-plain.png",
			"2600ms ptr-click",
			"2700ms kbd-up ctrl",
			"2900ms snap released", "2950ms dump-pane 0 released.png",
			"3000ms type sh mouse.sh\\r",
			"3700ms ptr-at cell 0 2 4",
			"3800ms ptr-click",
			"4000ms kbd-down ctrl",
			"4100ms ptr-at cell 0 10 4",
			"4200ms ptr-click",
			"4300ms kbd-down shift",
			"4400ms ptr-click",
			"4500ms kbd-up shift",
			"4600ms kbd-up ctrl",
			"9500ms snap end",
			"9600ms quit",
		},
		limit: 20e9,
	})
	if p := g.snap("no-ctrl").pane(t, 0); p.Hover != "" || p.Cursor != "text" {
		t.Errorf("without Ctrl: hover %q, cursor %q", p.Hover, p.Cursor)
	}
	ho := g.snap("hover-osc8").pane(t, 0)
	if ho.Hover != "https://osc8.example/doc" || ho.Cursor != "pointer" {
		t.Errorf("Ctrl over the OSC 8 link: hover %q, cursor %q", ho.Hover, ho.Cursor)
	}
	if hp := g.snap("hover-plain").pane(t, 0); hp.Hover != "https://plain.example/a-(b)" || hp.Cursor != "pointer" {
		t.Errorf("Ctrl over the plain URL: hover %q, cursor %q", hp.Hover, hp.Cursor)
	}
	if r := g.snap("released").pane(t, 0); r.Hover != "" || r.Cursor != "text" {
		t.Errorf("Ctrl released: hover %q, cursor %q", r.Hover, r.Cursor)
	}
	// The underline: hovered cells differ from the released frame, every
	// other cell of the row is unchanged.
	cell := ho.Cell
	released := loadPNG(t, g.file("released.png"))
	underlined := func(name string, row, x0, x1 int) {
		img := loadPNG(t, g.file(name))
		for col := 0; col < 45; col++ {
			diff := 0
			for y := row * cell[1]; y < (row+1)*cell[1]; y++ {
				for x := col * cell[0]; x < (col+1)*cell[0]; x++ {
					if at(img, x, y) != at(released, x, y) {
						diff++
					}
				}
			}
			if in := col >= x0 && col <= x1; in != (diff > 0) {
				t.Errorf("%s: cell %d of row %d has %d changed pixels (in the link: %v)", name, col, row, diff, in)
			}
		}
	}
	underlined("hover-osc8.png", 0, 6, 13)
	underlined("hover-plain.png", 1, 11, 37)

	want := []string{"https://osc8.example/doc", "https://plain.example/a-(b)", "https://app.example/x"}
	if !slices.Equal(g.Opened, want) {
		t.Errorf("opened %q, want %q", g.Opened, want)
	}
	clicks, err := os.ReadFile(g.file("clicks.bin"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the application read %q", clicks)
	for _, seq := range []string{"\x1b[<0;3;5M", "\x1b[<0;3;5m", "\x1b[<16;11;5M"} {
		if !strings.Contains(string(clicks), seq) {
			t.Errorf("the application did not get %q", seq)
		}
	}
	if strings.Contains(string(clicks), "\x1b[<20;") || strings.Contains(string(clicks), "\x1b[<4;") {
		t.Error("the Ctrl+Shift click that opened the link reached the application")
	}
}
