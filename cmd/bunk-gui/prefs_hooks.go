package main

// Agent hooks: the section of the preferences that sets them up, and the
// reminder about an agent that runs without them (hooks_hint).
//
// With hooks, an agent reports its own state; without them bunk infers it
// from output (docs/agent-hooks.md). The section shows each agent's setup
// as notify.CheckHooks finds it, installs the snippets that are files of
// their own (Codex, Copilot CLI) and copies the others, which the user
// merges into a settings file that holds other settings too. It never
// writes a file without a click.
//
// The reminder is a toast, once per agent and window process, when a tab
// shows an agent whose hooks are missing or outdated. Its button opens
// this section.

import (
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/notify"
	"bunk/internal/session"
)

// hooksDocsURL is the setup guide the section and the docs link to.
const hooksDocsURL = "https://github.com/jsnjack/bunk/blob/master/docs/agent-hooks.md#set-up"

// hookDirs locates the agents' hook configuration: the user's, or the
// --agent-home directory of a test run.
func (a *App) hookDirs() notify.Dirs {
	if a.cfg.agentHome != "" {
		return notify.Dirs{Home: a.cfg.agentHome}
	}
	return notify.UserDirs()
}

// hooksPrefs are the preferences window's Agent hooks controls.
type hooksPrefs struct {
	rows  *gtk.Box // one row per agent, rebuilt by refreshHooks
	note  *gtk.Label
	hint  *gtk.Switch
	shown []notify.Hooks
}

// hookLooks are the states as the section shows them.
var hookLooks = map[notify.HookState]string{
	notify.HooksInstalled: "✓ Installed", notify.HooksOutdated: "⚠ Outdated", notify.HooksMissing: "✗ Not set up",
}

// addHooks adds the Agent hooks section to the preferences.
func (w *prefsWindow) addHooks(box *gtk.Box) {
	hp := &hooksPrefs{rows: gtk.NewBox(gtk.OrientationVertical, 10), note: label("", "statelabel"), hint: gtk.NewSwitch()}
	h := heading("Agent hooks")
	box.Append(h)
	w.controls["hooks"] = h
	intro := label("With hooks, an agent tells bunk-gui when it works, needs you and is done. "+
		"Without them, its state is guessed from its output.", "statelabel")
	intro.SetWrap(true)
	intro.SetXAlign(0)
	box.Append(intro)
	box.Append(hp.rows)
	hp.note.SetWrap(true)
	hp.note.SetXAlign(0)
	box.Append(hp.note)
	guide := gtk.NewButtonWithLabel("Setup guide")
	guide.SetHAlign(gtk.AlignStart)
	guide.ConnectClicked(func() { openURL(w.a, hooksDocsURL) })
	box.Append(guide)
	w.controls["hooks-guide"] = guide
	hp.hint.SetActive(w.a.fileSettings.hooksHint)
	hp.hint.ConnectStateSet(func(on bool) bool {
		if !w.syncing {
			w.saveTop(keysEdit{action: "hooks_hint", value: strconv.FormatBool(on)})
		}
		return false
	})
	box.Append(prefRow("Remind me when an agent runs without hooks", hp.hint))
	w.controls["hooks-hint"] = hp.hint
	w.hooks = hp
	w.refreshHooks()
}

// refreshHooks rebuilds the agent rows from the files as they are now.
func (w *prefsWindow) refreshHooks() {
	hp := w.hooks
	for c := hp.rows.FirstChild(); c != nil; c = hp.rows.FirstChild() {
		hp.rows.Remove(c)
	}
	d := w.a.hookDirs()
	hp.shown = notify.CheckHooks(d)
	for _, h := range hp.shown {
		row := gtk.NewBox(gtk.OrientationHorizontal, 12)
		lines := gtk.NewBox(gtk.OrientationVertical, 2)
		top := gtk.NewBox(gtk.OrientationHorizontal, 8)
		top.Append(label(h.Agent, "agent"))
		state := hookLooks[h.State]
		if !h.CLI {
			state += " · not on PATH"
		}
		top.Append(label(state, "statelabel"))
		lines.Append(top)
		path := label(d.Short(h.Path), "repo")
		path.SetXAlign(0)
		lines.Append(path)
		if h.Problem != "" {
			p := label(h.Problem, "notice")
			p.SetWrap(true)
			p.SetXAlign(0)
			lines.Append(p)
		}
		lines.SetHExpand(true)
		row.Append(lines)
		agent := h.Agent
		if h.Installable && h.State != notify.HooksInstalled {
			b := gtk.NewButtonWithLabel("Install")
			b.SetVAlign(gtk.AlignCenter)
			b.ConnectClicked(func() { w.installHooks(agent) })
			row.Append(b)
			w.controls["hooks-"+agent+"-install"] = b
		} else {
			delete(w.controls, "hooks-"+agent+"-install")
		}
		c := gtk.NewButtonWithLabel("Copy snippet")
		c.SetVAlign(gtk.AlignCenter)
		c.ConnectClicked(func() { w.copyHooks(agent) })
		row.Append(c)
		w.controls["hooks-"+agent+"-copy"] = c
		hp.rows.Append(row)
	}
}

