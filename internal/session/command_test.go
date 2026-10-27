package session

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// cmdStep is one event in a command test, at an offset from the start: a
// shell mark (OSC 133;C or 133;D) or a process poll.
type cmdStep struct {
	at    time.Duration
	mark  string // "C", "D" with or without ";status", or "" for a poll
	pgid  int    // poll: the foreground group; shellGroup is the shell's
	name  string // poll: its process name
	argv  []string
	agent string // poll: the agent bunk recognised in the group
}

// shellGroup leads the shell's process group in the command tests.
const shellGroup = 100

func mark(at time.Duration, m string) cmdStep { return cmdStep{at: at, mark: m} }

func poll(at time.Duration, pgid int, name string, argv ...string) cmdStep {
	return cmdStep{at: at, pgid: pgid, name: name, argv: argv}
}

func shellPoll(at time.Duration) cmdStep { return poll(at, shellGroup, "bash", "bash") }

// runCommandSteps feeds steps to a new session from t0 and returns it.
func runCommandSteps(t *testing.T, t0 time.Time, steps []cmdStep) *Session {
	t.Helper()
	s := newTestSession(20, 2)
	last := 0
	for _, st := range steps {
		now := t0.Add(st.at)
		s.Lock()
		switch {
		case st.mark == "C":
			s.commandStarted(now)
		case strings.HasPrefix(st.mark, "D"):
			osc, ok := parseStatusOSC(133, []byte(st.mark))
			if !ok || !osc.finished {
				t.Fatalf("mark %q does not parse as 133;D", st.mark)
			}
			s.commandFinished(osc.exitCode, osc.exitKnown, now)
		default:
			s.agent = st.agent
			s.noteForeground(shellGroup, st.pgid, st.name, st.argv, st.pgid != last, now)
			last = st.pgid
		}
		s.Unlock()
	}
	return s
}

// TestCommandEnds: the command a session reports as ended, from OSC 133
// marks where the shell sends them and from the process poll where it
// does not. A marked command runs from 133;C to 133;D, whatever groups
// come and go between; the poll names it after the first group it sees
// that started after the last prompt. Commands an agent ran in are not
// reported.
func TestCommandEnds(t *testing.T) {
	t.Parallel()
	const ms = time.Millisecond
	const sec = time.Second
	t0 := time.Unix(1000, 0)
	for _, tc := range []struct {
		name  string
		steps []cmdStep
		want  Command // Ended is an offset from t0 in Took's unit; zero means none
		ended time.Duration
	}{
		{"marks", []cmdStep{
			mark(0, "D;0"), shellPoll(500 * ms), mark(sec, "C"),
			poll(1500*ms, 200, "tar", "tar", "czf", "my backup.tgz", "src"),
			shellPoll(3 * sec), mark(3100*ms, "D;2"),
		}, Command{Name: "tar", Line: "tar czf 'my backup.tgz' src", Took: 2100 * ms, Exit: 2, ExitKnown: true}, 3100 * ms},
		{"a loop between marks", []cmdStep{
			mark(0, "C"), poll(500*ms, 200, "gzip", "gzip", "a"), shellPoll(sec),
			poll(1500*ms, 201, "gzip", "gzip", "b"), shellPoll(2 * sec), mark(3*sec, "D;0"),
		}, Command{Name: "gzip", Line: "gzip a", Took: 3 * sec, ExitKnown: true}, 3 * sec},
		{"the poll sees the group before the mark", []cmdStep{
			mark(0, "D;0"), shellPoll(200 * ms), poll(sec, 200, "rsync", "rsync", "-a", "src/", "dst/"),
			mark(sec+ms, "C"), mark(5*sec, "D;0"),
		}, Command{Name: "rsync", Line: "rsync -a src/ dst/", Took: 4*sec - ms, ExitKnown: true}, 5 * sec},
		{"a group from before the prompt names nothing", []cmdStep{
			poll(0, 300, "ssh", "ssh", "build01"), mark(sec, "D;0"), mark(2*sec, "C"), mark(4*sec, "D;1"),
		}, Command{Took: 2 * sec, Exit: 1, ExitKnown: true}, 4 * sec},
		{"a nested shell", []cmdStep{
			mark(0, "C"), poll(500*ms, 200, "bash", "bash"), mark(600*ms, "D;0"),
			poll(sec, 200, "bash", "bash"), mark(2*sec, "C"), poll(2500*ms, 201, "sleep", "sleep", "9"),
			mark(11*sec, "D;0"),
		}, Command{Name: "sleep", Line: "sleep 9", Took: 9 * sec, ExitKnown: true}, 11 * sec},
		{"a mark without a status", []cmdStep{mark(0, "C"), mark(sec, "D")}, Command{Took: sec}, sec},
		{"a new mark restarts the command", []cmdStep{mark(0, "C"), mark(2*sec, "C"), mark(3*sec, "D;0")},
			Command{Took: sec, ExitKnown: true}, 3 * sec},
		{"prompts alone end nothing", []cmdStep{mark(0, "D;0"), mark(sec, "D;1"), shellPoll(2 * sec)}, Command{}, 0},
		{"no marks", []cmdStep{
			shellPoll(0), poll(sec, 200, "make", "make", "-j8"), poll(2*sec, 200, "make", "make", "-j8"),
			poll(3*sec, 0, ""), shellPoll(12 * sec),
		}, Command{Name: "make", Line: "make -j8", Took: 11 * sec}, 12 * sec},
		{"no marks, a list of commands", []cmdStep{
			shellPoll(0), poll(sec, 200, "sleep", "sleep", "5"), poll(6*sec, 201, "tar", "tar", "xf", "a.tar"),
			shellPoll(9 * sec),
		}, Command{Name: "sleep", Line: "sleep 5", Took: 8 * sec}, 9 * sec},
		{"no marks, still running", []cmdStep{shellPoll(0), poll(sec, 200, "make", "make")}, Command{}, 0},
		{"an agent ran", []cmdStep{
			mark(0, "C"), {at: sec, pgid: 200, name: "node", argv: []string{"node", "claude"}, agent: "claude"},
			shellPoll(9 * sec), mark(9*sec, "D;0"),
		}, Command{}, 0},
		{"an agent ran, no marks", []cmdStep{
			shellPoll(0), {at: sec, pgid: 200, name: "claude", argv: []string{"claude"}, agent: "claude"},
			shellPoll(9 * sec),
		}, Command{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := runCommandSteps(t, t0, tc.steps)
			want := tc.want
			if want != (Command{}) {
				want.Ended = t0.Add(tc.ended)
			}
			if got := s.Status().Command; got != want {
				t.Errorf("command %+v\nwant %+v", got, want)
			}
		})
	}
}

