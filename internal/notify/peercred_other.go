//go:build !linux

package notify

import (
	"errors"
	"net"
)

// peerUID needs SO_PEERCRED, which only Linux has; elsewhere every peer is
// rejected.
func peerUID(*net.UnixConn) (int, error) {
	return -1, errors.New("peer credentials: unsupported on this system")
}
