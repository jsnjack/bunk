// Package session runs one terminal session: a child process on a PTY, the
// vt10x emulator that tracks its screen, its scrollback, and the view state
// (scroll offset, selection, search highlights) that every front end shares.
// Front ends connect through Hooks and read state with the session locked.
package session

import (
	"bunk/internal/graphics"
	"bunk/internal/input"
	"bunk/internal/vt10x"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
)

// DefaultScrollbackLines is the default maximum number of scrollback lines
// retained per pane.  Overridden by the "scrollback" config field.
// Memory cost per pane:
//   - sbRing:  scrollbackLines × cols × ~24 bytes (rendered glyphs, on demand)
const DefaultScrollbackLines = 10_000

// SelPos identifies a cell in the pane's unified virtual coordinate space.
//
//	row 0 … sb.count-1  → scrollback ring entries (oldest … newest)
//	row sb.count + r    → live terminal row r
//
// This coordinate is stable across scrolls: as a line scrolls off the live
// view into the ring, its virtual row index does not change.
type SelPos struct {
	Row, Col int
}

// Session is one terminal session: a shell process attached to a PTY,
// the emulator that tracks its screen, scrollback, and the view state
// (scroll offset, selection, search highlights) every front end shares.
type Session struct {
	ID int

	PTY      *os.File      // PTY master: ReadLoop reads output; writes go through out
	out      ptyWriter     // queues replies and input for the PTY (writer.go)
	ptmxOnce sync.Once     // ensures PTY master fd is closed exactly once
	killOnce sync.Once     // Close arms the SIGKILL fallback once
	Cmd      *exec.Cmd     // the shell process
	exited   chan struct{} // closed by waitForExit; stops trackFgProcess at once

	// The mutex serialises all access to Term (writes from the reader goroutine
	// and reads from front ends) and every field marked "protected by mu".
	sync.Mutex
	Term vt10x.Terminal // VT100/ANSI state machine — also tracks bracketed paste,
	// sync update, cursor shape, and all DEC private modes.
	Dead bool // true once the shell process has exited

	// Scrollback buffer - lines that have scrolled off the vt10x grid top.
	// Protected by mu.
	Scrollback      Ring // ring buffer of captured rows
	ScrollOffset    int  // 0 = live view; N = N lines above live view
	ScrollbackLines int  // max scrollback rows (from config)

	// images keeps placement bitmaps (Options.ImageBytes); nil without.
	// Protected by mu.
	images *graphics.Store

	// Text selection state.  Protected by mu.
	// SelAnchor is where Button1 was pressed; SelCursor tracks the drag endpoint.
	// Both are in virtual (scrollback+live) row/col coordinates.
	SelAnchor, SelCursor SelPos
	SelActive            bool

	// SearchHL holds span-based match highlights for this pane.
	// nil when no search is active.  Protected by mu.
	SearchHL    *Highlight
	SearchHLGen int // incremented on every searchHL assignment

	// oscScan is the per-pane OSC pre-scanner (value, no alloc).
	// Forwards OSC 7 (CWD), OSC 8 (hyperlinks), OSC 52 (clipboard) to the
	// host terminal so those features work through the multiplexer.
	oscScan oscScanner

	// Process and container tracking.  All protected by mu.
	FgProcess     string // name of the current foreground process (e.g. "ssh", "sudo")
	ContainerID   string // active container name (updated live by trackFgProcess)
	ContainerType string // "toolbox", "distrobox", "podman", "lxc", or ""
	SSHHost       string // remote hostname when fgProcess is "ssh" or "mosh"

	// BaseContainerType/ID are set once at startup and represent the static
	// container context of the pane itself (e.g. bunk running inside a
	// Toolbox container).  They are used as fallback when the foreground
	// process is not inside any container.  Protected by mu.
	BaseContainerType string
	BaseContainerID   string

	// altEntryCursor remembers the primary-screen cursor position at the moment
	// the pane entered the alternate screen.  vt10x shares a single saved-cursor
	// slot for both ESC-7/8 (DECSC/DECRC) and \x1b[?1049h/l, so the primary
	// cursor is not reliably preserved across an alt-screen round-trip.
	// We restore it manually after \x1b[?1049l so programs like gh-copilot
	// (which render inline at the current cursor position) start in the right
	// place.  Protected by mu.
	altEntryCursorX, altEntryCursorY int

	// NeedsSync is set when the pane exits the alternate screen so that the
	// next render call uses screen.Sync() (full repaint) instead of
	// screen.Show() (differential).  This clears any residual background
	// colours that alt-screen apps like btop may have left in the terminal.
	// Protected by mu.
	NeedsSync bool

	// feed frames output passed to Feed. ReadLoop keeps its own framer.
	feed ptyStream

	// Agent status (status.go). Protected by mu.
	agents            []AgentSpec // Options.Agents, checked before DefaultAgents
	agent             string      // known agent in the foreground, or ""
	needsYou, failed  bool        // attention requested / last command failed
	notice            string      // text of the latest attention request
	lastOutput        time.Time   // when the last chunk of output arrived
	cwd, repo, branch string      // foreground cwd and its git work tree
	agentGone         bool        // the failed agent has exited; keep showing it
	lastAgent         string      // the agent that most recently left the foreground
	agentLeft         time.Time   // when lastAgent left
	report            agentReport // the agent's latest explicit report (Notify)
	statusHook        func()      // Hooks.StatusChanged, set by Run, for Notify
	shown             agentView   // the agent state StatusChanged last announced

	// syncDeadline releases an abandoned synchronized update (DEC 2026).
	// Protected by mu.
	syncDeadline time.Time

	// KittyStack is the per-pane kitty keyboard protocol flag stack.
	// When pane apps send \x1b[=<flags>u (push), we push onto this stack
	// and respond with the saved value on \x1b[?u (query).
	// On \x1b[<N>u (pop) we pop N levels.
	// All kitty keyboard sequences are stripped before vt10x sees them —
	// vt10x misinterprets the 'u' final byte as DECRC (restore cursor).
	// Protected by mu.
	KittyStack     []int
	KittyOwnerPGID int

	// themeFG/BG/Cursor are the default colours the session reports to applications in
	// XParseColor format ("rgb:<rrrr>/<gggg>/<bbbb>") when no dynamic OSC
	// override is active.  Empty means "unknown" and suppresses the reply.
	ThemeFG     string
	ThemeBG     string
	ThemeCursor string

	// backspace is the key the front end sends for Backspace; XTGETTCAP
	// "kbs" reports its encoding.
	backspace input.Key

	// timing holds the intervals of the session's timers (Options.Timing).
	// Set before the session runs; read without the lock.
	timing Timing
}

// Options describe a new Session.
type Options struct {
	ID         int
	Cols, Rows int      // emulator and PTY size in cells
	Scrollback int      // scrollback capacity in lines
	Dir        string   // working directory; "" keeps the current one
	Args       []string // argv for the child; empty runs $SHELL
	Env        []string // extra KEY=VALUE entries for the child environment
	// ThemeFG, ThemeBG and ThemeCursor answer OSC 10/11/12 queries in
	// XParseColor form; "" leaves the colour unknown.
	ThemeFG, ThemeBG, ThemeCursor string
	CellAspect                    []float64 // cell height/width for virtual pixels
	// CellPixels is the real size of a cell in pixels (width, height), for
	// the PTY's pixel size and CSI 14/16 t replies. Zero: virtual pixels,
	// 8 wide and as high as CellAspect gives (bunk's TUI).
	CellPixels [2]int
	Agents     []AgentSpec // extra agent CLIs to recognise, checked before DefaultAgents
	// Backspace is the key the front end sends for the Backspace key, so
	// XTGETTCAP "kbs" matches it: input.KeyBS (BS, the default, as bunk's
	// tcell front end sends) or input.KeyBackspace (DEL).
	Backspace input.Key
	// NotifySocket and NotifySession are exported to the child as
	// BUNK_NOTIFY_SOCKET and BUNK_NOTIFY_SESSION, so agent hooks can report
	// their state (internal/notify). Inherited values are always removed;
	// "" exports nothing.
	NotifySocket, NotifySession string
	// ImageBytes keeps the bitmaps of image placements, up to this many
	// bytes, so the front end can draw them at full resolution (Images).
	// Zero keeps only the cells' colour samples, as bunk's TUI draws them.
	ImageBytes int
	// InputBytes caps the input queued for a child that does not read it
	// (WriteInput); zero means DefaultInputBytes.
	InputBytes int
	// Timing sets the intervals of the session's timers; zero fields take
	// the defaults (ProcessPoll, IdleAfter, HangupGrace, SyncUpdateTimeout).
	Timing Timing
}

