package main

// Real input for tests: keys and pointer events injected through mutter's
// RemoteDesktop D-Bus API, so they reach the window as GDK events and go
// through the same controllers, input method and gestures as a user's.
// Only the private headless compositor of scripts/headless-gui.sh is ever
// driven: injection refuses to start unless BUNK_HEADLESS is set.

/*
#cgo pkg-config: gtk4
#include <gtk/gtk.h>
#include <stdio.h>

#define RD_NAME "org.gnome.Mutter.RemoteDesktop"

static GDBusConnection* inj_bus;
static char* inj_path;
static char inj_err[512];

static const char* inj_error(void) { return inj_err; }

static int inj_fail(GError* e) {
	snprintf(inj_err, sizeof inj_err, "%s", e ? e->message : "unknown error");
	if (e) g_error_free(e);
	return 0;
}

// The session lives as long as this connection: mutter ends it when the
// caller leaves the bus.
static int inj_open(void) {
	GError* e = NULL;
	if (inj_path) return 1;
	inj_bus = g_bus_get_sync(G_BUS_TYPE_SESSION, NULL, &e);
	if (!inj_bus) return inj_fail(e);
	GVariant* r = g_dbus_connection_call_sync(inj_bus, RD_NAME, "/org/gnome/Mutter/RemoteDesktop", RD_NAME,
		"CreateSession", NULL, G_VARIANT_TYPE("(o)"), G_DBUS_CALL_FLAGS_NONE, 5000, NULL, &e);
	if (!r) return inj_fail(e);
	g_variant_get(r, "(o)", &inj_path);
	g_variant_unref(r);
	r = g_dbus_connection_call_sync(inj_bus, RD_NAME, inj_path, RD_NAME ".Session", "Start", NULL, NULL,
		G_DBUS_CALL_FLAGS_NONE, 5000, NULL, &e);
	if (!r) {
		g_free(inj_path);
		inj_path = NULL;
		return inj_fail(e);
	}
	g_variant_unref(r);
	return 1;
}

static int inj_call(const char* method, GVariant* params) {
	GError* e = NULL;
	GVariant* r = g_dbus_connection_call_sync(inj_bus, RD_NAME, inj_path, RD_NAME ".Session", method, params, NULL,
		G_DBUS_CALL_FLAGS_NONE, 5000, NULL, &e);
	if (!r) return inj_fail(e);
	g_variant_unref(r);
	return 1;
}

static int inj_motion(double dx, double dy) { return inj_call("NotifyPointerMotionRelative", g_variant_new("(dd)", dx, dy)); }
static int inj_button(int button, int pressed) { return inj_call("NotifyPointerButton", g_variant_new("(ib)", button, pressed)); }
static int inj_keysym(unsigned keysym, int pressed) { return inj_call("NotifyKeyboardKeysym", g_variant_new("(ub)", keysym, pressed)); }
static int inj_keycode(unsigned code, int pressed) { return inj_call("NotifyKeyboardKeycode", g_variant_new("(ub)", code, pressed)); }

// The evdev keycode that types keyval in the first layout group, and the
// shift level it needs; -1 when the layout has no such key.
static int inj_map_keyval(unsigned keyval, int* level) {
	GdkKeymapKey* keys = NULL;
	int n = 0;
	if (!gdk_display_map_keyval(gdk_display_get_default(), keyval, &keys, &n) || n == 0) return -1;
	int best = 0;
	for (int i = 1; i < n; i++) {
		if (keys[i].group < keys[best].group || (keys[i].group == keys[best].group && keys[i].level < keys[best].level)) best = i;
	}
	int code = (int)keys[best].keycode - 8;
	*level = keys[best].level;
	g_free(keys);
	return code;
}
static int inj_scroll(int steps) { return inj_call("NotifyPointerAxisDiscrete", g_variant_new("(ui)", 0u, steps)); }

// The position of a popover's surface relative to its parent surface.
static int inj_popup_pos(uintptr_t popover, int* x, int* y) {
	GdkSurface* s = gtk_native_get_surface(GTK_NATIVE((GtkWidget*)popover));
	if (!s || !GDK_IS_POPUP(s)) return 0;
	*x = gdk_popup_get_position_x(GDK_POPUP(s));
	*y = gdk_popup_get_position_y(GDK_POPUP(s));
	return 1;
}
*/
import "C"

