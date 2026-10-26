package main

// Key bindings: the [keys] table of config.toml. Action names and the key
// syntax ("ctrl+shift+j", "alt+f1", "f12") follow bunk's TUI. A value is a
// string or an array of strings; "none" or "" disables the action, so its
// keys reach the terminal.

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
)

// keyCombo is one key with modifiers. key is a lower-case GDK keyval.
type keyCombo struct {
	key  uint
	mods gdk.ModifierType
}

const comboMods = gdk.ControlMask | gdk.ShiftMask | gdk.AltMask | gdk.SuperMask

// keyAction is one bindable action.
type keyAction struct {
	name, desc string
	defs       []string // default bindings
}

// keyActions lists every action in the order the shortcuts window shows
// them. The names shared with bunk's TUI mean the same there. The defaults
// are bunk's TUI keys plus Ptyxis's, with F2 and Ctrl+Left/Right for tabs as
// the user set them in Ptyxis, and Ctrl+Up/Down for tabs, which the user
// asked for to match the rail's vertical tab list (AGENTS.md, Design
// Decisions).
var keyActions = []keyAction{
	{"new_tab", "New tab", []string{"f2"}},
	{"close_tab", "Close tab", []string{"ctrl+shift+w"}},
	{"new_window", "New window", []string{"ctrl+shift+n"}},
	{"next_tab", "Next tab", []string{"ctrl+right", "ctrl+down"}},
	{"prev_tab", "Previous tab", []string{"ctrl+left", "ctrl+up"}},
	{"move_tab_left", "Move the tab left", []string{"ctrl+shift+pgup"}},
	{"move_tab_right", "Move the tab right", []string{"ctrl+shift+pgdn"}},
	{"select_tab_1", "Select tab 1", []string{"alt+1"}},
	{"select_tab_2", "Select tab 2", []string{"alt+2"}},
	{"select_tab_3", "Select tab 3", []string{"alt+3"}},
	{"select_tab_4", "Select tab 4", []string{"alt+4"}},
	{"select_tab_5", "Select tab 5", []string{"alt+5"}},
	{"select_tab_6", "Select tab 6", []string{"alt+6"}},
	{"select_tab_7", "Select tab 7", []string{"alt+7"}},
	{"select_tab_8", "Select tab 8", []string{"alt+8"}},
	{"select_tab_9", "Select tab 9", []string{"alt+9"}},
	{"select_tab_10", "Select tab 10", []string{"alt+0"}},
	{"switcher", "Switch tab", []string{"ctrl+shift+o"}},
	{"next_needs_you", "Next tab needing you", []string{"alt+a"}},
	{"split", "Split pane", []string{"f1"}},
	{"split_context", "Split in the same container", []string{"alt+f1"}},
	{"zoom", "Zoom pane", []string{"f12"}},
	{"nav_up", "Focus the pane above", []string{"alt+up"}},
	{"nav_down", "Focus the pane below", []string{"alt+down"}},
	{"nav_left", "Focus the pane on the left", []string{"alt+left"}},
	{"nav_right", "Focus the pane on the right", []string{"alt+right"}},
	{"scroll_up", "Scroll back", []string{"shift+pgup"}},
	{"scroll_down", "Scroll forward", []string{"shift+pgdn"}},
	{"search", "Find in the pane", []string{"ctrl+f", "ctrl+shift+f"}},
	{"search_next", "Next match (while finding)", []string{"ctrl+n", "enter"}},
	{"search_prev", "Previous match (while finding)", []string{"ctrl+p"}},
	{"search_exit", "Close the find bar", []string{"escape"}},
	{"copy", "Copy", []string{"ctrl+shift+c", "ctrl+c"}},
	{"paste", "Paste", []string{"ctrl+shift+v", "ctrl+v"}},
	{"select_all", "Select all", []string{"ctrl+shift+a"}},
	{"passthrough", "Send all keys to the terminal", []string{"ctrl+f12"}},
	{"toggle_rail", "Expand or collapse the rail", []string{"ctrl+shift+b"}},
	{"toggle_fullscreen", "Fullscreen", []string{"f11"}},
	{"font_bigger", "Larger text", []string{"ctrl+equal", "ctrl+plus", "ctrl+kp_add"}},
	{"font_smaller", "Smaller text", []string{"ctrl+minus", "ctrl+kp_subtract"}},
	{"font_reset", "Reset text size", []string{"ctrl+0", "ctrl+kp_0"}},
	{"menu", "Options menu", []string{"f10"}},
	{"preferences", "Preferences", []string{"ctrl+comma"}},
	{"shortcuts", "Keyboard shortcuts", []string{"ctrl+shift+question", "ctrl+question"}},
	{"quit", "Quit", []string{"ctrl+q", "ctrl+shift+q"}},
}