// Hooks connect a Session to its front end. Every hook is optional.
type Hooks struct {
	// Applied runs with the session locked, right after a chunk of output
	// reached the emulator. scrolled reports a scrolled-back view; syncing
	// reports an open synchronized update.
	Applied func(chunk []byte, scrolled, syncing bool)
	// Output runs after the lock is released, for repaint scheduling.
	Output func(chunk []byte, scrolled, syncing bool)
	// OSC receives each OSC sequence worth forwarding (7, 8, 52, 133). The
	// slice is reused after the call.
	OSC func(seq []byte)
	// Redraw asks for a repaint outside the output path: an expired
	// synchronized update or a foreground-process change.
	Redraw func()
	// Exited runs once after the child process exits.
	Exited func()
	// Closed runs once when ReadLoop returns: the PTY is closed and all of
	// the child's output has been applied. It can come before or after
	// Exited, and never comes while another process still holds the PTY,
	// unless Close closed the session.
	Closed func()
	// StatusChanged runs, without the lock, when Status may have changed:
	// a new agent, directory or branch, output starting or stopping, an
	// attention request, or an agent's report (Notify). It must not block.
	StatusChanged func()
}

// Start spawns the child process on a new PTY and creates its emulator.
// Call Run to start reading output.
func Start(o Options) (*Session, error) {
	args := o.Args
	if len(args) == 0 {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		args = []string{shell}
	}

	cmd := exec.Command(args[0], args[1:]...)
	if o.Dir != "" {
		cmd.Dir = o.Dir
	}

	// Build the child environment.
	// Filter out TERM and COLORTERM from the host before setting our own.
	// (Simply appending would not override them on most shells/kernels.)
	// • TERM=xterm-256color - the emulation profile we advertise.
	// • COLORTERM=truecolor - tells colour-aware apps 24-bit RGB works.
	// • BUNK_NOTIFY_SOCKET/BUNK_NOTIFY_SESSION come only from Options, so a
	//   session never reports to the front end that started its parent.
	filtered := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TERM=") && !strings.HasPrefix(e, "COLORTERM=") &&
			!strings.HasPrefix(e, "VTE_VERSION=") && !strings.HasPrefix(e, EnvNotifySocket+"=") &&
			!strings.HasPrefix(e, EnvNotifySession+"=") {
			filtered = append(filtered, e)
		}
	}
	// VTE_VERSION tells apps like Neovim that we support VTE 0.78+ features:
	// SGR 4:3 curly underline, SGR 58 colored underline, kitty keyboard protocol.
	cmd.Env = append(filtered, "TERM="+TermName, "COLORTERM=truecolor")
	cmd.Env = append(cmd.Env, o.Env...)
	cmd.Env = append(cmd.Env, "VTE_VERSION=8203")
	if o.NotifySocket != "" {
		cmd.Env = append(cmd.Env, EnvNotifySocket+"="+o.NotifySocket)
	}
	if o.NotifySession != "" {
		cmd.Env = append(cmd.Env, EnvNotifySession+"="+o.NotifySession)
	}

	cellWidth, cellHeight := VirtualCellPixels(o.CellAspect)
	if o.CellPixels[0] > 0 && o.CellPixels[1] > 0 {
		cellWidth, cellHeight = o.CellPixels[0], o.CellPixels[1]
	}
	ptmx, err := pty.StartWithSize(cmd, Winsize(o.Cols, o.Rows, cellWidth, cellHeight))
	if err != nil {
		return nil, fmt.Errorf("pty.StartWithSize: %w", err)
	}
	// A pollable master lets Close end ReadLoop (ptyfd_linux.go).
	if p, err := pollable(ptmx); err != nil {
		L().Warn("session: PTY master stays blocking", "id", o.ID, "err", err)
	} else {
		ptmx = p
	}

	// Ordinary device queries are answered by CaptureAndWrite using session
	// state and theme colours. Graphics acknowledgements are emitted by the
	// decoder, independently, so requests are answered exactly once.
	s := &Session{
		ID:              o.ID,
		PTY:             ptmx,
		Cmd:             cmd,
		ScrollbackLines: o.Scrollback,
		Scrollback:      Ring{MaxLines: o.Scrollback},
		ThemeFG:         o.ThemeFG,
		ThemeBG:         o.ThemeBG,
		ThemeCursor:     o.ThemeCursor,
		backspace:       o.Backspace,
		agents:          o.Agents,
		exited:          make(chan struct{}),
		timing:          o.Timing,
	}
	s.out.inputCap = o.InputBytes
	s.oscScan.status = s.onStatusOSC
	s.Term = vt10x.New(vt10x.WithSize(o.Cols, o.Rows), vt10x.WithScrollCallback(s.OnScrollRow), vt10x.WithBellCallback(s.onBell),
		vt10x.WithScrollbackClearCallback(s.OnScrollbackClear), vt10x.WithGraphicsReply(s.reply), vt10x.WithCellPixels(cellWidth, cellHeight),
		vt10x.WithLinkReferences(s.MarkLinks), s.imageStore(o.ImageBytes))

	// One-time container detection: read the shell process's own environ.
	if cmd.Process != nil {
		if ct := detectContainerFromPID(cmd.Process.Pid); ct != "" {
			s.ContainerType = ct
			s.BaseContainerType = ct
			if ct == "lxc" {
				name := lxdContainerName()
				s.ContainerID = name
				s.BaseContainerID = name
			}
			L().Debug("session: container detected", "id", s.ID, "type", s.ContainerType, "name", s.ContainerID)
		}
	}
	return s, nil
}

// Feed passes PTY output through the same framing as ReadLoop and writes it
// to the emulator, answering queries. Its framing state carries across calls,
// for callers that supply output themselves. Must be called with the session
// locked.
func (s *Session) Feed(chunk []byte) {
	s.CaptureAndWrite(s.feed.scan(chunk))
}

// Resize changes the emulator and PTY size, reflowing stored lines first so
// the application redraws over the reflowed screen. It reports whether stored
// cells were reflowed to a new width.
func (s *Session) Resize(cols, rows int) (reflowed bool) {
	s.Lock()
	reflowed = s.ResizeAndReflow(cols, rows)
	cw, ch := s.Term.CellPixels()
	s.Unlock()
	if s.PTY != nil {
		if err := Setsize(s.PTY, Winsize(cols, rows, cw, ch)); err != nil {
			L().Debug("session: resize PTY", "id", s.ID, "err", err)
		}
	}
	return reflowed
}

// SetCellPixels changes the size of a cell in pixels (a front end's font
// or scale changed). The emulator places images with it, CSI 14/16 t
// report it, and the PTY's pixel size follows, which signals the child
// with SIGWINCH when it changed.
func (s *Session) SetCellPixels(width, height int) {
	s.Lock()
	defer s.Unlock()
	s.Term.SetCellPixels(width, height)
	if s.PTY != nil {
		cols, rows := s.Term.Size()
		cw, ch := s.Term.CellPixels()
		if err := Setsize(s.PTY, Winsize(cols, rows, cw, ch)); err != nil {
			L().Debug("session: resize PTY", "id", s.ID, "err", err)
		}
	}
}

// ContentRows returns the number of live rows up to the last one with
// content or the cursor. Must be called with the session locked.
func (s *Session) ContentRows() int {
	cols, rows := s.Term.Size()
	return findContentRows(s.Term, cols, rows)
}

// Run starts the goroutines that read output, wait for the child to exit
// and track its foreground process. Closing done stops them on shutdown.
func (s *Session) Run(h Hooks, done <-chan struct{}) {
	s.Lock()
	s.statusHook = h.StatusChanged
	s.Unlock()
	go s.ReadLoop(h)
	go s.waitForExit(h)
	go s.trackFgProcess(h, done)
}

