// Package wire is the format of agent status reports (docs/agent-hooks.md):
// one JSON line, sent over the notify socket (internal/notify) or written
// into a terminal as an OSC 1337 user variable and read back from session
// output (internal/session, docs/remote-agent-status.md).
//
// It knows events only by their wire names, so it depends on neither
// package and both can import it.
package wire

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// Version is the "v" field of every report.
	Version = 1
	// MaxLine caps one report, newline included, in bytes.
	MaxLine = 4096
	// MaxText caps a report's message, in bytes.
	MaxText = 1024
	// MaxField caps each of a report's agent, host, repo, branch and cwd,
	// in bytes. With MaxText, escaped as JSON, a report stays under MaxLine.
	MaxField = 128
	// MaxToken caps the session token, in bytes.
	MaxToken = 64
	// MaxAgentSession caps the agent's own session ID, in bytes.
	MaxAgentSession = 128
	// UserVar is the name of the OSC 1337 user variable that carries a
	// report through a terminal.
	UserVar = "bunk_notify"
)

// userVarPrefix starts the payload of an in-band report, after "1337;".
const userVarPrefix = "SetUserVar=" + UserVar + "="

// Report is one report as it travels. Session is the token from
// BUNK_NOTIFY_SESSION; an in-band report may leave it empty. Agent, Host,
// Repo, Branch and Cwd say where an agent on another machine runs.
// AgentSession is the agent's own session ID, which resumes its
// conversation (claude --resume ID); only ValidAgentSession IDs travel.
type Report struct {
	V       int    `json:"v"`
	Session string `json:"session,omitempty"`
	Event   string `json:"event"`
	Message string `json:"message,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Host    string `json:"host,omitempty"`
	Repo    string `json:"repo,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Cwd     string `json:"cwd,omitempty"`

	AgentSession string `json:"agent_session,omitempty"`
	// Held marks a needs-you whose end the agent reports itself, as its
	// question tools' PostToolUse hooks do: typing does not end it.
	Held bool `json:"held,omitempty"`
}

var (
	errTooLong = errors.New("report longer than MaxLine")
	errVersion = errors.New("unsupported protocol version")
	errEvent   = errors.New("missing event")
	errToken   = errors.New("oversized session token")
)

// Encode returns r as one JSON line ending in a newline, with Version set.
// It cleans the message (CleanText) and the other text fields (cleanField)
// first. It does not check the event name.
func (r Report) Encode() ([]byte, error) {
	if len(r.Session) > MaxToken {
		return nil, fmt.Errorf("encode: %w", errToken)
	}
	if r.Event == "" {
		return nil, fmt.Errorf("encode: %w", errEvent)
	}
	r.V = Version
	r.Message = CleanText(r.Message)
	r.Agent, r.Host, r.Repo = cleanField(r.Agent), cleanField(r.Host), cleanField(r.Repo)
	r.Branch, r.Cwd = cleanField(r.Branch), cleanField(r.Cwd)
	if !ValidAgentSession(r.AgentSession) {
		r.AgentSession = ""
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // "<" stays one byte, so the caps fit MaxLine
	if err := enc.Encode(r); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	if buf.Len() > MaxLine {
		return nil, fmt.Errorf("encode: %w", errTooLong)
	}
	return buf.Bytes(), nil
}

// Decode parses one line, with or without its newline. It rejects lines
// over MaxLine, other versions, a missing event and an oversized token. An
// empty token passes; callers that need one check it. Text fields are valid
// UTF-8 but not otherwise cleaned.
func Decode(line []byte) (Report, error) {
	line = bytes.TrimSuffix(line, []byte("\n"))
	if len(line) >= MaxLine {
		return Report{}, fmt.Errorf("decode: %w", errTooLong)
	}
	var r Report
	if err := json.Unmarshal(line, &r); err != nil {
		return Report{}, fmt.Errorf("decode: %w", err)
	}
	if r.V != Version {
		return Report{}, fmt.Errorf("decode: %w %d", errVersion, r.V)
	}
	if r.Event == "" {
		return Report{}, fmt.Errorf("decode: %w", errEvent)
	}
	if len(r.Session) > MaxToken {
		return Report{}, fmt.Errorf("decode: %w", errToken)
	}
	return r, nil
}

// ValidAgentSession reports whether id may serve as an agent's session ID:
// 1 to MaxAgentSession letters, digits, '.', '_', ':' and '-', starting
// with a letter or digit. A restored pane types the ID into a shell as an
// argument, so nothing else passes, and it cannot pass for an option.
func ValidAgentSession(id string) bool {
	if id == "" || len(id) > MaxAgentSession {
		return false
	}
	for i := range len(id) {
		switch c := id[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == ':' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// CleanText makes text safe to send: invalid UTF-8 and control characters
// become replacement characters and spaces, and the result is trimmed and
// cut to at most MaxText bytes on a character boundary.
func CleanText(text string) string {
	return clean(text, MaxText)
}

// cleanField is CleanText for the agent, host, repo, branch and cwd.
func cleanField(text string) string {
	return clean(text, MaxField)
}

func clean(text string, limit int) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(text, "�"))
	text = strings.TrimSpace(text)
	if len(text) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = strings.TrimSpace(text[:cut])
	}
	return text
}

// InBand returns an encoded report line as the escape sequence that
// carries it through a terminal: OSC 1337 SetUserVar=bunk_notify= with the
// line, without its newline, in base64, ended by BEL.
func InBand(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte("\n"))
	seq := make([]byte, 0, 8+len(userVarPrefix)+base64.StdEncoding.EncodedLen(len(line)))
	seq = append(seq, "\x1b]1337;"+userVarPrefix...)
	seq = base64.StdEncoding.AppendEncode(seq, line)
	return append(seq, '\a')
}

// FromUserVar returns the report line that an OSC 1337 payload (the text
// after "1337;", without the terminator) carries, or false when the
// payload is not bunk's user variable or not valid base64.
func FromUserVar(payload []byte) ([]byte, bool) {
	value, ok := bytes.CutPrefix(payload, []byte(userVarPrefix))
	if !ok || len(value) > base64.StdEncoding.EncodedLen(MaxLine) {
		return nil, false
	}
	line, err := base64.StdEncoding.AppendDecode(nil, value)
	if err != nil {
		return nil, false
	}
	return line, true
}

// Wrap prepares seq for the terminal multiplexer that getenv reveals, so the
// multiplexer passes it on to the terminal it runs in: inside tmux ($TMUX)
// a DCS tmux; passthrough with every ESC doubled (tmux needs
// allow-passthrough), inside GNU screen ($STY) a plain DCS. Elsewhere seq
// is returned as it is.
func Wrap(seq []byte, getenv func(string) string) []byte {
	switch {
	case getenv("TMUX") != "":
		out := append([]byte("\x1bPtmux;"), bytes.ReplaceAll(seq, []byte("\x1b"), []byte("\x1b\x1b"))...)
		return append(out, "\x1b\\"...)
	case getenv("STY") != "":
		out := append([]byte("\x1bP"), seq...)
		return append(out, "\x1b\\"...)
	}
	return seq
}
