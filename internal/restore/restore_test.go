package restore

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func twoPaneTab() Tab {
	return Tab{Name: "work", Focus: 1, Root: &Node{Ratio: 0.3, Vertical: true,
		A: &Node{Pane: &Pane{Cwd: "/src/bunk", Agent: "claude", AgentSession: "0f3c2a6e-5d1b", History: "0-0"}},
		B: &Node{Pane: &Pane{Cwd: "/src/bunk", Remote: []string{"ssh", "-p", "2222", "gpu01"}}},
	}}
}

func TestSlotRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "windows"))
	if err != nil {
		t.Fatal(err)
	}
	sl, err := st.New()
	if err != nil {
		t.Fatal(err)
	}
	w := Window{Width: 1200, Height: 800, Selected: 1, Tabs: []Tab{{Root: &Node{Pane: &Pane{Cwd: "/"}}}, twoPaneTab()}}
	if err := sl.Save(w); err != nil {
		t.Fatal(err)
	}
	if err := sl.SaveHistory("0-0", []byte("hello\r\n")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(st.Dir(), sl.ID()+".json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("snapshot file: %v, mode %v; want 0600", err, fi.Mode())
	}
	// A running window's snapshot is not offered.
	if saved := st.Saved(); len(saved) != 0 {
		t.Errorf("Saved while the window holds its slot: %d", len(saved))
	}
	if n := st.Running(nil); n != 1 {
		t.Errorf("Running = %d, want 1", n)
	}
	if _, err := st.Claim(sl.ID()); !errors.Is(err, ErrClaimed) {
		t.Errorf("Claim of a held slot: %v, want ErrClaimed", err)
	}
	sl.Release()

	saved := st.Saved()
	if len(saved) != 1 || saved[0].ID != sl.ID() {
		t.Fatalf("Saved = %+v", saved)
	}
	got := saved[0].Window
	if got.V != Version || time.Since(got.Saved) > time.Minute {
		t.Errorf("stamp: v %d, saved %v", got.V, got.Saved)
	}
	got.V, got.Saved = 0, time.Time{}
	if !reflect.DeepEqual(got, w) {
		t.Errorf("loaded %+v\nwant %+v", got, w)
	}
	again, err := st.Claim(sl.ID())
	if err != nil {
		t.Fatal(err)
	}
	if h, err := again.History("0-0"); err != nil || string(h) != "hello\r\n" {
		t.Errorf("History = %q, %v", h, err)
	}
	again.KeepHistory(map[string]bool{})
	if _, err := again.History("0-0"); err == nil {
		t.Error("KeepHistory kept a file it was not told to")
	}
	if err := again.Remove(); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(st.Dir()); len(ents) != 0 {
		t.Errorf("Remove left %v", ents)
	}
}

