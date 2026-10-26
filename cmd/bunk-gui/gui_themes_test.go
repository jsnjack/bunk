package main

// Themes in the window (themes.go, prefs_theme.go): each built-in theme at
// start, a switch in Preferences with real clicks, and bad theme settings.
// Pixels are checked in window shots and pane dumps: the default
// background, a row of ANSI colours a shell prints, a badge and the
// divider between two panes.

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bunk/internal/theme"
)

// sgrCommand types a printf that prints "AB:", then the 16 ANSI colours
// as backgrounds (SGR 40-47, 100-107), two cells each. The command line
// itself shows no "AB:": printf makes it from octal escapes.
func sgrCommand() string {
	var b strings.Builder
	b.WriteString(`type printf '\\101\\102:`)
	for i := range 16 {
		code := 40 + i
		if i >= 8 {
			code = 100 + i - 8
		}
		fmt.Fprintf(&b, `\\033[%dm  `, code)
	}
	b.WriteString(`\\033[0m\\n'\r`)
	return b.String()
}

// sgrCol is the first column of ANSI colour i in the row sgrCommand prints.
func sgrCol(i int) int { return 3 + 2*i }

// cellAt is the pixel at the centre of cell (col, row) of pane p, whose
// texture starts at (ox, oy) in img.
func cellAt(img image.Image, ox, oy int, p paneReport, col, row int) rgb {
	return at(img, ox+col*p.Cell[0]+p.Cell[0]/2, oy+row*p.Cell[1]+p.Cell[1]/2)
}

// sgrRow is the row of pane p that shows sgrCommand's output, or -1.
func sgrRow(p paneReport) int {
	for i, l := range p.Text {
		if strings.HasPrefix(l, "AB:") {
			return i
		}
	}
	return -1
}

// checkPaneColors checks a pane's frame at (ox, oy) in img against theme
// th: its last row shows the default background, and, when wantSGR, the
// row of sgrCommand shows the 16 ANSI colours.
func checkPaneColors(t *testing.T, img image.Image, ox, oy int, p paneReport, th *theme.Theme, wantSGR bool, label string) {
	t.Helper()
	if c := cellAt(img, ox, oy, p, p.Cols/2, p.Rows-1); c.dist(hex(th.Background.Hex())) > 1 {
		t.Errorf("%s: pane %d background %v, want %v", label, p.ID, c, th.Background)
	}
	if !wantSGR {
		return
	}
	row := sgrRow(p)
	if row < 0 {
		t.Errorf("%s: pane %d shows no ANSI row: %q", label, p.ID, p.Text)
		return
	}
	for i, want := range th.ANSI {
		for _, col := range []int{sgrCol(i), sgrCol(i) + 1} {
			if c := cellAt(img, ox, oy, p, col, row); c.dist(hex(want.Hex())) > 1 {
				t.Errorf("%s: pane %d ANSI colour %d at column %d: %v, want %v", label, p.ID, i, col, c, want)
			}
		}
	}
}

// checkThemeShot checks a window shot of a tab split in two, pane 1
// focused with sgrCommand's row and a container badge, against theme th:
// both panes' backgrounds, the ANSI row, the badge and the divider.
func checkThemeShot(t *testing.T, img image.Image, s *uiSnapshot, th *theme.Theme, label string) {
	t.Helper()
	for _, id := range []int{0, 1} {
		p := s.pane(t, id)
		checkPaneColors(t, img, p.Texture[0], p.Texture[1], p, th, id == 1, label)
	}
	b := s.pane(t, 1).Badges
	if len(b) == 0 || b[0].Role != "container" {
		t.Errorf("%s: pane 1 badges %+v, want the container", label, b)
	} else {
		checkThemeBadgePixels(t, img, label, b[0], th)
	}
	d := s.Tree.Divider
	if d == nil {
		t.Errorf("%s: no divider", label)
		return
	}
	// A 1 px divider at 133 % covers more than half of at least one
	// device pixel per row.
	want := hex(dividerColor(th).Hex())
	if n := blendCount(img, d.DX-1, d.DY, d.DW+2, d.DH, want, hex(th.Background.Hex())); n < d.DH*4/5 {
		var row []string
		for x := d.DX - 3; x < d.DX+d.DW+3; x++ {
			row = append(row, at(img, x, d.DY+d.DH/2).String())
		}
		t.Errorf("%s: divider %+v has %d pixels of %v, want a line of %d; middle row from x %d: %v", label, *d, n, want, d.DH, d.DX-3, row)
	}
}

