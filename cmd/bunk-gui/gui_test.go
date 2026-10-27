package main

// Window scenarios: one bunk-gui launch each, on the headless compositor
// (see harness_test.go). Checks read the --report JSON and sample pixels of
// window shots and pane dumps at coordinates the report gives; nothing is
// compared against a stored image, so font or driver updates do not break
// them.

import (
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// slack widens latency bounds for -race builds.
func slack() float64 {
	if raceEnabled {
		return 4
	}
	return 1
}

// Prototype colours (Main.dc.html, Horizontal.dc.html).
var (
	colChrome    = hex(0x131416)
	colBarActive = hex(0x23252a)
	colRailActv  = hex(0x26292f)
	colTint      = hex(0x2a2216)
	colMenu      = hex(0x1b1c20)
	colWorking   = hex(0x5aa9ff)
	colNeeds     = hex(0xffb454)
	colIdle      = hex(0x9aa0a8)
	colFailed    = hex(0xff8a8a)
	colLoc       = hex(0xc7b8ff)
)

// TestGUIFrame: rendering is pixel-exact through GSK at 133 %, the renderer
// is GL on the GPU, an idle window draws nothing, frames allocate nothing
// while typing and scrolling, and key echo latency stays low.
func TestGUIFrame(t *testing.T) {
	g := runGUI(t, scenario{
		args:   []string{"--alloc-probe", "--verify", "--size", "100x30"},
		script: frameScript(),
	})

	if g.GSKRenderer != "GskGLRenderer" {
		t.Errorf("GSK renderer %q, want GskGLRenderer", g.GSKRenderer)
	}
	if g.GLRenderer == "" || strings.Contains(strings.ToLower(g.GLRenderer), "llvmpipe") {
		t.Errorf("GL renderer %q: want a GPU, not llvmpipe", g.GLRenderer)
	}
	if v := g.Verify; v == nil {
		t.Error("no --verify result")
	} else {
		t.Logf("verify: frame %d of %d pixels differ; checkerboard %d of %d differ, %d not black/white",
			v.FrameDiffer, v.FramePixels, v.CheckerDiffer, v.CheckerPixels, v.CheckerNonBinary)
		if v.FramePixels == 0 || v.FrameDiffer != 0 {
			t.Errorf("last frame through GSK: %d of %d pixels differ (max delta %d)", v.FrameDiffer, v.FramePixels, v.FrameMaxDelta)
		}
		if v.CheckerPixels == 0 || v.CheckerDiffer != 0 || v.CheckerNonBinary != 0 {
			t.Errorf("checkerboard through GSK: %d of %d pixels differ, %d not black/white", v.CheckerDiffer, v.CheckerPixels, v.CheckerNonBinary)
		}
	}
	if s := g.snap("end"); s.Window.Scale < 1.33 || s.Window.Scale > 1.34 {
		t.Errorf("scale %.4f, want the 133 %% test monitor", s.Window.Scale)
	}

	idle := g.between("idle-start", "idle-end")
	typing := g.between("type-start", "type-end")
	scroll := g.between("scroll-start", "scroll-end")
	t.Logf("idle %d ms: %d frames, %d frame-clock frames", idle.TMs, idle.Frames, idle.ClockFrames)
	t.Logf("typing %d ms: %d frames, %d key echoes, %d objects allocated by the frame path", typing.TMs, typing.Frames, typing.KeyRenders, typing.RenderMallocs)
	t.Logf("scrolling %d ms: %d frames, %d objects allocated by the frame path", scroll.TMs, scroll.Frames, scroll.RenderMallocs)
	if idle.Frames != 0 || idle.ClockFrames != 0 {
		t.Errorf("idle: %d frames rendered, %d frame-clock frames; want none", idle.Frames, idle.ClockFrames)
	}
	if typing.Frames < 20 || typing.KeyRenders < 20 {
		t.Errorf("typing: %d frames, %d key echoes drawn; want at least 20", typing.Frames, typing.KeyRenders)
	}
	if typing.RenderMallocs != 0 {
		t.Errorf("typing: the frame path allocated %d objects over %d frames; want 0", typing.RenderMallocs, typing.Frames)
	}
	if scroll.Frames < 10 {
		t.Errorf("scrolling: %d frames; want at least 10", scroll.Frames)
	}
	if scroll.RenderMallocs != 0 {
		t.Errorf("scrolling: the frame path allocated %d objects over %d frames; want 0", scroll.RenderMallocs, scroll.Frames)
	}

	kr, kp := g.Latency["key_render"], g.Latency["key_present"]
	t.Logf("key write -> render p50 %.0f µs p99 %.0f µs (n %d); -> presented p50 %.0f µs p99 %.0f µs (n %d)",
		kr.P50, kr.P99, kr.N, kp.P50, kp.P99, kp.N)
	if kr.N < 20 || kr.P50 > 10000*slack() {
		t.Errorf("key write -> render: p50 %.0f µs over %d keys", kr.P50, kr.N)
	}
	// p99 is one or two stalled keys: under load (check-all runs bunk's
	// race tests beside this suite) a scheduler or GC pause decides it, so
	// it is checked only where the machine is quiet, by make bench-check.
	if os.Getenv("BUNKGUI_LATENCY_P99") != "" && kr.P99 > 50000*slack() {
		t.Errorf("key write -> render: p99 %.0f µs over %d keys", kr.P99, kr.N)
	}
	if kp.N < 20 || kp.P50 > 40000*slack() {
		t.Errorf("key write -> presented: p50 %.0f µs over %d keys", kp.P50, kp.N)
	}
	if p := g.snap("end").focused(t); !strings.Contains(p.text(), "3000") {
		t.Errorf("pane text after seq: %q", p.Text)
	}
}

// frameScript is TestGUIFrame's scenario. The idle and typing windows
// are real time: the checks count frames in them. Scrolling waits for one
// frame per step.
func frameScript() []string {
	script := []string{
		"wait focused.line=$",
		"type seq 1 3000\\r",
		"wait focused.text=3000 focused.line=$",
		// The status poll (once a second) saw seq or not; once it shows
		// the shell nothing changes until the window is used.
		"wait tab.1.status.process=sh idle 300ms",
		"mark idle-start",
		"sleep 2s",
		"mark idle-end",
		"typing start 25ms",
		"sleep 400ms",
		"mark type-start",
		"sleep 2s",
		"mark type-end",
		"typing stop",
		"wait idle 100ms",
		"mark scroll-start",
	}
	for _, n := range []int{3, 3, 3, 3, 3, 3, 3, 3, 3, 3, -3, -3, -3, -3, -3} {
		script = append(script, fmt.Sprintf("scroll %d", n), "wait frames 1")
	}
	return append(script, "mark scroll-end", "snap end", "quit")
}

// TestGUIClipboard: Ctrl+Shift+C and Ctrl+Shift+V (real key events),
// double-click selection to the primary selection, middle-click paste, and
// an application's OSC 52 write. The compositor has its own clipboard.
// It also types a command key by key through the input method.
func TestGUIClipboard(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		args: []string{"--size", "80x24"},
		script: []string{
			"wait focused.line=$",
			"type echo alpha hello-clip omega\\r",
			"wait focused.text=\"alpha hello-clip omega\" focused.line=$",
			"dclick 8 1",
			"kbd ctrl+shift+c",
			"wait clipboard hello-clip primary hello-clip",
			"snap copied",
			"clip pasted-text",
			"wait clipboard pasted-text",
			"kbd ctrl+shift+v",
			"wait focused.text=\"$ pasted-text\"",
			"snap pasted",
			"click 3 5 2",
			"wait focused.text=\"$ pasted-texthello-clip\"",
			"snap primary-pasted",
			"kbd ctrl+u",
			"wait focused.line=$",
			"type printf '\\\\033]52;c;%s\\\\007' \"$(printf osc52-text | base64)\"\\r",
			"wait clipboard osc52-text",
			"snap osc52",
			"kbd-type echo Typed-By-Keys\\r",
			"wait focused.text=Typed-By-Keys focused.line=$",
			"snap typed",
			"quit",
		},
	})
	s := g.snap("copied")
	if s.Primary == nil || *s.Primary != "hello-clip" {
		t.Errorf("primary after double click: %v, want hello-clip", strp(s.Primary))
	}
	if s.Clipboard == nil || *s.Clipboard != "hello-clip" {
		t.Errorf("clipboard after Ctrl+Shift+C: %v, want hello-clip", strp(s.Clipboard))
	}
	if p := g.snap("pasted").focused(t); !slices.Contains(p.Text, "$ pasted-text") {
		t.Errorf("after Ctrl+Shift+V the prompt line is missing: %q", p.Text)
	}
	if p := g.snap("primary-pasted").focused(t); !slices.Contains(p.Text, "$ pasted-texthello-clip") {
		t.Errorf("after a middle click the prompt line is missing: %q", p.Text)
	}
	if s := g.snap("osc52"); s.Clipboard == nil || *s.Clipboard != "osc52-text" {
		t.Errorf("clipboard after OSC 52: %v, want osc52-text", strp(s.Clipboard))
	}
	if p := g.snap("typed").focused(t); !slices.Contains(p.Text, "Typed-By-Keys") {
		t.Errorf("typed key by key, the echo is missing: %q", p.Text)
	}
	iw := g.Latency["inject_write"]
	t.Logf("key injected -> written to the PTY: %.0f µs", iw.P50)
	if iw.N < 1 || iw.P50 > 100000*slack() {
		t.Errorf("key injected -> written to the PTY: p50 %.0f µs over %d keys", iw.P50, iw.N)
	}
}

