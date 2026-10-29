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
	"sync"
	"syscall"
	"time"

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
	s := &Server{path: path, ln: ln, uid: uid, done: make(chan struct{}), targets: map[string]Target{}, issued: map[string]bool{}}
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
	s.mu.Unlock()
	<-s.done
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
		s.handle(c)
		s.mu.Lock()
		s.cur = nil
		s.mu.Unlock()
		if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.Debug("notify: close connection", "err", err)
		}
	}
}

// handle reads up to maxLinesPerConn messages from one client.
func (s *Server) handle(c *net.UnixConn) {
	uid, err := peerUID(c)
	if err != nil || uid != s.uid {
		slog.Warn("notify: rejected peer", "uid", uid, "err", err)
		return
	}
	if err := c.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		slog.Debug("notify: set deadline", "err", err)
		return
	}
	r := bufio.NewReaderSize(c, MaxLine)
	for range maxLinesPerConn {
		line, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			slog.Debug("notify: dropped oversized message")
			return
		}
		if len(bytes.TrimSpace(line)) > 0 {
			s.dispatch(line)
		}
		if err != nil {
			return
		}
	}
}

func (s *Server) dispatch(line []byte) {
	m, err := Decode(line)
	if err != nil {
		slog.Debug("notify: dropped message", "err", err)
		return
	}
	s.mu.Lock()
	t := s.targets[m.Session]
	s.mu.Unlock()
	if t == nil {
		slog.Debug("notify: dropped message for an unknown session", "event", m.Event.String())
		return
	}
	if rt, ok := t.(reportTarget); ok {
		rt.NotifyReport(session.Report{Event: m.Event, Message: m.Text, AgentSession: m.AgentSession, Held: m.Held})
		return
	}
	t.Notify(m.Event, m.Text)
}
