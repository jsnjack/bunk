package main

// Pane status badges (badge.go): the shared cases through bunk-gui's badge
// input, the role colours, and window scenarios that show every badge
// kind with real keys and check it in the report and in pixels.

import (
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"bunk/internal/badges"
	"bunk/internal/badges/badgetest"
	"bunk/internal/session"
)

// TestBadgeParityGUI gives a pane each shared case's state (badgetest) as
// bunk-gui holds it: session status, passthrough, the scroll offset and
// width of the last frame, the tab's zoom and the flash. The pane's badge
// input must be the case's, and the badges the case's: bunk's
// TestBadgeParityTUI checks the TUI against the same cases.
func TestBadgeParityGUI(t *testing.T) {
	for _, c := range badgetest.Cases {
		t.Run(c.Name, func(t *testing.T) {
			in := c.In
			p := &Pane{app: &App{}, tab: &Tab{}, tv: newTermView(), passthrough: in.Passthrough, flash: in.Flash}
			p.st = session.Status{Agent: in.Agent, State: in.State, Notice: in.Notice, Process: in.Process,
				Container: strings.TrimSpace(in.ContainerType + " " + in.ContainerID), SSHHost: in.SSHHost}
			p.tv.sbOff, p.tv.cols = in.ScrollOffset, in.Width
			if in.Zoomed {
				p.tab.zoom = p
			}
			got := p.badgeInput()
			if got != in {
				t.Fatalf("badge input %+v, want %+v", got, in)
			}
			row := badges.Layout(got)
			if !slices.Equal(row.Badges, c.Want) || row.Inset != c.Inset {
				t.Errorf("badges %q inset %d, want %q inset %d", row.Badges, row.Inset, c.Want, c.Inset)
			}
		})
	}
}

// TestBadgeLooksGUI: each role has the colours bunk's TUI gives it under
// the "default" theme (badgetest.DefaultLooks), and the CSS sets them.
func TestBadgeLooksGUI(t *testing.T) {
	css := badgeCSS()
	for _, role := range badges.Roles {
		t.Run(role.String(), func(t *testing.T) {
			fg, bg, bold := badgeLook(role)
			got := badgetest.Look{FG: fg, BG: bg, Bold: bold}
			if want := badgetest.DefaultLooks[role]; got != want {
				t.Errorf("look %+v, want %+v", got, want)
			}
			weight := map[bool]int{false: 400, true: 700}[bold]
			rule := fmt.Sprintf(".badge.%s { color: #%06x; background: #%06x; font-weight: %d; }", badgeClass(role), fg, bg, weight)
			if !strings.Contains(css, rule) {
				t.Errorf("CSS lacks %q", rule)
			}
		})
	}
}

// inputOf is the badges.Input a report records.
func inputOf(r badgeInputReport) badges.Input {
	return badges.Input{Passthrough: r.Passthrough, ScrollOffset: r.Scroll, Agent: r.Agent, State: stateNames[r.State],
		Notice: r.Notice, Process: r.Process, ContainerType: r.ContainerType, ContainerID: r.ContainerID,
		SSHHost: r.SSHHost, Zoomed: r.Zoomed, Flash: r.Flash, Width: r.Width}
}

