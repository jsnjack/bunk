package main

/*
#cgo pkg-config: webkitgtk-6.0 gtk4
#include <webkit/webkit.h>
#include <stdint.h>
#include <stdlib.h>

extern void goPresentScheme(uintptr_t req, int view, char* uri);
extern int goPresentNavigate(int view, char* uri, int user, int window);
extern void goPresentLoad(int view, int event, char* uri);
extern void goPresentFailed(int view, char* uri, char* err);
extern void goPresentAsk(int view);
extern void goPresentEval(int view, int call, char* value, char* err);
extern void goPresentCrashed(int view);
extern void goPresentFindCount(int view, int n);
extern void goPresentReply(int view, char* json);
extern void goPresentInput(int view);

// One web context, network session and settings serve every document
// view: the session is ephemeral, so nothing a page stores outlives the
// process, and the settings turn off what documents never need.
static WebKitWebContext* bp_ctx;
static WebKitNetworkSession* bp_session;
static WebKitSettings* bp_settings;

static int bp_id(gpointer obj) {
	return GPOINTER_TO_INT(g_object_get_data(G_OBJECT(obj), "bunk-view")) - 1;
}

// bp_scheme_cb hands a request to Go, which may answer it later, from the
// main loop: it holds a reference until bp_scheme_finish or bp_scheme_fail.
static void bp_scheme_cb(WebKitURISchemeRequest* req, gpointer data) {
	WebKitWebView* v = webkit_uri_scheme_request_get_web_view(req);
	g_object_ref(req);
	goPresentScheme((uintptr_t)req, v ? bp_id(v) : -1, (char*)webkit_uri_scheme_request_get_uri(req));
}

static void bp_init(void) {
	if (bp_ctx) return;
	bp_session = webkit_network_session_new_ephemeral();
	bp_ctx = webkit_web_context_new();
	webkit_web_context_register_uri_scheme(bp_ctx, "bunk-present", bp_scheme_cb, NULL, NULL);
	WebKitSecurityManager* sm = webkit_web_context_get_security_manager(bp_ctx);
	webkit_security_manager_register_uri_scheme_as_secure(sm, "bunk-present");
	webkit_security_manager_register_uri_scheme_as_cors_enabled(sm, "bunk-present");
	bp_settings = webkit_settings_new();
	webkit_settings_set_enable_developer_extras(bp_settings, FALSE);
	webkit_settings_set_javascript_can_open_windows_automatically(bp_settings, FALSE);
	webkit_settings_set_javascript_can_access_clipboard(bp_settings, FALSE);
	webkit_settings_set_enable_media_stream(bp_settings, FALSE);
	webkit_settings_set_enable_webrtc(bp_settings, FALSE);
	webkit_settings_set_enable_write_console_messages_to_stdout(bp_settings, FALSE);
}

// bp_scheme_finish answers a bunk-present request with data, copied, and
// drops the request.
static void bp_scheme_finish(uintptr_t r, const char* data, gsize len, const char* ctype, const char* csp, int cors) {
	WebKitURISchemeRequest* req = (WebKitURISchemeRequest*)r;
	GBytes* b = g_bytes_new(data, len);
	GInputStream* s = g_memory_input_stream_new_from_bytes(b);
	WebKitURISchemeResponse* resp = webkit_uri_scheme_response_new(s, (gint64)len);
	webkit_uri_scheme_response_set_content_type(resp, ctype);
	SoupMessageHeaders* h = soup_message_headers_new(SOUP_MESSAGE_HEADERS_RESPONSE);
	soup_message_headers_append(h, "Cache-Control", "no-store");
	soup_message_headers_append(h, "Content-Type", ctype);
	if (csp && *csp) soup_message_headers_append(h, "Content-Security-Policy", csp);
	if (cors) soup_message_headers_append(h, "Access-Control-Allow-Origin", "*");
	webkit_uri_scheme_response_set_http_headers(resp, h);
	webkit_uri_scheme_response_set_status(resp, 200, NULL);
	webkit_uri_scheme_request_finish_with_response(req, resp);
	g_object_unref(resp);
	g_object_unref(s);
	g_bytes_unref(b);
	g_object_unref(req);
}

static void bp_scheme_fail(uintptr_t r, const char* msg) {
	GError* e = g_error_new_literal(g_quark_from_static_string("bunk-present"), 404, msg);
	webkit_uri_scheme_request_finish_error((WebKitURISchemeRequest*)r, e);
	g_error_free(e);
	g_object_unref((WebKitURISchemeRequest*)r);
}

// A response that WebKit cannot show would otherwise become a download.
static const char bp_unsupported[] = "WebKit cannot show this kind of file";

static gboolean bp_decide(WebKitWebView* v, WebKitPolicyDecision* d, WebKitPolicyDecisionType t, gpointer u) {
	if (t == WEBKIT_POLICY_DECISION_TYPE_RESPONSE) {
		WebKitResponsePolicyDecision* r = WEBKIT_RESPONSE_POLICY_DECISION(d);
		if (webkit_response_policy_decision_is_mime_type_supported(r)) return FALSE;
		const char* uri = webkit_uri_request_get_uri(webkit_response_policy_decision_get_request(r));
		goPresentFailed(bp_id(v), (char*)(uri ? uri : ""), (char*)bp_unsupported);
		webkit_policy_decision_ignore(d);
		return TRUE;
	}
	WebKitNavigationAction* a = webkit_navigation_policy_decision_get_navigation_action(WEBKIT_NAVIGATION_POLICY_DECISION(d));
	WebKitURIRequest* r = webkit_navigation_action_get_request(a);
	const char* uri = webkit_uri_request_get_uri(r);
	int window = t == WEBKIT_POLICY_DECISION_TYPE_NEW_WINDOW_ACTION;
	if (goPresentNavigate(bp_id(v), (char*)(uri ? uri : ""), webkit_navigation_action_is_user_gesture(a), window))
		webkit_policy_decision_use(d);
	else
		webkit_policy_decision_ignore(d);
	return TRUE;
}

static void bp_load(WebKitWebView* v, WebKitLoadEvent ev, gpointer u) {
	const char* uri = webkit_web_view_get_uri(v);
	goPresentLoad(bp_id(v), (int)ev, (char*)(uri ? uri : ""));
}

static gboolean bp_failed(WebKitWebView* v, WebKitLoadEvent ev, char* uri, GError* err, gpointer u) {
	// bp_decide said why it stopped the load.
	if (err && g_error_matches(err, WEBKIT_POLICY_ERROR, WEBKIT_POLICY_ERROR_FRAME_LOAD_INTERRUPTED_BY_POLICY_CHANGE)) return FALSE;
	goPresentFailed(bp_id(v), uri, err ? err->message : "");
	return FALSE;
}

static gboolean bp_deny(WebKitWebView* v, WebKitPermissionRequest* req, gpointer u) {
	webkit_permission_request_deny(req);
	return TRUE;
}

static void bp_crashed(WebKitWebView* v, WebKitWebProcessTerminationReason reason, gpointer u) { goPresentCrashed(bp_id(v)); }

// bp_input tells the panel that the user clicked or typed in the page
// (pageReply). A legacy controller in the capture phase sees each event
// before WebKit and lets it through; it is written in C because gotk4
// v0.4.1 cannot pass a GdkEvent to Go.
static gboolean bp_input(GtkEventControllerLegacy* c, GdkEvent* ev, gpointer data) {
	switch (gdk_event_get_event_type(ev)) {
	case GDK_BUTTON_PRESS:
	case GDK_BUTTON_RELEASE:
	case GDK_KEY_PRESS:
	case GDK_TOUCH_BEGIN:
		goPresentInput(GPOINTER_TO_INT(data) - 1);
		break;
	default:
		break;
	}
	return FALSE;
}

// bp_reply passes what a page posted to window.webkit.messageHandlers.bunkReply.
static void bp_reply(WebKitUserContentManager* m, JSCValue* v, gpointer data) {
	if (!jsc_value_is_string(v)) return;
	char* s = jsc_value_to_string(v);
	goPresentReply(GPOINTER_TO_INT(data) - 1, s);
	g_free(s);
}

static void bp_find_counted(WebKitFindController* fc, guint n, gpointer u) {
	goPresentFindCount(bp_id(webkit_find_controller_get_web_view(fc)), (int)n);
}

static void bp_ask(GSimpleAction* act, GVariant* param, gpointer data) { goPresentAsk(GPOINTER_TO_INT(data) - 1); }

// A right-click on selected text offers to ask the agent about it.
static gboolean bp_menu(WebKitWebView* v, WebKitContextMenu* m, WebKitHitTestResult* h, gpointer u) {
	if (!webkit_hit_test_result_context_is_selection(h)) return FALSE;
	GAction* act = G_ACTION(g_object_get_data(G_OBJECT(v), "bunk-ask"));
	webkit_context_menu_append(m, webkit_context_menu_item_new_separator());
	webkit_context_menu_append(m, webkit_context_menu_item_new_from_gaction(act, "Ask the agent about this…", NULL));
	return FALSE;
}

static GtkWidget* bp_view_new(int id) {
	bp_init();
	WebKitWebView* v = g_object_new(WEBKIT_TYPE_WEB_VIEW,
		"web-context", bp_ctx, "network-session", bp_session, "settings", bp_settings, NULL);
	g_object_set_data(G_OBJECT(v), "bunk-view", GINT_TO_POINTER(id + 1));
	GSimpleAction* act = g_simple_action_new("bunk-ask", NULL);
	g_signal_connect(act, "activate", G_CALLBACK(bp_ask), GINT_TO_POINTER(id + 1));
	g_object_set_data_full(G_OBJECT(v), "bunk-ask", act, g_object_unref);
	g_signal_connect(v, "decide-policy", G_CALLBACK(bp_decide), NULL);
	g_signal_connect(v, "load-changed", G_CALLBACK(bp_load), NULL);
	g_signal_connect(v, "load-failed", G_CALLBACK(bp_failed), NULL);
	g_signal_connect(v, "permission-request", G_CALLBACK(bp_deny), NULL);
	g_signal_connect(v, "context-menu", G_CALLBACK(bp_menu), NULL);
	g_signal_connect(v, "web-process-terminated", G_CALLBACK(bp_crashed), NULL);
	g_signal_connect(webkit_web_view_get_find_controller(v), "counted-matches", G_CALLBACK(bp_find_counted), NULL);
	GtkEventController* input = gtk_event_controller_legacy_new();
	gtk_event_controller_set_propagation_phase(input, GTK_PHASE_CAPTURE);
	g_signal_connect(input, "event", G_CALLBACK(bp_input), GINT_TO_POINTER(id + 1));
	gtk_widget_add_controller(GTK_WIDGET(v), input);
	WebKitUserContentManager* ucm = webkit_web_view_get_user_content_manager(v);
	webkit_user_content_manager_register_script_message_handler(ucm, "bunkReply", NULL);
	g_signal_connect(ucm, "script-message-received::bunkReply", G_CALLBACK(bp_reply), GINT_TO_POINTER(id + 1));
	return GTK_WIDGET(v);
}

// bp_view_close ends the view's web process, so a closed tab's page stops
// running at once. The view's signals name no view after it.
static void bp_view_close(uintptr_t v) {
	WebKitWebView* w = WEBKIT_WEB_VIEW((gpointer)v);
	g_object_set_data(G_OBJECT(w), "bunk-view", NULL);
	webkit_web_view_terminate_web_process(w);
}

// bp_view_kill ends the web process as a crash would, for window tests.
static void bp_view_kill(uintptr_t v) { webkit_web_view_terminate_web_process(WEBKIT_WEB_VIEW((gpointer)v)); }

static void bp_load_uri(uintptr_t v, const char* uri) { webkit_web_view_load_uri(WEBKIT_WEB_VIEW((gpointer)v), uri); }

// bp_find finds text in the page, case-insensitively and wrapping
// around: op 0 starts a search for text (an empty one ends it), 1 and 2
// go to the next and the previous match, 3 ends the search. A search
// counts its matches too (counted-matches).
static void bp_find(uintptr_t v, const char* text, int op) {
	WebKitFindController* fc = webkit_web_view_get_find_controller(WEBKIT_WEB_VIEW((gpointer)v));
	guint32 opts = WEBKIT_FIND_OPTIONS_CASE_INSENSITIVE | WEBKIT_FIND_OPTIONS_WRAP_AROUND;
	switch (op) {
	case 0:
		if (!text || !*text) {
			webkit_find_controller_search_finish(fc);
			goPresentFindCount(bp_id(webkit_find_controller_get_web_view(fc)), 0);
			return;
		}
		webkit_find_controller_count_matches(fc, text, opts, 10000);
		webkit_find_controller_search(fc, text, opts, 10000);
		break;
	case 1: webkit_find_controller_search_next(fc); break;
	case 2: webkit_find_controller_search_previous(fc); break;
	default: webkit_find_controller_search_finish(fc);
	}
}

// bp_add_script runs src in bunk's isolated script world of every page
// the view loads, before the page's own scripts.
static void bp_add_script(uintptr_t v, const char* src) {
	WebKitUserContentManager* m = webkit_web_view_get_user_content_manager(WEBKIT_WEB_VIEW((gpointer)v));
	WebKitUserScript* s = webkit_user_script_new_for_world(src, WEBKIT_USER_CONTENT_INJECT_TOP_FRAME,
		WEBKIT_USER_SCRIPT_INJECT_AT_DOCUMENT_START, "bunk", NULL, NULL);
	webkit_user_content_manager_add_script(m, s);
	webkit_user_script_unref(s);
}

static void bp_set_zoom(uintptr_t v, double level) { webkit_web_view_set_zoom_level(WEBKIT_WEB_VIEW((gpointer)v), level); }

static void bp_set_bg(uintptr_t v, double r, double g, double b) {
	GdkRGBA c = {r, g, b, 1};
	webkit_web_view_set_background_color(WEBKIT_WEB_VIEW((gpointer)v), &c);
}

static void bp_eval_done(GObject* o, GAsyncResult* res, gpointer data) {
	int call = GPOINTER_TO_INT(data);
	GError* err = NULL;
	JSCValue* val = webkit_web_view_evaluate_javascript_finish(WEBKIT_WEB_VIEW(o), res, &err);
	if (!val) {
		goPresentEval(bp_id(o), call, NULL, err ? err->message : "no value");
		if (err) g_error_free(err);
		return;
	}
	char* s = NULL;
	if (jsc_value_is_string(val)) s = jsc_value_to_string(val);
	else if (!jsc_value_is_undefined(val) && !jsc_value_is_null(val)) s = jsc_value_to_json(val, 0);
	goPresentEval(bp_id(o), call, s ? s : "", NULL);
	g_free(s);
	g_object_unref(val);
}

// bp_eval runs script in the page; world names an isolated script world,
// whose globals the page's own scripts cannot see or change, or is NULL
// for the page's world.
static void bp_eval(uintptr_t v, const char* script, const char* world, int call) {
	webkit_web_view_evaluate_javascript(WEBKIT_WEB_VIEW((gpointer)v), script, -1, world, NULL, NULL, bp_eval_done, GINT_TO_POINTER(call));
}
*/
import "C"