// installHooks writes an agent's hooks file and says what comes next.
func (w *prefsWindow) installHooks(agent string) {
	d := w.a.hookDirs()
	path, err := notify.InstallHooks(d, agent)
	if err != nil {
		slog.Debug("install hooks", "agent", agent, "err", err)
		w.hooks.note.SetText("Not installed: " + err.Error())
	} else {
		text := "Installed " + d.Short(path) + "."
		if h, _ := notify.CheckHook(d, agent); h.After != "" {
			text += " " + h.After
		}
		w.hooks.note.SetText(text)
	}
	w.refreshHooks()
}

// copyHooks puts an agent's snippet on the clipboard and says where it
// goes.
func (w *prefsWindow) copyHooks(agent string) {
	snippet, ok := notify.HookSnippet(agent)
	if !ok {
		return
	}
	clipboard(false).SetText(snippet)
	d := w.a.hookDirs()
	h, _ := notify.CheckHook(d, agent)
	where := fmt.Sprintf("merge its \"hooks\" key into %s, next to the hooks and settings there.", d.Short(h.Path))
	if h.Installable {
		where = "save it as " + d.Short(h.Path) + "."
		if h.After != "" {
			where += " " + h.After
		}
	}
	w.hooks.note.SetText("Copied the " + agent + " snippet: " + where)
}

// hooksReport is one agent's row in --report.
type hooksReport struct {
	Agent       string `json:"agent"`
	State       string `json:"state"`
	Path        string `json:"path"`
	Installable bool   `json:"installable"`
}

func (w *prefsWindow) hooksShown() []hooksReport {
	out := []hooksReport{}
	for _, h := range w.hooks.shown {
		out = append(out, hooksReport{Agent: h.Agent, State: h.State.String(), Path: h.Path, Installable: h.Installable})
	}
	return out
}

// hooksHint reminds the user, once per agent and window process, that an
// agent in a tab runs without bunk's hooks or with outdated ones, so its
// state is guessed. A remote agent's hooks are on its host, and an agent
// that reported through hooks evidently has them. st is a pane's status
// from its session.
func (a *App) hooksHint(st session.Status) {
	if !a.settings.hooksHint || st.Agent == "" || st.Host != "" || st.SSHHost != "" || a.hinted[st.Agent] {
		return
	}
	if a.hinted == nil {
		a.hinted = map[string]bool{}
	}
	a.hinted[st.Agent] = true
	if st.Reported {
		return
	}
	h, ok := notify.CheckHook(a.hookDirs(), st.Agent)
	if !ok || h.State == notify.HooksInstalled {
		return
	}
	text := st.Agent + " runs without bunk's hooks, so its state is guessed from its output"
	if h.State == notify.HooksOutdated {
		text = st.Agent + "'s bunk hooks are outdated, so some of its states are guessed"
	}
	a.toastAction(text, "Set up", func() { a.showPreferencesAt("hooks") })
}

// toastAction shows a toast with a button that runs act.
func (a *App) toastAction(text, button string, act func()) {
	if a.toasts == nil {
		a.toast(text)
		return
	}
	t := adw.NewToast(text)
	t.SetTimeout(10)
	t.SetButtonLabel(button)
	t.ConnectButtonClicked(act)
	a.toasts.AddToast(t)
	a.lastToast, a.toastButton, a.toastAct = text, button, act
	a.toastLog = append(a.toastLog, text)
}

// showPreferencesAt opens the preferences and scrolls to the control
// named name once the window is laid out.
func (a *App) showPreferencesAt(name string) {
	a.showPreferences()
	w := a.prefsWin
	if w == nil {
		return
	}
	deadline := time.Now().Add(2 * time.Second)
	coreglib.TimeoutAdd(16, func() bool {
		if a.prefsWin != w {
			return false
		}
		err := w.scrollTo(name)
		if err != nil && time.Now().Before(deadline) {
			return true
		}
		if err != nil {
			slog.Debug("preferences: no scroll", "to", name, "err", err)
		}
		return false
	})
}
