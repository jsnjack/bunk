package present

import (
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

var (
	urlAttr = regexp.MustCompile(`(?:src|href)="([^"]*)"`)
	// runtimeFile is a script or stylesheet named in bunk-doc.js.
	runtimeFile = regexp.MustCompile(`'([^'\s"]+\.(?:js|css))'`)
	cssURL      = regexp.MustCompile(`url\(([^)]*)\)`)
	baseElem    = regexp.MustCompile(`<base href="([^"]*)">`)
)

func TestAssetsReferenced(t *testing.T) {
	t.Parallel()
	const base = "bunk-present://assets/"
	assets := Assets()
	page := renderPageString(t, "# Doc\n\n$x$\n\n```mermaid\ngraph TD; A-->B\n```\n", Options{AssetBase: base})

	var names []string
	for _, m := range urlAttr.FindAllStringSubmatch(page, -1) {
		if name, ok := strings.CutPrefix(m[1], base); ok {
			names = append(names, name)
		}
	}
	if len(names) < 3 {
		t.Fatalf("page references %d assets, want the runtime, its stylesheet and KaTeX's: %q", len(names), names)
	}
	script, err := fs.ReadFile(assets, assetScript)
	if err != nil {
		t.Fatalf("read the runtime: %v", err)
	}
	// Every script and stylesheet the runtime names: those it loads and
	// those of bunkDoc.use.
	files := runtimeFile.FindAllStringSubmatch(string(script), -1)
	var loaded []string
	for _, m := range files {
		names = append(names, m[1])
		loaded = append(loaded, m[1])
	}
	for _, want := range []string{"katex/katex.min.js", "mermaid.min.js", "echarts.min.js", "viz-global.js", "d3.min.js",
		"highlight/highlight.min.js", "highlight/github.min.css", "highlight/github-dark.min.css"} {
		if !slices.Contains(loaded, want) {
			t.Errorf("the runtime does not name %s: %q", want, loaded)
		}
	}
	css, err := fs.ReadFile(assets, assetKatexCSS)
	if err != nil {
		t.Fatalf("read KaTeX's stylesheet: %v", err)
	}
	fonts := cssURL.FindAllStringSubmatch(string(css), -1)
	if len(fonts) == 0 {
		t.Error("KaTeX's stylesheet names no fonts")
	}
	for _, m := range fonts {
		if !strings.HasSuffix(m[1], ".woff2") {
			t.Errorf("KaTeX's stylesheet names %q, which is not bundled", m[1])
		}
		names = append(names, path.Join(path.Dir(assetKatexCSS), m[1]))
	}
	names = append(names, "mermaid.LICENSE", "katex/LICENSE", "echarts.LICENSE", "echarts.NOTICE", "d3.LICENSE",
		"viz.LICENSE", "graphviz.LICENSE", "expat.LICENSE", "highlight/LICENSE")
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			info, err := fs.Stat(assets, name)
			if err != nil {
				t.Fatalf("Assets lacks %q: %v", name, err)
			}
			if info.IsDir() || info.Size() == 0 {
				t.Errorf("asset %q is a directory or empty", name)
			}
		})
	}
}

