package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
)

// fakeSystemd answers StartTransientUnit on a test bus as the user's
// systemd would, and records the calls. A unit name it has seen fails
// as UnitExists.
type fakeSystemd struct {
	conn  *gio.DBusConnection
	mu    sync.Mutex
	in    []*gio.DBusMessage // calls waiting for an answer
	units map[string]bool
	log   []string // each call's member, argument types and arguments
}

func newFakeSystemd(t *testing.T, addr string) *fakeSystemd {
	t.Helper()
	s := &fakeSystemd{conn: busConn(t, addr), units: map[string]bool{}}
	// The filter runs on GDBus's worker thread: it only queues the calls,
	// and the test's main loop answers them.
	s.conn.AddFilter(func(_ *gio.DBusConnection, m *gio.DBusMessage, incoming bool) *gio.DBusMessage {
		if !incoming || m.MessageType() != gio.DBusMessageTypeMethodCall || m.Interface() != sdIface {
			return m
		}
		s.mu.Lock()
		s.in = append(s.in, m)
		s.mu.Unlock()
		return nil
	})
	_, err := s.conn.CallSync(context.Background(), "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus",
		"RequestName", glib.NewVariantTuple([]*glib.Variant{glib.NewVariantString(sdName), glib.NewVariantUint32(4)}),
		glib.NewVariantType("(u)"), gio.DBusCallFlagsNone, -1)
	if err != nil {
		t.Fatalf("own %s: %v", sdName, err)
	}
	return s
}

// serve answers the queued calls.
func (s *fakeSystemd) serve(t *testing.T) {
	s.mu.Lock()
	in := s.in
	s.in = nil
	s.mu.Unlock()
	for _, m := range in {
		body := m.Body()
		s.log = append(s.log, m.Member()+" "+body.TypeString()+" "+body.Print(true))
		unit := body.ChildValue(0).String()
		var reply *gio.DBusMessage
		if s.units[unit] {
			reply = m.NewMethodErrorLiteral(sdName+".UnitExists", "Unit "+unit+" was already loaded or has a fragment file.")
		} else {
			s.units[unit] = true
			reply = m.NewMethodReply()
			reply.SetBody(glib.NewVariantTuple([]*glib.Variant{glib.NewVariantObjectPath(sdPath + "/job/7")}))
		}
		if _, err := s.conn.SendMessage(reply, gio.DBusSendMessageFlagsNone); err != nil {
			t.Errorf("answer %s: %v", m.Member(), err)
		}
	}
}

// TestPaneScope: a pane's child goes to a scope of its own, named after
// the bunk-gui process and the pane, in app.slice, where an OOM kill ends
// only the process the kernel picked and that systemd removes once empty.
// A unit systemd already has leaves the pane in bunk-gui's scope; both
// answers are logged at debug level only.
func TestPaneScope(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	logs := captureLogs(t)
	addr := privateBus(t)
	sd := newFakeSystemd(t, addr)
	bus := busConn(t, addr)

	moveToScope(bus, 3, 4242)
	unit := fmt.Sprintf("bunk-gui-%d-pane-3.scope", os.Getpid())
	pump(t, "the scope", func() bool { sd.serve(t); return strings.Contains(logs.String(), "pane in its own scope") })
	want := "StartTransientUnit (ssa(sv)a(sa(sv))) ('" + unit + "', 'fail', [('PIDs', <[uint32 4242]>), " +
		"('Description', <'bunk-gui pane 3'>), ('Slice', <'app.slice'>), ('OOMPolicy', <'continue'>), " +
		"('CollectMode', <'inactive-or-failed'>)], @a(sa(sv)) [])"
	if len(sd.log) != 1 || sd.log[0] != want {
		t.Fatalf("systemd calls:\n%s\nwant:\n%s", strings.Join(sd.log, "\n"), want)
	}
	if !strings.Contains(logs.String(), `level=DEBUG msg="pane in its own scope" pane=3 unit=`+unit) {
		t.Errorf("no debug record of the scope:\n%s", logs)
	}

	moveToScope(bus, 3, 4243)
	pump(t, "the refusal", func() bool { sd.serve(t); return strings.Contains(logs.String(), "stays in bunk-gui's scope") })
	if got := logs.String(); !strings.Contains(got, `level=DEBUG msg="pane stays in bunk-gui's scope" pane=3`) ||
		!strings.Contains(got, "UnitExists") {
		t.Errorf("no debug record of the refusal:\n%s", got)
	}
	runFinalizers(t)
	if got := logs.String(); strings.Contains(got, "level=WARN") || strings.Contains(got, "level=ERROR") {
		t.Errorf("a scope logged above debug:\n%s", got)
	}
}

// TestPaneScopeNoSystemd: without systemd on the bus, as on the window
// tests' bus, the pane stays in bunk-gui's scope, logged at debug level.
func TestPaneScopeNoSystemd(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	logs := captureLogs(t)
	moveToScope(busConn(t, privateBus(t)), 5, 4242)
	pump(t, "the failure", func() bool { return strings.Contains(logs.String(), "stays in bunk-gui's scope") })
	runFinalizers(t)
	got := logs.String()
	if !strings.Contains(got, `level=DEBUG msg="pane stays in bunk-gui's scope" pane=5`) || !strings.Contains(got, sdName) {
		t.Errorf("no debug record of the failure:\n%s", got)
	}
	if strings.Contains(got, "level=WARN") || strings.Contains(got, "level=ERROR") {
		t.Errorf("a missing systemd logged above debug:\n%s", got)
	}
}
