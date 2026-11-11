package notify

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"bunk/internal/notify/wire"
)

// clientTimeout bounds connecting, writing and waiting for a present
// reply, so a stuck server cannot stall an agent's hook.
const clientTimeout = 2 * time.Second

// The replies to a present request.
const (
	// PresentShown: the front end shows the document.
	PresentShown = "shown"
	// PresentUnsupported: the front end has no side panel (bunk's TUI), or
	// predates present requests and dropped the line without a reply.
	PresentUnsupported = "unsupported"
	// PresentUnknown: no session has the request's token.
	PresentUnknown = "unknown"
)

// maxReply caps the reply a client reads.
const maxReply = 64

// checkWait is how long a front end waits for a checked document's page to
// load before it replies; a client of a check waits checkSlack longer.
const (
	checkWait  = 5 * time.Second
	checkSlack = 2 * time.Second
)

// The lines of a check's reply after the first: one per problem, at most
// maxProblems of at most maxProblemBytes.
const (
	problemPrefix   = "problem: "
	maxProblems     = 12
	maxProblemBytes = 300
)

// Send delivers m to the server listening on socket. It checks that the
// server runs as this user and does not wait for a reply.
func Send(socket string, m Message) error {
	return send(socket, m, os.Getuid())
}

func send(socket string, m Message, uid int) (err error) {
	line, err := m.Encode()
	if err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	c, err := connect(socket, uid)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := c.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("notify: close: %w", cerr)
		}
	}()
	if _, err := c.Write(line); err != nil {
		return fmt.Errorf("notify: send: %w", err)
	}
	return nil
}

// Present asks the server listening on socket to show the document at
// path, an absolute path, in the session of token, and returns its reply:
// PresentShown, PresentUnsupported or PresentUnknown. A server that closes
// the connection without a reply, as one that predates present requests
// does, counts as PresentUnsupported. auto marks a request from a hook.
func Present(socket, token, path string, auto bool) (string, error) {
	return presentAs(socket, token, path, auto, os.Getuid())
}

func presentAs(socket, token, path string, auto bool, uid int) (reply string, err error) {
	reply, _, err = presentRequest(socket, wire.Report{Session: token, Event: wire.EventPresent, Path: path, Auto: auto}, uid)
	return reply, err
}

// PresentCheck is Present for the agent or the user asking: the front end
// replies once the document's page has loaded, or after a few seconds,
// with the problems it had, such as a diagram that does not parse, a
// script error or a file the page could not load. A front end that does
// not check replies at once, without problems.
func PresentCheck(socket, token, path string) (reply string, problems []string, err error) {
	return presentRequest(socket, wire.Report{Session: token, Event: wire.EventPresent, Path: path, Check: true}, os.Getuid())
}

func presentRequest(socket string, r wire.Report, uid int) (reply string, problems []string, err error) {
	if r.Session == "" {
		return "", nil, fmt.Errorf("present: %w", errToken)
	}
	line, err := r.Encode()
	if err != nil {
		return "", nil, fmt.Errorf("present: %w", err)
	}
	c, err := connect(socket, uid)
	if err != nil {
		return "", nil, err
	}
	defer func() {
		if cerr := c.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("present: close: %w", cerr)
		}
	}()
	if _, err := c.Write(line); err != nil {
		return "", nil, fmt.Errorf("present: send: %w", err)
	}
	// An older server reads on until the end of input or its deadline.
	if err := c.CloseWrite(); err != nil {
		return "", nil, fmt.Errorf("present: send: %w", err)
	}
	timeout, limit := clientTimeout, int64(maxReply)
	if r.Check {
		timeout, limit = checkWait+checkSlack, int64(maxReply+maxProblems*(len(problemPrefix)+maxProblemBytes+len("…")+1))
	}
	if err := c.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return "", nil, fmt.Errorf("present: set deadline: %w", err)
	}
	in := bufio.NewReader(io.LimitReader(c, limit))
	got, err := in.ReadString('\n')
	switch {
	case errors.Is(err, io.EOF) && got == "":
		return PresentUnsupported, nil, nil
	case err != nil && !errors.Is(err, io.EOF):
		return "", nil, fmt.Errorf("present: read reply: %w", err)
	}
	for err == nil {
		var l string
		l, err = in.ReadString('\n')
		if p, ok := strings.CutPrefix(strings.TrimRight(l, "\n"), problemPrefix); ok {
			problems = append(problems, p)
		}
	}
	return strings.TrimSpace(got), problems, nil
}

// connect dials the server on socket and checks that it runs as uid. The
// connection has a write deadline of clientTimeout.
func connect(socket string, uid int) (*net.UnixConn, error) {
	conn, err := net.DialTimeout("unix", socket, clientTimeout)
	if err != nil {
		return nil, fmt.Errorf("notify: connect: %w", err)
	}
	c, ok := conn.(*net.UnixConn)
	if !ok {
		closeQuietly(conn)
		return nil, fmt.Errorf("notify: %s is not a Unix socket", socket)
	}
	got, err := peerUID(c)
	if err != nil {
		closeQuietly(c)
		return nil, fmt.Errorf("notify: %w", err)
	}
	if got != uid {
		closeQuietly(c)
		return nil, fmt.Errorf("notify: server runs as uid %d, not %d", got, uid)
	}
	if err := c.SetWriteDeadline(time.Now().Add(clientTimeout)); err != nil {
		closeQuietly(c)
		return nil, fmt.Errorf("notify: set deadline: %w", err)
	}
	return c, nil
}

// closeQuietly closes a connection whose use already failed.
func closeQuietly(c io.Closer) {
	if err := c.Close(); err != nil {
		slog.Debug("notify: close", "err", err)
	}
}

// serverGone reports whether err means that the front end which exported
// the socket has exited: the socket is missing or nobody listens on it.
func serverGone(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}
