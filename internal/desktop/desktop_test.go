package desktop

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"bunk/internal/alert"
	"bunk/internal/desktop/desktoptest"
)

// TestMain points the session bus of anything these tests start at a
// path that does not exist: no test reaches the user's desktop.
func TestMain(m *testing.M) {
	if err := os.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/bunk-tests-have-no-session-bus"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// TestSessionBus: desktop notifications go to the session bus when bunk
// runs on the desktop's machine, and nowhere over SSH or without a bus.
func TestSessionBus(t *testing.T) {
	sock := func(paths ...string) func(string) bool {
		return func(p string) bool { return slices.Contains(paths, p) }
	}
	for _, tc := range []struct {
		name     string
		env      map[string]string
		isSocket func(string) bool
		addr     string
		why      string
	}{
		{"bus address", map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus"}, sock(),
			"unix:path=/run/user/1000/bus", ""},
		{"runtime directory", map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, sock("/run/user/1000/bus"),
			"unix:path=/run/user/1000/bus", ""},
		{"runtime directory needing escapes", map[string]string{"XDG_RUNTIME_DIR": "/tmp/my run"}, sock("/tmp/my run/bus"),
			"unix:path=/tmp/my%20run/bus", ""},
		{"runtime directory without a bus", map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, sock(), "", "no session bus"},
		{"nothing", nil, sock(), "", "no session bus"},
		{"over SSH", map[string]string{"SSH_CONNECTION": "10.0.0.2 51234 10.0.0.9 22", "DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus"},
			sock("/run/user/1000/bus"), "", "running over SSH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr, why := SessionBus(func(k string) string { return tc.env[k] }, tc.isSocket)
			if addr != tc.addr || why != tc.why {
				t.Errorf("SessionBus = %q, %q; want %q, %q", addr, why, tc.addr, tc.why)
			}
		})
	}
	t.Run("IsSocket", func(t *testing.T) {
		if IsSocket(t.TempDir()) || IsSocket(filepath.Join(t.TempDir(), "missing")) {
			t.Error("a directory or a missing path is a socket")
		}
	})
}

const testBus = "unix:path=/nonexistent/test-bus"

// waitIdle waits until the notifier has made every call it was asked for.
func waitIdle(t *testing.T, n *Notifier) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		n.mu.Lock()
		running := n.running
		n.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the notifier's calls did not end")
		}
	}
}

