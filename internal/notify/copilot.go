package notify

import (
	"bytes"
	"cmp"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bunk/internal/notify/wire"
)

// Copilot CLI raises a permission request, and runs its permission_prompt
// notification hooks, before it applies what Assisted Permissions or a
// host policy decided: the request is approved or denied a few
// milliseconds later without asking the user, and no hook reports that.
// The hook input does not say whether the user is asked, but the
// session's event log does. Copilot appends each event to
// session-state/<sessionId>/events.jsonl in its home directory: the
// request's permission.requested, then the notification's hook.start,
// then permission.completed once the request is decided (Copilot CLI
// 1.0.107).
//
// The hook's input is not the input the event log shows: the hook gets
// notification_type and hook_event_name, while the log's hook.start
// holds notificationType (Copilot CLI 1.0.107). Both carry the same
// sessionId and timestamp.
const (
	// copilotPrompt is the notification type of a permission prompt.
	copilotPrompt = "permission_prompt"
	// copilotEventLog is the name of a session's event log.
	copilotEventLog = "events.jsonl"
	// copilotScan bounds how much of the end of the event log is read. A
	// request that is still open lies near the end: the agent waits for
	// its decision.
	copilotScan = 1 << 20
	// copilotWait bounds how long a permission prompt waits for Copilot to
	// decide its request. Copilot decides within ~20 ms of the request;
	// the user takes half a second or more to answer.
	copilotWait = 500 * time.Millisecond
	// copilotPoll is how often the event log is read while waiting.
	copilotPoll = 25 * time.Millisecond
)

// The event types of a Copilot CLI event log that copilotPending counts,
// as they appear quoted in a line.
var (
	copilotRequested = []byte(`"permission.requested"`)
	copilotCompleted = []byte(`"permission.completed"`)
	copilotHookStart = []byte(`"hook.start"`)
)

// copilotDecided reports whether text is a Copilot CLI permission_prompt
// notification whose request Copilot decided without asking the user:
// within wait, the session's event log under d shows the notification
// and no request still open. An input without a readable event log, or
// with a request still open after wait, counts as asking the user.
func copilotDecided(text string, d Dirs, wait time.Duration) bool {
	var in struct {
		Type      string `json:"notification_type"`
		TypeC     string `json:"notificationType"` // as the event log shows the input
		Session   string `json:"sessionId"`
		Timestamp int64  `json:"timestamp"`
	}
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") || json.Unmarshal([]byte(trimmed), &in) != nil ||
		cmp.Or(in.Type, in.TypeC) != copilotPrompt || in.Timestamp == 0 || !wire.ValidAgentSession(in.Session) {
		return false
	}
	r, _ := recipe("copilot")
	path := filepath.Join(d.dir(r), "session-state", in.Session, copilotEventLog)
	deadline := time.Now().Add(wait)
	for {
		seen, open, err := copilotPending(path, in.Timestamp)
		switch {
		case err != nil:
			slog.Debug("notify: copilot event log", "err", err)
			return false
		case seen && open == 0:
			return true
		case time.Now().After(deadline):
			return false
		}
		time.Sleep(copilotPoll)
	}
}

// copilotPending reads the last copilotScan bytes of the Copilot CLI event
// log at path. seen reports whether they hold the hook.start of the
// permission_prompt notification sent at ts (milliseconds since the
// epoch); open counts their permission requests that are not completed.
func copilotPending(path string, ts int64) (seen bool, open int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, 0, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return false, 0, err
	}
	start := max(0, info.Size()-copilotScan)
	b, err := io.ReadAll(io.NewSectionReader(f, start, info.Size()-start))
	if err != nil {
		return false, 0, err
	}
	if start > 0 {
		// The first line is cut off.
		_, b, _ = bytes.Cut(b, []byte("\n"))
	}
	requests := map[string]bool{} // request ID: still open
	for line := range bytes.Lines(b) {
		if !bytes.Contains(line, copilotRequested) && !bytes.Contains(line, copilotCompleted) && !bytes.Contains(line, copilotHookStart) {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			Data struct {
				RequestID string `json:"requestId"`
				HookType  string `json:"hookType"`
				Input     struct {
					Type      string `json:"notificationType"`
					Timestamp int64  `json:"timestamp"`
				} `json:"input"`
			} `json:"data"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue // the line Copilot is writing
		}
		switch ev.Type {
		case "permission.requested":
			requests[ev.Data.RequestID] = true
		case "permission.completed":
			if requests[ev.Data.RequestID] {
				requests[ev.Data.RequestID] = false
			}
		case "hook.start":
			if ev.Data.HookType == "notification" && ev.Data.Input.Type == copilotPrompt && ev.Data.Input.Timestamp == ts {
				seen = true
			}
		}
	}
	for _, o := range requests {
		if o {
			open++
		}
	}
	return seen, open, nil
}