func TestWriteStandalone(t *testing.T) {
	t.Parallel()
	docDir := filepath.Join(t.TempDir(), "my docs")
	if err := os.MkdirAll(docDir, 0o700); err != nil {
		t.Fatal(err)
	}
	mdPath := filepath.Join(docDir, "plan.md")
	src := "# Plan\n\n![shot](img/a.png) [spec](spec.md) [top](#plan)\n\n$x$\n"
	if err := os.WriteFile(mdPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(t.TempDir(), "out")
	index, err := WriteStandalone(mdPath, outDir, DefaultTheme())
	if err != nil {
		t.Fatalf("WriteStandalone: %v", err)
	}
	if filepath.Dir(filepath.Dir(index)) != outDir || filepath.Base(index) != "index.html" {
		t.Errorf("WriteStandalone returned %q, want %s/<key>/index.html", index, outDir)
	}
	data, err := os.ReadFile(index)
	if err != nil {
		t.Fatalf("read page: %v", err)
	}
	page := string(data)

	m := baseElem.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("page has no <base>:\n%s", page)
	}
	if want := FileURL(docDir) + "/"; m[1] != want {
		t.Errorf("<base href> = %q, want %q", m[1], want)
	}
	if strings.Contains(page, "data-bunk-theme") || !strings.Contains(page, darkScheme+"{\n:root{color-scheme:dark;") {
		t.Error("the page does not follow the system's colour scheme")
	}
	for _, want := range []string{`<title>plan.md</title>`, `src="img/a.png"`, `href="spec.md"`, `href="#plan"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	policy := pageCSP(t, page)
	if got := strings.Join(directive(policy, "script-src"), " "); got != "file: 'wasm-unsafe-eval'" {
		t.Errorf("script-src = %q, want file: 'wasm-unsafe-eval'", got)
	}
	if got := strings.Join(directive(policy, "base-uri"), " "); got != "'self' file:" {
		t.Errorf("base-uri = %q, want 'self' file:", got)
	}

	var assetURLs []string
	for _, m := range urlAttr.FindAllStringSubmatch(baseElem.ReplaceAllString(page, ""), -1) {
		if strings.HasPrefix(m[1], "file://") {
			assetURLs = append(assetURLs, m[1])
		}
	}
	if len(assetURLs) < 3 {
		t.Fatalf("page has %d file:// asset URLs, want at least 3: %q", len(assetURLs), assetURLs)
	}
	for _, raw := range assetURLs {
		u, err := url.Parse(raw)
		if err != nil {
			t.Errorf("asset URL %q: %v", raw, err)
			continue
		}
		if !strings.HasPrefix(u.Path, filepath.Join(outDir, "assets")+"/") {
			t.Errorf("asset URL %q is outside %s/assets", raw, outDir)
		}
		if _, err := os.Stat(u.Path); err != nil {
			t.Errorf("asset URL %q: %v", raw, err)
		}
	}
	err = fs.WalkDir(Assets(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := os.Stat(filepath.Join(outDir, "assets", filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("asset %q was not copied: %v", name, err)
			return nil
		}
		if info.Mode().Perm() != fileMode {
			t.Errorf("asset %q has mode %v, want %v", name, info.Mode().Perm(), fs.FileMode(fileMode))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk assets: %v", err)
	}

	// The same document gets the same page, and the assets stay as they
	// are; another document gets a page of its own beside them.
	t.Run("again", func(t *testing.T) {
		js := filepath.Join(outDir, "assets", "bunk-doc.js")
		old := time.Now().Add(-time.Hour)
		must(t, os.Chtimes(js, old, old))
		again, err := WriteStandalone(mdPath, outDir, DefaultTheme())
		if err != nil || again != index {
			t.Errorf("second WriteStandalone = %q, %v; want %q", again, err, index)
		}
		if fi, err := os.Stat(js); err != nil || !fi.ModTime().Equal(old) {
			t.Errorf("assets written again: %v", err)
		}
		other := filepath.Join(docDir, "spec.md")
		must(t, os.WriteFile(other, []byte("# Spec\n"), 0o600))
		page, err := WriteStandalone(other, outDir, DefaultTheme())
		if err != nil || filepath.Dir(filepath.Dir(page)) != outDir || page == index {
			t.Errorf("another document's page = %q, %v", page, err)
		}
		entries, err := os.ReadDir(outDir)
		if err != nil || len(entries) != 3 {
			t.Errorf("%s holds %d entries, want assets and two pages: %v", outDir, len(entries), err)
		}
	})
	t.Run("repairs assets", func(t *testing.T) {
		js := filepath.Join(outDir, "assets", "bunk-doc.js")
		must(t, os.Remove(js))
		if _, err := WriteStandalone(mdPath, outDir, DefaultTheme()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(js); err != nil {
			t.Errorf("a missing asset was not written again: %v", err)
		}
	})
	t.Run("missing document", func(t *testing.T) {
		if _, err := WriteStandalone(filepath.Join(docDir, "gone.md"), outDir, DefaultTheme()); err == nil {
			t.Error("WriteStandalone of a missing file returned no error")
		}
	})
}

// TestStandaloneLive: every kind of browser copy loads bunk-live.js, and
// each write leaves a new version.js beside the page, which the open copy
// watches to reload.
func TestStandaloneLive(t *testing.T) {
	t.Parallel()
	dir, root := t.TempDir(), t.TempDir()
	for _, name := range []string{"plan.md", "page.html", "notes.txt"} {
		src := "# Doc\n"
		if name == "page.html" {
			src = `<html><head><link rel="stylesheet" href="bunk-present://assets/bunk-doc.css"></head><body>x</body></html>`
		}
		writeTestFile(t, filepath.Join(dir, name), src)
		page, err := WriteStandalone(filepath.Join(dir, name), root, DefaultTheme())
		if err != nil {
			t.Fatal(err)
		}
		live := FileURL(filepath.Join(root, "assets", "bunk-live.js"))
		if s := readTestFile(t, page); !strings.Contains(s, `<script src="`+live+`" defer></script>`) {
			t.Errorf("%s: the copy does not load bunk-live.js:\n%s", name, s)
		}
		first := readTestFile(t, filepath.Join(filepath.Dir(page), versionFile))
		if !strings.HasPrefix(first, "bunkVersion(\"") {
			t.Errorf("%s: version.js is %q", name, first)
		}
		time.Sleep(time.Millisecond)
		if _, err := WriteStandalone(filepath.Join(dir, name), root, DefaultTheme()); err != nil {
			t.Fatal(err)
		}
		if again := readTestFile(t, filepath.Join(filepath.Dir(page), versionFile)); again == first {
			t.Errorf("%s: a second write kept version %q", name, first)
		}
	}
	if _, err := fs.Stat(Assets(), assetLive); err != nil {
		t.Errorf("Assets lacks %s: %v", assetLive, err)
	}
}
