// hookstatus.go - whether an agent's hooks are set up on this machine, and
// installing the snippets that are files of their own.
package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// HookState says how far an agent's hooks are set up.
type HookState uint8

// The hook states.
const (
	HooksMissing   HookState = iota // no bunk hook in the agent's configuration
	HooksOutdated                   // bunk hooks, but not every hook the snippet has now
	HooksInstalled                  // every hook of the current snippet
)

// String names the state as listings show it.
func (s HookState) String() string {
	switch s {
	case HooksInstalled:
		return "installed"
	case HooksOutdated:
		return "outdated"
	}
	return "missing"
}

// Hooks is one agent's hook setup on this machine.
type Hooks struct {
	Agent string
	Path  string // the file the snippet goes in, with the home directory expanded
	State HookState
	// Missing lists the snippet's events with a hook the agent lacks, each
	// once.
	Missing []string
	// Installable: InstallHooks sets the hooks up, always now; Merges: it
	// merges them into a settings file that holds other settings, with a
	// backup, rather than writing a file of bunk's own.
	Installable bool
	Merges      bool
	CLI         bool   // the agent's command is on PATH
	Problem     string // why the configuration could not be read; "" when it could
	After       string // what to do once InstallHooks wrote the file, or ""
	// Setting is a setting of the agent's own that its hooks need, as
	// people read it, SettingPath the file that holds it and SettingWhy
	// what it does, after "so"; all "" for an agent that needs none.
	// SettingMissing: the file lacks it, so the hooks are not installed
	// and InstallHooks adds it.
	Setting, SettingPath, SettingWhy string
	SettingMissing                   bool
}

// agentSetting is a setting of the agent's own that its hooks need, in a
// settings file of its directory: Codex's daemon_auto_start = false
// (codexdaemon.go). CheckHooks looks for it and InstallHooks adds it.
type agentSetting struct {
	file  string // the settings file, in the agent's directory
	text  string // the setting as people read it
	why   string // what it does for bunk, after "so" in a sentence
	after string // what to do once InstallHooks added it, or ""
	has   func(data []byte) (bool, error)
	add   func(data []byte) ([]byte, error) // data with the setting
}

// Dirs locates the agents' configuration directories: the home directory
// and the variables that move them (CLAUDE_CONFIG_DIR, CODEX_HOME,
// COPILOT_HOME).
type Dirs struct {
	Home   string
	Getenv func(string) string // nil: no variable is set
}

// UserDirs is Dirs for this user and process.
func UserDirs() Dirs {
	home, err := os.UserHomeDir()
	if err != nil {
		slog.Debug("notify: no home directory for agent hooks", "err", err)
	}
	return Dirs{Home: home, Getenv: os.Getenv}
}

// onPath reports whether the command name is an executable in a
// directory of PATH.
func (d Dirs) onPath(name string) bool {
	path := os.Getenv("PATH")
	if d.Getenv != nil {
		path = d.Getenv("PATH")
	}
	for _, dir := range filepath.SplitList(path) {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return true
		}
	}
	return false
}

func (d Dirs) dir(r agentHooks) string {
	if r.envDir != "" && d.Getenv != nil {
		if v := d.Getenv(r.envDir); v != "" {
			return v
		}
	}
	return filepath.Join(d.Home, r.homeDir)
}

// Short writes path with the home directory as "~", for people.
func (d Dirs) Short(path string) string {
	if d.Home != "" {
		if rest, ok := strings.CutPrefix(path, d.Home+string(filepath.Separator)); ok {
			return "~/" + rest
		}
	}
	return path
}

// CheckHooks returns the hook setup of every agent HookSnippet knows, in
// listing order.
func CheckHooks(d Dirs) []Hooks {
	out := make([]Hooks, 0, len(recipes))
	for _, r := range recipes {
		out = append(out, check(d, r))
	}
	return out
}

// CheckHook returns the hook setup of one agent.
func CheckHook(d Dirs, agent string) (Hooks, bool) {
	r, ok := recipe(agent)
	if !ok {
		return Hooks{}, false
	}
	return check(d, r), true
}

