package notify

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

// clientTimeout bounds connecting and writing, so a stuck server cannot
// stall an agent's hook.
const clientTimeout = 2 * time.Second

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
	conn, err := net.DialTimeout("unix", socket, clientTimeout)
	if err != nil {
		return fmt.Errorf("notify: connect: %w", err)
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("notify: close: %w", cerr)
		}
	}()
	c, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("notify: %s is not a Unix socket", socket)
	}
	got, err := peerUID(c)
	if err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	if got != uid {
		return fmt.Errorf("notify: server runs as uid %d, not %d", got, uid)
	}
	if err := c.SetWriteDeadline(time.Now().Add(clientTimeout)); err != nil {
		return fmt.Errorf("notify: set deadline: %w", err)
	}
	if _, err := c.Write(line); err != nil {
		return fmt.Errorf("notify: send: %w", err)
	}
	return nil
}

// serverGone reports whether err means that the front end which exported
// the socket has exited: the socket is missing or nobody listens on it.
func serverGone(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}
