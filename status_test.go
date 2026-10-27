package main

import (
	"bunk/internal/alert"
	"bunk/internal/badges"
	"bunk/internal/badges/badgetest"
	"bunk/internal/session"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"
)

// badgeState is the part of a pane that its badges show, as the TUI holds
// it.
type badgeState struct {
	pass                      bool
	scroll                    int
	agent                     paneAgent
	process, ctype, cid, host string
	zoomed                    bool
	flash                     string
	flashOn                   bool // the flash has not expired
	width                     int
	mark                      alert.Mark
}

// apply sets p's fields to s.
func (s badgeState) apply(p *Pane) {
	p.passthrough, p.ScrollOffset, p.agent, p.mark = s.pass, s.scroll, s.agent, s.mark
	p.FgProcess, p.ContainerType, p.ContainerID, p.SSHHost = s.process, s.ctype, s.cid, s.host
	p.statusMsg, p.statusMsgEnd = s.flash, time.Time{}
	if s.flash != "" {
		p.statusMsgEnd = time.Now().Add(-time.Second)
		if s.flashOn {
			p.statusMsgEnd = time.Now().Add(time.Hour)
		}
	}
	p.x, p.y, p.w = 0, 0, s.width
}

// randomBadgeStates returns n pane states drawn from values that reach
// every branch of the badge rules, widths 0 to 130.
func randomBadgeStates(n int) []badgeState {
	rng := rand.New(rand.NewPCG(7, 11))
	pick := func(xs ...string) string { return xs[rng.IntN(len(xs))] }
	long := "Claude needs your permission to use Bash for npm install"
	agents := []paneAgent{
		{},
		{"claude", session.AgentWorking, ""},
		{"codex", session.AgentIdle, "ok"},
		{"claude", session.AgentNeedsYou, long},
		{"agent", session.AgentDone, "完了しました。テストはすべて合格です。"},
		{"claude", session.AgentFailed, "API Error: 429"},
		{"", session.AgentNeedsYou, "👍🏽 approve?"},
	}
	out := make([]badgeState, n)
	for i := range out {
		c := [][2]string{{"", ""}, {"toolbox", "fedora-42"}, {"distrobox", ""}, {"podman", "名前"}, {"lxd", "web"}}[rng.IntN(5)]
		out[i] = badgeState{
			pass:    rng.IntN(2) == 0,
			scroll:  []int{0, 0, 3, 120, 99999}[rng.IntN(5)],
			agent:   agents[rng.IntN(len(agents))],
			process: pick("", "sh", "ssh", "mosh", "sudo", "su", "vim"),
			ctype:   c[0], cid: c[1],
			host:    pick("", "build01"),
			zoomed:  rng.IntN(2) == 0,
			flash:   pick("", "", badges.Copied, "input not sent"),
			flashOn: rng.IntN(4) != 0,
			width:   rng.IntN(131),
		}
	}
	return out
}

// sameRow reports the first cell of row y where two screens differ: text,
// width or style.
func sameRow(a, b tcell.SimulationScreen, y int) (int, bool) {
	w, _ := a.Size()
	for x := 0; x < w; x++ {
		am, ac, as, aw := a.GetContent(x, y) //nolint:staticcheck // rune-level access
		bm, bc, bs, bw := b.GetContent(x, y) //nolint:staticcheck // rune-level access
		if am != bm || string(ac) != string(bc) || as != bs || aw != bw {
			return x, false
		}
	}
	return 0, true
}

func simScreen(t *testing.T, w, h int) tcell.SimulationScreen {
	t.Helper()
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scr.Fini)
	scr.SetSize(w, h)
	return scr
}