func strp(s *string) string {
	if s == nil {
		return "<unread>"
	}
	return *s
}

// TestGUILayoutsAndMenu: a config file selects the bar and the font; every
// options-menu item works; Ctrl+B and the menu switch layouts with the
// sizes of the prototypes; Ctrl+=/-/0 and the menu's −/+ change the text
// size.
func TestGUILayoutsAndMenu(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		config: "tabs = \"bar\"\nfont = \"Noto Sans Mono\"\nfont_size = 12\n",
		args:   []string{"--size", "100x30", "--tab", ""},
		script: []string{
			"wait chrome.h=44 tabs.#=2 focused.settled=true",
			"snap bar",
			"shot bar.png",
			"menu open",
			"wait menu_open=true",
			"snap menu",
			"shot menu.png menu",
			"menu-item Tabs on the side",
			"wait layout=rail rail_open=false chrome.w=48",
			"snap rail",
			"shot rail.png",
			"kbd ctrl+shift+b",
			"wait rail_open=true chrome.w=268",
			"snap rail-open",
			"shot rail-open.png",
			"kbd ctrl+shift+b",
			"wait rail_open=false chrome.w=48",
			"snap rail-closed",
			"kbd ctrl+equal",
			"wait text_percent=110 focused.settled=true",
			"snap zoom-in",
			"kbd ctrl+minus ctrl+minus",
			"wait text_percent=90",
			"snap zoom-out",
			"kbd ctrl+0",
			"wait text_percent=100",
			"snap zoom-reset",
			"menu open",
			"wait menu_open=true",
			"menu-item +",
			"wait text_percent=110",
			"snap menu-plus",
			"menu-item −",
			"wait text_percent=100",
			"snap menu-minus",
			"menu-item New tab",
			"wait tabs.#=3 selected_tab=3",
			"snap new-tab",
			"menu open",
			"wait menu_open=true",
			"menu-item Split pane",
			"wait tab.3.panes.#=2",
			"snap split",
			"menu open",
			"wait menu_open=true",
			"menu-item Zoom pane",
			"wait zoomed_pane>=0",
			"snap zoomed",
			"menu open",
			"wait menu_open=true",
			"menu-item Close tab",
			"wait tabs.#=2",
			"snap closed-tab",
			"menu open",
			"wait menu_open=true",
			"menu-item Switch tab",
			"wait switcher.open=true",
			"snap switcher",
			"switcher close",
			"wait switcher.open=false",
			"menu open",
			"wait menu_open=true",
			"menu-item Next needing you",
			"wait run.toasts~\"No agent needs you\"",
			"menu open",
			"wait menu_open=true",
			"menu-item New window",
			"wait run.windows.#=1",
			"kbd ctrl+shift+n",
			"wait run.windows.#=2",
			"menu open",
			"wait menu_open=true",
			"menu-item Find",
			"wait find!=null",
			"snap find-open",
			"kbd Escape",
			"wait find=null",
			"menu open",
			"wait menu_open=true",
			"menu-item Preferences",
			"wait prefs!=null",
			"snap prefs-open",
			"dialog-close",
			"wait prefs=null",
			"menu open",
			"wait menu_open=true",
			"menu-item Keyboard shortcuts",
			"wait shortcuts!=null",
			"snap shortcuts-open",
			"dialog-close",
			"wait shortcuts=null",
			"menu open",
			"wait menu_open=true",
			"menu-item Expand rail",
			"wait rail_open=true chrome.w=268",
			"snap expand-menu",
			"menu open",
			"wait menu_open=true",
			"menu-item Tabs on top",
			"wait layout=bar chrome.h=44",
			"snap bar-again",
			"menu open",
			"wait menu_open=true",
			"menu-item About bunk-gui",
			"wait about_open=true",
			"snap about",
			"menu open",
			"wait menu_open=true",
			"menu-item Quit",
		},
	})
	if g.Reason != "quit" {
		t.Errorf("run ended by %q, want the menu's Quit", g.Reason)
	}
	if g.ConfigError != "" || g.Settings["tabs"] != "bar" || g.Settings["font_size"] != 12.0 {
		t.Errorf("settings %v, config error %q", g.Settings, g.ConfigError)
	}

	bar := g.snap("bar")
	if bar.Layout != "bar" || len(bar.Tabs) != 2 || bar.Font != "Noto Sans Mono 12" || bar.TextPercent != 100 {
		t.Errorf("start: layout %q, %d tabs, font %q, %d %%", bar.Layout, len(bar.Tabs), bar.Font, bar.TextPercent)
	}
	checkChromeButtons(t, "bar", bar)
	if bar.Chrome == nil || bar.Chrome.H != 44 || bar.Chrome.W != float64(bar.Window.W) {
		t.Errorf("bar rect %+v, want the window's width and 44 px", bar.Chrome)
	} else {
		img := loadPNG(t, g.file("bar.png"))
		c := bar.Chrome
		if p := at(img, c.DX+c.DW/2, c.DY+2); p.dist(colChrome) > 1 {
			t.Errorf("bar background %v, want %v", p, colChrome)
		}
		it := bar.tab(t, 1).Item
		x, y, w, h := inner(it, 2)
		if n := count(img, x, y, w, h, colBarActive, 1); n < w*h/3 {
			t.Errorf("selected bar tab: %d of %d pixels %v", n, w*h, colBarActive)
		}
		it = bar.tab(t, 2).Item
		x, y, w, h = inner(it, 2)
		if n := count(img, x, y, w, h, colChrome, 1); n < w*h/3 {
			t.Errorf("other bar tab: %d of %d pixels %v", n, w*h, colChrome)
		}
	}
	if s := g.snap("menu"); !s.MenuOpen {
		t.Error("menu open: not open")
	}
	menu := loadPNG(t, g.file("menu.png"))
	b := menu.Bounds()
	if n := count(menu, 0, 0, b.Dx(), b.Dy(), colMenu, 1); n < b.Dx()*b.Dy()/3 {
		t.Errorf("menu background: %d of %d pixels %v", n, b.Dx()*b.Dy(), colMenu)
	}

	for _, c := range []struct {
		label    string
		layout   string
		open     bool
		w, h     float64
		bgSample bool
	}{
		{"rail", "rail", false, 48, 0, true},
		{"rail-open", "rail", true, 268, 0, true},
		{"rail-closed", "rail", false, 48, 0, false},
		{"expand-menu", "rail", true, 268, 0, false},
		{"bar-again", "bar", true, 0, 44, false},
	} {
		s := g.snap(c.label)
		if s.Layout != c.layout || s.RailOpen != c.open {
			t.Errorf("%s: layout %q rail open %v", c.label, s.Layout, s.RailOpen)
		}
		switch {
		case s.Chrome == nil:
			t.Errorf("%s: no chrome", c.label)
		case c.w != 0 && (s.Chrome.W != c.w || s.Chrome.H != float64(s.Window.H)):
			t.Errorf("%s: rail %vx%v, want %vx%d", c.label, s.Chrome.W, s.Chrome.H, c.w, s.Window.H)
		case c.h != 0 && s.Chrome.H != c.h:
			t.Errorf("%s: bar height %v, want %v", c.label, s.Chrome.H, c.h)
		}
		checkChromeButtons(t, c.label, s)
		if c.bgSample && s.Chrome != nil {
			img := loadPNG(t, g.file(c.label+".png"))
			ch := s.Chrome
			if p := at(img, ch.DX+ch.DW/2, ch.DY+ch.DH-ch.DH/6); p.dist(colChrome) > 1 {
				t.Errorf("%s: rail background %v, want %v", c.label, p, colChrome)
			}
		}
	}

	cols := bar.focused(t).Cols
	for _, c := range []struct {
		label   string
		percent int
		size    float64
	}{
		{"zoom-in", 110, 13.2}, {"zoom-out", 90, 10.8}, {"zoom-reset", 100, 12}, {"menu-plus", 110, 13.2}, {"menu-minus", 100, 12},
	} {
		s := g.snap(c.label)
		if s.TextPercent != c.percent || math.Abs(s.FontSize-c.size) > 1e-9 {
			t.Errorf("%s: text %d %% (%v pt), want %d %% (%v pt)", c.label, s.TextPercent, s.FontSize, c.percent, c.size)
		}
	}
	// The rail takes 48 px from the bar's width; a larger font fits fewer
	// columns still.
	if zc := g.snap("zoom-in").focused(t).Cols; zc >= cols {
		t.Errorf("columns at 110 %%: %d, at 100 %% %d", zc, cols)
	}

	if s := g.snap("new-tab"); len(s.Tabs) != 3 || s.SelectedTab != 3 {
		t.Errorf("New tab: %d tabs, selected %d", len(s.Tabs), s.SelectedTab)
	}
	s := g.snap("split")
	if tb := s.tab(t, 3); len(tb.Panes) != 2 || s.Tree == nil || s.Tree.Split == "" {
		t.Errorf("Split pane: tab 3 panes %v, tree %+v", tb.Panes, s.Tree)
	}
	if s := g.snap("zoomed"); s.ZoomedPane < 0 || s.ZoomedPane != s.FocusedPane {
		t.Errorf("Zoom pane: zoomed %d, focused %d", s.ZoomedPane, s.FocusedPane)
	}
	if s := g.snap("closed-tab"); len(s.Tabs) != 2 {
		t.Errorf("Close tab: %d tabs", len(s.Tabs))
	}
	if s := g.snap("switcher"); !s.Switcher.Open || len(s.Switcher.Rows) != 2 {
		t.Errorf("Switch tab: switcher %+v", s.Switcher)
	}
	if s := g.snap("shortcuts-open"); s.Shortcuts == nil || len(s.Shortcuts.Rows) != len(keyActions) {
		t.Errorf("Keyboard shortcuts: window %+v", s.Shortcuts)
	}
	if s := g.snap("find-open"); s.Find == nil {
		t.Error("Find: no find bar")
	}
	if s := g.snap("prefs-open"); s.Prefs == nil {
		t.Error("Preferences: no window")
	}
	if dir, err := filepath.EvalSymlinks(g.dir); err != nil || !slices.Equal(g.Windows, []string{dir, dir}) {
		t.Errorf("New window and Ctrl+Shift+N opened windows in %q, want two in %q (%v)", g.Windows, dir, err)
	}
	for _, want := range []string{"No agent needs you"} {
		if !slices.ContainsFunc(g.Toasts, func(s string) bool { return strings.HasPrefix(s, want) }) {
			t.Errorf("no toast %q in %q", want, g.Toasts)
		}
	}
	if s := g.snap("about"); !s.AboutOpen || s.AboutVersion != Version {
		t.Errorf("About bunk-gui: open %v, version %q, want %q", s.AboutOpen, s.AboutVersion, Version)
	}
}

