package main

// Desktop notifications for agent states and long commands, one per pane,
// as in the TUI (bunk's alerts.go): two agents split in one tab each send
// their own.
//
//   - internal/alert decides: an agent enters needs-you, done or failed
//     ([notifications] states), or a command that ran command_after
//     seconds ends, while the user is not looking at its pane: the window
//     is inactive, or the pane is not the focused pane of the selected
//     tab. A newer state replaces the pane's notification; looking at the
//     pane, the state leaving those states, turning notifications off and
//     closing the pane withdraw it. A command's notification stays until
//     the user looks at the pane.
//   - syncAlerts runs whenever a pane's status, the selection, the
//     focused pane or the window's focus changes (refreshChrome and the
//     is-active notify). It also reads each pane's command mark for its
//     tab and badge: a command running command_after seconds or longer,
//     then done or failed until the user looks at the pane.
//   - Clicking a notification runs app.show-pane with the pane's ID: the
//     window takes the focus with the shell's activation token, the
//     pane's tab is selected and the pane focused.
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

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/alert"
	"bunk/internal/session"
)

// showPaneAction is the app action a notification's default action runs,
// with the pane's ID as its target.
const showPaneAction = "show-pane"

// alertState is the window's desktop notifications.
type alertState struct {
	tracker alert.Tracker
	desk    *deskNotifier // nil on the test compositor and without a session bus
	due     time.Time     // when the timeout for a held alert fires
	click   string        // activation token of the click show-pane handles
	decoy   *gtk.Window   // a test window holding the focus ("decoy open")
}

// alertRecord is one notification call in --report.
type alertRecord struct {
	Op     string `json:"op"`   // send, withdraw, or activate (a click)
	Pane   int    `json:"pane"` // the pane's ID: the notification's key and its action's target
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
	State  string `json:"state,omitempty"`  // needs-you, done, failed or command
	Action string `json:"action,omitempty"` // the default action
	Token  bool   `json:"token,omitempty"`  // activate: the click came with an activation token
	TMs    int64  `json:"t_ms"`
}

// postAlert shows al as the pane's desktop notification, replacing the
// one it has. Tests replace it. It records the call in --report.
var postAlert = func(a *App, pane int, al alert.Alert) {
	a.recordAlert(alertRecord{Op: "send", Pane: pane, Title: al.Title, Body: al.Body(),
		State: al.Name(), Action: "app." + showPaneAction})
	if a.alerts.desk != nil {
		a.alerts.desk.send(pane, al)
	}
}

// withdrawAlert removes the pane's desktop notification. Tests replace
// it. It records the call in --report.
var withdrawAlert = func(a *App, pane int) {
	a.recordAlert(alertRecord{Op: "withdraw", Pane: pane})
	if a.alerts.desk != nil {
		a.alerts.desk.withdraw(pane)
	}
}

func (a *App) recordAlert(r alertRecord) {
	r.TMs = time.Since(a.started).Milliseconds()
	a.rep.Notifications = append(a.rep.Notifications, r)
	fmt.Printf("notification: %s pane %d %q\n", r.Op, r.Pane, r.Title)
}

