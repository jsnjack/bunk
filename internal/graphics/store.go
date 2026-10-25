package graphics

import "image"

// DefaultStoreBytes is a front end's usual Store limit.
const DefaultStoreBytes = 64 << 20

// sweepEvery sweeps unreferenced placements after this many additions, so
// images that were erased or left the scrollback do not wait for the limit.
const sweepEvery = 64

// Stored is the bitmap of one placement and where it was drawn. Cell (col,
// row) of the placement covers pixels [col*CellWidth, (col+1)*CellWidth) x
// [row*CellHeight, (row+1)*CellHeight) of the placement; the image fills
// the rectangle at (OffsetX, OffsetY) of PixelWidth x PixelHeight pixels,
// scaled from Source of Image. Every field is immutable, so a front end may
// keep a copy and draw from it without the session lock.
type Stored struct {
	ID                      uint64
	Image                   *image.NRGBA
	Source                  image.Rectangle
	PixelWidth, PixelHeight int
	OffsetX, OffsetY        int
	CellWidth, CellHeight   int
	Cols, Rows              int
}

type storeEntry struct {
	Stored
	prev, next *storeEntry // least recently used order; the list head is a sentinel
}

// Store keeps the decoded bitmaps of placements so a front end can draw
// image cells at full resolution. Cells refer to a placement by ID; the
// store holds the pixels. Bytes count each distinct bitmap once and stay
// at or below the limit: an addition first drops placements no cell refers
// to (Referenced), then the least recently used ones. A cell whose bitmap
// is gone keeps its two colour samples. The store is not safe for
// concurrent use; a session guards it with its lock.
type Store struct {
	limit   int
	bytes   int
	nextID  uint64
	entries map[uint64]*storeEntry
	shared  map[*image.NRGBA]int // placements per bitmap
	lru     storeEntry           // sentinel: lru.next is the most recently used
	added   int                  // additions since the last sweep

	// Referenced, when set, calls mark with the ID of every placement a
	// cell still refers to. Sweep keeps only those.
	Referenced func(mark func(id uint64))

	// Evicted and Swept count placements dropped for room and because no
	// cell referred to them.
	Evicted, Swept int
}

// NewStore returns a store that holds at most limit bytes of pixels.
func NewStore(limit int) *Store {
	s := &Store{limit: max(0, limit), entries: map[uint64]*storeEntry{}, shared: map[*image.NRGBA]int{}}
	s.lru.prev, s.lru.next = &s.lru, &s.lru
	return s
}

func imageBytes(img *image.NRGBA) int { return len(img.Pix) }

// Add stores p's bitmap as placed with cells of cellW x cellH pixels and
// returns its ID, or 0 when the bitmap alone exceeds the limit.
func (s *Store) Add(p *Placement, cellW, cellH int) uint64 {
	if s == nil || p == nil || p.Image == nil {
		return 0
	}
	n := imageBytes(p.Image)
	if s.shared[p.Image] > 0 {
		n = 0 // another placement holds the same pixels
	}
	if imageBytes(p.Image) > s.limit {
		return 0
	}
	s.added++
	if s.added >= sweepEvery || s.bytes+n > s.limit {
		s.Sweep()
		if s.shared[p.Image] == 0 {
			n = imageBytes(p.Image)
		}
	}
	for s.bytes+n > s.limit && s.lru.prev != &s.lru {
		s.remove(s.lru.prev)
		s.Evicted++
		if s.shared[p.Image] == 0 {
			n = imageBytes(p.Image)
		}
	}
	s.nextID++
	e := &storeEntry{Stored: Stored{
		ID: s.nextID, Image: p.Image, Source: p.Source,
		PixelWidth: p.PixelWidth, PixelHeight: p.PixelHeight, OffsetX: p.OffsetX, OffsetY: p.OffsetY,
		CellWidth: cellW, CellHeight: cellH, Cols: p.Cols, Rows: p.Rows,
	}}
	s.entries[e.ID] = e
	s.shared[p.Image]++
	s.bytes += n
	s.pushFront(e)
	return e.ID
}

// Get returns a placement's bitmap and marks it used.
func (s *Store) Get(id uint64) (Stored, bool) {
	if s == nil {
		return Stored{}, false
	}
	e, ok := s.entries[id]
	if !ok {
		return Stored{}, false
	}
	s.unlink(e)
	s.pushFront(e)
	return e.Stored, true
}

// Touch marks a placement used and reports whether the store has it.
func (s *Store) Touch(id uint64) bool {
	_, ok := s.Get(id)
	return ok
}

// Has reports whether the store holds a placement, without marking it used.
func (s *Store) Has(id uint64) bool {
	if s == nil {
		return false
	}
	_, ok := s.entries[id]
	return ok
}

// Bytes returns the pixel bytes held.
func (s *Store) Bytes() int {
	if s == nil {
		return 0
	}
	return s.bytes
}

// Len returns the number of placements held.
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	return len(s.entries)
}

// Limit returns the byte limit.
func (s *Store) Limit() int {
	if s == nil {
		return 0
	}
	return s.limit
}

// Sweep drops every placement Referenced does not mark. Without Referenced
// it keeps everything.
func (s *Store) Sweep() {
	if s == nil {
		return
	}
	s.added = 0
	if s.Referenced == nil || len(s.entries) == 0 {
		return
	}
	keep := make(map[uint64]bool, len(s.entries))
	var last uint64
	s.Referenced(func(id uint64) {
		// Neighbouring cells usually share a placement.
		if id != last {
			keep[id], last = true, id
		}
	})
	for e := s.lru.next; e != &s.lru; {
		next := e.next
		if !keep[e.ID] {
			s.remove(e)
			s.Swept++
		}
		e = next
	}
}

func (s *Store) remove(e *storeEntry) {
	s.unlink(e)
	delete(s.entries, e.ID)
	if s.shared[e.Image]--; s.shared[e.Image] <= 0 {
		delete(s.shared, e.Image)
		s.bytes -= imageBytes(e.Image)
	}
}

func (s *Store) unlink(e *storeEntry) {
	e.prev.next, e.next.prev = e.next, e.prev
	e.prev, e.next = nil, nil
}

func (s *Store) pushFront(e *storeEntry) {
	e.prev, e.next = &s.lru, s.lru.next
	s.lru.next.prev = e
	s.lru.next = e
}
