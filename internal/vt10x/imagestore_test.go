package vt10x

import (
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"testing"

	"bunk/internal/graphics"
)

// kittyRGBA returns a Kitty transmit-and-display command for a w x h
// bitmap whose pixel (x, y) is px(x, y), with extra keys such as "i=1".
func kittyRGBA(w, h int, extra string, px func(x, y int) color.NRGBA) string {
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

// quadrants colours the four quadrants of a w x h bitmap.
func quadrants(w, h int) func(x, y int) color.NRGBA {
	cols := [4]color.NRGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}, {255, 255, 0, 255}}
	return func(x, y int) color.NRGBA {
		i := 0
		if x >= w/2 {
			i++
		}
		if y >= h/2 {
			i += 2
		}
		return cols[i]
	}
}

// storedSamples recomputes a cell's two samples from its stored bitmap, as
// paintGraphics computed them at placement time.
func storedSamples(st graphics.Stored, col, row int) (top, bottom color.NRGBA) {
	p := &graphics.Placement{Image: st.Image, Source: st.Source, PixelWidth: st.PixelWidth, PixelHeight: st.PixelHeight,
		OffsetX: st.OffsetX, OffsetY: st.OffsetY}
	px, py := col*st.CellWidth, row*st.CellHeight
	top = p.Sample(image.Rect(px, py, px+st.CellWidth, py+st.CellHeight/2))
	bottom = p.Sample(image.Rect(px, py+st.CellHeight/2, px+st.CellWidth, py+st.CellHeight))
	return top, bottom
}

func newImageTerm(cols, rows int, opts ...TerminalOption) (Terminal, *graphics.Store) {
	store := graphics.NewStore(1 << 20)
	term := New(append([]TerminalOption{WithSize(cols, rows), WithCellPixels(4, 6), WithImageStore(store)}, opts...)...)
	store.Referenced = term.MarkImages
	return term, store
}

// TestImageCellsReferenceStoredBitmap: each image cell names its placement's
// stored bitmap and its own column and row in it, and its samples match
// the sub-rectangle they name.
func TestImageCellsReferenceStoredBitmap(t *testing.T) {
	term, store := newImageTerm(10, 5)
	writeGraphics(t, term, "\x1b[2;3H"+kittyRGBA(8, 12, "", quadrants(8, 12)))
	var id uint64
	for row := 0; row < 2; row++ {
		for col := 0; col < 2; col++ {
			g := term.Cell(2+col, 1+row)
			if g.Image == nil || g.Image.Placement.Bitmap == 0 {
				t.Fatalf("cell %d,%d: %+v", col, row, g.Image)
			}
			if id == 0 {
				id = g.Image.Placement.Bitmap
			}
			if g.Image.Placement.Bitmap != id || int(g.Image.Col) != col || int(g.Image.Row) != row || g.Image.Composite {
				t.Fatalf("cell %d,%d refers to %d at %d,%d (composite %v)", col, row, g.Image.Placement.Bitmap, g.Image.Col, g.Image.Row, g.Image.Composite)
			}
			st, ok := store.Get(id)
			if !ok {
				t.Fatal("bitmap not stored")
			}
			if top, bottom := storedSamples(st, col, row); top != g.Image.Top || bottom != g.Image.Bottom {
				t.Fatalf("cell %d,%d samples %v %v, stored bitmap gives %v %v", col, row, g.Image.Top, g.Image.Bottom, top, bottom)
			}
		}
	}
	st, _ := store.Get(id)
	if st.CellWidth != 4 || st.CellHeight != 6 || st.Cols != 2 || st.Rows != 2 || st.PixelWidth != 8 || st.PixelHeight != 12 {
		t.Fatalf("stored geometry %+v", st)
	}
	if store.Bytes() != 8*12*4 {
		t.Fatalf("store holds %d bytes", store.Bytes())
	}
	// The cell size changes later (a front end's font); placed cells keep theirs.
	term.SetCellPixels(12, 27)
	if w, h := term.CellPixels(); w != 12 || h != 27 {
		t.Fatalf("CellPixels after SetCellPixels: %dx%d", w, h)
	}
	if st2, _ := store.Get(id); st2.CellWidth != 4 {
		t.Fatalf("stored cell width changed to %d", st2.CellWidth)
	}
}

