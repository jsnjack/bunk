package present

import (
	"bytes"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// mathExtension parses TeX between dollars: $...$ inline, and $$...$$
// display math, inline or as a block of its own lines. The parsers take
// the TeX whole, so emphasis and escapes never touch it.
type mathExtension struct{}

func (mathExtension) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(
		parser.WithBlockParsers(util.Prioritized(mathBlockParser{}, 701)),
		parser.WithInlineParsers(util.Prioritized(mathInlineParser{}, 501)),
	)
}

var (
	kindMathInline = ast.NewNodeKind("MathInline")
	kindMathBlock  = ast.NewNodeKind("MathBlock")
)

var mathFence = []byte("$$")

// mathInline is TeX in a paragraph: $...$, or $$...$$ for display math.
type mathInline struct {
	ast.BaseInline
	tex     []byte
	display bool
}

func (n *mathInline) Kind() ast.NodeKind { return kindMathInline }

func (n *mathInline) Dump(src []byte, level int) {
	ast.DumpHelper(n, src, level, map[string]string{"TeX": string(n.tex)}, nil)
}

// mathBlock is display math on lines of its own between $$ lines, or one
// $$...$$ line. Its Lines hold the TeX.
type mathBlock struct {
	ast.BaseBlock
	closed bool
}

func (n *mathBlock) Kind() ast.NodeKind { return kindMathBlock }

func (n *mathBlock) IsRaw() bool { return true }

func (n *mathBlock) Dump(src []byte, level int) {
	ast.DumpHelper(n, src, level, nil, nil)
}

type mathBlockParser struct{}

func (mathBlockParser) Trigger() []byte { return []byte{'$'} }

// Open starts a block at a line that holds only "$$", or a whole
// "$$...$$" with exactly two dollars on each side. Any other line starting
// with $$ is left to the paragraph, so text like "$$5 off" never swallows
// the lines after it.
func (mathBlockParser) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	line, segment := reader.PeekLine()
	pos := pc.BlockOffset()
	if pos < 0 || !bytes.HasPrefix(line[pos:], mathFence) {
		return nil, parser.NoChildren
	}
	rest := util.TrimRightSpace(line[pos+len(mathFence):])
	node := &mathBlock{}
	if len(rest) == 0 {
		reader.AdvanceToEOL()
		return node, parser.NoChildren
	}
	inner, ok := bytes.CutSuffix(rest, mathFence)
	if !ok || util.IsBlank(inner) || inner[0] == '$' || inner[len(inner)-1] == '$' || bytes.Contains(inner, mathFence) {
		return nil, parser.NoChildren
	}
	start := segment.Start - segment.Padding + pos + len(mathFence)
	node.Lines().Append(text.NewSegment(start, start+len(inner)))
	node.closed = true
	reader.AdvanceToEOL()
	return node, parser.NoChildren
}

// Continue adds lines until one that ends with $$; TeX before the $$ on
// that line belongs to the block.
func (mathBlockParser) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	n := node.(*mathBlock)
	if n.closed {
		return parser.Close
	}
	line, segment := reader.PeekLine()
	if line == nil {
		return parser.Close
	}
	if inner, ok := bytes.CutSuffix(util.TrimRightSpace(line), mathFence); ok {
		if !util.IsBlank(inner) {
			stop := segment.Start - segment.Padding + len(inner)
			n.Lines().Append(text.NewSegmentPadding(segment.Start, stop, segment.Padding))
		}
		reader.AdvanceToEOL()
		n.closed = true
		return parser.Close
	}
	n.Lines().Append(segment)
	reader.AdvanceToEOL()
	return parser.Continue | parser.NoChildren
}

func (mathBlockParser) Close(node ast.Node, reader text.Reader, pc parser.Context) {}

// CanInterruptParagraph is false, so a $$ line inside a paragraph closes
// that paragraph's inline $$ rather than opening a block that runs to the
// end of the document.
func (mathBlockParser) CanInterruptParagraph() bool { return false }

func (mathBlockParser) CanAcceptIndentedLine() bool { return false }

type mathInlineParser struct{}

func (mathInlineParser) Trigger() []byte { return []byte{'$'} }

// Parse reads $...$ or $$...$$, across lines of the paragraph. Single
// dollars follow the rules that keep prices out of math: the opening $ has
// no space after it, the closing $ has no space before it and no digit
// after it, an unescaped $ between them that cannot close ends the attempt,
// and digits alone are an amount. So "$5 and $6" stays text, also before
// "$x$" in the same paragraph. A backslash escapes the next character, so
// \$ never closes. On failure goldmark rewinds the reader and keeps the
// dollar as text.
func (mathInlineParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	if block.PrecendingCharacter() == '$' {
		return nil
	}
	line, _ := block.PeekLine()
	open := 0
	for open < len(line) && line[open] == '$' {
		open++
	}
	if open > len(mathFence) || open == len(line) || (open == 1 && util.IsSpace(line[open])) {
		return nil
	}
	block.Advance(open)
	var tex []byte
	for {
		line, _ := block.PeekLine()
		if line == nil {
			return nil
		}
		for i := 0; i < len(line); i++ {
			switch line[i] {
			case '\\':
				i++
			case '$':
				if !closesMath(line, i, open, tex) {
					if open == 1 {
						return nil
					}
					continue
				}
				tex = append(tex, line[:i]...)
				if open == 1 && isAmount(tex) {
					return nil
				}
				block.Advance(i + open)
				return &mathInline{tex: tex, display: open == len(mathFence)}
			}
		}
		tex = append(tex, line...)
		block.AdvanceLine()
	}
}

// closesMath reports whether the $ at line[i] closes math opened with open
// dollars, after the TeX of earlier lines.
func closesMath(line []byte, i, open int, earlier []byte) bool {
	if open == len(mathFence) {
		if i+1 >= len(line) || line[i+1] != '$' {
			return false
		}
		return !util.IsBlank(earlier) || !util.IsBlank(line[:i])
	}
	before := byte('\n')
	if i > 0 {
		before = line[i-1]
	} else if len(earlier) > 0 {
		before = earlier[len(earlier)-1]
	}
	if util.IsSpace(before) {
		return false
	}
	return i+1 >= len(line) || !isDigit(line[i+1])
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// isAmount reports whether tex holds only digits, separators and spaces.
func isAmount(tex []byte) bool {
	for _, c := range tex {
		if !isDigit(c) && c != '.' && c != ',' && !util.IsSpace(c) {
			return false
		}
	}
	return true
}

func renderMathInline(w util.BufWriter, src []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	n := node.(*mathInline)
	class := "math math-inline"
	if n.display {
		class = "math math-display"
	}
	var out bytes.Buffer
	writeEscaped(&out, `<span class="`+class+`">`, n.tex, "</span>")
	return writeOut(w, &out)
}

func renderMathBlock(w util.BufWriter, src []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	n := node.(*mathBlock)
	var tex bytes.Buffer
	for i := range n.Lines().Len() {
		seg := n.Lines().At(i)
		tex.Write(seg.Value(src))
	}
	var out bytes.Buffer
	writeEscaped(&out, `<div class="math math-display">`, tex.Bytes(), "</div>\n")
	return writeOut(w, &out)
}
