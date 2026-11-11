package main

// The document panel (docs/present.md): a side panel per tab that shows the
// Markdown and HTML files agents present ("bunk present FILE", or a hook
// after the agent wrote one) in a WebKit view (webview.go). It sits beside
// the tab's panes in Tab.page, so the panes keep their own layout; closing
// it gives them the width back.
//
// A file change reloads the document in place: Markdown swaps the article
// and keeps the reader at the same heading, marking sections that changed
// (the page runtime of internal/present); HTML reloads and scrolls back.
// "Ask the agent" pastes a question about the selected text into the
// agent's pane, without Enter, so the user reviews and sends it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	"bunk/internal/present"
	"bunk/internal/restore"
	"bunk/internal/vt10x"
)

const (
	// panelMinWidth and panelShare size a panel when it first opens: a
	// share of the tab's width, at least panelMinWidth.
	panelMinWidth = 360
	panelShare    = 0.42
	// reloadDelay is the quiet time after a file change before the
	// document reloads, so an editor's write-and-rename reloads once.
	reloadDelay = 250 * time.Millisecond
	// maxQuote caps the selected text quoted into a question, in bytes.
	maxQuote = 1500
	// maxHistory caps the documents Back remembers.
	maxHistory = 50
)

// panelDoc is one document in a tab's panel.
type panelDoc struct {
	id     int    // its origin: bunk-present://doc-<id>/
	path   string // absolute
	kind   present.Kind
	pane   *Pane // the pane that presented it; nil when it is gone or unknown
	auto   bool  // added by a hook, not asked for
	mon    gio.FileMonitorrer
	timer  coreglib.SourceHandle
	loaded bool   // its page finished loading at least once
	broken string // why its last load failed; "" when it did not

	crashed bool // its page ended the web process since the file last changed
	gen     int  // counts reloads begun; a render finishing late is dropped

	reloads int // reloads since it was added
	changed int // sections the last Markdown reload marked
	scrollY string
	lastY   string // where the reader was when another document took the view
	anchor  string // the id of the heading to scroll to once it loads
	// state is what the reader did in an HTML page (present.SaveStateScript)
	// before it loaded again, given back once it has loaded.
	state   string
	updated time.Time
}

// panel is a tab's document panel.
type panel struct {
	app    *App
	tab    *Tab
	box    *gtk.Box
	pick   *gtk.DropDown
	status *gtk.Label
	askBtn *gtk.Button
	view   *webView
	docs   []*panelDoc
	cur    *panelDoc
	open   bool
	width  int
	// history holds the documents shown before the current one, the last
	// most recent, for the Back button.
	history []*panelDoc
	backBtn *gtk.Button
	// goingBack: show runs for Back, which must not add to the history.
	goingBack bool
	// ptrRects holds, for window tests (ptr-at doc SELECTOR), the
	// rectangle each selector's element had in the page, as JSON.
	ptrRects map[string]string
	// zoom scales the panel's pages; 0 or 1 is their own size.
	zoom   float64
	finder *panelFind
	// placing: the panel waits for GTK to lay the tab out before it takes
	// its width (reveal); until then GTK's own position is not the user's.
	placing bool
	// committed is the address of the page the view committed to since
	// the last load began, or "". A load that a newer one cancelled still
	// finishes, and must not count for the document shown now.
	committed string
	// picking: the drop-down is being rebuilt; its selection signal is ours.
	picking bool

	overlay *gtk.Overlay
	compose *composeBox
	sent    string // the last question pasted into a pane (report)
	eval    string // the last test evaluation (report, --do present-eval)
}

// composeBox is the question popover of "Ask the agent".
type composeBox struct {
	pop     *gtk.Popover
	title   *gtk.Label
	quote   *gtk.Label
	entry   *gtk.Entry
	send    *gtk.Button
	doc     *panelDoc
	section string
	text    string
	target  *Pane
}

var nextDocID int

// docPanel is the tab's panel, created on first use.
func (t *Tab) docPanel() *panel {
	if t.panel == nil {
		t.panel = t.app.newPanel(t)
	}
	return t.panel
}

func (a *App) newPanel(t *Tab) *panel {
	pn := &panel{app: a, tab: t}
	pn.box = gtk.NewBox(gtk.OrientationVertical, 0)
	pn.box.AddCSSClass("bunk-panel")
	pn.box.SetVisible(false)

	head := gtk.NewBox(gtk.OrientationHorizontal, 4)
	head.AddCSSClass("bunk-panel-head")
	pn.backBtn = panelButton("go-previous-symbolic", "Back to the previous document", pn.back)
	pn.backBtn.SetSensitive(false)
	head.Append(pn.backBtn)
	pn.pick = gtk.NewDropDownFromStrings(nil)
	pn.pick.SetHExpand(true)
	pn.pick.SetTooltipText("Documents in this tab")
	pn.pick.NotifyProperty("selected", func() {
		if pn.picking {
			return
		}
		if i := int(pn.pick.Selected()); i >= 0 && i < len(pn.docs) && pn.docs[i] != pn.cur {
			pn.show(pn.docs[i])
		}
	})
	head.Append(pn.pick)
	pn.askBtn = panelButton("chat-message-new-symbolic", "Ask the agent about the selected text", pn.ask)
	head.Append(pn.askBtn)
	head.Append(panelButton("document-open-symbolic", "Open a file in the panel…", func() { a.pickPreview(t) }))
	head.Append(panelButton("web-browser-symbolic", "Open in the browser", pn.openExternal))
	head.Append(panelButton("list-remove-symbolic", "Remove this document from the panel", pn.removeCurrent))
	head.Append(panelButton("window-close-symbolic", "Hide the panel", pn.hide))
	pn.box.Append(head)

	pn.view = a.newWebView(pn)
	pn.view.widget.SetHExpand(true)
	pn.view.widget.SetVExpand(true)
	pn.view.setBackground(a.theme.Background.Hex())
	pn.overlay = gtk.NewOverlay()
	pn.overlay.SetChild(pn.view.widget)
	pn.box.Append(pn.overlay)

	pn.status = gtk.NewLabel("")
	pn.status.AddCSSClass("bunk-panel-status")
	pn.status.SetXAlign(0)
	pn.status.SetEllipsize(pango.EllipsizeEnd)
	pn.box.Append(pn.status)

	t.page.SetEndChild(pn.box)
	t.page.NotifyProperty("position", func() {
		if pn.open && !pn.placing && t.page.Width() > 0 {
			pn.width = t.page.Width() - t.page.Position()
		}
	})
	// A tab not shown yet has no width: the panel takes its width when GTK
	// first lays the tab out, as a restored split takes its ratio.
	t.page.NotifyProperty("max-position", func() {
		if pn.placing {
			coreglib.IdleAdd(pn.place) // not during the allocation that announced the size
		}
	})
	return pn
}

