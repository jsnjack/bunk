package present

import (
	"bytes"
	"html"
	"regexp"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/util"
)

// Raw HTML in Markdown passes only for a few tags that GitHub's READMEs
// use for structure and that carry nothing but their meaning: no
// attributes, so no scripts, styles or addresses, except "open" on
// <details>. Other tags are dropped and their text kept; the content of
// elements that hold code or other documents is dropped with them.
var allowedTags = map[string]bool{
	"details": true, "summary": true, "kbd": true, "sub": true, "sup": true,
	"mark": true, "ins": true, "del": true, "s": true, "small": true,
	"br": true, "hr": true, "dl": true, "dt": true, "dd": true,
}

// voidTags have no closing tag.
var voidTags = map[string]bool{"br": true, "hr": true}

// droppedContent are the elements whose content goes with them.
var droppedContent = map[string]bool{
	"script": true, "style": true, "iframe": true, "object": true, "embed": true,
	"template": true, "textarea": true, "title": true, "noscript": true, "svg": true, "math": true,
}

// rawTag matches a comment or a tag with its attributes.
var rawTag = regexp.MustCompile(`(?s)<!--.*?-->|<(/?)([a-zA-Z][a-zA-Z0-9-]*)((?:\s+[^\s/>"'=]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'=<>` + "`" + `]+))?)*)\s*/?>`)

// safeHTML returns raw HTML with only the allowed tags, attribute-free but
// for <details open>, and the text between tags escaped.
func safeHTML(raw []byte) []byte {
	var b bytes.Buffer
	text := func(t []byte) { b.WriteString(html.EscapeString(html.UnescapeString(string(t)))) }
	last := 0
	skipUntil := "" // the closing tag that ends dropped content
	for _, m := range rawTag.FindAllSubmatchIndex(raw, -1) {
		if skipUntil == "" {
			text(raw[last:m[0]])
		}
		last = m[1]
		if m[4] < 0 {
			continue // a comment
		}
		closing := m[3] > m[2]
		name := strings.ToLower(string(raw[m[4]:m[5]]))
		if skipUntil != "" {
			if closing && name == skipUntil {
				skipUntil = ""
			}
			continue
		}
		if droppedContent[name] && !closing {
			skipUntil = name
			continue
		}
		if !allowedTags[name] {
			continue
		}
		attrs := strings.ToLower(strings.TrimSpace(string(raw[m[6]:m[7]])))
		switch {
		case closing && voidTags[name]:
		case closing:
			b.WriteString("</" + name + ">")
		case name == "details" && attrs == "open":
			b.WriteString("<details open>")
		default:
			b.WriteString("<" + name + ">")
		}
	}
	if skipUntil == "" {
		text(raw[last:])
	}
	return b.Bytes()
}

// renderRawHTML draws inline raw HTML through safeHTML.
func renderRawHTML(w util.BufWriter, src []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	n := node.(*ast.RawHTML)
	var raw bytes.Buffer
	for i := range n.Segments.Len() {
		seg := n.Segments.At(i)
		raw.Write(seg.Value(src))
	}
	out := bytes.NewBuffer(safeHTML(raw.Bytes()))
	return writeOut(w, out)
}

// renderHTMLBlock draws a block of raw HTML through safeHTML.
func renderHTMLBlock(w util.BufWriter, src []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	n := node.(*ast.HTMLBlock)
	var raw bytes.Buffer
	for i := range n.Lines().Len() {
		seg := n.Lines().At(i)
		raw.Write(seg.Value(src))
	}
	if n.HasClosure() {
		raw.Write(n.ClosureLine.Value(src))
	}
	out := bytes.NewBuffer(safeHTML(raw.Bytes()))
	return writeOut(w, out)
}
