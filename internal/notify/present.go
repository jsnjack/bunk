// present.go - "bunk present", which shows a Markdown or HTML document in
// bunk-gui's side panel, or in a web browser where no panel can show it
// (docs/present.md).
package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"unicode"

	"bunk/internal/notify/wire"
	"bunk/internal/present"
)

// presentUsage is the help text; %[1]s is the program name, bunk or
// bunk-gui. Agents read it, so it stays short and exact.
const presentUsage = `usage: %[1]s present FILE[#HEADING-ID]
       %[1]s present --hook AGENT -
       %[1]s present --session-start AGENT -
       %[1]s present skill | status | install AGENT

Shows a file to the user. In a bunk-gui pane it opens in the side panel
of the pane's tab, which reloads it whenever it is saved. In bunk's
terminal UI and outside bunk it opens in the web browser, or in its
default application. On a host reached over ssh, or without a graphical
display, it says that it cannot show the file. It prints one line saying
which happened and exits 0; a missing file or one it cannot show exits 1.
FILE#ID opens the document at the heading whose id is ID, as
docs/plan.md#rollout, so the user reads the part you point to.
In bunk-gui it waits for the page to load, at most a few seconds, and
then lists the problems it had: diagrams or math that do not parse,
script errors, files it could not load, content its policy blocked. Fix
them and run it again to check again.
Agents run it only inside bunk, where BUNK_NOTIFY_SESSION is set, and
never open a browser on their own.

Documents for the user to read:
HTML (.html, .htm), preferred: one page with inline CSS and JavaScript,
interactive where it helps. It is shown from its own directory: other
files it loads must be relative files there; files whose names start
with "." and anything outside the directory are not served, and nothing
loads from the network or from file:// URLs. bunk serves its own assets
at ` + present.AssetsURL + `: bunk-doc.css styles a page as bunk's
documents, and bunk-doc.js draws <pre class="mermaid"> diagrams,
<pre class="bunk-chart"> ECharts options and <pre class="bunk-graphviz">
graphs, typesets <span class="math"> and <div class="math math-display">
TeX, highlights <code class="language-x">, and gives sortable tables,
tabs, steps, callouts, badges and data-bunk-reply buttons that answer
you; bunkDoc.use('d3') loads D3. Colour with the theme's variables, so
the page follows the user's theme: var(--bunk-bg), var(--bunk-fg),
var(--bunk-muted), var(--bunk-accent), var(--bunk-border) and
var(--bunk-code-bg).
Markdown (.md, .markdown, .mdown, .mkd): GitHub-flavoured Markdown with
tables, task lists, highlighted code, ` + "```mermaid, ```chart, ```dot and ```diff" + `
fences, $inline$ and $$display$$ math, > [!NOTE] callouts, [TOC],
<details>, and images by paths relative to the file. The panel reloads
it in place and marks the sections that changed.
Other files show as previews: images, PDF, video, audio, and text with
syntax highlighting.

The user can select text in the panel and send a question about it, or
a review of several comments ("Review of docs/plan.md:"), or press a
reply button. It arrives as the agent's next input:
  About docs/plan.md › Session storage:
  > the selected text
  the user's question
Answer it, and edit the file in place when asked: the panel reloads.

--hook AGENT -   reads the JSON input of a tool hook of claude, codex or
                 copilot from stdin and offers bunk-gui the Markdown and
                 HTML files the tool call wrote whole, not the ones it
                 edited, or shows the plan the agent asks the user to
                 approve. It prints nothing and always exits 0.
--session-start AGENT -
                 in a bunk-gui pane, prints the context of a SessionStart
                 hook that tells the agent about the panel; elsewhere
                 nothing. It always exits 0.
skill            prints the bunk-present skill, which tells agents when
                 and how to present.
status           shows where each agent's copy of the skill is, and
                 whether it is installed, outdated or missing.
install AGENT    writes the skill for claude, codex or copilot.
`

// errNoOpener: no program that opens a file in its default application.
var errNoOpener = errors.New("neither gio nor xdg-open is on PATH")

// RunPresent runs the present command; args exclude the "present" word,
// env is the process environment and stdin is read for "--hook AGENT -".
// It returns the exit status. Messages name the program after os.Args[0],
// so bunk and bunk-gui can both run it.
func RunPresent(args, env []string, stdin io.Reader) int {
	return runPresent(progName(), args, env, stdin, os.Stdout, os.Stderr)
}

