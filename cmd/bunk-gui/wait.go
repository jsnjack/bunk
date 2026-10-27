package main

// Script waits (action "wait", see script.go): a step that holds the
// script until conditions on the UI hold, so scenarios run as fast as the
// window allows instead of on guessed timelines.
//
//	wait COND... [timeout=DUR]
//
// All conditions must hold at one check. Checks run on the main thread
// every 10 ms; the default timeout is 10 s. A wait that times out records
// an error naming each condition that failed with the value last seen,
// adds a snapshot labelled "wait-timeout", and ends the run.
//
// Conditions:
//
//	PATH OP VALUE      a field of the UI snapshot (the "snap" record)
//	frames N           N pane frames rendered since the step before the wait
//	                   started (scroll 3; wait frames 1)
//	idle DUR           no frame rendered or painted for DUR
//	input              every key and button event injected (kbd*, ptr-click…)
//	                   reached the window or the options menu
//	clipboard TEXT     the clipboard holds TEXT (read asynchronously)
//	primary TEXT       the primary selection holds TEXT
//
// PATH is dot-separated JSON names of the snapshot (report.go), with these
// roots besides its own fields:
//
//	tab.N              tab N (1-based), e.g. tab.2.state=needs
//	pane.ID            the pane with that id, e.g. pane.0.ring=true
//	focused            the selected tab's focused pane
//	run.FIELD          frames, clock_frames, reloads, toasts, opened, windows,
//	                   errors, notifications
//	res.FIELD          fds, goroutines, children, zombies, panes, textures (as "mark")
//
// A numeric segment indexes an array (0-based); another segment picks the
// element whose "action" is that name (shortcuts.rows.zoom.enabled); "#"
// is the length of an array, map or string, e.g. tabs.#=3. A pane also has
// "screen": its rows joined with newlines, trailing blank rows dropped.
// Pane fields made for waits: line (the cursor's row, "$" at an idle sh
// prompt), settled (drawn at its size and font, no frame or reflow
// pending) and busy (the foreground process when not an idle shell).
//
// OP is = != ~ (contains) !~ > >= < <=. On an array, = and ~ hold when any
// element matches, != and !~ when none does. A missing field (no such tab,
// a closed find bar) never matches; a nil one equals "null". VALUE may be
// double-quoted with Go escapes ("a b\n"); a numeric VALUE may be
// @MARK.FIELD[+N|-N], a counter of an earlier mark.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"

	"bunk/internal/leakcheck"
)

const (
	waitTick    = 10 * time.Millisecond
	waitDefault = 10 * time.Second
)

type condKind int

const (
	condPath condKind = iota
	condFrames
	condIdle
	condInput
	condClip
)

type waitCond struct {
	src      string
	kind     condKind
	path     string
	op       string
	want     string
	n        int
	d        time.Duration
	primary  bool
	got      string // value at the last check, for the timeout message
	clipText *string
	clipBusy bool
}

// splitQuoted splits s at spaces outside double quotes; quoted parts are
// unquoted with Go escapes and joined to the text around them.
func splitQuoted(s string) ([]string, error) {
	var out []string
	var b strings.Builder
	in := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			j := i + 1
			for ; j < len(s) && s[j] != '"'; j++ {
				if s[j] == '\\' {
					j++
				}
			}
			if j >= len(s) {
				return nil, fmt.Errorf("unterminated quote in %q", s)
			}
			u, err := strconv.Unquote(s[i : j+1])
			if err != nil {
				return nil, fmt.Errorf("%s: %v", s[i:j+1], err)
			}
			b.WriteString(u)
			in = true
			i = j
		case c == ' ' || c == '\t':
			if in {
				out = append(out, b.String())
				b.Reset()
				in = false
			}
		default:
			b.WriteByte(c)
			in = true
		}
	}
	if in {
		out = append(out, b.String())
	}
	return out, nil
}

