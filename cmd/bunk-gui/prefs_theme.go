package main

// The Theme section of the preferences window: one choice per theme, the
// built-in ones and the config's [themes.NAME] tables, each with a preview
// of its colours. Picking one writes theme = "NAME" into config.toml, and
// the reload repaints every pane in it at once (themes.go).

import (
	"fmt"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/theme"
)

// themeChoice is one theme of the section and its radio button.
type themeChoice struct {
	name string
	b    *gtk.CheckButton
}

// addThemeSection appends the Theme section, with the theme of st chosen.
// The caller holds w.syncing, so setting the choice saves nothing.
func (w *prefsWindow) addThemeSection(box *gtk.Box, st Settings) {
	box.Append(heading("Theme"))
	hint := label("Colours of the terminal, the badges and the dividers between panes. "+
		"The terminal theme shows bunk-gui's own colours, those of default.", "statelabel")
	hint.SetWrap(true)
	hint.SetXAlign(0)
	box.Append(hint)
	w.themes = nil
	for _, name := range st.themes.Names() {
		t, _ := st.themes.Lookup(name)
		row := gtk.NewBox(gtk.OrientationHorizontal, 12)
		l := label(name, "shortcutdesc")
		l.SetXAlign(0)
		l.SetHExpand(true)
		row.Append(l)
		preview := gtk.NewLabel("")
		preview.SetMarkup(themePreview(t.Filled(defaultTheme())))
		row.Append(preview)
		b := gtk.NewCheckButton()
		b.SetChild(row)
		if len(w.themes) > 0 {
			b.SetGroup(w.themes[0].b)
		}
		b.SetActive(name == st.Theme)
		b.ConnectToggled(func() {
			if !w.syncing && b.Active() {
				w.saveTheme(name)
			}
		})
		w.controls["theme-"+name] = b
		w.themes = append(w.themes, themeChoice{name: name, b: b})
		box.Append(b)
	}
}

// saveTheme writes theme = name into config.toml; the reload shows it.
func (w *prefsWindow) saveTheme(name string) {
	w.saveTop(keysEdit{action: "theme", value: tomlString(name)})
	if f := w.a.cfg.theme; f != "" {
		w.message.SetText("Saved theme. --theme " + f + " is in force until bunk-gui restarts.")
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

// themePreview is Pango markup that shows t: a prompt in the foreground
// on the background, then blocks of the palette's red to cyan.
func themePreview(t theme.Theme) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<span font_family="monospace" background="%s" foreground="%s"> $ ls `, t.Background, t.Foreground)
	for i := theme.Red; i <= theme.Cyan; i++ {
		fmt.Fprintf(&b, `<span foreground="%s">██</span>`, t.ANSI[i])
	}
	b.WriteString(" </span>")
	return b.String()
}
