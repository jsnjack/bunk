package notify

import (
	"fmt"
	"net"
	"syscall"
)

// peerUID returns the UID of the process at the other end of c, from
// SO_PEERCRED: the client's for a server, the listener's for a client.
func peerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, fmt.Errorf("peer credentials: %w", err)
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return -1, fmt.Errorf("peer credentials: %w", err)
	}
	if credErr != nil {
		return -1, fmt.Errorf("peer credentials: %w", credErr)
	}
	return int(cred.Uid), nil
}
