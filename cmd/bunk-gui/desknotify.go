package main

// The desktop's notification server (org.freedesktop.Notifications), as
// the panes' notifications use it (alerts.go).
//
// bunk-gui does not use GNotification. On GNOME, GLib sends it through
// org.gtk.Notifications: GNOME Shell takes it only from an installed
// desktop file, and runs its action through the application's bus name,
// which a non-unique application such as bunk-gui never owns, so a click
// would do nothing. GLib's freedesktop backend avoids both, but it drops
// the activation token the shell sends with a click, and a Wayland window
// cannot take the focus without one. This client keeps the token.
//
// Calls are asynchronous and run on the main thread. A server that is
// missing or fails makes the call fail, which is logged at debug level.

import (
	"context"
	"log/slog"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"

	"bunk/internal/alert"
)

const (
	fdoName  = "org.freedesktop.Notifications"
	fdoPath  = "/org/freedesktop/Notifications"
	fdoIface = fdoName
	// fdoDefault is the key of a notification's default action: the
	// server invokes it when the notification itself is clicked.
	fdoDefault = "default"
)

// deskNotifier keeps one notification per pane on the notification server.
type deskNotifier struct {
	bus    *gio.DBusConnection
	notes  map[int]*deskNote // by pane ID
	paneOf map[uint32]int    // pane ID by server ID
	// The activation token the server sent last, and the server ID of
	// the notification it came with.
	token    string
	tokenFor uint32
	sub      uint
	// onClick runs when the user clicks a pane's notification, with the
	// activation token for it ("" when the server sent none).
	onClick func(pane int, token string)
}

// deskNote is one pane's notification.
type deskNote struct {
	id       uint32       // the server's ID; 0 while none is known
	busy     bool         // a Notify call waits for its answer
	next     *alert.Alert // to show when that call answers
	withdraw bool         // withdraw when that call answers
}

// newDeskNotifier listens to the notification server's signals on bus.
func newDeskNotifier(bus *gio.DBusConnection, onClick func(pane int, token string)) *deskNotifier {
	d := &deskNotifier{bus: bus, notes: map[int]*deskNote{}, paneOf: map[uint32]int{}, onClick: onClick}
	d.sub = bus.SignalSubscribe(fdoName, fdoIface, "", fdoPath, "", gio.DBusSignalFlagsNone, d.onSignal)
	return d
}

// close stops listening to the server.
func (d *deskNotifier) close() { d.bus.SignalUnsubscribe(d.sub) }

// send shows al as pane's notification, replacing the one it has.
func (d *deskNotifier) send(pane int, al alert.Alert) {
	n := d.notes[pane]
	if n == nil {
		n = &deskNote{}
		d.notes[pane] = n
	}
	if n.busy {
		// The server answers with the ID to replace; the newest alert
		// goes out then.
		n.next, n.withdraw = &al, false
		return
	}
	n.busy = true
	d.bus.Call(context.Background(), fdoName, fdoPath, fdoIface, "Notify", notifyArgs(n.id, al),
		glib.NewVariantType("(u)"), gio.DBusCallFlagsNone, -1, func(res gio.AsyncResulter) {
			v, err := d.bus.CallFinish(res)
			n.busy = false
			if err != nil {
				slog.Debug("desktop notification not shown", "pane", pane, "err", err)
			} else {
				id := v.ChildValue(0).Uint32()
				if n.id != 0 && n.id != id {
					delete(d.paneOf, n.id)
				}
				n.id = id
				d.paneOf[id] = pane
			}
			switch {
			case n.withdraw:
				n.withdraw = false
				d.withdraw(pane)
			case n.next != nil:
				next := *n.next
				n.next = nil
				d.send(pane, next)
			}
		})
}

// withdraw removes pane's notification.
func (d *deskNotifier) withdraw(pane int) {
	n := d.notes[pane]
	if n == nil {
		return
	}
	if n.busy {
		n.withdraw, n.next = true, nil
		return
	}
	delete(d.notes, pane)
	if n.id == 0 {
		return
	}
	delete(d.paneOf, n.id)
	args := glib.NewVariantTuple([]*glib.Variant{glib.NewVariantUint32(n.id)})
	d.bus.Call(context.Background(), fdoName, fdoPath, fdoIface, "CloseNotification", args,
		nil, gio.DBusCallFlagsNone, -1, func(res gio.AsyncResulter) {
			if _, err := d.bus.CallFinish(res); err != nil {
				slog.Debug("desktop notification not withdrawn", "pane", pane, "err", err)
			}
		})
}

// onSignal handles the server's signals. Their arguments come from
// another process, so their types are checked first.
func (d *deskNotifier) onSignal(_ *gio.DBusConnection, _, _, _, signal string, params *glib.Variant) {
	switch {
	case signal == "ActivationToken" && params.IsOfType(glib.NewVariantType("(us)")):
		d.tokenFor, d.token = params.ChildValue(0).Uint32(), params.ChildValue(1).String()
	case signal == "ActionInvoked" && params.IsOfType(glib.NewVariantType("(us)")):
		id, key := params.ChildValue(0).Uint32(), params.ChildValue(1).String()
		token := ""
		if d.tokenFor == id {
			token = d.token
		}
		d.token, d.tokenFor = "", 0
		if pane, ok := d.paneOf[id]; ok && key == fdoDefault {
			d.onClick(pane, token)
		}
	case signal == "NotificationClosed" && params.IsOfType(glib.NewVariantType("(uu)")):
		id := params.ChildValue(0).Uint32()
		pane, ok := d.paneOf[id]
		if !ok {
			return
		}
		delete(d.paneOf, id)
		if n := d.notes[pane]; n != nil && n.id == id {
			n.id = 0
			if !n.busy {
				delete(d.notes, pane)
			}
		}
	}
}

// notifyArgs are the arguments of a Notify call that shows al, replacing
// the server's notification replaces (0 for a new one). The text is
// cleaned and its body escaped as markup (alert.DesktopText).
func notifyArgs(replaces uint32, al alert.Alert) *glib.Variant {
	hint := func(key string, v *glib.Variant) *glib.Variant {
		return glib.NewVariantDictEntry(glib.NewVariantString(key), glib.NewVariantVariant(v))
	}
	hints := glib.NewVariantArray(glib.NewVariantType("{sv}"), []*glib.Variant{
		hint("desktop-entry", glib.NewVariantString(appID)),
		hint("urgency", glib.NewVariantByte(1)), // normal
	})
	title, body := alert.DesktopText(al)
	return glib.NewVariantTuple([]*glib.Variant{
		glib.NewVariantString(defaultTitle),
		glib.NewVariantUint32(replaces),
		glib.NewVariantString(appID),
		glib.NewVariantString(title),
		glib.NewVariantString(body),
		glib.NewVariantStrv([]string{fdoDefault, "Show tab"}),
		hints,
		glib.NewVariantInt32(-1),
	})
}
