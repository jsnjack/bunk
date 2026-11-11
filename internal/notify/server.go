package notify

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"bunk/internal/notify/wire"
	"bunk/internal/session"
)

const (
	// dirName is the directory under $XDG_RUNTIME_DIR that holds the
	// sockets of every bunk front end.
	dirName = "bunk"
	// maxSocketPath is the longest path a Linux sockaddr_un holds.
	maxSocketPath = 107
	// readTimeout bounds how long one client may hold the server.
	readTimeout = time.Second
	// maxLinesPerConn caps the messages one connection may send.
	maxLinesPerConn = 16
)

// Target receives the reports for one session token. *session.Session
// implements it.
type Target interface {
	Notify(ev session.Event, message string)
}

// reportTarget is a Target that also takes a report's agent session ID.
// *session.Session implements it.
type reportTarget interface {
	NotifyReport(r session.Report)
}

// Server accepts reports on a Unix socket and passes them to the Target
// registered for their token. It handles one connection at a time, so
// reports reach their targets in the order their clients connected.
type Server struct {
	path string
	ln   *net.UnixListener
	uid  int // the only peer UID accepted
	done chan struct{}

	mu      sync.Mutex
	targets map[string]Target
	issued  map[string]bool // every token registered since Listen
	cur     *net.UnixConn   // connection being read; Close closes it
	present Presenter

	// closing ends the wait of checks (deferReply) when the server closes;
	// replies counts the checks waiting.
	closing   chan struct{}
	replies   sync.WaitGroup
	checkWait time.Duration // under mu; tests shorten it
}

// A Presenter shows the document of a present request and returns false
// when it cannot show it.
type Presenter func(r PresentRequest) bool

// PresentRequest is a present request as a Presenter gets it.
type PresentRequest struct {
	Token string // the token of the pane that sent it
	Path  string // the document, an absolute path
	// Anchor is the id of the heading to open the document at, or "".
	Anchor string
	// Auto marks a request from a hook, not from the agent or the user.
	Auto bool
	// Check is nil unless the request asked for a check: the presenter then
	// calls it once, from any goroutine, with the problems the document's
	// page had once it loaded.
	Check func(problems []string)
}

// Listen creates the socket $XDG_RUNTIME_DIR/bunk/notify-<pid>.sock
// (directory 0700, socket 0600) and starts serving it. The front end logs
// the path, as only it knows where its log lines may go.
func Listen() (*Server, error) {
	return listen(os.Getuid())
}

func listen(uid int) (*Server, error) {
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" || !filepath.IsAbs(rt) {
		return nil, fmt.Errorf("notify: XDG_RUNTIME_DIR is unset or relative: %q", rt)
	}
	dir := filepath.Join(rt, dirName)
	if err := privateDir(dir); err != nil {
		return nil, fmt.Errorf("notify: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("notify-%d.sock", os.Getpid()))
	if len(path) > maxSocketPath {
		return nil, fmt.Errorf("notify: socket path longer than %d bytes: %s", maxSocketPath, path)
	}
	// A socket left behind by a crashed process with the same PID.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSocket != 0 {
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("notify: remove stale socket: %w", err)
		}
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("notify: listen: %w", err)
	}
	// The directory already keeps other users out; this covers a looser one.
	if err := os.Chmod(path, 0o600); err != nil {
		if cerr := ln.Close(); cerr != nil {
			slog.Debug("notify: close listener", "err", cerr)
		}
		return nil, fmt.Errorf("notify: chmod socket: %w", err)
	}
	s := &Server{path: path, ln: ln, uid: uid, done: make(chan struct{}), targets: map[string]Target{}, issued: map[string]bool{},
		closing: make(chan struct{}), checkWait: checkWait}
	go s.serve()
	return s, nil
}

// privateDir creates dir with mode 0700, or checks that an existing one is
// a real directory owned by this user, and tightens its mode to 0700.
func privateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not owned by uid %d", dir, os.Getuid())
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("chmod %s: %w", dir, err)
		}
	}
	return nil
}

// Path returns the socket path, for Options.NotifySocket.
func (s *Server) Path() string { return s.path }

