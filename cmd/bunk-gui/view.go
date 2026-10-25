package main

// TermView: a GtkWidget subclass (in C) that renders the terminal into its
// own GL texture at the exact device-pixel size of the widget and hands it to
// GSK as a GskTextureNode. GtkGLArea sizes its framebuffer with the integer
// scale factor (2 at 133%), so GSK has to shrink it; here the texture bounds
// are chosen so that one texel covers exactly one device pixel.
//
// Geometry, per snapshot:
//
//	origin  = widget origin in surface logical coords (compute_point + surface transform)
//	x0, y0  = ceil(origin * scale)               first whole device pixel inside the widget
//	x1, y1  = floor((origin + size) * scale)     end of the last whole device pixel
//	texture = (x1-x0) x (y1-y0) texels
//	bounds  = (x0/scale - origin, y0/scale - origin, w/scale, h/scale) in widget coords
//
// so bounds*scale + origin*scale lands exactly on device pixel edges.
// Sub-pixel slivers at the edges are covered by a colour node.
//
// Node type matters. A GskTextureScaleNode (gtk_snapshot_append_scaled_texture)
// is first resampled to bounds-size pixels at scale 1 in an offscreen, then
// drawn at 1.333: two resamples. A plain GskTextureNode is drawn straight into
// the window's pass; with a 1:1 texel:pixel mapping its linear filter samples
// texel centres (verified pixel-identical with -verify).