// checkChromeButtons checks that the options and close-window buttons keep
// their requested size (34 px, 40 in the collapsed rail) and, in the bar,
// that the tabs keep their own width: the drag area takes the spare width.
// The close button's centred icon used to widen it to a share of the bar,
// and the tabs took another share.
func checkChromeButtons(t *testing.T, label string, s *uiSnapshot) {
	t.Helper()
	for _, b := range []struct {
		name string
		r    *rectReport
	}{{"options", s.Options}, {"close window", s.CloseWindow}} {
		if b.r == nil || b.r.W > 40 {
			t.Errorf("%s: %s button %+v, want at most 40 px wide", label, b.name, b.r)
		}
	}
	if s.Layout != "bar" {
		return
	}
	for _, tb := range s.Tabs {
		if tb.Item == nil || tb.NaturalW < 132 || tb.Item.W != float64(tb.NaturalW) {
			t.Errorf("%s: tab %d %+v, want its natural width %d (at least 132)", label, tb.Index, tb.Item, tb.NaturalW)
		}
	}
}

// TestGUIConfigErrors: a config with bad values or unknown keys, or one
// that does not parse, shows a toast and leaves the defaults.
func TestGUIConfigErrors(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, config string
		want         []string
	}{
		{"bad-values", "tabs = \"sideways\"\nfont_size = 300\ncolour = \"red\"\n", []string{"colour", "sideways", "font_size = 300"}},
		{"broken", "tabs = [\n", []string{"toml"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			g := runGUI(t, scenario{config: c.config, args: []string{"--size", "60x20"},
				script: []string{"wait last_toast~\"Settings: \"", "snap start", "quit"}})
			s := g.snap("start")
			if s.Layout != "rail" || s.RailOpen || s.Font != "Noto Sans Mono 11" {
				t.Errorf("not the defaults: layout %q, rail open %v, font %q", s.Layout, s.RailOpen, s.Font)
			}
			if !strings.HasPrefix(s.LastToast, "Settings: ") {
				t.Errorf("toast %q", s.LastToast)
			}
			for _, w := range c.want {
				if !strings.Contains(g.ConfigError, w) {
					t.Errorf("config error %q does not mention %q", g.ConfigError, w)
				}
			}
		})
	}
}

