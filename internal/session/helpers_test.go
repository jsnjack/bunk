package session

import (
	"os/exec"

	"bunk/internal/vt10x"
)

// newTestSession returns a session without a child process: output is fed
// with Feed or CaptureAndWrite, and scrolled rows reach the scrollback.
func newTestSession(cols, rows int) *Session {
	s := &Session{Cmd: &exec.Cmd{}, ScrollbackLines: 1000, Scrollback: Ring{MaxLines: 1000}}
	s.Term = vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollCallback(s.OnScrollRow), vt10x.WithScrollbackClearCallback(s.OnScrollbackClear))
	return s
}

// makeGlyphRow returns a row holding chars.
func makeGlyphRow(chars ...rune) []vt10x.Glyph {
	row := make([]vt10x.Glyph, len(chars))
	for i, ch := range chars {
		row[i] = vt10x.Glyph{Char: ch}
	}
	return row
}
