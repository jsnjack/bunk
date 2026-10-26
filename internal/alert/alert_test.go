package alert_test

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"bunk/internal/alert"
	"bunk/internal/session"
)

var (
	needs   = session.AgentNeedsYou
	done    = session.AgentDone
	failed  = session.AgentFailed
	working = session.AgentWorking
	idle    = session.AgentIdle
)

func status(state session.AgentState, notice string) session.Status {
	return session.Status{Agent: "claude", State: state, Notice: notice, Repo: "bunk", Branch: "main"}
}

func boolp(b bool) *bool { return &b }

// TestConfigSettings: the [notifications] table resolves to settings;
// missing keys keep their defaults and a bad state name is an error.
func TestConfigSettings(t *testing.T) {
	all := []session.AgentState{needs, done, failed}
	for _, tc := range []struct {
		name    string
		cfg     alert.Config
		enabled bool
		states  []session.AgentState
		err     string
	}{
		{name: "no table", enabled: true, states: all},
		{name: "off", cfg: alert.Config{Enabled: boolp(false)}, enabled: false, states: all},
		{name: "on", cfg: alert.Config{Enabled: boolp(true)}, enabled: true, states: all},
		{name: "two states", cfg: alert.Config{States: []string{"done", "needs-you"}}, enabled: true, states: []session.AgentState{done, needs}},
		{name: "repeated state", cfg: alert.Config{States: []string{"done", "done"}}, enabled: true, states: []session.AgentState{done}},
		{name: "no states", cfg: alert.Config{States: []string{}}, enabled: true, states: []session.AgentState{}},
		{name: "unknown state", cfg: alert.Config{States: []string{"done", "working"}}, enabled: true, states: all,
			err: `notifications.states: unknown state "working" (want needs-you, done or failed)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := tc.cfg.Settings()
			if got := fmt.Sprint(err); (tc.err == "" && err != nil) || (tc.err != "" && got != tc.err) {
				t.Errorf("error %v, want %q", err, tc.err)
			}
			if s.Enabled != tc.enabled || !slices.Equal(s.States, tc.states) {
				t.Errorf("settings %+v, want enabled %v states %v", s, tc.enabled, tc.states)
			}
		})
	}
}

// TestStateNames: config names round-trip and Names keeps config order.
func TestStateNames(t *testing.T) {
	for _, st := range alert.AlertingStates() {
		name := alert.StateName(st)
		if back, ok := alert.ParseState(name); !ok || back != st {
			t.Errorf("state %v: name %q parses to %v %v", st, name, back, ok)
		}
	}
	if n := alert.StateName(working); n != "" {
		t.Errorf("working has config name %q", n)
	}
	if got := alert.Names([]session.AgentState{failed, needs}); !slices.Equal(got, []string{"needs-you", "failed"}) {
		t.Errorf("Names = %q", got)
	}
	if got := alert.Names(nil); got == nil || len(got) != 0 {
		t.Errorf("Names(nil) = %#v, want an empty list", got)
	}
}

// TestAlertText: the title names the agent and the state; the body is the
// notice, or a default, and the repo and branch.
func TestAlertText(t *testing.T) {
	for _, tc := range []struct {
		name  string
		st    session.Status
		title string
		body  string
	}{
		{"needs you", session.Status{Agent: "claude", State: needs, Notice: "Allow npm install?", Repo: "bunk", Branch: "main"},
			"claude needs you", "Allow npm install?\nbunk ⎇ main"},
		{"done", session.Status{Agent: "codex", State: done, Repo: "bunk", Branch: "gui-notifications"},
			"codex is done", "Finished its turn\nbunk ⎇ gui-notifications"},
		{"failed", session.Status{Agent: "gemini", State: failed, Notice: "  "},
			"gemini failed", "Stopped with an error"},
		{"needs you without notice", session.Status{Agent: "aider", State: needs, Repo: "notes"},
			"aider needs you", "Waiting for you\nnotes"},
		{"unnamed agent", session.Status{State: done, Notice: "ok", Branch: "main"},
			"agent is done", "ok"},
		{"on another host", session.Status{Agent: "claude", State: needs, Notice: "Allow?", SSHHost: "gpu01", Repo: "model", Branch: "train"},
			"claude on gpu01 needs you", "Allow?\nmodel ⎇ train"},
		{"unnamed agent on another host", session.Status{State: failed, SSHHost: "gpu01"},
			"agent on gpu01 failed", "Stopped with an error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := alert.For(tc.st)
			if a.Title != tc.title || a.Body() != tc.body || a.State != tc.st.State {
				t.Errorf("alert %+v body %q, want title %q body %q", a, a.Body(), tc.title, tc.body)
			}
		})
	}
}

// TestDecisionTable: an agent entering each state from working, watched
// or not, under each setting, alerts exactly when the settings name the
// state and nobody watches the source.
func TestDecisionTable(t *testing.T) {
	settings := map[string]alert.Settings{
		"default":        alert.Default(),
		"off":            {Enabled: false, States: alert.AlertingStates()},
		"needs-you only": {Enabled: true, States: []session.AgentState{needs}},
		"no states":      {Enabled: true, States: []session.AgentState{}},
	}
	states := []session.AgentState{session.AgentNone, working, idle, needs, done, failed}
	for sname, set := range settings {
		for _, st := range states {
			for _, watched := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/state %d/watched %v", sname, st, watched), func(t *testing.T) {
					tr := alert.Tracker{Settings: set}
					now := time.Unix(1000, 0)
					if act, _ := tr.Update(1, status(working, ""), false, now); act != alert.Keep {
						t.Fatalf("working: %v", act)
					}
					act, a := tr.Update(1, status(st, "n"), watched, now.Add(time.Second))
					want := alert.Keep
					if !watched && set.Alerts(st) {
						want = alert.Send
					}
					if act != want {
						t.Fatalf("got %v, want %v", act, want)
					}
					if act == alert.Send && (a.State != st || !strings.HasPrefix(a.Title, "claude ")) {
						t.Errorf("alert %+v", a)
					}
					if tr.Shown(1) != (act == alert.Send) {
						t.Errorf("shown %v after %v", tr.Shown(1), act)
					}
				})
			}
		}
	}
}

// step is one Update in a sequence.
type step struct {
	at      time.Duration
	st      session.AgentState
	notice  string
	watched bool
	want    alert.Action
}

func runSteps(t *testing.T, tr *alert.Tracker, steps []step) {
	t.Helper()
	t0 := time.Unix(1000, 0)
	for i, s := range steps {
		act, a := tr.Update(7, status(s.st, s.notice), s.watched, t0.Add(s.at))
		if act != s.want {
			t.Fatalf("step %d (%v at %v, watched %v): %v, want %v", i, s.st, s.at, s.watched, act, s.want)
		}
		if act == alert.Send && a.State != s.st {
			t.Fatalf("step %d: alert for %v, want %v", i, a.State, s.st)
		}
	}
}

// TestReplaceAndWithdraw: a newer state replaces the notification; looking
// at the source, leaving the alerting states, turning alerts off and
// removing the source withdraw it.
func TestReplaceAndWithdraw(t *testing.T) {
	s := time.Second
	t.Run("done replaces needs-you, watching withdraws", func(t *testing.T) {
		runSteps(t, &alert.Tracker{Settings: alert.Default()}, []step{
			{0, needs, "Allow?", false, alert.Send},
			{s, needs, "Allow?", false, alert.Keep},
			{2 * s, done, "Finished", false, alert.Send},
			{3 * s, done, "Finished", true, alert.Withdraw},
			{4 * s, done, "Finished", true, alert.Keep},
			{5 * s, done, "Finished", false, alert.Keep}, // already seen
		})
	})
	t.Run("acknowledged: the state leaves", func(t *testing.T) {
		runSteps(t, &alert.Tracker{Settings: alert.Default()}, []step{
			{0, needs, "", false, alert.Send},
			{s, idle, "", false, alert.Withdraw},
			{2 * s, working, "", false, alert.Keep},
		})
	})
	t.Run("a new notice in the same state", func(t *testing.T) {
		runSteps(t, &alert.Tracker{Settings: alert.Default()}, []step{
			{0, needs, "first", false, alert.Send},
			{20 * s, needs, "second", false, alert.Send},
		})
	})
	t.Run("seen while watched", func(t *testing.T) {
		runSteps(t, &alert.Tracker{Settings: alert.Default()}, []step{
			{0, needs, "", true, alert.Keep},
			{s, needs, "", false, alert.Keep},
		})
	})
	t.Run("turned off", func(t *testing.T) {
		tr := &alert.Tracker{Settings: alert.Default()}
		runSteps(t, tr, []step{{0, failed, "", false, alert.Send}})
		tr.Settings.Enabled = false
		runSteps(t, tr, []step{{0, failed, "", false, alert.Withdraw}})
		tr.Settings.Enabled = true
		runSteps(t, tr, []step{{0, failed, "", false, alert.Keep}})
	})
	t.Run("removed", func(t *testing.T) {
		tr := &alert.Tracker{Settings: alert.Default()}
		runSteps(t, tr, []step{{0, done, "", false, alert.Send}})
		if act := tr.Remove(7); act != alert.Withdraw {
			t.Errorf("remove with a notification up: %v", act)
		}
		if act := tr.Remove(7); act != alert.Keep {
			t.Errorf("remove again: %v", act)
		}
		if ids := tr.IDs(); len(ids) != 0 {
			t.Errorf("ids after remove: %v", ids)
		}
	})
}

// TestFlappingStates: an agent that flaps between needs-you and working
// sends needs-you at most once per Repeat; the held alert goes out when
// Next says, and looking at the source lifts the limit.
func TestFlappingStates(t *testing.T) {
	const repeat = 10 * time.Second
	tr := &alert.Tracker{Settings: alert.Default(), Repeat: repeat}
	t0 := time.Unix(1000, 0)
	sends, withdraws := 0, 0
	for i := range 40 { // 20 s of flapping every 500 ms
		st := needs
		if i%2 == 1 {
			st = working
		}
		act, _ := tr.Update(3, status(st, ""), false, t0.Add(time.Duration(i)*500*time.Millisecond))
		switch act {
		case alert.Send:
			sends++
		case alert.Withdraw:
			withdraws++
		}
	}
	if sends != 2 || withdraws != 2 {
		t.Errorf("20 s of flapping: %d sends, %d withdraws; want 2 and 2", sends, withdraws)
	}

	tr = &alert.Tracker{Settings: alert.Default(), Repeat: repeat}
	runSteps(t, tr, []step{
		{0, needs, "", false, alert.Send},
		{time.Second, working, "", false, alert.Withdraw},
		{2 * time.Second, needs, "", false, alert.Keep},
	})
	if next := tr.Next(); !next.Equal(t0.Add(repeat)) {
		t.Fatalf("Next = %v, want %v", next, t0.Add(repeat))
	}
	runSteps(t, tr, []step{
		{5 * time.Second, needs, "", false, alert.Keep},
		{repeat, needs, "", false, alert.Send},
	})
	if next := tr.Next(); !next.IsZero() {
		t.Errorf("Next after the held alert went out: %v", next)
	}

	t.Run("another state is not held", func(t *testing.T) {
		runSteps(t, &alert.Tracker{Settings: alert.Default(), Repeat: repeat}, []step{
			{0, needs, "", false, alert.Send},
			{time.Second, done, "", false, alert.Send},
		})
	})
	t.Run("a held alert drops when the state leaves", func(t *testing.T) {
		tr := &alert.Tracker{Settings: alert.Default(), Repeat: repeat}
		runSteps(t, tr, []step{
			{0, needs, "", false, alert.Send},
			{time.Second, working, "", false, alert.Withdraw},
			{2 * time.Second, needs, "", false, alert.Keep},
			{3 * time.Second, working, "", false, alert.Keep},
		})
		if next := tr.Next(); !next.IsZero() {
			t.Errorf("Next = %v, want none held", next)
		}
	})
	t.Run("looking lifts the limit", func(t *testing.T) {
		runSteps(t, &alert.Tracker{Settings: alert.Default(), Repeat: repeat}, []step{
			{0, needs, "", false, alert.Send},
			{time.Second, idle, "", true, alert.Withdraw},
			{2 * time.Second, needs, "", false, alert.Send},
		})
	})
}

// TestOSC777: the notify sequence keeps program text from escaping the
// OSC string or the title, and stays within its size.
func TestOSC777(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    alert.Alert
		want string
	}{
		{"plain", alert.Alert{Title: "claude needs you", Notice: "Allow npm install?", Where: "bunk ⎇ main"},
			"\x1b]777;notify;claude needs you;Allow npm install? · bunk ⎇ main\x1b\\"},
		{"no repo", alert.Alert{Title: "codex is done", Notice: "Finished its turn"},
			"\x1b]777;notify;codex is done;Finished its turn\x1b\\"},
		{"escapes and controls", alert.Alert{Title: "evil\x1b]0;pwn\x07agent needs you", Notice: "line one\r\nline\ttwo\u009b31m\x00"},
			"\x1b]777;notify;evil ]0,pwn agent needs you;line one line two 31m\x1b\\"},
		{"semicolons", alert.Alert{Title: "a;b needs you", Notice: "x;y", Where: "r;1 ⎇ b;2"},
			"\x1b]777;notify;a,b needs you;x,y · r,1 ⎇ b,2\x1b\\"},
		{"bidi and invalid UTF-8", alert.Alert{Title: "claude\u202e needs you", Notice: "ok\xff\xfe \u2066x\u2069 \ufffd"},
			"\x1b]777;notify;claude needs you;ok x \ufffd\x1b\\"},
		{"line separators", alert.Alert{Title: "t", Notice: "a\u2028b\u2029c\u0085d"},
			"\x1b]777;notify;t;a b c d\x1b\\"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(alert.OSC777(tc.a)); got != tc.want {
				t.Errorf("OSC777 = %q\nwant      %q", got, tc.want)
			}
		})
	}
	t.Run("long text is cut", func(t *testing.T) {
		a := alert.Alert{Title: strings.Repeat("é", 300), Notice: strings.Repeat("🙂", 300), Where: "bunk ⎇ main"}
		seq := alert.OSC777(a)
		checkOSC777(t, seq)
		_, rest, _ := bytes.Cut(seq, []byte("notify;"))
		title, body, _ := bytes.Cut(bytes.TrimSuffix(rest, []byte("\x1b\\")), []byte(";"))
		if len(title) > 120 || len(body) > 360 || !bytes.HasSuffix(title, []byte("…")) || !bytes.HasSuffix(body, []byte("…")) {
			t.Errorf("title %d bytes, body %d bytes: %q / %q", len(title), len(body), title[len(title)-8:], body[len(body)-8:])
		}
	})
}

// checkOSC777 checks the invariants of a notify sequence: one OSC string,
// valid UTF-8, no control characters inside, three fields, bounded.
func checkOSC777(t *testing.T, seq []byte) {
	t.Helper()
	const head, tail = "\x1b]777;notify;", "\x1b\\"
	if !bytes.HasPrefix(seq, []byte(head)) || !bytes.HasSuffix(seq, []byte(tail)) {
		t.Fatalf("not a notify sequence: %q", seq)
	}
	inner := seq[len(head) : len(seq)-len(tail)]
	if !utf8.Valid(inner) {
		t.Errorf("invalid UTF-8: %q", inner)
	}
	for _, r := range string(inner) {
		if unicode.IsControl(r) || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) {
			t.Errorf("character %U inside: %q", r, inner)
		}
	}
	if n := bytes.Count(inner, []byte(";")); n != 1 {
		t.Errorf("%d separators inside: %q", n, inner)
	}
	if len(seq) > len(head)+120+1+360+len(tail) {
		t.Errorf("%d bytes", len(seq))
	}
}

func FuzzOSC777(f *testing.F) {
	f.Add("claude needs you", "Allow?", "bunk ⎇ main")
	f.Add("a;b\x1b\\", "\x07\x9b\u202e;", "\xff")
	f.Add(strings.Repeat("x", 500), strings.Repeat("é", 500), "")
	f.Fuzz(func(t *testing.T, title, notice, where string) {
		checkOSC777(t, alert.OSC777(alert.Alert{Title: title, Notice: notice, Where: where}))
	})
}
