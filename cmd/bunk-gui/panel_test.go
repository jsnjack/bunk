package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuestionText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                           string
		path, section, quote, question string
		want                           string
	}{
		{"section", "docs/plan.md", "Session storage", "Sessions live in Redis.", "Why not Postgres?",
			"About docs/plan.md › Session storage:\n> Sessions live in Redis.\nWhy not Postgres?"},
		{"no section, no question", "plan.md", "", "Intro", "",
			"About plan.md:\n> Intro"},
		{"lines trimmed, blank runs collapsed", "a.md", "S", "one  \n\n\n\ntwo\t\n", "  q  ",
			"About a.md › S:\n> one\n>\n> two\nq"},
	}
	for _, c := range cases {
		if got := questionText(c.path, c.section, c.quote, c.question); got != c.want {
			t.Errorf("%s:\n%q\nwant\n%q", c.name, got, c.want)
		}
	}
	long := strings.Repeat("é", maxQuote)
	got := questionText("a.md", "", long, "")
	if len(got) > maxQuote+40 || !strings.HasSuffix(got, "…") {
		t.Errorf("long quote: %d bytes, ends %q", len(got), got[len(got)-8:])
	}
	if !strings.HasPrefix(got, "About a.md:\n> é") || strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("long quote cut inside a character: %q", got[:40])
	}
}

func TestDisplayPath(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	cases := []struct{ path, cwd, want string }{
		{"/repo/docs/plan.md", "/repo", "docs/plan.md"},
		{"/repo/plan.md", "/repo", "plan.md"},
		{"/other/plan.md", "/repo", "/other/plan.md"},
		{"/repo/plan.md", "", "/repo/plan.md"},
		{filepath.Join(home, "x", "plan.md"), "/repo", "~/x/plan.md"},
	}
	for _, c := range cases {
		if got := displayPath(c.path, c.cwd); got != c.want {
			t.Errorf("displayPath(%q, %q) = %q, want %q", c.path, c.cwd, got, c.want)
		}
	}
}

// TestDocFile: a document's page reads its own directory, and nothing
// hidden, nothing outside it and nothing beside a document in the home
// directory, whichever path leads there.
func TestDocFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	dir := filepath.Join(root, "proj", "docs")
	outside := filepath.Join(root, "secret.txt")
	homeDoc := filepath.Join(root, "home", "notes.html")
	for _, f := range []string{filepath.Join(dir, "plan.md"), filepath.Join(dir, "img", "a.png"), filepath.Join(dir, ".env"),
		filepath.Join(dir, ".git", "config"), outside, homeDoc, filepath.Join(root, "home", "id_rsa")} {
		must(t, os.MkdirAll(filepath.Dir(f), 0o755))
		must(t, os.WriteFile(f, []byte("x"), 0o600))
	}
	must(t, os.Symlink(outside, filepath.Join(dir, "link.txt")))
	must(t, os.Symlink(filepath.Join(dir, "img"), filepath.Join(dir, "pics")))
	// Another path to the home directory, as /home is on Fedora Atomic.
	must(t, os.Symlink(filepath.Join(root, "home"), filepath.Join(root, "alias")))
	aliasDoc := filepath.Join(root, "alias", "notes.html")
	doc := filepath.Join(dir, "plan.md")
	cases := []struct {
		doc, rel string
		err      error
	}{
		{doc, "", nil},
		{doc, "plan.md", nil},
		{doc, "img/a.png", nil},
		{doc, "pics/a.png", nil}, // a link that stays inside
		{doc, ".env", errRefused},
		{doc, ".git/config", errRefused},
		{doc, "link.txt", errRefused},
		{doc, "../../secret.txt", errRefused},
		{doc, "missing.png", errNotFound},
		{homeDoc, "", nil},
		{homeDoc, "notes.html", nil},
		{homeDoc, "id_rsa", errRefused},
		{aliasDoc, "", nil},
		{aliasDoc, "id_rsa", errRefused},
	}
	for _, c := range cases {
		got, err := docFile(c.doc, c.rel)
		if !errors.Is(err, c.err) {
			t.Errorf("docFile(%s, %q) = %q, %v; want %v", filepath.Base(c.doc), c.rel, got, err, c.err)
		}
		if err == nil && !strings.HasPrefix(got, filepath.Dir(c.doc)+"/") {
			t.Errorf("docFile(%s, %q) = %q, outside the document's directory", filepath.Base(c.doc), c.rel, got)
		}
	}
}

func TestWithCSP(t *testing.T) {
	t.Parallel()
	meta := `<meta http-equiv="Content-Security-Policy" content="P">`
	cases := []struct{ page, want string }{
		{`<!doctype html><html><head><title>t</title></head><body>b</body></html>`,
			`<!doctype html><html><head>` + meta + `<title>t</title></head><body>b</body></html>`},
		{`<HTML lang="en"><HEAD class="x"><script src="a.js"></script></HEAD></HTML>`,
			`<HTML lang="en"><HEAD class="x">` + meta + `<script src="a.js"></script></HEAD></HTML>`},
		{`<!DOCTYPE html><html><body>no head</body></html>`,
			`<!DOCTYPE html><html><head>` + meta + `</head><body>no head</body></html>`},
		{`<!doctype html><p>fragment`, `<!doctype html><head>` + meta + `</head><p>fragment`},
		{`<p>fragment`, `<head>` + meta + `</head><p>fragment`},
		{`<header>not a head</header><head></head>`, `<header>not a head</header><head>` + meta + `</head>`},
	}
	for _, c := range cases {
		if got := string(withCSP([]byte(c.page), "P")); got != c.want {
			t.Errorf("withCSP(%q)\n= %q\nwant %q", c.page, got, c.want)
		}
	}
}

func TestEscapePath(t *testing.T) {
	t.Parallel()
	if got := escapePath("my docs/plan #1?.md"); got != "my%20docs/plan%20%231%3F.md" {
		t.Errorf("escapePath = %q", got)
	}
}