// parseWait parses the arguments of a wait action.
func parseWait(arg string) ([]*waitCond, time.Duration, error) {
	toks, err := splitQuoted(arg)
	if err != nil {
		return nil, 0, fmt.Errorf("wait: %w", err)
	}
	timeout := waitDefault
	var conds []*waitCond
	for i := 0; i < len(toks); i++ {
		tok := toks[i]
		next := func() (string, error) {
			if i+1 >= len(toks) {
				return "", fmt.Errorf("%s needs a value", tok)
			}
			i++
			return toks[i], nil
		}
		switch {
		case strings.HasPrefix(tok, "timeout="):
			if timeout, err = time.ParseDuration(strings.TrimPrefix(tok, "timeout=")); err != nil {
				return nil, 0, fmt.Errorf("wait %s: %w", tok, err)
			}
		case tok == "frames":
			v, err := next()
			if err != nil {
				return nil, 0, err
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, 0, fmt.Errorf("frames %q: %v", v, err)
			}
			conds = append(conds, &waitCond{src: "frames " + v, kind: condFrames, n: n})
		case tok == "idle":
			v, err := next()
			if err != nil {
				return nil, 0, err
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				return nil, 0, fmt.Errorf("wait idle: %w", err)
			}
			conds = append(conds, &waitCond{src: "idle " + v, kind: condIdle, d: d})
		case tok == "input":
			conds = append(conds, &waitCond{src: "input", kind: condInput})
		case tok == "clipboard" || tok == "primary":
			v, err := next()
			if err != nil {
				return nil, 0, err
			}
			conds = append(conds, &waitCond{src: fmt.Sprintf("%s %q", tok, v), kind: condClip, want: v, primary: tok == "primary"})
		default:
			c := &waitCond{src: tok, kind: condPath}
			at := strings.IndexAny(tok, "!=~<>")
			if at <= 0 {
				return nil, 0, fmt.Errorf("condition %q: want PATH OP VALUE", tok)
			}
			c.op = tok[at : at+1]
			if rest := tok[at+1:]; (c.op == "!" && rest != "" && (rest[0] == '=' || rest[0] == '~')) ||
				((c.op == "<" || c.op == ">") && strings.HasPrefix(rest, "=")) {
				c.op = tok[at : at+2]
			}
			if c.op == "!" {
				return nil, 0, fmt.Errorf("condition %q: want != or !~", tok)
			}
			c.path, c.want = tok[:at], tok[at+len(c.op):]
			conds = append(conds, c)
		}
	}
	if len(conds) == 0 {
		return nil, 0, errors.New("wait: no condition")
	}
	return conds, timeout, nil
}

// waiter is one wait step in progress.
type waiter struct {
	a       *App
	src     string
	conds   []*waitCond
	start   time.Time
	timeout time.Duration
	// Frame counters at the start, and when they last changed (idle).
	frames0, frames, clock int
	quietSince             time.Time
	quietChecks            int         // checks in a row that saw no new frame
	snap                   *uiSnapshot // this check's snapshot, made on demand
	res                    *markReport
}

// startWait runs a wait step and calls next when its conditions hold.
func (a *App) startWait(arg string, next func()) {
	conds, timeout, err := parseWait(arg)
	if err != nil {
		a.reportError("wait %s: %v", arg, err)
		next()
		return
	}
	now := time.Now()
	w := &waiter{a: a, src: arg, conds: conds, start: now, timeout: timeout,
		frames0: a.prevStepFrames, frames: a.st.frames, clock: a.st.clockFrames, quietSince: now}
	if w.check() {
		next()
		return
	}
	// Each check arms the next waitTick after it ends. A repeating timeout
	// is due again from the start of the loop iteration that ran it, so a
	// check slower than waitTick (snapshots under --alloc-probe) would run
	// back to back and starve the idle callbacks that bring status changes
	// to the panes.
	var arm func()
	arm = func() {
		coreglib.TimeoutAdd(uint(waitTick.Milliseconds()), func() bool {
			switch {
			case a.finished:
			case w.check():
				next()
			case time.Since(w.start) >= w.timeout:
				w.fail()
			default:
				arm()
			}
			return false
		})
	}
	arm()
}

// check evaluates every condition; all are evaluated so the timeout
// message shows each one's last value.
func (w *waiter) check() bool {
	a := w.a
	if a.st.frames != w.frames || a.st.clockFrames != w.clock {
		w.frames, w.clock, w.quietSince, w.quietChecks = a.st.frames, a.st.clockFrames, time.Now(), 0
	} else {
		w.quietChecks++
	}
	w.snap, w.res = nil, nil
	ok := true
	for _, c := range w.conds {
		if !w.eval(c) {
			ok = false
		}
	}
	w.snap, w.res = nil, nil
	return ok
}

