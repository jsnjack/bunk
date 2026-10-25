package notify

import (
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"bunk/internal/session"
)

// TestRoundTrip sends each event through a real socket and checks what the
// registered target receives.
func TestRoundTrip(t *testing.T) {
	srv, token, target := startServer(t)
	for _, tc := range []struct {
		name string
		ev   session.Event
		text string
	}{
		{"working", session.EventWorking, ""},
		{"idle", session.EventIdle, ""},
		{"needs-you", session.EventNeedsYou, "Claude needs your permission to use Bash"},
		{"done", session.EventDone, "All 212 tests pass."},
		{"failed", session.EventFailed, "API Error: Rate limit reached"},
		{"clear", session.EventClear, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Send(srv.Path(), Message{Session: token, Event: tc.ev, Text: tc.text}); err != nil {
				t.Fatal(err)
			}
			if got := next(t, target); got != (report{tc.ev, tc.text}) {
				t.Fatalf("target got %+v, want %v %q", got, tc.ev, tc.text)
			}
		})
	}
}

// TestPermissions: the socket directory is 0700 and the socket 0600, both
// owned by this user, even when the directory existed with a looser mode.
func TestPermissions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, dir string)
	}{
		{"new directory", func(*testing.T, string) {}},
		{"existing 0755 directory", func(t *testing.T, dir string) {
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"stale socket with our PID", func(t *testing.T, dir string) {
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, fmt.Sprintf("notify-%d.sock", os.Getpid())), Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			ln.SetUnlinkOnClose(false)
			if err := ln.Close(); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := runtimeDir(t)
			t.Setenv("XDG_RUNTIME_DIR", rt)
			dir := filepath.Join(rt, dirName)
			tc.prepare(t, dir)
			srv, err := Listen()
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := srv.Close(); err != nil {
					t.Error(err)
				}
			}()
			if filepath.Dir(srv.Path()) != dir {
				t.Fatalf("socket %s is not in %s", srv.Path(), dir)
			}
			for _, c := range []struct {
				path string
				mode fs.FileMode
				kind fs.FileMode
			}{
				{dir, 0o700, fs.ModeDir},
				{srv.Path(), 0o600, fs.ModeSocket},
			} {
				fi, err := os.Lstat(c.path)
				if err != nil {
					t.Fatal(err)
				}
				uid := int(fi.Sys().(*syscall.Stat_t).Uid)
				if fi.Mode().Perm() != c.mode || fi.Mode().Type() != c.kind || uid != os.Getuid() {
					t.Errorf("%s: mode %v uid %d; want %v%v uid %d", c.path, fi.Mode(), uid, c.kind, c.mode, os.Getuid())
				}
			}
		})
	}
}

