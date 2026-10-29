package restore

import (
	"strings"

	"bunk/internal/notify/wire"
)

// resumers are the agents whose conversation a command resumes: by the
// session ID their hooks report, or, without one, the latest in the
// directory.
var resumers = map[string]struct{ byID, latest string }{
	"claude": {"claude --resume ", "claude --continue"},
	"codex":  {"codex resume ", "codex resume --last"},
	// --resume takes an optional value, so the ID goes after '='.
	"copilot": {"copilot --resume=", "copilot --continue"},
	"gemini":  {"gemini --resume ", "gemini --resume latest"},
}

// ResumeCommands returns, for each pane of w in Tabs and Panes order, the
// shell command that resumes its agent's conversation, or "".
//
// A pane with a session ID resumes it. A pane without one, as when the
// agent's hooks are not installed, resumes the latest conversation in its
// directory, unless another pane of ws ran the same agent there and the
// latest may be the other's. A remote agent (Host) is not resumed here,
// nor an agent bunk knows no command for.
func ResumeCommands(w Window, ws []Window) [][]string {
	type place struct{ agent, cwd string }
	here := map[place]int{}
	for _, other := range ws {
		for _, t := range other.Tabs {
			for _, p := range t.Root.Panes() {
				if Resumable(p.Agent) && p.Host == "" {
					here[place{p.Agent, p.Cwd}]++
				}
			}
		}
	}
	out := make([][]string, len(w.Tabs))
	for i, t := range w.Tabs {
		for _, p := range t.Root.Panes() {
			out[i] = append(out[i], resumeCommand(p, here[place{p.Agent, p.Cwd}] > 1))
		}
	}
	return out
}

func resumeCommand(p *Pane, ambiguous bool) string {
	r, ok := resumers[p.Agent]
	switch {
	case !ok || p.Host != "":
		return ""
	case wire.ValidAgentSession(p.AgentSession):
		return r.byID + p.AgentSession
	case ambiguous:
		return ""
	}
	return r.latest
}

// Resumable reports whether bunk knows how to resume agent.
func Resumable(agent string) bool {
	_, ok := resumers[agent]
	return ok
}

// ShellQuote joins argv for a POSIX shell, quoting each word that needs
// it. It returns "" when a word holds a control character: typed into a
// shell, a newline would run the line.
func ShellQuote(argv []string) string {
	words := make([]string, len(argv))
	for i, a := range argv {
		if strings.ContainsFunc(a, func(r rune) bool { return r < ' ' || r == 0x7f }) {
			return ""
		}
		if a != "" && !strings.ContainsFunc(a, func(r rune) bool { return !plainRune(r) }) {
			words[i] = a
			continue
		}
		words[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(words, " ")
}

// plainRune reports whether r needs no quoting in a shell word.
func plainRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%+=:,./-_", r)
}