import (
	"errors"
	"time"
	"unsafe"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/present"
)

// presentScheme is the URI scheme that serves documents and their assets
// to the web views (presentserve.go).
const presentScheme = "bunk-present"

// webView is a WebKitWebView that shows documents. id indexes
// App.webViews; it is not a pane ID.
type webView struct {
	id     int
	ptr    uintptr
	widget *gtk.Widget
	panel  *panel
	calls  map[int]func(string, error)
	call   int
}

func (a *App) newWebView(pn *panel) *webView {
	v := &webView{id: len(a.webViews), panel: pn, calls: map[int]func(string, error){}}
	a.webViews = append(a.webViews, v)
	a.rep.WebViews++
	ptr := C.bp_view_new(C.int(v.id))
	v.ptr = uintptr(unsafe.Pointer(ptr))
	v.widget = wrapWidget(unsafe.Pointer(ptr))
	catcher := C.CString(present.ProblemCatcherScript)
	defer C.free(unsafe.Pointer(catcher))
	C.bp_add_script(C.uintptr_t(v.ptr), catcher)
	return v
}

// closeWebView ends v's web process and forgets v, whose widget GTK frees
// with its tab. Other calls on v do nothing after it.
func (a *App) closeWebView(v *webView) {
	if v.ptr == 0 {
		return
	}
	C.bp_view_close(C.uintptr_t(v.ptr))
	a.webViews[v.id] = nil
	a.rep.WebViews--
	v.ptr, v.widget, v.panel, v.calls = 0, nil, nil, nil
}

