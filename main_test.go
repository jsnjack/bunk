package main

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"bunk/internal/ptychild"
	"bunk/internal/session"
)

// testClipboard records what bunk would copy to the desktop clipboard.
var testClipboard struct {
	sync.Mutex
	texts []string
}

// fastTiming shortens the session timers of panes in tests that wait for
// a foreground-process poll. Idle and the others keep their defaults.
var fastTiming = session.Timing{Poll: 20 * time.Millisecond}

// clipboardHas reports whether bunk tried to copy text to the desktop
// clipboard during the test run.
func clipboardHas(text string) bool {
	testClipboard.Lock()
	defer testClipboard.Unlock()
	return slices.Contains(testClipboard.texts, text)
}

// TestMain keeps every test away from the user's desktop clipboard.
func TestMain(m *testing.M) {
	// A pane's child that never reads its input (writer_test.go).
	if ptychild.Requested() {
		os.Exit(ptychild.Main())
	}
	// Run as "bunk" (a symlink to this binary, see bunkOnPath), the test
	// binary is the bunk command line, so panes can run "bunk notify".
	if filepath.Base(os.Args[0]) == "bunk" {
		Execute()
		os.Exit(0)
	}
	writeNativeClipboard = func(text string) bool {
		testClipboard.Lock()
		testClipboard.texts = append(testClipboard.texts, text)
		testClipboard.Unlock()
		return true
	}
	os.Exit(m.Run())
}
