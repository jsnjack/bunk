package session

// MarkLinks reports the hyperlink ID of every scrollback cell that has
// one, so a link sweep keeps those IDs (vt10x.WithLinkReferences). The
// emulator calls it with the session locked, also from inside Term.Write.
func (s *Session) MarkLinks(mark func(id uint16)) {
	r := &s.Scrollback
	for i := 0; i < r.Count; i++ {
		r.Row(i).MarkLinks(mark)
	}
}