func webViewByID(id C.int) *webView {
	if theApp == nil || int(id) < 0 || int(id) >= len(theApp.webViews) {
		return nil
	}
	return theApp.webViews[id]
}

func (v *webView) load(uri string) {
	if v.ptr == 0 {
		return
	}
	cs := C.CString(uri)
	defer C.free(unsafe.Pointer(cs))
	C.bp_load_uri(C.uintptr_t(v.ptr), cs)
}

// kill ends the web process as a crash would (--do present-kill).
func (v *webView) kill() {
	if v.ptr != 0 {
		C.bp_view_kill(C.uintptr_t(v.ptr))
	}
}

func (v *webView) setBackground(rgb uint32) {
	if v.ptr == 0 {
		return
	}
	C.bp_set_bg(C.uintptr_t(v.ptr), C.double(float64(rgb>>16&0xff)/255), C.double(float64(rgb>>8&0xff)/255), C.double(float64(rgb&0xff)/255))
}

// find drives WebKit's find controller (bp_find): op 0 searches for
// text, 1 and 2 go to the next and previous match, 3 ends the search.
func (v *webView) find(text string, op int) {
	if v.ptr == 0 {
		return
	}
	cs := C.CString(text)
	defer C.free(unsafe.Pointer(cs))
	C.bp_find(C.uintptr_t(v.ptr), cs, C.int(op))
}