import (
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// evdev button codes (linux/input-event-codes.h).
var evdevButton = map[int]C.int{1: 0x110, 2: 0x112, 3: 0x111}

func injectOpen() error {
	if err := headlessOnly(); err != nil {
		return err
	}
	if C.inj_open() == 0 {
		return errors.New("RemoteDesktop: " + C.GoString(C.inj_error()))
	}
	return nil
}

// headlessOnly refuses injection anywhere but the wrapper's compositor. A
// stray BUNK_HEADLESS is not enough: the display must be one the wrapper
// named, and the bus must not be the desktop session's.
func headlessOnly() error {
	if os.Getenv("BUNK_HEADLESS") == "" {
		return errors.New("input injection only runs on the headless test compositor (BUNK_HEADLESS unset)")
	}
	if !strings.HasPrefix(os.Getenv("WAYLAND_DISPLAY"), "bunk-headless-") {
		return errors.New("input injection refused: WAYLAND_DISPLAY is not a scripts/headless-gui.sh display")
	}
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" &&
		strings.Contains(os.Getenv("DBUS_SESSION_BUS_ADDRESS"), rt+"/bus") {
		return errors.New("input injection refused: D-Bus is the desktop session bus")
	}
	return nil
}

func injectOK(rc C.int) error {
	if rc == 0 {
		return errors.New("RemoteDesktop: " + C.GoString(C.inj_error()))
	}
	return nil
}

// evdev keycodes of the modifier keys.
var modKeycodes = map[string]uint{"ctrl": 29, "shift": 42, "alt": 56, "super": 125}

// injectKeys presses and releases one combination such as "ctrl+shift+t",
// "F1" or "alt+Left" as physical keys (keycodes), like a keyboard does:
// letters name their key, and a character on a shifted level ("plus")
// presses Shift as well.
func (a *App) injectKeys(spec string) error {
	parts := strings.Split(spec, "+")
	name := parts[len(parts)-1]
	if name == "" && len(parts) > 1 { // "ctrl++"
		name = "plus"
	}
	var mods []uint
	for _, m := range parts[:len(parts)-1] {
		k, ok := modKeycodes[strings.ToLower(m)]
		if !ok {
			return errors.New("unknown modifier " + m)
		}
		mods = append(mods, k)
	}
	// Key names as in [keys] ("f12", "pgup", "return"), else GDK's.
	var key uint
	if c, err := parseCombo(name); err == nil {
		key = c.key
	} else if key = gdk.KeyvalFromName(name); key == 0 || key == 0xffffff {
		return errors.New("unknown key " + name)
	}
	var level C.int
	code := C.inj_map_keyval(C.uint(key), &level)
	if code < 0 {
		return errors.New("no key in the layout for " + name)
	}
	if level == 1 && !slices.Contains(mods, modKeycodes["shift"]) {
		mods = append(mods, modKeycodes["shift"])
	}
	for _, m := range mods {
		if err := injectOK(C.inj_keycode(C.uint(m), 1)); err != nil {
			return err
		}
	}
	err := injectOK(C.inj_keycode(C.uint(code), 1))
	if err == nil {
		err = injectOK(C.inj_keycode(C.uint(code), 0))
	}
	for i := len(mods) - 1; i >= 0; i-- {
		if e := injectOK(C.inj_keycode(C.uint(mods[i]), 0)); err == nil {
			err = e
		}
	}
	return err
}

// addDevices taps Shift and nudges the pointer: mutter adds a virtual
// keyboard and pointer to the seat on their first events, and the window
// misses those first events.
func (a *App) addDevices() error {
	for _, rc := range []func() C.int{
		func() C.int { return C.inj_keycode(C.uint(modKeycodes["shift"]), 1) },
		func() C.int { return C.inj_keycode(C.uint(modKeycodes["shift"]), 0) },
		func() C.int { return C.inj_motion(1, 1) },
		func() C.int { return C.inj_motion(-1, -1) },
	} {
		if err := injectOK(rc()); err != nil {
			return err
		}
	}
	return nil
}

// injectText types text key by key; mutter adds Shift where a character
// needs it. \n and \r are Return, \t is Tab.
func (a *App) injectText(text string) error {
	a.markInject()
	for _, r := range text {
		var key uint
		switch r {
		case '\n', '\r':
			key = gdk.KEY_Return
		case '\t':
			key = gdk.KEY_Tab
		default:
			key = gdk.UnicodeToKeyval(uint32(r))
		}
		if err := injectOK(C.inj_keysym(C.uint(key), 1)); err != nil {
			return err
		}
		if err := injectOK(C.inj_keysym(C.uint(key), 0)); err != nil {
			return err
		}
	}
	return nil
}

// markInject stamps the injection, for the inject -> PTY write latency.
func (a *App) markInject() { a.injectAt = monoNs() }

// pointerTo moves the pointer to window coordinates. The window must be
// maximized: it then starts at the monitor's origin, and the pointer is
// first pushed into the corner, so relative motion lands exactly.
func (a *App) pointerTo(x, y float64) error {
	if !a.win.IsMaximized() {
		return errors.New("pointer injection needs the window maximized (action maximize)")
	}
	if err := injectOK(C.inj_motion(-1e5, -1e5)); err != nil {
		return err
	}
	return injectOK(C.inj_motion(C.double(x), C.double(y)))
}

func (a *App) pointerButton(btn int, pressed bool) error {
	b, ok := evdevButton[btn]
	if !ok {
		return errors.New("unknown button " + strconv.Itoa(btn))
	}
	p := 0
	if pressed {
		p = 1
	}
	return injectOK(C.inj_button(b, C.int(p)))
}

// pointerDrag presses button 1 where the pointer is, moves by (dx, dy) in
// steps one frame apart, and releases.
func (a *App) pointerDrag(dx, dy float64, steps int) error {
	if err := a.pointerButton(1, true); err != nil {
		return err
	}
	steps = max(1, steps)
	i := 0
	coreglib.TimeoutAdd(16, func() bool {
		i++
		if err := injectOK(C.inj_motion(C.double(dx/float64(steps)), C.double(dy/float64(steps)))); err != nil {
			a.reportError("ptr-drag: %v", err)
			return false
		}
		if i < steps {
			return true
		}
		coreglib.TimeoutAdd(50, func() bool {
			if err := a.pointerButton(1, false); err != nil {
				a.reportError("ptr-drag: %v", err)
			}
			return false
		})
		return false
	})
	return nil
}

func (a *App) pointerScroll(steps int) error { return injectOK(C.inj_scroll(C.int(steps))) }

// walkWidgets calls f for w and its descendants, depth first.
func walkWidgets(w gtk.Widgetter, f func(*gtk.Widget)) {
	base := gtk.BaseWidget(w)
	f(base)
	for c := base.FirstChild(); c != nil; c = gtk.BaseWidget(c).NextSibling() {
		walkWidgets(c, f)
	}
}

// menuTarget is the centre of an options menu item in window coordinates:
// the popover is its own surface, placed relative to the window's.
func (a *App) menuTarget(label string) (float64, float64, error) {
	b, ok := a.menuButtons[label]
	if !ok {
		return 0, 0, errors.New("no menu item " + label)
	}
	pop := a.optionsBtn.Popover()
	var px, py C.int
	if !pop.IsVisible() || C.inj_popup_pos(C.uintptr_t(nativePtr(pop)), &px, &py) == 0 {
		return 0, 0, errors.New("the menu is not open")
	}
	bnd, ok := b.ComputeBounds(pop)
	if !ok {
		return 0, 0, errors.New("no bounds for menu item " + label)
	}
	psx, psy := pop.SurfaceTransform()
	wsx, wsy := a.win.SurfaceTransform()
	x := float64(px) + psx + float64(bnd.X()+bnd.Width()/2) - wsx
	y := float64(py) + psy + float64(bnd.Y()+bnd.Height()/2) - wsy
	return x, y, nil
}

// target finds a point to aim the pointer at, in window coordinates:
//
//	tab N | tabclose N | newtab | options | closewin
//	pane ID | cell ID COL ROW | divider PATH (r, ra, rb, rab, … from the selected tab's root)
func (a *App) target(args []string) (float64, float64, error) {
	num := func(i int) int {
		if i < len(args) {
			n, _ := strconv.Atoi(args[i])
			return n
		}
		return 0
	}
	centre := func(w gtk.Widgetter) (float64, float64, error) {
		x, y, wd, ht, ok := a.boundsIn(w)
		if !ok {
			return 0, 0, errors.New("no bounds for " + strings.Join(args, " "))
		}
		return x + wd/2, y + ht/2, nil
	}
	if len(args) == 0 {
		return 0, 0, errors.New("no target")
	}
	switch args[0] {
	case "tab", "tabclose":
		i := num(1) - 1
		if i < 0 || i >= len(a.tabItems) {
			return 0, 0, errors.New("no tab item " + strings.Join(args, " "))
		}
		if args[0] == "tab" {
			return centre(a.tabItems[i].item)
		}
		return centre(a.tabItems[i].close)
	case "newtab":
		return centre(a.newTabBtn)
	case "options":
		return centre(a.optionsBtn)
	case "closewin":
		return centre(a.closeWinBtn)
	case "pane", "cell":
		id := num(1)
		if id < 0 || id >= len(a.panes) || a.panes[id] == nil {
			return 0, 0, errors.New("no pane " + strings.Join(args, " "))
		}
		p := a.panes[id]
		if args[0] == "pane" {
			return centre(p.widget)
		}
		cx, cy := p.cellCenter(num(2), num(3))
		x, y, _, _, ok := a.boundsIn(p.widget)
		if !ok {
			return 0, 0, errors.New("pane not shown")
		}
		return x + cx, y + cy, nil
	case "shortcut-keys", "shortcut-switch":
		if a.shortcutsWin == nil || len(args) < 2 {
			return 0, 0, errors.New("the shortcuts window is not open")
		}
		r, ok := a.shortcutsWin.rows[args[1]]
		if !ok {
			return 0, 0, errors.New("no shortcut row " + args[1])
		}
		if args[0] == "shortcut-keys" {
			return centre(r.keys)
		}
		return centre(r.sw)
	case "button":
		want := strings.Join(args[1:], " ")
		var found gtk.Widgetter
		walkWidgets(a.win, func(w *gtk.Widget) {
			if b, ok := w.Cast().(*gtk.Button); ok && w.IsDrawable() && strings.ReplaceAll(b.Label(), "_", "") == want {
				found = b
			}
		})
		if found == nil {
			return 0, 0, errors.New("no button " + want)
		}
		return centre(found)
	case "divider":
		if a.active == nil || len(args) < 2 || !strings.HasPrefix(args[1], "r") {
			return 0, 0, errors.New("divider PATH: r, ra, rb, …")
		}
		n := a.active.root
		for _, c := range args[1][1:] {
			if n == nil || n.pane != nil {
				break
			}
			if c == 'a' {
				n = n.a
			} else {
				n = n.b
			}
		}
		if n == nil || n.pane != nil {
			return 0, 0, errors.New("no split at " + args[1])
		}
		x, y, w, h, ok := a.dividerRect(n)
		if !ok {
			return 0, 0, errors.New("divider not shown")
		}
		return x + w/2, y + h/2, nil
	}
	return 0, 0, errors.New("unknown target " + args[0])
}
