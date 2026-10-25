package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
)

// runScript schedules -do actions. Each action is "TIME VERB [ARGS]" with
// TIME measured from startup; actions are separated by ';'. They run on the
// main thread and call the same handlers as the GTK controllers, so a test
// can drive scrolling, selection, clipboard, mouse reports, IME commits and
// tabs without injecting GDK events. Arguments take Go escapes (\n, \x1b).
// "-do @FILE" reads the actions from a file, one per line.
//
//	scroll N | scroll-reset          scrollback view (N > 0: back in history)
//	key [MODS+]NAME                  a key press, e.g. key ctrl+shift+c, key Return
//	type TEXT | preedit TEXT [CUR]   IME commit / preedit
//	imkeys SPEC...                   keys fed through the input method (compose checks)
//	press|motion|release COL ROW [BTN] [shift]
//	click|dclick COL ROW             left click / double click
//	wheel N                          wheel notches (N > 0: down)
//	copy | paste | paste-primary | clip TEXT | print-clip | print-primary
//	new-tab [CMD] | close-tab | next-tab | prev-tab
//	dump FILE [WxH] | maximize | unmaximize | print-size | print-tabs | quit
func (a *App) runScript(script string) {
	if path, ok := strings.CutPrefix(script, "@"); ok { // -do @FILE
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bunk-gui: -do: %v\n", err)
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
		if step == "" {
			continue
		}
		at, rest, _ := strings.Cut(step, " ")
		d, err := time.ParseDuration(at)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bunk-gui: -do %q: %v\n", step, err)
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

func (a *App) doAction(action string) {
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
	p := a.activePane()
	if p == nil && verb != "quit" {
		return
	}
	fmt.Printf("do: %s\n", action)
	switch verb {
	case "scroll":
		p.scrollBy(num(0, 1))
	case "scroll-reset":
		p.snapToLive()
	case "key":
		keyval, state := parseKeySpec(arg)
		if keyval == 0 {
			fmt.Printf("do: unknown key %q\n", arg)
			return
		}
		a.onKey(p, keyval, state)
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
	case "new-tab":
		a.newTab(unescape(arg), p.s.Cwd())
	case "close-tab":
		a.closeTab(p)
	case "next-tab":
		a.tabs.SelectNextPage()
	case "prev-tab":
		a.tabs.SelectPreviousPage()
	case "dump":
		if len(args) > 1 {
			a.cfg.dumpSize = args[1]
		}
		a.writeDump(p, args[0])
	case "maximize":
		a.win.Maximize()
	case "unmaximize":
		a.win.Unmaximize()
	case "print-size":
		p.s.Lock()
		cols, rows := p.s.Term.Size()
		off, count := p.s.ScrollOffset, p.s.Scrollback.Count
		p.s.Unlock()
		fmt.Printf("print-size: tab %d grid %dx%d scroll %d/%d\n", p.id, cols, rows, off, count)
	case "print-tabs":
		for i := 0; i < a.tabs.NPages(); i++ {
			q := a.paneOf(a.tabs.NthPage(i))
			if q != nil {
				fmt.Printf("print-tabs: %d %q selected=%v cwd=%q\n", i, q.title, q == p, q.s.Cwd())
			}
		}
	case "quit":
		a.finish("script")
	default:
		fmt.Printf("do: unknown action %q\n", verb)
	}
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