// ReadLoop is the VT100 parsing bridge (write side). It returns when the
// PTY closes. For each chunk of raw PTY bytes it:
//  1. Frames controls and passes OSC sequences to h.OSC.
//  2. Feeds the bytes into vt10x and answers terminal queries.
//  3. Tracks synchronized updates (DEC 2026).
//  4. Calls h.Applied under the lock and h.Output after it.
func (s *Session) ReadLoop(h Hooks) {
	buf := make([]byte, 32768)
	var stream ptyStream
	emitOSC := h.OSC
	if emitOSC == nil {
		emitOSC = func([]byte) {}
	}
	var syncTimer, idleTimer *time.Timer
	defer func() {
		if syncTimer != nil {
			syncTimer.Stop()
		}
		if idleTimer != nil {
			idleTimer.Stop()
		}
	}()

	for {
		n, err := s.PTY.Read(buf)
		if n > 0 {
			chunk := stream.scan(buf[:n])
			if len(chunk) == 0 {
				if err != nil {
					L().Debug("readPTY: PTY read error (shell exited)", "pane", s.ID, "err", err)
					break
				}
				continue
			}

			// Check the level first: quoting a 32 KiB chunk is costly.
			if L().Enabled(context.Background(), LevelTrace) {
				L().Log(context.Background(), LevelTrace, "readPTY: chunk", "pane", s.ID, "data", fmt.Sprintf("%q", chunk))
			}

			// Always scan: attention requests (OSC 9, 99, 777, 133) feed Status
			// even when the front end forwards nothing.
			s.oscScan.Scan(chunk, emitOSC)

			// Query replies, mode flags and the sync state change together
			// under one lock, before any front end can read them.
			now := time.Now()
			s.Lock()
			s.CaptureAndWrite(chunk)
			s.lastOutput = now
			watched := s.agent != ""
			agentChanged := s.takeAgentChange(now)
			scrolled := s.ScrollOffset > 0
			syncing := s.Term.Mode()&vt10x.ModeSync != 0
			if syncing && s.syncDeadline.IsZero() {
				release := s.timing.syncRelease()
				until := now.Add(release)
				s.syncDeadline = until
				syncTimer = time.AfterFunc(release, func() {
					s.expireSyncUpdate(until)
					if h.Redraw != nil {
						h.Redraw()
					}
				})
			} else if !syncing {
				s.syncDeadline = time.Time{}
				if syncTimer != nil {
					syncTimer.Stop()
				}
			}
			if h.Applied != nil {
				h.Applied(chunk, scrolled, syncing)
			}
			s.Unlock()
			if h.Output != nil {
				h.Output(chunk, scrolled, syncing)
			}
			// An agent's state follows its output: report output starting,
			// an attention request or a failure, and idle once output has
			// stopped for Timing.Idle. trackFgProcess reports whatever this
			// misses, such as an agent that went quiet before it was known.
			if agentChanged && h.StatusChanged != nil {
				h.StatusChanged()
			}
			if watched && h.StatusChanged != nil {
				if idleTimer == nil {
					idleTimer = time.AfterFunc(s.timing.idle(), func() {
						if s.agentChangedNow() {
							h.StatusChanged()
						}
					})
				} else {
					idleTimer.Reset(s.timing.idle())
				}
			}
		}
		if err != nil {
			L().Debug("readPTY: PTY read error (shell exited)", "pane", s.ID, "err", err)
			break
		}
	}
	s.closePTX()
	if h.Closed != nil {
		h.Closed()
	}
}

// SyncUpdateTimeout releases a synchronized update (DEC 2026) that an
// application left open (default of Timing.SyncRelease).
const SyncUpdateTimeout = time.Second

func (s *Session) expireSyncUpdate(until time.Time) {
	s.Lock()
	defer s.Unlock()
	if s.syncDeadline.Equal(until) {
		if _, err := s.Term.Write([]byte("\x1b[?2026l")); err != nil {
			L().Warn("expire synchronized update", "pane", s.ID, "err", err)
		}
		s.syncDeadline = time.Time{}
	}
}

// OnScrollRow is the vt10x scroll callback installed in Start via
// WithScrollCallback.  It is called synchronously inside vt10x.Write()
// for each row that scrolls off the top of the primary screen (orig == 0
// in vt10x.scrollUp), before that row's backing storage is cleared.
//
// The session lock is already held by CaptureAndWrite, which is the
// only caller of term.Write under that lock; no additional locking is needed.
//
// Alt-screen scrolls must be ignored: TUI apps (vim, htop, less) use the
// alt-screen and their scroll events must not populate primary scrollback.
//
// The ring copies the row's compact cells without trailing blanks.
func (s *Session) OnScrollRow(row vt10x.Row) {
	if s.Term.Mode()&vt10x.ModeAltScreen != 0 {
		return
	}
	oldCount := s.Scrollback.Count
	oldSbOff := s.ScrollOffset
	s.Scrollback.PushRow(row)
	s.adjustAfterScrollbackPush(1, oldCount, oldSbOff)
}

// OnScrollbackClear is the vt10x scrollback-erase callback installed in
// Start via WithScrollbackClearCallback.  It fires synchronously inside
// vt10x.Write() when the application requests scrollback erasure: ED 3
// (CSI 3 J, sent by clear(1) via the xterm E3 capability) or RIS (ESC c,
// sent by reset(1)).  the session lock is already held by CaptureAndWrite, same as
// onScrollRow.
func (s *Session) OnScrollbackClear() {
	L().Debug("scrollback clear requested", "pane", s.ID, "dropped", s.Scrollback.Count)
	s.Scrollback.Clear()
	s.ScrollOffset = 0
	s.SelActive = false
}

// CaptureAndWrite writes chunk to vt10x and answers terminal capability
// queries against the terminal state that exists at that byte offset in the
// stream.  Scrollback capture is handled by the onScrollRow callback installed
// at pane creation — vt10x calls it directly when rows scroll off the top, so
// no post-write diffing is required here.
//
// Must be called with the session locked.
func (s *Session) CaptureAndWrite(chunk []byte) {
	if len(chunk) == 0 {
		return
	}

	offset := 0
	for offset < len(chunk) {
		q, ok := nextTerminalQuery(chunk, offset)
		if !ok {
			s.writeTerminalChunk(chunk[offset:])
			return
		}
		if q.start > offset {
			s.writeTerminalChunk(chunk[offset:q.start])
		}
		s.writeTerminalChunk(chunk[q.start:q.end])
		s.replyTerminalQuery(q)
		offset = q.end
	}
}

