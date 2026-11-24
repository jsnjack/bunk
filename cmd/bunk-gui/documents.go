package main

// The documents bunk-gui's panels showed (restore.Doc): every window
// records the documents it lists in one list for all of them, and the
// Documents page (present.RenderDocuments) shows that list, to show a
// document again after its tab or bunk-gui closed. An agent's
// SessionStart hook lists its conversation's documents again when the
// conversation resumes (internal/notify).

import (
	"cmp"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"

	"bunk/internal/present"
	"bunk/internal/restore"
)

// documentsHost is the host of the Documents page's address.
const documentsHost = "documents"

// docsFile is the list's file; "" when the state directory is unknown.
func docsFile() string { return restore.DocumentsPath(os.Getenv) }

// docsMu keeps this window's records in order; other windows wait on the
// list's lock.
var docsMu sync.Mutex

// recordDoc adds path, which a panel of t lists, to the list: from
// presented it, or the user opened it (nil). keep: a restored window lists
// it again, which changes nothing the list knows. The list is written off
// the main loop, as another window may hold it.
func (a *App) recordDoc(t *Tab, path string, from *Pane, keep bool) {
	file := docsFile()
	if file == "" {
		return
	}
	d := restore.Doc{Path: path, Shown: time.Now()}
	p := from
	if p == nil && t != nil {
		p = t.focus
	}
	if p != nil {
		d.Where = paneWhere(p)
		if from != nil {
			st := p.status()
			d.Agent, d.Session = st.Agent, st.AgentSession
		}
	}
	go func() {
		docsMu.Lock()
		defer docsMu.Unlock()
		if err := restore.RecordDoc(file, d, keep); err != nil {
			slog.Debug("documents: cannot record", "path", path, "err", err)
		}
	}()
}

// paneWhere is the repository of p, else its folder: the status's, or the
// shell's own until the status has found it, as in a tab just opened.
func paneWhere(p *Pane) string {
	st := p.status()
	return cmp.Or(st.Repo, st.Cwd, p.s.Cwd())
}

// showDocuments shows the Documents page in the panel, in place of the
// shown document, which Back returns to.
func (pn *panel) showDocuments() {
	pn.leave(nil)
	pn.cur = nil
	pn.home = true
	pn.load(present.DocumentsURL)
	pn.syncChanges()
	pn.syncPick()
	pn.updateStatus()
}

// navigateDocuments decides a navigation to the Documents page's address:
// the page itself loads, and a click on one of its documents shows that
// document in this tab. Only the Documents page reaches them, so no other
// page can open files through it.
func (pn *panel) navigateDocuments(u *url.URL, user, window bool) bool {
	if !pn.home || window {
		return false
	}
	switch strings.Trim(u.Path, "/") {
	case "":
		return true
	case present.DocumentsOpen:
		if file := u.Query().Get("path"); user && file != "" {
			// Not while WebKit decides this navigation: showing the
			// document loads another page in the same view.
			coreglib.IdleAdd(func() {
				if pn.home {
					pn.app.previewFile(pn.tab, file)
				}
			})
		}
	}
	return false
}

// documentsJob renders the Documents page for v's tab: the documents in
// the list whose files are still there, the tab's repository first.
func (a *App) documentsJob(v *webView) func() (served, error) {
	here := ""
	if p := v.panel.tab.focus; p != nil {
		here = paneWhere(p)
	}
	file, home, theme := docsFile(), os.Getenv("HOME"), a.presentTheme()
	return func() (served, error) {
		var docs []present.DocEntry
		for _, d := range restore.LoadDocs(file) {
			if fi, err := os.Stat(d.Path); err != nil || !fi.Mode().IsRegular() {
				continue
			}
			docs = append(docs, present.DocEntry{Path: d.Path, Where: d.Where, Agent: d.Agent, Shown: d.Shown})
		}
		page, err := present.RenderDocuments(docs, here, home, time.Now(), present.Options{AssetBase: present.AssetsURL, Theme: theme})
		if err != nil {
			return served{}, err
		}
		return served{data: page, ctype: "text/html; charset=utf-8"}, nil
	}
}
