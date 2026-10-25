package main

// config.toml is watched: edits apply to the running window. Key bindings
// always reload; the layout and the font apply when the file changed them
// (and no command-line flag set them).

import (
	"context"
	"log/slog"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
)

func (a *App) watchConfig() {
	if a.cfg.configFile == "" {
		return
	}
	mon, err := gio.NewFileForPath(a.cfg.configFile).MonitorFile(context.Background(), gio.FileMonitorWatchMoves)
	if err != nil {
		slog.Warn("config: not watching for changes", "path", a.cfg.configFile, "err", err)
		return
	}
	gen := 0
	gio.BaseFileMonitor(mon).ConnectChanged(func(_, _ gio.Filer, ev gio.FileMonitorEvent) {
		// Editors write in several steps: reload once things settle.
		gen++
		mine := gen
		coreglib.TimeoutAdd(150, func() bool {
			if mine == gen {
				a.reloadSettings()
			}
			return false
		})
	})
	a.cfgMonitor = mon
}

func (a *App) reloadSettings() {
	if a.finished {
		return
	}
	st, err := loadSettings(a.cfg.configFile)
	old := a.fileSettings
	a.fileSettings = st
	a.rep.Reloads++
	a.settings.Keys, a.settings.keymap, a.settings.was = st.Keys, st.keymap, st.was
	a.settings.Agents = st.Agents
	if (st.Font != old.Font || st.FontSize != old.FontSize || st.TextZoom != old.TextZoom) && a.cfg.font == "" {
		a.settings.Font, a.settings.FontSize, a.settings.TextZoom = st.Font, st.FontSize, st.TextZoom
		a.fontSize = st.startSize()
		a.fontDesc = a.font()
		for _, p := range a.panes {
			if p != nil && !p.removeOnce {
				p.queueRender()
			}
		}
	}
	if (st.Tabs != old.Tabs || st.RailOpen != old.RailOpen) && a.cfg.layout == "" {
		a.setLayout(st.Tabs, st.RailOpen)
	} else {
		a.buildChrome() // the menu and tooltips show the keys
	}
	if a.shortcutsWin != nil {
		a.shortcutsWin.refresh()
	}
	a.configErr = err
	if err != nil {
		a.toast("Settings: " + err.Error())
	}
}