func runPresent(prog string, args, env []string, stdin io.Reader, stdout, stderr io.Writer) int {
	help := fmt.Sprintf(presentUsage, prog)
	if len(args) == 0 {
		write(stderr, help)
		return exitError
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		write(stdout, help)
		return 0
	case "--hook":
		presentHook(args[1:], env, stdin)
		return 0
	case "--session-start":
		sessionStart(prog, args[1:], env, stdin, stdout)
		return 0
	case "skill":
		if len(args) == 1 {
			write(stdout, skillText)
			return 0
		}
	case "status":
		if len(args) == 1 {
			write(stdout, skillStatus(prog, envDirs(env)))
			return 0
		}
	case "install":
		if len(args) == 2 {
			return installSkill(prog, args[1], envDirs(env), stdout, stderr)
		}
		write(stderr, fmt.Sprintf("%s present: install takes one of: %s\n", prog, strings.Join(skillAgents, ", ")))
		return exitError
	default:
		if len(args) == 1 && !strings.HasPrefix(args[0], "-") {
			return presentFile(prog, args[0], env, stdout, stderr)
		}
	}
	write(stderr, fmt.Sprintf("%s present: unexpected arguments %q\n\n%s", prog, args, help))
	return exitError
}

// progName names the program after os.Args[0], else "bunk".
func progName() string {
	prog := filepath.Base(os.Args[0])
	if prog == "." || prog == string(filepath.Separator) {
		prog = "bunk"
	}
	return prog
}

// presentFile shows one file: in bunk-gui's side panel when the pane's
// front end shows it, else in the web browser. FILE#ID opens it at the
// heading whose id is ID, unless a file of that very name exists.
func presentFile(prog, arg string, env []string, stdout, stderr io.Writer) int {
	arg, anchor := splitAnchor(arg)
	path, err := filepath.Abs(arg)
	if err != nil {
		write(stderr, fmt.Sprintf("%s present: %s: %v\n", prog, arg, err))
		return exitError
	}
	name := displayPath(path)
	if err := readableFile(path); err != nil {
		write(stderr, fmt.Sprintf("%s present: cannot read %s: %v\n", prog, name, err))
		return exitError
	}
	kind, err := present.Preview(path)
	if err != nil {
		write(stderr, fmt.Sprintf("%s present: cannot show %s: %v\n", prog, name, err))
		return exitError
	}
	if !validPresentPath(path) {
		write(stderr, fmt.Sprintf("%s present: the path of %s is longer than %d bytes or holds control characters\n", prog, name, wire.MaxPath))
		return exitError
	}
	socket, token := lookupEnv(env, EnvSocket), lookupEnv(env, EnvSession)
	if socket != "" && token != "" {
		reply, problems, err := PresentCheck(socket, token, path, anchor)
		if err != nil {
			slog.Debug("present: request", "err", err)
		}
		if reply == PresentShown {
			shown := name
			if anchor != "" {
				shown += "#" + anchor
			}
			write(stdout, "Showing "+shown+" in bunk-gui's side panel.\n"+problemReport(problems))
			return 0
		}
	}
	if why := noBrowser(env, socket); why != "" {
		write(stdout, "Cannot show "+name+" here: "+why+".\n")
		return 0
	}
	page, where := path, "the web browser"
	switch {
	case present.NeedsPage(path, kind):
		if page, err = standalonePage(path, env); err != nil {
			write(stderr, fmt.Sprintf("%s present: %v\n", prog, err))
			return exitError
		}
	case kind != present.KindHTML:
		where = "its default application" // an image viewer, a PDF reader, a player
	}
	if err := openFile(page, env); err != nil {
		if errors.Is(err, errNoOpener) {
			write(stdout, "Cannot show "+name+" here: "+err.Error()+".\n")
			return 0
		}
		write(stderr, fmt.Sprintf("%s present: open %s: %v\n", prog, name, err))
		return exitError
	}
	write(stdout, "Opened "+name+" in "+where+".\n")
	return 0
}

// splitAnchor splits FILE#ID into the file and the heading's id, unless a
// file named FILE#ID exists or ID is not a plausible id.
func splitAnchor(arg string) (string, string) {
	i := strings.LastIndexByte(arg, '#')
	if i <= 0 || i == len(arg)-1 {
		return arg, ""
	}
	if _, err := os.Stat(arg); err == nil {
		return arg, ""
	}
	anchor := arg[i+1:]
	if len(anchor) > wire.MaxAnchor || strings.ContainsAny(anchor, "/ \t") || strings.ContainsFunc(anchor, unicode.IsControl) {
		return arg, ""
	}
	return arg[:i], anchor
}

