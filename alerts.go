// alerts.go - agent notifications through the host terminal.
//
// When an agent in a pane the user is not looking at needs them, is done
// or failed, bunk writes an OSC 777 notify sequence to the host terminal,
// which shows it as a desktop notification. internal/alert decides when,
// from [notifications] in config.toml, and builds the bounded, sanitised
// sequence: the notice is program-controlled text. A host terminal cannot
// withdraw a notification, so only sends reach it.
package main

import (
	"bunk/internal/alert"
	"context"
	"time"
)

// forwardAlerts writes a notification to the host for each pane whose
// agent entered an alerting state while the user was not looking at it:
// another pane was active, or the host terminal did not have the focus.
// render calls it under app.mu, before any early return, so a pane's
// status is read in lock order on every frame.
func (app *App) forwardAlerts(root *Node, active *Pane) {
	now := time.Now()
	for _, leaf := range root.leaves() {
		p := leaf.pane
		watched := p == active && !app.hostBlurred
		act, al := app.alerts.Update(p.ID, p.Status(), watched, now)
		if act != alert.Send {
			continue
		}
		L.Debug("alert: notification to the host terminal", "pane", p.ID, "state", alert.StateName(al.State))
		if _, err := app.host().Write(alert.OSC777(al)); err != nil {
			L.Log(context.Background(), LevelTrace, "alert: write notification", "pane", p.ID, "err", err)
		}
	}
	// A held alert goes out on the first frame after it is due.
	if next := app.alerts.Next(); !next.IsZero() && !next.Equal(app.alertWake) {
		app.alertWake = next
		time.AfterFunc(time.Until(next), app.triggerRedraw)
	}
}
