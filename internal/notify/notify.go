// Package notify lets agent CLIs (Claude Code, Codex, Copilot CLI, Gemini
// CLI, …) report their state to a front end exactly, instead of the front
// end inferring it from output, bells and OSC notifications.
//
// The front end listens on a Unix socket under $XDG_RUNTIME_DIR (Listen) and
// starts every session with Options.NotifySocket and Options.NotifySession,
// which export BUNK_GUI_SOCKET and BUNK_GUI_SESSION, a random per-session
// token. An agent hook runs "bunk-gui notify <event> [message]" (RunCLI),
// which sends one JSON line per message:
//
//	{"v":1,"session":"<token>","event":"needs-you","message":"Allow npm install?"}
//
// The server accepts peers with its own UID only, maps the token to its
// session and calls session.Session.Notify. It drops lines over MaxLine,
// malformed lines and unknown tokens. Events: working, idle, needs-you,
// done, failed, clear.
package notify

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"bunk/internal/session"
)

const (
	// ProtocolVersion is the "v" field of every message.
	ProtocolVersion = 1
	// MaxLine caps one message, newline included, in bytes.
	MaxLine = 4096
	// MaxText caps the message text a client sends, in bytes. Escaped as
	// JSON it stays under MaxLine.
	MaxText = 1024
	// maxToken caps the session token on the wire, in bytes.
	maxToken = 64
)

// EnvSocket and EnvSession are the environment variables that carry the
// socket path and the session token into a session.
const (
	EnvSocket  = session.EnvNotifySocket
	EnvSession = session.EnvNotifySession
)

// Message is one report from an agent.
type Message struct {
	Session string        // token from BUNK_GUI_SESSION
	Event   session.Event // what the agent reports
	Text    string        // notice text for needs-you, done and failed
}

// wire is a Message as JSON.
type wire struct {
	V       int    `json:"v"`
	Session string `json:"session"`
	Event   string `json:"event"`
	Message string `json:"message,omitempty"`
}

var (
	errTooLong = errors.New("message longer than MaxLine")
	errVersion = errors.New("unsupported protocol version")
	errEvent   = errors.New("unknown event")
	errToken   = errors.New("missing or oversized session token")
)

// Encode returns m as one JSON line ending in a newline. It cleans m.Text
// first (CleanText).
func (m Message) Encode() ([]byte, error) {
	if m.Session == "" || len(m.Session) > maxToken {
		return nil, fmt.Errorf("encode: %w", errToken)
	}
	if _, ok := session.ParseEvent(m.Event.String()); !ok {
		return nil, fmt.Errorf("encode: %w: %v", errEvent, m.Event)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // "<" stays one byte, so MaxText fits MaxLine
	err := enc.Encode(wire{V: ProtocolVersion, Session: m.Session, Event: m.Event.String(), Message: CleanText(m.Text)})
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	if buf.Len() > MaxLine {
		return nil, fmt.Errorf("encode: %w", errTooLong)
	}
	return buf.Bytes(), nil
}

// Decode parses one line, with or without its newline. It rejects lines
// over MaxLine, other protocol versions, unknown events and missing tokens.
// Text is valid UTF-8 but not otherwise cleaned.
func Decode(line []byte) (Message, error) {
	line = bytes.TrimSuffix(line, []byte("\n"))
	if len(line) >= MaxLine {
		return Message{}, fmt.Errorf("decode: %w", errTooLong)
	}
	var w wire
	if err := json.Unmarshal(line, &w); err != nil {
		return Message{}, fmt.Errorf("decode: %w", err)
	}
	if w.V != ProtocolVersion {
		return Message{}, fmt.Errorf("decode: %w %d", errVersion, w.V)
	}
	ev, ok := session.ParseEvent(w.Event)
	if !ok {
		return Message{}, fmt.Errorf("decode: %w %q", errEvent, w.Event)
	}
	if w.Session == "" || len(w.Session) > maxToken {
		return Message{}, fmt.Errorf("decode: %w", errToken)
	}
	return Message{Session: w.Session, Event: ev, Text: w.Message}, nil
}

// CleanText makes text safe to send: invalid UTF-8 and control characters
// become replacement characters and spaces, and the result is trimmed and
// cut to at most MaxText bytes on a character boundary.
func CleanText(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(text, "�"))
	text = strings.TrimSpace(text)
	if len(text) > MaxText {
		cut := MaxText
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = strings.TrimSpace(text[:cut])
	}
	return text
}

// NewToken returns a random, unguessable session token: 32 hex digits.
func NewToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("new token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
