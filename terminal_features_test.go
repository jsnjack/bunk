package main

// End-to-end tests for the groups of terminal_features.sh. Each test runs
// the group's script in a bunk pane (features_harness_test.go) and checks
// what a person would judge by eye: painted text, widths, styles and
// colours, the cursor, replies the script prints, and bytes sent to the
// host terminal.

import (
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"bunk/internal/vt10x"

	"github.com/gdamore/tcell/v2"
)

// plainStyle is the style of unstyled text with testTheme.
func plainStyle() tcell.Style {
	rt := testTheme()
	return tcell.StyleDefault.Foreground(rt.fg).Background(rt.bg)
}

// themeColor is the colour bunk paints for palette index i with testTheme:
// the theme's entry for 0-15 when it sets one, otherwise the host palette.
func themeColor(i int) tcell.Color {
	rt := testTheme()
	if i < 16 && rt.palette[i] != tcell.ColorDefault {
		return rt.palette[i]
	}
	return tcell.PaletteColor(i)
}

// dimmed is fg blended halfway toward the theme background, as bunk paints
// SGR 2 on RGB colours.
func dimmed(fg tcell.Color) tcell.Color {
	r1, g1, b1 := fg.RGB()
	r2, g2, b2 := testTheme().bg.RGB()
	return tcell.NewRGBColor((r1+r2)/2, (g1+g2)/2, (b1+b2)/2)
}

// checkStyles fails unless every cell has style want.
func checkStyles(t *testing.T, what string, cells []screenCell, want tcell.Style) {
	t.Helper()
	for _, c := range cells {
		if c.style != want {
			t.Errorf("%s: cell %d %q has style %+v, want %+v", what, c.x, c.text, c.style, want)
			return
		}
	}
}

// wantCell is the text and width bunk should paint in one cell.
type wantCell struct {
	text  string
	width int
}

// checkCells fails unless row shows want from screen column x on, each cell
// at the column after the previous cell's width.
func checkCells(t *testing.T, what string, row []screenCell, x int, want []wantCell) {
	t.Helper()
	i := slices.IndexFunc(row, func(c screenCell) bool { return c.x == x })
	if i < 0 || len(row)-i < len(want) {
		t.Errorf("%s: no cells from column %d", what, x)
		return
	}
	for k, w := range want {
		c := row[i+k]
		if c.x != x || c.text != w.text || c.width != w.width {
			t.Errorf("%s: cell %d = %+q width %d at column %d, want %+q width %d at column %d",
				what, k, c.text, c.width, c.x, w.text, w.width, x)
			return
		}
		x += w.width
	}
}

// narrow returns one-column cells for each rune of s.
func narrow(s string) []wantCell {
	var cells []wantCell
	for _, r := range s {
		cells = append(cells, wantCell{string(r), 1})
	}
	return cells
}

