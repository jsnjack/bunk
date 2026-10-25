package main

import "testing"

// Injected input must never reach the desktop session, even with a stray
// BUNK_HEADLESS in the environment.
func TestHeadlessOnlyRefusesDesktopSession(t *testing.T) {
	cases := []struct {
		name, headless, display, bus string
		ok                           bool
	}{
		{"wrapper", "1", "bunk-headless-123", "unix:path=/tmp/dbus-abc,guid=1", true},
		{"no BUNK_HEADLESS", "", "bunk-headless-123", "unix:path=/tmp/dbus-abc", false},
		{"desktop display", "1", "wayland-0", "unix:path=/tmp/dbus-abc", false},
		{"desktop bus", "1", "bunk-headless-123", "unix:path=/run/user/1000/bus", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("BUNK_HEADLESS", c.headless)
			t.Setenv("WAYLAND_DISPLAY", c.display)
			t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
			t.Setenv("DBUS_SESSION_BUS_ADDRESS", c.bus)
			if err := headlessOnly(); (err == nil) != c.ok {
				t.Fatalf("headlessOnly() = %v, want ok=%v", err, c.ok)
			}
		})
	}
}
