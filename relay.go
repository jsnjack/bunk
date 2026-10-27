// relay.go - reports the pane whose agent most wants the user to the
// terminal bunk runs in, so the bunk-gui tab or the outer bunk pane around
// this bunk shows it (docs/remote-agent-status.md).
package main

import (
	"context"
	"os"

	"bunk/internal/notify/wire"
	"bunk/internal/session"
)

// agentRelay writes one in-band report to the host terminal each time the
// most urgent pane changes. It exists only when bunk runs inside a bunk
// front end (an inherited BUNK_NOTIFY_SESSION) or on a host reached over
// ssh; elsewhere newAgentRelay returns nil.
type agentRelay struct {
	token  string              // the BUNK_NOTIFY_SESSION bunk inherited; "" over plain ssh
	host   string              // this machine's name over ssh, else ""
	getenv func(string) string // tells tmux and screen apart for the wrapping
	last   string              // the report line written last; "" before the first
}

// newAgentRelay returns the relay for the environment getenv reads, or nil
// when nothing outside would read its reports. It reads the token before
// panes start: they get their own.
func newAgentRelay(getenv func(string) string) *agentRelay {
	token, overSSH := getenv(session.EnvNotifySession), getenv("SSH_CONNECTION") != ""
	if token == "" && !overSSH {
		return nil
	}
	r := &agentRelay{token: token, getenv: getenv}
	if overSSH {
		if h, err := os.Hostname(); err == nil {
			r.host = h
		} else {
			L.Debug("relay: host name", "err", err)
		}
	}
	return r
}

// urgency ranks a state by how much it wants the user: needs you, done,
// failed, working, idle, then no agent.
func urgency(state session.AgentState) int {
	switch state {
	case session.AgentNeedsYou:
		return 5
	case session.AgentDone:
		return 4
	case session.AgentFailed:
		return 3
	case session.AgentWorking:
		return 2
	case session.AgentIdle:
		return 1
	}
	return 0
}

// relayEvents name the report for each state an agent can be in.
var relayEvents = map[session.AgentState]session.Event{
	session.AgentWorking:  session.EventWorking,
	session.AgentIdle:     session.EventIdle,
	session.AgentNeedsYou: session.EventNeedsYou,
	session.AgentDone:     session.EventDone,
	session.AgentFailed:   session.EventFailed,
}

// mostUrgent returns the status of the pane whose agent most wants the
// user. On a tie the active pane wins, then layout order. ok is false when
// no pane has an agent. It takes each pane's lock.
func mostUrgent(panes []*Pane, active *Pane) (st session.Status, ok bool) {
	best := 0
	for _, p := range panes {
		s := p.Status()
		u := urgency(s.State)
		if u > best || (u == best && u > 0 && p == active) {
			st, best = s, u
		}
	}
	return st, best > 0
}

// update returns the report for the most urgent of panes, ready for the
// host terminal, or nil when it matches the report written last. It
// starts writing at the first agent: before one, there is nothing to
// clear. app.mu must be held.
func (r *agentRelay) update(panes []*Pane, active *Pane) []byte {
	if r == nil {
		return nil
	}
	rep := wire.Report{Session: r.token, Event: session.EventClear.String()}
	if st, ok := mostUrgent(panes, active); ok {
		host := st.Host
		if host == "" {
			host = r.host
		}
		rep = wire.Report{Session: r.token, Event: relayEvents[st.State].String(), Message: st.Notice,
			Agent: st.Agent, Host: host, Repo: st.Repo, Branch: st.Branch, Cwd: st.Cwd}
	}
	if r.last == "" && rep.Event == session.EventClear.String() {
		return nil
	}
	return r.write(rep)
}

// finish returns a clear report when the last report was not one, so the
// outer pane stops showing this bunk's agent once bunk exits; else nil.
func (r *agentRelay) finish() []byte {
	if r == nil || r.last == "" {
		return nil
	}
	return r.write(wire.Report{Session: r.token, Event: session.EventClear.String()})
}

// write encodes rep and returns it as an in-band sequence for the host
// terminal, or nil when it repeats the last one or cannot be encoded.
func (r *agentRelay) write(rep wire.Report) []byte {
	line, err := rep.Encode()
	if err != nil {
		L.Debug("relay: encode", "err", err)
		return nil
	}
	if string(line) == r.last {
		return nil
	}
	r.last = string(line)
	return wire.Wrap(wire.InBand(line), r.getenv)
}

// relayAgents writes the relay's report, if any, to the host terminal.
// render calls it under app.mu, before any early return, as it does
// forwardAlerts: tcell writes from the same goroutine, so the report never
// splits tcell's output.
func (app *App) relayAgents(root *Node, active *Pane) {
	if app.relay == nil {
		return
	}
	leaves := root.leaves()
	panes := make([]*Pane, 0, len(leaves))
	for _, l := range leaves {
		panes = append(panes, l.pane)
	}
	seq := app.relay.update(panes, active)
	if seq == nil {
		return
	}
	if _, err := app.host().Write(seq); err != nil {
		L.Log(context.Background(), LevelTrace, "relay: write report", "err", err)
	}
}