// setZoom scales the page; 1 is its own size.
func (v *webView) setZoom(level float64) {
	if v.ptr != 0 {
		C.bp_set_zoom(C.uintptr_t(v.ptr), C.double(level))
	}
}

// eval runs script in the page and calls done with its value: a string as
// it is, anything else as JSON, "" for undefined. isolated runs it in
// bunk's own script world. A closed view never calls done.
func (v *webView) eval(script string, isolated bool, done func(string, error)) {
	if v.ptr == 0 {
		return
	}
	v.call++
	v.calls[v.call] = done
	cs := C.CString(script)
	defer C.free(unsafe.Pointer(cs))
	var world *C.char
	if isolated {
		world = C.CString("bunk")
		defer C.free(unsafe.Pointer(world))
	}
	C.bp_eval(C.uintptr_t(v.ptr), cs, world, C.int(v.call))
}

// finishScheme answers request req with data of type ctype. csp, when set,
// becomes the response's Content-Security-Policy header; cors lets pages
// of other documents load it (fonts need it).
func finishScheme(req uintptr, data []byte, ctype, csp string, cors bool) {
	ct, cc := C.CString(ctype), C.CString(csp)
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(cc))
	var p *C.char
	if len(data) > 0 {
		p = (*C.char)(unsafe.Pointer(&data[0]))
	}
	corsFlag := C.int(0)
	if cors {
		corsFlag = 1
	}
	C.bp_scheme_finish(C.uintptr_t(req), p, C.gsize(len(data)), ct, cc, corsFlag)
}

