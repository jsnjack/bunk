package notify

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"syscall"

	"bunk/internal/notify/wire"
	"bunk/internal/session"
)

// envSSH is set by sshd in every session it starts. On such a host the CLI
// reports in-band even without a token (docs/remote-agent-status.md).
const envSSH = "SSH_CONNECTION"

// maxAncestors bounds the walk up the process tree for the agent and for
// a terminal.
const maxAncestors = 16

var errNoTerminal = errors.New("no terminal among this process and its ancestors")

// openTerminal and describeAgent read the machine for reportInBand; tests
// replace them.
var (
	openTerminal  = findTerminal
	describeAgent = agentContext
)

// reportInBand writes r to the terminal the agent runs in, as an OSC 1337
// user variable that a bunk session reads from its output. The token in r
// may be empty: bunk then accepts the report only while ssh or mosh runs
// in the pane. Host is set only on a host reached over ssh.
func reportInBand(prog string, r wire.Report, env []string, stderr io.Writer) int {
	where := describeAgent()
	r.Agent, r.Repo, r.Branch, r.Cwd = where.Agent, where.Repo, where.Branch, where.Cwd
	if lookupEnv(env, envSSH) != "" {
		r.Host = where.Host
	}
	line, err := r.Encode()
	if err != nil {
		write(stderr, prog+" notify: "+err.Error()+"\n")
		return exitError
	}
	tty, err := openTerminal()
	if err != nil {
		// A hook started away from any terminal: nothing leads to bunk.
		slog.Debug("notify: no terminal for an in-band report", "err", err)
		return 0
	}
	defer func() {
		if err := tty.Close(); err != nil {
			slog.Debug("notify: close terminal", "err", err)
		}
	}()
	// One write: Linux handles a write to a terminal under a lock, so the
	// agent's own writes do not split the sequence.
	seq := wire.Wrap(wire.InBand(line), func(key string) string { return lookupEnv(env, key) })
	if _, err := tty.Write(seq); err != nil {
		if errors.Is(err, syscall.EIO) || errors.Is(err, syscall.ENXIO) {
			return 0 // the terminal has hung up
		}
		write(stderr, prog+" notify: write to terminal: "+err.Error()+"\n")
		return exitError
	}
	return 0
}

// findTerminal opens the controlling terminal, or else the terminal on
// stdin, stdout or stderr of the nearest ancestor that has one: agents may
// start hooks in a new session without a controlling terminal. The
// ancestor search needs /proc.
func findTerminal() (io.WriteCloser, error) {
	if f, err := openTTY("/dev/tty"); err == nil {
		return f, nil
	}
	for _, pid := range ancestors(os.Getpid()) {
		for fd := range 3 {
			path, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", pid, fd))
			if err != nil || !isTerminalPath(path) {
				continue
			}
			if f, err := openTTY(path); err == nil {
				return f, nil
			}
		}
	}
	return nil, errNoTerminal
}

// isTerminalPath reports whether path names a terminal device: a pseudo
// terminal or a console.
func isTerminalPath(path string) bool {
	if !strings.HasPrefix(path, "/dev/pts/") && !strings.HasPrefix(path, "/dev/tty") {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// openTTY opens path for writing without making it the controlling
// terminal of this process.
func openTTY(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return f, nil
}

// agentContext names the agent among this process's ancestors (the hook's
// agent), the host name, the working directory and its git repository and
// branch.
func agentContext() wire.Report {
	var r wire.Report
	for _, pid := range ancestors(os.Getppid()) {
		if a := session.AgentOf(procComm(pid), procArgs(pid)); a != "" {
			r.Agent = a
			break
		}
	}
	if h, err := os.Hostname(); err == nil {
		r.Host = h
	} else {
		slog.Debug("notify: host name", "err", err)
	}
	if wd, err := os.Getwd(); err == nil {
		r.Cwd = wd
		r.Repo, r.Branch = session.GitHead(wd)
	} else {
		slog.Debug("notify: working directory", "err", err)
	}
	return r
}

// ancestors returns pid and its parents, nearest first, up to maxAncestors
// and short of init. It needs /proc and returns only pid without it.
func ancestors(pid int) []int {
	var out []int
	for len(out) < maxAncestors && pid > 1 {
		out = append(out, pid)
		pid = parentPID(pid)
	}
	return out
}

// parentPID returns the parent of pid from /proc/<pid>/stat, or 0.
func parentPID(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	// "pid (comm) state ppid …"; comm may hold spaces and parentheses.
	i := strings.LastIndexByte(string(data), ')')
	if i < 0 {
		return 0
	}
	fields := strings.Fields(string(data[i+1:]))
	if len(fields) < 2 {
		return 0
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0
	}
	return ppid
}

// procComm returns the name of process pid, or "".
func procComm(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// procArgs returns the argv of process pid, or nil.
func procArgs(pid int) []string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(data) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
}