func (w *waiter) eval(c *waitCond) bool {
	a := w.a
	switch c.kind {
	case condFrames:
		n := a.st.frames - w.frames0
		c.got = strconv.Itoa(n)
		return n >= c.n
	case condIdle:
		// Three checks in a row as well: after the main loop stalled, the
		// first check runs before a frame that was due.
		q := time.Since(w.quietSince)
		c.got = fmt.Sprintf("quiet for %v", q.Round(time.Millisecond))
		return q >= c.d && w.quietChecks >= 3
	case condInput:
		seen := inputSeen()
		c.got = fmt.Sprintf("%d of %d events seen", seen, a.inputSent)
		return seen >= a.inputSent
	case condClip:
		if !c.clipBusy {
			c.clipBusy = true
			cb := clipboard(c.primary)
			cb.ReadTextAsync(context.Background(), func(res gio.AsyncResulter) {
				text, err := cb.ReadTextFinish(res)
				if err != nil {
					text = "<" + err.Error() + ">"
				}
				c.clipText, c.clipBusy = &text, false
			})
		}
		if c.clipText == nil {
			c.got = "<unread>"
			return false
		}
		c.got = strconv.Quote(*c.clipText)
		return *c.clipText == c.want
	}
	v, err := w.resolve(c.path)
	if err != nil {
		c.got = err.Error()
		return false
	}
	want := c.want
	if strings.HasPrefix(want, "@") {
		n, err := a.markValue(want[1:])
		if err != nil {
			c.got = err.Error()
			return false
		}
		want = strconv.FormatFloat(n, 'g', -1, 64)
	}
	ok, got := compareValue(v, c.op, want)
	c.got = got
	return ok
}

// fail ends the run: the script cannot go on in a state it did not reach.
func (w *waiter) fail() {
	a := w.a
	var why []string
	for _, c := range w.conds {
		if !w.eval(c) {
			why = append(why, fmt.Sprintf("%s (got %s)", c.src, c.got))
		}
	}
	a.reportError("wait %s: timed out after %v: %s", w.src, w.timeout, strings.Join(why, "; "))
	if a.reportOn() {
		a.rep.Snapshots = append(a.rep.Snapshots, a.snapshot("wait-timeout"))
	}
	a.finish("wait timeout")
}

// runState is the "run" root of wait paths: counters of the whole run.
type runState struct {
	Frames      int      `json:"frames"`
	ClockFrames int      `json:"clock_frames"`
	KeyRenders  int      `json:"key_renders"`
	Reloads     int      `json:"reloads"`
	Toasts      []string `json:"toasts"`
	Opened      []string `json:"opened"`
	Windows     []string `json:"windows"`
	Errors      []string `json:"errors"`
	// Notifications are the desktop notification calls so far.
	Notifications []alertRecord `json:"notifications"`
}

// resolve finds the value a condition path names.
func (w *waiter) resolve(path string) (reflect.Value, error) {
	a := w.a
	segs := strings.Split(path, ".")
	snap := func() *uiSnapshot {
		if w.snap == nil {
			w.snap = a.snapshot("wait")
		}
		return w.snap
	}
	var v reflect.Value
	switch segs[0] {
	case "tab":
		if len(segs) < 2 {
			return v, errors.New("tab.N: no N")
		}
		n, err := strconv.Atoi(segs[1])
		s := snap()
		if err != nil || n < 1 || n > len(s.Tabs) {
			return v, fmt.Errorf("no tab %s (%d tabs)", segs[1], len(s.Tabs))
		}
		v, segs = reflect.ValueOf(s.Tabs[n-1]), segs[2:]
	case "pane":
		if len(segs) < 2 {
			return v, errors.New("pane.ID: no ID")
		}
		id, err := strconv.Atoi(segs[1])
		found := false
		for _, p := range snap().Panes {
			if err == nil && p.ID == id {
				v, found = reflect.ValueOf(p), true
			}
		}
		if !found {
			return v, fmt.Errorf("no pane %s", segs[1])
		}
		segs = segs[2:]
	case "focused":
		s := snap()
		found := false
		for _, p := range s.Panes {
			if p.ID == s.FocusedPane {
				v, found = reflect.ValueOf(p), true
			}
		}
		if !found {
			return v, errors.New("no focused pane")
		}
		segs = segs[1:]
	case "run":
		v, segs = reflect.ValueOf(runState{Frames: a.st.frames, ClockFrames: a.st.clockFrames, KeyRenders: len(a.st.keyRender),
			Reloads: a.rep.Reloads, Toasts: a.toastLog, Opened: a.rep.Opened, Windows: a.rep.Windows, Errors: a.rep.Errors,
			Notifications: a.rep.Notifications}), segs[1:]
	case "res":
		if w.res == nil {
			r := a.resources()
			w.res = &r
		}
		v, segs = reflect.ValueOf(*w.res), segs[1:]
	default:
		v = reflect.ValueOf(snap()).Elem()
	}
	return walkValue(v, segs)
}

