package main

// Session restore (internal/restore). Each window keeps a snapshot of its
// tabs, splits, directories, agents and scrollback in a slot of the store
// under $XDG_STATE_HOME/bunk-gui/windows, rewritten as it changes. A start
// that finds snapshots no running window holds shows a banner above the
// panes; Restore brings them back, the first into this window and each
// other in a window of its own (bunk-gui --restore ID), and Dismiss
// deletes them.
//
// A restored pane starts the user's shell in its directory, shows its old
// scrollback, and once the shell is quiet types the command that resumes
// its agent's conversation (restore.ResumeCommands), or, for ssh and mosh,
// the command line without running it. An agent pane skips the scrollback:
// the agent prints its conversation again.
//
// Quitting keeps the snapshot for the next start unless other windows are
// still open; a window whose last pane ended leaves none. restore = false
// in config.toml turns saving and the banner off.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/restore"
	"bunk/internal/session"
)

const (
	// saveEvery is how often the layout is checked and, when it changed,
	// written.
	saveEvery = 2 * time.Second
	// historyEvery is how often panes with new output have their
	// scrollback written, in saveEvery ticks.
	historyEvery = 15
	// typeAfterQuiet is how long a restored shell stays silent before its
	// command is typed; typeWithin bounds the wait for a shell that never
	// is.
	typeAfterQuiet = 300 * time.Millisecond
	typeWithin     = 5 * time.Second
)

// restorer is the window's part in session restore.
type restorer struct {
	store   *restore.Store
	slot    *restore.Slot // this window's; nil when restore is off
	offer   []restore.Saved
	banner  *banner
	answer  string // "restored" or "dismissed" once the banner is answered
	last    []byte // the layout last written
	ticks   int
	busy    atomic.Bool // a scrollback round is writing
	saves   int
	spawned []string // snapshots opened in windows of their own
	typed   []typedReport
}

// typedReport is a command typed into a restored pane.
type typedReport struct {
	Pane int    `json:"pane"`
	Text string `json:"text"`
}

type restoreReport struct {
	Slot    string        `json:"slot"`
	Offer   string        `json:"offer"`  // what the banner offers, "" without one
	Banner  bool          `json:"banner"` // the banner is shown
	Answer  string        `json:"answer"`
	Saves   int           `json:"saves"` // layouts written
	Spawned []string      `json:"spawned"`
	Typed   []typedReport `json:"typed"`
}

func (a *App) restoreReport() restoreReport {
	r := &a.restorer
	rep := restoreReport{Answer: r.answer, Saves: r.saves, Spawned: r.spawned, Typed: r.typed}
	if r.slot != nil {
		rep.Slot = r.slot.ID()
	}
	if r.banner.shown() {
		rep.Banner = true
		rep.Offer = r.banner.label.Text()
	}
	return rep
}

// initRestore opens the store and takes this window's slot. With
// --restore it builds the window from that snapshot and reports true;
// otherwise it offers the snapshots no window holds.
func (a *App) initRestore() bool {
	r := &a.restorer
	if !a.settings.restore {
		return false
	}
	dir := restore.DefaultDir(os.Getenv)
	if dir == "" {
		return false
	}
	st, err := restore.Open(dir)
	if err != nil {
		slog.Warn("restore: running without saving windows", "err", err)
		return false
	}
	r.store = st
	if id := a.cfg.restoreID; id != "" {
		slot, err := st.Claim(id)
		if err == nil {
			w, lerr := slot.Load()
			if lerr == nil && a.restoreWindow(w, slot, []restore.Window{w}) > 0 {
				r.slot = slot
				a.applyWindowSize(w)
				coreglib.TimeoutAdd(uint(saveEvery.Milliseconds()), a.saveTick)
				return true
			}
			err = lerr
			slot.Release()
		}
		slog.Warn("restore: window not restored", "id", id, "err", err)
	}
	if r.slot, err = st.New(); err != nil {
		slog.Warn("restore: running without saving windows", "err", err)
		return false
	}
	coreglib.TimeoutAdd(uint(saveEvery.Milliseconds()), a.saveTick)
	if a.cfg.offerRestore {
		r.offer = st.Saved()
	}
	return false
}

