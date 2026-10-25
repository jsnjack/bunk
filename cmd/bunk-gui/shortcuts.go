package main

// The shortcuts window (action "shortcuts"): one row per action with its
// keys, a switch, and click-to-rebind. Changes go straight into the [keys]
// table of config.toml (keysfile.go); the file watcher then reloads them
// like any other edit.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

type shortcutsWindow struct {
	a         *App
	dlg       *adw.Dialog
	scroll    *gtk.ScrolledWindow
	list      *gtk.Box
	message   *gtk.Label
	rows      map[string]*shortcutRow
	capturing string // action waiting for a key; "" when none
}

type shortcutRow struct {
	box     *gtk.Box
	keys    *gtk.Button
	sw      *gtk.Switch
	warn    *gtk.Label
	syncing bool
}

const collideHint = "May collide with terminal apps: turn it off to send the key to the terminal"

func (a *App) showShortcuts() {
	if a.shortcutsWin != nil {
		return
	}
	w := &shortcutsWindow{a: a, rows: map[string]*shortcutRow{}}
	w.dlg = adw.NewDialog()
	w.dlg.SetTitle("Keyboard shortcuts")
	w.dlg.SetContentWidth(680)
	w.dlg.SetContentHeight(760)
	view := adw.NewToolbarView()
	view.AddTopBar(adw.NewHeaderBar())
	content := gtk.NewBox(gtk.OrientationVertical, 6)
	content.SetMarginStart(12)
	content.SetMarginEnd(12)
	content.SetMarginBottom(12)
	w.message = label("Click a shortcut to change it: Esc cancels, Backspace clears. ⚠ may collide with terminal apps.", "statelabel")
	w.message.SetWrap(true)
	w.message.SetXAlign(0)
	content.Append(w.message)
	w.list = gtk.NewBox(gtk.OrientationVertical, 0)
	for _, act := range keyActions {
		w.list.Append(w.newRow(act))
	}
	w.scroll = gtk.NewScrolledWindow()
	w.scroll.SetVExpand(true)
	w.scroll.SetChild(w.list)
	content.Append(w.scroll)
	view.SetContent(content)
	w.dlg.SetChild(view)

	keys := gtk.NewEventControllerKey()
	keys.SetPropagationPhase(gtk.PhaseCapture)
	keys.ConnectKeyPressed(func(keyval, _ uint, state gdk.ModifierType) bool { return w.onKey(keyval, state) })
	w.dlg.AddController(keys)
	w.dlg.ConnectClosed(func() {
		a.shortcutsWin = nil
		if p := a.activePane(); p != nil {
			p.widget.GrabFocus()
		}
	})
	a.shortcutsWin = w
	w.refresh()
	w.dlg.Present(a.win)
}

func (w *shortcutsWindow) newRow(act keyAction) gtk.Widgetter {
	r := &shortcutRow{}
	r.box = gtk.NewBox(gtk.OrientationHorizontal, 10)
	r.box.AddCSSClass("shortcutrow")
	text := gtk.NewBox(gtk.OrientationVertical, 1)
	text.SetHExpand(true)
	t := label(act.desc, "shortcutdesc")
	t.SetXAlign(0)
	n := label(act.name, "repo")
	n.SetXAlign(0)
	text.Append(t)
	text.Append(n)
	r.box.Append(text)
	r.warn = label("⚠", "collide")
	r.warn.SetTooltipText(collideHint)
	r.box.Append(r.warn)
	r.keys = gtk.NewButton()
	r.keys.AddCSSClass("shortcutkeys")
	r.keys.SetVAlign(gtk.AlignCenter)
	name := act.name
	r.keys.ConnectClicked(func() { w.startCapture(name) })
	r.box.Append(r.keys)
	r.sw = gtk.NewSwitch()
	r.sw.SetVAlign(gtk.AlignCenter)
	r.sw.ConnectStateSet(func(on bool) bool {
		if !r.syncing {
			w.setEnabled(name, on)
		}
		return false
	})
	r.box.Append(r.sw)
	w.rows[act.name] = r
	return r.box
}

// refresh shows the bindings in force.
func (w *shortcutsWindow) refresh() {
	km := w.a.keys()
	for name, r := range w.rows {
		cs := km.bind[name]
		r.syncing = true
		r.sw.SetActive(len(cs) > 0)
		r.syncing = false
		switch {
		case w.capturing == name:
			r.keys.SetLabel("Press a key…")
		case len(cs) == 0:
			r.keys.SetLabel("Disabled")
		default:
			r.keys.SetLabel(km.labels(name))
		}
		collide := false
		for _, c := range cs {
			collide = collide || c.collides()
		}
		r.warn.SetOpacity(map[bool]float64{true: 1, false: 0}[collide])
	}
}

