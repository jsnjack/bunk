package main

import (
	"strconv"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/notify"
)

// presentPrefs is the Documents section of Preferences: whether documents
// that agent hooks report open the panel ([present] auto), and each
// agent's bunk-present skill with an Install button (panel.go).
type presentPrefs struct {
	auto  *gtk.Switch
	rows  *gtk.Box
	note  *gtk.Label
	shown []notify.SkillState
}

var skillLooks = map[notify.HookState]string{
	notify.HooksInstalled: "✓ Skill installed",
	notify.HooksOutdated:  "⚠ Skill outdated",
	notify.HooksMissing:   "✗ No skill",
}

func (w *prefsWindow) addPresent(box *gtk.Box) {
	pp := &presentPrefs{auto: gtk.NewSwitch(), rows: gtk.NewBox(gtk.OrientationVertical, 10), note: label("", "statelabel")}
	h := heading("Documents")
	box.Append(h)
	w.controls["present"] = h
	intro := label("Agents show the Markdown and HTML they write in a panel beside the tab's panes. "+
		"The skill tells an agent when and how to do it.", "statelabel")
	intro.SetWrap(true)
	intro.SetXAlign(0)
	box.Append(intro)
	pp.auto.SetActive(w.a.fileSettings.presentAuto)
	pp.auto.ConnectStateSet(func(on bool) bool {
		if !w.syncing {
			w.save(func(content string) string {
				return patchTable(content, "present", []keysEdit{{action: "auto", value: strconv.FormatBool(on)}})
			})
			w.message.SetText("Saved documents.")
		}
		return false
	})
	box.Append(prefRow("Open the panel for documents an agent writes", pp.auto))
	w.controls["present-auto"] = pp.auto
	box.Append(pp.rows)
	pp.note.SetWrap(true)
	pp.note.SetXAlign(0)
	box.Append(pp.note)
	w.present = pp
	w.refreshSkills()
}

// refreshSkills rebuilds the skill rows from the files as they are now.
func (w *prefsWindow) refreshSkills() {
	pp := w.present
	for c := pp.rows.FirstChild(); c != nil; c = pp.rows.FirstChild() {
		pp.rows.Remove(c)
	}
	d := w.a.hookDirs()
	pp.shown = notify.CheckSkills(d)
	for _, s := range pp.shown {
		row := gtk.NewBox(gtk.OrientationHorizontal, 12)
		lines := gtk.NewBox(gtk.OrientationVertical, 2)
		top := gtk.NewBox(gtk.OrientationHorizontal, 8)
		top.Append(label(s.Agent, "agent"))
		top.Append(label(skillLooks[s.State], "statelabel"))
		lines.Append(top)
		path := label(d.Short(s.Path), "repo")
		path.SetXAlign(0)
		lines.Append(path)
		lines.SetHExpand(true)
		row.Append(lines)
		agent := s.Agent
		if s.State != notify.HooksInstalled {
			text := "Install"
			if s.State == notify.HooksOutdated {
				text = "Update"
			}
			b := gtk.NewButtonWithLabel(text)
			b.SetVAlign(gtk.AlignCenter)
			b.ConnectClicked(func() { w.installSkill(agent) })
			row.Append(b)
			w.controls["skill-"+agent+"-install"] = b
		} else {
			delete(w.controls, "skill-"+agent+"-install")
		}
		pp.rows.Append(row)
	}
}

func (w *prefsWindow) installSkill(agent string) {
	d := w.a.hookDirs()
	path, err := notify.InstallSkill(d, agent)
	if err != nil {
		w.present.note.SetText("Could not install the " + agent + " skill: " + err.Error())
	} else {
		w.present.note.SetText("Installed the " + agent + " skill in " + d.Short(path) + ". New " + agent + " sessions load it.")
	}
	w.refreshSkills()
}

// skillsShown is the skill rows in --report.
func (w *prefsWindow) skillsShown() []hooksReport {
	out := []hooksReport{}
	for _, s := range w.present.shown {
		out = append(out, hooksReport{Agent: s.Agent, State: s.State.String(), Path: s.Path, Installable: true})
	}
	return out
}