// problemReport tells the agent what was wrong with the page bunk-gui
// showed, or is "" when nothing was.
func problemReport(problems []string) string {
	if len(problems) == 0 {
		return ""
	}
	var b strings.Builder
	if len(problems) == 1 {
		b.WriteString("The page has a problem:\n")
	} else {
		fmt.Fprintf(&b, "The page has %d problems:\n", len(problems))
	}
	for _, p := range problems {
		b.WriteString("- " + p + "\n")
	}
	b.WriteString("Fix them in the file: the panel reloads it. Run this command again to check the page again.\n")
	return b.String()
}

// displayPath writes path relative to the working directory when it lies
// inside it, for messages.
func displayPath(path string) string {
	wd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(wd, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return rel
}

// readableFile returns why path cannot be presented: it is missing,
// unreadable or not a regular file.
func readableFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return pe.Err
		}
		return err
	}
	defer func() {
		if err := f.Close(); err != nil {
			slog.Debug("present: close", "err", err)
		}
	}()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	return nil
}

// noBrowser returns why no browser can show a file here, or "". socket is
// the pane's notify socket: without one, SSH_CONNECTION means a host
// reached over ssh, whose browser the user does not see.
func noBrowser(env []string, socket string) string {
	if runtime.GOOS == "darwin" {
		return ""
	}
	switch {
	case socket == "" && lookupEnv(env, envSSH) != "":
		return "this host is reached over ssh, where neither bunk-gui nor the user's browser runs"
	case lookupEnv(env, "WAYLAND_DISPLAY") == "" && lookupEnv(env, "DISPLAY") == "":
		return "there is no graphical display (WAYLAND_DISPLAY and DISPLAY are unset)"
	}
	return ""
}

// standalonePage writes the page a browser shows for the file at path, under
// bunk's cache directory (present.WriteStandalone), and returns its path.
func standalonePage(path string, env []string) (string, error) {
	cache := lookupEnv(env, "XDG_CACHE_HOME")
	if !filepath.IsAbs(cache) {
		home := lookupEnv(env, "HOME")
		if home == "" {
			home = UserDirs().Home
		}
		if home == "" {
			return "", errors.New("no cache directory: XDG_CACHE_HOME and HOME are unset")
		}
		cache = filepath.Join(home, ".cache")
	}
	page, err := present.WriteStandalone(path, filepath.Join(cache, present.CacheDir), present.DefaultTheme())
	if err != nil {
		return "", fmt.Errorf("render %s: %w", path, err)
	}
	return page, nil
}

// openFile opens path in the desktop's default application for it, with
// gio open, else xdg-open (open on macOS). The opener runs detached, in a
// session of its own, with its output discarded. Tests replace it.
var openFile = func(path string, env []string) error {
	argv := openerArgv(env)
	if argv == nil {
		return errNoOpener
	}
	c := exec.Command(argv[0], append(argv[1:], path)...)
	c.Env = env
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return fmt.Errorf("start %s: %w", argv[0], err)
	}
	if err := c.Process.Release(); err != nil {
		slog.Debug("present: release opener", "err", err)
	}
	return nil
}

// openerArgv returns the command that opens a file, without the file, or
// nil when none is on env's PATH.
func openerArgv(env []string) []string {
	if runtime.GOOS == "darwin" {
		return []string{"/usr/bin/open"}
	}
	if gio := lookPath(env, "gio"); gio != "" {
		return []string{gio, "open"}
	}
	if xdg := lookPath(env, "xdg-open"); xdg != "" {
		return []string{xdg}
	}
	return nil
}

// lookPath returns the executable name in a directory of env's PATH, or "".
func lookPath(env []string, name string) string {
	for _, dir := range filepath.SplitList(lookupEnv(env, "PATH")) {
		if dir == "" {
			continue
		}
		path := filepath.Join(dir, name)
		if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return path
		}
	}
	return ""
}