// walkValue follows JSON names, indexes, map keys and "#" from v.
func walkValue(v reflect.Value, segs []string) (reflect.Value, error) {
	for i, seg := range segs {
		for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
			if v.IsNil() {
				return v, fmt.Errorf("%s is null", strings.Join(segs[:i], "."))
			}
			v = v.Elem()
		}
		if seg == "#" {
			switch v.Kind() {
			case reflect.Slice, reflect.Array, reflect.Map, reflect.String:
				v = reflect.ValueOf(v.Len())
				continue
			}
			return v, fmt.Errorf("%s has no length", strings.Join(segs[:i], "."))
		}
		switch v.Kind() {
		case reflect.Struct:
			if p, ok := v.Interface().(paneReport); ok && seg == "screen" {
				v = reflect.ValueOf(strings.TrimRight(strings.Join(p.Text, "\n"), "\n"))
				continue
			}
			f, ok := jsonField(v, seg)
			if !ok {
				return v, fmt.Errorf("no field %s", strings.Join(segs[:i+1], "."))
			}
			v = f
		case reflect.Slice, reflect.Array:
			n, err := strconv.Atoi(seg)
			if err != nil && v.Type().Elem().Kind() == reflect.Struct {
				// Rows with an "action" field are found by it:
				// shortcuts.rows.switcher.enabled.
				if f, ok := v.Type().Elem().FieldByName("Action"); ok && f.Type.Kind() == reflect.String {
					for j := 0; j < v.Len(); j++ {
						if v.Index(j).FieldByIndex(f.Index).String() == seg {
							n, err = j, nil
							break
						}
					}
				}
			}
			if err != nil || n < 0 || n >= v.Len() {
				return v, fmt.Errorf("no element %s (length %d)", strings.Join(segs[:i+1], "."), v.Len())
			}
			v = v.Index(n)
		case reflect.Map:
			e := v.MapIndex(reflect.ValueOf(seg).Convert(v.Type().Key()))
			if !e.IsValid() {
				return v, fmt.Errorf("no key %s", strings.Join(segs[:i+1], "."))
			}
			v = e
		default:
			return v, fmt.Errorf("%s is a %s, not a record", strings.Join(segs[:i], "."), v.Kind())
		}
	}
	return v, nil
}

// jsonField returns the field of struct v with the JSON name (or Go name)
// seg.
func jsonField(v reflect.Value, seg string) (reflect.Value, bool) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == seg || name == "" && strings.EqualFold(f.Name, seg) {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

// scalarString formats a scalar the way conditions compare it.
func scalarString(v reflect.Value) (string, bool) {
	switch v.Kind() {
	case reflect.Invalid:
		return "null", true
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return "null", true
		}
		return scalarString(v.Elem())
	case reflect.String:
		return v.String(), true
	case reflect.Bool:
		return strconv.FormatBool(v.Bool()), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10), true
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64), true
	}
	return "", false
}

