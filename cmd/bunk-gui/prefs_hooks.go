package main

// Agent setup: the preferences' Agents page, which sets up each agent's
// hooks and bunk-present skill, and the reminder about an agent that runs
// without them (hooks_hint).
//
// With hooks, an agent reports its own state and the session ID that
// restore resumes; without them bunk infers its state from output
// (docs/agent-hooks.md). The page shows each agent's setup as
// notify.CheckHooks and notify.CheckSkills find it, installs what is
// missing or outdated (notify.InstallHooks: files of their own for Codex
// and Copilot CLI, merged into settings.json with a backup for Claude
// Code and Gemini CLI; notify.InstallSkill) and copies the snippets. It
// never writes a file without a click.
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

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
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

// setupPrefs is the Agents page's Setup section: a row per agent with its
// hooks and, for an agent that loads skills, its bunk-present skill. Set
// up (or Update) installs what is missing or outdated, as the banner
// does; expanding the row shows the files and Copy snippet.
type setupPrefs struct {
	group    *adw.PreferencesGroup
	rows     []*adw.ExpanderRow // rebuilt by refreshSetup
	expanded map[string]bool    // the agents whose rows were open
	note     *gtk.Label
	hooks    []notify.Hooks
	skills   []notify.SkillState
}

// hookLooks are the states as a hooks row shows them, and setupLooks as an
// agent's row sums them up.
var (
	hookLooks = map[notify.HookState]string{
		notify.HooksInstalled: "✓ Installed", notify.HooksOutdated: "⚠ Outdated", notify.HooksMissing: "✗ Not set up",
	}
	setupLooks = map[notify.HookState][2]string{
		notify.HooksInstalled: {"✓ Hooks", "✓ Skill"},
		notify.HooksOutdated:  {"⚠ Hooks outdated", "⚠ Skill outdated"},
		notify.HooksMissing:   {"✗ No hooks", "✗ No skill"},
	}
)

// agentTitles are the agents' names as the preferences show them.
var agentTitles = map[string]string{"claude": "Claude Code", "codex": "Codex", "copilot": "Copilot CLI", "gemini": "Gemini CLI"}

// agentTitle is agent's name as the preferences show it.
func agentTitle(agent string) string {
	if t, ok := agentTitles[agent]; ok {
		return t
	}
	return agent
}

// addSetup adds the Setup section to the preferences.
func (w *prefsWindow) addSetup() {
	sp := &setupPrefs{note: label("", "statelabel"), expanded: map[string]bool{}}
	sp.group = w.group("Setup", "With hooks, an agent tells bunk-gui when it works, needs you and is done; "+
		"without them, its state is guessed from its output. The skill tells it when and how to show documents in the panel.")
	w.controls["hooks"] = sp.group
	guide := gtk.NewButtonWithLabel("Setup guide")
	guide.AddCSSClass("flat")
	guide.SetVAlign(gtk.AlignCenter)
	guide.ConnectClicked(func() { openURL(w.a, hooksDocsURL) })
	sp.group.SetHeaderSuffix(guide)
	w.controls["hooks-guide"] = guide
	sp.note.SetWrap(true)
	sp.note.SetXAlign(0)
	sp.note.SetMarginTop(6)
	sp.note.SetVisible(false)
	sp.group.Add(sp.note)
	w.setup = sp
	w.refreshSetup()
}

// addAgentBehaviour adds the switches for documents and the reminder.
func (w *prefsWindow) addAgentBehaviour() {
	g := w.group("Behaviour", "")
	w.presentAuto = w.switchRow(g, "present-auto", "Open the panel for documents an agent writes", "", w.a.fileSettings.presentAuto,
		func(on bool) {
			if w.save(func(content string) string {
				return patchTable(content, "present", []keysEdit{{action: "auto", value: strconv.FormatBool(on)}})
			}) {
				w.message = ""
			}
		})
	w.hooksHint = w.switchRow(g, "hooks-hint", "Remind me when an agent runs without hooks or skill", "", w.a.fileSettings.hooksHint,
		func(on bool) { w.saveTop(keysEdit{action: "hooks_hint", value: strconv.FormatBool(on)}) })
}