// themeSteps split the tab with F1, print the ANSI row in the focused
// pane and give it a container badge, whose background is a palette
// colour blended toward the theme background.
var themeSteps = []string{
	"wait focused.line=$",
	"kbd F1",
	"wait tab.1.panes.#=2 focused_pane=1 pane.1.line=$",
	sgrCommand(),
	"wait focused.text~AB: focused.line=$ focused.status.process=sh",
	"fake-container toolbox themebox",
	"wait focused.badges.0.role=container pane.0.settled=true pane.1.settled=true idle 200ms",
}

// TestGUIThemes starts bunk-gui with each built-in theme (theme key) and
// checks the theme in force and its pixels: both panes' default
// background, the 16 ANSI colours a shell prints, a container badge and
// the divider. The terminal theme shows the default theme's colours.
func TestGUIThemes(t *testing.T) {
	t.Parallel()
	def := defaultTheme()
	for _, b := range theme.Builtins() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			th := b.Filled(def)
			g := runGUI(t, scenario{
				cost:   1.5,
				config: "theme = \"" + b.Name + "\"\n",
				args:   []string{"--size", "120x30"},
				script: append(append([]string{}, themeSteps...), "snap shown", "shot shown.png", "quit"),
			})
			s := g.snap("shown")
			if s.Theme != b.Name || s.ConfigError != "" {
				t.Fatalf("theme %q, config error %q; want %q", s.Theme, s.ConfigError, b.Name)
			}
			checkThemeShot(t, loadPNG(t, g.file("shown.png")), s, &th, b.Name)
		})
	}
}

// TestGUIThemeFlag: --theme takes priority over the theme key.
func TestGUIThemeFlag(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		config: "theme = \"nord\"\n",
		args:   []string{"--size", "60x20", "--theme", "dracula"},
		script: []string{"wait focused.line=$ focused.settled=true idle 100ms", "snap start", "dump-pane 0 start.png", "quit"},
	})
	s := g.snap("start")
	if s.Theme != "dracula" {
		t.Fatalf("theme %q, want dracula", s.Theme)
	}
	d, _ := theme.Builtin("dracula")
	checkPaneColors(t, loadPNG(t, g.file("start.png")), 0, 0, s.focused(t), &d, false, "--theme dracula")
}

