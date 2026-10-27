package main

import (
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
)

func TestParseCombo(t *testing.T) {
	for _, c := range []struct {
		in, out string
		key     uint
		mods    gdk.ModifierType
	}{
		{"ctrl+shift+j", "ctrl+shift+j", gdk.KEY_j, gdk.ControlMask | gdk.ShiftMask},
		{"Ctrl+Shift+J", "ctrl+shift+j", gdk.KEY_j, gdk.ControlMask | gdk.ShiftMask},
		{"alt+f1", "alt+f1", gdk.KEY_F1, gdk.AltMask},
		{"f12", "f12", gdk.KEY_F12, 0},
		{"ctrl+f12", "ctrl+f12", gdk.KEY_F12, gdk.ControlMask},
		{"shift+pgup", "shift+pgup", gdk.KEY_Page_Up, gdk.ShiftMask},
		{"shift+pageup", "shift+pgup", gdk.KEY_Page_Up, gdk.ShiftMask},
		{"ctrl+=", "ctrl+equal", gdk.KEY_equal, gdk.ControlMask},
		{"ctrl++", "ctrl+plus", gdk.KEY_plus, gdk.ControlMask},
		{"ctrl+shift+space", "ctrl+shift+space", gdk.KEY_space, gdk.ControlMask | gdk.ShiftMask},
		{"ctrl+1", "ctrl+1", gdk.KEY_1, gdk.ControlMask},
		{"ctrl+kp_add", "ctrl+kp_add", gdk.KEY_KP_Add, gdk.ControlMask},
		{"ctrl+?", "ctrl+question", gdk.KEY_question, gdk.ControlMask},
		{"super+enter", "shift+super+enter", 0, 0}, // checked below
	} {
		got, err := parseCombo(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if c.key == 0 {
			continue
		}
		if got.key != c.key || got.mods != c.mods || got.String() != c.out {
			t.Errorf("%q: got %v %v %q", c.in, got.key, got.mods, got.String())
		}
	}
	for _, bad := range []string{"", "ctrl+", "hyper+a", "ctrl+banana", "ctrl+shift"} {
		if _, err := parseCombo(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
	if l := mustCombo(t, "ctrl+shift+question").Label(); l != "Ctrl+Shift+?" {
		t.Errorf("label %q", l)
	}
	if l := mustCombo(t, "ctrl+pgdn").Label(); l != "Ctrl+PgDn" {
		t.Errorf("label %q", l)
	}
}

func mustCombo(t *testing.T, s string) keyCombo {
	t.Helper()
	c, err := parseCombo(s)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCollides(t *testing.T) {
	for s, want := range map[string]bool{
		"ctrl+j": true, "alt+b": true, "ctrl+1": true, "ctrl+equal": true, "ctrl+space": true,
		"f1": true, "f12": true, "alt+left": true, "ctrl+up": true,
		"ctrl+c": true, "ctrl+v": true, "ctrl+f": true, "ctrl+n": true, "ctrl+p": true, "ctrl+q": true,
		"ctrl+left": true, "ctrl+right": true, "alt+1": true, "alt+0": true, "alt+a": true,
		"f2": true, "f10": true, "f11": true, "ctrl+question": true,
		"ctrl+shift+j": false, "ctrl+shift+space": false, "ctrl+f12": false, "alt+f1": false,
		"shift+pgup": false, "ctrl+shift+question": false, "ctrl+pgdn": false,
		"ctrl+shift+o": false, "ctrl+shift+a": false, "ctrl+shift+pgup": false, "enter": false, "escape": false,
	} {
		if got := mustCombo(t, s).collides(); got != want {
			t.Errorf("%s: collides %v, want %v", s, got, want)
		}
	}
}

// defaultKeys are bunk-gui's default bindings: the keys of bunk's TUI and
// Ptyxis the user has today (AGENTS.md, Design Decisions).
var defaultKeys = map[string][]string{
	"new_tab":           {"f2"},
	"close_tab":         {"ctrl+shift+w"},
	"new_window":        {"ctrl+shift+n"},
	"rename_tab":        nil,
	"next_tab":          {"ctrl+right", "ctrl+down"},
	"prev_tab":          {"ctrl+left", "ctrl+up"},
	"move_tab_left":     {"ctrl+shift+pgup"},
	"move_tab_right":    {"ctrl+shift+pgdn"},
	"select_tab_1":      {"alt+1"},
	"select_tab_2":      {"alt+2"},
	"select_tab_3":      {"alt+3"},
	"select_tab_4":      {"alt+4"},
	"select_tab_5":      {"alt+5"},
	"select_tab_6":      {"alt+6"},
	"select_tab_7":      {"alt+7"},
	"select_tab_8":      {"alt+8"},
	"select_tab_9":      {"alt+9"},
	"select_tab_10":     {"alt+0"},
	"switcher":          {"ctrl+shift+o"},
	"next_needs_you":    {"alt+a"},
	"split":             {"f1"},
	"split_context":     {"alt+f1"},
	"zoom":              {"f12"},
	"nav_up":            {"alt+up"},
	"nav_down":          {"alt+down"},
	"nav_left":          {"alt+left"},
	"nav_right":         {"alt+right"},
	"scroll_up":         {"shift+pgup"},
	"scroll_down":       {"shift+pgdn"},
	"search":            {"ctrl+f", "ctrl+shift+f"},
	"search_next":       {"ctrl+n", "enter"},
	"search_prev":       {"ctrl+p"},
	"search_exit":       {"escape"},
	"copy":              {"ctrl+shift+c", "ctrl+c"},
	"paste":             {"ctrl+shift+v", "ctrl+v"},
	"select_all":        {"ctrl+shift+a"},
	"passthrough":       {"ctrl+f12"},
	"toggle_rail":       {"ctrl+shift+b"},
	"toggle_fullscreen": {"f11"},
	"font_bigger":       {"ctrl+equal", "ctrl+plus", "ctrl+kp_add"},
	"font_smaller":      {"ctrl+minus", "ctrl+kp_subtract"},
	"font_reset":        {"ctrl+0", "ctrl+kp_0"},
	"menu":              {"f10"},
	"preferences":       {"ctrl+comma"},
	"shortcuts":         {"ctrl+shift+question", "ctrl+question"},
	"quit":              {"ctrl+q", "ctrl+shift+q"},
}

// TestDefaultKeys: every action's default keys, as written and as bound
// with an empty [keys] table.
func TestDefaultKeys(t *testing.T) {
	m, problems := resolveKeys(nil)
	if len(problems) != 0 {
		t.Fatalf("defaults: %q", problems)
	}
	if len(keyActions) != len(defaultKeys) {
		t.Errorf("%d actions, %d in the table", len(keyActions), len(defaultKeys))
	}
	for _, act := range keyActions {
		t.Run(act.name, func(t *testing.T) {
			want, ok := defaultKeys[act.name]
			if !ok {
				t.Fatalf("no defaults listed for %s", act.name)
			}
			if !slices.Equal(act.defs, want) {
				t.Errorf("defaults %q, want %q", act.defs, want)
			}
			var bound []string
			for _, c := range m.bind[act.name] {
				bound = append(bound, c.String())
			}
			if !slices.Equal(bound, want) {
				t.Errorf("bound %q, want %q", bound, want)
			}
		})
	}
}

func TestResolveKeys(t *testing.T) {
	m, problems := resolveKeys(nil)
	if len(problems) != 0 {
		t.Fatalf("defaults: %q", problems)
	}
	// Keys shells and editors need stay with the terminal.
	for _, free := range []string{"ctrl+a", "ctrl+b", "ctrl+d", "ctrl+e", "ctrl+j", "ctrl+k", "ctrl+l", "ctrl+r",
		"ctrl+u", "ctrl+w", "ctrl+z", "ctrl+space", "ctrl+shift+t", "ctrl+tab", "ctrl+pgdn"} {
		if a, ok := m.action[mustCombo(t, free)]; ok {
			t.Errorf("%s is bound to %s; it must reach the terminal", free, a)
		}
	}

	var table map[string]any
	if _, err := toml.Decode(`
new_tab = "ctrl+alt+n"
switcher = "none"
zoom = ""
next_needs_you = ["ctrl+shift+j", "f9"]
split = "ctrl+banana"
frobnicate = "f3"
copy = "f9"
paste = "ctrl+alt+n"
`, &table); err != nil {
		t.Fatal(err)
	}
	m, problems = resolveKeys(table)
	all := strings.Join(problems, "; ")
	for _, want := range []string{"frobnicate", "ctrl+banana", "f9 is bound to both next_needs_you and copy",
		"ctrl+alt+n is bound to both new_tab and paste"} {
		if !strings.Contains(all, want) {
			t.Errorf("problems %q lack %q", all, want)
		}
	}
	check := func(action string, want ...string) {
		t.Helper()
		var got []string
		for _, c := range m.bind[action] {
			got = append(got, c.String())
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: %v, want %v", action, got, want)
		}
	}
	check("new_tab", "ctrl+alt+n")
	check("switcher")
	check("zoom")
	check("next_needs_you", "ctrl+shift+j", "f9")
	check("split") // only a bad key: nothing bound
	check("copy")  // f9 went to next_needs_you, listed first
	check("paste") // ctrl+alt+n went to new_tab
	check("quit", "ctrl+q", "ctrl+shift+q")
	if _, ok := m.action[mustCombo(t, "f2")]; ok {
		t.Error("the default new_tab key stays bound after rebinding")
	}
}

func TestKeymapMatch(t *testing.T) {
	m, _ := resolveKeys(nil)
	none := func() uint { return 0 }
	slash := func() uint { return gdk.KEY_slash }
	for _, c := range []struct {
		keyval  uint
		state   gdk.ModifierType
		unshift func() uint
		want    string
		combo   string
	}{
		{gdk.KEY_a, gdk.AltMask, none, "next_needs_you", "alt+a"},
		{gdk.KEY_J, gdk.ControlMask | gdk.ShiftMask, none, "", ""},
		{gdk.KEY_O, gdk.ControlMask | gdk.ShiftMask, none, "switcher", "ctrl+shift+o"},
		{gdk.KEY_space, gdk.ControlMask | gdk.ShiftMask, none, "", ""},
		{gdk.KEY_question, gdk.ControlMask | gdk.ShiftMask, slash, "shortcuts", "ctrl+shift+question"},
		{gdk.KEY_question, gdk.ControlMask, none, "shortcuts", "ctrl+question"},
		{gdk.KEY_plus, gdk.ControlMask | gdk.ShiftMask, none, "font_bigger", "ctrl+plus"}, // Ctrl+Shift+= on US layouts
		{gdk.KEY_Right, gdk.ControlMask, none, "next_tab", "ctrl+right"},
		{gdk.KEY_Left, gdk.ControlMask, none, "prev_tab", "ctrl+left"},
		{gdk.KEY_Down, gdk.ControlMask, none, "next_tab", "ctrl+down"},
		{gdk.KEY_Up, gdk.ControlMask, none, "prev_tab", "ctrl+up"},
		{gdk.KEY_Page_Up, gdk.ControlMask | gdk.ShiftMask, none, "move_tab_left", "ctrl+shift+pgup"},
		{gdk.KEY_Page_Down, gdk.ControlMask | gdk.ShiftMask, none, "move_tab_right", "ctrl+shift+pgdn"},
		{gdk.KEY_Tab, gdk.ControlMask, none, "", ""},
		{gdk.KEY_F12, gdk.ControlMask, none, "passthrough", "ctrl+f12"},
		{gdk.KEY_F12, 0, none, "zoom", "f12"},
		{gdk.KEY_F1, gdk.AltMask, none, "split_context", "alt+f1"},
		{gdk.KEY_F2, 0, none, "new_tab", "f2"},
		{gdk.KEY_F10, 0, none, "menu", "f10"},
		{gdk.KEY_F11, 0, none, "toggle_fullscreen", "f11"},
		{gdk.KEY_Page_Up, gdk.ShiftMask, none, "scroll_up", "shift+pgup"},
		{gdk.KEY_q, gdk.ControlMask, none, "quit", "ctrl+q"},
		{gdk.KEY_Q, gdk.ControlMask | gdk.ShiftMask, none, "quit", "ctrl+shift+q"},
		{gdk.KEY_c, gdk.ControlMask, none, "copy", "ctrl+c"},
		{gdk.KEY_C, gdk.ControlMask | gdk.ShiftMask, none, "copy", "ctrl+shift+c"},
		{gdk.KEY_v, gdk.ControlMask, none, "paste", "ctrl+v"},
		{gdk.KEY_A, gdk.ControlMask | gdk.ShiftMask, none, "select_all", "ctrl+shift+a"},
		{gdk.KEY_f, gdk.ControlMask, none, "search", "ctrl+f"},
		{gdk.KEY_n, gdk.ControlMask, none, "search_next", "ctrl+n"},
		{gdk.KEY_p, gdk.ControlMask, none, "search_prev", "ctrl+p"},
		{gdk.KEY_3, gdk.AltMask, none, "select_tab_3", "alt+3"},
		{gdk.KEY_0, gdk.AltMask, none, "select_tab_10", "alt+0"},
		{gdk.KEY_0, gdk.ControlMask, none, "font_reset", "ctrl+0"},
		{gdk.KEY_3, gdk.ControlMask, none, "", ""},
	} {
		got, combo, ok := m.match(c.keyval, c.state|gdk.LockMask, c.unshift)
		if ok != (c.want != "") || got != c.want || (ok && combo.String() != c.combo) {
			t.Errorf("%s with %v: %q by %q, want %q by %q", gdk.KeyvalName(c.keyval), c.state, got, combo.String(), c.want, c.combo)
		}
	}
}

func TestPatchKeys(t *testing.T) {
	const cfg = `# my bunk-gui settings
tabs = "bar"   # tabs on top

[keys]
# window keys
new_tab = "ctrl+alt+n"   # muscle memory
switcher = [
  "ctrl+shift+space",   # first
  "f9",
]
quit = "ctrl+shift+q"

[[agents]]
name = "aider"
processes = ["aider"]
`
	out := patchKeys(cfg, []keysEdit{
		{action: "switcher", value: `"none"`, was: `["ctrl+shift+space", "f9"]`},
		{action: "new_tab", value: `"ctrl+shift+t"`},
		{action: "zoom", value: `"f11"`},
	})
	want := `# my bunk-gui settings
tabs = "bar"   # tabs on top

[keys]
# window keys
new_tab = "ctrl+shift+t"   # muscle memory
switcher = "none"  # was ["ctrl+shift+space", "f9"]
quit = "ctrl+shift+q"
zoom = "f11"

[[agents]]
name = "aider"
processes = ["aider"]
`
	if out != want {
		t.Errorf("patched:\n%s\nwant:\n%s", out, want)
	}
	var parsed Settings
	if _, err := toml.Decode(out, &parsed); err != nil {
		t.Fatalf("patched file does not parse: %v", err)
	}
	if was := keysWas(out); strings.Join(was["switcher"], ",") != "ctrl+shift+space,f9" {
		t.Errorf("was: %v", was)
	}
	// Turning it back on drops our comment.
	back := patchKeys(out, []keysEdit{{action: "switcher", value: `["ctrl+shift+space", "f9"]`}})
	if !strings.Contains(back, "switcher = [\"ctrl+shift+space\", \"f9\"]\nquit") || strings.Contains(back, "# was") {
		t.Errorf("re-enabled:\n%s", back)
	}

	// No [keys] table: added at the end; no file: just the table.
	if got := patchKeys("tabs = \"rail\"", []keysEdit{{action: "zoom", value: `"none"`}}); got != "tabs = \"rail\"\n\n[keys]\nzoom = \"none\"\n" {
		t.Errorf("no table: %q", got)
	}
	if got := patchKeys("", []keysEdit{{action: "zoom", value: `"f11"`}}); got != "[keys]\nzoom = \"f11\"\n" {
		t.Errorf("no file: %q", got)
	}
	// An empty table at the end of a file without a final newline.
	if got := patchKeys("[keys]", []keysEdit{{action: "zoom", value: `"f11"`}}); got != "[keys]\nzoom = \"f11\"\n" {
		t.Errorf("empty table: %q", got)
	}
	// Indented keys and CRLF line ends are kept.
	if got := patchKeys("[keys]\r\n  zoom = \"f12\"\r\n", []keysEdit{{action: "zoom", value: `"f11"`}}); got != "[keys]\r\n  zoom = \"f11\"\r\n" {
		t.Errorf("crlf: %q", got)
	}
}

//go:noinline
func allocForTest(n int) []byte { return make([]byte, n) }

var sink []byte

// recordAllocs makes the heap profile record every allocation, for
// mallocsUnder. The next sample point was drawn at the old rate, so
// allocate past it once.
func recordAllocs() {
	runtime.MemProfileRate = 1
	sink = make([]byte, 8<<20)
	sink = nil
}

// mallocsUnder must see allocations made under a named function, and only
// those.
func TestMallocsUnder(t *testing.T) {
	before := mallocsUnder("allocForTest")
	for i := 0; i < 10; i++ {
		sink = allocForTest(1000 + i)
	}
	sink = make([]byte, 2000) // not under allocForTest
	if got := mallocsUnder("allocForTest") - before; got != 10 {
		t.Errorf("counted %d allocations, want 10", got)
	}
}