// TestGUITabDrag drags tabs with the pointer in the expanded rail, the
// collapsed rail and the bar: while dragging, the report names the slot,
// the release moves the tab there, a drag never clicks (the selected tab
// stays), and a press without a drag still selects.
func TestGUITabDrag(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost:   1.5,
		config: "tabs = \"rail\"\nrail_open = true\n",
		args:   []string{"--size", "100x30", "--tab", "", "--tab", "", "--tab", ""},
		script: []string{
			"maximize",
			// Each shell's first status rebuilds the tab items, and a press
			// right after a rebuild finds no item with a size yet.
			"wait window.maximized=true tabs.#=4 tab.1.agent=sh tab.2.agent=sh tab.3.agent=sh tab.4.agent=sh",
			"snap start",
			"ptr-at tab 1",
			"ptr-press",
			"ptr-at tab 3 0 10",
			"wait tab_drag.slot=4",
			"snap rail-dragging",
			"ptr-release",
			"wait tab_drag=null tab.3.id=0",
			"snap rail",
			"kbd ctrl+shift+b",
			"wait rail_open=false",
			"ptr-at tab 4",
			"ptr-press",
			"ptr-at tab 1 0 -8",
			"wait tab_drag.slot=1",
			"ptr-release",
			"wait tab_drag=null tab.1.id=3",
			"snap dots",
			"layout bar",
			"wait layout=bar",
			"ptr-at tab 1",
			"ptr-press",
			"ptr-at tab 2 20 0",
			"wait tab_drag.slot=3",
			"ptr-release",
			"wait tab_drag=null tab.2.id=3",
			"snap bar",
			"ptr-at tab 3",
			"ptr-click",
			"wait selected_tab=3",
			"snap clicked",
			"quit",
		},
	})
	ids := func(s *uiSnapshot) []int {
		var out []int
		for _, tb := range s.Tabs {
			out = append(out, tb.ID)
		}
		return out
	}
	start := g.snap("start")
	selected := start.tab(t, start.SelectedTab).ID
	if s := g.snap("rail-dragging"); s.TabDrag == nil || s.TabDrag.Tab != 1 || !slices.Equal(ids(s), []int{0, 1, 2, 3}) {
		t.Errorf("dragging in the rail: %+v, order %v; want tab 1 dragged, order unchanged until the release", s.TabDrag, ids(s))
	}
	for label, want := range map[string][]int{"rail": {1, 2, 0, 3}, "dots": {3, 1, 2, 0}, "bar": {1, 3, 2, 0}} {
		s := g.snap(label)
		if got := ids(s); !slices.Equal(got, want) || s.tab(t, s.SelectedTab).ID != selected {
			t.Errorf("%s: order %v, selected tab id %d; want %v, selected id %d", label, got, s.tab(t, s.SelectedTab).ID, want, selected)
		}
	}
	if s := g.snap("clicked"); s.tab(t, 3).ID != 2 {
		t.Errorf("click on tab 3: selected tab id %d, want 2", s.tab(t, 3).ID)
	}
}

