// status.go – foreground process tracking and container/context detection.
//
// For each pane we poll the foreground process group of its PTY every second
// by reading /proc/<shellPid>/stat (field tpgid = terminal foreground PGID),
// then reading /proc/<tpgid>/comm for the process name.
//
// Container context is detected at pane startup by reading the shell process's
// /proc/<pid>/environ.  Toolbox, Distrobox, Podman, and LXD each leave
// distinctive markers in the environment or filesystem.
//
// The status badge is rendered last in each render pass (on top of everything
// else) in the top-right corner of each pane.  It shows:
//
//	⬡ <container-name>  – Toolbox / Podman container
//	▣ <container-name>  – Distrobox container
//	ssh / sudo / su / root – notable foreground process
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
)

// ---------------------------------------------------------------------------
// /proc helpers
// ---------------------------------------------------------------------------

// termFgPGID returns the foreground process group ID (tpgid) of the terminal
// controlling the given process.  It parses field 8 of /proc/<pid>/stat.
// Returns 0 on any error.
func termFgPGID(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	s := string(data)
	// The comm field is wrapped in parentheses and may itself contain parens/
	// spaces; find the LAST ')' so we always skip the full comm field.
	rp := strings.LastIndex(s, ")")
	if rp < 0 {
		return 0
	}
	// After ')': state ppid pgrp session tty_nr tpgid ...
	fields := strings.Fields(s[rp+1:])
	if len(fields) < 6 {
		return 0
	}
	pgid, err := strconv.Atoi(fields[5]) // index 5 = tpgid
	if err != nil || pgid <= 0 {
		return 0
	}
	return pgid
}

// procComm returns the name of the process with the given PID by reading
// /proc/<pid>/comm.  Returns "" on error.
func procComm(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// detectFromEnvSlice checks a slice of "KEY=VALUE" strings for known
// container markers.  Returns "toolbox", "distrobox", "podman", or "".
func detectFromEnvSlice(env []string) string {
	for _, entry := range env {
		switch {
		case strings.HasPrefix(entry, "TOOLBOX_PATH="):
			return "toolbox"
		case strings.HasPrefix(entry, "DISTROBOX_ENTER_PATH="):
			return "distrobox"
		case entry == "container=podman" || strings.HasPrefix(entry, "container=podman"):
			return "podman"
		}
	}
	return ""
}

// detectContainerFromPID returns the container type for the environment of the
// given PID.  It uses three sources in order:
//
//  1. os.Environ() of the running bunk process.  The child shell inherits
//     its environment from bunk via cmd.Env, so this is always correct for
//     the initial pane and any split that spawns a bare shell.
//
//  2. /proc/<pid>/environ – for cases where the child might differ (e.g. an
//     async OSC 176 callback firing for a process spawned differently).
//     This may fail with EACCES if the caller is not the process owner.
//
//  3. Filesystem markers that are world-readable:
//     - /run/.containerenv  – Podman creates this 0-byte file in every container
//     - /dev/lxd/sock       – LXD mounts this guest API socket into every container
//     - /proc/1/cgroup      – world-readable; cgroupsv1 paths contain "/lxc/"
//     - /proc/1/environ     – only readable as root; tried last
func detectContainerFromPID(pid int) string {
	// 1. Own environment (most reliable, no permission issues).
	if ct := detectFromEnvSlice(os.Environ()); ct != "" {
		return ct
	}

	// 2. Target process /proc environ.
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid)); err == nil {
		if ct := detectFromEnvSlice(strings.Split(string(data), "\x00")); ct != "" {
			return ct
		}
	}

	// 3b. Podman fallback: /run/.containerenv is created by Podman in every
	//     container, world-readable, even when container=podman is not
	//     propagated to the child process.
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return "podman"
	}

	// 3c. LXD/LXC/Incus detection (multiple methods, all non-root-friendly).
	// Method 1: guest API socket – LXD mounts /dev/lxd/sock, Incus /dev/incus/sock.
	if _, err := os.Stat("/dev/lxd/sock"); err == nil {
		return "lxd"
	}
	if _, err := os.Stat("/dev/incus/sock"); err == nil {
		return "lxd"
	}
	// Method 2: /run/systemd/container – systemd inside an LXD container
	//           writes "lxc" here; world-readable.
	if data, err := os.ReadFile("/run/systemd/container"); err == nil {
		if strings.TrimSpace(string(data)) == "lxc" {
			return "lxd"
		}
	}
	// Method 3: /proc/1/cgroup – world-readable; cgroupsv1 paths have "/lxc/".
	if cg, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		if strings.Contains(string(cg), "/lxc/") {
			return "lxd"
		}
	}
	// Method 4: /proc/1/environ – only readable as root.
	if pid != 1 {
		if init1, err := os.ReadFile("/proc/1/environ"); err == nil {
			for _, entry := range strings.Split(string(init1), "\x00") {
				if entry == "container=lxc" {
					return "lxd"
				}
			}
		}
	}

	return ""
}

