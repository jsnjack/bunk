package present

import (
	"regexp"
	"strings"
	"testing"
)

// renderBody renders src with RenderBody and fails the test on an error.
func renderBody(t *testing.T, src string) string {
	t.Helper()
	out, err := RenderBody([]byte(src))
	if err != nil {
		t.Fatalf("RenderBody(%q): %v", src, err)
	}
	return string(out)
}

var headingTag = regexp.MustCompile(`<h([1-6])([^>]*)>`)
var attr = regexp.MustCompile(`([a-z-]+)="([^"]*)"`)

// headings returns the attributes of each heading in body, in order.
func headings(body string) []map[string]string {
	var out []map[string]string
	for _, m := range headingTag.FindAllStringSubmatch(body, -1) {
		attrs := map[string]string{"level": m[1]}
		for _, a := range attr.FindAllStringSubmatch(m[2], -1) {
			attrs[a[1]] = a[2]
		}
		out = append(out, attrs)
	}
	return out
}

func TestHeadingIDs(t *testing.T) {
	t.Parallel()
	src := "# Intro\n\n## Intro\n\n## Intro\n\n### Set *up* `go` [the](http://x.org) tool!\n\n" +
		"## Ünïcödé — Héading\n\n## snake_case and-dash\n\n#\n\nSetext\n======\n"
	want := []string{
		"intro", "intro-1", "intro-2", "set-up-go-the-tool",
		"ünïcödé--héading", "snake_case-and-dash", "section", "setext",
	}
	got := headings(renderBody(t, src))
	if len(got) != len(want) {
		t.Fatalf("got %d headings, want %d", len(got), len(want))
	}
	for i, w := range want {
		t.Run(w, func(t *testing.T) {
			if got[i]["id"] != w {
				t.Errorf("heading %d id = %q, want %q", i, got[i]["id"], w)
			}
		})
	}
}

// sectionHashes returns each heading's data-bunk-hash by its ID.
func sectionHashes(t *testing.T, src string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, h := range headings(renderBody(t, src)) {
		out[h["id"]] = h["data-bunk-hash"]
	}
	return out
}

func TestSectionHashes(t *testing.T) {
	t.Parallel()
	const base = "# Plan\n\nintro\n\n## Session storage\n\nstore text\n\n### Details\n\ndetail text\n\n" +
		"## Other\n\nother text\n\n##### Deep\n\ndeep text\n"
	before := sectionHashes(t, base)
	seen := map[string]bool{}
	for id, h := range before {
		if id == "deep" {
			if h != "" {
				t.Errorf("h5 %q has hash %q, want none", id, h)
			}
			continue
		}
		if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(h) {
			t.Errorf("heading %q hash = %q, want 12 hex digits", id, h)
		}
		if seen[h] {
			t.Errorf("heading %q shares hash %q with another section", id, h)
		}
		seen[h] = true
	}
	tests := []struct {
		name    string
		src     string
		changed []string
	}{
		{"unchanged", base, nil},
		{"paragraph under an h2", strings.Replace(base, "store text", "store words", 1), []string{"session-storage"}},
		{"paragraph under an h3", strings.Replace(base, "detail text", "detail words", 1), []string{"details"}},
		{"intro under the h1", strings.Replace(base, "intro", "preface", 1), []string{"plan"}},
		{"heading text", strings.Replace(base, "## Other", "## Other!", 1), []string{"other"}},
		{"new subsection", strings.Replace(base, "store text\n", "store text\n\n### Sub\n\nsub text\n", 1), []string{"sub"}},
		{"removed subsection", strings.Replace(base, "### Details\n\ndetail text\n\n", "", 1), nil},
		{"under an h5", strings.Replace(base, "deep text", "deep words", 1), nil},
		{"trailing blank lines", base + "\n\n\n", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// A heading is marked when its hash was not on the page before,
			// as bunkDoc.replace decides.
			var changed []string
			for _, h := range headings(renderBody(t, tc.src)) {
				if hash := h["data-bunk-hash"]; hash != "" && !seen[hash] {
					changed = append(changed, h["id"])
				}
			}
			if strings.Join(changed, ",") != strings.Join(tc.changed, ",") {
				t.Errorf("changed sections = %q, want %q", changed, tc.changed)
			}
		})
	}
}

