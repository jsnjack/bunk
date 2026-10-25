package session

import (
	"bunk/internal/graphics"
	"bunk/internal/vt10x"
)

// imageStore creates the session's image store when limit > 0 and returns
// the emulator option that fills it.
func (s *Session) imageStore(limit int) vt10x.TerminalOption {
	if limit > 0 {
		s.images = graphics.NewStore(limit)
		s.images.Referenced = s.markImages
	}
	return vt10x.WithImageStore(s.images)
}

// EnableImages keeps image bitmaps up to limit bytes from now on, for a
// session built without Start (tests). Must be called before any output.
func (s *Session) EnableImages(limit int) {
	s.images = graphics.NewStore(limit)
	s.images.Referenced = s.markImages
	if t, ok := s.Term.(interface{ SetImageStore(*graphics.Store) }); ok {
		t.SetImageStore(s.images)
	}
}

// Images returns the store that keeps the bitmaps of image placements, or
// nil when the session keeps only cell samples (Options.ImageBytes). Cells
// name their placement's bitmap with ImageCell.Placement.Bitmap. Use it
// with the session locked.
func (s *Session) Images() *graphics.Store { return s.images }

// markImages reports every placement the screens and the scrollback refer
// to. It runs with the session locked, also from inside Term.Write.
func (s *Session) markImages(mark func(id uint64)) {
	s.Term.MarkImages(mark)
	s.Scrollback.markImages(mark)
}

// SweepImages drops stored bitmaps that no cell refers to any more. Must
// be called with the session locked.
func (s *Session) SweepImages() {
	if s.images != nil {
		s.images.Sweep()
	}
}

// markImages reports the placements the stored rows refer to.
func (r *Ring) markImages(mark func(id uint64)) {
	for i := 0; i < r.Count; i++ {
		r.Row(i).MarkImages(mark)
	}
}
