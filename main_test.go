package main

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

// testClipboard records what bunk would copy to the desktop clipboard.
var testClipboard struct {
	sync.Mutex
	texts []string
}

// clipboardHas reports whether bunk tried to copy text to the desktop
// clipboard during the test run.
func clipboardHas(text string) bool {
	testClipboard.Lock()
	defer testClipboard.Unlock()
	return slices.Contains(testClipboard.texts, text)
}

// TestMain keeps every test away from the user's desktop clipboard.
func TestMain(m *testing.M) {
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
