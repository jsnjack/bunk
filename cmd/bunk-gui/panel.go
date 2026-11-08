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
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
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
	stale  bool   // changed while another document showed
	loaded bool   // its page finished loading at least once
	broken string // why its last load failed; "" when it did not

	reloads int // reloads since it was added
	changed int // sections the last Markdown reload marked
	scrollY string
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
	// picking: the drop-down is being rebuilt; its selection signal is ours.
	picking bool

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
	pn.askBtn = panelButton("chat-bubble-text-symbolic", "Ask the agent about the selected text", pn.ask)
	head.Append(pn.askBtn)
	head.Append(panelButton("web-browser-symbolic", "Open in the browser", pn.openExternal))
	head.Append(panelButton("list-remove-symbolic", "Remove this document from the panel", pn.removeCurrent))
	head.Append(panelButton("window-close-symbolic", "Hide the panel", pn.hide))
	pn.box.Append(head)

	pn.view = a.newWebView(pn)
	pn.view.widget.SetHExpand(true)
	pn.view.widget.SetVExpand(true)
	pn.view.setBackground(a.theme.Background.Hex())
	pn.box.Append(pn.view.widget)

	pn.status = gtk.NewLabel("")
	pn.status.AddCSSClass("bunk-panel-status")
	pn.status.SetXAlign(0)
	pn.status.SetEllipsize(pango.EllipsizeEnd)
	pn.box.Append(pn.status)

	t.page.SetEndChild(pn.box)
	t.page.NotifyProperty("position", func() {
		if pn.open && t.page.Width() > 0 {
			pn.width = t.page.Width() - t.page.Position()
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
	from.tab.docPanel().present(path, from, auto)
}

// present adds path to the panel, or finds it there, and shows it unless
// a hook reported it and [present] auto is off.
func (pn *panel) present(path string, from *Pane, auto bool) {
	kind := present.KindOf(path)
	if kind == present.KindNone || !filepath.IsAbs(path) {
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
	pn.syncPick()
	if auto && !pn.app.settings.presentAuto {
		pn.updateStatus()
		pn.app.refreshChrome()
		return
	}
	if d != pn.cur || !d.loaded {
		pn.show(d)
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
	pn.box.SetVisible(true)
	place := func() {
		w := pn.tab.page.Width()
		if w <= 0 {
			return
		}
		width := pn.width
		if width <= 0 {
			width = max(panelMinWidth, int(float64(w)*panelShare))
		}
		width = min(width, w-panelMinWidth)
		pn.tab.page.SetPosition(max(panelMinWidth, w-width))
	}
	place()
	coreglib.IdleAdd(place) // the first time, after GTK has laid the panel out
}

// hide closes the panel; its documents stay for the next time.
func (pn *panel) hide() {
	if !pn.open {
		return
	}
	pn.open = false
	pn.box.SetVisible(false)
	if f := pn.tab.focus; f != nil && pn.app.active == pn.tab {
		f.widget.GrabFocus()
	}
	pn.app.refreshChrome()
}

// toggleDocuments is the documents action: it shows or hides t's panel,
// or says how documents get there when it has none.
func (a *App) toggleDocuments(t *Tab) {
	if t == nil {
		return
	}
	if t.panel == nil || len(t.panel.docs) == 0 {
		a.toast("No documents yet: an agent shows one with \"bunk present FILE\"")
		return
	}
	t.panel.toggle()
}

// toggle shows or hides the panel; it has nothing to show without documents.
func (pn *panel) toggle() {
	switch {
	case pn.open:
		pn.hide()
	case len(pn.docs) > 0:
		if pn.cur == nil {
			pn.show(pn.docs[len(pn.docs)-1])
		}
		pn.reveal()
		pn.app.refreshChrome()
	}
}

// show loads d into the view.
func (pn *panel) show(d *panelDoc) {
	pn.cur = d
	d.stale = false
	d.broken = ""
	d.scrollY = ""
	pn.view.load(d.uri(filepath.Base(d.path)))
	pn.syncPick()
	pn.updateStatus()
}

// uri is the address of the file rel (slash-separated, relative to the
// document's directory) in d's origin.
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

// syncPick rebuilds the drop-down from the document list.
func (pn *panel) syncPick() {
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
		ours := d.kind == present.KindHTML
		for _, n := range names {
			if n == base {
				ours = true
			}
		}
		if !ours && d.kind == present.KindMarkdown {
			// An image the document may show.
			for _, n := range names {
				if imageFile(n) {
					ours = true
				}
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
	if d != pn.cur || !d.loaded {
		d.stale = d != pn.cur
		return
	}
	if d.kind == present.KindMarkdown {
		src, err := os.ReadFile(d.path)
		if err != nil {
			// The page keeps the last content; the status says why.
			d.broken = "the file is gone"
			if !errors.Is(err, fs.ErrNotExist) {
				d.broken = err.Error()
			}
			pn.updateStatus()
			return
		}
		d.broken = ""
		body, err := present.RenderBody(src)
		if err != nil {
			slog.Debug("present: render", "path", d.path, "err", err)
			return
		}
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
		return
	}
	pn.view.eval("String(window.scrollY)", true, func(v string, err error) {
		if err == nil {
			d.scrollY = v
		}
		d.reloads++
		d.updated = time.Now()
		pn.view.load(d.uri(filepath.Base(d.path)))
		pn.updateStatus()
	})
}

// loaded runs when the view finished loading a page.
func (pn *panel) loaded() {
	d := pn.cur
	if d == nil || d.broken != "" {
		pn.updateStatus()
		return
	}
	d.loaded = true
	if y, err := strconv.Atoi(d.scrollY); err == nil && y > 0 {
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

// crashed reloads the document after its web process ended.
func (pn *panel) crashed() {
	slog.Warn("present: the web process ended")
	if pn.cur != nil {
		pn.show(pn.cur)
	}
}

// navigate decides a navigation in the view, of the page or of a frame in
// it: the document's own origin and about: pages load, links elsewhere
// open in the browser when the user clicked them, and nothing else loads.
// data: and blob: documents never load: they would not carry the
// document's Content-Security-Policy.
func (pn *panel) navigate(uri string, user, window bool) bool {
	scheme, rest, _ := strings.Cut(uri, ":")
	switch strings.ToLower(scheme) {
	case presentScheme:
		host, _, _ := strings.Cut(strings.TrimPrefix(rest, "//"), "/")
		if d := pn.cur; d != nil && host == "doc-"+strconv.Itoa(d.id) && !window {
			return true
		}
		return false
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
		pn.view.load("about:blank")
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
	pn.view.panel = nil
}

// openExternal opens the shown document in the default browser: HTML as
// it is, Markdown rendered to a page on disk.
func (pn *panel) openExternal() {
	d := pn.cur
	if d == nil {
		return
	}
	if d.kind == present.KindHTML {
		openURL(pn.app, "file://"+d.path)
		return
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		pn.app.toast("Could not open the document: " + err.Error())
		return
	}
	out := filepath.Join(dir, "bunk-gui", "present", strconv.Itoa(os.Getpid())+"-"+strconv.Itoa(d.id))
	index, err := present.WriteStandalone(d.path, out, pn.app.presentTheme())
	if err != nil {
		pn.app.toast("Could not open the document: " + err.Error())
		return
	}
	openURL(pn.app, "file://"+index)
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
	if d := pn.cur; d != nil && d.kind == present.KindMarkdown {
		pn.view.eval("String(window.scrollY)", true, func(v string, err error) {
			if err == nil {
				d.scrollY = v
			}
			pn.view.load(d.uri(filepath.Base(d.path)))
		})
	}
}

// panelReport is a tab's document panel in --report.
type panelReport struct {
	Open    bool        `json:"open"`
	Rect    *rectReport `json:"rect,omitempty"` // nil while hidden
	Docs    []docReport `json:"docs"`
	Current int         `json:"current"` // index into docs, -1 when none
	Status  string      `json:"status"`
	Ask     *askReport  `json:"ask,omitempty"` // the question popover while it is open
	AskBtn  *rectReport `json:"ask_button,omitempty"`
	Sent    string      `json:"sent"` // the last question pasted into a pane
	Eval    string      `json:"eval"` // the result of the last --do present-eval
}

type docReport struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"` // markdown, html
	Loaded  bool   `json:"loaded"`
	Reloads int    `json:"reloads"`
	Changed int    `json:"changed"` // sections the last reload marked
	Auto    bool   `json:"auto"`
	Pane    int    `json:"pane"` // the pane that presented it, -1 when none
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
	r := &panelReport{Open: pn.open, Current: -1, Status: pn.status.Text(), Sent: pn.sent, Eval: pn.eval, Docs: []docReport{}}
	if pn.open && a.active == t {
		r.Rect = a.widgetRect(g, pn.box)
		r.AskBtn = a.widgetRect(g, pn.askBtn)
	}
	for i, d := range pn.docs {
		kind := "markdown"
		if d.kind == present.KindHTML {
			kind = "html"
		}
		dr := docReport{Path: d.path, Kind: kind, Loaded: d.loaded, Reloads: d.reloads, Changed: d.changed, Auto: d.auto, Pane: -1}
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
	}
	return nil
}

// panelSnapshot is the panel as session restore keeps it; nil without
// documents.
func (t *Tab) panelSnapshot() *restore.Panel {
	pn := t.panel
	if pn == nil || len(pn.docs) == 0 {
		return nil
	}
	rp := &restore.Panel{Open: pn.open, Width: pn.width}
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
		kind := present.KindOf(path)
		if kind == present.KindNone || pn.find(path) != nil {
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
	pn.show(cur)
	if rp.Open {
		pn.reveal()
	}
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
