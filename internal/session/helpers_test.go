package session

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"bunk/internal/vt10x"
)

// closeWriter waits until s has written everything it queued for its PTY,
// then closes w, the write end of the pipe that stands in for the PTY, so
// the test can read the pipe to its end.
func closeWriter(t *testing.T, s *Session, w *os.File) {
	t.Helper()
	if !s.WaitWritten(5 * time.Second) {
		t.Fatal("the session did not write its queued replies")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// fastTiming shortens a session's timers for tests that wait for them. The
// values leave room for -race on a loaded machine: Idle stays long enough
// for a test that polls Status to see the working state.
var fastTiming = Timing{
	Poll:         20 * time.Millisecond,
	Idle:         250 * time.Millisecond,
	HangupGrace:  100 * time.Millisecond,
	RecentlyLeft: 250 * time.Millisecond,
	SyncRelease:  50 * time.Millisecond,
}

// timingCase is one set of timer intervals a test runs with.
type timingCase struct {
	name   string
	timing Timing
}

// timings returns the intervals a timer test runs with: fast, and the
// defaults (the zero Timing) unless -short. A default run waits as long as
// a user does, so callers run the cases as parallel subtests.
func timings(fast Timing) []timingCase {
	cases := []timingCase{{"short", fast}}
	if !testing.Short() {
		cases = append(cases, timingCase{"default", Timing{}})
	}
	return cases
}

// newTestSession returns a session without a child process: output is fed
// with Feed or CaptureAndWrite, and scrolled rows reach the scrollback.
func newTestSession(cols, rows int) *Session {
	s := &Session{Cmd: &exec.Cmd{}, ScrollbackLines: 1000, Scrollback: Ring{MaxLines: 1000}}
	s.Term = vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollCallback(s.OnScrollRow), vt10x.WithScrollbackClearCallback(s.OnScrollbackClear),
		vt10x.WithLinkReferences(s.MarkLinks))
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