// TestTerminalFeaturesText replaces judging `terminal_features.sh text` by
// eye: each SGR attribute and underline style reaches its own words, and
// nothing leaks into the labels or the text after it.
func TestTerminalFeaturesText(t *testing.T) {
	t.Parallel()
	sp := runFeatureScript(t, "01_text_attributes.sh", 140, 60)
	sp.finish()
	sp.render()
	rt := testTheme()
	plain := plainStyle()
	for _, tc := range []struct {
		label, text string
		want        tcell.Style
	}{
		{"SGR 1  bold", "Bold text", plain.Bold(true)},
		{"SGR 2  dim", "Dim/faint text", plain.Foreground(dimmed(rt.fg))},
		{"SGR 3  italic", "Italic text", plain.Italic(true)},
		{"SGR 5  blink", "Blink text", plain.Blink(true)},
		{"SGR 7  reverse", "Reverse text", tcell.StyleDefault.Foreground(rt.bg).Background(rt.fg)},
		{"SGR 9  strikethrough", "Strikethrough", plain.StrikeThrough(true)},
		{"SGR 53 overline", "Overline text", plain.Overline(true)},
		{"SGR 4  underline", "Underline text", plain.Underline(tcell.UnderlineStyleSolid)},
		{"SGR 4:1 solid", "Underline text", plain.Underline(tcell.UnderlineStyleSolid)},
		{"SGR 4:2 double", "Double underline", plain.Underline(tcell.UnderlineStyleDouble)},
		{"SGR 21 double", "Double underline", plain.Underline(tcell.UnderlineStyleDouble)},
		{"SGR 4:3 curly", "Curly underline", plain.Underline(tcell.UnderlineStyleCurly)},
		{"SGR 4:4 dotted", "Dotted underline", plain.Underline(tcell.UnderlineStyleDotted)},
		{"SGR 4:5 dashed", "Dashed underline", plain.Underline(tcell.UnderlineStyleDashed)},
		{"SGR combined", "Bold+italic+under", plain.Bold(true).Italic(true).Underline(tcell.UnderlineStyleSolid)},
		{"SGR combined", "Dim + strikethrough", plain.Foreground(dimmed(rt.fg)).StrikeThrough(true)},
	} {
		y, cells := sp.spanAfter(tc.label+" ", tc.text)
		checkStyles(t, tc.label+" "+tc.text, cells, tc.want)
		left, right := sp.around(y, cells)
		checkStyles(t, fmt.Sprintf("row %d before %q", y, tc.text), left, plain)
		checkStyles(t, fmt.Sprintf("row %d after %q", y, tc.text), right, plain)
	}

	// SGR 8 paints blanks but keeps the text for copy and search.
	y, row, i := sp.find("SGR 8  invisible ")
	if got := sp.rowText(y); got != "  SGR 8  invisible                   <-- should be blank" {
		t.Errorf("invisible row = %q", got)
	}
	checkStyles(t, "invisible row", sp.content(row), plain)
	start := row[i].x + len("SGR 8  invisible ") + 6 // %-22s column
	sp.pane.Lock()
	var hidden strings.Builder
	for x := start; x < start+len("(invisible)"); x++ {
		hidden.WriteString(sp.pane.Term.Cell(x, y).Text())
	}
	sp.pane.Unlock()
	if hidden.String() != "(invisible)" {
		t.Errorf("hidden text = %q, want (invisible)", hidden.String())
	}
}