// TestGUITabsAndStatus: status from the test hook drives dots, labels,
// locations and tints; the switcher sorts needs-you first, moves its
// cursor with Up and Down (wrapping, kept on its tab when the rows
// reorder) and filters by typed text; Alt+A visits tabs needing you in order; Alt+1…9 select
// tabs; a pane needing you gets the ring until it is focused; inactive
// panes are dimmed.
func TestGUITabsAndStatus(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		cost:   2.5,
		config: "tabs = \"rail\"\nrail_open = true\n",
		args:   []string{"--size", "100x30", "--tab", "", "--tab", "", "--tab", ""},
		script: []string{
			"wait tabs.#=4 tab.1.agent=sh",
			"status tab=2 agent=codex state=needs repo=surfly branch=fix-login notice=Approve edit?",
			"status tab=3 agent=copilot state=working repo=it branch=main container=toolbox:fedora-42",
			"status tab=4 agent=claude state=failed repo=gh-mirror branch=main ssh=build01",
			"wait tab.2.state=needs tab.3.state=working tab.4.state=failed",
			"snap states",
			"shot states.png",
			"kbd ctrl+shift+o",
			"wait switcher.open=true keyboard_focus=switcher",
			"snap switcher",
			"kbd down down",
			"wait switcher.cursor=3",
			"snap switcher-down",
			"kbd up up up",
			"wait switcher.cursor=1",
			"snap switcher-wrap",
			"kbd down down",
			"wait switcher.cursor=4",
			"status tab=4 state=idle",
			"wait switcher.rows.1=3",
			"snap switcher-reorder",
			"kbd-type ma",
			"wait switcher.filter=ma",
			"snap switcher-typed",
			"kbd BackSpace BackSpace",
			"wait switcher.filter=\"\"",
			"kbd-type gh-mirror",
			"wait switcher.filter=gh-mirror switcher.rows.#=1",
			"snap switcher-filter",
			"kbd Return",
			"wait switcher.open=false selected_tab=4 keyboard_focus=\"pane 3\"",
			"snap picked",
			"kbd alt+1",
			"wait selected_tab=1",
			"status tab=4 state=needs notice=Second request",
			"kbd alt+a",
			"wait selected_tab=2 tab.2.state=idle",
			"snap jump1",
			"kbd alt+a",
			"wait selected_tab=4 tab.4.state=idle",
			"snap jump2",
			"kbd alt+a",
			"wait last_toast=\"No agent needs you\"",
			"snap jump3",
			"kbd alt+3",
			"wait selected_tab=3",
			"snap alt3",
			"kbd alt+9",
			"wait input",
			"snap alt9",
			"kbd alt+1",
			"wait selected_tab=1",
			"split",
			"wait tab.1.panes.#=2 pane.4.line=$",
			"status pane=0 agent=claude state=needs notice=Ring me",
			"wait pane.0.ring=true pane.0.settled=true pane.4.settled=true",
			"snap ring",
			"dump-pane 0 ring.png",
			"dump-pane 4 focused.png",
			"focus left",
			"wait focused_pane=0 pane.0.ring=false",
			"snap acked",
			"dump-pane 0 acked.png",
			"quit",
		},
	})

	s := g.snap("states")
	for _, c := range []struct {
		tab                      int
		agent, state, label, loc string
		needs, selected          bool
		dot                      rgb
	}{
		{1, "sh", "none", "", "", false, true, colIdle},
		{2, "codex", "needs", "needs you", "", true, false, colNeeds},
		{3, "copilot", "working", "working", "⬡ toolbox fedora-42", false, false, colWorking},
		{4, "claude", "failed", "failed", "⇄ ssh build01", false, false, colFailed},
	} {
		tb := s.tab(t, c.tab)
		if tb.Agent != c.agent || tb.State != c.state || tb.Label != c.label || tb.Loc != c.loc || tb.Needs != c.needs || tb.Selected != c.selected {
			t.Errorf("tab %d: %+v", c.tab, tb)
		}
	}
	if tb := s.tab(t, 2); tb.Notice != "Approve edit?" || !strings.Contains(tb.Tooltip, "Approve edit?") || tb.Repo != "surfly ⎇ fix-login" {
		t.Errorf("tab 2 notice %q, tooltip %q, repo %q", tb.Notice, tb.Tooltip, tb.Repo)
	}
	img := loadPNG(t, g.file("states.png"))
	for _, c := range []struct {
		tab     int
		bg, dot rgb
		loc     bool
	}{
		{1, colRailActv, colIdle, false}, {2, colTint, colNeeds, false}, {3, colChrome, colWorking, true}, {4, colChrome, colFailed, true},
	} {
		it := s.tab(t, c.tab).Item
		if it == nil {
			t.Errorf("tab %d: no rail row", c.tab)
			continue
		}
		x, y, w, h := inner(it, 3)
		if n := count(img, x, y, w, h, c.bg, 1); n < w*h/3 {
			t.Errorf("tab %d row: %d of %d pixels %v", c.tab, n, w*h, c.bg)
		}
		// The dot sits in the row's first 30 logical px.
		if n := blendCount(img, it.DX, it.DY, int(30*s.Window.Scale), it.DH, c.dot, c.bg); n < 4 {
			t.Errorf("tab %d dot: %d pixels of %v", c.tab, n, c.dot)
		}
		if n := blendCount(img, it.DX, it.DY, it.DW, it.DH, colLoc, c.bg); (n >= 10) != c.loc {
			t.Errorf("tab %d location text: %d pixels of %v", c.tab, n, colLoc)
		}
	}

	if s := g.snap("switcher"); !s.Switcher.Open || !slices.Equal(s.Switcher.Rows, []int{2, 4, 3, 1}) || s.KeyboardFocus != "switcher" {
		t.Errorf("switcher: %+v, keyboard focus %q; want rows needs-you, failed, working, shell", s.Switcher, s.KeyboardFocus)
	}
	for label, want := range map[string]struct {
		rows   []int
		cursor int
	}{
		"switcher":         {[]int{2, 4, 3, 1}, 2},
		"switcher-down":    {[]int{2, 4, 3, 1}, 3},
		"switcher-wrap":    {[]int{2, 4, 3, 1}, 1},
		"switcher-reorder": {[]int{2, 3, 4, 1}, 4},
		"switcher-typed":   {[]int{3, 4}, 3},
	} {
		if s := g.snap(label); !slices.Equal(s.Switcher.Rows, want.rows) || s.Switcher.Cursor != want.cursor || s.KeyboardFocus != "switcher" {
			t.Errorf("%s: switcher %+v, keyboard focus %q; want rows %v, cursor on tab %d", label, s.Switcher, s.KeyboardFocus, want.rows, want.cursor)
		}
	}
	if s := g.snap("switcher-filter"); s.Switcher.Filter != "gh-mirror" || !slices.Equal(s.Switcher.Rows, []int{4}) || s.Switcher.Cursor != 4 {
		t.Errorf("filtered switcher: %+v", s.Switcher)
	}
	if s := g.snap("picked"); s.Switcher.Open || s.SelectedTab != 4 || s.KeyboardFocus != "pane 3" {
		t.Errorf("after Enter: switcher open %v, selected %d, keyboard %q", s.Switcher.Open, s.SelectedTab, s.KeyboardFocus)
	}
	if s := g.snap("jump1"); s.SelectedTab != 2 || s.tab(t, 2).State != "idle" || s.tab(t, 4).State != "needs" {
		t.Errorf("first Alt+A: selected %d, tab 2 %q, tab 4 %q", s.SelectedTab, s.tab(t, 2).State, s.tab(t, 4).State)
	}
	if s := g.snap("jump2"); s.SelectedTab != 4 || s.tab(t, 4).State != "idle" {
		t.Errorf("second Alt+A: selected %d, tab 4 %q", s.SelectedTab, s.tab(t, 4).State)
	}
	if s := g.snap("jump3"); s.SelectedTab != 4 || s.LastToast != "No agent needs you" {
		t.Errorf("third Alt+A: selected %d, toast %q", s.SelectedTab, s.LastToast)
	}
	if s := g.snap("alt3"); s.SelectedTab != 3 {
		t.Errorf("Alt+3: selected %d", s.SelectedTab)
	}
	if s := g.snap("alt9"); s.SelectedTab != 3 {
		t.Errorf("Alt+9 with 4 tabs: selected %d", s.SelectedTab)
	}

	ring := g.snap("ring")
	p0, p4 := ring.pane(t, 0), ring.pane(t, 4)
	if !p0.Ring || !p0.Dimmed || p0.Focused || p4.Ring || p4.Dimmed || !p4.Focused {
		t.Errorf("split with a pane needing you: pane 0 %+v; pane 4 %+v", p0, p4)
	}
	ringPx := int(math.Round(2 * ring.Window.Scale))
	checkRing(t, "ring.png", loadPNG(t, g.file("ring.png")), ringPx, true)
	checkRing(t, "acked.png", loadPNG(t, g.file("acked.png")), ringPx, false)
	// Dimming fades text towards the background.
	if d, f := maxBright(loadPNG(t, g.file("ring.png")), ringPx+2), maxBright(loadPNG(t, g.file("focused.png")), ringPx+2); d >= f-20 {
		t.Errorf("text brightness: dimmed pane %d, focused pane %d", d, f)
	}
	if a := g.snap("acked"); !a.pane(t, 0).Focused || a.pane(t, 0).Ring || a.pane(t, 0).Status.State == "needs" {
		t.Errorf("after focusing the pane: %+v", a.pane(t, 0))
	}
}

