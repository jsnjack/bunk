package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"

	"bunk/internal/alert"
	"bunk/internal/session"
)

// alertsInTests records the notification calls of unit tests: the test
// binary never reaches a notification server through them (window tests'
// bunk-gui records them in --report and stays off the server too).
var alertsInTests []string

func init() {
	postAlert = func(_ *App, pane int, al alert.Alert) {
		alertsInTests = append(alertsInTests, fmt.Sprintf("send %d %s", pane, al.Title))
	}
	withdrawAlert = func(_ *App, pane int) { alertsInTests = append(alertsInTests, fmt.Sprintf("withdraw %d", pane)) }
}

// privateBus starts a message bus for the test alone, with no service
// files, and returns its address. Nothing on it reaches the desktop.
func privateBus(t *testing.T) string {
	t.Helper()
	exe, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not installed")
	}
	dir := t.TempDir()
	conf := filepath.Join(dir, "bus.conf")
	must(t, os.WriteFile(conf, []byte(`<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:dir=`+dir+`</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`), 0o600))
	cmd := exec.Command(exe, "--config-file="+conf, "--print-address=1", "--nofork")
	out, err := cmd.StdoutPipe()
	must(t, err)
	must(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("dbus-daemon address: %v", err)
	}
	return strings.TrimSpace(addr)
}

func busConn(t *testing.T, addr string) *gio.DBusConnection {
	t.Helper()
	c, err := gio.NewDBusConnectionForAddressSync(context.Background(), addr,
		gio.DBusConnectionFlagsAuthenticationClient|gio.DBusConnectionFlagsMessageBusConnection, nil)
	if err != nil {
		t.Fatalf("connect to the test bus: %v", err)
	}
	t.Cleanup(func() {
		if err := c.CloseSync(context.Background()); err != nil {
			t.Logf("close test bus connection: %v", err)
		}
	})
	return c
}

// pump runs the default main context until done holds, for 5 s at most.
func pump(t *testing.T, what string, done func() bool) {
	t.Helper()
	ctx := glib.MainContextDefault()
	for deadline := time.Now().Add(5 * time.Second); !done(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		if !ctx.Iteration(false) {
			time.Sleep(time.Millisecond)
		}
	}
}

// captureLogs sends slog records at debug level and above to a buffer
// for the rest of the test.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	var buf syncBuffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var testAlert = alert.For(session.Status{Agent: "claude", State: session.AgentNeedsYou, Notice: "Allow npm install?",
	Repo: "bunk", Branch: "main"})

// TestDeskNotifierNoServer: without a notification server on the bus,
// showing and withdrawing a notification fail quietly, logged at debug
// level, and leave nothing behind.
func TestDeskNotifierNoServer(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	logs := captureLogs(t)
	d := newDeskNotifier(busConn(t, privateBus(t)), func(int, string) { t.Error("click without a server") })
	defer d.close()
	d.send(3, testAlert)
	pump(t, "the Notify answer", func() bool { return !d.notes[3].busy })
	d.withdraw(3)
	if len(d.notes)+len(d.paneOf) != 0 {
		t.Errorf("left behind: notes %v, ids %v", d.notes, d.paneOf)
	}
	got := logs.String()
	if !strings.Contains(got, `level=DEBUG msg="desktop notification not shown" pane=3`) ||
		!strings.Contains(got, fdoName) {
		t.Errorf("no debug record of the failure:\n%s", got)
	}
	if strings.Contains(got, "level=WARN") || strings.Contains(got, "level=ERROR") {
		t.Errorf("a missing server logged above debug:\n%s", got)
	}
}

// fakeServer answers Notify and CloseNotification on a test bus as a
// notification server would, and records the calls.
type fakeServer struct {
	conn *gio.DBusConnection
	mu   sync.Mutex
	in   []*gio.DBusMessage // calls waiting for an answer
	next uint32
	log  []string
	sigs []string // the argument types of each call
}

func newFakeServer(t *testing.T, addr string) *fakeServer {
	t.Helper()
	s := &fakeServer{conn: busConn(t, addr), next: 41}
	// The filter runs on GDBus's worker thread: it only queues the calls,
	// and the test's main loop answers them.
	s.conn.AddFilter(func(_ *gio.DBusConnection, m *gio.DBusMessage, incoming bool) *gio.DBusMessage {
		if !incoming || m.MessageType() != gio.DBusMessageTypeMethodCall || m.Interface() != fdoIface {
			return m
		}
		s.mu.Lock()
		s.in = append(s.in, m)
		s.mu.Unlock()
		return nil
	})
	_, err := s.conn.CallSync(context.Background(), "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus",
		"RequestName", glib.NewVariantTuple([]*glib.Variant{glib.NewVariantString(fdoName), glib.NewVariantUint32(4)}),
		glib.NewVariantType("(u)"), gio.DBusCallFlagsNone, -1)
	if err != nil {
		t.Fatalf("own %s: %v", fdoName, err)
	}
	return s
}

