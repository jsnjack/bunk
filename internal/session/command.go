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
//
// A 133;C counts as a mark only after a 133;D since the shell's previous
// 133;C, which shows that its prompts send the end mark. vte.sh puts
// 133;D in PS1 and 133;C in PS0, so a PS1 set after it, as many bashrc
// files do, drops 133;D alone. Such a command starts at its 133;C and ends
// as an unmarked one does, when the shell's group holds the foreground.
//
// While a command runs, Status.Running shows it if it is one the user may
// wait on: not an agent, not a full-screen program, and not a session that
// runs commands of its own, such as ssh or a nested shell.
package session

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	"bunk/internal/vt10x"
)

// Command is a command that ended in a session's foreground, or for
// Status.Running one that runs.
type Command struct {
	Name      string        // process name, such as "tar"; "" when no poll saw it run
	Line      string        // its command line, cleaned and capped; "" when unknown
	Started   time.Time     // when it started; zero for no command
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
	marked bool // OSC 133;C after a 133;D started it, so only the next 133;D ends it
	named  bool // name and line come from a process group bunk saw run in it
	name   string
	line   string
	agent  bool // an agent ran in it: the agent alerts about itself
}

// foreground is the process group the last process poll saw.
type foreground struct {
	pgid    int
	shell   bool // the shell's own group: it waits at its prompt
	name    string
	line    string    // its leader's command line
	since   time.Time // when a poll first saw the group
	carrier bool      // it runs a session of its own (carrier)
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

// commandStarted handles OSC 133;C: the shell starts a command line. It
// marks the command only when a 133;D came since the previous 133;C, so a
// shell whose prompt lost its 133;D cannot leave a command running. A
// group the poll first saw after the last prompt is the command's own, in
// case the poll ran before this mark arrived; one seen before, such as the
// ssh that carries a remote shell's marks, names nothing. It requires the
// session lock.
func (s *Session) commandStarted(now time.Time) {
	s.cmd = runningCommand{start: now, marked: s.promptMarked}
	s.promptMarked = false
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
	s.promptAt, s.promptMarked = now, true
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
		s.fg = foreground{pgid: pgid, shell: shell, name: name, line: commandLine(argv), since: now, carrier: carrier(name, argv)}
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
	s.ended = Command{Name: c.name, Line: c.line, Started: c.start, Took: now.Sub(c.start), Exit: exit, ExitKnown: exitKnown, Ended: now}
}

// shells are the process names of shells; one without a script is a
// nested interactive shell.
var shells = map[string]bool{
	"bash": true, "zsh": true, "fish": true, "sh": true, "dash": true, "ksh": true, "mksh": true,
	"tcsh": true, "csh": true, "nu": true, "xonsh": true, "elvish": true,
}

// carrier reports whether the process name with command line argv runs a
// session of its own, whose commands the user waits on rather than on the
// process: ssh or mosh, a nested shell, or sudo, su or doas starting a
// shell. sudo or a shell running a command or a script is a command.
func carrier(name string, argv []string) bool {
	var args []string
	if len(argv) > 1 {
		args = argv[1:]
	}
	switch {
	case remoteClients[name]:
		return true
	case shells[name]:
		for _, a := range args {
			if a == "-c" || !strings.HasPrefix(a, "-") {
				return false // a command or a script
			}
		}
		return true
	case name == "su":
		return !slices.Contains(args, "-c") && !slices.Contains(args, "--command")
	case name == "sudo" || name == "doas" || name == "run0":
		for i := 0; i < len(args); i++ {
			switch a := args[i]; {
			case a == "-i" || a == "-s" || a == "--login" || a == "--shell":
				return true
			case a == "-u" || a == "-g" || a == "-C" || a == "-D" || a == "-h" || a == "-p" || a == "-r" || a == "-t" || a == "-T" || a == "-U":
				i++ // the option's value
			case a == "--":
			case !strings.HasPrefix(a, "-"):
				cmd := filepath.Base(a)
				return shells[cmd] || cmd == "su"
			}
		}
		return name == "run0" // run0 alone opens a root shell
	}
	return false
}

// running returns the command the shell runs now, for Status.Running, or
// the zero Command when there is none the user would wait on: an agent
// runs in it (agent: the session shows an agent), a full-screen program
// holds the alternate screen, or the foreground is a carrier or a
// container entered from this shell (toolbox enter). A command that the shell's marks
// started after that foreground group appeared, such as one a remote shell
// runs, counts. It requires the session lock.
func (s *Session) running(agent bool) Command {
	c := s.cmd
	if c.start.IsZero() || c.agent || agent || s.Term == nil || s.Term.Mode()&vt10x.ModeAltScreen != 0 {
		return Command{}
	}
	entered := s.ContainerType != "" && (s.ContainerType != s.BaseContainerType || s.ContainerID != s.BaseContainerID)
	inner := c.marked && c.start.After(s.fg.since)
	if (s.fg.carrier || entered) && s.fg.pgid > 0 && !s.fg.shell && !inner {
		return Command{}
	}
	return Command{Name: c.name, Line: c.line, Started: c.start}
}
