package present

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// hashedLevel is the deepest heading level that gets a data-bunk-hash.
const hashedLevel = 4

// The chroma styles of code blocks.
const (
	codeStyleDark  = "github-dark"
	codeStyleLight = "github"
)

// converter is shared: goldmark's parser and renderer keep no state between
// documents, and neither do the extensions here.
var converter = sync.OnceValue(func() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.Footnote, mathExtension{}),
		goldmark.WithParserOptions(
			parser.WithASTTransformers(util.Prioritized(docTransformer{}, 1000)),
		),
		goldmark.WithRendererOptions(
			renderer.WithNodeRenderers(util.Prioritized(docRenderer{}, 100)),
		),
	)
})

var titleKey = parser.NewContextKey()

// rendered is a Markdown document turned into the content of the page's
// article.
type rendered struct {
	body  []byte
	title string // the text of the first heading
}

func renderMarkdown(src []byte) (rendered, error) {
	md := converter()
	pc := parser.NewContext()
	doc := md.Parser().Parse(text.NewReader(src), parser.WithContext(pc))
	var b bytes.Buffer
	if err := md.Renderer().Render(&b, src, doc); err != nil {
		return rendered{}, fmt.Errorf("render markdown: %w", err)
	}
	title, _ := pc.Get(titleKey).(string)
	return rendered{body: b.Bytes(), title: title}, nil
}

// docTransformer gives headings GitHub's IDs and section hashes, and marks
// task lists as GitHub does.
type docTransformer struct{}

func (docTransformer) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	src := reader.Source()
	var heads []*ast.Heading
	ids := uniqueIDs{seen: map[string]bool{}, count: map[string]int{}}
	var visit func(n ast.Node)
	visit = func(n ast.Node) {
		switch n := n.(type) {
		case *ast.Heading:
			var label strings.Builder
			plainText(n, src, &label)
			name := strings.TrimSpace(label.String())
			if len(heads) == 0 {
				pc.Set(titleKey, name)
			}
			heads = append(heads, n)
			n.SetAttributeString("id", []byte(ids.next(slug(name))))
			return
		case *ast.Blockquote:
			callout(n, src)
		case *extast.TaskCheckBox:
			if item, ok := n.Parent().Parent().(*ast.ListItem); ok {
				item.SetAttributeString("class", []byte("task-list-item"))
				item.Parent().SetAttributeString("class", []byte("contains-task-list"))
			}
		}
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			visit(c)
		}
	}
	visit(doc)
	hashSections(heads, src)
}

// calloutTitles are GitHub's alerts, which a blockquote starting with
// [!NOTE] and the like becomes, by their marker.
var calloutTitles = map[string]string{
	"NOTE": "Note", "TIP": "Tip", "IMPORTANT": "Important", "WARNING": "Warning", "CAUTION": "Caution",
}

// callout makes a blockquote whose first line is "[!NOTE]", "[!TIP]",
// "[!IMPORTANT]", "[!WARNING]" or "[!CAUTION]" a callout, as GitHub does:
// class "bunk-callout note" and a title paragraph in place of the marker.
func callout(q *ast.Blockquote, src []byte) {
	p, ok := q.FirstChild().(*ast.Paragraph)
	if !ok || p.Lines().Len() == 0 {
		return
	}
	first := p.Lines().At(0)
	marker := strings.TrimSpace(string(first.Value(src)))
	kind, ok := strings.CutPrefix(marker, "[!")
	kind, ok2 := strings.CutSuffix(kind, "]")
	title, known := calloutTitles[strings.ToUpper(kind)]
	if !ok || !ok2 || !known {
		return
	}
	// Drop the marker's inline nodes, and the line break after it.
	for c := p.FirstChild(); c != nil; {
		next := c.NextSibling()
		if t, isText := c.(*ast.Text); isText && t.Segment.Start >= first.Stop {
			break
		} else if !isText {
			if _, isString := c.(*ast.String); !isString {
				break
			}
		}
		p.RemoveChild(p, c)
		c = next
	}
	if p.ChildCount() == 0 {
		q.RemoveChild(q, p)
	}
	head := ast.NewParagraph()
	head.SetAttributeString("class", []byte("bunk-callout-title"))
	head.AppendChild(head, ast.NewString([]byte(title)))
	q.InsertBefore(q, q.FirstChild(), head)
	q.SetAttributeString("class", []byte("bunk-callout "+strings.ToLower(kind)))
}

// hashSections sets data-bunk-hash on each heading up to hashedLevel: a
// short hash of the heading's own content, from the heading to the next
// heading of any level. An edit then marks only the heading above it, not
// every heading whose section holds it.
func hashSections(heads []*ast.Heading, src []byte) {
	starts := make([]int, len(heads))
	for i, h := range heads {
		starts[i] = headingStart(h, src)
	}
	for i, h := range heads {
		if h.Level > hashedLevel {
			continue
		}
		end := len(src)
		if i+1 < len(heads) {
			end = starts[i+1]
		}
		// Headings in footnotes move to the end of the document.
		if end < starts[i] {
			end = len(src)
		}
		sum := sha256.Sum256(bytes.TrimRightFunc(src[starts[i]:end], unicode.IsSpace))
		h.SetAttributeString("data-bunk-hash", []byte(hex.EncodeToString(sum[:6])))
	}
}

// headingStart returns the offset of the start of the line a heading
// begins on. A setext heading starts at its text, before its underline.
func headingStart(h *ast.Heading, src []byte) int {
	p := h.Pos()
	if h.Lines().Len() > 0 {
		if s := h.Lines().At(0).Start; p < 0 || s < p {
			p = s
		}
	}
	p = min(max(p, 0), len(src))
	for p > 0 && src[p-1] != '\n' {
		p--
	}
	return p
}

