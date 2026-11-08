// Package present renders the documents that bunk-gui shows beside a tab:
// Markdown becomes a complete HTML page with highlighted code, Mermaid
// diagrams and KaTeX math. The page loads its runtime, Mermaid and KaTeX
// from Assets and nothing from the network, so the same page works in
// bunk-gui's web view and, written with WriteStandalone, in a browser.
package present

import (
	"bytes"
	"fmt"
	"html"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// Kind is the kind of document present shows.
type Kind int

// The kinds of documents, by file extension.
const (
	KindNone     Kind = iota
	KindMarkdown      // .md .markdown .mdown .mkd
	KindHTML          // .html .htm
)

// KindOf returns the kind of the document at path, by its extension in any
// case.
func KindOf(path string) Kind {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return KindMarkdown
	case ".html", ".htm":
		return KindHTML
	}
	return KindNone
}

// Theme is the colours a rendered page uses, as CSS colour strings
// ("#1b1c20").
type Theme struct {
	Background, Foreground, Muted, Accent, Border, CodeBackground, Selection string

	// Dark picks the code highlighting style and Mermaid's theme.
	Dark bool
}

// DefaultTheme returns a dark theme close to bunk-gui's default colours.
func DefaultTheme() Theme {
	return Theme{
		Background:     "#1b1c20",
		Foreground:     "#e6e8eb",
		Muted:          "#9a9ea6",
		Accent:         "#5aa9ff",
		Border:         "#34363c",
		CodeBackground: "#24262b",
		Selection:      "#2d4a6e",
		Dark:           true,
	}
}

// lightTheme fills the colours a light Theme leaves out.
func lightTheme() Theme {
	return Theme{
		Background:     "#ffffff",
		Foreground:     "#1f2328",
		Muted:          "#59636e",
		Accent:         "#0969da",
		Border:         "#d1d9e0",
		CodeBackground: "#f6f8fa",
		Selection:      "#b6d7ff",
	}
}

// cssColor accepts hex colours, names and colour functions, and nothing that
// could end the declaration or the <style> element it goes into.
var cssColor = regexp.MustCompile(`^[#a-zA-Z0-9(),.%/ +-]{1,64}$`)

// filled returns t with every missing or unusable colour taken from the
// default theme of the same darkness. A zero Theme is DefaultTheme.
func (t Theme) filled() Theme {
	if t == (Theme{}) {
		return DefaultTheme()
	}
	def := lightTheme()
	if t.Dark {
		def = DefaultTheme()
	}
	pick := func(v, fallback string) string {
		v = strings.TrimSpace(v)
		if !cssColor.MatchString(v) {
			return fallback
		}
		return v
	}
	return Theme{
		Background:     pick(t.Background, def.Background),
		Foreground:     pick(t.Foreground, def.Foreground),
		Muted:          pick(t.Muted, def.Muted),
		Accent:         pick(t.Accent, def.Accent),
		Border:         pick(t.Border, def.Border),
		CodeBackground: pick(t.CodeBackground, def.CodeBackground),
		Selection:      pick(t.Selection, def.Selection),
		Dark:           t.Dark,
	}
}

// Options control the page RenderMarkdown makes.
type Options struct {
	// Title is the page's <title>. When it is empty, the page takes the
	// text of the document's first heading; callers pass the file name.
	Title string

	// AssetBase is the URL prefix the page loads Assets from, ending in
	// "/": "bunk-present://assets/" in bunk-gui, or a relative "assets/"
	// for a page on disk.
	AssetBase string

	Theme Theme
}

// The page's own assets. The runtime loads KaTeX's and Mermaid's scripts
// itself, only for documents that need them.
const (
	assetScript   = "bunk-doc.js"
	assetStyle    = "bunk-doc.css"
	assetKatexCSS = "katex/katex.min.css"
)

// articleOpen starts the element whose content RenderBody returns.
const articleOpen = `<article id="bunk-doc">`

// RenderMarkdown returns a complete HTML document for the Markdown src.
func RenderMarkdown(src []byte, opts Options) ([]byte, error) {
	return renderPage(src, opts, "")
}

