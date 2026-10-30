package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSettingsNewDir(t *testing.T) {
	for _, tc := range []struct{ file, want, err string }{
		{"", "~", ""},
		{`new_dir = "current"`, "current", ""},
		{`new_dir = "~/workspace"`, "~/workspace", ""},
		{`new_dir = "/srv"`, "/srv", ""},
		{`new_dir = "workspace"`, "~", `new_dir = "workspace"`},
		{`new_dir = "~bob"`, "~", `new_dir = "~bob"`},
	} {
		st, err := loadSettings(writeConfig(t, tc.file+"\n"))
		if st.NewDir != tc.want || (err == nil) != (tc.err == "") || err != nil && !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%q: new_dir %q, error %v; want %q, %q", tc.file, st.NewDir, err, tc.want, tc.err)
		}
	}
}

// TestNewDir: new_dir resolves under $HOME, and a directory that is gone
// gives the home directory.
func TestNewDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	must(t, os.Mkdir(filepath.Join(home, "workspace"), 0o700))
	other := t.TempDir()
	for _, tc := range []struct{ setting, want string }{
		{"", home},
		{"~", home},
		{"~/workspace", filepath.Join(home, "workspace")},
		{"~/missing", home},
		{other, other},
		{filepath.Join(other, "missing"), home},
		{"current", home}, // no active pane
	} {
		if got := newDir(tc.setting, nil); got != tc.want {
			t.Errorf("newDir(%q) = %q, want %q", tc.setting, got, tc.want)
		}
	}
}