// checkRing looks for the needs-you ring along a pane dump's edges.
func checkRing(t *testing.T, name string, img image.Image, px int, want bool) {
	t.Helper()
	b := img.Bounds()
	edge := []image.Point{{0, b.Dy() / 2}, {px - 1, b.Dy() / 2}, {b.Dx() / 2, 0}, {b.Dx() / 2, px - 1},
		{b.Dx() - 1, b.Dy() / 2}, {b.Dx() / 2, b.Dy() - 1}}
	for _, p := range edge {
		if got := at(img, p.X, p.Y) == colNeeds; got != want {
			t.Errorf("%s: pixel %v is %v; ring wanted: %v", name, p, at(img, p.X, p.Y), want)
		}
	}
	if want {
		if p := at(img, px+1, b.Dy()/2); p == colNeeds {
			t.Errorf("%s: the ring is wider than %d px", name, px)
		}
	}
}

// maxBright is the brightest channel in the top text row, inside margin m.
func maxBright(img image.Image, m int) int {
	best := 0
	for y := m; y < m+20; y++ {
		for x := m; x < m+60; x++ {
			p := at(img, x, y)
			best = max(best, p.r, p.g, p.b)
		}
	}
	return best
}

// TestGUIAgentStatus: a fake claude in a real pane sends an OSC 9
// notification, rings the bell and reports a failure through OSC 133;D;
// the tab follows each state, needs-you shows the tint and the ring, and
// focusing or typing acknowledges it.
func TestGUIAgentStatus(t *testing.T) {
	t.Parallel()
	bin := t.TempDir()
	// The fake agent goes on when the script creates go1, go2 and go3. It
	// writes output until go1, so it counts as working until then.
	must(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(`#!/bin/sh
echo "fake claude working"
until [ -e "$BUNK_TEST_DIR/go1" ]; do printf '\033[0m'; sleep 0.1; done
printf '\033]9;Approve edit to main.go?\007'
IFS= read -r answer
echo "got: $answer"
until [ -e "$BUNK_TEST_DIR/go2" ]; do sleep 0.05; done
printf '\007'
IFS= read -r answer
printf '\033]133;D;3\007'
until [ -e "$BUNK_TEST_DIR/go3" ]; do sleep 0.05; done
exit 3
`), 0o755))
	must(t, os.WriteFile(filepath.Join(bin, "aider"), []byte("#!/bin/sh\necho aider ready\nsleep 4\n"), 0o755))
	g := runGUI(t, scenario{
		cost:   4,
		config: "rail_open = true\n\n[[agents]]\nname = \"aider\"\nprocesses = [\"aider\"]\n",
		args:   []string{"--size", "100x30", "--tab", ""},
		env:    []string{"PATH=" + bin + ":" + os.Getenv("PATH")},
		script: []string{
			"kbd alt+2",
			"wait selected_tab=2 focused.line=$",
			"type claude\\r",
			"kbd alt+1",
			"wait selected_tab=1 focused.line=$",
			"type aider\\r",
			"wait tab.2.status.agent=claude tab.2.state=working tab.1.agent=aider",
			"snap working",
			"run touch go1",
			"wait tab.2.state=needs",
			"snap needs",
			"shot needs.png",
			"kbd alt+a",
			"wait selected_tab=2 tab.2.state!=needs tab.2.notice=\"\"",
			"snap acked",
			"type yes\\r",
			"wait pane.1.text=\"got: yes\"",
			"run touch go2",
			"wait tab.2.state=needs pane.1.ring=true",
			"snap bell",
			"dump-pane 1 bell.png",
			"type \\r",
			"wait tab.2.state=failed pane.1.ring=false",
			"snap failed",
			"shot failed.png",
			"run touch go3",
			// The process poll sees the shell back in the foreground.
			"wait tab.2.status.process=sh",
			"snap exited",
			"type sleep 1.5\\r",
			"wait tab.2.status.agent=\"\" tab.2.state=none",
			"snap next-command",
			"quit",
		},
		limit: 20e9,
	})
	if s := g.snap("working"); s.tab(t, 2).Status.Agent != "claude" || s.tab(t, 2).State != "working" || s.SelectedTab != 1 {
		t.Errorf("claude started: tab 2 %+v, selected %d", s.tab(t, 2).Status, s.SelectedTab)
	}
	if s := g.snap("working"); s.tab(t, 1).Agent != "aider" || !s.RailOpen {
		t.Errorf("config [[agents]] aider: tab 1 shows %q (%+v); rail open %v", s.tab(t, 1).Agent, s.tab(t, 1).Status, s.RailOpen)
	}
	s := g.snap("needs")
	if tb := s.tab(t, 2); tb.State != "needs" || tb.Notice != "Approve edit to main.go?" || !tb.Needs {
		t.Errorf("after OSC 9: tab 2 %+v", tb)
	} else {
		img := loadPNG(t, g.file("needs.png"))
		x, y, w, h := inner(tb.Item, 3)
		if n := count(img, x, y, w, h, colTint, 1); n < w*h/3 {
			t.Errorf("needs-you row tint: %d of %d pixels %v", n, w*h, colTint)
		}
		if n := blendCount(img, tb.Item.DX, tb.Item.DY, tb.Item.DW, tb.Item.DH, colNeeds, colTint); n < 30 {
			t.Errorf("needs-you dot, label and notice: %d pixels of %v", n, colNeeds)
		}
	}
	if s := g.snap("acked"); s.SelectedTab != 2 || s.tab(t, 2).State == "needs" || s.tab(t, 2).Notice != "" {
		t.Errorf("after Alt+A: selected %d, tab 2 %+v", s.SelectedTab, s.tab(t, 2))
	}
	b := g.snap("bell")
	if tb := b.tab(t, 2); tb.State != "needs" || !b.pane(t, 1).Ring {
		t.Errorf("after BEL: tab 2 %q, pane 1 ring %v", tb.State, b.pane(t, 1).Ring)
	}
	checkRing(t, "bell.png", loadPNG(t, g.file("bell.png")), int(math.Round(2*b.Window.Scale)), true)
	f := g.snap("failed")
	if tb := f.tab(t, 2); tb.State != "failed" || tb.Dot != "✕" || f.pane(t, 1).Ring {
		t.Errorf("after OSC 133;D;3: tab 2 %+v, ring %v", tb, f.pane(t, 1).Ring)
	} else {
		img := loadPNG(t, g.file("failed.png"))
		if n := blendCount(img, tb.Item.DX, tb.Item.DY, int(30*f.Window.Scale), tb.Item.DH, colFailed, colRailActv); n < 4 {
			t.Errorf("failed dot: %d pixels of %v", n, colFailed)
		}
	}
	if s := g.snap("exited"); s.tab(t, 2).State != "failed" || s.tab(t, 2).Status.Agent != "claude" {
		t.Errorf("after claude exited 3: tab 2 %+v", s.tab(t, 2).Status)
	}
	if s := g.snap("next-command"); s.tab(t, 2).Status.Agent != "" || s.tab(t, 2).State != "none" {
		t.Errorf("after the next command: tab 2 %+v", s.tab(t, 2).Status)
	}
}

