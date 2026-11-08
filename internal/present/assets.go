package present

import (
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
)

//go:embed assets
var assetFiles embed.FS

const assetRoot = "assets"

// Assets is everything AssetBase serves: the page runtime and stylesheet,
// Mermaid, KaTeX with its fonts, and their licences. Paths are relative:
// "bunk-doc.js", "mermaid.min.js", "katex/katex.min.css".
func Assets() fs.FS {
	sub, err := fs.Sub(assetFiles, assetRoot)
	if err != nil {
		// fs.Sub fails only for an invalid directory name.
		panic(fmt.Sprintf("present: assets: %v", err))
	}
	return sub
}

// The modes of the files WriteStandalone writes. Documents can be private,
// so only their owner reads them.
const (
	dirMode  = 0o700
	fileMode = 0o600
)

// WriteStandalone renders the Markdown file at mdPath into dir/index.html,
// with Assets copied to dir/assets, so a browser can open the page from
// disk. The page's <base> is the document's directory, so its relative
// links and images resolve there, and its asset URLs are absolute file://
// URLs into dir/assets. It returns the path of index.html.
func WriteStandalone(mdPath, dir string, theme Theme) (string, error) {
	src, err := os.ReadFile(mdPath)
	if err != nil {
		return "", fmt.Errorf("read document: %w", err)
	}
	absDoc, err := filepath.Abs(mdPath)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", mdPath, err)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", dir, err)
	}
	assetDir := filepath.Join(absDir, assetRoot)
	if err := copyAssets(assetDir); err != nil {
		return "", err
	}
	opts := Options{
		Title:     filepath.Base(mdPath),
		AssetBase: fileURL(assetDir) + "/",
		Theme:     theme,
	}
	page, err := renderPage(src, opts, fileURL(filepath.Dir(absDoc))+"/")
	if err != nil {
		return "", fmt.Errorf("render %s: %w", mdPath, err)
	}
	out := filepath.Join(absDir, "index.html")
	if err := os.WriteFile(out, page, fileMode); err != nil {
		return "", fmt.Errorf("write page: %w", err)
	}
	return out, nil
}

// fileURL returns the file:// URL of an absolute path.
func fileURL(p string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(p)}).String()
}

// copyAssets writes every file of Assets under dir.
func copyAssets(dir string) error {
	assets := Assets()
	err := fs.WalkDir(assets, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if d.IsDir() {
			return os.MkdirAll(target, dirMode)
		}
		data, err := fs.ReadFile(assets, name)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, fileMode)
	})
	if err != nil {
		return fmt.Errorf("copy assets to %s: %w", dir, err)
	}
	return nil
}
