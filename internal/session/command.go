// command.go - the commands a session's shell runs, so that a front end
// can tell the user when a long one ends.
//
// A shell with OSC 133 marks (VTE's vte.sh, kitty's, WezTerm's and other
// shell integrations) sends 133;C as a command line starts and 133;D with
// its exit status at the next prompt: the command runs from one to the
// other, a loop or a list of commands included. Without marks, a command
// runs while a process group other than the shell's holds the terminal's
// foreground, as the process poll sees it, to within Timing.Poll, and its
// exit status is unknown. Either way the poll names the command after the
// first process group it sees run in it.
package session

import (
	"strings"
	"time"
)

// Command is a command that ended in a session's foreground.
type Command struct {
	Name      string        // process name, such as "tar"; "" when no poll saw it run
	Line      string        // its command line, cleaned and capped; "" when unknown
	Took      time.Duration // from its start to its end
	Exit      int           // exit status from the shell's OSC 133;D mark
	ExitKnown bool          // the shell reported Exit
	Ended     time.Time     // when it ended; zero when no command has ended
}

// Failed reports whether the shell reported a non-zero exit status.
func (c Command) Failed() bool { return c.ExitKnown && c.Exit != 0 }

// runningCommand is the command the shell runs now. The zero value means
// the shell waits at its prompt.
type runningCommand struct {
	start  time.Time
	marked bool // OSC 133;C started it, so only the shell's 133;D ends it
	named  bool // name and line come from a process group bunk saw run in it
	name   string
	line   string
	agent  bool // an agent ran in it: the agent alerts about itself
}

// foreground is the process group the last process poll saw.
type foreground struct {
	pgid  int
	shell bool // the shell's own group: it waits at its prompt
	name  string
	line  string    // its leader's command line
	since time.Time // when a poll first saw the group
}

// commandLineLimit caps the command line kept for notifications.
const commandLineLimit = noticeLimit

// commandLine joins argv into one line, quoting the arguments that hold
// spaces or nothing, so the line reads like what the user typed.
func commandLine(argv []string) string {
	var b strings.Builder
	for i, arg := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		if arg == "" || strings.ContainsAny(arg, " \t\n'\"") {
			b.WriteString("'" + strings.ReplaceAll(arg, "'", `'\''`) + "'")
			continue
		}
		b.WriteString(arg)
		if b.Len() > 4*commandLineLimit {
			break // cleanNotice caps it; the rest is never shown
		}
	}
	return cleanNotice(b.String(), "")
}

// commandStarted handles OSC 133;C: the shell starts a command line. A
// group the poll first saw after the last prompt is the command's own, in
// case the poll ran before this mark arrived; one seen before, such as the
// ssh that carries a remote shell's marks, names nothing. It requires the
// session lock.
func (s *Session) commandStarted(now time.Time) {
	s.cmd = runningCommand{start: now, marked: true}
	if f := s.fg; f.pgid > 0 && !f.shell && f.since.After(s.promptAt) {
		s.cmd.named, s.cmd.name, s.cmd.line = true, f.name, f.line
		s.cmd.agent = s.agent != ""
	}
}

// commandFinished handles OSC 133;D: the shell shows its prompt after a
// command that exited with status exit, when exitKnown. A mark without a
// running command, such as the first prompt's, ends nothing. It requires
// the session lock.
func (s *Session) commandFinished(exit int, exitKnown bool, now time.Time) {
	s.promptAt = now
	if !s.cmd.start.IsZero() {
		s.endCommand(now, exit, exitKnown)
	}
}

// noteForeground follows the commands through a process poll: pgid is the
// foreground group, name its process name and argv its leader's command
// line, read when the group changed; shellPid leads the shell's group. A
// group other than the shell's names the running command, or starts one
// when the shell sent no 133;C. The shell's group back in the foreground
// ends a command no mark started. It requires the session lock and runs
// after the poll updated s.agent.
func (s *Session) noteForeground(shellPid, pgid int, name string, argv []string, pgidChanged bool, now time.Time) {
	if pgid <= 0 {
		return
	}
	shell := pgid == shellPid
	if pgidChanged {
		s.fg = foreground{pgid: pgid, shell: shell, name: name, line: commandLine(argv), since: now}
	}
	switch {
	case s.cmd.start.IsZero():
		if !shell {
			s.cmd = runningCommand{start: now, named: true, name: s.fg.name, line: s.fg.line}
		}
	case shell:
		if !s.cmd.marked {
			s.endCommand(now, 0, false)
			return
		}
	case pgidChanged && !s.cmd.named:
		s.cmd.named, s.cmd.name, s.cmd.line = true, s.fg.name, s.fg.line
	}
	if !s.cmd.start.IsZero() && s.agent != "" {
		s.cmd.agent = true
	}
}

// endCommand records the running command as ended at now. A command an
// agent ran in is not recorded: the agent's own states alert the user. It
// requires the session lock.
func (s *Session) endCommand(now time.Time, exit int, exitKnown bool) {
	c := s.cmd
	s.cmd = runningCommand{}
	if c.agent {
		return
	}
	s.ended = Command{Name: c.name, Line: c.line, Took: now.Sub(c.start), Exit: exit, ExitKnown: exitKnown, Ended: now}
}
