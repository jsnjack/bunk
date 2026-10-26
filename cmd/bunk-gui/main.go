// Command bunk-gui is a GTK4 terminal for coding agents, around bunk's
// session core.
//
// One AdwApplicationWindow without a title bar holds tabs; a tab is a BSP
// tree of panes (GtkPaned), and a pane is a TermView (view.go): a widget that
// renders one session with OpenGL into a texture at the exact device-pixel
// size of the widget, so text stays on the pixel grid at fractional scales.
// Tabs show their agent's status in a bar on top or a rail on the side
// (chrome.go). The emulator, PTY, scrollback and agent status come from
// bunk/internal/session; keys and mouse reports are encoded by
// bunk/internal/input.
//
//	bunk-gui                                # run $SHELL
//	bunk-gui --command 'htop'               # run sh -c CMD; quit when it exits
//	bunk-gui --command 'cat big.txt; sleep 1' --stats --timeout 10s
//
// Default keys follow bunk's TUI and Ptyxis (bindings.go): F2 new tab;
// Ctrl+Shift+W/N close tab, new window; Ctrl+Left/Right and Alt+1…0 switch
// tabs; Ctrl+Shift+PgUp/PgDn move the tab; Ctrl+Shift+O switcher; Alt+A
// next tab needing you; Ctrl+Shift+B expand/collapse the rail; F1 / Alt+F1
// split (in the same container); F12 zoom pane; Alt+arrows move between
// panes; Ctrl+F find; Ctrl+C/V and Ctrl+Shift+C/V copy/paste (Ctrl+C
// copies only a selection); Ctrl+Shift+A select all; Shift+PgUp/PgDn
// scroll; Ctrl+= / Ctrl+- / Ctrl+0 text size; F10 menu; F11 fullscreen;
// Ctrl+Q quit.
package main

import "runtime"

// Version is set at build time via ldflags.
var Version = "dev"

// GTK must be called from the thread that initialised it.
func init() { runtime.LockOSThread() }

func main() {
	Execute()
}
