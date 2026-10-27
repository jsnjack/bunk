package main

import (
	"fmt"
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

// TestMain keeps every test away from the user's desktop clipboard and
// session bus.
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
	// No test reaches the user's session bus: a desktop notification goes
	// to a bus that does not exist, through a gdbus that refuses, unless a
	// test puts a fake gdbus (internal/desktop/desktoptest) first.
	guard, err := keepOffSessionBus()
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain:", err)
		os.Exit(1)
	}
	writeNativeClipboard = func(text string) bool {
		testClipboard.Lock()
		testClipboard.texts = append(testClipboard.texts, text)
		testClipboard.Unlock()
		return true
	}
	code := m.Run()
	if err := os.RemoveAll(guard); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain:", err)
	}
	os.Exit(code)
}

// keepOffSessionBus points DBUS_SESSION_BUS_ADDRESS at a socket that does
// not exist and puts a gdbus that only fails first on PATH. It returns the
// directory holding that gdbus, to remove after the tests.
func keepOffSessionBus() (string, error) {
	dir, err := os.MkdirTemp("", "bunk-test-gdbus-")
	if err != nil {
		return "", fmt.Errorf("guard gdbus: %w", err)
	}
	guard := "#!/bin/sh\necho 'bunk tests never use the session bus' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "gdbus"), []byte(guard), 0o755); err != nil {
		return dir, fmt.Errorf("guard gdbus: %w", err)
	}
	for k, v := range map[string]string{
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=" + filepath.Join(dir, "no-bus"),
		"PATH":                     dir + string(os.PathListSeparator) + os.Getenv("PATH"),
	} {
		if err := os.Setenv(k, v); err != nil {
			return dir, fmt.Errorf("guard gdbus: %w", err)
		}
	}
	return dir, nil
}
