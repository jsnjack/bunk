package main

// -report FILE writes a JSON record of a run for the automated tests
// (gui_test.go): renderer names, -verify results, frame and allocation
// counters at named marks, latency percentiles, and snapshots of the UI
// state taken by the "snap" script action. Geometry is given in logical
// window coordinates and in device pixels of a "shot FILE window" image, so
// tests can sample pixels where widgets are.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/session"
)

type runReport struct {
	Reason      string               `json:"reason"`
	GLRenderer  string               `json:"gl_renderer"`
	GLVersion   string               `json:"gl_version"`
	GSKRenderer string               `json:"gsk_renderer"`
	Race        bool                 `json:"race"`
	ActiveMs    int64                `json:"active_ms"` // launch to the start of -do (after -wait-focus)
	Verify      *verifyReport        `json:"verify,omitempty"`
	Frames      int                  `json:"frames"`
	ClockFrames int                  `json:"clock_frames"`
	AllocProbe  bool                 `json:"alloc_probe"`
	Marks       []markReport         `json:"marks"`
	Latency     map[string]latReport `json:"latency_us"`
	Snapshots   []*uiSnapshot        `json:"snapshots"`
	Final       *uiSnapshot          `json:"final"`
	Errors      []string             `json:"errors"`
	Toasts      []string             `json:"toasts"`
	Settings    map[string]any       `json:"settings"`
	ConfigError string               `json:"config_error"`
	Reloads     int                  `json:"reloads"` // config.toml reloads after a change
}

type verifyReport struct {
	FramePixels      int `json:"frame_pixels"`
	FrameDiffer      int `json:"frame_differ"`
	FrameMaxDelta    int `json:"frame_max_delta"`
	CheckerPixels    int `json:"checker_pixels"`
	CheckerDiffer    int `json:"checker_differ"`
	CheckerMaxDelta  int `json:"checker_max_delta"`
	CheckerNonBinary int `json:"checker_non_binary"`
}

// markReport is the counters at a "mark" action; tests subtract two marks.
type markReport struct {
	Name          string `json:"name"`
	TMs           int64  `json:"t_ms"`
	Frames        int    `json:"frames"`
	ClockFrames   int    `json:"clock_frames"`
	RenderMallocs int64  `json:"render_mallocs"` // objects allocated under (*Pane).render; -1 without -alloc-probe
	Mallocs       uint64 `json:"mallocs"`        // whole process
	KeyRenders    int    `json:"key_renders"`    // key writes drawn so far
}

type latReport struct {
	N    int     `json:"n"`
	P50  float64 `json:"p50"`
	P99  float64 `json:"p99"`
	Max  float64 `json:"max"`
	Mean float64 `json:"mean"`
}

type rectReport struct {
	X  float64 `json:"x"` // logical, window coordinates
	Y  float64 `json:"y"`
	W  float64 `json:"w"`
	H  float64 `json:"h"`
	DX int     `json:"dx"` // device pixels in a window shot
	DY int     `json:"dy"`
	DW int     `json:"dw"`
	DH int     `json:"dh"`
}

