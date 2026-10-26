package main

// Themes (internal/theme, shared with bunk's TUI): the theme key of
// config.toml or --theme picks one, by name, among the built-in themes and
// the file's [themes.NAME] tables. The theme sets the terminal colours
// (the ANSI palette and the default foreground, background and cursor),
// the badge colours, the content background and the dividers between
// panes. The rest of the chrome keeps its own dark libadwaita style.
//
// bunk-gui has no host terminal to inherit colours from. Every colour a
// theme leaves to the host comes from the default theme, so the terminal
// theme shows bunk-gui's own colours, those of the default theme.
//
// Programs' colour overrides (OSC 4/10/11/12) stay in front of the theme:
// termView.loadColors applies them over it.

import (
	"fmt"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/session"
	"bunk/internal/theme"
)

// defaultTheme is the theme that fills the colours other themes leave to
// the host terminal.
func defaultTheme() theme.Theme {
	t, _ := theme.Builtin(theme.Default)
	return t
}

// selectTheme returns the theme the settings select, or --theme, with
// every colour set. An unknown --theme selects the default theme and an
// error to show.
func (a *App) selectTheme() (theme.Theme, error) {
	name := a.settings.Theme
	if a.cfg.theme != "" {
		name = a.cfg.theme
	}
	t, err := a.settings.themes.Select(name, theme.UI{})
	if err != nil {
		err = fmt.Errorf("--theme: %w", err)
	}
	return t.Filled(defaultTheme()), err
}

// applyTheme shows theme t (every colour set). Each pane draws one frame
// in the new colours; the badges, the content background and the
// dividers only restyle. It does nothing when t is the theme shown.
func (a *App) applyTheme(t theme.Theme) {
	if t == a.theme && a.themeCSS != nil {
		return
	}
	a.theme = t
	a.loadThemeCSS()
	for _, p := range a.panes {
		if p != nil && !p.removeOnce && p.s != nil {
			p.themeChanged()
		}
	}
}

// loadThemeCSS styles what follows the theme: the content background
// behind the panes, the dividers between them and the badges.
func (a *App) loadThemeCSS() {
	if a.themeCSS == nil {
		a.themeCSS = gtk.NewCSSProvider()
		gtk.StyleContextAddProviderForDisplay(gdk.DisplayGetDefault(), a.themeCSS, gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
	}
	a.themeCSS.LoadFromString(themeCSS(&a.theme))
}

// dividerDim is how far the 1-px dividers between panes blend the theme's
// inactive border toward its background; for the default theme that keeps
// them as quiet as the fixed #2a2c31 they replaced.
const dividerDim = 75

// dividerColor is the colour of the dividers between panes in theme t.
func dividerColor(t *theme.Theme) theme.Color {
	return theme.Mix(t.InactiveBorder, t.Background, dividerDim)
}

// themeCSS is the CSS of the colours that follow theme t.
func themeCSS(t *theme.Theme) string {
	// libadwaita draws the divider line as an inset shadow over the
	// background; without it the line is the background alone.
	return fmt.Sprintf(".bunk-content { background: %s; }\npaned > separator { background: %s; box-shadow: none; }\n",
		t.Background, dividerColor(t)) + badgeCSS(t)
}

// xparse writes c as OSC 10/11/12 replies give colours.
func xparse(c theme.Color) string { return session.XParseColor(fmt.Sprintf("%06x", c.Hex())) }

// themeChanged redraws the pane in the theme now in force: the session
// answers OSC 10/11/12 queries with its colours, and the next frame
// rebuilds every row in them. The view's background, drawn around the
// texture in the same frame, changes at once.
func (p *Pane) themeChanged() {
	t := &p.app.theme
	p.s.Lock()
	p.s.ThemeFG, p.s.ThemeBG, p.s.ThemeCursor = xparse(t.Foreground), xparse(t.Background), xparse(t.Cursor)
	p.tv.loadColors(p.s.Term)
	p.s.Unlock()
	p.tv.invalidate()
	viewSetBGC(p.view, unrgba(p.tv.defBG))
	p.queueRender()
}