// Register routes reports for token to t, replacing an earlier target.
func (s *Server) Register(token string, t Target) {
	s.mu.Lock()
	s.targets[token] = t
	s.issued[token] = true
	s.mu.Unlock()
}

// Issued reports whether token was registered since Listen, even if it was
// unregistered since. It is session.Options.KnownToken for in-band reports:
// an agent in a remote tmux keeps the token of the tab it started from.
func (s *Server) Issued(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.issued[token]
}

// SetPresenter makes f show the documents of present requests (RunPresent).
// f runs on the server's goroutine while the client waits for the reply,
// so it must return quickly; a check's reply waits for its problems on a
// goroutine of its own, at most checkWait. Requests for tokens that are
// not registered never reach f. Without a presenter, as in bunk's TUI,
// every request gets PresentUnsupported.
func (s *Server) SetPresenter(f Presenter) {
	s.mu.Lock()
	s.present = f
	s.mu.Unlock()
}

// Unregister stops routing reports for token; later ones are dropped.
func (s *Server) Unregister(token string) {
	s.mu.Lock()
	delete(s.targets, token)
	s.mu.Unlock()
}

// Close stops serving and removes the socket. It waits for the connection
// being read, if any, to finish.
func (s *Server) Close() error {
	err := s.ln.Close()
	s.mu.Lock()
	if s.cur != nil {
		if cerr := s.cur.Close(); cerr != nil {
			slog.Debug("notify: close connection", "err", cerr)
		}
	}
	close(s.closing)
	s.mu.Unlock()
	<-s.done
	s.replies.Wait()
	if err != nil {
		return fmt.Errorf("notify: close: %w", err)
	}
	return nil
}

func (s *Server) serve() {
	defer close(s.done)
	for {
		c, err := s.ln.AcceptUnix()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			// EMFILE and the like: wait for descriptors instead of spinning.
			slog.Debug("notify: accept", "err", err)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		s.mu.Lock()
		s.cur = c
		s.mu.Unlock()
		kept := s.handle(c)
		s.mu.Lock()
		s.cur = nil
		s.mu.Unlock()
		if kept {
			continue // deferReply closes it
		}
		if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.Debug("notify: close connection", "err", err)
		}
	}
}

// handle reads up to maxLinesPerConn messages from one client. A present
// request gets its reply and ends the connection. It returns true when a
// check keeps the connection to reply later (deferReply).
func (s *Server) handle(c *net.UnixConn) bool {
	uid, err := peerUID(c)
	if err != nil || uid != s.uid {
		slog.Warn("notify: rejected peer", "uid", uid, "err", err)
		return false
	}
	if err := c.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		slog.Debug("notify: set deadline", "err", err)
		return false
	}
	r := bufio.NewReaderSize(c, MaxLine)
	for range maxLinesPerConn {
		line, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			slog.Debug("notify: dropped oversized message")
			return false
		}
		if len(bytes.TrimSpace(line)) > 0 {
			if reply, wait := s.dispatch(line); reply != "" {
				if wait != nil {
					return s.deferReply(c, reply, wait)
				}
				s.reply(c, reply)
				return false
			}
		}
		if err != nil {
			return false
		}
	}
	return false
}

// deferReply answers a check once the presenter has the page's problems,
// or after checkWait, on a goroutine of its own, so reports keep flowing
// meanwhile, and then closes the connection. It returns false, and the
// caller closes the connection, when the server is closing.
func (s *Server) deferReply(c *net.UnixConn, reply string, wait <-chan []string) bool {
	s.mu.Lock()
	select {
	case <-s.closing:
		s.mu.Unlock()
		return false
	default:
	}
	s.replies.Add(1)
	limit := s.checkWait
	s.mu.Unlock()
	go func() {
		defer s.replies.Done()
		var problems []string
		t := time.NewTimer(limit)
		defer t.Stop()
		select {
		case problems = <-wait:
		case <-t.C:
			problems = []string{fmt.Sprintf("the page had not finished loading after %v", limit)}
		case <-s.closing:
		}
		s.reply(c, reply+problemLines(problems))
		if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.Debug("notify: close connection", "err", err)
		}
	}()
	return true
}

