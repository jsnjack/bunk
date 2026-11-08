// skill.go - the bunk-present skill: when and how agents present documents
// (skill/SKILL.md), whether each agent has it, and installing it.
package notify

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

// skillText is the skill as agents load it.
//
//go:embed skill/SKILL.md
var skillText string

// SkillName is the skill's directory in each agent's skills directory.
const SkillName = "bunk-present"

// skillAgents are the agents that load skills from a directory of their
// configuration, in listing order: Claude Code, Codex and Copilot CLI.
var skillAgents = []string{"claude", "codex", "copilot"}

// SkillState is one agent's copy of the bunk-present skill on this
// machine. State uses the hook states: installed is the current skill,
// outdated another text, missing no file.
type SkillState struct {
	Agent   string
	Path    string // SKILL.md in the agent's skills directory
	State   HookState
	CLI     bool   // the agent's command is on PATH
	Problem string // why the file could not be read; "" when it could
}

// skillPath returns where agent loads the skill from: SKILL.md under
// skills/bunk-present in its configuration directory, which
// CLAUDE_CONFIG_DIR, CODEX_HOME or COPILOT_HOME move.
func skillPath(d Dirs, agent string) (string, bool) {
	r, ok := recipe(agent)
	if !ok || !slices.Contains(skillAgents, agent) {
		return "", false
	}
	return filepath.Join(d.dir(r), "skills", SkillName, "SKILL.md"), true
}

// CheckSkills returns each agent's copy of the skill, in listing order.
func CheckSkills(d Dirs) []SkillState {
	out := make([]SkillState, 0, len(skillAgents))
	for _, agent := range skillAgents {
		path, _ := skillPath(d, agent)
		st := SkillState{Agent: agent, Path: path, CLI: d.onPath(agent)}
		data, err := readFile(path)
		switch {
		case err != nil:
			st.Problem = err.Error()
		case data == nil:
		case string(data) == skillText:
			st.State = HooksInstalled
		default:
			st.State = HooksOutdated
		}
		out = append(out, st)
	}
	return out
}

// InstallSkill writes the current skill for agent and returns the path it
// wrote. Its directories are created with mode 0755 and the file gets
// 0644; a SKILL.md that is a symbolic link stays one, and its target gets
// the skill.
func InstallSkill(d Dirs, agent string) (string, error) {
	path, ok := skillPath(d, agent)
	if !ok {
		return "", fmt.Errorf("no skill for %q", agent)
	}
	target := path
	if t, err := filepath.EvalSymlinks(path); err == nil {
		target = t
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("install the %s skill: %w", agent, err)
	}
	if err := writeAtomic(target, []byte(skillText), 0o644); err != nil {
		return "", fmt.Errorf("install the %s skill: %w", agent, err)
	}
	return path, nil
}

// skillStatus lists each agent's copy of the skill with its state, and the
// step that installs it when it is not.
func skillStatus(prog string, d Dirs) string {
	var b strings.Builder
	for _, s := range CheckSkills(d) {
		state := s.State.String()
		line := fmt.Sprintf("%-8s %-10s %s", s.Agent, state, d.Short(s.Path))
		if !s.CLI {
			line += fmt.Sprintf(" (%s is not on PATH)", s.Agent)
		}
		b.WriteString(line + "\n")
		if s.Problem != "" {
			fmt.Fprintf(&b, "%-19s %s\n", "", s.Problem)
		}
		if s.State != HooksInstalled {
			fmt.Fprintf(&b, "%-19s run %s present install %s\n", "", prog, s.Agent)
		}
	}
	fmt.Fprintf(&b, "\n%s present skill prints the skill; docs/present.md describes it.\n", prog)
	return b.String()
}

// installSkill runs "install <agent>".
func installSkill(prog, agent string, d Dirs, stdout, stderr io.Writer) int {
	if _, ok := skillPath(d, agent); !ok {
		write(stderr, fmt.Sprintf("%s present: install takes one of: %s\n", prog, strings.Join(skillAgents, ", ")))
		return exitError
	}
	path, err := InstallSkill(d, agent)
	if err != nil {
		write(stderr, fmt.Sprintf("%s present: %v\n", prog, err))
		return exitError
	}
	write(stdout, "Installed "+d.Short(path)+".\n")
	return 0
}
