package main

// Banners above the panes: a line of text with two buttons that asks
// without blocking the terminal. The restore offer (restore.go) and the
// agent hooks offer (prefs_hooks.go) use them, stacked in that order.

import (
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

type banner struct {
	rev    *gtk.Revealer
	label  *gtk.Label
	accept *gtk.Button
}

// newBanner returns a hidden banner whose buttons run onDismiss and
// onAccept.
func newBanner(dismiss, accept string, onDismiss, onAccept func()) *banner {
	b := &banner{rev: gtk.NewRevealer(), label: gtk.NewLabel("")}
	b.rev.SetTransitionType(gtk.RevealerTransitionTypeSlideDown)
	row := gtk.NewBox(gtk.OrientationHorizontal, 8)
	row.AddCSSClass("bunk-banner")
	b.label.SetHExpand(true)
	b.label.SetXAlign(0)
	b.label.SetWrap(true)
	row.Append(b.label)
	no := gtk.NewButtonWithLabel(dismiss)
	no.SetFocusOnClick(false)
	no.SetVAlign(gtk.AlignCenter)
	no.ConnectClicked(onDismiss)
	row.Append(no)
	b.accept = gtk.NewButtonWithLabel(accept)
	b.accept.AddCSSClass("suggested-action")
	b.accept.SetFocusOnClick(false)
	b.accept.SetVAlign(gtk.AlignCenter)
	b.accept.ConnectClicked(onAccept)
	row.Append(b.accept)
	b.rev.SetChild(row)
	return b
}

func (b *banner) show(text string) {
	b.label.SetText(text)
	b.rev.SetRevealChild(true)
}

func (b *banner) hide() { b.rev.SetRevealChild(false) }

// shown reports whether the banner is up; false for a nil banner.
func (b *banner) shown() bool { return b != nil && b.rev.RevealChild() }

// buildContent returns the box of the banners and the panes.
func (a *App) buildContent(stack gtk.Widgetter) *gtk.Box {
	box := gtk.NewBox(gtk.OrientationVertical, 0)
	box.SetHExpand(true)
	box.SetVExpand(true)
	a.restorer.banner = newBanner("Dismiss", "Restore", a.dismissRestore, a.acceptRestore)
	a.hookOffer.banner = newBanner("Not now", "Update", a.declineHooks, a.acceptHooks)
	box.Append(a.restorer.banner.rev)
	box.Append(a.hookOffer.banner.rev)
	box.Append(stack)
	return box
}
