package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/session"
)

// runScript schedules -do actions. Each action is "TIME VERB [ARGS]" with
// TIME measured from startup, or from keyboard focus with -wait-focus;
// actions are separated by ';'. They run on the
// main thread. Most call the same handlers as the GTK controllers; kbd* and
// ptr* inject real input through the headless test compositor instead.
// Arguments take Go escapes (\n, \x1b). "-do @FILE" reads the actions from
// a file, one per line. A failed or unknown action is listed under "errors"
// in -report.
//
// Terminal input, called directly:
//
//	scroll N | scroll-reset          scrollback view (N > 0: back in history)
//	key [MODS+]NAME                  a key press (window shortcuts first), e.g. key ctrl+shift+c
//	type TEXT | preedit TEXT [CUR]   IME commit / preedit
//	imkeys SPEC...                   keys fed through the input method (compose checks)
//	press|motion|release COL ROW [BTN] [shift]
//	click|dclick COL ROW [BTN]       click / double click
//	wheel N                          wheel notches (N > 0: down)
//	copy | paste | paste-primary | clip TEXT | print-clip | print-primary
//	typing start DUR | typing stop   write 'x' to the focused pane every DUR (latency)
//
// Real input through the compositor (window maximized for ptr*):
//
//	kbd SPEC...                      key combinations, e.g. kbd ctrl+shift+t F1 alt+Left
//	kbd-down KEY... | kbd-up KEY...  hold or release keys, e.g. kbd-down ctrl shift
//	kbd-type TEXT                    text typed key by key
//	ptr-move X Y                     pointer to window coordinates
//	ptr-at TARGET [DX DY]            tab N | tabclose N | newtab | options | closewin |
//	                                 pane ID | cell ID COL ROW | divider r[a|b…] | menu LABEL |
//	                                 shortcut-keys ACTION | shortcut-switch ACTION | button LABEL |
//	                                 pref NAME (tabs-rail, font-size, agent-0-name, agent-add, …)
//	ptr-press [BTN] | ptr-release [BTN] | ptr-click [BTN] [N] | ptr-drag DX DY [STEPS] | ptr-scroll N
//
// Window and tabs:
//
//	new-tab [CMD] | close-tab | next-tab | prev-tab | select-tab N (1-based)
//	split [container] | close-pane | zoom | focus left|right|up|down
//	fake-container TYPE ID           pretend the focused pane runs in a container
//	layout bar|rail | rail open|closed | font +|-|0 | menu open|close | menu-item LABEL | menu-check
//	switcher [filter TEXT|enter|close] | jump
//	status [tab=N|pane=ID] [agent=A] [state=working|needs|idle|failed|none]
//	       [repo=R] [branch=B] [container=C] [ssh=H] [notice=TEXT...]
//	                                 override a pane's status (test hook)
//	status-clear [tab=N|pane=ID]
//	run CMD                          a shell command, e.g. to edit config.toml
//	shortcut-scroll ACTION           scroll the shortcuts window to a row
//	quit-dialog cancel               close the quit dialog
//	dialog-close                     close the open dialog (quit, shortcuts, preferences, About)
//	maximize | unmaximize | quit
//
// Records:
//
//	snap LABEL                       UI state snapshot in -report
//	mark NAME                        frame and allocation counters in -report
//	dump FILE [WxH] | dump-pane ID FILE   a pane's last frame
//	shot FILE [window|chrome|menu]   a widget rendered offscreen through GSK
//	print-size | print-tabs | print-status | print-ui
func (a *App) runScript(script string) {
	if path, ok := strings.CutPrefix(script, "@"); ok { // -do @FILE
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bunk-gui: -do: %v\n", err)
			a.rep.Errors = append(a.rep.Errors, err.Error())
			return
		}
		script = string(b)
	}
	sep := ";"
	if strings.HasPrefix(a.cfg.script, "@") {
		sep = "\n" // a file holds one action per line, so text may contain ';'
	}
	for _, step := range strings.Split(script, sep) {
		step = strings.TrimSpace(step)
		if step == "" || strings.HasPrefix(step, "#") {
			continue
		}
		at, rest, _ := strings.Cut(step, " ")
		d, err := time.ParseDuration(at)
		if err != nil {
			a.reportError("-do %q: %v", step, err)
			continue
		}
		rest = strings.TrimSpace(rest)
		coreglib.TimeoutAdd(uint(d.Milliseconds()), func() bool {
			a.doAction(rest)
			return false
		})
	}
}