// TestGUISplitsAndPointer drives splits with real keys and the chrome with
// real pointer events: F1 splits by aspect, Alt+arrows move focus, F12
// zooms, the divider drags, Alt+F1 opens a shell in the pane's container,
// exits close panes and tabs, and clicks on the chrome and the menu work.
func TestGUISplitsAndPointer(t *testing.T) {
	t.Parallel()
	bin := t.TempDir()
	must(t, os.WriteFile(filepath.Join(bin, "toolbox"), []byte(`#!/bin/sh
printf '%s\n' "$@" > "$BUNK_TEST_DIR/toolbox-args"
shift 3
exec "$@"
`), 0o755))
	g := runGUI(t, scenario{
		cost: 2.5,
		args: []string{"--size", "100x30"},
		env:  []string{"PATH=" + bin + ":" + os.Getenv("PATH")},
		script: []string{
			"maximize",
			"wait window.maximized=true window.w>1800 focused.settled=true",
			"kbd F1",
			"wait tab.1.panes.#=2 focused.settled=true",
			"kbd F1",
			"wait tab.1.panes.#=3 focused.settled=true",
			"kbd alt+Left",
			"wait focused_pane=0",
			"kbd F1",
			"wait tab.1.panes.#=4 focused_pane=3 pane.3.line=$",
			"wait idle 50ms pane.0.settled=true pane.1.settled=true pane.2.settled=true pane.3.settled=true",
			"snap grid",
			"shot grid.png",
			"kbd alt+Right",
			"wait focused_pane=2",
			"snap focus-right",
			"kbd alt+Up",
			"wait focused_pane=1 keyboard_focus=\"pane 1\"",
			"snap focus-up",
			"kbd F12",
			"wait zoomed_pane=1",
			"wait idle 50ms pane.1.settled=true",
			"snap zoomed",
			"kbd F12",
			"wait zoomed_pane=-1",
			"wait idle 50ms pane.0.settled=true pane.1.settled=true",
			"snap unzoomed",
			"ptr-at divider r",
			"snap before-drag",
			"ptr-press",
			"wait split_ratio!=null",
			"snap ratio-pressed",
			"ptr-by -150 0",
			"ptr-by -150 0",
			"wait input idle 50ms",
			"snap ratio-dragging",
			"ptr-release",
			"wait input idle 50ms split_ratio=null",
			"snap dragged",
			"fake-container toolbox fakebox",
			"kbd alt+F1",
			"wait tab.1.panes.#=5 focused.line=$",
			"snap container",
			"kbd alt+F1",
			"wait last_toast=\"This pane is not in a container\"",
			"snap no-container",
			"ptr-at newtab",
			"ptr-click",
			"wait tabs.#=2 selected_tab=2",
			"snap clicked-new",
			"ptr-at tab 1",
			"ptr-click",
			"wait selected_tab=1",
			"snap clicked-tab1",
			"ptr-at options",
			"ptr-click",
			"wait menu_open=true",
			"snap menu-open",
			"ptr-at menu Split pane",
			"ptr-click",
			"wait menu_open=false tab.1.panes.#=6",
			"snap menu-split",
			"wait focused.line=$",
			"type exit\\r",
			"wait tab.1.panes.#=5",
			"snap closed-pane",
			"kbd alt+2",
			"wait selected_tab=2 focused.line=$",
			"type exit\\r",
			"wait tabs.#=1 selected_tab=1",
			"snap closed-tab",
			"kbd f2",
			"wait tabs.#=2",
			"layout bar",
			"wait layout=bar",
			"ptr-at tabclose 2",
			"ptr-click",
			"wait tabs.#=1",
			"snap clicked-close",
			"quit",
		},
		limit: 20e9,
	})
	grid := g.snap("grid")
	if !grid.Window.Maximized {
		t.Fatalf("window not maximized: %+v", grid.Window)
	}
	// h( v(0, 3), v(1, 2) ): side by side first (the window is wide), then
	// each half stacked (taller than wide).
	tr := grid.Tree
	if tr == nil || tr.Split != "h" || tr.A == nil || tr.A.Split != "v" || tr.B == nil || tr.B.Split != "v" ||
		*tr.A.A.Pane != 0 || *tr.A.B.Pane != 3 || *tr.B.A.Pane != 1 || *tr.B.B.Pane != 2 {
		t.Fatalf("2x2 tree: %s", treeString(tr))
	}
	if grid.FocusedPane != 3 {
		t.Errorf("focused pane %d, want 3", grid.FocusedPane)
	}
	for _, p := range grid.Panes {
		if p.Dimmed != (p.ID != 3) || p.Rect == nil {
			t.Errorf("pane %d: dimmed %v, rect %v", p.ID, p.Dimmed, p.Rect)
		}
	}
	// Quadrants: pane 0 top-left, 3 bottom-left, 1 top-right, 2 bottom-right.
	r0, r1, r2, r3 := grid.pane(t, 0).Rect, grid.pane(t, 1).Rect, grid.pane(t, 2).Rect, grid.pane(t, 3).Rect
	if !(r0.X < r1.X && r3.X < r2.X && r0.Y < r3.Y && r1.Y < r2.Y) {
		t.Errorf("quadrants: 0 %+v 1 %+v 2 %+v 3 %+v", r0, r1, r2, r3)
	}
	img := loadPNG(t, g.file("grid.png"))
	focused, dimmed := texBright(img, grid.pane(t, 3)), texBright(img, grid.pane(t, 0))
	if dimmed >= focused-20 {
		t.Errorf("prompt brightness: dimmed pane %d, focused pane %d", dimmed, focused)
	}
	if d := tr.Divider; d == nil || d.W > 2 {
		t.Errorf("root divider %+v: want a 1 px line", d)
	}

	if s := g.snap("focus-right"); s.FocusedPane != 2 {
		t.Errorf("Alt+Right from pane 3: focused %d, want 2", s.FocusedPane)
	}
	if s := g.snap("focus-up"); s.FocusedPane != 1 || s.KeyboardFocus != "pane 1" {
		t.Errorf("Alt+Up from pane 2: focused %d, keyboard %q, want 1", s.FocusedPane, s.KeyboardFocus)
	}
	z := g.snap("zoomed")
	if z.ZoomedPane != 1 {
		t.Errorf("F12: zoomed %d, want 1", z.ZoomedPane)
	}
	if p := z.pane(t, 1); p.Rect == nil || p.Rect.W < float64(z.Window.W)-60 || p.Dimmed {
		t.Errorf("zoomed pane 1: %+v", p)
	}
	for _, id := range []int{0, 2, 3} {
		if z.pane(t, id).Visible {
			t.Errorf("pane %d visible while 1 is zoomed", id)
		}
	}
	if s := g.snap("unzoomed"); s.ZoomedPane != -1 || !s.pane(t, 0).Visible {
		t.Errorf("second F12: zoomed %d", s.ZoomedPane)
	}

	before, after := g.snap("before-drag"), g.snap("dragged")
	if d := before.Tree.Divider; d == nil || math.Abs(before.Pointer[0]-(d.X+d.W/2)) > 1.5 {
		t.Errorf("pointer %v not on the divider %+v", before.Pointer, d)
	}
	w0, w1 := before.pane(t, 0).Rect.W, after.pane(t, 0).Rect.W
	if math.Abs((w0-w1)-300) > 4 {
		t.Errorf("dragging the divider 300 px left: pane 0 width %v -> %v", w0, w1)
	}
	if pos0, pos1 := before.Tree.Position, after.Tree.Position; math.Abs(float64(pos0-pos1)-300) > 4 {
		t.Errorf("divider position %d -> %d", pos0, pos1)
	}
	// While the divider is held, a label on it shows the shares of the
	// split's width; the release hides it.
	for _, label := range []string{"ratio-pressed", "ratio-dragging"} {
		s := g.snap(label)
		r, d := s.SplitRatio, s.Tree.Divider
		if r == nil || r.Rect == nil || d == nil {
			t.Errorf("%s: split ratio %+v, divider %+v", label, r, d)
			continue
		}
		if cx := d.X + d.W/2; math.Abs(r.Rect.X+r.Rect.W/2-cx) > 2 {
			t.Errorf("%s: ratio label %+v not centred on the divider at x %.1f", label, r.Rect, cx)
		}
		total := s.pane(t, 0).Rect.W + s.pane(t, 1).Rect.W
		want := int(math.Round(100 * float64(s.Tree.Position) / total))
		if r.Text != fmt.Sprintf("%d/%d", want, 100-want) {
			t.Errorf("%s: ratio %q, want %d/%d (position %d of %.0f)", label, r.Text, want, 100-want, s.Tree.Position, total)
		}
	}
	if r := g.snap("ratio-dragging").SplitRatio; r != nil && r.Text == "50/50" {
		t.Errorf("ratio unchanged by the drag: %q", r.Text)
	}

	c := g.snap("container")
	if tb := c.tab(t, 1); len(tb.Panes) != 5 {
		t.Errorf("Alt+F1 in a container: tab 1 panes %v", tb.Panes)
	}
	if args, err := os.ReadFile(g.file("toolbox-args")); err != nil || string(args) != "run\n--container\nfakebox\n/bin/sh\n" {
		t.Errorf("toolbox ran with %q (%v)", args, err)
	}
	if s := g.snap("no-container"); s.LastToast != "This pane is not in a container" {
		t.Errorf("Alt+F1 outside a container: toast %q", s.LastToast)
	}
	if s := g.snap("clicked-new"); len(s.Tabs) != 2 || s.SelectedTab != 2 {
		t.Errorf("click on +: %d tabs, selected %d", len(s.Tabs), s.SelectedTab)
	}
	if s := g.snap("clicked-tab1"); s.SelectedTab != 1 {
		t.Errorf("click on tab 1: selected %d", s.SelectedTab)
	}
	if s := g.snap("menu-open"); !s.MenuOpen {
		t.Error("click on options: menu not open")
	}
	ms := g.snap("menu-split")
	if ms.MenuOpen || len(ms.tab(t, 1).Panes) != 6 {
		t.Errorf("click on Split pane: menu open %v, tab 1 panes %v", ms.MenuOpen, ms.tab(t, 1).Panes)
	}
	cp := g.snap("closed-pane")
	if len(cp.tab(t, 1).Panes) != 5 || slices.Contains(cp.tab(t, 1).Panes, ms.FocusedPane) {
		t.Errorf("exit in pane %d: tab 1 panes %v", ms.FocusedPane, cp.tab(t, 1).Panes)
	}
	if s := g.snap("closed-tab"); len(s.Tabs) != 1 || s.SelectedTab != 1 {
		t.Errorf("exit in the last pane of tab 2: %d tabs, selected %d", len(s.Tabs), s.SelectedTab)
	}
	if s := g.snap("clicked-close"); len(s.Tabs) != 1 || s.Layout != "bar" {
		t.Errorf("click on the bar tab's ×: %d tabs, layout %q", len(s.Tabs), s.Layout)
	}
}

