package present

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPreview: kinds by extension first, then text by content: UTF-8
// without NUL bytes, a character cut at the end of the sample included.
func TestPreview(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	long := strings.Repeat("a", sniffBytes-1) + "é" + "tail" // é straddles the sample's end
	files := map[string]string{
		"plan.md":    "# Plan",
		"page.HTML":  "<p>x</p>",
		"shot.png":   "\x89PNG\x00",
		"doc.pdf":    "%PDF-1.4\x00",
		"clip.webm":  "\x1a\x45",
		"song.mp3":   "ID3",
		"main.go":    "package main\n",
		"Makefile":   "all:\n\tgo build\n",
		"empty":      "",
		"utf8.txt":   "naïve — 日本語\n",
		"long.log":   long,
		"binary.bin": "ab\x00cd",
		"latin1.txt": "caf\xe9\n",
	}
	for name, content := range files {
		writeTestFile(t, filepath.Join(dir, name), content)
	}
	for name, want := range map[string]Kind{
		"plan.md": KindMarkdown, "page.HTML": KindHTML, "shot.png": KindImage, "doc.pdf": KindPDF,
		"clip.webm": KindVideo, "song.mp3": KindAudio, "main.go": KindText, "Makefile": KindText,
		"empty": KindText, "utf8.txt": KindText, "long.log": KindText,
	} {
		if got, err := Preview(filepath.Join(dir, name)); got != want || err != nil {
			t.Errorf("Preview(%s) = %v, %v; want %v", name, got, err, want)
		}
	}
	for _, name := range []string{"binary.bin", "latin1.txt"} {
		if got, err := Preview(filepath.Join(dir, name)); !errors.Is(err, ErrNoPreview) {
			t.Errorf("Preview(%s) = %v, %v; want ErrNoPreview", name, got, err)
		}
	}
	if _, err := Preview(filepath.Join(dir, "missing.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Preview of a missing file: %v", err)
	}
	if !KindMarkdown.Document() || !KindHTML.Document() || KindText.Document() || KindImage.Document() {
		t.Error("Document: only Markdown and HTML are documents")
	}
}

// TestRenderPreview: text is escaped and highlighted by the file's name,
// media load the file by its escaped name, and every page carries the
// theme and a policy without inline scripts.
func TestRenderPreview(t *testing.T) {
	t.Parallel()
	opts := Options{AssetBase: "bunk-present://assets/", Theme: DefaultTheme()}
	for _, tc := range []struct {
		name string
		kind Kind
		src  string
		want []string
		not  []string
	}{
		{"main.go", KindText, "package main\n// </pre><script>alert(1)</script>\n",
			[]string{`class="bunk-file bunk-text"`, `<pre class="chroma">`, `<span class="kn">package</span>`, "&lt;/pre&gt;&lt;script&gt;"},
			[]string{"<script>alert"}},
		{"notes", KindText, "plain <b>text</b>\n", []string{"<pre><code>plain &lt;b&gt;text&lt;/b&gt;"}, []string{`<pre class="chroma">`}},
		{"big.go", KindText, "package main\n" + strings.Repeat("// x\n", highlightBytes/5+1), []string{"<pre><code>package main"}, []string{`class="kn"`}},
		{"my shot #1.png", KindImage, "", []string{`<img src="my%20shot%20%231.png" alt="my shot #1.png">`, "bunk-image"}, nil},
		{"clip.webm", KindVideo, "", []string{`<video controls src="clip.webm">`}, nil},
		{"song.mp3", KindAudio, "", []string{`<audio controls src="song.mp3">`}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := RenderPreview(tc.name, tc.kind, []byte(tc.src), opts)
			if err != nil {
				t.Fatal(err)
			}
			s := string(page)
			for _, want := range append(tc.want, "--bunk-bg:", "<title>"+tc.name+"</title>", "bunk-present://assets/bunk-doc.css") {
				if !strings.Contains(s, want) {
					t.Errorf("page lacks %q", want)
				}
			}
			for _, not := range tc.not {
				if strings.Contains(s, not) {
					t.Errorf("page has %q", not)
				}
			}
			if got := strings.Join(directive(pageCSP(t, s), "script-src"), " "); got != "bunk-present://assets" {
				t.Errorf("script-src = %q", got)
			}
		})
	}
	for _, kind := range []Kind{KindMarkdown, KindHTML, KindPDF, KindNone} {
		if _, err := RenderPreview("x", kind, nil, opts); err == nil {
			t.Errorf("RenderPreview of %v made a page", kind)
		}
	}
}

