package main

import (
	"bunk/internal/alert"
	"bunk/internal/desktop"
	"bunk/internal/desktop/desktoptest"
	"bunk/internal/session"
	"bunk/internal/vt10x"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/gdamore/tcell/v2"
)

// alertPane is a pane without a PTY, at x, with an ID. Its agent's state
// changes through Session.Notify, as with "bunk notify".
func alertPane(id, x int) *Pane {
	return &Pane{
		Session: &session.Session{
			ID:              id,
			Term:            vt10x.New(vt10x.WithSize(39, 10)),
			Cmd:             &exec.Cmd{},
			ScrollbackLines: 100,
			Scrollback:      session.Ring{MaxLines: 100},
		},
		x: x, w: 40, h: 10,
	}
}

// alertApp returns an App with two panes side by side, the first active,
// that renders to a simulation screen and writes host output to the
// returned buffer.
func alertApp(t *testing.T) (*App, *Pane, *Pane, *bytes.Buffer) {
	t.Helper()
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scr.Fini)
	scr.SetSize(81, 10)
	a, b := alertPane(1, 0), alertPane(2, 41)
	root := &Node{w: 81, h: 10, dir: splitVertical}
	root.left = &Node{w: 40, h: 10, pane: a, parent: root}
	root.right = &Node{x: 41, w: 40, h: 10, pane: b, parent: root}
	var host bytes.Buffer
	app := &App{
		screen: scr, theme: testTheme(), keys: resolveKeybindings(nil),
		root: root, active: a, redraw: make(chan struct{}, 1), done: make(chan struct{}),
		oscBuf: newOSCBuffer(), hostOut: &host, lastCursorStyle: -1,
	}
	app.alerts.Settings = alert.Default()
	t.Cleanup(func() { close(app.done) })
	return app, a, b, &host
}

// notifications returns the OSC 777 notify sequences in host output and
// clears it.
func notifications(host *bytes.Buffer) []string {
	var out []string
	rest := host.Bytes()
	for {
		i := bytes.Index(rest, []byte("\x1b]777;"))
		if i < 0 {
			break
		}
		j := bytes.Index(rest[i:], []byte("\x1b\\"))
		if j < 0 {
			out = append(out, string(rest[i:]))
			break
		}
		out = append(out, string(rest[i:i+j+2]))
		rest = rest[i+j+2:]
	}
	host.Reset()
	return out
}

// TestAlertsToHost: an agent in an inactive pane that needs the user, is
// done or failed sends one OSC 777 notification to the host terminal,
// sanitised and bounded; the active pane sends none while the host
// terminal has the focus, and none go out when they are turned off.
func TestAlertsToHost(t *testing.T) {
	app, a, b, host := alertApp(t)
	app.render()
	if got := notifications(host); len(got) != 0 {
		t.Fatalf("idle panes: %q", got)
	}

	b.Notify(session.EventNeedsYou, "Allow \x1b]0;pwned\x07 npm install; now?\u202e "+strings.Repeat("x", 400))
	app.render()
	app.render()
	got := notifications(host)
	if len(got) != 1 {
		t.Fatalf("inactive pane needs you: %d notifications %q, want 1", len(got), got)
	}
	seq := got[0]
	const head = "\x1b]777;notify;agent needs you;Allow ]0,pwned npm install, now? xxx"
	if !strings.HasPrefix(seq, head) || !strings.HasSuffix(seq, "…\x1b\\") {
		t.Errorf("notification %q, want it to start %q and end cut short", seq, head)
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b]777;"), "\x1b\\")
	if strings.IndexFunc(inner, unicode.IsControl) >= 0 || strings.ContainsRune(inner, '\u202e') || strings.Count(inner, ";") != 2 {
		t.Errorf("unsafe notification %q", seq)
	}
	if len(seq) > 512 {
		t.Errorf("notification of %d bytes", len(seq))
	}

	a.Notify(session.EventDone, "finished")
	app.render()
	if got := notifications(host); len(got) != 0 {
		t.Errorf("active pane done: %q", got)
	}

	app.handleFocus(false)
	a.Notify(session.EventFailed, "tests failed")
	app.render()
	if got := notifications(host); !slices.Equal(got, []string{"\x1b]777;notify;agent failed;tests failed\x1b\\"}) {
		t.Errorf("active pane failed while the host terminal is unfocused: %q", got)
	}
	app.handleFocus(true)

	app.alerts.Settings.Enabled = false
	b.Notify(session.EventDone, "all done")
	app.render()
	if got := notifications(host); len(got) != 0 {
		t.Errorf("turned off: %q", got)
	}
	app.alerts.Settings.Enabled = true
	b.Notify(session.EventWorking, "")
	app.render()
	b.Notify(session.EventDone, "all done")
	app.render()
	if got := notifications(host); !slices.Equal(got, []string{"\x1b]777;notify;agent is done;all done\x1b\\"}) {
		t.Errorf("turned on again, inactive pane done: %q", got)
	}
}

