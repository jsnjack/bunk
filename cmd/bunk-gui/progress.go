package main

import (
	"fmt"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/session"
)

// Progress that programs report with OSC 9;4 (session.Progress), shown as
// a thin bar under each tab in the bar, the rail and the collapsed rail,
// as Ptyxis shows it in its tabs.

// tabProgress is the progress a tab shows: its agent pane's, else the
// first of its panes that reports one, with a failure before the rest.
func tabProgress(t *Tab, agent *Pane) session.Progress {
	var got session.Progress
	if agent != nil {
		got = agent.status().Progress
	}
	for _, p := range t.panes() {
		pr := p.status().Progress
		if pr.State == session.ProgressError || got.State == session.ProgressNone {
			got = pr
		}
		if got.State == session.ProgressError {
			break
		}
	}
	return got
}

// progressName is a progress for the report and the tooltip: "42%",
// "failed at 42%", "paused at 42%", "busy"; "" for none.
func progressName(p session.Progress) string {
	switch p.State {
	case session.ProgressNormal:
		return fmt.Sprintf("%d%%", p.Percent)
	case session.ProgressError:
		return fmt.Sprintf("failed at %d%%", p.Percent)
	case session.ProgressPaused:
		return fmt.Sprintf("paused at %d%%", p.Percent)
	case session.ProgressIndeterminate:
		return "busy"
	}
	return ""
}

// progressBar draws a progress under a tab: the part done in blue, red
// when it failed and amber when paused; a progress without a percentage
// fills the bar, dimmed.
func progressBar(p session.Progress) *gtk.ProgressBar {
	b := gtk.NewProgressBar()
	b.AddCSSClass("tabprogress")
	b.SetCanTarget(false)
	frac := float64(p.Percent) / 100
	switch p.State {
	case session.ProgressError:
		b.AddCSSClass("error")
	case session.ProgressPaused:
		b.AddCSSClass("paused")
	case session.ProgressIndeterminate:
		b.AddCSSClass("busy")
		frac = 1
	}
	b.SetFraction(frac)
	return b
}