func findAction(name string) (keyAction, bool) {
	for _, a := range keyActions {
		if a.name == name {
			return a, true
		}
	}
	return keyAction{}, false
}

// keyNameList maps key names to keyvals; the first name of a keyval is the
// one bindings are written with.
var keyNameList = []struct {
	name string
	key  uint
}{
	{"up", gdk.KEY_Up}, {"down", gdk.KEY_Down}, {"left", gdk.KEY_Left}, {"right", gdk.KEY_Right},
	{"pgup", gdk.KEY_Page_Up}, {"pageup", gdk.KEY_Page_Up}, {"page_up", gdk.KEY_Page_Up},
	{"pgdn", gdk.KEY_Page_Down}, {"pagedown", gdk.KEY_Page_Down}, {"page_down", gdk.KEY_Page_Down},
	{"home", gdk.KEY_Home}, {"end", gdk.KEY_End},
	{"enter", gdk.KEY_Return}, {"return", gdk.KEY_Return},
	{"escape", gdk.KEY_Escape}, {"esc", gdk.KEY_Escape},
	{"backspace", gdk.KEY_BackSpace}, {"delete", gdk.KEY_Delete}, {"del", gdk.KEY_Delete},
	{"tab", gdk.KEY_Tab}, {"insert", gdk.KEY_Insert}, {"space", gdk.KEY_space},
	{"plus", gdk.KEY_plus}, {"minus", gdk.KEY_minus}, {"equal", gdk.KEY_equal},
	{"comma", gdk.KEY_comma}, {"period", gdk.KEY_period}, {"slash", gdk.KEY_slash},
	{"backslash", gdk.KEY_backslash}, {"question", gdk.KEY_question}, {"semicolon", gdk.KEY_semicolon},
	{"apostrophe", gdk.KEY_apostrophe}, {"grave", gdk.KEY_grave},
	{"bracketleft", gdk.KEY_bracketleft}, {"bracketright", gdk.KEY_bracketright},
	{"underscore", gdk.KEY_underscore},
	{"kp_add", gdk.KEY_KP_Add}, {"kp_subtract", gdk.KEY_KP_Subtract},
	{"kp_multiply", gdk.KEY_KP_Multiply}, {"kp_divide", gdk.KEY_KP_Divide}, {"kp_enter", gdk.KEY_KP_Enter},
	{"kp_0", gdk.KEY_KP_0}, {"kp_1", gdk.KEY_KP_1}, {"kp_2", gdk.KEY_KP_2}, {"kp_3", gdk.KEY_KP_3},
	{"kp_4", gdk.KEY_KP_4}, {"kp_5", gdk.KEY_KP_5}, {"kp_6", gdk.KEY_KP_6}, {"kp_7", gdk.KEY_KP_7},
	{"kp_8", gdk.KEY_KP_8}, {"kp_9", gdk.KEY_KP_9},
}

var keyByName, nameByKey = func() (map[string]uint, map[uint]string) {
	byName, byKey := map[string]uint{}, map[uint]string{}
	for _, n := range keyNameList {
		byName[n.name] = n.key
		if _, ok := byKey[n.key]; !ok {
			byKey[n.key] = n.name
		}
	}
	for i := 1; i <= 24; i++ {
		k := uint(gdk.KEY_F1 + i - 1)
		byName[fmt.Sprintf("f%d", i)] = k
		byKey[k] = fmt.Sprintf("f%d", i)
	}
	return byName, byKey
}()