func unescape(s string) string {
	if u, err := strconv.Unquote(`"` + strings.ReplaceAll(s, `"`, `\"`) + `"`); err == nil {
		return u
	}
	return s
}

// paneVerbs need a focused pane.
var paneVerbs = map[string]bool{
	"scroll": true, "scroll-reset": true, "type": true, "imkeys": true, "preedit": true,
	"press": true, "motion": true, "release": true, "click": true, "dclick": true, "wheel": true,
	"copy": true, "paste": true, "paste-primary": true, "new-tab": true, "split": true, "close-pane": true,
	"zoom": true, "focus": true, "fake-container": true, "dump": true, "print-size": true,
}

func (a *App) doAction(action string) {
	if a.finished {
		return
	}
	verb, arg, _ := strings.Cut(action, " ")
	arg = strings.TrimSpace(arg)
	args := strings.Fields(arg)
	num := func(i, def int) int {
		if i < len(args) {
			if n, err := strconv.Atoi(args[i]); err == nil {
				return n
			}
		}
		return def
	}
	fnum := func(i int) float64 {
		if i < len(args) {
			if f, err := strconv.ParseFloat(args[i], 64); err == nil {
				return f
			}
		}
		return 0
	}
	fmt.Printf("do: %s\n", action)
	p := a.activePane()
	if p == nil && paneVerbs[verb] {
		a.reportError("%s: no focused pane", action)
		return
	}
	check := func(err error) {
		if err != nil {
			a.reportError("%s: %v", action, err)
		}
	}
	switch verb {
	case "scroll":
		p.scrollBy(num(0, 1))
	case "scroll-reset":
		p.snapToLive()
	case "key":
		keyval, state := parseKeySpec(arg)
		if keyval == 0 {
			a.reportError("%s: unknown key", action)
			return
		}
		if !a.onShortcut(keyval, 0, state) && p != nil {
			a.onKey(p, keyval, state)
		}
	case "type":
		p.imCommit(unescape(arg))
	case "imkeys": // feed keys through the pane's input method, e.g. imkeys ctrl+shift+u e 9 space
		for _, spec := range args {
			keyval, state := parseKeySpec(spec)
			down, up := imFeedC(p.imPtr, nativePtr(p.widget), keyval, uint(state))
			fmt.Printf("imkeys: %s filtered=%d/%d\n", spec, down, up)
		}
	case "preedit":
		text, cur := arg, -1
		if i := strings.LastIndexByte(arg, ' '); i > 0 {
			if n, err := strconv.Atoi(arg[i+1:]); err == nil {
				text, cur = arg[:i], n
			}
		}
		text = unescape(text)
		if cur < 0 {
			cur = len([]rune(text))
		}
		p.setPreedit(text, cur)
	case "press", "motion", "release", "click", "dclick":
		x, y := p.cellCenter(num(0, 0), num(1, 0))
		var state gdk.ModifierType
		if strings.Contains(arg, "shift") {
			state |= gdk.ShiftMask
		}
		btn := uint(num(2, 1))
		switch verb {
		case "press":
			p.mousePress(btn, 1, x, y, state)
		case "motion":
			p.mouseMotion(x, y, state)
		case "release":
			p.mouseRelease(x, y, state)
		case "click":
			p.mousePress(btn, 1, x, y, state)
			p.mouseRelease(x, y, state)
		case "dclick":
			p.mousePress(1, 1, x, y, state)
			p.mouseRelease(x, y, state)
			p.mousePress(1, 2, x, y, state)
			p.mouseRelease(x, y, state)
		}
	case "wheel":
		p.wheel(float64(num(0, 1)), 0)
	case "copy":
		a.copySelection(p)
	case "paste":
		a.paste(p, false)
	case "paste-primary":
		a.paste(p, true)
	case "clip":
		clipboard(false).SetText(unescape(arg))
	case "print-clip", "print-primary":
		cb := clipboard(verb == "print-primary")
		cb.ReadTextAsync(context.Background(), func(res gio.AsyncResulter) {
			text, err := cb.ReadTextFinish(res)
			fmt.Printf("%s: %q err=%v\n", verb, text, err)
		})
	case "typing":
		if len(args) > 0 && args[0] == "stop" {
			a.stopTyping()
			return
		}
		d, err := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(arg, "start")))
		if err != nil || d <= 0 {
			a.reportError("%s: want typing start DURATION", action)
			return
		}
		a.startTyping(d)

	case "kbd", "kbd-down", "kbd-up", "kbd-type", "ptr-move", "ptr-at", "ptr-press", "ptr-release", "ptr-click", "ptr-drag", "ptr-scroll":
		if err := injectOpen(); err != nil {
			a.reportError("%s: %v", action, err)
			return
		}
		check(a.injectAction(verb, arg, args, num, fnum))

	case "new-tab":
		a.newTab(unescape(arg), p.s.Cwd())
	case "close-tab":
		a.closeTab(a.active)
	case "next-tab":
		a.selectOffset(+1)
	case "prev-tab":
		a.selectOffset(-1)
	case "select-tab":
		if i := num(0, 1) - 1; i >= 0 && i < len(a.tabs) {
			a.selectTab(a.tabs[i])
		} else {
			a.reportError("%s: no such tab", action)
		}
	case "split":
		a.splitActive(arg == "container")
	case "close-pane":
		a.closePane(p)
	case "zoom":
		a.active.toggleZoom(p)
	case "focus":
		d, ok := focusDirs[arg]
		if !ok {
			a.reportError("%s: want left, right, up or down", action)
			return
		}
		a.active.moveFocus(d[0], d[1])
	case "fake-container":
		if len(args) != 2 {
			a.reportError("%s: want TYPE ID", action)
			return
		}
		p.s.Lock()
		p.s.ContainerType, p.s.ContainerID = args[0], args[1]
		p.s.Unlock()
	case "layout":
		if arg != "bar" && arg != "rail" {
			a.reportError("%s: want bar or rail", action)
			return
		}
		a.setLayout(arg, a.railOpen)
	case "rail":
		a.setLayout("rail", arg == "open")
	case "font":
		dir, ok := map[string]int{"+": 1, "-": -1, "0": 0}[arg]
		if !ok {
			a.reportError("%s: want +, - or 0", action)
			return
		}
		a.zoom(dir)
	case "menu":
		if arg == "close" {
			a.optionsBtn.Popdown()
		} else {
			a.optionsBtn.Popup()
		}
	case "menu-check":
		// Send all keys to terminal, activated like a click.
		if a.passCheck == nil {
			a.reportError("%s: no check item", action)
			return
		}
		a.passCheck.Activate()
	case "menu-item":
		b, ok := a.menuButtons[arg]
		if !ok {
			a.reportError("%s: no menu item %q", action, arg)
			return
		}
		coreglib.BaseObject(b).Emit("clicked")
	case "switcher":
		sub, text, _ := strings.Cut(arg, " ")
		switch sub {
		case "":
			a.openSwitcher()
		case "filter":
			if a.switcher != nil {
				a.switcher.entry.SetText(unescape(text))
			}
		case "enter":
			if a.switcher != nil {
				a.switcher.pick()
			}
		case "close":
			a.closeSwitcher()
		default:
			a.reportError("%s: unknown switcher action", action)
		}
	case "jump":
		a.jumpNeedingYou()
	case "status", "status-clear":
		a.scriptStatus(verb == "status-clear", arg)
	case "maximize":
		a.win.Maximize()
	case "unmaximize":
		a.win.Unmaximize()
	case "quit":
		a.finish("script")

	case "run":
		// A shell command, synchronously: tests edit config.toml with it.
		if out, err := exec.Command("/bin/sh", "-c", unescape(arg)).CombinedOutput(); err != nil {
			a.reportError("%s: %v: %s", action, err, out)
		}
	case "shortcut-scroll":
		if a.shortcutsWin == nil {
			a.reportError("%s: the shortcuts window is not open", action)
			return
		}
		check(a.shortcutsWin.scrollTo(arg))
	case "quit-dialog":
		if a.quitDialog == nil {
			a.reportError("%s: no quit dialog", action)
			return
		}
		a.quitDialog.Close() // the close response: cancel
	case "dialog-close":
		switch {
		case a.quitDialog != nil:
			a.quitDialog.Close()
		case a.shortcutsWin != nil:
			a.shortcutsWin.dlg.Close()
		case a.prefsWin != nil:
			a.prefsWin.dlg.Close()
		case a.about != nil:
			a.about.Close()
		default:
			a.reportError("%s: no dialog", action)
		}
	case "snap":
		if !a.reportOn() {
			a.reportError("%s: needs -report", action)
			return
		}
		a.snap(arg)
	case "mark":
		if !a.reportOn() {
			a.reportError("%s: needs -report", action)
			return
		}
		a.addMark(arg)
	case "shot":
		check(a.shot(args))
	case "dump":
		if len(args) > 1 {
			a.cfg.dumpSize = args[1]
		}
		a.writeDump(p, args[0])
	case "dump-pane":
		id := num(0, -1)
		if id < 0 || id >= len(a.panes) || a.panes[id] == nil || len(args) < 2 {
			a.reportError("%s: want dump-pane ID FILE", action)
			return
		}
		saved := a.cfg.dumpSize
		a.cfg.dumpSize = "100000x100000"
		a.writeDump(a.panes[id], args[1])
		a.cfg.dumpSize = saved
	case "print-ui":
		fmt.Printf("print-ui: active=%v maximized=%v layout=%s rail_open=%v font=%q text=%s switcher=%v keyboard=%s pointer=%.1f,%.1f last_toast=%q\n",
			a.win.IsActive(), a.win.IsMaximized(), a.layout, a.railOpen, a.fontDesc, a.zoomText(), a.switcher != nil, a.keyboardFocus(), a.ptrX, a.ptrY, a.lastToast)
	case "print-status":
		for _, in := range a.tabInfos() {
			fmt.Printf("print-status: tab %d %s %s %q repo=%q loc=%q needs=%v active=%v panes=%d tooltip=%q\n",
				in.index+1, in.look.dot, in.agent, in.look.label, in.repo, in.loc, in.needs, in.active, len(in.t.panes()), in.tooltip)
		}
	case "print-size":
		p.s.Lock()
		cols, rows := p.s.Term.Size()
		off, count := p.s.ScrollOffset, p.s.Scrollback.Count
		p.s.Unlock()
		fmt.Printf("print-size: pane %d grid %dx%d scroll %d/%d\n", p.id, cols, rows, off, count)
	case "print-tabs":
		for i, t := range a.tabs {
			for _, q := range t.panes() {
				g := viewGeomC(q.view)
				fmt.Printf("print-tabs: tab %d pane %d %q selected=%v focus=%v dimmed=%v zoom=%v grid %dx%d texture %dx%d cwd=%q\n",
					i+1, q.id, q.title, t == a.active, q == t.focus, t.dimmed(q), t.zoom == q, q.cols, q.rows, g.dw, g.dh, q.s.Cwd())
			}
		}
	default:
		a.reportError("unknown action %q", verb)
	}
}