// plainText writes the text a reader sees in n's inline content.
func plainText(n ast.Node, src []byte, b *strings.Builder) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			b.Write(c.Segment.Value(src))
		case *ast.String:
			b.Write(c.Value)
		case *ast.AutoLink:
			b.Write(c.Label(src))
		case *ast.RawHTML:
			// The page drops raw HTML, so the ID leaves it out too.
		case *mathInline:
			b.Write(c.tex)
		default:
			plainText(c, src, b)
		}
	}
}

// slug turns a heading's text into an ID as GitHub does: lower case,
// letters, digits, '-' and '_' kept, spaces turned into '-', everything else
// dropped.
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// uniqueIDs numbers repeated slugs as GitHub does: "a", "a-1", "a-2".
type uniqueIDs struct {
	seen  map[string]bool
	count map[string]int
}

func (u *uniqueIDs) next(base string) string {
	if base == "" {
		base = "section"
	}
	id := base
	for u.seen[id] {
		u.count[base]++
		id = base + "-" + strconv.Itoa(u.count[base])
	}
	u.seen[id] = true
	return id
}

// docRenderer draws fenced code and math: Mermaid and math for the page
// runtime, other languages highlighted with CSS classes, so one body suits
// a light and a dark page.
type docRenderer struct{}

func (docRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindFencedCodeBlock, renderFencedCode)
	reg.Register(ast.KindRawHTML, renderRawHTML)
	reg.Register(ast.KindHTMLBlock, renderHTMLBlock)
	reg.Register(kindMathInline, renderMathInline)
	reg.Register(kindMathBlock, renderMathBlock)
}

func renderFencedCode(w util.BufWriter, src []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	n := node.(*ast.FencedCodeBlock)
	var code bytes.Buffer
	for i := range n.Lines().Len() {
		seg := n.Lines().At(i)
		code.Write(seg.Value(src))
	}
	lang := string(n.Language(src))
	var out bytes.Buffer
	switch strings.ToLower(lang) {
	case "mermaid":
		writeEscaped(&out, `<pre class="mermaid">`, code.Bytes(), "</pre>\n")
	case "math":
		writeEscaped(&out, `<div class="math math-display">`, code.Bytes(), "</div>\n")
	default:
		if err := highlight(&out, lang, code.String()); err != nil {
			return ast.WalkStop, err
		}
	}
	return writeOut(w, &out)
}

// writeOut writes out to w and skips the node's children, which the caller
// has drawn.
func writeOut(w util.BufWriter, out *bytes.Buffer) (ast.WalkStatus, error) {
	if _, err := w.Write(out.Bytes()); err != nil {
		return ast.WalkStop, fmt.Errorf("write html: %w", err)
	}
	return ast.WalkSkipChildren, nil
}

// writeEscaped writes content escaped between open and close.
func writeEscaped(b *bytes.Buffer, open string, content []byte, close string) {
	b.WriteString(open)
	b.WriteString(html.EscapeString(string(content)))
	b.WriteString(close)
}

// highlight writes code in a <pre class="chroma">, each token a span with
// chroma's class for its type. Unknown languages stay plain.
func highlight(b *bytes.Buffer, lang, code string) error {
	class := ""
	if lang != "" {
		class = ` class="language-` + html.EscapeString(lang) + `"`
	}
	var tokens []chroma.Token
	if lexer := lexers.Get(lang); lang != "" && lexer != nil {
		it, err := chroma.Coalesce(lexer).Tokenise(nil, code)
		if err != nil {
			return fmt.Errorf("highlight %s: %w", lang, err)
		}
		tokens = it.Tokens()
	}
	if tokens == nil {
		writeEscaped(b, "<pre><code"+class+">", []byte(code), "</code></pre>\n")
		return nil
	}
	b.WriteString(`<pre class="chroma"><code` + class + ">")
	for _, t := range tokens {
		value := html.EscapeString(t.Value)
		cls := tokenClass(t.Type)
		if cls == "" {
			b.WriteString(value)
			continue
		}
		b.WriteString(`<span class="` + cls + `">` + value + "</span>")
	}
	b.WriteString("</code></pre>\n")
	return nil
}

// tokenClass returns chroma's class for a token type, or "" for plain text.
func tokenClass(t chroma.TokenType) string {
	if t == chroma.Text || t == chroma.TextWhitespace || (t < 0 && t != chroma.Error) {
		return ""
	}
	return chroma.StandardTypes[t]
}

// codeCSS returns the rules for the token classes of highlighted code in
// the light or the dark code style.
func codeCSS(dark bool) string {
	if dark {
		return darkCodeCSS()
	}
	return lightCodeCSS()
}

var (
	darkCodeCSS  = sync.OnceValue(func() string { return styleCSS(codeStyleDark) })
	lightCodeCSS = sync.OnceValue(func() string { return styleCSS(codeStyleLight) })
)

// styleCSS writes a rule for every token class whose look differs from
// plain text, so the page's foreground colour stays the text colour and
// every class the highlighter emits has the style's look, inherited from
// its category where the style names none.
func styleCSS(name string) string {
	style := styles.Get(name)
	bg := style.Get(chroma.Background)
	plain := chromahtml.StyleEntryToCSS(style.Get(chroma.Text).Sub(bg))
	types := make([]chroma.TokenType, 0, len(chroma.StandardTypes))
	for t := range chroma.StandardTypes {
		if tokenClass(t) != "" {
			types = append(types, t)
		}
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	var b strings.Builder
	for _, t := range types {
		css := chromahtml.StyleEntryToCSS(style.Get(t).Sub(bg))
		if css == "" || css == plain {
			continue
		}
		fmt.Fprintf(&b, ".chroma .%s{%s}\n", tokenClass(t), css)
	}
	return b.String()
}
