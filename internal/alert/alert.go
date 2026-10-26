// Package alert decides when an agent's state should alert a user who is
// not looking at it, and what the alert says. Both front ends use it:
// bunk-gui shows one desktop notification per tab, and bunk sends an
// OSC 777 notification per pane to its host terminal.
//
// A Tracker follows each source (a tab or a pane) and answers every status
// update with an Action: send an alert, withdraw the one shown, or leave it.
// An alert goes out when an agent enters a state the Settings name while
// nobody watches the source, and is withdrawn when someone does, when the
// state leaves those states, or when the source goes away. An agent that
// flaps between a state and another sends that state at most once per
// Tracker.Repeat.
package alert

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"bunk/internal/session"
)

// Settings say whether agents alert the user, and on which states.
type Settings struct {
	Enabled bool
	States  []session.AgentState
}

// stateNames are the config names of the states that can alert, in the
// order Default lists them.
var stateNames = []struct {
	state session.AgentState
	name  string
}{
	{session.AgentNeedsYou, "needs-you"},
	{session.AgentDone, "done"},
	{session.AgentFailed, "failed"},
}

// Default returns the settings without a [notifications] table: on, for
// needs-you, done and failed. A failed agent has stopped like a done one
// and waits for the user just the same.
func Default() Settings {
	s := Settings{Enabled: true}
	for _, n := range stateNames {
		s.States = append(s.States, n.state)
	}
	return s
}

// Alerts reports whether an agent entering state alerts the user.
func (s Settings) Alerts(state session.AgentState) bool {
	return s.Enabled && slices.Contains(s.States, state)
}

// StateName returns the config name of a state that can alert, such as
// "needs-you", or "" for another state.
func StateName(state session.AgentState) string {
	for _, n := range stateNames {
		if n.state == state {
			return n.name
		}
	}
	return ""
}

// ParseState returns the state with the config name name.
func ParseState(name string) (session.AgentState, bool) {
	for _, n := range stateNames {
		if n.name == name {
			return n.state, true
		}
	}
	return session.AgentNone, false
}

// AlertingStates lists every state that can alert, in config order.
func AlertingStates() []session.AgentState {
	out := make([]session.AgentState, len(stateNames))
	for i, n := range stateNames {
		out[i] = n.state
	}
	return out
}

// Config is the [notifications] table of config.toml as both front ends
// read it. A missing key keeps its default.
type Config struct {
	Enabled *bool    `toml:"enabled"`
	States  []string `toml:"states"`
}

// Settings resolves c. An unknown state name is an error, and the states
// then keep their default.
func (c Config) Settings() (Settings, error) {
	s := Default()
	if c.Enabled != nil {
		s.Enabled = *c.Enabled
	}
	if c.States == nil {
		return s, nil
	}
	var states []session.AgentState
	for _, name := range c.States {
		st, ok := ParseState(name)
		if !ok {
			return s, fmt.Errorf("notifications.states: unknown state %q (want needs-you, done or failed)", name)
		}
		if !slices.Contains(states, st) {
			states = append(states, st)
		}
	}
	s.States = states
	if s.States == nil {
		s.States = []session.AgentState{}
	}
	return s, nil
}

// Names returns the config names of states, in config order, for config
// editors.
func Names(states []session.AgentState) []string {
	out := []string{}
	for _, n := range stateNames {
		if slices.Contains(states, n.state) {
			out = append(out, n.name)
		}
	}
	return out
}

// Alert is what one notification about an agent says.
type Alert struct {
	State  session.AgentState
	Title  string // "claude needs you"
	Notice string // the agent's notice, or a default for the state
	Where  string // "repo ⎇ branch", the repo, or "" outside a git work tree
}

// unnamedAgent names an agent in a title when the status has no name.
const unnamedAgent = "agent"

// For returns the alert for an agent's status. An agent on another host
// is named with it: "claude on gpu01 needs you".
func For(st session.Status) Alert {
	agent := st.Agent
	if agent == "" {
		agent = unnamedAgent
	}
	if h := host(st); h != "" {
		agent += " on " + h
	}
	a := Alert{State: st.State, Notice: st.Notice}
	var fallback string
	switch st.State {
	case session.AgentNeedsYou:
		a.Title, fallback = agent+" needs you", "Waiting for you"
	case session.AgentDone:
		a.Title, fallback = agent+" is done", "Finished its turn"
	case session.AgentFailed:
		a.Title, fallback = agent+" failed", "Stopped with an error"
	default:
		a.Title = agent
	}
	if strings.TrimSpace(a.Notice) == "" {
		a.Notice = fallback
	}
	switch {
	case st.Repo != "" && st.Branch != "":
		a.Where = st.Repo + " ⎇ " + st.Branch
	case st.Repo != "":
		a.Where = st.Repo
	}
	return a
}