// writeTerminalChunk feeds chunk into vt10x while preserving
// scrollback, alt-screen hygiene, and kitty-keyboard handling.
func (s *Session) writeTerminalChunk(chunk []byte) {
	altScreen := s.Term.Mode()&vt10x.ModeAltScreen != 0
	if L().Enabled(context.Background(), LevelTrace) {
		cur := s.Term.Cursor()
		L().Log(context.Background(), LevelTrace, "writeTerminalChunk: start", "pane", s.ID, "cursor_y", cur.Y, "cursor_x", cur.X, "alt", altScreen, "chunk_len", len(chunk))
	}

	// Kitty keyboard protocol — the session acts as the "terminal" for applications.
	// Apps negotiate with us using three sequence types:
	//   \x1b[?u        - query current flags  → respond with \x1b[?<flags>u
	//   \x1b[>|=<n>u   - push flags onto stack (> per spec, = legacy)
	//   \x1b[<N>u      - pop N levels (N defaults to 1)
	// All are stripped before vt10x sees them; vt10x interprets the bare 'u'
	// final byte as DECRC (restore cursor), jumping the cursor to 0,0.
	if bytes.ContainsAny(chunk, "u") && bytes.Contains(chunk, []byte("\x1b[")) {
		chunk = s.handleKittyKeyboard(chunk)
	}

	// If this chunk crosses an alt-screen entry point:
	//   1. Save the primary cursor so we can restore it on exit (vt10x shares
	//      a single saved-cursor slot for ESC-7/8 and \x1b[?1049h/l, so the
	//      real primary position is lost once the alt-screen swap happens).
	//   2. Split processing and inject \x1b[2J\x1b[H right after the entry so
	//      the alt-screen starts with a clean slate (it may contain stale
	//      content if a previous TUI program crashed without sending \x1b[?1049l).
	wrote := false
	exitSplitAt := 0 // byte offset into chunk just after the ?1049l/47l sequence, 0 if none
	// Every alt-screen switch starts with "\x1b[?"; most chunks of output
	// hold none, and one search saves six.
	private := bytes.Contains(chunk, []byte("\x1b[?"))
	if !altScreen && private {
		for _, seq := range []string{"\x1b[?1049h", "\x1b[?1047h", "\x1b[?47h"} {
			b := []byte(seq)
			if i := bytes.Index(chunk, b); i >= 0 {
				// Write everything BEFORE the alt-screen entry sequence first,
				// so any cursor-movement sequences in this chunk (e.g. vim's
				// \r\n or \x1b[row;colH) update the vt10x cursor position.
				// Only then read the cursor — otherwise over SSH where many
				// sequences arrive in one chunk, the pre-entry cursor moves
				// are missed and we save y=0 instead of the actual prompt row.
				if i > 0 {
					s.Term.Write(chunk[:i]) //nolint:errcheck
				}
				cur := s.Term.Cursor()
				s.altEntryCursorX, s.altEntryCursorY = cur.X, cur.Y
				L().Log(context.Background(), LevelTrace, "captureAndWrite: alt-screen entry", "pane", s.ID, "seq", seq, "cursor_x", cur.X, "cursor_y", cur.Y)

				end := i + len(b)
				s.Term.Write(chunk[i:end])            //nolint:errcheck
				s.Term.Write([]byte("\x1b[2J\x1b[H")) //nolint:errcheck
				if end < len(chunk) {
					s.Term.Write(chunk[end:]) //nolint:errcheck
				}
				wrote = true
				break
			}
		}
	}
	// If this chunk crosses an alt-screen EXIT point:
	//   1. Inject \x1b[0m right after the exit sequence to prevent TUI background
	//      colour bleeding into the restored primary screen.
	//   2. Inject curRestore BEFORE writing chunk[end:] — over SSH, bash often
	//      sends the shell prompt in the same chunk as \x1b[?1049l.  If curRestore
	//      were injected after chunk[end:], it would rewind the cursor to x=0 on
	//      the prompt line (undoing the natural cursor advance from the prompt text)
	//      causing subsequent input to overwrite PS1.
	if !wrote && altScreen && private {
		for _, seq := range []string{"\x1b[?1049l", "\x1b[?1047l", "\x1b[?47l"} {
			b := []byte(seq)
			if i := bytes.Index(chunk, b); i >= 0 {
				end := i + len(b)
				exitSplitAt = end
				s.Term.Write(chunk[:end])       //nolint:errcheck
				s.Term.Write([]byte("\x1b[0m")) //nolint:errcheck
				// Restore primary cursor here — before any trailing output
				// in this chunk (e.g. prompt text) — so the prompt text
				// advances the cursor naturally to the correct position.
				L().Log(context.Background(), LevelTrace, "captureAndWrite: alt-screen exit", "pane", s.ID, "cursor_x", s.altEntryCursorX, "cursor_y", s.altEntryCursorY)
				curRestore := fmt.Sprintf("\x1b[%d;%dH", s.altEntryCursorY+1, s.altEntryCursorX+1)
				L().Log(context.Background(), LevelTrace, "captureAndWrite: injecting curRestore", "pane", s.ID, "seq", curRestore)
				s.Term.Write([]byte(curRestore)) //nolint:errcheck
				if end < len(chunk) {
					s.Term.Write(chunk[end:]) //nolint:errcheck
				}
				wrote = true
				break
			}
		}
	}
	if !wrote {
		s.Term.Write(chunk) //nolint:errcheck
	}

	if altScreen && s.Term.Mode()&vt10x.ModeAltScreen == 0 {
		// Reset kitty keyboard protocol stack.  TUI apps push onto the stack
		// when entering alt-screen but may not pop if they crash, are killed,
		// or exit abnormally.  Clearing here prevents stale flags from
		// affecting the shell after the alt-screen app is gone.
		s.KittyStack = s.KittyStack[:0]

		const sgrReset = "\x1b[0m"
		if exitSplitAt == 0 {
			s.Term.Write([]byte(sgrReset)) //nolint:errcheck
			L().Log(context.Background(), LevelTrace, "captureAndWrite: alt-screen exit (fallback)", "pane", s.ID, "cursor_x", s.altEntryCursorX, "cursor_y", s.altEntryCursorY)
			curRestore := fmt.Sprintf("\x1b[%d;%dH", s.altEntryCursorY+1, s.altEntryCursorX+1)
			L().Log(context.Background(), LevelTrace, "captureAndWrite: injecting curRestore (fallback)", "pane", s.ID, "seq", curRestore)
			s.Term.Write([]byte(curRestore)) //nolint:errcheck
		}

		// Signal the render loop to do a full Sync() repaint so any residual
		// background colour from the TUI app is cleared from the terminal.
		s.NeedsSync = true
	}

}

type terminalQueryKind int

const (
	terminalQueryDA terminalQueryKind = iota
	terminalQueryDA2
	terminalQueryXTVERSION
	terminalQueryXTGETTCAP
	terminalQueryCPR
	terminalQueryDECRQM
	terminalQueryOSC10
	terminalQueryOSC11
	terminalQueryOSC12
	terminalQueryDA3
	terminalQueryDSR
	terminalQueryDECRQSS
	terminalQuerySize
)

type terminalQuery struct {
	start, end int
	kind       terminalQueryKind
	mode       int
	payload    string
}

func (s *Session) replyTerminalQuery(q terminalQuery) {
	switch q.kind {
	case terminalQuerySize:
		cols, rows := s.Term.Size()
		cw, ch := s.Term.CellPixels()
		switch q.mode {
		case 14:
			s.reply(fmt.Appendf(nil, "\x1b[4;%d;%dt", rows*ch, cols*cw))
		case 16:
			s.reply(fmt.Appendf(nil, "\x1b[6;%d;%dt", ch, cw))
		case 18:
			s.reply(fmt.Appendf(nil, "\x1b[8;%d;%dt", rows, cols))
		}
	case terminalQueryDA3:
		s.reply([]byte("\x1bP!|00000000\x1b\\"))
	case terminalQueryDSR:
		s.reply([]byte("\x1b[0n"))
	case terminalQueryDECRQSS:
		s.reply([]byte(s.Term.StatusString(q.payload)))
	case terminalQueryDA:
		s.reply([]byte("\x1b[?62;1;2;4;6;9;15;22c"))
		L().Log(context.Background(), LevelTrace, "captureAndWrite: DA response", "pane", s.ID)
	case terminalQueryDA2:
		s.reply([]byte("\x1b[>0;279;0c"))
		L().Log(context.Background(), LevelTrace, "captureAndWrite: DA2 response", "pane", s.ID)
	case terminalQueryXTVERSION:
		s.reply([]byte("\x1bP>|VTE(8203)\x1b\\"))
		L().Log(context.Background(), LevelTrace, "captureAndWrite: XTVERSION response", "pane", s.ID)
	case terminalQueryXTGETTCAP:
		flags := 0
		if len(s.KittyStack) > 0 {
			flags = s.KittyStack[len(s.KittyStack)-1]
		}
		for _, hexCap := range strings.Split(q.payload, ";") {
			if resp := xtgettcapResponseKey(hexCap, flags, s.Term.Mode(), s.backspace); resp != "" {
				s.reply([]byte(resp))
			}
		}
		L().Log(context.Background(), LevelTrace, "captureAndWrite: XTGETTCAP response", "pane", s.ID, "payload", q.payload)
	case terminalQueryCPR:
		x, y := s.Term.CursorPosition()
		prefix := ""
		if q.mode != 0 {
			prefix = "?"
		}
		resp := fmt.Sprintf("\x1b[%s%d;%dR", prefix, y+1, x+1)
		s.reply([]byte(resp))
		L().Log(context.Background(), LevelTrace, "captureAndWrite: CPR response", "pane", s.ID, "row", y+1, "col", x+1)
	case terminalQueryDECRQM:
		status := s.Term.QueryPrivateMode(q.mode)
		resp := fmt.Sprintf("\x1b[?%d;%c$y", q.mode, status)
		s.reply([]byte(resp))
		L().Log(context.Background(), LevelTrace, "captureAndWrite: DECRQM response", "pane", s.ID, "mode", q.mode, "status", string(status))
	case terminalQueryOSC10:
		s.replyOSCColorQuery(10, vt10x.DefaultFG, s.ThemeFG)
	case terminalQueryOSC11:
		s.replyOSCColorQuery(11, vt10x.DefaultBG, s.ThemeBG)
	case terminalQueryOSC12:
		s.replyOSCColorQuery(12, vt10x.DefaultCursor, s.ThemeCursor)
	}
}

func (s *Session) replyOSCColorQuery(num int, def vt10x.Color, fallback string) {
	color := s.currentOSCColor(def, fallback)
	if color == "" {
		return
	}
	resp := fmt.Sprintf("\x1b]%d;%s\x1b\\", num, color)
	s.reply([]byte(resp))
	L().Log(context.Background(), LevelTrace, "captureAndWrite: OSC color response", "pane", s.ID, "osc_num", num, "color", color)
}

func (s *Session) currentOSCColor(def vt10x.Color, fallback string) string {
	if c, ok := s.Term.ColorOverride(def); ok {
		return vtColorToXParse(c)
	}
	return fallback
}

func vtColorToXParse(c vt10x.Color) string {
	if c >= vt10x.DefaultFG {
		return ""
	}
	r := byte(c >> 16)
	g := byte(c >> 8)
	b := byte(c)
	return fmt.Sprintf("rgb:%02x%02x/%02x%02x/%02x%02x", r, r, g, g, b, b)
}

