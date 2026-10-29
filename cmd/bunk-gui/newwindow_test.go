package main

import (
	"slices"
	"strings"
	"testing"
)

// TestWindowCmd: a new window runs this bunk-gui in the pane's directory,
// in its own session, with the activation token and a settings file other
// than the default. Only a start of its own offers the last session back.
func TestWindowCmd(t *testing.T) {
	cmd := windowCmd("/usr/bin/bunk-gui", "/home/u/src", configPath(), "tok-1")
	if !slices.Equal(cmd.Args, []string{"/usr/bin/bunk-gui", "--offer-restore=false"}) || cmd.Dir != "/home/u/src" {
		t.Errorf("default settings: args %q in %q", cmd.Args, cmd.Dir)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Error("the new window shares this window's session")
	}
	for _, want := range []string{"XDG_ACTIVATION_TOKEN=tok-1", "DESKTOP_STARTUP_ID=tok-1"} {
		if !slices.Contains(cmd.Env, want) {
			t.Errorf("no %s in the environment", want)
		}
	}

	cmd = windowCmd("/usr/bin/bunk-gui", "", "/tmp/other.toml", "")
	if !slices.Equal(cmd.Args, []string{"/usr/bin/bunk-gui", "--offer-restore=false", "--config", "/tmp/other.toml"}) {
		t.Errorf("--config: args %q", cmd.Args)
	}
	if slices.ContainsFunc(cmd.Env, func(s string) bool { return strings.HasPrefix(s, "XDG_ACTIVATION_TOKEN=tok") }) {
		t.Error("a token without one to pass")
	}
}
