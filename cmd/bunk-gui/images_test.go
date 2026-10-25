package main

import (
	"encoding/base64"
	"fmt"
	"image/color"
	"testing"
)

// kittyImage returns a Kitty transmit-and-display command for a w x h
// bitmap of px(x, y), with extra keys such as "C=1".
func kittyImage(w, h int, extra string, px func(x, y int) color.NRGBA) string {
	data := make([]byte, 0, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := px(x, y)
			data = append(data, c.R, c.G, c.B, c.A)
		}
	}
	if extra != "" {
		extra = "," + extra
	}
	return fmt.Sprintf("\x1b_Ga=T,f=32,s=%d,v=%d%s;%s\x1b\\", w, h, extra, base64.StdEncoding.EncodeToString(data))
}

func solid(c color.NRGBA) func(int, int) color.NRGBA { return func(int, int) color.NRGBA { return c } }

var (
	red  = color.NRGBA{255, 0, 0, 255}
	blue = color.NRGBA{0, 0, 255, 255}
)

// newImageFixture is newFixture with an image store of limit bytes; the
// emulator places images with 8x16 virtual cells.
func newImageFixture(cols, rows, limit int) *frameFixture {
	f := newFixture(cols, rows)
	f.s.EnableImages(limit)
	return f
}

// imgFrame runs a frame as Pane.render does: pull and sync under the lock,
// then convert. It reports the rows converted.
func (f *frameFixture) imgFrame() int {
	f.s.Lock()
	ok := f.tv.pull(f.s)
	if f.r.img.sync(f.s.Images(), f.tv) {
		f.tv.invalidate()
		ok = true
	}
	f.s.Unlock()
	f.r.img.collect()
	if !ok {
		return 0
	}
	return f.tv.convert(f.r)
}

func cellOf(c imgCell) (x, y, col, row int) {
	return int(c.inst.Cell & 0xffff), int(c.inst.Cell >> 16), int(c.inst.Src & 0xffff), int(c.inst.Src >> 16)
}

// TestConvertImageCells: cells of a stored placement draw no glyph and go
// to their row's image list with their grid position and their own
// column and row in the placement.
func TestConvertImageCells(t *testing.T) {
	f := newImageFixture(20, 6, 1<<20)
	f.write("\x1b[?25l\x1b[2;4H" + kittyImage(16, 32, "C=1", solid(red))) // 2x2 cells at column 3, row 1
	f.imgFrame()
	for y := 1; y <= 2; y++ {
		cells := f.r.img.rows[y]
		if len(cells) != 2 {
			t.Fatalf("row %d: %d image cells, want 2", y, len(cells))
		}
		for i, c := range cells {
			x, gy, col, row := cellOf(c)
			if x != 3+i || gy != y || col != i || row != y-1 || c.id == 0 {
				t.Errorf("row %d cell %d: grid %d,%d placement %d,%d id %d", y, i, x, gy, col, row, c.id)
			}
			if in := f.cell(x, y); in.Glyph != 0 || in.BG != f.tv.defBG {
				t.Errorf("image cell %d,%d instance %+v: want no glyph over the background", x, y, in)
			}
		}
	}
	if !f.r.img.dirty {
		t.Error("image lists changed but the layer is not dirty")
	}
	f.r.img.dirty = false
	if rows := f.imgFrame(); rows != 0 || f.r.img.dirty {
		t.Errorf("idle frame: %d rows rebuilt, dirty %v", rows, f.r.img.dirty)
	}
	if e := f.r.img.cache[f.r.img.rows[1][0].id]; e == nil || e.st.Cols != 2 || e.st.Rows != 2 || e.st.CellWidth != 8 {
		t.Errorf("cached placement %+v", e)
	}
}