// parseCombo parses "ctrl+shift+j", "alt+f1", "f12", "ctrl+=" or "ctrl++".
func parseCombo(s string) (keyCombo, error) {
	orig := s
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.HasSuffix(s, "++") { // "ctrl++"
		s = strings.TrimSuffix(s, "++") + "+plus"
	}
	if s == "+" {
		s = "plus"
	}
	parts := strings.Split(s, "+")
	var c keyCombo
	for _, m := range parts[:len(parts)-1] {
		switch m {
		case "ctrl", "control":
			c.mods |= gdk.ControlMask
		case "shift":
			c.mods |= gdk.ShiftMask
		case "alt":
			c.mods |= gdk.AltMask
		case "super":
			c.mods |= gdk.SuperMask
		default:
			return keyCombo{}, fmt.Errorf("unknown modifier %q in %q", m, orig)
		}
	}
	name := parts[len(parts)-1]
	if k, ok := keyByName[name]; ok {
		c.key = k
		return c, nil
	}
	if r := []rune(name); len(r) == 1 && unicode.IsPrint(r[0]) && r[0] != ' ' {
		c.key = gdk.KeyvalToLower(gdk.UnicodeToKeyval(uint32(r[0])))
		return c, nil
	}
	return keyCombo{}, fmt.Errorf("unknown key %q in %q", name, orig)
}

// String writes the combo in config syntax, e.g. "ctrl+shift+j".
func (c keyCombo) String() string {
	var b strings.Builder
	for _, m := range []struct {
		mask gdk.ModifierType
		name string
	}{{gdk.ControlMask, "ctrl"}, {gdk.AltMask, "alt"}, {gdk.ShiftMask, "shift"}, {gdk.SuperMask, "super"}} {
		if c.mods&m.mask != 0 {
			b.WriteString(m.name + "+")
		}
	}
	if n, ok := nameByKey[c.key]; ok {
		b.WriteString(n)
	} else if r := gdk.KeyvalToUnicode(c.key); r != 0 {
		b.WriteRune(rune(r))
	} else {
		b.WriteString(strings.ToLower(gdk.KeyvalName(c.key)))
	}
	return b.String()
}

// Label is the combo as menus show it, e.g. "Ctrl+Shift+J".
func (c keyCombo) Label() string {
	parts := strings.Split(c.String(), "+")
	for i, p := range parts {
		switch {
		case p == "":
			parts[i] = "+"
		case p == "pgup":
			parts[i] = "PgUp"
		case p == "pgdn":
			parts[i] = "PgDn"
		case strings.HasPrefix(p, "kp_"):
			parts[i] = "KP " + strings.ToUpper(p[3:4]) + p[4:]
		case p == "equal":
			parts[i] = "="
		case p == "minus":
			parts[i] = "-"
		case p == "comma":
			parts[i] = ","
		case p == "question":
			parts[i] = "?"
		case p == "plus":
			parts[i] = "+"
		default:
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "+")
}

// collides reports whether a terminal application may want the combo:
// Ctrl or Alt with a letter, digit or punctuation and no Shift; Ctrl+Space;
// plain F-keys; Alt or Ctrl with an arrow.
func (c keyCombo) collides() bool {
	ctrl, alt, shift := c.mods&gdk.ControlMask != 0, c.mods&gdk.AltMask != 0, c.mods&gdk.ShiftMask != 0
	r := gdk.KeyvalToUnicode(c.key)
	switch {
	case c.key == gdk.KEY_space:
		return ctrl && !shift
	case (ctrl || alt) && !shift && r != 0 && unicode.IsPrint(rune(r)):
		return true
	case c.mods == 0 && c.key >= gdk.KEY_F1 && c.key <= gdk.KEY_F24:
		return true
	case (ctrl || alt) && (c.key == gdk.KEY_Up || c.key == gdk.KEY_Down || c.key == gdk.KEY_Left || c.key == gdk.KEY_Right):
		return true
	}
	return false
}

// keymap is the resolved [keys] table.
type keymap struct {
	bind    map[string][]keyCombo // action → combos; empty: disabled
	action  map[keyCombo]string
	written map[string]bool // actions the config file sets
}

func (m keymap) labels(action string) string {
	var ls []string
	for _, c := range m.bind[action] {
		ls = append(ls, c.Label())
	}
	return strings.Join(ls, " / ")
}

// keyValues turns a [keys] value (a string or an array of strings) into
// binding strings; "none" and "" give none.
func keyValues(v any) ([]string, error) {
	var vals []string
	switch v := v.(type) {
	case string:
		vals = []string{v}
	case []any:
		for _, x := range v {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("want strings, got %v", x)
			}
			vals = append(vals, s)
		}
	case []string:
		vals = v
	default:
		return nil, fmt.Errorf("want a string or an array of strings, got %v", v)
	}
	var out []string
	for _, s := range vals {
		if s = strings.TrimSpace(s); s != "" && !strings.EqualFold(s, "none") {
			out = append(out, s)
		}
	}
	return out, nil
}

