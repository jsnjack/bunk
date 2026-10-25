package graphics

import (
	"image"
	"testing"
)

// placementOf returns a placement showing a w x h bitmap 1:1.
func placementOf(img *image.NRGBA) *Placement {
	b := img.Bounds()
	return &Placement{Image: img, Source: b, PixelWidth: b.Dx(), PixelHeight: b.Dy(), Cols: 1, Rows: 1, Move: true}
}

func bitmap(w, h int) *image.NRGBA { return image.NewNRGBA(image.Rect(0, 0, w, h)) }

// checkStore verifies the store's accounting against its entries: bytes
// count each distinct bitmap once, the LRU list holds every entry once,
// and bytes stay within the limit.
func checkStore(t *testing.T, s *Store) {
	t.Helper()
	distinct := map[*image.NRGBA]int{}
	for _, e := range s.entries {
		distinct[e.Image]++
	}
	bytes := 0
	for img, n := range distinct {
		bytes += len(img.Pix)
		if s.shared[img] != n {
			t.Fatalf("bitmap shared by %d placements, counted %d", n, s.shared[img])
		}
	}
	if len(s.shared) != len(distinct) {
		t.Fatalf("%d shared counts for %d bitmaps", len(s.shared), len(distinct))
	}
	if bytes != s.Bytes() {
		t.Fatalf("Bytes() = %d, entries hold %d", s.Bytes(), bytes)
	}
	if s.Bytes() > s.Limit() {
		t.Fatalf("Bytes() = %d over the limit %d", s.Bytes(), s.Limit())
	}
	n := 0
	for e := s.lru.next; e != &s.lru; e = e.next {
		if s.entries[e.ID] != e || e.next.prev != e {
			t.Fatalf("LRU list is inconsistent at %d", e.ID)
		}
		n++
	}
	if n != len(s.entries) {
		t.Fatalf("LRU list holds %d of %d entries", n, len(s.entries))
	}
}

func TestStoreAddGet(t *testing.T) {
	s := NewStore(1 << 20)
	img := bitmap(10, 6)
	p := placementOf(img)
	p.OffsetX, p.OffsetY, p.Cols, p.Rows = 3, 2, 4, 2
	id := s.Add(p, 12, 27)
	if id == 0 {
		t.Fatal("Add returned 0")
	}
	got, ok := s.Get(id)
	want := Stored{ID: id, Image: img, Source: img.Bounds(), PixelWidth: 10, PixelHeight: 6, OffsetX: 3, OffsetY: 2,
		CellWidth: 12, CellHeight: 27, Cols: 4, Rows: 2}
	if !ok || got != want {
		t.Fatalf("Get = %+v, %v; want %+v", got, ok, want)
	}
	if s.Bytes() != 10*6*4 || s.Len() != 1 {
		t.Fatalf("Bytes %d Len %d", s.Bytes(), s.Len())
	}
	if id2 := s.Add(placementOf(bitmap(2, 2)), 8, 16); id2 <= id {
		t.Fatalf("second ID %d not after %d", id2, id)
	}
	if _, ok := s.Get(999); ok || s.Has(999) || s.Touch(999) {
		t.Fatal("unknown ID found")
	}
	checkStore(t, s)
}

// TestStoreSharedBitmapCountsOnce: Kitty places one cached bitmap many
// times; its bytes count once while any placement holds it.
func TestStoreSharedBitmapCountsOnce(t *testing.T) {
	s := NewStore(1000)
	img := bitmap(10, 10) // 400 bytes
	a := s.Add(placementOf(img), 8, 16)
	b := s.Add(placementOf(img), 8, 16)
	c := s.Add(placementOf(img), 8, 16)
	if s.Bytes() != 400 || s.Len() != 3 {
		t.Fatalf("three placements of one bitmap: %d bytes, %d entries", s.Bytes(), s.Len())
	}
	keep := map[uint64]bool{b: true}
	s.Referenced = func(mark func(uint64)) {
		for id := range keep {
			mark(id)
		}
	}
	s.Sweep()
	if s.Has(a) || !s.Has(b) || s.Has(c) || s.Bytes() != 400 {
		t.Fatalf("after sweep: a %v b %v c %v, %d bytes", s.Has(a), s.Has(b), s.Has(c), s.Bytes())
	}
	keep = nil
	s.Sweep()
	if s.Len() != 0 || s.Bytes() != 0 {
		t.Fatalf("nothing referenced: %d entries, %d bytes", s.Len(), s.Bytes())
	}
	checkStore(t, s)
}