// TestGUIThemeSwitch picks themes in Preferences with real clicks: a
// built-in one, then a custom [themes.paper] table. The colours change
// at once, without a restart, while the dialog is open; each pane draws
// one frame for the switch; config.toml changes in the theme line only,
// its comment kept. Afterwards an idle window draws nothing, and typing
// allocates nothing in the frame path.
func TestGUIThemeSwitch(t *testing.T) {
	t.Parallel()
	const paperTable = `# a light theme of mine
[themes.paper]
background = "#FDF6E3"
foreground = "#586E75"
palette = ["#073642", "#DC322F", "#859900", "#B58900", "#268BD2", "#D33682", "#2AA198", "#EEE8D5",
           "#002B36", "#CB4B16", "#586E75", "#657B83", "#839496", "#6C71C4", "#93A1A1", "#FDF6E3"]
`
	cfg := "# bunk-gui test settings\ntheme = \"default\"   # the usual\nfont_size = 11\n\n" + paperTable
	script := append([]string{"maximize", "wait window.maximized=true window.w>1800"}, themeSteps...)
	script = append(script,
		"snap default", "shot default.png",
		"kbd ctrl+comma",
		"wait prefs!=null idle 300ms",
		"pref-scroll theme-dracula",
		"ptr-at pref theme-dracula",
		"wait idle 300ms",
		"snap before",
		"ptr-click",
		"wait theme=dracula prefs.theme=dracula idle 300ms",
		"snap dracula",
		"dump-pane 0 dracula-0.png", "dump-pane 1 dracula-1.png",
		"pref-scroll theme-paper",
		"ptr-at pref theme-paper", "ptr-click",
		"wait theme=paper prefs.theme=paper idle 300ms",
		"dialog-close",
		"wait prefs=null pane.0.settled=true pane.1.settled=true idle 300ms",
		"snap paper", "shot paper.png",
		"mark idle-start", "sleep 1s", "mark idle-end",
		"typing start 25ms", "sleep 300ms",
		"mark type-start", "sleep 1s", "mark type-end",
		"typing stop",
		"quit",
	)
	g := runGUI(t, scenario{
		cost:   3,
		config: cfg,
		args:   []string{"--alloc-probe", "--size", "100x30"},
		script: script,
		limit:  40e9,
	})
	def, dracula := defaultTheme(), func() theme.Theme { d, _ := theme.Builtin("dracula"); return d }()
	reg, err := theme.NewRegistry(map[string]theme.Def{"paper": {Background: "#FDF6E3", Foreground: "#586E75",
		Palette: []string{"#073642", "#DC322F", "#859900", "#B58900", "#268BD2", "#D33682", "#2AA198", "#EEE8D5",
			"#002B36", "#CB4B16", "#586E75", "#657B83", "#839496", "#6C71C4", "#93A1A1", "#FDF6E3"}}})
	must(t, err)
	paper, _ := reg.Lookup("paper")

	if s := g.snap("default"); s.Theme != theme.Default {
		t.Fatalf("start: theme %q", s.Theme)
	} else {
		checkThemeShot(t, loadPNG(t, g.file("default.png")), s, &def, "default")
	}
	before := g.snap("before")
	if p := before.Prefs; p == nil || p.Theme != theme.Default ||
		strings.Join(p.Themes, ",") != strings.Join(append(theme.BuiltinNames(), "paper"), ",") {
		t.Fatalf("Preferences offer %+v", before.Prefs)
	}
	after := g.snap("dracula")
	for _, id := range []int{0, 1} {
		if b, a := before.pane(t, id).Renders, after.pane(t, id).Renders; a != b+1 {
			t.Errorf("pane %d drew %d frames for the switch, want 1", id, a-b)
		}
	}
	// The panes, live behind the open dialog.
	for _, id := range []int{0, 1} {
		img := loadPNG(t, g.file(fmt.Sprintf("dracula-%d.png", id)))
		checkPaneColors(t, img, 0, 0, after.pane(t, id), &dracula, id == 1, "dracula, dialog open")
	}
	s := g.snap("paper")
	if s.Theme != "paper" {
		t.Fatalf("theme %q, want paper", s.Theme)
	}
	checkThemeShot(t, loadPNG(t, g.file("paper.png")), s, &paper, "paper")

	idle, typing := g.between("idle-start", "idle-end"), g.between("type-start", "type-end")
	t.Logf("idle: %d frames, %d frame-clock frames; typing: %d frames, %d objects by the frame path",
		idle.Frames, idle.ClockFrames, typing.Frames, typing.RenderMallocs)
	if idle.Frames != 0 || idle.ClockFrames != 0 {
		t.Errorf("idle after the switch: %d frames, %d frame-clock frames; want none", idle.Frames, idle.ClockFrames)
	}
	if typing.Frames < 10 || typing.RenderMallocs != 0 {
		t.Errorf("typing after the switch: %d frames, %d objects allocated by the frame path; want 0", typing.Frames, typing.RenderMallocs)
	}
	got, err := os.ReadFile(filepath.Join(g.dir, "config.toml"))
	must(t, err)
	if want := strings.Replace(cfg, `theme = "default"`, `theme = "paper"`, 1); string(got) != want {
		t.Errorf("config.toml:\n%s\nwant:\n%s", got, want)
	}
}

// TestGUIThemeErrors: an unknown theme, an invalid custom theme or an
// unknown --theme shows a toast and leaves the default theme.
func TestGUIThemeErrors(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, config string
		args         []string
		want         []string // in the toast
	}{
		{name: "unknown", config: "theme = \"solarized\"\n", want: []string{"Settings: ", `theme = "solarized"`}},
		{name: "invalid custom", config: "theme = \"mine\"\n[themes.mine]\nbackground = \"#1234\"\nforeground = \"#FFFFFF\"\n",
			want: []string{"Settings: ", `themes.mine.background: "#1234" is not #RRGGBB`, "themes.mine.palette has 0 colours", `theme = "mine"`}},
		{name: "unknown flag", args: []string{"--theme", "nope"}, want: []string{"--theme: ", `theme = "nope": no such theme`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			g := runGUI(t, scenario{config: c.config, args: append([]string{"--size", "60x20"}, c.args...),
				script: []string{"wait last_toast!=\"\" focused.line=$ focused.settled=true idle 100ms", "snap start",
					"dump-pane 0 start.png", "quit"}})
			s := g.snap("start")
			for _, w := range c.want {
				if !strings.Contains(s.LastToast, w) {
					t.Errorf("toast %q does not mention %q", s.LastToast, w)
				}
			}
			if s.Theme != theme.Default {
				t.Errorf("theme %q, want the default", s.Theme)
			}
			def := defaultTheme()
			checkPaneColors(t, loadPNG(t, g.file("start.png")), 0, 0, s.focused(t), &def, false, c.name)
		})
	}
}
