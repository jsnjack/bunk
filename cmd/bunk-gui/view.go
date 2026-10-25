package main

// TermView: a GtkWidget subclass (in C) that renders one terminal into its
// own GL texture at the exact device-pixel size of the widget and hands it
// to GSK as a GskTextureNode. GtkGLArea sizes its framebuffer with the
// integer scale factor (2 at 133%), so GSK has to shrink it; here the
// texture bounds are chosen so that one texel covers exactly one device
// pixel.
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
//
// Each view has its own GL context, render slots and statistics; Go finds
// its state by the view's id. A view that is not mapped (a hidden tab) gets
// no snapshots, so it costs nothing to render.

/*
#cgo pkg-config: gtk4
#cgo LDFLAGS: -lm
#include <gtk/gtk.h>
#include <math.h>
#include <stdint.h>
#include <stdio.h>

extern int goViewRender(int id, int dw, int dh, double scale);
extern void goViewRealized(int id);
extern void goViewUnrealize(int id);
extern void goViewUnmapped(int id);

#define TV_SLOTS 4

typedef struct _TermView TermView;

typedef struct { TermView* view; int slot; } TvToken;

struct _TermView {
	GtkWidget parent_instance;
	int id;
	GdkGLContext* ctx;
	GdkTexture* last;          // last appended texture (we hold one ref)
	graphene_rect_t bounds;    // its bounds in widget coords
	int dw, dh, x0, y0, lw, lh;
	double scale, ox, oy;
	gboolean need_render;
	int busy[TV_SLOTS];        // 1 while GTK may still read the slot's texture
	TvToken tokens[TV_SLOTS];
	gulong scale_handler;
	GdkSurface* surface;
	GdkRGBA bg;
	char err[256];
	int snapshots, renders, reused, wakes, scale_changes;
};

typedef struct { GtkWidgetClass parent_class; } TermViewClass;

G_DEFINE_TYPE(TermView, term_view, GTK_TYPE_WIDGET)

// The GdkTexture is gone: GSK no longer reads the slot.
static void tv_tex_release(gpointer data) {
	TvToken* t = data;
	t->view->busy[t->slot] = 0;
	g_object_unref(t->view);
}

static void tv_on_scale(GObject* o, GParamSpec* ps, gpointer w) {
	TermView* v = (TermView*)w;
	v->scale_changes++;
	v->need_render = TRUE;
	gtk_widget_queue_draw(GTK_WIDGET(w));
}

static void term_view_realize(GtkWidget* w) {
	TermView* v = (TermView*)w;
	GTK_WIDGET_CLASS(term_view_parent_class)->realize(w);
	GError* err = NULL;
	v->surface = gtk_native_get_surface(gtk_widget_get_native(w));
	v->ctx = gdk_surface_create_gl_context(v->surface, &err);
	if (v->ctx) {
		gdk_gl_context_set_allowed_apis(v->ctx, GDK_GL_API_GL);
		gdk_gl_context_set_required_version(v->ctx, 3, 3);
		if (!gdk_gl_context_realize(v->ctx, &err)) g_clear_object(&v->ctx);
	}
	if (err) {
		snprintf(v->err, sizeof v->err, "%s", err->message);
		g_error_free(err);
	}
	// A fractional scale change (e.g. 1.25 -> 1.5) keeps the integer scale
	// factor, so watch the surface's fractional "scale" property directly.
	v->scale_handler = g_signal_connect(v->surface, "notify::scale", G_CALLBACK(tv_on_scale), w);
	if (v->ctx) gdk_gl_context_make_current(v->ctx);
	goViewRealized(v->id);
}

static void term_view_unrealize(GtkWidget* w) {
	TermView* v = (TermView*)w;
	if (v->scale_handler) g_signal_handler_disconnect(v->surface, v->scale_handler);
	v->scale_handler = 0;
	g_clear_object(&v->last);
	if (v->ctx) {
		gdk_gl_context_make_current(v->ctx);
		goViewUnrealize(v->id);
		gdk_gl_context_clear_current();
	}
	g_clear_object(&v->ctx);
	GTK_WIDGET_CLASS(term_view_parent_class)->unrealize(w);
}

static void term_view_unmap(GtkWidget* w) {
	TermView* v = (TermView*)w;
	GTK_WIDGET_CLASS(term_view_parent_class)->unmap(w);
	// A hidden tab keeps no frame: drop the texture and free idle slots.
	g_clear_object(&v->last);
	v->need_render = TRUE;
	if (v->ctx) {
		gdk_gl_context_make_current(v->ctx);
		goViewUnmapped(v->id);
	}
}

static void term_view_snapshot(GtkWidget* w, GtkSnapshot* s) {
	TermView* v = (TermView*)w;
	int lw = gtk_widget_get_width(w), lh = gtk_widget_get_height(w);
	v->snapshots++;
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

	gtk_snapshot_append_color(s, &v->bg, &GRAPHENE_RECT_INIT(0, 0, lw, lh));
	if (dw <= 0 || dh <= 0 || !v->ctx) return;

	gboolean geometry = dw != v->dw || dh != v->dh || scale != v->scale || x0 != v->x0 || y0 != v->y0;
	v->dw = dw; v->dh = dh; v->x0 = x0; v->y0 = y0; v->lw = lw; v->lh = lh;
	v->scale = scale; v->ox = ox; v->oy = oy;
	v->bounds.origin.x = x0 / scale - ox;
	v->bounds.origin.y = y0 / scale - oy;
	if (v->need_render || geometry || !v->last) {
		v->need_render = FALSE;
		gdk_gl_context_make_current(v->ctx);
		if (goViewRender(v->id, dw, dh, scale)) v->renders++;
		else v->reused++;
	} else {
		v->reused++;
	}
	if (!v->last) return;
	// Size the node from the texture itself, so every texel maps to one
	// device pixel even if a frame was skipped after a resize.
	v->bounds.size.width = gdk_texture_get_width(v->last) / scale;
	v->bounds.size.height = gdk_texture_get_height(v->last) / scale;
	gtk_snapshot_append_texture(s, v->last, &v->bounds);
}

static void term_view_class_init(TermViewClass* klass) {
	GtkWidgetClass* wc = GTK_WIDGET_CLASS(klass);
	wc->realize = term_view_realize;
	wc->unrealize = term_view_unrealize;
	wc->unmap = term_view_unmap;
	wc->snapshot = term_view_snapshot;
}

static void term_view_init(TermView* v) {
	gtk_widget_set_hexpand(GTK_WIDGET(v), TRUE);
	gtk_widget_set_vexpand(GTK_WIDGET(v), TRUE);
	gtk_widget_set_focusable(GTK_WIDGET(v), TRUE);
	gtk_widget_set_cursor_from_name(GTK_WIDGET(v), "text");
	v->need_render = TRUE;
	v->bg = (GdkRGBA){0.11f, 0.11f, 0.12f, 1.0f};
	for (int i = 0; i < TV_SLOTS; i++) v->tokens[i] = (TvToken){v, i};
}

static GtkWidget* term_view_new(int id) {
	TermView* v = g_object_new(term_view_get_type(), NULL);
	v->id = id;
	return GTK_WIDGET(v);
}

static void tv_make_current(TermView* v) { if (v->ctx) gdk_gl_context_make_current(v->ctx); }
static int tv_slot_busy(TermView* v, int slot) { return v->busy[slot]; }
static void tv_set_bg(TermView* v, float r, float g, float b) { v->bg.red = r; v->bg.green = g; v->bg.blue = b; }
static int tv_has_ctx(TermView* v) { return v->ctx != NULL; }

static GdkTexture* tv_wrap_gl_texture(TermView* v, unsigned int tex, uintptr_t sync, int w, int h, GDestroyNotify release, gpointer data) {
	GdkGLTextureBuilder* b = gdk_gl_texture_builder_new();
	gdk_gl_texture_builder_set_context(b, v->ctx);
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
static void tv_publish(TermView* v, int slot, unsigned int tex, uintptr_t sync, int w, int h) {
	v->busy[slot] = 1;
	g_object_ref(v); // released with the texture
	GdkTexture* t = tv_wrap_gl_texture(v, tex, sync, w, h, tv_tex_release, &v->tokens[slot]);
	if (!t) {
		v->busy[slot] = 0;
		g_object_unref(v);
	}
	if (v->last) g_object_unref(v->last);
	v->last = t;
}

static gboolean tv_wake_cb(gpointer data) {
	TermView* v = data;
	v->wakes++;
	v->need_render = TRUE;
	gtk_widget_queue_draw(GTK_WIDGET(v));
	return G_SOURCE_REMOVE;
}

// Thread-safe: g_idle_add wakes the main loop, the callback queues a frame.
// The view is kept alive until the callback ran.
static void tv_wake(TermView* v) {
	g_idle_add_full(G_PRIORITY_DEFAULT, tv_wake_cb, g_object_ref(v), g_object_unref);
}

// Main thread only: render on the next frame.
static void tv_queue_render(TermView* v) {
	v->need_render = TRUE;
	gtk_widget_queue_draw(GTK_WIDGET(v));
}

// Tells the input method where the cursor is, in widget coordinates.
static void tv_set_im_cursor(uintptr_t im, int x, int y, int w, int h) {
	GdkRectangle r = {x, y, w, h};
	gtk_im_context_set_cursor_location((GtkIMContext*)im, &r);
}

typedef struct {
	int dw, dh, x0, y0, lw, lh, snapshots, renders, reused, wakes, scale_changes;
	double scale, ox, oy, bx, by;
} tv_geom;

static tv_geom tv_get_geom(TermView* v) {
	tv_geom g = {v->dw, v->dh, v->x0, v->y0, v->lw, v->lh, v->snapshots, v->renders, v->reused, v->wakes,
		v->scale_changes, v->scale, v->ox, v->oy, v->bounds.origin.x, v->bounds.origin.y};
	return g;
}

static const char* tv_error(TermView* v) { return v->err; }

static void tv_gl_version(TermView* v, int* major, int* minor) {
	if (v->ctx) gdk_gl_context_get_version(v->ctx, major, minor);
}

// ---- -dump and -verify helpers (never on the frame path) ----

// Downloads a texture as B8G8R8A8 premultiplied (GDK_MEMORY_DEFAULT), w*4 stride.
static guchar* tv_download(GdkTexture* t) {
	int w = gdk_texture_get_width(t), h = gdk_texture_get_height(t);
	guchar* data = g_malloc((gsize)w * h * 4);
	gdk_texture_download(t, data, (gsize)w * 4);
	return data;
}

static guchar* tv_download_last(TermView* v, int* w, int* h) {
	if (!v->last) return NULL;
	*w = gdk_texture_get_width(v->last);
	*h = gdk_texture_get_height(v->last);
	return tv_download(v->last);
}

// Renders tex the way the window's GSK renderer composites it on screen: a
// GskTextureNode at the view's bounds, offset by the widget origin and
// scaled by the surface scale, into a viewport covering exactly the widget's
// device pixels.
static guchar* tv_proxy_composite(TermView* v, GdkTexture* tex) {
	GskRenderer* r = gtk_native_get_renderer(gtk_widget_get_native(GTK_WIDGET(v)));
	graphene_rect_t b = GRAPHENE_RECT_INIT(v->bounds.origin.x + v->ox, v->bounds.origin.y + v->oy,
		gdk_texture_get_width(tex) / v->scale, gdk_texture_get_height(tex) / v->scale);
	GskRenderNode* n = gsk_texture_node_new(tex, &b);
	GskRenderNode* root = gsk_transform_node_new(n, gsk_transform_scale(NULL, v->scale, v->scale));
	graphene_rect_t vp = GRAPHENE_RECT_INIT(v->x0, v->y0, gdk_texture_get_width(tex), gdk_texture_get_height(tex));
	GdkTexture* out = gsk_renderer_render_texture(r, root, &vp);
	guchar* data = NULL;
	if (out && gdk_texture_get_width(out) == gdk_texture_get_width(tex)) data = tv_download(out);
	if (out) g_object_unref(out);
	gsk_render_node_unref(root);
	gsk_render_node_unref(n);
	return data;
}

static int tv_dummy;
static void tv_noop_release(gpointer data) {}

// Wraps a GL texture, downloads it and composites it through GSK.
static guchar* tv_proxy_gl(TermView* v, unsigned int tex, uintptr_t sync, int w, int h, guchar** texdata) {
	GdkTexture* t = tv_wrap_gl_texture(v, tex, sync, w, h, tv_noop_release, &tv_dummy);
	*texdata = tv_download(t);
	guchar* out = tv_proxy_composite(v, t);
	g_object_unref(t);
	return out;
}

static guchar* tv_proxy_last(TermView* v) { return v->last ? tv_proxy_composite(v, v->last) : NULL; }
*/
import "C"