/*
#cgo pkg-config: gtk4
#cgo LDFLAGS: -lm
#include <gtk/gtk.h>
#include <math.h>
#include <stdint.h>
#include <stdio.h>

extern int goViewRender(int dw, int dh, double scale);
extern void goViewRealized(void);

#define TV_SLOTS 4

typedef struct { GtkWidget parent_instance; } TermView;
typedef struct { GtkWidgetClass parent_class; } TermViewClass;

G_DEFINE_TYPE(TermView, term_view, GTK_TYPE_WIDGET)

// One view per process in milestone 1: state lives in statics.
static GtkWidget* tv_widget;
static GdkGLContext* tv_ctx;
static GdkTexture* tv_last;          // last appended texture (we hold one ref)
static graphene_rect_t tv_bounds;    // its bounds in widget coords
static int tv_dw, tv_dh, tv_x0, tv_y0, tv_lw, tv_lh;
static double tv_scale, tv_ox, tv_oy;
static gboolean tv_need_render = TRUE;
static int tv_busy[TV_SLOTS];        // 1 while GTK may still read the slot's texture
static gulong tv_scale_handler;
static GdkSurface* tv_surface;
static GdkRGBA tv_bg = {0.11f, 0.11f, 0.12f, 1.0f};
static char tv_err[512];
static int tv_snapshots, tv_renders, tv_reused, tv_wakes, tv_scale_changes;

// data is slot+1: GDK rejects a destroy notify with NULL data.
static void tv_tex_release(gpointer data) { tv_busy[GPOINTER_TO_INT(data) - 1] = 0; }

static void tv_on_scale(GObject* o, GParamSpec* ps, gpointer w) {
	tv_scale_changes++;
	tv_need_render = TRUE;
	gtk_widget_queue_draw(GTK_WIDGET(w));
}

static void term_view_realize(GtkWidget* w) {
	GTK_WIDGET_CLASS(term_view_parent_class)->realize(w);
	GError* err = NULL;
	tv_surface = gtk_native_get_surface(gtk_widget_get_native(w));
	tv_ctx = gdk_surface_create_gl_context(tv_surface, &err);
	if (tv_ctx) {
		gdk_gl_context_set_allowed_apis(tv_ctx, GDK_GL_API_GL);
		gdk_gl_context_set_required_version(tv_ctx, 3, 3);
		if (!gdk_gl_context_realize(tv_ctx, &err)) g_clear_object(&tv_ctx);
	}
	if (err) {
		snprintf(tv_err, sizeof tv_err, "%s", err->message);
		g_error_free(err);
	}
	// A fractional scale change (e.g. 1.25 -> 1.5) keeps the integer scale
	// factor, so watch the surface's fractional "scale" property directly.
	tv_scale_handler = g_signal_connect(tv_surface, "notify::scale", G_CALLBACK(tv_on_scale), w);
	if (tv_ctx) gdk_gl_context_make_current(tv_ctx);
	goViewRealized();
}

static void term_view_unrealize(GtkWidget* w) {
	if (tv_scale_handler) g_signal_handler_disconnect(tv_surface, tv_scale_handler);
	tv_scale_handler = 0;
	g_clear_object(&tv_last);
	g_clear_object(&tv_ctx);
	GTK_WIDGET_CLASS(term_view_parent_class)->unrealize(w);
}

static void term_view_snapshot(GtkWidget* w, GtkSnapshot* s) {
	int lw = gtk_widget_get_width(w), lh = gtk_widget_get_height(w);
	tv_snapshots++;
	if (lw <= 0 || lh <= 0) return;
	GtkNative* native = gtk_widget_get_native(w);
	double scale = gdk_surface_get_scale(gtk_native_get_surface(native));
	graphene_point_t p = GRAPHENE_POINT_INIT(0, 0), q;
	if (!gtk_widget_compute_point(w, GTK_WIDGET(native), &p, &q)) q = p;
	double tx = 0, ty = 0;
	gtk_native_get_surface_transform(native, &tx, &ty);
	double ox = q.x + tx, oy = q.y + ty;
	int x0 = (int)ceil(ox * scale - 1e-6), y0 = (int)ceil(oy * scale - 1e-6);
	int x1 = (int)floor((ox + lw) * scale + 1e-6), y1 = (int)floor((oy + lh) * scale + 1e-6);
	int dw = x1 - x0, dh = y1 - y0;

	gtk_snapshot_append_color(s, &tv_bg, &GRAPHENE_RECT_INIT(0, 0, lw, lh));
	if (dw <= 0 || dh <= 0 || !tv_ctx) return;

	gboolean geometry = dw != tv_dw || dh != tv_dh || scale != tv_scale || x0 != tv_x0 || y0 != tv_y0;
	if (tv_need_render || geometry || !tv_last) {
		tv_need_render = FALSE;
		tv_dw = dw; tv_dh = dh; tv_x0 = x0; tv_y0 = y0; tv_lw = lw; tv_lh = lh;
		tv_scale = scale; tv_ox = ox; tv_oy = oy;
		gdk_gl_context_make_current(tv_ctx);
		if (goViewRender(dw, dh, scale)) tv_renders++;
		else tv_reused++;
	} else {
		tv_reused++;
	}
	if (!tv_last) return;
	// Size the node from the texture itself, so every texel maps to one
	// device pixel even if a frame was skipped after a resize.
	int tw = gdk_texture_get_width(tv_last), th = gdk_texture_get_height(tv_last);
	tv_bounds = GRAPHENE_RECT_INIT(x0 / scale - ox, y0 / scale - oy, tw / scale, th / scale);
	gtk_snapshot_append_texture(s, tv_last, &tv_bounds);
}

static void term_view_class_init(TermViewClass* klass) {
	GtkWidgetClass* wc = GTK_WIDGET_CLASS(klass);
	wc->realize = term_view_realize;
	wc->unrealize = term_view_unrealize;
	wc->snapshot = term_view_snapshot;
}

static void term_view_init(TermView* v) {
	gtk_widget_set_hexpand(GTK_WIDGET(v), TRUE);
	gtk_widget_set_vexpand(GTK_WIDGET(v), TRUE);
	gtk_widget_set_focusable(GTK_WIDGET(v), TRUE);
}

static GtkWidget* term_view_new(void) {
	tv_widget = g_object_new(term_view_get_type(), NULL);
	return tv_widget;
}

static void tv_make_current(void) { if (tv_ctx) gdk_gl_context_make_current(tv_ctx); }
static int tv_slot_busy(int slot) { return tv_busy[slot]; }
static void tv_set_bg(float r, float g, float b) { tv_bg.red = r; tv_bg.green = g; tv_bg.blue = b; }

static GdkTexture* tv_wrap_gl_texture(unsigned int tex, uintptr_t sync, int w, int h, GDestroyNotify release, gpointer data) {
	GdkGLTextureBuilder* b = gdk_gl_texture_builder_new();
	gdk_gl_texture_builder_set_context(b, tv_ctx);
	gdk_gl_texture_builder_set_id(b, tex);
	gdk_gl_texture_builder_set_width(b, w);
	gdk_gl_texture_builder_set_height(b, h);
	gdk_gl_texture_builder_set_format(b, GDK_MEMORY_R8G8B8A8_PREMULTIPLIED);
	if (sync) gdk_gl_texture_builder_set_sync(b, (gpointer)sync);
	GdkTexture* t = gdk_gl_texture_builder_build(b, release, data);
	g_object_unref(b);
	return t;
}

// Called by Go (inside goViewRender) once the slot's texture is drawn and fenced.
static void tv_publish(int slot, unsigned int tex, uintptr_t sync, int w, int h) {
	tv_busy[slot] = 1;
	GdkTexture* t = tv_wrap_gl_texture(tex, sync, w, h, tv_tex_release, GINT_TO_POINTER(slot + 1));
	if (!t) tv_busy[slot] = 0;
	if (tv_last) g_object_unref(tv_last);
	tv_last = t;
}

static gboolean tv_wake_cb(gpointer data) {
	tv_wakes++;
	tv_need_render = TRUE;
	gtk_widget_queue_draw(tv_widget);
	return G_SOURCE_REMOVE;
}

// Thread-safe: g_idle_add wakes the main loop, the callback queues a frame.
static void tv_wake(void) { g_idle_add_full(G_PRIORITY_DEFAULT, tv_wake_cb, NULL, NULL); }

typedef struct {
	int dw, dh, x0, y0, lw, lh, snapshots, renders, reused, wakes, scale_changes;
	double scale, ox, oy, bx, by, bw, bh;
} tv_geom;

static tv_geom tv_get_geom(void) {
	tv_geom g = {tv_dw, tv_dh, tv_x0, tv_y0, tv_lw, tv_lh, tv_snapshots, tv_renders, tv_reused, tv_wakes, tv_scale_changes,
		tv_scale, tv_ox, tv_oy, tv_bounds.origin.x, tv_bounds.origin.y, tv_bounds.size.width, tv_bounds.size.height};
	return g;
}

static const char* tv_error(void) { return tv_err; }

static void tv_gl_version(int* major, int* minor) {
	if (tv_ctx) gdk_gl_context_get_version(tv_ctx, major, minor);
}

// ---- -dump and -verify helpers (never on the frame path) ----

// Downloads a texture as B8G8R8A8 premultiplied (GDK_MEMORY_DEFAULT), w*4 stride.
static guchar* tv_download(GdkTexture* t) {
	int w = gdk_texture_get_width(t), h = gdk_texture_get_height(t);
	guchar* data = g_malloc((gsize)w * h * 4);
	gdk_texture_download(t, data, (gsize)w * 4);
	return data;
}

static guchar* tv_download_last(int* w, int* h) {
	if (!tv_last) return NULL;
	*w = gdk_texture_get_width(tv_last);
	*h = gdk_texture_get_height(tv_last);
	return tv_download(tv_last);
}

// Renders tex the way the window's GSK renderer composites it on screen: a
// GskTextureNode at `bounds` (widget coords), offset by the widget origin and
// scaled by the surface scale, into a viewport covering exactly the widget's
// device pixels.
static guchar* tv_proxy_composite(GdkTexture* tex) {
	GskRenderer* r = gtk_native_get_renderer(gtk_widget_get_native(tv_widget));
	graphene_rect_t b = GRAPHENE_RECT_INIT(tv_bounds.origin.x + tv_ox, tv_bounds.origin.y + tv_oy,
		tv_bounds.size.width, tv_bounds.size.height);
	GskRenderNode* n = gsk_texture_node_new(tex, &b);
	GskRenderNode* root = gsk_transform_node_new(n, gsk_transform_scale(NULL, tv_scale, tv_scale));
	graphene_rect_t vp = GRAPHENE_RECT_INIT(tv_x0, tv_y0, tv_dw, tv_dh);
	GdkTexture* out = gsk_renderer_render_texture(r, root, &vp);
	guchar* data = NULL;
	if (out && gdk_texture_get_width(out) == tv_dw && gdk_texture_get_height(out) == tv_dh) data = tv_download(out);
	if (out) g_object_unref(out);
	gsk_render_node_unref(root);
	gsk_render_node_unref(n);
	return data;
}

static int tv_dummy;
static void tv_noop_release(gpointer data) {}

// Wraps a GL texture, downloads it and composites it through GSK.
static guchar* tv_proxy_gl(unsigned int tex, uintptr_t sync, int w, int h, guchar** texdata) {
	GdkTexture* t = tv_wrap_gl_texture(tex, sync, w, h, tv_noop_release, &tv_dummy);
	*texdata = tv_download(t);
	guchar* out = tv_proxy_composite(t);
	g_object_unref(t);
	return out;
}

static guchar* tv_proxy_last(void) { return tv_last ? tv_proxy_composite(tv_last) : NULL; }
*/
import "C"