func failScheme(req uintptr, err error) {
	cm := C.CString(err.Error())
	defer C.free(unsafe.Pointer(cm))
	C.bp_scheme_fail(C.uintptr_t(req), cm)
}

//export goPresentScheme
func goPresentScheme(req C.uintptr_t, view C.int, uri *C.char) {
	if theApp == nil {
		failScheme(uintptr(req), errors.New("bunk-gui is closing"))
		return
	}
	theApp.serveScheme(uintptr(req), webViewByID(view), C.GoString(uri))
}

//export goPresentNavigate
func goPresentNavigate(view C.int, uri *C.char, user, window C.int) C.int {
	v := webViewByID(view)
	if v == nil || v.panel == nil {
		return 0
	}
	if v.panel.navigate(C.GoString(uri), user != 0, window != 0) {
		return 1
	}
	return 0
}

//export goPresentLoad
func goPresentLoad(view, event C.int, uri *C.char) {
	v := webViewByID(view)
	if v == nil || v.panel == nil {
		return
	}
	switch event {
	case C.WEBKIT_LOAD_COMMITTED:
		v.panel.committed = C.GoString(uri)
	case C.WEBKIT_LOAD_FINISHED:
		v.panel.loaded()
	}
}

//export goPresentFailed
func goPresentFailed(view C.int, uri, errText *C.char) {
	if v := webViewByID(view); v != nil && v.panel != nil {
		v.panel.failed(C.GoString(uri), C.GoString(errText))
	}
}

//export goPresentAsk
func goPresentAsk(view C.int) {
	if v := webViewByID(view); v != nil && v.panel != nil {
		v.panel.ask()
	}
}

//export goPresentEval
func goPresentEval(view, call C.int, value, errText *C.char) {
	v := webViewByID(view)
	if v == nil {
		return
	}
	done := v.calls[int(call)]
	delete(v.calls, int(call))
	if done == nil {
		return
	}
	if errText != nil {
		done("", errors.New(C.GoString(errText)))
		return
	}
	done(C.GoString(value), nil)
}

//export goPresentFindCount
func goPresentFindCount(view, n C.int) {
	if v := webViewByID(view); v != nil && v.panel != nil {
		v.panel.foundMatches(int(n))
	}
}

//export goPresentInput
func goPresentInput(view C.int) {
	if v := webViewByID(view); v != nil && v.panel != nil {
		v.panel.lastInput = time.Now()
	}
}

//export goPresentReply
func goPresentReply(view C.int, raw *C.char) {
	if v := webViewByID(view); v != nil && v.panel != nil {
		v.panel.pageReply(C.GoString(raw))
	}
}

//export goPresentCrashed
func goPresentCrashed(view C.int) {
	if v := webViewByID(view); v != nil && v.panel != nil {
		v.panel.crashed()
	}
}
