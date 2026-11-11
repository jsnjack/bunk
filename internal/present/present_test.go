package present

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func TestKindOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path string
		want Kind
	}{
		{"README.md", KindMarkdown},
		{"/tmp/plan.MD", KindMarkdown},
		{"notes.markdown", KindMarkdown},
		{"a.mdown", KindMarkdown},
		{"a.mkd", KindMarkdown},
		{"report.html", KindHTML},
		{"Report.HTM", KindHTML},
		{"main.go", KindNone},
		{"md", KindNone},
		{"archive.md.gz", KindNone},
		{"", KindNone},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			if got := KindOf(tc.path); got != tc.want {
				t.Errorf("KindOf(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// renderPageString renders src with RenderMarkdown and fails the test on an
// error.
func renderPageString(t *testing.T, src string, opts Options) string {
	t.Helper()
	out, err := RenderMarkdown([]byte(src), opts)
	if err != nil {
		t.Fatalf("RenderMarkdown(%q): %v", src, err)
	}
	return string(out)
}

var cspMeta = regexp.MustCompile(`<meta http-equiv="Content-Security-Policy" content="([^"]*)">`)

// pageCSP returns the policy of page's CSP meta, unescaped.
func pageCSP(t *testing.T, page string) string {
	t.Helper()
	m := cspMeta.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("page has no CSP meta:\n%s", page)
	}
	return strings.ReplaceAll(m[1], "&#39;", "'")
}

// directive returns the sources of a CSP directive, or nil.
func directive(policy, name string) []string {
	for _, d := range strings.Split(policy, ";") {
		f := strings.Fields(d)
		if len(f) > 0 && f[0] == name {
			return f[1:]
		}
	}
	return nil
}

func TestPageCSP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, base           string
		scripts, images, def string
	}{
		{"custom scheme", "bunk-present://assets/", "bunk-present://assets", "'self' data: bunk-present:", "'self' bunk-present://assets"},
		{"relative", "assets/", "'self'", "'self' data:", "'self'"},
		{"file", "file:///tmp/x/assets/", "file:", "'self' data: file:", "'self' file:"},
		{"https", "https://example.com/a/", "https://example.com", "'self' data: https://example.com", "'self' https://example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			page := renderPageString(t, "# Doc\n", Options{AssetBase: tc.base})
			head := page[strings.Index(page, "<head>")+len("<head>"):]
			if !strings.HasPrefix(strings.TrimSpace(head), `<meta http-equiv="Content-Security-Policy"`) {
				t.Errorf("the CSP meta is not the first element of <head>:\n%s", page)
			}
			policy := pageCSP(t, page)
			check := func(name, want string) {
				if got := strings.Join(directive(policy, name), " "); got != want {
					t.Errorf("%s = %q, want %q (policy %q)", name, got, want, policy)
				}
			}
			check("connect-src", "'none'")
			// Graphviz compiles WebAssembly; nothing may eval JavaScript.
			check("script-src", tc.scripts+" 'wasm-unsafe-eval'")
			check("img-src", tc.images)
			check("default-src", tc.def)
			check("font-src", tc.scripts)
			check("style-src", tc.scripts+" 'unsafe-inline'")
			check("object-src", "'none'")
			check("base-uri", "'none'")
			if strings.Contains(policy, "'unsafe-eval'") {
				t.Errorf("policy allows eval: %q", policy)
			}
		})
	}
}

func TestPageHasNoInlineScript(t *testing.T) {
	t.Parallel()
	page := renderPageString(t, "# Doc\n\n<script>alert(1)</script>\n", Options{AssetBase: "bunk-present://assets/"})
	scripts := regexp.MustCompile(`<script[^>]*>`).FindAllString(page, -1)
	if len(scripts) != 1 {
		t.Fatalf("page has %d scripts, want 1: %q", len(scripts), scripts)
	}
	if want := `<script src="bunk-present://assets/bunk-doc.js">`; scripts[0] != want {
		t.Errorf("script = %q, want %q", scripts[0], want)
	}
	if !strings.Contains(page, "</script>\n</body>") {
		t.Errorf("the runtime is not the last element of <body>:\n%s", page)
	}
}

func TestRenderBodyMatchesPage(t *testing.T) {
	t.Parallel()
	src := "# Plan\n\nSome *text* with $x$.\n\n```go\nx := 1\n```\n\n```mermaid\ngraph TD; A-->B\n```\n\n- [ ] task\n\nNote[^n].\n\n[^n]: Here.\n"
	body := renderBody(t, src)
	page := renderPageString(t, src, Options{AssetBase: "bunk-present://assets/", Title: "plan.md"})
	start := strings.Index(page, articleOpen)
	end := strings.LastIndex(page, "</article>")
	if start < 0 || end < start {
		t.Fatalf("page has no article:\n%s", page)
	}
	if article := page[start+len(articleOpen) : end]; article != body {
		t.Errorf("article content differs from RenderBody:\narticle:\n%s\nbody:\n%s", article, body)
	}
}

func TestPageTitle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, src, title, want string
	}{
		{"given", "# Heading\n", "plan.md", "plan.md"},
		{"escaped", "text\n", "a <b> & c", "a &lt;b&gt; &amp; c"},
		{"first heading", "intro\n\n## First *one*\n\n# Second\n", "", "First one"},
		{"no heading", "just text\n", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			page := renderPageString(t, tc.src, Options{Title: tc.title})
			if want := "<title>" + tc.want + "</title>"; !strings.Contains(page, want) {
				t.Errorf("page lacks %q:\n%s", want, page)
			}
		})
	}
}