// serve answers the queued calls; it reports whether there were any.
func (s *fakeServer) serve(t *testing.T) bool {
	s.mu.Lock()
	in := s.in
	s.in = nil
	s.mu.Unlock()
	for _, m := range in {
		body := m.Body()
		s.sigs = append(s.sigs, body.TypeString())
		reply := m.NewMethodReply()
		switch m.Member() {
		case "Notify":
			id := body.ChildValue(1).Uint32()
			if id == 0 {
				s.next++
				id = s.next
			}
			hints := body.ChildValue(6).Print(false)
			s.log = append(s.log, strings.Join([]string{"notify", body.ChildValue(0).String(), u32(body.ChildValue(1)),
				body.ChildValue(2).String(), body.ChildValue(3).String(), body.ChildValue(4).String(),
				strings.Join(body.ChildValue(5).Strv(), ","), hints}, " | "))
			reply.SetBody(glib.NewVariantTuple([]*glib.Variant{glib.NewVariantUint32(id)}))
		case "CloseNotification":
			s.log = append(s.log, "close | "+u32(body.ChildValue(0)))
		}
		if _, err := s.conn.SendMessage(reply, gio.DBusSendMessageFlagsNone); err != nil {
			t.Errorf("answer %s: %v", m.Member(), err)
		}
	}
	return len(in) > 0
}

func (s *fakeServer) emit(t *testing.T, signal string, args ...*glib.Variant) {
	t.Helper()
	if err := s.conn.EmitSignal("", fdoPath, fdoIface, signal, glib.NewVariantTuple(args)); err != nil {
		t.Fatalf("emit %s: %v", signal, err)
	}
}

func u32(v *glib.Variant) string { return v.Print(false) }

// TestDeskNotifier: against a fake notification server, a pane's alert
// shows with the app's name, icon and desktop entry, the title and the
// escaped body, and a default action; a newer alert replaces it, also one
// sent before the server answered; withdrawing closes it; a click with
// its activation token reaches onClick; a notification the server closed
// is forgotten.
func TestDeskNotifier(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	logs := captureLogs(t)
	addr := privateBus(t)
	srv := newFakeServer(t, addr)
	type click struct {
		pane  int
		token string
	}
	var clicks []click
	d := newDeskNotifier(busConn(t, addr), func(pane int, token string) { clicks = append(clicks, click{pane, token}) })
	defer d.close()
	settle := func(what string) {
		t.Helper()
		pump(t, what, func() bool {
			srv.serve(t)
			for _, n := range d.notes {
				if n.busy {
					return false
				}
			}
			return true
		})
	}

	d.send(1, testAlert)
	odd := alert.For(session.Status{Agent: "co<de>x", State: session.AgentDone, Notice: "a < b && c > d", Repo: "r\xffx", Branch: "b\x00r"})
	d.send(2, odd)
	settle("two notifications")
	done := alert.For(session.Status{Agent: "claude", State: session.AgentDone, Repo: "bunk", Branch: "main"})
	d.send(1, done)
	failed := alert.For(session.Status{Agent: "claude", State: session.AgentFailed, Repo: "bunk", Branch: "main"})
	d.send(1, failed) // before the server answered the first replacement
	settle("the replacements")
	hints := "{'desktop-entry': <'" + appID + "'>, 'urgency': <byte 0x01>}"
	want := []string{
		"notify | bunk-gui | 0 | " + appID + " | claude needs you | Allow npm install?\nbunk ⎇ main | default,Show tab | " + hints,
		"notify | bunk-gui | 0 | " + appID + " | co<de>x is done | a &lt; b &amp;&amp; c &gt; d\nrx ⎇ b r | default,Show tab | " + hints,
		"notify | bunk-gui | 42 | " + appID + " | claude is done | Finished its turn\nbunk ⎇ main | default,Show tab | " + hints,
		"notify | bunk-gui | 42 | " + appID + " | claude failed | Stopped with an error\nbunk ⎇ main | default,Show tab | " + hints,
	}
	if !slices.Equal(srv.log, want) {
		t.Fatalf("server calls:\n%s\nwant:\n%s", strings.Join(srv.log, "\n"), strings.Join(want, "\n"))
	}
	if d.notes[1].id != 42 || d.notes[2].id != 43 || d.paneOf[42] != 1 || d.paneOf[43] != 2 {
		t.Fatalf("notes %+v %+v, ids %v", *d.notes[1], *d.notes[2], d.paneOf)
	}

	srv.log = nil
	d.withdraw(1)
	pump(t, "CloseNotification", func() bool { srv.serve(t); return len(srv.log) == 1 })
	if srv.log[0] != "close | 42" || d.notes[1] != nil {
		t.Errorf("withdraw: server calls %q, note %v", srv.log, d.notes[1])
	}

	srv.emit(t, "ActionInvoked", glib.NewVariantUint32(42), glib.NewVariantString(fdoDefault)) // withdrawn: no pane
	srv.emit(t, "ActivationToken", glib.NewVariantUint32(43), glib.NewVariantString("token-43"))
	srv.emit(t, "ActionInvoked", glib.NewVariantUint32(43), glib.NewVariantString(fdoDefault))
	srv.emit(t, "NotificationClosed", glib.NewVariantUint32(43), glib.NewVariantUint32(2))
	pump(t, "the click", func() bool { return len(clicks) > 0 && d.notes[2] == nil })
	if !slices.Equal(clicks, []click{{2, "token-43"}}) {
		t.Errorf("clicks %+v, want pane 2 with token-43", clicks)
	}
	srv.log = nil
	d.withdraw(2)
	if len(d.paneOf) != 0 || len(d.notes) != 0 {
		t.Errorf("after the server closed it: notes %v, ids %v", d.notes, d.paneOf)
	}
	if got := logs.String(); strings.Contains(got, "level=WARN") || strings.Contains(got, "level=ERROR") {
		t.Errorf("logged:\n%s", got)
	}
}
