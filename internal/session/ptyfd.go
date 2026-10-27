package session

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"github.com/creack/pty"
)

// Setsize sets the window size of the PTY master f, as pty.Setsize does,
// and the child gets SIGWINCH when it changed. pty.Setsize reads the
// descriptor with Fd, which does not hold it: when ReadLoop closes the PTY
// at the same time (the child exited), the ioctl races with Close and can
// reach another file that reused the number. SyscallConn's Control holds
// the descriptor for the call, or fails once f is closed.
func Setsize(f *os.File, ws *pty.Winsize) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return fmt.Errorf("pty setsize: %w", err)
	}
	var errno syscall.Errno
	err = rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(ws)))
	})
	if err != nil {
		return fmt.Errorf("pty setsize: %w", err)
	}
	if errno != 0 {
		return fmt.Errorf("pty setsize: %w", errno)
	}
	return nil
}