// panelButton is a flat icon button of the panel's header.
func panelButton(icon, tip string, f func()) *gtk.Button {
	b := iconButton(icon, 16, tip, "flat")
	b.ConnectClicked(f)
	return b
}

// presentFor shows path in the panel of the tab whose pane has token.
// auto: a hook reported the file; it opens the panel only with
// [present] auto, and is otherwise listed for later.
func (a *App) presentFor(token, path string, auto bool) {
	var from *Pane
	for _, p := range a.panes {
		if p != nil && !p.removeOnce && p.token == token {
			from = p
			break
		}
	}
	if from == nil || from.tab == nil {
		return
	}
	from.tab.docPanel().present(path, from, auto, "")
}

// present adds path to the panel, or finds it there, and shows it unless
// a hook reported it and [present] auto is off. anchor, when set, is the
// id of the heading to scroll to.
func (pn *panel) present(path string, from *Pane, auto bool, anchor string) {
	if !filepath.IsAbs(path) {
		return
	}
	kind, err := present.Preview(path)
	if err != nil {
		slog.Debug("present: cannot show", "path", path, "err", err)
		return
	}
	d := pn.find(path)
	if d == nil {
		nextDocID++
		d = &panelDoc{id: nextDocID, path: path, kind: kind, pane: from, auto: auto}
		pn.docs = append(pn.docs, d)
		pn.watch(d)
	} else if !auto {
		d.pane, d.auto = from, false
	}
	d.anchor = anchor
	pn.syncPick()
	if auto && !pn.app.settings.presentAuto {
		pn.updateStatus()
		pn.app.refreshChrome()
		return
	}
	if d != pn.cur || !d.loaded {
		pn.show(d)
	} else if anchor != "" {
		pn.scrollToAnchor(d)
	}
	pn.reveal()
	pn.app.refreshChrome()
}

func (pn *panel) find(path string) *panelDoc {
	for _, d := range pn.docs {
		if d.path == path {
			return d
		}
	}
	return nil
}

// reveal opens the panel at its last width, or a share of the tab's.
func (pn *panel) reveal() {
	if pn.open {
		return
	}
	pn.open = true
	pn.placing = true
	pn.box.SetVisible(true)
	pn.place()
	coreglib.IdleAdd(pn.place) // the first time, after GTK has laid the panel out
}

// place gives the open panel its width once the tab has one.
func (pn *panel) place() {
	w := pn.tab.page.Width()
	if !pn.open || !pn.placing || w <= 0 {
		return
	}
	width := pn.width
	if width <= 0 {
		width = max(panelMinWidth, int(float64(w)*panelShare))
	}
	width = min(width, w-panelMinWidth)
	pn.tab.page.SetPosition(max(panelMinWidth, w-width))
	pn.width = w - pn.tab.page.Position()
	pn.placing = false
}

// hide closes the panel; its documents stay for the next time.
func (pn *panel) hide() {
	if !pn.open {
		return
	}
	pn.open = false
	pn.placing = false
	pn.box.SetVisible(false)
	if f := pn.tab.focus; f != nil && pn.app.active == pn.tab {
		f.widget.GrabFocus()
	}
	pn.app.refreshChrome()
}

// toggleDocuments is the documents action: it shows or hides t's panel,
// or asks for a file to preview when it has none.
func (a *App) toggleDocuments(t *Tab) {
	if t == nil {
		return
	}
	if t.panel == nil || len(t.panel.docs) == 0 {
		a.pickPreview(t)
		return
	}
	t.panel.toggle()
}

// pickPreview is the open_in_panel action: a file dialog, which starts in
// the directory of t's focused pane, picks a file to preview in t's panel.
func (a *App) pickPreview(t *Tab) {
	if t == nil || a.finished {
		return
	}
	d := gtk.NewFileDialog()
	d.SetTitle("Open a file in the panel")
	d.SetAcceptLabel("Open")
	if p := t.focus; p != nil {
		if cwd := p.s.Cwd(); cwd != "" {
			d.SetInitialFolder(gio.NewFileForPath(cwd))
		}
	}
	a.openDialog = true
	d.Open(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		a.openDialog = false
		f, err := d.OpenFinish(res)
		if err != nil || f == nil || f.Object == nil {
			slog.Debug("present: file dialog", "err", err) // dismissed
			return
		}
		path := f.Path()
		if path == "" {
			a.toast("Cannot preview " + f.Basename() + ": not a local file")
			return
		}
		a.previewFile(t, path)
	})
}

// previewFile shows the file at path in t's panel, as the user asked for
// it (the file dialog, a drop, --do preview), or says why it cannot.
// Questions about it go to the tab's agent.
func (a *App) previewFile(t *Tab, path string) {
	if !slices.Contains(a.tabs, t) {
		return // closed meanwhile
	}
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		a.toast("Cannot preview " + filepath.Base(path) + ": not a file")
		return
	}
	if _, err := present.Preview(path); err != nil {
		a.toast("Cannot preview " + filepath.Base(path) + ": " + err.Error())
		return
	}
	t.docPanel().present(path, nil, false, "")
}

// dropPreviews lets files dropped on the tab's page preview in its panel.
func (a *App) dropPreviews(t *Tab) {
	drop := gtk.NewDropTarget(gdk.GTypeFileList, gdk.ActionCopy)
	drop.ConnectDrop(func(v *coreglib.Value, _, _ float64) bool {
		list, ok := v.GoValue().(*gdk.FileList)
		if !ok {
			return false
		}
		for _, f := range list.Files() {
			if path := f.Path(); path != "" {
				a.previewFile(t, path)
			}
		}
		return true
	})
	t.page.AddController(drop)
}