// TestCommandLine: the line reads like what the user typed; control
// characters go and a long line is cut.
func TestCommandLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{nil, ""},
		{[]string{"rsync", "-a", "src/", "host:dst/"}, "rsync -a src/ host:dst/"},
		{[]string{"tar", "czf", "my backup.tgz", ""}, "tar czf 'my backup.tgz' ''"},
		{[]string{"echo", "it's"}, `echo 'it'\''s'`},
		{[]string{"printf", "a\x1b]0;x\x07b"}, "printf a ]0;x b"},
		{[]string{"cat", strings.Repeat("x", 500)}, "cat " + strings.Repeat("x", noticeLimit-5) + "…"},
	} {
		if got := commandLine(tc.argv); got != tc.want {
			t.Errorf("commandLine(%q) = %q, want %q", tc.argv, got, tc.want)
		}
	}
}

// TestCommandEndAnnouncedOnce: a command's end is a status change that
// StatusChanged announces once.
func TestCommandEndAnnouncedOnce(t *testing.T) {
	t.Parallel()
	s := newTestSession(20, 2)
	now := time.Now()
	s.Lock()
	defer s.Unlock()
	s.takeAgentChange(now)
	s.commandStarted(now)
	if s.takeAgentChange(now) {
		t.Error("a command start was announced")
	}
	s.commandFinished(0, true, now.Add(time.Second))
	if !s.takeAgentChange(now) {
		t.Error("a command end was not announced")
	}
	if s.takeAgentChange(now) {
		t.Error("a command end was announced twice")
	}
}

// TestCommandsOnPTY runs an interactive bash on a PTY, with OSC 133 marks
// in its prompts as vte.sh sets them and without, and checks the command
// it reports when a command line ends: the poll names it, and only the
// marks give its exit status.
func TestCommandsOnPTY(t *testing.T) {
	t.Parallel()
	const ps0 = `PS0=\e]133;C\e\\`
	const ps1 = `PS1=\[\e]133;D;$?\e\\\]$ `
	for _, tm := range timings(fastTiming) {
		for _, marks := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/marks %v", tm.name, marks), func(t *testing.T) {
				t.Parallel()
				// The poll must see the command run at least once.
				nap := max(300*time.Millisecond, tm.timing.poll()*3/2)
				args := []string{"env", "HISTFILE=", "PS1=$ "}
				if marks {
					args = append(args, ps0, ps1)
				}
				args = append(args, "bash", "--norc", "--noprofile", "-i")
				s, err := Start(Options{Cols: 60, Rows: 10, Scrollback: 100, Args: args, Timing: tm.timing})
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan struct{})
				defer func() {
					close(done)
					s.Close()
				}()
				changed := make(chan struct{}, 1)
				s.Run(Hooks{StatusChanged: func() {
					select {
					case changed <- struct{}{}:
					default:
					}
				}}, done)
				line := fmt.Sprintf("sleep %.1f; (exit 3)", nap.Seconds())
				if err := s.WriteInput([]byte(line + "\r")); err != nil {
					t.Fatal(err)
				}
				deadline := time.After(10*time.Second + nap)
				for s.Status().Command.Ended.IsZero() {
					select {
					case <-changed:
					case <-deadline:
						t.Fatalf("no command ended: %+v", s.Status())
					}
				}
				c := s.Status().Command
				want := fmt.Sprintf("sleep %.1f", nap.Seconds())
				// The marks time the command line to within the reader's
				// delay; the poll sees it start up to a poll late, and a
				// loaded machine delays ticks.
				least := nap - 50*time.Millisecond
				if !marks {
					least = nap - tm.timing.poll() - 100*time.Millisecond
				}
				if c.Name != "sleep" || c.Line != want || c.Took < least || c.Took > nap+5*time.Second {
					t.Errorf("command %+v, want sleep %q that took at least %v", c, want, least)
				}
				if marks && (!c.ExitKnown || c.Exit != 3) {
					t.Errorf("with marks: exit %d known %v, want 3", c.Exit, c.ExitKnown)
				}
				if !marks && c.ExitKnown {
					t.Errorf("without marks: exit %d known", c.Exit)
				}
			})
		}
	}
}