// showRestoreOffer reveals the banner when snapshots wait.
func (a *App) showRestoreOffer() {
	r := &a.restorer
	if len(r.offer) == 0 || r.banner == nil {
		return
	}
	ws := make([]restore.Window, len(r.offer))
	for i, s := range r.offer {
		ws[i] = s.Window
	}
	when := r.offer[0].Window.Saved.Local().Format("Jan 2 15:04")
	r.banner.show(fmt.Sprintf("Restore the last session (%s): %s?", when, restore.Summary(ws)))
}

func (a *App) hideRestoreOffer(answer string) {
	r := &a.restorer
	r.answer, r.offer = answer, nil
	r.banner.hide()
	if p := a.activePane(); p != nil {
		p.widget.GrabFocus()
	}
}

// acceptRestore brings the offered windows back: the newest into this
// window, beside its tabs or in place of a first tab nobody typed in, and
// each other in a window of its own.
func (a *App) acceptRestore() {
	r := &a.restorer
	offer := r.offer
	if len(offer) == 0 {
		return
	}
	a.hideRestoreOffer("restored")
	all := make([]restore.Window, len(offer))
	for i, s := range offer {
		all[i] = s.Window
	}
	var fresh *Tab
	if len(a.tabs) == 1 && len(a.tabs[0].panes()) == 1 && !a.tabs[0].focus.typed && a.tabs[0].name == "" {
		fresh = a.tabs[0]
	}
	slot, err := r.store.Claim(offer[0].ID)
	if err != nil {
		slog.Warn("restore: window not restored", "id", offer[0].ID, "err", err)
		a.toast("Could not restore the window: it is open elsewhere")
	} else {
		if a.restoreWindow(offer[0].Window, slot, all) > 0 && fresh != nil {
			a.closeTab(fresh)
		}
		if err := slot.Remove(); err != nil {
			slog.Warn("restore: remove", "err", err)
		}
	}
	for _, s := range offer[1:] {
		a.spawnRestore(s.ID)
	}
}

// dismissRestore deletes the offered snapshots.
func (a *App) dismissRestore() {
	offer := a.restorer.offer
	a.hideRestoreOffer("dismissed")
	for _, s := range offer {
		if err := a.restorer.store.Discard(s.ID); err != nil {
			slog.Debug("restore: discard", "id", s.ID, "err", err)
		}
	}
}

// spawnRestore opens the snapshot id in a new bunk-gui. On the test
// compositor it only records it.
func (a *App) spawnRestore(id string) {
	a.restorer.spawned = append(a.restorer.spawned, id)
	if err := launchRefused(); err != nil {
		slog.Debug("restore window not opened", "id", id, "err", err)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		slog.Warn("restore: find bunk-gui", "err", err)
		return
	}
	var token string
	ctx := gdk.DisplayGetDefault().AppLaunchContext()
	if info, err := gio.AppInfoCreateFromCommandline(exe, "bunk-gui", gio.AppInfoCreateNone); err == nil {
		token = ctx.StartupNotifyID(info, nil)
	}
	cmd := windowCmd(exe, "", a.cfg.configFile, token)
	cmd.Args = append(cmd.Args, "--restore", id)
	if err := cmd.Start(); err != nil {
		if token != "" {
			ctx.LaunchFailed(token)
		}
		slog.Warn("restore: open window", "err", err)
		return
	}
	go func() { _ = cmd.Wait() }()
}

// applyWindowSize gives a window restored at start its old size.
func (a *App) applyWindowSize(w restore.Window) {
	if w.Width > 0 && w.Height > 0 {
		a.win.SetDefaultSize(w.Width, w.Height)
	}
	if w.Maximized {
		a.win.Maximize()
	}
}