// TestImageCellsKeepOffsetsWhenScrolling: a placement taller than the
// screen scrolls while it is drawn; every row keeps its own offset.
func TestImageCellsKeepOffsetsWhenScrolling(t *testing.T) {
	var history []Row
	term, _ := newImageTerm(4, 2, WithScrollCallback(func(r Row) {
		var c Row
		c.CopyFrom(r, r.Len())
		history = append(history, c)
	}))
	writeGraphics(t, term, kittyRGBA(4, 18, "", quadrants(4, 18))) // 1 column, 3 rows
	var rows []int
	for _, r := range history {
		if img := r.Image(0); img != nil {
			rows = append(rows, int(img.Row))
		}
	}
	for y := 0; y < 2; y++ {
		if img := term.Cell(0, y).Image; img != nil {
			rows = append(rows, int(img.Row))
		}
	}
	if fmt.Sprint(rows) != "[0 1 2]" {
		t.Fatalf("image rows from top to bottom: %v, want [0 1 2]", rows)
	}
}

// TestImageStoreSweptOnDeleteAndClear: Kitty deletion, ED 3 and RIS drop
// bitmaps no cell refers to; bitmaps still shown stay.
func TestImageStoreSweptOnDeleteAndClear(t *testing.T) {
	term, store := newImageTerm(20, 6)
	red := func(int, int) color.NRGBA { return color.NRGBA{255, 0, 0, 255} }
	writeGraphics(t, term, "\x1b[1;1H"+kittyRGBA(4, 6, "i=1,C=1", red))
	writeGraphics(t, term, "\x1b[1;5H"+kittyRGBA(4, 6, "i=2,C=1", red))
	if store.Len() != 2 {
		t.Fatalf("two placements: %d stored", store.Len())
	}
	first := term.Cell(0, 0).Image.Placement.Bitmap
	writeGraphics(t, term, "\x1b_Ga=d,d=i,i=1\x1b\\")
	if store.Has(first) || store.Len() != 1 {
		t.Fatalf("after deleting image 1: has it %v, %d stored", store.Has(first), store.Len())
	}
	writeGraphics(t, term, "\x1b[3J") // scrollback only: the screen still shows image 2
	if store.Len() != 1 {
		t.Fatalf("ED 3 dropped an image on screen: %d stored", store.Len())
	}
	writeGraphics(t, term, "\x1b[2J\x1b[3J")
	if store.Len() != 0 || store.Bytes() != 0 {
		t.Fatalf("after clear: %d stored, %d bytes", store.Len(), store.Bytes())
	}
	writeGraphics(t, term, kittyRGBA(4, 6, "", red)+"\x1bc")
	if store.Len() != 0 {
		t.Fatalf("after RIS: %d stored", store.Len())
	}
}

// TestCompositeImageCells: an opaque image replaces the one beneath and
// refers to its own bitmap; a translucent one blends into the samples and
// marks the cells Composite.
func TestCompositeImageCells(t *testing.T) {
	term, _ := newImageTerm(10, 4)
	red := func(int, int) color.NRGBA { return color.NRGBA{255, 0, 0, 255} }
	blue := func(int, int) color.NRGBA { return color.NRGBA{0, 0, 255, 255} }
	glass := func(int, int) color.NRGBA { return color.NRGBA{0, 255, 0, 128} }
	writeGraphics(t, term, "\x1b[H"+kittyRGBA(4, 6, "C=1", red))
	writeGraphics(t, term, "\x1b[H"+kittyRGBA(4, 6, "C=1", blue))
	g := term.Cell(0, 0)
	if g.Image.Composite || g.Image.Top != (color.NRGBA{0, 0, 255, 255}) {
		t.Fatalf("opaque over opaque: %+v", g.Image)
	}
	writeGraphics(t, term, "\x1b[H"+kittyRGBA(4, 6, "C=1", glass))
	g = term.Cell(0, 0)
	if !g.Image.Composite || g.Image.Top.B == 0 || g.Image.Top.G == 0 || g.Image.Top.A != 255 {
		t.Fatalf("translucent over blue: %+v", g.Image)
	}
}

// TestImagesWithoutStoreKeepSamples: bunk's TUI keeps no bitmaps.
func TestImagesWithoutStoreKeepSamples(t *testing.T) {
	term := New(WithSize(10, 4), WithCellPixels(4, 6))
	writeGraphics(t, term, kittyRGBA(8, 12, "", quadrants(8, 12)))
	g := term.Cell(1, 1)
	if g.Image == nil || g.Image.Placement.Bitmap != 0 || g.Image.Col != 1 || g.Image.Row != 1 {
		t.Fatalf("cell 1,1: %+v", g.Image)
	}
	if g.Image.Top != (color.NRGBA{255, 255, 0, 255}) {
		t.Fatalf("bottom-right quadrant sample %v", g.Image.Top)
	}
}