// testNotifier returns a Notifier on the fake, recording fallbacks and
// debug records.
func testNotifier(f *desktoptest.FakeGdbus) (*Notifier, *[]string, *syncBuffer) {
	var mu sync.Mutex
	var fell []string
	logs := &syncBuffer{}
	n := &Notifier{Gdbus: f.Path, Address: testBus, AppName: "bunk", Icon: "utilities-terminal",
		Log: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Fallback: func(key int, a alert.Alert) {
			mu.Lock()
			fell = append(fell, fmt.Sprintf("%d %s", key, a.Title))
			mu.Unlock()
		}}
	return n, &fell, logs
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

func notifyCall(replaces int, title, body string) string {
	return desktoptest.NotifyCall(testBus, replaces, title, body)
}

func closeCall(id int) string { return desktoptest.CloseCall(testBus, id) }

var (
	needs = alert.Alert{Title: "claude needs you", Notice: "Allow 'npm' install?", Where: "bunk ⎇ main"}
	done  = alert.Alert{Title: "claude is done", Notice: "a < b \\ c"}
)

// TestNotifier: one notification per key, through gdbus on the given bus:
// a newer alert replaces it by the server's ID, Withdraw closes it once,
// and Close withdraws the rest.
func TestNotifier(t *testing.T) {
	f := desktoptest.New(t)
	n, fell, _ := testNotifier(f)
	n.Send(1, needs)
	f.WaitCalls(t, 1)
	n.Send(1, done)
	f.WaitCalls(t, 2)
	n.Send(2, needs)
	f.WaitCalls(t, 3)
	n.Withdraw(1)
	f.WaitCalls(t, 4)
	n.Withdraw(1)
	n.Withdraw(9)
	n.Close(5 * time.Second)
	n.Send(3, needs) // after Close: dropped
	waitIdle(t, n)
	want := []string{
		notifyCall(0, `'claude needs you'`, `'Allow \'npm\' install?\nbunk ⎇ main'`),
		notifyCall(1, `'claude is done'`, `'a &lt; b \\ c'`),
		notifyCall(0, `'claude needs you'`, `'Allow \'npm\' install?\nbunk ⎇ main'`),
		closeCall(1),
		closeCall(2),
	}
	if got := f.Calls(); !slices.Equal(got, want) {
		t.Errorf("gdbus calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(*fell) != 0 {
		t.Errorf("fallbacks: %q", *fell)
	}
}

// TestNotifierCoalesces: a key's request that has not run yet gives way to
// a newer one, so a burst costs one call per key.
func TestNotifierCoalesces(t *testing.T) {
	f := desktoptest.New(t)
	n, _, _ := testNotifier(f)
	n.mu.Lock() // the goroutine cannot take a request yet
	n.put(1, request{alert: needs})
	n.put(2, request{alert: needs})
	n.put(1, request{alert: done})
	n.put(2, request{withdraw: true})
	n.mu.Unlock()
	waitIdle(t, n)
	n.Close(5 * time.Second)
	want := []string{notifyCall(0, `'claude is done'`, `'a &lt; b \\ c'`), closeCall(1)}
	if got := f.Calls(); !slices.Equal(got, want) {
		t.Errorf("gdbus calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestNotifierFailure: a Notify that fails is logged at debug level and
// falls back, once, for that alert; nothing is left to withdraw. A call
// that gets no answer in time does not fall back: the server may have
// shown it.
func TestNotifierFailure(t *testing.T) {
	f := desktoptest.New(t)
	f.SetMode(t, "fail")
	n, fell, logs := testNotifier(f)
	n.Send(4, needs)
	waitIdle(t, n)
	n.Withdraw(4)
	n.Close(5 * time.Second)
	if !slices.Equal(*fell, []string{"4 claude needs you"}) {
		t.Errorf("fallbacks %q, want one for key 4", *fell)
	}
	if c := f.Calls(); len(c) != 1 {
		t.Errorf("calls %q, want the failed Notify alone", c)
	}
	if got := logs.String(); !strings.Contains(got, `level=DEBUG msg="desktop notification not shown" key=4`) ||
		!strings.Contains(got, "ServiceUnknown") {
		t.Errorf("debug records:\n%s", got)
	}

	t.Run("missing gdbus", func(t *testing.T) {
		n, fell, logs := testNotifier(&desktoptest.FakeGdbus{Path: filepath.Join(t.TempDir(), "gdbus")})
		n.Send(5, done)
		waitIdle(t, n)
		n.Close(5 * time.Second)
		if !slices.Equal(*fell, []string{"5 claude is done"}) || !strings.Contains(logs.String(), "level=DEBUG") {
			t.Errorf("fallbacks %q, logs:\n%s", *fell, logs.String())
		}
	})

	t.Run("no answer in time", func(t *testing.T) {
		f := desktoptest.New(t)
		f.SetMode(t, "hang")
		n, fell, logs := testNotifier(f)
		n.Timeout = time.Second
		start := time.Now()
		n.Send(6, needs)
		waitIdle(t, n)
		if len(*fell) != 0 || !strings.Contains(logs.String(), "no answer in time") {
			t.Errorf("fallbacks %q, logs:\n%s", *fell, logs.String())
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("a hung gdbus held the notifier for %v", d)
		}
	})
}

// TestVariantString: text reaches gdbus as one GVariant string literal.
func TestVariantString(t *testing.T) {
	for in, want := range map[string]string{
		"plain":              `'plain'`,
		`it's a\b`:           `'it\'s a\\b'`,
		"two\nlines\ttab":    `'two\nlines\u0009tab'`,
		"nul\x00 and \x9b":   "'nul and \ufffd'",
		"c1 \u009b esc \x1b": `'c1 \u009b esc \u001b'`,
		"⎇ émoji 🙂":          `'⎇ émoji 🙂'`,
	} {
		if got := variantString(in); got != want {
			t.Errorf("variantString(%q) = %s, want %s", in, got, want)
		}
	}
}

// TestParseID: Notify's answer gives the notification's ID.
func TestParseID(t *testing.T) {
	for out, want := range map[string]uint32{"(uint32 42,)\n": 42, "(uint32 1,)": 1, "(uint32 0,)\n": 0, "()": 0, "(uint32 99999999999,)": 0} {
		got, err := parseID(out)
		if got != want || (want == 0) != (err != nil) {
			t.Errorf("parseID(%q) = %d, %v; want %d", out, got, err, want)
		}
	}
}
