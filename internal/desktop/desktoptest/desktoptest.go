// Package desktoptest is a gdbus stand-in for tests of desktop
// notifications: tests never reach a real session bus through it.
package desktoptest

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// FakeGdbus is a shell script named gdbus that records each call's
// arguments and session bus, and answers as its mode says.
type FakeGdbus struct {
	Path string // the script; its directory holds nothing else named gdbus
	Dir  string // the script's directory, for PATH
}

// New writes the script into a test directory, in mode "ok". Modes:
//
//	ok    a new Notify answers with IDs 1, 2, …; a replacing one with the
//	      ID it replaces; CloseNotification with ()
//	fail  prints a D-Bus error on stderr and exits 1
//	hang  never answers
func New(t testing.TB) *FakeGdbus {
	t.Helper()
	dir := t.TempDir()
	f := &FakeGdbus{Path: filepath.Join(dir, "gdbus"), Dir: dir}
	const script = `#!/bin/sh
d=$(dirname "$0")
{ printf 'bus=%s' "$DBUS_SESSION_BUS_ADDRESS"; for a in "$@"; do printf '\037%s' "$a"; done; printf '\n'; } >> "$d/calls"
case $(cat "$d/mode" 2>/dev/null) in
fail) echo "Error: GDBus.Error:org.freedesktop.DBus.Error.ServiceUnknown: The name org.freedesktop.Notifications was not provided by any .service files" >&2; exit 1 ;;
hang) exec sleep 30 ;;
esac
# Notify's second argument, the ID to replace, comes third after --method.
i=0 r=0
for a in "$@"; do
	[ $i -gt 0 ] && i=$((i + 1)) && [ $i -eq 4 ] && r=${a#uint32 }
	[ "$a" = --method ] && i=1
done
case "$*" in
*Notifications.Notify*)
	if [ "$r" = 0 ]; then r=$(( $(cat "$d/next" 2>/dev/null || echo 0) + 1 )); echo $r > "$d/next"; fi
	echo "(uint32 $r,)" ;;
*) echo "()" ;;
esac
`
	if err := os.WriteFile(f.Path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	f.SetMode(t, "ok")
	return f
}

// SetMode switches how the fake answers.
func (f *FakeGdbus) SetMode(t testing.TB, mode string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.Dir, "mode"), []byte(mode), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Calls returns the calls so far, each "bus=ADDR" and then the arguments,
// joined by " | ".
func (f *FakeGdbus) Calls() []string {
	data, err := os.ReadFile(filepath.Join(f.Dir, "calls"))
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		out = append(out, strings.ReplaceAll(l, "\037", " | "))
	}
	return out
}

// WaitCalls waits until the fake has recorded n calls and returns them.
func (f *FakeGdbus) WaitCalls(t testing.TB, n int) []string {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		c := f.Calls()
		if len(c) >= n {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d gdbus calls, got %d: %q", n, len(c), c)
		}
	}
}

// NotifyCall is the fake's record of a Notify call on bus that replaces
// the ID replaces, with GVariant text title and body.
func NotifyCall(bus string, replaces int, title, body string) string {
	return callHead(bus, "Notify") + " | 'bunk' | uint32 " + strconv.Itoa(replaces) +
		" | 'utilities-terminal' | " + title + " | " + body + " | @as [] | @a{sv} {'urgency': <byte 1>} | int32 -1"
}

// CloseCall is the fake's record of a CloseNotification call for id.
func CloseCall(bus string, id int) string {
	return callHead(bus, "CloseNotification") + " | uint32 " + strconv.Itoa(id)
}

func callHead(bus, method string) string {
	return "bus=" + bus + " | call | --session | --timeout | 3 | --dest | org.freedesktop.Notifications | --object-path | " +
		"/org/freedesktop/Notifications | --method | org.freedesktop.Notifications." + method
}