// lxdContainerName returns the LXD container name using two methods:
//
//  1. cgroupsv1: parse "/lxc/<name>/" from /proc/1/cgroup (world-readable).
//  2. Fallback:  hostname — LXD sets the container hostname to its name.
func lxdContainerName() string {
	if cg, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		for _, line := range strings.Split(string(cg), "\n") {
			if idx := strings.Index(line, "/lxc/"); idx >= 0 {
				rest := line[idx+5:] // skip "/lxc/"
				if end := strings.IndexByte(rest, '/'); end > 0 {
					return rest[:end]
				}
				if name := strings.TrimSpace(rest); name != "" {
					return name
				}
			}
		}
	}
	if name, err := os.Hostname(); err == nil {
		return name
	}
	return ""
}

// containerSpawnArgs returns the argv for spawning `shell` inside the named
// container.  Returns nil for an unrecognised containerType or for "lxd"
// (LXD/LXC) where bunk is already running inside the container and new
// panes will naturally inherit the container context without special wrapping.
func containerSpawnArgs(containerID, containerType, shell string) []string {
	switch containerType {
	case "toolbox":
		return []string{"toolbox", "run", "--container", containerID, shell}
	case "distrobox":
		return []string{"distrobox", "enter", "-n", containerID, "--", shell}
	case "podman":
		return []string{"podman", "exec", "-it", containerID, shell}
	case "lxd":
		// bunk is running inside the LXD container; child panes inherit
		// the container context automatically.  No wrapper needed.
		return nil
	}
	return nil
}

// ---------------------------------------------------------------------------
// Goroutine: poll foreground process
// ---------------------------------------------------------------------------

// detectFromCgroup reads /proc/<pid>/cgroup (world-readable) and checks for
// Podman's libpod cgroup path pattern.  This works even when the process is
// owned by a different UID (e.g. rootless Podman with user namespaces), where
// /proc/<pid>/environ would return EACCES.
//
// Returns ("podman", containerID) if a libpod cgroup is found, ("","") otherwise.
// The containerID is the full hex container ID, not the friendly name.
func detectFromCgroup(pid int) (ctype, containerID string) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		// cgroupsv2: "0::<path>"
		// cgroupsv1: "<n>:<subsystem>:<path>"
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}
		path := parts[2]

		// Podman: libpod_payload-<id> or libpod-<id>
		for _, sep := range []string{"libpod_payload-", "libpod-"} {
			if idx := strings.Index(path, sep); idx >= 0 {
				rest := path[idx+len(sep):]
				end := strings.IndexAny(rest, "./\n")
				if end > 0 {
					return "podman", rest[:end]
				}
				if id := strings.TrimSpace(rest); id != "" {
					return "podman", id
				}
			}
		}

		// LXD/LXC: cgroup path contains "/lxc/<name>/"
		if idx := strings.Index(path, "/lxc/"); idx >= 0 {
			rest := path[idx+5:] // skip "/lxc/"
			end := strings.IndexAny(rest, "/\n")
			if end > 0 {
				return "lxd", rest[:end]
			}
			if name := strings.TrimSpace(rest); name != "" {
				return "lxd", name
			}
		}
	}
	return "", ""
}

