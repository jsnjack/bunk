package main

// The preferences window (action "preferences"): a control for each key
// of config.toml: layout, the rail and its peek delay, where new tabs
// start, the font and the default text zoom, the theme, session restore,
// desktop notifications (alerts.go), agent hooks, documents and the
// [[agents]] list. [keys] has a window of its own (shortcuts.go), which a
// row opens, and [themes.NAME] tables are written by hand: Open in
// editor opens config.toml in the default application for it. Every
// change is written into config.toml at once by the line editor
// (keysfile.go), so the user's comments and order stay; the reload
// applies it.
//
// The window is an AdwPreferencesDialog with four pages: General,
// Appearance, Notifications and Agents. Each section is a group of rows,
// one setting to a row: its title on the left, its control on the right
// and any explanation in the subtitle. A plain change needs no message,
// since the control shows it; refusals, warnings and errors arrive as
// toasts.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	"bunk/internal/session"
)

type prefsWindow struct {
	a        *App
	dlg      *adw.PreferencesDialog
	page     *adw.PreferencesPage     // the page sections are added to
	controls map[string]gtk.Widgetter // by name, for tests
	message  string                   // the last toast's text, for --report
	syncing  bool

	position, railStyle *adw.ToggleGroup // tabs ("rail" or "bar") and rail_style ("ring" or "map")
	railRows            []gtk.Widgetter  // rows that apply only to tabs on the side
	railOpen            *adw.SwitchRow
	peekDelay           *adw.SpinRow     // rail_peek_delay, ms
	dirHome, dirCur     *gtk.CheckButton // new_dir = "~" or "current"
	dirOther            *gtk.CheckButton // new_dir is the folder in dir
	dir                 *adw.EntryRow
	restore, restoreSB  *adw.SwitchRow // restore and restore_scrollback
	font                *gtk.FontDialogButton
	fontSize, zoom      *adw.SpinRow
	themes              []themeChoice // the Theme section (prefs_theme.go)
	notify              *notifyPrefs
	setup               *setupPrefs    // each agent's hooks and skill (prefs_hooks.go)
	presentAuto         *adw.SwitchRow // [present] auto
	hooksHint           *adw.SwitchRow // hooks_hint

	agents   *adw.PreferencesGroup // the [[agents]] entries
	addAgent *adw.ButtonRow        // last in agents
	rows     []*agentRow
}

// agentRow is one [[agents]] entry: an expander row titled with its name,
// holding a field for each key.
type agentRow struct {
	row                      *adw.ExpanderRow
	name, processes, scripts *adw.EntryRow
	saved                    bool // the entry exists in the file
}

func (a *App) showPreferences() {
	if a.prefsWin != nil {
		return
	}
	w := &prefsWindow{a: a, controls: map[string]gtk.Widgetter{}}
	st := a.fileSettings
	w.dlg = adw.NewPreferencesDialog()
	w.dlg.SetTitle("Preferences")
	w.dlg.SetContentWidth(640)
	w.dlg.SetContentHeight(720)
	w.dlg.SetSearchEnabled(true)

	w.syncing = true
	w.addPage("general", "General", "preferences-system-symbolic")
	w.addTabs(st)
	w.addNewDir(st)
	w.addRestore(st)
	w.addSettingsFile()
	w.addPage("appearance", "Appearance", "preferences-desktop-appearance-symbolic")
	w.addThemeSection(st)
	w.addText(st)
	w.addPage("notifications", "Notifications", "preferences-system-notifications-symbolic")
	w.addNotifications()
	w.addPage("agents", "Agents", "utilities-terminal-symbolic")
	w.addSetup()
	w.addAgentBehaviour()
	w.addAgents()
	w.syncing = false

	w.dlg.ConnectClosed(func() {
		a.prefsWin = nil
		// Keyboard shortcuts opens its window as this one closes.
		if p := a.activePane(); p != nil && !a.modalOpen() {
			p.widget.GrabFocus()
		}
	})
	a.prefsWin = w
	w.dlg.Present(a.win)
}

// addPage adds a page to the window; the sections added next go on it.
func (w *prefsWindow) addPage(name, title, icon string) {
	w.page = adw.NewPreferencesPage()
	w.page.SetName(name)
	w.page.SetTitle(title)
	w.page.SetIconName(icon)
	w.dlg.Add(w.page)
}