// checkBadgeRows checks every shown pane of a snapshot. The badge input is
// the pane's state as the report gives it; the labels show what
// internal/badges lays out from it, right to left from the pane's right
// edge (less the scrollbar column while scrolled back), one row high,
// at the pane's top or below the find bar. It returns the badges laid out.
func checkBadgeRows(t *testing.T, s *uiSnapshot) map[int]badges.Row {
	t.Helper()
	rows := map[int]badges.Row{}
	for _, p := range s.Panes {
		if !p.Visible || p.Rect == nil {
			continue
		}
		st, in := p.Status, p.BadgeInput
		ctype, cid, _ := strings.Cut(st.Container, " ")
		want := badgeInputReport{Passthrough: p.Pass, Scroll: p.Scroll, Agent: st.Agent, State: st.State,
			Notice: st.Notice, Process: st.Process, ContainerType: ctype, ContainerID: cid, SSHHost: st.SSHHost,
			Zoomed: p.Zoomed, Flash: in.Flash, Width: p.Cols}
		if in != want || (in.Flash != "" && in.Flash != badges.Copied) {
			t.Errorf("%s: pane %d badge input %+v, want %+v", s.Label, p.ID, in, want)
		}
		row := badges.Layout(inputOf(in))
		rows[p.ID] = row
		var got, wantB []string
		for _, b := range p.Badges {
			got = append(got, b.Text+"/"+b.Role)
		}
		screen := ""
		for _, b := range row.Badges {
			wantB = append(wantB, b.Text+"/"+b.Role.String())
			screen = b.Text + screen
		}
		if !slices.Equal(got, wantB) || p.BadgeRow != screen {
			t.Errorf("%s: pane %d shows %q (%q), want %q", s.Label, p.ID, got, p.BadgeRow, wantB)
			continue
		}
		cell := float64(p.Cell[0]) / s.Window.Scale
		top := p.Rect.Y
		if f := s.Find; f != nil && f.Pane == p.ID && f.Bar != nil {
			top = f.Bar.Y + f.Bar.H
		}
		right := p.Rect.X + p.Rect.W - float64(row.Inset)*cell
		for i, b := range p.Badges {
			r := b.Rect
			if r == nil {
				t.Errorf("%s: pane %d badge %q not drawn", s.Label, p.ID, b.Text)
				continue
			}
			if math.Abs(r.X+r.W-right) > 1 || r.Y < top-0.5 || r.Y > top+6 || r.X < p.Rect.X-0.5 {
				t.Errorf("%s: pane %d badge %d %q at %+v; want its right edge at %.1f, its top at %.1f (pane %+v)",
					s.Label, p.ID, i, b.Text, *r, right, top, *p.Rect)
			}
			// One row high, about a cell per column of text.
			cells := float64(badgeCells(b.Text))
			if ch := float64(p.Cell[1]) / s.Window.Scale; math.Abs(r.H-ch) > 1 || math.Abs(r.W-cells*cell) > 0.1*cells*cell+1 {
				t.Errorf("%s: pane %d badge %q is %.1fx%.1f, want about %.1fx%.1f", s.Label, p.ID, b.Text, r.W, r.H, cells*cell, ch)
			}
			right = r.X
		}
	}
	return rows
}

// badgeCells is the width of a badge's text in cells.
func badgeCells(text string) int {
	return badges.Row{Badges: []badges.Badge{{Text: text}}}.Cells()
}

// checkBadgePixels checks a window shot: the badge's background fills its
// rect, less the glyphs drawn on it in its text colour.
func checkBadgePixels(t *testing.T, img image.Image, label string, b badgeReport) {
	t.Helper()
	var role badges.Role
	for _, r := range badges.Roles {
		if r.String() == b.Role {
			role = r
		}
	}
	fg, bg, _ := badgeLook(role)
	x, y, w, h := inner(b.Rect, 1)
	nbg := count(img, x, y, w, h, hex(bg), 3)
	nfg := blendCount(img, x, y, w, h, hex(fg), hex(bg))
	t.Logf("%s: badge %q (%s) %dx%d px: %d background %v, %d text %v", label, b.Text, b.Role, w, h, nbg, hex(bg), nfg, hex(fg))
	if nbg < w*h/2 || nfg < 10 {
		t.Errorf("%s: badge %q: %d of %d pixels %v, %d text pixels %v", label, b.Text, nbg, w*h, hex(bg), nfg, hex(fg))
	}
}

// fakeCommands returns a directory with copies of sleep named ssh, mosh
// and sudo, so a pane runs a process with that name.
func fakeCommands(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src, err := os.Open("/usr/bin/sleep")
	must(t, err)
	defer func() { must(t, src.Close()) }()
	for _, name := range []string{"ssh", "mosh", "sudo"} {
		_, err := src.Seek(0, io.SeekStart)
		must(t, err)
		dst, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY, 0o755)
		must(t, err)
		_, err = io.Copy(dst, src)
		must(t, err)
		must(t, dst.Close())
	}
	return dir
}