func TestThemeCSS(t *testing.T) {
	t.Parallel()
	if css := ThemeCSS(DefaultTheme()); !strings.Contains(css, "color-scheme:dark;--bunk-dark:1;--bunk-bg:#1b1c20;") {
		t.Errorf("dark: %s", css)
	}
	if css := ThemeCSS(Theme{Background: "#fff;}</style>", Foreground: "#000"}); !strings.Contains(css, "color-scheme:light;--bunk-dark:0;--bunk-bg:#ffffff;") {
		t.Errorf("light, with an unusable colour: %s", css)
	}
}

// TestStandalonePages: text gets a page like Markdown; an HTML page that
// loads bunk's assets gets a copy that loads them from disk, with its
// directory as base and a policy that keeps it off the network; other
// files open as they are. Pages follow the system's colour scheme.
func TestStandalonePages(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "src")
	root := t.TempDir()
	code := filepath.Join(dir, "main.go")
	withAssets := filepath.Join(dir, "page.html")
	plain := filepath.Join(dir, "plain.html")
	writeTestFile(t, code, "package main\n")
	writeTestFile(t, withAssets, `<!doctype html><html><head><title>P</title>`+
		`<script src="bunk-present://assets/bunk-doc.js"></script></head><body><pre class="mermaid">graph LR; A-->B</pre></body></html>`)
	writeTestFile(t, plain, "<p>plain</p>")
	writeTestFile(t, filepath.Join(dir, "shot.png"), "\x89PNG")

	for path, want := range map[string]bool{code: true, withAssets: true, plain: false, filepath.Join(dir, "shot.png"): false} {
		kind, err := Preview(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := NeedsPage(path, kind); got != want {
			t.Errorf("NeedsPage(%s) = %v", filepath.Base(path), got)
		}
	}
	page, err := WriteStandalone(code, root, DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}
	if s := readTestFile(t, page); !strings.Contains(s, `<span class="kn">package</span>`) || !strings.Contains(s, `<base href="`+FileURL(dir)+`/">`) ||
		!strings.Contains(s, darkScheme+"{\n"+codeCSS(true)) {
		t.Errorf("text page:\n%s", s)
	}
	page, err = WriteStandalone(withAssets, root, DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}
	s := readTestFile(t, page)
	assets := FileURL(filepath.Join(root, "assets")) + "/"
	for _, want := range []string{
		`"><base href="` + FileURL(dir) + `/"><style>:root{color-scheme:light;`,
		darkScheme + "{\n:root{color-scheme:dark;",
		`<script src="` + assets + `bunk-doc.js">`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("HTML copy lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, AssetsURL) {
		t.Errorf("HTML copy still loads %s", AssetsURL)
	}
	// The copy keeps the page off the network, as the panel does, and its
	// policy comes first, before anything of the page's own.
	if !strings.Contains(s, "<head><meta http-equiv=\"Content-Security-Policy\"") {
		t.Errorf("HTML copy has no policy first in its head:\n%s", s)
	}
	policy := pageCSP(t, s)
	for _, d := range []string{"default-src", "script-src", "style-src", "img-src", "font-src", "media-src", "frame-src"} {
		for _, src := range directive(policy, d) {
			if strings.Contains(src, "http") || src == "*" || src == "https:" {
				t.Errorf("%s allows the network: %q", d, src)
			}
		}
	}
	if got := strings.Join(directive(policy, "connect-src"), " "); got != "'none'" {
		t.Errorf("connect-src = %q, want 'none'", got)
	}
	if _, err := os.Stat(filepath.Join(root, "assets", "mermaid.min.js")); err != nil {
		t.Errorf("assets: %v", err)
	}
	if _, err := WriteStandalone(filepath.Join(dir, "shot.png"), root, DefaultTheme()); err == nil {
		t.Error("WriteStandalone made a page for an image")
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o700))
	must(t, os.WriteFile(path, []byte(content), 0o600))
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	must(t, err)
	return string(data)
}