// RenderBody returns only the inner HTML of the document's
// <article id="bunk-doc">, so a live page can swap its content without a
// reload (bunkDoc.replace in the page runtime).
func RenderBody(src []byte) ([]byte, error) {
	doc, err := renderMarkdown(src)
	if err != nil {
		return nil, err
	}
	return doc.body, nil
}

// renderPage renders src into a page. A non-empty baseHref adds a <base>
// element, so relative links of a page written elsewhere still reach the
// document's directory.
func renderPage(src []byte, opts Options, baseHref string) ([]byte, error) {
	doc, err := renderMarkdown(src)
	if err != nil {
		return nil, err
	}
	theme := opts.Theme.filled()
	title := opts.Title
	if title == "" {
		title = doc.title
	}
	mode := "light"
	if theme.Dark {
		mode = "dark"
	}
	asset := func(name string) string { return html.EscapeString(opts.AssetBase + name) }

	var b bytes.Buffer
	fmt.Fprintf(&b, "<!doctype html>\n<html lang=\"en\" data-bunk-theme=\"%s\">\n<head>\n", mode)
	fmt.Fprintf(&b, "<meta http-equiv=\"Content-Security-Policy\" content=\"%s\">\n",
		html.EscapeString(contentSecurityPolicy(opts.AssetBase, baseHref != "")))
	b.WriteString("<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	if baseHref != "" {
		fmt.Fprintf(&b, "<base href=\"%s\">\n", html.EscapeString(baseHref))
	}
	fmt.Fprintf(&b, "<title>%s</title>\n", html.EscapeString(title))
	fmt.Fprintf(&b, "<link rel=\"stylesheet\" href=\"%s\">\n", asset(assetStyle))
	fmt.Fprintf(&b, "<link rel=\"stylesheet\" href=\"%s\">\n", asset(assetKatexCSS))
	b.WriteString("<style>\n")
	fmt.Fprintf(&b, ":root{color-scheme:%s;--bunk-bg:%s;--bunk-fg:%s;--bunk-muted:%s;--bunk-accent:%s;"+
		"--bunk-border:%s;--bunk-code-bg:%s;--bunk-selection:%s}\n",
		mode, theme.Background, theme.Foreground, theme.Muted, theme.Accent,
		theme.Border, theme.CodeBackground, theme.Selection)
	b.WriteString(codeCSS(theme.Dark))
	b.WriteString("</style>\n</head>\n<body>\n")
	b.WriteString(articleOpen)
	b.Write(doc.body)
	b.WriteString("</article>\n")
	fmt.Fprintf(&b, "<script src=\"%s\"></script>\n", asset(assetScript))
	b.WriteString("</body>\n</html>\n")
	return b.Bytes(), nil
}

// contentSecurityPolicy allows the page its assets and the document's own
// images, and nothing else: no inline script, no requests from scripts, no
// network. Mermaid adds inline styles, so styles may be inline.
func contentSecurityPolicy(assetBase string, withBase bool) string {
	assets, scheme := "'self'", ""
	if u, err := url.Parse(assetBase); err == nil && u.Scheme != "" {
		scheme = u.Scheme + ":"
		switch {
		case u.Scheme == "file" || u.Host == "":
			// A file: URL's origin is opaque, so only its scheme can name it.
			assets = scheme
		default:
			assets = u.Scheme + "://" + u.Host
		}
		if u.Scheme == "http" || u.Scheme == "https" {
			scheme = assets
		}
	}
	defaults := "'self'"
	if assets != "'self'" {
		defaults += " " + assets
	}
	images := "'self' data:"
	if scheme != "" {
		images += " " + scheme
	}
	base := "'none'"
	if withBase {
		base = "'self'"
		if scheme != "" {
			base += " " + scheme
		}
	}
	return strings.Join([]string{
		"default-src " + defaults,
		"script-src " + assets,
		"style-src " + assets + " 'unsafe-inline'",
		"img-src " + images,
		"font-src " + assets,
		"connect-src 'none'",
		"object-src 'none'",
		"form-action 'none'",
		"base-uri " + base,
	}, "; ")
}