// TestTerminalFeaturesColors replaces judging `terminal_features.sh colors`
// by eye: every ANSI, 256-colour and truecolour block, coloured underline
// and dimmed RGB text gets its exact colour, and resets stop colour bleed.
func TestTerminalFeaturesColors(t *testing.T) {
	t.Parallel()
	sp := runFeatureScript(t, "02_colors.sh", 100, 70)
	sp.finish()
	sp.render()
	plain := plainStyle()
	bgStyle := func(c tcell.Color) tcell.Style { return plain.Background(c) }

	// checkRun checks n blocks of size cells each, from column x on row y.
	checkRun := func(what string, y, x, n, size int, text string, style func(i int) tcell.Style) {
		t.Helper()
		row := sp.row(y)
		for i := 0; i < n; i++ {
			for k := 0; k < size; k++ {
				c := row[x+i*size+k]
				if c.text != text || c.style != style(i) {
					t.Errorf("%s block %d: %q %+v, want %q %+v", what, i, c.text, c.style, text, style(i))
					return
				}
			}
		}
		if c := row[x+n*size]; c.style != plain {
			t.Errorf("%s: colour bleeds past the last block: %+v", what, c.style)
		}
	}

	y, row, _ := sp.find("  FG (30-37):  ")
	checkRun("FG 30-37", y, 15, 8, 1, "█", func(i int) tcell.Style { return plain.Foreground(themeColor(i)) })
	checkRun("bold FG 30-37", y, findIn(row, 0, "bold: ")+len("bold: "), 8, 1, "█", func(i int) tcell.Style {
		return plain.Foreground(themeColor(8 + i)).Bold(true)
	})
	y, _, _ = sp.find("  BG (40-47):  ")
	checkRun("BG 40-47", y, 15, 8, 2, " ", func(i int) tcell.Style { return bgStyle(themeColor(i)) })
	y, _, _ = sp.find("  Bright BG:   ")
	checkRun("BG 100-107", y, 15, 8, 2, " ", func(i int) tcell.Style { return bgStyle(themeColor(8 + i)) })
	y, _, _ = sp.find("  System 0-15:  ")
	checkRun("48;5;0-15", y, 16, 16, 2, " ", func(i int) tcell.Style { return bgStyle(themeColor(i)) })
	y, _, _ = sp.find("  Color cube:   ")
	for r := 0; r < 6; r++ {
		checkRun(fmt.Sprintf("colour cube row %d", r), y+r, 16, 36, 1, " ", func(i int) tcell.Style {
			return bgStyle(tcell.PaletteColor(16 + r*36 + i))
		})
	}
	y, _, _ = sp.find("  Greys 232-255:")
	checkRun("greys", y, 16, 24, 1, " ", func(i int) tcell.Style { return bgStyle(tcell.PaletteColor(232 + i)) })

	ramp := func(i int) int32 { return int32(i * 255 / 71) }
	rainbow := func(i int) tcell.Color {
		deg := i * 360 / 72
		f := int32((deg % 60) * 255 / 59)
		q := 255 - f
		switch deg / 60 {
		case 0:
			return tcell.NewRGBColor(255, f, 0)
		case 1:
			return tcell.NewRGBColor(q, 255, 0)
		case 2:
			return tcell.NewRGBColor(0, 255, f)
		case 3:
			return tcell.NewRGBColor(0, q, 255)
		case 4:
			return tcell.NewRGBColor(f, 0, 255)
		}
		return tcell.NewRGBColor(255, 0, q)
	}
	for _, g := range []struct {
		label string
		color func(i int) tcell.Color
	}{
		{"  Red gradient:", func(i int) tcell.Color { return tcell.NewRGBColor(ramp(i), 0, 0) }},
		{"  Green gradient:", func(i int) tcell.Color { return tcell.NewRGBColor(0, ramp(i), 0) }},
		{"  Blue gradient:", func(i int) tcell.Color { return tcell.NewRGBColor(0, 0, ramp(i)) }},
		{"  Rainbow:", rainbow},
	} {
		y, _, _ := sp.find(g.label)
		checkRun(g.label, y, 19, 72, 1, " ", func(i int) tcell.Style { return bgStyle(g.color(i)) })
	}

	underline := func(us tcell.UnderlineStyle, c tcell.Color) tcell.Style { return plain.Underline(us).Underline(c) }
	for _, tc := range []struct {
		text string
		want tcell.Style
	}{
		{"Red 256-color underline", underline(tcell.UnderlineStyleSolid, tcell.PaletteColor(196))},
		{"Green 256-color underline", underline(tcell.UnderlineStyleSolid, tcell.PaletteColor(82))},
		{"Orange RGB underline", underline(tcell.UnderlineStyleSolid, tcell.NewRGBColor(255, 80, 0))},
		{"Curly+colored underline", underline(tcell.UnderlineStyleCurly, tcell.NewRGBColor(255, 0, 128))},
		{"Dim + true-color text", plain.Foreground(dimmed(tcell.NewRGBColor(100, 200, 255)))},
	} {
		y, cells := sp.spanAfter(" ", tc.text)
		checkStyles(t, tc.text, cells, tc.want)
		_, right := sp.around(y, cells)
		checkStyles(t, fmt.Sprintf("row %d after %q", y, tc.text), right, plain)
	}
}