// host is the machine the agent runs on when it is not this one: the SSH
// host of the pane's foreground process. A host that reports the agent's
// state itself goes here too.
func host(st session.Status) string {
	return st.SSHHost
}

// Body is the notice and, on a second line, where the agent runs.
func (a Alert) Body() string {
	if a.Where == "" {
		return a.Notice
	}
	return a.Notice + "\n" + a.Where
}

// Action is what a front end does with a source's notification.
type Action uint8

// The actions an Update or a Remove asks for.
const (
	Keep     Action = iota // leave the source's notification as it is
	Send                   // show the alert, replacing the source's notification
	Withdraw               // remove the source's notification
)

// String names the action, for logs and test messages.
func (a Action) String() string {
	switch a {
	case Send:
		return "send"
	case Withdraw:
		return "withdraw"
	}
	return "keep"
}

// DefaultRepeat is how soon an alert for the same state may follow the
// last one of its source, unless the user looked at the source in between.
const DefaultRepeat = 10 * time.Second

// Tracker follows the agent state of each source and decides when to
// alert. Sources are identified by an int: bunk-gui's tab ID, bunk's pane
// ID. The zero Tracker is ready to use with alerts off; set Settings. It is
// not safe for concurrent use.
type Tracker struct {
	Settings Settings
	// Repeat is how soon the same state may alert again; zero is
	// DefaultRepeat.
	Repeat time.Duration

	sources map[int]*source
}

// source is what a Tracker knows about one source.
type source struct {
	seen    view // state and notice at the last Update
	pending bool // entered an alerting state; not sent yet
	shown   bool // a notification is up
	// The state of the last alert sent since the user last watched the
	// source, and when it went out.
	sent   session.AgentState
	sentAt time.Time
}

// view is the part of a status whose change is a new event: the state and
// the agent's notice.
type view struct {
	state  session.AgentState
	notice string
}

func (t *Tracker) repeat() time.Duration {
	if t.Repeat > 0 {
		return t.Repeat
	}
	return DefaultRepeat
}

// Update takes a source's current status. watched says that the user is
// looking at it: in bunk-gui, the window is active and the tab selected;
// in bunk, the host terminal has the focus and the pane is the active one.
// It returns what to do with the source's notification and, for Send, the
// alert to show.
//
// A source alerts once it enters an alerting state, or its notice changes
// in one, while nobody watches it. Watching it, or leaving the alerting
// states, withdraws its notification. The same state alerts again only
// Repeat after the last alert, unless the user watched the source since;
// until then the alert is held, and Next says when to call Update again.
func (t *Tracker) Update(id int, st session.Status, watched bool, now time.Time) (Action, Alert) {
	if t.sources == nil {
		t.sources = map[int]*source{}
	}
	s := t.sources[id]
	if s == nil {
		s = &source{}
		t.sources[id] = s
	}
	v := view{st.State, st.Notice}
	changed := v != s.seen
	s.seen = v
	if watched || !t.Settings.Alerts(st.State) {
		s.pending = false
		if watched {
			s.sent = session.AgentNone // seen: the next alert need not wait
		}
		if s.shown {
			s.shown = false
			return Withdraw, Alert{}
		}
		return Keep, Alert{}
	}
	if changed {
		s.pending = true
	}
	if !s.pending || s.held(t.repeat(), now) {
		return Keep, Alert{}
	}
	s.pending, s.shown, s.sent, s.sentAt = false, true, st.State, now
	return Send, For(st)
}

// held reports whether the source's pending alert waits for the repeat
// interval of the last alert of the same state.
func (s *source) held(repeat time.Duration, now time.Time) bool {
	return s.seen.state == s.sent && now.Sub(s.sentAt) < repeat
}

// Next returns when the earliest held alert may go out; zero when no alert
// is held.
func (t *Tracker) Next() time.Time {
	var next time.Time
	for _, s := range t.sources {
		if !s.pending || s.seen.state != s.sent {
			continue
		}
		if at := s.sentAt.Add(t.repeat()); next.IsZero() || at.Before(next) {
			next = at
		}
	}
	return next
}

// Remove forgets a source that went away. It returns Withdraw when the
// source had a notification up.
func (t *Tracker) Remove(id int) Action {
	s := t.sources[id]
	delete(t.sources, id)
	if s != nil && s.shown {
		return Withdraw
	}
	return Keep
}

// IDs lists the sources the tracker knows, for front ends that reconcile
// them with their tabs or panes.
func (t *Tracker) IDs() []int {
	ids := make([]int, 0, len(t.sources))
	for id := range t.sources {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Shown reports whether the source has a notification up.
func (t *Tracker) Shown(id int) bool {
	s := t.sources[id]
	return s != nil && s.shown
}