// podmanContainerName resolves a Podman container ID to its friendly name by
// running "podman inspect".  Returns the ID itself (first 12 chars) on failure.
func podmanContainerName(containerID string) string {
	if containerID == "" {
		return ""
	}
	out, err := exec.Command("podman", "inspect", "--format", "{{.Name}}", containerID).Output()
	if err != nil || len(out) == 0 {
		if len(containerID) > 12 {
			return containerID[:12]
		}
		return containerID
	}
	return strings.TrimSpace(string(out))
}

// detectContainerInfoFromProcEnv returns the
// container type and container name for that specific process.
// Unlike detectContainerFromPID it does NOT check os.Environ() or filesystem
// markers — it only looks at the given process's own environment.
//
// If /proc/<pid>/environ is unreadable (e.g. rootless Podman user namespaces
// where container processes appear as sub-UIDs), falls back to cgroup-based
// detection, which is always world-readable.
//
// If cgroup detection also fails, tries detectExecSession to handle the case
// where the foreground process is a container runtime CLI (podman exec, docker
// exec, kubectl exec) whose own environ is on the host, not inside the
// container.
func detectContainerInfoFromProcEnv(pid int) (ctype, cname string) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err == nil {
		for _, entry := range strings.Split(string(data), "\x00") {
			switch {
			case strings.HasPrefix(entry, "TOOLBOX_PATH="):
				ctype = "toolbox"
			case strings.HasPrefix(entry, "DISTROBOX_ENTER_PATH="):
				ctype = "distrobox"
			case entry == "container=podman" || strings.HasPrefix(entry, "container=podman"):
				ctype = "podman"
			case strings.HasPrefix(entry, "HOSTNAME="):
				cname = entry[9:]
			case strings.HasPrefix(entry, "CONTAINER_ID="):
				if cname == "" {
					cname = entry[13:]
				}
			}
		}
		if ctype != "" {
			L.Debug("detectContainerInfoFromProcEnv: found via environ", "pid", pid, "type", ctype, "name", cname)
			return
		}
	}

	// Cgroup fallback: /proc/<pid>/cgroup is world-readable.
	// Rootless Podman puts container processes under a libpod cgroup path
	// even when their UID differs (making environ unreadable by bunk).
	if ct, cid := detectFromCgroup(pid); ct != "" {
		ctype = ct
		// Resolve the hex container ID to a friendly name via `podman inspect`.
		// Called only when the foreground process changes (~1s polling), so
		// the ~30ms subprocess cost is acceptable.
		cname = podmanContainerName(cid)
		L.Debug("detectContainerInfoFromProcEnv: found via cgroup", "pid", pid, "type", ctype, "name", cname)
		return
	}

	// CLI exec session fallback: handles "podman exec <name>", "docker exec",
	// "kubectl exec" — the CLI process runs on the HOST so its environ and
	// cgroup won't show container markers; we parse the cmdline instead.
	if ct, cn := detectExecSession(pid); ct != "" {
		L.Debug("detectContainerInfoFromProcEnv: found via exec cmdline", "pid", pid, "type", ct, "name", cn)
		return ct, cn
	}

	return
}

