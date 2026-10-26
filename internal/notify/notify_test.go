package notify

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"bunk/internal/notify/wire"
	"bunk/internal/session"
)

// cliHelperEnv makes the test binary run RunCLI instead of the tests, so a
// real child process can act as an agent hook.
const cliHelperEnv = "BUNK_NOTIFY_TEST_CLI"

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if os.Getenv(cliHelperEnv) == "1" {
		os.Exit(RunCLI(os.Args[1:], os.Environ(), os.Stdin))
	}
	// The tests never write to a real terminal: the walk up the process
	// tree would reach the terminal they run in. stubInBand replaces this.
	openTerminal = func() (io.WriteCloser, error) { return nil, errNoTerminal }
	os.Exit(m.Run())
}

// stubWhere is what stubInBand's describeAgent reports.
var stubWhere = wire.Report{Agent: "claude", Host: "build01", Repo: "bunk", Branch: "main", Cwd: "/home/u/bunk"}

// stubInBand makes reportInBand write to the returned buffer, or find no
// terminal when tty is false, and describe the agent as stubWhere. Tests
// that call it must not run in parallel.
func stubInBand(t *testing.T, tty bool) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOpen, prevDescribe := openTerminal, describeAgent
	t.Cleanup(func() { openTerminal, describeAgent = prevOpen, prevDescribe })
	openTerminal = func() (io.WriteCloser, error) {
		if !tty {
			return nil, errNoTerminal
		}
		return nopCloser{&buf}, nil
	}
	describeAgent = func() wire.Report { return stubWhere }
	return &buf
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// decodeInBand returns the report in the in-band sequences written to buf,
// unwrapping tmux passthrough; ok is false when buf holds none.
func decodeInBand(t *testing.T, buf []byte) (wire.Report, bool) {
	t.Helper()
	if len(buf) == 0 {
		return wire.Report{}, false
	}
	if inner, ok := bytes.CutPrefix(buf, []byte("\x1bPtmux;")); ok {
		inner, ok = bytes.CutSuffix(inner, []byte("\x1b\\"))
		if !ok {
			t.Fatalf("tmux wrapper not closed: %q", buf)
		}
		buf = bytes.ReplaceAll(inner, []byte("\x1b\x1b"), []byte("\x1b"))
	}
	payload, ok := bytes.CutPrefix(buf, []byte("\x1b]1337;"))
	payload, ok2 := bytes.CutSuffix(payload, []byte("\a"))
	if !ok || !ok2 {
		t.Fatalf("not one in-band sequence: %q", buf)
	}
	line, ok := wire.FromUserVar(payload)
	if !ok {
		t.Fatalf("not bunk's user variable: %q", payload)
	}
	r, err := wire.Decode(line)
	if err != nil {
		t.Fatal(err)
	}
	return r, true
}

// report is one call to a fakeTarget.
type report struct {
	ev   session.Event
	text string
}

// fakeTarget records reports.
type fakeTarget chan report

func (f fakeTarget) Notify(ev session.Event, text string) { f <- report{ev, text} }

// runtimeDir returns a short private directory for XDG_RUNTIME_DIR: socket
// paths must stay under 108 bytes, which long test names would exceed.
func runtimeDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove %s: %v", dir, err)
		}
	})
	return dir
}

// startServer listens in a fresh runtime directory and registers a target.
func startServer(t *testing.T) (*Server, string, fakeTarget) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir(t))
	srv, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	target := make(fakeTarget, 64)
	srv.Register(token, target)
	return srv, token, target
}

// next returns the next report, failing after 5 s.
func next(t *testing.T, ch fakeTarget) report {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no report arrived")
		return report{}
	}
}

// expectEmpty fails when a report is waiting.
func expectEmpty(t *testing.T, ch fakeTarget) {
	t.Helper()
	select {
	case r := <-ch:
		t.Fatalf("unexpected report %+v", r)
	default:
	}
}

// sendRaw writes data, half-closes the connection and waits until the
// server closes its end: by then the server has handled every line, so the
// caller can check the target without waiting.
func sendRaw(t *testing.T, path string, data []byte) {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	c := conn.(*net.UnixConn)
	defer func() {
		if err := c.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	// A server that rejects the client may close before reading it all.
	peerClosed := func(err error) bool {
		return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ENOTCONN)
	}
	if _, err := c.Write(data); err != nil && !peerClosed(err) {
		t.Fatal(err)
	}
	if err := c.CloseWrite(); err != nil && !peerClosed(err) {
		t.Fatal(err)
	}
	if err := c.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, c); err != nil && !peerClosed(err) {
		t.Fatalf("waiting for the server to close: %v", err)
	}
}

