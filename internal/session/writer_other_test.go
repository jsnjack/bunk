//go:build !linux

package session

import (
	"os"
	"testing"
)

// setPipeSize keeps the pipe's own size where F_SETPIPE_SZ is missing.
func setPipeSize(*testing.T, *os.File, int) {}
