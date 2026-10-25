package main

// The preferences window (action "preferences"): layout, the rail, the
// font and the default text zoom, and the [[agents]] list. Every change is
// written into config.toml at once by the line editor (keysfile.go), so
// the user's comments and order stay; the reload applies it.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/session"
)

type prefsWindow struct {
	a        *App
	dlg      *adw.Dialog
	controls map[string]gtk.Widgetter // by name, for tests
	agents   *gtk.Box
	rows     []*agentRow
	message  *gtk.Label
	syncing  bool

	tabsRail, tabsBar *gtk.CheckButton
	railOpen          *gtk.Switch
	font              *gtk.Entry
	fontSize, zoom    *gtk.SpinButton
}

type agentRow struct {
	name, processes, scripts *gtk.Entry
	saved                    bool // the entry exists in the file
}

func (a *App) showPreferences() {
	if a.prefsWin != nil {
		return
	}
	w := &prefsWindow{a: a, controls: map[string]gtk.Widgetter{}}
	st := a.fileSettings
	w.dlg = adw.NewDialog()
	w.dlg.SetTitle("Preferences")
	w.dlg.SetContentWidth(640)
	w.dlg.SetContentHeight(720)
	view := adw.NewToolbarView()
	view.AddTopBar(adw.NewHeaderBar())
	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.SetMarginStart(16)
	box.SetMarginEnd(16)
	box.SetMarginBottom(16)
	w.message = label("Changes are saved to "+a.cfg.configFile+" as you make them.", "statelabel")
	w.message.SetWrap(true)
	w.message.SetXAlign(0)
	box.Append(w.message)

	w.syncing = true
	box.Append(heading("Tabs"))
	w.tabsRail = gtk.NewCheckButtonWithLabel("Tabs on the side")
	w.tabsBar = gtk.NewCheckButtonWithLabel("Tabs on top")
	w.tabsBar.SetGroup(w.tabsRail)
	w.tabsRail.SetActive(st.Tabs == "rail")
	w.tabsBar.SetActive(st.Tabs == "bar")
	for _, c := range []*gtk.CheckButton{w.tabsRail, w.tabsBar} {
		c.ConnectToggled(func() {
			if !w.syncing && c.Active() {
				w.saveTop(keysEdit{action: "tabs", value: tomlString(map[bool]string{true: "bar", false: "rail"}[w.tabsBar.Active()])})
			}
		})
		box.Append(c)
	}
	w.railOpen = gtk.NewSwitch()
	w.railOpen.SetActive(st.RailOpen)
	w.railOpen.ConnectStateSet(func(on bool) bool {
		if !w.syncing {
			w.saveTop(keysEdit{action: "rail_open", value: map[bool]string{true: "true", false: "false"}[on]})
		}
		return false
	})
	box.Append(prefRow("Rail expanded at start", w.railOpen))

	box.Append(heading("Text"))
	w.font = gtk.NewEntry()
	w.font.SetText(st.Font)
	w.font.SetHExpand(true)
	w.font.ConnectActivate(func() {
		if f := strings.TrimSpace(w.font.Text()); f != "" {
			w.saveTop(keysEdit{action: "font", value: tomlString(f)})
		}
	})
	box.Append(prefRow("Font family", w.font))
	w.fontSize = gtk.NewSpinButtonWithRange(4, 96, 0.5)
	w.fontSize.SetDigits(1)
	w.fontSize.SetValue(st.FontSize)
	w.fontSize.ConnectValueChanged(func() {
		if !w.syncing {
			w.saveTop(keysEdit{action: "font_size", value: tomlFloat(w.fontSize.Value())})
		}
	})
	box.Append(prefRow("Font size (points)", w.fontSize))
	w.zoom = gtk.NewSpinButtonWithRange(50, 200, 10)
	w.zoom.SetValue(float64(st.TextZoom))
	w.zoom.ConnectValueChanged(func() {
		if !w.syncing {
			w.saveTop(keysEdit{action: "text_zoom", value: tomlFloat(w.zoom.Value())})
		}
	})
	box.Append(prefRow("Default text zoom (%)", w.zoom))

	box.Append(heading("Agents"))
	hint := label("Agent CLIs to recognise besides the built-in ones: a name, and process names or script paths (comma separated).", "statelabel")
	hint.SetWrap(true)
	hint.SetXAlign(0)
	box.Append(hint)
	w.agents = gtk.NewBox(gtk.OrientationVertical, 6)
	box.Append(w.agents)
	add := gtk.NewButtonWithLabel("Add agent")
	add.SetHAlign(gtk.AlignStart)
	add.ConnectClicked(func() { w.addRow(session.AgentSpec{}, false) })
	box.Append(add)
	w.controls["agent-add"] = add
	w.controls["tabs-rail"], w.controls["tabs-bar"] = w.tabsRail, w.tabsBar
	w.controls["rail-open"], w.controls["font"] = w.railOpen, w.font
	w.controls["font-size"], w.controls["text-zoom"] = w.fontSize, w.zoom
	w.loadAgents()
	w.syncing = false

	scroll := gtk.NewScrolledWindow()
	scroll.SetVExpand(true)
	scroll.SetChild(box)
	view.SetContent(scroll)
	w.dlg.SetChild(view)
	w.dlg.ConnectClosed(func() {
		a.prefsWin = nil
		if p := a.activePane(); p != nil {
			p.widget.GrabFocus()
		}
	})
	a.prefsWin = w
	w.dlg.Present(a.win)
}

