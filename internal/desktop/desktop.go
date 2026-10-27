// Package desktop shows desktop notifications on org.freedesktop.Notifications
// through the gdbus command of GLib, for bunk's TUI, which has no D-Bus
// connection of its own. A Notifier keeps one notification per key (a
// pane): a newer alert replaces it, and Withdraw and Close remove it.
//
// Calls run on one goroutine at a time, never on the caller's: the TUI
// asks from its render loop. gdbus's output goes to buffers, never to the
// terminal. A call that fails is logged at debug level, and a Notify that
// fails runs Fallback, so the alert can still reach the user another way.
// A call that times out runs no Fallback: the server may have shown it.
package desktop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"bunk/internal/alert"
)

// The notification server's D-Bus name and object.
const (
	serverName = "org.freedesktop.Notifications"
	serverPath = "/org/freedesktop/Notifications"
)

// SessionBus returns the address of the session bus desktop notifications
// may go to, or "" and why not. Over SSH (SSH_CONNECTION) a bus, if any,
// belongs to the remote machine, not the desktop the user sits at. Without
// DBUS_SESSION_BUS_ADDRESS, the bus is $XDG_RUNTIME_DIR/bus when that
// socket exists, as GLib and libdbus find it. getenv and isSocket are
// os.Getenv and IsSocket outside tests.
func SessionBus(getenv func(string) string, isSocket func(string) bool) (addr, why string) {
	if getenv("SSH_CONNECTION") != "" {
		return "", "running over SSH"
	}
	if a := getenv("DBUS_SESSION_BUS_ADDRESS"); a != "" {
		return a, ""
	}
	if dir := getenv("XDG_RUNTIME_DIR"); dir != "" {
		if p := filepath.Join(dir, "bus"); isSocket(p) {
			return "unix:path=" + escapeAddress(p), ""
		}
	}
	return "", "no session bus"
}

// IsSocket reports whether path is a Unix socket.
func IsSocket(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

// escapeAddress escapes a value for a D-Bus address: bytes other than
// [-0-9A-Za-z_/.\*] become %XX.
func escapeAddress(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', strings.IndexByte("-_/.\\*", c) >= 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02x", c)
		}
	}
	return b.String()
}

// DefaultTimeout bounds one gdbus call when Notifier.Timeout is zero.
const DefaultTimeout = 3 * time.Second

// Notifier keeps one desktop notification per key on the notification
// server. Set its fields before the first Send.
type Notifier struct {
	Gdbus   string        // the gdbus executable
	Address string        // the session bus (SessionBus)
	AppName string        // shown as the notification's source
	Icon    string        // a themed icon name
	Timeout time.Duration // per call; zero is DefaultTimeout
	Log     *slog.Logger  // debug records; nil discards them
	// Fallback shows an alert another way after its Notify call failed.
	// It runs on the notifier's goroutine.
	Fallback func(key int, a alert.Alert)

	mu       sync.Mutex
	pending  map[int]request // the newest request of each key not yet run
	queue    []int           // keys with a pending request, oldest first
	ids      map[int]uint32  // the server's ID of each key's notification
	running  bool            // a goroutine runs the queue
	inFlight *int            // the key whose request runs now
	drained  chan struct{}   // closed when that goroutine ends
	closed   bool            // Close ran: no more requests
}

// request is what a key's notification should become.
type request struct {
	withdraw bool
	alert    alert.Alert
}

// Send shows a as key's notification, replacing the one it has.
func (n *Notifier) Send(key int, a alert.Alert) { n.request(key, request{alert: a}) }

// Withdraw removes key's notification.
func (n *Notifier) Withdraw(key int) { n.request(key, request{withdraw: true}) }

func (n *Notifier) request(key int, r request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.closed {
		n.put(key, r)
	}
}

// put queues r for key, replacing a request of key that has not run, and
// starts the goroutine when none runs. It requires n.mu.
func (n *Notifier) put(key int, r request) {
	if n.pending == nil {
		n.pending, n.ids = map[int]request{}, map[int]uint32{}
	}
	if _, ok := n.pending[key]; !ok {
		n.queue = append(n.queue, key)
	}
	n.pending[key] = r
	if !n.running {
		n.running, n.drained = true, make(chan struct{})
		go n.run(n.drained)
	}
}

// Close withdraws every notification and takes no more requests. It waits
// up to wait for the calls to end.
func (n *Notifier) Close(wait time.Duration) {
	n.mu.Lock()
	n.closed = true
	for key := range n.ids {
		n.put(key, request{withdraw: true})
	}
	if n.inFlight != nil {
		// Its Notify may be showing a notification right now.
		n.put(*n.inFlight, request{withdraw: true})
	}
	for key := range n.pending {
		n.pending[key] = request{withdraw: true}
	}
	running, drained := n.running, n.drained
	n.mu.Unlock()
	if !running {
		return
	}
	select {
	case <-drained:
	case <-time.After(wait):
		n.log().Debug("desktop notifications: calls still running at close", "waited", wait)
	}
}

