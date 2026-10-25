package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Command is how hook snippets run the notify CLI.
const Command = "bunk-gui notify"

// hookTimeout is the timeout snippets give each hook, in seconds. The CLI
// gives up after clientTimeout plus stdinTimeout.
const hookTimeout = 5

// hook maps one of an agent's hook events to a notify command.
type hook struct {
	event   string // the agent's event name
	matcher string // "" matches every occurrence
	args    string // notify arguments, such as "needs-you -"
}

// agentHooks is one agent's recipe for "notify hooks <agent>".
type agentHooks struct {
	name   string
	dest   string // where the snippet goes
	hooks  []hook
	render func([]hook) (string, error)
}

// recipes are the agents "notify hooks" knows, in listing order.
var recipes = []agentHooks{
	{
		name: "claude", dest: "~/.claude/settings.json (merge the \"hooks\" key)",
		hooks: []hook{
			{event: "UserPromptSubmit", args: "working"},
			{event: "Notification", matcher: "permission_prompt|elicitation_dialog|elicitation_url_dialog|agent_needs_input", args: "needs-you -"},
			{event: "Stop", args: "done -"},
			{event: "StopFailure", args: "failed -"},
			{event: "SessionEnd", args: "clear"},
		},
		render: renderClaude,
	},
	{
		name: "codex", dest: "~/.codex/config.toml (then trust the hooks with /hooks)",
		hooks: []hook{
			{event: "UserPromptSubmit", args: "working"},
			{event: "PermissionRequest", args: "needs-you Approval requested"},
			{event: "Stop", args: "done -"},
			{event: "Interrupt", args: "idle"},
			{event: "SessionEnd", args: "clear"},
		},
		render: renderCodex,
	},
	{
		name: "copilot", dest: "~/.copilot/hooks/bunk-gui.json",
		hooks: []hook{
			{event: "userPromptSubmitted", args: "working"},
			{event: "notification", matcher: "permission_prompt|elicitation_dialog", args: "needs-you -"},
			{event: "agentStop", args: "done"},
			{event: "sessionEnd", args: "clear"},
		},
		render: renderCopilot,
	},
	{
		name: "gemini", dest: "~/.gemini/settings.json (merge the \"hooks\" key)",
		hooks: []hook{
			{event: "BeforeAgent", args: "working"},
			{event: "Notification", args: "needs-you -"},
			{event: "AfterAgent", args: "done -"},
			{event: "SessionEnd", args: "clear"},
		},
		render: renderGemini,
	},
}

// HookSnippet returns the hook configuration for agent ("claude", "codex",
// "copilot" or "gemini"), ready to merge into the file HookAgents names.
func HookSnippet(agent string) (string, bool) {
	for _, r := range recipes {
		if r.name == agent {
			s, err := r.render(r.hooks)
			if err != nil {
				return "", false
			}
			return s, true
		}
	}
	return "", false
}

// HookAgents lists the agents HookSnippet knows, one "name  destination"
// line each.
func HookAgents() string {
	var b strings.Builder
	for _, r := range recipes {
		fmt.Fprintf(&b, "%-8s %s\n", r.name, r.dest)
	}
	return b.String()
}

// shellCommand runs the notify CLI only inside bunk-gui, so the hook
// succeeds in other terminals even where bunk-gui is not installed.
func shellCommand(args string) string {
	return `[ -z "$` + EnvSession + `" ] || ` + Command + " " + args
}

// field is one key of an ordered JSON object.
type field struct {
	key   string
	value any
}

// object is a JSON object that keeps its key order.
type object []field

// MarshalJSON writes the fields in order.
func (o object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(f.key)
		if err != nil {
			return nil, fmt.Errorf("marshal key: %w", err)
		}
		v, err := json.Marshal(f.value)
		if err != nil {
			return nil, fmt.Errorf("marshal %s: %w", f.key, err)
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func indentJSON(v any) (string, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render hooks: %w", err)
	}
	return string(out) + "\n", nil
}

// renderClaude writes Claude Code's settings.json form:
// event → matcher groups → command handlers.
func renderClaude(hs []hook) (string, error) {
	events := object{}
	for _, h := range hs {
		group := object{}
		if h.matcher != "" {
			group = append(group, field{"matcher", h.matcher})
		}
		handler := object{{"type", "command"}, {"command", shellCommand(h.args)}, {"timeout", hookTimeout}}
		group = append(group, field{"hooks", []any{handler}})
		events = append(events, field{h.event, []any{group}})
	}
	return indentJSON(object{{"hooks", events}})
}

// renderGemini writes Gemini CLI's settings.json form, with named handlers
// and timeouts in milliseconds.
func renderGemini(hs []hook) (string, error) {
	events := object{}
	for _, h := range hs {
		name := "bunk-gui-" + strings.Fields(h.args)[0]
		handler := object{{"name", name}, {"type", "command"}, {"command", shellCommand(h.args)}, {"timeout", hookTimeout * 1000}}
		group := object{}
		if h.matcher != "" {
			group = append(group, field{"matcher", h.matcher})
		}
		group = append(group, field{"hooks", []any{handler}})
		events = append(events, field{h.event, []any{group}})
	}
	return indentJSON(object{{"hooks", events}})
}

// renderCopilot writes a Copilot CLI hooks file: version 1, with one flat
// handler list per event.
func renderCopilot(hs []hook) (string, error) {
	events := object{}
	for _, h := range hs {
		handler := object{{"type", "command"}}
		if h.matcher != "" {
			handler = append(handler, field{"matcher", h.matcher})
		}
		handler = append(handler, field{"bash", shellCommand(h.args)}, field{"timeoutSec", hookTimeout})
		events = append(events, field{h.event, []any{handler}})
	}
	return indentJSON(object{{"version", 1}, {"hooks", events}})
}

// renderCodex writes inline [hooks] tables for Codex's config.toml, with
// the notify program as a commented alternative.
func renderCodex(hs []hook) (string, error) {
	var b strings.Builder
	b.WriteString("# bunk-gui agent status. Run /hooks in Codex once to trust these hooks.\n")
	b.WriteString("# Codex without hooks: use the notify program instead, as a top-level\n")
	b.WriteString("# key above the first [table]:\n")
	b.WriteString(`# notify = ["bunk-gui", "notify", "done"]` + "\n")
	for _, h := range hs {
		cmd := shellCommand(h.args)
		if strings.Contains(cmd, "'") {
			return "", fmt.Errorf("render hooks: quote in %q", cmd)
		}
		fmt.Fprintf(&b, "\n[[hooks.%s]]\n", h.event)
		if h.matcher != "" {
			fmt.Fprintf(&b, "matcher = %q\n", h.matcher)
		}
		fmt.Fprintf(&b, "[[hooks.%s.hooks]]\ntype = \"command\"\ncommand = '%s'\ntimeout = %d\n", h.event, cmd, hookTimeout)
	}
	return b.String(), nil
}
