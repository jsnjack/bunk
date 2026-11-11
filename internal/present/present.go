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

// Kind is the kind of file present shows.
type Kind int

// The kinds of files. Markdown and HTML are documents, which agents write
// for the user; the others are files the user previews.
const (
	KindNone     Kind = iota
	KindMarkdown      // .md .markdown .mdown .mkd
	KindHTML          // .html .htm
	KindImage         // .png .jpg .jpeg .gif .webp .avif .bmp .ico .svg
	KindPDF           // .pdf
	KindVideo         // .mp4 .m4v .webm .ogv .mov
	KindAudio         // .mp3 .ogg .oga .opus .wav .flac .m4a .aac
	KindText          // any other UTF-8 text: code, data, logs (Preview)
)

// KindOf returns the kind of the file at path by its extension in any
// case. Text has no extensions of its own, so KindOf never returns
// KindText; Preview looks inside the file.
func KindOf(path string) Kind {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return KindMarkdown
	case ".html", ".htm":
		return KindHTML
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".bmp", ".ico", ".svg":
		return KindImage
	case ".pdf":
		return KindPDF
	case ".mp4", ".m4v", ".webm", ".ogv", ".mov":
		return KindVideo
	case ".mp3", ".ogg", ".oga", ".opus", ".wav", ".flac", ".m4a", ".aac":
		return KindAudio
	}
	return KindNone
}

// Document reports whether k is a document: Markdown or HTML, what agents
// write for the user to read. Hooks offer only documents.
func (k Kind) Document() bool { return k == KindMarkdown || k == KindHTML }

// String names k as --report and the docs do.
func (k Kind) String() string {
	switch k {
	case KindMarkdown:
		return "markdown"
	case KindHTML:
		return "html"
	case KindImage:
		return "image"
	case KindPDF:
		return "pdf"
	case KindVideo:
		return "video"
	case KindAudio:
		return "audio"
	case KindText:
		return "text"
	}
	return "none"
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

	// System makes the page follow the colour scheme the browser asks for
	// (prefers-color-scheme), as a page a web browser opens should:
	// Theme for its own darkness, bunk's default theme for the other
	// (systemThemes). Without it the page keeps Theme, as in the panel.
	System bool
}

// The page's own assets. The runtime loads KaTeX's and Mermaid's scripts
// itself, only for documents that need them.
const (
	assetScript   = "bunk-doc.js"
	assetStyle    = "bunk-doc.css"
	assetKatexCSS = "katex/katex.min.css"
)

// articleOpen starts the element whose content RenderBody returns. Its
// class tells the page runtime that the page is Markdown, whose tables
// sort without markup of their own.
const articleOpen = `<article id="bunk-doc" class="bunk-md">`

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
	// The page names its theme for the runtime, unless it follows the
	// system: then the runtime reads --bunk-dark, which the scheme sets.
	mode := ""
	switch {
	case opts.System:
	case theme.Dark:
		mode = ` data-bunk-theme="dark"`
	default:
		mode = ` data-bunk-theme="light"`
	}
	asset := func(name string) string { return html.EscapeString(opts.AssetBase + name) }

	var b bytes.Buffer
	fmt.Fprintf(&b, "<!doctype html>\n<html lang=\"en\"%s>\n<head>\n", mode)
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
	b.WriteString(pageCSS(theme, opts.System, true))
	b.WriteString("</style>\n</head>\n<body>\n")
	b.WriteString(articleOpen)
	b.Write(doc.body)
	b.WriteString("</article>\n")
	fmt.Fprintf(&b, "<script src=\"%s\"></script>\n", asset(assetScript))
	b.WriteString("</body>\n</html>\n")
	return b.Bytes(), nil
}