// restoreWindow adds w's tabs, with the scrollback in slot, and selects
// the one w had selected. all are the windows restored together, which
// decide whether an agent without a session ID may resume its directory's
// latest conversation. It returns the number of tabs added.
func (a *App) restoreWindow(w restore.Window, slot *restore.Slot, all []restore.Window) int {
	cmds := restore.ResumeCommands(w, all)
	var tabs []*Tab
	for i, rt := range w.Tabs {
		if t := a.restoreTab(rt, cmds[i], slot, w.Saved); t != nil {
			tabs = append(tabs, t)
		}
	}
	if len(tabs) == 0 {
		return 0
	}
	a.selectTab(tabs[min(w.Selected, len(tabs)-1)])
	return len(tabs)
}

// restoreTab builds a tab from rt; cmds are its panes' resume commands,
// and saved is when the snapshot was written.
func (a *App) restoreTab(rt restore.Tab, cmds []string, slot *restore.Slot, saved time.Time) *Tab {
	t := &Tab{app: a, holder: gtk.NewBox(gtk.OrientationVertical, 0), name: rt.Name}
	t.holder.SetHExpand(true)
	t.holder.SetVExpand(true)
	cols, rows := a.initCols, a.initRows
	if cur := a.activePane(); cur != nil && cur.cols > 1 {
		cols, rows = cur.cols, cur.rows
	}
	var panes []*Pane
	var build func(rn *restore.Node, cols, rows int) *node
	build = func(rn *restore.Node, cols, rows int) *node {
		if rn.Pane != nil {
			p := a.restorePane(t, rn.Pane, cmds[len(panes)], slot, saved, cols, rows)
			if p == nil {
				return nil
			}
			panes = append(panes, p)
			return &node{pane: p}
		}
		ac, ar, bc, br := cols, rows, cols, rows
		if rn.Vertical {
			ar = max(1, int(float64(rows)*rn.Ratio))
			br = max(1, rows-ar)
		} else {
			ac = max(2, int(float64(cols)*rn.Ratio))
			bc = max(2, cols-ac)
		}
		na := build(rn.A, ac, ar)
		if na == nil {
			return nil
		}
		nb := build(rn.B, bc, br)
		if nb == nil {
			return nil
		}
		orient := gtk.OrientationHorizontal
		if rn.Vertical {
			orient = gtk.OrientationVertical
		}
		n := &node{a: na, b: nb, vertical: rn.Vertical, ratio: rn.Ratio}
		n.paned = a.newPaned(orient, n)
		n.paned.SetStartChild(na.widget())
		n.paned.SetEndChild(nb.widget())
		na.parent, nb.parent = n, n
		return n
	}
	root := build(rt.Root, cols, rows)
	if root == nil {
		for _, p := range panes {
			a.removePane(p)
		}
		return nil
	}
	t.root, t.id = root, panes[0].id
	t.focus = panes[min(max(rt.Focus, 0), len(panes)-1)]
	t.holder.Append(root.widget())
	// A page shown again gives the focus to the child each container last
	// focused, and a restored tab's containers have none yet: point them
	// at the focused pane.
	for n := t.find(t.focus); n.parent != nil; n = n.parent {
		n.parent.paned.SetFocusChild(n.widget())
	}
	t.holder.SetFocusChild(root.widget())
	a.tabs = append(a.tabs, t)
	a.stack.AddChild(t.holder)
	if rt.Zoom && len(panes) > 1 {
		t.toggleZoom(t.focus)
	}
	t.restyle()
	a.refreshChrome()
	return t
}