func TestPageTheme(t *testing.T) {
	t.Parallel()
	light := Theme{
		Background: "#fafafa", Foreground: "rgb(20, 20, 20)", Muted: "gray",
		Accent: "#0969da", Border: "#ddd", CodeBackground: "#f0f0f0", Selection: "#b6d7ff80",
	}
	hostile := DefaultTheme()
	hostile.Background = "red}</style><script>alert(1)</script>"
	hostile.Accent = "blue; background: url(https://example.com/x)"
	tests := []struct {
		name  string
		theme Theme
		want  []string
		not   []string
	}{
		{
			name:  "zero is the default",
			theme: Theme{},
			want:  []string{"--bunk-bg:#1b1c20;", "--bunk-fg:#e6e8eb;", "--bunk-accent:#5aa9ff;", `data-bunk-theme="dark"`, "color-scheme:dark"},
		},
		{
			name:  "light",
			theme: light,
			want:  []string{"--bunk-bg:#fafafa;", "--bunk-fg:rgb(20, 20, 20);", "--bunk-muted:gray;", "--bunk-selection:#b6d7ff80}", `data-bunk-theme="light"`},
		},
		{
			name:  "missing colours of a light theme",
			theme: Theme{Background: "#fdfdfd"},
			want:  []string{"--bunk-bg:#fdfdfd;", "--bunk-fg:" + lightTheme().Foreground + ";"},
		},
		{
			name:  "unsafe colours",
			theme: hostile,
			want:  []string{"--bunk-bg:#1b1c20;", "--bunk-accent:#5aa9ff;"},
			not:   []string{"alert", "example.com"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			page := renderPageString(t, "# Doc\n", Options{Theme: tc.theme})
			for _, w := range tc.want {
				if !strings.Contains(page, w) {
					t.Errorf("page lacks %q", w)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(page, n) {
					t.Errorf("page has %q", n)
				}
			}
		})
	}
}

var styleElem = regexp.MustCompile(`(?s)<style>\n(.*?)</style>`)

// TestSystemTheme: a page that follows the system's colour scheme, as in a
// browser, has the light theme first and the dark theme for the dark
// scheme: the theme given for its own darkness and bunk's default for the
// other. Each code style applies only in its own scheme, and the page names
// no theme of its own, so the runtime reads --bunk-dark.
func TestSystemTheme(t *testing.T) {
	t.Parallel()
	dark := DefaultTheme()
	dark.Background = "#101010"
	tests := []struct {
		name        string
		theme       Theme
		light, dark string // the background of each scheme
	}{
		{"dark theme", dark, lightTheme().Background, "#101010"},
		{"light theme", Theme{Background: "#fafafa"}, "#fafafa", DefaultTheme().Background},
		{"zero is the default", Theme{}, lightTheme().Background, DefaultTheme().Background},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			page := renderPageString(t, "# Doc\n\n```go\npackage main\n```\n", Options{Theme: tc.theme, System: true})
			if strings.Contains(page, "data-bunk-theme") {
				t.Error("a page that follows the system names its theme")
			}
			m := styleElem.FindStringSubmatch(page)
			if m == nil {
				t.Fatalf("page has no <style>:\n%s", page)
			}
			css := m[1]
			if want := ":root{color-scheme:light;--bunk-dark:0;--bunk-bg:" + tc.light + ";"; !strings.HasPrefix(css, want) {
				t.Errorf("rules start %.80q, want %q", css, want)
			}
			if want := darkScheme + "{\n:root{color-scheme:dark;--bunk-dark:1;--bunk-bg:" + tc.dark + ";"; !strings.Contains(css, want) {
				t.Errorf("rules lack %q", want)
			}
			lightCode := lightScheme + "{\n" + codeCSS(false) + "}\n"
			darkCode := darkScheme + "{\n" + codeCSS(true) + "}\n"
			for _, block := range []string{lightCode, darkCode} {
				if !strings.Contains(css, block) {
					t.Errorf("rules lack %.80q…", block)
				}
				css = strings.Replace(css, block, "", 1)
			}
			if strings.Contains(css, ".chroma") {
				t.Errorf("code rules outside their scheme:\n%s", css)
			}
		})
	}
}

func TestSelectionScript(t *testing.T) {
	t.Parallel()
	s := strings.TrimSpace(SelectionScript)
	if !strings.HasPrefix(s, "(function () {") || !strings.HasSuffix(s, "})()") {
		t.Errorf("SelectionScript is not a function call expression:\n%s", s)
	}
	for _, want := range []string{"getSelection", "JSON.stringify", "text: text", "section: section", "4000"} {
		if !strings.Contains(s, want) {
			t.Errorf("SelectionScript lacks %q", want)
		}
	}
	if strings.Contains(s, "bunkDoc") {
		t.Errorf("SelectionScript depends on the page runtime")
	}
	if err := bracketsBalanced(s); err != "" {
		t.Errorf("SelectionScript: %s", err)
	}
}

// bracketsBalanced checks that the brackets of a script outside its string
// literals pair up, and returns the first problem.
func bracketsBalanced(s string) string {
	pairs := map[rune]rune{')': '(', ']': '[', '}': '{'}
	var stack []rune
	var quote rune
	escaped := false
	for i, r := range s {
		switch {
		case quote != 0:
			switch {
			case escaped:
				escaped = false
			case r == '\\':
				escaped = true
			case r == quote:
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '(' || r == '[' || r == '{':
			stack = append(stack, r)
		case pairs[r] != 0:
			if len(stack) == 0 || stack[len(stack)-1] != pairs[r] {
				return fmt.Sprintf("unmatched %c at byte %d", r, i)
			}
			stack = stack[:len(stack)-1]
		}
	}
	if quote != 0 {
		return "unterminated string"
	}
	if len(stack) > 0 {
		return fmt.Sprintf("unclosed %c", stack[len(stack)-1])
	}
	return ""
}
