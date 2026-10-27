package main

import (
	"io"
	"os"
	"syscall"
	"testing"
)

// stderrDuring runs fn with file descriptor 2 on a pipe and returns what
// the process and its children wrote there: bunk's TUI must write nothing.
func stderrDuring(t *testing.T, fn func()) string {
	t.Helper()
	saved, err := syscall.Dup(2)
	if err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		got <- b
	}()
	if err := syscall.Dup3(int(w.Fd()), 2, 0); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := syscall.Dup3(saved, 2, 0); err != nil {
			t.Error(err)
		}
		if err := syscall.Close(saved); err != nil {
			t.Error(err)
		}
	}()
	fn()
	if err := syscall.Dup3(saved, 2, 0); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b := <-got
	if err := r.Close(); err != nil {
		t.Error(err)
	}
	return string(b)
}