func (a *App) injectAction(verb, arg string, args []string, num func(int, int) int, fnum func(int) float64) error {
	switch verb {
	case "kbd":
		for _, spec := range args {
			if err := a.injectKeys(spec); err != nil {
				return err
			}
		}
	case "kbd-down", "kbd-up":
		for _, spec := range args {
			if err := a.injectKeyState(spec, verb == "kbd-down"); err != nil {
				return err
			}
		}
	case "kbd-type":
		return a.injectText(unescape(arg))
	case "ptr-move":
		return a.pointerTo(fnum(0), fnum(1))
	case "ptr-at":
		var x, y float64
		var err error
		dx, dy := 0.0, 0.0
		if len(args) > 0 && args[0] == "menu" {
			x, y, err = a.menuTarget(strings.TrimSpace(strings.TrimPrefix(arg, "menu")))
		} else {
			// Trailing numbers after the target are an offset.
			targ := args
			if n := len(args); n >= 3 && isNum(args[n-1]) && isNum(args[n-2]) && (args[0] != "cell" || n != 4) {
				dx, _ = strconv.ParseFloat(args[n-2], 64)
				dy, _ = strconv.ParseFloat(args[n-1], 64)
				targ = args[:n-2]
			}
			x, y, err = a.target(targ)
		}
		if err != nil {
			return err
		}
		return a.pointerTo(x+dx, y+dy)
	case "ptr-press", "ptr-release":
		return a.pointerButton(num(0, 1), verb == "ptr-press")
	case "ptr-click":
		for range max(1, num(1, 1)) {
			if err := a.pointerButton(num(0, 1), true); err != nil {
				return err
			}
			if err := a.pointerButton(num(0, 1), false); err != nil {
				return err
			}
		}
	case "ptr-drag":
		return a.pointerDrag(fnum(0), fnum(1), num(2, 10))
	case "ptr-scroll":
		return a.pointerScroll(num(0, 1))
	}
	return nil
}

