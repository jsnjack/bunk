package main

// Desktop notifications for agent states, one per tab.
//
//   - internal/alert decides: an agent enters needs-you, done or failed
//     ([notifications] states) while the window is inactive or another tab
//     is selected. A newer state replaces the tab's notification; looking
//     at the tab, the state leaving those states, turning notifications
//     off and closing the tab withdraw it.
//   - syncAlerts runs whenever a tab's status, the selection or the
//     window's focus changes (refreshChrome and the is-active notify).
//   - Clicking a notification runs app.show-tab with the tab's ID: the
//     window takes the focus with the shell's activation token, and the
//     tab is selected.
//   - postAlert and withdrawAlert record every call in --report and reach
//     the notification server (desknotify.go) only off the test
//     compositor. Tests replace them.
//
// The window is not marked as needing attention. GTK 4 has no urgency
// hint; on Wayland, presenting a window without an activation token makes
// mutter mark it, and GNOME Shell then posts a second notification
// ("bunk-gui is ready") next to the agent's own.

import (
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/alert"
	"bunk/internal/session"
)

// showTabAction is the app action a notification's default action runs,
// with the tab's ID as its target.
const showTabAction = "show-tab"

// alertState is the window's desktop notifications.
type alertState struct {
	tracker alert.Tracker
	desk    *deskNotifier // nil on the test compositor and without a session bus
	due     time.Time     // when the timeout for a held alert fires
	click   string        // activation token of the click show-tab handles
	decoy   *gtk.Window   // a test window holding the focus ("decoy open")
}

// alertRecord is one notification call in --report.
type alertRecord struct {
	Op     string `json:"op"`  // send, withdraw, or activate (a click)
	Tab    int    `json:"tab"` // the tab's ID: the notification's key and its action's target
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
	State  string `json:"state,omitempty"`  // needs-you, done or failed
	Action string `json:"action,omitempty"` // the default action
	Token  bool   `json:"token,omitempty"`  // activate: the click came with an activation token
	TMs    int64  `json:"t_ms"`
}

// postAlert shows al as the tab's desktop notification, replacing the one
// it has. Tests replace it. It records the call in --report.
var postAlert = func(a *App, tab int, al alert.Alert) {
	a.recordAlert(alertRecord{Op: "send", Tab: tab, Title: al.Title, Body: al.Body(),
		State: alert.StateName(al.State), Action: "app." + showTabAction})
	if a.alerts.desk != nil {
		a.alerts.desk.send(tab, al)
	}
}

// withdrawAlert removes the tab's desktop notification. Tests replace it.
// It records the call in --report.
var withdrawAlert = func(a *App, tab int) {
	a.recordAlert(alertRecord{Op: "withdraw", Tab: tab})
	if a.alerts.desk != nil {
		a.alerts.desk.withdraw(tab)
	}
}

func (a *App) recordAlert(r alertRecord) {
	r.TMs = time.Since(a.started).Milliseconds()
	a.rep.Notifications = append(a.rep.Notifications, r)
	fmt.Printf("notification: %s tab %d %q\n", r.Op, r.Tab, r.Title)
}

// initAlerts adds the show-tab action and connects the notifications to
// the window's focus and the notification server.
func (a *App) initAlerts() {
	act := gio.NewSimpleAction(showTabAction, glib.NewVariantType("i"))
	act.ConnectActivate(func(target *glib.Variant) {
		if target != nil {
			a.showTab(int(target.Int32()))
		}
	})
	a.app.AddAction(act)
	a.win.NotifyProperty("is-active", a.syncAlerts)
	a.app.ConnectShutdown(a.withdrawAlerts)
	if where := testCompositor(); where != "" {
		slog.Debug("desktop notifications are recorded, not shown", "on", where)
		return
	}
	bus := a.app.DBusConnection()
	if bus == nil {
		slog.Debug("desktop notifications off: no session bus")
		return
	}
	a.alerts.desk = newDeskNotifier(bus, a.clickAlert)
}

// clickAlert runs a clicked notification's default action, as the
// notification server asked: app.show-tab with the tab's ID.
func (a *App) clickAlert(tab int, token string) {
	a.alerts.click = token
	a.app.ActivateAction(showTabAction, glib.NewVariantInt32(int32(tab)))
	a.alerts.click = ""
}

// showTab is app.show-tab: it raises the window and selects the tab with
// the ID id. With the activation token of the click, the window takes the
// focus on Wayland.
func (a *App) showTab(id int) {
	token := a.alerts.click
	a.alerts.click = ""
	a.recordAlert(alertRecord{Op: "activate", Tab: id, Token: token != ""})
	t := a.tabByID(id)
	if t == nil || a.finished {
		return
	}
	if token != "" {
		a.win.SetStartupID(token)
	} else {
		a.win.Present()
	}
	a.selectTab(t)
}

func (a *App) tabByID(id int) *Tab {
	for _, t := range a.tabs {
		if t.id == id {
			return t
		}
	}
	return nil
}

