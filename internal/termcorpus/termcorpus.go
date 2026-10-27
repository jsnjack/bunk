// Package termcorpus builds terminal output streams for benchmarks. Each kind
// imitates one program's output; the same kind and size always give the same
// bytes.
package termcorpus

import (
	"bytes"
	_ "embed"
	"fmt"
	"go/scanner"
	"go/token"
	"strings"
)

// Kinds lists the streams Generate builds.
var Kinds = []string{"claude", "gosyntax", "boxdraw", "cjk", "emoji"}

// goSource is a copy of vt10x's csi.go, frozen so the gosyntax stream does
// not change when the parser does.
//
//go:embed testdata/csi.go.txt
var goSource []byte

// Generate returns at least size bytes of one kind of output:
//
//   - claude: a coding-agent transcript. Truecolour bullets, bold and dim
//     words, per-character spinner colours, diff lines with truecolour
//     backgrounds, a box-drawn prompt and in-place redraws.
//   - gosyntax: Go source as a syntax highlighter prints it. Every token gets
//     its own 24-bit colour, after a line-number gutter.
//   - boxdraw: a system monitor redrawing its panels. Absolute cursor
//     moves, rounded boxes, braille and block graphs in gradient colours.
//   - cjk: Japanese, Chinese and Korean prose with some ASCII.
//   - emoji: status lines with emoji, including skin tones, ZWJ sequences,
//     flags, keycaps and variation selectors.
func Generate(kind string, size int) []byte {
	var b strings.Builder
	for i := 0; b.Len() < size; i++ {
		switch kind {
		case "claude":
			claude(&b, i)
		case "gosyntax":
			goSyntax(&b, i)
		case "boxdraw":
			boxDraw(&b, i)
		case "cjk":
			cjk(&b, i)
		case "emoji":
			emoji(&b, i)
		default:
			panic("termcorpus: unknown kind " + kind)
		}
	}
	return []byte(b.String())
}

func fg(b *strings.Builder, r, g, bl int) { fmt.Fprintf(b, "\x1b[38;2;%d;%d;%dm", r, g, bl) }
func bg(b *strings.Builder, r, g, bl int) { fmt.Fprintf(b, "\x1b[48;2;%d;%d;%dm", r, g, bl) }

var proseWords = strings.Fields(`the parser spends most of its time in grapheme segmentation
which runs for every rune even when the rune cannot join the previous cell so we cache
the width and class of each code point and write runs of simple runes directly while
escape sequences are scanned in one pass over the buffer`)

var codeWords = []string{"extendGrapheme", "putASCII", "setAttr", "runeCellWidth", "csi.parse", "go test ./...", "Write"}

