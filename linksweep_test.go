package main

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"bunk/internal/session"
	"bunk/internal/vt10x"

	"github.com/gdamore/tcell/v2"
)

// hostLinks parses terminal output and returns the printable text sent
// under each OSC 8 URL.
func hostLinks(out string) map[string]string {
	links := map[string]string{}
	url := ""
	for i := 0; i < len(out); {
		switch {
		case strings.HasPrefix(out[i:], "\x1b]8;"):
			end := strings.Index(out[i:], "\x1b\\")
			if end < 0 {
				return links
			}
			_, url, _ = strings.Cut(out[i+4:i+end], ";")
			i += end + 2
		case strings.HasPrefix(out[i:], "\x1b["):
			i += 2
			for i < len(out) && (out[i] < 0x40 || out[i] > 0x7e) {
				i++
			}
			i++
		case strings.HasPrefix(out[i:], "\x1b]"):
			end := strings.IndexAny(out[i:], "\a\x1b")
			if end <= 0 {
				return links
			}
			i += end + 1
		case strings.HasPrefix(out[i:], "\x1b(") || strings.HasPrefix(out[i:], "\x1b)"):
			i += 3
		case out[i] == 0x1b:
			i += 2
		case out[i] < 0x20:
			i++
		default:
			if url != "" {
				links[url] += out[i : i+1]
			}
			i++
		}
	}
	return links
}

// TestRenderLinksAfterSweeps: after thousands of unique links forced link
// sweeps, the TUI sends the host the URL of every link it draws: one that
// stayed on screen through the sweeps, one that took a reused ID, churned
// links still on screen, and a link in the scrollback.
func TestRenderLinksAfterSweeps(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	tty := &captureTty{}
	scr, err := tcell.NewTerminfoScreenFromTty(tty)
	if err != nil {
		t.Skipf("terminfo screen unavailable: %v", err)
	}
	if err := scr.Init(); err != nil {
		t.Skipf("screen Init failed: %v", err)
	}
	defer scr.Fini()
	const w, h = 60, 6
	scr.SetSize(w, h)
	p := &Pane{Session: &session.Session{Cmd: &exec.Cmd{}, ScrollbackLines: 100, Scrollback: session.Ring{MaxLines: 100}}, w: w, h: h}
	p.Term = vt10x.New(vt10x.WithSize(w-1, h), vt10x.WithScrollCallback(p.OnScrollRow), vt10x.WithLinkReferences(p.MarkLinks))
	write := func(s string) {
		p.Lock()
		p.CaptureAndWrite([]byte(s))
		p.Unlock()
	}
	link := func(url, text string) string { return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\" }
	write(link("https://scrolled.example/doc", "scrolled away") + strings.Repeat("\r\n", h))
	// The keeper stays on the top row; the churn scrolls through the rows
	// below it and is dropped.
	write("\x1b[H" + link("https://keep.example/doc", "the keeper") + "\x1b[2;6r\x1b[2;1H")
	var b strings.Builder
	for i := range 10000 {
		fmt.Fprintf(&b, "%s\r\n", link(fmt.Sprintf("https://churn.example/%d", i), fmt.Sprintf("churn %d", i)))
	}
	write(b.String())
	write(link("https://fresh.example/new", "the fresh one"))
	p.Lock()
	st := p.Term.LinkStats()
	p.Unlock()
	if st.Sweeps < 2 || st.Freed < 8000 {
		t.Fatalf("the churn did not sweep: %+v", st)
	}

	render := func() map[string]string {
		tty.out.Reset()
		renderPane(scr, p, testTheme())
		closeHostHyperlink(&tty.out)
		scr.Show()
		return hostLinks(tty.out.String())
	}
	sent := render()
	want := map[string]string{
		"https://keep.example/doc":   "the keeper",
		"https://fresh.example/new":  "the fresh one",
		"https://churn.example/9999": "churn 9999",
		"https://churn.example/9996": "churn 9996",
	}
	for url, text := range want {
		if sent[url] != text {
			t.Errorf("sent %q under %s, want %q", sent[url], url, text)
		}
	}
	for url, text := range sent {
		if n, ok := strings.CutPrefix(url, "https://churn.example/"); ok && text != "churn "+n {
			t.Errorf("sent %q under %s", text, url)
		}
	}
	p.Lock()
	p.ScrollOffset = p.Scrollback.Count
	p.Unlock()
	if got := render()["https://scrolled.example/doc"]; got != "scrolled away" {
		t.Errorf("scrollback link: sent %q", got)
	}
	t.Logf("%+v; sent %d links", st, len(sent))
}