import (
	"unsafe"
)

const viewSlots = int(C.TV_SLOTS)

type viewGeom struct {
	dw, dh, x0, y0, lw, lh                      int
	snapshots, renders, reused, wakes, scaleChg int
	scale, ox, oy, bx, by, bw, bh               float64
}

func viewNewC() unsafe.Pointer { return unsafe.Pointer(C.term_view_new()) }
func viewWakeC()               { C.tv_wake() }
func viewMakeCurrentC()        { C.tv_make_current() }
func viewSlotBusyC(slot int) bool {
	return C.tv_slot_busy(C.int(slot)) != 0
}
func viewSetBGC(rgb uint32) {
	C.tv_set_bg(C.float(float32(rgb>>16&0xff)/255), C.float(float32(rgb>>8&0xff)/255), C.float(float32(rgb&0xff)/255))
}

func viewPublishC(slot int, tex uint32, sync uintptr, w, h int) {
	C.tv_publish(C.int(slot), C.uint(tex), C.uintptr_t(sync), C.int(w), C.int(h))
}

func viewGeomC() viewGeom {
	g := C.tv_get_geom()
	return viewGeom{
		dw: int(g.dw), dh: int(g.dh), x0: int(g.x0), y0: int(g.y0), lw: int(g.lw), lh: int(g.lh),
		snapshots: int(g.snapshots), renders: int(g.renders), reused: int(g.reused), wakes: int(g.wakes),
		scaleChg: int(g.scale_changes),
		scale:    float64(g.scale), ox: float64(g.ox), oy: float64(g.oy),
		bx: float64(g.bx), by: float64(g.by), bw: float64(g.bw), bh: float64(g.bh),
	}
}