// TestBadgesMatchOldRenderer: with the rules moved to internal/badges, the
// TUI draws every cell of the badge row as before (the oracle in
// status_oracle_test.go): text, width and style, over thousands of states,
// every width from 0 to 130, and three themes.
func TestBadgesMatchOldRenderer(t *testing.T) {
	// Unset slots now take the host's palette colour undimmed, where the
	// old code painted white (TestTerminalThemeBadgesUseHostColours); here
	// they are the host's palette colours themselves, so both paths still
	// run: palette-index colours and RGB ones.
	test := testTheme()
	for i, c := range test.palette {
		if c == tcell.ColorDefault {
			test.palette[i] = tcell.PaletteColor(i)
		}
	}
	themes := map[string]resolvedTheme{
		"test":           test,
		"default":        builtinTheme("default"),
		"solarized-dark": builtinTheme("solarized-dark"),
	}
	states := randomBadgeStates(3000)
	every := badgeState{pass: true, scroll: 7, agent: paneAgent{"claude", session.AgentNeedsYou, "Approve edit to main.go?"},
		process: "ssh", ctype: "toolbox", cid: "fedora", host: "build01", zoomed: true, flash: badges.Copied, flashOn: true}
	for w := 0; w <= 130; w++ {
		every.width = w
		states = append(states, every)
	}
	if testing.Short() {
		states = states[len(states)-300:]
	}
	const cols = 140
	p := regressionPane(10, 2)
	for name, rt := range themes {
		t.Run(name, func(t *testing.T) {
			got, want := simScreen(t, cols, 2), simScreen(t, cols, 2)
			for i, s := range states {
				got.Clear()
				want.Clear()
				s.apply(p)
				drawPaneStatus(got, p, false, rt, s.zoomed)
				oldDrawPaneStatus(want, p, false, rt, s.zoomed)
				if x, ok := sameRow(got, want, 0); !ok {
					t.Fatalf("state %d %+v: cell %d differs:\n got  %q\n want %q", i, s, x,
						cellsText(got, 0, 0, cols), cellsText(want, 0, 0, cols))
				}
			}
		})
	}
}

// TestBadgeParityTUI draws the shared cases (badgetest.Cases) through the
// TUI: each badge's text at its place from the right edge, in its role's
// style, nothing else on the row. bunk-gui's TestBadgeParityGUI checks the
// same cases, so both front ends show the same badges.
func TestBadgeParityTUI(t *testing.T) {
	rt := builtinTheme("default")
	p := regressionPane(10, 2)
	for _, c := range badgetest.Cases {
		t.Run(c.Name, func(t *testing.T) {
			in := c.In
			s := badgeState{pass: in.Passthrough, scroll: in.ScrollOffset, agent: paneAgent{in.Agent, in.State, in.Notice},
				process: in.Process, ctype: in.ContainerType, cid: in.ContainerID, host: in.SSHHost,
				zoomed: in.Zoomed, flash: in.Flash, flashOn: true, width: in.Width,
				mark: alert.Mark{State: in.CommandState, Name: in.Command}}
			s.apply(p)
			const cols = 140
			scr := simScreen(t, cols, 2)
			drawPaneStatus(scr, p, false, rt, s.zoomed)
			x := in.Width - c.Inset
			for _, b := range c.Want {
				x -= uniseg.StringWidth(b.Text)
				end := x + uniseg.StringWidth(b.Text)
				if got := cellsText(scr, 0, x, end); got != b.Text {
					t.Fatalf("badge %q: row has %q at %d-%d; row %q", b.Text, got, x, end, cellsText(scr, 0, 0, cols))
				}
				want := badgeStyle(b.Role, rt)
				for cx := x; cx < end; {
					_, _, style, w := scr.GetContent(cx, 0) //nolint:staticcheck // rune-level access
					if style != want {
						t.Fatalf("badge %q cell %d: style %v, want %s %v", b.Text, cx, style, b.Role, want)
					}
					cx += max(1, w)
				}
			}
			blank := cellsText(scr, 0, 0, x) + cellsText(scr, 0, in.Width-c.Inset, cols)
			if strings.TrimSpace(blank) != "" {
				t.Errorf("cells outside the badges: %q", blank)
			}
		})
	}
}

// TestBadgeLooksTUI: under bunk's "default" theme each role has the
// colours in badgetest.DefaultLooks, which bunk-gui's badges use too.
func TestBadgeLooksTUI(t *testing.T) {
	rt := builtinTheme("default")
	hex := func(c tcell.Color) uint32 {
		r, g, b := c.RGB()
		return uint32(r)<<16 | uint32(g)<<8 | uint32(b)
	}
	for _, role := range badges.Roles {
		t.Run(role.String(), func(t *testing.T) {
			fg, bg, attrs := badgeStyle(role, rt).Decompose()
			got := badgetest.Look{FG: hex(fg), BG: hex(bg), Bold: attrs&tcell.AttrBold != 0}
			if want := badgetest.DefaultLooks[role]; got != want {
				t.Errorf("look %s, want %s", lookString(got), lookString(want))
			}
		})
	}
}

func lookString(l badgetest.Look) string {
	return fmt.Sprintf("#%06x on #%06x bold=%v", l.FG, l.BG, l.Bold)
}