// toggle shows or hides the panel; it has nothing to show without documents.
func (pn *panel) toggle() {
	switch {
	case pn.open:
		pn.hide()
	case len(pn.docs) > 0:
		switch {
		case pn.cur == nil:
			pn.show(pn.docs[len(pn.docs)-1])
		case !pn.cur.loaded:
			pn.show(pn.cur) // restored closed: it loads when it opens
		}
		pn.reveal()
		pn.app.refreshChrome()
	}
}

// show loads d into the view, where the reader left it. The document it
// replaces goes on the history, unless Back is showing d.
func (pn *panel) show(d *panelDoc) {
	if old := pn.cur; old != nil && old != d {
		if old.loaded {
			// Sent before the load, so they read the old page.
			pn.saveState(old)
			pn.view.eval("String(window.scrollY)", true, func(v string, err error) {
				if err == nil {
					old.lastY = v
				}
			})
		}
		if !pn.goingBack {
			pn.history = append(pn.history, old)
			if len(pn.history) > maxHistory {
				pn.history = pn.history[1:]
			}
		}
	}
	pn.cur = d
	d.crashed = false
	d.broken = ""
	d.scrollY = d.lastY
	pn.load(d.page())
	pn.syncPick()
	pn.updateStatus()
}

// saveState keeps what the reader did in d's HTML page, which is shown, for
// the next time it loads: the sections they opened, the tab they chose,
// what they typed. Markdown reloads in place and keeps all of it.
func (pn *panel) saveState(d *panelDoc) {
	if d.kind != present.KindHTML {
		return
	}
	pn.view.eval(present.SaveStateScript, true, func(v string, err error) {
		if err == nil && strings.HasPrefix(v, "{") {
			d.state = v
		}
	})
}

// back shows the document shown before the current one that is still in
// the panel.
func (pn *panel) back() {
	for len(pn.history) > 0 {
		d := pn.history[len(pn.history)-1]
		pn.history = pn.history[:len(pn.history)-1]
		if slices.Contains(pn.docs, d) && d != pn.cur {
			pn.goingBack = true
			pn.show(d)
			pn.goingBack = false
			return
		}
	}
	pn.syncPick()
}

// follow shows the file at rel, which a link the user clicked in the shown
// document from names in its directory, as a document of its own: it
// reloads when it changes, and questions about it name it. The panel's
// limits hold: no hidden files, nothing outside the directory, and nothing
// beside a document in the home directory or /. frag is the id of the
// heading to scroll to.
func (pn *panel) follow(from *panelDoc, rel, frag string) {
	file, err := docFile(from.path, rel)
	if err != nil {
		pn.app.toast("Cannot open " + path.Base(rel) + ": the panel shows only files beside the document")
		return
	}
	if fi, err := os.Stat(file); err != nil || !fi.Mode().IsRegular() {
		pn.app.toast("Cannot open " + path.Base(rel) + ": not a file")
		return
	}
	if _, err := present.Preview(file); err != nil {
		pn.app.toast("Cannot open " + path.Base(rel) + ": " + err.Error())
		return
	}
	pn.present(file, from.pane, false, frag)
}

// scrollToAnchor scrolls the shown page to the heading d.anchor names.
func (pn *panel) scrollToAnchor(d *panelDoc) {
	id, _ := json.Marshal(d.anchor)
	d.anchor = ""
	pn.view.eval("(function(id){var e=document.getElementById(id);if(e)e.scrollIntoView();return String(!!e)})("+string(id)+")", true, func(string, error) {})
}

// uri is the address of the file rel (slash-separated, relative to the
// document's directory) in d's origin.
// page is the address the view loads for d: the file itself, or the page
// that previews it.
func (d *panelDoc) page() string {
	u := d.uri(filepath.Base(d.path))
	if d.kind.PreviewPage() {
		u += "?" + previewQuery
	}
	return u
}

func (d *panelDoc) uri(rel string) string {
	return fmt.Sprintf("%s://doc-%d/%s", presentScheme, d.id, escapePath(rel))
}

func escapePath(rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// syncPick rebuilds the drop-down from the document list, and enables
// Back when there is a document to go back to.
func (pn *panel) syncPick() {
	pn.backBtn.SetSensitive(slices.ContainsFunc(pn.history, func(d *panelDoc) bool {
		return d != pn.cur && slices.Contains(pn.docs, d)
	}))
	names := make([]string, len(pn.docs))
	sel := -1
	for i, d := range pn.docs {
		names[i] = pn.docLabel(d)
		if d == pn.cur {
			sel = i
		}
	}
	pn.picking = true
	pn.pick.SetModel(gtk.NewStringList(names))
	if sel >= 0 {
		pn.pick.SetSelected(uint(sel))
	}
	pn.picking = false
}

// docLabel names d by its file, with its directory when another document
// has the same file name.
func (pn *panel) docLabel(d *panelDoc) string {
	base := filepath.Base(d.path)
	for _, o := range pn.docs {
		if o != d && filepath.Base(o.path) == base {
			return filepath.Base(filepath.Dir(d.path)) + "/" + base
		}
	}
	return base
}

func (pn *panel) updateStatus() {
	d := pn.cur
	switch {
	case d == nil:
		pn.status.SetText("")
	case d.broken != "":
		pn.status.SetText("Could not show " + tildePath(d.path) + ": " + d.broken)
	case d.updated.IsZero():
		pn.status.SetText(tildePath(d.path))
	default:
		text := "Updated " + d.updated.Format("15:04:05")
		if d.changed == 1 {
			text += " · 1 section changed"
		} else if d.changed > 1 {
			text += " · " + strconv.Itoa(d.changed) + " sections changed"
		}
		pn.status.SetText(text + " · " + tildePath(d.path))
	}
	if z := pn.zoomPercent(); z != 100 && d != nil && d.broken == "" {
		pn.status.SetText(pn.status.Text() + " · " + strconv.Itoa(z) + "%")
	}
	pn.status.SetTooltipText(func() string {
		if d == nil {
			return ""
		}
		return d.path
	}())
}

func tildePath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && (path == home || strings.HasPrefix(path, home+"/")) {
		return "~" + path[len(home):]
	}
	return path
}

