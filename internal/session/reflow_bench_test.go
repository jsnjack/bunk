package session

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"bunk/internal/vt10x"
)

// reflowKinds are the scrollback contents the reflow benchmark and the
// equivalence tests build: every kind of cell reflow has to carry.
var reflowKinds = []string{"ascii", "styled", "wide", "links", "images", "wrapped", "shell", "mixed"}

// reflowLine appends one line of the given kind, then CR LF. Lines fill
// about 200 columns, except "shell" (short, mostly unchanged by a resize)
// and "wrapped" (520 columns, soft-wrapped).
func reflowLine(buf *bytes.Buffer, kind string, i int) {
	if kind == "mixed" {
		kind = reflowKinds[i%(len(reflowKinds)-1)]
	}
	switch kind {
	case "ascii":
		fmt.Fprintf(buf, "%06d %s", i, strings.Repeat(string(rune('a'+i%26)), 193))
	case "styled":
		for w := 0; w < 20; w++ {
			switch w % 4 {
			case 0:
				fmt.Fprintf(buf, "\x1b[1;38;2;%d;%d;%dm", (i+w)%256, (i*7)%256, w*9)
			case 1:
				fmt.Fprintf(buf, "\x1b[4:3;58;2;%d;2;3m", w)
			case 2:
				fmt.Fprintf(buf, "\x1b[48;5;%dm", (i+w)%256)
			default:
				buf.WriteString("\x1b[0m")
			}
			fmt.Fprintf(buf, "w%08d ", i*20+w)
		}
		buf.WriteString("\x1b[0m")
	case "wide":
		fmt.Fprintf(buf, "%07d ", i)
		for w := 0; w < 96; w++ {
			buf.WriteString([]string{"日", "本", "語", "😀", "🎉", "👩‍👩‍👧"}[(i+w)%6])
		}
	case "links":
		for w := 0; w < 5; w++ {
			fmt.Fprintf(buf, "\x1b]8;;https://example.org/%d/%d\x1b\\link-%010d-%d\x1b]8;;\x1b\\%s", i, w, i, w, strings.Repeat(" ", 20))
		}
	case "images":
		// A one-row image, 40 cells wide; every cell holds its own samples.
		buf.WriteString("\x1b_Ga=T,f=24,s=2,v=1,c=40,r=1;/wAAAP8A\x1b\\")
		fmt.Fprintf(buf, " image %06d %s", i, strings.Repeat("i", 140))
	case "wrapped":
		fmt.Fprintf(buf, "%06d %s", i, strings.Repeat(string(rune('A'+i%26)), 513))
	case "shell":
		switch i % 3 {
		case 0:
			fmt.Fprintf(buf, "drwxr-xr-x  2 jsn jsn 4096 Oct 25 12:%02d \x1b[1;34mdir%d\x1b[0m", i%60, i)
		case 1:
			fmt.Fprintf(buf, "$ go test ./... -run Test%d", i)
		default:
			fmt.Fprintf(buf, "ok  \tbunk/internal/session\t0.%03ds", i%1000)
		}
	}
	buf.WriteString("\r\n")
}

// newReflowSession returns a session of cols×rows whose scrollback (capacity
// lines) is full of the given kind, with the cursor after a prompt on the
// last row.
func newReflowSession(kind string, cols, rows, lines int) *Session {
	s := newTestSession(cols, rows)
	s.ScrollbackLines, s.Scrollback = lines, Ring{MaxLines: lines}
	var buf bytes.Buffer
	for i := 0; s.Scrollback.Count < lines || i < rows; i++ {
		buf.Reset()
		reflowLine(&buf, kind, i)
		s.Feed(buf.Bytes())
	}
	s.Feed([]byte("$ "))
	return s
}

// reflowFixture restores a session to the state it had when the fixture was
// made, so every benchmark iteration reflows the same content.
type reflowFixture struct {
	s          *Session
	ring       Ring
	screen     [][]vt10x.Glyph
	cursor     vt10x.Cursor
	cols, rows int
	offset     int
}

func newReflowFixture(s *Session) *reflowFixture {
	f := &reflowFixture{s: s, ring: cloneRing(s.Scrollback), cursor: s.Term.Cursor(), offset: s.ScrollOffset}
	f.cols, f.rows = s.Term.Size()
	for y := 0; y < f.rows; y++ {
		f.screen = append(f.screen, CaptureRow(s.Term, y, f.cols))
	}
	return f
}

