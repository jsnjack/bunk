//go:build !linux

package session

import "os"

// pollable leaves f as it is: kqueue on the BSDs and macOS does not
// reliably poll terminal devices, so the master stays in blocking mode
// there and Close ends ReadLoop only once the PTY's other users are gone.
func pollable(f *os.File) (*os.File, error) { return f, nil }