// problemLines are the lines of a check's reply after the first, each
// "problem: TEXT" with control characters replaced, cut to
// maxProblemBytes, at most maxProblems.
func problemLines(problems []string) string {
	var b strings.Builder
	for i, p := range problems {
		if i == maxProblems {
			break
		}
		p = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, strings.TrimSpace(p))
		if len(p) > maxProblemBytes {
			p = strings.ToValidUTF8(p[:maxProblemBytes], "") + "…"
		}
		b.WriteString("\n" + problemPrefix + p)
	}
	return b.String()
}

// reply writes a present request's reply line.
func (s *Server) reply(c *net.UnixConn, reply string) {
	if err := c.SetWriteDeadline(time.Now().Add(readTimeout)); err != nil {
		slog.Debug("notify: set deadline", "err", err)
		return
	}
	if _, err := c.Write([]byte(reply + "\n")); err != nil {
		slog.Debug("notify: present reply", "err", err)
	}
}

// dispatch passes a report to its target, or a present request to the
// presenter. It returns the reply to a present request, else "", and for a
// check the channel its problems arrive on.
func (s *Server) dispatch(line []byte) (string, <-chan []string) {
	r, err := wire.Decode(line)
	if err != nil {
		slog.Debug("notify: dropped message", "err", err)
		return "", nil
	}
	if r.Event == wire.EventPresent {
		return s.presentRequest(r)
	}
	m, err := message(r)
	if err != nil {
		slog.Debug("notify: dropped message", "err", err)
		return "", nil
	}
	s.mu.Lock()
	t := s.targets[m.Session]
	s.mu.Unlock()
	if t == nil {
		slog.Debug("notify: dropped message for an unknown session", "event", m.Event.String())
		return "", nil
	}
	if rt, ok := t.(reportTarget); ok {
		rt.NotifyReport(session.Report{Event: m.Event, Message: m.Text, AgentSession: m.AgentSession, Held: m.Held})
		return "", nil
	}
	t.Notify(m.Event, m.Text)
	return "", nil
}

// presentRequest runs a present request and returns its reply. A request
// without a token or with a path that is not absolute and clean of
// control characters gets none. A check that the presenter shows also
// returns the channel its problems arrive on; a hook's request is never
// checked.
func (s *Server) presentRequest(r wire.Report) (string, <-chan []string) {
	if r.Session == "" || !validPresentPath(r.Path) {
		slog.Debug("notify: dropped present request", "token", r.Session != "", "path_bytes", len(r.Path))
		return "", nil
	}
	s.mu.Lock()
	known, f := s.targets[r.Session] != nil, s.present
	s.mu.Unlock()
	switch {
	case !known:
		return PresentUnknown, nil
	case f == nil:
		return PresentUnsupported, nil
	}
	var wait chan []string
	var check func([]string)
	if r.Check && !r.Auto {
		wait = make(chan []string, 1)
		check = func(p []string) {
			select {
			case wait <- p:
			default:
			}
		}
	}
	anchor := r.Anchor
	if !validAnchor(anchor) {
		anchor = ""
	}
	if !f(PresentRequest{Token: r.Session, Path: r.Path, Anchor: anchor, Auto: r.Auto, Check: check}) {
		return PresentUnsupported, nil
	}
	if wait == nil {
		return PresentShown, nil
	}
	return PresentShown, wait
}

// validAnchor reports whether anchor may name a heading: at most
// wire.MaxAnchor bytes of valid UTF-8 without control characters.
func validAnchor(anchor string) bool {
	return len(anchor) <= wire.MaxAnchor && utf8.ValidString(anchor) && !strings.ContainsFunc(anchor, unicode.IsControl)
}

// validPresentPath reports whether path may name a document to present:
// absolute, at most wire.MaxPath bytes of valid UTF-8 without control
// characters.
func validPresentPath(path string) bool {
	if !filepath.IsAbs(path) || len(path) > wire.MaxPath || !utf8.ValidString(path) {
		return false
	}
	return !strings.ContainsFunc(path, unicode.IsControl)
}
