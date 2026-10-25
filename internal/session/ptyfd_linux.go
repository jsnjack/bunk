package session

import (
	"fmt"
	"os"
	"syscall"
)

// pollable returns the PTY master f as a file served by the runtime's
// poller (epoll), so Close interrupts a Read pending in ReadLoop.
//
// creack/pty leaves the master in blocking mode: its ioctls call Fd, which
// makes a file blocking. Close cannot interrupt a blocking Read; the
// descriptor stays open until that Read returns, and a PTY Read returns
// only once every process holding the slave side has exited. A child that
// leaves a process behind (`server &`, then exit) would keep the master,
// ReadLoop and the emulator alive after the front end closed the session.
//
// pollable duplicates the descriptor, makes it non-blocking and wraps it
// with os.NewFile, which registers it with the poller. Fd on a file made
// this way does not switch it back to blocking mode. Resizes still go
// through Setsize (ptyfd.go), which holds the descriptor during the ioctl.
func pollable(f *os.File) (*os.File, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return nil, fmt.Errorf("pollable pty: %w", err)
	}
	dup, dupErr := -1, error(nil)
	if err := rc.Control(func(fd uintptr) {
		r, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_DUPFD_CLOEXEC, 0)
		if errno != 0 {
			dupErr = errno
			return
		}
		dup = int(r)
	}); err != nil {
		return nil, fmt.Errorf("pollable pty: %w", err)
	}
	if dupErr != nil {
		return nil, fmt.Errorf("pollable pty: dup: %w", dupErr)
	}
	if err := syscall.SetNonblock(dup, true); err != nil {
		if cerr := syscall.Close(dup); cerr != nil {
			L().Debug("pollable pty: close duplicate", "err", cerr)
		}
		return nil, fmt.Errorf("pollable pty: set non-blocking: %w", err)
	}
	p := os.NewFile(uintptr(dup), f.Name())
	if err := f.Close(); err != nil {
		L().Debug("pollable pty: close blocking master", "err", err)
	}
	return p, nil
}