// TestAlertsFollowZoom: while another pane fills the screen, an agent in
// a hidden pane still notifies.
func TestAlertsFollowZoom(t *testing.T) {
	app, a, b, host := alertApp(t)
	app.zoomedPane = a
	b.Notify(session.EventNeedsYou, "")
	app.render()
	if got := notifications(host); len(got) != 1 || !strings.Contains(got[0], ";agent needs you;Needs your attention") {
		t.Errorf("hidden pane needs you: %q", got)
	}
}

// TestLoadConfigNotifications: [notifications] in bunk's config.toml sets
// the alerts; a state name it does not know fails at startup.
func TestLoadConfigNotifications(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		enabled       bool
		states        []string
		err           string
	}{
		{name: "no table", enabled: true, states: []string{"needs-you", "done", "failed"}},
		{name: "documented defaults", content: DefaultConfigTOML(), enabled: true, states: []string{"needs-you", "done", "failed"}},
		{name: "off", content: "[notifications]\nenabled = false\n", states: []string{"needs-you", "done", "failed"}},
		{name: "needs-you only", content: "[notifications]\nstates = [\"needs-you\"]\n", enabled: true, states: []string{"needs-you"}},
		{name: "unknown state", content: "[notifications]\nstates = [\"idle\"]\n", err: `unknown state "idle"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path, "")
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) || !strings.Contains(err.Error(), path) {
					t.Fatalf("error %v, want %q and the path", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Alerts.Enabled != tc.enabled || !slices.Equal(alert.Names(cfg.Alerts.States), tc.states) {
				t.Errorf("alerts %+v, want enabled %v states %q", cfg.Alerts, tc.enabled, tc.states)
			}
		})
	}
}

// TestDesktopAlertsChoice: bunk on the desktop's machine, with a session
// bus and gdbus, sends desktop notifications; over SSH, without a bus or
// without gdbus it writes OSC 777 to the host terminal.
func TestDesktopAlertsChoice(t *testing.T) {
	f := desktoptest.New(t)
	const bus = "unix:path=/nonexistent/test-bus"
	withFake := f.Dir + string(os.PathListSeparator) + os.Getenv("PATH")
	for _, tc := range []struct {
		name    string
		env     map[string]string
		path    string
		desktop bool
	}{
		{"local session bus", map[string]string{"DBUS_SESSION_BUS_ADDRESS": bus}, withFake, true},
		{"over SSH", map[string]string{"DBUS_SESSION_BUS_ADDRESS": bus, "SSH_CONNECTION": "10.0.0.2 5 10.0.0.9 22"}, withFake, false},
		{"no session bus", map[string]string{"XDG_RUNTIME_DIR": t.TempDir()}, withFake, false},
		{"no gdbus", map[string]string{"DBUS_SESSION_BUS_ADDRESS": bus}, t.TempDir(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PATH", tc.path)
			app := &App{}
			app.startDesktopAlerts(func(k string) string { return tc.env[k] })
			if (app.desktop != nil) != tc.desktop {
				t.Fatalf("desktop notifier %+v, want one: %v", app.desktop, tc.desktop)
			}
			if d := app.desktop; d != nil && (d.Gdbus != f.Path || d.Address != bus || d.AppName != "bunk") {
				t.Errorf("notifier gdbus %q, bus %q, app %q", d.Gdbus, d.Address, d.AppName)
			}
		})
	}
}

// TestAlertsToDesktop: with a desktop notifier, an inactive pane's agent
// alert becomes one desktop notification, not OSC 777; a newer state
// replaces it; looking at the pane withdraws it, and so do closing the
// pane and quitting. When gdbus fails, that alert reaches the host
// terminal as OSC 777 instead, once. With the host terminal unfocused,
// the active pane notifies too. Nothing is written to stderr.
func TestAlertsToDesktop(t *testing.T) {
	f := desktoptest.New(t)
	const bus = "unix:path=/nonexistent/test-bus"
	app, a, b, host := alertApp(t)
	app.desktop = &desktop.Notifier{Gdbus: f.Path, Address: bus, AppName: desktopAppName, Icon: desktopIcon,
		Fallback: app.alertToHostLater}
	needs := desktoptest.NotifyCall(bus, 0, "'agent needs you'", `'Allow npm install?'`)
	stderr := stderrDuring(t, func() {
		b.Notify(session.EventNeedsYou, "Allow npm install?")
		app.render()
		f.WaitCalls(t, 1)
		b.Notify(session.EventDone, "Finished")
		app.render()
		f.WaitCalls(t, 2)
		app.focusPane(b) // acknowledges done, and b is watched
		app.render()
		f.WaitCalls(t, 3)
		if got := notifications(host); len(got) != 0 {
			t.Errorf("OSC 777 next to desktop notifications: %q", got)
		}

		f.SetMode(t, "fail")
		a.Notify(session.EventFailed, "tests failed")
		app.render()
		f.WaitCalls(t, 4)
		// The failure comes back from gdbus asynchronously and the fallback
		// goes out on a later frame; app.redraw may still hold an older
		// wake-up, so render until it arrives, then once more to see it
		// is not sent twice.
		var got []string
		for deadline := time.Now().Add(10 * time.Second); len(got) == 0 && time.Now().Before(deadline); {
			select {
			case <-app.redraw:
			case <-time.After(20 * time.Millisecond):
			}
			app.render()
			got = append(got, notifications(host)...)
		}
		app.render()
		got = append(got, notifications(host)...)
		if !slices.Equal(got, []string{"\x1b]777;notify;agent failed;tests failed\x1b\\"}) {
			t.Errorf("gdbus failed: host got %q, want the alert as OSC 777 once", got)
		}

		f.SetMode(t, "ok")
		a.Notify(session.EventWorking, "")
		app.render()
		a.Notify(session.EventNeedsYou, "Allow npm install?")
		app.render()
		f.WaitCalls(t, 5)
		app.removePane(a)
		f.WaitCalls(t, 6)

		app.handleFocus(false) // the active pane is not watched now
		b.Notify(session.EventNeedsYou, "Run the tests?")
		app.render()
		f.WaitCalls(t, 7)
		app.closeDesktopAlerts()
	})
	want := []string{
		needs,
		desktoptest.NotifyCall(bus, 1, "'agent is done'", "'Finished'"),
		desktoptest.CloseCall(bus, 1),
		desktoptest.NotifyCall(bus, 0, "'agent failed'", "'tests failed'"),
		desktoptest.NotifyCall(bus, 0, "'agent needs you'", `'Allow npm install?'`),
		desktoptest.CloseCall(bus, 2),
		desktoptest.NotifyCall(bus, 0, "'agent needs you'", `'Run the tests?'`),
		desktoptest.CloseCall(bus, 3),
	}
	if got := f.Calls(); !slices.Equal(got, want) {
		t.Errorf("gdbus calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := notifications(host); len(got) != 0 {
		t.Errorf("OSC 777 at the end: %q", got)
	}
	if stderr != "" {
		t.Errorf("stderr: %q", stderr)
	}
}