// detectExecSession detects when the foreground process is a container runtime
// CLI executing a command inside a container (podman exec, docker exec,
// kubectl exec / oc exec).  It parses /proc/<pid>/cmdline to extract the
// subcommand and container/pod name.
//
// Layout of the relevant subcommands:
//
//	podman exec  [options] CONTAINER COMMAND [ARG...]
//	docker exec  [options] CONTAINER COMMAND [ARG...]
//	kubectl exec [options] POD [-c CONTAINER] -- COMMAND [ARG...]
//	oc exec      [options] POD [-c CONTAINER] -- COMMAND [ARG...]
//	lxc exec     CONTAINER -- COMMAND [ARG...]
func detectExecSession(pid int) (ctype, cname string) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(data) == 0 {
		return "", ""
	}
	// cmdline is NUL-separated; drop trailing NUL.
	args := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	if len(args) < 2 {
		return "", ""
	}

	bin := args[0]
	// Trim path prefix: "/usr/bin/podman" → "podman"
	if idx := strings.LastIndexByte(bin, '/'); idx >= 0 {
		bin = bin[idx+1:]
	}

	// Find the subcommand (first non-flag arg).
	subcmdIdx := -1
	for i := 1; i < len(args); i++ {
		if args[i] != "" && !strings.HasPrefix(args[i], "-") {
			subcmdIdx = i
			break
		}
	}
	if subcmdIdx < 0 {
		return "", ""
	}
	subcmd := args[subcmdIdx]
	rest := args[subcmdIdx+1:]

	switch bin {
	case "podman", "docker":
		if subcmd != "exec" && subcmd != "run" {
			return "", ""
		}
		// First positional arg after flags is the container name.
		name := firstPositionalCLIArg(rest)
		if name == "" {
			return "", ""
		}
		return bin, name

	case "kubectl", "oc":
		if subcmd != "exec" {
			return "", ""
		}
		// kubectl exec [options] POD [-c CONTAINER] -- cmd
		// First positional arg is the pod name.
		name := firstPositionalCLIArg(rest)
		if name == "" {
			return "", ""
		}
		return "kubectl", name

	case "lxc", "incus":
		// lxc exec CONTAINER -- COMMAND [ARG...]
		// incus exec CONTAINER -- COMMAND [ARG...]
		// The subcommand IS the container name when the first non-flag arg
		// is not a known lxc subcommand.  lxc/incus uses positional: first arg is
		// always the subcommand, second is the container.
		if subcmd != "exec" && subcmd != "shell" {
			return "", ""
		}
		// After "exec", first positional arg is the container name.
		name := firstPositionalCLIArg(rest)
		if name == "" {
			return "", ""
		}
		return "lxd", name
	}

	return "", ""
}

// firstPositionalCLIArg returns the first positional (non-flag) argument from
// a CLI arg list, skipping flags and their values.
//
// For flags that take a value (e.g. --user root, -e VAR=val) we use a simple
// heuristic: long flags (--foo) without "=" skip the next arg; short flags
// (-f) that are not in a known set of boolean flags also skip the next arg.
// This handles the common cases without needing per-runtime flag tables.
func firstPositionalCLIArg(args []string) string {
	// Boolean short flags common to podman/docker exec; don't consume next arg.
	boolShort := map[string]bool{
		"-i": true, "-t": true, "-d": true,
		"--interactive": true, "--tty": true, "--detach": true, "--privileged": true,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "" || arg == "--" {
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			return arg // positional found
		}
		if boolShort[arg] {
			continue // boolean flag, no value
		}
		if strings.Contains(arg, "=") {
			continue // --flag=value form, value already consumed
		}
		// Assume flag takes a separate value → skip next arg.
		i++
	}
	return ""
}

// trackFgProcess polls the foreground process group of this pane's PTY once
// per second.  When the process name or container context changes it updates
// the Pane fields and signals the render loop to repaint.
// Stops when done is closed or the pane dies.
func (p *Pane) trackFgProcess(redraw chan struct{}, done chan struct{}) {
	if p.cmd.Process == nil {
		return
	}
	shellPid := p.cmd.Process.Pid
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	var lastName, lastCT, lastCN string
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
		}

		p.mu.Lock()
		dead := p.dead
		baseCT := p.baseContainerType
		baseCN := p.baseContainerID
		p.mu.Unlock()
		if dead {
			return
		}

		name := ""
		var pgid int
		if pgid = termFgPGID(shellPid); pgid > 0 {
			name = procComm(pgid)
		}

		// Detect container context of the current foreground process.
		// If the user ran `podman exec`, `toolbox enter`, etc., the
		// foreground PGID's /proc environ will carry the container markers.
		// Fall back to the pane's startup-detected base context when the
		// foreground process has no container markers.
		ct, cn := "", ""
		if pgid > 0 {
			ct, cn = detectContainerInfoFromProcEnv(pgid)
			L.Debug("trackFgProcess: detection", "pane", p.id, "pgid", pgid, "name", name, "ct", ct, "cn", cn)
		}
		if ct == "" {
			ct, cn = baseCT, baseCN
		}

		if name == lastName && ct == lastCT && cn == lastCN {
			continue
		}
		L.Debug("trackFgProcess: status change", "pane", p.id, "name", name, "container_type", ct, "container_id", cn)
		lastName, lastCT, lastCN = name, ct, cn

		p.mu.Lock()
		p.fgProcess = name
		p.containerType = ct
		p.containerID = cn
		p.mu.Unlock()

		select {
		case redraw <- struct{}{}:
		default:
		}
	}
}