func (w *shortcutsWindow) say(format string, args ...any) {
	w.message.SetText(fmt.Sprintf(format, args...))
}

func (w *shortcutsWindow) startCapture(action string) {
	w.capturing = action
	act, _ := findAction(action)
	w.say("Press the new keys for %s: Esc cancels, Backspace clears.", act.desc)
	w.refresh()
}

// onKey takes the key being captured; other keys work as usual.
func (w *shortcutsWindow) onKey(keyval uint, state gdk.ModifierType) bool {
	if w.capturing == "" {
		return false
	}
	switch keyval {
	case gdk.KEY_Shift_L, gdk.KEY_Shift_R, gdk.KEY_Control_L, gdk.KEY_Control_R, gdk.KEY_Alt_L, gdk.KEY_Alt_R,
		gdk.KEY_Super_L, gdk.KEY_Super_R, gdk.KEY_Meta_L, gdk.KEY_Meta_R, gdk.KEY_ISO_Level3_Shift, gdk.KEY_Caps_Lock:
		return true // wait for the key itself
	}
	action := w.capturing
	w.capturing = ""
	mods := state & comboMods
	act, _ := findAction(action)
	switch {
	case keyval == gdk.KEY_Escape && mods == 0:
		w.say("Unchanged: %s.", act.desc)
	case keyval == gdk.KEY_BackSpace && mods == 0:
		w.apply(action, nil)
	default:
		c := keyCombo{gdk.KeyvalToLower(keyval), mods}
		if other, ok := w.a.keys().action[c]; ok && other != action {
			o, _ := findAction(other)
			w.say("%s is already used by %s.", c.Label(), o.desc)
		} else {
			w.apply(action, []keyCombo{c})
		}
	}
	w.refresh()
	return true
}

// setEnabled turns an action off (its keys go to the terminal) or back on
// with the keys it had before it was turned off.
func (w *shortcutsWindow) setEnabled(action string, on bool) {
	if !on {
		w.apply(action, nil)
		w.refresh()
		return
	}
	act, _ := findAction(action)
	prev := w.a.settings.was[action]
	if len(prev) == 0 {
		prev = act.defs
	}
	var combos []keyCombo
	var taken []string
	for _, s := range prev {
		c, err := parseCombo(s)
		if err != nil {
			continue
		}
		if other, ok := w.a.keys().action[c]; ok && other != action {
			taken = append(taken, c.Label())
			continue
		}
		combos = append(combos, c)
	}
	if len(combos) == 0 {
		w.say("Cannot turn %s on: %s already used.", act.desc, strings.Join(taken, ", "))
		w.refresh()
		return
	}
	w.apply(action, combos)
	if len(taken) > 0 {
		w.say("%s is on without %s (already used).", act.desc, strings.Join(taken, ", "))
	}
	w.refresh()
}

func (w *shortcutsWindow) apply(action string, combos []keyCombo) {
	act, _ := findAction(action)
	if err := w.a.setActionKeys(action, combos); err != nil {
		w.say("Could not save %s: %v", act.desc, err)
		return
	}
	if len(combos) == 0 {
		w.say("%s is off: its keys go to the terminal.", act.desc)
	} else {
		w.say("%s: %s.", act.desc, w.a.keys().labels(action))
	}
}

// setActionKeys binds action to combos (none: off), saves the [keys] line
// in config.toml and refreshes the window's labels. Turning an action off
// keeps its keys in a "# was" comment for turning it back on.
func (a *App) setActionKeys(action string, combos []keyCombo) error {
	path := a.cfg.configFile
	if path == "" {
		return errors.New("no config file")
	}
	cur := a.keys().bind[action]
	edit := keysEdit{action: action, value: tomlValue(combos)}
	if len(combos) == 0 && len(cur) > 0 {
		edit.was = tomlValue(cur)
		var was []string
		for _, c := range cur {
			was = append(was, c.String())
		}
		a.settings.was[action] = was
	}
	if err := a.editConfig(func(content string) string { return patchKeys(content, []keysEdit{edit}) }); err != nil {
		return err
	}
	a.settings.keymap = a.keys().withBinding(action, combos)
	a.buildChrome() // menu and tooltips show keys
	return nil
}

// scrollTo brings an action's row into view (tests aim the pointer at it).
func (w *shortcutsWindow) scrollTo(action string) error {
	r, ok := w.rows[action]
	if !ok {
		return errors.New("no shortcut row " + action)
	}
	b, ok := r.box.ComputeBounds(w.list)
	if !ok {
		return errors.New("shortcut row not laid out")
	}
	w.scroll.VAdjustment().SetValue(float64(b.Y()) - 60)
	return nil
}
