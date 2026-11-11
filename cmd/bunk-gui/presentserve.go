package main

import (
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"

	"bunk/internal/present"
)

// maxServed caps a file the document panel serves, in bytes.
const maxServed = 32 << 20

// htmlCSP is the policy of agent-written HTML: the page may run its own
// scripts, load files from its own directory and bunk's assets (the page
// runtime, its stylesheet, Mermaid and KaTeX), and nothing from the
// network or the rest of the disk.
const htmlCSP = "default-src 'self'; script-src 'self' " + assetSource + " 'unsafe-inline' 'unsafe-eval'; " +
	"style-src 'self' " + assetSource + " 'unsafe-inline'; img-src 'self' " + assetSource + " data: blob:; " +
	"font-src 'self' " + assetSource + " data:; media-src 'self' data: blob:; connect-src 'self'; frame-src 'self'; " +
	"worker-src 'self' blob:; form-action 'none'; base-uri 'self'; object-src 'none'"

// assetSource names the origin of bunk's assets in a policy.
const assetSource = presentScheme + "://assets"

// previewQuery asks for the preview page of a file that shows through one
// (present.Kind.PreviewPage), rather than the file itself, which the page
// loads: bunk-present://doc-N/shot.png?bunk-preview.
const previewQuery = "bunk-preview"

var (
	errNotFound = errors.New("not found")
	errRefused  = errors.New("refused")
)

// serveScheme answers a bunk-present request of view v:
//
//	bunk-present://assets/PATH   the page runtime, Mermaid and KaTeX
//	bunk-present://doc-N/PATH    a file in document N's directory; the
//	                             document itself rendered when Markdown
//
// Each document has its own origin, so one document's page cannot read
// another's files. A view serves only its own panel's documents. Files are
// read and rendered off the main loop: a large Markdown document takes
// seconds, and the panes keep running meanwhile.
func (a *App) serveScheme(req uintptr, v *webView, raw string) {
	job, err := a.schemeJob(v, raw)
	if err != nil {
		v.notServed(raw, err)
		failScheme(req, fmt.Errorf("%s: %w", raw, err))
		return
	}
	go func() {
		r, err := job()
		coreglib.IdleAdd(func() {
			if err != nil {
				v.notServed(raw, err)
				failScheme(req, fmt.Errorf("%s: %w", raw, err))
				return
			}
			finishScheme(req, r.data, r.ctype, r.csp, r.cors)
		})
	}()
}

// served is the answer to a bunk-present request.
type served struct {
	data       []byte
	ctype, csp string
	cors       bool
}

// schemeJob finds what raw names, on the main loop, and returns the work
// that reads it, which may run on any goroutine.
func (a *App) schemeJob(v *webView, raw string) (func() (served, error), error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	rel := strings.TrimPrefix(path.Clean("/"+u.Path), "/")
	if u.Host == "assets" {
		return func() (served, error) {
			data, err := fs.ReadFile(present.Assets(), rel)
			if err != nil {
				return served{}, errNotFound
			}
			return served{data: data, ctype: contentType(rel), cors: true}, nil
		}, nil
	}
	if v == nil || v.panel == nil {
		return nil, errRefused
	}
	id, ok := strings.CutPrefix(u.Host, "doc-")
	if !ok {
		return nil, errNotFound
	}
	var doc string
	for _, o := range v.panel.docs {
		if strconv.Itoa(o.id) == id {
			doc = o.path
		}
	}
	if doc == "" {
		return nil, errNotFound
	}
	theme, preview := a.presentTheme(), u.RawQuery == previewQuery
	return func() (served, error) { return docServed(doc, rel, theme, preview) }, nil
}

// docServed reads the file rel of the document at doc: Markdown rendered,
// HTML with its policy and the theme's colours, and with preview set the
// preview page of a file that has one.
func docServed(doc, rel string, theme present.Theme, preview bool) (served, error) {
	file, err := docFile(doc, rel)
	if err != nil {
		return served{}, err
	}
	opts := present.Options{Title: filepath.Base(file), AssetBase: present.AssetsURL, Theme: theme}
	if kind, err := present.Preview(file); preview && err == nil && kind.PreviewPage() {
		var src []byte
		if kind == present.KindText {
			if src, err = readCapped(file); err != nil {
				return served{}, err
			}
		} else if _, err := os.Stat(file); err != nil {
			return served{}, errNotFound
		}
		page, err := present.RenderPreview(filepath.Base(file), kind, src, opts)
		if err != nil {
			return served{}, err
		}
		return served{data: page, ctype: "text/html; charset=utf-8"}, nil
	}
	src, err := readCapped(file)
	if err != nil {
		return served{}, err
	}
	switch present.KindOf(file) {
	case present.KindMarkdown:
		page, err := present.RenderMarkdown(src, opts)
		if err != nil {
			return served{}, err
		}
		return served{data: page, ctype: "text/html; charset=utf-8"}, nil
	case present.KindHTML:
		page := withCSP(present.InsertHead(src, "<style>"+present.ThemeCSS(theme)+"</style>"), htmlCSP)
		return served{data: page, ctype: "text/html; charset=utf-8", csp: htmlCSP}, nil
	}
	return served{data: src, ctype: contentType(file), csp: htmlCSP}, nil
}

// docFile is the file rel names in the directory of the document at doc.
// It refuses hidden files and directories, anything outside the directory
// (symbolic links included), and every file but the document itself when
// the document lies in the home directory or the root, where its
// neighbours would be the user's private files.
func docFile(doc, rel string) (string, error) {
	dir := filepath.Dir(doc)
	if rel == "" {
		rel = filepath.Base(doc)
	}
	file := filepath.Join(dir, filepath.FromSlash(rel))
	if file == doc {
		return file, nil
	}
	for _, part := range strings.Split(rel, "/") {
		if strings.HasPrefix(part, ".") {
			return "", errRefused
		}
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", errNotFound
	}
	// Resolved, so that another path to the home directory, through a
	// symbolic link such as Fedora Atomic's /home, counts as well.
	if realDir == "/" || realDir == realHome() {
		return "", errRefused
	}
	real, err := filepath.EvalSymlinks(file)
	if err != nil {
		return "", errNotFound
	}
	if !strings.HasPrefix(real, realDir+string(filepath.Separator)) {
		return "", errRefused
	}
	return file, nil
}

// realHome is the home directory with symbolic links resolved, or "".
func realHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	real, err := filepath.EvalSymlinks(home)
	if err != nil {
		return ""
	}
	return real
}

func readCapped(file string) ([]byte, error) {
	fi, err := os.Stat(file)
	if err != nil {
		return nil, errNotFound
	}
	if !fi.Mode().IsRegular() {
		return nil, errRefused
	}
	if fi.Size() > maxServed {
		return nil, fmt.Errorf("larger than %d MiB", maxServed>>20)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, errNotFound
	}
	return data, nil
}

// withCSP puts the policy into the page itself as well, first in its head,
// so it holds whatever WebKit makes of the response's header.
func withCSP(page []byte, csp string) []byte {
	return present.InsertHead(page, `<meta http-equiv="Content-Security-Policy" content="`+csp+`">`)
}

// contentType names a served file's type by its extension.
func contentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".woff2":
		return "font/woff2"
	case ".json":
		return "application/json"
	case ".txt", ".md", ".markdown":
		return "text/plain; charset=utf-8"
	}
	if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}