// watch reloads d when its directory changes: the file itself, an image
// it may show or, for HTML, any file it may load.
func (pn *panel) watch(d *panelDoc) {
	dir := gio.NewFileForPath(filepath.Dir(d.path))
	mon, err := dir.MonitorDirectory(context.Background(), gio.FileMonitorWatchMoves)
	if err != nil {
		slog.Debug("present: watch", "path", d.path, "err", err)
		return
	}
	d.mon = mon
	base := filepath.Base(d.path)
	gio.BaseFileMonitor(mon).ConnectChanged(func(file, other gio.Filer, ev gio.FileMonitorEvent) {
		switch ev {
		case gio.FileMonitorEventChanged, gio.FileMonitorEventAttributeChanged, gio.FileMonitorEventPreUnmount, gio.FileMonitorEventUnmounted:
			return // CHANGES_DONE_HINT follows a write
		}
		names := []string{}
		if file != nil {
			names = append(names, file.Basename())
		}
		if other != nil {
			names = append(names, other.Basename())
		}
		ours := false
		for _, n := range names {
			if n == base || d.kind.Document() && imageFile(n) || d.kind == present.KindHTML && pageFile(n) {
				ours = true
			}
		}
		if ours {
			pn.changedLater(d)
		}
	})
}

// imageFile reports whether name is an image a Markdown document can show.
// Other files beside a document, such as logs and build output, change
// often and do not alter its page.
func imageFile(name string) bool {
	if strings.HasPrefix(name, ".") {
		return false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".avif", ".bmp":
		return true
	}
	return false
}

// pageFile reports whether name is code, a style, a font or media an HTML
// page may load besides images. Other files change too often in a busy
// directory such as /tmp, logs and data included, and each reload
// restarts the page's scripts.
func pageFile(name string) bool {
	if strings.HasPrefix(name, ".") {
		return false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html", ".htm", ".css", ".js", ".mjs",
		".woff", ".woff2", ".ttf", ".otf", ".mp4", ".webm", ".mp3", ".ogg", ".wav":
		return true
	}
	return false
}

func (pn *panel) changedLater(d *panelDoc) {
	if d.timer != 0 {
		coreglib.SourceRemove(d.timer)
	}
	d.timer = coreglib.TimeoutAdd(uint(reloadDelay/time.Millisecond), func() bool {
		d.timer = 0
		pn.reload(d)
		return false
	})
}

// reload shows d's new content: in place for Markdown, so the reader keeps
// the place, else by loading the page again and scrolling back.
func (pn *panel) reload(d *panelDoc) {
	if pn.find(d.path) == nil {
		return
	}
	d.crashed = false // a new version may run
	if d != pn.cur || !d.loaded {
		return // it loads whole when shown
	}
	if d.kind == present.KindMarkdown {
		// Rendered off the main loop, like the page itself (serveScheme).
		d.gen++
		gen, path := d.gen, d.path
		go func() {
			src, readErr := readCapped(path)
			var body []byte
			var err error
			if readErr == nil {
				body, err = present.RenderBody(src)
			}
			coreglib.IdleAdd(func() {
				if gen != d.gen || pn.find(d.path) != d || d != pn.cur {
					return // a newer reload, or no longer shown
				}
				switch {
				case errors.Is(readErr, errNotFound):
					d.broken = "the file is gone" // the page keeps the last content
				case readErr != nil:
					d.broken = readErr.Error()
				case err != nil:
					slog.Debug("present: render", "path", d.path, "err", err)
					return
				default:
					d.broken = ""
					pn.replace(d, body)
					return
				}
				pn.updateStatus()
			})
		}()
		return
	}
	pn.saveState(d)
	pn.view.eval("String(window.scrollY)", true, func(v string, err error) {
		if err == nil {
			d.scrollY = v
		}
		d.reloads++
		d.updated = time.Now()
		d.broken = "" // load-failed sets it again
		pn.load(d.page())
		pn.updateStatus()
	})
}

// replace swaps the article of d's page for body, marking what changed.
func (pn *panel) replace(d *panelDoc, body []byte) {
	arg, _ := json.Marshal(string(body))
	pn.view.eval("bunkDoc.replace("+string(arg)+")", false, func(v string, err error) {
		if err != nil {
			pn.show(d) // the page runtime is not there: load it whole
			return
		}
		d.changed, _ = strconv.Atoi(strings.TrimSpace(v))
		d.reloads++
		d.updated = time.Now()
		pn.updateStatus()
	})
}

// load starts loading uri in the view.
func (pn *panel) load(uri string) {
	pn.committed = ""
	pn.view.load(uri)
}

// loaded runs when the view finished loading a page: the shown document's,
// once a load of its address committed.
func (pn *panel) loaded() {
	d := pn.cur
	if d == nil || d.broken != "" || !strings.HasPrefix(pn.committed, d.uri("")) {
		pn.updateStatus()
		return
	}
	d.loaded = true
	if d.state != "" {
		// Before the scroll: open sections change the page's height.
		pn.view.eval(fmt.Sprintf(present.RestoreStateScript, d.state), true, func(string, error) {})
		d.state = ""
	}
	if f := pn.finder; f != nil && f.query != "" {
		pn.view.find(f.query, 0) // the new page's matches
	}
	if d.anchor != "" {
		pn.scrollToAnchor(d)
	} else if y, err := strconv.Atoi(d.scrollY); err == nil && y > 0 {
		pn.view.eval("window.scrollTo(0,"+strconv.Itoa(y)+")", true, func(string, error) {})
	}
	d.scrollY = ""
	pn.updateStatus()
}

// failed records why the page of the shown document did not load: the
// file is gone, or it could not be read or rendered.
func (pn *panel) failed(uri, msg string) {
	d := pn.cur
	if d == nil || !strings.HasPrefix(uri, d.uri("")) {
		return
	}
	d.broken = msg
	slog.Debug("present: load failed", "uri", uri, "err", msg)
	pn.updateStatus()
}

// crashed loads the document again after its web process ended, once: a
// page that ends it again, say by using up its memory, stays down until
// its file changes or the user shows it again.
func (pn *panel) crashed() {
	d := pn.cur
	slog.Info("present: the web process ended", "doc", d != nil && d.crashed)
	if d == nil {
		return
	}
	if d.crashed {
		d.broken = "its page stopped twice; it loads again when the file changes"
		pn.updateStatus()
		return
	}
	pn.show(d)
	d.crashed = true
}

