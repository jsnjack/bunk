package main

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestTOMLEdits: the line editor changes top-level keys and [[agents]]
// entries, keeps comments (with their spacing), order and other tables, and
// leaves no double or trailing blank lines when an entry goes.
func TestTOMLEdits(t *testing.T) {
	const cfg = `# bunk-gui test settings
tabs = "rail"   # side
font_size = 11

[keys]
new_tab = "ctrl+alt+n"   # mine

# agents I use
[[agents]]
name = "aider"   # the pair programmer
processes = ["aider"]

[[agents]]
name = "goose"
processes = ["goose"]
scripts = ["goose/cli.py"]
`
	steps := []struct {
		name string
		edit func(string) string
		want string
	}{
		{"set a top-level key", func(s string) string {
			return patchTable(s, "", []keysEdit{{action: "tabs", value: tomlString("bar")}})
		}, strings.Replace(cfg, `tabs = "rail"   # side`, `tabs = "bar"   # side`, 1)},
		{"add top-level keys", func(s string) string {
			return patchTable(s, "", []keysEdit{{action: "rail_open", value: "true"}, {action: "font", value: tomlString(`Iosevka "Term"`)}})
		}, strings.Replace(cfg, "font_size = 11\n", "font_size = 11\nrail_open = true\nfont = \"Iosevka \\\"Term\\\"\"\n", 1)},
		{"edit an entry", func(s string) string {
			return patchArrayEntry(s, "agents", 0, []keysEdit{{action: "name", value: tomlString("aider")},
				{action: "processes", value: tomlStrings([]string{"aider", "aider-chat"})}, {action: "scripts", remove: true}})
		}, strings.Replace(cfg, `processes = ["aider"]`, `processes = ["aider", "aider-chat"]`, 1)},
		{"remove a key of an entry", func(s string) string {
			return patchArrayEntry(s, "agents", 1, []keysEdit{{action: "scripts", remove: true}})
		}, strings.Replace(cfg, "scripts = [\"goose/cli.py\"]\n", "", 1)},
		{"remove the last entry", func(s string) string {
			return removeArrayEntry(s, "agents", 1)
		}, strings.Replace(cfg, "\n[[agents]]\nname = \"goose\"\nprocesses = [\"goose\"]\nscripts = [\"goose/cli.py\"]\n", "", 1)},
		{"remove the first entry", func(s string) string {
			return removeArrayEntry(s, "agents", 0)
		}, strings.Replace(cfg, "[[agents]]\nname = \"aider\"   # the pair programmer\nprocesses = [\"aider\"]\n\n", "", 1)},
		{"append an entry", func(s string) string {
			return appendArrayEntry(s, "agents", []keysEdit{{action: "name", value: tomlString("crush")},
				{action: "processes", value: tomlStrings([]string{"crush"})}})
		}, cfg + "\n[[agents]]\nname = \"crush\"\nprocesses = [\"crush\"]\n"},
	}
	for _, st := range steps {
		got := st.edit(cfg)
		if got != st.want {
			t.Errorf("%s:\n%s\nwant:\n%s", st.name, got, st.want)
		}
		var parsed Settings
		if _, err := toml.Decode(got, &parsed); err != nil {
			t.Errorf("%s: the result does not parse: %v", st.name, err)
		}
	}
	// Removing an entry from the end of a file and appending another gives
	// one blank line between tables, not two.
	got := appendArrayEntry(removeArrayEntry(cfg, "agents", 1), "agents", []keysEdit{{action: "name", value: `"x"`}})
	if !strings.HasSuffix(got, "processes = [\"aider\"]\n\n[[agents]]\nname = \"x\"\n") {
		t.Errorf("remove then append:\n%s", got)
	}
	// Top-level keys in a file that has none go above the first table and
	// the comments attached to it.
	if got := patchTable("# keys\n[keys]\nzoom = \"f12\"\n", "", []keysEdit{{action: "tabs", value: `"bar"`}}); got != "tabs = \"bar\"\n\n# keys\n[keys]\nzoom = \"f12\"\n" {
		t.Errorf("first top-level key: %q", got)
	}
	if got := patchTable("", "", []keysEdit{{action: "tabs", value: `"bar"`}}); got != "tabs = \"bar\"\n" {
		t.Errorf("empty file: %q", got)
	}
	if got := tomlString("a\"b\\c\nd\x01"); got != `"a\"b\\c\nd\u0001"` {
		t.Errorf("tomlString: %s", got)
	}
}
