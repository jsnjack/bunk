package present

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sync"
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

// CacheDir is where pages for a web browser go, under the user's cache
// directory: one directory per document and the assets they share.
const CacheDir = "bunk/present"

// AssetsURL is where bunk-gui's panel serves Assets. HTML pages load the
// page runtime, its stylesheet, Mermaid and KaTeX from it.
const AssetsURL = "bunk-present://assets/"

// NeedsPage reports whether a browser needs a page that WriteStandalone
// writes to show the file at path of kind: Markdown and text are
// rendered, and an HTML page that loads Assets from AssetsURL, or uses
// the theme's colours (ThemeCSS), gets a copy that loads them from disk
// and has the colours. A browser opens other files as they are.
func NeedsPage(path string, kind Kind) bool {
	switch kind {
	case KindMarkdown, KindText:
		return true
	case KindHTML:
		src, err := os.ReadFile(path)
		return err == nil && (bytes.Contains(src, []byte(AssetsURL)) || bytes.Contains(src, []byte("var(--bunk-")))
	}
	return false
}

// WriteStandalone writes the page a browser opens from disk for the file
// at path (NeedsPage): root/<key>/index.html, where key stands for the
// file's path, so each file has one page however often it opens. Pages
// share root/assets, which is written only when it is missing or differs
// from Assets. The page's <base> is the file's directory, so its relative
// links and images resolve there, and its asset URLs are absolute file://
// URLs into root/assets. It returns the page's path.
func WriteStandalone(path, root string, theme Theme) (string, error) {
	kind, err := Preview(path)
	if err != nil {
		return "", err
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read document: %w", err)
	}
	absDoc, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", root, err)
	}
	assetDir := filepath.Join(absRoot, assetRoot)
	if err := copyAssets(assetDir); err != nil {
		return "", err
	}
	opts := Options{
		Title:     filepath.Base(path),
		AssetBase: FileURL(assetDir) + "/",
		Theme:     theme,
	}
	base := FileURL(filepath.Dir(absDoc)) + "/"
	var page []byte
	switch kind {
	case KindMarkdown:
		page, err = renderPage(src, opts, base)
	case KindText:
		page, err = renderPreview(filepath.Base(path), kind, src, opts, base)
	case KindHTML:
		page = bytes.ReplaceAll(src, []byte(AssetsURL), []byte(opts.AssetBase))
		page = InsertHead(page, `<base href="`+html.EscapeString(base)+`"><style>`+ThemeCSS(theme)+`</style>`)
	default:
		err = fmt.Errorf("a browser opens %s files as they are", kind)
	}
	if err != nil {
		return "", fmt.Errorf("render %s: %w", path, err)
	}
	sum := sha256.Sum256([]byte(absDoc))
	dir := filepath.Join(absRoot, hex.EncodeToString(sum[:8]))
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	out := filepath.Join(dir, "index.html")
	if err := writeAtomic(out, page); err != nil {
		return "", fmt.Errorf("write page: %w", err)
	}
	return out, nil
}

// FileURL returns the file:// URL of an absolute path, escaped.
func FileURL(p string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(p)}).String()
}

// assetStamp names the content of Assets; copyAssets writes it last into
// the copy, as stampFile.
var assetStamp = sync.OnceValue(func() string {
	h := sha256.New()
	err := fs.WalkDir(Assets(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(Assets(), name)
		if err != nil {
			return err
		}
		h.Write(fmt.Appendf(nil, "%s\x00%d\x00", name, len(data)))
		h.Write(data)
		return nil
	})
	if err != nil {
		panic(fmt.Sprintf("present: assets: %v", err)) // embedded: cannot fail
	}
	return hex.EncodeToString(h.Sum(nil))
})

const stampFile = ".stamp"

// copyAssets writes every file of Assets under dir, unless dir holds them
// already: its stamp matches and every file is there with its size.
func copyAssets(dir string) error {
	if assetsCurrent(dir) {
		return nil
	}
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
		return writeAtomic(target, data)
	})
	if err == nil {
		err = writeAtomic(filepath.Join(dir, stampFile), []byte(assetStamp()))
	}
	if err != nil {
		return fmt.Errorf("copy assets to %s: %w", dir, err)
	}
	return nil
}

// assetsCurrent reports whether dir holds the current Assets.
func assetsCurrent(dir string) bool {
	stamp, err := os.ReadFile(filepath.Join(dir, stampFile))
	if err != nil || string(stamp) != assetStamp() {
		return false
	}
	err = fs.WalkDir(Assets(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		want, err := d.Info()
		if err != nil {
			return err
		}
		got, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil || got.Size() != want.Size() {
			return fs.ErrNotExist
		}
		return nil
	})
	return err == nil
}

// writeAtomic writes data to path through a temporary file in its
// directory, so a browser, or another bunk writing the same file, never
// sees half of it.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), fileMode)
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}