// TestTerminalFeaturesCursor replaces watching `terminal_features.sh
// cursor`: the host cursor takes each DECSCUSR shape, blink and OSC 12
// colour at each pause, sits after the prompt, and resets at the end.
// Wide, combined and emoji cells paint at their columns with mode 2027 set
// and reset, and the CJK line reflows without splitting a wide character.
func TestTerminalFeaturesCursor(t *testing.T) {
	t.Parallel()
	sp := runFeatureScript(t, "03_cursor_unicode.sh", 100, 80)
	themeCursor := oscCursorColor(sp.pane.ThemeCursor)
	orange := tcell.NewRGBColor(0xff, 0x88, 0x00)
	for _, pause := range []struct {
		line  string
		style tcell.CursorStyle
		color tcell.Color
	}{
		{"  Shape 1 -- blinking block", tcell.CursorStyleBlinkingBlock, themeCursor},
		{"  Shape 2 -- steady block", tcell.CursorStyleSteadyBlock, themeCursor},
		{"  Shape 3 -- blinking underline", tcell.CursorStyleBlinkingUnderline, themeCursor},
		{"  Shape 4 -- steady underline", tcell.CursorStyleSteadyUnderline, themeCursor},
		{"  Shape 5 -- blinking bar", tcell.CursorStyleBlinkingBar, themeCursor},
		{"  Shape 6 -- steady bar", tcell.CursorStyleSteadyBar, themeCursor},
		{"  Steady orange bar", tcell.CursorStyleSteadyBar, orange},
		{"  Blinking orange bar", tcell.CursorStyleBlinkingBar, orange},
	} {
		sp.waitPrompt("(press Enter)")
		sp.render()
		style, color := sp.scr.cursorStyle()
		if style != pause.style || color != pause.color {
			t.Errorf("%s: cursor style %d colour %v, want %d %v", pause.line, style, color, pause.style, pause.color)
		}
		y, row, _ := sp.find(pause.line)
		end := findIn(row, 0, "(press Enter)") + len("(press Enter)")
		if x, cy, visible := sp.scr.GetCursor(); !visible || x != end || cy != y {
			t.Errorf("%s: cursor at %d,%d visible=%v, want %d,%d after the prompt", pause.line, x, cy, visible, end, y)
		}
		sp.pressEnter()
	}
	sp.finish()
	sp.render()
	if style, color := sp.scr.cursorStyle(); style != tcell.CursorStyleDefault || color != themeCursor {
		t.Errorf("cursor not reset: style %d colour %v", style, color)
	}
	sp.find("  ✔ Cursor reset to default")

	wide := func(s string) []wantCell { return []wantCell{{s, 2}} }
	join := func(parts ...[]wantCell) []wantCell { return slices.Concat(parts...) }
	unicodeRows := []struct {
		label string
		x     int
		want  []wantCell
	}{
		{"  CJK wide chars:   ", 20, join(narrow("|"), wide("中"), wide("文"), wide("日"), wide("本"), wide("語"),
			wide("한"), wide("국"), wide("어"), narrow("|"))},
		{"  Emoji sequence:   ", 20, join(narrow("|"), wide("😀"), narrow(" "), wide("🎉"), narrow(" "), wide("🦀"), narrow(" "),
			wide("🔥"), narrow(" "), wide("✅"), narrow(" "), wide("❌"), narrow("|"))},
		{"  Grapheme clusters: ", 21, join(narrow("|"), []wantCell{{"e\u0301", 1}}, narrow("|"), wide("❤\ufe0f"), narrow("|"),
			wide("👍🏽"), narrow("|"), wide("🇳🇱"), narrow("|"), wide("1\ufe0f\u20e3"), narrow("|"),
			wide("👩\u200d👩\u200d👧\u200d👦"), narrow("|"))},
		{"  Box drawing:      ", 20, narrow("┌─┬─┐  ╔═╦═╗  ├─┼─┤")},
		{"  Braille spinner:  ", 20, narrow("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")},
		{"  Powerline glyphs: ", 20, narrow("\ue0b0\ue0b1\ue0b2\ue0b3 (needs Nerd Font)")},
		// DECRST 2027: VS16 no longer widens the heart, and the flag and
		// the family split into their code points.
		{"  Legacy widths:    ", 20, join(narrow("|❤|"), wide("🇳"), wide("🇱"), narrow("|"), wide("👩\u200d"), wide("👧"), narrow("|"))},
	}
	for _, r := range unicodeRows {
		_, row, _ := sp.find(r.label)
		checkCells(t, r.label, row, r.x, r.want)
	}
	y, _, _ := sp.find("  Box drawing:      ")
	checkCells(t, "box drawing second row", sp.row(y+1), 20, narrow("│ │ │  ║ ║ ║  └─┴─┘"))

	// Reflow: at 30 columns only one column is left for 語, so the CJK
	// line wraps before it instead of splitting it. The full line returns
	// at the original width.
	sp.resizeHost(31, 80)
	sp.render()
	y, _, _ = sp.find("  CJK wide chars:   ")
	if got, want := sp.rowText(y), "  CJK wide chars:   |中文日本"; got != want {
		t.Errorf("narrow CJK row = %q, want %q", got, want)
	}
	checkCells(t, "wrapped CJK row", sp.row(y+1), 0, join(wide("語"), wide("한"), wide("국"), wide("어"), narrow("|")))
	sp.resizeHost(101, 80)
	sp.render()
	for _, r := range unicodeRows {
		_, row, _ := sp.find(r.label)
		checkCells(t, "after reflow: "+r.label, row, r.x, r.want)
	}
}