func (f *reflowFixture) restore() {
	f.s.Scrollback = cloneRing(f.ring)
	f.s.ScrollOffset = f.offset
	f.s.Term.Resize(f.cols, f.rows)
	f.s.Term.ReplaceScreen(f.cols, f.rows, f.screen, f.cursor, f.s.Term)
}

// cloneRing returns a deep copy of r.
func cloneRing(r Ring) Ring {
	c := Ring{MaxLines: r.MaxLines}
	for i := 0; i < r.Count; i++ {
		c.PushRow(r.Row(i))
	}
	return c
}

// BenchmarkReflow measures ResizeAndReflow over 10 000 rows of scrollback,
// scrolled half way up so the scroll anchor is mapped too. Names give the
// content and the widths: "200-120-200" reflows twice per iteration, "drag"
// narrows one column at a time from 200 to 190, as a window resize does.
// cpu-ns/op is the CPU time of all threads, garbage collection included;
// compare it when other processes share the CPU.
func BenchmarkReflow(b *testing.B) {
	type widths struct {
		name  string
		start int
		steps []int
	}
	narrowWiden := widths{"200-120-200", 200, []int{120, 200}}
	narrow := widths{"200-80", 200, []int{80}}
	widen := widths{"80-200", 80, []int{200}}
	drag := widths{"drag", 200, []int{199, 198, 197, 196, 195, 194, 193, 192, 191, 190}}
	cases := []struct {
		kind   string
		widths []widths
	}{
		{"ascii", []widths{narrowWiden}},
		{"styled", []widths{narrowWiden}},
		{"wide", []widths{narrowWiden}},
		{"links", []widths{narrowWiden}},
		{"images", []widths{narrowWiden}},
		{"wrapped", []widths{narrowWiden}},
		{"shell", []widths{narrowWiden, narrow, widen, drag}},
		{"mixed", []widths{narrowWiden, narrow, widen, drag}},
	}
	const rows, lines = 50, 10000
	for _, c := range cases {
		for _, w := range c.widths {
			b.Run(c.kind+"/"+w.name, func(b *testing.B) {
				s := newReflowSession(c.kind, w.start, rows, lines)
				s.ScrollOffset = s.Scrollback.Count / 2
				f := newReflowFixture(s)
				b.ReportAllocs()
				var cpu time.Duration
				b.ResetTimer()
				for range b.N {
					b.StopTimer()
					f.restore()
					// Collect the restore's garbage now, so the timed part
					// pays only for collections its own allocations cause.
					runtime.GC()
					start := processCPUTime()
					b.StartTimer()
					for _, cols := range w.steps {
						s.ResizeAndReflow(cols, rows)
					}
					b.StopTimer()
					cpu += processCPUTime() - start
					b.StartTimer()
				}
				b.ReportMetric(float64(cpu.Nanoseconds())/float64(b.N), "cpu-ns/op")
			})
		}
	}
}

// processCPUTime returns the CPU time used by every thread of the process,
// garbage collection included. Unlike wall time, it does not grow while
// other processes hold the CPU, so runs on a busy machine stay comparable.
func processCPUTime() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// TestReflowFixtureContent checks that the benchmark's scrollback holds the
// cells it claims to: soft wraps, wide characters, links, underline colours
// and image samples.
func TestReflowFixtureContent(t *testing.T) {
	for _, kind := range reflowKinds {
		t.Run(kind, func(t *testing.T) {
			s := newReflowSession(kind, 200, 10, 300)
			if s.Scrollback.Count != 300 {
				t.Fatalf("scrollback holds %d rows, want 300", s.Scrollback.Count)
			}
			var wrapped, wide, links, images, underline int
			for r := 0; r < s.Scrollback.Count; r++ {
				row := s.Scrollback.Get(r)
				if len(row) > 0 && row[len(row)-1].Mode&vt10x.AttrWrap != 0 {
					wrapped++
				}
				for _, g := range row {
					switch {
					case g.Width == 2:
						wide++
					case g.Link != 0:
						links++
					case g.Image != nil:
						images++
					case g.UL != vt10x.DefaultUL:
						underline++
					}
				}
			}
			want := map[string]*int{"wrapped": &wrapped, "wide": &wide, "links": &links, "images": &images, "styled": &underline}
			for name, n := range want {
				if (kind == name || kind == "mixed") && *n == 0 {
					t.Errorf("no %s cells in %s scrollback", name, kind)
				}
			}
			if cur := s.Term.Cursor(); cur.X != 2 {
				t.Errorf("cursor at %+v, want after the prompt", cur)
			}
		})
	}
}