// TestImageFallbackWhenEvicted: a placement the store dropped draws its
// samples as half blocks, both when it was evicted before its first frame
// and when it was on screen.
func TestImageFallbackWhenEvicted(t *testing.T) {
	f := newImageFixture(20, 6, 16*32*4) // room for one 16x32 bitmap
	f.write("\x1b[?25l\x1b[H" + kittyImage(16, 32, "C=1", solid(red)))
	f.imgFrame()
	if len(f.r.img.rows[0]) != 2 {
		t.Fatalf("red image: %d cells drawn from its bitmap", len(f.r.img.rows[0]))
	}
	f.write("\x1b[1;10H" + kittyImage(16, 32, "C=1", solid(blue))) // evicts red
	f.imgFrame()
	if f.s.Images().Evicted != 1 {
		t.Fatalf("evicted %d", f.s.Images().Evicted)
	}
	want := rgba(0xff0000)
	for x := 0; x < 2; x++ {
		if in := f.cell(x, 0); in.Glyph != flagHalf || in.FG != want || in.BG != want {
			t.Errorf("evicted cell %d: %+v, want a red half block %08x", x, in, want)
		}
	}
	if n := len(f.r.img.rows[0]); n != 2 {
		t.Errorf("row 0: %d bitmap cells, want blue's 2", n)
	}
	if x, _, _, _ := cellOf(f.r.img.rows[0][0]); x != 9 {
		t.Errorf("bitmap cell at column %d, want blue's 9", x)
	}
	for id, e := range f.r.img.cache {
		if e.st.Image.Pix[2] != 255 {
			t.Errorf("cache keeps placement %d of the evicted image", id)
		}
	}
}

// TestHalfColor matches bunk's TUI blending of samples over the background.
func TestHalfColor(t *testing.T) {
	bg := rgba(0x1c1c1f)
	if got := halfColor(color.NRGBA{255, 0, 0, 255}, bg); got != rgba(0xff0000) {
		t.Errorf("opaque red: %08x", got)
	}
	if got := halfColor(color.NRGBA{}, bg); got != bg {
		t.Errorf("transparent: %08x, want the background %08x", got, bg)
	}
	if got := halfColor(color.NRGBA{255, 255, 255, 128}, rgba(0)); got != rgba(0x808080) {
		t.Errorf("half white over black: %08x", got)
	}
}

// TestImageCellsScrollIntoScrollback: scrolled into the scrollback and
// viewed there, image cells keep their placement offsets.
func TestImageCellsScrollIntoScrollback(t *testing.T) {
	f := newImageFixture(20, 4, 1<<20)
	f.write("\x1b[?25l\x1b[H" + kittyImage(8, 32, "", solid(red)) + "\r\n\r\n\r\n\r\n\r\n") // 1x2 cells
	f.imgFrame()
	for y := range f.r.img.rows {
		if len(f.r.img.rows[y]) != 0 {
			t.Fatalf("row %d still shows an image after scrolling it away", y)
		}
	}
	f.s.ScrollUp(1 << 20) // the top of the scrollback
	f.imgFrame()
	for y := 0; y < 2; y++ {
		if len(f.r.img.rows[y]) != 1 {
			t.Fatalf("scrolled back, row %d: %d image cells", y, len(f.r.img.rows[y]))
		}
		if x, gy, col, row := cellOf(f.r.img.rows[y][0]); x != 0 || gy != y || col != 0 || row != y {
			t.Errorf("row %d: grid %d,%d placement %d,%d", y, x, gy, col, row)
		}
	}
}

// TestImageFrameAllocs: with images on screen, frames that pull, sync and
// convert scrolled rows allocate nothing once the placements are known.
func TestImageFrameAllocs(t *testing.T) {
	f := newImageFixture(60, 20, 1<<22)
	f.write("\x1b[?25l")
	for i := 0; i < 4; i++ {
		f.write(kittyImage(24, 48, "", solid(red)) + fmt.Sprintf(" image %d\r\n", i))
	}
	for i := 0; i < 30; i++ {
		f.write(fmt.Sprintf("output line %d\r\n", i))
	}
	f.s.ScrollUp(1 << 20) // the images are at the top of the scrollback
	step := func(i int) {
		if i%2 == 0 {
			f.s.ScrollDown(4)
		} else {
			f.s.ScrollUp(4)
		}
		if rows := f.imgFrame(); rows == 0 {
			t.Fatal("scrolling rebuilt nothing")
		}
		f.r.img.rebuildLists()
	}
	for i := 0; i < 10; i++ { // warm up: cache entries, list capacities
		step(i)
	}
	frame := []string{"(*termView).pull", "(*termView).convert", "(*imageLayer).sync", "(*imageLayer).rebuildLists"}
	before := mallocsUnder(frame...)
	if before == 0 {
		t.Fatalf("no allocation attributed to %v so far: the names match nothing", frame)
	}
	for i := 0; i < 100; i++ {
		step(i)
	}
	if n := mallocsUnder(frame...) - before; n != 0 {
		t.Errorf("100 frames with images allocated %d objects", n)
	}
	if len(f.r.img.flat) == 0 {
		t.Error("no image cell on screen while measuring")
	}
}