// addTabs adds where the tabs are and, for tabs on the side, whether the
// rail starts expanded, how it draws a collapsed tab and how long Alt is
// held alone before it labels them.
func (w *prefsWindow) addTabs(st Settings) {
	g := w.group("Tabs", "")
	w.position = w.toggleRow(g, "Position", "", st.Tabs, []toggleChoice{
		{"rail", "Side", "tabs-rail"}, {"bar", "Top", "tabs-bar"},
	}, func(name string) {
		w.saveTop(keysEdit{action: "tabs", value: tomlString(name)})
		w.syncRail()
	})
	w.railOpen = w.switchRow(g, "rail-open", "Side tabs expanded at start", "Each window opens with the tabs' labels shown",
		st.RailOpen, func(on bool) { w.saveTop(keysEdit{action: "rail_open", value: strconv.FormatBool(on)}) })
	style := "ring"
	if st.RailStyle == "map" {
		style = "map"
	}
	var styleRow *adw.ActionRow
	w.railStyle, styleRow = w.toggleRowWithRow(g, "Collapsed side tabs show", "A ring of the panes' states, or a map of the panes as they are split",
		style, []toggleChoice{{"ring", "Ring", "rail-ring"}, {"map", "Map", "rail-map"}},
		func(name string) { w.saveTop(keysEdit{action: "rail_style", value: tomlString(name)}) })
	w.peekDelay = w.spinRow(g, "peek-delay", "Label delay", "Milliseconds to hold Alt alone to label the collapsed tabs",
		0, railPeekDelayMax, 50, 0, float64(st.RailPeekDelay),
		func(v float64) { w.saveTop(keysEdit{action: "rail_peek_delay", value: strconv.Itoa(int(v))}) })
	w.railRows = []gtk.Widgetter{w.railOpen, styleRow, w.peekDelay}
	w.syncRail()
}

// syncRail makes the rail's rows usable only while the tabs are on the
// side, where they apply.
func (w *prefsWindow) syncRail() {
	on := w.position.ActiveName() != "bar"
	for _, r := range w.railRows {
		gtk.BaseWidget(r).SetSensitive(on)
	}
}

// addNewDir adds where new tabs and windows start (new_dir): the home
// folder, the active pane's, or a folder typed in, saved on Enter or with
// the field's apply button.
func (w *prefsWindow) addNewDir(st Settings) {
	g := w.group("New tabs and windows", "Where their first pane starts. A split always starts in its pane's folder.")
	w.dirHome, _ = w.radioRow(g, "dir-home", "Your home folder", nil)
	w.dirCur, _ = w.radioRow(g, "dir-current", "The active pane's folder", w.dirHome)
	w.dirOther = gtk.NewCheckButton()
	w.dirOther.SetGroup(w.dirHome)
	w.dirOther.SetVAlign(gtk.AlignCenter)
	w.dir = adw.NewEntryRow()
	w.dir.SetUseMarkup(false)
	w.dir.SetTitle("Another folder, such as ~/projects")
	w.dir.SetShowApplyButton(true)
	w.dir.AddPrefix(w.dirOther)
	switch st.NewDir {
	case "~", "":
		w.dirHome.SetActive(true)
	case "current":
		w.dirCur.SetActive(true)
	default:
		w.dirOther.SetActive(true)
		w.dir.SetText(st.NewDir)
	}
	w.dirHome.ConnectToggled(func() {
		if !w.syncing && w.dirHome.Active() {
			w.saveTop(keysEdit{action: "new_dir", value: tomlString("~")})
		}
	})
	w.dirCur.ConnectToggled(func() {
		if !w.syncing && w.dirCur.Active() {
			w.saveTop(keysEdit{action: "new_dir", value: tomlString("current")})
		}
	})
	w.dirOther.ConnectToggled(func() {
		if !w.syncing && w.dirOther.Active() {
			if strings.TrimSpace(w.dir.Text()) == "" {
				w.toast("Type a folder and press Enter.")
				w.dir.GrabFocus()
				return
			}
			w.saveNewDir()
		}
	})
	w.dir.ConnectApply(w.saveNewDir)
	g.Add(w.dir)
	w.controls["dir-other"], w.controls["dir"] = w.dirOther, w.dir
}