func TestCodeHighlighting(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, src string
		want      []string
		not       []string
	}{
		{
			name: "known language",
			src:  "```go\nfunc main() {}\n```\n",
			want: []string{`<pre class="chroma"><code class="language-go">`, `<span class="kd">func</span>`},
			not:  []string{"style="},
		},
		{
			name: "unknown language",
			src:  "```nosuchlang\na < b\n```\n",
			want: []string{`<pre><code class="language-nosuchlang">a &lt; b`},
		},
		{
			name: "no language",
			src:  "```\n<b>x</b>\n```\n",
			want: []string{"<pre><code>&lt;b&gt;x&lt;/b&gt;"},
		},
		{
			name: "indented code",
			src:  "    <i>x</i>\n",
			want: []string{"<pre><code>&lt;i&gt;x&lt;/i&gt;"},
		},
		{
			name: "mermaid",
			src:  "```mermaid\ngraph TD; A-->B[\"<script>\"]\n```\n",
			want: []string{"<pre class=\"mermaid\">graph TD; A--&gt;B[&#34;&lt;script&gt;&#34;]\n</pre>"},
			not:  []string{"<script"},
		},
		{
			name: "mermaid fence in another case",
			src:  "```Mermaid\npie\n```\n",
			want: []string{`<pre class="mermaid">pie`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := renderBody(t, tc.src)
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("body lacks %q:\n%s", w, body)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(body, n) {
					t.Errorf("body has %q:\n%s", n, body)
				}
			}
		})
	}
}

func TestCodeStyleFollowsTheme(t *testing.T) {
	t.Parallel()
	src := []byte("```go\nfunc main() {}\n```\n")
	tests := []struct {
		name string
		dark bool
	}{
		{"dark", true},
		{"light", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			theme := DefaultTheme()
			theme.Dark = tc.dark
			page, err := RenderMarkdown(src, Options{Theme: theme})
			if err != nil {
				t.Fatal(err)
			}
			css := codeCSS(tc.dark)
			if !strings.Contains(css, ".chroma .kd{") {
				t.Fatalf("code CSS has no rule for keywords:\n%s", css)
			}
			if !strings.Contains(string(page), css) {
				t.Errorf("page lacks the %s code CSS", tc.name)
			}
			if strings.Contains(string(page), codeCSS(!tc.dark)) {
				t.Errorf("page has the other style's code CSS")
			}
		})
	}
}

func TestRawHTMLDropped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, src string
	}{
		{"script block", "<script>alert(1)</script>\n"},
		{"inline handler", "a <b onclick=\"alert(1)\">b</b> c\n"},
		{"image handler", "<img src=x onerror=alert(1)>\n"},
		{"iframe", "<iframe src=\"https://example.com\"></iframe>\n"},
		{"javascript link", "[x](javascript:alert(1))\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := renderBody(t, tc.src)
			for _, bad := range []string{"<script", "onclick", "onerror", "<iframe", "javascript:"} {
				if strings.Contains(body, bad) {
					t.Errorf("body has %q:\n%s", bad, body)
				}
			}
		})
	}
}

func TestMarkdownFeatures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, src string
		want      []string
		not       []string
	}{
		{
			name: "relative link and image",
			src:  "[spec](docs/spec.md) ![shot](img/a.png)\n",
			want: []string{`<a href="docs/spec.md">spec</a>`, `<img src="img/a.png" alt="shot">`},
			not:  []string{"target="},
		},
		{
			name: "table",
			src:  "| a | b |\n|---|--:|\n| 1 | 2 |\n",
			want: []string{"<table>", "<th>a</th>", "<td>1</td>"},
		},
		{name: "strikethrough", src: "~~old~~\n", want: []string{"<del>old</del>"}},
		{
			name: "autolink",
			src:  "see https://example.com/x now\n",
			want: []string{`<a href="https://example.com/x">https://example.com/x</a>`},
			not:  []string{"target="},
		},
		{
			name: "task list",
			src:  "- [x] done\n- [ ] todo\n",
			want: []string{`<ul class="contains-task-list">`, `<li class="task-list-item"><input checked="" disabled="" type="checkbox"> done</li>`},
		},
		{
			name: "footnote",
			src:  "Text[^1].\n\n[^1]: The note.\n",
			want: []string{`href="#fn:1"`, `<div class="footnotes" role="doc-endnotes">`},
		},
		{name: "no typographer", src: "\"quoted\" -- dash...\n", want: []string{"&quot;quoted&quot; -- dash..."}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := renderBody(t, tc.src)
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("body lacks %q:\n%s", w, body)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(body, n) {
					t.Errorf("body has %q:\n%s", n, body)
				}
			}
		})
	}
}

