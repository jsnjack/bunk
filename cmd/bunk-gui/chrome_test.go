package main

import (
	"testing"

	"bunk/internal/session"
)

// TestTabLoc: a tab names where every pane runs as the panes' context
// badges do, each kind of place once with its names in order and no
// repeats.
func TestTabLoc(t *testing.T) {
	ssh := func(h string) session.Status { return session.Status{Process: "ssh", SSHHost: h} }
	box := func(c string) session.Status { return session.Status{Container: c} }
	for _, c := range []struct {
		sts        []session.Status
		loc, glyph string
	}{
		{nil, "", ""},
		{[]session.Status{{}, {Process: "vim"}}, "", ""},
		{[]session.Status{ssh("ti-ci")}, "⇄ ti-ci", "⇄"},
		{[]session.Status{ssh("ti-ci"), ssh("int-build"), ssh("int-devops")}, "⇄ ti-ci, int-build, int-devops", "⇄"},
		{[]session.Status{ssh("a"), ssh("b"), ssh("a")}, "⇄ a, b", "⇄"},
		{[]session.Status{{Process: "mosh", SSHHost: "gpu01"}, {Process: "ssh"}}, "⇄ gpu01, ssh", "⇄"},
		// A local agent pane: the other panes' places still show.
		{[]session.Status{{}, ssh("a")}, "⇄ a", "⇄"},
		{[]session.Status{box("toolbox fedora"), ssh("a"), box("toolbox fedora"), ssh("b")}, "⬡ fedora · ⇄ a, b", "⬡"},
		{[]session.Status{box("distrobox arch"), box("podman web")}, "▣ arch · ⬡ web", "▣"},
		// ssh from inside a container: both, as in the pane's badge.
		{[]session.Status{{Container: "toolbox dev", Process: "ssh", SSHHost: "a"}}, "⬡ dev · ⇄ a", "⬡"},
		// sudo is no place.
		{[]session.Status{{Process: "sudo"}, {Container: "toolbox dev", Process: "su"}}, "⬡ dev", "⬡"},
		// A remote agent's host through a client that is not ssh; with
		// ssh, its destination as typed.
		{[]session.Status{{Process: "et", Host: "gpu01"}}, "⇄ gpu01", "⇄"},
		{[]session.Status{{Process: "ssh", SSHHost: "gpu", Host: "gpu01.lan"}}, "⇄ gpu", "⇄"},
	} {
		if loc, glyph := tabLoc(c.sts); loc != c.loc || glyph != c.glyph {
			t.Errorf("tabLoc(%+v) = %q, %q; want %q, %q", c.sts, loc, glyph, c.loc, c.glyph)
		}
	}
}