func viewErrorC() string { return C.GoString(C.tv_error()) }

func viewGLVersionC() (int, int) {
	var major, minor C.int
	C.tv_gl_version(&major, &minor)
	return int(major), int(minor)
}

// takeC copies a g_malloc'd buffer into Go memory and frees it.
func takeC(p *C.guchar, n int) []byte {
	if p == nil {
		return nil
	}
	b := C.GoBytes(unsafe.Pointer(p), C.int(n))
	C.g_free(C.gpointer(p))
	return b
}

// downloadLastC returns the last appended texture (BGRA premultiplied).
func downloadLastC() (pix []byte, w, h int) {
	var cw, ch C.int
	p := C.tv_download_last(&cw, &ch)
	w, h = int(cw), int(ch)
	return takeC(p, w*h*4), w, h
}

// proxyLastC composites the last texture through the window's GSK renderer.
func proxyLastC() []byte {
	g := viewGeomC()
	return takeC(C.tv_proxy_last(), g.dw*g.dh*4)
}

// proxyGLC wraps a device-sized GL texture, downloads it and composites it.
// Returns (composited pixels, texture pixels).
func proxyGLC(tex uint32, sync uintptr, w, h int) ([]byte, []byte) {
	g := viewGeomC()
	var texdata *C.guchar
	out := C.tv_proxy_gl(C.uint(tex), C.uintptr_t(sync), C.int(w), C.int(h), &texdata)
	return takeC(out, g.dw*g.dh*4), takeC(texdata, w*h*4)
}
