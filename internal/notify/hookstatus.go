// hookstatus.go - whether an agent's hooks are set up on this machine, and
// installing the snippets that are files of their own.
package notify

import (
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
	// Missing lists the snippet's events whose hook the agent lacks.
	Missing []string
	// Installable: the snippet is a file of its own, which InstallHooks
	// writes. Otherwise it is merged into a settings file by hand.
	Installable bool
	CLI         bool   // the agent's command is on PATH
	Problem     string // why the configuration could not be read; "" when it could
	After       string // what to do once InstallHooks wrote the file, or ""
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
	h := Hooks{Agent: r.name, Path: filepath.Join(dir, r.file), Installable: r.own, After: r.after}
	h.CLI = d.onPath(r.name)
	found, err := r.read(dir)
	if err != nil {
		h.Problem = err.Error()
	}
	for _, want := range r.hooks {
		cmd := shellCommand(want.args)
		if !slices.ContainsFunc(found, func(f foundHook) bool {
			return f.event == want.event && f.matcher == want.matcher && f.command == cmd
		}) {
			h.Missing = append(h.Missing, want.event)
		}
	}
	switch {
	case len(h.Missing) == 0:
		h.State = HooksInstalled
	case slices.ContainsFunc(found, func(f foundHook) bool { return bunkCommand(f.command) }):
		h.State = HooksOutdated
	}
	return h
}

// bunkHook matches the notify commands of bunk's snippets, older ones too.
var bunkHook = regexp.MustCompile(`\bbunk(-gui)? notify\b|"\$b" notify\b`)

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

// InstallHooks writes the snippet of an agent whose hooks are a file of
// their own (codex, copilot) and returns the file. It refuses a file that
// holds hooks besides bunk's, which the snippet would replace, and an
// agent whose hooks go into a settings file with others.
func InstallHooks(d Dirs, agent string) (string, error) {
	r, ok := recipe(agent)
	if !ok {
		return "", fmt.Errorf("no hooks for %q", agent)
	}
	path := filepath.Join(d.dir(r), r.file)
	if !r.own {
		return path, fmt.Errorf("%s's hooks go into %s with its other settings: merge the snippet into it", agent, d.Short(path))
	}
	snippet, err := r.render(r.hooks)
	if err != nil {
		return path, err
	}
	cmds, err := fileCommands(agent, path)
	if err != nil {
		return path, fmt.Errorf("%s stays as it is: %w", d.Short(path), err)
	}
	for _, c := range cmds {
		if !bunkCommand(c) {
			return path, fmt.Errorf("%s holds other hooks, so it stays: merge the snippet into it", d.Short(path))
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, fmt.Errorf("install %s hooks: %w", agent, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bunk-hooks-*")
	if err != nil {
		return path, fmt.Errorf("install %s hooks: %w", agent, err)
	}
	_, werr := tmp.WriteString(snippet)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp.Name(), 0o644)); err != nil {
		_ = os.Remove(tmp.Name())
		return path, fmt.Errorf("install %s hooks: %w", agent, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return path, fmt.Errorf("install %s hooks: %w", agent, err)
	}
	return path, nil
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
