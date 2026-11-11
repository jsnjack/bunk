package main

import (
	"strings"
	"testing"
)

// TestLoadSettingsRailPeekDelay: rail_peek_delay takes 0 to 2000 ms and
// defaults to 200; a value out of range keeps the default and is named.
func TestLoadSettingsRailPeekDelay(t *testing.T) {
	for _, tc := range []struct {
		file string
		want int
		err  string
	}{
		{"", 200, ""},
		{"rail_peek_delay = 0", 0, ""},
		{"rail_peek_delay = 150", 150, ""},
		{"rail_peek_delay = 2000", 2000, ""},
		{"rail_peek_delay = -1", 200, "rail_peek_delay = -1 (want 0-2000 milliseconds)"},
		{"rail_peek_delay = 2001", 200, "rail_peek_delay = 2001"},
	} {
		st, err := loadSettings(writeConfig(t, tc.file+"\n"))
		if st.RailPeekDelay != tc.want || (err == nil) != (tc.err == "") || err != nil && !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%q: delay %d, error %v; want %d, %q", tc.file, st.RailPeekDelay, err, tc.want, tc.err)
		}
	}
}