// compareValue applies op to v and want; it returns the result and v as
// text for messages.
func compareValue(v reflect.Value, op, want string) (bool, string) {
	for (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && !v.IsNil() {
		v = v.Elem()
	}
	if v.Kind() == reflect.Slice || v.Kind() == reflect.Array {
		got := shortJSON(v)
		pos := op == "=" || op == "~"
		if !pos && op != "!=" && op != "!~" {
			return false, got + " (an array)"
		}
		elemOp := strings.TrimPrefix(op, "!")
		if elemOp == "" {
			elemOp = "="
		}
		any := false
		for i := 0; i < v.Len(); i++ {
			if ok, _ := compareValue(v.Index(i), elemOp, want); ok {
				any = true
				break
			}
		}
		return any == pos, got
	}
	got, ok := scalarString(v)
	if !ok { // a record: it only differs from null
		return op == "!=" && want == "null", shortJSON(v)
	}
	shown := got
	if v.Kind() == reflect.String {
		shown = strconv.Quote(got)
	}
	gf, gerr := strconv.ParseFloat(got, 64)
	wf, werr := strconv.ParseFloat(want, 64)
	numeric := gerr == nil && werr == nil && v.Kind() != reflect.String
	switch op {
	case "=":
		return got == want || numeric && gf == wf, shown
	case "!=":
		return !(got == want || numeric && gf == wf), shown
	case "~":
		return strings.Contains(got, want), shown
	case "!~":
		return !strings.Contains(got, want), shown
	}
	if gerr != nil || werr != nil {
		return false, shown + " (not a number)"
	}
	switch op {
	case ">":
		return gf > wf, shown
	case ">=":
		return gf >= wf, shown
	case "<":
		return gf < wf, shown
	case "<=":
		return gf <= wf, shown
	}
	return false, shown
}

func shortJSON(v reflect.Value) string {
	b, err := json.Marshal(v.Interface())
	if err != nil {
		return v.String()
	}
	if len(b) > 400 {
		return string(b[:400]) + "…"
	}
	return string(b)
}

// markValue reads MARK.FIELD[+N|-N] from the marks recorded so far.
func (a *App) markValue(ref string) (float64, error) {
	name, field, ok := strings.Cut(ref, ".")
	if !ok {
		return 0, fmt.Errorf("@%s: want @MARK.FIELD", ref)
	}
	off := 0.0
	if i := strings.IndexAny(field, "+-"); i > 0 {
		n, err := strconv.ParseFloat(field[i:], 64)
		if err != nil {
			return 0, fmt.Errorf("@%s: %v", ref, err)
		}
		field, off = field[:i], n
	}
	for i := len(a.rep.Marks) - 1; i >= 0; i-- {
		if m := a.rep.Marks[i]; m.Name == name {
			f, ok := jsonField(reflect.ValueOf(m), field)
			if !ok {
				return 0, fmt.Errorf("@%s: marks have no %s", ref, field)
			}
			s, _ := scalarString(f)
			n, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return 0, fmt.Errorf("@%s: %v", ref, err)
			}
			return n + off, nil
		}
	}
	return 0, fmt.Errorf("@%s: no mark %s", ref, name)
}

// resources counts what the process holds, as a mark does.
func (a *App) resources() markReport {
	m := markReport{FDs: len(leakcheck.FDs()), Goroutines: runtime.NumGoroutine()}
	for _, c := range leakcheck.Children() {
		m.Children++
		if c.State == 'Z' {
			m.Zombies++
		}
	}
	for _, p := range a.panes {
		if p == nil {
			continue
		}
		m.Panes++
		if p.r != nil {
			m.Textures++ // the glyph atlas
		}
		for i := range p.slots {
			if p.slots[i].tex != 0 {
				m.Textures++
			}
		}
	}
	return m
}

// onNextPaint runs f on the main loop after the window's next frame is
// painted, or after a second when no frame comes.
func (a *App) onNextPaint(f func()) {
	done := false
	run := func() {
		if !done {
			done = true
			f()
		}
	}
	a.paintWaiters = append(a.paintWaiters, run)
	coreglib.TimeoutAdd(1000, func() bool { run(); return false })
}

// paintDone runs the onNextPaint callbacks queued before this frame,
// outside the frame clock's signal handler.
func (a *App) paintDone() {
	if len(a.paintWaiters) == 0 {
		return
	}
	fs := a.paintWaiters
	a.paintWaiters = nil
	coreglib.IdleAdd(func() {
		for _, f := range fs {
			f()
		}
	})
}