// restorePane starts a shell for rp in its directory, with its scrollback
// unless cmd resumes its agent, and types cmd, or the ssh or mosh command
// line it ran without running it.
func (a *App) restorePane(t *Tab, rp *restore.Pane, cmd string, slot *restore.Slot, saved time.Time, cols, rows int) *Pane {
	dir := rp.Cwd
	if fi, err := os.Stat(dir); dir == "" || !filepath.IsAbs(dir) || err != nil || !fi.IsDir() {
		dir = "" // gone: the shell starts where bunk-gui did
	}
	var history []byte
	if cmd == "" && rp.History != "" && slot != nil && a.settings.restoreSB {
		if h, err := slot.History(rp.History); err == nil && len(h) > 0 {
			when := saved.Local().Format("Jan 2 15:04")
			history = append(h, "\x1b[0;2m── restored from "+when+" ──\x1b[0m\r\n"...)
		}
	}
	p := a.newPaneWith(t, nil, dir, cols, rows, history)
	if p == nil {
		return nil
	}
	switch {
	case cmd != "":
		a.typeWhenReady(p, cmd+"\r")
	case len(rp.Remote) > 0:
		if line := restore.ShellQuote(rp.Remote); line != "" {
			a.typeWhenReady(p, line)
		}
	}
	return p
}

// typeWhenReady writes text to p once its shell has printed its prompt and
// gone quiet, or after typeWithin.
func (a *App) typeWhenReady(p *Pane, text string) {
	start := monoNs()
	coreglib.TimeoutAdd(100, func() bool {
		if p.removeOnce || p.exited {
			return false
		}
		now, last := monoNs(), p.outLast.Load()
		if (last == 0 || now-last < int64(typeAfterQuiet)) && now-start < int64(typeWithin) {
			return true
		}
		a.restorer.typed = append(a.restorer.typed, typedReport{Pane: p.id, Text: text})
		if err := p.s.WriteInput([]byte(text)); err != nil {
			p.inputRefused(err)
		}
		return false
	})
}

// saveTick runs every saveEvery while the window lives.
func (a *App) saveTick() bool {
	if a.finished || a.restorer.slot == nil {
		return false
	}
	a.restorer.ticks++
	a.saveRestore(a.restorer.ticks%historyEvery == 0, false)
	return true
}

// saveRestore writes the layout when it changed, and with history the
// scrollback of panes with output since it was last written: off the main
// thread, unless final.
func (a *App) saveRestore(history, final bool) {
	r := &a.restorer
	if r.slot == nil || !a.settings.restore {
		return
	}
	if history && a.settings.restoreSB && (final || r.busy.CompareAndSwap(false, true)) {
		var jobs []*Pane
		keep := map[string]bool{}
		for _, t := range a.tabs {
			for _, p := range t.panes() {
				if p.exited || p.removeOnce {
					continue
				}
				keep[historyName(p)] = true
				st := p.s.Status()
				if (st.Agent != "" && restore.Resumable(st.Agent) && st.Host == "") || p.outLast.Load() == p.histAt {
					continue // an agent prints its conversation again; or nothing new
				}
				p.histAt = p.outLast.Load()
				jobs = append(jobs, p)
			}
		}
		slot := r.slot
		write := func() {
			for _, p := range jobs {
				if err := slot.SaveHistory(historyName(p), p.s.History(0)); err != nil {
					slog.Debug("restore: save scrollback", "pane", p.id, "err", err)
					continue
				}
				p.histSaved.Store(true)
			}
			slot.KeepHistory(keep)
		}
		if final {
			write()
		} else {
			go func() {
				defer r.busy.Store(false)
				write()
			}()
		}
	}
	w := a.snapshotWindow()
	if len(w.Tabs) == 0 {
		return
	}
	// Compare before Save adds its time stamp.
	key, err := json.Marshal(w)
	if err != nil || string(key) == string(r.last) {
		return
	}
	if err := r.slot.Save(w); err != nil {
		slog.Debug("restore: save", "err", err)
		return
	}
	r.last = key
	r.saves++
}

func historyName(p *Pane) string { return fmt.Sprintf("pane-%d", p.id) }