// run makes the queued calls in order until the queue is empty.
func (n *Notifier) run(drained chan struct{}) {
	defer close(drained)
	for {
		n.mu.Lock()
		if len(n.queue) == 0 {
			n.running = false
			n.mu.Unlock()
			return
		}
		key := n.queue[0]
		n.queue = n.queue[1:]
		n.inFlight = &key
		r := n.pending[key]
		delete(n.pending, key)
		id := n.ids[key]
		n.mu.Unlock()
		if r.withdraw {
			n.closeNote(key, id)
		} else {
			n.notify(key, id, r.alert)
		}
		n.mu.Lock()
		n.inFlight = nil
		n.mu.Unlock()
	}
}

// notify shows a as key's notification, replacing the server's
// notification replaces (0 for none).
func (n *Notifier) notify(key int, replaces uint32, a alert.Alert) {
	title, body := alert.DesktopText(a)
	out, err := n.call("Notify", variantString(n.AppName), "uint32 "+strconv.FormatUint(uint64(replaces), 10),
		variantString(n.Icon), variantString(title), variantString(body),
		"@as []", "@a{sv} {'urgency': <byte 1>}", "int32 -1")
	var id uint32
	if err == nil {
		id, err = parseID(out)
	}
	n.setID(key, id)
	if err == nil {
		return
	}
	n.log().Debug("desktop notification not shown", "key", key, "err", err)
	if !errors.Is(err, errTimeout) && n.Fallback != nil {
		n.Fallback(key, a)
	}
}

// closeNote removes the server's notification id of key.
func (n *Notifier) closeNote(key int, id uint32) {
	n.setID(key, 0)
	if id == 0 {
		return
	}
	if _, err := n.call("CloseNotification", "uint32 "+strconv.FormatUint(uint64(id), 10)); err != nil {
		n.log().Debug("desktop notification not withdrawn", "key", key, "err", err)
	}
}

func (n *Notifier) setID(key int, id uint32) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if id == 0 {
		delete(n.ids, key)
	} else {
		n.ids[key] = id
	}
}

func (n *Notifier) log() *slog.Logger {
	if n.Log != nil {
		return n.Log
	}
	return slog.New(slog.DiscardHandler)
}

// errTimeout marks a call whose answer did not come in time.
var errTimeout = errors.New("no answer in time")

// call runs gdbus for one method of the notification server and returns
// what it printed. Arguments are GVariant text, typed so that they parse
// without introspection.
func (n *Notifier) call(method string, args ...string) (string, error) {
	timeout := n.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	// gdbus gives up after its own timeout; the context is a backstop.
	ctx, cancel := context.WithTimeout(context.Background(), timeout+time.Second)
	defer cancel()
	argv := append([]string{"call", "--session", "--timeout", strconv.Itoa(int((timeout + time.Second - 1) / time.Second)),
		"--dest", serverName, "--object-path", serverPath, "--method", serverName + "." + method}, args...)
	cmd := exec.CommandContext(ctx, n.Gdbus, argv...)
	cmd.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS="+n.Address)
	cmd.WaitDelay = time.Second // a child gdbus left behind cannot hold the output open
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	switch {
	case ctx.Err() != nil:
		return "", fmt.Errorf("gdbus %s: %w", method, errTimeout)
	case err != nil && strings.Contains(stderr.String(), "Timeout was reached"):
		return "", fmt.Errorf("gdbus %s: %w: %s", method, errTimeout, strings.TrimSpace(stderr.String()))
	case err != nil:
		return "", fmt.Errorf("gdbus %s: %w: %s", method, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// idReply is gdbus's print of Notify's answer, e.g. "(uint32 42,)".
var idReply = regexp.MustCompile(`^\(uint32 (\d+),\)\s*$`)

func parseID(out string) (uint32, error) {
	m := idReply.FindStringSubmatch(out)
	if m == nil {
		return 0, fmt.Errorf("gdbus Notify: unexpected answer %q", out)
	}
	id, err := strconv.ParseUint(m[1], 10, 32)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("gdbus Notify: notification ID %q", m[1])
	}
	return uint32(id), nil
}

// variantString writes s as a GVariant text string: quoted, with quotes,
// backslashes and control characters escaped.
func variantString(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range strings.ToValidUTF8(s, "�") {
		switch {
		case r == '\'' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == 0:
		case r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String()
}
