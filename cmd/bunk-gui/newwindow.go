package main

// New windows. bunk-gui is a non-unique application: every window is its
// own process, with its own tabs and notify socket. "New window" (menu,
// Ctrl+Shift+N) starts bunk-gui again in the active pane's directory; the
// desktop entry's new-window action does the same from the shell's app
// menu.

import (
	"log/slog"
	"os"
	"os/exec"
	"syscall"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
)

// openWindow starts another bunk-gui. It records the window's directory in
// --report and starts the process only off the test compositor.
func (a *App) openWindow() {
	dir := a.cwdForNew()
	a.rep.Windows = append(a.rep.Windows, dir)
	if err := launchRefused(); err != nil {
		slog.Debug("window not opened", "dir", dir, "err", err)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		a.toast("Could not open a window")
		slog.Warn("new window: find bunk-gui", "err", err)
		return
	}
	// The token lets the new window take the focus, as a launch from the
	// shell's app menu does.
	var token string
	ctx := gdk.DisplayGetDefault().AppLaunchContext()
	if info, err := gio.AppInfoCreateFromCommandline(exe, "bunk-gui", gio.AppInfoCreateNone); err == nil {
		token = ctx.StartupNotifyID(info, nil)
	}
	cmd := windowCmd(exe, dir, a.cfg.configFile, token)
	if err := cmd.Start(); err != nil {
		if token != "" {
			ctx.LaunchFailed(token)
		}
		a.toast("Could not open a window")
		slog.Warn("new window", "err", err)
		return
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			slog.Debug("new window exited", "pid", cmd.Process.Pid, "err", err)
		}
	}()
}

// windowCmd is the command of a new window: bunk-gui in dir, with this
// window's settings file and the activation token. It runs in its own
// session, so a hang-up of the terminal this window started from leaves
// it running.
func windowCmd(exe, dir, configFile, token string) *exec.Cmd {
	cmd := exec.Command(exe)
	if configFile != configPath() {
		cmd.Args = append(cmd.Args, "--config", configFile)
	}
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if token != "" {
		cmd.Env = append(cmd.Env, "XDG_ACTIVATION_TOKEN="+token, "DESKTOP_STARTUP_ID="+token)
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}
