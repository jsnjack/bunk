package session

import (
	"os"
	"syscall"
	"testing"
)

// setPipeSize shrinks the buffer of the pipe w writes to (F_SETPIPE_SZ),
// so a few kilobytes fill it.
func setPipeSize(t *testing.T, w *os.File, size int) {
	t.Helper()
	rc, err := w.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	const fSetPipeSz = 1031 // F_SETPIPE_SZ
	var errno syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_FCNTL, fd, fSetPipeSz, uintptr(size))
	}); err != nil {
		t.Fatal(err)
	}
	if errno != 0 {
		t.Fatalf("F_SETPIPE_SZ: %v", errno)
	}
}