func nextTerminalQuery(data []byte, from int) (terminalQuery, bool) {
	for i := from; i+1 < len(data); i++ {
		// Skip to the next ESC that has a byte after it.
		j := bytes.IndexByte(data[i:len(data)-1], 0x1b)
		if j < 0 {
			break
		}
		i += j
		switch data[i+1] {
		case '[':
			if q, ok := parseCSIQuery(data, i); ok {
				return q, true
			}
		case ']':
			end := oscSequenceEnd(data, i)
			if end <= i {
				continue
			}
			if q, ok := parseOSCQuery(data, i, end); ok {
				return q, true
			}
			i = end - 1
		case 'P':
			end := dcsSequenceEnd(data, i)
			if end <= i {
				continue
			}
			if bytes.HasPrefix(data[i:end], []byte("\x1bP+q")) {
				return terminalQuery{
					start:   i,
					end:     end,
					kind:    terminalQueryXTGETTCAP,
					payload: string(data[i+4 : end-2]),
				}, true
			}
			if bytes.HasPrefix(data[i:end], []byte("\x1bP$q")) {
				return terminalQuery{start: i, end: end, kind: terminalQueryDECRQSS, payload: string(data[i+4 : end-2])}, true
			}
			i = end - 1
		case '_', '^', 'X':
			if end := dcsSequenceEnd(data, i); end > i {
				i = end - 1
			}
		}
	}
	return terminalQuery{}, false
}

func parseCSIQuery(data []byte, start int) (terminalQuery, bool) {
	rest := data[start:]
	if len(rest) < 3 {
		return terminalQuery{}, false
	}
	// The byte after "\x1b[" picks the candidates; most sequences, SGR
	// among them, match none.
	switch rest[2] {
	case '1':
		switch {
		case bytes.HasPrefix(rest, []byte("\x1b[14t")):
			return terminalQuery{start: start, end: start + 5, kind: terminalQuerySize, mode: 14}, true
		case bytes.HasPrefix(rest, []byte("\x1b[16t")):
			return terminalQuery{start: start, end: start + 5, kind: terminalQuerySize, mode: 16}, true
		case bytes.HasPrefix(rest, []byte("\x1b[18t")):
			return terminalQuery{start: start, end: start + 5, kind: terminalQuerySize, mode: 18}, true
		}
	case '=':
		switch {
		case bytes.HasPrefix(rest, []byte("\x1b[=c")):
			return terminalQuery{start: start, end: start + 4, kind: terminalQueryDA3}, true
		case bytes.HasPrefix(rest, []byte("\x1b[=0c")):
			return terminalQuery{start: start, end: start + 5, kind: terminalQueryDA3}, true
		}
	case '5':
		if bytes.HasPrefix(rest, []byte("\x1b[5n")) {
			return terminalQuery{start: start, end: start + 4, kind: terminalQueryDSR}, true
		}
	case '>':
		switch {
		case bytes.HasPrefix(rest, []byte("\x1b[>0q")):
			return terminalQuery{start: start, end: start + len("\x1b[>0q"), kind: terminalQueryXTVERSION}, true
		case bytes.HasPrefix(rest, []byte("\x1b[>q")):
			return terminalQuery{start: start, end: start + len("\x1b[>q"), kind: terminalQueryXTVERSION}, true
		case bytes.HasPrefix(rest, []byte("\x1b[>0c")):
			return terminalQuery{start: start, end: start + len("\x1b[>0c"), kind: terminalQueryDA2}, true
		case bytes.HasPrefix(rest, []byte("\x1b[>c")):
			return terminalQuery{start: start, end: start + len("\x1b[>c"), kind: terminalQueryDA2}, true
		}
	case '0':
		if bytes.HasPrefix(rest, []byte("\x1b[0c")) {
			return terminalQuery{start: start, end: start + len("\x1b[0c"), kind: terminalQueryDA}, true
		}
	case 'c':
		if bytes.HasPrefix(rest, []byte("\x1b[c")) {
			return terminalQuery{start: start, end: start + len("\x1b[c"), kind: terminalQueryDA}, true
		}
	case '6':
		if bytes.HasPrefix(rest, []byte("\x1b[6n")) {
			return terminalQuery{start: start, end: start + len("\x1b[6n"), kind: terminalQueryCPR}, true
		}
	case '?':
		switch {
		case bytes.HasPrefix(rest, []byte("\x1b[?6n")):
			return terminalQuery{start: start, end: start + 5, kind: terminalQueryCPR, mode: 1}, true
		case bytes.HasPrefix(rest, []byte("\x1b[?")):
			mode, end, ok := parseDECRQM(data, start)
			if ok {
				return terminalQuery{start: start, end: end, kind: terminalQueryDECRQM, mode: mode}, true
			}
		}
	}
	return terminalQuery{}, false
}

func parseDECRQM(data []byte, start int) (mode, end int, ok bool) {
	i := start + len("\x1b[?")
	j := i
	for j < len(data) && data[j] >= '0' && data[j] <= '9' {
		mode = mode*10 + int(data[j]-'0')
		if mode > 65535 {
			return 0, 0, false
		}
		j++
	}
	if j == i || j+1 >= len(data) || data[j] != '$' || data[j+1] != 'p' {
		return 0, 0, false
	}
	return mode, j + 2, true
}

func parseOSCQuery(data []byte, start, end int) (terminalQuery, bool) {
	bodyEnd := end - 1
	if end-start >= 2 && data[end-1] != 0x07 {
		bodyEnd = end - 2
	}
	switch string(data[start+2 : bodyEnd]) {
	case "10;?":
		return terminalQuery{start: start, end: end, kind: terminalQueryOSC10}, true
	case "11;?":
		return terminalQuery{start: start, end: end, kind: terminalQueryOSC11}, true
	case "12;?":
		return terminalQuery{start: start, end: end, kind: terminalQueryOSC12}, true
	default:
		return terminalQuery{}, false
	}
}

func oscSequenceEnd(data []byte, start int) int {
	for i := start + 2; i < len(data); i++ {
		switch data[i] {
		case 0x07:
			return i + 1
		case 0x1b:
			if i+1 < len(data) && data[i+1] == '\\' {
				return i + 2
			}
		}
	}
	return -1
}

func dcsSequenceEnd(data []byte, start int) int {
	for i := start + 2; i < len(data); i++ {
		if data[i] == 0x1b && i+1 < len(data) && data[i+1] == '\\' {
			return i + 2
		}
	}
	return -1
}

// waitForExit blocks until the shell process exits, then marks the session
// dead and tells the front end.
func (s *Session) waitForExit(h Hooks) {
	s.Cmd.Wait() //nolint:errcheck
	L().Debug("pane process exited", "id", s.ID)
	s.Lock()
	s.Dead = true
	s.Unlock()
	if s.exited != nil {
		close(s.exited)
	}
	if h.Exited != nil {
		h.Exited()
	}
}

// ScrollUp scrolls the view n lines toward the past (increases sbOff).
// Clamped so sbOff never exceeds the number of captured lines.
// p.sb is populated in real time by onScrollRow; no rebuild is needed here.
func (s *Session) ScrollUp(n int) {
	s.Lock()
	defer s.Unlock()
	before := s.ScrollOffset
	s.ScrollOffset += n
	if s.ScrollOffset > s.Scrollback.Count {
		s.ScrollOffset = s.Scrollback.Count
	}
	L().Debug("scrollUp", "pane", s.ID, "from", before, "to", s.ScrollOffset, "max", s.Scrollback.Count)
}

// ScrollDown scrolls the view n lines toward the present (decreases sbOff).
func (s *Session) ScrollDown(n int) {
	s.Lock()
	defer s.Unlock()
	before := s.ScrollOffset
	s.ScrollOffset -= n
	if s.ScrollOffset < 0 {
		s.ScrollOffset = 0
	}
	L().Debug("scrollDown", "pane", s.ID, "from", before, "to", s.ScrollOffset)
}

// ScrollReset snaps back to the live view (sbOff = 0).
func (s *Session) ScrollReset() {
	s.Lock()
	defer s.Unlock()
	if s.ScrollOffset != 0 {
		L().Debug("scrollReset", "pane", s.ID, "was", s.ScrollOffset)
	}
	s.ScrollOffset = 0
}

// InScrollback reports whether the pane is currently showing scrollback.
// Safe to call without the session lock (reads an int, which is atomically readable
// on all Go-supported platforms, but we use a lock for correctness).
func (s *Session) InScrollback() bool {
	s.Lock()
	defer s.Unlock()
	return s.ScrollOffset > 0
}

// IsDead reports whether the shell has exited.
func (s *Session) IsDead() bool {
	s.Lock()
	defer s.Unlock()
	return s.Dead
}

