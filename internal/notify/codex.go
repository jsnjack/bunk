package notify

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
)

// approvalRequested starts the notice of a Codex approval request, as
// Codex's own notification words it.
const approvalRequested = "Approval requested"

// Codex runs its PermissionRequest hooks before it routes the request to
// a reviewer: the user, or with approvals_reviewer = "auto_review" a
// model that approves or denies without asking. The hook input does not
// say which, but the rollout transcript it names does: each turn starts
// with a turn_context line that holds the turn's approvals_reviewer.
const (
	// turnContextMarker starts the payload of a turn_context line. Codex
	// writes each line as compact JSON with "timestamp" (and "ordinal")
	// before "type" and "payload".
	turnContextMarker = `"type":"turn_context","payload":`
	// transcriptChunk is how much of the transcript one read covers.
	transcriptChunk = 256 << 10
	// transcriptScan bounds how far back from the end the turn_context is
	// looked for; a turn can write megabytes of tool output after it.
	transcriptScan = 64 << 20
	// reviewerUser is the approvals_reviewer that shows the request to
	// the user.
	reviewerUser = "user"
)

// codexAutoReview reports whether text is a Codex PermissionRequest input
// that Codex's automatic reviewer decides, so the user is not asked. An
// input without a readable transcript, or a transcript without the
// reviewer, counts as asking the user.
func codexAutoReview(text string) bool {
	var in struct {
		Event      string  `json:"hook_event_name"`
		TurnID     *string `json:"turn_id"` // a Codex extension
		Transcript string  `json:"transcript_path"`
	}
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") || json.Unmarshal([]byte(trimmed), &in) != nil ||
		in.Event != "PermissionRequest" || in.TurnID == nil || in.Transcript == "" {
		return false
	}
	reviewer := codexReviewer(in.Transcript)
	return reviewer != "" && reviewer != reviewerUser
}

// codexReviewer returns the approvals_reviewer of the last turn_context in
// the Codex transcript at path, or "" when there is none within
// transcriptScan of its end.
func codexReviewer(path string) string {
	f, err := os.Open(path)
	if err != nil {
		slog.Debug("notify: codex transcript", "err", err)
		return ""
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	at := lastIndex(f, info.Size(), transcriptScan, []byte(turnContextMarker))
	if at < 0 {
		return ""
	}
	start := at + int64(len(turnContextMarker))
	var payload struct {
		Reviewer string `json:"approvals_reviewer"`
	}
	if err := json.NewDecoder(io.NewSectionReader(f, start, info.Size()-start)).Decode(&payload); err != nil {
		slog.Debug("notify: codex turn context", "err", err)
		return ""
	}
	return payload.Reviewer
}

// lastIndex returns the offset of the last sep that starts in the final
// limit bytes of r, which holds size bytes, or -1. It reads backwards a
// chunk at a time; consecutive chunks overlap by len(sep)-1 bytes, so a
// sep across their boundary is found.
func lastIndex(r io.ReaderAt, size, limit int64, sep []byte) int64 {
	floor := max(0, size-limit)
	buf := make([]byte, transcriptChunk+len(sep)-1)
	for end := size; end > floor; {
		start := max(floor, end-transcriptChunk)
		b := buf[:min(size, end+int64(len(sep))-1)-start]
		if _, err := r.ReadAt(b, start); err != nil {
			return -1
		}
		if i := bytes.LastIndex(b, sep); i >= 0 {
			return start + int64(i)
		}
		end = start
	}
	return -1
}

// approvalNotice returns the notice of a PermissionRequest input:
// approvalRequested, with the first line of the command a shell tool asks
// to run. Other inputs give "".
func approvalNotice(obj map[string]json.RawMessage) string {
	var event, tool string
	if json.Unmarshal(obj["hook_event_name"], &event) != nil || event != "PermissionRequest" {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(obj["tool_name"], &tool) == nil && tool == "Bash" &&
		json.Unmarshal(obj["tool_input"], &in) == nil {
		if line, _, _ := strings.Cut(strings.TrimSpace(in.Command), "\n"); line != "" {
			return approvalRequested + ": " + line
		}
	}
	return approvalRequested
}