func recipe(agent string) (agentHooks, bool) {
	for _, r := range recipes {
		if r.name == agent {
			return r, true
		}
	}
	return agentHooks{}, false
}

// check compares the hooks the agent has with the current snippet. A hook
// counts when its event, matcher and command are the snippet's; timeouts
// and the agent's other hooks do not matter.
func check(d Dirs, r agentHooks) Hooks {
	dir := d.dir(r)
	h := Hooks{Agent: r.name, Path: filepath.Join(dir, r.file), Installable: true, Merges: !r.own, After: r.after}
	h.CLI = d.onPath(r.name)
	found, err := r.read(dir)
	if err != nil {
		h.Problem = err.Error()
	}
	for _, want := range r.hooks {
		cmd := want.command()
		if !slices.ContainsFunc(found, func(f foundHook) bool {
			return f.event == want.event && f.matcher == want.matcher && f.command == cmd
		}) && !slices.Contains(h.Missing, want.event) {
			h.Missing = append(h.Missing, want.event)
		}
	}
	if s := r.setting; s != nil {
		h.Setting, h.SettingPath, h.SettingWhy = s.text, filepath.Join(dir, s.file), s.why
		has, err := s.lookup(h.SettingPath)
		switch {
		case err == nil:
			h.SettingMissing = !has
		case h.Problem == "":
			h.Problem = err.Error()
		}
	}
	switch {
	case len(h.Missing) == 0 && !h.SettingMissing:
		h.State = HooksInstalled
	case slices.ContainsFunc(found, func(f foundHook) bool { return bunkCommand(f.command) }):
		h.State = HooksOutdated
	}
	return h
}

// bunkHook matches the notify and present commands of bunk's snippets,
// older ones too.
var bunkHook = regexp.MustCompile(`\bbunk(-gui)? (notify|present)\b|"\$b" (notify|present)\b`)

func bunkCommand(cmd string) bool { return bunkHook.MatchString(cmd) }

// foundHook is one command hook in an agent's configuration.
type foundHook struct{ event, matcher, command string }

// settingsHooks is the "hooks" key of Claude Code's and Gemini CLI's
// settings.json and of Codex's hooks.json and config.toml: event →
// matcher groups → handlers.
type settingsHooks struct {
	Hooks map[string][]hookGroup `json:"hooks"`
}

// hookGroup is one matcher group of an event: its handlers.
type hookGroup struct {
	Matcher string `json:"matcher" toml:"matcher"`
	Hooks   []struct {
		Command string `json:"command" toml:"command"`
	} `json:"hooks" toml:"hooks"`
}

func (s settingsHooks) found() []foundHook {
	var out []foundHook
	for ev, groups := range s.Hooks {
		for _, g := range groups {
			for _, h := range g.Hooks {
				out = append(out, foundHook{ev, g.Matcher, h.Command})
			}
		}
	}
	return out
}

// readFile reads path; a missing file is no error.
func readFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// readSettingsHooks reads the hooks of a JSON settings file in the
// agent's directory.
func readSettingsHooks(file string) func(dir string) ([]foundHook, error) {
	return func(dir string) ([]foundHook, error) {
		path := filepath.Join(dir, file)
		data, err := readFile(path)
		if err != nil || data == nil {
			return nil, err
		}
		var s settingsHooks
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return s.found(), nil
	}
}

// readCodexHooks reads Codex's hooks.json and the inline [hooks] tables
// of its config.toml, which Codex merges.
func readCodexHooks(dir string) ([]foundHook, error) {
	out, err := readSettingsHooks("hooks.json")(dir)
	path := filepath.Join(dir, "config.toml")
	data, terr := readFile(path)
	if terr == nil && data != nil {
		var s settingsHooks
		if s, terr = codexTOMLHooks(string(data)); terr == nil {
			out = append(out, s.found()...)
		} else {
			terr = fmt.Errorf("%s: %w", path, terr)
		}
	}
	return out, errors.Join(err, terr)
}