// navigate decides a navigation in the view, of the page or of a frame in
// it: the document's own origin and about: pages load, links elsewhere
// open in the browser when the user clicked them, and nothing else loads.
// A link the user clicked to another file of the document's origin shows
// that file as a document of its own (follow), so the panel never shows
// one file while it reloads and asks about another. data: and blob:
// documents never load: they would not carry the document's
// Content-Security-Policy.
func (pn *panel) navigate(uri string, user, window bool) bool {
	scheme, _, _ := strings.Cut(uri, ":")
	switch strings.ToLower(scheme) {
	case presentScheme:
		d := pn.cur
		u, err := url.Parse(uri)
		if d == nil || err != nil || u.Host != "doc-"+strconv.Itoa(d.id) {
			return false
		}
		rel := strings.TrimPrefix(path.Clean("/"+u.Path), "/")
		own := rel == filepath.Base(d.path)
		if user && !own {
			pn.follow(d, rel, u.Fragment)
			return false
		}
		return !window
	case "about":
		return !window
	case "http", "https", "mailto":
		if user {
			openURL(pn.app, uri)
		}
		return false
	}
	return false
}

// removeCurrent drops the shown document; the panel hides without any.
func (pn *panel) removeCurrent() {
	d := pn.cur
	if d == nil {
		return
	}
	pn.drop(d)
	if len(pn.docs) == 0 {
		pn.cur = nil
		pn.load("about:blank")
		pn.syncPick()
		pn.updateStatus()
		pn.hide()
		return
	}
	pn.show(pn.docs[len(pn.docs)-1])
}

func (pn *panel) drop(d *panelDoc) {
	for i, o := range pn.docs {
		if o == d {
			pn.docs = append(pn.docs[:i], pn.docs[i+1:]...)
			break
		}
	}
	pn.history = slices.DeleteFunc(pn.history, func(o *panelDoc) bool { return o == d })
	if d.timer != 0 {
		coreglib.SourceRemove(d.timer)
		d.timer = 0
	}
	if d.mon != nil {
		gio.BaseFileMonitor(d.mon).Cancel()
		d.mon = nil
	}
}

// close releases the panel with its tab.
func (pn *panel) close() {
	for len(pn.docs) > 0 {
		pn.drop(pn.docs[0])
	}
	pn.app.closeWebView(pn.view)
}

// openExternal opens the shown document in the default browser. Markdown,
// text, and an HTML page that loads bunk's assets or colours get a page on
// disk (present.NeedsPage), since a browser cannot reach the panel's
// bunk-present:// URLs; other files open as they are.
func (pn *panel) openExternal() {
	d := pn.cur
	if d == nil {
		return
	}
	if !present.NeedsPage(d.path, d.kind) {
		openURL(pn.app, present.FileURL(d.path))
		return
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		pn.app.toast("Could not open the document: " + err.Error())
		return
	}
	index, err := present.WriteStandalone(d.path, filepath.Join(dir, present.CacheDir), pn.app.presentTheme())
	if err != nil {
		pn.app.toast("Could not open the document: " + err.Error())
		return
	}
	openURL(pn.app, present.FileURL(index))
}

// target is the pane a question about d goes to: the pane that presented
// it while it is open in this tab, else the tab's agent.
func (pn *panel) target(d *panelDoc) *Pane {
	if d != nil && d.pane != nil && pn.app.paneByID(d.pane.id) == d.pane && d.pane.tab == pn.tab {
		return d.pane
	}
	return pn.tab.agentPane()
}

// selection is the text selected in the document and its section, as
// present.SelectionScript reports them.
type selection struct {
	Text    string `json:"text"`
	Section string `json:"section"`
}

// ask starts a question about the selected text.
func (pn *panel) ask() {
	d := pn.cur
	if d == nil {
		return
	}
	pn.view.eval(present.SelectionScript, true, func(v string, err error) {
		var sel selection
		if err == nil {
			err = json.Unmarshal([]byte(v), &sel)
		}
		if err != nil || strings.TrimSpace(sel.Text) == "" {
			pn.app.toast("Select text in the document first")
			return
		}
		pn.openCompose(d, sel)
	})
}

func (pn *panel) openCompose(d *panelDoc, sel selection) {
	target := pn.target(d)
	if target == nil {
		pn.app.toast("No pane to ask")
		return
	}
	c := pn.compose
	if c == nil {
		c = &composeBox{}
		c.pop = gtk.NewPopover()
		c.pop.SetParent(pn.askBtn)
		c.pop.AddCSSClass("bunk-ask")
		box := gtk.NewBox(gtk.OrientationVertical, 8)
		box.SetSizeRequest(340, -1)
		c.title = gtk.NewLabel("")
		c.title.SetXAlign(0)
		c.title.AddCSSClass("heading")
		c.quote = gtk.NewLabel("")
		c.quote.SetXAlign(0)
		c.quote.SetWrap(true)
		c.quote.SetLines(4)
		c.quote.SetEllipsize(pango.EllipsizeEnd)
		c.quote.SetMaxWidthChars(48)
		c.quote.AddCSSClass("bunk-ask-quote")
		c.entry = gtk.NewEntry()
		c.entry.SetPlaceholderText("Your question")
		c.entry.ConnectActivate(func() { pn.sendQuestion() })
		c.send = gtk.NewButtonWithLabel("Paste into the agent's input")
		c.send.AddCSSClass("suggested-action")
		c.send.ConnectClicked(func() { pn.sendQuestion() })
		hint := gtk.NewLabel("You press Enter in the pane to send it.")
		hint.SetXAlign(0)
		hint.AddCSSClass("dim-label")
		box.Append(c.title)
		box.Append(c.quote)
		box.Append(c.entry)
		box.Append(c.send)
		box.Append(hint)
		c.pop.SetChild(box)
		c.pop.ConnectClosed(func() { c.doc = nil })
		pn.compose = c
	}
	c.doc, c.section, c.text, c.target = d, sel.Section, sel.Text, target
	about := filepath.Base(d.path)
	if sel.Section != "" {
		about += " › " + sel.Section
	}
	c.title.SetText("Ask " + paneAgentName(target) + " about " + about)
	c.quote.SetText(strings.TrimSpace(sel.Text))
	c.entry.SetText("")
	c.pop.Popup()
	c.entry.GrabFocus()
}

func paneAgentName(p *Pane) string {
	if st := p.status(); st.Agent != "" {
		return st.Agent
	}
	return "the agent"
}