// claude writes one turn of an agent transcript.
func claude(b *strings.Builder, i int) {
	// Spinner line, one colour per character, redrawn in place.
	b.WriteString("\x1b[2K\x1b[1A\x1b[2K\x1b[G")
	fg(b, 215, 119, 87)
	b.WriteString("✻\x1b[39m ")
	for k, ch := range "Thinking… " {
		shade := (i*3 + k*17) % 60
		fg(b, 215+shade/2, 119+shade, 87+shade)
		b.WriteRune(ch)
		b.WriteString("\x1b[39m")
	}
	fmt.Fprintf(b, "\x1b[2m(%ds · ↑ %d.%dk tokens · esc to interrupt)\x1b[22m\r\n", i%300, i%9, i%10)

	// Assistant prose: bold words and inline code in their own colours.
	fg(b, 255, 255, 255)
	b.WriteString("⏺\x1b[39m ")
	for k := 0; k < 24; k++ {
		w := proseWords[(i*5+k)%len(proseWords)]
		switch (i + k) % 7 {
		case 2:
			fmt.Fprintf(b, "\x1b[1m%s\x1b[22m ", w)
		case 5:
			fg(b, 177, 185, 249)
			fmt.Fprintf(b, "%s\x1b[39m ", codeWords[(i*3+k/7)%len(codeWords)])
		default:
			b.WriteString(w)
			b.WriteByte(' ')
		}
	}
	b.WriteString("\r\n\r\n")

	// A tool call with a coloured diff.
	fg(b, 78, 186, 101)
	fmt.Fprintf(b, "⏺\x1b[39m \x1b[1mUpdate\x1b[22m(internal/vt10x/parse_%d.go)\r\n", i%13)
	fmt.Fprintf(b, "  ⎿  Updated \x1b[1minternal/vt10x/parse_%d.go\x1b[22m with \x1b[1m%d\x1b[22m additions and \x1b[1m%d\x1b[22m removals\r\n", i%13, 2+i%9, 1+i%4)
	for k := 0; k < 6; k++ {
		fg(b, 110, 110, 110)
		fmt.Fprintf(b, "      %4d\x1b[39m ", 40+i%200+k)
		switch k % 3 {
		case 0:
			bg(b, 34, 92, 43)
			fg(b, 255, 255, 255)
			b.WriteString("+\x1b[39m    if n := t.putText(p[i:]); n > 0 {")
		case 1:
			bg(b, 122, 41, 54)
			fg(b, 255, 255, 255)
			b.WriteString("-\x1b[39m    if n := t.putASCII(p[i:]); n > 0 {")
		default:
			b.WriteString("         i += n")
		}
		b.WriteString("\x1b[49m\r\n")
	}
	b.WriteString("\r\n")

	// The prompt box, redrawn after every turn.
	const inner = 96
	fg(b, 136, 136, 136)
	b.WriteString("╭" + strings.Repeat("─", inner) + "╮\x1b[39m\r\n")
	fg(b, 136, 136, 136)
	b.WriteString("│\x1b[39m > \x1b[7m \x1b[27m" + strings.Repeat(" ", inner-4))
	fg(b, 136, 136, 136)
	b.WriteString("│\x1b[39m\r\n")
	fg(b, 136, 136, 136)
	b.WriteString("╰" + strings.Repeat("─", inner) + "╯\x1b[39m\r\n")
	b.WriteString("  \x1b[2m? for shortcuts\x1b[22m\r\n")
}

// goSyntax writes the embedded Go source once, coloured token by token
// like bat with a 24-bit theme.
func goSyntax(b *strings.Builder, _ int) {
	fset := token.NewFileSet()
	file := fset.AddFile("csi.go", -1, len(goSource))
	var s scanner.Scanner
	s.Init(file, goSource, nil, scanner.ScanComments)
	line, last := 0, 0
	gutter := func() {
		line++
		fg(b, 117, 113, 94)
		fmt.Fprintf(b, "%4d\x1b[0m ", line)
		fg(b, 117, 113, 94)
		b.WriteString("│\x1b[0m ")
	}
	// text writes source between tokens, starting a gutter on every line.
	text := func(src []byte) {
		for len(src) > 0 {
			k := bytes.IndexByte(src, '\n')
			if k < 0 {
				b.Write(src)
				return
			}
			b.Write(src[:k])
			b.WriteString("\x1b[0m\r\n")
			src = src[k+1:]
			gutter()
		}
	}
	gutter()
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		off := file.Offset(pos)
		if tok == token.SEMICOLON && lit == "\n" {
			continue // an inserted semicolon is the newline itself
		}
		text(goSource[last:off])
		end := off + len(lit)
		if lit == "" {
			end = off + len(tok.String())
		}
		switch {
		case tok == token.COMMENT:
			fg(b, 117, 113, 94)
		case tok.IsKeyword():
			fg(b, 249, 38, 114)
		case tok == token.STRING || tok == token.CHAR:
			fg(b, 230, 219, 116)
		case tok == token.INT || tok == token.FLOAT:
			fg(b, 190, 132, 255)
		case tok == token.IDENT && end < len(goSource) && goSource[end] == '(':
			fg(b, 166, 226, 46)
		case tok == token.IDENT:
			fg(b, 248, 248, 242)
		default:
			fg(b, 249, 38, 114)
		}
		text(goSource[off:end])
		last = end
	}
	text(goSource[last:])
	b.WriteString("\x1b[0m\r\n")
}

var (
	braille = []rune("⠀⣀⣄⣤⣦⣶⣷⣿")
	blocks  = []rune(" ▁▂▃▄▅▆▇█")
)