// TestStoreLRUEviction: when nothing can be swept, the least recently used
// placement goes first; Get makes a placement recent.
func TestStoreLRUEviction(t *testing.T) {
	s := NewStore(2 * 400)
	a := s.Add(placementOf(bitmap(10, 10)), 8, 16)
	b := s.Add(placementOf(bitmap(10, 10)), 8, 16)
	s.Get(a) // a is now newer than b
	c := s.Add(placementOf(bitmap(10, 10)), 8, 16)
	if !s.Has(a) || s.Has(b) || !s.Has(c) || s.Evicted != 1 {
		t.Fatalf("after the third: a %v b %v c %v, %d evicted", s.Has(a), s.Has(b), s.Has(c), s.Evicted)
	}
	d := s.Add(placementOf(bitmap(10, 10)), 8, 16)
	if s.Has(a) || !s.Has(c) || !s.Has(d) {
		t.Fatalf("after the fourth: a %v c %v d %v", s.Has(a), s.Has(c), s.Has(d))
	}
	checkStore(t, s)
}

// TestStoreSweepsBeforeEvicting: under pressure, placements no cell refers
// to go before referenced ones, however recent.
func TestStoreSweepsBeforeEvicting(t *testing.T) {
	s := NewStore(2 * 400)
	var referenced []uint64
	s.Referenced = func(mark func(uint64)) {
		for _, id := range referenced {
			mark(id)
		}
	}
	a := s.Add(placementOf(bitmap(10, 10)), 8, 16)
	b := s.Add(placementOf(bitmap(10, 10)), 8, 16)
	referenced = []uint64{a} // b was erased; a is older but still on screen
	c := s.Add(placementOf(bitmap(10, 10)), 8, 16)
	if !s.Has(a) || s.Has(b) || !s.Has(c) || s.Swept != 1 || s.Evicted != 0 {
		t.Fatalf("a %v b %v c %v, swept %d evicted %d", s.Has(a), s.Has(b), s.Has(c), s.Swept, s.Evicted)
	}
	checkStore(t, s)
}

// TestStoreTooLarge: a bitmap over the limit is not kept; its cells use
// their samples.
func TestStoreTooLarge(t *testing.T) {
	s := NewStore(100)
	if id := s.Add(placementOf(bitmap(10, 10)), 8, 16); id != 0 {
		t.Fatalf("400-byte bitmap in a 100-byte store: ID %d", id)
	}
	if s.Len() != 0 || s.Bytes() != 0 {
		t.Fatalf("%d entries, %d bytes", s.Len(), s.Bytes())
	}
	if id := NewStore(0).Add(placementOf(bitmap(1, 1)), 8, 16); id != 0 {
		t.Fatalf("a store without room kept ID %d", id)
	}
	var nilStore *Store
	if nilStore.Add(placementOf(bitmap(1, 1)), 8, 16) != 0 || nilStore.Has(1) || nilStore.Len() != 0 || nilStore.Bytes() != 0 {
		t.Fatal("a nil store holds something")
	}
	nilStore.Sweep()
}

// TestStoreSweepsPeriodically: erased images leave even without pressure.
func TestStoreSweepsPeriodically(t *testing.T) {
	s := NewStore(1 << 30)
	var last uint64
	s.Referenced = func(mark func(uint64)) { mark(last) }
	for i := 0; i < sweepEvery; i++ {
		last = s.Add(placementOf(bitmap(1, 1)), 8, 16)
	}
	if s.Len() > 2 || s.Swept < sweepEvery-2 {
		t.Fatalf("after %d additions with one referenced: %d entries, %d swept", sweepEvery, s.Len(), s.Swept)
	}
	checkStore(t, s)
}

// FuzzStore applies random additions, lookups and sweeps and checks the
// accounting, the limit, and that IDs are never reused.
func FuzzStore(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9})
	f.Add([]byte{0, 0, 0, 200, 3, 3, 3, 9, 9, 9, 1, 1})
	f.Add([]byte{40, 7, 7, 7, 7, 130, 2, 2, 250, 251})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 || len(data) > 400 {
			return
		}
		s := NewStore(int(data[0]) * 40)
		pool := make([]*image.NRGBA, 6)
		for i := range pool {
			pool[i] = bitmap(i+1, i%3+1)
		}
		referenced := map[uint64]bool{}
		s.Referenced = func(mark func(uint64)) {
			for id := range referenced {
				mark(id)
			}
		}
		var lastID uint64
		var ids []uint64
		for _, b := range data[1:] {
			switch b % 5 {
			case 0, 1:
				img := pool[int(b/5)%len(pool)]
				id := s.Add(placementOf(img), 8, 16)
				if id != 0 {
					if id <= lastID {
						t.Fatalf("ID %d after %d", id, lastID)
					}
					lastID = id
					ids = append(ids, id)
					if b&0x80 != 0 {
						referenced[id] = true
					}
				}
			case 2:
				if len(ids) > 0 {
					id := ids[int(b/5)%len(ids)]
					got, ok := s.Get(id)
					if ok && got.ID != id {
						t.Fatalf("Get(%d) returned %d", id, got.ID)
					}
				}
			case 3:
				if len(ids) > 0 {
					delete(referenced, ids[int(b/5)%len(ids)])
				}
			case 4:
				s.Sweep()
				for e := s.lru.next; e != &s.lru; e = e.next {
					if !referenced[e.ID] {
						t.Fatalf("sweep kept unreferenced %d", e.ID)
					}
				}
			}
			checkStore(t, s)
		}
	})
}