func treeString(t *treeReport) string {
	switch {
	case t == nil:
		return "nil"
	case t.Pane != nil:
		return strings.Repeat("", 0) + string(rune('0'+*t.Pane))
	}
	return t.Split + "(" + treeString(t.A) + "," + treeString(t.B) + ")"
}

// texBright is the brightest channel in the top-left text of a pane's
// texture in a window shot.
func texBright(img image.Image, p paneReport) int {
	x0, y0 := p.Texture[0], p.Texture[1]
	best := 0
	for y := y0 + 2; y < y0+24; y++ {
		for x := x0 + 2; x < x0+40; x++ {
			c := at(img, x, y)
			best = max(best, c.r, c.g, c.b)
		}
	}
	return best
}

// TestGUITermProgram: panes name bunk-gui in TERM_PROGRAM, in place of the
// one bunk-gui inherited.
func TestGUITermProgram(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		args: []string{"--size", "80x10", "--command", "echo \"tp=$TERM_PROGRAM v=$TERM_PROGRAM_VERSION.\"; sleep 30"},
		env:  []string{"TERM_PROGRAM=outer-term", "TERM_PROGRAM_VERSION=9"},
		script: []string{
			"wait pane.0.text~\"tp=\"",
			"snap env",
			"quit",
		},
	})
	want := "tp=bunk-gui v=" + Version + "."
	if p := g.snap("env").pane(t, 0); !slices.Contains(p.Text, want) {
		t.Errorf("pane printed %q, want %q", p.Text, want)
	}
}