// boxDraw writes one frame of a system monitor in a 120x40 area.
func boxDraw(b *strings.Builder, i int) {
	const w = 120
	b.WriteString("\x1b[?2026h\x1b[H")
	panel := func(row, height int, title string) {
		fmt.Fprintf(b, "\x1b[%d;1H\x1b[38;5;%dm╭─\x1b[1m%s\x1b[22m%s╮", row, 60+i%8, title, strings.Repeat("─", w-4-len(title)))
		for y := 1; y < height-1; y++ {
			fmt.Fprintf(b, "\x1b[%d;1H│\x1b[%d;%dH│", row+y, row+y, w)
		}
		fmt.Fprintf(b, "\x1b[%d;1H╰%s╯\x1b[0m", row+height-1, strings.Repeat("─", w-2))
	}
	panel(1, 12, "cpu")
	// CPU graph: braille cells in a green-to-red gradient, colour per cell.
	for y := 0; y < 10; y++ {
		fmt.Fprintf(b, "\x1b[%d;3H", 2+y)
		for x := 0; x < w-4; x++ {
			v := (x*7 + y*3 + i*11) % len(braille)
			fg(b, 80+v*20, 200-v*18, 80)
			b.WriteRune(braille[v])
		}
	}
	panel(13, 28, "proc")
	fmt.Fprintf(b, "\x1b[14;3H\x1b[1;38;5;250m%-8s %-10s %6s %-12s %s\x1b[0m", "pid", "user", "cpu%", "usage", "command")
	for y := 0; y < 25; y++ {
		pid := 1000 + (i*37+y*101)%9000
		cpu := (i*13 + y*29) % 1000
		fmt.Fprintf(b, "\x1b[%d;3H\x1b[38;5;252m%-8d \x1b[38;5;110m%-10s \x1b[38;5;%dm%3d.%d ", 15+y, pid, "jsn", 150+cpu/50, cpu/10, cpu%10)
		for x := 0; x < 12; x++ {
			v := min(len(blocks)-1, max(0, cpu/80-x))
			fg(b, 100+x*12, 220-x*10, 120)
			b.WriteRune(blocks[v])
		}
		fmt.Fprintf(b, " \x1b[38;5;252m%-40s\x1b[0m", []string{"bunk --trace", "claude", "go test ./...", "btop", "nvim parse.go"}[(i+y)%5])
	}
	b.WriteString("\x1b[?2026l")
}

var cjkLines = []string{
	"日本語のテキストを表示するテストです。端末エミュレータは全角文字を二列で描画します。",
	"中文文本：终端模拟器必须正确处理宽字符、换行和光标位置，否则界面会错位。",
	"한국어 텍스트: 터미널 에뮬레이터는 한글 음절을 두 칸 너비로 그려야 합니다.",
	"ログ出力（エラー 3 件、警告 12 件）：ファイル「設定.toml」を読み込めませんでした。",
	"构建完成，用时 2.4 秒；共编译 128 个文件，生成 3 个二进制文件。",
}

// cjk writes one line of East Asian prose.
func cjk(b *strings.Builder, i int) {
	fmt.Fprintf(b, "%05d %s\r\n", i, cjkLines[i%len(cjkLines)])
}

var emojiLines = []string{
	"✅ 128 tests passed 🎉 in 4.2s",
	"❌ 2 tests failed: TestParse, TestResize ⚠\ufe0f see log",
	"🚀 Deploying build 👍\U0001f3fd to staging … 📦 3 artefacts",
	"👨\u200d👩\u200d👧 family plan · \U0001f1f3\U0001f1f1 Amsterdam · \U0001f1ef\U0001f1f5 Tokyo · ❤\ufe0f saved",
	"🔧 fix: keep 1\ufe0f\u20e3 keycaps and #\ufe0f\u20e3 together ✨ 🐛 → ✓",
	"● running  ◐ waiting  ✗ failed  ✓ done  ★ starred  ☕ break",
}

// emoji writes one status line with emoji.
func emoji(b *strings.Builder, i int) {
	fmt.Fprintf(b, "%05d %s\r\n", i, emojiLines[i%len(emojiLines)])
}