// focusedBadges returns the focused pane's badges in a snapshot.
func focusedBadges(t *testing.T, s *uiSnapshot) []badgeReport {
	t.Helper()
	return s.focused(t).Badges
}

// TestGUIBadges shows every badge kind on a pane, through real keys where
// the TUI has them: Shift+PgUp scrolls back (-N follows the offset), F12
// zooms, a fake ssh, mosh and sudo run, a container, each agent state,
// and Ctrl+F12 (PASS). Each state is checked against internal/badges
// through the report and in pixels.
func TestGUIBadges(t *testing.T) {
	t.Parallel()
	bin := fakeCommands(t)
	shots := []string{"scroll", "zoom", "ssh", "sudo", "mosh", "container",
		"agent-working", "agent-idle", "agent-needs", "agent-done", "agent-failed", "pass"}
	script := []string{
		"wait focused.line=$",
		"type seq 1 300\\r",
		"wait focused.text=300 focused.line=$",
		"kbd shift+pgup",
		"wait focused.scroll>0 focused.badges.0.role=scroll focused.settled=true idle 100ms",
		"snap scroll", "shot scroll.png",
		"kbd shift+pgup",
		"wait focused.scroll>30 idle 100ms",
		"snap scroll-2",
		"scroll-reset",
		"wait focused.scroll=0 focused.badges.#=0 idle 100ms",
		"snap live",
		"kbd F1",
		"wait tab.1.panes.#=2 focused_pane=1 focused.line=$",
		"kbd F12",
		"wait zoomed_pane=1 focused.badges.0.role=zoom focused.settled=true idle 100ms",
		"snap zoom", "shot zoom.png",
		"kbd F12",
		"wait zoomed_pane=-1 pane.1.badges.#=0 idle 100ms",
		"snap unzoomed",
		"type ssh infinity\\r",
		"wait focused.status.process=ssh focused.badges.0.role=ssh idle 100ms",
		"snap ssh", "shot ssh.png",
		"kbd ctrl+c",
		"wait focused.status.process=sh focused.line=$ focused.badges.#=0",
		"type sudo infinity\\r",
		"wait focused.status.process=sudo focused.badges.0.role=sudo idle 100ms",
		"snap sudo", "shot sudo.png",
		"kbd ctrl+c",
		"wait focused.status.process=sh focused.line=$ focused.badges.#=0",
		"type mosh infinity\\r",
		"wait focused.status.process=mosh focused.badges.0.role=mosh idle 100ms",
		"snap mosh", "shot mosh.png",
		"kbd ctrl+c",
		"wait focused.status.process=sh focused.line=$ focused.badges.#=0",
		// Until the foreground process changes again.
		"fake-container toolbox fakebox",
		"wait focused.badges.0.text=\" ⬡ fakebox \" idle 100ms",
		"snap container", "shot container.png",
	}
	for _, st := range []string{"working", "idle", "needs", "done", "failed"} {
		script = append(script,
			"status agent=claude state="+st+" notice=Approve edit to main.go?",
			"wait focused.badges.0.role=agent-"+st+" idle 100ms",
			"snap agent-"+st, "shot agent-"+st+".png")
	}
	script = append(script,
		"status-clear",
		"kbd ctrl+f12",
		"wait focused.passthrough=true focused.badges.0.role=pass idle 100ms",
		"snap pass", "shot pass.png",
		"kbd ctrl+f12",
		"wait focused.passthrough=false idle 100ms",
		"snap pass-off",
		"quit")
	g := runGUI(t, scenario{
		cost:   3,
		args:   []string{"--size", "100x24"},
		env:    []string{"PATH=" + bin + ":" + os.Getenv("PATH")},
		script: script,
		limit:  40e9,
	})
	for _, s := range g.Snapshots {
		checkBadgeRows(t, s)
	}
	for _, label := range []string{"scroll", "scroll-2"} {
		p := g.snap(label).focused(t)
		if want := fmt.Sprintf(" -%d ", p.Scroll); len(p.Badges) == 0 || p.Badges[0].Text != want {
			t.Errorf("%s: badges %+v, want %q first (scroll %d)", label, p.Badges, want, p.Scroll)
		}
	}
	if a, b := g.snap("scroll").focused(t).Scroll, g.snap("scroll-2").focused(t).Scroll; b <= a {
		t.Errorf("a second Shift+PgUp: scroll %d -> %d", a, b)
	}
	if b := focusedBadges(t, g.snap("live")); len(b) != 0 {
		t.Errorf("live view: badges %+v", b)
	}
	for label, want := range map[string]string{
		"zoom": " ZOOM ", "ssh": " ⇄ infinity ", "sudo": " sudo ", "container": " ⬡ fakebox ",
		"mosh": " ⇄ infinity ", "pass": " PASS ",
	} {
		if b := focusedBadges(t, g.snap(label)); len(b) == 0 || b[0].Text != want {
			t.Errorf("%s: badges %+v, want %q first", label, b, want)
		}
	}
	// Half a 100-column window: no room for the notice beside the container.
	if b := focusedBadges(t, g.snap("agent-needs")); len(b) != 2 || b[0].Text != " ◐ claude needs you " || b[1].Text != " ⬡ fakebox " {
		t.Errorf("agent-needs: badges %+v, want the agent and the container", b)
	}
	for _, label := range shots {
		img := loadPNG(t, g.file(label+".png"))
		for _, b := range focusedBadges(t, g.snap(label)) {
			checkBadgePixels(t, img, label, b)
		}
	}
}