import (
	"unsafe"
)

const viewSlots = int(C.TV_SLOTS)

// cview is a TermView* owned by GTK.
type cview = *C.TermView

type viewGeom struct {
	dw, dh, x0, y0, lw, lh                      int
	snapshots, renders, reused, wakes, scaleChg int
	scale, ox, oy, bx, by                       float64
}

func viewNewC(id int) (unsafe.Pointer, cview) {
	w := C.term_view_new(C.int(id))
	return unsafe.Pointer(w), (*C.TermView)(unsafe.Pointer(w))
}
func viewWakeC(v cview)        { C.tv_wake(v) }
func viewQueueRenderC(v cview) { C.tv_queue_render(v) }
func viewMakeCurrentC(v cview) { C.tv_make_current(v) }
func viewHasCtxC(v cview) bool { return C.tv_has_ctx(v) != 0 }
func viewSlotBusyC(v cview, slot int) bool {
	return C.tv_slot_busy(v, C.int(slot)) != 0
}
func viewSetBGC(v cview, rgb uint32) {
	C.tv_set_bg(v, C.float(float32(rgb>>16&0xff)/255), C.float(float32(rgb>>8&0xff)/255), C.float(float32(rgb&0xff)/255))
}
func viewSetIMCursorC(im uintptr, x, y, w, h int) {
	C.tv_set_im_cursor(C.uintptr_t(im), C.int(x), C.int(y), C.int(w), C.int(h))
}