// sendQuestion pastes the composed question into the target pane, focuses
// that pane and leaves sending to the user.
func (pn *panel) sendQuestion() {
	c := pn.compose
	if c == nil || c.doc == nil {
		return
	}
	target := c.target
	if pn.app.paneByID(target.id) != target {
		pn.app.toast("The pane is gone")
		c.pop.Popdown()
		return
	}
	msg := questionText(displayPath(c.doc.path, target.s.Cwd()), c.section, c.text, c.entry.Text())
	c.pop.Popdown()
	target.paste(pasteSafe(target, msg))
	pn.sent = msg
	pn.app.showPane(target.id)
	pn.app.toast("Pasted into " + paneAgentName(target) + "'s input. Press Enter to send it.")
}

// pasteSafe keeps a question on one line for a program without bracketed
// paste, where each line break would act as Enter.
func pasteSafe(p *Pane, msg string) string {
	p.s.Lock()
	bracketed := p.s.Term.Mode()&vt10x.ModeSetPaste != 0
	p.s.Unlock()
	if bracketed {
		return msg
	}
	return strings.Join(strings.Fields(msg), " ")
}

// questionText is the question as the agent reads it:
//
//	About docs/plan.md › Session storage:
//	> the selected text
//	the user's question
func questionText(path, section, quote, question string) string {
	var b strings.Builder
	b.WriteString("About " + path)
	if section != "" {
		b.WriteString(" › " + section)
	}
	b.WriteString(":\n")
	quote = strings.TrimSpace(quote)
	if len(quote) > maxQuote {
		quote = cutUTF8(quote, maxQuote) + "…"
	}
	blank := 0
	for _, line := range strings.Split(quote, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line == "" {
			if blank++; blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		b.WriteString(strings.TrimRight("> "+line, " ") + "\n")
	}
	if q := strings.TrimSpace(question); q != "" {
		b.WriteString(q)
	}
	return strings.TrimRight(b.String(), "\n")
}

func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xc0 == 0x80 {
		n--
	}
	return s[:n]
}

// displayPath names path for the agent: relative to its directory when the
// file is inside it, else with ~ for the home directory.
func displayPath(path, cwd string) string {
	if cwd != "" {
		if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return tildePath(path)
}

// presentTheme is the colours of documents in the panel: the terminal's.
func (a *App) presentTheme() present.Theme {
	t := a.theme
	bg, fg := t.Background.Hex(), t.Foreground.Hex()
	accent := t.ANSI[12].Hex()
	return present.Theme{
		Background:     hexColor(bg),
		Foreground:     hexColor(fg),
		Muted:          hexColor(mixRGB(fg, bg, 0.62)),
		Accent:         hexColor(accent),
		Border:         hexColor(mixRGB(fg, bg, 0.22)),
		CodeBackground: hexColor(mixRGB(fg, bg, 0.07)),
		Selection:      hexColor(mixRGB(accent, bg, 0.38)),
		Dark:           luminance(bg) < 0.5,
	}
}

func hexColor(rgb uint32) string { return fmt.Sprintf("#%06x", rgb&0xffffff) }

// mixRGB is a share of a over b.
func mixRGB(a, b uint32, share float64) uint32 {
	ch := func(x, y uint32) uint32 {
		return uint32(float64(x)*share + float64(y)*(1-share) + 0.5)
	}
	return ch(a>>16&0xff, b>>16&0xff)<<16 | ch(a>>8&0xff, b>>8&0xff)<<8 | ch(a&0xff, b&0xff)
}

func luminance(rgb uint32) float64 {
	return (0.2126*float64(rgb>>16&0xff) + 0.7152*float64(rgb>>8&0xff) + 0.0722*float64(rgb&0xff)) / 255
}

// themeChanged restyles the shown document after a theme change.
func (pn *panel) themeChanged() {
	pn.view.setBackground(pn.app.theme.Background.Hex())
	// Every page but a PDF has the theme's colours.
	if d := pn.cur; d != nil && d.kind != present.KindPDF {
		pn.view.eval("String(window.scrollY)", true, func(v string, err error) {
			if err == nil {
				d.scrollY = v
			}
			pn.load(d.page())
		})
	}
}

// panelReport is a tab's document panel in --report.
type panelReport struct {
	Open    bool             `json:"open"`
	Rect    *rectReport      `json:"rect,omitempty"` // nil while hidden
	Docs    []docReport      `json:"docs"`
	Current int              `json:"current"` // index into docs, -1 when none
	Status  string           `json:"status"`
	Ask     *askReport       `json:"ask,omitempty"` // the question popover while it is open
	AskBtn  *rectReport      `json:"ask_button,omitempty"`
	Sent    string           `json:"sent"`           // the last question pasted into a pane
	Eval    string           `json:"eval"`           // the result of the last --do present-eval
	Back    bool             `json:"back"`           // the Back button is enabled
	Zoom    int              `json:"zoom"`           // the pages' scale in percent
	Find    *panelFindReport `json:"find,omitempty"` // the find bar while it is open
}

type panelFindReport struct {
	Query string `json:"query"`
	Count string `json:"count"` // as the bar shows it: "2/5"
}

type docReport struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"` // markdown, html, image, pdf, video, audio, text
	Loaded  bool   `json:"loaded"`
	Reloads int    `json:"reloads"`
	Changed int    `json:"changed"` // sections the last reload marked
	Auto    bool   `json:"auto"`
	Pane    int    `json:"pane"`    // the pane that presented it, -1 when none
	Crashed bool   `json:"crashed"` // its page ended the web process since the file changed
}

type askReport struct {
	Title   string `json:"title"`
	Quote   string `json:"quote"`
	Section string `json:"section"`
	Target  int    `json:"target"`
}

func (a *App) panelReport(g winGeom, t *Tab) *panelReport {
	pn := t.panel
	if pn == nil {
		return nil
	}
	r := &panelReport{Open: pn.open, Current: -1, Status: pn.status.Text(), Sent: pn.sent, Eval: pn.eval, Docs: []docReport{}, Back: pn.backBtn.Sensitive(), Zoom: pn.zoomPercent()}
	if f := pn.finder; f != nil {
		r.Find = &panelFindReport{Query: f.query, Count: f.count.Text()}
	}
	if pn.open && a.active == t {
		r.Rect = a.widgetRect(g, pn.box)
		r.AskBtn = a.widgetRect(g, pn.askBtn)
	}
	for i, d := range pn.docs {
		dr := docReport{Path: d.path, Kind: d.kind.String(), Loaded: d.loaded, Reloads: d.reloads, Changed: d.changed, Auto: d.auto, Pane: -1, Crashed: d.crashed}
		if d.pane != nil {
			dr.Pane = d.pane.id
		}
		r.Docs = append(r.Docs, dr)
		if d == pn.cur {
			r.Current = i
		}
	}
	if c := pn.compose; c != nil && c.doc != nil && c.pop.Visible() {
		r.Ask = &askReport{Title: c.title.Text(), Quote: c.text, Section: c.section, Target: c.target.id}
	}
	return r
}

