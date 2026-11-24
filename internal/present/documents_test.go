package present

import (
	"io/fs"
	"strings"
	"testing"
	"time"
)

// TestRenderDocuments: the page groups documents by repository, this
// tab's first and the others by their newest document; each entry links
// to DocumentsOpen with its path, names its folder inside the repository,
// and the page loads its filter script from the assets.
func TestRenderDocuments(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 24, 16, 0, 0, 0, time.UTC)
	docs := []DocEntry{
		{Path: "/home/u/p2p/.specs/leave.md", Where: "/home/u/p2p", Agent: "claude", Shown: now.Add(-10 * time.Minute)},
		{Path: "/tmp/scratch/a b&c.html", Where: "/home/u/webfuse", Agent: "claude", Shown: now.Add(-time.Hour)},
		{Path: "/home/u/webfuse/.specs/guide.html", Where: "/home/u/webfuse", Agent: "codex", Shown: now.Add(-2 * time.Hour)},
		{Path: "/home/u/notes.md", Shown: now.Add(-48 * time.Hour)},
	}
	page, err := RenderDocuments(docs, "/home/u/webfuse", "/home/u", now, Options{AssetBase: AssetsURL})
	if err != nil {
		t.Fatal(err)
	}
	s := string(page)
	if strings.Contains(s, "ZgotmplZ") {
		t.Fatal("html/template refused a URL")
	}
	order := []string{`>webfuse <span class="bunk-badge info">this tab</span>`, "a b&amp;c.html", "guide.html", ">p2p</h2>", "leave.md", ">Other</h2>", "notes.md"}
	at := 0
	for _, want := range order {
		i := strings.Index(s[at:], want)
		if i < 0 {
			t.Fatalf("%q missing after offset %d, or out of order:\n%s", want, at, s)
		}
		at += i + len(want)
	}
	for _, want := range []string{
		`href="` + DocumentsURL + DocumentsOpen + "?path=%2Ftmp%2Fscratch%2Fa%20b%26c.html" + `"`,
		`<span class="bunk-docs-dir">.specs</span>`,       // inside its repository
		`<span class="bunk-docs-dir">/tmp/scratch</span>`, // outside it
		`<span class="bunk-docs-dir">~</span>`,
		`<span class="bunk-docs-agent">codex</span>`,
		`<time datetime="2026-11-24T15:50:00Z">15:50</time>`,
		`src="` + AssetsURL + assetDocuments + `"`,
		`id="bunk-docs-filter"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if _, err := fs.Stat(Assets(), assetDocuments); err != nil {
		t.Errorf("the filter script is not an asset: %v", err)
	}

	empty, err := RenderDocuments(nil, "", "/home/u", now, Options{AssetBase: AssetsURL})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(empty), "No documents yet.") || strings.Contains(string(empty), "bunk-docs-filter\"") {
		t.Errorf("empty page:\n%s", empty)
	}
}

func TestShownLabel(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 24, 16, 0, 0, 0, time.UTC) // a Tuesday
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{now.Add(-15 * time.Hour), "01:00"},
		{now.Add(-17 * time.Hour), "yesterday"},
		{now.Add(-3 * 24 * time.Hour), "Saturday"},
		{now.Add(-8 * 24 * time.Hour), "Nov 16"},
		{now.AddDate(-1, 0, 0), "Nov 24, 2025"},
	} {
		if got := shownLabel(c.t, now); got != c.want {
			t.Errorf("shownLabel(%s) = %q, want %q", c.t, got, c.want)
		}
	}
}