func TestSavedOrderAndSkips(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := range 3 {
		sl, err := st.New()
		if err != nil {
			t.Fatal(err)
		}
		if err := sl.Save(Window{Tabs: []Tab{{Root: &Node{Pane: &Pane{Cwd: string(rune('a' + i))}}}}}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sl.ID())
		sl.Release()
		time.Sleep(2 * time.Millisecond)
	}
	// Broken files, another version and a lock without a snapshot.
	write := func(name, data string) {
		if err := os.WriteFile(filepath.Join(st.Dir(), name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("abc.json", "{")
	write("abd.json", `{"v":99,"tabs":[{"root":{"pane":{}}}]}`)
	write("abe.json", `{"v":1,"tabs":[{"root":{"ratio":0.5,"a":{"pane":{}}}}]}`)
	write("abf.lock", "")
	saved := st.Saved()
	var got []string
	for _, s := range saved {
		got = append(got, s.ID)
	}
	if want := []string{ids[2], ids[1], ids[0]}; !reflect.DeepEqual(got, want) {
		t.Errorf("Saved = %v, want newest first %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(st.Dir(), "abf.lock")); err == nil {
		t.Error("an orphan lock stayed")
	}
	if err := st.Discard(ids[0]); err != nil {
		t.Fatal(err)
	}
	if len(st.Saved()) != 2 {
		t.Error("Discard kept the snapshot")
	}
}

func TestClaimRejectsPaths(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../x", "ABC", "a/b"} {
		if _, err := st.Claim(id); err == nil {
			t.Errorf("Claim(%q) passed", id)
		}
	}
	sl, err := st.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sl.Release()
	for _, name := range []string{"", "..", "../x", "a/b"} {
		if err := sl.SaveHistory(name, nil); err == nil {
			t.Errorf("SaveHistory(%q) passed", name)
		}
	}
}

func TestDefaultDir(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"XDG_STATE_HOME": "/s", "HOME": "/h"}, "/s/bunk-gui/windows"},
		{map[string]string{"XDG_STATE_HOME": "rel", "HOME": "/h"}, "/h/.local/state/bunk-gui/windows"},
		{map[string]string{"HOME": "/h"}, "/h/.local/state/bunk-gui/windows"},
		{map[string]string{}, ""},
	} {
		if got := DefaultDir(env(tc.env)); got != tc.want {
			t.Errorf("DefaultDir(%v) = %q, want %q", tc.env, got, tc.want)
		}
	}
}

func TestResumeCommands(t *testing.T) {
	pane := func(agent, id, cwd, host string) *Node {
		return &Node{Pane: &Pane{Agent: agent, AgentSession: id, Cwd: cwd, Host: host}}
	}
	split := func(a, b *Node) *Node { return &Node{Ratio: 0.5, A: a, B: b} }
	w := Window{Tabs: []Tab{
		{Root: split(pane("claude", "abc-1", "/a", ""), pane("codex", "", "/b", ""))},
		{Root: split(pane("copilot", "c1", "/c", ""), pane("gemini", "", "/d", ""))},
		{Root: split(pane("claude", "", "/e", ""), pane("claude", "", "/e", ""))},
		{Root: split(pane("claude", "x1", "/f", "gpu01"), pane("aider", "", "/g", ""))},
		{Root: split(pane("claude", "-rm", "/h", ""), pane("", "", "/i", ""))},
		{Root: pane("codex", "", "/j", "")},
	}}
	// The other window ran codex in /j too: its latest may be that one.
	other := Window{Tabs: []Tab{{Root: pane("codex", "", "/j", "")}}}
	got := ResumeCommands(w, []Window{w, other})
	want := [][]string{
		{"claude --resume abc-1", "codex resume --last"},
		{"copilot --resume=c1", "gemini --resume latest"},
		{"", ""},
		{"", ""},
		{"claude --continue", ""}, // an ID that could pass for an option is no ID
		{""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ResumeCommands =\n%q\nwant\n%q", got, want)
	}
}

func TestShellQuote(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"ssh", "-p", "2222", "user@gpu01"}, "ssh -p 2222 user@gpu01"},
		{[]string{"ssh", "host", "echo it's"}, `ssh host 'echo it'\''s'`},
		{[]string{"mosh", ""}, "mosh ''"},
		{[]string{"ssh", "a\nb"}, ""},
	} {
		if got := ShellQuote(tc.argv); got != tc.want {
			t.Errorf("ShellQuote(%q) = %q, want %q", tc.argv, got, tc.want)
		}
	}
}

func TestSummary(t *testing.T) {
	w := Window{Tabs: []Tab{twoPaneTab(), {Root: &Node{Pane: &Pane{}}}}}
	if got := Summary([]Window{w}); got != "2 tabs, 1 agent" {
		t.Errorf("Summary = %q", got)
	}
	if got := Summary([]Window{w, {Tabs: []Tab{{Root: &Node{Pane: &Pane{}}}}}}); got != "2 windows, 3 tabs, 1 agent" {
		t.Errorf("Summary = %q", got)
	}
}