// ResizeAndReflow preserves primary-screen logical lines and lets
// alternate-screen applications redraw at their new dimensions. It reports
// whether stored cells were reflowed to a new width, which invalidates any
// cached rendering. Must be called with the session locked.
func (s *Session) ResizeAndReflow(newCols, newRows int) (reflowed bool) {
	if newCols < 1 || newRows < 1 {
		return false // terminal too small to be usable
	}
	oldCols, oldRows := s.Term.Size()
	if oldCols == newCols && oldRows == newRows {
		return false
	}

	if s.Term.Mode()&vt10x.ModeAltScreen != 0 {
		// Reset cursor attributes before resize so that vt10x clears the
		// newly created cells (in both the alt and saved normal screen)
		// with default colours instead of the TUI app's current style.
		// Without this, exiting the alt-screen app after a resize leaks
		// its background colour into the normal screen's expanded area.
		s.Term.Write([]byte("\x1b[0m")) //nolint:errcheck
		s.Term.Resize(newCols, newRows)
		return false
	}

	if oldCols == newCols {
		s.resizeHeightOnly(newCols, oldRows, newRows)
		return false
	}
	s.reflowCells(newCols, newRows)
	s.SweepImages() // rows beyond the scrollback were dropped
	return true
}

// resizeHeightOnly handles the common case where only the row count changed
// (columns stayed the same).  No text re-wrapping is needed — we just
// redistribute existing rows between the scrollback ring and the live grid.
//
// Must be called with the session locked.
func (s *Session) resizeHeightOnly(cols, oldRows, newRows int) {
	if newRows > oldRows {
		// Terminal grew taller: pull rows from scrollback into the live grid.
		// Capture the current live grid, resize vt10x, then inject the
		// combined rows (pulled scrollback + old live content).
		extra := newRows - oldRows
		pull := extra
		if pull > s.Scrollback.Count {
			pull = s.Scrollback.Count
		}

		// Collect rows to inject: pulled scrollback + existing live rows.
		combined := make([][]vt10x.Glyph, newRows)
		for i := 0; i < pull; i++ {
			combined[i] = s.Scrollback.Get(s.Scrollback.Count - pull + i)
		}
		for r := 0; r < oldRows; r++ {
			combined[pull+r] = CaptureRow(s.Term, r, cols)
		}
		// Blank padding for remaining rows.
		for r := pull + oldRows; r < newRows; r++ {
			combined[r] = make([]vt10x.Glyph, cols)
		}

		// Shrink scrollback by the pulled amount.
		if pull > 0 {
			newSB := Ring{MaxLines: s.ScrollbackLines}
			for i := 0; i < s.Scrollback.Count-pull; i++ {
				newSB.PushRow(s.Scrollback.Row(i))
			}
			s.Scrollback = newSB
		}

		// Move the cursor down by the number of rows pulled from history.
		origCur := s.Term.Cursor()
		combCurRow := pull + origCur.Y

		// Preserve the scroll position.  The newest `pull` scrollback rows
		// moved into the live terminal, so the user's distance from the live
		// view bottom decreases by `pull`.  Clamp to zero (live view) when
		// the user was looking at rows that are now in the live grid.
		if s.ScrollOffset > pull {
			s.ScrollOffset -= pull
		} else {
			s.ScrollOffset = 0
		}

		origCur.Y = max(0, min(combCurRow, newRows-1))
		s.Term.ReplaceScreen(cols, newRows, combined, origCur, s.Term)
	} else {
		// Terminal shrank: push excess live rows into scrollback.
		// First find the actual content extent — trailing blank rows should
		// be discarded rather than causing content to scroll off the top.
		contentRows := findContentRows(s.Term, cols, oldRows)
		excess := contentRows - newRows
		if excess < 0 {
			excess = 0
		}
		oldSbOff := s.ScrollOffset
		for r := 0; r < excess; r++ {
			s.Scrollback.Push(CaptureRow(s.Term, r, cols))
		}
		// Capture the visible rows (starting from where content begins
		// to fit in the new height).
		remaining := make([][]vt10x.Glyph, newRows)
		for r := 0; r < newRows; r++ {
			srcRow := excess + r
			if srcRow < oldRows {
				remaining[r] = CaptureRow(s.Term, srcRow, cols)
			} else {
				remaining[r] = make([]vt10x.Glyph, cols)
			}
		}
		// Move the cursor up by the number of rows archived into history.
		origCur := s.Term.Cursor()
		remCurRow := origCur.Y - excess

		// Preserve the scroll position.  `excess` rows were pushed from the
		// top of the live terminal into scrollback; the user's distance from
		// the live view bottom increases by the same amount.
		if oldSbOff > 0 {
			s.ScrollOffset = min(oldSbOff+excess, s.Scrollback.Count)
		} // else: live view stays live view (sbOff 0 → 0)

		origCur.Y = max(0, min(remCurRow, newRows-1))
		s.Term.ReplaceScreen(cols, newRows, remaining, origCur, s.Term)
	}

	L().Debug("resizeHeightOnly: done", "pane", s.ID,
		"cols", cols, "oldRows", oldRows, "newRows", newRows,
		"sb_count", s.Scrollback.Count)
}

// utf8Boundary returns the largest index i (0 ≤ i ≤ len(b)) such that b[:i]
// contains only complete UTF-8 sequences.  b[i:] is the trailing incomplete
// sequence (if any) that should be carried over to the next read.
//
// Only the last 3 bytes need to be inspected because the longest UTF-8
// sequence is 4 bytes and a complete sequence at the end is always valid.
func utf8Boundary(b []byte) int {
	n := len(b)
	if n == 0 || b[n-1] < 0x80 {
		return n // ends with ASCII — always complete
	}
	// Walk backwards up to 3 bytes looking for the start byte.
	for i := 1; i <= 3 && i <= n; i++ {
		c := b[n-i]
		if c < 0x80 {
			return n // hit an ASCII byte; everything after is invalid but
			// that can't happen in a well-formed stream — treat as complete.
		}
		if utf8.RuneStart(c) {
			// Found start byte.  A start byte at n-i needs seqLen bytes.
			var seqLen int
			switch {
			case c < 0xE0:
				seqLen = 2
			case c < 0xF0:
				seqLen = 3
			default:
				seqLen = 4
			}
			if i < seqLen {
				return n - i // incomplete — split here
			}
			return n // sequence is complete
		}
		// continuation byte (0x80..0xBF) — keep scanning
	}
	return n
}

// scanDECRQM scans data for DECRQM private-mode queries (\x1b[?N$p) and calls
// fn with each parsed mode number. Multiple queries can appear in one chunk.
func scanDECRQM(data []byte, fn func(n int)) {
	const prefix = "\x1b[?"
	i := 0
	for i < len(data) {
		idx := bytes.Index(data[i:], []byte(prefix))
		if idx < 0 {
			break
		}
		i += idx + len(prefix)
		// Parse decimal digits up to the suffix.
		j := i
		for j < len(data) && data[j] >= '0' && data[j] <= '9' {
			j++
		}
		if j > i && j+1 < len(data) && data[j] == '$' && data[j+1] == 'p' {
			n := 0
			for _, b := range data[i:j] {
				n = n*10 + int(b-'0')
			}
			fn(n)
		}
		i = j
	}
}

// findContentRows includes the cursor row and styled blank cells.
func findContentRows(t vt10x.Terminal, cols, totalRows int) int {
	cur := t.Cursor()
	contentRows := cur.Y + 1
	for r := totalRows - 1; r >= contentRows; r-- {
		blank := true
		for c := 0; c < cols; c++ {
			g := t.Cell(c, r)
			if reflowCellHasContent(g) {
				blank = false
				break
			}
		}
		if !blank {
			contentRows = r + 1
			break
		}
	}
	return contentRows
}

// adjustAfterScrollbackPush updates sbOff and selection coordinates after
// `pushed` rows have been added to the scrollback ring.  oldCount and
// oldSbOff are the values before the push.  Must be called with the session locked.
func (s *Session) adjustAfterScrollbackPush(pushed, oldCount, oldSbOff int) {
	if s.ScrollOffset > 0 {
		s.ScrollOffset += pushed
		if s.ScrollOffset > s.Scrollback.Count {
			s.ScrollOffset = s.Scrollback.Count
		}
	}
	if s.SelActive {
		oldVT := oldCount - oldSbOff
		newVT := s.Scrollback.Count - s.ScrollOffset
		if d := newVT - oldVT; d != 0 {
			s.SelAnchor.Row += d
			s.SelCursor.Row += d
		}
	}
}

// HangupGrace is how long Close lets the child handle SIGHUP before it
// kills the child's process group (default of Timing.HangupGrace).
const HangupGrace = time.Second