// finishRestore writes the last snapshot as bunk-gui quits. The user's
// quit with other windows still open, or a window without a live pane,
// leaves none; anything else keeps it for the next start.
func (a *App) finishRestore(reason string) {
	r := &a.restorer
	if r.slot == nil {
		return
	}
	a.saveRestore(true, true)
	if !a.settings.restore || len(a.snapshotWindow().Tabs) == 0 || reason == "quit" && r.store.Running(r.slot) > 0 {
		if err := r.slot.Remove(); err != nil {
			slog.Debug("restore: remove", "err", err)
		}
	} else {
		r.slot.Release()
	}
	r.slot = nil
}

// snapshotWindow describes the window's live panes.
func (a *App) snapshotWindow() restore.Window {
	w := restore.Window{Maximized: a.win.IsMaximized()}
	w.Width, w.Height = a.win.DefaultSize()
	for _, t := range a.tabs {
		var panes []*Pane
		root := a.snapshotNode(t.root, &panes)
		if root == nil {
			continue
		}
		rt := restore.Tab{Name: t.name, Root: root, Zoom: t.zoom != nil}
		for i, p := range panes {
			if p == t.focus {
				rt.Focus = i
			}
		}
		if t == a.active {
			w.Selected = len(w.Tabs)
		}
		w.Tabs = append(w.Tabs, rt)
	}
	return w
}

// snapshotNode describes n, leaving out panes whose child has exited,
// and appends its panes to panes.
func (a *App) snapshotNode(n *node, panes *[]*Pane) *restore.Node {
	switch {
	case n == nil:
		return nil
	case n.pane != nil:
		p := n.pane
		if p.exited || p.removeOnce || p.s == nil {
			return nil
		}
		*panes = append(*panes, p)
		return &restore.Node{Pane: a.snapshotPane(p)}
	}
	na, nb := a.snapshotNode(n.a, panes), a.snapshotNode(n.b, panes)
	switch {
	case na == nil:
		return nb
	case nb == nil:
		return na
	}
	ratio := n.ratio // not laid out yet: the restored one
	if ratio <= 0 {
		ratio = 0.5
		start, end := n.paned.StartChild(), n.paned.EndChild()
		size := func(w gtk.Widgetter) int {
			if n.vertical {
				return gtk.BaseWidget(w).Height()
			}
			return gtk.BaseWidget(w).Width()
		}
		if start != nil && end != nil {
			if total := size(start) + size(end); total > 0 {
				ratio = float64(n.paned.Position()) / float64(total)
			}
		}
		ratio = min(max(ratio, 0.05), 0.95)
	}
	return &restore.Node{Vertical: n.vertical, Ratio: ratio, A: na, B: nb}
}

// remoteClients are the foreground programs whose command line a restored
// pane types again.
var remoteClients = map[string]bool{"ssh": true, "mosh": true}

func (a *App) snapshotPane(p *Pane) *restore.Pane {
	st := p.s.Status()
	rp := &restore.Pane{Cwd: st.Cwd, Agent: st.Agent, AgentSession: st.AgentSession, Host: st.Host}
	if st.Host != "" {
		rp.Cwd = "" // the remote agent's directory is on its host
	}
	if rp.Cwd == "" {
		rp.Cwd = p.s.Cwd()
	}
	if remoteClients[st.Process] && st.Host == "" && p.s.Cmd != nil && p.s.Cmd.Process != nil {
		rp.Remote = session.FgProcessCmdline(p.s.Cmd.Process.Pid)
	}
	if p.histSaved.Load() {
		rp.History = historyName(p)
	}
	return rp
}

// errNoRestore is the error of a restore step when restore is off.
var errNoRestore = errors.New("restore is off")

// scriptRestore runs the script action "restore accept|dismiss|save".
func (a *App) scriptRestore(arg string) error {
	if a.restorer.slot == nil && a.restorer.store == nil {
		return errNoRestore
	}
	switch strings.TrimSpace(arg) {
	case "accept":
		a.acceptRestore()
	case "dismiss":
		a.dismissRestore()
	case "save":
		a.saveRestore(true, true)
	default:
		return fmt.Errorf("restore %q: want accept, dismiss or save", arg)
	}
	return nil
}
