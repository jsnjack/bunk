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
// hooks or bunk-present skill are outdated, and when a tab first shows an
// agent whose hooks or skill are missing or outdated. It names what each
// agent lacks; Set up (or Update) installs all of it, Not now leaves it
// until the next start. hooks_hint = false turns it off.

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

// hookOffer is the banner that offers to set up agents' hooks and skills.
type hookOffer struct {
	banner *banner
	agents []agentSetup // the agents it names, in the order they came
	seen   map[string]bool
	// result is what the last Update said, for --report.
	result string
}

// agentSetup is what the banner offers for one agent: its hooks, its
// bunk-present skill, or both.
type agentSetup struct {
	hooks notify.Hooks
	skill notify.SkillState // Agent is "" for an agent without a skill
	// fixHooks and fixSkill: the banner installs them.
	fixHooks, fixSkill bool
}

// setupOf is what agent lacks of bunk's setup: hooks that are not
// installed and a skill that is not current.
func (a *App) setupOf(agent string) (agentSetup, bool) {
	d := a.hookDirs()
	h, ok := notify.CheckHook(d, agent)
	if !ok {
		return agentSetup{}, false
	}
	s := agentSetup{hooks: h, fixHooks: h.State != notify.HooksInstalled}
	if sk, ok := notify.CheckSkill(d, agent); ok {
		s.skill, s.fixSkill = sk, sk.State != notify.HooksInstalled
	}
	return s, true
}

type hookOfferReport struct {
	Banner bool     `json:"banner"`
	Text   string   `json:"text"`
	Agents []string `json:"agents"`
	Skills []string `json:"skills"` // the agents whose skill it installs
	Result string   `json:"result"`
}

func (a *App) hookOfferReport() hookOfferReport {
	o := &a.hookOffer
	r := hookOfferReport{Banner: o.banner.shown(), Agents: []string{}, Skills: []string{}, Result: o.result}
	if r.Banner {
		r.Text = o.banner.label.Text()
	}
	for _, s := range o.agents {
		r.Agents = append(r.Agents, s.hooks.Agent)
		if s.fixSkill {
			r.Skills = append(r.Skills, s.hooks.Agent)
		}
	}
	return r
}

// offerOutdatedHooks offers, at start, to update the agents whose bunk
// hooks or skill are older than the current ones, an update of bunk's,
// together with the rest of their setup.
func (a *App) offerOutdatedHooks() {
	if !a.settings.hooksHint {
		return
	}
	for _, h := range notify.CheckHooks(a.hookDirs()) {
		s, ok := a.setupOf(h.Agent)
		if ok && (s.hooks.State == notify.HooksOutdated || s.fixSkill && s.skill.State == notify.HooksOutdated) {
			a.offerSetup(s)
		}
	}
}

// hooksHint offers, once per agent and window process, to set up the hooks
// and skill of an agent a tab shows when either is missing or outdated:
// without hooks its state is guessed and restore cannot resume its
// conversation, and without the skill it does not know the document
// panel. A remote agent's hooks and skills are on its host, and an agent
// that reports through hooks has them, unless they are outdated. st is a
// pane's status from its session.
func (a *App) hooksHint(st session.Status) {
	if !a.settings.hooksHint || st.Agent == "" || st.Host != "" || st.SSHHost != "" || a.hookOffer.seen[st.Agent] {
		return
	}
	s, ok := a.setupOf(st.Agent)
	if !ok {
		return
	}
	if st.Reported && s.hooks.State == notify.HooksMissing {
		s.fixHooks = false
	}
	if s.fixHooks || s.fixSkill {
		a.offerSetup(s)
	}
}

// offerSetup adds s's agent to the banner and shows it.
func (a *App) offerSetup(s agentSetup) {
	o := &a.hookOffer
	if o.seen == nil {
		o.seen = map[string]bool{}
	}
	agent := s.hooks.Agent
	if o.seen[agent] {
		return
	}
	o.seen[agent] = true
	o.agents = append(o.agents, s)
	d := a.hookDirs()
	var names, files []string
	outdated, hooks, skills := true, false, false
	state := func(st notify.HookState, missing string) string {
		if st == notify.HooksMissing {
			outdated = false
			return missing
		}
		return "outdated"
	}
	for _, s := range o.agents {
		var lacks []string
		if s.fixHooks {
			hooks = true
			lacks = append(lacks, "hooks "+state(s.hooks.State, "not set up"))
			file := d.Short(s.hooks.Path)
			if s.hooks.Merges {
				file += ", keeping a copy"
			}
			files = append(files, file)
		}
		if s.fixSkill {
			skills = true
			lacks = append(lacks, "skill "+state(s.skill.State, "missing"))
			files = append(files, d.Short(s.skill.Path))
		}
		names = append(names, fmt.Sprintf("%s (%s)", s.hooks.Agent, strings.Join(lacks, ", ")))
	}
	verb := "Set up"
	if outdated {
		verb = "Update"
	}
	var why []string
	if hooks {
		why = append(why, "Without current hooks, states are guessed and restored agents can't resume their conversations.")
	}
	if skills {
		why = append(why, "The bunk-present skill tells agents how to show you documents in the side panel.")
	}
	o.banner.accept.SetLabel(verb)
	o.banner.show(fmt.Sprintf("%s bunk for %s? %s This changes %s.",
		verb, strings.Join(names, ", "), strings.Join(why, " "), strings.Join(files, "; ")))
}

// acceptHooks installs what the banner offers for every agent it names.
func (a *App) acceptHooks() {
	o := &a.hookOffer
	o.banner.hide()
	var notes []string
	failed := false
	for _, s := range o.agents {
		agent := s.hooks.Agent
		if s.fixHooks {
			text, ok := a.installHooks(agent)
			notes = append(notes, agent+": "+text)
			failed = failed || !ok
		}
		if s.fixSkill {
			path, err := notify.InstallSkill(a.hookDirs(), agent)
			if err != nil {
				notes = append(notes, agent+" skill: Not installed: "+err.Error())
				failed = true
			} else {
				notes = append(notes, agent+" skill: Installed "+a.hookDirs().Short(path)+".")
			}
		}
	}
	o.agents = nil
	o.result = strings.Join(notes, " ")
	if failed {
		a.toastAction("Some of the setup was not installed", "Details", func() { a.showPreferencesAt("hooks") })
	} else {
		a.toast("Installed. New agent sessions use it.")
	}
	if a.prefsWin != nil {
		a.prefsWin.refreshHooks()
		a.prefsWin.refreshSkills()
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
	a.lastAdwToast = t
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