// ---------------------------------------------------------------------------
// Status badge rendering
// ---------------------------------------------------------------------------

// drawAllPaneStatus renders the status badge for every pane in the subtree.
// Must be called AFTER all other drawing so the badge paints on top.
func drawAllPaneStatus(scr tcell.Screen, n *Node, active *Pane) {
	if n.isLeaf() {
		drawPaneStatus(scr, n.pane, n.pane == active)
		return
	}
	drawAllPaneStatus(scr, n.left, active)
	drawAllPaneStatus(scr, n.right, active)
}

// drawPaneStatus draws a compact status badge in the top-right corner of
// pane p.  Nothing is drawn when there is no notable state to display.
//
// Badge format examples:
//
//	[⬡ my-toolbox]          – inside a Toolbox/Podman container
//	[▣ my-box]              – inside a Distrobox container
//	[⬡ my-toolbox · ssh]    – container + SSH session
//	[sudo]                  – sudo or su running
func drawPaneStatus(scr tcell.Screen, p *Pane, isActive bool) {
	p.mu.Lock()
	fgProc := p.fgProcess
	containerID := p.containerID
	containerType := p.containerType
	px, py, pw := p.x, p.y, p.w
	hasSB := p.sb.count > 0
	sbOff := p.sbOff
	p.mu.Unlock()

	// Build badge segments.
	var parts []string
	if containerType != "" {
		icon := "⬡"
		if containerType == "distrobox" {
			icon = "▣"
		}
		label := containerType
		if containerID != "" {
			label = containerID
		}
		parts = append(parts, icon+" "+label)
	}
	switch fgProc {
	case "ssh":
		parts = append(parts, "ssh")
	case "sudo":
		parts = append(parts, "sudo")
	case "su":
		parts = append(parts, "su")
	}
	if len(parts) == 0 {
		return
	}

	badge := "[" + strings.Join(parts, " · ") + "]"

	// Right edge: leave one cell for the scrollbar when it is visible, plus
	// enough room for the scrolled-line counter ("-N") that drawScrollbar
	// overlays on row 0 when sbOff > 0.
	rightEdge := px + pw
	if hasSB {
		rightEdge-- // scrollbar column
		if sbOff > 0 {
			rightEdge -= len(fmt.Sprintf("-%d", sbOff))
		}
	}
	maxW := rightEdge - px
	if maxW < 6 {
		return // pane too narrow for a useful badge
	}

	runes := []rune(badge)
	if len(runes) > maxW {
		runes = runes[:maxW]
	}
	startX := rightEdge - len(runes)
	if startX < px {
		startX = px
	}

	// Badge colour priority: sudo/su (red) > ssh (blue) > container (teal).
	// Coloured backgrounds are used regardless of focus so the indicator is
	// always conspicuous at a glance.
	var style tcell.Style
	switch {
	case fgProc == "sudo" || fgProc == "su":
		style = tcell.StyleDefault.
			Foreground(tcell.ColorWhite).
			Background(tcell.NewRGBColor(180, 30, 30)).
			Bold(true)
	case fgProc == "ssh":
		style = tcell.StyleDefault.
			Foreground(tcell.ColorWhite).
			Background(tcell.NewRGBColor(30, 80, 160)).
			Bold(true)
	case containerType != "":
		style = tcell.StyleDefault.
			Foreground(tcell.ColorWhite).
			Background(tcell.NewRGBColor(20, 120, 100)).
			Bold(true)
	default:
		style = tcell.StyleDefault.
			Foreground(tcell.ColorWhite).
			Background(tcell.NewRGBColor(60, 60, 60))
	}

	for i, ch := range runes {
		if startX+i < px+pw {
			scr.SetContent(startX+i, py, ch, nil, style)
		}
	}
}