func TestEncodeDecode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		m    Message
		line string
	}{
		{"working", Message{Session: "tok", Event: session.EventWorking}, `{"v":1,"session":"tok","event":"working"}` + "\n"},
		{"needs-you", Message{Session: "tok", Event: session.EventNeedsYou, Text: "Allow <npm> & co?"},
			`{"v":1,"session":"tok","event":"needs-you","message":"Allow <npm> & co?"}` + "\n"},
		{"cleaned text", Message{Session: "tok", Event: session.EventDone, Text: " a\tb\x00\xff "},
			`{"v":1,"session":"tok","event":"done","message":"a b ` + "�" + `"}` + "\n"},
		{"clear", Message{Session: "tok", Event: session.EventClear}, `{"v":1,"session":"tok","event":"clear"}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line, err := tc.m.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if string(line) != tc.line {
				t.Fatalf("Encode = %q, want %q", line, tc.line)
			}
			m, err := Decode(line)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.m
			want.Text = wire.CleanText(want.Text)
			if m != want {
				t.Fatalf("Decode = %+v, want %+v", m, want)
			}
		})
	}
}

func TestEncodeRejects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		m    Message
	}{
		{"no token", Message{Event: session.EventIdle}},
		{"long token", Message{Session: strings.Repeat("a", wire.MaxToken+1), Event: session.EventIdle}},
		{"no event", Message{Session: "tok"}},
		{"unknown event", Message{Session: "tok", Event: session.Event(99)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if line, err := tc.m.Encode(); err == nil {
				t.Fatalf("Encode = %q, want an error", line)
			}
		})
	}
	// The longest text, escaped at twice its size, still fits one line.
	m := Message{Session: strings.Repeat("f", wire.MaxToken), Event: session.EventNeedsYou, Text: strings.Repeat(`"`, 5000)}
	line, err := m.Encode()
	if err != nil || len(line) > MaxLine {
		t.Fatalf("worst-case text: %d bytes, %v", len(line), err)
	}
}

func TestDecodeRejects(t *testing.T) {
	t.Parallel()
	pad := func(n int) string {
		head := `{"v":1,"session":"tok","event":"idle"`
		return head + strings.Repeat(" ", n-len(head)-1) + "}"
	}
	for _, tc := range []struct {
		name string
		line string
	}{
		{"empty", ""},
		{"not JSON", "working tok"},
		{"array", `["working"]`},
		{"null", "null"},
		{"no version", `{"session":"tok","event":"working"}`},
		{"version 2", `{"v":2,"session":"tok","event":"working"}`},
		{"unknown event", `{"v":1,"session":"tok","event":"waiting"}`},
		{"event as number", `{"v":1,"session":"tok","event":1}`},
		{"no token", `{"v":1,"event":"working"}`},
		{"long token", `{"v":1,"session":"` + strings.Repeat("a", wire.MaxToken+1) + `","event":"working"}`},
		{"trailing data", `{"v":1,"session":"tok","event":"working"} {}`},
		{"two lines", "{\"v\":1,\"session\":\"tok\",\"event\":\"working\"}\n{}"},
		{"too long", pad(MaxLine)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if m, err := Decode([]byte(tc.line)); err == nil {
				t.Fatalf("Decode(%.60q) = %+v, want an error", tc.line, m)
			}
		})
	}
	if _, err := Decode([]byte(pad(MaxLine-1) + "\n")); err != nil {
		t.Fatalf("a line of exactly MaxLine bytes: %v", err)
	}
	m, err := Decode([]byte(`{"v":1,"session":"tok","event":"done","message":"ok","agent":"future field"}`))
	if err != nil || m.Text != "ok" {
		t.Fatalf("unknown fields must be ignored: %+v, %v", m, err)
	}
}

func TestNewToken(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	hex := regexp.MustCompile(`^[0-9a-f]{32}$`)
	for range 100 {
		tok, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if !hex.MatchString(tok) || seen[tok] {
			t.Fatalf("token %q: not 32 hex digits, or repeated", tok)
		}
		seen[tok] = true
	}
}

// TestEnvNamesSurviveRedaction: Gemini CLI removes variables whose names
// match these patterns from its hooks' environment
// (packages/core/src/services/environmentSanitization.ts).
func TestEnvNamesSurviveRedaction(t *testing.T) {
	t.Parallel()
	redacted := regexp.MustCompile(`(?i)TOKEN|SECRET|PASSWORD|PASSWD|KEY|AUTH|CREDENTIAL|CREDS|PRIVATE|CERT`)
	for _, name := range []string{EnvSocket, EnvSession} {
		if redacted.MatchString(name) {
			t.Errorf("%s would be stripped from Gemini CLI hooks", name)
		}
	}
}

// FuzzDecode: Decode never panics, accepts only valid messages, and what it
// accepts survives Encode and Decode again.
func FuzzDecode(f *testing.F) {
	for _, seed := range []string{
		`{"v":1,"session":"0123456789abcdef0123456789abcdef","event":"needs-you","message":"Allow npm install?"}` + "\n",
		`{"v":1,"session":"t","event":"working"}`,
		`{"v":1,"session":"t","event":"done","message":"a\u0000b c"}`,
		`{"v":1,"session":"\u0000","event":"clear","message":"` + strings.Repeat(`\"`, 1500) + `"}`,
		`{"V":1,"SESSION":"t","Event":"idle"}`,
		`{"v":1,"session":"t","event":"failed","message":"\xff\xfe"}`,
		`{"v":1.0,"session":"t","event":"idle"}`,
		`[]`, `null`, ``, "\n", `{"v":1`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		m, err := Decode(line)
		if err != nil {
			return
		}
		if len(bytes.TrimSuffix(line, []byte("\n"))) >= MaxLine {
			t.Fatalf("accepted %d bytes", len(line))
		}
		if _, ok := session.ParseEvent(m.Event.String()); !ok {
			t.Fatalf("accepted event %v", m.Event)
		}
		if m.Session == "" || len(m.Session) > wire.MaxToken || !utf8.ValidString(m.Session) || !utf8.ValidString(m.Text) {
			t.Fatalf("accepted %+v", m)
		}
		enc, err := m.Encode()
		if err != nil {
			t.Fatalf("re-encode %+v: %v", m, err)
		}
		again, err := Decode(enc)
		want := Message{Session: m.Session, Event: m.Event, Text: wire.CleanText(m.Text)}
		if err != nil || again != want {
			t.Fatalf("round trip: %+v, %v; want %+v", again, err, want)
		}
	})
}