func heading(text string) *gtk.Label {
	l := label(text, "agent")
	l.SetXAlign(0)
	l.SetMarginTop(8)
	return l
}

func prefRow(title string, control gtk.Widgetter) *gtk.Box {
	row := gtk.NewBox(gtk.OrientationHorizontal, 12)
	l := label(title, "shortcutdesc")
	l.SetXAlign(0)
	l.SetHExpand(true)
	row.Append(l)
	row.Append(control)
	return row
}

// fileAgents reads the [[agents]] entries as written, invalid ones too, so
// rows match the file's entries one to one.
func fileAgents(path string) []session.AgentSpec {
	var f struct {
		Agents []session.AgentSpec `toml:"agents"`
	}
	if data, err := os.ReadFile(path); err == nil {
		_, _ = toml.Decode(string(data), &f)
	}
	return f.Agents
}

func (w *prefsWindow) loadAgents() {
	for c := w.agents.FirstChild(); c != nil; c = w.agents.FirstChild() {
		w.agents.Remove(c)
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

func (w *prefsWindow) addRow(ag session.AgentSpec, saved bool) {
	r := &agentRow{saved: saved}
	i := len(w.rows)
	w.rows = append(w.rows, r)
	box := gtk.NewBox(gtk.OrientationHorizontal, 6)
	entry := func(text, placeholder, key string) *gtk.Entry {
		e := gtk.NewEntry()
		e.SetText(text)
		e.SetPlaceholderText(placeholder)
		e.SetHExpand(true)
		e.ConnectActivate(func() { w.saveAgent(r) })
		w.controls["agent-"+itoa(i)+"-"+key] = e
		box.Append(e)
		return e
	}
	r.name = entry(ag.Name, "name", "name")
	r.processes = entry(strings.Join(ag.Processes, ", "), "processes", "processes")
	r.scripts = entry(strings.Join(ag.Scripts, ", "), "scripts", "scripts")
	rm := iconButton("user-trash-symbolic", 16, "Remove", "chrome")
	rm.ConnectClicked(func() { w.removeAgent(r) })
	w.controls["agent-"+itoa(i)+"-remove"] = rm
	box.Append(rm)
	w.agents.Append(box)
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
		w.message.SetText("An agent needs a name and processes or scripts.")
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
	w.save(func(content string) string {
		if r.saved {
			return patchArrayEntry(content, "agents", i, edits)
		}
		return appendArrayEntry(content, "agents", edits)
	})
	r.saved = true
	w.message.SetText("Saved agent " + name + ".")
}

func (w *prefsWindow) removeAgent(r *agentRow) {
	if r.saved {
		i := w.index(r)
		w.save(func(content string) string { return removeArrayEntry(content, "agents", i) })
	}
	w.loadAgents()
	if !r.saved {
		return
	}
	w.message.SetText("Removed the agent.")
}

func (w *prefsWindow) saveTop(e keysEdit) {
	w.save(func(content string) string { return patchTable(content, "", []keysEdit{e}) })
	w.message.SetText("Saved " + e.action + ".")
}

// save rewrites config.toml through edit and applies it at once (the file
// watcher would too, a moment later).
func (w *prefsWindow) save(edit func(string) string) {
	if err := w.a.editConfig(edit); err != nil {
		w.message.SetText("Could not save: " + err.Error())
		return
	}
	w.a.reloadSettings()
}

// editConfig applies edit to config.toml's text and writes the result.
func (a *App) editConfig(edit func(string) string) error {
	path := a.cfg.configFile
	if path == "" {
		return errors.New("no config file")
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(edit(string(data))), 0o644)
}