// TestGUIBadgeFlash: Ctrl+Shift+C with a selection shows COPIED for
// badges.FlashFor, as the TUI does, and badge changes draw no pane frame.
// With badges shown an idle window draws nothing; typing allocates nothing
// in the frame path or the badge sync; scrolling allocates nothing in the
// frame path while -N follows each step.
func TestGUIBadgeFlash(t *testing.T) {
	t.Parallel()
	script := []string{
		"wait focused.line=$",
		"type echo copy-me\\r",
		"wait focused.text=copy-me focused.line=$",
		"dclick 2 1",
		"wait primary copy-me idle 200ms",
		"mark before-copy",
		"kbd ctrl+shift+c",
		"wait focused.badges.0.text=\" COPIED \" clipboard copy-me",
		"mark copied",
		"snap copied", "shot copied.png",
		"wait focused.badges.#=0 timeout=8s",
		"mark copy-gone",
		"snap copy-gone",
		"type seq 1 300\\r",
		"wait focused.line=$ focused.text=300",
		// The status poll (once a second) saw seq or not; once it shows the
		// shell again nothing changes until the window is used. Wait for
		// that before the status override hides the real status.
		"wait tab.1.status.process=sh idle 300ms",
		"status agent=claude state=working notice=Running the tests",
		"wait focused.badges.0.role=agent-working idle 300ms",
		"mark idle-start",
		"sleep 1500ms",
		"mark idle-end",
		"typing start 25ms",
		"sleep 300ms",
		"mark type-start",
		"sleep 1500ms",
		"mark type-end",
		"typing stop",
		"wait idle 100ms",
		"mark scroll-start",
	}
	for range 10 {
		script = append(script, "scroll 3", "wait frames 1")
	}
	script = append(script, "wait idle 100ms", "mark scroll-end", "snap scrolled", "quit")
	g := runGUI(t, scenario{
		cost:   2,
		args:   []string{"--alloc-probe", "--size", "80x24"},
		script: script,
		limit:  40e9,
	})
	for _, s := range g.Snapshots {
		checkBadgeRows(t, s)
	}
	checkBadgePixels(t, loadPNG(t, g.file("copied.png")), "copied", focusedBadges(t, g.snap("copied"))[0])
	shown := g.between("copied", "copy-gone")
	t.Logf("COPIED shown %d ms: %d pane frames, %d frame-clock frames", shown.TMs, shown.Frames, shown.ClockFrames)
	if d := badges.FlashFor.Milliseconds(); shown.TMs < d-300 || shown.TMs > d+1500 {
		t.Errorf("COPIED shown for %d ms, want about %d", shown.TMs, d)
	}
	if appear := g.between("before-copy", "copied"); appear.Frames != 0 || shown.Frames != 0 || shown.ClockFrames == 0 {
		t.Errorf("the flash drew %d+%d pane frames (want 0: labels only) and %d frame-clock frames", appear.Frames, shown.Frames, shown.ClockFrames)
	}

	idle := g.between("idle-start", "idle-end")
	typing := g.between("type-start", "type-end")
	scroll := g.between("scroll-start", "scroll-end")
	badgeAllocs := func(a, b string) int64 { return g.mark(b).BadgeMallocs - g.mark(a).BadgeMallocs }
	t.Logf("idle with badges %d ms: %d frames, %d frame-clock frames", idle.TMs, idle.Frames, idle.ClockFrames)
	t.Logf("typing: %d frames, %d objects by the frame path, %d by badge syncs", typing.Frames, typing.RenderMallocs, badgeAllocs("type-start", "type-end"))
	t.Logf("scrolling: %d frames, %d frame-clock frames, %d objects by the frame path, %d by badge syncs",
		scroll.Frames, scroll.ClockFrames, scroll.RenderMallocs, badgeAllocs("scroll-start", "scroll-end"))
	if idle.Frames != 0 || idle.ClockFrames != 0 {
		t.Errorf("idle with badges: %d frames, %d frame-clock frames; want none", idle.Frames, idle.ClockFrames)
	}
	if typing.Frames < 20 || typing.RenderMallocs != 0 || badgeAllocs("type-start", "type-end") != 0 {
		t.Errorf("typing: %d frames; frame path allocated %d, badge syncs %d; want 20+ frames, no objects",
			typing.Frames, typing.RenderMallocs, badgeAllocs("type-start", "type-end"))
	}
	if scroll.Frames < 10 || scroll.RenderMallocs != 0 {
		t.Errorf("scrolling: %d frames, frame path allocated %d objects; want 10+ frames, none", scroll.Frames, scroll.RenderMallocs)
	}
	if p := g.snap("scrolled").focused(t); p.Scroll != 30 || len(p.Badges) < 2 || p.Badges[0].Text != " -30 " {
		t.Errorf("after scrolling 30 lines: scroll %d, badges %+v", p.Scroll, p.Badges)
	}
}