// presentHook runs "--hook AGENT -": it offers the front end each Markdown
// and HTML file that the tool call the hook's input describes wrote whole,
// or shows the plan the call asks the user to approve. It never prints and
// never fails, so no agent shows an error for it.
func presentHook(args, env []string, stdin io.Reader) {
	if len(args) != 2 || args[1] != "-" || !slices.Contains(skillAgents, args[0]) {
		slog.Debug("present: bad hook arguments", "args", args)
		return
	}
	socket, token := lookupEnv(env, EnvSocket), lookupEnv(env, EnvSession)
	if socket == "" || token == "" {
		return // no bunk front end to show it; in-band reports carry no requests
	}
	paths, show := hookFiles(args[0], readStdin(stdin), envDirs(env))
	for _, path := range paths {
		reply, err := Present(socket, token, path, !show)
		if err != nil {
			slog.Debug("present: hook request", "err", err)
			return
		}
		slog.Debug("present: hook request", "path", path, "reply", reply)
	}
}

// sessionHint is the context the SessionStart hook gives an agent in a
// bunk-gui pane, %[1]s the program. Agents cannot see the environment, so
// without it they cannot tell that the bunk-present skill applies.
const sessionHint = "This terminal is bunk-gui, which shows documents in a panel beside you. " +
	"When you write a plan, spec, design, report, comparison or explanation for the user to read " +
	"(not code or logs), make it one self-contained HTML page, interactive where that helps " +
	"(tabs, sortable tables, charts, diagrams, buttons that reply to you), or Markdown for plain prose, " +
	"and run `%[1]s present FILE` to show it there; it lists the page's problems for you to fix, " +
	"and FILE#heading-id opens it at a heading. " +
	"Colour pages with var(--bunk-bg), var(--bunk-fg) and the other --bunk- variables, not fixed colours, " +
	"so they follow the user's theme. " +
	"The panel reloads the file whenever you save it. " +
	"The user's questions, reviews and replies arrive as your input, starting \"About FILE › Section:\" or \"Review of FILE:\". " +
	"Load the bunk-present skill before you write the page: it has bunk's stylesheet and components, " +
	"Mermaid, KaTeX, ECharts, Graphviz and D3, all without the network."

// sessionStart runs "--session-start AGENT -", a SessionStart hook: in a
// bunk-gui pane, which has the panel, it prints sessionHint as the context
// the agent reads from the hook; elsewhere, bunk's terminal UI and hosts
// reached over ssh included, nothing.
func sessionStart(prog string, args, env []string, stdin io.Reader, stdout io.Writer) {
	if len(args) != 2 || args[1] != "-" || !slices.Contains(skillAgents, args[0]) {
		slog.Debug("present: bad session-start arguments", "args", args)
		return
	}
	readStdin(stdin) // the agent writes its input; take it all before exiting
	if lookupEnv(env, EnvSocket) == "" || lookupEnv(env, EnvSession) == "" || lookupEnv(env, "TERM_PROGRAM") != "bunk-gui" {
		return
	}
	hint := fmt.Sprintf(sessionHint, prog)
	var out any = map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": "SessionStart", "additionalContext": hint}}
	if args[0] == "copilot" {
		out = map[string]string{"additionalContext": hint}
	}
	data, err := json.Marshal(out)
	if err != nil {
		slog.Debug("present: session hint", "err", err)
		return
	}
	write(stdout, string(data)+"\n")
}

// planFile returns the plan file of a tool call that asks the user to
// approve a plan, or "".
type planFile func(in hookInput, d Dirs) string

// copilotPlanTool is Copilot CLI's tool that asks the user to approve the
// plan of plan mode.
const copilotPlanTool = "exit_plan_mode"

// planTools are the tools that ask the user to approve a plan, by agent.
// Their PreToolUse hooks show the plan with the panel open, whatever
// [present] auto says: the user reads it now.
var planTools = map[string]map[string]planFile{
	// Claude Code adds the plan and its file to ExitPlanMode's input.
	"claude": {exitPlanMode: func(in hookInput, _ Dirs) string { return parseToolArgs(in.raw()).str("planFilePath") }},
	// Copilot CLI keeps the plan in plan.md of the session's state
	// directory, which the session's ID names.
	"copilot": {copilotPlanTool: func(in hookInput, d Dirs) string {
		id := in.SessionID
		if id == "" || id != filepath.Base(id) || strings.HasPrefix(id, ".") {
			return ""
		}
		r, _ := recipe("copilot")
		return filepath.Join(d.dir(r), "session-state", id, "plan.md")
	}},
}

// writeTool returns the paths, as the tool's arguments give them, of the
// files one tool call wrote whole.
type writeTool func(args toolArgs) []string