// saveNewDir saves the folder typed in as new_dir and chooses it.
func (w *prefsWindow) saveNewDir() {
	dir := strings.TrimSpace(w.dir.Text())
	if dir == "" || dir == "current" || !validNewDir(dir) {
		w.toast("A folder is an absolute path, or starts with ~/.")
		return
	}
	w.syncing = true
	w.dirOther.SetActive(true)
	w.syncing = false
	w.saveTop(keysEdit{action: "new_dir", value: tomlString(dir)})
	if got := newDir(dir, nil); dir != "~" && got != expandHome(dir) {
		w.toast(dir + " is not a folder, so new tabs start in your home folder until it is.")
	}
}

// expandHome turns a leading ~ into the home directory.
func expandHome(dir string) string {
	home, _ := os.UserHomeDir()
	if dir == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(dir, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return dir
}

// addText adds the font, chosen among the installed monospace fonts, its
// size and the default text zoom.
func (w *prefsWindow) addText(st Settings) {
	g := w.group("Text", "")
	dlg := gtk.NewFontDialog()
	dlg.SetTitle(fontDialogTitle)
	dlg.SetFilter(&monospaceFilter().Filter)
	w.font = gtk.NewFontDialogButton(dlg)
	w.font.SetLevel(gtk.FontLevelFamily)
	w.font.SetUseFont(true)
	w.font.SetVAlign(gtk.AlignCenter)
	desc := pango.NewFontDescription()
	desc.SetFamily(st.Font)
	w.font.SetFontDesc(desc)
	w.font.NotifyProperty("font-desc", func() {
		if f := w.fontFamily(); !w.syncing && f != "" {
			w.saveTop(keysEdit{action: "font", value: tomlString(f)})
		}
	})
	row := actionRow("Font", "")
	row.AddSuffix(w.font)
	row.SetActivatableWidget(w.font)
	g.Add(row)
	w.controls["font"] = w.font
	w.fontSize = w.spinRow(g, "font-size", "Size", "In points", 4, 96, 0.5, 1, st.FontSize,
		func(v float64) { w.saveTop(keysEdit{action: "font_size", value: tomlFloat(v)}) })
	w.zoom = w.spinRow(g, "text-zoom", "Default zoom", "In percent", 50, 200, 10, 0, st.TextZoom,
		func(v float64) { w.saveTop(keysEdit{action: "text_zoom", value: tomlFloat(v)}) })
}

// fontDialogTitle names the font dialog, which tests look for.
const fontDialogTitle = "Terminal Font"

// fontFamily is the family the font button shows.
func (w *prefsWindow) fontFamily() string {
	if d := w.font.FontDesc(); d != nil {
		return d.Family()
	}
	return ""
}

// monospaceFilter keeps the font families whose characters all have one
// width, as the terminal's grid needs.
func monospaceFilter() *gtk.CustomFilter {
	return gtk.NewCustomFilter(func(item *coreglib.Object) bool {
		switch f := item.Cast().(type) {
		case pango.FontFamilier:
			return pango.BaseFontFamily(f).IsMonospace()
		case pango.FontFacer:
			if fam := pango.BaseFontFace(f).Family(); fam != nil {
				return pango.BaseFontFamily(fam).IsMonospace()
			}
		}
		return true
	})
}

// fontDialogOpen reports whether the font dialog is on screen.
func fontDialogOpen() bool {
	tops := gtk.WindowGetToplevels()
	for i := uint(0); i < tops.NItems(); i++ {
		w, ok := tops.Item(i).Cast().(interface {
			Title() string
			IsVisible() bool
		})
		if ok && w.IsVisible() && w.Title() == fontDialogTitle {
			return true
		}
	}
	return false
}

// addRestore adds session restore: whether windows are saved and offered
// back at start (restore), and with them each pane's scrollback
// (restore_scrollback), which needs the first.
func (w *prefsWindow) addRestore(st Settings) {
	g := w.group("Session restore", "")
	w.restore = w.switchRow(g, "restore", "Restore windows", "Save the windows and offer them back at the next start", st.restore,
		func(on bool) { w.saveTop(keysEdit{action: "restore", value: strconv.FormatBool(on)}) })
	w.restoreSB = w.switchRow(g, "restore-scrollback", "Include scrollback", "Save each pane's scrollback with its window", st.restoreSB,
		func(on bool) { w.saveTop(keysEdit{action: "restore_scrollback", value: strconv.FormatBool(on)}) })
	w.restoreSB.SetSensitive(st.restore)
	w.restore.NotifyProperty("active", func() { w.restoreSB.SetSensitive(w.restore.Active()) })
}

// addSettingsFile adds the file the window writes, with a button that
// opens it, and the row that opens the keyboard shortcuts.
func (w *prefsWindow) addSettingsFile() {
	g := w.group("Settings file", "Changes are saved as you make them. Themes of your own and comments are written by hand.")
	file := actionRow("config.toml", tildePath(w.a.cfg.configFile))
	file.SetSubtitleSelectable(true)
	edit := gtk.NewButtonWithLabel("Open in editor")
	edit.SetVAlign(gtk.AlignCenter)
	edit.ConnectClicked(w.openInEditor)
	file.AddSuffix(edit)
	g.Add(file)
	keys := actionRow("Keyboard shortcuts", "")
	keys.AddSuffix(gtk.NewImageFromIconName("go-next-symbolic"))
	keys.SetActivatable(true)
	keys.ConnectActivated(func() {
		w.dlg.Close()
		w.a.showShortcuts()
	})
	g.Add(keys)
	w.controls["open-editor"], w.controls["shortcuts"] = edit, keys
}

// openInEditor closes the preferences and opens config.toml in the
// default application for it, a text editor, creating the file if there
// is none. The watcher applies what the user saves there.
func (w *prefsWindow) openInEditor() {
	path := w.a.cfg.configFile
	if err := ensureFile(path); err != nil {
		w.toast("Could not open the settings: " + err.Error())
		return
	}
	w.dlg.Close()
	openURL(w.a, (&url.URL{Scheme: "file", Path: path}).String())
}

// ensureFile creates path, empty, unless it exists.
func ensureFile(path string) error {
	if path == "" {
		return errors.New("no config file")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create settings: %w", err)
	}
	return f.Close()
}

// errNoControl: the window has no control of that name.
var errNoControl = errors.New("no preference control")

// scrollTo shows the page a control is on, expands the rows it is in and
// scrolls it into view (tests aim the pointer at it). Until the page and
// rows are laid out it fails, and scrollWhenReady tries again.
func (w *prefsWindow) scrollTo(name string) error {
	c, ok := w.controls[name]
	if !ok {
		return fmt.Errorf("%w %s", errNoControl, name)
	}
	var sw *gtk.ScrolledWindow
	for p := gtk.BaseWidget(c).Parent(); p != nil; p = gtk.BaseWidget(p).Parent() {
		switch p := p.(type) {
		case *adw.ExpanderRow:
			p.SetExpanded(true)
		case *gtk.ScrolledWindow:
			if sw == nil {
				sw = p
			}
		case *adw.PreferencesPage:
			if w.dlg.VisiblePageName() != p.Name() {
				w.dlg.SetVisiblePageName(p.Name())
			}
		}
	}
	if sw == nil {
		return errors.New("preference control not in the window")
	}
	if w.animating() {
		return errors.New("preference rows still moving")
	}
	content := sw.Child()
	if vp, ok := content.(*gtk.Viewport); ok {
		content = vp.Child()
	}
	b, ok := gtk.BaseWidget(c).ComputeBounds(content)
	if !ok || !gtk.BaseWidget(c).IsDrawable() {
		return errors.New("preference control not laid out")
	}
	adj := sw.VAdjustment()
	want := max(adj.Lower(), min(float64(b.Y())-60, adj.Upper()-adj.PageSize()))
	if math.Abs(adj.Value()-want) > 0.5 {
		// The focus moves to the control, or leaves the page when the
		// control takes none, so the viewport does not scroll back to
		// the widget that had it.
		if !gtk.BaseWidget(c).GrabFocus() {
			w.dlg.SetFocus(nil)
		}
		adj.SetValue(want)
		return errors.New("preferences scrolling")
	}
	// The control is in place once the scrolled content is laid out
	// again at the new offset.
	if at, ok := gtk.BaseWidget(c).ComputeBounds(sw); !ok || math.Abs(float64(at.Y()-b.Y())+want) > 0.5 {
		return errors.New("preferences scrolling")
	}
	return nil
}

// scrollWhenReady scrolls to the control named name once its page and
// rows are laid out, for up to 2 s, then calls done with the outcome.
func (w *prefsWindow) scrollWhenReady(name string, done func(error)) {
	deadline := time.Now().Add(2 * time.Second)
	coreglib.TimeoutAdd(16, func() bool {
		if w.a.prefsWin != w {
			done(errors.New("the preferences window closed"))
			return false
		}
		err := w.scrollTo(name)
		if err != nil && !errors.Is(err, errNoControl) && time.Now().Before(deadline) {
			return true
		}
		done(err)
		return false
	})
}

// animating reports whether a row of the window is still expanding or
// collapsing, so its fields are not where they end up.
func (w *prefsWindow) animating() bool {
	moving := false
	walkWidgets(w.dlg, func(c *gtk.Widget) {
		if r, ok := c.Cast().(*gtk.Revealer); ok && r.RevealChild() != r.ChildRevealed() {
			moving = true
		}
	})
	return moving
}

// group adds a section to the page: its title unless title is empty, the
// description under it unless desc is empty, and the boxed list its rows
// go in. Widgets that are not rows go below the list.
func (w *prefsWindow) group(title, desc string) *adw.PreferencesGroup {
	g := adw.NewPreferencesGroup()
	if title != "" {
		g.SetTitle(title)
	}
	if desc != "" {
		g.SetDescription(glib.MarkupEscapeText(desc))
	}
	w.page.Add(g)
	return g
}

// actionRow is a row with a title and, unless it is empty, a subtitle,
// both plain text.
func actionRow(title, subtitle string) *adw.ActionRow {
	r := adw.NewActionRow()
	r.SetUseMarkup(false)
	r.SetTitle(title)
	if subtitle != "" {
		r.SetSubtitle(subtitle)
	}
	return r
}

// switchRow adds a row with a switch, which calls save as the user turns
// it.
func (w *prefsWindow) switchRow(g *adw.PreferencesGroup, name, title, subtitle string, on bool, save func(bool)) *adw.SwitchRow {
	r := adw.NewSwitchRow()
	r.SetUseMarkup(false)
	r.SetTitle(title)
	if subtitle != "" {
		r.SetSubtitle(subtitle)
	}
	r.SetActive(on)
	r.NotifyProperty("active", func() {
		if !w.syncing {
			save(r.Active())
		}
	})
	g.Add(r)
	w.controls[name] = r
	return r
}

// spinRow adds a row with a number, which calls save as it changes. Its
// control for tests is the text inside, where a click edits the number.
func (w *prefsWindow) spinRow(g *adw.PreferencesGroup, name, title, subtitle string, lo, hi, step float64, digits uint,
	value float64, save func(float64)) *adw.SpinRow {
	r := adw.NewSpinRowWithRange(lo, hi, step)
	r.SetUseMarkup(false)
	r.SetTitle(title)
	if subtitle != "" {
		r.SetSubtitle(subtitle)
	}
	r.SetDigits(digits)
	r.SetValue(value)
	r.NotifyProperty("value", func() {
		if !w.syncing {
			save(r.Value())
		}
	})
	g.Add(r)
	w.controls[name] = childOf[*gtk.Text](r)
	return r
}

// radioRow adds a row whose title chooses it, with the check button of
// group's choice.
func (w *prefsWindow) radioRow(g *adw.PreferencesGroup, name, title string, group *gtk.CheckButton) (*gtk.CheckButton, *adw.ActionRow) {
	c := gtk.NewCheckButton()
	if group != nil {
		c.SetGroup(group)
	}
	c.SetVAlign(gtk.AlignCenter)
	r := actionRow(title, "")
	r.AddPrefix(c)
	r.SetActivatableWidget(c)
	g.Add(r)
	w.controls[name] = r
	return c, r
}

// toggleChoice is one button of a toggle row: its value, its label and
// its control's name for tests.
type toggleChoice struct{ name, label, control string }

func (w *prefsWindow) toggleRow(g *adw.PreferencesGroup, title, subtitle, active string, choices []toggleChoice,
	save func(string)) *adw.ToggleGroup {
	t, _ := w.toggleRowWithRow(g, title, subtitle, active, choices, save)
	return t
}

// toggleRowWithRow adds a row with a choice between a few values shown
// side by side, which calls save with the value chosen.
func (w *prefsWindow) toggleRowWithRow(g *adw.PreferencesGroup, title, subtitle, active string, choices []toggleChoice,
	save func(string)) (*adw.ToggleGroup, *adw.ActionRow) {
	tg := adw.NewToggleGroup()
	for _, c := range choices {
		t := adw.NewToggle()
		t.SetName(c.name)
		t.SetLabel(c.label)
		tg.Add(t)
	}
	tg.SetActiveName(active)
	tg.SetVAlign(gtk.AlignCenter)
	tg.NotifyProperty("active", func() {
		if !w.syncing {
			save(tg.ActiveName())
		}
	})
	r := actionRow(title, subtitle)
	r.AddSuffix(tg)
	g.Add(r)
	for _, c := range choices {
		w.controls[c.control] = labelIn(tg, c.label)
	}
	return tg, r
}

// childOf is the first widget of type T in w, or w when there is none.
func childOf[T gtk.Widgetter](w gtk.Widgetter) gtk.Widgetter {
	var found gtk.Widgetter
	walkWidgets(w, func(c *gtk.Widget) {
		if t, ok := c.Cast().(T); ok && found == nil {
			found = t
		}
	})
	if found == nil {
		return w
	}
	return found
}

// labelIn is the label in w that reads text, or w when there is none.
func labelIn(w gtk.Widgetter, text string) gtk.Widgetter {
	var found gtk.Widgetter = w
	walkWidgets(w, func(c *gtk.Widget) {
		if l, ok := c.Cast().(*gtk.Label); ok && l.Label() == text {
			found = l
		}
	})
	return found
}

// fileAgents reads the [[agents]] entries as written, invalid ones too, so
// rows match the file's entries one to one.
func fileAgents(path string) []session.AgentSpec {
	var f struct {
		Agents []session.AgentSpec `toml:"agents"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Log(context.Background(), LevelTrace, "preferences: read agents", "path", path, "err", err)
		}
		return nil
	}
	// A malformed file still gives the entries before the error; the
	// settings toast already reports it.
	if _, err := toml.Decode(string(data), &f); err != nil {
		slog.Log(context.Background(), LevelTrace, "preferences: decode agents", "path", path, "err", err)
	}
	return f.Agents
}

// addAgents adds the [[agents]] entries, one expander row each, and the
// row that adds one.
func (w *prefsWindow) addAgents() {
	w.agents = w.group("Other agents", "Agent CLIs to recognise besides the built-in ones, by their process names or script paths.")
	w.addAgent = adw.NewButtonRow()
	w.addAgent.SetTitle("Add agent")
	w.addAgent.SetStartIconName("list-add-symbolic")
	w.addAgent.ConnectActivated(func() { w.addRow(session.AgentSpec{}, false) })
	w.agents.Add(w.addAgent)
	w.controls["agent-add"] = w.addAgent
	w.loadAgents()
}

func (w *prefsWindow) loadAgents() {
	for _, r := range w.rows {
		w.agents.Remove(r.row)
	}
	w.rows = nil
	for k := range w.controls {
		if strings.HasPrefix(k, "agent-") && k != "agent-add" {
			delete(w.controls, k)
		}
	}
	for _, ag := range fileAgents(w.a.cfg.configFile) {
		w.addRow(ag, true)
	}
}

// addRow adds an entry's row above Add agent; a new one opens with its
// name to type.
func (w *prefsWindow) addRow(ag session.AgentSpec, saved bool) {
	r := &agentRow{saved: saved, row: adw.NewExpanderRow()}
	i := len(w.rows)
	w.rows = append(w.rows, r)
	r.row.SetUseMarkup(false)
	entry := func(title, text, key string) *adw.EntryRow {
		e := adw.NewEntryRow()
		e.SetUseMarkup(false)
		e.SetTitle(title)
		e.SetText(text)
		e.SetShowApplyButton(true)
		e.ConnectApply(func() { w.saveAgent(r) })
		w.controls["agent-"+itoa(i)+"-"+key] = e
		r.row.AddRow(e)
		return e
	}
	r.name = entry("Name", ag.Name, "name")
	r.processes = entry("Process names, comma separated", strings.Join(ag.Processes, ", "), "processes")
	r.scripts = entry("Script paths, comma separated", strings.Join(ag.Scripts, ", "), "scripts")
	rm := gtk.NewButtonFromIconName("user-trash-symbolic")
	rm.AddCSSClass("flat")
	rm.SetVAlign(gtk.AlignCenter)
	rm.SetTooltipText("Remove")
	rm.ConnectClicked(func() { w.removeAgent(r) })
	r.row.AddSuffix(rm)
	w.controls["agent-"+itoa(i)] = r.row
	w.controls["agent-"+itoa(i)+"-remove"] = rm
	w.titleAgent(r)
	w.agents.Remove(w.addAgent)
	w.agents.Add(r.row)
	w.agents.Add(w.addAgent)
	if !saved {
		r.row.SetExpanded(true)
		r.name.GrabFocus()
	}
}

// titleAgent titles r's row with the entry's name, its processes and
// scripts under it.
func (w *prefsWindow) titleAgent(r *agentRow) {
	name := strings.TrimSpace(r.name.Text())
	if name == "" {
		name = "New agent"
	}
	r.row.SetTitle(name)
	r.row.SetSubtitle(strings.Join(append(splitList(r.processes.Text()), splitList(r.scripts.Text())...), ", "))
}

func itoa(i int) string { return strconv.Itoa(i) }

func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
}

// index is r's entry among the file's [[agents]].
func (w *prefsWindow) index(r *agentRow) int {
	n := 0
	for _, x := range w.rows {
		if x == r {
			return n
		}
		if x.saved {
			n++
		}
	}
	return -1
}

func (w *prefsWindow) saveAgent(r *agentRow) {
	name := strings.TrimSpace(r.name.Text())
	procs, scripts := splitList(r.processes.Text()), splitList(r.scripts.Text())
	if name == "" || len(procs)+len(scripts) == 0 {
		w.toast("An agent needs a name and processes or scripts.")
		return
	}
	edits := []keysEdit{{action: "name", value: tomlString(name)}}
	for _, kv := range []struct {
		key  string
		vals []string
	}{{"processes", procs}, {"scripts", scripts}} {
		if len(kv.vals) > 0 {
			edits = append(edits, keysEdit{action: kv.key, value: tomlStrings(kv.vals)})
		} else {
			edits = append(edits, keysEdit{action: kv.key, remove: true})
		}
	}
	i := w.index(r)
	if !w.save(func(content string) string {
		if r.saved {
			return patchArrayEntry(content, "agents", i, edits)
		}
		return appendArrayEntry(content, "agents", edits)
	}) {
		return
	}
	r.saved = true
	w.message = ""
	w.titleAgent(r)
}

func (w *prefsWindow) removeAgent(r *agentRow) {
	if r.saved {
		i := w.index(r)
		if !w.save(func(content string) string { return removeArrayEntry(content, "agents", i) }) {
			return
		}
	}
	w.loadAgents()
	w.message = ""
}

// saveTop writes a top-level key; the control already shows the change.
func (w *prefsWindow) saveTop(e keysEdit) {
	if w.save(func(content string) string { return patchTable(content, "", []keysEdit{e}) }) {
		w.message = ""
	}
}

// save rewrites config.toml through edit and applies it at once (the file
// watcher would too, a moment later). It says why when it cannot.
func (w *prefsWindow) save(edit func(string) string) bool {
	if err := w.a.editConfig(edit); err != nil {
		w.toast("Could not save: " + err.Error())
		return false
	}
	w.a.reloadSettings()
	return true
}

// showNote shows text in a section's note, which is hidden while empty.
func showNote(l *gtk.Label, text string) {
	l.SetText(text)
	l.SetVisible(text != "")
}

// toast shows text at the foot of the window.
func (w *prefsWindow) toast(text string) {
	w.message = text
	t := adw.NewToast(text)
	t.SetUseMarkup(false)
	w.dlg.AddToast(t)
}

// editConfig applies edit to config.toml's text and writes the result.
func (a *App) editConfig(edit func(string) string) error {
	path := a.cfg.configFile
	if path == "" {
		return errors.New("no config file")
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read settings: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(edit(string(data))), 0o644); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return nil
}