// presentScript selects the first occurrence of a text in the document
// (--do present-select), as a reader's drag would.
const presentSelectScript = `(function(t){
  var w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  for (var n = w.nextNode(); n; n = w.nextNode()) {
    var i = n.data.indexOf(t);
    if (i >= 0) {
      var r = document.createRange();
      r.setStart(n, i); r.setEnd(n, i + t.length);
      var s = window.getSelection(); s.removeAllRanges(); s.addRange(r);
      return "selected";
    }
  }
  return "missing";
})(%s)`

// presentStep runs the --do steps of the document panel:
//
//	present PATH [auto]     what "bunk present PATH" from the focused pane does
//	present-eval JS         evaluate JS in the shown page; the result goes to panel.eval
//	present-select TEXT     select TEXT in the shown page
//	present-ask             Ask the agent, as its button does
//	present-toggle          the documents action of the selected tab
//	present-kill            end the shown page's web process, as a crash would
//	present-browser         open the shown document in the browser, as its button does
//	present-back            show the previous document, as the Back button does
//	preview PATH            what the file dialog of open_in_panel does with PATH
func (a *App) presentStep(verb, arg string) error {
	t := a.active
	if t == nil {
		return fmt.Errorf("no tab")
	}
	if verb == "present" {
		p := t.focus
		if p == nil {
			return fmt.Errorf("no focused pane")
		}
		path, mode, _ := strings.Cut(arg, " ")
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.s.Cwd(), path)
		}
		if _, err := os.Stat(path); err != nil {
			return err // "bunk present" refuses it too
		}
		a.presentFor(p.token, path, mode == "auto")
		return nil
	}
	if verb == "present-toggle" {
		a.toggleDocuments(t)
		return nil
	}
	if verb == "preview" {
		path := arg
		if p := t.focus; p != nil && !filepath.IsAbs(path) {
			path = filepath.Join(p.s.Cwd(), path)
		}
		a.previewFile(t, path)
		return nil
	}
	pn := t.panel
	if pn == nil || pn.cur == nil {
		return fmt.Errorf("no document shown")
	}
	switch verb {
	case "present-eval":
		pn.eval = ""
		pn.view.eval(arg, false, func(v string, err error) {
			if err != nil {
				v = "error: " + err.Error()
			}
			pn.eval = v
		})
	case "present-select":
		q, _ := json.Marshal(arg)
		pn.view.eval(fmt.Sprintf(presentSelectScript, q), true, func(v string, err error) {
			if err != nil || v != "selected" {
				a.reportError("present-select %q: %s %v", arg, v, err)
			}
		})
	case "present-ask":
		pn.ask()
	case "present-kill":
		pn.view.kill()
	case "present-browser":
		pn.openExternal()
	case "present-back":
		pn.back()
	}
	return nil
}

// docTarget is the centre of the element that selector picks in the shown
// document, in the window's coordinates (ptr-at doc SELECTOR). The page
// measures it asynchronously: calls fail until it has answered, and
// pointerAt asks again.
func (a *App) docTarget(selector string) (float64, float64, error) {
	t := a.active
	if t == nil || t.panel == nil || t.panel.cur == nil || !t.panel.open {
		return 0, 0, errors.New("no document shown")
	}
	pn := t.panel
	if pn.ptrRects == nil {
		pn.ptrRects = map[string]string{}
	}
	q, _ := json.Marshal(selector)
	pn.view.eval(fmt.Sprintf(docRectScript, q), true, func(v string, err error) {
		if err == nil {
			pn.ptrRects[selector] = v
		}
	})
	var c [2]float64
	if raw := pn.ptrRects[selector]; raw == "" || json.Unmarshal([]byte(raw), &c) != nil {
		return 0, 0, errors.New("no element " + selector + " in the document yet")
	}
	x, y, _, _, ok := a.boundsIn(pn.view.widget)
	if !ok {
		return 0, 0, errors.New("the document view is not shown")
	}
	z := float64(pn.zoomPercent()) / 100 // CSS pixels to the view's
	return x + c[0]*z, y + c[1]*z, nil
}

// docRectScript returns the centre of the element a selector picks, in the
// view's coordinates, as JSON, or "" when there is none.
const docRectScript = `(function(s){
  var e = document.querySelector(s);
  if (!e) return "";
  var r = e.getBoundingClientRect();
  return JSON.stringify([r.x + r.width / 2, r.y + r.height / 2]);
})(%s)`

// panelSnapshot is the panel as session restore keeps it; nil without
// documents.
func (t *Tab) panelSnapshot() *restore.Panel {
	pn := t.panel
	if pn == nil || len(pn.docs) == 0 {
		return nil
	}
	rp := &restore.Panel{Open: pn.open, Width: pn.width, Zoom: pn.zoom}
	for i, d := range pn.docs {
		rp.Docs = append(rp.Docs, d.path)
		if d == pn.cur {
			rp.Current = i
		}
	}
	return rp
}

// restorePanel lists the documents of a restored tab that still exist and
// opens the panel as it was. Questions go to the tab's agent: the panes
// that presented the documents are new.
func (t *Tab) restorePanel(rp restore.Panel) {
	pn := t.docPanel()
	var cur *panelDoc
	for i, path := range rp.Docs {
		if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() || !filepath.IsAbs(path) {
			continue
		}
		kind, err := present.Preview(path)
		if err != nil || pn.find(path) != nil {
			continue
		}
		nextDocID++
		d := &panelDoc{id: nextDocID, path: path, kind: kind}
		pn.docs = append(pn.docs, d)
		pn.watch(d)
		if i == rp.Current || cur == nil {
			cur = d
		}
	}
	if cur == nil {
		return
	}
	pn.width = rp.Width
	pn.setZoom(rp.Zoom)
	if !rp.Open {
		// Listed: it loads when the panel opens (toggle), so hidden
		// panels start no web process.
		pn.cur = cur
		pn.syncPick()
		pn.updateStatus()
		return
	}
	pn.show(cur)
	pn.reveal()
}