// ThemeCSS is the rule that gives a page the theme's colours as custom
// properties on :root (--bunk-bg, --bunk-fg, --bunk-muted, --bunk-accent,
// --bunk-border, --bunk-code-bg, --bunk-selection; --bunk-dark is 1 for a
// dark theme, else 0) and its color-scheme, so unstyled text and controls
// suit the background.
func ThemeCSS(t Theme) string {
	t = t.filled()
	mode, dark := "light", 0
	if t.Dark {
		mode, dark = "dark", 1
	}
	return fmt.Sprintf(":root{color-scheme:%s;--bunk-dark:%d;--bunk-bg:%s;--bunk-fg:%s;--bunk-muted:%s;--bunk-accent:%s;"+
		"--bunk-border:%s;--bunk-code-bg:%s;--bunk-selection:%s}\n",
		mode, dark, t.Background, t.Foreground, t.Muted, t.Accent, t.Border, t.CodeBackground, t.Selection)
}

// The media queries of the system's colour schemes. Print takes the light
// scheme, as paper is white and browsers leave out backgrounds.
const (
	lightScheme = "@media print, (prefers-color-scheme: light)"
	darkScheme  = "@media screen and (prefers-color-scheme: dark)"
)

// systemThemes returns the light and the dark theme of a page that follows
// the browser's colour scheme: t for its own darkness, bunk's default
// theme for the other.
func systemThemes(t Theme) (light, dark Theme) {
	t = t.filled()
	if t.Dark {
		return lightTheme(), t
	}
	return t, DefaultTheme()
}

// pageCSS returns a page's colour rules: ThemeCSS(t) and, with code, the
// rules of highlighted code. With system, the page follows the colour
// scheme the browser asks for, between the themes of systemThemes.
func pageCSS(t Theme, system, code bool) string {
	if !system {
		t = t.filled()
		css := ThemeCSS(t)
		if code {
			css += codeCSS(t.Dark)
		}
		return css
	}
	light, dark := systemThemes(t)
	var b strings.Builder
	b.WriteString(ThemeCSS(light))
	b.WriteString(darkScheme + "{\n" + ThemeCSS(dark) + "}\n")
	if code {
		// Each code style only in its own scheme: a token class that one
		// style leaves plain must not keep the other style's colour.
		b.WriteString(lightScheme + "{\n" + codeCSS(false) + "}\n")
		b.WriteString(darkScheme + "{\n" + codeCSS(true) + "}\n")
	}
	return b.String()
}

var (
	headTag    = regexp.MustCompile(`(?i)<head(\s[^>]*)?>`)
	htmlTag    = regexp.MustCompile(`(?i)<html(\s[^>]*)?>`)
	doctypeTag = regexp.MustCompile(`(?i)^\s*<!doctype[^>]*>`)
)

// InsertHead puts markup first into the page's <head>, so it comes before
// anything of the page's own: a policy holds for all of it, and the page's
// styles override the ones inserted. A page without a <head> gets one.
func InsertHead(page []byte, markup string) []byte {
	at, wrap := 0, true // no tags: a <head> first
	if loc := headTag.FindIndex(page); loc != nil {
		at, wrap = loc[1], false
	} else if loc := htmlTag.FindIndex(page); loc != nil {
		at = loc[1]
	} else if loc := doctypeTag.FindIndex(page); loc != nil {
		at = loc[1]
	}
	if wrap {
		markup = "<head>" + markup + "</head>"
	}
	out := make([]byte, 0, len(page)+len(markup))
	out = append(out, page[:at]...)
	out = append(out, markup...)
	return append(out, page[at:]...)
}

// contentSecurityPolicy allows the page its assets and the document's own
// images, and nothing else: no inline script, no requests from scripts, no
// network. Mermaid adds inline styles, so styles may be inline, and
// Graphviz compiles WebAssembly ('wasm-unsafe-eval', which allows no
// eval of JavaScript).
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
		"script-src " + assets + " 'wasm-unsafe-eval'",
		"style-src " + assets + " 'unsafe-inline'",
		"img-src " + images,
		"font-src " + assets,
		"connect-src 'none'",
		"object-src 'none'",
		"form-action 'none'",
		"base-uri " + base,
	}, "; ")
}
