package main

// The Theme section of the preferences window: a grid of the themes, the
// built-in ones and the config's [themes.NAME] tables, each drawn as a
// small terminal in its colours above its name. Picking one writes
// theme = "NAME" into config.toml, and the reload repaints every pane in
// it at once (themes.go).

import (
	"math"

	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/theme"
)

// themeChoice is one theme of the section and its button.
type themeChoice struct {
	name string
	b    *gtk.ToggleButton
}

// addThemeSection adds the Theme section, with the theme of st chosen.
// The caller holds w.syncing, so setting the choice saves nothing.
func (w *prefsWindow) addThemeSection(st Settings) {
	g := w.group("Theme", "Colours of the terminal, the badges and the dividers between panes. "+
		"The terminal theme leaves the colours to bunk-gui's own, those of default. "+
		"Your own themes are [themes.NAME] tables in config.toml.")
	grid := gtk.NewFlowBox()
	grid.SetSelectionMode(gtk.SelectionNone)
	grid.SetHomogeneous(true)
	grid.SetMinChildrenPerLine(2)
	grid.SetMaxChildrenPerLine(4)
	grid.SetColumnSpacing(12)
	grid.SetRowSpacing(12)
	w.themes = nil
	for _, name := range st.themes.Names() {
		t, _ := st.themes.Lookup(name)
		card := gtk.NewBox(gtk.OrientationVertical, 6)
		card.Append(themeCard(t.Filled(defaultTheme())))
		card.Append(gtk.NewLabel(name))
		b := gtk.NewToggleButton()
		b.AddCSSClass("themecard")
		b.SetChild(card)
		b.SetTooltipText(name)
		if len(w.themes) > 0 {
			b.SetGroup(w.themes[0].b)
		}
		b.SetActive(name == st.Theme)
		b.ConnectToggled(func() {
			if !w.syncing && b.Active() {
				w.saveTheme(name)
			}
		})
		grid.Append(b)
		w.controls["theme-"+name] = b
		w.themes = append(w.themes, themeChoice{name: name, b: b})
	}
	g.Add(grid)
}

// themeCard draws t as a small terminal: a prompt in its foreground on
// its background, and its red to cyan below.
func themeCard(t theme.Theme) *gtk.DrawingArea {
	d := gtk.NewDrawingArea()
	d.SetContentWidth(120)
	d.SetContentHeight(56)
	d.SetCanTarget(false) // clicks go to the button
	d.SetDrawFunc(func(_ *gtk.DrawingArea, cr *cairo.Context, w, h int) {
		fw, fh := float64(w), float64(h)
		const r, pad, gap = 6.0, 10.0, 4.0
		cr.NewSubPath()
		cr.Arc(fw-r, r, r, -math.Pi/2, 0)
		cr.Arc(fw-r, fh-r, r, 0, math.Pi/2)
		cr.Arc(r, fh-r, r, math.Pi/2, math.Pi)
		cr.Arc(r, r, r, math.Pi, 3*math.Pi/2)
		cr.ClosePath()
		setColor(cr, t.Background)
		cr.Fill()
		setColor(cr, t.Foreground)
		cr.SelectFontFace("monospace", cairo.FontSlantNormal, cairo.FontWeightNormal)
		cr.SetFontSize(12)
		cr.MoveTo(pad, pad+12)
		cr.ShowText("$ ls")
		n := float64(theme.Cyan - theme.Red + 1)
		sw := (fw - 2*pad - (n-1)*gap) / n
		for i := theme.Red; i <= theme.Cyan; i++ {
			setColor(cr, t.ANSI[i])
			cr.Rectangle(pad+float64(i-theme.Red)*(sw+gap), fh-pad-10, sw, 10)
			cr.Fill()
		}
	})
	return d
}

// setColor makes c the colour cairo draws with.
func setColor(cr *cairo.Context, c theme.Color) {
	v := c.Hex()
	cr.SetSourceRGB(float64(v>>16&0xff)/255, float64(v>>8&0xff)/255, float64(v&0xff)/255)
}

// saveTheme writes theme = name into config.toml; the reload shows it.
func (w *prefsWindow) saveTheme(name string) {
	w.saveTop(keysEdit{action: "theme", value: tomlString(name)})
	if f := w.a.cfg.theme; f != "" {
		w.toast("Saved the theme. --theme " + f + " is in force until bunk-gui restarts.")
	}
}

// chosenTheme is the theme whose button is active.
func (w *prefsWindow) chosenTheme() string {
	for _, c := range w.themes {
		if c.b.Active() {
			return c.name
		}
	}
	return ""
}