// codexTOMLHooks decodes the event arrays of config.toml's [hooks]. The
// table also holds Codex's own tables, such as the trust hashes of
// [hooks.state], which are no events and are skipped.
func codexTOMLHooks(data string) (settingsHooks, error) {
	var f struct {
		Hooks map[string]toml.Primitive `toml:"hooks"`
	}
	md, err := toml.Decode(data, &f)
	if err != nil {
		return settingsHooks{}, err
	}
	s := settingsHooks{Hooks: map[string][]hookGroup{}}
	for ev, p := range f.Hooks {
		if t := md.Type("hooks", ev); t != "Array" && t != "ArrayHash" {
			continue
		}
		var groups []hookGroup
		if err := md.PrimitiveDecode(p, &groups); err != nil {
			return settingsHooks{}, fmt.Errorf("hooks.%s: %w", ev, err)
		}
		s.Hooks[ev] = groups
	}
	return s, nil
}

// readCopilotHooks reads every hooks file of Copilot CLI: version 1, with
// one flat handler list per event.
func readCopilotHooks(dir string) ([]foundHook, error) {
	files, err := filepath.Glob(filepath.Join(dir, "hooks", "*.json"))
	if err != nil {
		return nil, fmt.Errorf("copilot hooks: %w", err)
	}
	var out []foundHook
	var errs []error
	for _, path := range files {
		data, err := readFile(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var f struct {
			Hooks map[string][]struct {
				Matcher string `json:"matcher"`
				Bash    string `json:"bash"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(data, &f); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		for ev, hs := range f.Hooks {
			for _, h := range hs {
				out = append(out, foundHook{ev, h.Matcher, h.Bash})
			}
		}
	}
	return out, errors.Join(errs...)
}

// lookup reports whether the settings file at path has the setting. A
// missing file lacks it.
func (s *agentSetting) lookup(path string) (bool, error) {
	data, err := readFile(path)
	if err != nil || data == nil {
		return false, err
	}
	has, err := s.has(data)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return has, nil
}

// edit returns the settings file at path, which may be missing, as it is
// and with the setting added; a symbolic link gives its target's path.
func (s *agentSetting) edit(path string) (target string, old, data []byte, err error) {
	if t, err := filepath.EvalSymlinks(path); err == nil {
		path = t
	}
	if old, err = readFile(path); err != nil {
		return path, nil, nil, err
	}
	data, err = s.add(old)
	return path, old, data, err
}

// pending reports whether adding the setting changes the settings file
// at path, or why it cannot be added.
func (s *agentSetting) pending(path string) (bool, error) {
	_, old, data, err := s.edit(path)
	return err == nil && !bytes.Equal(data, old), err
}

// addTo adds the setting to the settings file at path, which may be
// missing, and returns the backup of its old contents, "" when there was
// nothing to keep. The file keeps its mode, and a symbolic link stays:
// its target changes.
func (s *agentSetting) addTo(path string) (backup string, err error) {
	path, old, data, err := s.edit(path)
	if err != nil || bytes.Equal(data, old) {
		return "", err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if old != nil {
		backup = path + BackupSuffix
		if err := writeAtomic(backup, old, mode); err != nil {
			return "", err
		}
	}
	return backup, writeAtomic(path, data, mode)
}

// Installed is what InstallHooks changed: the hooks file, and the copy of
// its old contents it kept, "" when it kept none; then the settings file
// it added the agent's setting to (Hooks.Setting) and the copy of that,
// "" when it changed none.
type Installed struct {
	Path, Backup           string
	Setting, SettingBackup string
}

// InstallNote says what InstallHooks did for agent and what is left to
// do, for people.
func InstallNote(d Dirs, agent string, done Installed) string {
	r, _ := recipe(agent)
	text := "Installed " + d.Short(done.Path) + "."
	if done.Backup != "" {
		text += " The old file is in " + d.Short(done.Backup) + "."
	}
	if r.after != "" {
		text += " " + r.after
	}
	if s := r.setting; s != nil && done.Setting != "" {
		text += fmt.Sprintf(" Set %s in %s, %s.", s.text, d.Short(done.Setting), s.why)
		if done.SettingBackup != "" {
			text += " The old file is in " + d.Short(done.SettingBackup) + "."
		}
		if s.after != "" {
			text += " " + s.after
		}
	}
	return text
}

// InstallHooks sets up an agent's current hooks. A hooks file of bunk's
// own (codex, copilot) is written whole. A settings file that holds other
// settings or hooks (claude, gemini, a Codex hooks.json with hooks of the
// user's) has only bunk's hooks replaced, its other keys kept in their
// order, after a copy of it is saved as BackupSuffix. A Copilot CLI hooks
// file with other hooks is refused, and so is a file that is not JSON.
// Then the setting the hooks need, if the agent lacks it, is added to its
// settings file, which is kept as BackupSuffix first.
func InstallHooks(d Dirs, agent string) (Installed, error) {
	r, ok := recipe(agent)
	if !ok {
		return Installed{}, fmt.Errorf("no hooks for %q", agent)
	}
	// A settings file that cannot take the setting stops the install
	// before the hooks file changes.
	var setting string
	if s := r.setting; s != nil {
		path := filepath.Join(d.dir(r), s.file)
		pending, err := s.pending(path)
		if err != nil {
			return Installed{Path: filepath.Join(d.dir(r), r.file)}, fmt.Errorf("%s stays as it is: %w", d.Short(path), err)
		}
		if pending {
			setting = path
		}
	}
	done, err := installHookFile(d, agent)
	if err != nil || setting == "" {
		return done, err
	}
	if done.SettingBackup, err = r.setting.addTo(setting); err != nil {
		return done, fmt.Errorf("%s stays as it is: %w", d.Short(setting), err)
	}
	done.Setting = setting
	return done, nil
}

// installHookFile writes or merges agent's hooks file for InstallHooks.
func installHookFile(d Dirs, agent string) (Installed, error) {
	r, ok := recipe(agent)
	if !ok {
		return Installed{}, fmt.Errorf("no hooks for %q", agent)
	}
	path := filepath.Join(d.dir(r), r.file)
	done := Installed{Path: path}
	snippet, err := r.render(r.hooks)
	if err != nil {
		return done, err
	}
	if r.own {
		cmds, err := fileCommands(agent, path)
		if err != nil {
			return done, fmt.Errorf("%s stays as it is: %w", d.Short(path), err)
		}
		if !slices.ContainsFunc(cmds, func(c string) bool { return !bunkCommand(c) }) {
			if err := writeAtomic(path, []byte(snippet), 0o644); err != nil {
				return done, fmt.Errorf("install %s hooks: %w", agent, err)
			}
			return done, nil
		}
		if agent == "copilot" {
			return done, fmt.Errorf("%s holds other hooks, so it stays: merge the snippet into it", d.Short(path))
		}
	}
	if done.Backup, err = mergeInto(path, snippet); err != nil {
		return done, fmt.Errorf("%s stays as it is: %w", d.Short(path), err)
	}
	return done, nil
}

// fileCommands returns the hook commands of one hooks file of agent:
// Copilot CLI's flat form, else the settings form. A missing file has none.
func fileCommands(agent, path string) ([]string, error) {
	data, err := readFile(path)
	if err != nil || data == nil {
		return nil, err
	}
	var cmds []string
	if agent == "copilot" {
		var f struct {
			Hooks map[string][]struct {
				Bash string `json:"bash"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("not JSON: %w", err)
		}
		for _, hs := range f.Hooks {
			for _, h := range hs {
				cmds = append(cmds, h.Bash)
			}
		}
		return cmds, nil
	}
	var s settingsHooks
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	for _, f := range s.found() {
		cmds = append(cmds, f.command)
	}
	return cmds, nil
}