// Close shuts down the PTY and sends SIGHUP to the shell so it exits cleanly.
// A child that ignores SIGHUP and still runs after Timing.HangupGrace is
// killed with its process group, so a closed session never keeps its
// process, its waitForExit and trackFgProcess goroutines. Safe to call
// multiple times.
func (s *Session) Close() {
	L().Debug("pane close", "pane", s.ID)
	s.closePTX()
	if s.Cmd.Process != nil {
		s.Cmd.Process.Signal(syscall.SIGHUP) //nolint:errcheck
		s.killOnce.Do(func() {
			// The timer holds only what it needs, not the session with its
			// emulator and scrollback.
			id, proc, exited := s.ID, s.Cmd.Process, s.exited
			time.AfterFunc(s.timing.hangupGrace(), func() { killIfRunning(id, proc, exited) })
		})
	}
}

// killIfRunning kills the child proc and its process group unless exited
// (closed by waitForExit) says it has exited. Start makes the child a
// session and process group leader, so the group also holds what it started
// without job control; jobs that an interactive shell put in groups of
// their own (nohup, &) are left alone.
func killIfRunning(id int, proc *os.Process, exited <-chan struct{}) {
	select {
	case <-exited:
		return
	default:
	}
	L().Debug("session: child outlived SIGHUP, killing its process group", "pane", id, "pid", proc.Pid)
	if err := syscall.Kill(-proc.Pid, syscall.SIGKILL); err != nil {
		L().Debug("session: kill process group", "pane", id, "pid", proc.Pid, "err", err)
	}
	if err := proc.Kill(); err != nil {
		L().Debug("session: kill child", "pane", id, "pid", proc.Pid, "err", err)
	}
}

// Cwd returns the working directory of the pane's foreground process (or the
// shell itself).  Returns "" if the information is unavailable or the path
// doesn't exist on the host (e.g. a container-internal path).
func (s *Session) Cwd() string {
	if s.Cmd.Process == nil {
		return ""
	}
	pid := s.Cmd.Process.Pid
	// Try the foreground process first, fall back to the shell itself.
	if pgid := TermFgPGID(pid); pgid > 0 {
		if d, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pgid)); err == nil {
			if info, err := os.Stat(d); err == nil && info.IsDir() {
				return d
			}
		}
	}
	d, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	if err != nil {
		return ""
	}
	if info, err := os.Stat(d); err == nil && info.IsDir() {
		return d
	}
	return ""
}

// closePTX closes the PTY master exactly once.  Closing the master causes the
// kernel to send HUP to the shell's controlling terminal. The writer drops
// its queue first; a write blocked on a child that does not read returns
// once the master is closed.
func (s *Session) closePTX() {
	s.out.close()
	s.ptmxOnce.Do(func() { s.PTY.Close() }) //nolint:errcheck // PTY master close on shutdown
}

// ---------------------------------------------------------------------------
// Text selection helpers (all require the session lock)
// ---------------------------------------------------------------------------

// SelNorm returns the selection endpoints in top-left → bottom-right order.
func (s *Session) SelNorm() (start, end SelPos) {
	a, c := s.SelAnchor, s.SelCursor
	if a.Row < c.Row || (a.Row == c.Row && a.Col <= c.Col) {
		return a, c
	}
	return c, a
}

// SelContains reports whether virtual cell (vRow, col) falls within
// the current selection.  Requires the session lock.
func (s *Session) SelContains(vRow, col int) bool {
	if !s.SelActive {
		return false
	}
	start, end := s.SelNorm()
	if vRow < start.Row || vRow > end.Row {
		return false
	}
	if start.Row == end.Row {
		return col >= start.Col && col <= end.Col
	}
	if vRow == start.Row {
		return col >= start.Col
	}
	if vRow == end.Row {
		return col <= end.Col
	}
	return true
}

// SelText extracts the selected text from the virtual grid (scrollback + live).
// Lines are newline-separated; trailing spaces on each line are trimmed.
// Soft-wrapped rows (attrWrap on the last cell) are joined without a newline
// so that copy-paste preserves the original logical lines.
// Requires the session lock.
func (s *Session) SelText() string {
	if !s.SelActive {
		return ""
	}
	start, end := s.SelNorm()
	if start == end {
		return ""
	}
	cols, rows := s.Term.Size()
	sbCount := s.Scrollback.Count
	var buf strings.Builder
	var prevCells []vt10x.Glyph
	for vRow := start.Row; vRow <= end.Row; vRow++ {
		var cells []vt10x.Glyph
		if vRow < sbCount {
			cells = s.Scrollback.Get(vRow)
		} else if tr := vRow - sbCount; tr >= 0 && tr < rows {
			cells = CaptureRow(s.Term, tr, cols)
		}
		if vRow > start.Row {
			// Only insert \n if the previous row ended with a hard break.
			// Soft-wrapped rows have attrWrap on their last cell.
			softWrap := false
			if len(prevCells) > 0 {
				softWrap = prevCells[len(prevCells)-1].Mode&vt10x.AttrWrap != 0
			}
			if !softWrap {
				buf.WriteByte('\n')
			}
		}
		fromCol, toCol := 0, cols-1
		if vRow == start.Row {
			fromCol = max(start.Col, 0)
		}
		if vRow == end.Row {
			toCol = end.Col
		}
		var line strings.Builder
		for c := fromCol; c <= toCol && c < cols; c++ {
			if cells != nil && c < len(cells) && cells[c].Width < 0 {
				if cells[c].Width == -1 && c == fromCol && c > 0 {
					line.WriteString(cells[c-1].Text())
				}
				continue
			}
			ch := rune(' ')
			if cells != nil && c < len(cells) {
				if g := cells[c].Char; g != 0 {
					ch = g
				}
			}
			line.WriteRune(ch)
			if cells != nil && c < len(cells) {
				line.WriteString(cells[c].Combining)
			}
		}
		// Only trim trailing spaces on hard-break rows; soft-wrapped rows
		// are full-width by definition.
		s := line.String()
		if len(cells) == 0 || cells[len(cells)-1].Mode&vt10x.AttrWrap == 0 {
			s = strings.TrimRight(s, " ")
		}
		buf.WriteString(s)
		prevCells = cells
	}
	return buf.String()
}