// TestListenRefuses: no socket is created in an unsafe or unusable place.
func TestListenRefuses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runtime func(t *testing.T) string
		wantErr string
	}{
		{"XDG_RUNTIME_DIR unset", func(*testing.T) string { return "" }, "XDG_RUNTIME_DIR"},
		{"relative XDG_RUNTIME_DIR", func(*testing.T) string { return "run/user" }, "XDG_RUNTIME_DIR"},
		{"symlinked directory", func(t *testing.T) string {
			rt := runtimeDir(t)
			if err := os.Symlink(t.TempDir(), filepath.Join(rt, dirName)); err != nil {
				t.Fatal(err)
			}
			return rt
		}, "not a directory"},
		{"file in place of the directory", func(t *testing.T) string {
			rt := runtimeDir(t)
			if err := os.WriteFile(filepath.Join(rt, dirName), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return rt
		}, "not a directory"},
		{"socket path too long", func(t *testing.T) string {
			rt := filepath.Join(runtimeDir(t), strings.Repeat("d", maxSocketPath))
			if err := os.Mkdir(rt, 0o700); err != nil {
				t.Fatal(err)
			}
			return rt
		}, "longer than"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", tc.runtime(t))
			srv, err := Listen()
			if err == nil {
				if cerr := srv.Close(); cerr != nil {
					t.Error(cerr)
				}
				t.Fatalf("Listen succeeded at %s", srv.Path())
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestPeerUID: SO_PEERCRED names this user on both ends; a server that
// expects another UID drops the message, and a client refuses a server
// running as another UID.
func TestPeerUID(t *testing.T) {
	srv, token, target := startServer(t)
	conn, err := net.Dial("unix", srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	uid, err := peerUID(conn.(*net.UnixConn))
	if cerr := conn.Close(); cerr != nil {
		t.Error(cerr)
	}
	if err != nil || uid != os.Getuid() {
		t.Fatalf("peerUID = %d, %v; want %d", uid, err, os.Getuid())
	}

	err = send(srv.Path(), Message{Session: token, Event: session.EventWorking}, os.Getuid()+1)
	if err == nil || !strings.Contains(err.Error(), "server runs as uid") {
		t.Fatalf("client accepted a server of another uid: %v", err)
	}
	sendRaw(t, srv.Path(), line(t, token, session.EventIdle))
	if got := next(t, target); got.ev != session.EventIdle {
		t.Fatalf("the refused client's message arrived: %+v", got)
	}

	// A server that only accepts another UID.
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir(t))
	strict, err := listen(os.Getuid() + 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := strict.Close(); err != nil {
			t.Error(err)
		}
	}()
	strictTarget := make(fakeTarget, 4)
	strict.Register(token, strictTarget)
	sendRaw(t, strict.Path(), line(t, token, session.EventNeedsYou))
	expectEmpty(t, strictTarget)
}

// line encodes one valid message.
func line(t *testing.T, token string, ev session.Event) []byte {
	t.Helper()
	b, err := Message{Session: token, Event: ev}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestServerDropsBadInput: oversized, malformed and unknown-session lines
// are dropped, and the server keeps serving.
func TestServerDropsBadInput(t *testing.T) {
	srv, token, target := startServer(t)
	exact := func(n int, tail string) []byte {
		head := fmt.Sprintf(`{"v":1,"session":%q,"event":"needs-you","message":"x"`, token)
		return []byte(head + strings.Repeat(" ", n-len(head)-len(tail)) + tail)
	}
	other, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
		want []report
	}{
		{"exactly MaxLine with newline", exact(MaxLine, "}\n"), []report{{session.EventNeedsYou, "x"}}},
		{"one byte over MaxLine", exact(MaxLine+1, "}\n"), nil},
		{"oversized line, then a valid one", append(exact(MaxLine+100, "}\n"), line(t, token, session.EventDone)...), nil},
		{"endless line", []byte(strings.Repeat("x", 3*MaxLine)), nil},
		{"not JSON", []byte("needs-you please\n"), nil},
		{"wrong version", fmt.Appendf(nil, `{"v":9,"session":%q,"event":"idle"}`+"\n", token), nil},
		{"unknown event", fmt.Appendf(nil, `{"v":1,"session":%q,"event":"waiting"}`+"\n", token), nil},
		{"unknown session", line(t, other, session.EventFailed), nil},
		{"malformed line, then a valid one", append([]byte("{\n"), line(t, token, session.EventWorking)...), []report{{session.EventWorking, ""}}},
		{"no trailing newline", line(t, token, session.EventIdle)[:len(line(t, token, session.EventIdle))-1], []report{{session.EventIdle, ""}}},
		{"blank lines", []byte("\n\n  \n"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sendRaw(t, srv.Path(), tc.data)
			for _, want := range tc.want {
				if got := next(t, target); got != want {
					t.Fatalf("got %+v, want %+v", got, want)
				}
			}
			expectEmpty(t, target)
		})
	}
	// Still serving.
	if err := Send(srv.Path(), Message{Session: token, Event: session.EventClear}); err != nil {
		t.Fatal(err)
	}
	if got := next(t, target); got.ev != session.EventClear {
		t.Fatalf("after bad input: %+v", got)
	}
}

// TestServerLimitsLinesPerConnection: one connection delivers at most
// maxLinesPerConn messages.
func TestServerLimitsLinesPerConnection(t *testing.T) {
	srv, token, target := startServer(t)
	var data []byte
	for range maxLinesPerConn + 5 {
		data = append(data, line(t, token, session.EventWorking)...)
	}
	sendRaw(t, srv.Path(), data)
	if len(target) != maxLinesPerConn {
		t.Fatalf("%d reports from one connection, want %d", len(target), maxLinesPerConn)
	}
}

// TestRegisterUnregister: reports follow the current registration.
func TestRegisterUnregister(t *testing.T) {
	srv, token, target := startServer(t)
	sendRaw(t, srv.Path(), line(t, token, session.EventWorking))
	if got := next(t, target); got.ev != session.EventWorking {
		t.Fatalf("got %+v", got)
	}
	srv.Unregister(token)
	sendRaw(t, srv.Path(), line(t, token, session.EventDone))
	expectEmpty(t, target)

	replacement := make(fakeTarget, 4)
	srv.Register(token, replacement)
	sendRaw(t, srv.Path(), line(t, token, session.EventFailed))
	expectEmpty(t, target)
	if got := next(t, replacement); got.ev != session.EventFailed {
		t.Fatalf("replacement got %+v", got)
	}
}

// TestReportsKeepOrder: reports from successive hook runs arrive in the
// order the hooks ran, as with Stop followed by SessionEnd.
func TestReportsKeepOrder(t *testing.T) {
	srv, token, target := startServer(t)
	events := []session.Event{session.EventWorking, session.EventNeedsYou, session.EventWorking, session.EventDone, session.EventClear}
	for i := range 4 {
		for _, ev := range events {
			if err := Send(srv.Path(), Message{Session: token, Event: ev, Text: fmt.Sprint(i)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := range 4 {
		for _, ev := range events {
			if got, want := next(t, target), (report{ev, fmt.Sprint(i)}); got != want {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		}
	}
}

// TestCloseRemovesSocket: Close unlinks the socket, so later hooks see the
// front end as gone.
func TestCloseRemovesSocket(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir(t))
	srv, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(srv.Path()); !os.IsNotExist(err) {
		t.Fatalf("socket still exists: %v", err)
	}
	err = Send(srv.Path(), Message{Session: "tok", Event: session.EventIdle})
	if !serverGone(err) {
		t.Fatalf("Send after Close: %v, want a gone server", err)
	}
}
