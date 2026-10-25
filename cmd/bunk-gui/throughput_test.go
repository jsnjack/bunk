package main

import (
	"os"
	"testing"
	"time"

	"bunk/internal/session"
	"bunk/internal/vt10x"
)

// TestSessionThroughput feeds a file through the session core without a
// window or PTY, as a baseline for `bunk-gui -e 'cat FILE'`. It runs only
// when BUNKGUI_THROUGHPUT_FILE names a file.
func TestSessionThroughput(t *testing.T) {
	path := os.Getenv("BUNKGUI_THROUGHPUT_FILE")
	if path == "" {
		t.Skip("set BUNKGUI_THROUGHPUT_FILE to run")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := &session.Session{ScrollbackLines: session.DefaultScrollbackLines}
	s.Scrollback = session.Ring{MaxLines: s.ScrollbackLines}
	s.Term = vt10x.New(vt10x.WithSize(120, 36), vt10x.WithScrollCallback(s.OnScrollRow))
	tv := newTermView()
	f := newFixture(120, 36)
	start := time.Now()
	frames := 0
	const chunk = 32 << 10
	for off := 0; off < len(data); off += chunk {
		s.Lock()
		s.Feed(data[off:min(off+chunk, len(data))])
		s.Unlock()
		// One frame per 8 chunks, roughly what the GUI manages during cat.
		if off/chunk%8 == 0 {
			s.Lock()
			tv.pull(s)
			s.Unlock()
			tv.convert(f.r)
			frames++
		}
	}
	el := time.Since(start)
	t.Logf("%d MB in %v = %.1f MB/s, %d frames pulled and converted", len(data)>>20, el, float64(len(data))/el.Seconds()/1e6, frames)
}