// handleKittyKeyboard processes kitty keyboard protocol sequences in the
// byte stream emitted by pane applications, strips them before vt10x sees
// them (to prevent DECRC misinterpretation), and responds to queries.
//
// Three sequence types (all use the 'u' final byte):
//
//	\x1b [ ? u         - query: respond with \x1b[?<flags>u (top of stack or 0)
//	\x1b [ > <n> u     - push: save current flags and activate <n>  (spec)
//	\x1b [ = <n> ; <mode> u - replace/add/remove current flag bits
//	\x1b [ < <N> u     - pop:  pop N stack levels (N omitted → 1)
//
// Must be called with the session locked (CaptureAndWrite contract).
// The query reply is queued (reply), not written under the lock.
func (s *Session) handleKittyKeyboard(chunk []byte) []byte {
	// We walk through chunk looking for ESC [ followed by a kitty intro byte
	// (?, =, <). copied tracks how far we've flushed into out; out is nil
	// until we actually strip something (avoids allocation when nothing matches).
	var out []byte
	copied := 0 // index up to which chunk has been appended to out

	i := 0
	for i < len(chunk) {
		esc := bytes.IndexByte(chunk[i:], 0x1b)
		if esc < 0 {
			break // no more ESC [ — remainder is flushed below
		}
		abs := i + esc // absolute position of ESC in chunk
		if abs+1 >= len(chunk) {
			break
		}
		if chunk[abs+1] != '[' {
			end := abs + 2
			switch chunk[abs+1] {
			case ']':
				end = oscSequenceEnd(chunk, abs)
			case 'P', '_', '^', 'X':
				end = dcsSequenceEnd(chunk, abs)
			}
			if end < 0 {
				break
			}
			i = end
			continue
		}
		j := abs + 2 // first byte after '['
		if j >= len(chunk) {
			break // truncated sequence at end of chunk — pass through
		}

		intro := chunk[j]
		stripped := false
		newI := abs // default: don't skip (fall through to next iteration)

		switch intro {
		case '?':
			// \x1b[?u  (exactly 4 bytes: ESC [ ? u)
			if j+1 < len(chunk) && chunk[j+1] == 'u' {
				flags := 0
				if len(s.KittyStack) > 0 {
					flags = s.KittyStack[len(s.KittyStack)-1]
				}
				s.reply(fmt.Appendf(nil, "\x1b[?%du", flags))
				newI = j + 2
				stripped = true
			}
		case '>', '=':
			// \x1b[><flags>u   — push flags onto the stack (spec).
			// \x1b[=<flags>;<mode>u — set current flags (mode 1=all, 2=set,
			//   3=reset); the trailing ";<mode>" param means the scan must skip
			//   ';' as well as digits, otherwise the sequence is not recognised
			//   and leaks to vt10x, which misreads the final 'u' as DECRC
			//   (restore cursor) and corrupts cursor-relative drawing.
			k := j + 1
			for k < len(chunk) && ((chunk[k] >= '0' && chunk[k] <= '9') || chunk[k] == ';') {
				k++
			}
			if k < len(chunk) && chunk[k] == 'u' {
				flags := 0
				fmt.Sscanf(string(chunk[j+1:k]), "%d", &flags) //nolint:errcheck // best-effort parse of the first param; flags defaults to 0
				mode := 1
				if sep := bytes.IndexByte(chunk[j+1:k], ';'); sep >= 0 {
					fmt.Sscanf(string(chunk[j+2+sep:k]), "%d", &mode) //nolint:errcheck // omitted mode defaults to replacement
				}
				if intro == '=' && len(s.KittyStack) == 0 {
					s.KittyStack = append(s.KittyStack, 0)
				}
				if intro == '=' && len(s.KittyStack) > 0 {
					top := &s.KittyStack[len(s.KittyStack)-1]
					switch mode {
					case 1:
						*top = flags
					case 2:
						*top |= flags
					case 3:
						*top &^= flags
					}
				} else {
					if len(s.KittyStack) >= 32 {
						s.KittyStack = s.KittyStack[1:]
					}
					s.KittyStack = append(s.KittyStack, flags)
				}
				if s.Cmd != nil && s.Cmd.Process != nil {
					s.KittyOwnerPGID = TermFgPGID(s.Cmd.Process.Pid)
				}
				newI = k + 1
				stripped = true
			}
		case '<':
			// \x1b[<N>u  (N is optional digits, default 1)
			k := j + 1
			for k < len(chunk) && chunk[k] >= '0' && chunk[k] <= '9' {
				k++
			}
			if k < len(chunk) && chunk[k] == 'u' {
				count := 1
				fmt.Sscanf(string(chunk[j+1:k]), "%d", &count) //nolint:errcheck // best-effort parse; count defaults to 1
				if count < 1 {
					count = 1
				}
				if count >= len(s.KittyStack) {
					s.KittyStack = s.KittyStack[:0]
				} else {
					s.KittyStack = s.KittyStack[:len(s.KittyStack)-count]
				}
				newI = k + 1
				stripped = true
			}
		}

		if stripped {
			// Flush bytes before this sequence into out.
			if out == nil {
				out = make([]byte, 0, len(chunk))
			}
			out = append(out, chunk[copied:abs]...)
			copied = newI
			i = newI
		} else {
			// Not a kitty sequence; skip past ESC [ and continue.
			i = abs + 2
		}
	}

	if out == nil {
		return chunk // nothing stripped
	}
	// Flush remaining bytes.
	out = append(out, chunk[copied:]...)
	return out
}

// XParseColor converts an RGB hex string "rrggbb" (6 hex digits) to the
// XParseColor format "rgb:<rrrr>/<gggg>/<bbbb>" used in OSC 10/11 responses.
// Each 8-bit component is doubled to 16-bit by repeating the byte.
func XParseColor(rrggbb string) string {
	if len(rrggbb) != 6 {
		return "rgb:0000/0000/0000"
	}
	return fmt.Sprintf("rgb:%s%s/%s%s/%s%s",
		rrggbb[0:2], rrggbb[0:2],
		rrggbb[2:4], rrggbb[2:4],
		rrggbb[4:6], rrggbb[4:6])
}

const TermName = "xterm-256color"

// xtgettcapResponse builds the DCS response for a single XTGETTCAP capability
// query.  hexCap is the hex-encoded capability name as sent by the app.
// The response is "found" (DCS 1+r...) for capabilities we implement and
// "not found" (DCS 0+r...) for everything else.
func xtgettcapResponse(hexCap string, flags int, mode vt10x.ModeFlag) string {
	return xtgettcapResponseKey(hexCap, flags, mode, input.KeyBS)
}

// xtgettcapResponseKey is xtgettcapResponse for a front end whose Backspace
// key sends backspace (input.KeyBS or input.KeyBackspace).
func xtgettcapResponseKey(hexCap string, flags int, mode vt10x.ModeFlag, backspace input.Key) string {
	capBytes, err := hex.DecodeString(hexCap)
	if err != nil || len(capBytes) == 0 {
		return ""
	}
	// Capabilities we actively support — values are standard terminfo strings.
	// Smulx: extended underline style (SGR 4:N)
	// Setulc: underline colour (SGR 58:2::R:G:B)
	// Su: synonym for extended underline support
	caps := map[string]string{
		"Smulx":  "\x1b[4:%p1%dm",
		"Setulc": "\x1b[58:2::%p1%d:%p2%d:%p3%dm",
		"Su":     "\x1b[4:%p1%dm",
		"TN":     TermName,
		"name":   TermName,
		"Co":     "256",
		"colors": "256",
		"RGB":    "8",
	}
	name := string(capBytes)
	val, ok := caps[name]
	if !ok {
		if ev, found := capabilityKey(name, backspace); found {
			val = string(input.EncodeKey(ev, InputModes(flags, mode)))
			ok = val != ""
		}
	}
	if ok {
		hexVal := hex.EncodeToString([]byte(val))
		return "\x1bP1+r" + hexCap + "=" + hexVal + "\x1b\\"
	}
	return "\x1bP0+r" + hexCap + "\x1b\\"
}

// capabilityKey maps terminfo/termcap names to the same events used for input.
// Replies therefore follow the pane's negotiated modes, not the outer terminal.
func capabilityKey(name string, backspace input.Key) (input.Event, bool) {
	if backspace != input.KeyBackspace {
		backspace = input.KeyBS
	}
	for _, entry := range []struct {
		info, cap string
		key       input.Key
	}{
		{"kcuu1", "ku", input.KeyUp}, {"kcud1", "kd", input.KeyDown},
		{"kcub1", "kl", input.KeyLeft}, {"kcuf1", "kr", input.KeyRight},
		{"khome", "kh", input.KeyHome}, {"kend", "@7", input.KeyEnd},
		{"kich1", "kI", input.KeyInsert}, {"kdch1", "kD", input.KeyDelete},
		{"kpp", "kP", input.KeyPgUp}, {"knp", "kN", input.KeyPgDn},
		// kbs reports what the front end sends for Backspace: BS from
		// bunk's tcell, DEL from bunk-gui (Options.Backspace).
		{"kbs", "kb", backspace}, {"kcbt", "kB", input.KeyBacktab},
	} {
		if name == entry.info || name == entry.cap {
			return input.Event{Key: entry.key}, true
		}
	}
	if name == "kent" || name == "@8" {
		return input.Event{Key: input.KeyEnter, Mod: input.ModKeypad}, true
	}
	for n := 1; n <= 24; n++ {
		capName := ""
		switch {
		case n < 10:
			capName = fmt.Sprintf("k%d", n)
		case n == 10:
			capName = "k;"
		case n < 20:
			capName = "F" + string(rune('1'+n-11))
		default:
			capName = "F" + string(rune('A'+n-20))
		}
		if name == fmt.Sprintf("kf%d", n) || name == capName {
			return input.Event{Key: input.KeyF(n)}, true
		}
	}
	for _, entry := range []struct {
		name string
		key  input.Key
	}{
		{"kUP", input.KeyUp}, {"kDN", input.KeyDown},
		{"kLFT", input.KeyLeft}, {"kRIT", input.KeyRight},
		{"kHOM", input.KeyHome}, {"kEND", input.KeyEnd},
		{"kIC", input.KeyInsert}, {"kDC", input.KeyDelete},
		{"kPRV", input.KeyPgUp}, {"kNXT", input.KeyPgDn},
	} {
		modifier := 2
		if name != entry.name {
			if len(name) != len(entry.name)+1 || !strings.HasPrefix(name, entry.name) || name[len(name)-1] < '2' || name[len(name)-1] > '8' {
				continue
			}
			modifier = int(name[len(name)-1] - '0')
		}
		var mod input.Mod
		if (modifier-1)&1 != 0 {
			mod |= input.ModShift
		}
		if (modifier-1)&2 != 0 {
			mod |= input.ModAlt
		}
		if (modifier-1)&4 != 0 {
			mod |= input.ModCtrl
		}
		return input.Event{Key: entry.key, Mod: mod}, true
	}
	return input.Event{}, false
}
