package main

// Each pane's programs run in a systemd scope of their own, as in VTE's
// terminals (GNOME Terminal, Ptyxis).
//
// GNOME starts bunk-gui in app-gnome-<app ID>-<PID>.scope, and that scope's
// OOMPolicy is stop: when the kernel's OOM killer ends one process in it,
// systemd stops the whole scope. A pane's programs used to run there, so one
// program that ran out of memory in a pane closed bunk-gui with every pane.
// Now newPane asks the user's systemd to move the pane's child into a scope
// of its own, and the child's later processes start there. Its OOMPolicy is
// continue, so the OOM killer ends only the process it picked, and the
// pane's shell and agents go on. systemd removes the scope when its last
// process ends (CollectMode), also after a kill.
//
// The call is asynchronous, like VTE's move after the fork. systemd moves
// the child when it runs the scope's job, about 15 ms after the child
// started; a process the child forks before that stays in bunk-gui's
// scope. Without systemd on the session bus (the window tests' bus has
// none), the pane stays in bunk-gui's scope, logged at debug level.

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
)

const (
	sdName  = "org.freedesktop.systemd1"
	sdPath  = "/org/freedesktop/systemd1"
	sdIface = sdName + ".Manager"
)

// scopeUnit names the scope of the pane with this ID. Pane IDs are not
// reused within a bunk-gui process.
func scopeUnit(pane int) string {
	return fmt.Sprintf("bunk-gui-%d-pane-%d.scope", os.Getpid(), pane)
}

// moveToScope asks systemd on bus to move pid, the child of the pane with
// this ID, into a new scope.
func moveToScope(bus *gio.DBusConnection, pane, pid int) {
	unit := scopeUnit(pane)
	bus.Call(context.Background(), sdName, sdPath, sdIface, "StartTransientUnit", scopeArgs(unit, pane, pid),
		glib.NewVariantType("(o)"), gio.DBusCallFlagsNoAutoStart, -1, func(res gio.AsyncResulter) {
			if _, err := callFinish(bus, res); err != nil {
				slog.Debug("pane stays in bunk-gui's scope", "pane", pane, "err", err)
				return
			}
			slog.Debug("pane in its own scope", "pane", pane, "unit", unit)
		})
}

// scopeArgs are StartTransientUnit's arguments for a scope that holds pid.
func scopeArgs(unit string, pane, pid int) *glib.Variant {
	prop := func(name string, v *glib.Variant) *glib.Variant {
		return glib.NewVariantTuple([]*glib.Variant{glib.NewVariantString(name), glib.NewVariantVariant(v)})
	}
	props := glib.NewVariantArray(glib.NewVariantType("(sv)"), []*glib.Variant{
		prop("PIDs", glib.NewVariantArray(glib.NewVariantType("u"), []*glib.Variant{glib.NewVariantUint32(uint32(pid))})),
		prop("Description", glib.NewVariantString(fmt.Sprintf("bunk-gui pane %d", pane))),
		prop("Slice", glib.NewVariantString("app.slice")),
		prop("OOMPolicy", glib.NewVariantString("continue")),
		prop("CollectMode", glib.NewVariantString("inactive-or-failed")),
	})
	return glib.NewVariantTuple([]*glib.Variant{
		glib.NewVariantString(unit),
		glib.NewVariantString("fail"), // a unit of that name already exists: keep the pane where it is
		props,
		glib.NewVariantArray(glib.NewVariantType("(sa(sv))"), nil), // no auxiliary units
	})
}
