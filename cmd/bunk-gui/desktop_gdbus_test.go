package main

import (
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"bunk/internal/alert"
	"bunk/internal/desktop"
	"bunk/internal/session"
)

// TestTUIDesktopNotifierWithGdbus: the notifier of bunk's TUI
// (internal/desktop) runs the real gdbus against a fake notification
// server on a private bus. The server gets every argument with the type
// it expects, a newer alert replaces the notification by its ID, and
// CloseNotification withdraws it. The test lives here because this
// package has the GLib fake server; it never touches the session bus.
func TestTUIDesktopNotifierWithGdbus(t *testing.T) {
	gdbus, err := exec.LookPath("gdbus")
	if err != nil {
		t.Skip("gdbus not installed")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	addr := privateBus(t)
	srv := newFakeServer(t, addr)
	n := &desktop.Notifier{Gdbus: gdbus, Address: addr, AppName: "bunk", Icon: "utilities-terminal",
		Fallback: func(key int, a alert.Alert) { t.Errorf("fallback for %d: %+v", key, a) }}
	defer n.Close(5 * time.Second)
	serveUntil := func(calls int) {
		t.Helper()
		pump(t, "gdbus calls", func() bool { srv.serve(t); return len(srv.log) >= calls })
	}
	n.Send(1, alert.For(session.Status{Agent: "claude", State: session.AgentNeedsYou,
		Notice: "Allow 'npm' <install> & go?", Repo: "bunk", Branch: "main"}))
	serveUntil(1) // the notifier took the request: the next one waits for its answer
	n.Send(1, alert.For(session.Status{Agent: "claude", State: session.AgentDone, Host: "gpu01"}))
	serveUntil(2)
	n.Withdraw(1)
	serveUntil(3)
	const hints = "{'urgency': <byte 0x01>}"
	want := []string{
		"notify | bunk | 0 | utilities-terminal | claude needs you | Allow 'npm' &lt;install&gt; &amp; go?\nbunk ⎇ main |  | " + hints,
		"notify | bunk | 42 | utilities-terminal | claude on gpu01 is done | Finished its turn |  | " + hints,
		"close | 42",
	}
	if !slices.Equal(srv.log, want) {
		t.Errorf("server calls:\n%s\nwant:\n%s", strings.Join(srv.log, "\n"), strings.Join(want, "\n"))
	}
	if sigs := []string{"(susssasa{sv}i)", "(susssasa{sv}i)", "(u)"}; !slices.Equal(srv.sigs, sigs) {
		t.Errorf("argument types %q, want %q", srv.sigs, sigs)
	}
}