// refreshSetup rebuilds the agent rows from the files as they are now,
// keeping open the rows that were.
func (w *prefsWindow) refreshSetup() {
	sp := w.setup
	for _, r := range sp.rows {
		sp.group.Remove(r)
	}
	sp.rows = nil
	d := w.a.hookDirs()
	sp.hooks, sp.skills = notify.CheckHooks(d), notify.CheckSkills(d)
	skills := map[string]notify.SkillState{}
	for _, s := range sp.skills {
		skills[s.Agent] = s
	}
	for _, h := range sp.hooks {
		agent := h.Agent
		sk, hasSkill := skills[agent]
		row := adw.NewExpanderRow()
		row.SetUseMarkup(false)
		row.SetTitle(agentTitle(agent))
		sum := []string{setupLooks[h.State][0]}
		if hasSkill {
			sum = append(sum, setupLooks[sk.State][1])
		}
		if !h.CLI {
			sum = append(sum, "not on PATH")
		}
		if h.Problem != "" || hasSkill && sk.Problem != "" {
			sum = append(sum, "⚠ a file cannot be read")
		}
		row.SetSubtitle(strings.Join(sum, " · "))
		missing := h.State == notify.HooksMissing || hasSkill && sk.State == notify.HooksMissing
		if h.State != notify.HooksInstalled || hasSkill && sk.State != notify.HooksInstalled {
			text := "Update"
			if missing {
				text = "Set up"
			}
			b := gtk.NewButtonWithLabel(text)
			b.SetVAlign(gtk.AlignCenter)
			b.ConnectClicked(func() { w.setUpAgent(agent) })
			row.AddSuffix(b)
			w.controls["setup-"+agent] = b
		} else {
			delete(w.controls, "setup-"+agent)
		}
		hooks := actionRow("Hooks", fileLine(hookLooks[h.State], d.Short(h.Path), h.Problem))
		c := gtk.NewButtonWithLabel("Copy snippet")
		c.SetVAlign(gtk.AlignCenter)
		c.ConnectClicked(func() { w.copyHooks(agent) })
		hooks.AddSuffix(c)
		w.controls["hooks-"+agent+"-copy"] = c
		row.AddRow(hooks)
		if hasSkill {
			row.AddRow(actionRow("bunk-present skill", fileLine(hookLooks[sk.State], d.Short(sk.Path), sk.Problem)))
		}
		row.NotifyProperty("expanded", func() { sp.expanded[agent] = row.Expanded() })
		row.SetExpanded(sp.expanded[agent])
		w.controls["setup-row-"+agent] = row
		sp.group.Add(row)
		sp.rows = append(sp.rows, row)
	}
}

// fileLine is a subtitle for a file: its state and path, then on a line
// of its own why it could not be read.
func fileLine(state, path, problem string) string {
	line := state + " · " + path
	if problem != "" {
		line += "\n" + problem
	}
	return line
}

// setUpAgent installs agent's hooks and skill where they are missing or
// outdated, and says what it did.
func (w *prefsWindow) setUpAgent(agent string) {
	d := w.a.hookDirs()
	var notes []string
	if h, ok := notify.CheckHook(d, agent); ok && h.State != notify.HooksInstalled {
		text, _ := w.a.installHooks(agent)
		notes = append(notes, text)
	}
	if s, ok := notify.CheckSkill(d, agent); ok && s.State != notify.HooksInstalled {
		if path, err := notify.InstallSkill(d, agent); err != nil {
			notes = append(notes, "The skill was not installed: "+err.Error())
		} else {
			notes = append(notes, "Installed the skill in "+d.Short(path)+".")
		}
	}
	showNote(w.setup.note, agentTitle(agent)+": "+strings.Join(notes, " ")+" New sessions use it.")
	w.refreshSetup()
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
	showNote(w.setup.note, "Copied the "+agent+" snippet: "+where)
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
	for _, h := range w.setup.hooks {
		out = append(out, hooksReport{Agent: h.Agent, State: h.State.String(), Path: h.Path, Installable: h.Installable})
	}
	return out
}

// skillsShown is the skill rows in --report.
func (w *prefsWindow) skillsShown() []hooksReport {
	out := []hooksReport{}
	for _, s := range w.setup.skills {
		out = append(out, hooksReport{Agent: s.Agent, State: s.State.String(), Path: s.Path, Installable: true})
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
		a.prefsWin.refreshSetup()
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

// showPreferencesAt opens the preferences at the control named name,
// once the window is laid out.
func (a *App) showPreferencesAt(name string) {
	a.showPreferences()
	if w := a.prefsWin; w != nil {
		w.scrollWhenReady(name, func(err error) {
			if err != nil {
				slog.Debug("preferences: no scroll", "to", name, "err", err)
			}
		})
	}
}