// TestTerminalFeaturesIntegration replaces judging `terminal_features.sh
// integration`: the OSC 8 link covers exactly its text, a host paste
// arrives bracketed while DECSET 2004 is on, and OSC 7, 52 and 133 reach
// the host terminal through app.oscBuf. OSC 52 also reaches bunk's native
// clipboard mirror (replaced in TestMain).
func TestTerminalFeaturesIntegration(t *testing.T) {
	t.Parallel()
	sp := runFeatureScript(t, "04_host_integration.sh", 120, 60)
	sp.waitPrompt("  Paste something now, then press Enter to continue. ")
	sp.pane.Lock()
	bracketed := sp.pane.Term.Mode()&vt10x.ModeSetPaste != 0
	sp.pane.Unlock()
	if !bracketed {
		t.Error("DECSET 2004 not active at the paste prompt")
	}
	sp.paste("bunk paste")
	sp.pressEnter()
	sp.finish()
	sp.render()

	const url = "https://github.com/jsnjack/bunk"
	plain := plainStyle()
	y, _, _ := sp.find("  Click this link -> ")
	if got := sp.rowText(y); got != "  Click this link -> bunk on GitHub" {
		t.Errorf("link row = %q", got)
	}
	y, link := sp.spanAfter("  Click this link -> ", "bunk on GitHub")
	checkStyles(t, "link text", link, plain.Url(url))
	left, right := sp.around(y, link)
	checkStyles(t, "before the link", left, plain)
	checkStyles(t, "after the link", right, plain)

	rows := sp.rows()
	for _, want := range []string{
		"  Received: ^[[200~bunk paste^[[201~",
		"  Visible output: sample command under OSC 133 markers",
		"sample command under OSC 133 markers",
		"  ✔ Bracketed paste disabled",
	} {
		if !slices.Contains(rows, want) {
			t.Errorf("no row %q:\n%s", want, sp.dump())
		}
	}
	sp.pane.Lock()
	bracketed = sp.pane.Term.Mode()&vt10x.ModeSetPaste != 0
	sp.pane.Unlock()
	if bracketed {
		t.Error("DECRST 2004 did not turn bracketed paste off")
	}

	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	clip := base64.StdEncoding.EncodeToString([]byte("bunk OSC 52 test"))
	got := sp.host.String()
	at := 0
	for _, want := range []string{
		"\x1b]7;file://" + host + dir + "\x1b\\",
		"\x1b]52;c;" + clip + "\x1b\\",
		"\x1b]133;A\x1b\\", "\x1b]133;B\x1b\\", "\x1b]133;C\x1b\\", "\x1b]133;D;0\x1b\\",
	} {
		i := strings.Index(got[at:], want)
		if i < 0 {
			t.Fatalf("host did not receive %q after offset %d: %q", want, at, got)
		}
		at += i + len(want)
	}
	if strings.Contains(got, "\x1b]8;;"+url) {
		t.Errorf("raw OSC 8 forwarded to the host: %q", got)
	}
	deadline := time.Now().Add(featureTimeout)
	for !clipboardHas("bunk OSC 52 test") {
		if time.Now().After(deadline) {
			t.Fatal("OSC 52 text never reached the native clipboard mirror")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestTerminalFeaturesQueries replaces reading the replies `terminal_
// features.sh queries` prints: each query's reply, written back to the PTY
// by bunk, is exact. Each "Bunk typically reports" line in the script
// matches the reply printed above it.
func TestTerminalFeaturesQueries(t *testing.T) {
	t.Parallel()
	const cols, rows = 160, 120
	sp := runFeatureScript(t, "05_queries.sh", cols, rows)
	sp.finish()
	sp.render()
	screen := sp.rows()
	want := []string{
		`  Response: ^[[?62;1;2;4;6;9;15;22c`,
		`  Response: ^[[>0;279;0c`,
		`  DA3: ^[P!|00000000^[\`,
		`  DSR: ^[[0n`,
		`  SGR setting: ^[P1$r0m^[\`,
		`  Cursor style setting: ^[P1$r4 q^[\`,
		fmt.Sprintf(`  Scroll margins setting: ^[P1$r1;%dr^[\`, rows),
		`  Unsupported setting: ^[P0$r^[\`,
		`  Response: ^[P>|VTE(8203)^[\`,
		fmt.Sprintf(`  Text area pixels (CSI 14 t): ^[[4;%d;%dt`, rows*16, cols*8),
		`  Cell pixels (CSI 16 t):      ^[[6;16;8t`,
		fmt.Sprintf(`  Text area cells (CSI 18 t):  ^[[8;%d;%dt`, rows, cols),
		`  Response:     ^[P1+r536d756c78=1b5b343a25703125646d^[\`,
		`  Response:     ^[P1+r536574756c63=1b5b35383a323a3a25703125643a25703225643a25703325646d^[\`,
		`  Name / indexed colors / RGB depth: ^[P1+r544e=787465726d2d323536636f6c6f72^[\^[P1+r436f=323536^[\^[P1+r524742=38^[\`,
		`  Application cursor Up: ^[P1+r6b63757531=1b4f41^[\`,
		`  Response:        ^[[?2026;2$y`,
		`  Response:        ^[[?2004;2$y`,
		`  Response: ^[]10;rgb:d0d0/d0d0/d0d0^[\`,
		`  Response: ^[]11;rgb:1a1a/1a1a/2e2e^[\`,
		`  Response: ^[]12;rgb:d0d0/d0d0/d0d0^[\`,
		`  Response: ^[[?0u`,
		`  Push 5, query, pop, query: ^[[?5u^[[?0u`,
	}
	// CPR reports the row the script prints its reply on, after "Response: ".
	y, _, _ := sp.find("  Query:    ^[[6n")
	want = append(want, fmt.Sprintf("  Response: ^[[%d;13R", y+2))
	for _, w := range want {
		if !slices.Contains(screen, w) {
			t.Errorf("no row %q", w)
		}
	}

	// The script's own expectations must match bunk.
	checked := 0
	response := ""
	for _, line := range screen {
		if r, ok := strings.CutPrefix(strings.TrimLeft(line, " "), "Response:"); ok {
			response = strings.TrimLeft(r, " ")
		}
		if reported, ok := strings.CutPrefix(line, "  Expect: Bunk typically reports "); ok {
			checked++
			if reported != response {
				t.Errorf("script expects %q, bunk replied %q", reported, response)
			}
		}
	}
	if checked != 3 {
		t.Errorf("checked %d documented replies, want 3 (DA1, DA2, XTVERSION)", checked)
	}
	if t.Failed() {
		t.Log(sp.dump())
	}
}

// TestTerminalFeaturesOSC runs `terminal_features.sh osc`, the PASS/FAIL
// smoke script, and requires a PASS line for every assertion in it.
func TestTerminalFeaturesOSC(t *testing.T) {
	t.Parallel()
	script, err := os.ReadFile("scripts/terminal_features/06_osc_smoke.sh")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range regexp.MustCompile(`(?m)^\s+assert_(?:eq|empty) "([^"$]+)"`).FindAllStringSubmatch(string(script), -1) {
		names = append(names, m[1])
	}
	if len(names) < 13 {
		t.Fatalf("found %d assertions in the script, want at least 13", len(names))
	}
	sp := runFeatureScript(t, "06_osc_smoke.sh", 100, 40)
	sp.finish()
	sp.render()
	screen := sp.rows()
	for _, name := range names {
		if !slices.Contains(screen, "PASS  "+name) {
			t.Errorf("no PASS for %q", name)
		}
	}
	for _, want := range []string{
		fmt.Sprintf("Summary: PASS=%d FAIL=0", len(names)),
		`  OSC 10: ^[]10;rgb:d0d0/d0d0/d0d0^[\`,
		`  OSC 11: ^[]11;rgb:1a1a/1a1a/2e2e^[\`,
		`  OSC 12: ^[]12;rgb:d0d0/d0d0/d0d0^[\`,
	} {
		if !slices.Contains(screen, want) {
			t.Errorf("no row %q", want)
		}
	}
	if t.Failed() {
		t.Log(sp.dump())
	}
}

// TestTerminalFeaturesGraphics replaces inspecting `terminal_features.sh
// graphics`: SIXEL, Kitty and iTerm2 images become 8x3 half-block cells
// with the right colours, render from scrollback, stay inside the pane at
// every width, and come back whole after a resize.
func TestTerminalFeaturesGraphics(t *testing.T) {
	t.Parallel()
	const cols, rows = 40, 12
	sp := runFeatureScript(t, "07_graphics.sh", cols, rows)
	sp.waitPrompt("Resize the pane, inspect scrollback, then press Enter. ")
	red, blue := tcell.NewRGBColor(255, 0, 0), tcell.NewRGBColor(0, 0, 255)
	redBlue := [3][2]tcell.Color{{red, red}, {red, blue}, {blue, blue}}
	allRed := [3][2]tcell.Color{{red, red}, {red, red}, {red, red}}

	// image returns the first screen row of the image below header, or -1
	// when the header or any image row is off screen.
	image := func(header string) int {
		for y := 0; y < rows; y++ {
			if findIn(sp.row(y), 0, header) != 0 {
				continue
			}
			for top := y + 1; top < min(y+3, rows); top++ {
				if sp.row(top)[0].text == "▀" {
					if top+3 > rows {
						return -1
					}
					return top
				}
			}
		}
		return -1
	}
	checkImage := func(header string, want [3][2]tcell.Color) {
		t.Helper()
		top := image(header)
		if top < 0 {
			t.Fatalf("%s: image not on screen:\n%s", header, sp.dump())
		}
		for r := 0; r < 3; r++ {
			row := sp.row(top + r)
			for x := 0; x < 8; x++ {
				fg, bg, _ := row[x].style.Decompose()
				if row[x].text != "▀" || fg != want[r][0] || bg != want[r][1] {
					t.Fatalf("%s: row %d cell %d = %q %v/%v, want ▀ %v/%v", header, r, x, row[x].text, fg, bg, want[r][0], want[r][1])
				}
			}
			if row[8].text == "▀" {
				t.Fatalf("%s: row %d is wider than 8 cells", header, r)
			}
		}
	}
	// inPane fails if an image cell is painted outside the pane's columns.
	inPane := func(what string) {
		t.Helper()
		sp.app.mu.Lock()
		w := sp.pane.w
		sp.app.mu.Unlock()
		for y := 0; y < rows; y++ {
			for _, c := range sp.row(y) {
				if c.x >= w-1 && c.text == "▀" {
					t.Fatalf("%s: image cell at column %d of a %d-column pane:\n%s", what, c.x, w-1, sp.dump())
				}
			}
		}
	}
	imageCells := func() int {
		sp.pane.Lock()
		defer sp.pane.Unlock()
		n := 0
		for i := 0; i < sp.pane.Scrollback.Count; i++ {
			for _, g := range sp.pane.Scrollback.Get(i) {
				if g.Image != nil {
					n++
				}
			}
		}
		c, r := sp.pane.Term.Size()
		for y := 0; y < r; y++ {
			for x := 0; x < c; x++ {
				if sp.pane.Term.Cell(x, y).Image != nil {
					n++
				}
			}
		}
		return n
	}
	offset := func() int {
		sp.pane.Lock()
		defer sp.pane.Unlock()
		return sp.pane.ScrollOffset
	}
	scroll := func(key tcell.Key) int {
		sp.press(key, 0, tcell.ModShift)
		sp.render()
		return offset()
	}
	// scrollUntil pages back with Shift+PgUp until header's image shows.
	scrollUntil := func(header string) {
		t.Helper()
		for image(header) < 0 {
			before := offset()
			if scroll(tcell.KeyPgUp) == before {
				t.Fatalf("%s: not found in scrollback", header)
			}
		}
	}
	toLive := func() {
		for scroll(tcell.KeyPgDn) != 0 {
		}
	}

	sp.render()
	checkImage("▶ iTerm2: red GIF", allRed)
	inPane("live view")
	if n := imageCells(); n != 3*8*3 {
		t.Fatalf("%d image cells, want 72", n)
	}
	scrollUntil("▶ Kitty: red above blue")
	checkImage("▶ Kitty: red above blue", redBlue)
	inPane("scrollback")
	scrollUntil("▶ SIXEL: red above blue")
	checkImage("▶ SIXEL: red above blue", redBlue)
	inPane("scrollback")
	toLive()

	// A 5-column pane wraps the image rows inside the pane; every scrolled
	// view keeps them off the scrollbar column.
	sp.resizeHost(6, rows)
	sp.render()
	inPane("narrow live view")
	for {
		before := offset()
		if scroll(tcell.KeyPgUp) == before {
			break
		}
		inPane(fmt.Sprintf("narrow view at offset %d", before))
	}
	toLive()
	if n := imageCells(); n != 72 {
		t.Errorf("%d image cells at 5 columns, want 72", n)
	}
	sp.resizeHost(cols+1, rows)
	sp.render()
	checkImage("▶ iTerm2: red GIF", allRed)
	scrollUntil("▶ SIXEL: red above blue")
	checkImage("▶ SIXEL: red above blue", redBlue)
	toLive()
	if n := imageCells(); n != 72 {
		t.Errorf("%d image cells after resizing back, want 72", n)
	}

	sp.pressEnter()
	sp.finish()
	sp.render()
	sp.find("Done.")
}