// initAlerts adds the show-pane action and connects the notifications to
// the window's focus and the notification server.
func (a *App) initAlerts() {
	act := gio.NewSimpleAction(showPaneAction, glib.NewVariantType("i"))
	act.ConnectActivate(func(target *glib.Variant) {
		if target != nil {
			a.showPane(int(target.Int32()))
		}
	})
	a.app.AddAction(act)
	a.win.NotifyProperty("is-active", a.syncMarks)
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
// notification server asked: app.show-pane with the pane's ID.
func (a *App) clickAlert(pane int, token string) {
	a.alerts.click = token
	a.app.ActivateAction(showPaneAction, glib.NewVariantInt32(int32(pane)))
	a.alerts.click = ""
}

// showPane is app.show-pane: it raises the window, selects the tab of the
// pane with the ID id and focuses the pane. With the activation token of
// the click, the window takes the focus on Wayland.
func (a *App) showPane(id int) {
	token := a.alerts.click
	a.alerts.click = ""
	a.recordAlert(alertRecord{Op: "activate", Pane: id, Token: token != ""})
	p := a.paneByID(id)
	if p == nil || a.finished {
		return
	}
	if token != "" {
		a.win.SetStartupID(token)
	} else {
		a.win.Present()
	}
	a.selectTab(p.tab)
	if p.tab.focus != p {
		p.widget.GrabFocus()
	}
	a.onPaneFocus(p)
}

// paneByID is the open pane with the ID id, or nil.
func (a *App) paneByID(id int) *Pane {
	if id < 0 || id >= len(a.panes) || a.panes[id] == nil || a.panes[id].removeOnce {
		return nil
	}
	return a.panes[id]
}

// syncAlerts brings every pane's notification and command mark up to date
// with its status, the selected tab, its focused pane and the window's
// focus. When a held alert or a mark is due later, it runs again then.
func (a *App) syncAlerts() {
	if a.finished || a.win == nil {
		return
	}
	tr := &a.alerts.tracker
	now, active := time.Now(), a.win.IsActive()
	for _, t := range a.tabs {
		// A muted tab's panes alert as with notifications off: what they
		// show is withdrawn, and their marks stay.
		tr.Settings = a.settings.alerts
		if t.muted {
			tr.Settings.Enabled = false
		}
		seen := t.seen // Mark as seen: watched once
		t.seen = false
		for _, p := range t.panes() {
			watched := seen || active && t == a.active && t.focus == p
			switch act, al := tr.Update(p.id, p.status(), watched, now); act {
			case alert.Send:
				postAlert(a, p.id, al)
			case alert.Withdraw:
				withdrawAlert(a, p.id)
			}
			p.mark = tr.Mark(p.id, now)
		}
	}
	tr.Settings = a.settings.alerts
	for _, id := range tr.IDs() {
		if a.paneByID(id) == nil && tr.Remove(id) == alert.Withdraw {
			withdrawAlert(a, id)
		}
	}
	if next := tr.Next(); !next.IsZero() && !next.Equal(a.alerts.due) {
		a.alerts.due = next
		coreglib.TimeoutAdd(uint(max(0, time.Until(next).Milliseconds())+1), func() bool {
			a.alerts.due = time.Time{}
			a.syncMarks()
			return false
		})
	}
}

// syncMarks shows the command marks where they changed: in the tabs and
// the selected tab's badges. refreshChrome syncs the alerts first, so the
// marks are current.
func (a *App) syncMarks() {
	a.refreshChrome()
	a.syncBadges()
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

// alertClick is the "alert-click ID" script action: it clicks the
// notification of the pane with that ID as the notification server
// would, without a token.
func (a *App) alertClick(id int) error {
	if !a.alerts.tracker.Shown(id) {
		return fmt.Errorf("pane %d has no notification", id)
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
	on           *adw.SwitchRow
	states       []*adw.SwitchRow // in alert.AlertingStates order
	commandAfter *adw.SpinRow     // seconds; 0 marks and sends none for commands
}

// notifyLabels name the states in the preferences window.
var notifyLabels = map[session.AgentState]string{
	session.AgentNeedsYou: "When an agent needs you", session.AgentDone: "When an agent is done", session.AgentFailed: "When an agent fails",
}

// addNotifications adds the Notifications section to the preferences:
// a switch for desktop notifications, the states that send one, and how
// long a command must run for its tab to mark it and its end to send
// one, saved to [notifications] as they change.
func (w *prefsWindow) addNotifications() {
	st := w.a.fileSettings.alerts
	np := &notifyPrefs{}
	g := w.group("", "")
	np.on = w.switchRow(g, "notify", "Desktop notifications", "For tabs you are not looking at", st.Enabled,
		func(on bool) { w.saveNotify(keysEdit{action: "enabled", value: strconv.FormatBool(on)}) })
	g = w.group("Agents", "")
	for _, s := range alert.AlertingStates() {
		r := w.switchRow(g, "notify-"+alert.StateName(s), notifyLabels[s], "", slices.Contains(st.States, s), func(bool) {
			_, names := w.notifyShown()
			w.saveNotify(keysEdit{action: "states", value: tomlStrings(names)})
		})
		r.SetSensitive(st.Enabled)
		np.states = append(np.states, r)
	}
	np.on.NotifyProperty("active", func() {
		for _, r := range np.states {
			r.SetSensitive(np.on.Active())
		}
	})
	g = w.group("Long commands", "A command in a tab you are not looking at that runs this long gets a mark on its tab, "+
		"and its end sends a notification.")
	np.commandAfter = w.spinRow(g, "notify-command-after", "Mark after", "Seconds; 0 turns this off", 0, 24*60*60, 1, 0,
		st.CommandAfter.Seconds(), func(v float64) { w.saveNotify(keysEdit{action: "command_after", value: strconv.Itoa(int(v))}) })
	w.notify = np
}

func (w *prefsWindow) saveNotify(e keysEdit) {
	if w.save(func(content string) string { return patchTable(content, "notifications", []keysEdit{e}) }) {
		w.message = ""
	}
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
