package restore

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDocumentsPath(t *testing.T) {
	env := map[string]string{"XDG_STATE_HOME": "/state", "HOME": "/home/u"}
	if got := DocumentsPath(func(k string) string { return env[k] }); got != "/state/bunk-gui/documents.json" {
		t.Errorf("DocumentsPath = %q", got)
	}
	delete(env, "XDG_STATE_HOME")
	if got := DocumentsPath(func(k string) string { return env[k] }); got != "/home/u/.local/state/bunk-gui/documents.json" {
		t.Errorf("DocumentsPath without XDG_STATE_HOME = %q", got)
	}
	if got := DocumentsPath(func(string) string { return "" }); got != "" {
		t.Errorf("DocumentsPath without a home = %q", got)
	}
}

// TestRecordDoc: the list is newest first, an entry shown again moves up
// and keeps its conversation when a person reopens it, a restored window
// changes nothing it knows, and the file is the user's alone.
func TestRecordDoc(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bunk-gui", "documents.json")
	t0 := time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC)
	record := func(d Doc, keep bool) {
		t.Helper()
		if err := RecordDoc(path, d, keep); err != nil {
			t.Fatal(err)
		}
	}
	record(Doc{Path: "/r/a.html", Where: "/r", Agent: "claude", Session: "s1", Shown: t0}, false)
	record(Doc{Path: "/r/b.md", Where: "/r", Agent: "claude", Session: "s1", Shown: t0.Add(time.Minute)}, false)
	record(Doc{Path: "/q/c.md", Where: "/q", Shown: t0.Add(2 * time.Minute)}, false)
	record(Doc{Path: "/r/a.html", Shown: t0.Add(3 * time.Minute)}, false)                   // reopened by hand
	record(Doc{Path: "/r/b.md", Where: "/elsewhere", Shown: t0.Add(4 * time.Minute)}, true) // a restored window

	docs := LoadDocs(path)
	var order []string
	for _, d := range docs {
		order = append(order, d.Path)
	}
	if fmt.Sprint(order) != "[/r/a.html /q/c.md /r/b.md]" {
		t.Fatalf("order %v", order)
	}
	if a := docs[0]; a.Where != "/r" || a.Agent != "claude" || a.Session != "s1" || !a.Shown.Equal(t0.Add(3*time.Minute)) {
		t.Errorf("reopened entry %+v", a)
	}
	if b := docs[2]; b.Where != "/r" || !b.Shown.Equal(t0.Add(time.Minute)) {
		t.Errorf("a restored window changed %+v", b)
	}
	if got := SessionDocs(docs, "claude", "s1"); len(got) != 2 || got[0].Path != "/r/a.html" {
		t.Errorf("SessionDocs = %+v", got)
	}
	if got := SessionDocs(docs, "codex", "s1"); len(got) != 0 {
		t.Errorf("another agent's conversation got %+v", got)
	}
	if got := SessionDocs(docs, "claude", ""); got != nil {
		t.Errorf("no conversation got %+v", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("documents.json mode %v, want 0600", fi.Mode().Perm())
	}
}

// TestRecordDocKeepsMax drops the oldest beyond MaxDocs.
func TestRecordDocKeepsMax(t *testing.T) {
	path := filepath.Join(t.TempDir(), "documents.json")
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range MaxDocs + 5 {
		if err := RecordDoc(path, Doc{Path: fmt.Sprintf("/d/%d.md", i), Shown: t0.Add(time.Duration(i) * time.Second)}, false); err != nil {
			t.Fatal(err)
		}
	}
	docs := LoadDocs(path)
	if len(docs) != MaxDocs || docs[0].Path != fmt.Sprintf("/d/%d.md", MaxDocs+4) || docs[MaxDocs-1].Path != "/d/5.md" {
		t.Errorf("kept %d, first %s, last %s", len(docs), docs[0].Path, docs[len(docs)-1].Path)
	}
}

// TestRecordDocConcurrent: windows record at once and lose nothing.
func TestRecordDocConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "documents.json")
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if err := RecordDoc(path, Doc{Path: fmt.Sprintf("/d/%d.md", i), Shown: time.Now()}, false); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := len(LoadDocs(path)); n != 20 {
		t.Errorf("%d documents recorded, want 20", n)
	}
}

// TestLoadDocsDamaged: a damaged or missing file is an empty list.
func TestLoadDocsDamaged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "documents.json")
	if docs := LoadDocs(path); docs != nil {
		t.Errorf("missing file: %v", docs)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if docs := LoadDocs(path); docs != nil {
		t.Errorf("damaged file: %v", docs)
	}
	if err := RecordDoc(path, Doc{Path: "/a.md", Shown: time.Now()}, false); err != nil {
		t.Fatal(err)
	}
	if docs := LoadDocs(path); len(docs) != 1 {
		t.Errorf("after recording over a damaged file: %v", docs)
	}
}