// resolveKeys applies the config's [keys] table to the defaults. It
// returns the problems found: unknown actions, bad keys, and keys bound to
// two actions. A key the config binds wins over a default of another
// action; between two configured actions the earlier one in keyActions
// wins.
func resolveKeys(table map[string]any) (keymap, []string) {
	m := keymap{bind: map[string][]keyCombo{}, action: map[keyCombo]string{}, written: map[string]bool{}}
	var problems []string
	var unknown []string
	for name := range table {
		if _, ok := findAction(name); !ok {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		problems = append(problems, "unknown key actions: "+strings.Join(unknown, ", "))
	}
	// Configured actions claim their keys first, then the defaults.
	for _, pass := range []bool{true, false} {
		for _, act := range keyActions {
			v, set := table[act.name]
			if set != pass {
				continue
			}
			vals := act.defs
			if set {
				m.written[act.name] = true
				var err error
				if vals, err = keyValues(v); err != nil {
					problems = append(problems, fmt.Sprintf("keys.%s: %v", act.name, err))
					vals = act.defs
				}
			}
			m.bind[act.name] = []keyCombo{}
			for _, s := range vals {
				c, err := parseCombo(s)
				if err != nil {
					problems = append(problems, fmt.Sprintf("keys.%s: %v", act.name, err))
					continue
				}
				if other, taken := m.action[c]; taken {
					if other != act.name && (set || m.written[other]) {
						problems = append(problems, fmt.Sprintf("%s is bound to both %s and %s", c, other, act.name))
					}
					continue
				}
				m.action[c] = act.name
				m.bind[act.name] = append(m.bind[act.name], c)
			}
		}
	}
	return m, problems
}

// match finds the action for a key press and the binding it matched.
// unshift returns the keyval the key's keycode gives without Shift (0 if
// unknown), for bindings written with Shift and the unshifted key
// ("ctrl+shift+slash").
func (m keymap) match(keyval uint, state gdk.ModifierType, unshift func() uint) (string, keyCombo, bool) {
	mods := state & comboMods
	key := gdk.KeyvalToLower(keyval)
	cands := []keyCombo{{key, mods}}
	if mods&gdk.ShiftMask != 0 {
		// Shift chose a symbol ("?" from "/"): "ctrl+?" matches too.
		if gdk.KeyvalToUpper(key) == key {
			cands = append(cands, keyCombo{key, mods &^ gdk.ShiftMask})
		}
		if k := unshift(); k != 0 {
			cands = append(cands, keyCombo{gdk.KeyvalToLower(k), mods})
		}
	}
	for _, c := range cands {
		if a, ok := m.action[c]; ok {
			return a, c, true
		}
	}
	return "", keyCombo{}, false
}

// withBinding returns a copy of m with action bound to combos.
func (m keymap) withBinding(action string, combos []keyCombo) keymap {
	n := keymap{bind: map[string][]keyCombo{}, action: map[keyCombo]string{}, written: map[string]bool{}}
	for k, v := range m.written {
		n.written[k] = v
	}
	n.written[action] = true
	for a, cs := range m.bind {
		if a == action {
			cs = combos
		}
		n.bind[a] = slices.Clone(cs)
		for _, c := range cs {
			n.action[c] = a
		}
	}
	if _, ok := m.bind[action]; !ok {
		n.bind[action] = slices.Clone(combos)
		for _, c := range combos {
			n.action[c] = action
		}
	}
	return n
}

// tomlValue writes combos as a [keys] value.
func tomlValue(combos []keyCombo) string {
	switch len(combos) {
	case 0:
		return `"none"`
	case 1:
		return fmt.Sprintf("%q", combos[0].String())
	}
	var qs []string
	for _, c := range combos {
		qs = append(qs, fmt.Sprintf("%q", c.String()))
	}
	return "[" + strings.Join(qs, ", ") + "]"
}