// writeTools are the tools of each agent that write whole files, whose
// PostToolUse hooks run "present --hook", by tool name (hooks.go matches
// them).
var writeTools = map[string]map[string]writeTool{
	"claude": {"Write": argPath("file_path")},
	// Codex reports every file edit as apply_patch, with the patch in
	// tool_input.command.
	"codex": {"apply_patch": patchArg},
	// Copilot CLI's toolArgs are the model's arguments: an object or its
	// JSON text, or for apply_patch the patch itself.
	"copilot": {
		"create":      argPath("path"),
		"apply_patch": patchArg,
	},
}

// toolArgs is a tool call's arguments: a JSON object, or text when they
// are not one.
type toolArgs struct {
	obj  map[string]json.RawMessage
	text string
}

// parseToolArgs reads arguments that are an object, the JSON text of an
// object, or other text.
func parseToolArgs(raw json.RawMessage) toolArgs {
	var a toolArgs
	if json.Unmarshal(raw, &a.obj) == nil {
		return a
	}
	if json.Unmarshal(raw, &a.text) == nil && json.Unmarshal([]byte(a.text), &a.obj) == nil {
		a.text = ""
	}
	return a
}

// str returns the string argument key, or "".
func (a toolArgs) str(key string) string {
	var s string
	if json.Unmarshal(a.obj[key], &s) != nil {
		return ""
	}
	return s
}

// argPath reads the path a tool writes from its argument key.
func argPath(key string) writeTool {
	return func(a toolArgs) []string {
		if p := a.str(key); p != "" {
			return []string{p}
		}
		return nil
	}
}

// patchArg reads the files an apply_patch call added from its patch: the
// arguments' text, or their "command" as a string or an argv, or their
// "input".
func patchArg(a toolArgs) []string {
	if a.text != "" {
		return patchFiles(a.text)
	}
	if s := a.str("command"); s != "" {
		return patchFiles(s)
	}
	var argv []string
	if json.Unmarshal(a.obj["command"], &argv) == nil {
		return patchFiles(strings.Join(argv, "\n"))
	}
	return patchFiles(a.str("input"))
}

// patchAdd starts the line of an apply_patch patch that adds a file.
const patchAdd = "*** Add File: "

// patchFiles returns the files a patch adds, in the patch's order. Files
// it updates, moves or deletes do not count.
func patchFiles(patch string) []string {
	var out []string
	for line := range strings.Lines(patch) {
		if name, ok := strings.CutPrefix(line, patchAdd); ok && strings.TrimSpace(name) != "" {
			out = append(out, strings.TrimSpace(name))
		}
	}
	return out
}

// hookInput is the input of an agent's tool hook: Claude Code's and Codex's
// names, or Copilot CLI's.
type hookInput struct {
	Cwd        string          `json:"cwd"`
	WorkingDir string          `json:"workingDirectory"`
	Tool       string          `json:"tool_name"`
	ToolC      string          `json:"toolName"`
	Input      json.RawMessage `json:"tool_input"`
	Args       json.RawMessage `json:"toolArgs"`
	SessionID  string          `json:"sessionId"`
}

// raw is the tool call's arguments.
func (in hookInput) raw() json.RawMessage {
	if in.Input != nil {
		return in.Input
	}
	return in.Args
}

// hookFiles returns the absolute paths of the Markdown and HTML files that
// the tool call in text, the input of agent's tool hook, wrote whole and
// that are regular files now, each once, in the call's order; or the plan
// it asks the user to approve, with show set. Relative paths are taken
// from the input's working directory; d places agents' directories.
func hookFiles(agent, text string, d Dirs) (out []string, show bool) {
	var in hookInput
	if json.Unmarshal([]byte(strings.TrimSpace(text)), &in) != nil {
		return nil, false
	}
	tool, cwd := either(in.Tool, in.ToolC), either(in.Cwd, in.WorkingDir)
	var paths []string
	if plan, ok := planTools[agent][tool]; ok {
		paths, show = []string{plan(in, d)}, true
	} else if read, ok := writeTools[agent][tool]; ok {
		paths = read(parseToolArgs(in.raw()))
	} else {
		return nil, false
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			if !filepath.IsAbs(cwd) {
				continue
			}
			p = filepath.Join(cwd, p)
		}
		p = filepath.Clean(p)
		if !present.KindOf(p).Document() || !validPresentPath(p) || slices.Contains(out, p) {
			continue
		}
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		out = append(out, p)
	}
	return out, show
}

// either returns a, or b when a is empty.
func either(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