type statusReport struct {
	Reported  bool   `json:"reported"` // from the agent's own hook report
	Agent     string `json:"agent"`
	State     string `json:"state"`
	Process   string `json:"process"`
	Notice    string `json:"notice"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	Cwd       string `json:"cwd"`
	Container string `json:"container"`
	SSHHost   string `json:"ssh_host"`
}

type tabReport struct {
	Index    int          `json:"index"` // 1-based
	Title    string       `json:"title"`
	Agent    string       `json:"agent"` // as shown: the agent, else the process
	State    string       `json:"state"`
	Label    string       `json:"label"`
	Dot      string       `json:"dot"`
	Repo     string       `json:"repo"` // as shown: "repo ⎇ branch" or the directory
	Branch   string       `json:"branch"`
	Loc      string       `json:"loc"`
	Notice   string       `json:"notice"`
	Tooltip  string       `json:"tooltip"`
	Selected bool         `json:"selected"`
	Needs    bool         `json:"needs"`
	Panes    []int        `json:"panes"`
	Focus    int          `json:"focus"`
	Zoomed   int          `json:"zoomed"` // pane id, -1 when none
	Item     *rectReport  `json:"item,omitempty"`
	Close    *rectReport  `json:"close,omitempty"`
	Status   statusReport `json:"status"` // of the agent pane
	Pass     bool         `json:"pass"`   // shows the PASS tag
}

type paneReport struct {
	ID       int          `json:"id"`
	Tab      int          `json:"tab"`
	Rect     *rectReport  `json:"rect,omitempty"` // nil when not shown
	Texture  [4]int       `json:"texture"`        // device rect of the texture in a window shot
	Cols     int          `json:"cols"`
	Rows     int          `json:"rows"`
	Cell     [2]int       `json:"cell"`      // device pixels of a cell, as the session reports them
	Focused  bool         `json:"focused"`   // the tab's focused pane
	HasFocus bool         `json:"has_focus"` // GTK keyboard focus
	Dimmed   bool         `json:"dimmed"`
	Zoomed   bool         `json:"zoomed"`
	Ring     bool         `json:"ring"`
	Visible  bool         `json:"visible"`
	Override bool         `json:"override"`
	Pass     bool         `json:"passthrough"`
	Badge    *rectReport  `json:"badge,omitempty"` // the PASS badge, when shown
	Status   statusReport `json:"status"`
	Title    string       `json:"title"`
	Scroll   int          `json:"scroll"`
	Text     []string     `json:"text"` // live grid rows, trailing blanks trimmed
}

type treeReport struct {
	Pane     *int        `json:"pane,omitempty"`
	Split    string      `json:"split,omitempty"` // "h": side by side, "v": stacked
	Position int         `json:"position,omitempty"`
	Divider  *rectReport `json:"divider,omitempty"`
	A        *treeReport `json:"a,omitempty"`
	B        *treeReport `json:"b,omitempty"`
}

type uiSnapshot struct {
	Label         string              `json:"label"`
	TMs           int64               `json:"t_ms"`
	Layout        string              `json:"layout"`
	RailOpen      bool                `json:"rail_open"`
	Window        windowReport        `json:"window"`
	Chrome        *rectReport         `json:"chrome"`
	NewTab        *rectReport         `json:"new_tab,omitempty"`
	Options       *rectReport         `json:"options,omitempty"`
	CloseWindow   *rectReport         `json:"close_window,omitempty"`
	Tabs          []tabReport         `json:"tabs"`
	SelectedTab   int                 `json:"selected_tab"`
	Panes         []paneReport        `json:"panes"`
	Tree          *treeReport         `json:"tree"`
	FocusedPane   int                 `json:"focused_pane"`
	ZoomedPane    int                 `json:"zoomed_pane"`
	Font          string              `json:"font"`
	FontSize      float64             `json:"font_size"`
	TextPercent   int                 `json:"text_percent"`
	Switcher      switchReport        `json:"switcher"`
	MenuOpen      bool                `json:"menu_open"`
	AboutOpen     bool                `json:"about_open"`
	LastToast     string              `json:"last_toast"`
	Clipboard     *string             `json:"clipboard"`
	Primary       *string             `json:"primary"`
	Pointer       [2]float64          `json:"pointer"` // last position seen by the window, logical
	KeyboardFocus string              `json:"keyboard_focus"`
	Keys          map[string][]string `json:"keys"` // action → bindings in force
	ConfigError   string              `json:"config_error"`
	Shortcuts     *shortcutsReport    `json:"shortcuts,omitempty"`
	Quit          *quitReport         `json:"quit_dialog,omitempty"`
	MenuPassCheck bool                `json:"menu_pass_check"`
	Find          *findReport         `json:"find,omitempty"`
	TextZoom      float64             `json:"text_zoom"` // the default, from the config
	Agents        []session.AgentSpec `json:"agents"`    // extra agents in force
	Prefs         *prefsReport        `json:"prefs,omitempty"`
}

type prefsReport struct {
	Message  string                 `json:"message"`
	Tabs     string                 `json:"tabs"`
	RailOpen bool                   `json:"rail_open"`
	Font     string                 `json:"font"`
	FontSize float64                `json:"font_size"`
	TextZoom float64                `json:"text_zoom"`
	Agents   [][3]string            `json:"agents"` // name, processes, scripts as the rows show them
	Controls map[string]*rectReport `json:"controls"`
}

type findReport struct {
	Pane    int         `json:"pane"`
	Query   string      `json:"query"`
	Count   string      `json:"count"` // as the bar shows it, e.g. "3/17"
	Index   int         `json:"index"` // current match, -1 if none
	Total   int         `json:"total"`
	Focused bool        `json:"focused"` // the bar has the keyboard
	Busy    bool        `json:"busy"`    // a scan is running
	Bar     *rectReport `json:"bar,omitempty"`
	// Visible matches: virtual row, column and length, and the row on
	// screen.
	Current *matchReport  `json:"current,omitempty"`
	Visible []matchReport `json:"visible"`
}

type matchReport struct {
	VRow, Col, Length, Row int
}

type shortcutsReport struct {
	Capturing string               `json:"capturing"`
	Message   string               `json:"message"`
	Rows      []shortcutsRowReport `json:"rows"`
}

type shortcutsRowReport struct {
	Action   string      `json:"action"`
	Keys     string      `json:"keys"` // as the row shows them
	Enabled  bool        `json:"enabled"`
	Collides bool        `json:"collides"`
	KeysBtn  *rectReport `json:"keys_button,omitempty"`
	Switch   *rectReport `json:"switch,omitempty"`
}

type quitReport struct {
	Body []string `json:"body"`
}

type windowReport struct {
	W         int     `json:"w"` // logical
	H         int     `json:"h"`
	Scale     float64 `json:"scale"`
	Maximized bool    `json:"maximized"`
	Active    bool    `json:"active"`
	Title     string  `json:"title"`
}

type switchReport struct {
	Open   bool   `json:"open"`
	Filter string `json:"filter"`
	Rows   []int  `json:"rows"` // tab indexes in the order shown
}

var stateName = map[session.AgentState]string{
	session.AgentNone: "none", session.AgentWorking: "working", session.AgentIdle: "idle",
	session.AgentNeedsYou: "needs", session.AgentFailed: "failed", session.AgentDone: "done",
}

func statusOf(st session.Status) statusReport {
	return statusReport{
		Reported: st.Reported, Agent: st.Agent, State: stateName[st.State], Process: st.Process, Notice: st.Notice,
		Repo: st.Repo, Branch: st.Branch, Cwd: st.Cwd, Container: st.Container, SSHHost: st.SSHHost,
	}
}

func (a *App) reportOn() bool { return a.cfg.report != "" }

func (a *App) reportError(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Println("do: error:", msg)
	a.rep.Errors = append(a.rep.Errors, msg)
}

// winGeom converts logical window coordinates to device pixels of a window
// shot (see bg_shot): x*scale plus the fractional part of the surface
// offset.
type winGeom struct{ scale, fx, fy float64 }

func (a *App) geom() winGeom {
	sx, sy := a.win.SurfaceTransform()
	scale := a.monScale
	if p := a.activePane(); p != nil {
		if g := viewGeomC(p.view); g.scale > 0 {
			scale = g.scale
		}
	}
	return winGeom{scale, sx*scale - math.Floor(sx*scale), sy*scale - math.Floor(sy*scale)}
}

func (g winGeom) rect(x, y, w, h float64) *rectReport {
	x0, y0 := int(math.Round(x*g.scale+g.fx)), int(math.Round(y*g.scale+g.fy))
	x1, y1 := int(math.Round((x+w)*g.scale+g.fx)), int(math.Round((y+h)*g.scale+g.fy))
	return &rectReport{X: x, Y: y, W: w, H: h, DX: x0, DY: y0, DW: x1 - x0, DH: y1 - y0}
}

// boundsIn returns w's bounds in window coordinates.
func (a *App) boundsIn(w gtk.Widgetter) (x, y, wd, ht float64, ok bool) {
	if w == nil {
		return 0, 0, 0, 0, false
	}
	b, ok := gtk.BaseWidget(w).ComputeBounds(a.win)
	if !ok {
		return 0, 0, 0, 0, false
	}
	return float64(b.X()), float64(b.Y()), float64(b.Width()), float64(b.Height()), true
}

func (a *App) widgetRect(g winGeom, w gtk.Widgetter) *rectReport {
	x, y, wd, ht, ok := a.boundsIn(w)
	if !ok || !gtk.BaseWidget(w).IsDrawable() {
		return nil
	}
	return g.rect(x, y, wd, ht)
}

// snapshot records the UI state now; clipboard texts arrive asynchronously.
func (a *App) snapshot(label string) *uiSnapshot {
	g := a.geom()
	s := &uiSnapshot{
		Label: label, TMs: time.Since(a.started).Milliseconds(),
		Layout: a.layout, RailOpen: a.railOpen,
		Window: windowReport{W: a.win.Width(), H: a.win.Height(), Scale: g.scale,
			Maximized: a.win.IsMaximized(), Active: a.win.IsActive(), Title: a.win.Title()},
		Chrome: a.widgetRect(g, a.chrome), NewTab: a.widgetRect(g, a.newTabBtn),
		CloseWindow: a.widgetRect(g, a.closeWinBtn),
		FocusedPane: -1, ZoomedPane: -1,
		Font: a.fontDesc, FontSize: a.fontSize, TextPercent: int(a.fontSize/a.settings.FontSize*100 + 0.5),
		LastToast: a.lastToast, Pointer: [2]float64{a.ptrX, a.ptrY},
	}
	if a.optionsBtn != nil {
		s.Options = a.widgetRect(g, a.optionsBtn)
		s.MenuOpen = a.optionsBtn.Active()
	}
	s.AboutOpen = a.about != nil
	infos := a.tabInfos()
	for i, in := range infos {
		t := in.t
		tr := tabReport{
			Index: in.index + 1, Agent: in.agent, State: stateName[in.st.State], Label: in.look.label, Dot: in.look.dot,
			Repo: in.repo, Branch: in.st.Branch, Loc: in.loc, Notice: in.st.Notice, Tooltip: in.tooltip,
			Selected: in.active, Needs: in.needs, Focus: -1, Zoomed: -1, Status: statusOf(in.st), Pass: in.pass,
		}
		if t.focus != nil {
			tr.Focus, tr.Title = t.focus.id, t.focus.title
		}
		if t.zoom != nil {
			tr.Zoomed = t.zoom.id
		}
		for _, p := range t.panes() {
			tr.Panes = append(tr.Panes, p.id)
		}
		if i < len(a.tabItems) {
			tr.Item = a.widgetRect(g, a.tabItems[i].item)
			tr.Close = a.widgetRect(g, a.tabItems[i].close)
		}
		s.Tabs = append(s.Tabs, tr)
		if in.active {
			s.SelectedTab = in.index + 1
		}
	}
	ox, oy := a.surfaceOrigin(g)
	for _, t := range a.tabs {
		for _, p := range t.panes() {
			s.Panes = append(s.Panes, a.paneReport(g, ox, oy, p))
		}
	}
	if t := a.active; t != nil {
		s.Tree = a.treeReport(g, t.root)
		if t.focus != nil {
			s.FocusedPane = t.focus.id
		}
		if t.zoom != nil {
			s.ZoomedPane = t.zoom.id
		}
	}
	if sw := a.switcher; sw != nil {
		s.Switcher.Open, s.Switcher.Filter = true, sw.entry.Text()
		for _, t := range sw.shown {
			s.Switcher.Rows = append(s.Switcher.Rows, a.tabIndex(t)+1)
		}
	}
	s.KeyboardFocus = a.keyboardFocus()
	s.Keys = map[string][]string{}
	for name, cs := range a.keys().bind {
		s.Keys[name] = []string{}
		for _, c := range cs {
			s.Keys[name] = append(s.Keys[name], c.String())
		}
	}
	if a.configErr != nil {
		s.ConfigError = a.configErr.Error()
	}
	if a.passCheck != nil {
		s.MenuPassCheck = a.passCheck.Active()
	}
	if w := a.shortcutsWin; w != nil {
		sr := &shortcutsReport{Capturing: w.capturing, Message: w.message.Text()}
		for _, act := range keyActions {
			r := w.rows[act.name]
			sr.Rows = append(sr.Rows, shortcutsRowReport{
				Action: act.name, Keys: r.keys.Label(), Enabled: r.sw.Active(), Collides: r.warn.Opacity() > 0,
				KeysBtn: a.widgetRect(g, r.keys), Switch: a.widgetRect(g, r.sw),
			})
		}
		s.Shortcuts = sr
	}
	if a.quitDialog != nil {
		s.Quit = &quitReport{Body: a.quitBody}
	}
	s.TextZoom, s.Agents = a.settings.TextZoom, a.settings.Agents
	if w := a.prefsWin; w != nil {
		pr := &prefsReport{Message: w.message.Text(), Tabs: map[bool]string{true: "bar", false: "rail"}[w.tabsBar.Active()],
			RailOpen: w.railOpen.Active(), Font: w.font.Text(), FontSize: w.fontSize.Value(), TextZoom: w.zoom.Value(),
			Agents: [][3]string{}, Controls: map[string]*rectReport{}}
		for _, r := range w.rows {
			pr.Agents = append(pr.Agents, [3]string{r.name.Text(), r.processes.Text(), r.scripts.Text()})
		}
		for k, c := range w.controls {
			pr.Controls[k] = a.widgetRect(g, c)
		}
		s.Prefs = pr
	}
	if f := a.find; f != nil {
		fr := &findReport{Pane: f.p.id, Query: f.query, Count: f.count.Text(), Index: f.idx, Total: len(f.matches),
			Focused: a.findFocused(), Busy: f.busy, Bar: a.widgetRect(g, f.bar), Visible: []matchReport{}}
		f.p.s.Lock()
		_, rows := f.p.s.Term.Size()
		top := f.p.s.Scrollback.Count - f.p.s.ScrollOffset
		f.p.s.Unlock()
		for i, m := range f.matches {
			mr := matchReport{m.VRow, m.Col, m.Length, -1}
			if m.VRow >= top && m.VRow < top+rows {
				mr.Row = m.VRow - top
				fr.Visible = append(fr.Visible, mr)
			}
			if i == f.idx {
				fr.Current = &mr
			}
		}
		s.Find = fr
	}
	return s
}

// surfaceOrigin is the window shot's origin in surface device pixels.
func (a *App) surfaceOrigin(g winGeom) (int, int) {
	sx, sy := a.win.SurfaceTransform()
	return int(math.Floor(sx * g.scale)), int(math.Floor(sy * g.scale))
}

func (a *App) keyboardFocus() string {
	fw := a.win.Focus()
	if fw == nil {
		return "none"
	}
	w := gtk.BaseWidget(fw)
	if a.switcher != nil && w.IsAncestor(a.switcher.entry) {
		return "switcher"
	}
	for _, p := range a.panes {
		if p != nil && !p.removeOnce && p.widget.HasFocus() {
			return fmt.Sprintf("pane %d", p.id)
		}
	}
	return "other"
}

func (a *App) paneReport(g winGeom, ox, oy int, p *Pane) paneReport {
	t := p.tab
	r := paneReport{
		ID: p.id, Tab: a.tabIndex(t) + 1, Cols: p.cols, Rows: p.rows,
		Focused: t.focus == p, HasFocus: p.widget.HasFocus(), Dimmed: t.dimmed(p), Zoomed: t.zoom == p,
		Ring: p.status().State == session.AgentNeedsYou, Visible: p.widget.IsDrawable(),
		Override: p.statusOverride != nil, Status: statusOf(p.status()), Title: p.title,
		Pass: p.passthrough, Cell: [2]int{p.atlas.CellW, p.atlas.CellH},
	}
	if p.passthrough {
		r.Badge = a.widgetRect(g, p.passBadge)
	}
	if r.Visible {
		r.Rect = a.widgetRect(g, p.widget)
		vg := viewGeomC(p.view)
		r.Texture = [4]int{vg.x0 - ox, vg.y0 - oy, vg.dw, vg.dh}
	}
	if p.s != nil {
		p.s.Lock()
		cols, rows := p.s.Term.Size()
		r.Scroll = p.s.ScrollOffset
		for y := 0; y < rows; y++ {
			var b strings.Builder
			for _, gl := range session.CaptureRow(p.s.Term, y, cols) {
				if gl.Width < 0 {
					continue
				}
				if gl.Char == 0 {
					b.WriteByte(' ')
				} else {
					b.WriteRune(gl.Char)
				}
			}
			r.Text = append(r.Text, strings.TrimRight(b.String(), " "))
		}
		p.s.Unlock()
	}
	return r
}

func (a *App) treeReport(g winGeom, n *node) *treeReport {
	if n == nil {
		return nil
	}
	if n.pane != nil {
		id := n.pane.id
		return &treeReport{Pane: &id}
	}
	tr := &treeReport{Split: "h", Position: n.paned.Position(), A: a.treeReport(g, n.a), B: a.treeReport(g, n.b)}
	if n.vertical {
		tr.Split = "v"
	}
	if x, y, w, h, ok := a.dividerRect(n); ok {
		tr.Divider = g.rect(x, y, w, h)
	}
	return tr
}

// dividerRect is the gap between a split's two children, in window
// coordinates: GtkPaned draws its 1 px handle there.
func (a *App) dividerRect(n *node) (x, y, w, h float64, ok bool) {
	ax, ay, aw, ah, ok1 := a.boundsIn(n.a.widget())
	bx, by, _, _, ok2 := a.boundsIn(n.b.widget())
	px, py, pw, ph, ok3 := a.boundsIn(n.paned)
	if !ok1 || !ok2 || !ok3 {
		return 0, 0, 0, 0, false
	}
	if n.vertical {
		return px, ay + ah, pw, by - (ay + ah), true
	}
	return ax + aw, py, bx - (ax + aw), ph, true
}

// snap records a snapshot in the report and fills in the clipboard and
// primary selection texts when GTK has read them.
func (a *App) snap(label string) {
	s := a.snapshot(label)
	a.rep.Snapshots = append(a.rep.Snapshots, s)
	for _, primary := range []bool{false, true} {
		cb := clipboard(primary)
		cb.ReadTextAsync(context.Background(), func(res gio.AsyncResulter) {
			text, err := cb.ReadTextFinish(res)
			if err != nil {
				text = ""
			}
			if primary {
				s.Primary = &text
			} else {
				s.Clipboard = &text
			}
		})
	}
}

// renderMallocs counts objects allocated under (*Pane).render so far. It
// needs runtime.MemProfileRate = 1 (-alloc-probe).
func renderMallocs() int64 { return mallocsUnder("(*Pane).render") }

// pkgPrefix starts this package's function names: "main." in bunk-gui,
// the import path in its test binary.
var pkgPrefix = strings.TrimSuffix(runtime.FuncForPC(reflect.ValueOf(pkgAnchor).Pointer()).Name(), "pkgAnchor")

func pkgAnchor() {}

// mallocsUnder counts the objects allocated so far by calls with one of
// funcs (names in this package, e.g. "(*Pane).render") on the stack. Other
// goroutines do not count, unlike runtime.MemStats.Mallocs. It needs
// runtime.MemProfileRate = 1, and runs two collections so the profile
// includes the latest allocations.
func mallocsUnder(funcs ...string) int64 {
	full := make([]string, len(funcs))
	for i, f := range funcs {
		full[i] = pkgPrefix + f
	}
	runtime.GC()
	runtime.GC()
	n, _ := runtime.MemProfile(nil, true)
	recs := make([]runtime.MemProfileRecord, n+256)
	n, ok := runtime.MemProfile(recs, true)
	if !ok {
		return -1
	}
	var total int64
	for i := range recs[:n] {
		r := &recs[i]
		frames := runtime.CallersFrames(r.Stack())
		for {
			f, more := frames.Next()
			if slices.Contains(full, f.Function) {
				total += r.AllocObjects
				break
			}
			if !more {
				break
			}
		}
	}
	return total
}

func (a *App) addMark(name string) markReport {
	m := markReport{Name: name, TMs: time.Since(a.started).Milliseconds(), Frames: a.st.frames,
		ClockFrames: a.st.clockFrames, RenderMallocs: -1, KeyRenders: len(a.st.keyRender)}
	if a.cfg.allocProbe {
		m.RenderMallocs = renderMallocs()
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	m.Mallocs = ms.Mallocs
	a.rep.Marks = append(a.rep.Marks, m)
	return m
}

func latOf(s series) latReport {
	m := summarize(s)
	return latReport{N: m.n, P50: m.p50, P99: m.p99, Max: m.max, Mean: m.mean}
}

func (a *App) writeReport(reason string) {
	r := a.rep
	r.Reason = reason
	r.Race = raceEnabled
	r.GLRenderer, r.GLVersion, r.GSKRenderer = a.glRenderer, a.glVersion, a.gskRenderer
	r.Frames, r.ClockFrames = a.st.frames, a.st.clockFrames
	r.AllocProbe = a.cfg.allocProbe
	r.Toasts = a.toastLog
	r.Settings = map[string]any{
		"tabs": a.settings.Tabs, "rail_open": a.settings.RailOpen, "font": a.settings.Font,
		"font_size": a.settings.FontSize, "agents": a.settings.Agents,
	}
	if a.configErr != nil {
		r.ConfigError = a.configErr.Error()
	}
	r.Latency = map[string]latReport{
		"key_render": latOf(a.st.keyRender), "key_present": latOf(a.st.keyPresent),
		"output_render": latOf(a.st.outOldest), "frame": latOf(a.st.total),
		"inject_write": latOf(a.st.injectWrite),
	}
	a.addMark("end")
	r.Final = a.snapshot("final")
	data, err := json.MarshalIndent(r, "", "  ")
	if err == nil {
		err = os.WriteFile(a.cfg.report, data, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "bunk-gui: -report:", err)
	}
}
