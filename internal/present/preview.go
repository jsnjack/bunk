package present

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"html"
	"io"
	"net/url"
	"os"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// sniffBytes is how much of a file Preview reads to tell text from binary.
const sniffBytes = 8 << 10

// highlightBytes caps the text a preview highlights: highlighting takes
// about a second per MiB, so larger files show as plain text.
const highlightBytes = 1 << 20

// ErrNoPreview: the file is neither a document, an image, a PDF, video,
// audio nor text.
var ErrNoPreview = errors.New("not a document, image, PDF, video, audio or text file")

// Preview returns the kind of the file at path as bunk shows it: its kind
// by extension, else KindText when its first bytes are UTF-8 text without
// NUL bytes, else ErrNoPreview. An empty file is text.
func Preview(path string) (Kind, error) {
	if k := KindOf(path); k != KindNone {
		return k, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return KindNone, err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, sniffBytes)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return KindNone, err
	}
	if !isText(head[:n], n == sniffBytes) {
		return KindNone, ErrNoPreview
	}
	return KindText, nil
}

// isText reports whether b is UTF-8 text without NUL bytes. cut: b is the
// start of a longer file, so a character cut off at its end still counts.
func isText(b []byte, cut bool) bool {
	if bytes.IndexByte(b, 0) >= 0 {
		return false
	}
	for i := 0; cut && i < utf8.UTFMax-1 && len(b) > 0; i++ {
		if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size != 1 {
			break
		}
		b = b[:len(b)-1] // a byte of the character the sample cut
	}
	return utf8.Valid(b)
}

// previewClass is the article's class for each kind of preview page.
var previewClass = map[Kind]string{
	KindText:  "bunk-file bunk-text",
	KindImage: "bunk-file bunk-image",
	KindVideo: "bunk-file bunk-media",
	KindAudio: "bunk-file bunk-media",
}

// PreviewPage reports whether files of kind show through a page that
// RenderPreview makes: text, images, video and audio. Documents have pages
// of their own, and a PDF shows as it is.
func (k Kind) PreviewPage() bool {
	_, ok := previewClass[k]
	return ok
}

// RenderPreview returns the page that shows the file name, of kind, whose
// content is src (only text needs it): text highlighted by the file's name,
// an image, a video or audio player that loads the file from the page's
// own directory. PDF and documents have pages of their own.
func RenderPreview(name string, kind Kind, src []byte, opts Options) ([]byte, error) {
	return renderPreview(name, kind, src, opts, "")
}

func renderPreview(name string, kind Kind, src []byte, opts Options, baseHref string) ([]byte, error) {
	class, ok := previewClass[kind]
	if !ok {
		return nil, fmt.Errorf("no preview page for %s files", kind)
	}
	theme := opts.Theme.filled()
	file := html.EscapeString((&url.URL{Path: name}).EscapedPath())
	var body bytes.Buffer
	switch kind {
	case KindText:
		if err := highlightFile(&body, name, src); err != nil {
			return nil, err
		}
	case KindImage:
		fmt.Fprintf(&body, `<img src="%s" alt="%s">`+"\n", file, html.EscapeString(name))
	case KindVideo:
		fmt.Fprintf(&body, `<video controls src="%s"></video>`+"\n", file)
	case KindAudio:
		fmt.Fprintf(&body, `<audio controls src="%s"></audio>`+"\n", file)
	}
	asset := func(n string) string { return html.EscapeString(opts.AssetBase + n) }
	var b bytes.Buffer
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n")
	fmt.Fprintf(&b, "<meta http-equiv=\"Content-Security-Policy\" content=\"%s\">\n",
		html.EscapeString(contentSecurityPolicy(opts.AssetBase, baseHref != "")))
	b.WriteString("<meta charset=\"utf-8\">\n")
	if baseHref != "" {
		fmt.Fprintf(&b, "<base href=\"%s\">\n", html.EscapeString(baseHref))
	}
	fmt.Fprintf(&b, "<title>%s</title>\n", html.EscapeString(cmp.Or(opts.Title, name)))
	fmt.Fprintf(&b, "<link rel=\"stylesheet\" href=\"%s\">\n", asset(assetStyle))
	if opts.Live {
		fmt.Fprintf(&b, "<script src=\"%s\" defer></script>\n", asset(assetLive))
	}
	b.WriteString("<style>\n")
	b.WriteString(pageCSS(theme, opts.System, kind == KindText))
	b.WriteString("</style>\n</head>\n<body>\n")
	fmt.Fprintf(&b, "<article id=\"bunk-doc\" class=\"%s\">\n", class)
	b.Write(body.Bytes())
	b.WriteString("</article>\n</body>\n</html>\n")
	return b.Bytes(), nil
}

// highlightFile writes the text src of the file name in a <pre>, its tokens
// classed by the lexer that the name picks, or plain past highlightBytes or
// when no lexer knows the name.
func highlightFile(b *bytes.Buffer, name string, src []byte) error {
	lexer := lexers.Match(name)
	if lexer == nil || len(src) > highlightBytes {
		writeEscaped(b, "<pre><code>", src, "</code></pre>\n")
		return nil
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, string(src))
	if err != nil {
		return fmt.Errorf("highlight %s: %w", name, err)
	}
	b.WriteString(`<pre class="chroma"><code>`)
	for _, t := range it.Tokens() {
		value := html.EscapeString(t.Value)
		if cls := tokenClass(t.Type); cls != "" {
			b.WriteString(`<span class="` + cls + `">` + value + "</span>")
			continue
		}
		b.WriteString(value)
	}
	b.WriteString("</code></pre>\n")
	return nil
}
