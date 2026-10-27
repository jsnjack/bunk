//go:build !linux

package main

import "testing"

// stderrDuring runs fn; only Linux test runs capture descriptor 2.
func stderrDuring(_ *testing.T, fn func()) string {
	fn()
	return ""
}