// TestCallouts: a blockquote whose first line is GitHub's alert marker
// becomes a callout with a title; another blockquote stays one.
func TestCallouts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		src, want string
	}{
		{"> [!NOTE]\n> Useful **info**.\n", `<blockquote class="bunk-callout note"><p class="bunk-callout-title">Note</p>` + "\n<p>Useful <strong>info</strong>.</p>"},
		{"> [!warning]\n> Careful.\n", `<blockquote class="bunk-callout warning"><p class="bunk-callout-title">Warning</p>` + "\n<p>Careful.</p>"},
		{"> [!CAUTION]\n", `<blockquote class="bunk-callout caution"><p class="bunk-callout-title">Caution</p>` + "\n</blockquote>"},
		{"> [!TIP] on the same line\n", "<blockquote>\n<p>[!TIP] on the same line</p>"},
		{"> [!OTHER]\n> text\n", "<blockquote>\n<p>[!OTHER]"},
	}
	for _, tc := range tests {
		if got := renderBody(t, tc.src); !strings.Contains(got, tc.want) {
			t.Errorf("%q:\n%s\nwant it to hold:\n%s", tc.src, got, tc.want)
		}
	}
}

// TestRawHTMLAllowed: a few tags pass, without attributes but for
// <details open>; other tags go and their text stays, and the content of
// a script goes with it.
func TestRawHTMLAllowed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		src, want string
	}{
		{"<details>\n<summary>More <b onclick=x>here</b></summary>\n\nInside **md**\n\n</details>\n",
			"<details>\n<summary>More here</summary>\n<p>Inside <strong>md</strong></p>\n</details>"},
		{"<details open class=\"x\">\n<summary>S</summary>\n\nx\n\n</details>\n", "<details>\n<summary>S</summary>"},
		{"<details open>\n<summary>S</summary>\n\nx\n\n</details>\n", "<details open>\n<summary>S</summary>"},
		{"Press <kbd>Ctrl</kbd>+<kbd>C</kbd>, H<sub>2</sub>O, x<sup>2</sup>, <span style=\"color:red\">kept</span><br/>\n",
			"<p>Press <kbd>Ctrl</kbd>+<kbd>C</kbd>, H<sub>2</sub>O, x<sup>2</sup>, kept<br></p>"},
		{"<style>p{}</style>\n\n<p>a &amp; b</p>\n", "a &amp; b"},
	}
	for _, tc := range tests {
		got := renderBody(t, tc.src)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%q:\n%s\nwant it to hold:\n%s", tc.src, got, tc.want)
		}
		for _, bad := range []string{"onclick", "style", "class=\"x\"", "p{}"} {
			if strings.Contains(got, bad) {
				t.Errorf("%q: body has %q:\n%s", tc.src, bad, got)
			}
		}
	}
}

// TestKitMarkup: [TOC] alone becomes the outline's placeholder and the
// headings after it keep their IDs; a diff fence colours its lines; the
// article says the page is Markdown.
func TestKitMarkup(t *testing.T) {
	t.Parallel()
	body := renderBody(t, "# Doc\n\n[TOC]\n\n## One\n\n## Two\n\n```diff\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old <b>\n+new\n ctx\n```\n")
	for _, want := range []string{
		`<nav class="bunk-toc"></nav>`, `<h2 id="one"`, `<h2 id="two"`,
		`<pre class="bunk-diff"><code><span class="d-file">--- a/x` + "\n</span>",
		`<span class="d-hunk">@@ -1 +1 @@` + "\n</span>",
		`<span class="d-del">-old &lt;b&gt;` + "\n</span>",
		`<span class="d-add">+new` + "\n</span>",
		`<span class="d-ctx"> ctx` + "\n</span></code></pre>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if page := renderPageString(t, "# Doc\n", Options{}); !strings.Contains(page, `<article id="bunk-doc" class="bunk-md">`) {
		t.Error("the article does not say the page is Markdown")
	}
}

// TestLibraryFences: chart and dot fences become the elements the page
// runtime draws with ECharts and Graphviz, their text escaped.
func TestLibraryFences(t *testing.T) {
	t.Parallel()
	body := renderBody(t, "```chart\n{\"series\": [{\"type\": \"bar\", \"data\": [1, 2]}]}\n```\n\n```dot\ndigraph { a -> \"<b>\" }\n```\n")
	for _, want := range []string{
		`<pre class="bunk-chart">{&#34;series&#34;: [{&#34;type&#34;: &#34;bar&#34;, &#34;data&#34;: [1, 2]}]}`,
		`<pre class="bunk-graphviz">digraph { a -&gt; &#34;&lt;b&gt;&#34; }`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}
