// Package notify lets agent CLIs (Claude Code, Codex, Copilot CLI, Gemini
// CLI, …) report their state to a front end exactly, instead of the front
// end inferring it from output, bells and OSC notifications.
//
// The protocol does not depend on the front end: bunk's TUI and bunk-gui
// both serve it, and both binaries run the same CLI. The front end listens
// on a Unix socket under $XDG_RUNTIME_DIR (Listen) and starts every session
// with Options.NotifySocket and Options.NotifySession, which export
// BUNK_NOTIFY_SOCKET and BUNK_NOTIFY_SESSION, a random per-session token.
// An agent hook runs "bunk notify <event> [message]" or "bunk-gui notify
// <event> [message]" (RunCLI), which sends one JSON line per message:
//
//	{"v":1,"session":"<token>","event":"needs-you","message":"Allow npm install?"}
//
// The server accepts peers with its own UID only, maps the token to its
// session and calls session.Session.Notify. It drops lines over MaxLine,
// malformed lines and unknown tokens. Events: working, idle, needs-you,
// done, failed, clear.
//
// Where no socket is reachable, as on a host reached over ssh, the CLI
// writes the same line to its terminal instead, inside an OSC 1337 user
// variable (package wire), and the session parses it from its output.
// "bunk ssh" (RunSSH) gives the remote shell the pane's token for that
// path (docs/remote-agent-status.md).
package notify

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"bunk/internal/notify/wire"
	"bunk/internal/session"
)

const (
	// ProtocolVersion is the "v" field of every message.
	ProtocolVersion = wire.Version
	// MaxLine caps one message, newline included, in bytes.
	MaxLine = wire.MaxLine
)

// EnvSocket and EnvSession are the environment variables that carry the
// socket path and the session token into a session.
const (
	EnvSocket  = session.EnvNotifySocket
	EnvSession = session.EnvNotifySession
)

// Message is one report from an agent.
type Message struct {
	Session string        // token from BUNK_NOTIFY_SESSION
	Event   session.Event // what the agent reports
	Text    string        // notice text for needs-you, done and failed
}

var (
	errEvent = errors.New("unknown event")
	errToken = errors.New("missing session token")
)

// Encode returns m as one JSON line ending in a newline. It cleans m.Text
// first (wire.CleanText).
func (m Message) Encode() ([]byte, error) {
	if m.Session == "" {
		return nil, fmt.Errorf("encode: %w", errToken)
	}
	if _, ok := session.ParseEvent(m.Event.String()); !ok {
		return nil, fmt.Errorf("encode: %w: %v", errEvent, m.Event)
	}
	line, err := wire.Report{Session: m.Session, Event: m.Event.String(), Message: m.Text}.Encode()
	if err != nil {
		return nil, fmt.Errorf("notify: %w", err)
	}
	return line, nil
}

// Decode parses one line, with or without its newline. It rejects lines
// over MaxLine, other protocol versions, unknown events and missing tokens.
// Text is valid UTF-8 but not otherwise cleaned. Fields for agents on other
// machines are ignored: they travel in-band only.
func Decode(line []byte) (Message, error) {
	r, err := wire.Decode(line)
	if err != nil {
		return Message{}, fmt.Errorf("notify: %w", err)
	}
	ev, ok := session.ParseEvent(r.Event)
	if !ok {
		return Message{}, fmt.Errorf("decode: %w %q", errEvent, r.Event)
	}
	if r.Session == "" {
		return Message{}, fmt.Errorf("decode: %w", errToken)
	}
	return Message{Session: r.Session, Event: ev, Text: r.Message}, nil
}

// NewToken returns a random, unguessable session token: 32 hex digits.
func NewToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("new token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
