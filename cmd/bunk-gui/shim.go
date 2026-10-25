package main

// Hot-path GTK glue written in C so per-frame signals do not go through
// gotk4's generic closure marshalling (which wraps every GObject argument
// into a new Go object). Setup still uses gotk4.

/*
#cgo pkg-config: gtk4 pangocairo
#include <gtk/gtk.h>
#include <pango/pangocairo.h>
#include <stdint.h>

// gotk4 v0.4.1 pangocairo.ContextSetFontOptions passes a bogus pointer for
// its hand-written cairo.FontOptions (SIGSEGV), so set the options here.
static void bg_set_font_options(uintptr_t pango_ctx, int antialias, int hint_style, int hint_metrics) {
	cairo_font_options_t* o = cairo_font_options_create();
	cairo_font_options_set_antialias(o, (cairo_antialias_t)antialias);
	cairo_font_options_set_hint_style(o, (cairo_hint_style_t)hint_style);
	cairo_font_options_set_hint_metrics(o, (cairo_hint_metrics_t)hint_metrics);
	pango_cairo_context_set_font_options((PangoContext*)pango_ctx, o);
	cairo_font_options_destroy(o);
}

extern void goBeforePaint(void);
extern void goAfterPaint(void);

static void bg_on_before_paint(GdkFrameClock* c, gpointer d) { goBeforePaint(); }
static void bg_on_after_paint(GdkFrameClock* c, gpointer d) { goAfterPaint(); }

static void bg_connect_clock(uintptr_t clock) {
	g_signal_connect((GdkFrameClock*)clock, "before-paint", G_CALLBACK(bg_on_before_paint), NULL);
	g_signal_connect((GdkFrameClock*)clock, "after-paint", G_CALLBACK(bg_on_after_paint), NULL);
}

// rc is -1 if the frame left the history, 0 if not complete yet, 1 if done.
// Returned by value: passing Go pointers to C would make them escape to the
// heap (one allocation each per call).
typedef struct { int rc; int64_t present, refresh, frame_time; } bg_timing;

static bg_timing bg_timings(uintptr_t clock, int64_t counter) {
	bg_timing out = {0};
	GdkFrameTimings* t = gdk_frame_clock_get_timings((GdkFrameClock*)clock, counter);
	if (!t) { out.rc = -1; return out; }
	if (!gdk_frame_timings_get_complete(t)) return out;
	out.rc = 1;
	out.present = gdk_frame_timings_get_presentation_time(t);
	out.refresh = gdk_frame_timings_get_refresh_interval(t);
	out.frame_time = gdk_frame_timings_get_frame_time(t);
	return out;
}

static int64_t bg_frame_counter(uintptr_t clock) {
	return gdk_frame_clock_get_frame_counter((GdkFrameClock*)clock);
}

static int64_t bg_monotonic(void) { return g_get_monotonic_time(); }

// Feeds one key to an input method context the way GTK's key controller
// does (gtk_im_context_filter_key), for -do tests: no GdkEvent is needed.
// Returns -1 when the current layout has no key for keyval.
static int bg_im_feed(uintptr_t im, uintptr_t widget, guint keyval, guint state, int press) {
	GtkWidget* w = (GtkWidget*)widget;
	GdkDisplay* d = gtk_widget_get_display(w);
	GdkKeymapKey* keys = NULL;
	int n = 0;
	if (!gdk_display_map_keyval(d, keyval, &keys, &n) || n == 0) return -1;
	guint keycode = keys[0].keycode;
	int group = keys[0].group, level = keys[0].level;
	g_free(keys);
	GdkDevice* kbd = gdk_seat_get_keyboard(gdk_display_get_default_seat(d));
	GdkSurface* surf = gtk_native_get_surface(gtk_widget_get_native(w));
	if (level == 1) state |= GDK_SHIFT_MASK;
	return gtk_im_context_filter_key((GtkIMContext*)im, press, surf, kbd, GDK_CURRENT_TIME, keycode, (GdkModifierType)state, group);
}
*/
import "C"

import (
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
)

func nativePtr(obj coreglib.Objector) uintptr { return coreglib.BaseObject(obj).Native() }

func connectClockC(clock coreglib.Objector) { C.bg_connect_clock(C.uintptr_t(nativePtr(clock))) }

type frameTimings struct {
	present, refresh, frameTime int64 // µs, g_get_monotonic_time base
}

func clockTimings(clock uintptr, counter int64, out *frameTimings) int {
	t := C.bg_timings(C.uintptr_t(clock), C.int64_t(counter))
	out.present, out.refresh, out.frameTime = int64(t.present), int64(t.refresh), int64(t.frame_time)
	return int(t.rc)
}

func clockCounter(clock uintptr) int64 { return int64(C.bg_frame_counter(C.uintptr_t(clock))) }
func glibMonotonicUs() int64           { return int64(C.bg_monotonic()) }

// imFeedC feeds a key press and release to an input method context.
func imFeedC(im, widget uintptr, keyval uint, state uint) (int, int) {
	down := int(C.bg_im_feed(C.uintptr_t(im), C.uintptr_t(widget), C.guint(keyval), C.guint(state), 1))
	up := int(C.bg_im_feed(C.uintptr_t(im), C.uintptr_t(widget), C.guint(keyval), C.guint(state), 0))
	return down, up
}

func setFontOptionsC(pangoCtx coreglib.Objector, antialias, hintStyle, hintMetrics int) {
	C.bg_set_font_options(C.uintptr_t(nativePtr(pangoCtx)), C.int(antialias), C.int(hintStyle), C.int(hintMetrics))
}