// presentWait is --do present-wait EXPR: it evaluates EXPR in the shown
// page every 50 ms until it is true, as "wait" does for the report, and
// gives up after 15 s.
func (a *App) presentWait(expr string, next func()) {
	deadline := time.Now().Add(15 * time.Second)
	var try func()
	try = func() {
		t := a.active
		if t == nil || t.panel == nil || t.panel.cur == nil {
			a.reportError("present-wait %s: no document shown", expr)
			next()
			return
		}
		t.panel.view.eval("String(Boolean("+expr+"))", false, func(v string, err error) {
			switch {
			case err == nil && v == "true":
				next()
			case time.Now().After(deadline):
				a.reportError("present-wait %s: still %q (%v)", expr, v, err)
				next()
			default:
				coreglib.TimeoutAdd(50, func() bool {
					try()
					return false
				})
			}
		})
	}
	try()
}

// panelFind is the find bar of a panel: WebKit finds the text in the
// shown page, highlights every match and scrolls to the current one.
type panelFind struct {
	bar   *gtk.Box
	entry *gtk.Entry
	count *gtk.Label
	query string
	total int // matches WebKit counted
	idx   int // the current match, from 1; 0 when none
}

// focusedPanel is the selected tab's panel while it has the keyboard
// focus: the search and text size actions then find and zoom in its page.
func (a *App) focusedPanel() *panel {
	t := a.active
	if t == nil || t.panel == nil || !t.panel.open || t.panel.cur == nil {
		return nil
	}
	fw := a.win.Focus()
	if fw == nil || !gtk.BaseWidget(fw).IsAncestor(t.panel.box) {
		return nil
	}
	return t.panel
}

// openFind shows the find bar over the page, or focuses it again.
func (pn *panel) openFind() {
	if f := pn.finder; f != nil {
		f.entry.GrabFocus()
		return
	}
	a := pn.app
	f := &panelFind{}
	f.bar = gtk.NewBox(gtk.OrientationHorizontal, 6)
	f.bar.AddCSSClass("findbar")
	f.bar.SetHAlign(gtk.AlignEnd)
	f.bar.SetVAlign(gtk.AlignStart)
	f.entry = gtk.NewEntry()
	f.entry.AddCSSClass("findentry")
	f.entry.SetPlaceholderText("Find in the document")
	f.entry.SetWidthChars(22)
	f.count = label("", "findcount")
	cl := iconButton("window-close-symbolic", 14, a.withKeys("Close", "search_exit"), "tabclose")
	cl.SetVAlign(gtk.AlignCenter)
	cl.ConnectClicked(pn.closeFind)
	f.bar.Append(f.entry)
	f.bar.Append(f.count)
	f.bar.Append(cl)
	f.entry.ConnectChanged(func() {
		if pn.finder == f {
			f.query = f.entry.Text()
			f.idx, f.total = 0, 0
			pn.view.find(f.query, 0)
			pn.showFind()
		}
	})
	pn.overlay.AddOverlay(f.bar)
	pn.finder = f
	f.entry.GrabFocus()
}

// closeFind removes the bar and the highlights, and gives the page the
// keyboard back.
func (pn *panel) closeFind() {
	f := pn.finder
	if f == nil {
		return
	}
	pn.finder = nil
	pn.overlay.RemoveOverlay(f.bar)
	pn.view.find("", 3)
	if pn.open {
		pn.view.widget.GrabFocus()
	}
}

// findFocused reports the keyboard in the panel's find bar.
func (pn *panel) findFocused() bool {
	if pn.finder == nil {
		return false
	}
	fw := pn.app.win.Focus()
	return fw != nil && gtk.BaseWidget(fw).IsAncestor(pn.finder.entry)
}

// findAction runs search_next, search_prev and search_exit in the bar.
func (pn *panel) findAction(name string) bool {
	f := pn.finder
	if f == nil {
		return false
	}
	switch name {
	case "search_next", "search_prev":
		if f.total == 0 {
			return true
		}
		if name == "search_next" {
			pn.view.find("", 1)
			f.idx = f.idx%f.total + 1
		} else {
			pn.view.find("", 2)
			f.idx = (f.idx+f.total-2)%f.total + 1
		}
		pn.showFind()
	case "search_exit":
		pn.closeFind()
	}
	return true
}

// foundMatches takes WebKit's count of the query's matches; the search
// has gone to the first.
func (pn *panel) foundMatches(n int) {
	f := pn.finder
	if f == nil {
		return
	}
	f.total = n
	f.idx = min(1, n)
	pn.showFind()
}

func (pn *panel) showFind() {
	f := pn.finder
	if f.query == "" {
		f.count.SetText("")
		return
	}
	f.count.SetText(fmt.Sprintf("%d/%d", f.idx, f.total))
}

// zoomSteps are the scales the text size actions step through in the
// panel, as browsers do.
var zoomSteps = []float64{0.5, 0.67, 0.75, 0.8, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2, 2.5, 3}

// zoomBy makes the panel's pages larger (dir > 0) or smaller (dir < 0),
// or their own size (dir 0).
func (pn *panel) zoomBy(dir int) {
	if dir == 0 {
		pn.setZoom(1)
		return
	}
	cur := pn.zoom
	if cur <= 0 {
		cur = 1
	}
	i := 0
	for i < len(zoomSteps)-1 && zoomSteps[i] < cur-0.001 {
		i++
	}
	switch {
	case dir > 0 && zoomSteps[i] > cur+0.001:
		// between steps: the next one up is this one
	case dir > 0:
		i = min(i+1, len(zoomSteps)-1)
	default:
		i = max(i-1, 0)
	}
	pn.setZoom(zoomSteps[i])
}

// setZoom scales the panel's pages; z outside the steps' range is taken
// as their own size.
func (pn *panel) setZoom(z float64) {
	if z < zoomSteps[0] || z > zoomSteps[len(zoomSteps)-1] {
		z = 1
	}
	pn.zoom = z
	pn.view.setZoom(z)
	pn.updateStatus()
}

// zoomPercent is the pages' scale in percent.
func (pn *panel) zoomPercent() int {
	if pn.zoom <= 0 {
		return 100
	}
	return int(pn.zoom*100 + 0.5)
}
