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
	"regexp"
	"strconv"
	"strings"

	"bunk/internal/present"
)

// maxServed caps a file the document panel serves, in bytes.
const maxServed = 32 << 20

// htmlCSP is the policy of agent-written HTML: the page may run its own
// scripts and load files from its own directory, and nothing from the
// network or the rest of the disk.
const htmlCSP = "default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval'; " +
	"style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; " +
	"media-src 'self' data: blob:; connect-src 'self'; frame-src 'self'; " +
	"worker-src 'self' blob:; form-action 'none'; base-uri 'self'; object-src 'none'"

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
// another's files. A view serves only its own panel's documents.
func (a *App) serveScheme(req uintptr, v *webView, raw string) {
	data, ctype, csp, cors, err := a.schemeFile(v, raw)
	if err != nil {
		failScheme(req, fmt.Errorf("%s: %w", raw, err))
		return
	}
	finishScheme(req, data, ctype, csp, cors)
}

func (a *App) schemeFile(v *webView, raw string) (data []byte, ctype, csp string, cors bool, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", "", false, err
	}
	rel := strings.TrimPrefix(path.Clean("/"+u.Path), "/")
	if u.Host == "assets" {
		data, err := fs.ReadFile(present.Assets(), rel)
		if err != nil {
			return nil, "", "", false, errNotFound
		}
		return data, contentType(rel), "", true, nil
	}
	if v == nil || v.panel == nil {
		return nil, "", "", false, errRefused
	}
	id, ok := strings.CutPrefix(u.Host, "doc-")
	if !ok {
		return nil, "", "", false, errNotFound
	}
	var d *panelDoc
	for _, o := range v.panel.docs {
		if strconv.Itoa(o.id) == id {
			d = o
		}
	}
	if d == nil {
		return nil, "", "", false, errNotFound
	}
	file, err := docFile(d.path, rel)
	if err != nil {
		return nil, "", "", false, err
	}
	src, err := readCapped(file)
	if err != nil {
		return nil, "", "", false, err
	}
	switch present.KindOf(file) {
	case present.KindMarkdown:
		page, err := present.RenderMarkdown(src, present.Options{
			Title: filepath.Base(file), AssetBase: presentScheme + "://assets/", Theme: a.presentTheme(),
		})
		if err != nil {
			return nil, "", "", false, err
		}
		return page, "text/html; charset=utf-8", "", false, nil
	case present.KindHTML:
		return withCSP(src, htmlCSP), "text/html; charset=utf-8", htmlCSP, false, nil
	}
	return src, contentType(file), htmlCSP, false, nil
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
	if home, err := os.UserHomeDir(); err == nil && dir == filepath.Clean(home) || dir == "/" {
		return "", errRefused
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", errNotFound
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

var (
	headTag    = regexp.MustCompile(`(?i)<head(\s[^>]*)?>`)
	htmlTag    = regexp.MustCompile(`(?i)<html(\s[^>]*)?>`)
	doctypeTag = regexp.MustCompile(`(?i)^\s*<!doctype[^>]*>`)
)

// withCSP puts the policy into the page itself as well, first in its head,
// so it holds whatever WebKit makes of the response's header.
func withCSP(page []byte, csp string) []byte {
	meta := []byte(`<meta http-equiv="Content-Security-Policy" content="` + csp + `">`)
	at, wrap := -1, false
	if loc := headTag.FindIndex(page); loc != nil {
		at = loc[1]
	} else if loc := htmlTag.FindIndex(page); loc != nil {
		at, wrap = loc[1], true
	} else if loc := doctypeTag.FindIndex(page); loc != nil {
		at, wrap = loc[1], true
	} else {
		at, wrap = 0, true
	}
	if wrap {
		meta = append(append([]byte("<head>"), meta...), "</head>"...)
	}
	out := make([]byte, 0, len(page)+len(meta))
	out = append(out, page[:at]...)
	out = append(out, meta...)
	return append(out, page[at:]...)
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
