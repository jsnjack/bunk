package present

import (
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	urlAttr  = regexp.MustCompile(`(?:src|href)="([^"]*)"`)
	jsLoad   = regexp.MustCompile(`load\('([^']+)'\)`)
	cssURL   = regexp.MustCompile(`url\(([^)]*)\)`)
	baseElem = regexp.MustCompile(`<base href="([^"]*)">`)
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
	loads := jsLoad.FindAllStringSubmatch(string(script), -1)
	if len(loads) != 2 {
		t.Errorf("the runtime loads %d scripts, want KaTeX and Mermaid", len(loads))
	}
	for _, m := range loads {
		names = append(names, m[1])
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
	names = append(names, "mermaid.LICENSE", "katex/LICENSE")
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
	if want := filepath.Join(outDir, "index.html"); index != want {
		t.Errorf("WriteStandalone returned %q, want %q", index, want)
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
	if want := fileURL(docDir) + "/"; m[1] != want {
		t.Errorf("<base href> = %q, want %q", m[1], want)
	}
	for _, want := range []string{`<title>plan.md</title>`, `src="img/a.png"`, `href="spec.md"`, `href="#plan"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	policy := pageCSP(t, page)
	if got := strings.Join(directive(policy, "script-src"), " "); got != "file:" {
		t.Errorf("script-src = %q, want file:", got)
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

	t.Run("again", func(t *testing.T) {
		if _, err := WriteStandalone(mdPath, outDir, DefaultTheme()); err != nil {
			t.Errorf("second WriteStandalone into the same directory: %v", err)
		}
	})
	t.Run("missing document", func(t *testing.T) {
		if _, err := WriteStandalone(filepath.Join(docDir, "gone.md"), outDir, DefaultTheme()); err == nil {
			t.Error("WriteStandalone of a missing file returned no error")
		}
	})
}