func isNum(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// parseKeySpec parses "ctrl+shift+c" or "Return" into a GDK keyval and state.
func parseKeySpec(spec string) (uint, gdk.ModifierType) {
	parts := strings.Split(spec, "+")
	var state gdk.ModifierType
	for _, m := range parts[:len(parts)-1] {
		switch strings.ToLower(m) {
		case "ctrl":
			state |= gdk.ControlMask
		case "shift":
			state |= gdk.ShiftMask
		case "alt":
			state |= gdk.AltMask
		}
	}
	name := parts[len(parts)-1]
	if state&gdk.ShiftMask != 0 && len(name) == 1 {
		name = strings.ToUpper(name)
	}
	return gdk.KeyvalFromName(name), state
}

// cellCenter returns the widget coordinates of a cell's centre.
func (p *Pane) cellCenter(col, row int) (float64, float64) {
	g := viewGeomC(p.view)
	if g.scale <= 0 {
		return 0, 0
	}
	return g.bx + (float64(col)+0.5)*float64(p.atlas.CellW)/g.scale, g.by + (float64(row)+0.5)*float64(p.atlas.CellH)/g.scale
}

var focusDirs = map[string][2]int{"left": {-1, 0}, "right": {1, 0}, "up": {0, -1}, "down": {0, 1}}

var stateNames = map[string]session.AgentState{
	"none": session.AgentNone, "working": session.AgentWorking, "idle": session.AgentIdle,
	"needs": session.AgentNeedsYou, "failed": session.AgentFailed,
}

// scriptStatus sets or clears a status override on the focused pane of the
// selected tab (or of tab=N, or pane=ID). The override starts from the
// pane's real status; the chrome and the needs-you ring show it like a real
// report.
func (a *App) scriptStatus(clear bool, arg string) {
	p := a.activePane()
	rest, notice, hasNotice := strings.Cut(arg, "notice=")
	fields := strings.Fields(rest)
	for _, f := range fields {
		k, v, _ := strings.Cut(f, "=")
		n, err := strconv.Atoi(v)
		switch {
		case err != nil:
		case k == "tab" && n >= 1 && n <= len(a.tabs):
			p = a.tabs[n-1].focus
		case k == "pane" && n >= 0 && n < len(a.panes) && a.panes[n] != nil:
			p = a.panes[n]
		case k == "tab" || k == "pane":
			a.reportError("status: no %s %d", k, n)
			return
		}
	}
	if p == nil {
		a.reportError("status: no pane")
		return
	}
	if clear {
		p.statusOverride = nil
	} else {
		st := p.status()
		for _, f := range fields {
			k, v, _ := strings.Cut(f, "=")
			switch k {
			case "agent":
				st.Agent = v
			case "state":
				s, ok := stateNames[v]
				if !ok {
					a.reportError("status: unknown state %q", v)
					return
				}
				st.State = s
			case "repo":
				st.Repo = v
			case "branch":
				st.Branch = v
			case "container":
				st.Container = strings.ReplaceAll(v, ":", " ")
			case "ssh":
				st.SSHHost = v
			}
		}
		if hasNotice {
			st.Notice = unescape(strings.TrimSpace(notice))
		}
		p.statusOverride = &st
	}
	p.queueRender()
	a.refreshChrome()
}

// shot writes a widget's last drawn contents as a PNG: the whole window
// (default), the tab chrome, or the options menu.
func (a *App) shot(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("want shot FILE [window|chrome|menu]")
	}
	var w gtk.Widgetter = a.win
	what := "window"
	if len(args) > 1 {
		what = args[1]
	}
	switch what {
	case "window":
	case "chrome":
		w = a.chrome
	case "menu":
		w = a.optionsBtn.Popover()
	default:
		return fmt.Errorf("unknown widget %q", what)
	}
	take := func() bool {
		pix, wd, ht := shotC(nativePtr(w))
		if pix == nil {
			return false
		}
		a.savePNG(args[0], pix, wd, wd, ht)
		fmt.Printf("shot: %s %dx%d -> %s\n", what, wd, ht, args[0])
		return true
	}
	if take() {
		return nil
	}
	// A widget that changed has no render node until the next frame draws
	// it: try again each frame for up to a second.
	tries := 0
	a.shotsPending++
	coreglib.TimeoutAdd(16, func() bool {
		if a.finished {
			return false
		}
		tries++
		switch {
		case take():
		case tries < 60:
			return true
		default:
			a.reportError("shot %s: %s: nothing drawn", args[0], what)
		}
		a.shotsPending--
		return false
	})
	return nil
}
