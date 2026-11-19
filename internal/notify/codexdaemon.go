// codexdaemon.go - Codex's shared app-server daemon, which gives every
// Codex session the environment of the terminal that started it: the
// setting that turns it off, and finding it above a command.
package notify

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Codex 0.170 and later run every session in one shared background server,
// `codex app-server --managed-daemon`, unless features.daemon_auto_start is
// false. The server keeps the environment of the terminal that started it,
// so the commands and hooks of a Codex session in any pane get that
// terminal's BUNK_NOTIFY_SESSION and BUNK_NOTIFY_SOCKET: bunk-gui shows
// their documents and states in that pane, and drops them once the pane
// has closed. Without the daemon, each Codex runs its server in its own
// process, with the environment of its own pane.
const codexDaemonFeature = "daemon_auto_start"

// codexSetting is the setting Codex's hooks need in its config.toml.
var codexSetting = &agentSetting{
	file: "config.toml",
	text: codexDaemonFeature + " = false under [features]",
	why:  "so each session reports to its own pane rather than through Codex's shared daemon",
	after: "Once no Codex session runs, stop the daemon with codex app-server daemon stop; " +
		"until then, a new Codex session asks whether to run without it.",
	has: codexDaemonSet,
	add: codexDaemonOff,
}

// codexDaemonSet reports whether config.toml sets
// features.daemon_auto_start, to any value: one set to true is the user's
// choice.
func codexDaemonSet(data []byte) (bool, error) {
	var v map[string]any
	md, err := toml.Decode(string(data), &v)
	if err != nil {
		return false, err
	}
	return md.IsDefined("features", codexDaemonFeature), nil
}

// featuresHeader matches the line of config.toml's [features] header.
var featuresHeader = regexp.MustCompile(`(?m)^[ \t]*\[[ \t]*features[ \t]*\][ \t]*(#[^\r\n]*)?\r?$`)

// errFeatures: config.toml defines its features without a [features]
// header, so no line can be added under one.
var errFeatures = errors.New("its features have no [features] header: add " + codexDaemonFeature + " = false to them")

// codexDaemonOff returns config.toml's text with daemon_auto_start = false
// added: on the line after the [features] header, else, when nothing
// defines features, in a [features] table appended at the end. Text that
// sets it already, to any value, comes back as it is. Text that is not
// TOML is refused, and so are features defined by dotted keys, an inline
// table or sub-tables alone: a [features] header after them is not TOML,
// though this decoder takes it. So is a result that does not read back
// with the setting false.
func codexDaemonOff(data []byte) ([]byte, error) {
	var v map[string]any
	md, err := toml.Decode(string(data), &v)
	if err != nil {
		return nil, err
	}
	if md.IsDefined("features", codexDaemonFeature) {
		return data, nil
	}
	line := codexDaemonFeature + " = false\n"
	var tries []string
	if loc := featuresHeader.FindIndex(data); loc != nil {
		at := loc[1]
		if at < len(data) {
			tries = append(tries, string(data[:at+1])+line+string(data[at+1:])) // after the header's "\n"
		} else {
			tries = append(tries, string(data)+"\n"+line)
		}
	}
	if !md.IsDefined("features") {
		text := string(data)
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		if text != "" {
			text += "\n"
		}
		tries = append(tries, text+"[features]\n"+line)
	}
	for _, out := range tries {
		if codexDaemonOffIn(out) {
			return []byte(out), nil
		}
	}
	return nil, errFeatures
}

// codexDaemonOffIn reports whether text is TOML whose
// features.daemon_auto_start is false.
func codexDaemonOffIn(text string) bool {
	var v struct {
		Features map[string]any `toml:"features"`
	}
	if _, err := toml.Decode(text, &v); err != nil {
		return false
	}
	on, ok := v.Features[codexDaemonFeature].(bool)
	return ok && !on
}

// codexDaemonHint is the line that explains a present request with
// another pane's token, or none, under Codex's shared daemon: it names the
// setting and how to stop the daemon.
func codexDaemonHint(d Dirs) string {
	r, _ := recipe("codex")
	return fmt.Sprintf("Codex runs this command in its shared daemon, which has the BUNK_NOTIFY_SESSION of the pane that started it. "+
		"Set %s in %s (bunk-gui's Codex setup does it), then stop the daemon with codex app-server daemon stop.\n",
		codexSetting.text, d.Short(filepath.Join(d.dir(r), codexSetting.file)))
}

// procDir is where processes are looked up.
const procDir = "/proc"

// codexDaemonDepth bounds how many ancestors codexDaemonAbove looks at.
const codexDaemonDepth = 32

// codexDaemonAbove reports whether process pid, or one of its ancestors,
// is Codex's shared daemon: codex app-server --managed-daemon. proc is
// where processes are looked up, procDir but in tests. Without /proc
// (macOS), or where a sandbox hides the parents, it finds none.
func codexDaemonAbove(proc string, pid int) bool {
	for range codexDaemonDepth {
		if pid <= 1 {
			return false
		}
		argv, ppid, err := procParent(proc, pid)
		if err != nil {
			return false
		}
		if len(argv) > 1 && filepath.Base(argv[0]) == "codex" && argv[1] == "app-server" && slices.Contains(argv, "--managed-daemon") {
			return true
		}
		pid = ppid
	}
	return false
}

// procParent returns the command line of process pid and its parent's
// pid, from proc.
func procParent(proc string, pid int) ([]string, int, error) {
	dir := filepath.Join(proc, strconv.Itoa(pid))
	cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil {
		return nil, 0, err
	}
	stat, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return nil, 0, err
	}
	// The command name in parentheses may hold spaces and parentheses;
	// the state and the parent's pid follow the last ")".
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return nil, 0, errors.New("bad stat")
	}
	f := strings.Fields(string(stat[i+1:]))
	if len(f) < 2 {
		return nil, 0, errors.New("bad stat")
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return nil, 0, err
	}
	return strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00"), ppid, nil
}
