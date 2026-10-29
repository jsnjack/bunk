package main

// Agent hooks: the section of the preferences that sets them up, and the
// reminder about an agent that runs without them (hooks_hint).
//
// With hooks, an agent reports its own state and the session ID that
// restore resumes; without them bunk infers its state from output
// (docs/agent-hooks.md). The section shows each agent's setup as
// notify.CheckHooks finds it, installs the hooks (notify.InstallHooks:
// files of their own for Codex and Copilot CLI, merged into settings.json
// with a backup for Claude Code and Gemini CLI) and copies the snippets.
// It never writes a file without a click.
//
// The reminder is a banner over the panes: at start for agents whose bunk
// hooks are outdated, and when a tab first shows an agent whose hooks are
// missing or outdated. Update installs them for every agent it names; Not
// now leaves them until the next start. hooks_hint = false turns it off.

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
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
		if h.State != notify.HooksInstalled {
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
	text, _ := w.a.installHooks(agent)
	w.hooks.note.SetText(text)
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
	if !h.Merges {
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

// installHooks sets up agent's hooks and says what it did, or why not.
func (a *App) installHooks(agent string) (string, bool) {
	d := a.hookDirs()
	done, err := notify.InstallHooks(d, agent)
	if err != nil {
		slog.Debug("install hooks", "agent", agent, "err", err)
		return "Not installed: " + err.Error(), false
	}
	text := "Installed " + d.Short(done.Path) + "."
	if done.Backup != "" {
		text += " The old file is in " + d.Short(done.Backup) + "."
	}
	if h, _ := notify.CheckHook(d, agent); h.After != "" {
		text += " " + h.After
	}
	return text, true
}

// hookOffer is the banner that offers to set up agents' hooks.
type hookOffer struct {
	banner *banner
	agents []notify.Hooks // the agents it names, in the order they came
	seen   map[string]bool
	// result is what the last Update said, for --report.
	result string
}

type hookOfferReport struct {
	Banner bool     `json:"banner"`
	Text   string   `json:"text"`
	Agents []string `json:"agents"`
	Result string   `json:"result"`
}

func (a *App) hookOfferReport() hookOfferReport {
	o := &a.hookOffer
	r := hookOfferReport{Banner: o.banner.shown(), Agents: []string{}, Result: o.result}
	if r.Banner {
		r.Text = o.banner.label.Text()
	}
	for _, h := range o.agents {
		r.Agents = append(r.Agents, h.Agent)
	}
	return r
}

// offerOutdatedHooks offers, at start, to update the agents whose bunk
// hooks are older than the current snippets: an update of bunk's added
// hooks they lack.
func (a *App) offerOutdatedHooks() {
	if !a.settings.hooksHint {
		return
	}
	for _, h := range notify.CheckHooks(a.hookDirs()) {
		if h.State == notify.HooksOutdated {
			a.offerHooks(h)
		}
	}
}

// hooksHint offers, once per agent and window process, to set up the hooks
// of an agent a tab shows whose hooks are missing or outdated, so its state
// is guessed and restore cannot resume its conversation. A remote agent's
// hooks are on its host, and an agent that reports through hooks has
// them, unless they are outdated. st is a pane's status from its session.
func (a *App) hooksHint(st session.Status) {
	if !a.settings.hooksHint || st.Agent == "" || st.Host != "" || st.SSHHost != "" || a.hookOffer.seen[st.Agent] {
		return
	}
	h, ok := notify.CheckHook(a.hookDirs(), st.Agent)
	if !ok || h.State == notify.HooksInstalled || st.Reported && h.State == notify.HooksMissing {
		return
	}
	a.offerHooks(h)
}

// offerHooks adds h's agent to the banner and shows it.
func (a *App) offerHooks(h notify.Hooks) {
	o := &a.hookOffer
	if o.seen == nil {
		o.seen = map[string]bool{}
	}
	if o.seen[h.Agent] {
		return
	}
	o.seen[h.Agent] = true
	o.agents = append(o.agents, h)
	d := a.hookDirs()
	var names, files []string
	outdated := true
	for _, h := range o.agents {
		state := "outdated"
		if h.State == notify.HooksMissing {
			state, outdated = "not set up", false
		}
		names = append(names, fmt.Sprintf("%s (%s)", h.Agent, state))
		file := d.Short(h.Path)
		if h.Merges {
			file += ", keeping a copy"
		}
		files = append(files, file)
	}
	verb := "Set up"
	if outdated {
		verb = "Update"
	}
	o.banner.accept.SetLabel(verb)
	o.banner.show(fmt.Sprintf("%s bunk's hooks for %s? Without current hooks, states are guessed and restored agents can't resume their conversations. This changes %s.",
		verb, strings.Join(names, ", "), strings.Join(files, "; ")))
}

// acceptHooks installs the hooks of every agent the banner names.
func (a *App) acceptHooks() {
	o := &a.hookOffer
	o.banner.hide()
	var notes []string
	failed := false
	for _, h := range o.agents {
		text, ok := a.installHooks(h.Agent)
		notes = append(notes, h.Agent+": "+text)
		failed = failed || !ok
	}
	o.agents = nil
	o.result = strings.Join(notes, " ")
	if failed {
		a.toastAction("Some hooks were not installed", "Details", func() { a.showPreferencesAt("hooks") })
	} else {
		a.toast("Hooks installed")
	}
	if a.prefsWin != nil {
		a.prefsWin.refreshHooks()
	}
	if p := a.activePane(); p != nil {
		p.widget.GrabFocus()
	}
}

// declineHooks hides the banner until the next start.
func (a *App) declineHooks() {
	a.hookOffer.banner.hide()
	a.hookOffer.agents = nil
	if p := a.activePane(); p != nil {
		p.widget.GrabFocus()
	}
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