// TestGUIBadgeFit: narrow split panes drop badge forms as the TUI does
// (the agent's notice, then its name and label, then whole badges), and
// with the find bar open the badges sit below it, both readable.
func TestGUIBadgeFit(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost: 2,
		args: []string{"--size", "60x9"},
		script: []string{
			"wait focused.line=$",
			"type seq 1 300\\r",
			"wait focused.line=$ focused.text=300",
			"status agent=claude state=needs notice=Approve edit to main.go?",
			"wait focused.badges.0.role=agent-needs idle 100ms",
			"snap top",
			// The entry's caret blinks while the bar has the keyboard, so
			// these waits look for the badges' place, not for idle.
			"kbd ctrl+shift+f",
			"wait find.focused=true focused.badges.0.rect.y>30",
			"snap find", "shot find.png",
			"kbd-type 150",
			"wait find.busy=false find.total>0 focused.scroll>0 focused.badges.0.role=scroll focused.badges.0.rect.y>30",
			"snap find-scrolled", "shot find-scrolled.png",
			"kbd Escape",
			"wait find=null idle 100ms",
			"snap find-closed",
			"scroll-reset",
			"status-clear",
			"kbd F1",
			"wait tab.1.panes.#=2 focused.line=$ focused.settled=true idle 50ms",
			"kbd F1",
			"wait tab.1.panes.#=3 focused.line=$",
			"wait idle 100ms pane.0.settled=true pane.1.settled=true pane.2.settled=true",
			"status pane=0 agent=claude state=working notice=Running the whole test suite",
			"status pane=1 agent=codex state=idle process=sudo",
			"status pane=2 agent=claude state=needs notice=Approve edit to main.go?",
			"kbd ctrl+f12",
			"wait pane.2.passthrough=true pane.2.badges.0.role=pass idle 100ms",
			"snap narrow", "shot narrow.png",
			"kbd ctrl+f12",
			"wait pane.2.passthrough=false",
			"quit",
		},
	})
	rows := map[string]map[int]badges.Row{}
	for _, s := range g.Snapshots {
		rows[s.Label] = checkBadgeRows(t, s)
	}

	// Find: the bar at the top right, the badges right below it.
	for _, label := range []string{"find", "find-scrolled"} {
		s := g.snap(label)
		p, f := s.focused(t), s.Find
		if f == nil || f.Bar == nil || len(p.Badges) == 0 {
			t.Fatalf("%s: find bar %+v, badges %+v", label, f, p.Badges)
		}
		bar, first := f.Bar, p.Badges[len(p.Badges)-1].Rect
		t.Logf("%s: find bar %+v; badges from %+v", label, *bar, *first)
		if first.Y < bar.Y+bar.H || first.Y > bar.Y+bar.H+8 || first.Y+first.H > p.Rect.Y+p.Rect.H {
			t.Errorf("%s: badges at y %.1f-%.1f, want right below the find bar (%.1f-%.1f) inside the pane", label,
				first.Y, first.Y+first.H, bar.Y, bar.Y+bar.H)
		}
		img := loadPNG(t, g.file(label+".png"))
		for _, b := range p.Badges {
			checkBadgePixels(t, img, label, b)
		}
		x, y, w, h := inner(bar, 3)
		if n := count(img, x, y, w, h, colMenu, 3); n < w*h/4 {
			t.Errorf("%s: find bar: %d of %d pixels %v", label, n, w*h, colMenu)
		}
	}
	if p := g.snap("find-scrolled").focused(t); p.Badges[0].Role != "scroll" {
		t.Errorf("find scrolled to a match: badges %+v, want the scroll badge first", p.Badges)
	}
	if a, b := g.snap("top").focused(t), g.snap("find-closed").focused(t); math.Abs(b.Badges[0].Rect.Y-a.Badges[0].Rect.Y) > 0.5 {
		t.Errorf("after the find bar closed the badges sit at y %.1f, before it opened %.1f", b.Badges[0].Rect.Y, a.Badges[0].Rect.Y)
	}

	// Narrow panes: the forms the model picks for each width.
	s := g.snap("narrow")
	for _, p := range s.Panes {
		t.Logf("narrow: pane %d, %d cols: %q", p.ID, p.Cols, p.BadgeRow)
	}
	if n := s.pane(t, 2); n.Cols > 15 || len(n.Badges) == 0 || n.Badges[0].Text != " PASS " {
		t.Errorf("narrow pane 2: %d cols, badges %+v; want 15 cols or fewer, PASS first", n.Cols, n.Badges)
	}
	for _, b := range s.pane(t, 2).Badges {
		if strings.Contains(b.Text, "claude") {
			t.Errorf("narrow pane 2: the agent badge kept its name: %q", b.Text)
		}
	}
	if w := s.pane(t, 0); len(w.Badges) == 0 || w.Badges[0].Text != " ● claude working " {
		t.Errorf("pane 0, %d cols: badges %+v; want the agent badge without its notice", w.Cols, w.Badges)
	}
	img := loadPNG(t, g.file("narrow.png"))
	for _, p := range s.Panes {
		for _, b := range p.Badges {
			checkBadgePixels(t, img, fmt.Sprintf("narrow pane %d", p.ID), b)
		}
	}
	if len(rows["narrow"]) != 3 {
		t.Errorf("narrow: %d panes checked", len(rows["narrow"]))
	}
}