func viewPublishC(v cview, slot int, tex uint32, sync uintptr, w, h int) {
	C.tv_publish(v, C.int(slot), C.uint(tex), C.uintptr_t(sync), C.int(w), C.int(h))
}

func viewGeomC(v cview) viewGeom {
	g := C.tv_get_geom(v)
	return viewGeom{
		dw: int(g.dw), dh: int(g.dh), x0: int(g.x0), y0: int(g.y0), lw: int(g.lw), lh: int(g.lh),
		snapshots: int(g.snapshots), renders: int(g.renders), reused: int(g.reused), wakes: int(g.wakes),
		scaleChg: int(g.scale_changes),
		scale:    float64(g.scale), ox: float64(g.ox), oy: float64(g.oy), bx: float64(g.bx), by: float64(g.by),
	}
}

func viewErrorC(v cview) string { return C.GoString(C.tv_error(v)) }

func viewGLVersionC(v cview) (int, int) {
	var major, minor C.int
	C.tv_gl_version(v, &major, &minor)
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

// downloadLastC returns the view's last texture (BGRA premultiplied).
func downloadLastC(v cview) (pix []byte, w, h int) {
	var cw, ch C.int
	p := C.tv_download_last(v, &cw, &ch)
	w, h = int(cw), int(ch)
	return takeC(p, w*h*4), w, h
}

// proxyLastC composites the last texture through the window's GSK renderer.
func proxyLastC(v cview, w, h int) []byte {
	return takeC(C.tv_proxy_last(v), w*h*4)
}

// proxyGLC wraps a GL texture, downloads it and composites it.
// Returns (composited pixels, texture pixels).
func proxyGLC(v cview, tex uint32, sync uintptr, w, h int) ([]byte, []byte) {
	var texdata *C.guchar
	out := C.tv_proxy_gl(v, C.uint(tex), C.uintptr_t(sync), C.int(w), C.int(h), &texdata)
	return takeC(out, w*h*4), takeC(texdata, w*h*4)
}
