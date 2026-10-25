// timing.go - the intervals of a session's timers.
package session

import "time"

// ProcessPoll is how often a session reads its foreground process: the
// agent, directory, container and SSH host it shows, and the owner of the
// kitty keyboard flags.
const ProcessPoll = time.Second

// Timing sets the intervals of a session's timers (Options.Timing). A zero
// field takes its default, so front ends leave Timing empty; tests shorten
// it to wait less.
type Timing struct {
	Poll         time.Duration // foreground-process poll; default ProcessPoll
	Idle         time.Duration // silence before working turns idle; default IdleAfter
	HangupGrace  time.Duration // Close: from SIGHUP to killing the group; default HangupGrace
	RecentlyLeft time.Duration // a failure this soon after the agent left is the agent's; default recentlyLeft
	SyncRelease  time.Duration // release of an abandoned synchronized update; default SyncUpdateTimeout
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func (t Timing) poll() time.Duration         { return orDefault(t.Poll, ProcessPoll) }
func (t Timing) idle() time.Duration         { return orDefault(t.Idle, IdleAfter) }
func (t Timing) hangupGrace() time.Duration  { return orDefault(t.HangupGrace, HangupGrace) }
func (t Timing) recentlyLeft() time.Duration { return orDefault(t.RecentlyLeft, recentlyLeft) }
func (t Timing) syncRelease() time.Duration  { return orDefault(t.SyncRelease, SyncUpdateTimeout) }
