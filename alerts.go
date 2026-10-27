// alerts.go - agent notifications on the desktop or through the host
// terminal.
//
// When an agent in a pane the user is not looking at needs them, is done
// or failed, or a long command there ends, bunk notifies the user.
// internal/alert decides when, from [notifications] in config.toml, and
// cleans the program-controlled text.
//
//   - On the desktop's own machine, with a session bus and gdbus, bunk
//     keeps one desktop notification per pane through D-Bus
//     (internal/desktop): a newer state replaces it, and acknowledging
//     the pane, turning notifications off, closing the pane or quitting
//     withdraws it. The notifications have no actions: bunk cannot raise
//     the host terminal's window.
//   - Over SSH, without a bus or gdbus, or when a D-Bus call fails, bunk
//     writes an OSC 777 notify sequence to the host terminal, which shows
//     it if it supports the sequence. A host terminal cannot withdraw it.
//
// One alert takes one of the two ways, never both.
package main

import (
	"bunk/internal/alert"
	"bunk/internal/desktop"
	"context"
	"os/exec"
	"time"
)

// desktopAppName and desktopIcon are the source and icon of bunk's desktop
// notifications.
const (
	desktopAppName = "bunk"
	desktopIcon    = "utilities-terminal"
)

// startDesktopAlerts chooses how alerts reach the user: desktop
// notifications when bunk runs on the user's desktop with a session bus
// and gdbus, OSC 777 to the host terminal otherwise. getenv is os.Getenv
// outside tests.
func (app *App) startDesktopAlerts(getenv func(string) string) {
	addr, why := desktop.SessionBus(getenv, desktop.IsSocket)
	if addr == "" {
		L.Debug("alert: notifications go to the host terminal", "why", why)
		return
	}
	gdbus, err := exec.LookPath("gdbus")
	if err != nil {
		L.Debug("alert: notifications go to the host terminal", "why", "no gdbus", "err", err)
		return
	}
	L.Debug("alert: desktop notifications through D-Bus", "bus", addr, "gdbus", gdbus)
	app.desktop = &desktop.Notifier{Gdbus: gdbus, Address: addr, AppName: desktopAppName, Icon: desktopIcon,
		Log: L, Fallback: app.alertToHostLater}
}

// closeDesktopAlerts withdraws the desktop notifications when bunk quits.
func (app *App) closeDesktopAlerts() {
	if app.desktop != nil {
		app.desktop.Close(2 * time.Second)
	}
}

// forwardAlerts shows or withdraws the notification of each pane whose
// agent entered or left an alerting state, where a long command ended, or
// that the user started or stopped looking at: the active pane while the
// host terminal has the focus. It also gives each pane its command mark
// for the badge. render calls it under app.mu, before any early return, so a
// pane's status is read in lock order on every frame.
func (app *App) forwardAlerts(root *Node, active *Pane) {
	now := time.Now()
	for _, leaf := range root.leaves() {
		p := leaf.pane
		watched := p == active && !app.hostBlurred
		switch act, al := app.alerts.Update(p.ID, p.Status(), watched, now); act {
		case alert.Send:
			app.sendAlert(p.ID, al)
		case alert.Withdraw:
			app.withdrawAlert(p.ID)
		}
		m := app.alerts.Mark(p.ID, now)
		p.Lock()
		p.mark = m
		p.Unlock()
	}
	// A held alert goes out, and a long command's mark shows, on the first
	// frame after it is due.
	if next := app.alerts.Next(); !next.IsZero() && !next.Equal(app.alertWake) {
		app.alertWake = next
		time.AfterFunc(time.Until(next), app.triggerRedraw)
	}
}

// sendAlert shows pane id's alert: a desktop notification, or OSC 777 to
// the host terminal. Called under app.mu; desktop calls run elsewhere.
func (app *App) sendAlert(id int, al alert.Alert) {
	L.Debug("alert: notify", "pane", id, "about", al.Name(), "desktop", app.desktop != nil)
	if app.desktop != nil {
		app.desktop.Send(id, al)
		return
	}
	if _, err := app.host().Write(alert.OSC777(al)); err != nil {
		L.Log(context.Background(), LevelTrace, "alert: write notification", "pane", id, "err", err)
	}
}

// withdrawAlert removes pane id's desktop notification; one sent to the
// host terminal stays.
func (app *App) withdrawAlert(id int) {
	if app.desktop != nil {
		app.desktop.Withdraw(id)
	}
}

// alertToHostLater is the desktop notifier's fallback: the alert goes to
// the host terminal with the next frame, through the OSC buffer, from the
// notifier's goroutine.
func (app *App) alertToHostLater(_ int, al alert.Alert) {
	app.oscBuf.append(alert.OSC777(al))
	app.triggerRedraw()
}
