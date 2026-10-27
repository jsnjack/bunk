package session

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"bunk/internal/input"
)

// TestXTGETTCAPBackspaceOption: kbs follows Options.Backspace, so a front
// end that sends DEL for Backspace advertises DEL.
func TestXTGETTCAPBackspaceOption(t *testing.T) {
	kbs := hex.EncodeToString([]byte("kbs"))
	for _, tc := range []struct {
		key  input.Key
		want string
	}{
		{0, "\x08"}, // unset: BS, as bunk's tcell front end sends
		{input.KeyBS, "\x08"},
		{input.KeyBackspace, "\x7f"},
	} {
		want := "\x1bP1+r" + kbs + "=" + hex.EncodeToString([]byte(tc.want)) + "\x1b\\"
		if got := xtgettcapResponseKey(kbs, 0, 0, tc.key); got != want {
			t.Errorf("backspace %v: %q, want %q", tc.key, got, want)
		}
	}
}

// TestClosedHookAfterOutput: Hooks.Closed runs once ReadLoop has applied all
// of the child's output and the PTY is closed.
func TestClosedHookAfterOutput(t *testing.T) {
	s, err := Start(Options{Cols: 40, Rows: 5, Scrollback: 100, Args: []string{"/bin/sh", "-c", "printf closed-hook-ok"}})
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	done := make(chan struct{})
	defer close(done)
	s.Run(Hooks{Closed: func() { close(closed) }}, done)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Closed did not run")
	}
	s.Lock()
	row := CaptureRow(s.Term, 0, 40)
	s.Unlock()
	var b strings.Builder
	for _, g := range row {
		b.WriteRune(g.Char)
	}
	if !strings.Contains(b.String(), "closed-hook-ok") {
		t.Errorf("row 0 = %q when Closed ran, want the child's output", b.String())
	}
}