// syncAlerts brings every tab's notification up to date with its agent's
// status, the selected tab and the window's focus.
func (a *App) syncAlerts() {
	if a.finished || a.win == nil {
		return
	}
	tr := &a.alerts.tracker
	tr.Settings = a.settings.alerts
	now, active := time.Now(), a.win.IsActive()
	for _, t := range a.tabs {
		var st session.Status
		if p := t.agentPane(); p != nil {
			st = p.status()
		}
		switch act, al := tr.Update(t.id, st, active && t == a.active, now); act {
		case alert.Send:
			postAlert(a, t.id, al)
		case alert.Withdraw:
			withdrawAlert(a, t.id)
		}
	}
	for _, id := range tr.IDs() {
		if a.tabByID(id) == nil && tr.Remove(id) == alert.Withdraw {
			withdrawAlert(a, id)
		}
	}
	if next := tr.Next(); !next.IsZero() && !next.Equal(a.alerts.due) {
		a.alerts.due = next
		coreglib.TimeoutAdd(uint(max(0, time.Until(next).Milliseconds())+1), func() bool {
			a.alerts.due = time.Time{}
			a.syncAlerts()
			return false
		})
	}
}

// withdrawAlerts removes every notification the window has up, when the
// application shuts down.
func (a *App) withdrawAlerts() {
	tr := &a.alerts.tracker
	for _, id := range tr.IDs() {
		if tr.Remove(id) == alert.Withdraw {
			withdrawAlert(a, id)
		}
	}
	if a.alerts.desk != nil {
		a.alerts.desk.close()
	}
}

// alertClick is the "alert-click N" script action: it clicks tab N's
// notification as the notification server would, without a token.
func (a *App) alertClick(n int) error {
	if n < 1 || n > len(a.tabs) {
		return fmt.Errorf("no tab %d", n)
	}
	id := a.tabs[n-1].id
	if !a.alerts.tracker.Shown(id) {
		return fmt.Errorf("tab %d has no notification", n)
	}
	a.clickAlert(id, "")
	return nil
}

// decoy is the "decoy open|close" script action: a second window takes
// the keyboard focus, so bunk-gui's window is inactive, until it closes.
func (a *App) decoy(open bool) error {
	switch {
	case open && a.alerts.decoy == nil:
		w := gtk.NewWindow()
		w.SetTitle("decoy")
		w.SetDefaultSize(320, 200)
		w.Present()
		a.alerts.decoy = w
	case !open && a.alerts.decoy != nil:
		a.alerts.decoy.Destroy()
		a.alerts.decoy = nil
	default:
		return fmt.Errorf("the decoy window is already %s", map[bool]string{true: "open", false: "closed"}[open])
	}
	return nil
}

// notifyPrefs are the preferences window's notification controls.
type notifyPrefs struct {
	on     *gtk.Switch
	states []*gtk.CheckButton // in alert.AlertingStates order
}

// notifyLabels name the states in the preferences window.
var notifyLabels = map[session.AgentState]string{
	session.AgentNeedsYou: "Needs you", session.AgentDone: "Is done", session.AgentFailed: "Failed",
}

// addNotifications adds the Notifications section to the preferences:
// a switch for desktop notifications and the states that send one,
// saved to [notifications] as they change.
func (w *prefsWindow) addNotifications(box *gtk.Box) {
	st := w.a.fileSettings.alerts
	np := &notifyPrefs{on: gtk.NewSwitch()}
	box.Append(heading("Notifications"))
	np.on.SetActive(st.Enabled)
	np.on.ConnectStateSet(func(on bool) bool {
		if !w.syncing {
			w.saveNotify(keysEdit{action: "enabled", value: strconv.FormatBool(on)})
		}
		return false
	})
	box.Append(prefRow("Desktop notifications", np.on))
	w.controls["notify"] = np.on
	hint := label("Sent when an agent in a tab you are not looking at:", "statelabel")
	hint.SetXAlign(0)
	box.Append(hint)
	for _, s := range alert.AlertingStates() {
		c := gtk.NewCheckButtonWithLabel(notifyLabels[s])
		c.SetActive(slices.Contains(st.States, s))
		c.ConnectToggled(func() {
			if !w.syncing {
				_, names := w.notifyShown()
				w.saveNotify(keysEdit{action: "states", value: tomlStrings(names)})
			}
		})
		box.Append(c)
		w.controls["notify-"+alert.StateName(s)] = c
		np.states = append(np.states, c)
	}
	w.notify = np
}

func (w *prefsWindow) saveNotify(e keysEdit) {
	w.save(func(content string) string { return patchTable(content, "notifications", []keysEdit{e}) })
	w.message.SetText("Saved notifications.")
}

// notifyShown returns what the notification controls show: the switch,
// and the config names of the checked states.
func (w *prefsWindow) notifyShown() (bool, []string) {
	var states []session.AgentState
	for i, s := range alert.AlertingStates() {
		if w.notify.states[i].Active() {
			states = append(states, s)
		}
	}
	return w.notify.on.Active(), alert.Names(states)
}
